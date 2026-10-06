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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/inttest/flows/eventstore/internal/measure"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/persistence/postgres"
)

// TestJournalAdapterBench measures the REAL adapter, current vs the experimental variants, under one load driven
// through the public persistence.EventsStore of each (WriteEvents with its real preconditions, GetShardEvents):
//
//   - current: postgres.EventStore as it is, reading by timestamp offset (the defect of #332);
//   - current+index: the same plus a composite read index over the timestamp, a CONTROL with an index equivalent
//     to the one the experimental variants carry (and the same extra write cost of maintaining one more index),
//     so that no difference is credited to a scheme that an index explains;
//   - serialized, serialized-late, horizon: the experimental variants (persistence/postgres, tag journalexp).
//
// The event timestamp is taken just before each write, as the actor does, so the current adapter's reader can be
// handed a cursor behind an event that commits later. Readers poll in a tight loop and every run ends by draining
// them and counting what was committed and never delivered ("omitted"); each omitted event is then checked to be
// returned by a read from zero. For the experimental variants a non-zero count fails the run. The omission
// percentages are those of THIS experimental load (closed loop at saturation, a reader polling without pause);
// they show that the mechanism is easy to trigger, not what a deployment omits.
//
// Scenarios: W1-W4 at saturation (closed loop); W1 and W3 below saturation (open loop, a fixed offered rate,
// latency measured from the INTENDED start so a queue is not hidden); and W1 at 300 tx/s offered with a held
// transaction in the same shard, another scope, or another database of the server. The held transaction (500 ms
// held, 100 ms gap, repeated) is part of the experimental load: the experimental variants hold a real adapter
// write parked before its commit; the current adapter has no hook, so its holder is a raw transaction that
// inserts the same revision and event rows and waits.
//
// Latencies: write = the WriteEvents call; start-to-delivery = from the intended start of the write to the moment
// the polling reader was handed the event (write + lock waits + the reader's query time, whose idle floor is the
// poll floor of TestJournalBench). Opt-in: URD_JOURNAL_BENCH=1 and -tags journalexp. Own container, so fsync is a
// stated parameter. The adapter opens a fixed pool of 20 connections shared by writers and readers.
func TestJournalAdapterBench(t *testing.T) {
	if os.Getenv("URD_JOURNAL_BENCH") != "1" {
		t.Skip("measurement, not a correctness test: set URD_JOURNAL_BENCH=1 to run it")
	}
	writers := envInt("URD_JOURNAL_ADAPTER_WRITERS", 12)
	duration := time.Duration(envInt("URD_JOURNAL_BENCH_SECONDS", 5)) * time.Second
	reps := envInt("URD_JOURNAL_BENCH_REPS", 3)
	hold := time.Duration(envInt("URD_JOURNAL_BENCH_HOLD_MS", 500)) * time.Millisecond
	gap := time.Duration(envInt("URD_JOURNAL_BENCH_GAP_MS", 100)) * time.Millisecond
	fsync := envStr("URD_JOURNAL_BENCH_FSYNC", "on")

	ctx := context.Background()
	box, err := startBenchPostgres(ctx, fsync)
	if err != nil {
		t.Fatalf("start the benchmark container: %v", err)
	}
	t.Cleanup(func() { _ = box.terminate(ctx) })
	t.Logf("BENCH|config|%s|writers=%d duration=%s reps=%d hold=%s gap=%s adapter-pool=20", box.settings(ctx, t), writers, duration, reps, hold, gap)

	scenarios := []adapterScenario{
		{name: "W1 conditional, hot shard, saturated", shards: 1, workload: wlConditional},
		{name: "W2 multi-entity batch, hot shard, saturated", shards: 1, workload: wlMultiEntity},
		{name: "W3 multi-shard batch (8 shards), saturated", shards: 8, workload: wlMultiShard},
		{name: "W4 overlapping entities, hot shard, saturated", shards: 1, workload: wlOverlap, unordered: true},
		{name: "R100 W1 at 100 tx/s offered", shards: 1, workload: wlConditional, rate: 100},
		{name: "R300 W1 at 300 tx/s offered", shards: 1, workload: wlConditional, rate: 300},
		{name: "R500 W1 at 500 tx/s offered", shards: 1, workload: wlConditional, rate: 500},
		{name: "R500M W3 (8 shards) at 500 tx/s offered", shards: 8, workload: wlMultiShard, rate: 500},
		{name: "H1 W1 at 300 tx/s, held tx in the SAME shard", shards: 1, workload: wlConditional, rate: 300, holder: holdSameShard},
		{name: "H2 W1 at 300 tx/s, held tx in ANOTHER SCOPE", shards: 1, workload: wlConditional, rate: 300, holder: holdOtherScope},
		{name: "H3 W1 at 300 tx/s, held tx in ANOTHER DATABASE", shards: 1, workload: wlConditional, rate: 300, holder: holdOtherDatabase},
		{name: "B1 W1 at 300 tx/s, publisher stalled 10s then resumed (batch only)", shards: 1, workload: wlConditional, rate: 300, stallPublisher: 10 * time.Second, duration: 25 * time.Second},
	}
	scenarios = filterByPrefix(scenarios, os.Getenv("URD_JOURNAL_BENCH_SCENARIOS"), func(s adapterScenario) string { return s.name })
	kinds := []string{"current", "current+index", postgres.KindSerialized, postgres.KindSerializedLate, postgres.KindHorizon}
	if only := os.Getenv("URD_JOURNAL_BENCH_KINDS"); only != "" {
		kinds = strings.Split(only, ",")
	}
	for _, kind := range kinds {
		for _, sc := range scenarios {
			if sc.stallPublisher > 0 && !strings.HasPrefix(kind, postgres.KindBatch) {
				continue // only the variant that publishes after the commit has a publisher to stall
			}
			var runs []adapterRun
			for range reps {
				runs = append(runs, adapterBenchOnce(ctx, t, box, kind, sc, writers, duration, hold, gap))
			}
			reportAdapter(t, kind, sc, runs)
		}
	}
}

func filterByPrefix[T any](all []T, csv string, name func(T) string) []T {
	if csv == "" {
		return all
	}
	var kept []T
	for _, x := range all {
		for _, prefix := range strings.Split(csv, ",") {
			if strings.HasPrefix(name(x), strings.TrimSpace(prefix)+" ") || name(x) == strings.TrimSpace(prefix) {
				kept = append(kept, x)
				break
			}
		}
	}
	return kept
}

type adapterHolder int

const (
	holdNone adapterHolder = iota
	holdSameShard
	holdOtherScope
	holdOtherDatabase
)

// writeGen returns the next write of one writer.
type writeGen func() ([]*egopb.Event, persistence.WritePrecondition)

type adapterScenario struct {
	name     string
	shards   int
	workload func(i, shards int) writeGen
	// rate is the offered load in transactions per second over all writers; 0 saturates (closed loop).
	rate   int
	holder adapterHolder
	// unordered: entities are shared by several writers, so per-entity delivery order is not asserted.
	unordered bool
	// stallPublisher (batch variant only): the publisher does not run for this long at the start of the run, then
	// starts; the run lasts stallPublisher + duration. It measures the backlog and the recovery.
	stallPublisher time.Duration
	// duration overrides the run length of the scenario (0 uses the global one)
	duration time.Duration
}

type adapterRecorder struct {
	lat     []time.Duration
	starts  map[string]time.Time
	commits int
	events  int
	failure error
}

func adapterKey(id string, seq uint64) string { return fmt.Sprintf("%s/%d", id, seq) }

type adapterRun struct {
	txs, events, omitted, recovered int
	elapsed                         time.Duration
	writeLat, delivery, lockWait    []time.Duration
	holds                           int
	// cost of the run, from the database: bytes of events_store with its indexes, WAL written during the run
	tableBytes, walBytes int64
	// batch variant: the largest backlog sampled, and the time from the publisher starting until it reached zero
	maxBacklog   int64
	drainResumed time.Duration
}

func adapterBenchOnce(ctx context.Context, t *testing.T, box *benchPostgres, kind string, sc adapterScenario, writers int, duration, hold, gap time.Duration) adapterRun {
	t.Helper()
	dsn := box.newDatabase(t)
	var (
		store   persistence.EventsStore
		waitMu  sync.Mutex
		waits   []time.Duration
		scope   = persistence.Unscoped()
		stopped atomic.Bool
		holder  func() (func(), error) // one hold cycle; returns when released

		publication postgres.PublicationControl // set for the batch variant
		pubMu       sync.Mutex
		stopPub     func()
		resumedAt   time.Time
	)
	onWait := func(d time.Duration) { waitMu.Lock(); waits = append(waits, d); waitMu.Unlock() }

	// "batch:1000" is the batch variant with a publisher batch size of 1000 (sensitivity to N)
	factoryKind, batchN := kind, 0
	if strings.HasSuffix(kind, "+pubpool") {
		factoryKind = strings.TrimSuffix(kind, "+pubpool")
	}
	if strings.HasPrefix(kind, postgres.KindBatch+":") {
		factoryKind = postgres.KindBatch
		fmt.Sscanf(strings.TrimPrefix(kind, postgres.KindBatch+":"), "%d", &batchN)
	}
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
	default:
		s, err := provisionExperimentalKind(factoryKind, dsn)
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		if err := s.(interface{ Connect(context.Context) error }).Connect(ctx); err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer s.(interface{ Disconnect(context.Context) error }).Disconnect(ctx) //nolint:errcheck
		if b, ok := s.(*postgres.ExperimentalBatchStore); ok && batchN > 0 {
			b.BatchSize = batchN
		}
		if b, ok := s.(*postgres.ExperimentalBatchStore); ok && strings.HasSuffix(kind, "+pubpool") {
			// DIAGNOSTIC kind "batch+pubpool": the publisher has its own pool of 4 connections
			if err := b.DedicatePublisherPool(ctx, dsn, 4); err != nil {
				t.Fatalf("publisher pool: %v", err)
			}
		}
		s.SetOnLockWait(onWait)
		store = s
		if pc, ok := s.(postgres.PublicationControl); ok {
			publication = pc
		}
	}

	// holder: a transaction held open, repeated until the writers stop. Part of the experimental load.
	var otherDB *pgxpool.Pool
	holderScope := persistence.Unscoped()
	if sc.holder == holdOtherScope {
		holderScope = tenantScope(t, "tenant-h")
	}
	if sc.holder == holdOtherDatabase {
		var err error
		if otherDB, err = pgxpool.New(ctx, box.newDatabase(t)); err != nil {
			t.Fatalf("connect to the other database: %v", err)
		}
		defer otherDB.Close()
	}
	if sc.holder == holdSameShard || sc.holder == holdOtherScope {
		holderSeq := uint64(0)
		switch kind {
		case "current", "current+index":
			pool, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			defer pool.Close()
			tenant := ""
			if sc.holder == holdOtherScope {
				tenant = "tenant-h"
			}
			holder = func() (func(), error) {
				holderSeq++
				tx, err := pool.Begin(ctx)
				if err != nil {
					return nil, err
				}
				// the rows a real write of the current adapter holds: its revision row and its event row
				if _, err := tx.Exec(ctx, `INSERT INTO events_store_revisions (tenant_id, persistence_id, revision) VALUES ($1, 'holder', $2)
					ON CONFLICT (tenant_id, persistence_id) DO UPDATE SET revision = EXCLUDED.revision`, tenant, holderSeq); err != nil {
					return nil, err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO events_store (tenant_id, persistence_id, sequence_number, event_payload, event_manifest, timestamp, shard_number)
					VALUES ($1, 'holder', $2, '\x'::bytea, 'x', $3, 0)`, tenant, holderSeq, time.Now().UnixNano()); err != nil {
					return nil, err
				}
				return func() { time.Sleep(hold); _ = tx.Commit(ctx) }, nil
			}
		default:
			hs, err := provisionExperimentalKindOver(factoryKind, dsn)
			if err != nil {
				t.Fatalf("holder store: %v", err)
			}
			defer hs.(interface{ Disconnect(context.Context) error }).Disconnect(ctx) //nolint:errcheck
			hs.SetBeforeCommit(func() bool { time.Sleep(hold); return true })
			holder = func() (func(), error) {
				holderSeq++
				return func() {}, hs.WriteEvents(ctx, holderScope, []*egopb.Event{expEvent("holder", holderSeq, 0, time.Now().UnixNano())}, persistence.Unconditional())
			}
		}
	}

	// cost accounting: WAL written during the run, read from the database
	costConn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect for costs: %v", err)
	}
	defer costConn.Close(ctx) //nolint:errcheck
	var startLSN string
	if err := costConn.QueryRow(ctx, `SELECT pg_current_wal_lsn()::text`).Scan(&startLSN); err != nil {
		t.Fatalf("wal position: %v", err)
	}

	// batch variant: the background publisher (a round every 5 ms and right after a local commit), possibly stalled
	startPublisher := func() {
		pubMu.Lock()
		defer pubMu.Unlock()
		if publication != nil && stopPub == nil {
			stopPub = publication.StartPublisher(ctx, 5*time.Millisecond)
			resumedAt = time.Now()
		}
	}
	var (
		maxBacklog atomic.Int64
		drainedAt  atomic.Int64 // unix nanos of the first zero sample after the publisher resumed
		samplerWG  sync.WaitGroup
		samplerEnd = make(chan struct{})
	)
	if publication != nil {
		if sc.stallPublisher > 0 {
			time.AfterFunc(sc.stallPublisher, startPublisher)
		} else {
			startPublisher()
		}
		samplerWG.Add(1)
		go func() {
			defer samplerWG.Done()
			tick := time.NewTicker(50 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-samplerEnd:
					return
				case <-tick.C:
					n, err := publication.Pending(ctx)
					if err != nil {
						continue
					}
					if n > maxBacklog.Load() {
						maxBacklog.Store(n)
					}
					pubMu.Lock()
					resumed := !resumedAt.IsZero()
					at := resumedAt
					pubMu.Unlock()
					if resumed && n == 0 && drainedAt.Load() == 0 && time.Since(at) > 0 {
						drainedAt.Store(time.Now().UnixNano())
					}
				}
			}
		}()
	}

	recs := make([]*adapterRecorder, writers)
	var writersWG sync.WaitGroup
	begin := time.Now()
	for i := range writers {
		rec := &adapterRecorder{starts: map[string]time.Time{}}
		recs[i] = rec
		gen := sc.workload(i, sc.shards)
		writersWG.Add(1)
		go func() {
			defer writersWG.Done()
			var interval time.Duration
			if sc.rate > 0 {
				interval = time.Duration(float64(writers) / float64(sc.rate) * float64(time.Second))
			}
			next := time.Now().Add(time.Duration(i) * interval / time.Duration(writers)) // spread the writers
			for !stopped.Load() {
				intended := time.Now()
				if interval > 0 { // open loop: the schedule is the offered load, not the previous completion
					if d := time.Until(next); d > 0 {
						time.Sleep(d)
					}
					intended, next = next, next.Add(interval)
					if stopped.Load() {
						return
					}
				}
				events, pre := gen()
				call := time.Now()
				for _, e := range events {
					e.Timestamp = call.UnixNano() // stamped just before the write, as the actor does
				}
				err := store.WriteEvents(ctx, scope, events, pre)
				lat := time.Since(call)
				if err != nil {
					rec.failure = err
					return
				}
				rec.lat = append(rec.lat, lat)
				rec.commits++
				for _, e := range events {
					rec.events++
					rec.starts[adapterKey(e.GetPersistenceId(), e.GetSequenceNumber())] = intended
				}
			}
		}()
	}

	var holdsDone atomic.Int64
	var holderWG sync.WaitGroup
	if sc.holder == holdOtherDatabase || holder != nil {
		holderWG.Add(1)
		go func() {
			defer holderWG.Done()
			for !stopped.Load() {
				var release func()
				if sc.holder == holdOtherDatabase {
					tx, err := otherDB.Begin(ctx)
					if err != nil {
						return
					}
					if _, err := tx.Exec(ctx, `SELECT pg_current_xact_id()`); err != nil {
						return
					}
					release = func() { time.Sleep(hold); _ = tx.Commit(ctx) }
				} else {
					r, err := holder()
					if err != nil {
						return
					}
					release = r
				}
				release()
				holdsDone.Add(1)
				if !stopped.Load() {
					time.Sleep(gap)
				}
			}
		}()
	}

	type delivery struct {
		key string
		id  string
		seq uint64
		at  time.Time
	}
	delivered := make([][]delivery, sc.shards)
	var (
		allWritten atomic.Bool
		readersWG  sync.WaitGroup
		readerErr  atomic.Pointer[error]
	)
	for shard := range sc.shards {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			var after int64
			for {
				finished := allWritten.Load() // the exit read must START after the last commit
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

	runFor := duration
	if sc.duration > 0 {
		runFor = sc.duration
	}
	time.Sleep(runFor + sc.stallPublisher) // the run length is the load
	stopped.Store(true)
	writersWG.Wait()
	elapsed := time.Since(begin)
	holderWG.Wait()
	if publication != nil {
		startPublisher() // a stall that outlived the run still ends here
		if _, err := publication.PublishAll(ctx); err != nil {
			t.Fatalf("%s / %s: final publication: %v", kind, sc.name, err)
		}
		pubMu.Lock()
		stop := stopPub
		pubMu.Unlock()
		stop()
		close(samplerEnd)
		samplerWG.Wait()
	}
	allWritten.Store(true)
	readersWG.Wait()

	for i, rec := range recs {
		if rec.failure != nil {
			t.Fatalf("%s / %s: writer %d: %v", kind, sc.name, i, rec.failure)
		}
	}
	if p := readerErr.Load(); p != nil {
		t.Fatalf("%s / %s: reader: %v", kind, sc.name, *p)
	}

	starts := map[string]time.Time{}
	run := adapterRun{elapsed: elapsed, holds: int(holdsDone.Load())}
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
				t.Fatalf("%s / %s: %s delivered twice", kind, sc.name, d.key)
			}
			seen[d.key] = true
			if !sc.unordered && !strings.HasPrefix(d.id, "holder") && d.seq <= last[d.id] && kind != "current" && kind != "current+index" {
				t.Fatalf("%s / %s: shard %d entity %s delivered seq %d after %d", kind, sc.name, shard, d.id, d.seq, last[d.id])
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
		for shard := range sc.shards {
			var after int64
			for {
				events, next, err := store.GetShardEvents(ctx, scope, uint64(shard), after, 1000)
				if err != nil {
					t.Fatalf("%s / %s: read from zero: %v", kind, sc.name, err)
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
				t.Fatalf("%s / %s: %s was omitted and a read from zero does not recover it", kind, sc.name, key)
			}
		}
		run.recovered = len(omittedKeys)
	}
	if kind != "current" && kind != "current+index" && run.omitted > 0 {
		t.Fatalf("%s / %s: %d committed events were never delivered: an omission", kind, sc.name, run.omitted)
	}
	waitMu.Lock()
	run.lockWait = slices.Clone(waits)
	waitMu.Unlock()

	if err := costConn.QueryRow(ctx, `SELECT pg_wal_lsn_diff(pg_current_wal_lsn(), $1::pg_lsn)::bigint`, startLSN).Scan(&run.walBytes); err != nil {
		t.Fatalf("wal diff: %v", err)
	}
	if err := costConn.QueryRow(ctx, `SELECT pg_total_relation_size('events_store')`).Scan(&run.tableBytes); err != nil {
		t.Fatalf("relation size: %v", err)
	}
	run.maxBacklog = maxBacklog.Load()
	if at := drainedAt.Load(); at != 0 && !resumedAt.IsZero() {
		run.drainResumed = time.Unix(0, at).Sub(resumedAt)
	}
	return run
}

// provisionExperimentalKindOver migrates nothing (the schema exists) and returns a connected variant: the holder
// store, over the database the measured store already provisioned.
func provisionExperimentalKindOver(kind, dsn string) (postgres.ExperimentalStore, error) {
	s, err := postgres.NewExperimentalStore(kind, dsn)
	if err != nil {
		return nil, err
	}
	if err := s.(interface{ Connect(context.Context) error }).Connect(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func reportAdapter(t *testing.T, kind string, sc adapterScenario, runs []adapterRun) {
	t.Helper()
	var lat, del, lw []time.Duration
	var txps, evps []float64
	var events, omitted, recovered, holds int
	var tableBytes, walBytes, maxBacklog int64
	var drains []time.Duration
	for _, r := range runs {
		tableBytes += r.tableBytes
		walBytes += r.walBytes
		maxBacklog = max(maxBacklog, r.maxBacklog)
		if r.drainResumed > 0 {
			drains = append(drains, r.drainResumed)
		}
		lat = append(lat, r.writeLat...)
		del = append(del, r.delivery...)
		lw = append(lw, r.lockWait...)
		txps = append(txps, float64(r.txs)/r.elapsed.Seconds())
		evps = append(evps, float64(r.events)/r.elapsed.Seconds())
		events += r.events
		omitted += r.omitted
		recovered += r.recovered
		holds += r.holds
	}
	mean := func(xs []float64) float64 {
		var sum float64
		for _, x := range xs {
			sum += x
		}
		return sum / float64(len(xs))
	}
	l, d, c := measure.Summarize(lat), measure.Summarize(del), measure.Summarize(lw)
	t.Logf("BENCH|row|%s|%s|tx/s=%.0f (%.0f..%.0f)|ev/s=%.0f|write p50/p95/p99=%s/%s/%s|start-to-delivery p50/p95/p99=%s/%s/%s|lock-wait p50/p95/p99=%s/%s/%s|events=%d omitted=%d (%.3f%%) recovered-by-read-from-zero=%d|held-tx=%d|table+idx B/event=%.0f WAL B/event=%.0f|max-backlog=%d drain-after-resume=%v",
		kind, sc.name, mean(txps), slices.Min(txps), slices.Max(txps), mean(evps),
		l.P50, l.P95, l.P99, d.P50, d.P95, d.P99, c.P50, c.P95, c.P99,
		events, omitted, 100*float64(omitted)/float64(max(events, 1)), recovered, holds,
		float64(tableBytes)/float64(max(events, 1)), float64(walBytes)/float64(max(events, 1)), maxBacklog, drains)
}

// ---- workloads: the same code for every variant

func wlConditional(i, _ int) writeGen {
	id, rev := fmt.Sprintf("cond-%d", i), uint64(0)
	return func() ([]*egopb.Event, persistence.WritePrecondition) {
		events := []*egopb.Event{expEvent(id, rev+1, 0, 0), expEvent(id, rev+2, 0, 0), expEvent(id, rev+3, 0, 0)}
		pre := persistence.ExpectRevision(rev)
		if rev == 0 {
			pre = persistence.ExpectGenesis()
		}
		rev += 3
		return events, pre
	}
}

func wlMultiEntity(i, _ int) writeGen {
	ids := []string{fmt.Sprintf("me-%d-a", i), fmt.Sprintf("me-%d-b", i), fmt.Sprintf("me-%d-c", i)}
	seq := uint64(0)
	return func() ([]*egopb.Event, persistence.WritePrecondition) {
		seq++
		return []*egopb.Event{expEvent(ids[0], seq, 0, 0), expEvent(ids[1], seq, 0, 0), expEvent(ids[2], seq, 0, 0)}, persistence.Unconditional()
	}
}

func wlMultiShard(i, shards int) writeGen {
	a, b := uint64(i%shards), uint64((i+3)%shards)
	x, y := fmt.Sprintf("ms-%d-x", i), fmt.Sprintf("ms-%d-y", i)
	seq := uint64(0)
	return func() ([]*egopb.Event, persistence.WritePrecondition) {
		seq++
		events := []*egopb.Event{expEvent(x, seq, a, 0), expEvent(y, seq, b, 0)}
		if i%2 == 1 { // list the shards in the opposite order
			events[0], events[1] = events[1], events[0]
		}
		return events, persistence.Unconditional()
	}
}

// overlapSeq hands out unique sequence numbers per shared entity, process-wide, so concurrent unconditional
// batches never collide on the primary key; the order in which they commit is up to the database. It is shared
// by every run of the process, so sequence numbers only grow.
var overlapSeq [12]atomic.Uint64

func wlOverlap(i, _ int) writeGen {
	rng := rand.New(rand.NewSource(int64(i) + 1))
	return func() ([]*egopb.Event, persistence.WritePrecondition) {
		var events []*egopb.Event
		for _, idx := range rng.Perm(len(overlapSeq))[:3] {
			events = append(events, expEvent(fmt.Sprintf("ov-%d", idx), overlapSeq[idx].Add(1), 0, 0))
		}
		return events, persistence.Unconditional()
	}
}
