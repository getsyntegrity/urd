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

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/persistence/postgres"
	"github.com/getsyntegrity/urd/tenancy"
)

// Correctness of the #332 experiments on the REAL adapter, through the real WriteEvents/DeleteEvents/
// GetShardEvents, for each variant: serialized (counter rows before the inserts), serialized-late (counter rows
// after the inserts) and horizon (xid8 position, xmin horizon). The same tests run on all three; where the
// schemes differ on purpose (does a later writer wait? does a transaction elsewhere delay a reader?) the
// expectation is stated per variant. Transactions are coordinated explicitly: BeforeCommit parks a writer inside
// its transaction and the test learns that another writer is blocked from pg_locks. None is parallel (they park
// transactions, and the horizon is cluster-wide).

type expKind struct {
	name string
	// serializes: a later writer of the same (scope, shard) waits for an in-flight one.
	serializes bool
}

var expKinds = []expKind{
	{postgres.KindSerialized, true},
	{postgres.KindSerializedLate, true},
	{postgres.KindHorizon, false},
}

func expEvent(id string, seq uint64, shard uint64, ts int64) *egopb.Event {
	payload, err := anypb.New(&testpb.AccountCreated{AccountId: id, AccountBalance: float64(seq)})
	if err != nil {
		panic(err)
	}
	return &egopb.Event{PersistenceId: id, SequenceNumber: seq, Event: payload, Timestamp: ts, Shard: shard}
}

func tenantScope(t testing.TB, id string) persistence.Scope {
	t.Helper()
	scope, err := persistence.NewTenantScope(tenancy.TenantID(id))
	if err != nil {
		t.Fatalf("tenant scope: %v", err)
	}
	return scope
}

// expRig is one database with its own connected stores (each a "process") and an observer pool.
type expRig struct {
	t    *testing.T
	kind expKind
	dsn  string
	obs  *pgxpool.Pool
	main postgres.ExperimentalStore
}

func newExpRig(t *testing.T, kind expKind) *expRig {
	t.Helper()
	dsn := shared.NewDatabase(t)
	if _, err := provisionExperimentalKind(kind.name, dsn); err != nil {
		t.Fatalf("provision: %v", err)
	}
	rig := &expRig{t: t, kind: kind, dsn: dsn, obs: openJournalPool(t, dsn)}
	rig.main = rig.process()
	return rig
}

// process opens another connected store of the same variant over the same database: a restarted or concurrent
// process. Horizon stores assert, on every write, that no transaction id exists before the entity locks.
func (r *expRig) process() postgres.ExperimentalStore {
	r.t.Helper()
	s, err := postgres.NewExperimentalStore(r.kind.name, r.dsn)
	if err != nil {
		r.t.Fatalf("store: %v", err)
	}
	if h, ok := s.(*postgres.ExperimentalHorizonStore); ok {
		h.AssertNoXidBeforeLocks = true
	}
	if err := s.(interface{ Connect(context.Context) error }).Connect(context.Background()); err != nil {
		r.t.Fatalf("connect: %v", err)
	}
	r.t.Cleanup(func() { _ = s.(interface{ Disconnect(context.Context) error }).Disconnect(context.Background()) })
	return s
}

func (r *expRig) mustWrite(s persistence.EventsStore, scope persistence.Scope, pre persistence.WritePrecondition, events ...*egopb.Event) {
	r.t.Helper()
	if err := s.WriteEvents(context.Background(), scope, events, pre); err != nil {
		r.t.Fatalf("write: %v", err)
	}
}

func (r *expRig) page(scope persistence.Scope, shard uint64, after int64, limit uint64) ([]string, int64) {
	r.t.Helper()
	events, next, err := r.main.GetShardEvents(context.Background(), scope, shard, after, limit)
	if err != nil {
		r.t.Fatalf("read: %v", err)
	}
	if len(events) == 0 {
		return nil, after
	}
	keys := make([]string, 0, len(events))
	for _, e := range events {
		keys = append(keys, fmt.Sprintf("%s/%d", e.GetPersistenceId(), e.GetSequenceNumber()))
	}
	return keys, next
}

func (r *expRig) drainFrom(scope persistence.Scope, shard uint64, after int64, limit uint64) ([]string, int64) {
	r.t.Helper()
	var all []string
	for page := 0; page < 10000; page++ {
		keys, next := r.page(scope, shard, after, limit)
		if len(keys) == 0 {
			return all, after
		}
		all = append(all, keys...)
		after = next
	}
	r.t.Fatalf("paging did not terminate")
	return nil, 0
}

func (r *expRig) drain(scope persistence.Scope, shard uint64, after int64, limit uint64) []string {
	r.t.Helper()
	keys, _ := r.drainFrom(scope, shard, after, limit)
	return keys
}

// parked is a write parked right before its commit.
type parked struct {
	release chan bool
	done    chan error
}

func (r *expRig) park(scope persistence.Scope, pre persistence.WritePrecondition, events ...*egopb.Event) *parked {
	r.t.Helper()
	p := &parked{release: make(chan bool, 1), done: make(chan error, 1)}
	entered := make(chan struct{})
	store := r.process()
	store.SetBeforeCommit(func() bool { close(entered); return <-p.release })
	go func() { p.done <- store.WriteEvents(context.Background(), scope, events, pre) }()
	select {
	case <-entered:
	case err := <-p.done:
		r.t.Fatalf("the parked write ended before it was parked: %v", err)
	case <-time.After(stepTimeout):
		r.t.Fatalf("the write was never parked")
	}
	return p
}

func (r *expRig) finish(p *parked, commit bool) error {
	r.t.Helper()
	p.release <- commit
	select {
	case err := <-p.done:
		return err
	case <-time.After(stepTimeout):
		r.t.Fatalf("the parked write did not finish")
		return nil
	}
}

// blocked waits until some session waits on a lock, an observable fact.
func (r *expRig) blocked() {
	r.t.Helper()
	deadline := time.Now().Add(stepTimeout)
	for time.Now().Before(deadline) {
		var n int
		if err := r.obs.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE NOT granted`).Scan(&n); err != nil {
			r.t.Fatalf("pg_locks: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	r.t.Fatalf("no session ever blocked on a lock")
}

type bgWrite struct{ done chan error }

func (r *expRig) startWrite(ctx context.Context, s persistence.EventsStore, scope persistence.Scope, pre persistence.WritePrecondition, events ...*egopb.Event) *bgWrite {
	w := &bgWrite{done: make(chan error, 1)}
	go func() { w.done <- s.WriteEvents(ctx, scope, events, pre) }()
	return w
}

func (r *expRig) wait(w *bgWrite) error {
	r.t.Helper()
	select {
	case err := <-w.done:
		return err
	case <-time.After(stepTimeout):
		r.t.Fatalf("the write did not finish")
		return nil
	}
}

func (r *expRig) counter(tenant string, shard int) int64 {
	r.t.Helper()
	var last int64
	if err := r.obs.QueryRow(context.Background(), `SELECT last FROM journal_shard_positions WHERE tenant_id=$1 AND shard_number=$2`, tenant, shard).Scan(&last); err != nil {
		r.t.Fatalf("counter: %v", err)
	}
	return last
}

func (r *expRig) noNullPositions() {
	r.t.Helper()
	var n int
	if err := r.obs.QueryRow(context.Background(), `SELECT count(*) FROM events_store WHERE journal_pos IS NULL`).Scan(&n); err != nil || n != 0 {
		r.t.Fatalf("every committed row must carry a position: %d rows without one (err=%v)", n, err)
	}
}

func eq(t *testing.T, want, got []string, what string) {
	t.Helper()
	if !slices.Equal(want, got) {
		t.Fatalf("%s:\n  want %v\n  got  %v", what, want, got)
	}
}

// TestExperimentalAdapter_InFlightWriterAndReader is the #332 sequence on the real adapter, for each write path
// of the holder (conditional and unconditional) and each outcome (commit, rollback): the reader is never given
// anything above an in-flight write. A later writer either waits for the shard (serialization) or commits and
// waits to be eligible (horizon: an inverted commit). A rollback leaves no hole.
func TestExperimentalAdapter_InFlightWriterAndReader(t *testing.T) {
	scope := tenantScope(t, "tenant-a")
	for _, kind := range expKinds {
		for _, path := range []string{"conditional", "unconditional"} {
			for _, commit := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/commit=%v", kind.name, path, commit), func(t *testing.T) {
					rig := newExpRig(t, kind)
					rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("seed", 1, 1, 100))
					_, cursor := rig.page(scope, 1, 0, 10)

					pre := persistence.Unconditional()
					if path == "conditional" {
						pre = persistence.ExpectGenesis()
					}
					h := rig.park(scope, pre, expEvent("holder", 1, 1, 200))
					late := rig.startWrite(context.Background(), rig.process(), scope, persistence.Unconditional(), expEvent("late", 1, 1, 150))
					if kind.serializes {
						rig.blocked() // the later writer waits for the shard counter
					} else if err := rig.wait(late); err != nil {
						t.Fatalf("late write: %v", err) // an inverted commit: late commits while holder is in flight
					}

					got, _ := rig.page(scope, 1, cursor, 10)
					eq(t, nil, got, "nothing above an in-flight write may be delivered")

					err := rig.finish(h, commit)
					if commit && err != nil || !commit && !errors.Is(err, postgres.ErrExperimentalRollback) {
						t.Fatalf("holder outcome: %v", err)
					}
					if kind.serializes {
						if err := rig.wait(late); err != nil {
							t.Fatalf("late write: %v", err)
						}
					}
					if commit {
						eq(t, []string{"holder/1", "late/1"}, rig.drain(scope, 1, cursor, 10), "the holder's lower position first, nothing skipped")
					} else {
						eq(t, []string{"late/1"}, rig.drain(scope, 1, cursor, 10), "a rollback leaves no hole to wait for")
						if kind.serializes {
							if last := rig.counter("tenant-a", 1); last != 2 {
								t.Fatalf("the rolled-back position must be handed back: last=%d (want 2: seed + late)", last)
							}
						}
					}
					rig.noNullPositions()
				})
			}
		}
	}
}

// TestExperimentalAdapter_CancellationLeaksNothing: a writer cancelled while it waits (for the shard counter,
// or for the entity lock under the horizon) returns, holds no lock afterwards, and commits nothing.
func TestExperimentalAdapter_CancellationLeaksNothing(t *testing.T) {
	scope := tenantScope(t, "tenant-a")
	for _, kind := range expKinds {
		t.Run(kind.name, func(t *testing.T) {
			rig := newExpRig(t, kind)
			// serialization: the waiter is another entity of the same shard and waits for the counter, holding
			// its own revision row; horizon: nothing is shared between entities, so the waiter targets the
			// holder's entity and waits for its advisory lock.
			waiterID, waiterSeq, pre := "waiter", uint64(1), persistence.ExpectGenesis()
			if !kind.serializes {
				waiterID, waiterSeq, pre = "holder", 2, persistence.Unconditional()
			}

			h := rig.park(scope, persistence.Unconditional(), expEvent("holder", 1, 1, 100))
			ctx, cancel := context.WithCancel(context.Background())
			waiter := rig.startWrite(ctx, rig.process(), scope, pre, expEvent(waiterID, waiterSeq, 1, 150))
			rig.blocked()
			cancel()
			if err := rig.wait(waiter); err == nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("a cancelled waiter must fail with context.Canceled, got %v", err)
			}
			if err := rig.finish(h, true); err != nil {
				t.Fatalf("holder: %v", err)
			}

			// no lock leaked and nothing committed: the same write goes through, with the same precondition
			rig.mustWrite(rig.main, scope, pre, expEvent(waiterID, waiterSeq, 1, 160))
			want := []string{"holder/1", fmt.Sprintf("%s/%d", waiterID, waiterSeq)}
			eq(t, want, rig.drain(scope, 1, 0, 10), "the cancelled write committed nothing")
			if kind.serializes {
				if last := rig.counter("tenant-a", 1); last != 2 {
					t.Fatalf("positions are dense, the cancelled write took none: last=%d", last)
				}
			}
			rig.noNullPositions()
		})
	}
}

// TestExperimentalAdapter_LockOrderWithRevisionRowsAndDelete: a parked writer holds its locks; a writer of
// another entity and a DeleteEvents of the parked entity run against it. None of them can form a cycle: all
// complete once the holder ends. (The stable order reduces cycles between these paths; it does not by itself
// prove the absence of every possible deadlock: see the stress test and the limits in design.md.)
func TestExperimentalAdapter_LockOrderWithRevisionRowsAndDelete(t *testing.T) {
	scope := tenantScope(t, "tenant-a")
	for _, kind := range expKinds {
		t.Run(kind.name, func(t *testing.T) {
			rig := newExpRig(t, kind)
			rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("e", 1, 1, 100))

			h := rig.park(scope, persistence.ExpectRevision(1), expEvent("e", 2, 1, 200))
			other := rig.startWrite(context.Background(), rig.process(), scope, persistence.ExpectGenesis(), expEvent("f", 1, 1, 300))
			del := make(chan error, 1)
			go func() { del <- rig.process().DeleteEvents(context.Background(), scope, "e", 1) }()
			rig.blocked()

			if err := rig.finish(h, true); err != nil {
				t.Fatalf("holder: %v", err)
			}
			if err := rig.wait(other); err != nil {
				t.Fatalf("other writer: %v", err)
			}
			select {
			case err := <-del:
				if err != nil {
					t.Fatalf("delete: %v", err)
				}
			case <-time.After(stepTimeout):
				t.Fatalf("DeleteEvents did not finish: a lock cycle or a leaked lock")
			}
			eq(t, []string{"e/2", "f/1"}, rig.drain(scope, 1, 0, 10), "e/1 was deleted, the rest delivered in position order")
		})
	}
}

// TestExperimentalAdapter_OtherScopeInFlight: a write of tenant a is parked. A tenant b write of the SAME shard
// number never waits, in any variant. What differs is the reader of tenant b: under serialization the counter is
// per (scope, shard) and tenant b reads at once; under the horizon tenant b waits for tenant a's transaction.
func TestExperimentalAdapter_OtherScopeInFlight(t *testing.T) {
	a, b := tenantScope(t, "tenant-a"), tenantScope(t, "tenant-b")
	for _, kind := range expKinds {
		t.Run(kind.name, func(t *testing.T) {
			rig := newExpRig(t, kind)
			h := rig.park(a, persistence.Unconditional(), expEvent("held", 1, 1, 100))
			other := rig.startWrite(context.Background(), rig.process(), b, persistence.Unconditional(), expEvent("other", 1, 1, 100))
			if err := rig.wait(other); err != nil {
				t.Fatalf("tenant b's write must not wait for tenant a: %v", err)
			}
			if kind.serializes {
				eq(t, []string{"other/1"}, rig.drain(b, 1, 0, 10), "tenant b reads at once, and only its own")
			} else {
				eq(t, nil, rig.drain(b, 1, 0, 10), "the horizon is cluster-wide: tenant b waits for tenant a's transaction")
			}
			eq(t, nil, rig.drain(a, 1, 0, 10), "tenant a's parked write is invisible")
			if err := rig.finish(h, true); err != nil {
				t.Fatalf("holder: %v", err)
			}
			eq(t, []string{"held/1"}, rig.drain(a, 1, 0, 10), "tenant a sees only its own event")
			eq(t, []string{"other/1"}, rig.drain(b, 1, 0, 10), "tenant b sees only its own event")
		})
	}
}

// TestExperimentalAdapter_HorizonIsClusterWide: a transaction in ANOTHER DATABASE of the same server holds back
// the reader of the horizon variant, whatever its scope or shard, and releases it when it ends. The
// serialization variants are not affected by it.
func TestExperimentalAdapter_HorizonIsClusterWide(t *testing.T) {
	scope := tenantScope(t, "tenant-a")
	for _, kind := range expKinds {
		t.Run(kind.name, func(t *testing.T) {
			rig := newExpRig(t, kind)
			foreign := openJournalPool(t, shared.NewDatabase(t))
			tx, err := foreign.Begin(context.Background())
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
			if _, err := tx.Exec(context.Background(), `SELECT pg_current_xact_id()`); err != nil {
				t.Fatalf("assign an xid: %v", err)
			}

			rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("e", 1, 1, 100))
			if kind.serializes {
				eq(t, []string{"e/1"}, rig.drain(scope, 1, 0, 10), "a foreign transaction does not delay a serialization reader")
			} else {
				eq(t, nil, rig.drain(scope, 1, 0, 10), "a foreign transaction in another database holds the horizon back")
				if err := tx.Commit(context.Background()); err != nil {
					t.Fatalf("commit: %v", err)
				}
				eq(t, []string{"e/1"}, rig.drain(scope, 1, 0, 10), "released when the foreign transaction ends: delayed, not lost")
			}
		})
	}
}

// TestExperimentalAdapter_RestartResume: nothing but the committed cursor survives a restart. A new process
// writes after it, positions keep growing, and resuming from the cursor loses nothing and does not cut a group.
func TestExperimentalAdapter_RestartResume(t *testing.T) {
	scope := tenantScope(t, "tenant-a")
	for _, kind := range expKinds {
		t.Run(kind.name, func(t *testing.T) {
			rig := newExpRig(t, kind)
			rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("a", 1, 1, 1), expEvent("b", 1, 1, 1)) // one group
			rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("c", 1, 1, 2))
			first, cursor := rig.page(scope, 1, 0, 1)
			eq(t, []string{"a/1", "b/1"}, first, "limit 1 still returns the whole group")
			_, last := rig.drainFrom(scope, 1, 0, 100)

			// "restart": every connection of the first process is gone, a new one writes
			rig.main = rig.process()
			rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("d", 1, 1, 3))

			rest, end := rig.drainFrom(scope, 1, cursor, 1)
			eq(t, []string{"c/1", "d/1"}, rest, "resuming from the committed cursor recovers the rest")
			if end <= last {
				t.Fatalf("a write after the restart must land above everything delivered: %d <= %d", end, last)
			}
			rig.noNullPositions()
		})
	}
}

// TestExperimentalAdapter_CounterLastNeverLeavesAPositionBehindTheCursor is the demonstration that taking the
// counter at the end is safe. A writer is parked AFTER its rows are inserted and BEFORE it takes the counter;
// another writer takes the counter, commits and is delivered; only then the first resumes. Its position is
// above the cursor the reader already advanced to, and no committed row ever lacks a position.
func TestExperimentalAdapter_CounterLastNeverLeavesAPositionBehindTheCursor(t *testing.T) {
	scope := tenantScope(t, "tenant-a")
	rig := newExpRig(t, expKind{postgres.KindSerializedLate, true})

	a, err := postgres.NewExperimentalStore(postgres.KindSerializedLate, rig.dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	store := a.(*postgres.ExperimentalShardSerializedStore)
	if err := store.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = store.Disconnect(context.Background()) })
	entered, release := make(chan struct{}), make(chan struct{})
	store.BeforePositions = func() { close(entered); <-release }
	first := rig.startWrite(context.Background(), store, scope, persistence.Unconditional(), expEvent("first", 1, 1, 100))
	select {
	case <-entered:
	case <-time.After(stepTimeout):
		t.Fatalf("the first writer never reached the point before the counter")
	}
	var visible int
	if err := rig.obs.QueryRow(context.Background(), `SELECT count(*) FROM events_store WHERE persistence_id='first'`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("the first writer's rows are inserted but uncommitted: they must be invisible (visible=%d err=%v)", visible, err)
	}

	rig.mustWrite(rig.main, scope, persistence.Unconditional(), expEvent("second", 1, 1, 200)) // takes the counter first
	got, cursor := rig.page(scope, 1, 0, 10)
	eq(t, []string{"second/1"}, got, "the second writer is delivered while the first has no position yet")

	close(release)
	if err := rig.wait(first); err != nil {
		t.Fatalf("first writer: %v", err)
	}
	var firstPos int64
	if err := rig.obs.QueryRow(context.Background(), `SELECT journal_pos FROM events_store WHERE persistence_id='first'`).Scan(&firstPos); err != nil {
		t.Fatalf("position: %v", err)
	}
	if firstPos <= cursor {
		t.Fatalf("the first writer's position %d must be above the cursor %d the reader already reached", firstPos, cursor)
	}
	eq(t, []string{"first/1"}, rig.drain(scope, 1, cursor, 10), "delivered after the cursor, not skipped")
	rig.noNullPositions()
}

// TestExperimentalAdapter_ConcurrentWritersLoseNothing is the stress: two processes; conditional single-entity
// writers (shard 0); unconditional multi-entity writers on a pool of shared entities (shard 0, overlapping
// batches); multi-shard batches listing their shards in opposite orders (shards 1-3). Every 7th write of one
// process rolls back. Readers poll by position during the whole run. Afterwards: no write failed (a deadlock
// would show as 40P01), every committed event was delivered exactly once, no committed row lacks a position,
// positions are dense per shard under serialization, and the entities that one writer owns were delivered in
// order. Unconditional writes carry no per-entity ordering guarantee, so the shared pool is checked for
// completeness only. This is evidence about these paths under this load, not a proof that no other
// interleaving can deadlock.
func TestExperimentalAdapter_ConcurrentWritersLoseNothing(t *testing.T) {
	scope := tenantScope(t, "tenant-a")
	for _, kind := range expKinds {
		t.Run(kind.name, func(t *testing.T) { runStress(t, kind, scope) })
	}
}

func runStress(t *testing.T, kind expKind, scope persistence.Scope) {
	rig := newExpRig(t, kind)
	procA, procB := rig.main, rig.process()
	var calls atomic.Int64
	procB.SetBeforeCommit(func() bool { return calls.Add(1)%7 != 0 })
	stores := []postgres.ExperimentalStore{procA, procB}

	const (
		shards     = 4
		txPerKind  = 60
		sharedPool = 6
	)
	var (
		mu        sync.Mutex
		committed = map[string]bool{}
		failures  []error
		wg        sync.WaitGroup
		sharedSeq [sharedPool]atomic.Uint64
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
				finished := readersDone.Load() // the exit read must start after the last commit
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

	for w := range 4 { // conditional, one owned entity each, shard 0
		store := stores[w%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, rev := fmt.Sprintf("cond-%d", w), uint64(0)
			for committedTx := 0; committedTx < txPerKind; {
				events := []*egopb.Event{expEvent(id, rev+1, 0, 1), expEvent(id, rev+2, 0, 1)}
				pre := persistence.ExpectRevision(rev)
				if rev == 0 {
					pre = persistence.ExpectGenesis()
				}
				switch err := store.WriteEvents(context.Background(), scope, events, pre); {
				case err == nil:
					record(events)
					rev += 2
					committedTx++
				case rolledBack(err):
				default:
					fail(err)
					return
				}
			}
		}()
	}
	for w := range 4 { // unconditional multi-entity over shared entities, shard 0
		store := stores[w%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range txPerKind {
				var events []*egopb.Event
				for _, idx := range []int{(w + i) % sharedPool, (w + i + 2) % sharedPool, (w + i + 4) % sharedPool} {
					events = append(events, expEvent(fmt.Sprintf("shared-%d", idx), sharedSeq[idx].Add(1), 0, 1))
				}
				switch err := store.WriteEvents(context.Background(), scope, events, persistence.Unconditional()); {
				case err == nil:
					record(events)
				case rolledBack(err): // the sequence numbers are simply not used: gaps are allowed
				default:
					fail(err)
					return
				}
			}
		}()
	}
	for w := range 4 { // multi-shard, shards listed in opposite orders, entities owned by the writer
		store := stores[w%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, b := uint64(1+w%2), uint64(2+w%2)
			if w >= 2 {
				a, b = b, a
			}
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
	readersDone.Store(true)
	readerWG.Wait()

	if len(failures) > 0 {
		t.Fatalf("a write failed (a deadlock would show here as 40P01): %v", errors.Join(failures...))
	}
	if p := readerErr.Load(); p != nil {
		t.Fatalf("a reader failed: %v", *p)
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
			if id[:6] != "shared" {
				if seq <= lastSeq[id] {
					t.Fatalf("entity %s delivered seq %d after %d: per-entity order broken", id, seq, lastSeq[id])
				}
				lastSeq[id] = seq
			}
		}
	}
	for key := range committed {
		if !seen[key] {
			t.Fatalf("OMISSION: %s was committed and never delivered", key)
		}
	}
	if len(seen) != len(committed) {
		t.Fatalf("delivered %d events but %d were committed (an uncommitted event was delivered)", len(seen), len(committed))
	}
	if calls.Load()/7 == 0 {
		t.Fatalf("the run exercised no rollback")
	}
	t.Logf("%s: committed events: %d, rollbacks injected: %d, no omission, no duplicate, no deadlock", kind.name, len(committed), calls.Load()/7)
	rig.noNullPositions()

	if !kind.serializes {
		return
	}
	for shard := range uint64(shards) {
		rows, err := rig.obs.Query(context.Background(), `SELECT DISTINCT journal_pos FROM events_store WHERE tenant_id='tenant-a' AND shard_number=$1 ORDER BY 1`, shard)
		if err != nil {
			t.Fatalf("positions: %v", err)
		}
		var got []int64
		for rows.Next() {
			var pos int64
			if err := rows.Scan(&pos); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got = append(got, pos)
		}
		rows.Close()
		last := rig.counter("tenant-a", int(shard))
		if int64(len(got)) != last {
			t.Fatalf("shard %d: %d distinct positions but the counter is %d", shard, len(got), last)
		}
		for i, pos := range got {
			if pos != int64(i+1) {
				t.Fatalf("shard %d: positions must be dense 1..%d, position %d is %d", shard, last, i+1, pos)
			}
		}
	}
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}
