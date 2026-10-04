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

package durablestate

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	behaviorport "github.com/getsyntegrity/urd/port/behavior"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

// flakyFirstCommandDurableStateBehavior fails the very first HandleCommand
// invocation it ever receives, as any real behavior might on invalid input,
// and behaves like a normal account behavior on every subsequent call. It
// proves that a failed first command must not appropriate the actor for the
// tenant that sent it (DS2 P1 #1 regression): a later tenant's valid command
// must still be free to claim the actor.
type flakyFirstCommandDurableStateBehavior struct {
	id string

	mu    sync.Mutex
	calls int
}

var _ behaviorport.DurableState = (*flakyFirstCommandDurableStateBehavior)(nil)

func newFlakyFirstCommandDurableStateBehavior(id string) *flakyFirstCommandDurableStateBehavior {
	return &flakyFirstCommandDurableStateBehavior{id: id}
}

func (x *flakyFirstCommandDurableStateBehavior) ID() string {
	return x.id
}

func (x *flakyFirstCommandDurableStateBehavior) InitialState() State {
	return new(testpb.Account)
}

// nolint
func (x *flakyFirstCommandDurableStateBehavior) HandleCommand(_ context.Context, command Command, priorVersion uint64, _ State) (State, uint64, error) {
	x.mu.Lock()
	x.calls++
	isFirstCall := x.calls == 1
	x.mu.Unlock()

	if isFirstCall {
		return nil, 0, errors.New("simulated failure on the first command")
	}

	switch cmd := command.(type) {
	case *testpb.CreateAccount:
		return &testpb.Account{
			AccountId:      x.id,
			AccountBalance: cmd.GetAccountBalance(),
		}, priorVersion + 1, nil
	default:
		return nil, 0, errors.New("unhandled command")
	}
}

// callCount reports how many times HandleCommand has run so far.
func (x *flakyFirstCommandDurableStateBehavior) callCount() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.calls
}

func (x *flakyFirstCommandDurableStateBehavior) MarshalBinary() ([]byte, error) {
	return json.Marshal(struct {
		ID string `json:"id"`
	}{ID: x.id})
}

func (x *flakyFirstCommandDurableStateBehavior) UnmarshalBinary(data []byte) error {
	aux := struct {
		ID string `json:"id"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	x.id = aux.ID
	return nil
}

// tenantContextFor builds the named tenant's TenantContext for a spec, failing
// the running case when the name is not a valid tenant.
func tenantContextFor(ctx *specs.Context, name tenancy.TenantID) tenancy.TenantContext {
	tc, err := tenancy.NewTenantContext(name)
	ctx.Expect(err).To(specs.BeNil())
	return tc
}

// tenantScopeFor builds the named tenant's persistence scope for a spec,
// failing the running case when the name is not a valid tenant.
func tenantScopeFor(ctx *specs.Context, name tenancy.TenantID) persistence.Scope {
	scope, err := persistence.NewTenantScope(name)
	ctx.Expect(err).To(specs.BeNil())
	return scope
}

// TestDurableStateActorRecoverFromStoreSeedsActorTenant covers tasks.md
// Phase 1 (DS2): recoverFromStore must seed entity.actorTenant from the
// recovered record's carried tenant metadata before its state payload is
// touched, leave a genesis actor unseeded without error, and fail closed
// (ErrInvalid) when a tenant-aware actor recovers a non-genesis record whose
// tenant metadata is absent or malformed. Legacy mode never seeds.
func TestDurableStateActorRecoverFromStoreSeedsActorTenant(t *testing.T) {
	specs.Describe(t, "recoverFromStore seeds the actor's tenant from the recovered record and fails closed on missing or malformed metadata", func(s *specs.Spec) {
		bg := context.Background()
		persistenceID := "acct-1"

		newDurableState := func(ctx *specs.Context, md tenancy.Metadata) *egopb.DurableState {
			stateAny, err := anypb.New(&testpb.Account{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			return &egopb.DurableState{
				PersistenceId:  persistenceID,
				VersionNumber:  1,
				ResultingState: stateAny,
				Timestamp:      time.Now().UnixNano(),
				TenantMetadata: md,
			}
		}

		s.It("seeds actorTenant from valid persisted metadata", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			durableStore := testkit.NewDurableStore()
			ctx.Expect(durableStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(durableStore.WriteState(bg, persistence.Unscoped(), newDurableState(ctx, tenancy.MarshalMetadata(tenantA)), persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountDurableStateBehavior(persistenceID),
				stateStore:    durableStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			ctx.Expect(entity.recoverFromStore(bg)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(tenantA)
		})

		s.It("genesis leaves actorTenant unseeded without error", func(ctx *specs.Context) {
			durableStore := testkit.NewDurableStore()
			ctx.Expect(durableStore.Connect(bg)).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountDurableStateBehavior(persistenceID),
				stateStore:    durableStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			ctx.Expect(entity.recoverFromStore(bg)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(noTenantContext)
		})

		s.It("fails closed when tenant-aware and persisted metadata is absent", func(ctx *specs.Context) {
			durableStore := testkit.NewDurableStore()
			ctx.Expect(durableStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(durableStore.WriteState(bg, persistence.Unscoped(), newDurableState(ctx, nil), persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountDurableStateBehavior(persistenceID),
				stateStore:    durableStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			err := entity.recoverFromStore(bg)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrInvalid))
		})

		s.It("fails closed when tenant-aware and persisted metadata is malformed", func(ctx *specs.Context) {
			durableStore := testkit.NewDurableStore()
			ctx.Expect(durableStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(durableStore.WriteState(bg, persistence.Unscoped(), newDurableState(ctx, tenancy.Metadata{"ego.tenant.scope": "not-a-real-scope"}), persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountDurableStateBehavior(persistenceID),
				stateStore:    durableStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			err := entity.recoverFromStore(bg)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrInvalid))
		})

		s.It("legacy mode never seeds actorTenant, even when metadata is present", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			durableStore := testkit.NewDurableStore()
			ctx.Expect(durableStore.Connect(bg)).To(specs.BeNil())
			ctx.Expect(durableStore.WriteState(bg, persistence.Unscoped(), newDurableState(ctx, tenancy.MarshalMetadata(tenantA)), persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountDurableStateBehavior(persistenceID),
				stateStore:    durableStore,
				scope:         persistence.Unscoped(),
			}

			ctx.Expect(entity.recoverFromStore(bg)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(noTenantContext)
		})
	})
}

// TestDurableStateActorProcessCommandRejectsCrossTenant covers tasks.md
// Phase 2 (DS1): the T4-A pre-handler gate must reject a command whose
// resolved TenantContext differs from this actor's already-established
// actorTenant, before HandleCommand ever runs — Actor had no
// such identity check before PR2, only tenancy.Require's presence-only
// proof (see TestDurableStateActorTenancyGate).
func TestDurableStateActorProcessCommandRejectsCrossTenant(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("rejects a command from a tenant other than the established one before HandleCommand runs", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			persistenceID := uuid.NewString()
			behavior := enginetest.NewTenancyProbeDurableStateBehavior(persistenceID)

			rig := startActorRig(ctx, durableStore, extensions.NewTenancyMarker(false))
			pid := rig.spawnForTenant(ctx, behavior, "acme")

			tenantA := tenantContextFor(ctx, "acme")
			tenantB := tenantContextFor(ctx, "globex")

			// tenant A's first command must succeed and establish actorTenant
			first := ask(ctx, attachedTo(ctx, tenantA), pid, &testpb.CreateAccount{AccountBalance: 500})
			ctx.Expect(isStateReply(first)).To(specs.BeTrue())

			// the rejection is carried in the CommandReply, not in the Ask error
			second := ask(ctx, attachedTo(ctx, tenantB), pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 10})
			message, isError := errorReplyMessage(second)
			ctx.Expect(isError).To(specs.BeTrue())

			// the rejection must be the fail-closed tenant error VerifyUnchanged
			// produces, not an invented error type
			ctx.Expect(message).ToEqual(tenancy.VerifyUnchanged(tenantA, tenantB).Error())
			// HandleCommand must not run for the rejected cross-tenant command
			ctx.Expect(behavior.InvocationCount()).ToEqual(1)
		})
	})
}

// TestDurableStateActorPersistStateAndPublishWritesTenantMetadata covers
// tasks.md Phase 3 (DS3): persistStateAndPublish must write
// entity.actorTenant's metadata onto the persisted DurableState using
// tenancy.MarshalMetadata's exact ego.tenant.* keys, and write nothing in
// legacy mode.
func TestDurableStateActorPersistStateAndPublishWritesTenantMetadata(t *testing.T) {
	specs.Describe(t, "persistStateAndPublish writes the actor's established tenant onto the persisted state in tenant-aware mode only", func(s *specs.Spec) {
		bg := context.Background()
		persistenceID := "acct-1"

		newEntity := func(ctx *specs.Context, tenantAware bool, tc tenancy.TenantContext, scope persistence.Scope, store *testkit.DurableStore, stream eventstream.Stream) *Actor {
			state := &testpb.Account{AccountId: persistenceID, AccountBalance: 100}
			cachedAny, err := anypb.New(state)
			ctx.Expect(err).To(specs.BeNil())
			return &Actor{
				persistenceID:   persistenceID,
				currentState:    state,
				cachedStateAny:  cachedAny,
				currentVersion:  1,
				lastCommandTime: time.Now(),
				stateStore:      store,
				eventsStream:    stream,
				tenantAware:     tenantAware,
				actorTenant:     tc,
				scope:           scope,
			}
		}

		s.It("legacy mode writes no tenant metadata", func(ctx *specs.Context) {
			durableStore := testkit.NewDurableStore()
			ctx.Expect(durableStore.Connect(bg)).To(specs.BeNil())
			eventStream := eventstream.New()

			entity := newEntity(ctx, false, noTenantContext, persistence.Unscoped(), durableStore, eventStream)
			ctx.Expect(entity.persistStateAndPublish(bg)).To(specs.BeNil())

			latest, err := durableStore.GetLatestState(bg, persistence.Unscoped(), persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest == nil).To(specs.BeFalse())
			ctx.Expect(len(latest.GetTenantMetadata())).ToEqual(0)

			eventStream.Close()
			ctx.Expect(durableStore.Disconnect(bg)).To(specs.BeNil())
		})

		s.It("tenant-aware mode writes the actor's established tenant", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			scopeA, err := persistence.NewTenantScope("acme")
			ctx.Expect(err).To(specs.BeNil())

			durableStore := testkit.NewDurableStore()
			ctx.Expect(durableStore.Connect(bg)).To(specs.BeNil())
			eventStream := eventstream.New()

			entity := newEntity(ctx, true, tenantA, scopeA, durableStore, eventStream)
			ctx.Expect(entity.persistStateAndPublish(bg)).To(specs.BeNil())

			latest, err := durableStore.GetLatestState(bg, scopeA, persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest == nil).To(specs.BeFalse())
			ctx.Expect(len(latest.GetTenantMetadata()) > 0).To(specs.BeTrue())
			ctx.Expect(latest.GetTenantMetadata()).ToEqual(map[string]string(tenancy.MarshalMetadata(tenantA)))

			roundTripped, err := tenancy.UnmarshalMetadata(latest.GetTenantMetadata())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(roundTripped).ToEqual(tenantA)

			eventStream.Close()
			ctx.Expect(durableStore.Disconnect(bg)).To(specs.BeNil())
		})
	})
}

// TestDurableStateActorPostStopTenantPersist covers tasks.md Phase 3's four
// mandatory DS3 tests for the lifecycle flush: a tenant-aware actor that
// never established a tenant identity (genesis, no command ever handled)
// must skip PostStop's persist entirely rather than write a DurableState
// whose tenant_metadata is empty (which recoverFromStore/DS2 would then
// refuse to recover from — a bricking bug); a tenant-aware, seeded actor
// must have PostStop persist with the established tenant's metadata;
// legacy mode keeps today's unconditional flush even for a never-touched
// genesis actor (regression); and an actor that fails recovery because its
// persisted tenant metadata was invalid never reaches PostStop's persist at
// all, because it never finishes PreStart.
func TestDurableStateActorPostStopTenantPersist(t *testing.T) {
	// The empty Describe name keeps the old subtest names.
	specs.Describe(t, "", func(s *specs.Spec) {
		bg := context.Background()

		s.It("tenant-aware and never-seeded: PostStop does not persist", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountDurableStateBehavior(persistenceID)
			scopeA := tenantScopeFor(ctx, "acme")

			// Ping and GetLatestState run once per PreStart attempt, and the
			// actor system retries PreStart (WithActorInitMaxRetries), and
			// Ping runs again in PostStop, so none of them has an exact count.
			ctrl := mock.NewController(ctx)
			ctrl.Method("Ping").Expect(mock.Any()).Return(nil).AtLeast(1)
			ctrl.Method("GetLatestState").Expect(mock.Any(), scopeA, behavior.ID()).Return(nil, nil).AtLeast(1)
			// PostStop must not persist: a WriteState here fails the case.
			ctrl.Method("WriteState").Expect(mock.Any(), mock.Any(), mock.Any(), mock.Any()).Never()

			rig := startActorRig(ctx, enginetest.NewStateStoreMock(ctrl), extensions.NewTenancyMarker(false))
			pid := rig.spawnForTenant(ctx, behavior, "acme")

			// Kill returns after PostStop ran, so the Never expectation above
			// has seen every call PostStop makes by the time the case ends.
			rig.kill(ctx, pid, behavior.ID())
		})

		s.It("tenant-aware and seeded: PostStop writes with the established tenant", func(ctx *specs.Context) {
			tenantA := tenantContextFor(ctx, "acme")
			scopeA := tenantScopeFor(ctx, "acme")

			durableStore := connectedDurableStore(ctx)
			persistenceID := uuid.NewString()
			behavior := enginetest.NewTenancyProbeDurableStateBehavior(persistenceID)

			rig := startActorRig(ctx, durableStore, extensions.NewTenancyMarker(false))
			pid := rig.spawnForTenant(ctx, behavior, "acme")

			reply := ask(ctx, attachedTo(ctx, tenantA), pid, &testpb.CreateAccount{AccountBalance: 500})
			ctx.Expect(isStateReply(reply)).To(specs.BeTrue())

			rig.kill(ctx, pid, behavior.ID())

			ctx.Eventually(latestState(durableStore, scopeA, persistenceID), bePersisted(),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			latest, err := durableStore.GetLatestState(bg, scopeA, persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest.GetTenantMetadata()).To(specs.Not(specs.BeEmpty()))
			ctx.Expect(latest.GetTenantMetadata()).ToEqual(map[string]string(tenancy.MarshalMetadata(tenantA)))
		})

		s.It("legacy mode keeps the unconditional flush, even for a never-touched genesis actor", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountDurableStateBehavior(persistenceID)

			rig := startActorRig(ctx, durableStore)
			pid := rig.spawn(ctx, behavior)

			rig.kill(ctx, pid, behavior.ID())

			// legacy mode must flush on PostStop even without ever handling a command
			ctx.Eventually(latestState(durableStore, persistence.Unscoped(), persistenceID), bePersisted(),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			latest, err := durableStore.GetLatestState(bg, persistence.Unscoped(), persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest.GetTenantMetadata()).To(specs.BeEmpty())
		})

		s.It("an actor that fails recovery on invalid tenant metadata never reaches PostStop's persist", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountDurableStateBehavior(persistenceID)

			stateAny, err := anypb.New(&testpb.Account{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			recorded := &egopb.DurableState{
				PersistenceId:  persistenceID,
				VersionNumber:  1,
				ResultingState: stateAny,
				Timestamp:      time.Now().UnixNano(),
				// TenantMetadata deliberately absent: a non-genesis record on a
				// tenant-aware actor must be refused, not silently recovered.
			}
			scopeA := tenantScopeFor(ctx, "acme")

			// As above, PreStart is retried, so Ping and GetLatestState have
			// no exact count.
			ctrl := mock.NewController(ctx)
			ctrl.Method("Ping").Expect(mock.Any()).Return(nil).AtLeast(1)
			ctrl.Method("GetLatestState").Expect(mock.Any(), scopeA, behavior.ID()).Return(recorded, nil).AtLeast(1)
			// PostStop is never invoked for an actor whose PreStart never
			// completed, so no WriteState may happen.
			ctrl.Method("WriteState").Expect(mock.Any(), mock.Any(), mock.Any(), mock.Any()).Never()

			rig := startActorRig(ctx, enginetest.NewStateStoreMock(ctrl), extensions.NewTenancyMarker(false))
			// Spawn returns only after PreStart gave up, so there is nothing
			// left to wait for: PreStart must fail closed on invalid persisted
			// tenant metadata.
			pid, err := rig.trySpawn(behavior, extensions.NewEntityTenantScope("acme"))
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(pid).To(specs.BeNil())
		})
	})
}

// TestDurableStateActorGetStateCommandTenancyGate covers tasks.md Phase 4
// (DS4): getStateAndReply must apply the same gate processCommand's T4-A
// does, since Receive dispatches *egopb.GetStateCommand directly, bypassing
// processCommand entirely — without this, a resolved tenant could read
// another tenant's full committed durable state.
func TestDurableStateActorGetStateCommandTenancyGate(t *testing.T) {
	// The empty Describe name keeps the old subtest names.
	specs.Describe(t, "", func(s *specs.Spec) {
		bg := context.Background()

		// spawnBoundToAcme starts a tenant-aware actor bound to "acme" whose
		// first command, from tenant A, has already succeeded.
		spawnBoundToAcme := func(ctx *specs.Context) (pid *goakt.PID, tenantA, tenantB tenancy.TenantContext) {
			durableStore := connectedDurableStore(ctx)
			behavior := enginetest.NewTenancyProbeDurableStateBehavior(uuid.NewString())

			rig := startActorRig(ctx, durableStore, extensions.NewTenancyMarker(false))
			pid = rig.spawnForTenant(ctx, behavior, "acme")

			tenantA = tenantContextFor(ctx, "acme")
			tenantB = tenantContextFor(ctx, "globex")

			// tenant A's command must succeed and establish actorTenant
			first := ask(ctx, attachedTo(ctx, tenantA), pid, &testpb.CreateAccount{AccountBalance: 500})
			ctx.Expect(isStateReply(first)).To(specs.BeTrue())
			return pid, tenantA, tenantB
		}

		s.It("foreign tenant is rejected", func(ctx *specs.Context) {
			pid, tenantA, tenantB := spawnBoundToAcme(ctx)

			// the rejection is carried in the CommandReply, not in the Ask error
			reply := ask(ctx, attachedTo(ctx, tenantB), pid, &egopb.GetStateCommand{})
			message, isError := errorReplyMessage(reply)
			// GetStateCommand from a different tenant must be rejected, not return tenant A's state
			ctx.Expect(isError).To(specs.BeTrue())

			// the rejection must be the fail-closed tenant error VerifyUnchanged
			// produces, not an invented error type
			ctx.Expect(message).ToEqual(tenancy.VerifyUnchanged(tenantA, tenantB).Error())
		})

		s.It("missing tenant context is rejected", func(ctx *specs.Context) {
			pid, _, _ := spawnBoundToAcme(ctx)

			reply := ask(ctx, bg, pid, &egopb.GetStateCommand{})
			_, isError := errorReplyMessage(reply)
			// GetStateCommand with no resolved tenant must be rejected on a tenant-aware actor
			ctx.Expect(isError).To(specs.BeTrue())
		})

		s.It("matching tenant succeeds", func(ctx *specs.Context) {
			pid, tenantA, _ := spawnBoundToAcme(ctx)

			reply := ask(ctx, attachedTo(ctx, tenantA), pid, &egopb.GetStateCommand{})
			ctx.Expect(isStateReply(reply)).To(specs.BeTrue())
		})
	})
}

// TestDurableStateActorFailedFirstCommandDoesNotAppropriateActor covers PR2
// review round 2's P1 #1, updated for TENANT-003 T4's spawn-time binding:
// establishActorTenant used to run before HandleCommand ever executed or
// validated a command, so a tenant whose first command failed HandleCommand
// (or produced an invalid state/version) would appropriate the actor
// without ever committing a mutation. Under the commitState fix, ownership
// only lands together with a successfully committed state and version.
//
// TENANT-003 T4 changes WHERE ownership comes from: an actor is now bound
// to its tenant at SPAWN (via the injected extensions.EntityTenantScope
// dependency, pre-seeding entity.actorTenant before any command runs), not
// lazily on first successful commit. So a DIFFERENT tenant can no longer
// "claim" this actor after the spawn-bound tenant's first command fails —
// that tenant was never eligible to begin with, regardless of how the
// spawn-bound tenant's commands turn out. This test now proves: the
// spawn-bound tenant's failed first command does not corrupt in-memory
// state or write a durable record, the SAME spawn-bound tenant's retry
// still succeeds afterward, and a foreign tenant is rejected throughout —
// before AND after the spawn-bound tenant's successful commit, and again
// after a restart.
func TestDurableStateActorFailedFirstCommandDoesNotAppropriateActor(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		bg := context.Background()

		s.It("keeps the spawn-bound tenant as owner across a failed first command, a retry and a restart", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			persistenceID := uuid.NewString()
			behavior := newFlakyFirstCommandDurableStateBehavior(persistenceID)

			rig := startActorRig(ctx, durableStore, extensions.NewTenancyMarker(false))
			pid := rig.spawnForTenant(ctx, behavior, "acme")

			tenantA := tenantContextFor(ctx, "acme")
			tenantB := tenantContextFor(ctx, "globex")
			scopeA := tenantScopeFor(ctx, "acme")
			ctxA := attachedTo(ctx, tenantA)
			ctxB := attachedTo(ctx, tenantB)

			// tenant A's first command must fail HandleCommand; the failure is
			// carried in the CommandReply, not in the Ask error
			reply := ask(ctx, ctxA, pid, &testpb.CreateAccount{AccountBalance: 500})
			_, isError := errorReplyMessage(reply)
			ctx.Expect(isError).To(specs.BeTrue())
			ctx.Expect(behavior.callCount()).ToEqual(1)

			// a failed first command must never write a durable record
			latest, err := durableStore.GetLatestState(bg, scopeA, persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.BeNil())

			// Tenant B was never the spawn-bound tenant, so it is rejected here
			// too: this actor was already bound to tenant A at spawn,
			// independent of whether tenant A's own commands succeed.
			reply = ask(ctx, ctxB, pid, &testpb.CreateAccount{AccountBalance: 700})
			_, isError = errorReplyMessage(reply)
			ctx.Expect(isError).To(specs.BeTrue())
			// a rejected cross-tenant command must never reach HandleCommand
			ctx.Expect(behavior.callCount()).ToEqual(1)

			// the spawn-bound tenant retries and must still be able to claim
			// the actor after its own earlier failed command
			reply = ask(ctx, ctxA, pid, &testpb.CreateAccount{AccountBalance: 900})
			ctx.Expect(isStateReply(reply)).To(specs.BeTrue())
			ctx.Expect(behavior.callCount()).ToEqual(2)

			// tenant B remains rejected once tenant A owns the actor
			reply = ask(ctx, ctxB, pid, &testpb.CreateAccount{AccountBalance: 1100})
			_, isError = errorReplyMessage(reply)
			ctx.Expect(isError).To(specs.BeTrue())
			ctx.Expect(behavior.callCount()).ToEqual(2)

			// Restart: PostStop must persist tenant A's ownership (currentVersion >
			// 0, established together with the committed state), and recovery must
			// cross-check the recovered metadata against the restarted instance's
			// own spawn-bound tenant (also A here).
			rig.killForRestart(ctx, pid, behavior.ID())

			// tenant A's committed state must survive PostStop
			ctx.Eventually(latestState(durableStore, scopeA, persistenceID), bePersisted(),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			latest, err = durableStore.GetLatestState(bg, scopeA, persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest.GetVersionNumber()).ToEqual(uint64(1))
			ctx.Expect(latest.GetTenantMetadata()).ToEqual(map[string]string(tenancy.MarshalMetadata(tenantA)))

			pid = rig.spawnForTenant(ctx, behavior, "acme")

			// tenant B must still be rejected after restart: recovery
			// cross-checked tenant A as the owner
			reply = ask(ctx, ctxB, pid, &testpb.CreateAccount{AccountBalance: 1300})
			_, isError = errorReplyMessage(reply)
			ctx.Expect(isError).To(specs.BeTrue())
		})
	})
}

// TestDurableStateActorRecoverFromStoreLegacyVersionZeroGenesis covers PR2
// review round 2's P1 #2: a legacy installation's PostStop used to flush
// InitialState() unconditionally even when the actor never handled a
// command, so a version-0 record can carry a real (non-empty) payload with
// no tenant metadata at all. In tenant-aware mode this must be treated as
// genesis without an owner — not a compromised record that permanently
// blocks recovery — while a committed (version > 0) record must still fail
// closed on missing or invalid metadata. This also exercises the full
// restart path: after recovering as genesis, the first command must be free
// to claim the actor for whichever tenant sends it, and that ownership must
// itself survive a further restart.
func TestDurableStateActorRecoverFromStoreLegacyVersionZeroGenesis(t *testing.T) {
	// The empty Describe name keeps the old subtest names.
	specs.Describe(t, "", func(s *specs.Spec) {
		bg := context.Background()
		persistenceID := uuid.NewString()

		// newLegacyRecord builds a fresh record each call: the underlying store
		// keeps whatever pointer it is handed, so sharing one instance across
		// cases would let an earlier case's mutation leak into a later one.
		newLegacyRecord := func(ctx *specs.Context, version uint64) *egopb.DurableState {
			legacyStateAny, err := anypb.New(&testpb.Account{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			return &egopb.DurableState{
				PersistenceId:  persistenceID,
				VersionNumber:  version,
				ResultingState: legacyStateAny,
				Timestamp:      time.Now().UnixNano(),
				// TenantMetadata deliberately absent: this is exactly what a
				// pre-tenancy PostStop used to persist unconditionally.
			}
		}

		s.It("recoverFromStore treats it as genesis, not a fail-closed rejection", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			ctx.Expect(durableStore.WriteState(bg, persistence.Unscoped(), newLegacyRecord(ctx, 0), persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountDurableStateBehavior(persistenceID),
				stateStore:    durableStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			ctx.Expect(entity.recoverFromStore(bg)).To(specs.BeNil())
			ctx.Expect(entity.actorTenant).ToEqual(noTenantContext)
			ctx.Expect(entity.currentVersion).ToEqual(uint64(0))
			// the legacy payload must be discarded, not unmarshaled, at version 0
			ctx.Expect(entity.currentState).ToEqual(entity.behavior.InitialState())
		})

		s.It("a committed (version > 0) record still fails closed on missing metadata", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			ctx.Expect(durableStore.WriteState(bg, persistence.Unscoped(), newLegacyRecord(ctx, 1), persistence.Unconditional())).To(specs.BeNil())

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountDurableStateBehavior(persistenceID),
				stateStore:    durableStore,
				tenantAware:   true,
				scope:         persistence.Unscoped(),
			}

			err := entity.recoverFromStore(bg)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrInvalid))
		})

		s.It("end-to-end: a full actor spawns clean off a legacy version-0 record and a command still claims it", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			behavior := enginetest.NewTenancyProbeDurableStateBehavior(persistenceID)
			ctx.Expect(durableStore.WriteState(bg, persistence.Unscoped(), newLegacyRecord(ctx, 0), persistence.Unconditional())).To(specs.BeNil())

			rig := startActorRig(ctx, durableStore, extensions.NewTenancyMarker(false))
			// recovering a legacy version-0 record must not block Spawn/PreStart
			// in tenant-aware mode
			pid := rig.spawnForTenant(ctx, behavior, "acme")

			tenantA := tenantContextFor(ctx, "acme")
			scopeA := tenantScopeFor(ctx, "acme")

			// a genesis actor recovered from a legacy version-0 record must
			// still accept its first command
			reply := ask(ctx, attachedTo(ctx, tenantA), pid, &testpb.CreateAccount{AccountBalance: 500})
			ctx.Expect(isStateReply(reply)).To(specs.BeTrue())

			rig.kill(ctx, pid, behavior.ID())

			// the newly committed ownership must survive PostStop after
			// recovering from legacy genesis
			ctx.Eventually(latestState(durableStore, scopeA, persistenceID), bePersisted(),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			latest, err := durableStore.GetLatestState(bg, scopeA, persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest.GetVersionNumber()).ToEqual(uint64(1))
			ctx.Expect(latest.GetTenantMetadata()).ToEqual(map[string]string(tenancy.MarshalMetadata(tenantA)))
		})
	})
}
