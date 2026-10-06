package eventstore_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/getsyntegrity/urd/inttest/flows/eventstore/internal/measure"
	pginfra "github.com/getsyntegrity/urd/inttest/infra/postgres"
)

// TestJournalBench compares the two #332 prototypes under the same load. It is a measurement, not a correctness
// test, so it only runs when URD_JOURNAL_BENCH=1 (and is reported as skipped otherwise); the correctness suite
// is journal_compare_test.go and is always on.
//
// What is measured, per variant and scenario (the same code drives both through journalVariant):
//
//   - throughput: committed transactions and events per second of the writer fleet;
//   - write latency: begin to commit-returned of one writer transaction, lock waits included;
//   - lock wait: the part of that latency spent taking locks/positions (entity advisory locks for xid-horizon, the
//     position counter row for shard-serialization);
//   - eligibility delay: from the moment the writer's commit returned to the moment a reader that polls in a
//     tight loop (no sleep) was handed the event. It contains the reader's own query time; the idle empty-read
//     latency ("poll floor") is reported next to it so the two can be told apart. Waiting for the horizon shows
//     up here, and only here.
//
// The held transaction of scenarios 3-5 is PART OF THE EXPERIMENTAL LOAD, not an incident: it holds a
// transaction open for URD_JOURNAL_BENCH_HOLD_MS, pauses URD_JOURNAL_BENCH_GAP_MS, and repeats until the writers
// stop. Every run ends by draining the reader and checking the guarantees of the correctness suite on the
// measured traffic: nothing skipped, nothing duplicated, per-entity order kept. A violation fails the run, so a
// number is never reported for a run that lost an event.
//
// It starts its own container (not the package's shared one, which runs fsync=off) so the durability of the
// commit is a stated parameter: URD_JOURNAL_BENCH_FSYNC=on|off.
func TestJournalBench(t *testing.T) {
	if os.Getenv("URD_JOURNAL_BENCH") != "1" {
		t.Skip("measurement, not a correctness test: set URD_JOURNAL_BENCH=1 to run it")
	}
	cfg := benchConfig{
		writers:     envInt("URD_JOURNAL_BENCH_WRITERS", 16),
		eventsPerTx: envInt("URD_JOURNAL_BENCH_EVENTS", 3),
		duration:    time.Duration(envInt("URD_JOURNAL_BENCH_SECONDS", 5)) * time.Second,
		reps:        envInt("URD_JOURNAL_BENCH_REPS", 3),
		hold:        time.Duration(envInt("URD_JOURNAL_BENCH_HOLD_MS", 500)) * time.Millisecond,
		gap:         time.Duration(envInt("URD_JOURNAL_BENCH_GAP_MS", 100)) * time.Millisecond,
		fsync:       envStr("URD_JOURNAL_BENCH_FSYNC", "on"),
		pollLimit:   100,
	}

	ctx := context.Background()
	box, err := startBenchPostgres(ctx, cfg.fsync)
	if err != nil {
		t.Fatalf("start the benchmark container: %v", err)
	}
	t.Cleanup(func() { _ = box.terminate(ctx) })
	t.Logf("BENCH|config|%s|writers=%d events/tx=%d duration=%s reps=%d hold=%s gap=%s poll-limit=%d goos/arch=%s/%s host-cpus=%d",
		box.settings(ctx, t), cfg.writers, cfg.eventsPerTx, cfg.duration, cfg.reps, cfg.hold, cfg.gap, cfg.pollLimit, runtime.GOOS, runtime.GOARCH, runtime.NumCPU())

	scenarios := []benchScenario{
		{name: "1 hot shard", shards: 1},
		{name: "2 eight independent shards", shards: 8},
		{name: "3 held tx, same shard", shards: 1, holder: holderSameShard},
		{name: "4 held tx, other shard", shards: 1, holder: holderOtherShard},
		{name: "5 held tx, other database", shards: 1, holder: holderOtherDatabase},
		// 6-7 emulate a slower commit (synchronous replication, slower storage) by parking each writer transaction
		// for commitDelay right before its commit, inside its locks. The delay is part of the load, and it is the
		// same for both variants.
		{name: "6 hot shard, commit +5ms", shards: 1, commitDelay: 5 * time.Millisecond},
		{name: "7 eight shards, commit +5ms", shards: 8, commitDelay: 5 * time.Millisecond},
	}
	if only := os.Getenv("URD_JOURNAL_BENCH_SCENARIOS"); only != "" {
		var kept []benchScenario
		for _, sc := range scenarios {
			for _, prefix := range strings.Split(only, ",") {
				if strings.HasPrefix(sc.name, strings.TrimSpace(prefix)) {
					kept = append(kept, sc)
					break
				}
			}
		}
		scenarios = kept
	}
	for _, v := range journalVariants {
		floor := benchPollFloor(ctx, t, box, v.journalVariant)
		t.Logf("BENCH|poll-floor|%s|idle empty read p50=%s p95=%s", v.name(), floor.P50, floor.P95)
		for _, sc := range scenarios {
			var runs []benchRun
			for rep := 1; rep <= cfg.reps; rep++ {
				runs = append(runs, benchOnce(ctx, t, box, v.journalVariant, sc, cfg))
			}
			reportBench(t, v.name(), sc, runs)
		}
	}
}

type benchConfig struct {
	writers, eventsPerTx int
	duration, hold, gap  time.Duration
	reps                 int
	fsync                string
	pollLimit            int64
}

type holderKind int

const (
	holderNone holderKind = iota
	holderSameShard
	holderOtherShard
	holderOtherDatabase
)

type benchScenario struct {
	name   string
	shards int
	holder holderKind
	// commitDelay parks every writer transaction right before its commit (see scenarios 6-7).
	commitDelay time.Duration
}

type commitRec struct {
	key   string
	shard int64
	at    time.Time
}

type deliverRec struct {
	key string
	id  string
	seq int64
	at  time.Time
}

type benchRun struct {
	txs, events, holderTxs int
	elapsed                time.Duration
	writeLat, lockWait     []time.Duration
	eligibility            []time.Duration
}

func benchOnce(ctx context.Context, t *testing.T, box *benchPostgres, v journalVariant, sc benchScenario, cfg benchConfig) benchRun {
	t.Helper()
	dsn := box.newDatabase(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := v.install(ctx, pool); err != nil {
		t.Fatalf("install %s: %v", v.name(), err)
	}
	var otherDB *pgxpool.Pool
	if sc.holder == holderOtherDatabase {
		if otherDB, err = pgxpool.New(ctx, box.newDatabase(t)); err != nil {
			t.Fatalf("connect to the other database: %v", err)
		}
		defer otherDB.Close()
	}

	const tenant = "t"
	var (
		stopWriters, stopHolder, allWritten atomic.Bool
		expected                            = make([]int, sc.shards)
		start                               = time.Now()
		failure                             atomic.Pointer[error]
	)
	fail := func(err error) { failure.CompareAndSwap(nil, &err) }

	// writers: one entity each, on shard (i mod shards); every transaction writes eventsPerTx consecutive events
	type writerOut struct {
		lat, lock []time.Duration
		commits   []commitRec
		last      time.Time
	}
	outs := make([]*writerOut, cfg.writers)
	var writers sync.WaitGroup
	for i := range cfg.writers {
		out := &writerOut{}
		outs[i] = out
		writers.Add(1)
		go func() {
			defer writers.Done()
			shard, id, seq := int64(i%sc.shards), fmt.Sprintf("w%d", i), int64(0)
			for !stopWriters.Load() {
				events := make([]jEvent, cfg.eventsPerTx)
				for k := range events {
					seq++
					events[k] = ev(tenant, shard, id, seq)
				}
				t0 := time.Now()
				var park func() bool
				if sc.commitDelay > 0 {
					park = func() bool { time.Sleep(sc.commitDelay); return true }
				}
				lock, err := v.write(ctx, pool, events, park)
				t1 := time.Now()
				if err != nil {
					fail(fmt.Errorf("writer %d: %w", i, err))
					return
				}
				out.lat = append(out.lat, t1.Sub(t0))
				out.lock = append(out.lock, lock)
				for _, e := range events {
					out.commits = append(out.commits, commitRec{key: e.key(), shard: shard, at: t1})
				}
				out.last = t1
			}
		}()
	}

	// holder: part of the experimental load, see the doc comment of TestJournalBench
	var (
		holderWG      sync.WaitGroup
		holderCommits []commitRec
		holderTxs     int
	)
	if sc.holder != holderNone {
		holderWG.Add(1)
		go func() {
			defer holderWG.Done()
			seq := int64(0)
			for !stopHolder.Load() {
				switch sc.holder {
				case holderSameShard, holderOtherShard:
					shard := int64(0)
					if sc.holder == holderOtherShard {
						shard = 99
					}
					seq++
					e := ev(tenant, shard, "holder", seq)
					if _, err := v.write(ctx, pool, []jEvent{e}, func() bool { time.Sleep(cfg.hold); return true }); err != nil {
						fail(fmt.Errorf("holder: %w", err))
						return
					}
					if shard == 0 {
						holderCommits = append(holderCommits, commitRec{key: e.key(), shard: 0, at: time.Now()})
					}
				case holderOtherDatabase:
					tx, err := otherDB.Begin(ctx)
					if err != nil {
						fail(err)
						return
					}
					if _, err := tx.Exec(ctx, `SELECT pg_current_xact_id()`); err != nil {
						fail(err)
						return
					}
					time.Sleep(cfg.hold)
					if err := tx.Commit(ctx); err != nil {
						fail(err)
						return
					}
				}
				holderTxs++
				if !stopHolder.Load() {
					time.Sleep(cfg.gap)
				}
			}
		}()
	}

	// readers: one per measured shard, tight polling, run until every committed event was delivered
	delivered := make([][]deliverRec, sc.shards)
	var readers sync.WaitGroup
	readCtx, cancelRead := context.WithTimeout(ctx, cfg.duration+cfg.hold+60*time.Second)
	defer cancelRead()
	for shard := range sc.shards {
		readers.Add(1)
		go func() {
			defer readers.Done()
			var after int64
			for {
				rs, next, err := v.read(readCtx, pool, tenant, int64(shard), after, cfg.pollLimit)
				if err != nil {
					fail(fmt.Errorf("reader of shard %d (events never became eligible?): %w", shard, err))
					return
				}
				if len(rs) > 0 {
					now := time.Now()
					for _, r := range rs {
						delivered[shard] = append(delivered[shard], deliverRec{key: r.key(), id: r.ID, seq: r.Seq, at: now})
					}
					after = next
				}
				if allWritten.Load() && len(delivered[shard]) >= expected[shard] {
					return
				}
			}
		}()
	}

	time.Sleep(cfg.duration) // the run length is the load; nothing here waits for a race
	stopWriters.Store(true)
	writers.Wait()
	stopHolder.Store(true)
	holderWG.Wait()

	commits := map[string]time.Time{}
	var run benchRun
	var lastWrite time.Time
	for _, o := range outs {
		run.writeLat = append(run.writeLat, o.lat...)
		run.lockWait = append(run.lockWait, o.lock...)
		run.txs += len(o.lat)
		run.events += len(o.commits)
		if o.last.After(lastWrite) {
			lastWrite = o.last
		}
		for _, c := range o.commits {
			commits[c.key] = c.at
			expected[c.shard]++
		}
	}
	for _, c := range holderCommits {
		commits[c.key] = c.at
		expected[c.shard]++
	}
	run.holderTxs = holderTxs
	run.elapsed = lastWrite.Sub(start)
	allWritten.Store(true)
	readers.Wait()

	if p := failure.Load(); p != nil {
		t.Fatalf("%s / %s: %v", v.name(), sc.name, *p)
	}

	// the guarantees, on the measured traffic itself
	seen := map[string]bool{}
	for shard, recs := range delivered {
		lastSeq := map[string]int64{}
		for _, r := range recs {
			if seen[r.key] {
				t.Fatalf("%s / %s: %s delivered twice", v.name(), sc.name, r.key)
			}
			seen[r.key] = true
			if r.id != "holder" && r.seq != lastSeq[r.id]+1 {
				t.Fatalf("%s / %s: shard %d entity %s delivered seq %d after %d", v.name(), sc.name, shard, r.id, r.seq, lastSeq[r.id])
			}
			lastSeq[r.id] = r.seq
			if at, ok := commits[r.key]; ok {
				run.eligibility = append(run.eligibility, max(0, r.at.Sub(at)))
			}
		}
	}
	if len(seen) != len(commits) {
		t.Fatalf("%s / %s: %d events committed, %d delivered", v.name(), sc.name, len(commits), len(seen))
	}
	return run
}

func benchPollFloor(ctx context.Context, t *testing.T, box *benchPostgres, v journalVariant) measure.Summary {
	t.Helper()
	pool, err := pgxpool.New(ctx, box.newDatabase(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := v.install(ctx, pool); err != nil {
		t.Fatalf("install: %v", err)
	}
	var samples []time.Duration
	for range 300 {
		t0 := time.Now()
		if _, _, err := v.read(ctx, pool, "t", 0, 0, 100); err != nil {
			t.Fatalf("read: %v", err)
		}
		samples = append(samples, time.Since(t0))
	}
	return measure.Summarize(samples)
}

func reportBench(t *testing.T, variant string, sc benchScenario, runs []benchRun) {
	t.Helper()
	var lat, lock, elig []time.Duration
	var txps, evps []float64
	holder := 0
	for _, r := range runs {
		lat = append(lat, r.writeLat...)
		lock = append(lock, r.lockWait...)
		elig = append(elig, r.eligibility...)
		txps = append(txps, float64(r.txs)/r.elapsed.Seconds())
		evps = append(evps, float64(r.events)/r.elapsed.Seconds())
		holder += r.holderTxs
	}
	mean := func(xs []float64) float64 {
		var sum float64
		for _, x := range xs {
			sum += x
		}
		return sum / float64(len(xs))
	}
	l, k, e := measure.Summarize(lat), measure.Summarize(lock), measure.Summarize(elig)
	t.Logf("BENCH|row|%s|%s|tx/s=%.0f (%.0f..%.0f)|ev/s=%.0f|write p50/p95/p99=%s/%s/%s|lock-wait p50/p95/p99=%s/%s/%s|eligibility p50/p95/p99=%s/%s/%s|writes=%d held-tx=%d",
		variant, sc.name, mean(txps), slices.Min(txps), slices.Max(txps), mean(evps),
		l.P50, l.P95, l.P99, k.P50, k.P95, k.P99, e.P50, e.P95, e.P99, l.N, holder)
}

// ---- own container, with durability as a parameter

type benchPostgres struct {
	container *tcpostgres.PostgresContainer
	baseDSN   *url.URL
}

func startBenchPostgres(ctx context.Context, fsync string) (*benchPostgres, error) {
	c, err := tcpostgres.Run(ctx, pginfra.PostgresImage,
		tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"),
		testcontainers.WithCmdArgs("-c", "max_connections=300", "-c", "fsync="+fsync),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		if c != nil {
			_ = testcontainers.TerminateContainer(c)
		}
		return nil, err
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = testcontainers.TerminateContainer(c)
		return nil, err
	}
	base, err := url.Parse(dsn)
	if err != nil {
		_ = testcontainers.TerminateContainer(c)
		return nil, err
	}
	return &benchPostgres{container: c, baseDSN: base}, nil
}

func (b *benchPostgres) terminate(ctx context.Context) error { return b.container.Terminate(ctx) }

func (b *benchPostgres) settings(ctx context.Context, t *testing.T) string {
	t.Helper()
	conn, err := pgx.Connect(ctx, b.baseDSN.String())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx) //nolint:errcheck
	var version, fsync, syncCommit, buffers, maxConns string
	for q, dst := range map[string]*string{
		`SELECT split_part(version(), ' on ', 1)`: &version, `SHOW fsync`: &fsync, `SHOW synchronous_commit`: &syncCommit,
		`SHOW shared_buffers`: &buffers, `SHOW max_connections`: &maxConns,
	} {
		if err := conn.QueryRow(ctx, q).Scan(dst); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return fmt.Sprintf("%s fsync=%s synchronous_commit=%s shared_buffers=%s max_connections=%s", version, fsync, syncCommit, buffers, maxConns)
}

// newDatabase creates an empty database and drops it when the test ends.
func (b *benchPostgres) newDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("name: %v", err)
	}
	name := "b_" + hex.EncodeToString(suffix)
	admin := func(stmt string) error {
		conn, err := pgx.Connect(ctx, b.baseDSN.String())
		if err != nil {
			return err
		}
		defer conn.Close(ctx) //nolint:errcheck
		_, err = conn.Exec(ctx, stmt)
		return err
	}
	if err := admin(`CREATE DATABASE ` + pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _ = admin(`DROP DATABASE IF EXISTS ` + pgx.Identifier{name}.Sanitize() + ` WITH (FORCE)`) })
	dsn := *b.baseDSN
	dsn.Path = "/" + name
	return dsn.String()
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func envStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
