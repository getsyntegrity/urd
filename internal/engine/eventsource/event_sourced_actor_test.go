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
	"errors"
	"testing"
	"time"

	specmock "github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/encryption"
	"github.com/getsyntegrity/urd/eventadapter"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

func TestEventSourcedActor(t *testing.T) {
	// These cases need the actor system: they spawn the real actor and its child
	// writers. They never sleep: a spawn waits until the actor reports itself
	// running, a command waits for its reply, and what a child actor does after
	// the reply (a snapshot, a delete) is polled. The empty Describe name keeps
	// the old subtest names.
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("with state reply", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior)

			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))
			expectAccountState(ctx, state, 1, &testpb.Account{AccountId: persistenceID, AccountBalance: 500.00})

			// send another command to credit the balance
			state = stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 250}))
			expectAccountState(ctx, state, 2, &testpb.Account{AccountId: persistenceID, AccountBalance: 750.00})
		})

		s.It("with error reply", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior)

			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))
			expectAccountState(ctx, state, 1, &testpb.Account{AccountId: persistenceID, AccountBalance: 500.00})

			// a command for another entity is rejected
			reply := ask(ctx, pid, &testpb.CreditAccount{AccountId: "different-id", Balance: 250})
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal("command sent to the wrong entity"))
		})

		s.It("with unhandled command", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior)

			reply := ask(ctx, pid, &testpb.TestSend{})
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal("unhandled command"))
		})

		s.It("with state recovery from event store", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			idle := rig.system.NumActors()
			pid := rig.spawn(ctx, behavior)

			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))
			expectAccountState(ctx, state, 1, &testpb.Account{AccountId: persistenceID, AccountBalance: 500.00})

			state = stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 250}))
			expectAccountState(ctx, state, 2, &testpb.Account{AccountId: persistenceID, AccountBalance: 750.00})

			// restart the actor: it rebuilds its state from the events store
			rig.killForRestart(ctx, pid, behavior.ID(), idle)
			pid = rig.spawn(ctx, behavior)

			// fetch the current state
			state = stateReplyOf(ctx, ask(ctx, pid, &egopb.GetStateCommand{}))
			expectAccountState(ctx, state, 2, &testpb.Account{AccountId: persistenceID, AccountBalance: 750.00})
		})

		s.It("with no event to persist", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior)

			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))
			expectAccountState(ctx, state, 1, &testpb.Account{AccountId: persistenceID, AccountBalance: 500.00})

			state = stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 250}))
			expectAccountState(ctx, state, 2, &testpb.Account{AccountId: persistenceID, AccountBalance: 750.00})

			// a command that produces no event leaves the sequence number and the state alone
			state = stateReplyOf(ctx, ask(ctx, pid, new(testpb.TestNoEvent)))
			expectAccountState(ctx, state, 2, &testpb.Account{AccountId: persistenceID, AccountBalance: 750.00})
		})

		s.It("with unhandled event", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior)

			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))
			expectAccountState(ctx, state, 1, &testpb.Account{AccountId: persistenceID, AccountBalance: 500.00})

			reply := ask(ctx, pid, new(emptypb.Empty))
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal("unhandled event"))
		})

		s.It("with snapshot store recovery", func(ctx *specs.Context) {
			bg := context.Background()
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			eventStore := connectedEventsStore(ctx)
			snapshotStore := connectedSnapshotStore(ctx)

			// pre-write a snapshot
			stateAny, err := anypb.New(&testpb.Account{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(snapshotStore.WriteSnapshot(bg, persistence.Unscoped(), &egopb.Snapshot{
				PersistenceId:  persistenceID,
				SequenceNumber: 1,
				State:          stateAny,
				Timestamp:      time.Now().Unix(),
			})).To(specs.BeNil())

			// pre-write an event after the snapshot
			eventAny, err := anypb.New(&testpb.AccountCredited{AccountId: persistenceID, AccountBalance: 50})
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(eventStore.WriteEvents(bg, persistence.Unscoped(), []*egopb.Event{{
				PersistenceId:  persistenceID,
				SequenceNumber: 2,
				Event:          eventAny,
				Timestamp:      time.Now().Unix(),
				Shard:          0,
			}}, persistence.Unconditional())).To(specs.BeNil())

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewSnapshotStore(snapshotStore))
			pid := rig.spawn(ctx, behavior)

			// the state is the snapshot with the later event applied
			state := stateReplyOf(ctx, ask(ctx, pid, &egopb.GetStateCommand{}))
			expectAccountState(ctx, state, 2, &testpb.Account{AccountId: persistenceID, AccountBalance: 150.00})
		})

		s.It("with telemetry extension", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			// create noop tracer and meter for telemetry
			noopTracer := tracenoop.NewTracerProvider().Tracer("test")
			noopMeter := noop.NewMeterProvider().Meter("test")

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewTelemetryExtension(noopTracer, noopMeter))
			pid := rig.spawn(ctx, behavior)

			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))
			expectAccountState(ctx, state, 1, &testpb.Account{AccountId: persistenceID, AccountBalance: 500.00})

			// stop the actor system (exercises PostStop metrics decrement)
			rig.stop(ctx)
		})

		s.It("with encryption during command processing", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			encryptor := encryption.NewAESEncryptor(testkit.NewKeyStore())

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewEncryptor(encryptor))
			pid := rig.spawn(ctx, behavior)

			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))
			expectAccountState(ctx, state, 1, &testpb.Account{AccountId: persistenceID, AccountBalance: 500.00})
		})

		s.It("with snapshot persistence on interval", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			snapshotStore := connectedSnapshotStore(ctx)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewSnapshotStore(snapshotStore))
			// snapshot interval of 1
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{SnapshotInterval: 1})

			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(1))

			// the snapshot writer child persists after the reply
			ctx.Eventually(latestSnapshot(snapshotStore, persistenceID), beSnapshotAt(1),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
		})

		s.It("with retention policy delete events on snapshot", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			eventStore := connectedEventsStore(ctx)
			snapshotStore := connectedSnapshotStore(ctx)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewSnapshotStore(snapshotStore))
			// snapshot interval of 2 and retention policy
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				SnapshotInterval:       2,
				HasRetentionPolicy:     true,
				DeleteEventsOnSnapshot: true,
				EventsRetentionCount:   0,
			})

			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))

			// the second command hits the snapshot interval
			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 100}))
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(2))

			// verify snapshot was written
			ctx.Eventually(latestSnapshot(snapshotStore, persistenceID), beSnapshotAt(2),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))

			// verify events were deleted (deleteUpTo = eventsCounter = 2 since EventsRetentionCount is 0)
			ctx.Eventually(latestEvent(eventStore, persistenceID), specs.BeNil(),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
		})

		s.It("with event adapters during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			eventStore := connectedEventsStore(ctx)

			// pre-write an event to the store
			eventAny, err := anypb.New(&testpb.AccountCreated{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(eventStore.WriteEvents(context.Background(), persistence.Unscoped(), []*egopb.Event{{
				PersistenceId:  persistenceID,
				SequenceNumber: 1,
				Event:          eventAny,
				Timestamp:      time.Now().Unix(),
				Shard:          0,
			}}, persistence.Unconditional())).To(specs.BeNil())

			// a no-op event adapter that passes events through unchanged
			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewEventAdapters([]eventadapter.EventAdapter{&noopEventAdapter{}}))
			pid := rig.spawn(ctx, behavior)

			// fetch the current state - should have recovered through the adapter chain
			state := stateReplyOf(ctx, ask(ctx, pid, &egopb.GetStateCommand{}))
			expectAccountState(ctx, state, 1, &testpb.Account{AccountId: persistenceID, AccountBalance: 100})
		})

		s.It("with snapshot and encryption during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			eventStore := connectedEventsStore(ctx)
			snapshotStore := connectedSnapshotStore(ctx)
			encryptor := encryption.NewAESEncryptor(testkit.NewKeyStore())

			// snapshot interval of 2 (snapshot at event 2, event 3 has no snapshot)
			entityCfg := &extensions.EntityConfig{SnapshotInterval: 2}

			// first actor system, with encryption and snapshot store
			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewSnapshotStore(snapshotStore),
				extensions.NewEncryptor(encryptor))
			pid := rig.spawn(ctx, behavior, entityCfg)

			// command 1 (event 1, no snapshot yet)
			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))

			// command 2 (event 2, snapshot taken at seq 2)
			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 200}))

			// command 3 (event 3, no snapshot - this encrypted event will need replay after snapshot)
			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 100}))

			// the snapshot writer child persists after the reply: the second actor
			// system must find the snapshot at seq 2 and replay only event 3
			ctx.Eventually(latestSnapshot(snapshotStore, persistenceID), beSnapshotAt(2),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))

			// stop the first actor system
			rig.stop(ctx)

			// start a NEW actor system with the same stores and encryption
			rig2 := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewSnapshotStore(snapshotStore),
				extensions.NewEncryptor(encryptor))

			// spawn the actor again - should recover from encrypted snapshot and events
			behavior2 := enginetest.NewAccountEventSourcedBehavior(persistenceID)
			pid2 := rig2.spawnWithoutStash(ctx, behavior2, entityCfg)

			// 500 + 200 + 100 = 800
			state := stateReplyOf(ctx, ask(ctx, pid2, &egopb.GetStateCommand{}))
			expectAccountState(ctx, state, 3, &testpb.Account{AccountId: persistenceID, AccountBalance: 800.00})
		})

		s.It("with encrypted event replay without snapshot store", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			eventStore := connectedEventsStore(ctx)
			encryptor := encryption.NewAESEncryptor(testkit.NewKeyStore())

			// first actor system: send commands with encryption (no snapshot store)
			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewEncryptor(encryptor))
			pid := rig.spawn(ctx, behavior)

			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 300.00}))
			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 150}))

			rig.stop(ctx)

			// second actor system: recover from encrypted events (no snapshot)
			rig2 := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewEncryptor(encryptor))
			behavior2 := enginetest.NewAccountEventSourcedBehavior(persistenceID)
			pid2 := rig2.spawnWithoutStash(ctx, behavior2)

			state := stateReplyOf(ctx, ask(ctx, pid2, &egopb.GetStateCommand{}))
			expectAccountState(ctx, state, 2, &testpb.Account{AccountId: persistenceID, AccountBalance: 450.00})
		})

		s.It("with retention policy delete snapshots on snapshot", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			snapshotStore := connectedSnapshotStore(ctx)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewSnapshotStore(snapshotStore))
			// snapshot interval of 2 and delete snapshots retention policy
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				SnapshotInterval:          2,
				HasRetentionPolicy:        true,
				DeleteSnapshotsOnSnapshot: true,
			})

			// send 4 commands: snapshots at events 2 and 4
			// command 1: create account
			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))
			// command 2: credit (triggers first snapshot at seq 2)
			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 100}))
			// command 3: credit
			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 50}))
			// command 4: credit (triggers second snapshot at seq 4, should delete snapshot at seq 2)
			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 25}))
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(4))

			// verify latest snapshot exists at seq 4
			ctx.Eventually(latestSnapshot(snapshotStore, persistenceID), beSnapshotAt(4),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
		})
	})

	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("With events store ping failed", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			// GoAkt retries PreStart before it gives up on the actor, so Ping runs at
			// least once. Recovery never starts: any other events store call is unexpected.
			ctrl := specmock.NewController(ctx)
			ctrl.Method("Ping").Expect(specmock.Any()).Return(errStoreFailure).AtLeast(1)

			rig := startActorRigWith(ctx, "TestActorSystem", 3,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)))

			rig.expectRefusedFor(ctx, nil, behavior)
		})

		// The two recovery failures below need no actor system: recover is the
		// whole behavior under test, so they run on an Actor built directly.
		s.It("With events store GetLatestEvent failed", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()

			ctrl := specmock.NewController(ctx)
			ctrl.Method("GetLatestEvent").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(nil, errStoreFailure)

			entity := newRecoveringActor(persistenceID, enginetest.NewAccountEventSourcedBehavior(persistenceID))
			entity.eventsStore = enginetest.NewEventsStoreMock(ctrl)

			expectRecoveryFailure(ctx, entity, "failed to get latest event", errStoreFailure)
		})

		s.It("With replay events failure during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			latestEvent := &egopb.Event{
				PersistenceId:  persistenceID,
				SequenceNumber: 1,
			}

			ctrl := specmock.NewController(ctx)
			ctrl.Method("GetLatestEvent").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(latestEvent, nil)
			ctrl.Method("ReplayEvents").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID, uint64(1), uint64(1), specmock.Any()).
				Return(nil, errStoreFailure)

			entity := newRecoveringActor(persistenceID, enginetest.NewAccountEventSourcedBehavior(persistenceID))
			entity.eventsStore = enginetest.NewEventsStoreMock(ctrl)

			expectRecoveryFailure(ctx, entity, "failed to replay events", errStoreFailure)
		})
	})
}

// TestEventSourcedActorTenancyGate exercises the T4-A pre-handler gate added
// to Actor: when the actor system carries the tenancy marker
// (tenant-aware mode), HandleCommand must never run without a TenantContext
// already attached to the incoming ctx. The gate reuses tenancy.Require — a
// read-only check — and never calls a resolver itself, so these tests spawn
// the actor directly and dispatch through goakt.Ask with a plain context,
// deliberately bypassing Engine.SendCommand's resolve-and-attach step
// (mirroring how a saga or any other internal caller can reach the actor
// runtime without crossing the trust boundary; see TestSagaFailsClosed...
// in saga_test.go for the end-to-end demonstration).
func TestEventSourcedActorTenancyGate(t *testing.T) {
	// The empty Describe name keeps the old subtest names.
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("non-batched: missing TenantContext blocks HandleCommand and persistence", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewTenancyProbeEventSourcedBehavior(persistenceID)
			eventStore := connectedEventsStore(ctx)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewTenancyMarker(false))
			pid := rig.spawn(ctx, behavior, extensions.NewEntityTenantScope("acme"))

			// No TenantContext attached: this is exactly what a caller that
			// bypasses Engine.SendCommand (e.g. a saga's context.Background()
			// dispatch, documented in #54) looks like from the actor's side.
			reply := ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500})

			_, wantErr := tenancy.Require(context.Background())
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal(wantErr.Error()))

			// HandleCommand must never run without an attached TenantContext
			ctx.Expect(behavior.InvocationCount()).ToEqual(0)

			// no event may be persisted when the gate blocks the command
			ctx.Expect(latestTenantEvent(eventStore, tenantScopeOf(ctx, "acme"), persistenceID)()).To(specs.BeNil())
		})

		s.It("batched: missing TenantContext blocks HandleCommand before flushBatch is ever reached", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewTenancyProbeEventSourcedBehavior(persistenceID)
			eventStore := connectedEventsStore(ctx)

			// A short flush window with a threshold that is never reached by a
			// single command: if the gate failed to block the command and
			// flushBatch ran, it would still take at least this long, giving the
			// assertion below a real window to catch a regression.
			entityCfg := &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 200 * time.Millisecond,
			}

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewTenancyMarker(false))
			pid := rig.spawn(ctx, behavior, entityCfg, extensions.NewEntityTenantScope("acme"))

			reply := ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500})

			_, wantErr := tenancy.Require(context.Background())
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal(wantErr.Error()))

			// HandleCommand must never run without an attached TenantContext
			ctx.Expect(behavior.InvocationCount()).ToEqual(0)

			// Watch for 2.5 flush windows: a wrongly-scheduled flush timer would
			// fire inside them, and the store must stay empty throughout.
			// flushBatch's own context.Background() call (T4-B, out of scope here)
			// must never even be reached.
			ctx.Consistently(latestTenantEvent(eventStore, tenantScopeOf(ctx, "acme"), persistenceID), specs.BeNil(),
				specs.WithTimeout(500*time.Millisecond), specs.WithInterval(pollInterval))
		})
	})
}

// TestEventSourcedActorVerifyTenantForPersist unit-tests the T4-B defensive
// persistence invariant (design.md D4) in isolation, without a goakt actor
// system: verifyTenantForPersist must be a no-op in legacy mode, must fail
// closed via tenancy.Require when tenant-aware mode has no TenantContext
// attached, and must succeed without touching a resolver (the actor struct
// holds no resolver field at all — see the Actor.tenantAware
// doc comment) when one is already attached.
func TestEventSourcedActorVerifyTenantForPersist(t *testing.T) {
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

// TestEventSourcedActorBatchTenantHomogeneity exercises the T4-B batched
// invariant (design.md D4): every command merged into the same batchBuffer
// before a flush must belong to the same tenant. Both commands here carry a
// TenantContext already attached (T4-A is satisfied for both, so
// HandleCommand runs for each); the second is rejected purely on
// homogeneity grounds, at buffer-append time, before flushBatch's
// context.Background() Ask is ever reached — proving this check is
// independent from, and additional to, T4-A.
func TestEventSourcedActorBatchTenantHomogeneity(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("rejects a second tenant in the same batch cycle", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewTenancyProbeEventSourcedBehavior(persistenceID)
			eventStore := connectedEventsStore(ctx)

			// A high threshold that a single command never reaches, and a short
			// flush window: this test only cares about the append-time check, not
			// any flush behavior, but the first command's reply is still deferred
			// until its cycle flushes by timer, so the window must stay well under
			// the Ask timeouts used below.
			entityCfg := &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: time.Second,
			}

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewTenancyMarker(false))
			pid := rig.spawn(ctx, behavior, entityCfg, extensions.NewEntityTenantScope("acme"))

			tenantA := tenantContextFor(ctx, "acme")
			tenantB := tenantContextFor(ctx, "globex")

			// The first command's reply is deferred (stashed) until its batch cycle
			// flushes by timer, since the threshold is never reached by one command
			// alone: send it in the background and let it run concurrently with the
			// second command below, exactly as it would for two real concurrent
			// callers sharing a batch cycle.
			first := askInBackground(attachTenant(ctx, tenantA), pid, &testpb.CreateAccount{AccountBalance: 500})

			// Wait until the first command has been received, passed the pre-handler
			// gate and run HandleCommand. The actor handles its messages one at a
			// time, so it has recorded tenant A in the batch buffer before the
			// second command, for a different tenant, is read from the mailbox.
			ctx.Eventually(invocations(behavior), specs.Equal(1),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))

			reply := askWith(ctx, attachTenant(ctx, tenantB), pid, &testpb.CreateAccount{AccountBalance: 10})

			// a second command for a different tenant in the same batch cycle must be rejected
			wantErr := tenancy.VerifyUnchanged(tenantA, tenantB)
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal(wantErr.Error()))

			// the first command of the batch cycle must succeed once its own cycle flushes
			stateReplyOf(ctx, first.await(ctx))

			// Exactly the first, tenant-A command's event was ever persisted: the
			// rejected tenant-B command never reached the buffer at all.
			latest := latestTenantEvent(eventStore, tenantScopeOf(ctx, "acme"), persistenceID)()
			ctx.Expect(latest).To(specs.Satisfy("is the event at sequence 1", func(v any) bool {
				event, ok := v.(*egopb.Event)
				return ok && event != nil && event.GetSequenceNumber() == 1
			}))
		})
	})
}

// TestEventSourcedActorResetBatchDoesNotClearActorTenant covers design.md
// D6 (EGO-TENANT-002): actorTenant is scoped to the actor's full lifetime,
// not to one batch cycle, so resetBatch must NOT clear it — superseding the
// prior "batchTenant" behavior, where resetBatch cleared the per-cycle field
// and a new cycle's first command from any tenant was accepted regardless of
// which tenant the previous, already-flushed cycle belonged to. BatchThreshold
// is 1, so each command completes a full, self-contained batch cycle (buffer,
// flush, reply, resetBatch) before the next command is sent. The second
// command uses a DIFFERENT tenant than the first and must now be REJECTED:
// actorTenant, seeded by the first cycle's persist, survives resetBatch and
// is compared against every later command for this actor's entire lifetime.
func TestEventSourcedActorResetBatchDoesNotClearActorTenant(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("keeps the actor tenant across batch cycles", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewTenancyProbeEventSourcedBehavior(persistenceID)

			entityCfg := &extensions.EntityConfig{
				BatchThreshold:   1,
				BatchFlushWindow: time.Second,
			}

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewTenancyMarker(false))
			pid := rig.spawn(ctx, behavior, entityCfg, extensions.NewEntityTenantScope("acme"))

			tenantA := tenantContextFor(ctx, "acme")
			tenantB := tenantContextFor(ctx, "globex")
			ctxA := attachTenant(ctx, tenantA)
			ctxB := attachTenant(ctx, tenantB)

			// First cycle: tenant A. BatchThreshold==1 drives this all the way
			// through flush, reply, and resetBatch before the Ask returns. The
			// first cycle's only command must succeed.
			stateReplyOf(ctx, askWith(ctx, ctxA, pid, &testpb.CreateAccount{AccountBalance: 500}))

			// Second cycle: tenant B, a brand new batch cycle. actorTenant (seeded
			// as tenant A by the first cycle's persist) is NOT cleared by
			// resetBatch, so this cross-tenant command must be rejected, even
			// though it is the first command of its own, freshly reset cycle. Ask
			// itself must not fail; the rejection is carried in the CommandReply.
			reply := askWith(ctx, ctxB, pid, &testpb.CreateAccount{AccountBalance: 10})

			// rejection must be the fail-closed tenant error VerifyUnchanged
			// produces, not an invented error type
			wantErr := tenancy.VerifyUnchanged(tenantA, tenantB)
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal(wantErr.Error()))

			// A third command from the SAME tenant (A) that established actorTenant
			// must still succeed in its own brand new batch cycle: actorTenant
			// surviving resetBatch is a cross-tenant guard, not a "one cycle only"
			// restriction on the tenant that originally established it.
			stateReplyOf(ctx, askWith(ctx, ctxA, pid, &testpb.CreateAccount{AccountBalance: 20}))
		})
	})
}

// TestEventSourcedActorBatchTenantHomogeneity_ZeroEventCrossTenant is the
// RED-before-fix regression test for Blocker 3 (EGO-TENANT-006 adversarial
// review). Before the fix, processAndBatch's tenant-homogeneity check ran
// AFTER entity.behavior.HandleCommand — and, for a command producing zero
// events, only after a StateReply had already been built from
// entity.latestState() (== entity.batchState, i.e. tenant A's in-flight,
// unpersisted data) and sent back to tenant B. Tenant B's HandleCommand also
// ran against tenant A's batchState before any homogeneity check occurred at
// all. Neither the wrong-tenant state leak nor the wrong-tenant HandleCommand
// invocation is acceptable: the check must gate BEFORE HandleCommand runs,
// exactly like the T4-A pre-handler gate does for a missing tenant.
func TestEventSourcedActorBatchTenantHomogeneity_ZeroEventCrossTenant(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("rejects a zero-event command of another tenant before HandleCommand runs", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewTenancyProbeEventSourcedBehavior(persistenceID)
			eventStore := connectedEventsStore(ctx)

			// A high threshold that tenant A's single command never reaches on its
			// own, so its batch cycle (and batchTenant) stays open when tenant B's
			// command arrives.
			entityCfg := &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: time.Second,
			}

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewTenancyMarker(false))
			pid := rig.spawn(ctx, behavior, entityCfg, extensions.NewEntityTenantScope("acme"))

			tenantA := tenantContextFor(ctx, "acme")
			tenantB := tenantContextFor(ctx, "globex")

			// Tenant A starts the batch: this command produces one event, so
			// batchTenant becomes A and the batch stays open (threshold not
			// reached). Its reply is stashed until the cycle flushes by timer, so
			// send it in the background exactly like the homogeneity test above.
			first := askInBackground(attachTenant(ctx, tenantA), pid, &testpb.CreateAccount{AccountBalance: 500})

			// Wait until tenant A's command has been received, passed the
			// pre-handler gate and run HandleCommand. The actor handles its
			// messages one at a time, so it has appended it to the batch (recording
			// batchTenant == A) before tenant B's command is sent into the same
			// open cycle. Tenant A's command must have run HandleCommand once.
			ctx.Eventually(invocations(behavior), specs.Equal(1),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))

			// Tenant B sends a command whose handler would produce zero events,
			// a genuine no-op and not an error, against the same actor and
			// persistence ID, while tenant A's batch is still open. Ask itself must
			// not fail; the rejection is carried in the CommandReply.
			reply := askWith(ctx, attachTenant(ctx, tenantB), pid, &testpb.TestNoEvent{})

			// rejection must be the fail-closed tenant error VerifyUnchanged
			// produces, not an invented error type
			wantErr := tenancy.VerifyUnchanged(tenantA, tenantB)
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal(wantErr.Error()))

			// The decisive assertion: tenant B's HandleCommand must never have run.
			// Before the Blocker 3 fix, it did run (against tenant A's batchState)
			// before the zero-event early-return path replied; this proves the
			// gate now runs first.
			ctx.Expect(behavior.InvocationCount()).ToEqual(1)

			// Tenant A's in-flight batch must be untouched by B's rejected attempt:
			// let A's cycle flush (by timer) and confirm it still succeeds and
			// persists exactly A's one event, uncontaminated by B's attempt.
			stateReplyOf(ctx, first.await(ctx))

			latest := latestTenantEvent(eventStore, tenantScopeOf(ctx, "acme"), persistenceID)()
			ctx.Expect(latest).To(specs.Satisfy("is the event at sequence 1", func(v any) bool {
				event, ok := v.(*egopb.Event)
				return ok && event != nil && event.GetSequenceNumber() == 1
			}))
		})
	})
}

// TestEventSourcedActorBatchTenantHomogeneity_ZeroEventSameTenant is the
// regression companion to the cross-tenant test above: the SAME tenant
// sending a zero-event command into its own open batch must still succeed
// and still hit the len(events)==0 cached-state-reply path in
// processAndBatch — the Blocker 3 fix's pre-handler homogeneity check must
// not reject a same-tenant command.
func TestEventSourcedActorBatchTenantHomogeneity_ZeroEventSameTenant(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("accepts a zero-event command of the same tenant", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewTenancyProbeEventSourcedBehavior(persistenceID)

			entityCfg := &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: time.Second,
			}

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewTenancyMarker(false))
			pid := rig.spawn(ctx, behavior, entityCfg, extensions.NewEntityTenantScope("acme"))

			ctxA := attachTenant(ctx, tenantContextFor(ctx, "acme"))
			first := askInBackground(ctxA, pid, &testpb.CreateAccount{AccountBalance: 500})

			// the first command must be in the open batch before the second one is sent
			ctx.Eventually(invocations(behavior), specs.Equal(1),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))

			// Same tenant, zero-event command, into the same still-open batch: it
			// must still succeed via the cached-state-reply path.
			stateReplyOf(ctx, askWith(ctx, ctxA, pid, &testpb.TestNoEvent{}))

			stateReplyOf(ctx, first.await(ctx))
		})
	})
}

func TestEventSourcedActorErrorPaths(t *testing.T) {
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("with missing behavior fails to start", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()

			// The spawn is refused on the missing behavior, before any store is
			// touched, so a call to the events store is unexpected.
			ctrl := specmock.NewController(ctx)
			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)))

			// spawn with no behavior dependency
			rig.expectRefused(ctx, nil, persistenceID, goakt.WithLongLived(), goakt.WithStashing())
		})

		specs.Table(s, []mistypedExtensionCase{
			{
				name:        "returns an error instead of panicking when the snapshot store extension is registered with an unexpected type",
				system:      "TestEventSourcedMistypedSnapshotSystem",
				extensionID: extensions.SnapshotStoreExtensionID,
			},
			{
				name:        "returns an error instead of panicking when the event adapters extension is registered with an unexpected type",
				system:      "TestEventSourcedMistypedEventAdaptersSystem",
				extensionID: extensions.EventAdaptersExtensionID,
			},
			{
				name:        "returns an error instead of panicking when the encryptor extension is registered with an unexpected type",
				system:      "TestEventSourcedMistypedEncryptorSystem",
				extensionID: extensions.EncryptorExtensionID,
			},
			{
				name:        "returns an error instead of panicking when the telemetry extension is registered with an unexpected type",
				system:      "TestEventSourcedMistypedTelemetrySystem",
				extensionID: extensions.TelemetryExtensionID,
			},
		}, func(c mistypedExtensionCase) string { return c.name }, func(ctx *specs.Context, c mistypedExtensionCase) {
			persistenceID := uuid.NewString()

			// PreStart fails on the mistyped extension before it reads the events
			// store, so the store sees no call.
			ctrl := specmock.NewController(ctx)
			rig := startActorRigWith(ctx, c.system, 1,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)),
				&enginetest.MistypedExtension{Name: c.extensionID})

			rig.expectRefused(ctx, extensions.ErrMissingRequiredExtensions, persistenceID,
				goakt.WithLongLived(), goakt.WithStashing())
		})

		s.It("with event encryption failure during command processing", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			eventStore := connectedEventsStore(ctx)

			ctrl := specmock.NewController(ctx)
			ctrl.Method("Encrypt").
				Expect(specmock.Any(), persistenceID, specmock.Any()).
				Return(nil, "", errStoreFailure)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewEncryptor(enginetest.NewEncryptorMock(ctrl)))
			pid := rig.spawn(ctx, behavior)

			reply := ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00})
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.StartWith("failed to encrypt event"))

			// nothing reached the store
			latest, err := eventStore.GetLatestEvent(context.Background(), persistence.Unscoped(), persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.BeNil())
		})

		s.It("with snapshot encryption failure during command processing", func(ctx *specs.Context) {
			// Snapshot encryption failures are logged by the snapshot writer child
			// actor but do not fail the command. Snapshots are an optimization for
			// faster recovery, not a correctness requirement. The command succeeds
			// with a state reply.
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			eventStore := connectedEventsStore(ctx)
			snapshotStore := connectedSnapshotStore(ctx)

			// event encryption (parent) succeeds; snapshot encryption (child) fails
			ctrl := specmock.NewController(ctx)
			encrypt := ctrl.Method("Encrypt")
			encrypt.Expect(specmock.Any(), persistenceID, specmock.Any()).Return([]byte("ciphertext"), "key-1", nil)
			encrypt.Expect(specmock.Any(), persistenceID, specmock.Any()).Return(nil, "", errStoreFailure)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewSnapshotStore(snapshotStore),
				extensions.NewEncryptor(enginetest.NewEncryptorMock(ctrl)))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{SnapshotInterval: 1})

			reply := ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00})
			state := stateReplyOf(ctx, reply)
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(1))

			// the child snapshot writer encrypts after the reply: wait for its call,
			// the second one, which fails
			ctx.Eventually(callCount(encrypt), specs.BeGreaterThanOrEqual(2),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))

			// verify no snapshot was written since encryption failed
			ctx.Consistently(latestSnapshot(snapshotStore, persistenceID), specs.BeNil(),
				specs.WithTimeout(quietPeriod), specs.WithInterval(pollInterval))
		})

		s.It("with DeleteEvents error in retention policy does not crash", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			// The janitor retries a failed delete with backoff, and stopping the actor
			// spec waits for the entire retry budget before releasing the mock.
			eventsCtrl := specmock.NewController(ctx)
			eventsCtrl.Method("Ping").Expect(specmock.Any()).Return(nil)
			eventsCtrl.Method("GetLatestEvent").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(nil, nil)
			eventsCtrl.Method("WriteEvents").
				Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).
				Return(nil).Times(2)
			deleteEvents := eventsCtrl.Method("DeleteEvents")
			deleteEvents.
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID, uint64(2)).
				Return(errStoreFailure).Times(defaultMaxRetries + 1)

			snapshotCtrl := specmock.NewController(ctx)
			snapshotCtrl.Method("Ping").Expect(specmock.Any()).Return(nil)
			snapshotCtrl.Method("GetLatestSnapshot").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(nil, nil)
			snapshotCtrl.Method("WriteSnapshot").
				Expect(specmock.Any(), persistence.Unscoped(), specmock.Any()).
				Return(nil)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(retentionFixture{enginetest.NewEventsStoreMock(eventsCtrl)}),
				extensions.NewSnapshotStore(enginetest.NewSnapshotStoreMock(snapshotCtrl)))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				SnapshotInterval:       2,
				HasRetentionPolicy:     true,
				DeleteEventsOnSnapshot: true,
				EventsRetentionCount:   0,
			})

			// first command
			reply := ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00})
			stateReplyOf(ctx, reply)

			// second command triggers snapshot interval (2) and then DeleteEvents which errors
			reply = ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 100})

			// actor must still be alive: error is only logged
			stateReplyOf(ctx, reply)

			// Wait for every retry before the spec releases the mock context.
			ctx.Eventually(callCount(deleteEvents), specs.Equal(defaultMaxRetries+1),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			ctx.Expect(pid.IsRunning()).To(specs.BeTrue())
		})

		s.It("with DeleteSnapshots error in retention policy does not crash", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			eventsCtrl := specmock.NewController(ctx)
			eventsCtrl.Method("Ping").Expect(specmock.Any()).Return(nil)
			eventsCtrl.Method("GetLatestEvent").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(nil, nil)
			eventsCtrl.Method("WriteEvents").
				Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).
				Return(nil).Times(4)

			// The janitor exhausts the retry budget before this spec ends.
			snapshotCtrl := specmock.NewController(ctx)
			snapshotCtrl.Method("Ping").Expect(specmock.Any()).Return(nil)
			snapshotCtrl.Method("GetLatestSnapshot").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(nil, nil)
			snapshotCtrl.Method("WriteSnapshot").
				Expect(specmock.Any(), persistence.Unscoped(), specmock.Any()).
				Return(nil).Times(2)
			deleteSnapshots := snapshotCtrl.Method("DeleteSnapshots")
			deleteSnapshots.
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID, uint64(2)).
				Return(errStoreFailure).Times(defaultMaxRetries + 1)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(retentionFixture{enginetest.NewEventsStoreMock(eventsCtrl)}),
				extensions.NewSnapshotStore(enginetest.NewSnapshotStoreMock(snapshotCtrl)))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				SnapshotInterval:          2,
				HasRetentionPolicy:        true,
				DeleteSnapshotsOnSnapshot: true,
			})

			// commands 1 and 2: first snapshot at seq 2
			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500.00}))
			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 100}))

			// commands 3 and 4: second snapshot at seq 4 -> DeleteSnapshots is called and errors
			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 50}))
			reply := ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 25})

			// actor still alive after logged error
			state := stateReplyOf(ctx, reply)
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(4))

			// Wait for every retry before the spec releases the mock context.
			ctx.Eventually(callCount(deleteSnapshots), specs.Equal(defaultMaxRetries+1),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			ctx.Expect(pid.IsRunning()).To(specs.BeTrue())
		})

		s.It("with unhandled non-command message does not crash", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRigWith(ctx, "TestActorSystem", 3,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior)

			ctx.Expect(goakt.Tell(context.Background(), pid, new(egopb.NoReply))).To(specs.BeNil())

			// The mailbox is ordered: the command below is only handled once the
			// unhandled message ahead of it is, so its state reply proves the actor
			// survived it.
			reply := ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500})
			stateReplyOf(ctx, reply)
		})

		s.It("with persistEvents write failure shuts down actor", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			ctrl := specmock.NewController(ctx)
			ctrl.Method("Ping").Expect(specmock.Any()).Return(nil)
			ctrl.Method("GetLatestEvent").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(nil, nil)
			ctrl.Method("WriteEvents").
				Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).
				Return(errStoreFailure)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)))
			pid := rig.spawn(ctx, behavior)

			reply := ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500})
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal(errStoreFailure.Error()))

			// the write failure leaves the actor's view of the store stale, so it stops
			waitStopped(ctx, pid)
		})
	})

	// The recovery failure cases below need no actor system: recover is the whole
	// behavior under test, so each one runs on an Actor built directly, with the
	// enginetest adapters standing in for the stores, the encryptor and the event
	// adapter. The empty Describe name keeps the old subtest names.
	specs.Describe(t, "", func(s *specs.Spec) {
		encryptedSnapshot := func(ctx *specs.Context, persistenceID string, state proto.Message) *egopb.Snapshot {
			stateAny, err := anypb.New(state)
			ctx.Expect(err).To(specs.BeNil())
			return &egopb.Snapshot{
				PersistenceId:   persistenceID,
				SequenceNumber:  1,
				State:           &anypb.Any{TypeUrl: stateAny.GetTypeUrl(), Value: []byte("fake-ciphertext")},
				Timestamp:       time.Now().Unix(),
				IsEncrypted:     true,
				EncryptionKeyId: "key-1",
			}
		}

		// persistedEvent is the one event the events store holds at sequence 1.
		persistedEvent := func(persistenceID string, payload *anypb.Any) *egopb.Event {
			return &egopb.Event{
				PersistenceId:  persistenceID,
				SequenceNumber: 1,
				Event:          payload,
				Timestamp:      time.Now().Unix(),
			}
		}

		// holdEvent scripts the events store to report event as the latest one and
		// to replay it, which is all recover asks of it for a single event.
		holdEvent := func(ctrl *specmock.Controller, persistenceID string, event *egopb.Event) {
			ctrl.Method("GetLatestEvent").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(event, nil)
			ctrl.Method("ReplayEvents").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID, uint64(1), uint64(1), specmock.Any()).
				Return([]*egopb.Event{event}, nil)
		}

		accountCreated := func(ctx *specs.Context, persistenceID string) *anypb.Any {
			eventAny, err := anypb.New(&testpb.AccountCreated{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			return eventAny
		}

		s.It("with snapshot store GetLatestSnapshot failure during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			ctrl := specmock.NewController(ctx)
			ctrl.Method("GetLatestSnapshot").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(nil, errStoreFailure)

			entity := newRecoveringActor(persistenceID, enginetest.NewAccountEventSourcedBehavior(persistenceID))
			entity.snapshotStore = enginetest.NewSnapshotStoreMock(ctrl)
			// recover must stop at the snapshot failure: any events store call is unexpected.
			entity.eventsStore = enginetest.NewEventsStoreMock(ctrl)

			expectRecoveryFailure(ctx, entity, "failed to load snapshot", errStoreFailure)
		})

		s.It("with snapshot decryption failure during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			ctrl := specmock.NewController(ctx)
			ctrl.Method("GetLatestSnapshot").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(encryptedSnapshot(ctx, persistenceID, &testpb.Account{AccountId: persistenceID, AccountBalance: 100}), nil)
			ctrl.Method("Decrypt").
				Expect(specmock.Any(), persistenceID, []byte("fake-ciphertext"), "key-1").
				Return(nil, errStoreFailure)

			entity := newRecoveringActor(persistenceID, enginetest.NewAccountEventSourcedBehavior(persistenceID))
			entity.snapshotStore = enginetest.NewSnapshotStoreMock(ctrl)
			entity.eventsStore = enginetest.NewEventsStoreMock(ctrl)
			entity.encryptor = enginetest.NewEncryptorMock(ctrl)

			expectRecoveryFailure(ctx, entity, "failed to decrypt snapshot", errStoreFailure)
		})

		s.It("with snapshot unmarshal failure after decryption during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			ctrl := specmock.NewController(ctx)
			ctrl.Method("GetLatestSnapshot").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(encryptedSnapshot(ctx, persistenceID, &testpb.Account{AccountId: persistenceID, AccountBalance: 100}), nil)
			// return non-proto garbage bytes so proto.Unmarshal fails
			ctrl.Method("Decrypt").
				Expect(specmock.Any(), persistenceID, []byte("fake-ciphertext"), "key-1").
				Return([]byte("not-valid-proto"), nil)

			entity := newRecoveringActor(persistenceID, enginetest.NewAccountEventSourcedBehavior(persistenceID))
			entity.snapshotStore = enginetest.NewSnapshotStoreMock(ctrl)
			entity.eventsStore = enginetest.NewEventsStoreMock(ctrl)
			entity.encryptor = enginetest.NewEncryptorMock(ctrl)

			expectRecoveryFailure(ctx, entity, "failed to decrypt snapshot: failed to unmarshal decrypted payload", nil)
		})

		s.It("with snapshot state type mismatch unmarshal failure during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			// write snapshot with incompatible state type (AccountCredited instead of Account)
			wrongState, err := anypb.New(&testpb.AccountCredited{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())

			ctrl := specmock.NewController(ctx)
			ctrl.Method("GetLatestSnapshot").
				Expect(specmock.Any(), persistence.Unscoped(), persistenceID).
				Return(&egopb.Snapshot{
					PersistenceId:  persistenceID,
					SequenceNumber: 1,
					State:          wrongState,
					Timestamp:      time.Now().Unix(),
				}, nil)

			entity := newRecoveringActor(persistenceID, enginetest.NewAccountEventSourcedBehavior(persistenceID))
			entity.snapshotStore = enginetest.NewSnapshotStoreMock(ctrl)
			entity.eventsStore = enginetest.NewEventsStoreMock(ctrl)

			expectRecoveryFailure(ctx, entity, "failed to unmarshal snapshot state", nil)
		})

		s.It("with event decryption failure during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			// an "encrypted" event with dummy ciphertext
			encryptedPayload := &anypb.Any{TypeUrl: accountCreated(ctx, persistenceID).GetTypeUrl(), Value: []byte("fake-cipher")}
			event := persistedEvent(persistenceID, encryptedPayload)
			event.IsEncrypted = true
			event.EncryptionKeyId = "key-1"

			ctrl := specmock.NewController(ctx)
			holdEvent(ctrl, persistenceID, event)
			ctrl.Method("Decrypt").
				Expect(specmock.Any(), persistenceID, []byte("fake-cipher"), "key-1").
				Return(nil, errStoreFailure)

			entity := newRecoveringActor(persistenceID, enginetest.NewAccountEventSourcedBehavior(persistenceID))
			entity.eventsStore = enginetest.NewEventsStoreMock(ctrl)
			entity.encryptor = enginetest.NewEncryptorMock(ctrl)

			expectRecoveryFailure(ctx, entity, "failed to decrypt event at sequence 1", errStoreFailure)
		})

		s.It("with event unmarshal failure after decryption during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			encryptedPayload := &anypb.Any{TypeUrl: accountCreated(ctx, persistenceID).GetTypeUrl(), Value: []byte("fake-cipher")}
			event := persistedEvent(persistenceID, encryptedPayload)
			event.IsEncrypted = true
			event.EncryptionKeyId = "key-1"

			ctrl := specmock.NewController(ctx)
			holdEvent(ctrl, persistenceID, event)
			// return garbage bytes so proto.Unmarshal of the Any fails
			ctrl.Method("Decrypt").
				Expect(specmock.Any(), persistenceID, []byte("fake-cipher"), "key-1").
				Return([]byte("not-valid-proto"), nil)

			entity := newRecoveringActor(persistenceID, enginetest.NewAccountEventSourcedBehavior(persistenceID))
			entity.eventsStore = enginetest.NewEventsStoreMock(ctrl)
			entity.encryptor = enginetest.NewEncryptorMock(ctrl)

			expectRecoveryFailure(ctx, entity, "failed to decrypt event at sequence 1: failed to unmarshal decrypted payload", nil)
		})

		s.It("with event adapter chain failure during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			payload := accountCreated(ctx, persistenceID)

			ctrl := specmock.NewController(ctx)
			holdEvent(ctrl, persistenceID, persistedEvent(persistenceID, payload))
			ctrl.Method("Adapt").
				Expect(specmock.Any(), uint64(1)).
				Return(nil, errStoreFailure)

			entity := newRecoveringActor(persistenceID, enginetest.NewAccountEventSourcedBehavior(persistenceID))
			entity.eventsStore = enginetest.NewEventsStoreMock(ctrl)
			entity.eventAdapters = []eventadapter.EventAdapter{enginetest.NewEventAdapterMock(ctrl)}

			expectRecoveryFailure(ctx, entity, "failed to adapt event at sequence 1", errStoreFailure)
		})

		s.It("with event UnmarshalNew failure during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			// an event with an unknown TypeUrl so UnmarshalNew fails
			unknown := &anypb.Any{TypeUrl: "type.googleapis.com/unknown.TypeThatDoesNotExist", Value: []byte{}}

			ctrl := specmock.NewController(ctx)
			holdEvent(ctrl, persistenceID, persistedEvent(persistenceID, unknown))

			entity := newRecoveringActor(persistenceID, enginetest.NewAccountEventSourcedBehavior(persistenceID))
			entity.eventsStore = enginetest.NewEventsStoreMock(ctrl)

			expectRecoveryFailure(ctx, entity, "failed to unmarshal event at sequence 1", nil)
		})

		s.It("with HandleEvent failure during recovery", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()

			ctrl := specmock.NewController(ctx)
			holdEvent(ctrl, persistenceID, persistedEvent(persistenceID, accountCreated(ctx, persistenceID)))

			// a behavior that returns an error from HandleEvent
			entity := newRecoveringActor(persistenceID, enginetest.NewFailingHandleEventBehavior(persistenceID))
			entity.eventsStore = enginetest.NewEventsStoreMock(ctrl)

			expectRecoveryFailure(ctx, entity, "failed to handle event at sequence 1", enginetest.ErrHandleEvent)
		})
	})

}

// noopEventAdapter is a no-op event adapter that passes events through unchanged
type noopEventAdapter struct{}

func (a *noopEventAdapter) Adapt(event *anypb.Any, _ uint64) (*anypb.Any, error) {
	return event, nil
}

// TestEventSourcedActorGetStateDuringPersist is the permanent regression
// suite for the P1 read-after-write consistency bug: GetStateCommand must
// never observe entity.currentState while a direct (non-batched) persist
// write is unsettled (phasePersisting/phaseDirectReplying), and it must
// never receive a persist failure addressed to a different command.
func TestEventSourcedActorGetStateDuringPersist(t *testing.T) {
	// These cases need the actor system and keep one write in flight on purpose.
	// The empty Describe name keeps the old subtest names. The time.After guards
	// are reply timeouts: they only turn a hung actor into a failure, they never
	// pace the case.
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("read during persist waits for confirmation then returns new state", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			// The first write (the create) is confirmed at once, the second (the
			// credit) is held in flight until the case releases it.
			gate := newWriteGate()
			ctrl := specmock.NewController(ctx)
			expectStoreStartup(ctrl, persistenceID)
			ctrl.Method("WriteEvents").Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).Times(1)
			ctrl.Method("WriteEvents").Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).
				Do(gate.hold(nil)).Times(1)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)))
			gate.openOnCleanup(ctx)
			pid := rig.spawn(ctx, behavior)

			createState := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500}))
			ctx.Expect(createState.GetSequenceNumber()).ToEqual(uint64(1))

			credit := askInBackground(context.Background(), pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 100})
			gate.awaitStarted(ctx, "the credit write to enter phasePersisting")

			read := askInBackground(context.Background(), pid, &egopb.GetStateCommand{})

			// Wait until the GetStateCommand has stashed itself behind the in-flight
			// write: the stash then holds the credit command and the read. Only
			// then let that write complete.
			ctx.Eventually(stashSize(pid), specs.Equal(uint64(2)),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))

			select {
			case <-read.done:
				ctx.T.Fatal("GetStateCommand replied before the in-flight persist write was confirmed")
			default:
			}

			gate.open()

			credit.awaitWithin(ctx, askTimeout, "the credit command reply")
			creditState := stateReplyOf(ctx, credit.await(ctx))
			ctx.Expect(creditState.GetSequenceNumber()).ToEqual(uint64(2))

			read.awaitWithin(ctx, askTimeout, "the deferred GetStateCommand reply")
			readState := stateReplyOf(ctx, read.await(ctx))
			expectAccountState(ctx, readState, 2, &testpb.Account{AccountId: persistenceID, AccountBalance: 600.00})
		})

		s.It("two concurrent commands preserve order and correct recipient", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			// The create is confirmed at once, command A is held in flight, and
			// command B writes after it.
			gate := newWriteGate()
			ctrl := specmock.NewController(ctx)
			expectStoreStartup(ctrl, persistenceID)
			ctrl.Method("WriteEvents").Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).Times(1)
			ctrl.Method("WriteEvents").Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).
				Do(gate.hold(nil)).Times(1)
			ctrl.Method("WriteEvents").Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).Times(1)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)))
			gate.openOnCleanup(ctx)
			pid := rig.spawn(ctx, behavior)

			ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500})

			a := askInBackground(context.Background(), pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 100})
			gate.awaitStarted(ctx, "command A to enter phasePersisting")

			b := askInBackground(context.Background(), pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 50})

			// Wait until command B has stashed behind the in-flight write: the stash
			// then holds command A and command B.
			ctx.Eventually(stashSize(pid), specs.Equal(uint64(2)),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			gate.open()

			a.awaitWithin(ctx, askTimeout, "command A's reply")
			b.awaitWithin(ctx, askTimeout, "command B's reply")

			aState := stateReplyOf(ctx, a.await(ctx))
			expectAccountState(ctx, aState, 2, &testpb.Account{AccountId: persistenceID, AccountBalance: 600.00})

			bState := stateReplyOf(ctx, b.await(ctx))
			expectAccountState(ctx, bState, 3, &testpb.Account{AccountId: persistenceID, AccountBalance: 650.00})
		})

		s.It("persistence error reaches the originating command only", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			// The create is confirmed at once, the credit write is held in flight and
			// then fails.
			gate := newWriteGate()
			ctrl := specmock.NewController(ctx)
			expectStoreStartup(ctrl, persistenceID)
			ctrl.Method("WriteEvents").Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).Times(1)
			ctrl.Method("WriteEvents").Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).
				Do(gate.hold(errStoreFailure)).Times(1)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)))
			gate.openOnCleanup(ctx)
			pid := rig.spawn(ctx, behavior)

			ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500})

			credit := askInBackground(context.Background(), pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 100})
			gate.awaitStarted(ctx, "the credit write to enter phasePersisting")

			read := askInBackground(context.Background(), pid, &egopb.GetStateCommand{})

			// Wait until the deferred read has stashed behind the failing write: the
			// stash then holds the credit command and the read.
			ctx.Eventually(stashSize(pid), specs.Equal(uint64(2)),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			gate.open()

			credit.awaitWithin(ctx, askTimeout, "the credit command reply")
			creditMessage := errorReplyMessage(ctx, credit.await(ctx))
			ctx.Expect(creditMessage).To(containsText(errStoreFailure.Error()))

			// The originating command's persist failure sets directShutdown, so the
			// actor stops itself (pre-existing fail-fast behavior, unrelated to this
			// fix) before it can dispatch the redelivered, deferred GetStateCommand.
			// The contract this test protects is narrower than "the deferred read
			// gets a normal reply": it must never receive the error reply meant for
			// the command that actually failed. An Ask timeout against a
			// now-stopped actor satisfies that (it is clearly not the mistaken
			// error), whereas an ErrorReply carrying errStoreFailure's message would
			// prove the two commands' responses got cross-wired.
			read.awaitWithin(ctx, 8*time.Second, "the deferred GetStateCommand to settle")
			if read.err == nil {
				deferred := read.await(ctx)
				if _, ok := deferred.GetReply().(*egopb.CommandReply_ErrorReply); ok {
					// the deferred GetStateCommand must not receive the originating command's persist error
					ctx.Expect(deferred.GetErrorReply().GetMessage()).To(specs.Not(containsText(errStoreFailure.Error())))
				}
			}
		})
	})
}

func TestEventSourcedActorBatch(t *testing.T) {
	// These cases need the actor system: they spawn the real actor and its child
	// writers in batch mode. They never sleep: a spawn waits until the actor
	// reports itself running, a command waits for its reply (which batch mode
	// defers until the cycle flushes), and what a child actor does after the
	// reply (a snapshot) is polled. The empty Describe name keeps the old
	// subtest names.
	specs.Describe(t, "", func(s *specs.Spec) {
		s.It("sequential commands flush by timer", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			// the threshold is never reached, so each reply waits for the flush timer
			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500}))
			expectAccountState(ctx, state, 1, &testpb.Account{AccountId: persistenceID, AccountBalance: 500})

			state = stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 250}))
			expectAccountState(ctx, state, 2, &testpb.Account{AccountId: persistenceID, AccountBalance: 750})
		})

		s.It("concurrent commands flush by threshold", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   2,
				BatchFlushWindow: 10 * time.Second,
			})

			// the flush window is far longer than the reply timeout: only the
			// threshold can flush the two commands
			replies := askAll(ctx, pid,
				&testpb.CreateAccount{AccountBalance: 500},
				&testpb.CreateAccount{AccountBalance: 300})
			for _, reply := range replies {
				stateReplyOf(ctx, reply)
			}

			state := stateReplyOf(ctx, ask(ctx, pid, &egopb.GetStateCommand{}))
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(2))
		})

		s.It("no-event command replies immediately in batch mode", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.TestNoEvent{}))
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(0))
		})

		s.It("error command replies immediately in batch mode", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			reply := ask(ctx, pid, &testpb.CreditAccount{AccountId: "wrong-id", Balance: 100})
			ctx.Expect(errorReplyMessage(ctx, reply)).To(specs.Equal("command sent to the wrong entity"))
		})

		s.It("batch with snapshot boundary crossing", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			snapshotStore := connectedSnapshotStore(ctx)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewSnapshotStore(snapshotStore))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				SnapshotInterval: 2,
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500}))

			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 100}))
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(2))

			// the snapshot writer child persists after the reply
			ctx.Eventually(latestSnapshot(snapshotStore, persistenceID), beSnapshotAt(2),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
		})

		s.It("commands arriving during flush are stashed and processed", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   1,
				BatchFlushWindow: time.Second,
			})

			const numCommands = 5

			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 100}))

			credits := make([]proto.Message, numCommands)
			for i := range credits {
				credits[i] = &testpb.CreditAccount{AccountId: persistenceID, Balance: 10}
			}
			for _, reply := range askAll(ctx, pid, credits...) {
				stateReplyOf(ctx, reply)
			}

			state := stateReplyOf(ctx, ask(ctx, pid, &egopb.GetStateCommand{}))
			expectAccountState(ctx, state, numCommands+1,
				&testpb.Account{AccountId: persistenceID, AccountBalance: 100 + float64(numCommands)*10})
		})

		s.It("batch persist failure returns error replies and shuts down actor", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			ctrl := specmock.NewController(ctx)
			expectStoreStartup(ctrl, persistenceID)
			ctrl.Method("WriteEvents").
				Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).
				Return(errStoreFailure)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   1,
				BatchFlushWindow: time.Second,
			})

			errorReplyMessage(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500}))
		})

		s.It("batch persist failure with telemetry records metrics and ends spans", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			noopTracer := tracenoop.NewTracerProvider().Tracer("test")
			noopMeter := noop.NewMeterProvider().Meter("test")

			ctrl := specmock.NewController(ctx)
			expectStoreStartup(ctrl, persistenceID)
			ctrl.Method("WriteEvents").
				Expect(specmock.Any(), persistence.Unscoped(), specmock.Any(), specmock.Any()).
				Return(errStoreFailure)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)),
				extensions.NewTelemetryExtension(noopTracer, noopMeter))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   1,
				BatchFlushWindow: time.Second,
			})

			errorReplyMessage(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500}))
		})

		s.It("batch mode with encryption failure in processAndBatch", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			eventStore := connectedEventsStore(ctx)

			ctrl := specmock.NewController(ctx)
			ctrl.Method("Encrypt").
				Expect(specmock.Any(), persistenceID, specmock.Any()).
				Return(nil, "", errStoreFailure)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewEncryptor(enginetest.NewEncryptorMock(ctrl)))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			errorReplyMessage(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500}))
		})

		s.It("batch mode with telemetry traces commands", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			noopTracer := tracenoop.NewTracerProvider().Tracer("test")
			noopMeter := noop.NewMeterProvider().Meter("test")

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewTelemetryExtension(noopTracer, noopMeter))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500}))
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(1))
		})

		s.It("batch mode with telemetry handles no-event command", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			noopTracer := tracenoop.NewTracerProvider().Tracer("test")
			noopMeter := noop.NewMeterProvider().Meter("test")

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewTelemetryExtension(noopTracer, noopMeter))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			stateReplyOf(ctx, ask(ctx, pid, &testpb.TestNoEvent{}))
		})

		s.It("batch mode with telemetry handles error command", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			noopTracer := tracenoop.NewTracerProvider().Tracer("test")
			noopMeter := noop.NewMeterProvider().Meter("test")

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewTelemetryExtension(noopTracer, noopMeter))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			errorReplyMessage(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: "wrong-id", Balance: 100}))
		})

		s.It("batch mode with default flush window when only threshold is set", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{BatchThreshold: 100})

			// one command never reaches the threshold: the default flush window flushes it
			state := stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500}))
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(1))
		})

		s.It("unhandled non-command message in batch mode", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			ctx.Expect(goakt.Tell(context.Background(), pid, new(egopb.NoReply))).To(specs.BeNil())

			// The mailbox is ordered: the command below is read after the unhandled
			// message, so its reply proves the actor survived it.
			stateReplyOf(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500}))
		})

		s.It("stopping with a batch in flight leaves the batch state to the receive path", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx, extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}))
			entity := New()
			pid, err := rig.system.Spawn(context.Background(), behavior.ID(), entity,
				goakt.WithDependencies(behavior, &extensions.EntityConfig{
					BatchThreshold:   100,
					BatchFlushWindow: time.Hour,
				}),
				goakt.WithLongLived(), goakt.WithStashing())
			ctx.Expect(err).To(specs.BeNil())
			waitRunning(ctx, pid)

			batched := func() int {
				entity.batchMu.Lock()
				defer entity.batchMu.Unlock()
				return len(entity.batchEntries)
			}

			// the command waits in the open batch: the flush window is an hour
			ctx.Expect(goakt.Tell(context.Background(), pid, &testpb.CreateAccount{AccountBalance: 500})).To(specs.BeNil())
			ctx.Eventually(func() any { return batched() }, specs.Equal(1),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))

			ctx.Expect(rig.system.Kill(context.Background(), behavior.ID())).To(specs.BeNil())
			waitStopped(ctx, pid)

			// PostStop may run while a Receive turn is still reading the batch,
			// so it must not clear it: the next PreStart does
			ctx.Expect(batched()).To(specs.Equal(1))
		})

		s.It("batch mode with encryption failure and telemetry ends span", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			noopTracer := tracenoop.NewTracerProvider().Tracer("test")
			noopMeter := noop.NewMeterProvider().Meter("test")

			eventStore := connectedEventsStore(ctx)

			ctrl := specmock.NewController(ctx)
			ctrl.Method("Encrypt").
				Expect(specmock.Any(), persistenceID, specmock.Any()).
				Return(nil, "", errStoreFailure)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewEncryptor(enginetest.NewEncryptorMock(ctrl)),
				extensions.NewTelemetryExtension(noopTracer, noopMeter))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			errorReplyMessage(ctx, ask(ctx, pid, &testpb.CreateAccount{AccountBalance: 500}))
		})

		s.It("multiple commands batch with telemetry covers reply span and timer dedup", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			noopTracer := tracenoop.NewTracerProvider().Tracer("test")
			noopMeter := noop.NewMeterProvider().Meter("test")

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewTelemetryExtension(noopTracer, noopMeter))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   3,
				BatchFlushWindow: 10 * time.Second,
			})

			replies := askAll(ctx, pid,
				&testpb.CreateAccount{AccountBalance: 500},
				&testpb.CreditAccount{AccountId: persistenceID, Balance: 100},
				&testpb.CreditAccount{AccountId: persistenceID, Balance: 50})
			for _, reply := range replies {
				stateReplyOf(ctx, reply)
			}

			state := stateReplyOf(ctx, ask(ctx, pid, &egopb.GetStateCommand{}))
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(3))
		})

		s.It("batch with snapshot and telemetry", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			noopTracer := tracenoop.NewTracerProvider().Tracer("test")
			noopMeter := noop.NewMeterProvider().Meter("test")

			snapshotStore := connectedSnapshotStore(ctx)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewSnapshotStore(snapshotStore),
				extensions.NewTelemetryExtension(noopTracer, noopMeter))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				SnapshotInterval: 2,
				BatchThreshold:   2,
				BatchFlushWindow: 10 * time.Second,
			})

			replies := askAll(ctx, pid,
				&testpb.CreateAccount{AccountBalance: 500},
				&testpb.CreditAccount{AccountId: persistenceID, Balance: 100})
			for _, reply := range replies {
				stateReplyOf(ctx, reply)
			}

			// the snapshot writer child persists after the replies
			ctx.Eventually(latestSnapshot(snapshotStore, persistenceID), beSnapshotAt(2),
				specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
		})

		// Test: batch trace spans are properly connected
		//
		// Verifies end-to-end trace context propagation through the batch processing
		// pipeline when multiple commands are processed as a single batch.
		//
		// Setup:
		//   - A real TracerProvider with an InMemoryExporter captures all emitted spans.
		//   - The global TextMapPropagator is configured with W3C TraceContext + Baggage,
		//     mirroring what Engine.Start does for the GoAkt context propagator
		//     (otelContextPropagator) to inject/extract trace context across actor boundaries.
		//   - Batch threshold is set to 3 with a long flush window so the batch flushes
		//     only when the threshold is reached (not by timer).
		//   - A parent span ("test.batch.parent") is created and its context is passed
		//     through goakt.Ask to the actor, simulating an inbound traced request.
		//
		// Assertions:
		//   - Exactly 3 "urd.command" spans are produced (one per batched command).
		//   - Every command span shares the same TraceID as the parent span, proving
		//     trace context flows from the caller through GoAkt into processAndBatch.
		//   - Every command span's Parent.SpanID equals the parent span's SpanID,
		//     proving direct parent-child linkage.
		//   - Every command span has a non-zero EndTime, proving the span lifecycle
		//     completes after replyFromBatch sends the pre-computed reply.
		//   - Every command span carries the ego.persistence_id and ego.command_type
		//     attributes set by processAndBatch.
		//   - All command spans have distinct SpanIDs (no accidental reuse).
		s.It("batch trace spans are properly connected", func(ctx *specs.Context) {
			recorder := newSpanRecorder(ctx)

			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewTelemetryExtension(recorder.tracer, noop.NewMeterProvider().Meter("test")))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   3,
				BatchFlushWindow: 10 * time.Second,
			})

			// Create a parent span to verify that child spans are properly linked.
			parentCtx, parentSpan := recorder.tracer.Start(context.Background(), "test.batch.parent")

			replies := askAllWith(ctx, parentCtx, pid,
				&testpb.CreateAccount{AccountBalance: 500},
				&testpb.CreditAccount{AccountId: persistenceID, Balance: 100},
				&testpb.CreditAccount{AccountId: persistenceID, Balance: 50})
			parentSpan.End()
			for _, reply := range replies {
				stateReplyOf(ctx, reply)
			}

			spans := recorder.spans(ctx)

			// Collect "urd.command" spans (one per batched command).
			commandSpans := spansNamed(spans, "urd.command")

			parent := findSpan(spans, "test.batch.parent")
			ctx.Expect(parent).To(specs.Not(specs.BeNil()))
			// each command in the batch should produce an ego.command span
			ctx.Expect(commandSpans).To(specs.HaveLen(3))

			// all command spans belong to the same trace as the parent
			ctx.Expect(commandSpans).To(specs.EveryElement(spanSatisfies("shares the parent trace ID",
				func(cs tracetest.SpanStub) bool { return cs.SpanContext.TraceID() == parent.SpanContext.TraceID() })))

			// each command span is a direct child of the parent span
			ctx.Expect(commandSpans).To(specs.EveryElement(spanSatisfies("is a child of the parent span",
				func(cs tracetest.SpanStub) bool { return cs.Parent.SpanID() == parent.SpanContext.SpanID() })))

			// the span has been ended (EndTime is set) after replyFromBatch
			ctx.Expect(commandSpans).To(specs.EveryElement(spanSatisfies("is ended",
				func(cs tracetest.SpanStub) bool { return !cs.EndTime.IsZero() })))

			// required observability attributes are present
			ctx.Expect(commandSpans).To(specs.EveryElement(spanSatisfies("has the ego.persistence_id attribute",
				func(cs tracetest.SpanStub) bool { return spanAttribute(cs, "urd.persistence_id") == persistenceID })))
			ctx.Expect(commandSpans).To(specs.EveryElement(spanSatisfies("has the ego.command_type attribute",
				func(cs tracetest.SpanStub) bool { return spanAttribute(cs, "urd.command_type") != "" })))

			// each command span has a unique span ID (no accidental reuse)
			spanIDs := make(map[trace.SpanID]struct{})
			for _, cs := range commandSpans {
				spanIDs[cs.SpanContext.SpanID()] = struct{}{}
			}
			ctx.Expect(spanIDs).To(specs.HaveLen(len(commandSpans)))
		})

		// Test: batch trace spans are ended on error
		//
		// Verifies that when HandleCommand returns an error the "urd.command" span
		// is still created and properly ended, rather than being leaked.
		//
		// Setup:
		//   - Real TracerProvider + InMemoryExporter + global TextMapPropagator.
		//   - A single CreditAccount command is sent with a wrong account ID,
		//     causing HandleCommand to return an error inside processAndBatch.
		//   - The parent span ("test.error.parent") provides the trace context.
		//
		// Assertions:
		//   - Exactly 1 "urd.command" span is produced despite the error.
		//   - The span has a non-zero EndTime, proving processAndBatch called
		//     span.End() on the error path before sendErrorReply.
		//   - The span shares the parent's TraceID (trace context propagated).
		//   - The span's Parent.SpanID equals the parent span's SpanID
		//     (direct parent-child linkage preserved even on failure).
		s.It("batch trace spans are ended on error", func(ctx *specs.Context) {
			recorder := newSpanRecorder(ctx)

			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewTelemetryExtension(recorder.tracer, noop.NewMeterProvider().Meter("test")))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			parentCtx, parentSpan := recorder.tracer.Start(context.Background(), "test.error.parent")

			// Send a command that will fail (CreditAccount on non-existent account).
			reply := askWith(ctx, parentCtx, pid, &testpb.CreditAccount{AccountId: "wrong-id", Balance: 100})
			parentSpan.End()

			errorReplyMessage(ctx, reply)

			expectOneEndedCommandSpan(ctx, recorder.spans(ctx), "test.error.parent")
		})

		// Test: batch trace spans with no-event command are ended immediately
		//
		// Verifies that when HandleCommand returns zero events the "urd.command"
		// span is ended immediately inside processAndBatch — it must not be
		// deferred to the batch flush cycle, because the command is answered
		// inline without entering the batch buffer.
		//
		// Setup:
		//   - Real TracerProvider + InMemoryExporter + global TextMapPropagator.
		//   - A TestNoEvent command is sent, which the behavior handles by
		//     returning an empty event slice.
		//   - The parent span ("test.noevent.parent") provides the trace context.
		//
		// Assertions:
		//   - Exactly 1 "urd.command" span is produced for the no-event command.
		//   - The span has a non-zero EndTime, proving processAndBatch called
		//     span.End() immediately when len(events) == 0.
		//   - The span shares the parent's TraceID (trace context propagated).
		//   - The span's Parent.SpanID equals the parent span's SpanID
		//     (direct parent-child linkage).
		s.It("batch trace spans with no-event command are ended immediately", func(ctx *specs.Context) {
			recorder := newSpanRecorder(ctx)

			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			rig := startActorRig(ctx,
				extensions.NewEventsStore(retentionFixture{connectedEventsStore(ctx)}),
				extensions.NewTelemetryExtension(recorder.tracer, noop.NewMeterProvider().Meter("test")))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			parentCtx, parentSpan := recorder.tracer.Start(context.Background(), "test.noevent.parent")

			reply := askWith(ctx, parentCtx, pid, &testpb.TestNoEvent{})
			parentSpan.End()

			stateReplyOf(ctx, reply)

			expectOneEndedCommandSpan(ctx, recorder.spans(ctx), "test.noevent.parent")
		})

		// Test: batch trace spans with encryption failure are ended
		//
		// Verifies that when buildEnvelopes fails due to an encryption error the
		// "urd.command" span is still ended, preventing span leaks on the
		// encryption-failure path inside processAndBatch.
		//
		// Setup:
		//   - Real TracerProvider + InMemoryExporter + global TextMapPropagator.
		//   - A mock Encryptor is wired to return an error for any Encrypt call.
		//   - A CreateAccount command (which produces events) triggers buildEnvelopes,
		//     which calls the encryptor and fails.
		//   - The parent span ("test.encrypt.parent") provides the trace context.
		//
		// Assertions:
		//   - Exactly 1 "urd.command" span is produced despite the encryption failure.
		//   - The span has a non-zero EndTime, proving processAndBatch called
		//     span.End() on the buildEnvelopes error path before sendErrorReply.
		//   - The span shares the parent's TraceID (trace context propagated).
		//   - The span's Parent.SpanID equals the parent span's SpanID
		//     (direct parent-child linkage preserved on encryption failure).
		s.It("batch trace spans with encryption failure are ended", func(ctx *specs.Context) {
			recorder := newSpanRecorder(ctx)

			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			eventStore := connectedEventsStore(ctx)

			ctrl := specmock.NewController(ctx)
			ctrl.Method("Encrypt").
				Expect(specmock.Any(), persistenceID, specmock.Any()).
				Return(nil, "", errStoreFailure)

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewEncryptor(enginetest.NewEncryptorMock(ctrl)),
				extensions.NewTelemetryExtension(recorder.tracer, noop.NewMeterProvider().Meter("test")))
			pid := rig.spawn(ctx, behavior, &extensions.EntityConfig{
				BatchThreshold:   100,
				BatchFlushWindow: 100 * time.Millisecond,
			})

			parentCtx, parentSpan := recorder.tracer.Start(context.Background(), "test.encrypt.parent")

			reply := askWith(ctx, parentCtx, pid, &testpb.CreateAccount{AccountBalance: 500})
			parentSpan.End()

			errorReplyMessage(ctx, reply)

			expectOneEndedCommandSpan(ctx, recorder.spans(ctx), "test.encrypt.parent")
		})
	})
}

// spanRecorder is a real tracer provider that exports every ended span to an
// in-memory exporter, with the global propagator set the way Engine.Start sets
// it so trace context crosses the actor boundary. Create it before the rig of
// its case: cleanups run last registered first, so the system stops before the
// provider shuts down.
type spanRecorder struct {
	exporter *tracetest.InMemoryExporter
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
}

func newSpanRecorder(ctx *specs.Context) *spanRecorder {
	// This is required for the GoAkt context propagator (otelContextPropagator)
	// to correctly inject/extract trace context across actor boundaries.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	ctx.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	return &spanRecorder{exporter: exporter, provider: provider, tracer: provider.Tracer("urd-test")}
}

// spans force-flushes the provider so every ended span is exported, and returns
// them.
func (r *spanRecorder) spans(ctx *specs.Context) tracetest.SpanStubs {
	ctx.Expect(r.provider.ForceFlush(context.Background())).To(specs.BeNil())
	return r.exporter.GetSpans()
}

// spansNamed returns the spans with the given name.
func spansNamed(spans tracetest.SpanStubs, name string) []tracetest.SpanStub {
	var named []tracetest.SpanStub
	for i := range spans {
		if spans[i].Name == name {
			named = append(named, spans[i])
		}
	}
	return named
}

// spanSatisfies matches a span that satisfies ok.
func spanSatisfies(description string, ok func(tracetest.SpanStub) bool) specs.Matcher {
	return specs.Satisfy(description, func(v any) bool {
		span, isSpan := v.(tracetest.SpanStub)
		return isSpan && ok(span)
	})
}

// spanAttribute returns the string value of the span attribute key, or "".
func spanAttribute(span tracetest.SpanStub, key string) string {
	for _, attr := range span.Attributes {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}

// expectOneEndedCommandSpan requires exactly one ego.command span among spans,
// ended, and a direct child of the span called parentName, in the same trace.
func expectOneEndedCommandSpan(ctx *specs.Context, spans tracetest.SpanStubs, parentName string) {
	commandSpans := spansNamed(spans, "urd.command")

	// a span is still produced, whatever way the command ended
	ctx.Expect(commandSpans).To(specs.HaveLen(1))
	cs := commandSpans[0]

	// the span was ended
	ctx.Expect(cs.EndTime.IsZero()).To(specs.BeFalse())

	parent := findSpan(spans, parentName)
	ctx.Expect(parent).To(specs.Not(specs.BeNil()))

	// trace context propagated through GoAkt into the actor
	ctx.Expect(cs.SpanContext.TraceID()).ToEqual(parent.SpanContext.TraceID())

	// direct parent-child linkage preserved
	ctx.Expect(cs.Parent.SpanID()).ToEqual(parent.SpanContext.SpanID())
}

// findSpan returns the first SpanStub with the given name, or nil.
func findSpan(spans tracetest.SpanStubs, name string) *tracetest.SpanStub {
	for i := range spans {
		if spans[i].Name == name {
			return &spans[i]
		}
	}
	return nil
}

// errStoreFailure is the error the mocked ports return in the failure cases.
var errStoreFailure = errors.New("event sourced actor test: port failure")
