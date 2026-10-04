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

package eventsource

import (
	"context"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	"github.com/tochemey/goakt/v4/extension"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

// tenantContextFor builds the named tenant's TenantContext for a spec, failing
// the running case when the name is not a valid tenant.
func tenantContextFor(ctx *specs.Context, name tenancy.TenantID) tenancy.TenantContext {
	tc, err := tenancy.NewTenantContext(name)
	ctx.Expect(err).To(specs.BeNil())
	return tc
}

// TestEventSourcedActorMarshalEventWritesTenantMetadata covers tasks
// 2.1/2.2 (sdd/ego-tenant-002/tasks Phase 2): marshalEvent must serialize
// the per-command TenantContext it is given into the built *egopb.Event's
// TenantMetadata field, using tenancy.MarshalMetadata's exact ego.tenant.*
// keys (D9 carrier reuse), round-tripping through tenancy.UnmarshalMetadata
// for both a tenant-scoped and an administrative-scoped context. Legacy
// mode (tenantAware == false) must write no tenant metadata at all (D2).
func TestEventSourcedActorMarshalEventWritesTenantMetadata(t *testing.T) {
	specs.Describe(t, "marshalEvent writes the command's tenant onto the event envelope in tenant-aware mode only", func(s *specs.Spec) {
		s.It("legacy mode writes no tenant metadata", func(ctx *specs.Context) {
			entity := &Actor{persistenceID: "acct-1"}

			envelope, err := entity.marshalEvent(context.Background(), &testpb.AccountCreated{AccountId: "acct-1"}, tenancy.TenantContext{}, 1, time.Now(), 0)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(len(envelope.GetTenantMetadata())).ToEqual(0)
		})

		s.It("tenant-scoped context round-trips via tenancy.UnmarshalMetadata", func(ctx *specs.Context) {
			entity := &Actor{persistenceID: "acct-1", tenantAware: true}
			tc := tenantContextFor(ctx, "acme")

			envelope, err := entity.marshalEvent(context.Background(), &testpb.AccountCreated{AccountId: "acct-1"}, tc, 1, time.Now(), 0)
			ctx.Expect(err).To(specs.BeNil())

			ctx.Expect(len(envelope.GetTenantMetadata()) > 0).To(specs.BeTrue())
			ctx.Expect(envelope.GetTenantMetadata()).ToEqual(map[string]string(tenancy.MarshalMetadata(tc)))

			roundTripped, err := tenancy.UnmarshalMetadata(envelope.GetTenantMetadata())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(roundTripped).ToEqual(tc)
		})

		s.It("administrative-scoped context round-trips via tenancy.UnmarshalMetadata", func(ctx *specs.Context) {
			entity := &Actor{persistenceID: "acct-1", tenantAware: true}
			admin, err := tenancy.NewAdministrative("ops-team", "crypto-shred")
			ctx.Expect(err).To(specs.BeNil())
			tc, err := tenancy.NewAdministrativeContext(admin)
			ctx.Expect(err).To(specs.BeNil())

			envelope, err := entity.marshalEvent(context.Background(), &testpb.AccountCreated{AccountId: "acct-1"}, tc, 1, time.Now(), 0)
			ctx.Expect(err).To(specs.BeNil())

			roundTripped, err := tenancy.UnmarshalMetadata(envelope.GetTenantMetadata())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(roundTripped).ToEqual(tc)
		})
	})
}

// TestEventSourcedActorNewSnapshotEnvelopeWritesTenantMetadata covers
// tasks 2.3/2.4 (Phase 2, D5): newSnapshotEnvelope must write the actor's
// established tenant identity (entity.actorTenant) onto the built
// *egopb.Snapshot, so a snapshot taken without any in-flight command
// context (e.g. after a batch flush) still carries tenant identity for
// recover() (Phase 3) to seed from. Legacy mode writes nothing.
func TestEventSourcedActorNewSnapshotEnvelopeWritesTenantMetadata(t *testing.T) {
	specs.Describe(t, "newSnapshotEnvelope writes the actor's established tenant onto the snapshot in tenant-aware mode only", func(s *specs.Spec) {
		s.It("legacy mode writes no tenant metadata", func(ctx *specs.Context) {
			entity := &Actor{persistenceID: "acct-1", eventsCounter: 3, lastCommandTime: time.Now()}
			snapshot := entity.newSnapshotEnvelope(nil)
			ctx.Expect(len(snapshot.GetTenantMetadata())).ToEqual(0)
		})

		s.It("tenant-aware mode writes the actor's established tenant", func(ctx *specs.Context) {
			tc := tenantContextFor(ctx, "acme")

			entity := &Actor{
				persistenceID:   "acct-1",
				eventsCounter:   3,
				lastCommandTime: time.Now(),
				tenantAware:     true,
				actorTenant:     tc,
			}

			snapshot := entity.newSnapshotEnvelope(nil)
			ctx.Expect(len(snapshot.GetTenantMetadata()) > 0).To(specs.BeTrue())

			roundTripped, err := tenancy.UnmarshalMetadata(snapshot.GetTenantMetadata())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(roundTripped).ToEqual(tc)
		})
	})
}

// TestEventSourcedActorRecoverSeedsActorTenant covers tasks 3.1-3.6
// (Phase 3, D5/D6): recover() must seed entity.actorTenant from persisted
// tenant metadata before this actor accepts any command — from the latest
// event, from the snapshot when events are retention-deleted, cross-check
// the two when both exist, and fail closed (not open) when tenant-aware and
// metadata is absent or malformed on persisted data that exists.
func TestEventSourcedActorRecoverSeedsActorTenant(t *testing.T) {
	specs.Describe(t, "recover seeds the actor's tenant from persisted metadata and fails closed on missing or conflicting metadata", func(s *specs.Spec) {
		bg := context.Background()
		persistenceID := "acct-1"

		newEvent := func(ctx *specs.Context, seqNr uint64, tc tenancy.TenantContext) *egopb.Event {
			eventAny, err := anypb.New(&testpb.AccountCreated{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			evt := &egopb.Event{
				PersistenceId:  persistenceID,
				SequenceNumber: seqNr,
				Event:          eventAny,
				Timestamp:      time.Now().UnixNano(),
			}
			if tc != noTenantContext {
				evt.TenantMetadata = tenancy.MarshalMetadata(tc)
			}
			return evt
		}

		newSnapshot := func(ctx *specs.Context, seqNr uint64, tc tenancy.TenantContext) *egopb.Snapshot {
			stateAny, err := anypb.New(&testpb.Account{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			snap := &egopb.Snapshot{
				PersistenceId:  persistenceID,
				SequenceNumber: seqNr,
				State:          stateAny,
				Timestamp:      time.Now().Unix(),
			}
			if tc != noTenantContext {
				snap.TenantMetadata = tenancy.MarshalMetadata(tc)
			}
			return snap
		}

		s.It("3.1/3.2: seeds actorTenant from the latest event's metadata", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			eventStore := testkit.NewEventsStore()
			ctx.Expect(eventStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(eventStore.WriteEvents(bg, persistence.Unscoped(), []*egopb.Event{newEvent(ctx, 1, tenantA)}, persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountEventSourcedBehavior(persistenceID),
				eventsStore:   eventStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			ctx.Expect(entity.recover(bg)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(tenantA)
		})

		s.It("3.3/3.4: seeds actorTenant from the snapshot when events are retention-deleted", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			eventStore := testkit.NewEventsStore()
			ctx.Expect(eventStore.Connect(bg)).To(specs.BeNil())
			snapshotStore := testkit.NewSnapshotStore()
			ctx.Expect(snapshotStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(snapshotStore.WriteSnapshot(bg, persistence.Unscoped(), newSnapshot(ctx, 5, tenantA))).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountEventSourcedBehavior(persistenceID),
				eventsStore:   eventStore,
				snapshotStore: snapshotStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			ctx.Expect(entity.recover(bg)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(tenantA)
		})

		s.It("3.3/3.4: snapshot and latest event agreeing on tenant both succeed and match", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			eventStore := testkit.NewEventsStore()
			ctx.Expect(eventStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(eventStore.WriteEvents(bg, persistence.Unscoped(), []*egopb.Event{newEvent(ctx, 6, tenantA)}, persistence.Unconditional())).To(specs.BeNil())
			snapshotStore := testkit.NewSnapshotStore()
			ctx.Expect(snapshotStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(snapshotStore.WriteSnapshot(bg, persistence.Unscoped(), newSnapshot(ctx, 5, tenantA))).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountEventSourcedBehavior(persistenceID),
				eventsStore:   eventStore,
				snapshotStore: snapshotStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			ctx.Expect(entity.recover(bg)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(tenantA)
		})

		s.It("3.3/3.4: snapshot and latest event disagreeing on tenant fails closed with ErrDenied", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			tenantB := tenantContextFor(ctx, "globex")
			eventStore := testkit.NewEventsStore()
			ctx.Expect(eventStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(eventStore.WriteEvents(bg, persistence.Unscoped(), []*egopb.Event{newEvent(ctx, 6, tenantB)}, persistence.Unconditional())).To(specs.BeNil())
			snapshotStore := testkit.NewSnapshotStore()
			ctx.Expect(snapshotStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(snapshotStore.WriteSnapshot(bg, persistence.Unscoped(), newSnapshot(ctx, 5, tenantA))).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountEventSourcedBehavior(persistenceID),
				eventsStore:   eventStore,
				snapshotStore: snapshotStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			err := entity.recover(bg)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrDenied))
		})

		s.It("3.5/3.6: fails closed when tenant-aware and the latest event carries no tenant metadata", func(ctx *specs.Context) {
			eventStore := testkit.NewEventsStore()
			ctx.Expect(eventStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(eventStore.WriteEvents(bg, persistence.Unscoped(), []*egopb.Event{newEvent(ctx, 1, noTenantContext)}, persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountEventSourcedBehavior(persistenceID),
				eventsStore:   eventStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			err := entity.recover(bg)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrInvalid))
		})

		s.It("3.5/3.6: fails closed when tenant-aware and the snapshot carries no tenant metadata", func(ctx *specs.Context) {
			eventStore := testkit.NewEventsStore()
			ctx.Expect(eventStore.Connect(bg)).To(specs.BeNil())
			snapshotStore := testkit.NewSnapshotStore()
			ctx.Expect(snapshotStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(snapshotStore.WriteSnapshot(bg, persistence.Unscoped(), newSnapshot(ctx, 5, noTenantContext))).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountEventSourcedBehavior(persistenceID),
				eventsStore:   eventStore,
				snapshotStore: snapshotStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			err := entity.recover(bg)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrInvalid))
		})

		s.It("legacy mode never seeds actorTenant, even when metadata is present", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			eventStore := testkit.NewEventsStore()
			ctx.Expect(eventStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(eventStore.WriteEvents(bg, persistence.Unscoped(), []*egopb.Event{newEvent(ctx, 1, tenantA)}, persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountEventSourcedBehavior(persistenceID),
				eventsStore:   eventStore,
				scope:         persistence.Unscoped(),
			}

			ctx.Expect(entity.recover(bg)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(noTenantContext)
		})

		s.It("a brand new actor with no persisted data recovers without a tenant identity yet", func(ctx *specs.Context) {
			eventStore := testkit.NewEventsStore()
			ctx.Expect(eventStore.Connect(bg)).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountEventSourcedBehavior(persistenceID),
				eventsStore:   eventStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			ctx.Expect(entity.recover(bg)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(noTenantContext)
		})

		// The next two cases are PR1 review-comment regressions: recover()
		// only validated latestEvent's tenant metadata before this fix (D3's
		// fail-closed intent applied to the wrong event). replayEvents replayed
		// every event strictly between the snapshot point and latestSeqNr
		// unchecked, so an intermediate event belonging to another tenant, or
		// missing tenant metadata altogether, would be silently applied to
		// state as long as the *latest* event still carried the actor's own
		// tenant.
		s.It("an intermediate event belonging to a different tenant fails closed with ErrDenied", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			tenantB := tenantContextFor(ctx, "globex")
			eventStore := testkit.NewEventsStore()
			ctx.Expect(eventStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(eventStore.WriteEvents(bg, persistence.Unscoped(), []*egopb.Event{
				newEvent(ctx, 1, tenantA),
				newEvent(ctx, 2, tenantB),
				newEvent(ctx, 3, tenantA),
			}, persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountEventSourcedBehavior(persistenceID),
				eventsStore:   eventStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			err := entity.recover(bg)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrDenied))
		})

		s.It("an intermediate event with no tenant metadata fails closed with ErrInvalid", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			eventStore := testkit.NewEventsStore()
			ctx.Expect(eventStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(eventStore.WriteEvents(bg, persistence.Unscoped(), []*egopb.Event{
				newEvent(ctx, 1, tenantA),
				newEvent(ctx, 2, noTenantContext),
				newEvent(ctx, 3, tenantA),
			}, persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountEventSourcedBehavior(persistenceID),
				eventsStore:   eventStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			err := entity.recover(bg)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrInvalid))
		})
	})
}

// TestEventSourcedActorRecoverRejectsMismatchedSpawnBoundTenant covers
// TENANT-003 T4's D6 strengthening: before this ticket, recover()'s call to
// seedActorTenant was always a FIRST seed, because entity.actorTenant
// started at its zero value (noTenantContext) until a persisted record set
// it. Now, resolveScope (called from PreStart before recover ever runs)
// pre-seeds entity.actorTenant from the per-spawn
// extensions.EntityTenantScope dependency the engine injects at spawn. So
// when recover() later reaches a persisted event whose TenantMetadata names
// a DIFFERENT tenant than the one this actor was spawned for, seedActorTenant
// no longer accepts it as the seed — it is a VerifyUnchanged cross-check
// against the spawn-bound tenant, and a mismatch must fail closed.
//
// This is deliberately exercised through the real entity.resolveScope
// method (not by hand-setting entity.scope/actorTenant on the struct
// literal, as the sibling tests above do), so the test also proves
// resolveScope's own pre-seeding wiring, not just recover()'s cross-check.
func TestEventSourcedActorRecoverRejectsMismatchedSpawnBoundTenant(t *testing.T) {
	specs.Describe(t, "recover rejects a persisted event whose tenant differs from the spawn-bound tenant", func(s *specs.Spec) {
		s.It("fails closed with ErrDenied through the real resolveScope pre-seeding", func(ctx *specs.Context) {
			bg := context.Background()
			persistenceID := uuid.NewString()

			tenantB := tenantContextFor(ctx, "globex")

			scopeA, err := persistence.NewTenantScope("acme")
			ctx.Expect(err).To(specs.BeNil())

			eventStore := testkit.NewEventsStore()
			ctx.Expect(eventStore.Connect(bg)).To(specs.BeNil())

			// A record physically stored under scope A (as if this actor's own
			// prior spawn wrote it) but whose carried TenantMetadata names tenant
			// B — the corrupted/mismatched shape D6's cross-check exists to catch,
			// since scope-keyed storage alone cannot rule out a metadata field that
			// disagrees with its own storage key.
			eventAny, err := anypb.New(&testpb.AccountCreated{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			mismatched := &egopb.Event{
				PersistenceId:  persistenceID,
				SequenceNumber: 1,
				Event:          eventAny,
				Timestamp:      time.Now().UnixNano(),
				TenantMetadata: tenancy.MarshalMetadata(tenantB),
			}
			ctx.Expect(eventStore.WriteEvents(bg, scopeA, []*egopb.Event{mismatched}, persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountEventSourcedBehavior(persistenceID),
				eventsStore:   eventStore,
				tenantAware:   true,
			}

			// resolveScope pre-seeds entity.actorTenant to tenant A (acme) and
			// binds entity.scope to scopeA, exactly as PreStart does at spawn.
			ctx.Expect(entity.resolveScope([]extension.Dependency{extensions.NewEntityTenantScope("acme")})).To(specs.BeNil())

			// recover must fail closed when the recovered event's tenant
			// metadata disagrees with the spawn-bound tenant, and the
			// rejection must be VerifyUnchanged's ErrDenied, not a silent
			// adoption of the recovered tenant.
			err = entity.recover(bg)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrDenied))
		})
	})
}

// TestEventSourcedActorSeedActorTenant covers the seedActorTenant helper
// (design.md Interfaces/Contracts, EGO-TENANT-002) in isolation: a no-op in
// legacy mode, an unconditional first seed, and a VerifyUnchanged
// cross-check once already seeded.
func TestEventSourcedActorSeedActorTenant(t *testing.T) {
	specs.Describe(t, "seedActorTenant seeds the actor's tenant once and rejects a different tenant afterwards", func(s *specs.Spec) {
		s.It("legacy mode is a no-op", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			entity := &Actor{}
			ctx.Expect(entity.seedActorTenant(tenantA)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(noTenantContext)
		})

		s.It("first call unconditionally seeds", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			entity := &Actor{tenantAware: true}
			ctx.Expect(entity.seedActorTenant(tenantA)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(tenantA)
		})

		s.It("second call with the same tenant is a no-op success", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			entity := &Actor{tenantAware: true, actorTenant: tenantA}
			ctx.Expect(entity.seedActorTenant(tenantA)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(tenantA)
		})

		s.It("second call with a different tenant fails with ErrDenied", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			tenantB := tenantContextFor(ctx, "globex")
			entity := &Actor{tenantAware: true, actorTenant: tenantA}
			err := entity.seedActorTenant(tenantB)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrDenied))
			// a rejected re-seed must not overwrite the original
			ctx.Expect(entity.actorTenant).ToEqual(tenantA)
		})
	})
}

// TestEventSourcedActorProcessCommandAndReplyRejectsCrossTenant covers
// tasks 4.4/4.5 (Phase 4): the non-batched path (processCommandAndReply)
// must reject a command whose resolved TenantContext differs from this
// actor's already-seeded actorTenant, mirroring processAndBatch's gate —
// net-new enforcement per design.md risk #3 (this gate previously only
// proved presence via tenancy.Require, never identity match).
func TestEventSourcedActorProcessCommandAndReplyRejectsCrossTenant(t *testing.T) {
	// The empty Describe name keeps the old test name.
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("rejects a command of another tenant on the non-batched path", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(connectedEventsStore(ctx)),
				extensions.NewTenancyMarker(false))
			pid := rig.spawn(ctx, behavior, extensions.NewEntityTenantScope("acme"))

			tenantA := tenantContextFor(ctx, "acme")
			tenantB := tenantContextFor(ctx, "globex")

			// tenant A's first command must succeed and establish actorTenant
			stateReplyOf(ctx, askWith(ctx, attachTenant(ctx, tenantA), pid, &testpb.CreateAccount{AccountBalance: 500}))

			// Ask itself must not fail; the rejection is carried in the CommandReply
			reply := askWith(ctx, attachTenant(ctx, tenantB), pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 10})

			// a cross-tenant command on the non-batched path must be rejected with
			// the fail-closed tenant error VerifyUnchanged produces, not an invented
			// error type
			wantErr := tenancy.VerifyUnchanged(tenantA, tenantB)
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal(wantErr.Error()))
		})
	})
}

// TestEventSourcedActorGetStateCommandRejectsCrossTenant is a PR1
// review-comment regression: Receive dispatched *egopb.GetStateCommand
// straight to getStateAndReply, bypassing every tenant gate that
// processCommandAndReply/processAndBatch enforce. A resolved tenant B could
// read tenant A's full committed state by sending GetStateCommand instead
// of a real command. getStateAndReply must apply the same T4-A style gate:
// require a resolved TenantContext and reject one that mismatches the
// actor's already-established actorTenant.
func TestEventSourcedActorGetStateCommandRejectsCrossTenant(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("rejects a GetStateCommand of another tenant", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(connectedEventsStore(ctx)),
				extensions.NewTenancyMarker(false))
			pid := rig.spawn(ctx, behavior, extensions.NewEntityTenantScope("acme"))

			tenantA := tenantContextFor(ctx, "acme")
			tenantB := tenantContextFor(ctx, "globex")

			// tenant A's command must succeed and establish actorTenant
			stateReplyOf(ctx, askWith(ctx, attachTenant(ctx, tenantA), pid, &testpb.CreateAccount{AccountBalance: 500}))

			// Ask itself must not fail; the rejection is carried in the CommandReply
			reply := askWith(ctx, attachTenant(ctx, tenantB), pid, &egopb.GetStateCommand{})

			// the GetStateCommand from a different tenant must be rejected, not
			// return tenant A's state, with the fail-closed tenant error
			// VerifyUnchanged produces, not an invented error type
			wantErr := tenancy.VerifyUnchanged(tenantA, tenantB)
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal(wantErr.Error()))
		})
	})
}

// TestEventSourcedActorGetStateCommandRequiresTenantWhenTenantAware is a
// companion to the cross-tenant regression above: in tenant-aware mode, a
// GetStateCommand with no resolved TenantContext at all must also be
// rejected (tenancy.Require's absence path), matching
// processCommandAndReply's T4-A gate rather than silently returning state.
func TestEventSourcedActorGetStateCommandRequiresTenantWhenTenantAware(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("rejects a GetStateCommand with no resolved tenant", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(connectedEventsStore(ctx)),
				extensions.NewTenancyMarker(false))
			pid := rig.spawn(ctx, behavior, extensions.NewEntityTenantScope("acme"))

			stateReplyOf(ctx, askWith(ctx, attachTenant(ctx, tenantContextFor(ctx, "acme")), pid, &testpb.CreateAccount{AccountBalance: 500}))

			// Ask itself must not fail; the rejection is carried in the
			// CommandReply. A GetStateCommand with no resolved tenant must be
			// rejected on a tenant-aware actor.
			reply := ask(ctx, pid, &egopb.GetStateCommand{})
			errorReplyMessage(ctx, reply)
		})
	})
}

// TestEventSourcedActorTenantIdentitySurvivesRestart covers task 4.6: a
// cross-tenant command must still be rejected after this actor restarts and
// recovers its tenant identity from persisted event metadata (Phase 3),
// not only while the original in-memory actorTenant is still warm.
func TestEventSourcedActorTenantIdentitySurvivesRestart(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("rejects a cross-tenant command after the actor restarts", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(connectedEventsStore(ctx)),
				extensions.NewTenancyMarker(false))

			tenantA := tenantContextFor(ctx, "acme")
			tenantB := tenantContextFor(ctx, "globex")

			// First actor instance: tenant A persists, establishing actorTenant,
			// then is stopped so no in-memory state survives.
			pid := rig.spawn(ctx, behavior, extensions.NewEntityTenantScope("acme"))
			stateReplyOf(ctx, askWith(ctx, attachTenant(ctx, tenantA), pid, &testpb.CreateAccount{AccountBalance: 500}))

			ctx.Expect(rig.system.Kill(context.Background(), behavior.ID())).To(specs.BeNil())
			waitStopped(ctx, pid)

			// the name is free again once the system forgets the stopped actor
			ctx.Eventually(func() any {
				exists, err := rig.system.ActorExists(context.Background(), behavior.ID())
				if err != nil {
					return err
				}
				return exists
			}, specs.BeFalse(), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))

			// Second actor instance under the same persistence ID: recover() must
			// seed actorTenant from the persisted event before this new in-process
			// actor accepts any command.
			restarted := rig.spawn(ctx, behavior, extensions.NewEntityTenantScope("acme"))

			// Ask itself must not fail; the rejection is carried in the CommandReply
			reply := askWith(ctx, attachTenant(ctx, tenantB), restarted, &testpb.CreditAccount{AccountId: persistenceID, Balance: 10})

			// tenant identity recovered from persisted event metadata must survive
			// the actor restart, and the rejection must be the fail-closed tenant
			// error VerifyUnchanged produces, not an invented error type
			wantErr := tenancy.VerifyUnchanged(tenantA, tenantB)
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal(wantErr.Error()))
		})
	})
}
