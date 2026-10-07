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

package eventstore_test

import (
	"context"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/persistence/conformance"
	"github.com/getsyntegrity/urd/persistence/postgres"
)

// postgresLatestSchemaVersion is the version of the newest file in persistence/postgres/schema. Adding a file
// means raising it here.
const postgresLatestSchemaVersion = 6

// legacyEventsStoreDDL is events_store as it existed before
// events_store_revisions: the revision was derived from MAX(sequence_number).
const legacyEventsStoreDDL = `
CREATE TABLE events_store
(
    tenant_id         VARCHAR(255) DEFAULT '' NOT NULL,
    persistence_id    VARCHAR(255)          NOT NULL,
    sequence_number   BIGINT                NOT NULL,
    is_deleted        BOOLEAN DEFAULT FALSE NOT NULL,
    event_payload     BYTEA                 NOT NULL,
    event_manifest    VARCHAR(255)          NOT NULL,
    timestamp         BIGINT                NOT NULL,
    shard_number      BIGINT                NOT NULL,
    encryption_key_id VARCHAR(255) DEFAULT '' NOT NULL,
    is_encrypted      BOOLEAN DEFAULT FALSE NOT NULL,
    PRIMARY KEY (tenant_id, persistence_id, sequence_number)
);
`

// TestPostgresEventStore_SchemaMigratesLegacyDatabase starts from a database
// that has events but no revision table, applies eventsStoreSchemaDDL (the
// same idempotent script the k8s init and README use), and proves each
// existing (tenant_id, persistence_id) gets its revision backfilled from its
// highest retained sequence number. Applying the script twice is a no-op.
func TestPostgresEventStore_SchemaMigratesLegacyDatabase(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.Migrate on a database without a revisions table", func(s *specs.Spec) {
		s.It("backfills each revision from the highest retained sequence number and is idempotent", func(sc *specs.Context) {
			ctx := context.Background()

			pool, err := pgxpool.New(ctx, dsn)
			sc.Expect(err).To(specs.BeNil())
			defer pool.Close()

			_, err = pool.Exec(ctx, legacyEventsStoreDDL)
			sc.Expect(err).To(specs.BeNil())
			for _, row := range []struct {
				tenantID string
				seq      int
			}{{"", 1}, {"", 2}, {"", 3}, {"tenant-a", 1}} {
				_, err = pool.Exec(ctx, `
					INSERT INTO events_store (tenant_id, persistence_id, sequence_number, event_payload, event_manifest, timestamp, shard_number)
					VALUES ($1, 'pg-legacy', $2, ''::bytea, '', 0, 1)`, row.tenantID, row.seq)
				sc.Expect(err).To(specs.BeNil())
			}

			store := postgres.NewEventStore(dsn)
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)
			sc.Expect(store.Migrate(ctx)).To(specs.BeNil())
			sc.Expect(store.Migrate(ctx)).To(specs.BeNil()) // Migrate must be idempotent
			version, err := store.SchemaVersion(ctx)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(version).To(specs.Equal(uint(postgresLatestSchemaVersion)))

			tenantA, err := persistence.NewTenantScope("tenant-a")
			sc.Expect(err).To(specs.BeNil())

			requireConflictAt(sc, store.WriteEvents(ctx, persistence.Unscoped(), pgMarkedEvent("pg-legacy", 1, 0), persistence.ExpectGenesis()), 3)
			sc.Expect(store.WriteEvents(ctx, persistence.Unscoped(), pgMarkedEvent("pg-legacy", 4, 4), persistence.ExpectRevision(3))).To(specs.BeNil())
			requireConflictAt(sc, store.WriteEvents(ctx, tenantA, pgMarkedEvent("pg-legacy", 1, 0), persistence.ExpectGenesis()), 1)
			sc.Expect(store.WriteEvents(ctx, tenantA, pgMarkedEvent("pg-legacy", 2, 2), persistence.ExpectRevision(1))).To(specs.BeNil())
		})
	})
}

// legacyEventsStoreDDLBeforeTenantMetadata is events_store and
// events_store_revisions as they existed right after #115's scoped-store
// migration but before tenant_metadata existed: everything
// eventsStoreSchemaDDL creates today except that column.
const legacyEventsStoreDDLBeforeTenantMetadata = `
CREATE TABLE events_store
(
    tenant_id         VARCHAR(255) DEFAULT '' NOT NULL,
    persistence_id    VARCHAR(255)          NOT NULL,
    sequence_number   BIGINT                NOT NULL,
    is_deleted        BOOLEAN DEFAULT FALSE NOT NULL,
    event_payload     BYTEA                 NOT NULL,
    event_manifest    VARCHAR(255)          NOT NULL,
    timestamp         BIGINT                NOT NULL,
    shard_number      BIGINT                NOT NULL,
    encryption_key_id VARCHAR(255) DEFAULT '' NOT NULL,
    is_encrypted      BOOLEAN DEFAULT FALSE NOT NULL,
    PRIMARY KEY (tenant_id, persistence_id, sequence_number)
);
CREATE TABLE events_store_revisions
(
    tenant_id      VARCHAR(255) DEFAULT '' NOT NULL,
    persistence_id VARCHAR(255)            NOT NULL,
    revision       BIGINT                  NOT NULL,
    PRIMARY KEY (tenant_id, persistence_id)
);
`

// TestPostgresEventStore_SchemaMigratesLegacyTenantMetadata starts from a
// database that has events_store and events_store_revisions but no
// tenant_metadata column, inserts an event row the old way with plain SQL
// (no such column to write to), applies eventsStoreSchemaDDL (the same
// idempotent script the k8s init and README use), and proves the column is
// added with NO backfill: the pre-existing row's tenant_metadata is NULL —
// never an invented tenant identity — and the row is otherwise unchanged.
// Applying the script twice is a no-op (#115 Codex P2).
func TestPostgresEventStore_SchemaMigratesLegacyTenantMetadata(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.Migrate on a database without the tenant_metadata column", func(s *specs.Spec) {
		s.It("adds the column without a backfill, leaves the legacy row unchanged and is idempotent", func(sc *specs.Context) {
			ctx := context.Background()

			pool, err := pgxpool.New(ctx, dsn)
			sc.Expect(err).To(specs.BeNil())
			defer pool.Close()

			_, err = pool.Exec(ctx, legacyEventsStoreDDLBeforeTenantMetadata)
			sc.Expect(err).To(specs.BeNil())

			const persistenceID = "pg-legacy-tenant-metadata"
			_, err = pool.Exec(ctx, `
				INSERT INTO events_store (tenant_id, persistence_id, sequence_number, event_payload, event_manifest, timestamp, shard_number)
				VALUES ('', $1, 1, ''::bytea, '', 1000, 1)`, persistenceID)
			sc.Expect(err).To(specs.BeNil())

			store := postgres.NewEventStore(dsn)
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)
			sc.Expect(store.Migrate(ctx)).To(specs.BeNil())
			sc.Expect(store.Migrate(ctx)).To(specs.BeNil()) // Migrate must be idempotent
			version, err := store.SchemaVersion(ctx)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(version).To(specs.Equal(uint(postgresLatestSchemaVersion)))

			var tenantMetadataIsNull bool
			sc.Expect(pool.QueryRow(ctx,
				`SELECT tenant_metadata IS NULL FROM events_store WHERE tenant_id='' AND persistence_id=$1 AND sequence_number=1`,
				persistenceID,
			).Scan(&tenantMetadataIsNull)).To(specs.BeNil())
			sc.Expect(tenantMetadataIsNull).To(specs.BeTrue()) // the migration must not backfill or invent tenant metadata for a pre-existing row

			latest, err := store.GetLatestEvent(ctx, persistence.Unscoped(), persistenceID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(latest).To(specs.Not(specs.BeNil()))
			sc.Expect(latest.GetTenantMetadata()).To(specs.BeEmpty())     // a pre-existing row must read back with no tenant metadata, never an invented identity
			sc.Expect(latest.GetTimestamp()).To(specs.Equal(int64(1000))) // the legacy row's other columns must be unaffected by the migration
		})
	})
}

// openSchemaMigrator is the opener of the schema suite: each call returns a new, connected EventStore, which is
// the SchemaMigrator under test, with its own pool as a separate cluster node would have.
type openSchemaMigrator = func(t conformance.SchemaT) persistence.SchemaMigrator

// postgresSchemaHarness wires the schema migrator suite to a real database. A backend is a database of its own,
// created empty on the shared container for each check, so no check has to reset anything.
func postgresSchemaHarness() conformance.SchemaMigratorHarness {
	ctx := context.Background()
	fatal := func(t conformance.SchemaT, what string, err error) {
		t.Helper()
		t.Errorf("%s: %v", what, err)
		t.FailNow()
	}
	// backendWith creates the database, lets setup leave a legacy shape on it, and returns the opener for it.
	// setup receives the opener of that database and returns SQL to run afterwards, or "".
	backendWith := func(setup func(t conformance.SchemaT, open openSchemaMigrator) string) func(conformance.SchemaT) openSchemaMigrator {
		return func(t conformance.SchemaT) openSchemaMigrator {
			dsn := shared.NewDatabase(t)
			open := func(t conformance.SchemaT) persistence.SchemaMigrator {
				store := postgres.NewEventStore(dsn)
				if err := store.Connect(ctx); err != nil {
					fatal(t, "connect a store", err)
				}
				t.Cleanup(func() { _ = store.Disconnect(ctx) })
				return store
			}
			if setup == nil {
				return open
			}
			ddl := setup(t, open)
			if ddl == "" {
				return open
			}
			pool, err := pgxpool.New(ctx, dsn)
			if err != nil {
				fatal(t, "open a setup pool", err)
			}
			defer pool.Close()
			if _, err = pool.Exec(ctx, ddl); err != nil {
				fatal(t, "apply the legacy shape", err)
			}
			return open
		}
	}
	legacy := func(ddl string) func(conformance.SchemaT) openSchemaMigrator {
		return backendWith(func(conformance.SchemaT, openSchemaMigrator) string { return ddl })
	}
	return conformance.SchemaMigratorHarness{
		NewBackend:    backendWith(nil),
		LatestVersion: postgresLatestSchemaVersion,
		Legacy: []conformance.LegacySchema{
			{Name: "EventsStoreWithoutRevisions", Prepare: legacy(legacyEventsStoreDDL)},
			{Name: "EventsStoreWithoutTenantMetadata", Prepare: legacy(legacyEventsStoreDDLBeforeTenantMetadata)},
			{
				// A database that is already at the latest shape, created by hand
				// (the k8s init.sql) and so without a version record.
				Name: "CurrentShapeWithoutVersionRecord",
				Prepare: backendWith(func(t conformance.SchemaT, open openSchemaMigrator) string {
					if err := open(t).Migrate(ctx); err != nil {
						fatal(t, "build the current shape", err)
					}
					return "DROP TABLE ego_schema_migrations"
				}),
			},
		},
	}
}

func TestPostgresSchemaMigratorConformance(t *testing.T) {
	t.Parallel()
	conformance.RunSchemaMigratorConformance(t, postgresSchemaHarness())
}

// TestPostgresEventStore_MigrateRefusesASchemaNewerThanTheBinary records a version no embedded file has, as a
// newer build would have, and expects an older binary to refuse instead of running on a schema it does not know.
func TestPostgresEventStore_MigrateRefusesASchemaNewerThanTheBinary(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.Migrate on a database migrated by a newer binary", func(s *specs.Spec) {
		s.It("fails with ErrSchemaAhead and changes nothing", func(sc *specs.Context) {
			ctx := context.Background()

			store := postgres.NewEventStore(dsn)
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer func() { _ = store.Disconnect(ctx) }()
			sc.Expect(store.Migrate(ctx)).To(specs.BeNil())

			pool, err := pgxpool.New(ctx, dsn)
			sc.Expect(err).To(specs.BeNil())
			defer pool.Close()
			_, err = pool.Exec(ctx, `INSERT INTO ego_schema_migrations (version) VALUES (99)`)
			sc.Expect(err).To(specs.BeNil())

			err = store.Migrate(ctx)
			sc.Expect(err).To(specs.MatchError(postgres.ErrSchemaAhead))
			sc.Expect(err.Error()).To(specs.MatchRegex("database is at version 99, this binary knows up to 6"))

			version, err := store.SchemaVersion(ctx)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(version).To(specs.Equal(uint(99)))
		})
	})
}

// TestPostgresEventStore_MigrateLeavesAForeignSchemaMigrationsTableAlone starts from a database that another tool
// already manages: it holds a schema_migrations table in golang-migrate's shape. urd must neither read that table
// as its own version line nor change it.
func TestPostgresEventStore_MigrateLeavesAForeignSchemaMigrationsTableAlone(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.Migrate next to a foreign schema_migrations table", func(s *specs.Spec) {
		s.It("creates its own tables, reports its own latest version and leaves the foreign table untouched", func(sc *specs.Context) {
			ctx := context.Background()

			pool, err := pgxpool.New(ctx, dsn)
			sc.Expect(err).To(specs.BeNil())
			defer pool.Close()

			_, err = pool.Exec(ctx, `CREATE TABLE schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL)`)
			sc.Expect(err).To(specs.BeNil())
			_, err = pool.Exec(ctx, `INSERT INTO schema_migrations (version, dirty) VALUES (20240101120000, false)`)
			sc.Expect(err).To(specs.BeNil())

			store := postgres.NewEventStore(dsn)
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer func() { _ = store.Disconnect(ctx) }()
			sc.Expect(store.Migrate(ctx)).To(specs.BeNil())

			version, err := store.SchemaVersion(ctx)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(version).To(specs.Equal(uint(postgresLatestSchemaVersion)))

			var eventsTable, offsetsTable bool
			sc.Expect(pool.QueryRow(ctx, `SELECT to_regclass('events_store') IS NOT NULL, to_regclass('offsets_store') IS NOT NULL`).Scan(&eventsTable, &offsetsTable)).To(specs.BeNil())
			sc.Expect(eventsTable).To(specs.BeTrue())
			sc.Expect(offsetsTable).To(specs.BeTrue())

			var foreignVersion int64
			var foreignDirty bool
			var foreignRows int
			sc.Expect(pool.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&foreignVersion, &foreignDirty)).To(specs.BeNil())
			sc.Expect(pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&foreignRows)).To(specs.BeNil())
			sc.Expect(foreignVersion).To(specs.Equal(int64(20240101120000)))
			sc.Expect(foreignDirty).To(specs.BeFalse())
			sc.Expect(foreignRows).To(specs.Equal(1))
		})
	})
}
