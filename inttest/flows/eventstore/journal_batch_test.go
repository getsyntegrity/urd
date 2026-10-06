//go:build journalexp

package eventstore_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/persistence/conformance"
	"github.com/getsyntegrity/urd/persistence/postgres"
)

// The batch-publication prototype (persistence/postgres, tag journalexp) on the real adapter: late events, per-entity
// order under concurrency, restart/resume, and the real conformance suite. Small on purpose.

var batchKind = expKind{name: postgres.KindBatch, serializes: false}

func batchOf(s postgres.ExperimentalStore) *postgres.ExperimentalBatchStore {
	return s.(*postgres.ExperimentalBatchStore)
}

func publishAll(t *testing.T, s postgres.ExperimentalStore) {
	t.Helper()
	if _, err := batchOf(s).PublishAll(context.Background()); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func pending(t *testing.T, s postgres.ExperimentalStore) int64 {
	t.Helper()
	n, err := batchOf(s).Pending(context.Background())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	return n
}

// TestBatch_LateEventsAreDelivered: the #332 regression on the real adapter, in both shapes. First the sequence
// as it was reported (a@100 and b@200 consumed, then late@200 and late@150 become visible); then the interleaving
// that causes it (a writer in flight while another commits and the reader advances).
func TestBatch_LateEventsAreDelivered(t *testing.T) {
	scope := tenantScope(t, "tenant-a")
	rig := newExpRig(t, batchKind)

	rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("a", 1, 1, 100))
	rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("b", 1, 1, 200))
	publishAll(t, rig.main)
	first, cursor := rig.drainFrom(scope, 1, 0, 10)
	eq(t, []string{"a/1", "b/1"}, first, "the consumer reads what is published")

	rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("late-200", 1, 1, 200))
	rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("late-150", 1, 1, 150))
	got, _ := rig.drainFrom(scope, 1, cursor, 10)
	eq(t, nil, got, "persisted but not published: not available, and not behind anything")
	publishAll(t, rig.main)
	got, cursor = rig.drainFrom(scope, 1, cursor, 10)
	eq(t, []string{"late-200/1", "late-150/1"}, got, "both late events are delivered after the committed cursor, once")

	// the interleaving: a writer is in flight (its timestamp is the oldest) while another commits and is consumed
	held := rig.park(scope, persistence.Unconditional(), expEvent("held", 1, 1, 100))
	rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("quick", 1, 1, 400))
	publishAll(t, rig.main)
	got, cursor = rig.drainFrom(scope, 1, cursor, 10)
	eq(t, []string{"quick/1"}, got, "the reader advances past the in-flight writer")
	if err := rig.finish(held, true); err != nil {
		t.Fatalf("held writer: %v", err)
	}
	publishAll(t, rig.main)
	got, _ = rig.drainFrom(scope, 1, cursor, 10)
	eq(t, []string{"held/1"}, got, "the writer that committed after the reader advanced is delivered, not skipped")
}

// TestBatch_ConcurrentWritersAndPublishers: writers on owned entities (conditional, multi-event), several writers
// on one contended entity (conditional with retry), multi-shard unconditional batches, every 7th write of one
// process rolled back, TWO background publishers on the same streams, readers polling. Afterwards every committed
// event is delivered exactly once, each entity in sequence order, nothing is left pending, and positions are unique.
func TestBatch_ConcurrentWritersAndPublishers(t *testing.T) {
	scope := tenantScope(t, "tenant-a")
	rig := newExpRig(t, batchKind)
	procA, procB, procC := rig.main, rig.process(), rig.process()
	var calls atomic.Int64
	procB.SetBeforeCommit(func() bool { return calls.Add(1)%7 != 0 })
	stores := []postgres.ExperimentalStore{procA, procB, procC}
	for _, s := range []postgres.ExperimentalStore{procA, procB, procC} {
		batchOf(s).BatchSize = 7 // small batches: many publisher transactions
	}
	stopA := batchOf(procA).StartPublisher(context.Background(), 2*time.Millisecond)
	stopC := batchOf(procC).StartPublisher(context.Background(), 2*time.Millisecond)

	const (
		shards    = 4
		txPerKind = 50
	)
	var (
		mu        sync.Mutex
		committed = map[string]bool{}
		failures  []error
		wg        sync.WaitGroup
	)
	keyOf := func(e *egopb.Event) string { return fmt.Sprintf("%s/%d", e.GetPersistenceId(), e.GetSequenceNumber()) }
	record := func(events []*egopb.Event) {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range events {
			committed[keyOf(e)] = true
		}
	}
	fail := func(err error) { mu.Lock(); failures = append(failures, err); mu.Unlock() }
	rolledBack := func(err error) bool { return errors.Is(err, postgres.ErrExperimentalRollback) }

	var (
		readersDone atomic.Bool
		readerWG    sync.WaitGroup
		delivered   [shards][]string
		readerErr   atomic.Pointer[error]
	)
	for shard := range uint64(shards) {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			var after int64
			for {
				finished := readersDone.Load()
				events, next, err := procA.GetShardEvents(context.Background(), scope, shard, after, 5)
				if err != nil {
					readerErr.CompareAndSwap(nil, &err)
					return
				}
				for _, e := range events {
					delivered[shard] = append(delivered[shard], keyOf(e))
				}
				if len(events) > 0 {
					after = next
				} else if finished {
					return
				}
			}
		}()
	}

	for w := range 4 { // owned entities, conditional, two events per transaction, shard 0
		store := stores[w%3]
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, rev := fmt.Sprintf("own-%d", w), uint64(0)
			for done := 0; done < txPerKind; {
				events := []*egopb.Event{expEvent(id, rev+1, 0, 1), expEvent(id, rev+2, 0, 1)}
				pre := persistence.ExpectRevision(rev)
				if rev == 0 {
					pre = persistence.ExpectGenesis()
				}
				switch err := store.WriteEvents(context.Background(), scope, events, pre); {
				case err == nil:
					record(events)
					rev += 2
					done++
				case rolledBack(err):
				default:
					fail(err)
					return
				}
			}
		}()
	}
	for w := range 4 { // one contended entity: the revision lock orders its writers, conflicts are retried
		store := stores[w%3]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for done := 0; done < txPerKind/2; {
				latest, err := store.GetLatestEvent(context.Background(), scope, "contended")
				if err != nil {
					fail(err)
					return
				}
				rev := latest.GetSequenceNumber()
				events := []*egopb.Event{expEvent("contended", rev+1, 0, 1)}
				pre := persistence.ExpectRevision(rev)
				if latest == nil {
					pre = persistence.ExpectGenesis()
				}
				switch err := store.WriteEvents(context.Background(), scope, events, pre); {
				case err == nil:
					record(events)
					done++
				case rolledBack(err), errors.Is(err, persistence.ErrConcurrencyConflict):
				default:
					fail(err)
					return
				}
			}
		}()
	}
	for w := range 4 { // multi-shard unconditional batches over entities the writer owns
		store := stores[w%3]
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, b := uint64(1+w%2), uint64(2+w%2)
			x, y := fmt.Sprintf("multi-%d-x", w), fmt.Sprintf("multi-%d-y", w)
			for i := uint64(1); i <= txPerKind; {
				events := []*egopb.Event{expEvent(x, i, a, 1), expEvent(y, i, b, 1)}
				switch err := store.WriteEvents(context.Background(), scope, events, persistence.Unconditional()); {
				case err == nil:
					record(events)
					i++
				case rolledBack(err):
				default:
					fail(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	stopA()
	stopC()
	publishAll(t, procA)
	readersDone.Store(true)
	readerWG.Wait()

	if len(failures) > 0 {
		t.Fatalf("a write failed: %v", errors.Join(failures...))
	}
	if p := readerErr.Load(); p != nil {
		t.Fatalf("a reader failed: %v", *p)
	}
	if n := pending(t, procA); n != 0 {
		t.Fatalf("%d rows are still pending after the drain", n)
	}

	seen := map[string]bool{}
	lastSeq := map[string]uint64{}
	for shard := range shards {
		for _, key := range delivered[shard] {
			if seen[key] {
				t.Fatalf("%s delivered twice", key)
			}
			seen[key] = true
			cut := lastSlash(key)
			id := key[:cut]
			var seq uint64
			if n, _ := fmt.Sscanf(key[cut+1:], "%d", &seq); n != 1 {
				t.Fatalf("bad key %q", key)
			}
			if seq <= lastSeq[id] {
				t.Fatalf("entity %s delivered seq %d after %d: per-entity order broken", id, seq, lastSeq[id])
			}
			lastSeq[id] = seq
		}
	}
	for key := range committed {
		if !seen[key] {
			t.Fatalf("OMISSION: %s was committed and never delivered", key)
		}
	}
	if len(seen) != len(committed) {
		t.Fatalf("delivered %d events but %d were committed", len(seen), len(committed))
	}
	if calls.Load()/7 == 0 {
		t.Fatalf("the run exercised no rollback")
	}

	// the monotonicity the design relies on (decisions O9): within an entity, pub_seq grows with the sequence number
	rows, err := rig.obs.Query(context.Background(), `SELECT persistence_id, sequence_number, pub_seq FROM events_store ORDER BY persistence_id, sequence_number`)
	if err != nil {
		t.Fatalf("pub_seq: %v", err)
	}
	defer rows.Close()
	var (
		lastID  string
		lastPub int64
	)
	for rows.Next() {
		var (
			id       string
			seq, pub int64
		)
		if err := rows.Scan(&id, &seq, &pub); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if id == lastID && pub <= lastPub {
			t.Fatalf("entity %s: pub_seq %d is not above %d at sequence %d", id, pub, lastPub, seq)
		}
		lastID, lastPub = id, pub
	}
	t.Logf("committed events: %d, rollbacks injected: %d, two background publishers, no omission, no duplicate, per-entity order kept, pub_seq monotone per entity", len(committed), calls.Load()/7)
}

// TestBatch_RestartResume: a crash after persisting and before publishing, a crash during a publisher
// transaction, and a consumer that resumes from its committed cursor after a restart.
func TestBatch_RestartResume(t *testing.T) {
	scope := tenantScope(t, "tenant-a")
	rig := newExpRig(t, batchKind)
	first := batchOf(rig.main)
	first.BatchSize = 2

	for _, w := range []struct {
		id  string
		seq uint64
	}{{"e", 1}, {"f", 1}, {"e", 2}, {"g", 1}, {"f", 2}, {"e", 3}} {
		rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent(w.id, w.seq, 1, 1))
	}
	if n, err := first.PublishRound(context.Background()); err != nil || n != 2 {
		t.Fatalf("one round must publish one batch of 2: n=%d err=%v", n, err)
	}
	page, cursor := rig.page(scope, 1, 0, 1)
	eq(t, []string{"e/1"}, page, "the consumer read one event and committed its cursor")

	// "crash": everything in memory of the first process is gone; the other rows are pending in the database
	second := rig.process()
	if n := pending(t, second); n != 4 {
		t.Fatalf("a crash before publishing must leave the persisted rows pending, got %d", n)
	}

	// a crash DURING a publisher transaction: nothing of it may become visible
	batchOf(second).BatchSize = 2
	batchOf(second).PublishFailpoint = func() error { return errors.New("injected crash before the publisher commit") }
	if _, err := batchOf(second).PublishRound(context.Background()); err == nil {
		t.Fatalf("the failpoint must fail the publisher transaction")
	}
	if n := pending(t, second); n != 4 {
		t.Fatalf("a failed publisher transaction must leave the rows pending, got %d", n)
	}
	got, _ := rig.drainFrom(scope, 1, cursor, 10)
	eq(t, []string{"f/1"}, got, "only what was published before the crash is available after the cursor")

	// recovery, then the consumer resumes from the cursor it committed before the restart
	batchOf(second).PublishFailpoint = nil
	publishAll(t, second)
	resumed, end := rig.drainFrom(scope, 1, cursor, 1)
	eq(t, []string{"f/1", "e/2", "g/1", "f/2", "e/3"}, resumed, "resuming from the committed cursor recovers the rest, once, in publication order")
	all, _ := rig.drainFrom(scope, 1, 0, 100)
	if len(all) != 6 {
		t.Fatalf("a read from zero must return all six events once: %v", all)
	}
	last := map[string]int{}
	for _, key := range all {
		var id string
		var seq int
		fmt.Sscanf(key[lastSlash(key)+1:], "%d", &seq)
		id = key[:lastSlash(key)]
		if seq <= last[id] {
			t.Fatalf("entity %s out of order in %v", id, all)
		}
		last[id] = seq
	}
	again, _ := rig.drainFrom(scope, 1, end, 10)
	eq(t, nil, again, "nothing is delivered after the head")
}

// autoPublish publishes synchronously after every write, so the unmodified conformance suite (which reads right after
// writing) sees what it expects.
type autoPublish struct {
	*postgres.ExperimentalBatchStore
}

func (a autoPublish) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, pre persistence.WritePrecondition) error {
	if err := a.ExperimentalBatchStore.WriteEvents(ctx, scope, events, pre); err != nil {
		return err
	}
	_, err := a.PublishAll(ctx)
	return err
}

// TestBatch_Conformance runs the real EventsStore conformance suite against the batch variant (publishing after each
// write) and pins the outcome: the #332 regression passes, the three checks that assert a timestamp offset fail, and
// every other check passes.
func TestBatch_Conformance(t *testing.T) {
	results := conformance.CaptureEventsStoreChecks(func() persistence.EventsStore {
		s, err := provisionExperimentalKind(postgres.KindBatch, shared.NewDatabase(t))
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		wrapped := autoPublish{batchOf(s)}
		if err := wrapped.Connect(context.Background()); err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { _ = wrapped.Disconnect(context.Background()) })
		return wrapped
	})
	if len(results) == 0 {
		t.Fatalf("the conformance suite returned no result")
	}
	for _, r := range results {
		switch {
		case r.Name == "ShardReads/LateVisibleEventsBehindACommittedOffsetAreDelivered" && r.Failed:
			t.Errorf("the #332 regression must pass on the batch variant: %v", r.Errors)
		case slices.Contains(timestampOffsetChecks, r.Name) && !r.Failed:
			t.Errorf("%s was expected to fail (it asserts a timestamp offset) and passed", r.Name)
		case !slices.Contains(timestampOffsetChecks, r.Name) && r.Failed:
			t.Errorf("unexpected failure of %s: %v", r.Name, r.Errors)
		}
	}
}
