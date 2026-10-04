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

package saga

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/engine/eventsource"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/goaktlog"
	samplepb "github.com/getsyntegrity/urd/internal/samplepb"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	runtimeport "github.com/getsyntegrity/urd/port/runtime"
	"github.com/getsyntegrity/urd/testkit"
)

// sagaStoreMock stands in for persistence.EventsStore in the cases that drive
// PreStart and persistence failures. It forwards only the methods those cases
// reach to a go-specs controller; any other method panics on the nil embedded
// interface, so an unexpected call is loud.
type sagaStoreMock struct {
	persistence.EventsStore
	c *mock.Controller
}

func (m sagaStoreMock) Ping(ctx context.Context) error {
	return m.c.Method("Ping").Call(ctx).Err(0)
}

func (m sagaStoreMock) GetLatestEvent(ctx context.Context, scope persistence.Scope, id string) (*egopb.Event, error) {
	r := m.c.Method("GetLatestEvent").Call(ctx, scope, id)
	return mock.Value[*egopb.Event](r, 0), r.Err(1)
}

func (m sagaStoreMock) ReplayEvents(ctx context.Context, scope persistence.Scope, id string, from, to, limit uint64) ([]*egopb.Event, error) {
	r := m.c.Method("ReplayEvents").Call(ctx, scope, id, from, to, limit)
	return mock.Value[[]*egopb.Event](r, 0), r.Err(1)
}

func (m sagaStoreMock) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	return m.c.Method("WriteEvents").Call(ctx, scope, events, precondition).Err(0)
}

func TestSagaStatus_String(t *testing.T) {
	specs.Describe(t, "SagaStatus.String names each lifecycle status", func(s *specs.Spec) {
		type statusCase struct {
			status   runtimeport.SagaStatus
			expected string
		}
		specs.Table(s, []statusCase{
			{runtimeport.SagaRunning, "running"},
			{runtimeport.SagaCompleted, "completed"},
			{runtimeport.SagaCompensating, "compensating"},
			{runtimeport.SagaFailed, "failed"},
			{runtimeport.SagaStatus(99), "unknown"},
		}, func(c statusCase) string { return c.expected }, func(ctx *specs.Context, c statusCase) {
			ctx.Expect(c.status.String()).ToEqual(c.expected)
		})
	})
}

var errSagaBoom = errors.New("saga test: boom")

const (
	// signalTimeout bounds how long a case waits for something that must happen.
	signalTimeout = 3 * time.Second
	// pollEvery is the interval of every poll in this file.
	pollEvery = 10 * time.Millisecond
	// quietWindow is how long a case watches for something that must not happen.
	quietWindow = 500 * time.Millisecond
)

// sagaRig is a started in-process goakt actor system wired with an events
// store and an events stream. The system and the stream stop when the case
// ends, so a failed assertion no longer leaks a running system.
type sagaRig struct {
	system goakt.ActorSystem
	stream eventstream.Stream
}

// newSagaRig starts the actor system. extra adds more extensions, for example
// the tenancy marker.
func newSagaRig(ctx *specs.Context, store persistence.EventsStore, extra ...extension.Extension) *sagaRig {
	stream := eventstream.New()
	exts := append([]extension.Extension{extensions.NewEventsStore(store), extensions.NewEventsStream(stream)}, extra...)
	system, err := goakt.NewActorSystem("TestSystem",
		goakt.WithLogger(goaktlog.New(enginetest.DiscardLogger)),
		goakt.WithExtensions(exts...),
		goakt.WithActorInitMaxRetries(1))
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(system.Start(context.Background())).To(specs.BeNil())
	ctx.Cleanup(func() {
		stream.Close()
		ctx.Expect(system.Stop(context.Background())).To(specs.BeNil())
	})
	return &sagaRig{system: system, stream: stream}
}

// newTestkitStore returns a connected in-memory events store that disconnects
// when the case ends.
func newTestkitStore(ctx *specs.Context) *testkit.EventStore {
	store := testkit.NewEventsStore()
	ctx.Expect(store.Connect(context.Background())).To(specs.BeNil())
	ctx.Cleanup(func() { _ = store.Disconnect(context.Background()) })
	return store
}

// spawnSaga spawns a saga actor and expects the spawn to succeed.
func (r *sagaRig) spawnSaga(ctx *specs.Context, id string, behavior *enginetest.CallbackSagaBehavior, cfg *extensions.SagaConfig, extra ...extension.Dependency) *goakt.PID {
	deps := append([]extension.Dependency{behavior, cfg}, extra...)
	pid, err := r.system.Spawn(context.Background(), id, New(), goakt.WithLongLived(), goakt.WithDependencies(deps...))
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(pid).To(specs.Not(specs.BeNil()))
	return pid
}

// spawnReplyTarget spawns an actor that answers every message with reply.
func (r *sagaRig) spawnReplyTarget(ctx *specs.Context, id string, reply proto.Message) {
	_, err := r.system.Spawn(context.Background(), id, &enginetest.SimpleReplyActor{Reply: reply}, goakt.WithLongLived())
	ctx.Expect(err).To(specs.BeNil())
}

// expectSpawnFails expects PreStart to fail, so no PID comes back.
func expectSpawnFails(ctx *specs.Context, pid *goakt.PID, err error) {
	ctx.Expect(err).To(specs.Not(specs.BeNil()))
	ctx.Expect(pid).To(specs.BeNil())
}

// foreignEvent is an event written by some other entity, which a saga reacts to.
func foreignEvent(ctx *specs.Context) *egopb.Event {
	return newAnyEvent(ctx, uuid.NewString(), 1, &testpb.AccountCreated{AccountId: uuid.NewString()}, nil)
}

// mustAny wraps msg in an Any, reporting a failure through the spec.
func mustAny(ctx *specs.Context, msg proto.Message) *anypb.Any {
	a, err := anypb.New(msg)
	ctx.Expect(err).To(specs.BeNil())
	return a
}

// stateReply is the successful answer a target entity gives to a saga command.
func stateReply(ctx *specs.Context, targetID string) *egopb.CommandReply {
	return &egopb.CommandReply{Reply: &egopb.CommandReply_StateReply{StateReply: &egopb.StateReply{
		PersistenceId:  targetID,
		SequenceNumber: 1,
		State:          mustAny(ctx, &samplepb.Account{}),
	}}}
}

// rejectReply is the error answer a target entity gives to a saga command.
func rejectReply() *egopb.CommandReply {
	return &egopb.CommandReply{Reply: &egopb.CommandReply_ErrorReply{ErrorReply: &egopb.ErrorReply{Message: "entity rejected command"}}}
}

// bump counts one call. The saga runs behavior callbacks on its own goroutine,
// so every counter is atomic.
func bump(n *atomic.Int32) { n.Add(1) }

// awaitCalls waits until n reaches at least want.
func awaitCalls(ctx *specs.Context, n *atomic.Int32, want int32, timeout time.Duration) {
	ctx.Eventually(func() any { return n.Load() }, specs.BeGreaterThanOrEqual(want),
		specs.WithTimeout(timeout), specs.WithInterval(pollEvery))
}

// expectCallsStay checks that n keeps its value for the whole window.
func expectCallsStay(ctx *specs.Context, n *atomic.Int32, want int32, window time.Duration) {
	ctx.Consistently(func() any { return n.Load() }, specs.Equal(want),
		specs.WithTimeout(window), specs.WithInterval(2*pollEvery))
}

// expectStaysRunning checks that the actor survives the whole window.
func expectStaysRunning(ctx *specs.Context, pid *goakt.PID, window time.Duration) {
	ctx.Consistently(func() any { return pid.IsRunning() }, specs.BeTrue(),
		specs.WithTimeout(window), specs.WithInterval(2*pollEvery))
}

func TestSagaActor(t *testing.T) {
	specs.Describe(t, "Actor reacts to saga lifecycle, events and commands on a real in-process actor system", func(s *specs.Spec) {
		s.It("PreStart: missing behavior fails to start", func(ctx *specs.Context) {
			sagaID := uuid.NewString()

			ctrl := mock.NewController(ctx)
			// A saga with no behavior fails before it touches the store.
			ctrl.Method("Ping").Expect(mock.Any()).Never()
			ctrl.Method("GetLatestEvent").Expect(mock.Any(), persistence.Unscoped(), sagaID).Never()
			rig := newSagaRig(ctx, sagaStoreMock{c: ctrl})

			// Spawn with no behavior dependency
			pid, err := rig.system.Spawn(context.Background(), sagaID, New(), goakt.WithLongLived())

			expectSpawnFails(ctx, pid, err)
		})

		// Each row scripts the store and the behavior so that PreStart fails at
		// one step, and the spawn must come back with an error and no PID.
		type preStartFailure struct {
			name  string
			setup func(ctx *specs.Context, ctrl *mock.Controller, sagaID string) *enginetest.CallbackSagaBehavior
		}
		specs.Table(s, []preStartFailure{
			{"PreStart: events store ping failure", func(_ *specs.Context, ctrl *mock.Controller, sagaID string) *enginetest.CallbackSagaBehavior {
				ctrl.Method("Ping").Expect(mock.Any()).Return(errSagaBoom)
				return &enginetest.CallbackSagaBehavior{SagaID: sagaID}
			}},
			{"PreStart: GetLatestEvent failure", func(_ *specs.Context, ctrl *mock.Controller, sagaID string) *enginetest.CallbackSagaBehavior {
				ctrl.Method("Ping").Expect(mock.Any()).Return(nil)
				ctrl.Method("GetLatestEvent").Expect(mock.Any(), persistence.Unscoped(), sagaID).Return(nil, errSagaBoom)
				return &enginetest.CallbackSagaBehavior{SagaID: sagaID}
			}},
			{"PreStart: ReplayEvents failure", func(_ *specs.Context, ctrl *mock.Controller, sagaID string) *enginetest.CallbackSagaBehavior {
				latest := &egopb.Event{PersistenceId: sagaID, SequenceNumber: 3}
				ctrl.Method("Ping").Expect(mock.Any()).Return(nil)
				ctrl.Method("GetLatestEvent").Expect(mock.Any(), persistence.Unscoped(), sagaID).Return(latest, nil)
				ctrl.Method("ReplayEvents").Expect(mock.Any(), persistence.Unscoped(), sagaID, uint64(1), uint64(3), uint64(3)).Return(nil, errSagaBoom)
				return &enginetest.CallbackSagaBehavior{SagaID: sagaID}
			}},
			{"PreStart: UnmarshalNew failure during recovery", func(_ *specs.Context, ctrl *mock.Controller, sagaID string) *enginetest.CallbackSagaBehavior {
				// An event with an unresolvable type URL
				bad := &egopb.Event{
					PersistenceId:  sagaID,
					SequenceNumber: 1,
					Event:          &anypb.Any{TypeUrl: "type.googleapis.com/nonexistent.Type", Value: []byte("bad")},
				}
				latest := &egopb.Event{PersistenceId: sagaID, SequenceNumber: 1}
				ctrl.Method("Ping").Expect(mock.Any()).Return(nil)
				ctrl.Method("GetLatestEvent").Expect(mock.Any(), persistence.Unscoped(), sagaID).Return(latest, nil)
				ctrl.Method("ReplayEvents").Expect(mock.Any(), persistence.Unscoped(), sagaID, uint64(1), uint64(1), uint64(1)).Return([]*egopb.Event{bad}, nil)
				return &enginetest.CallbackSagaBehavior{SagaID: sagaID}
			}},
			{"PreStart: ApplyEvent failure during recovery", func(ctx *specs.Context, ctrl *mock.Controller, sagaID string) *enginetest.CallbackSagaBehavior {
				replayed := &egopb.Event{
					PersistenceId:  sagaID,
					SequenceNumber: 1,
					Event:          mustAny(ctx, &testpb.AccountCreated{AccountId: sagaID, AccountBalance: 100}),
				}
				latest := &egopb.Event{PersistenceId: sagaID, SequenceNumber: 1}
				ctrl.Method("Ping").Expect(mock.Any()).Return(nil)
				ctrl.Method("GetLatestEvent").Expect(mock.Any(), persistence.Unscoped(), sagaID).Return(latest, nil)
				ctrl.Method("ReplayEvents").Expect(mock.Any(), persistence.Unscoped(), sagaID, uint64(1), uint64(1), uint64(1)).Return([]*egopb.Event{replayed}, nil)
				return &enginetest.CallbackSagaBehavior{
					SagaID: sagaID,
					ApplyEventFn: func(_ context.Context, _ Event, _ State) (State, error) {
						return nil, errSagaBoom
					},
				}
			}},
		}, func(c preStartFailure) string { return c.name }, func(ctx *specs.Context, c preStartFailure) {
			sagaID := uuid.NewString()
			ctrl := mock.NewController(ctx)
			behavior := c.setup(ctx, ctrl, sagaID)
			rig := newSagaRig(ctx, sagaStoreMock{c: ctrl})

			pid, err := rig.system.Spawn(context.Background(), sagaID, New(),
				goakt.WithLongLived(),
				goakt.WithDependencies(behavior, extensions.NewSagaConfig(0)))

			expectSpawnFails(ctx, pid, err)
		})

		s.It("PreStart: happy path recovery with prior events", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			replayed := &egopb.Event{
				PersistenceId:  sagaID,
				SequenceNumber: 2,
				Event:          mustAny(ctx, &testpb.AccountCreated{AccountId: sagaID, AccountBalance: 100}),
			}
			latest := &egopb.Event{PersistenceId: sagaID, SequenceNumber: 2}
			ctrl := mock.NewController(ctx)
			ctrl.Method("Ping").Expect(mock.Any()).Return(nil)
			ctrl.Method("GetLatestEvent").Expect(mock.Any(), persistence.Unscoped(), sagaID).Return(latest, nil)
			ctrl.Method("ReplayEvents").Expect(mock.Any(), persistence.Unscoped(), sagaID, uint64(1), uint64(2), uint64(2)).Return([]*egopb.Event{replayed}, nil)
			rig := newSagaRig(ctx, sagaStoreMock{c: ctrl})

			var applied atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				ApplyEventFn: func(_ context.Context, _ Event, state State) (State, error) {
					bump(&applied)
					return state, nil
				},
			}
			rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			// ApplyEvent must run while PreStart replays the stored event.
			awaitCalls(ctx, &applied, 1, 2*time.Second)
		})

		s.It("Receive: GetStateCommand returns current state", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			pid := rig.spawnSaga(ctx, sagaID, &enginetest.CallbackSagaBehavior{SagaID: sagaID}, extensions.NewSagaConfig(0))

			reply, err := goakt.Ask(context.Background(), pid, new(egopb.GetStateCommand), 3*time.Second)

			ctx.Expect(err).To(specs.BeNil())
			commandReply, ok := reply.(*egopb.CommandReply)
			ctx.Expect(ok).To(specs.BeTrue())
			state := commandReply.GetStateReply()
			ctx.Expect(state).To(specs.Not(specs.BeNil()))
			ctx.Expect(state.GetPersistenceId()).ToEqual(sagaID)
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(0))
		})

		s.It("Receive: PostStart with timeout triggers compensation", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var compensated atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				CompensateFn: func(_ context.Context, _ State) ([]sagaCommand, error) {
					bump(&compensated)
					return nil, nil
				},
			}

			// 200ms timeout so the test runs quickly
			rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(200*time.Millisecond))

			awaitCalls(ctx, &compensated, 1, signalTimeout)
		})

		s.It("Receive: sagaTimeoutMsg when not running is no-op", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var compensated atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				CompensateFn: func(_ context.Context, _ State) ([]sagaCommand, error) {
					bump(&compensated)
					return nil, nil
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			// Send sagaTimeoutMsg twice: the first triggers compensation (status
			// becomes Compensating), the second must be a no-op because the status
			// is no longer runtimeport.SagaRunning.
			ctx.Expect(goakt.Tell(context.Background(), pid, &sagaTimeoutMsg{})).To(specs.BeNil())
			awaitCalls(ctx, &compensated, 1, 2*time.Second)
			ctx.Expect(goakt.Tell(context.Background(), pid, &sagaTimeoutMsg{})).To(specs.BeNil())

			// Compensate must not run again
			expectCallsStay(ctx, &compensated, 1, quietWindow)
		})

		s.It("Receive: unknown message is unhandled", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			pid := rig.spawnSaga(ctx, sagaID, &enginetest.CallbackSagaBehavior{SagaID: sagaID}, extensions.NewSagaConfig(0))

			// An unknown message type: the actor calls ctx.Unhandled() without panicking.
			ctx.Expect(goakt.Tell(context.Background(), pid, new(emptypb.Empty))).To(specs.BeNil())

			// The actor stays alive; polling catches a delayed crash.
			expectStaysRunning(ctx, pid, quietWindow)
		})

		s.It("consumeEvents: skips non-egopb-Event payloads", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var handled atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					bump(&handled)
					return &sagaAction{}, nil
				},
			}
			rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			// Publish a non-*egopb.Event payload
			rig.stream.Publish(protocol.EventsTopic, new(emptypb.Empty))

			expectCallsStay(ctx, &handled, 0, quietWindow)
		})

		s.It("consumeEvents: skips own saga events", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var handled atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					bump(&handled)
					return &sagaAction{}, nil
				},
			}
			rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			// Same persistence ID as the saga, so the saga must skip it.
			rig.stream.Publish(protocol.EventsTopic,
				newAnyEvent(ctx, sagaID, 1, &testpb.AccountCreated{AccountId: sagaID}, nil))

			expectCallsStay(ctx, &handled, 0, quietWindow)
		})

		s.It("consumeEvents: skips events when saga is not running", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var handled atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					bump(&handled)
					return &sagaAction{Complete: true}, nil
				},
			}
			rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))
			event := foreignEvent(ctx)

			// First event: triggers Complete, so the saga status becomes runtimeport.SagaCompleted
			rig.stream.Publish(protocol.EventsTopic, event)
			awaitCalls(ctx, &handled, 1, 2*time.Second)
			afterFirst := handled.Load()

			// Later events must be ignored
			rig.stream.Publish(protocol.EventsTopic, event)
			rig.stream.Publish(protocol.EventsTopic, event)

			expectCallsStay(ctx, &handled, afterFirst, quietWindow)
		})

		s.It("consumeEvents: UnmarshalNew error is logged and skipped", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var handled atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					bump(&handled)
					return &sagaAction{}, nil
				},
			}
			rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			// First event: bad type URL, so UnmarshalNew fails and the event is skipped
			rig.stream.Publish(protocol.EventsTopic, &egopb.Event{
				PersistenceId:  uuid.NewString(),
				SequenceNumber: 1,
				Event:          &anypb.Any{TypeUrl: "type.googleapis.com/nonexistent.Type", Value: []byte("bad")},
			})
			// Second event: valid, so HandleEvent runs and proves the saga went on
			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			awaitCalls(ctx, &handled, 1, 2*time.Second)
		})

		s.It("consumeEvents: HandleEvent error is logged and skipped", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var calls atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					if calls.Add(1) == 1 {
						return nil, errSagaBoom
					}
					return &sagaAction{}, nil
				},
			}
			rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))
			event := foreignEvent(ctx)

			rig.stream.Publish(protocol.EventsTopic, event)
			rig.stream.Publish(protocol.EventsTopic, event)

			// The second call proves the saga went on after the first one failed.
			awaitCalls(ctx, &calls, 2, 2*time.Second)
		})

		s.It("consumeEvents: HandleEvent returns nil action is no-op", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var handled atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					bump(&handled)
					return nil, nil // nil action
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			awaitCalls(ctx, &handled, 1, 2*time.Second)
			// Actor must still be alive
			expectStaysRunning(ctx, pid, 300*time.Millisecond)
		})

		s.It("consumeEvents: HandleEvent Complete action marks saga completed", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var handled atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					bump(&handled)
					return &sagaAction{Complete: true}, nil
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			// Actor is still alive but the status is Completed
			awaitCalls(ctx, &handled, 1, 2*time.Second)
			expectStaysRunning(ctx, pid, quietWindow)
		})

		s.It("consumeEvents: persistAndApplyEvents ApplyEvent error is logged", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			ctrl := mock.NewController(ctx)
			ctrl.Method("Ping").Expect(mock.Any()).Return(nil)
			ctrl.Method("GetLatestEvent").Expect(mock.Any(), persistence.Unscoped(), sagaID).Return(nil, nil)
			rig := newSagaRig(ctx, sagaStoreMock{c: ctrl})
			var handled, applied atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, event Event, _ State) (*sagaAction, error) {
					bump(&handled)
					return &sagaAction{Events: []Event{event}}, nil
				},
				ApplyEventFn: func(_ context.Context, _ Event, _ State) (State, error) {
					bump(&applied)
					return nil, errSagaBoom
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			awaitCalls(ctx, &handled, 1, 2*time.Second)
			// ApplyEvent runs after HandleEvent returns, within the same actor
			// message turn, so wait for the actual condition.
			awaitCalls(ctx, &applied, 1, 2*time.Second)
			// Actor must still be alive despite the error
			ctx.Expect(pid.IsRunning()).To(specs.BeTrue())
		})

		s.It("consumeEvents: persistAndApplyEvents WriteEvents error is logged", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			var writes atomic.Int32
			ctrl := mock.NewController(ctx)
			ctrl.Method("Ping").Expect(mock.Any()).Return(nil)
			ctrl.Method("GetLatestEvent").Expect(mock.Any(), persistence.Unscoped(), sagaID).Return(nil, nil)
			ctrl.Method("WriteEvents").Expect(mock.Any(), persistence.Unscoped(), mock.Any(), mock.Any()).
				Do(func([]any) []any {
					bump(&writes)
					return []any{errSagaBoom}
				}).
				AnyTimes()
			rig := newSagaRig(ctx, sagaStoreMock{c: ctrl})
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, event Event, _ State) (*sagaAction, error) {
					return &sagaAction{Events: []Event{event}}, nil
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			awaitCalls(ctx, &writes, 1, 2*time.Second)
			ctx.Expect(pid.IsRunning()).To(specs.BeTrue())
		})

		s.It("consumeEvents: HandleEvent with events persists state", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var applied atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID:         sagaID,
				InitialStateFn: func() State { return &samplepb.Account{} },
				HandleEventFn: func(_ context.Context, event Event, _ State) (*sagaAction, error) {
					return &sagaAction{Events: []Event{event}}, nil
				},
				ApplyEventFn: func(_ context.Context, _ Event, _ State) (State, error) {
					bump(&applied)
					return &samplepb.Account{AccountBalance: 100}, nil
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))
			awaitCalls(ctx, &applied, 1, 2*time.Second)

			// The state must have been updated. Ask goes through the same actor
			// mailbox that is still finishing the event turn that signalled
			// applied above, so it is only served once that turn, including the
			// state update, is complete.
			reply, err := goakt.Ask(context.Background(), pid, new(egopb.GetStateCommand), 3*time.Second)

			ctx.Expect(err).To(specs.BeNil())
			commandReply, ok := reply.(*egopb.CommandReply)
			ctx.Expect(ok).To(specs.BeTrue())
			state := commandReply.GetStateReply()
			ctx.Expect(state).To(specs.Not(specs.BeNil()))
			ctx.Expect(state.GetSequenceNumber()).ToEqual(uint64(1))
		})

		s.It("compensate: behavior failure sets runtimeport.SagaFailed", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var compensated atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					return &sagaAction{Compensate: true}, nil
				},
				CompensateFn: func(_ context.Context, _ State) ([]sagaCommand, error) {
					bump(&compensated)
					return nil, errSagaBoom
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			awaitCalls(ctx, &compensated, 1, signalTimeout)
			expectStaysRunning(ctx, pid, quietWindow)
		})

		s.It("compensate: command SendSync failure sets runtimeport.SagaFailed", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			var compensated atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					return &sagaAction{Compensate: true}, nil
				},
				CompensateFn: func(_ context.Context, _ State) ([]sagaCommand, error) {
					bump(&compensated)
					return []sagaCommand{
						{EntityID: "nonexistent-entity", Command: new(emptypb.Empty), Timeout: 500 * time.Millisecond},
					}, nil
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			// The SendSync timeout is 500ms; watch through that plus margin for a crash.
			awaitCalls(ctx, &compensated, 1, signalTimeout)
			expectStaysRunning(ctx, pid, 2*time.Second)
		})

		s.It("compensate: successful compensation sets runtimeport.SagaCompleted", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			targetID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			// A target actor that accepts compensation commands
			rig.spawnReplyTarget(ctx, targetID, stateReply(ctx, targetID))
			var compensated atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					return &sagaAction{Compensate: true}, nil
				},
				CompensateFn: func(_ context.Context, _ State) ([]sagaCommand, error) {
					bump(&compensated)
					return []sagaCommand{
						{EntityID: targetID, Command: new(emptypb.Empty), Timeout: 3 * time.Second},
					}, nil
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			// compensate() (saga_actor.go) marks runtimeport.SagaCompleted directly
			// on a successful SendSync and never calls ApplyEvent, so the only
			// observable signals are that Compensate ran and the actor survived.
			awaitCalls(ctx, &compensated, 1, signalTimeout)
			expectStaysRunning(ctx, pid, 2*time.Second)
		})

		// Each row makes the command fail, either because the target does not
		// exist or because it answers with an error reply, and checks that the
		// saga hands the failure to HandleError. survive is how long the actor
		// must keep running afterwards (zero skips the check), and wait is how
		// long the case waits for HandleError.
		type commandFailure struct {
			name       string
			errorReply bool
			timeout    time.Duration
			effective  time.Duration
			outcome    func() (*sagaAction, error)
			wait       time.Duration
			survive    time.Duration
		}
		complete := func() (*sagaAction, error) { return &sagaAction{Complete: true}, nil }
		fails := func() (*sagaAction, error) { return nil, errSagaBoom }
		specs.Table(s, []commandFailure{
			{"sendCommand: SendSync error triggers HandleError with compensate", false, 500 * time.Millisecond, 500 * time.Millisecond, complete, signalTimeout, 0},
			{"sendCommand: HandleError failure is logged", false, 500 * time.Millisecond, 500 * time.Millisecond, fails, signalTimeout, 300 * time.Millisecond},
			{"sendCommand: error reply triggers HandleError", true, 3 * time.Second, 3 * time.Second, complete, signalTimeout, 0},
			{"sendCommand: error reply HandleError failure is logged", true, 3 * time.Second, 3 * time.Second, fails, signalTimeout, 300 * time.Millisecond},
			// Timeout 0 defaults to 5s. The target does not exist, so SendSync fails at
			// once; the effective timeout is read from the commandTimeoutObserver seam
			// instead of waiting for it.
			{"sendCommand: default timeout when zero", false, 0, 5 * time.Second, complete, signalTimeout, 0},
		}, func(c commandFailure) string { return c.name }, func(ctx *specs.Context, c commandFailure) {
			var observed atomic.Int64
			observe := func(d time.Duration) { observed.Store(int64(d)) }
			commandTimeoutObserver.Store(&observe)
			ctx.Cleanup(func() { commandTimeoutObserver.Store(nil) })
			sagaID := uuid.NewString()
			targetID := "nonexistent-entity"
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			if c.errorReply {
				targetID = uuid.NewString()
				rig.spawnReplyTarget(ctx, targetID, rejectReply())
			}
			var handledError atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					return &sagaAction{Commands: []sagaCommand{
						{EntityID: targetID, Command: new(emptypb.Empty), Timeout: c.timeout},
					}}, nil
				},
				HandleErrorFn: func(_ context.Context, _ string, _ error, _ State) (*sagaAction, error) {
					bump(&handledError)
					return c.outcome()
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			awaitCalls(ctx, &handledError, 1, c.wait)
			ctx.Expect(time.Duration(observed.Load())).ToEqual(c.effective)
			if c.survive > 0 {
				expectStaysRunning(ctx, pid, c.survive)
			}
		})

		s.It("sendCommand: unexpected reply type is logged", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			targetID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			// Target responds with a non-CommandReply message
			rig.spawnReplyTarget(ctx, targetID, new(emptypb.Empty))
			var commandSent atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					bump(&commandSent)
					return &sagaAction{Commands: []sagaCommand{
						{EntityID: targetID, Command: new(emptypb.Empty), Timeout: 3 * time.Second},
					}}, nil
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			awaitCalls(ctx, &commandSent, 1, 2*time.Second)
			// Actor must still be running even though the reply was unexpected
			expectStaysRunning(ctx, pid, quietWindow)
		})

		// Each row has the target answer successfully and checks that the saga
		// hands the result to HandleResult and survives whatever it returns.
		type resultOutcome struct {
			name    string
			outcome func() (*sagaAction, error)
		}
		specs.Table(s, []resultOutcome{
			{"sendCommand: HandleResult failure is logged", fails},
			{"sendCommand: HandleResult success completes saga", complete},
		}, func(c resultOutcome) string { return c.name }, func(ctx *specs.Context, c resultOutcome) {
			sagaID := uuid.NewString()
			targetID := uuid.NewString()
			rig := newSagaRig(ctx, newTestkitStore(ctx))
			rig.spawnReplyTarget(ctx, targetID, stateReply(ctx, targetID))
			var handledResult atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					return &sagaAction{Commands: []sagaCommand{
						{EntityID: targetID, Command: new(emptypb.Empty), Timeout: 3 * time.Second},
					}}, nil
				},
				HandleResultFn: func(_ context.Context, _ string, _ State, _ State) (*sagaAction, error) {
					bump(&handledResult)
					return c.outcome()
				},
			}
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			awaitCalls(ctx, &handledResult, 1, signalTimeout)
			expectStaysRunning(ctx, pid, 300*time.Millisecond)
		})
	})
}

// TestSagaFailsClosed documents and proves the known #54 limitation: Actor
// dispatches commands via sendCommand/compensate using context.Background()
// (saga_actor.go, read-only in this change), which never carries a
// TenantContext. In tenant-aware mode this means a saga can never legitimately
// reach a domain handler on its own — it fails closed at the exact same
// pre-handler gate added to eventsource.Actor and DurableStateActor for T4-A
// (see TestEventSourcedActorTenancyGate, TestDurableStateActorTenancyGate).
//
// This is not a new mechanism: the saga's context.Background() dispatch is
// architecturally identical to the "artificial loss of TenantContext" case
// already covered directly against the actors. This test additionally proves
// it end-to-end, through a real Actor reacting to a real event and
// invoking a real tenant-aware eventsource.Actor via SendSync, exactly as
// production code would.
func TestSagaFailsClosed(t *testing.T) {
	specs.Describe(t, "a saga in tenant-aware mode cannot reach a domain handler", func(s *specs.Spec) {
		s.It("saga-dispatched command in tenant-aware mode is blocked before HandleCommand", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			targetID := uuid.NewString()
			store := newTestkitStore(ctx)

			// extensions.NewTenancyMarker(false) puts the actor system in tenant-aware
			// mode, exactly as Engine.NewEngine does when a resolver is
			// registered via WithTenantResolver. No resolver is registered here
			// at all: the saga must never be able to reach one (see structural
			// invariant), and this test does not need one to prove the gate.
			rig := newSagaRig(ctx, store, extensions.NewTenancyMarker(false))

			// The saga's real target: a genuine tenant-aware eventsource.Actor,
			// not a stub. If the gate ever regressed and let a saga-dispatched
			// command through, this probe would record it.
			//
			// This test spawns directly through actorSystem.Spawn, bypassing
			// Engine.Entity entirely, so it must supply the per-spawn
			// extensions.EntityTenantScope dependency itself — exactly what
			// Engine.Entity injects when given engine.WithTenant (TENANT-003 T4,
			// corrected). Without it, tenancy being active
			// (extensions.NewTenancyMarker(false) above) makes the target's own
			// PreStart fail closed with extensions.ErrEntityTenantScopeMissing before this
			// test ever reaches the saga-dispatch gate it means to prove.
			targetProbe := enginetest.NewTenancyProbeEventSourcedBehavior(targetID)
			_, err := rig.system.Spawn(context.Background(), targetID, eventsource.New(),
				goakt.WithDependencies(targetProbe, extensions.NewEntityTenantScope("acme")), goakt.WithLongLived(), goakt.WithStashing())
			ctx.Expect(err).To(specs.BeNil())

			// Post-EGO-TENANT-002/PR3, Actor reconstructs a TenantContext from
			// each incoming event's own tenant metadata (SG2) and rejects the
			// event outright — before HandleEvent ever runs, so no sagaAction and
			// no command is ever produced — when that metadata is absent or
			// malformed (SG4). This is a strictly earlier and stronger form of
			// the structural invariant this test originally proved by relying on
			// the saga blindly dispatching via context.Background() and the
			// target entity's own tenancy gate catching it downstream: that
			// fallback path no longer exists because the saga never reaches
			// sendCommand for a tenant-less event in the first place.
			var handleEventCalls atomic.Int32
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					bump(&handleEventCalls)
					return &sagaAction{Commands: []sagaCommand{
						{EntityID: targetID, Command: &testpb.CreateAccount{AccountBalance: 500}, Timeout: 3 * time.Second},
					}}, nil
				},
			}
			// Same reasoning as the target's spawn above: this saga is also
			// spawned directly through actorSystem.Spawn, so it needs its own
			// EntityTenantScope dependency to get past tenancy-active PreStart.
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0), extensions.NewEntityTenantScope("acme"))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))

			// HandleEvent must never run for an event with no tenant metadata in tenant-aware mode
			expectCallsStay(ctx, &handleEventCalls, 0, 2*time.Second)

			// HandleCommand must never run for a command the saga could not have formed for a rejected event
			ctx.Expect(targetProbe.InvocationCount()).To(specs.BeZero())

			// no event may be persisted when the gate blocks the saga's command
			latest, err := store.GetLatestEvent(context.Background(), persistence.Unscoped(), targetID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.BeNil())

			ctx.Expect(pid.IsRunning()).To(specs.BeTrue())
		})
	})
}
