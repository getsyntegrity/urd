package eventstore_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests validate, on a real PostgreSQL, the properties the #332 xid-horizon design relies on. They run
// against a scratch table (spike_journal) with the position rule of the design, not against the store: the
// store does not implement the horizon yet. Everything is driven by held transactions and observable state;
// the only waiting is for a lock request to become visible in pg_locks.
//
// None of them is parallel, on purpose: the horizon is cluster-wide (pg_snapshot_xmin is the oldest in-flight
// xid of the whole server), so a transaction held open by one test would hold back the reads of every other test
// sharing the container, whatever database it uses. That is the operational cost of the design, observed.

// positionBase is 2^62: legacy rows carry position = timestamp (below it), new rows carry positionBase + xid8.
const positionBase = int64(1) << 62

const spikeSchema = `
CREATE TABLE spike_journal (
    persistence_id TEXT   NOT NULL,
    seq            BIGINT NOT NULL,
    pos            BIGINT NOT NULL,
    PRIMARY KEY (persistence_id, seq)
)`

// readSQL is one statement, hence one snapshot, for the horizon and for the rows: the same shape as the
// design's GetShardEvents. The batch is extended to the end of the group of rows sharing the position of the
// limit-th one, as #331 does for timestamps.
const readSQL = `
SELECT persistence_id, seq, pos FROM spike_journal
WHERE pos > $1 AND pos < $3::bigint + pg_snapshot_xmin(pg_current_snapshot())::text::bigint
  AND pos <= COALESCE((
      SELECT pos FROM spike_journal
      WHERE pos > $1 AND pos < $3::bigint + pg_snapshot_xmin(pg_current_snapshot())::text::bigint
      ORDER BY pos, persistence_id, seq
      OFFSET $2::bigint - 1 LIMIT 1), 9223372036854775807)
ORDER BY pos, persistence_id, seq`

const insertSQL = `INSERT INTO spike_journal (persistence_id, seq, pos)
VALUES ($1, $2, $3::bigint + pg_current_xact_id()::text::bigint) RETURNING pos`

func spikePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, shared.NewDatabase(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, spikeSchema); err != nil {
		t.Fatalf("create the scratch journal: %v", err)
	}
	return pool
}

func begin(t *testing.T, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func insert(t *testing.T, tx pgx.Tx, id string, seq int64) int64 {
	t.Helper()
	var pos int64
	if err := tx.QueryRow(context.Background(), insertSQL, id, seq, positionBase).Scan(&pos); err != nil {
		t.Fatalf("insert %s/%d: %v", id, seq, err)
	}
	return pos
}

func commit(t *testing.T, tx pgx.Tx) {
	t.Helper()
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func readPage(t *testing.T, pool *pgxpool.Pool, after, limit int64) (keys []string, next int64) {
	t.Helper()
	rows, err := pool.Query(context.Background(), readSQL, after, limit, positionBase)
	if err != nil {
		t.Fatalf("read after %d: %v", after, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id       string
			seq, pos int64
		)
		if err := rows.Scan(&id, &seq, &pos); err != nil {
			t.Fatalf("scan: %v", err)
		}
		keys = append(keys, fmt.Sprintf("%s/%d", id, seq))
		next = pos
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return keys, next
}

// drain pages exactly as the runner does and returns every key in delivery order plus the final cursor.
func drain(t *testing.T, pool *pgxpool.Pool, after, limit int64) ([]string, int64) {
	t.Helper()
	var all []string
	for page := 0; page < 100; page++ {
		keys, next := readPage(t, pool, after, limit)
		if len(keys) == 0 {
			return all, after
		}
		all = append(all, keys...)
		after = next
	}
	t.Fatalf("paging with limit %d did not terminate", limit)
	return nil, 0
}

func equal(t *testing.T, want, got []string, what string) {
	t.Helper()
	if !slices.Equal(want, got) {
		t.Fatalf("%s:\n  want %v\n  got  %v", what, want, got)
	}
}

// horizon is pg_snapshot_xmin as the reader's statement sees it.
func horizon(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var xmin int64
	if err := pool.QueryRow(context.Background(), `SELECT pg_snapshot_xmin(pg_current_snapshot())::text::bigint`).Scan(&xmin); err != nil {
		t.Fatalf("horizon: %v", err)
	}
	return xmin
}

// TestJournalHorizon_InvertedCommit is the #332 sequence: a later-stamped transaction commits first, the older
// one commits after the consumer has already advanced.
func TestJournalHorizon_InvertedCommit(t *testing.T) {
	pool := spikePool(t)

	// c is committed and consumed first; its position is the committed cursor.
	txC := begin(t, pool)
	insert(t, txC, "c", 1)
	commit(t, txC)
	got, cursor := readPage(t, pool, 0, 10)
	equal(t, []string{"c/1"}, got, "the first read")

	// a takes its xid before b, b commits before a: commit order is the inverse of position order.
	txA, txB := begin(t, pool), begin(t, pool)
	posA := insert(t, txA, "a", 1)
	posB := insert(t, txB, "b", 1)
	if posA >= posB {
		t.Fatalf("a must hold the lower position: a=%d b=%d", posA, posB)
	}
	commit(t, txB)

	// b is committed and visible to a plain SELECT, yet it must not be delivered: a is in flight and could still
	// publish a lower position. The horizon is exactly a's xid.
	got, _ = readPage(t, pool, cursor, 10)
	equal(t, nil, got, "b is committed but behind the horizon: nothing may be delivered")
	if h := positionBase + horizon(t, pool); h != posA {
		t.Fatalf("the horizon must stop at the oldest in-flight xid: horizon=%d a=%d", h, posA)
	}

	commit(t, txA)

	// the late event is now delivered, before b, and nothing was skipped
	got, next := readPage(t, pool, cursor, 10)
	equal(t, []string{"a/1", "b/1"}, got, "after a commits both are delivered in position order")
	if next != posB {
		t.Fatalf("the cursor must be b's position: got %d want %d", next, posB)
	}
	again, _ := readPage(t, pool, next, 10)
	equal(t, nil, again, "nothing is delivered twice")
}

func TestJournalHorizon_RollbackReleasesTheHorizon(t *testing.T) {
	pool := spikePool(t)

	txA, txB := begin(t, pool), begin(t, pool)
	insert(t, txA, "a", 1)
	insert(t, txB, "b", 1)
	commit(t, txB)

	got, _ := readPage(t, pool, 0, 10)
	equal(t, nil, got, "b waits while a is in flight")

	if err := txA.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	got, _ = readPage(t, pool, 0, 10)
	equal(t, []string{"b/1"}, got, "an aborted transaction leaves no hole: b is delivered at once")
}

// TestJournalHorizon_GroupsAreNeverCut: every event of one transaction shares one position, across entities.
func TestJournalHorizon_GroupsAreNeverCut(t *testing.T) {
	pool := spikePool(t)

	// three transactions committed one after another; the middle one writes three events over two entities
	tx1 := begin(t, pool)
	insert(t, tx1, "p", 1)
	commit(t, tx1)

	tx2 := begin(t, pool)
	pos := insert(t, tx2, "q", 1)
	for _, e := range []struct {
		id  string
		seq int64
	}{{"q", 2}, {"r", 1}} {
		if p := insert(t, tx2, e.id, e.seq); p != pos {
			t.Fatalf("events of one transaction must share a position: %d vs %d", p, pos)
		}
	}
	commit(t, tx2)

	tx3 := begin(t, pool)
	insert(t, tx3, "s", 1)
	commit(t, tx3)

	want := []string{"p/1", "q/1", "q/2", "r/1", "s/1"}
	for _, limit := range []int64{1, 2, 3, 4, 5, 100} {
		got, _ := drain(t, pool, 0, limit)
		equal(t, want, got, fmt.Sprintf("draining with limit %d delivers every event exactly once", limit))
	}

	// limit 0 reads nothing: the reader treats it as "no read" before the query, as GetShardEvents does
	// (OFFSET -1 would be an error in SQL, so the store must short-circuit; asserted here to pin that need)
	if _, err := pool.Exec(context.Background(), `SELECT 1 OFFSET -1`); err == nil {
		t.Fatalf("OFFSET -1 is expected to fail: the store must return early on limit 0")
	}
}

// TestJournalHorizon_RestartResumesFromTheCursor: nothing but the cursor survives a restart, even when the first
// page ends on a group and a late transaction commits while the consumer is down.
func TestJournalHorizon_RestartResumesFromTheCursor(t *testing.T) {
	pool := spikePool(t)

	txA := begin(t, pool)
	insert(t, txA, "a", 1) // in flight across the whole restart
	tx1 := begin(t, pool)
	insert(t, tx1, "x", 1)
	insert(t, tx1, "x", 2)
	commit(t, tx1)

	first, cursor := readPage(t, pool, 0, 1)
	equal(t, nil, first, "x is behind a's horizon")

	commit(t, txA)
	tx2 := begin(t, pool)
	insert(t, tx2, "y", 1)
	commit(t, tx2)

	// "restart": a fresh reader that only knows the cursor it committed (0)
	resumed, _ := drain(t, pool, cursor, 1)
	equal(t, []string{"a/1", "x/1", "x/2", "y/1"}, resumed, "resuming from the committed cursor recovers everything")

	// and a restart from the middle: commit the cursor after the first page, then resume
	page, mid := readPage(t, pool, 0, 1)
	equal(t, []string{"a/1"}, page, "first page")
	rest, _ := drain(t, pool, mid, 1)
	equal(t, []string{"x/1", "x/2", "y/1"}, rest, "resume after the first page: the group of x is not cut")
}

// TestJournalHorizon_LegacyRowsSortBelowNewOnes pins the migration transformation: a row backfilled with
// position = timestamp is read before, and never mixed with, rows positioned by xid.
func TestJournalHorizon_LegacyRowsSortBelowNewOnes(t *testing.T) {
	pool := spikePool(t)
	ctx := context.Background()

	// legacy rows: position = a UnixNano-sized timestamp, far below 2^62
	legacy := int64(1_780_000_000_000_000_000)
	if legacy >= positionBase {
		t.Fatalf("a current UnixNano must be below 2^62")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO spike_journal VALUES ('old', 1, $1), ('old', 2, $2)`, legacy, legacy+5); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}
	tx := begin(t, pool)
	insert(t, tx, "new", 1)
	commit(t, tx)

	got, _ := drain(t, pool, 0, 10)
	equal(t, []string{"old/1", "old/2", "new/1"}, got, "legacy rows first, then xid-positioned rows")

	// a legacy offset (a timestamp) resumes inside the legacy range and reaches every new row
	got, _ = drain(t, pool, legacy, 10)
	equal(t, []string{"old/2", "new/1"}, got, "an old offset keeps its meaning")
}

// TestJournalHorizon_PositionIsTheTopLevelXid: the position is xid8 of the main transaction, also when the
// insert runs inside a savepoint.
func TestJournalHorizon_PositionIsTheTopLevelXid(t *testing.T) {
	pool := spikePool(t)
	ctx := context.Background()

	tx := begin(t, pool)
	if _, err := tx.Exec(ctx, `SAVEPOINT s`); err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	pos := insert(t, tx, "sp", 1)
	var top int64
	if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text::bigint`).Scan(&top); err != nil {
		t.Fatalf("xid: %v", err)
	}
	if pos != positionBase+top {
		t.Fatalf("the position must be the top-level xid8: pos=%d xid=%d", pos, top)
	}
	commit(t, tx)
}

// waitBlocked returns once some session waits on an advisory lock: an observable state, not a guess.
func waitBlocked(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted`).Scan(&n); err != nil {
			t.Fatalf("pg_locks: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no session ever blocked on the advisory lock")
}

const lockEntity = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

// TestJournalHorizon_EntityOrder shows why the entity lock must precede the xid. A writer that already holds an
// xid (it wrote another entity first, as a multi-entity batch does) and then waits for the lock of E commits
// E/2 with a LOWER position than the E/1 that was committed while it waited. Taking every lock before the first
// write keeps position order equal to lock order.
func TestJournalHorizon_EntityOrder(t *testing.T) {
	run := func(t *testing.T, lockFirst bool) []string {
		pool := spikePool(t)
		ctx := context.Background()

		txA := begin(t, pool)
		if lockFirst {
			// A queues for E before anything else; it holds no xid while it waits
		} else {
			insert(t, txA, "other", 1) // A is assigned its xid BEFORE it asks for E's lock
		}

		// B owns E's lock and writes E/1, taking an xid after A's in the unsafe case
		txB := begin(t, pool)
		if _, err := txB.Exec(ctx, lockEntity, "E"); err != nil {
			t.Fatalf("B lock: %v", err)
		}
		insert(t, txB, "E", 1)

		done := make(chan error, 1)
		go func() {
			if _, err := txA.Exec(ctx, lockEntity, "E"); err != nil {
				done <- err
				return
			}
			if lockFirst {
				var assigned bool
				if err := txA.QueryRow(ctx, `SELECT pg_current_xact_id_if_assigned() IS NOT NULL`).Scan(&assigned); err != nil || assigned {
					done <- fmt.Errorf("a writer must hold no xid once it owns its locks (assigned=%v err=%v)", assigned, err)
					return
				}
				if err := txA.QueryRow(ctx, insertSQL, "other", 1, positionBase).Scan(new(int64)); err != nil {
					done <- err
					return
				}
			}
			if err := txA.QueryRow(ctx, insertSQL, "E", 2, positionBase).Scan(new(int64)); err != nil {
				done <- err
				return
			}
			done <- txA.Commit(ctx)
		}()

		waitBlocked(t, pool)
		commit(t, txB)
		if err := <-done; err != nil {
			t.Fatalf("A: %v", err)
		}
		all, _ := drain(t, pool, 0, 10)
		return all
	}

	t.Run("lock after xid breaks the entity order", func(t *testing.T) {
		got := run(t, false)
		if slices.Index(got, "E/2") > slices.Index(got, "E/1") {
			t.Fatalf("the hazard did not reproduce, E/1 came first: %v", got)
		}
	})
	t.Run("lock before xid keeps the entity order", func(t *testing.T) {
		got := run(t, true)
		if i2, i1 := slices.Index(got, "E/2"), slices.Index(got, "E/1"); i1 < 0 || i2 < 0 || i1 > i2 {
			t.Fatalf("E/1 must be delivered before E/2: %v", got)
		}
	})
}

// TestJournalHorizon_PositionBoundIsEnforced pins the limit of 2^62 + xid8 in a signed bigint: the largest xid8
// that fits succeeds, the next one fails loudly (22003) instead of wrapping, and the failing transaction stores
// nothing. The live cluster is far below the limit.
func TestJournalHorizon_PositionBoundIsEnforced(t *testing.T) {
	pool := spikePool(t)
	ctx := context.Background()

	const maxXid = positionBase - 1 // the largest xid8 for which positionBase + xid8 fits in int64

	var top int64
	if err := pool.QueryRow(ctx, `SELECT $1::bigint + $2::bigint`, positionBase, maxXid).Scan(&top); err != nil {
		t.Fatalf("the largest representable position must compute: %v", err)
	}
	if top != int64(^uint64(0)>>1) {
		t.Fatalf("the largest position must be the max int64: %d", top)
	}

	tx := begin(t, pool)
	_, err := tx.Exec(ctx, `INSERT INTO spike_journal VALUES ('over', 1, $1::bigint + $2::bigint)`, positionBase, maxXid+1)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "22003" {
		t.Fatalf("one above the limit must fail with 22003, got %v", err)
	}
	_ = tx.Rollback(ctx)
	if got, _ := drain(t, pool, 0, 10); len(got) != 0 {
		t.Fatalf("the overflowing write must store nothing: %v", got)
	}

	// the same bound applies to the read side: a horizon beyond the limit cannot be formed silently
	if _, err := pool.Exec(ctx, `SELECT $1::bigint + $2::bigint`, positionBase, maxXid+1); err == nil {
		t.Fatalf("a horizon beyond the limit must fail loudly")
	}

	if h := horizon(t, pool); h >= positionBase>>1 {
		t.Fatalf("the live xid8 %d is within half of the margin: the alert threshold is crossed", h)
	}
}
