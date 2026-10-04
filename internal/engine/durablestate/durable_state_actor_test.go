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
	"testing"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	behaviorport "github.com/getsyntegrity/urd/port/behavior"
	"github.com/getsyntegrity/urd/tenancy"
)

// errStoreFailure is the error the mocked state store returns in the failure
// cases.
var errStoreFailure = errors.New("durable state actor test: state store failure")

// expectAccountState asserts that reply carries a state reply at sequence
// number sequence whose state is the account id with the given balance.
func expectAccountState(ctx *specs.Context, reply *egopb.CommandReply, sequence uint64, id string, balance float64) {
	stateReply, ok := reply.GetReply().(*egopb.CommandReply_StateReply)
	ctx.Expect(ok).To(specs.BeTrue())
	ctx.Expect(stateReply.StateReply.GetSequenceNumber()).ToEqual(sequence)

	resulting := new(testpb.Account)
	ctx.Expect(stateReply.StateReply.GetState().UnmarshalTo(resulting)).To(specs.BeNil())
	expected := &testpb.Account{AccountId: id, AccountBalance: balance}
	ctx.Expect(proto.Equal(expected, resulting)).To(specs.BeTrue())
}

func TestDurableStateActorPreStartExtensions(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("returns an error instead of panicking when the telemetry extension is registered with an unexpected type", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			rig := startActorRig(ctx, durableStore,
				&enginetest.MistypedExtension{Name: extensions.TelemetryExtensionID})

			pid, err := rig.system.Spawn(context.Background(), "durable-state-mistyped-telemetry", New())
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(pid).To(specs.BeNil())
			ctx.Expect(err).To(specs.MatchError(extensions.ErrMissingRequiredExtensions))
		})
	})
}

func TestDurableStateBehavior(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		bg := context.Background()

		s.It("with state reply", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountDurableStateBehavior(persistenceID)
			rig := startActorRig(ctx, durableStore)
			pid := rig.spawn(ctx, behavior)

			reply := ask(ctx, bg, pid, &testpb.CreateAccount{AccountBalance: 500.00})
			expectAccountState(ctx, reply, 1, persistenceID, 500.00)

			// credit the balance
			reply = ask(ctx, bg, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 250})
			expectAccountState(ctx, reply, 2, persistenceID, 750.00)
		})

		s.It("with error reply", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountDurableStateBehavior(persistenceID)
			rig := startActorRig(ctx, durableStore)
			pid := rig.spawn(ctx, behavior)

			reply := ask(ctx, bg, pid, &testpb.CreateAccount{AccountBalance: 500.00})
			expectAccountState(ctx, reply, 1, persistenceID, 500.00)

			// a command addressed to another entity is rejected in the reply
			reply = ask(ctx, bg, pid, &testpb.CreditAccount{AccountId: "different-id", Balance: 250})
			message, isError := errorReplyMessage(reply)
			ctx.Expect(isError).To(specs.BeTrue())
			ctx.Expect(message).To(specs.Equal("command sent to the wrong entity"))
		})

		s.It("with state recovery from state store", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountDurableStateBehavior(persistenceID)
			rig := startActorRig(ctx, durableStore)
			pid := rig.spawn(ctx, behavior)

			reply := ask(ctx, bg, pid, &testpb.CreateAccount{AccountBalance: 500.00})
			expectAccountState(ctx, reply, 1, persistenceID, 500.00)

			reply = ask(ctx, bg, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 250})
			expectAccountState(ctx, reply, 2, persistenceID, 750.00)

			// restart the actor: it must come back with the committed state
			rig.killForRestart(ctx, pid, behavior.ID())
			pid = rig.spawn(ctx, behavior)

			reply = ask(ctx, bg, pid, &egopb.GetStateCommand{})
			expectAccountState(ctx, reply, 2, persistenceID, 750.00)
		})

		s.It("with telemetry extension", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountDurableStateBehavior(persistenceID)

			noopTracer := tracenoop.NewTracerProvider().Tracer("test")
			noopMeter := noop.NewMeterProvider().Meter("test")
			rig := startActorRig(ctx, durableStore, extensions.NewTelemetryExtension(noopTracer, noopMeter))
			pid := rig.spawn(ctx, behavior)

			reply := ask(ctx, bg, pid, &testpb.CreateAccount{AccountBalance: 500.00})
			expectAccountState(ctx, reply, 1, persistenceID, 500.00)

			// the rig stops the system in cleanup, which exercises the PostStop
			// metrics path
		})

		s.It("with mismatched state types from HandleCommand", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			behavior := &badStateDurableStateBehavior{id: uuid.NewString()}
			rig := startActorRig(ctx, durableStore)
			pid := rig.spawn(ctx, behavior)

			reply := ask(ctx, bg, pid, &testpb.CreateAccount{AccountBalance: 500.00})
			message, isError := errorReplyMessage(reply)
			ctx.Expect(isError).To(specs.BeTrue())
			ctx.Expect(message).To(specs.Contain("mismatch state types"))
		})

		s.It("with invalid version increment from HandleCommand", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			behavior := enginetest.NewBadVersionDurableStateBehavior(uuid.NewString())
			rig := startActorRig(ctx, durableStore)
			pid := rig.spawn(ctx, behavior)

			reply := ask(ctx, bg, pid, &testpb.CreateAccount{AccountBalance: 500.00})
			message, isError := errorReplyMessage(reply)
			ctx.Expect(isError).To(specs.BeTrue())
			ctx.Expect(message).To(specs.Contain("received version"))
		})

		// The two store-failure cases below need no actor system: recoverFromStore
		// is the whole behavior under test, so they run on the actor struct with a
		// StateStoreMock.
		s.It("with state recovery from state store failure", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			ctrl := mock.NewController(ctx)
			ctrl.Method("GetLatestState").
				Expect(mock.Any(), persistence.Unscoped(), persistenceID).
				Return(nil, errStoreFailure)

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountDurableStateBehavior(persistenceID),
				stateStore:    enginetest.NewStateStoreMock(ctrl),
				scope:         persistence.Unscoped(),
			}

			err := entity.recoverFromStore(bg)
			ctx.Expect(err).To(specs.MatchError(errStoreFailure))
			ctx.Expect(entity.currentState).To(specs.BeNil())
			ctx.Expect(entity.currentVersion).ToEqual(uint64(0))
		})

		s.It("with state recovery from state store with initial parsing failure", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			latestState := &egopb.DurableState{
				ResultingState: &anypb.Any{
					TypeUrl: "invalid-type-url",
					Value:   []byte("invalid-value"),
				},
			}
			ctrl := mock.NewController(ctx)
			ctrl.Method("GetLatestState").
				Expect(mock.Any(), persistence.Unscoped(), persistenceID).
				Return(latestState, nil)

			entity := &Actor{
				persistenceID: persistenceID,
				behavior:      enginetest.NewAccountDurableStateBehavior(persistenceID),
				stateStore:    enginetest.NewStateStoreMock(ctrl),
				scope:         persistence.Unscoped(),
			}

			err := entity.recoverFromStore(bg)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err.Error()).To(specs.MatchRegex("failed to unmarshal the latest state"))
			ctx.Expect(entity.currentState).To(specs.BeNil())
			ctx.Expect(entity.currentVersion).ToEqual(uint64(0))
		})
	})
}

// badStateDurableStateBehavior returns a different state type from HandleCommand than InitialState
type badStateDurableStateBehavior struct {
	id string
}

var _ behaviorport.DurableState = (*badStateDurableStateBehavior)(nil)

func (x *badStateDurableStateBehavior) ID() string {
	return x.id
}

func (x *badStateDurableStateBehavior) InitialState() State {
	return new(testpb.Account)
}

func (x *badStateDurableStateBehavior) HandleCommand(_ context.Context, _ Command, priorVersion uint64, _ State) (State, uint64, error) {
	// return a different protobuf type than InitialState
	return &testpb.AccountCreated{
		AccountId:      x.id,
		AccountBalance: 500.00,
	}, priorVersion + 1, nil
}

func (x *badStateDurableStateBehavior) MarshalBinary() ([]byte, error) {
	return json.Marshal(struct {
		ID string `json:"id"`
	}{ID: x.id})
}

func (x *badStateDurableStateBehavior) UnmarshalBinary(data []byte) error {
	aux := struct {
		ID string `json:"id"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	x.id = aux.ID
	return nil
}

// TestDurableStateActorTenancyGate exercises the T4-A pre-handler gate added
// to Actor: when the actor system carries the tenancy marker
// (tenant-aware mode), HandleCommand must never run without a TenantContext
// already attached to the incoming ctx. Like its EventSourcedActor
// counterpart, this spawns the actor directly and dispatches through
// goakt.Ask with a plain context, deliberately bypassing
// Engine.SendCommand's resolve-and-attach step.
func TestDurableStateActorTenancyGate(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("missing TenantContext blocks HandleCommand and persistence", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			persistenceID := uuid.NewString()
			behavior := enginetest.NewTenancyProbeDurableStateBehavior(persistenceID)
			rig := startActorRig(ctx, durableStore, extensions.NewTenancyMarker(false))
			pid := rig.spawnForTenant(ctx, behavior, "acme")

			// No TenantContext attached: mirrors a caller that bypasses
			// Engine.SendCommand entirely.
			reply := ask(ctx, context.Background(), pid, &testpb.CreateAccount{AccountBalance: 500})
			message, isError := errorReplyMessage(reply)
			ctx.Expect(isError).To(specs.BeTrue())

			_, wantErr := tenancy.Require(context.Background())
			ctx.Expect(message).To(specs.Equal(wantErr.Error()))

			// HandleCommand must never run without an attached TenantContext
			ctx.Expect(behavior.InvocationCount()).ToEqual(0)

			// no state may be persisted when the gate blocks the command
			scopeA := tenantScopeFor(ctx, "acme")
			latest, err := durableStore.GetLatestState(context.Background(), scopeA, persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.BeNil())
		})
	})
}

// TestDurableStateActorVerifyTenantForPersist unit-tests the T4-B defensive
// persistence invariant (design.md D4) in isolation, without a goakt actor
// system: verifyTenantForPersist must be a no-op in legacy mode, must fail
// closed via tenancy.Require when tenant-aware mode has no TenantContext
// attached, and must succeed without touching a resolver (the actor struct
// holds no resolver field at all — see Actor.tenantAware's doc
// comment) when one is already attached.
func TestDurableStateActorVerifyTenantForPersist(t *testing.T) {
	specs.Describe(t, "verifyTenantForPersist fails closed in tenant-aware mode when no valid TenantContext is attached", func(s *specs.Spec) {
		s.It("legacy mode is always a no-op", func(ctx *specs.Context) {
			entity := &Actor{}
			ctx.Expect(entity.verifyTenantForPersist(context.Background())).To(specs.BeNil())
		})

		s.It("tenant-aware mode fails closed when no TenantContext is attached", func(ctx *specs.Context) {
			entity := &Actor{tenantAware: true}
			err := entity.verifyTenantForPersist(context.Background())
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrMissing))
		})

		s.It("tenant-aware mode succeeds against an already-attached TenantContext", func(ctx *specs.Context) {
			entity := &Actor{tenantAware: true}
			tc := tenantContextFor(ctx, "acme")
			attached, err := tenancy.Attach(context.Background(), tc)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(entity.verifyTenantForPersist(attached)).To(specs.BeNil())
		})

		// Blocker 2 inheritance (EGO-TENANT-006 review fix): a resolver
		// returning the zero-value tenancy.TenantContext{} (Blocker 1) is now
		// rejected by tenancy.Attach itself (design.md Decision D8) before the
		// command ever reaches this actor, so ctx here ends up with nothing
		// attached — the same "missing" case this gate already covered. No
		// code change to verifyTenantForPersist was needed to inherit this
		// protection; it fails closed purely because tenancy.Require now
		// rejects malformed content, and Attach never let one through.
		s.It("a resolver-invalid TenantContext never gets attached, so persistence still fails closed", func(ctx *specs.Context) {
			entity := &Actor{tenantAware: true}

			attached, attachErr := tenancy.Attach(context.Background(), tenancy.TenantContext{})
			ctx.Expect(attachErr).To(specs.Not(specs.BeNil()))
			ctx.Expect(attachErr).To(specs.MatchError(tenancy.ErrInvalid))

			err := entity.verifyTenantForPersist(attached)
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrMissing))
		})
	})
}

// TestDurableStateActorTenancyWritePath is the end-to-end counterpart of
// TestDurableStateActorVerifyTenantForPersist: with a TenantContext properly
// attached (as Engine.SendCommand would do at the trust boundary), the
// command must succeed all the way through persistStateAndPublish, and
// HandleCommand must observe the exact same TenantContext instance that was
// attached — proving the T4-B gate reads it back rather than re-resolving
// it (there is no resolver reachable from the actor to re-resolve with in
// the first place).
func TestDurableStateActorTenancyWritePath(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("persists through a TenantContext attached at the trust boundary", func(ctx *specs.Context) {
			durableStore := connectedDurableStore(ctx)
			persistenceID := uuid.NewString()
			behavior := enginetest.NewTenancyProbeDurableStateBehavior(persistenceID)
			rig := startActorRig(ctx, durableStore, extensions.NewTenancyMarker(false))
			pid := rig.spawnForTenant(ctx, behavior, "acme")

			tenant := tenantContextFor(ctx, "acme")
			reply := ask(ctx, attachedTo(ctx, tenant), pid, &testpb.CreateAccount{AccountBalance: 500})

			// a command with a TenantContext already attached must succeed
			// through persistStateAndPublish
			ctx.Expect(isStateReply(reply)).To(specs.BeTrue())
			ctx.Expect(behavior.InvocationCount()).ToEqual(1)

			// HandleCommand must observe the exact TenantContext attached at the
			// trust boundary
			observed, ok := behavior.ObservedTenant()
			ctx.Expect(ok).To(specs.BeTrue())
			ctx.Expect(observed).To(specs.Equal(tenant))

			// the state must be persisted once the tenant is confirmed present
			scopeA := tenantScopeFor(ctx, "acme")
			latest, err := durableStore.GetLatestState(context.Background(), scopeA, persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.Not(specs.BeNil()))
		})
	})
}
