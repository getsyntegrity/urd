//go:build journalexp

package eventstore_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/inttest/flows/eventstore/internal/measure"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/persistence/postgres"
)

// TestJournalAdapterBench measures the REAL adapter, current vs experimental, under one load driven through the
// public persistence.EventsStore of both (WriteEvents with its real preconditions, GetShardEvents):
//
//   - "current": postgres.EventStore as it is, reading by timestamp offset (the defect of #332);
//   - "current+index": the same, plus a composite read index over the timestamp, a control so the index the
//     experimental store carries is not mistaken for an effect of the scheme;
//   - "serialized": ExperimentalShardSerializedStore, reading by position.
//
// The timestamp of every event is taken just before the write starts, as the actor does, so the current
// adapter's reader can be handed a cursor behind an event that commits later. The run does not hide that: the
// readers poll in a tight loop, and every run ends by draining them and counting what was committed and never
// delivered ("omitted"). For "current" a non-zero count is the measured defect; for "serialized" it fails the
// run. Measuring only writes would not show absence of omissions; this does.
//
// Latencies: write = WriteEvents call; delivery = from the START of the write to the moment the polling reader
// was handed the event (it includes the write, the lock waits and the reader's own query time, the idle floor of
// which is the poll floor of TestJournalBench). Eligibility alone, from the commit, is not recorded here.
//
// Opt-in: URD_JOURNAL_BENCH=1 (and -tags journalexp). Own container, so fsync is a stated parameter. The adapter
// opens a pool of 20 connections, a fixed constant of Connect, shared by writers and readers.
func TestJournalAdapterBench(t *testing.T) {
	if os.Getenv("URD_JOURNAL_BENCH") != "1" {
		t.Skip("measurement, not a correctness test: set URD_JOURNAL_BENCH=1 to run it")
	}
	writers := envInt("URD_JOURNAL_ADAPTER_WRITERS", 12)
	duration := time.Duration(envInt("URD_JOURNAL_BENCH_SECONDS", 5)) * time.Second
	reps := envInt("URD_JOURNAL_BENCH_REPS", 3)
	fsync := envStr("URD_JOURNAL_BENCH_FSYNC", "on")

	ctx := context.Background()
	box, err := startBenchPostgres(ctx, fsync)
	if err != nil {
		t.Fatalf("start the benchmark container: %v", err)
	}
	t.Cleanup(func() { _ = box.terminate(ctx) })
	t.Logf("BENCH|config|%s|writers=%d duration=%s reps=%d adapter-pool=20", box.settings(ctx, t), writers, duration, reps)

	workloads := []adapterWorkload{
		{name: "W1 conditional, one entity, hot shard", shards: 1, run: workloadConditional},
		{name: "W2 unconditional multi-entity batch, hot shard", shards: 1, run: workloadMultiEntity},
		{name: "W3 unconditional multi-shard batch, 8 shards", shards: 8, run: workloadMultiShard},
		{name: "W4 unconditional overlapping entities, hot shard", shards: 1, run: workloadOverlap, unorderedEntities: true},
	}
	if only := os.Getenv("URD_JOURNAL_BENCH_SCENARIOS"); only != "" {
		var kept []adapterWorkload
		for _, w := range workloads {
			for _, prefix := range strings.Split(only, ",") {
				if strings.HasPrefix(w.name, strings.TrimSpace(prefix)) {
					kept = append(kept, w)
					break
				}
			}
		}
		workloads = kept
	}
	for _, kind := range []string{"current", "current+index", "serialized"} {
		for _, w := range workloads {
			var runs []adapterRun
			for range reps {
				runs = append(runs, adapterBenchOnce(ctx, t, box, kind, w, writers, duration))
			}
			reportAdapter(t, kind, w, runs)
		}
	}
}

type adapterWorkload struct {
	name   string
	shards int
	// run drives writer i until stop; it returns nothing, every committed event goes through rec.
	run func(i, shards int, store persistence.EventsStore, scope persistence.Scope, stop *atomic.Bool, rec *adapterRecorder)
	// unorderedEntities: entities are shared by several writers, so per-entity delivery order is not asserted.
	unorderedEntities bool
}

type adapterRecorder struct {
	lat     []time.Duration
	starts  map[string]time.Time
	commits int
	events  int
	failure error
}

func (r *adapterRecorder) ok(start time.Time, lat time.Duration, events []*egopb.Event) {
	r.lat = append(r.lat, lat)
	r.commits++
	for _, e := range events {
		r.events++
		r.starts[adapterKey(e.GetPersistenceId(), e.GetSequenceNumber())] = start
	}
}

func adapterKey(id string, seq uint64) string { return fmt.Sprintf("%s/%d", id, seq) }

type adapterRun struct {
	txs, events, omitted, recovered int
	elapsed                         time.Duration
	writeLat, delivery              []time.Duration
	counterWait                     []time.Duration
}

func adapterBenchOnce(ctx context.Context, t *testing.T, box *benchPostgres, kind string, w adapterWorkload, writers int, duration time.Duration) adapterRun {
	t.Helper()
	dsn := box.newDatabase(t)
	var (
		store   persistence.EventsStore
		waitMu  sync.Mutex
		waits   []time.Duration
		scope   = persistence.Unscoped()
		stopped atomic.Bool
	)
	switch kind {
	case "current", "current+index":
		s, err := provisionPostgresTestStore(dsn)
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		if err := s.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer s.Disconnect(ctx) //nolint:errcheck
		if kind == "current+index" {
			// control: the same composite read index the experimental store carries, over the timestamp the
			// current reader orders by, so the index (not the scheme) is not what is being compared
			pool, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			defer pool.Close()
			if _, err := pool.Exec(ctx, `CREATE INDEX idx_events_store_ts_control ON events_store (tenant_id, shard_number, timestamp)`); err != nil {
				t.Fatalf("control index: %v", err)
			}
		}
		store = s
	case "serialized":
		s, err := provisionExperimentalStore(dsn)
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		if err := s.Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer s.Disconnect(ctx) //nolint:errcheck
		s.OnCounterWait = func(d time.Duration) { waitMu.Lock(); waits = append(waits, d); waitMu.Unlock() }
		store = s
	}

	recs := make([]*adapterRecorder, writers)
	var writersWG sync.WaitGroup
	begin := time.Now()
	for i := range writers {
		rec := &adapterRecorder{starts: map[string]time.Time{}}
		recs[i] = rec
		writersWG.Add(1)
		go func() {
			defer writersWG.Done()
			w.run(i, w.shards, store, scope, &stopped, rec)
		}()
	}

	type delivery struct {
		key string
		id  string
		seq uint64
		at  time.Time
	}
	delivered := make([][]delivery, w.shards)
	var (
		allWritten atomic.Bool
		readersWG  sync.WaitGroup
		readerErr  atomic.Pointer[error]
	)
	for shard := range w.shards {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			var after int64
			for {
				// the exit read must START after the last commit: an empty read issued before it proves nothing
				finished := allWritten.Load()
				events, next, err := store.GetShardEvents(ctx, scope, uint64(shard), after, 100)
				if err != nil {
					readerErr.CompareAndSwap(nil, &err)
					return
				}
				if len(events) > 0 {
					now := time.Now()
					for _, e := range events {
						delivered[shard] = append(delivered[shard], delivery{adapterKey(e.GetPersistenceId(), e.GetSequenceNumber()), e.GetPersistenceId(), e.GetSequenceNumber(), now})
					}
					after = next
				} else if finished {
					return
				}
			}
		}()
	}

	time.Sleep(duration) // the run length is the load
	stopped.Store(true)
	writersWG.Wait()
	elapsed := time.Since(begin)
	allWritten.Store(true)
	readersWG.Wait()

	for i, rec := range recs {
		if rec.failure != nil {
			t.Fatalf("%s / %s: writer %d: %v", kind, w.name, i, rec.failure)
		}
	}
	if p := readerErr.Load(); p != nil {
		t.Fatalf("%s / %s: reader: %v", kind, w.name, *p)
	}

	starts := map[string]time.Time{}
	run := adapterRun{elapsed: elapsed}
	for _, rec := range recs {
		run.txs += rec.commits
		run.events += rec.events
		run.writeLat = append(run.writeLat, rec.lat...)
		for k, v := range rec.starts {
			starts[k] = v
		}
	}
	seen := map[string]bool{}
	for shard, ds := range delivered {
		last := map[string]uint64{}
		for _, d := range ds {
			if seen[d.key] {
				t.Fatalf("%s / %s: %s delivered twice", kind, w.name, d.key)
			}
			seen[d.key] = true
			if !w.unorderedEntities && d.seq <= last[d.id] && kind == "serialized" {
				t.Fatalf("%s / %s: shard %d entity %s delivered seq %d after %d", kind, w.name, shard, d.id, d.seq, last[d.id])
			}
			last[d.id] = max(last[d.id], d.seq)
			if at, ok := starts[d.key]; ok {
				run.delivery = append(run.delivery, max(0, d.at.Sub(at)))
			}
		}
	}
	var omittedKeys []string
	for key := range starts {
		if !seen[key] {
			run.omitted++
			omittedKeys = append(omittedKeys, key)
		}
	}
	if len(omittedKeys) > 0 {
		// an omitted event is still in the journal: a read from zero (a rebuild) must recover every one of them
		fresh := map[string]bool{}
		for shard := range w.shards {
			var after int64
			for {
				events, next, err := store.GetShardEvents(ctx, scope, uint64(shard), after, 1000)
				if err != nil {
					t.Fatalf("%s / %s: read from zero: %v", kind, w.name, err)
				}
				if len(events) == 0 {
					break
				}
				for _, e := range events {
					fresh[adapterKey(e.GetPersistenceId(), e.GetSequenceNumber())] = true
				}
				after = next
			}
		}
		for _, key := range omittedKeys {
			if !fresh[key] {
				t.Fatalf("%s / %s: %s was omitted and a read from zero does not recover it", kind, w.name, key)
			}
		}
		run.recovered = len(omittedKeys)
	}
	if kind == "serialized" && run.omitted > 0 {
		t.Fatalf("serialized / %s: %d committed events were never delivered: an omission", w.name, run.omitted)
	}
	waitMu.Lock()
	run.counterWait = slices.Clone(waits)
	waitMu.Unlock()
	return run
}

func reportAdapter(t *testing.T, kind string, w adapterWorkload, runs []adapterRun) {
	t.Helper()
	var lat, del, cw []time.Duration
	var txps, evps []float64
	var events, omitted, recovered int
	for _, r := range runs {
		lat = append(lat, r.writeLat...)
		del = append(del, r.delivery...)
		cw = append(cw, r.counterWait...)
		txps = append(txps, float64(r.txs)/r.elapsed.Seconds())
		evps = append(evps, float64(r.events)/r.elapsed.Seconds())
		events += r.events
		omitted += r.omitted
		recovered += r.recovered
	}
	mean := func(xs []float64) float64 {
		var sum float64
		for _, x := range xs {
			sum += x
		}
		return sum / float64(len(xs))
	}
	l, d, c := measure.Summarize(lat), measure.Summarize(del), measure.Summarize(cw)
	t.Logf("BENCH|row|%s|%s|tx/s=%.0f (%.0f..%.0f)|ev/s=%.0f|write p50/p95/p99=%s/%s/%s|start-to-delivery p50/p95/p99=%s/%s/%s|counter-wait p50/p95/p99=%s/%s/%s|events=%d omitted=%d (%.3f%%) recovered-by-read-from-zero=%d",
		kind, w.name, mean(txps), slices.Min(txps), slices.Max(txps), mean(evps),
		l.P50, l.P95, l.P99, d.P50, d.P95, d.P99, c.P50, c.P95, c.P99, events, omitted, 100*float64(omitted)/float64(max(events, 1)), recovered)
}

// ---- workloads: the same code for both adapters

func timeIt(store persistence.EventsStore, scope persistence.Scope, events []*egopb.Event, pre persistence.WritePrecondition, rec *adapterRecorder) bool {
	start := time.Now()
	ts := start.UnixNano() // stamped before the write, as the actor does
	for _, e := range events {
		e.Timestamp = ts
	}
	err := store.WriteEvents(context.Background(), scope, events, pre)
	lat := time.Since(start)
	if err != nil {
		rec.failure = err
		return false
	}
	rec.ok(start, lat, events)
	return true
}

func workloadConditional(i, _ int, store persistence.EventsStore, scope persistence.Scope, stop *atomic.Bool, rec *adapterRecorder) {
	id, rev := fmt.Sprintf("cond-%d", i), uint64(0)
	for !stop.Load() {
		events := []*egopb.Event{expEvent(id, rev+1, 0, 0), expEvent(id, rev+2, 0, 0), expEvent(id, rev+3, 0, 0)}
		pre := persistence.ExpectRevision(rev)
		if rev == 0 {
			pre = persistence.ExpectGenesis()
		}
		if !timeIt(store, scope, events, pre, rec) {
			return
		}
		rev += 3
	}
}

func workloadMultiEntity(i, _ int, store persistence.EventsStore, scope persistence.Scope, stop *atomic.Bool, rec *adapterRecorder) {
	ids := []string{fmt.Sprintf("me-%d-a", i), fmt.Sprintf("me-%d-b", i), fmt.Sprintf("me-%d-c", i)}
	for seq := uint64(1); !stop.Load(); seq++ {
		events := []*egopb.Event{expEvent(ids[0], seq, 0, 0), expEvent(ids[1], seq, 0, 0), expEvent(ids[2], seq, 0, 0)}
		if !timeIt(store, scope, events, persistence.Unconditional(), rec) {
			return
		}
	}
}

func workloadMultiShard(i, shards int, store persistence.EventsStore, scope persistence.Scope, stop *atomic.Bool, rec *adapterRecorder) {
	a, b := uint64(i%shards), uint64((i+3)%shards)
	x, y := fmt.Sprintf("ms-%d-x", i), fmt.Sprintf("ms-%d-y", i)
	for seq := uint64(1); !stop.Load(); seq++ {
		events := []*egopb.Event{expEvent(x, seq, a, 0), expEvent(y, seq, b, 0)}
		if i%2 == 1 { // list the shards in the opposite order
			events[0], events[1] = events[1], events[0]
		}
		if !timeIt(store, scope, events, persistence.Unconditional(), rec) {
			return
		}
	}
}

// overlapSeq hands out unique sequence numbers per shared entity, process-wide, so concurrent unconditional
// batches never collide on the primary key; the order in which they commit is up to the database.
var overlapSeq [12]atomic.Uint64

func workloadOverlap(i, _ int, store persistence.EventsStore, scope persistence.Scope, stop *atomic.Bool, rec *adapterRecorder) {
	rng := rand.New(rand.NewSource(int64(i) + 1))
	for !stop.Load() {
		picked := rng.Perm(len(overlapSeq))[:3]
		var events []*egopb.Event
		for _, idx := range picked {
			events = append(events, expEvent(fmt.Sprintf("ov-%d", idx), overlapSeq[idx].Add(1), 0, 0))
		}
		if !timeIt(store, scope, events, persistence.Unconditional(), rec) {
			return
		}
	}
}

var _ = postgres.ErrExperimentalRollback
