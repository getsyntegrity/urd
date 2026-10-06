package eventstore_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/getsyntegrity/urd/inttest/flows/eventstore/internal/measure"
)

// The two prototypes of the #332 journal cursor, written against one interface so the correctness suite
// (journal_compare_test.go) and the measurements (journal_bench_test.go) run the same code on both.
//
// They are PROTOTYPES on scratch tables (cmp_a*, cmp_b). They do not touch persistence/postgres and a green run
// here does not show that the production store is corrected; that needs the same checks against the adapter.

// jEvent is one event of a prototype write: the scope, the shard it is read from, and its entity.
type jEvent struct {
	Tenant string
	Shard  int64
	ID     string
	Seq    int64
}

func (e jEvent) key() string { return fmt.Sprintf("%s/%s/%d", e.Tenant, e.ID, e.Seq) }

// jRead is one event a prototype read returned.
type jRead struct {
	Tenant string
	ID     string
	Seq    int64
	Pos    int64
}

func (r jRead) key() string { return fmt.Sprintf("%s/%s/%d", r.Tenant, r.ID, r.Seq) }

// errRolledBack is what write returns when beforeCommit chose to roll back.
var errRolledBack = errors.New("the transaction was rolled back on request")

// journalVariant is one position scheme.
type journalVariant interface {
	name() string
	install(ctx context.Context, pool *pgxpool.Pool) error
	// write runs one transaction holding events. beforeCommit, when not nil, runs after every lock and position
	// is taken and every row is inserted, immediately before the commit; it blocks the caller until the test (or
	// the load) lets it go, and its result decides commit (true) or rollback (false). lockWait is how long taking
	// the locks (and, for the shard scheme, the positions) took.
	write(ctx context.Context, pool *pgxpool.Pool, events []jEvent, beforeCommit func() bool) (lockWait time.Duration, err error)
	// read returns the next events of (tenant, shard) strictly after position `after`, never cutting a group of
	// events that share a position, and the position to pass to the next call. limit must be > 0.
	read(ctx context.Context, pool *pgxpool.Pool, tenant string, shard, after, limit int64) ([]jRead, int64, error)
}

// ---------------------------------------------------------------------------------------------------------
// B: xid8 position + xmin horizon

// variantHorizon positions every row at 2^62 + the top-level xid8 and reads only below the xmin of the reading
// statement's own snapshot. Writers take an advisory lock per entity, sorted, before the first statement that can
// assign an xid, so xid order equals lock order for one entity.
type variantHorizon struct{}

func (variantHorizon) name() string { return "xid-horizon" }

func (variantHorizon) install(ctx context.Context, pool *pgxpool.Pool) error {
	for _, ddl := range []string{
		`CREATE TABLE cmp_b (
			tenant_id TEXT NOT NULL, shard BIGINT NOT NULL, persistence_id TEXT NOT NULL, seq BIGINT NOT NULL, pos BIGINT NOT NULL,
			PRIMARY KEY (tenant_id, persistence_id, seq))`,
		`CREATE INDEX cmp_b_read ON cmp_b (tenant_id, shard, pos)`,
	} {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}

func (variantHorizon) write(ctx context.Context, pool *pgxpool.Pool, events []jEvent, beforeCommit func() bool) (time.Duration, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	start := time.Now()
	entities := make([]string, 0, len(events))
	for _, e := range events {
		// length-prefixed, so (tenant, id) pairs map to distinct keys whatever characters they hold
		entities = append(entities, fmt.Sprintf("%d:%s%s", len(e.Tenant), e.Tenant, e.ID))
	}
	for _, entity := range measure.SortedUnique(entities) {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, entity); err != nil {
			return 0, err
		}
	}
	lockWait := time.Since(start)

	for _, e := range events {
		if _, err := tx.Exec(ctx,
			`INSERT INTO cmp_b (tenant_id, shard, persistence_id, seq, pos)
			 VALUES ($1, $2, $3, $4, $5::bigint + pg_current_xact_id()::text::bigint)`,
			e.Tenant, e.Shard, e.ID, e.Seq, positionBase); err != nil {
			return lockWait, err
		}
	}
	if beforeCommit != nil && !beforeCommit() {
		return lockWait, errRolledBack
	}
	return lockWait, tx.Commit(ctx)
}

const horizonReadSQL = `
SELECT persistence_id, seq, pos FROM cmp_b
WHERE tenant_id = $1 AND shard = $2 AND pos > $3
  AND pos < $5::bigint + pg_snapshot_xmin(pg_current_snapshot())::text::bigint
  AND pos <= COALESCE((
      SELECT pos FROM cmp_b
      WHERE tenant_id = $1 AND shard = $2 AND pos > $3
        AND pos < $5::bigint + pg_snapshot_xmin(pg_current_snapshot())::text::bigint
      ORDER BY pos, persistence_id, seq
      OFFSET $4::bigint - 1 LIMIT 1), 9223372036854775807)
ORDER BY pos, persistence_id, seq`

func (variantHorizon) read(ctx context.Context, pool *pgxpool.Pool, tenant string, shard, after, limit int64) ([]jRead, int64, error) {
	return scanJournal(ctx, pool, tenant, horizonReadSQL, tenant, shard, after, limit, positionBase)
}

// ---------------------------------------------------------------------------------------------------------
// A: per-(scope, shard) serialization with a persistent position counter

// variantSharded gives each (tenant, shard) a row in cmp_a_pos holding the last position handed out. A writer
// takes that row with one UPDATE ... RETURNING before it inserts anything: the statement is the lock and the
// position assignment at once, the row lock is held to commit or rollback, so for one (tenant, shard) position
// order is commit order and a rollback hands the same position to the next writer (no gaps). The counter lives in
// the database, so it is safe across processes and restarts. A write that spans several (tenant, shard) pairs
// takes them all, in a stable order, before inserting.
type variantSharded struct {
	// order is the lock order of a multi-shard write; nil means the stable (sorted) order. The negative-control
	// test replaces it to show that an unsorted order deadlocks.
	order func([]shardKey) []shardKey
	// afterFirstLock, when not nil, runs after the first counter row is taken (negative control only).
	afterFirstLock func(n int)
}

type shardKey struct {
	tenant string
	shard  int64
}

func compareShardKey(a, b shardKey) int {
	if c := cmp.Compare(a.tenant, b.tenant); c != 0 {
		return c
	}
	return cmp.Compare(a.shard, b.shard)
}

func (variantSharded) name() string { return "shard-serialization" }

func (variantSharded) install(ctx context.Context, pool *pgxpool.Pool) error {
	for _, ddl := range []string{
		`CREATE TABLE cmp_a_pos (tenant_id TEXT NOT NULL, shard BIGINT NOT NULL, last BIGINT NOT NULL, PRIMARY KEY (tenant_id, shard))`,
		`CREATE TABLE cmp_a (
			tenant_id TEXT NOT NULL, shard BIGINT NOT NULL, persistence_id TEXT NOT NULL, seq BIGINT NOT NULL, pos BIGINT NOT NULL,
			PRIMARY KEY (tenant_id, persistence_id, seq))`,
		`CREATE INDEX cmp_a_read ON cmp_a (tenant_id, shard, pos)`,
	} {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}

func (v variantSharded) write(ctx context.Context, pool *pgxpool.Pool, events []jEvent, beforeCommit func() bool) (time.Duration, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	keys := make([]shardKey, 0, len(events))
	for _, e := range events {
		keys = append(keys, shardKey{e.Tenant, e.Shard})
	}
	ordered := measure.SortedUniqueFunc(keys, compareShardKey)
	if v.order != nil {
		ordered = v.order(ordered)
	}

	start := time.Now()
	positions := make(map[shardKey]int64, len(ordered))
	for i, k := range ordered {
		var pos int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO cmp_a_pos (tenant_id, shard, last) VALUES ($1, $2, 1)
			ON CONFLICT (tenant_id, shard) DO UPDATE SET last = cmp_a_pos.last + 1
			RETURNING last`, k.tenant, k.shard).Scan(&pos); err != nil {
			return 0, err
		}
		positions[k] = pos
		if v.afterFirstLock != nil {
			v.afterFirstLock(i)
		}
	}
	lockWait := time.Since(start)

	for _, e := range events {
		if _, err := tx.Exec(ctx,
			`INSERT INTO cmp_a (tenant_id, shard, persistence_id, seq, pos) VALUES ($1, $2, $3, $4, $5)`,
			e.Tenant, e.Shard, e.ID, e.Seq, positions[shardKey{e.Tenant, e.Shard}]); err != nil {
			return lockWait, err
		}
	}
	if beforeCommit != nil && !beforeCommit() {
		return lockWait, errRolledBack
	}
	return lockWait, tx.Commit(ctx)
}

const shardedReadSQL = `
SELECT persistence_id, seq, pos FROM cmp_a
WHERE tenant_id = $1 AND shard = $2 AND pos > $3
  AND pos <= COALESCE((
      SELECT pos FROM cmp_a
      WHERE tenant_id = $1 AND shard = $2 AND pos > $3
      ORDER BY pos, persistence_id, seq
      OFFSET $4::bigint - 1 LIMIT 1), 9223372036854775807)
ORDER BY pos, persistence_id, seq`

func (variantSharded) read(ctx context.Context, pool *pgxpool.Pool, tenant string, shard, after, limit int64) ([]jRead, int64, error) {
	return scanJournal(ctx, pool, tenant, shardedReadSQL, tenant, shard, after, limit)
}

// scanJournal runs one read statement and returns its rows and the position of the last one (after is not known
// here, so an empty page returns 0 and the caller keeps its own cursor).
func scanJournal(ctx context.Context, pool *pgxpool.Pool, tenant, sql string, args ...any) ([]jRead, int64, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var (
		out  []jRead
		next int64
	)
	for rows.Next() {
		r := jRead{Tenant: tenant}
		if err := rows.Scan(&r.ID, &r.Seq, &r.Pos); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
		next = r.Pos
	}
	return out, next, rows.Err()
}

func readKeys(rs []jRead) []string {
	keys := make([]string, 0, len(rs))
	for _, r := range rs {
		keys = append(keys, r.key())
	}
	return keys
}

func joinKeys(keys []string) string { return strings.Join(keys, " ") }
