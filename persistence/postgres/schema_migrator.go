// MIT License
//
// Copyright (c) 2022-2026 Arsene Tochemey Gandote
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package postgres

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/getsyntegrity/urd/persistence"
)

// schemaLockKey is the key of the Postgres advisory lock that serializes
// Migrate across every process that uses the same database. It is a fixed
// number ("ego_sche" in ASCII) so all nodes agree on it.
const schemaLockKey int64 = 0x65676f5f73636865

// createSchemaVersionsSQL creates the bookkeeping table: one row per applied
// schema file. The name is prefixed on purpose: a bare schema_migrations is what
// golang-migrate and other tools create, and urd must not read or write a table
// that belongs to them.
const createSchemaVersionsSQL = `
CREATE TABLE IF NOT EXISTS ego_schema_migrations
(
    version    BIGINT      PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// catalogSQL lists what the baseline inference looks at: the tables of the
// current schema, the columns of events_store, and every index.
const catalogSQL = `
SELECT table_name::text FROM information_schema.tables WHERE table_schema = current_schema()
UNION ALL
SELECT table_name::text || '.' || column_name::text FROM information_schema.columns
 WHERE table_schema = current_schema() AND table_name IN ('events_store', 'offsets_store')
UNION ALL
SELECT 'index.' || indexname::text FROM pg_indexes WHERE schemaname = current_schema()`

var embeddedSchemaFiles = sync.OnceValues(func() ([]schemaFile, error) {
	return loadSchemaFiles(schemaFS, schemaDir)
})

// SchemaMigrator brings a Postgres database to the schema Urd's stores expect:
// the events_store, events_store_revisions and offsets_store tables. It
// implements persistence.SchemaMigrator and is what EventStore.Migrate and
// OffsetStore.Migrate run, so either store migrates the whole schema; the two
// stores usually share a database, and one version line is simpler than two.
//
// Migrate applies the embedded schema/NNN_*.sql files in order. Each file runs
// in its own transaction together with the row that records its version in
// ego_schema_migrations, so a failure leaves the database at the last complete
// version. A session-level advisory lock serializes concurrent callers: a node
// that arrives while another is migrating waits, then finds nothing to do.
//
// A database created by hand before versions were recorded has no
// ego_schema_migrations rows. Migrate inspects its tables, columns and indexes,
// records the versions it already has and applies only the rest.
type SchemaMigrator struct {
	pool *pgxpool.Pool
}

var _ persistence.SchemaMigrator = (*SchemaMigrator)(nil)

// NewSchemaMigrator returns a SchemaMigrator that works through pool. The pool
// stays owned by the caller. A nil pool makes both methods return
// ErrNotConnected.
func NewSchemaMigrator(pool *pgxpool.Pool) *SchemaMigrator {
	return &SchemaMigrator{pool: pool}
}

// Migrate applies every schema file the database has not seen yet. It is
// idempotent and safe to call from several processes at once.
func (m *SchemaMigrator) Migrate(ctx context.Context) (err error) {
	if m.pool == nil {
		return ErrNotConnected
	}
	files, err := embeddedSchemaFiles()
	if err != nil {
		return err
	}

	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("postgres: schema: acquire a connection: %w", err)
	}
	defer conn.Release()

	// The lock belongs to this session, so it is taken and released on the one
	// connection, and released even if ctx is already canceled.
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock($1)", schemaLockKey); err != nil {
		return fmt.Errorf("postgres: schema: take the advisory lock: %w", err)
	}
	defer func() {
		unlockCtx := context.WithoutCancel(ctx)
		if _, unlockErr := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", schemaLockKey); unlockErr != nil {
			// A connection that may still hold the lock must not go back to the pool.
			_ = conn.Conn().Close(unlockCtx)
			if err == nil {
				err = fmt.Errorf("postgres: schema: release the advisory lock: %w", unlockErr)
			}
		}
	}()

	if _, err = conn.Exec(ctx, createSchemaVersionsSQL); err != nil {
		return fmt.Errorf("postgres: schema: create ego_schema_migrations: %w", err)
	}
	current, recorded, err := recordedVersion(ctx, conn)
	if err != nil {
		return err
	}
	if !recorded {
		if current, err = recordBaseline(ctx, conn); err != nil {
			return err
		}
	}

	if err = checkSchemaNotAhead(current, files[len(files)-1].version); err != nil {
		return err
	}

	for _, file := range pendingSchemaFiles(files, current) {
		if err = applySchemaFile(ctx, conn, file); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion returns the highest version recorded in ego_schema_migrations, or
// 0 when the table does not exist or has no rows. It changes nothing.
func (m *SchemaMigrator) SchemaVersion(ctx context.Context) (uint, error) {
	if m.pool == nil {
		return 0, ErrNotConnected
	}
	var tableExists bool
	if err := m.pool.QueryRow(ctx, "SELECT to_regclass('ego_schema_migrations') IS NOT NULL").Scan(&tableExists); err != nil {
		return 0, fmt.Errorf("postgres: schema: look for ego_schema_migrations: %w", err)
	}
	if !tableExists {
		return 0, nil
	}
	version, _, err := recordedVersion(ctx, m.pool)
	return version, err
}

// queryer is the one method recordedVersion needs; a pooled connection and the
// pool both have it.
type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// recordedVersion returns the highest recorded version and whether any version
// is recorded at all.
func recordedVersion(ctx context.Context, q queryer) (version uint, recorded bool, err error) {
	var highest *int64
	if err = q.QueryRow(ctx, "SELECT MAX(version) FROM ego_schema_migrations").Scan(&highest); err != nil {
		return 0, false, fmt.Errorf("postgres: schema: read the recorded version: %w", err)
	}
	if highest == nil {
		return 0, false, nil
	}
	return uint(*highest), true, nil
}

// recordBaseline infers the version of a database that has no version record,
// writes the rows for the versions it already has, and returns that version.
func recordBaseline(ctx context.Context, conn *pgxpool.Conn) (uint, error) {
	found, err := readCatalog(ctx, conn)
	if err != nil {
		return 0, err
	}
	version, err := inferSchemaVersion(found)
	if err != nil {
		return 0, err
	}
	if version == 0 {
		return 0, nil
	}
	if _, err = conn.Exec(ctx,
		"INSERT INTO ego_schema_migrations (version) SELECT generate_series(1, $1::bigint) ON CONFLICT DO NOTHING",
		int64(version)); err != nil {
		return 0, fmt.Errorf("postgres: schema: record the baseline version %d: %w", version, err)
	}
	return version, nil
}

func readCatalog(ctx context.Context, conn *pgxpool.Conn) (catalog, error) {
	rows, err := conn.Query(ctx, catalogSQL)
	if err != nil {
		return nil, fmt.Errorf("postgres: schema: inspect the database: %w", err)
	}
	defer rows.Close()
	found := catalog{}
	for rows.Next() {
		var object string
		if err = rows.Scan(&object); err != nil {
			return nil, fmt.Errorf("postgres: schema: inspect the database: %w", err)
		}
		found[object] = true
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: schema: inspect the database: %w", err)
	}
	return found, nil
}

// applySchemaFile runs one file and records its version in one transaction.
func applySchemaFile(ctx context.Context, conn *pgxpool.Conn, file schemaFile) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: schema: begin %s: %w", file.name, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err = tx.Exec(ctx, file.sql); err != nil {
		return fmt.Errorf("postgres: schema: apply %s: %w", file.name, err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO ego_schema_migrations (version) VALUES ($1)", int64(file.version)); err != nil {
		return fmt.Errorf("postgres: schema: record %s: %w", file.name, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: schema: commit %s: %w", file.name, err)
	}
	return nil
}
