package eventstore_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The same correctness suite runs on both #332 prototypes. Transactions are coordinated explicitly: a held writer
// parks inside its transaction on a channel, and the test learns that another writer is blocked (or finished) by
// observing pg_locks and the writer's own completion. The only polling is that observation of database state, at
// a 1 ms interval and bounded; nothing waits for "long enough".
//
// None of these tests is parallel: the horizon of the xid scheme is cluster-wide, so a transaction held by one
// test would hold back the reads of any other test on the same server.

var journalVariants = []struct {
	journalVariant
	// serializesWriters: a writer of the same (scope, shard) blocks while another one is in flight.
	serializesWriters bool
	// coupledReads: a transaction in flight on another scope/shard delays what a reader of this one may see.
	coupledReads bool
}{
	{journalVariant: variantSharded{}, serializesWriters: true, coupledReads: false},
	{journalVariant: variantHorizon{}, serializesWriters: false, coupledReads: true},
}

const stepTimeout = 20 * time.Second

type journalEnv struct {
	t    *testing.T
	v    journalVariant
	dsn  string
	pool *pgxpool.Pool
}

func newJournalEnv(t *testing.T, v journalVariant) *journalEnv {
	t.Helper()
	dsn := shared.NewDatabase(t)
	pool := openJournalPool(t, dsn)
	if err := v.install(context.Background(), pool); err != nil {
		t.Fatalf("install %s: %v", v.name(), err)
	}
	return &journalEnv{t: t, v: v, dsn: dsn, pool: pool}
}

func openJournalPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func (e *journalEnv) write(events ...jEvent) {
	e.t.Helper()
	if _, err := e.v.write(context.Background(), e.pool, events, nil); err != nil {
		e.t.Fatalf("write %v: %v", events, err)
	}
}

func (e *journalEnv) read(tenant string, shard, after, limit int64) ([]jRead, int64) {
	e.t.Helper()
	rs, next, err := e.v.read(context.Background(), e.pool, tenant, shard, after, limit)
	if err != nil {
		e.t.Fatalf("read (%s, %d) after %d: %v", tenant, shard, after, err)
	}
	if len(rs) == 0 {
		next = after
	}
	return rs, next
}

// drain pages as the runner does and checks the cursor contract on every page: positions only grow, every event
// is strictly after the previous cursor (a group is never delivered again), and the cursor is the last position.
func (e *journalEnv) drain(tenant string, shard, after, limit int64) ([]jRead, int64) {
	e.t.Helper()
	var all []jRead
	for page := 0; page < 1000; page++ {
		rs, next := e.read(tenant, shard, after, limit)
		if len(rs) == 0 {
			return all, after
		}
		for _, r := range rs {
			if r.Pos <= after {
				e.t.Fatalf("limit %d: %s at position %d delivered after cursor %d", limit, r.key(), r.Pos, after)
			}
		}
		if next != rs[len(rs)-1].Pos {
			e.t.Fatalf("the cursor must be the position of the last event")
		}
		all = append(all, rs...)
		after = next
	}
	e.t.Fatalf("paging with limit %d did not terminate", limit)
	return nil, 0
}

// held is a transaction parked right before its commit.
type held struct {
	release chan bool
	done    chan error
}

func (e *journalEnv) hold(events ...jEvent) *held {
	e.t.Helper()
	h := &held{release: make(chan bool, 1), done: make(chan error, 1)}
	entered := make(chan struct{})
	go func() {
		_, err := e.v.write(context.Background(), e.pool, events, func() bool {
			close(entered)
			return <-h.release
		})
		h.done <- err
	}()
	select {
	case <-entered:
	case err := <-h.done:
		e.t.Fatalf("the held transaction ended before it was parked: %v", err)
	case <-time.After(stepTimeout):
		e.t.Fatalf("the held transaction was never parked")
	}
	return h
}

func (e *journalEnv) finish(h *held, commit bool) error {
	e.t.Helper()
	h.release <- commit
	select {
	case err := <-h.done:
		return err
	case <-time.After(stepTimeout):
		e.t.Fatalf("the held transaction did not finish")
		return nil
	}
}

// pendingWrite is a write running on its own goroutine. done closes once, so it can be observed any number of
// times (settle) before it is awaited.
type pendingWrite struct {
	done chan struct{}
	err  error
}

func (e *journalEnv) startWrite(events ...jEvent) *pendingWrite {
	p := &pendingWrite{done: make(chan struct{})}
	go func() {
		_, p.err = e.v.write(context.Background(), e.pool, events, nil)
		close(p.done)
	}()
	return p
}

// settle returns once the write finished or some session waits on a lock; it reports which. Both are observable
// facts, so nothing guesses how long a blocked writer would take.
func (e *journalEnv) settle(p *pendingWrite) (finished bool) {
	e.t.Helper()
	deadline := time.Now().Add(stepTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			if p.err != nil {
				e.t.Fatalf("the concurrent write failed: %v", p.err)
			}
			return true
		default:
		}
		var waiting int
		if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE NOT granted`).Scan(&waiting); err != nil {
			e.t.Fatalf("pg_locks: %v", err)
		}
		if waiting > 0 {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	e.t.Fatalf("the concurrent write neither finished nor blocked")
	return false
}

func (e *journalEnv) await(p *pendingWrite) {
	e.t.Helper()
	select {
	case <-p.done:
		if p.err != nil {
			e.t.Fatalf("write: %v", p.err)
		}
	case <-time.After(stepTimeout):
		e.t.Fatalf("write did not finish")
	}
}

func ev(tenant string, shard int64, id string, seq int64) jEvent {
	return jEvent{Tenant: tenant, Shard: shard, ID: id, Seq: seq}
}

func wantKeys(t *testing.T, want []string, got []jRead, what string) {
	t.Helper()
	if !slices.Equal(want, readKeys(got)) {
		t.Fatalf("%s:\n  want %s\n  got  %s", what, joinKeys(want), joinKeys(readKeys(got)))
	}
}

// TestJournalCompare_ReaderNeverAdvancesPastAnInFlightWrite is the #332 sequence in both schemes: a writer that
// may still commit with a lower position is in flight, another writer of a different entity of the same shard
// arrives, and the reader must not be given anything above the in-flight position.
func TestJournalCompare_ReaderNeverAdvancesPastAnInFlightWrite(t *testing.T) {
	for _, tc := range journalVariants {
		for _, commit := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/holder-commits=%v", tc.name(), commit), func(t *testing.T) {
				e := newJournalEnv(t, tc.journalVariant)
				e.write(ev("t", 1, "seed", 1))
				_, cursor := e.read("t", 1, 0, 10)
				if cursor == 0 {
					t.Fatalf("the seed must be delivered")
				}

				h := e.hold(ev("t", 1, "holder", 1))
				late := e.startWrite(ev("t", 1, "late", 1))
				committedWhileHeld := e.settle(late)
				if committedWhileHeld == tc.serializesWriters {
					t.Fatalf("committedWhileHeld=%v but serializesWriters=%v", committedWhileHeld, tc.serializesWriters)
				}

				got, _ := e.read("t", 1, cursor, 10)
				wantKeys(t, nil, got, "nothing above an in-flight write may be delivered")

				err := e.finish(h, commit)
				if commit && err != nil || !commit && !errors.Is(err, errRolledBack) {
					t.Fatalf("holder outcome: %v", err)
				}
				e.await(late)

				got, next := e.drain("t", 1, cursor, 10)
				if commit {
					wantKeys(t, []string{"t/holder/1", "t/late/1"}, got, "the holder's lower position is delivered first, nothing skipped")
				} else {
					wantKeys(t, []string{"t/late/1"}, got, "a rolled-back write leaves no hole to wait for")
				}
				again, _ := e.read("t", 1, next, 10)
				wantKeys(t, nil, again, "nothing is delivered twice")
			})
		}
	}
}

// TestJournalCompare_GroupsAreNeverCut: one transaction's events on a shard share one position (several entities)
// and no limit cuts the group.
func TestJournalCompare_GroupsAreNeverCut(t *testing.T) {
	for _, tc := range journalVariants {
		t.Run(tc.name(), func(t *testing.T) {
			e := newJournalEnv(t, tc.journalVariant)
			e.write(ev("t", 1, "p", 1), ev("t", 1, "p", 2), ev("t", 1, "p", 3))
			e.write(ev("t", 1, "q", 1), ev("t", 1, "r", 1))
			e.write(ev("t", 1, "s", 1))

			want := []string{"t/p/1", "t/p/2", "t/p/3", "t/q/1", "t/r/1", "t/s/1"}
			for _, limit := range []int64{1, 2, 3, 4, 5, 6, 100} {
				got, _ := e.drain("t", 1, 0, limit)
				wantKeys(t, want, got, fmt.Sprintf("limit %d delivers every event exactly once", limit))
				pos := map[string]int64{}
				for _, r := range got {
					pos[r.ID] = r.Pos
				}
				if pos["p"] == 0 || pos["q"] != pos["r"] || pos["p"] >= pos["q"] || pos["q"] >= pos["s"] {
					t.Fatalf("groups must share a position and be ordered: %v", pos)
				}
			}
		})
	}
}

// TestJournalCompare_RestartResumesFromTheCommittedCursor: a new pool (a restarted process) writes after the
// restart, the position keeps growing, and a reader that only knows its committed cursor loses nothing.
func TestJournalCompare_RestartResumesFromTheCommittedCursor(t *testing.T) {
	for _, tc := range journalVariants {
		t.Run(tc.name(), func(t *testing.T) {
			e := newJournalEnv(t, tc.journalVariant)
			e.write(ev("t", 1, "a", 1), ev("t", 1, "a", 2))
			e.write(ev("t", 1, "b", 1))
			page, cursor := e.read("t", 1, 0, 1)
			wantKeys(t, []string{"t/a/1", "t/a/2"}, page, "the first page ends on a group boundary")
			before, _ := e.drain("t", 1, 0, 100)
			last := before[len(before)-1].Pos

			// "restart": every connection of the first process is gone
			e.pool.Close()
			e.pool = openJournalPool(t, e.dsn)
			e.write(ev("t", 1, "c", 1))

			rest, _ := e.drain("t", 1, cursor, 1)
			wantKeys(t, []string{"t/b/1", "t/c/1"}, rest, "resuming from the committed cursor recovers the rest")
			if rest[len(rest)-1].Pos <= last {
				t.Fatalf("a write after the restart must land above everything delivered: %d <= %d", rest[len(rest)-1].Pos, last)
			}
		})
	}
}

// TestJournalCompare_ScopeAndShardIsolation: data never crosses scopes, and what a transaction in flight on one
// (scope, shard) does to another (scope, shard) is the observable difference between the schemes.
func TestJournalCompare_ScopeAndShardIsolation(t *testing.T) {
	for _, tc := range journalVariants {
		t.Run(tc.name(), func(t *testing.T) {
			e := newJournalEnv(t, tc.journalVariant)
			h := e.hold(ev("a", 1, "held", 1))

			other := e.startWrite(ev("b", 1, "other-tenant", 1))
			e.await(other) // a writer of another scope is never blocked, in either scheme
			sameTenantOtherShard := e.startWrite(ev("a", 2, "other-shard", 1))
			e.await(sameTenantOtherShard)

			gotB, _ := e.read("b", 1, 0, 10)
			gotA2, _ := e.read("a", 2, 0, 10)
			if tc.coupledReads {
				wantKeys(t, nil, gotB, "the horizon is cluster-wide: tenant b waits for tenant a's transaction")
				wantKeys(t, nil, gotA2, "the horizon is cluster-wide: shard 2 waits for shard 1's transaction")
			} else {
				wantKeys(t, []string{"b/other-tenant/1"}, gotB, "serialization is per (scope, shard): tenant b reads at once")
				wantKeys(t, []string{"a/other-shard/1"}, gotA2, "serialization is per (scope, shard): shard 2 reads at once")
			}
			gotA1, _ := e.read("a", 1, 0, 10)
			wantKeys(t, nil, gotA1, "nothing of the in-flight transaction is visible")

			if err := e.finish(h, true); err != nil {
				t.Fatalf("holder: %v", err)
			}
			gotB, _ = e.drain("b", 1, 0, 10)
			wantKeys(t, []string{"b/other-tenant/1"}, gotB, "tenant b sees only its own event, never tenant a's")
			gotA1, _ = e.drain("a", 1, 0, 10)
			wantKeys(t, []string{"a/held/1"}, gotA1, "tenant a sees only its own event")
		})
	}
}

// TestJournalCompare_MultiShardWritesDoNotDeadlock: writers that span the same shards and list them in opposite
// order finish, in both schemes, because the locks are taken in a stable order.
func TestJournalCompare_MultiShardWritesDoNotDeadlock(t *testing.T) {
	for _, tc := range journalVariants {
		t.Run(tc.name(), func(t *testing.T) {
			e := newJournalEnv(t, tc.journalVariant)
			const perWriter = 40
			var wg sync.WaitGroup
			errs := make(chan error, 2*perWriter)
			for w, shards := range [][2]int64{{1, 2}, {2, 1}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 1; i <= perWriter; i++ {
						id := fmt.Sprintf("w%d", w)
						_, err := e.v.write(context.Background(), e.pool, []jEvent{
							ev("t", shards[0], id+"-x", int64(i)), ev("t", shards[1], id+"-y", int64(i)),
						}, nil)
						if err != nil {
							errs <- err
							return
						}
					}
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatalf("a multi-shard write failed: %v", err)
			}
			for _, shard := range []int64{1, 2} {
				got, _ := e.drain("t", shard, 0, 7)
				if len(got) != 2*perWriter {
					t.Fatalf("shard %d: want %d events, got %d", shard, 2*perWriter, len(got))
				}
			}
		})
	}
}

// TestJournalSharded_UnsortedLockOrderDeadlocks is the negative control of the previous test: the same two
// writers taking their shards in opposite order, with both holding their first shard before asking for the
// second, deadlock. It shows the stable order is what prevents it, not luck.
func TestJournalSharded_UnsortedLockOrderDeadlocks(t *testing.T) {
	var firstTaken sync.WaitGroup
	firstTaken.Add(2)
	barrier := func(n int) {
		if n != 0 {
			return
		}
		firstTaken.Done()
		firstTaken.Wait() // both writers hold their first shard before either asks for the second
	}
	e := newJournalEnv(t, variantSharded{})

	results := make(chan error, 2)
	for _, order := range []func([]shardKey) []shardKey{
		func(ks []shardKey) []shardKey { return ks },                     // ascending
		func(ks []shardKey) []shardKey { slices.Reverse(ks); return ks }, // descending
	} {
		v := variantSharded{order: order, afterFirstLock: barrier}
		go func() {
			_, err := v.write(context.Background(), e.pool, []jEvent{ev("t", 1, "x", 1), ev("t", 2, "y", 1)}, nil)
			results <- err
		}()
	}
	var deadlocks int
	for range 2 {
		select {
		case err := <-results:
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
				deadlocks++
			}
		case <-time.After(stepTimeout):
			t.Fatalf("neither writer finished: the database did not break the deadlock")
		}
	}
	if deadlocks != 1 {
		t.Fatalf("exactly one of the two writers must be chosen as the deadlock victim, got %d", deadlocks)
	}
}

// TestJournalSharded_PositionsAreDenseAcrossProcessesAndRollbacks: two independent pools (two processes) write
// one shard concurrently, some transactions roll back; the committed positions are exactly 1..N. The counter is
// in the database, so a rollback gives its position to the next writer and no process keeps a local one.
func TestJournalSharded_PositionsAreDenseAcrossProcessesAndRollbacks(t *testing.T) {
	e := newJournalEnv(t, variantSharded{})
	processes := []*pgxpool.Pool{e.pool, openJournalPool(t, e.dsn)}

	const writersPerProcess, txPerWriter = 4, 40
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		committed int
		failure   error
	)
	for p, pool := range processes {
		for w := range writersPerProcess {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := fmt.Sprintf("p%dw%d", p, w)
				seq := int64(0)
				for i := 1; i <= txPerWriter; i++ {
					rollback := i%5 == 0
					_, err := e.v.write(context.Background(), pool, []jEvent{ev("t", 1, id, seq+1), ev("t", 1, id, seq+2)}, func() bool { return !rollback })
					mu.Lock()
					switch {
					case rollback && errors.Is(err, errRolledBack):
					case err == nil && !rollback:
						committed++
						seq += 2
					default:
						failure = errors.Join(failure, err)
					}
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	if failure != nil {
		t.Fatalf("unexpected write outcome: %v", failure)
	}

	rows, err := e.pool.Query(context.Background(), `SELECT DISTINCT pos FROM cmp_a WHERE tenant_id='t' AND shard=1 ORDER BY pos`)
	if err != nil {
		t.Fatalf("positions: %v", err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var pos int64
		if err := rows.Scan(&pos); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, pos)
	}
	if len(got) != committed {
		t.Fatalf("every committed transaction must own one distinct position: %d positions, %d transactions", len(got), committed)
	}
	for i, pos := range got {
		if pos != int64(i+1) {
			t.Fatalf("positions must be dense 1..%d (rollbacks hand theirs back): position %d is %d", committed, i+1, pos)
		}
	}
	var last int64
	if err := e.pool.QueryRow(context.Background(), `SELECT last FROM cmp_a_pos WHERE tenant_id='t' AND shard=1`).Scan(&last); err != nil || last != int64(committed) {
		t.Fatalf("the persistent counter must equal the committed transactions: last=%d committed=%d err=%v", last, committed, err)
	}
}
