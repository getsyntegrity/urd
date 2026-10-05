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

package conformance

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
)

// RunEventsStoreConformance runs the full cross-tenant isolation matrix
// (EventsStoreChecks) against a persistence.EventsStore implementation.
// newStore MUST return a fresh, empty store on every call — see the package
// doc comment.
func RunEventsStoreConformance(t *testing.T, newStore func(t *testing.T) persistence.EventsStore) {
	t.Helper()
	runConformance(t, EventsStoreChecks, newStore)
}

// CaptureEventsStoreChecks runs EventsStoreChecks against a fresh store
// obtained from newStore for each check, capturing pass/fail without a
// *testing.T. See the package doc comment's Self-checking section.
func CaptureEventsStoreChecks(newStore func() persistence.EventsStore) []CheckResult {
	return captureChecks(EventsStoreChecks, newStore)
}

// EventsStoreChecks is the named isolation matrix RunEventsStoreConformance
// runs against a persistence.EventsStore. Exported so a permanent regression
// guard (testkit/conformance_test.go's TestConformanceCatchesNonIsolatingStore)
// can prove this suite actually fails against a store that ignores Scope.
var EventsStoreChecks = []Check[persistence.EventsStore]{
	{Name: "ReadIsolation/OtherTenantGetsNothing", Run: eventsOtherTenantGetsNothing},
	{Name: "ReadIsolation/UnscopedAndTenantDoNotCrossRead", Run: eventsUnscopedAndTenantDoNotCrossRead},
	{Name: "ReadIsolation/BothTenantsReadTheirOwnRecord", Run: eventsBothTenantsReadOwnRecord},
	{Name: "WriteIsolation/OtherTenantWriteLeavesRecordUntouched", Run: eventsOtherTenantWriteLeavesRecordUntouched},
	{Name: "WriteIsolation/DeleteIsScoped", Run: eventsDeleteIsScoped},
	{Name: "CAS/ExpectGenesisSucceedsForNewTenantOnEstablishedID", Run: eventsExpectGenesisSucceedsForNewTenant},
	{Name: "CAS/ExpectRevisionConflictCarriesItsScope", Run: eventsExpectRevisionConflictCarriesScope},
	{Name: "CAS/ConflictInOneScopeNotObservableInAnother", Run: eventsConflictNotObservableInAnotherScope},
	{Name: "Enumeration/PersistenceIDsScopedToOwnTenant", Run: eventsPersistenceIDsScopedToOwnTenant},
	{Name: "Enumeration/PersistenceIDsPaginationCoversEveryIDExactlyOnce", Run: eventsPersistenceIDsPaginationCoversEveryIDExactlyOnce},
	{Name: "Unscoped/NeverCollidesWithTenantNamedUnscoped", Run: eventsUnscopedNeverCollidesWithForgedTenant},
	{Name: "ShardReads/GetShardEventsReturnsOnlyTheScopesEvents", Run: eventsGetShardEventsScoped},
	{Name: "ShardReads/ShardOffsetsCoverOnlyTheScopesShards", Run: eventsShardOffsetsScoped},
	{Name: "ShardReads/UnscopedNeverReadsATenantNamedUnscoped", Run: eventsShardReadsUnscopedVersusForgedTenant},
	{Name: "ShardReads/InvalidScopeIsRejectedAndReadsNothing", Run: eventsShardReadsRejectInvalidScope},
}

func eventsOtherTenantGetsNothing(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")
	tenantB := mustTenantScope(t, "tenant-b")
	const id = "read-isolation"

	requireNoError(t, store.WriteEvents(ctx, tenantA, eventBatch(t, id, 1, 111), persistence.Unconditional()))

	got, err := store.GetLatestEvent(ctx, tenantB, id)
	requireNoError(t, err)
	requireNil(t, got, "tenant B must not see tenant A's record")

	replayed, err := store.ReplayEvents(ctx, tenantB, id, 1, 1, 10)
	requireNoError(t, err)
	requireEmpty(t, replayed, "tenant B must not replay tenant A's events")
}

func eventsUnscopedAndTenantDoNotCrossRead(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")

	const idWrittenByTenant = "unscoped-cross-read-tenant-wrote"
	requireNoError(t, store.WriteEvents(ctx, tenantA, eventBatch(t, idWrittenByTenant, 1, 111), persistence.Unconditional()))
	gotUnscoped, err := store.GetLatestEvent(ctx, persistence.Unscoped(), idWrittenByTenant)
	requireNoError(t, err)
	requireNil(t, gotUnscoped, "Unscoped() must not see a record written only under a tenant scope")

	const idWrittenByUnscoped = "unscoped-cross-read-unscoped-wrote"
	requireNoError(t, store.WriteEvents(ctx, persistence.Unscoped(), eventBatch(t, idWrittenByUnscoped, 1, 222), persistence.Unconditional()))
	gotTenant, err := store.GetLatestEvent(ctx, tenantA, idWrittenByUnscoped)
	requireNoError(t, err)
	requireNil(t, gotTenant, "a tenant scope must not see a record written only under Unscoped()")
}

func eventsBothTenantsReadOwnRecord(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")
	tenantB := mustTenantScope(t, "tenant-b")
	const id = "both-write-own-read"

	requireNoError(t, store.WriteEvents(ctx, tenantA, eventBatch(t, id, 1, 111), persistence.Unconditional()))
	requireNoError(t, store.WriteEvents(ctx, tenantB, eventBatch(t, id, 1, 222), persistence.Unconditional()))

	gotA, err := store.GetLatestEvent(ctx, tenantA, id)
	requireNoError(t, err)
	requireNotNil(t, gotA)
	requireEqual(t, float64(111), eventMarker(t, gotA), "tenant A must read back its own record, never tenant B's")

	gotB, err := store.GetLatestEvent(ctx, tenantB, id)
	requireNoError(t, err)
	requireNotNil(t, gotB)
	requireEqual(t, float64(222), eventMarker(t, gotB), "tenant B must read back its own record, never tenant A's")
}

func eventsOtherTenantWriteLeavesRecordUntouched(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")
	tenantB := mustTenantScope(t, "tenant-b")
	const id = "write-isolation"

	requireNoError(t, store.WriteEvents(ctx, tenantA, eventBatch(t, id, 1, 111), persistence.Unconditional()))
	before, err := store.GetLatestEvent(ctx, tenantA, id)
	requireNoError(t, err)
	requireNotNil(t, before)

	requireNoError(t, store.WriteEvents(ctx, tenantB, eventBatch(t, id, 1, 999), persistence.Unconditional()))

	after, err := store.GetLatestEvent(ctx, tenantA, id)
	requireNoError(t, err)
	requireNotNil(t, after)
	requireTrue(t, proto.Equal(before, after), "tenant B's write must not modify tenant A's record for the same persistence_id")
}

func eventsDeleteIsScoped(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")
	tenantB := mustTenantScope(t, "tenant-b")
	const id = "delete-isolation"

	requireNoError(t, store.WriteEvents(ctx, tenantA, eventBatch(t, id, 1, 111), persistence.Unconditional()))
	requireNoError(t, store.WriteEvents(ctx, tenantB, eventBatch(t, id, 1, 222), persistence.Unconditional()))

	requireNoError(t, store.DeleteEvents(ctx, tenantB, id, 100))

	gotA, err := store.GetLatestEvent(ctx, tenantA, id)
	requireNoError(t, err)
	requireNotNil(t, gotA, "deleting tenant B's events must not delete tenant A's")
	requireEqual(t, float64(111), eventMarker(t, gotA))

	gotB, err := store.GetLatestEvent(ctx, tenantB, id)
	requireNoError(t, err)
	requireNil(t, gotB, "tenant B's own record must actually be gone after its own scoped delete")
}

// eventsExpectGenesisSucceedsForNewTenant is the sharpest check in the
// suite. A store that merely tags rows with tenant_metadata
// (ego-store-001's R7) but still keys its compare-and-swap by
// persistence_id alone gets this wrong: it would observe tenant A's
// already-established revision and reject tenant B's genesis write with a
// spurious conflict, even though tenant B has never written this
// persistence_id before. Correct isolation means tenant B's CAS state is
// entirely independent of tenant A's.
func eventsExpectGenesisSucceedsForNewTenant(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")
	tenantB := mustTenantScope(t, "tenant-b")
	const id = "genesis-cross-tenant"

	requireNoError(t, store.WriteEvents(ctx, tenantA, eventBatch(t, id, 1, 111), persistence.ExpectGenesis()))

	err := store.WriteEvents(ctx, tenantB, eventBatch(t, id, 1, 222), persistence.ExpectGenesis())
	requireNoError(t, err, "ExpectGenesis() must succeed for tenant B: tenant A having already established this id must never leak into tenant B's own CAS state")

	gotB, err := store.GetLatestEvent(ctx, tenantB, id)
	requireNoError(t, err)
	requireNotNil(t, gotB)
	requireEqual(t, float64(222), eventMarker(t, gotB))
}

func eventsExpectRevisionConflictCarriesScope(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")
	const id = "conflict-carries-scope"

	requireNoError(t, store.WriteEvents(ctx, tenantA, eventBatch(t, id, 1, 111), persistence.ExpectGenesis()))

	err := store.WriteEvents(ctx, tenantA, eventBatch(t, id, 2, 222), persistence.ExpectRevision(999))
	requireError(t, err)
	requireErrorIs(t, err, persistence.ErrConcurrencyConflict)

	var conflictErr *persistence.ConflictError
	requireTrue(t, errors.As(err, &conflictErr), "the returned error must be a *persistence.ConflictError")
	requireTrue(t, conflictErr.Scope().Equal(tenantA), "the conflict must carry the scope the failed write actually targeted")
}

func eventsConflictNotObservableInAnotherScope(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")
	tenantB := mustTenantScope(t, "tenant-b")
	const id = "conflict-not-cross-scope"

	requireNoError(t, store.WriteEvents(ctx, tenantA, eventBatch(t, id, 1, 111), persistence.ExpectGenesis()))
	// This attempt conflicts in tenant A's own scope: A already has a record.
	err := store.WriteEvents(ctx, tenantA, eventBatch(t, id, 1, 999), persistence.ExpectGenesis())
	requireError(t, err)
	requireErrorIs(t, err, persistence.ErrConcurrencyConflict)

	// Tenant B, using the exact same precondition that just conflicted in A,
	// must succeed: A's conflict must not have left any observable trace in
	// B's own CAS state.
	err = store.WriteEvents(ctx, tenantB, eventBatch(t, id, 1, 222), persistence.ExpectGenesis())
	requireNoError(t, err, "a conflict raised in tenant A's scope must not be observable in tenant B's scope")
}

func eventsPersistenceIDsScopedToOwnTenant(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")
	tenantB := mustTenantScope(t, "tenant-b")
	const sharedID = "shared-id"
	const aOnlyID = "a-only-id"

	requireNoError(t, store.WriteEvents(ctx, tenantA, eventBatch(t, sharedID, 1, 1), persistence.Unconditional()))
	requireNoError(t, store.WriteEvents(ctx, tenantB, eventBatch(t, sharedID, 1, 2), persistence.Unconditional()))
	requireNoError(t, store.WriteEvents(ctx, persistence.Unscoped(), eventBatch(t, sharedID, 1, 3), persistence.Unconditional()))
	requireNoError(t, store.WriteEvents(ctx, tenantA, eventBatch(t, aOnlyID, 1, 4), persistence.Unconditional()))

	idsA, _, err := store.PersistenceIDs(ctx, tenantA, 100, "")
	requireNoError(t, err)
	requireElementsMatch(t, []string{sharedID, aOnlyID}, idsA, "PersistenceIDs(tenantA) must list exactly tenant A's own ids, never tenant B's or Unscoped()'s, even for an identical persistence_id string")

	idsB, _, err := store.PersistenceIDs(ctx, tenantB, 100, "")
	requireNoError(t, err)
	requireElementsMatch(t, []string{sharedID}, idsB)
	requireNotContains(t, idsB, aOnlyID)
}

// eventsPersistenceIDsPaginationCoversEveryIDExactlyOnce proves
// PersistenceIDs's pagination contract (persistence.EventsStore.PersistenceIDs's
// doc comment) holds across multiple page boundaries: writing enough
// aggregates in one scope to force several pages at a small page size, then
// iterating from an empty token until an empty token is returned, must
// yield exactly the set of ids written — none skipped, none duplicated. A
// store that hands back the first NOT-yet-returned key as its token, while
// resuming strictly after that same token on the following page, silently
// skips exactly that key at every page boundary (see
// testkit/eventstore.go's PersistenceIDs doc comment for the exact defect
// this guards against).
func eventsPersistenceIDsPaginationCoversEveryIDExactlyOnce(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenant := mustTenantScope(t, "pagination-tenant")

	const pageSize = 4
	const total = 3*pageSize + 1 // forces at least four pages at pageSize
	want := make(map[string]struct{}, total)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("pagination-id-%03d", i)
		want[id] = struct{}{}
		requireNoError(t, store.WriteEvents(ctx, tenant, eventBatch(t, id, 1, float64(i)), persistence.Unconditional()))
	}

	got := make(map[string]int, total)
	var pageToken string
	pages := 0
	for {
		ids, nextToken, err := store.PersistenceIDs(ctx, tenant, pageSize, pageToken)
		requireNoError(t, err)
		pages++
		requireLessOrEqual(t, pages, total+1, "pagination did not terminate: nextPageToken never became empty")
		for _, id := range ids {
			got[id]++
		}
		if nextToken == "" {
			break
		}
		pageToken = nextToken
	}

	gotIDs := make(map[string]struct{}, len(got))
	for id, count := range got {
		requireEqual(t, 1, count, "persistence id %q was returned more than once across pages", id)
		gotIDs[id] = struct{}{}
	}
	requireEqual(t, want, gotIDs, "pagination must cover every written persistence id exactly once, with none skipped at a page boundary")

	// This sanity check comes last, deliberately: a store that silently
	// skips ids at page boundaries can also terminate in fewer pages than
	// expected (fewer ids returned per page overall), so the coverage
	// assertion above is the sharper, more diagnostic failure and must be
	// seen first.
	requireGreaterOrEqual(t, pages, 4, "the test setup must actually force multiple pages")
}

func eventsUnscopedNeverCollidesWithForgedTenant(ctx context.Context, t TestingT, store persistence.EventsStore) {
	forgedTenant := mustTenantScope(t, "unscoped")
	const id = "forging-guard"

	requireNoError(t, store.WriteEvents(ctx, persistence.Unscoped(), eventBatch(t, id, 1, 111), persistence.ExpectGenesis()))
	// The sharp assertion is the CAS success below: a store that reduced Scope
	// to its String() form ("unscoped" vs "tenant:unscoped") could still pass
	// the read/write checks above by accident, but would incorrectly conflict
	// here if it ever compared scopes by text rather than structurally.
	err := store.WriteEvents(ctx, forgedTenant, eventBatch(t, id, 1, 222), persistence.ExpectGenesis())
	requireNoError(t, err, `a tenant literally named "unscoped" must never forge Unscoped()'s own CAS state`)

	gotUnscoped, err := store.GetLatestEvent(ctx, persistence.Unscoped(), id)
	requireNoError(t, err)
	requireNotNil(t, gotUnscoped)
	requireEqual(t, float64(111), eventMarker(t, gotUnscoped))

	gotForged, err := store.GetLatestEvent(ctx, forgedTenant, id)
	requireNoError(t, err)
	requireNotNil(t, gotForged)
	requireEqual(t, float64(222), eventMarker(t, gotForged))
}

// shardBatch is eventBatch placed on an explicit shard with an explicit
// timestamp, so a shard read can tell the scopes' events apart.
func shardBatch(t TestingT, persistenceID string, marker float64, shard uint64, timestamp int64) []*egopb.Event {
	events := eventBatch(t, persistenceID, 1, marker)
	events[0].Shard = shard
	events[0].Timestamp = timestamp
	return events
}

func shardEventIDs(events []*egopb.Event) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.GetPersistenceId())
	}
	return ids
}

func eventsGetShardEventsScoped(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")
	tenantB := mustTenantScope(t, "tenant-b")
	const shard = 7

	// the same shard in three scopes, with interleaved timestamps
	requireNoError(t, store.WriteEvents(ctx, tenantA, shardBatch(t, "shard-a", 1, shard, 100), persistence.Unconditional()))
	requireNoError(t, store.WriteEvents(ctx, tenantB, shardBatch(t, "shard-b", 2, shard, 200), persistence.Unconditional()))
	requireNoError(t, store.WriteEvents(ctx, persistence.Unscoped(), shardBatch(t, "shard-u", 3, shard, 300), persistence.Unconditional()))
	requireNoError(t, store.WriteEvents(ctx, tenantA, shardBatch(t, "shard-a-2", 4, shard, 400), persistence.Unconditional()))

	gotA, nextA, err := store.GetShardEvents(ctx, tenantA, shard, 0, 10)
	requireNoError(t, err)
	requireEqual(t, []string{"shard-a", "shard-a-2"}, shardEventIDs(gotA), "tenant A must read only its own events of the shard")
	requireEqual(t, int64(400), nextA, "the next offset is the timestamp of A's last event, not of another scope's")

	gotB, nextB, err := store.GetShardEvents(ctx, tenantB, shard, 0, 10)
	requireNoError(t, err)
	requireEqual(t, []string{"shard-b"}, shardEventIDs(gotB), "tenant B must read only its own events of the shard")
	requireEqual(t, int64(200), nextB)

	gotU, nextU, err := store.GetShardEvents(ctx, persistence.Unscoped(), shard, 0, 10)
	requireNoError(t, err)
	requireEqual(t, []string{"shard-u"}, shardEventIDs(gotU), "Unscoped() must not read a tenant's events")
	requireEqual(t, int64(300), nextU)

	// the limit and the offset apply within the scope
	limited, _, err := store.GetShardEvents(ctx, tenantA, shard, 0, 1)
	requireNoError(t, err)
	requireEqual(t, []string{"shard-a"}, shardEventIDs(limited))
	after, _, err := store.GetShardEvents(ctx, tenantA, shard, 100, 10)
	requireNoError(t, err)
	requireEqual(t, []string{"shard-a-2"}, shardEventIDs(after), "an offset earlier than another scope's event must not bring it back")
}

func eventsShardOffsetsScoped(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")
	tenantB := mustTenantScope(t, "tenant-b")
	tenantC := mustTenantScope(t, "tenant-c")

	requireNoError(t, store.WriteEvents(ctx, tenantA, shardBatch(t, "off-a", 1, 7, 100), persistence.Unconditional()))
	requireNoError(t, store.WriteEvents(ctx, tenantB, shardBatch(t, "off-b", 2, 8, 900), persistence.Unconditional()))
	requireNoError(t, store.WriteEvents(ctx, tenantB, shardBatch(t, "off-b-7", 3, 7, 950), persistence.Unconditional()))

	offsetsA, err := store.ShardOffsets(ctx, tenantA)
	requireNoError(t, err)
	requireEqual(t, map[uint64]int64{7: 100}, offsetsA, "tenant A sees only its own shards and its own newest timestamp")

	offsetsB, err := store.ShardOffsets(ctx, tenantB)
	requireNoError(t, err)
	requireEqual(t, map[uint64]int64{7: 950, 8: 900}, offsetsB)

	offsetsC, err := store.ShardOffsets(ctx, tenantC)
	requireNoError(t, err)
	requireEmpty(t, offsetsC, "a scope with no events has no shards")

	offsetsU, err := store.ShardOffsets(ctx, persistence.Unscoped())
	requireNoError(t, err)
	requireEmpty(t, offsetsU, "Unscoped() must not see the shards of a tenant")
}

func eventsShardReadsUnscopedVersusForgedTenant(ctx context.Context, t TestingT, store persistence.EventsStore) {
	forged := mustTenantScope(t, "unscoped")
	const shard = 5

	requireNoError(t, store.WriteEvents(ctx, persistence.Unscoped(), shardBatch(t, "forge-u", 1, shard, 100), persistence.Unconditional()))
	requireNoError(t, store.WriteEvents(ctx, forged, shardBatch(t, "forge-t", 2, shard, 200), persistence.Unconditional()))

	gotU, _, err := store.GetShardEvents(ctx, persistence.Unscoped(), shard, 0, 10)
	requireNoError(t, err)
	requireEqual(t, []string{"forge-u"}, shardEventIDs(gotU))

	gotT, _, err := store.GetShardEvents(ctx, forged, shard, 0, 10)
	requireNoError(t, err)
	requireEqual(t, []string{"forge-t"}, shardEventIDs(gotT))

	offsetsU, err := store.ShardOffsets(ctx, persistence.Unscoped())
	requireNoError(t, err)
	requireEqual(t, map[uint64]int64{shard: 100}, offsetsU)

	offsetsT, err := store.ShardOffsets(ctx, forged)
	requireNoError(t, err)
	requireEqual(t, map[uint64]int64{shard: 200}, offsetsT)
}

func eventsShardReadsRejectInvalidScope(ctx context.Context, t TestingT, store persistence.EventsStore) {
	tenantA := mustTenantScope(t, "tenant-a")
	requireNoError(t, store.WriteEvents(ctx, tenantA, shardBatch(t, "invalid-a", 1, 7, 100), persistence.Unconditional()))

	events, next, err := store.GetShardEvents(ctx, persistence.Scope{}, 7, 0, 10)
	requireErrorIs(t, err, persistence.ErrInvalidScope, "an invalid scope must be rejected, not read globally")
	requireEmpty(t, events)
	requireEqual(t, int64(0), next)

	offsets, err := store.ShardOffsets(ctx, persistence.Scope{})
	requireErrorIs(t, err, persistence.ErrInvalidScope, "an invalid scope must be rejected, not read globally")
	requireEmpty(t, offsets)
}
