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

// These tests exercise postgres.EventStore against a real PostgreSQL instance started by TestMain with
// Testcontainers. Each test gets its own database on that container, so tests run in parallel without seeing
// each other's rows. They never skip: when Docker is not available the package fails to start.
package eventstore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/persistence/conformance"
	"github.com/getsyntegrity/urd/persistence/postgres"
	"github.com/getsyntegrity/urd/tenancy"
)

func TestPostgresEventStore_Conformance(t *testing.T) {
	t.Parallel()
	conformance.RunEventsStoreConformance(t, func(t *testing.T) persistence.EventsStore {
		store, err := provisionPostgresTestStore(shared.NewDatabase(t))
		if err != nil { // the runner's callback takes a *testing.T by API
			t.Fatalf("provision the Postgres test store: %v", err)
		}
		return store
	})
}

// newPostgresTestStore migrates the schema of an empty database to the latest version. Every test owns its
// database (pginfra.Postgres.NewDatabase), so there is nothing to truncate. The returned store is not yet
// Connect()-ed; the conformance runner and the tests below do that themselves.
func newPostgresTestStore(sc *specs.Context, dsn string) *postgres.EventStore {
	sc.Helper()
	store, err := provisionPostgresTestStore(dsn)
	sc.Expect(err).To(specs.BeNil())
	return store
}

// provisionPostgresTestStore does the work of newPostgresTestStore and returns the error instead of asserting,
// for the conformance runner, whose callback has a *testing.T and no spec context. The schema comes from the
// store's own Migrate, the same path an engine started with WithSchemaMigration takes.
func provisionPostgresTestStore(dsn string) (*postgres.EventStore, error) {
	ctx := context.Background()

	migrating := postgres.NewEventStore(dsn)
	if err := migrating.Connect(ctx); err != nil {
		return nil, err
	}
	defer func() { _ = migrating.Disconnect(ctx) }()
	if err := migrating.Migrate(ctx); err != nil {
		return nil, err
	}
	return postgres.NewEventStore(dsn), nil
}

func countEventRows(sc *specs.Context, ctx context.Context, dsn, persistenceID string, sequenceNumber int) int {
	sc.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	sc.Expect(err).To(specs.BeNil())
	defer func() { _ = conn.Close(ctx) }()

	var count int
	sc.Expect(conn.QueryRow(ctx,
		`SELECT COUNT(*) FROM events_store WHERE tenant_id=$1 AND persistence_id=$2 AND sequence_number=$3`,
		"", persistenceID, sequenceNumber,
	).Scan(&count)).To(specs.BeNil())
	return count
}

// pgMarkedEvent builds a single-event batch carrying marker in its payload,
// so the winner of a race between two writers can be identified afterward
// from the persisted event alone. Modeled on testkit/concurrency_test.go's
// markedEvent.
func pgMarkedEvent(persistenceID string, sequenceNumber uint64, marker float64) []*egopb.Event {
	payload, err := anypb.New(&testpb.AccountCreated{AccountId: persistenceID, AccountBalance: marker})
	if err != nil {
		// Packing a known message cannot fail. Panic rather than report: raceTwoWriters and the
		// writer goroutines call this off the spec goroutine, where an assertion must not run.
		panic(err)
	}
	return []*egopb.Event{
		{PersistenceId: persistenceID, SequenceNumber: sequenceNumber, Event: payload, Timestamp: time.Now().UnixMilli(), Shard: 1},
	}
}

// pgEventMarker recovers the marker written by pgMarkedEvent.
func pgEventMarker(sc *specs.Context, event *egopb.Event) float64 {
	sc.Helper()
	var msg testpb.AccountCreated
	sc.Expect(event.GetEvent().UnmarshalTo(&msg)).To(specs.BeNil())
	return msg.GetAccountBalance()
}

// pgMarkedEventWithMetadata behaves exactly like pgMarkedEvent, except the
// built event also carries tenantMetadata — the field a tenant-aware
// EventSourcedActor populates via tenancy.MarshalMetadata before persisting
// (event_sourced_actor.go's marshalEvent), and which insertEvent/scanEvents
// must round-trip exactly (#115 Codex P2).
func pgMarkedEventWithMetadata(persistenceID string, sequenceNumber uint64, marker float64, tenantMetadata map[string]string) []*egopb.Event {
	events := pgMarkedEvent(persistenceID, sequenceNumber, marker)
	events[0].TenantMetadata = tenantMetadata
	return events
}

// pgRealisticTenantMetadata returns a TenantContext for tenantID and the
// Metadata a real write path actually attaches to an event: exactly what
// tenancy.MarshalMetadata(tc) produces (the ego.tenant.* carrier keys), plus
// one arbitrary extra key that is no part of that contract — proving the
// store round-trips the map byte for byte rather than special-casing known
// keys.
func pgRealisticTenantMetadata(sc *specs.Context, tenantID string) (tenancy.TenantContext, map[string]string) {
	sc.Helper()
	id, err := tenancy.NewTenantID(tenantID)
	sc.Expect(err).To(specs.BeNil())
	tc, err := tenancy.NewTenantContext(id)
	sc.Expect(err).To(specs.BeNil())
	metadata := map[string]string(tenancy.MarshalMetadata(tc))
	metadata["x-extra-carried-key"] = "pg-md-extra-value"
	return tc, metadata
}

// raceTwoWriters runs a and b as independent goroutines released
// simultaneously by a closed start channel, so the two calls genuinely
// contend against the store rather than against the test goroutine's own
// ordering. Modeled on testkit/concurrency_test.go's raceTwoWriters.
func raceTwoWriters(a, b func() error) (errA, errB error) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-start
		errA = a()
	}()
	go func() {
		defer wg.Done()
		<-start
		errB = b()
	}()

	close(start)
	wg.Wait()
	return errA, errB
}

// countOutcomes classifies two conditional-write results into (successes,
// conflicts), failing the test if either error is a non-conflict failure.
func countOutcomes(sc *specs.Context, errA, errB error) (successes, conflicts int) {
	sc.Helper()
	for _, err := range []error{errA, errB} {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, persistence.ErrConcurrencyConflict):
			conflicts++
		default:
			sc.Expect(err).To(specs.MatchError(persistence.ErrConcurrencyConflict)) // a non-conflict error
		}
	}
	return successes, conflicts
}

// TestPostgresEventStore_ConcurrentExpectRevisionHasExactlyOneWinner mirrors
// testkit/concurrency_test.go's T8: two independent writers race
// ExpectRevision(1) against the same persistence id, driving the store
// directly (no actor, no mailbox), and exactly one of them must commit.
func TestPostgresEventStore_ConcurrentExpectRevisionHasExactlyOneWinner(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.WriteEvents racing expect-revision writers", func(s *specs.Spec) {
		s.It("commits exactly one of two writers that race ExpectRevision(1) on the same persistence id", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			scope := persistence.Unscoped()
			const persistenceID = "pg-t8-event-race"
			sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 1, 0), persistence.ExpectGenesis())).To(specs.BeNil())

			errA, errB := raceTwoWriters(
				func() error {
					return store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 2, 111), persistence.ExpectRevision(1))
				},
				func() error {
					return store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 2, 222), persistence.ExpectRevision(1))
				},
			)

			successes, conflicts := countOutcomes(sc, errA, errB)
			sc.Expect(successes).To(specs.Equal(1)) // exactly one of the two racing writers must commit
			sc.Expect(conflicts).To(specs.Equal(1)) // the losing writer must observe a typed concurrency conflict

			latest, err := store.GetLatestEvent(ctx, scope, persistenceID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(latest).To(specs.Not(specs.BeNil()))
			sc.Expect(latest.GetSequenceNumber()).To(specs.Equal(uint64(2)))

			winnerMarker := pgEventMarker(sc, latest)
			if errA == nil {
				sc.Expect(winnerMarker).To(specs.Equal(float64(111))) // final revision must reflect writer A, the one that actually succeeded
			} else {
				sc.Expect(winnerMarker).To(specs.Equal(float64(222))) // final revision must reflect writer B, the one that actually succeeded
			}

			// Only the winner's row is persisted: a losing conditional writer must
			// never have inserted its own row before observing the conflict.
			count := countEventRows(sc, ctx, dsn, persistenceID, 2)
			sc.Expect(count).To(specs.Equal(1)) // exactly one row must exist at the contested sequence number
		})
	})
}

// TestPostgresEventStore_ExpectGenesisConflictsOnExistingID proves
// ExpectGenesis() against an already-established persistence id fails
// closed with a *persistence.ConflictError carrying the actual revision,
// without touching the existing row.
func TestPostgresEventStore_ExpectGenesisConflictsOnExistingID(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.WriteEvents genesis precondition", func(s *specs.Spec) {
		s.It("fails ExpectGenesis on an existing persistence id with a ConflictError carrying the actual revision", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			scope := persistence.Unscoped()
			const persistenceID = "pg-genesis-conflict"
			sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 1, 111), persistence.ExpectGenesis())).To(specs.BeNil())

			err := store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 1, 999), persistence.ExpectGenesis())
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(err).To(specs.MatchError(persistence.ErrConcurrencyConflict))

			var conflictErr *persistence.ConflictError
			sc.Expect(errors.As(err, &conflictErr)).To(specs.BeTrue())
			actual, ok := conflictErr.ActualRevision()
			sc.Expect(ok).To(specs.BeTrue())
			sc.Expect(actual).To(specs.Equal(uint64(1)))

			latest, err := store.GetLatestEvent(ctx, scope, persistenceID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(latest).To(specs.Not(specs.BeNil()))
			sc.Expect(pgEventMarker(sc, latest)).To(specs.Equal(float64(111))) // the existing record must be untouched by the failed genesis write
		})
	})
}

// TestPostgresEventStore_TenantScopeIsolatesRecords proves two different
// tenant scopes writing the same persistence_id keep independent
// StorageRevision and never observe each other's records, directly against
// Postgres (persistence/conformance's own matrix already covers this in
// depth; this test pins it once more at the SQL layer specifically).
func TestPostgresEventStore_TenantScopeIsolatesRecords(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore tenant scope isolation", func(s *specs.Spec) {
		s.It("keeps revisions and records of two tenants with the same persistence id independent", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			tenantA, err := persistence.NewTenantScope("tenant-a")
			sc.Expect(err).To(specs.BeNil())
			tenantB, err := persistence.NewTenantScope("tenant-b")
			sc.Expect(err).To(specs.BeNil())
			const persistenceID = "pg-tenant-isolation"

			sc.Expect(store.WriteEvents(ctx, tenantA, pgMarkedEvent(persistenceID, 1, 111), persistence.ExpectGenesis())).To(specs.BeNil())
			// Tenant B must be able to genesis the exact same persistence_id: its CAS
			// state is entirely independent of tenant A's.
			sc.Expect(store.WriteEvents(ctx, tenantB, pgMarkedEvent(persistenceID, 1, 222), persistence.ExpectGenesis())).To(specs.BeNil())

			gotA, err := store.GetLatestEvent(ctx, tenantA, persistenceID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(gotA).To(specs.Not(specs.BeNil()))
			sc.Expect(pgEventMarker(sc, gotA)).To(specs.Equal(float64(111)))

			gotB, err := store.GetLatestEvent(ctx, tenantB, persistenceID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(gotB).To(specs.Not(specs.BeNil()))
			sc.Expect(pgEventMarker(sc, gotB)).To(specs.Equal(float64(222)))
		})
	})
}

// TestPostgresEventStore_UnconditionalWriteCannotBreakExpectRevision races an
// ExpectRevision(1) writer against an unconditional writer that targets the
// same (scope, persistenceID) and the same sequence number. Both writers must
// take the same per-key lock, so exactly one of two orders is observable:
//
//   - the conditional writer commits first, and the unconditional insert of
//     sequence 2 is a no-op (its primary key already exists), or
//   - the unconditional writer commits first, the revision becomes 2, and the
//     conditional writer fails with a typed conflict carrying revision 2.
//
// Without a shared lock the unconditional commit can land between the
// conditional writer's revision read and its insert, which surfaces as a
// primary-key error instead of a conflict. The race is repeated to make that
// window observable.
func TestPostgresEventStore_UnconditionalWriteCannotBreakExpectRevision(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.WriteEvents unconditional write racing a conditional one", func(s *specs.Spec) {
		s.It("never turns the race into a primary-key error: the loser gets a typed conflict or a no-op", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			scope := persistence.Unscoped()
			const (
				iterations        = 200
				conditionalMark   = 111
				unconditionalMark = 222
			)
			for i := range iterations {
				persistenceID := fmt.Sprintf("pg-mixed-race-%d", i)
				sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 1, 0), persistence.ExpectGenesis())).To(specs.BeNil())

				errConditional, errUnconditional := raceTwoWriters(
					func() error {
						return store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 2, conditionalMark), persistence.ExpectRevision(1))
					},
					func() error {
						return store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 2, unconditionalMark), persistence.Unconditional())
					},
				)
				sc.Expect(errUnconditional).To(specs.BeNil())

				latest, err := store.GetLatestEvent(ctx, scope, persistenceID)
				sc.Expect(err).To(specs.BeNil())
				sc.Expect(latest).To(specs.Not(specs.BeNil()))
				sc.Expect(latest.GetSequenceNumber()).To(specs.Equal(uint64(2)))

				if errConditional == nil {
					sc.Expect(pgEventMarker(sc, latest)).To(specs.Equal(float64(conditionalMark)))
					continue
				}
				var conflictErr *persistence.ConflictError
				sc.Expect(errors.As(errConditional, &conflictErr)).To(specs.BeTrue())
				actual, ok := conflictErr.ActualRevision()
				sc.Expect(ok).To(specs.BeTrue())
				sc.Expect(actual).To(specs.Equal(uint64(2)))
				sc.Expect(pgEventMarker(sc, latest)).To(specs.Equal(float64(unconditionalMark)))
			}
		})
	})
}

// TestPostgresEventStore_UnconditionalMixedBatchesDoNotDeadlock runs two
// unconditional batches that touch the same two persistence ids in opposite
// orders, many times concurrently. Locks are acquired in a deterministic
// order, so neither batch can wait on the other in a cycle; PostgreSQL would
// otherwise abort one of them with a deadlock error.
func TestPostgresEventStore_UnconditionalMixedBatchesDoNotDeadlock(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.WriteEvents unconditional batches in opposite id order", func(s *specs.Spec) {
		s.It("lets concurrent batches over the same two persistence ids finish without a deadlock", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			scope := persistence.Unscoped()
			for i := range 100 {
				seq := uint64(i + 1)
				forward := append(pgMarkedEvent("pg-batch-a", seq, 1), pgMarkedEvent("pg-batch-b", seq, 1)...)
				backward := append(pgMarkedEvent("pg-batch-b", seq, 2), pgMarkedEvent("pg-batch-a", seq, 2)...)
				errA, errB := raceTwoWriters(
					func() error { return store.WriteEvents(ctx, scope, forward, persistence.Unconditional()) },
					func() error { return store.WriteEvents(ctx, scope, backward, persistence.Unconditional()) },
				)
				sc.Expect(errA).To(specs.BeNil())
				sc.Expect(errB).To(specs.BeNil())
			}

			for _, persistenceID := range []string{"pg-batch-a", "pg-batch-b"} {
				err := store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 101, 0), persistence.ExpectRevision(100))
				sc.Expect(err).To(specs.BeNil())
			}
		})
	})
}

// writeRevisions commits events 1..n for persistenceID, one ExpectRevision
// step at a time, so the test also exercises the revision each write leaves.
func writeRevisions(sc *specs.Context, store *postgres.EventStore, scope persistence.Scope, persistenceID string, n uint64) {
	sc.Helper()
	ctx := context.Background()
	sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 1, 1), persistence.ExpectGenesis())).To(specs.BeNil())
	for seq := uint64(2); seq <= n; seq++ {
		sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, seq, float64(seq)), persistence.ExpectRevision(seq-1))).To(specs.BeNil())
	}
}

// requireConflictAt asserts err is a typed conflict that observed revision.
func requireConflictAt(sc *specs.Context, err error, revision uint64) {
	sc.Helper()
	var conflictErr *persistence.ConflictError
	sc.Expect(errors.As(err, &conflictErr)).To(specs.BeTrue())
	actual, ok := conflictErr.ActualRevision()
	sc.Expect(ok).To(specs.BeTrue())
	sc.Expect(actual).To(specs.Equal(revision))
}

// appendApplicationName returns dsn with an application_name query parameter
// appended, so a store's own pooled connections can be told apart from any
// other connection against the same database when polling pg_stat_activity.
func appendApplicationName(dsn, name string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "application_name=" + name
}

// newPostgresTestStoreNamed behaves exactly like newPostgresTestStore, except
// every connection the returned store opens for its own operations carries
// applicationName. A deterministic lock-pinning race test uses that tag to
// identify, via pg_locks joined to pg_stat_activity, exactly which lock
// requests belong to this store — not to some other connection, another
// test, or another database — so it can wait for a precise number of lock
// waiters instead of a fixed sleep. Setup (schema creation and truncation)
// still runs over the plain, untagged dsn.
func newPostgresTestStoreNamed(sc *specs.Context, dsn, applicationName string) *postgres.EventStore {
	sc.Helper()
	newPostgresTestStore(sc, dsn) // provisions and truncates over the plain dsn
	return postgres.NewEventStore(appendApplicationName(dsn, applicationName))
}

// waitForLockWaiters polls pg_locks/pg_stat_activity, scoped to
// applicationName and the current database so unrelated connections, other
// databases and any test running concurrently cannot pollute the count,
// until at least want ungranted lock requests are observed. It fails the
// test with a clear message on timeout instead of letting a race test hang
// or, worse, proceed on unverified timing.
func waitForLockWaiters(sc *specs.Context, dsn, applicationName string, want int, timeout time.Duration) {
	sc.Helper()
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dsn)
	sc.Expect(err).To(specs.BeNil())
	defer adminPool.Close()

	sc.Eventually(func() any {
		var count int
		err := adminPool.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_locks l
			JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE a.application_name = $1 AND a.datname = current_database() AND NOT l.granted`,
			applicationName,
		).Scan(&count)
		if err != nil {
			return err // a query error is never a number, so the matcher reports it
		}
		return count
	}, specs.BeGreaterThanOrEqual(want), specs.WithTimeout(timeout), specs.WithInterval(20*time.Millisecond))
}

// rawLockTable opens a dedicated connection outside any store's pool, begins
// a transaction on it, and locks table in the given PostgreSQL lock mode.
// release rolls the transaction back (it never writes data on purpose) and
// closes the connection, both of which drop the lock. Used to pause a store
// write or delete deterministically at a precise point instead of relying on
// goroutine scheduling luck.
func rawLockTable(sc *specs.Context, ctx context.Context, dsn, table, mode string) (release func()) {
	sc.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	sc.Expect(err).To(specs.BeNil())
	tx, err := conn.Begin(ctx)
	sc.Expect(err).To(specs.BeNil())
	_, err = tx.Exec(ctx, fmt.Sprintf("LOCK TABLE %s IN %s MODE", table, mode))
	sc.Expect(err).To(specs.BeNil())
	return func() {
		_ = tx.Rollback(ctx)
		_ = conn.Close(ctx)
	}
}

// rawLockRow behaves like rawLockTable, but row-locks (with FOR UPDATE) the
// rows matched by query instead of locking a whole table. It fails the test
// if query matches no row, since that would silently lock nothing.
func rawLockRow(sc *specs.Context, ctx context.Context, dsn, query string, args ...any) (release func()) {
	sc.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	sc.Expect(err).To(specs.BeNil())
	tx, err := conn.Begin(ctx)
	sc.Expect(err).To(specs.BeNil())
	rows, err := tx.Query(ctx, query, args...)
	sc.Expect(err).To(specs.BeNil())
	matched := 0
	for rows.Next() {
		matched++
	}
	sc.Expect(rows.Err()).To(specs.BeNil())
	rows.Close()
	sc.Expect(matched).To(specs.BeGreaterThan(0))
	return func() {
		_ = tx.Rollback(ctx)
		_ = conn.Close(ctx)
	}
}

// TestPostgresEventStore_PartialDeleteKeepsRevision deletes the oldest events
// and proves the revision a conditional write compares against is still the
// highest committed sequence number.
func TestPostgresEventStore_PartialDeleteKeepsRevision(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.DeleteEvents partial delete", func(s *specs.Spec) {
		s.It("keeps the revision at the highest committed sequence number", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			scope := persistence.Unscoped()
			const persistenceID = "pg-partial-delete"
			writeRevisions(sc, store, scope, persistenceID, 5)

			sc.Expect(store.DeleteEvents(ctx, scope, persistenceID, 3)).To(specs.BeNil())

			replayed, err := store.ReplayEvents(ctx, scope, persistenceID, 1, 5, 10)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(replayed).To(specs.HaveLen(2)) // events 1..3 must no longer be replayable

			requireConflictAt(sc, store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 4, 0), persistence.ExpectRevision(3)), 5)
			requireConflictAt(sc, store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 1, 0), persistence.ExpectGenesis()), 5)
			sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 6, 6), persistence.ExpectRevision(5))).To(specs.BeNil())
		})
	})
}

// TestPostgresEventStore_TotalDeleteKeepsRevision deletes every event of a
// persistence id. ExpectGenesis() must not become valid again and a stale
// revision must not be accepted: the revision stays at the highest sequence
// number ever committed.
func TestPostgresEventStore_TotalDeleteKeepsRevision(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.DeleteEvents total delete", func(s *specs.Spec) {
		s.It("keeps the revision so ExpectGenesis stays invalid and a stale revision is refused", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			scope := persistence.Unscoped()
			const persistenceID = "pg-total-delete"
			writeRevisions(sc, store, scope, persistenceID, 5)

			sc.Expect(store.DeleteEvents(ctx, scope, persistenceID, 5)).To(specs.BeNil())

			latest, err := store.GetLatestEvent(ctx, scope, persistenceID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(latest).To(specs.BeNil()) // every event must be deleted

			requireConflictAt(sc, store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 1, 0), persistence.ExpectGenesis()), 5)
			requireConflictAt(sc, store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 3, 0), persistence.ExpectRevision(2)), 5)
			sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 6, 6), persistence.ExpectRevision(5))).To(specs.BeNil())

			// An unconditional write of an older sequence number must not move the
			// revision backwards either.
			sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 2, 2), persistence.Unconditional())).To(specs.BeNil())
			requireConflictAt(sc, store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 3, 0), persistence.ExpectRevision(2)), 6)
		})
	})
}

// TestPostgresEventStore_DeleteKeepsRevisionPerTenant proves the revision
// marker is scoped: deleting a tenant's events keeps that tenant's revision
// and leaves another tenant's same persistence id untouched.
func TestPostgresEventStore_DeleteKeepsRevisionPerTenant(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.DeleteEvents revision marker per tenant", func(s *specs.Spec) {
		s.It("keeps the deleting tenant's revision and leaves another tenant's record untouched", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			tenantA, err := persistence.NewTenantScope("tenant-a")
			sc.Expect(err).To(specs.BeNil())
			tenantB, err := persistence.NewTenantScope("tenant-b")
			sc.Expect(err).To(specs.BeNil())
			const persistenceID = "pg-tenant-delete"
			writeRevisions(sc, store, tenantA, persistenceID, 3)
			writeRevisions(sc, store, tenantB, persistenceID, 2)

			sc.Expect(store.DeleteEvents(ctx, tenantA, persistenceID, 3)).To(specs.BeNil())

			requireConflictAt(sc, store.WriteEvents(ctx, tenantA, pgMarkedEvent(persistenceID, 1, 0), persistence.ExpectGenesis()), 3)
			sc.Expect(store.WriteEvents(ctx, tenantB, pgMarkedEvent(persistenceID, 3, 3), persistence.ExpectRevision(2))).To(specs.BeNil())
			requireConflictAt(sc, store.WriteEvents(ctx, tenantA, pgMarkedEvent(persistenceID, 4, 0), persistence.ExpectRevision(2)), 3)
		})
	})
}

// TestPostgresEventStore_PersistenceIDs_ZeroPageSize_WithData is the
// Postgres-backed companion to TestPostgresEventStore_PersistenceIDs_ZeroPageSize
// (which proves the degenerate case with a nil pool and never reaches the
// database): here the scope actually holds persistence ids, so the zero-page
// short circuit is proven to return an empty page and an empty token even
// when there is real data it could otherwise have paged over.
func TestPostgresEventStore_PersistenceIDs_ZeroPageSize_WithData(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.PersistenceIDs zero page size on a scope with data", func(s *specs.Spec) {
		s.It("returns an empty page and an empty token even though persistence ids exist", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			scope := persistence.Unscoped()
			for _, id := range []string{"pg-page-a", "pg-page-b", "pg-page-c"} {
				sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(id, 1, 1), persistence.ExpectGenesis())).To(specs.BeNil())
			}

			ids, next, err := store.PersistenceIDs(ctx, scope, 0, "")
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(ids).To(specs.BeEmpty())
			sc.Expect(next).To(specs.BeEmpty())
		})
	})
}

// TestPostgresEventStore_UnconditionalRaceDistinctSequenceConflict pins the
// per-record lock deterministically instead of relying on goroutine
// scheduling luck: a raw connection holds a table-level lock that blocks any
// INSERT into events_store, so an Unconditional() write can be paused exactly
// after it has advanced the events_store_revisions row (see
// writeUnconditional) but before its own event insert commits. A concurrent
// ExpectRevision(1) write is then started and is forced to queue behind that
// same revision row (lockRevision) rather than racing straight for
// events_store, which is exactly the shared lock writeConditional and
// writeUnconditional's doc comments describe. The two writers target
// different sequence numbers (7 and 2), so there is no primary-key clash to
// mask a broken lock: without it, the conditional write could read the stale
// revision (1), "successfully" insert sequence 2, and never learn that
// revision 7 already won.
func TestPostgresEventStore_UnconditionalRaceDistinctSequenceConflict(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore revision lock between an unconditional write and ExpectRevision, distinct sequences", func(s *specs.Spec) {
		s.It("makes the conditional writer queue behind the revision row and fail with a typed conflict", func(sc *specs.Context) {
			const appName = "pg-race-distinct"
			store := newPostgresTestStoreNamed(sc, dsn, appName)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			// t.Cleanup, not a plain defer: it must run even if this goroutine is
			// currently unwinding via a failed assertion (e.g. from waitForLockWaiters timing
			// out), and it must run AFTER the raw lock's own sc.T.Cleanup(release)
			// below (t.Cleanup runs last-registered-first), or a still-blocked
			// writer goroutine would make store.Disconnect's pool.Close() hang
			// forever waiting for its connection to be returned.
			sc.T.Cleanup(func() { _ = store.Disconnect(ctx) })

			scope := persistence.Unscoped()
			const persistenceID = "pg-race-distinct-seq"
			sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 1, 1), persistence.ExpectGenesis())).To(specs.BeNil())

			release := rawLockTable(sc, ctx, dsn, "events_store", "SHARE ROW EXCLUSIVE")
			// Safety net: if a later assertion (or waitForLockWaiters itself) fails
			// before the explicit release() below runs, this still drops the raw
			// lock during cleanup, so a blocked goroutine cannot deadlock
			// store.Disconnect's pool.Close(). release is idempotent (a second
			// Rollback/Close is a harmless no-op error).
			sc.T.Cleanup(release)

			unconditionalDone := make(chan error, 1)
			go func() {
				unconditionalDone <- store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 7, 777), persistence.Unconditional())
			}()
			// The unconditional write advances the revision row (uncontended, since
			// nothing else holds it yet) and then blocks on its own INSERT, which
			// conflicts with the held table lock: exactly one waiter so far.
			waitForLockWaiters(sc, dsn, appName, 1, 5*time.Second)

			conditionalDone := make(chan error, 1)
			go func() {
				conditionalDone <- store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 2, 222), persistence.ExpectRevision(1))
			}()
			// The conditional write's lockRevision now queues behind the
			// unconditional write's held-but-uncommitted revision row lock: a second
			// waiter, blocked on a different lock than the first.
			waitForLockWaiters(sc, dsn, appName, 2, 5*time.Second)

			release()

			errUnconditional := <-unconditionalDone
			errConditional := <-conditionalDone

			sc.Expect(errUnconditional).To(specs.BeNil()) // the unconditional write must always succeed
			requireConflictAt(sc, errConditional, 7)

			count := countEventRows(sc, ctx, dsn, persistenceID, 2)
			sc.Expect(count).To(specs.Equal(0)) // the losing conditional write must never have inserted sequence 2
		})
	})
}

// TestPostgresEventStore_UnconditionalRaceSameSequenceConflict is the same
// deterministic pin as TestPostgresEventStore_UnconditionalRaceDistinctSequenceConflict,
// except both writers target the SAME sequence number. Without the shared
// revision-row lock, the conditional write's own INSERT would be the one to
// discover the clash, surfacing a raw primary-key violation instead of a
// typed *persistence.ConflictError — exactly the failure mode
// TestPostgresEventStore_UnconditionalWriteCannotBreakExpectRevision already
// guards against under timing luck; this test pins it deterministically.
func TestPostgresEventStore_UnconditionalRaceSameSequenceConflict(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore revision lock between an unconditional write and ExpectRevision, same sequence", func(s *specs.Spec) {
		s.It("returns a typed conflict instead of a raw primary-key violation", func(sc *specs.Context) {
			const appName = "pg-race-same"
			store := newPostgresTestStoreNamed(sc, dsn, appName)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			// See the identical t.Cleanup ordering comment in
			// TestPostgresEventStore_UnconditionalRaceDistinctSequenceConflict.
			sc.T.Cleanup(func() { _ = store.Disconnect(ctx) })

			scope := persistence.Unscoped()
			const persistenceID = "pg-race-same-seq"
			sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 1, 1), persistence.ExpectGenesis())).To(specs.BeNil())

			release := rawLockTable(sc, ctx, dsn, "events_store", "SHARE ROW EXCLUSIVE")
			// Safety net: if a later assertion (or waitForLockWaiters itself) fails
			// before the explicit release() below runs, this still drops the raw
			// lock during cleanup, so a blocked goroutine cannot deadlock
			// store.Disconnect's pool.Close(). release is idempotent (a second
			// Rollback/Close is a harmless no-op error).
			sc.T.Cleanup(release)

			unconditionalDone := make(chan error, 1)
			go func() {
				unconditionalDone <- store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 2, 222), persistence.Unconditional())
			}()
			waitForLockWaiters(sc, dsn, appName, 1, 5*time.Second)

			conditionalDone := make(chan error, 1)
			go func() {
				conditionalDone <- store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 2, 999), persistence.ExpectRevision(1))
			}()
			waitForLockWaiters(sc, dsn, appName, 2, 5*time.Second)

			release()

			errUnconditional := <-unconditionalDone
			errConditional := <-conditionalDone

			sc.Expect(errUnconditional).To(specs.BeNil()) // the unconditional write must always succeed

			var conflictErr *persistence.ConflictError
			sc.Expect(errors.As(errConditional, &conflictErr)).To(specs.BeTrue())
			actual, ok := conflictErr.ActualRevision()
			sc.Expect(ok).To(specs.BeTrue())
			sc.Expect(actual).To(specs.Equal(uint64(2)))

			latest, err := store.GetLatestEvent(ctx, scope, persistenceID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(latest).To(specs.Not(specs.BeNil()))
			sc.Expect(pgEventMarker(sc, latest)).To(specs.Equal(float64(222))) // only the unconditional writer's row may occupy sequence 2
		})
	})
}

// TestPostgresEventStore_DeleteEventsLocksRevisionAgainstConcurrentWrite
// proves DeleteEvents takes part in the same per-record lock as WriteEvents
// (see DeleteEvents's doc comment): a raw connection row-locks sequence 1 so
// a total DeleteEvents(..., 2) call blocks on its own DELETE statement AFTER
// it has already locked the events_store_revisions row via
// lockRevisionIfExists. A concurrent ExpectRevision(2) write is then started
// and must queue behind that same revision row rather than racing straight
// ahead, which this test confirms by observing exactly two lock waiters
// before releasing the pause: the blocked DELETE, and the blocked
// lockRevision. Once released, both operations must succeed, the delete must
// never have touched the revision, and the newly written sequence 3 becomes
// the record's new frontier.
func TestPostgresEventStore_DeleteEventsLocksRevisionAgainstConcurrentWrite(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.DeleteEvents locking of the revision row", func(s *specs.Spec) {
		s.It("makes a concurrent ExpectRevision write wait for the delete, then both succeed and sequence 3 becomes the frontier", func(sc *specs.Context) {
			const appName = "pg-race-delete"
			store := newPostgresTestStoreNamed(sc, dsn, appName)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			// See the identical t.Cleanup ordering comment in
			// TestPostgresEventStore_UnconditionalRaceDistinctSequenceConflict.
			sc.T.Cleanup(func() { _ = store.Disconnect(ctx) })

			scope := persistence.Unscoped()
			const persistenceID = "pg-race-delete-target"
			writeRevisions(sc, store, scope, persistenceID, 2)

			release := rawLockRow(sc, ctx, dsn,
				`SELECT sequence_number FROM events_store WHERE tenant_id=$1 AND persistence_id=$2 AND sequence_number=$3 FOR UPDATE`,
				"", persistenceID, uint64(1))
			// Safety net: see the identical sc.T.Cleanup(release) comment in
			// TestPostgresEventStore_UnconditionalRaceDistinctSequenceConflict.
			sc.T.Cleanup(release)

			deleteDone := make(chan error, 1)
			go func() {
				deleteDone <- store.DeleteEvents(ctx, scope, persistenceID, 2)
			}()
			// DeleteEvents has locked the revision row and is now blocked on its own
			// DELETE, which needs sequence 1's row lock: one waiter.
			waitForLockWaiters(sc, dsn, appName, 1, 5*time.Second)

			writeDone := make(chan error, 1)
			go func() {
				writeDone <- store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 3, 3), persistence.ExpectRevision(2))
			}()
			// The conditional write's lockRevision now queues behind DeleteEvents's
			// held revision-row lock: a second waiter, blocked on a different lock
			// than the first.
			waitForLockWaiters(sc, dsn, appName, 2, 5*time.Second)

			release()

			sc.Expect(<-deleteDone).To(specs.BeNil())
			sc.Expect(<-writeDone).To(specs.BeNil()) // the conditional write must observe the unchanged revision and succeed

			replayed, err := store.ReplayEvents(ctx, scope, persistenceID, 1, 10, 10)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(replayed).To(specs.HaveLen(1)) // only sequence 3 must remain once the delete and the write both commit
			sc.Expect(replayed[0].GetSequenceNumber()).To(specs.Equal(uint64(3)))

			requireConflictAt(sc, store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 1, 0), persistence.ExpectGenesis()), 3)
			requireConflictAt(sc, store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 4, 0), persistence.ExpectRevision(2)), 3)
		})
	})
}

// TestPostgresEventStore_TenantMetadataRoundTrips_UnconditionalWrite proves an
// unconditional write persists egopb.Event.TenantMetadata exactly, and both
// GetLatestEvent and ReplayEvents recover it unchanged (#115 Codex P2:
// insertEvent/scanEvents used to drop this field on every write and read
// path, so a tenant-aware EventSourcedActor rejected its own recovered
// events after a restart — see event_sourced_actor.go:572 and
// tenancy.UnmarshalMetadata).
func TestPostgresEventStore_TenantMetadataRoundTrips_UnconditionalWrite(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore tenant metadata on an unconditional write", func(s *specs.Spec) {
		s.It("persists the metadata exactly and returns it unchanged from GetLatestEvent and ReplayEvents", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			tc, metadata := pgRealisticTenantMetadata(sc, "pg-md-unconditional-tenant")
			scope, err := persistence.NewTenantScope("pg-md-unconditional-tenant")
			sc.Expect(err).To(specs.BeNil())
			const persistenceID = "pg-md-unconditional-event"

			sc.Expect(store.WriteEvents(ctx, scope,
				pgMarkedEventWithMetadata(persistenceID, 1, 1, metadata),
				persistence.Unconditional())).To(specs.BeNil())

			latest, err := store.GetLatestEvent(ctx, scope, persistenceID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(latest).To(specs.Not(specs.BeNil()))
			sc.Expect(latest.GetTenantMetadata()).To(specs.Equal(metadata)) // GetLatestEvent must recover the exact tenant metadata map

			replayed, err := store.ReplayEvents(ctx, scope, persistenceID, 1, 1, 10)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(replayed).To(specs.HaveLen(1))
			sc.Expect(replayed[0].GetTenantMetadata()).To(specs.Equal(metadata)) // ReplayEvents must recover the exact tenant metadata map

			gotTC, err := tenancy.UnmarshalMetadata(tenancy.Metadata(latest.GetTenantMetadata()))
			sc.Expect(err).To(specs.BeNil()) // the recovered metadata must still unmarshal into a valid TenantContext
			sc.Expect(gotTC).To(specs.Equal(tc))
		})
	})
}

// TestPostgresEventStore_TenantMetadataRoundTrips_ConditionalWrite is the same
// proof as TestPostgresEventStore_TenantMetadataRoundTrips_UnconditionalWrite,
// against writeConditional's insertEventSQL path instead of
// writeUnconditional's insertEventIgnoreDuplicateSQL (#115 Codex P2).
func TestPostgresEventStore_TenantMetadataRoundTrips_ConditionalWrite(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore tenant metadata on a conditional write", func(s *specs.Spec) {
		s.It("persists the metadata exactly and returns it unchanged from GetLatestEvent and ReplayEvents", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			tc, metadata := pgRealisticTenantMetadata(sc, "pg-md-conditional-tenant")
			scope, err := persistence.NewTenantScope("pg-md-conditional-tenant")
			sc.Expect(err).To(specs.BeNil())
			const persistenceID = "pg-md-conditional-event"

			sc.Expect(store.WriteEvents(ctx, scope,
				pgMarkedEventWithMetadata(persistenceID, 1, 1, metadata),
				persistence.ExpectGenesis())).To(specs.BeNil())

			latest, err := store.GetLatestEvent(ctx, scope, persistenceID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(latest).To(specs.Not(specs.BeNil()))
			sc.Expect(latest.GetTenantMetadata()).To(specs.Equal(metadata)) // GetLatestEvent must recover the exact tenant metadata map

			replayed, err := store.ReplayEvents(ctx, scope, persistenceID, 1, 1, 10)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(replayed).To(specs.HaveLen(1))
			sc.Expect(replayed[0].GetTenantMetadata()).To(specs.Equal(metadata)) // ReplayEvents must recover the exact tenant metadata map

			gotTC, err := tenancy.UnmarshalMetadata(tenancy.Metadata(latest.GetTenantMetadata()))
			sc.Expect(err).To(specs.BeNil()) // the recovered metadata must still unmarshal into a valid TenantContext
			sc.Expect(gotTC).To(specs.Equal(tc))
		})
	})
}

// TestPostgresEventStore_TenantMetadataRoundTrips_GetShardEvents proves
// GetShardEvents — the third scanEvents call site — also recovers
// egopb.Event.TenantMetadata exactly (#115 Codex P2).
func TestPostgresEventStore_TenantMetadataRoundTrips_GetShardEvents(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore.GetShardEvents tenant metadata", func(s *specs.Spec) {
		s.It("returns the stored tenant metadata unchanged", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			tc, metadata := pgRealisticTenantMetadata(sc, "pg-md-shard-tenant")
			scope, err := persistence.NewTenantScope("pg-md-shard-tenant")
			sc.Expect(err).To(specs.BeNil())
			const persistenceID = "pg-md-shard-event"

			sc.Expect(store.WriteEvents(ctx, scope,
				pgMarkedEventWithMetadata(persistenceID, 1, 1, metadata),
				persistence.ExpectGenesis())).To(specs.BeNil())

			events, _, err := store.GetShardEvents(ctx, scope, 1, 0, 10)
			sc.Expect(err).To(specs.BeNil())

			var found *egopb.Event
			for _, event := range events {
				if event.GetPersistenceId() == persistenceID {
					found = event
				}
			}
			sc.Expect(found).To(specs.Not(specs.BeNil()))                  // GetShardEvents must return the event written above
			sc.Expect(found.GetTenantMetadata()).To(specs.Equal(metadata)) // GetShardEvents must recover the exact tenant metadata map

			gotTC, err := tenancy.UnmarshalMetadata(tenancy.Metadata(found.GetTenantMetadata()))
			sc.Expect(err).To(specs.BeNil()) // the recovered metadata must still unmarshal into a valid TenantContext
			sc.Expect(gotTC).To(specs.Equal(tc))
		})
	})
}

// TestPostgresEventStore_TenantMetadataAbsent_ReadsAsNone proves an event
// written with no TenantMetadata (the nil, non-tenant-aware case — most of
// this file's other events) reads back with none: proto3 cannot distinguish
// a nil map from an empty one, so this is the same wire shape a legacy row
// produces (#115 Codex P2).
func TestPostgresEventStore_TenantMetadataAbsent_ReadsAsNone(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)
	specs.Describe(t, "postgres.EventStore tenant metadata when none was written", func(s *specs.Spec) {
		s.It("reads the event back with no tenant metadata", func(sc *specs.Context) {
			store := newPostgresTestStore(sc, dsn)
			ctx := context.Background()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			defer store.Disconnect(ctx)

			scope := persistence.Unscoped()
			const persistenceID = "pg-md-absent-event"

			sc.Expect(store.WriteEvents(ctx, scope, pgMarkedEvent(persistenceID, 1, 1), persistence.ExpectGenesis())).To(specs.BeNil())

			latest, err := store.GetLatestEvent(ctx, scope, persistenceID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(latest).To(specs.Not(specs.BeNil()))
			sc.Expect(latest.GetTenantMetadata()).To(specs.BeEmpty()) // an event written with no tenant metadata must read back with none
		})
	})
}
