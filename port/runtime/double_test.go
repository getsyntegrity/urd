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

package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/getsyntegrity/urd/command"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/port/behavior"
	"github.com/getsyntegrity/urd/port/runtime"
)

// The contract is implementable without GoAkt and without package engine:
// TestArchitectureRuntimeTestClosureExcludesGoAktAndRoot checks that this file's package
// depends on neither.
var _ runtime.Runtime = (*double)(nil)

// doubleRuntime is the runtime name the double reports in *UnsupportedError.
const doubleRuntime = "double"

// double is a map-backed runtime with no actor system, no mailbox, no
// persistence and no projections (openspec/changes/ego-runtime-001/design.md
// §D7). It proves the contract's shape, not a runtime's behavior: it hosts
// event-sourced entities and runs their commands synchronously, the way
// testkit/scenario.go does, and answers every other operation with an
// *UnsupportedError, before any side effect.
type double struct {
	entities map[string]*doubleEntity
}

type doubleEntity struct {
	behavior behavior.EventSourced
	state    behavior.State
	revision uint64
	settings runtime.SpawnSettings
}

func newDouble() *double {
	return &double{entities: make(map[string]*doubleEntity)}
}

func unsupported(operation string) error {
	return &runtime.UnsupportedError{Runtime: doubleRuntime, Operation: operation}
}

func (d *double) SpawnEventSourced(_ context.Context, b behavior.EventSourced, opts ...runtime.SpawnOption) error {
	if b == nil {
		return errors.New("double: nil behavior")
	}
	id := b.ID()
	if id == "" {
		return runtime.ErrUndefinedEntityID
	}
	if _, ok := d.entities[id]; ok {
		return nil // re-spawning a live id is an idempotent success
	}
	d.entities[id] = &doubleEntity{
		behavior: b,
		state:    b.InitialState(),
		settings: runtime.ResolveSpawnOptions(opts...),
	}
	return nil
}

func (d *double) SpawnDurableState(context.Context, behavior.DurableState, ...runtime.SpawnOption) error {
	return unsupported("SpawnDurableState")
}

func (d *double) EntityExists(context.Context, string) (bool, error) {
	return false, unsupported("EntityExists")
}

func (d *double) SendCommand(ctx context.Context, entityID string, cmd behavior.Command, _ time.Duration) (behavior.State, uint64, error) {
	if entityID == "" {
		return nil, 0, runtime.ErrUndefinedEntityID
	}
	e, ok := d.entities[entityID]
	if !ok {
		return nil, 0, fmt.Errorf("double: entity %q not found", entityID)
	}
	events, err := e.behavior.HandleCommand(ctx, cmd, e.state)
	if err != nil {
		return nil, 0, err
	}
	if len(events) == 0 {
		return nil, e.revision, nil
	}
	state := e.state
	for _, event := range events {
		if state, err = e.behavior.HandleEvent(ctx, event, state); err != nil {
			return nil, 0, err
		}
	}
	e.state = state
	e.revision += uint64(len(events))
	return e.state, e.revision, nil
}

func (d *double) Dispatch(context.Context, string, command.Envelope, time.Duration) (command.Result, error) {
	return command.Result{}, unsupported("Dispatch")
}

func (d *double) EraseEntity(context.Context, string, bool) error {
	return unsupported("EraseEntity")
}

func (d *double) SpawnSaga(context.Context, behavior.Saga, time.Duration, ...runtime.SpawnOption) error {
	return unsupported("SpawnSaga")
}

func (d *double) SagaStatus(context.Context, string, time.Duration) (*runtime.SagaInfo, error) {
	return nil, unsupported("SagaStatus")
}

func (d *double) StartProjection(context.Context, string) error {
	return unsupported("StartProjection")
}

func (d *double) StopProjection(context.Context, string) error {
	return unsupported("StopProjection")
}

func (d *double) IsProjectionRunning(context.Context, string) (bool, error) {
	return false, unsupported("IsProjectionRunning")
}

func (d *double) RebuildProjection(context.Context, string, time.Time) error {
	return unsupported("RebuildProjection")
}

func (d *double) ProjectionLag(context.Context, string) (map[uint64]time.Duration, error) {
	return nil, unsupported("ProjectionLag")
}

func (d *double) Subscribe() (eventstream.Subscriber, error) {
	return nil, unsupported("Subscribe")
}

// counter is an event-sourced behavior over wrapperspb values: a command is
// an Int64Value to add, its event is that same value, and the state is the
// running total. A zero command produces no event.
type counter struct{ id string }

var _ behavior.EventSourced = (*counter)(nil)

func (c *counter) ID() string                   { return c.id }
func (c *counter) InitialState() behavior.State { return wrapperspb.Int64(0) }

func (c *counter) HandleCommand(_ context.Context, cmd behavior.Command, _ behavior.State) ([]behavior.Event, error) {
	add, ok := cmd.(*wrapperspb.Int64Value)
	if !ok {
		return nil, fmt.Errorf("counter: unexpected command %T", cmd)
	}
	if add.GetValue() == 0 {
		return nil, nil
	}
	return []behavior.Event{wrapperspb.Int64(add.GetValue())}, nil
}

func (c *counter) HandleEvent(_ context.Context, event behavior.Event, prior behavior.State) (behavior.State, error) {
	total, ok := prior.(*wrapperspb.Int64Value)
	if !ok {
		return nil, fmt.Errorf("counter: unexpected state %T", prior)
	}
	added, ok := event.(*wrapperspb.Int64Value)
	if !ok {
		return nil, fmt.Errorf("counter: unexpected event %T", event)
	}
	return wrapperspb.Int64(total.GetValue() + added.GetValue()), nil
}

// ledger is a durable-state behavior, spawned only to show that an
// unsupported spawn hosts nothing.
type ledger struct{}

var _ behavior.DurableState = ledger{}

func (ledger) ID() string                   { return "ledger-1" }
func (ledger) InitialState() behavior.State { return wrapperspb.Int64(0) }
func (ledger) HandleCommand(context.Context, behavior.Command, uint64, behavior.State) (behavior.State, uint64, error) {
	return nil, 0, errors.New("ledger: never called")
}

// idleSaga is a saga, spawned only to show that an unsupported spawn hosts
// nothing.
type idleSaga struct{}

var _ behavior.Saga = idleSaga{}

func (idleSaga) ID() string                   { return "saga-1" }
func (idleSaga) InitialState() behavior.State { return wrapperspb.Int64(0) }
func (idleSaga) HandleEvent(context.Context, behavior.Event, behavior.State) (*behavior.SagaAction, error) {
	return nil, nil
}
func (idleSaga) HandleResult(context.Context, string, behavior.State, behavior.State) (*behavior.SagaAction, error) {
	return nil, nil
}
func (idleSaga) HandleError(context.Context, string, error, behavior.State) (*behavior.SagaAction, error) {
	return nil, nil
}
func (idleSaga) ApplyEvent(context.Context, behavior.Event, behavior.State) (behavior.State, error) {
	return nil, nil
}
func (idleSaga) Compensate(context.Context, behavior.State) ([]behavior.SagaCommand, error) {
	return nil, nil
}

type doubleKeyA struct{}

type doubleKeyB struct{}

// spawn goes through the capability a consumer would hold, runtime.Entities,
// and reports a failed spawn through the spec.
func spawn(ctx *specs.Context, entities runtime.Entities, id string, opts ...runtime.SpawnOption) {
	ctx.Expect(entities.SpawnEventSourced(context.Background(), &counter{id: id}, opts...)).To(specs.BeNil())
}

// protoEqual matches a proto.Message that proto.Equal reports equal to want.
func protoEqual(want proto.Message) specs.Matcher {
	return specs.Satisfy(fmt.Sprintf("be proto.Equal to %v", want), func(got any) bool {
		m, ok := got.(proto.Message)
		return ok && proto.Equal(want, m)
	})
}

func TestDoubleSpawnResolvesDocumentedDefaults(t *testing.T) {
	specs.Describe(t, "The double resolves the documented spawn defaults", func(s *specs.Spec) {
		s.It("hosts an entity spawned without options with the documented default settings", func(ctx *specs.Context) {
			d := newDouble()
			spawn(ctx, d, "account-1")

			settings := d.entities["account-1"].settings
			ctx.Expect(settings.PassivateAfter()).ToEqual(time.Duration(0))
			ctx.Expect(settings.Relocation()).To(specs.BeFalse())
			ctx.Expect(settings.SupervisorDirective()).ToEqual(runtime.RestartDirective)
			ctx.Expect(settings.Placement()).ToEqual(runtime.RoundRobin)
			ctx.Expect(settings.Tenant()).To(specs.BeZero())
		})
	})
}

func TestDoubleSpawnAppliesOptionsInOrderAndSkipsNil(t *testing.T) {
	specs.Describe(t, "The double applies spawn options in order and skips nil ones", func(s *specs.Spec) {
		s.It("keeps the last placement, the supervisor directive and the adapter setting under its own key", func(ctx *specs.Context) {
			d := newDouble()
			spawn(ctx, d, "account-1",
				runtime.WithPlacement(runtime.Random),
				nil,
				runtime.WithPlacement(runtime.Local),
				runtime.WithSupervisorDirective(runtime.StopDirective),
				runtime.WithAdapterSetting(doubleKeyA{}, 42),
			)

			settings := d.entities["account-1"].settings
			// A later option overrides an earlier one.
			ctx.Expect(settings.Placement()).ToEqual(runtime.Local)
			ctx.Expect(settings.SupervisorDirective()).ToEqual(runtime.StopDirective)

			// An adapter setting is visible under its own key.
			value, ok := settings.AdapterSetting(doubleKeyA{})
			ctx.Expect(ok).To(specs.BeTrue())
			ctx.Expect(value).ToEqual(42)
			// An adapter setting is invisible under another key.
			_, ok = settings.AdapterSetting(doubleKeyB{})
			ctx.Expect(ok).To(specs.BeFalse())
		})
	})
}

func TestDoubleSendCommandRunsTheBehavior(t *testing.T) {
	specs.Describe(t, "The double runs a command through the hosted behavior", func(s *specs.Spec) {
		s.It("applies events, keeps the state on a no-event command and rejects a missing entity", func(ctx *specs.Context) {
			d := newDouble()
			spawn(ctx, d, "account-1")
			var entities runtime.Entities = d
			bg := context.Background()

			state, revision, err := entities.SendCommand(bg, "account-1", wrapperspb.Int64(5), time.Second)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(state).To(protoEqual(wrapperspb.Int64(5)))
			ctx.Expect(revision).ToEqual(uint64(1))

			state, revision, err = entities.SendCommand(bg, "account-1", wrapperspb.Int64(7), time.Second)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(state).To(protoEqual(wrapperspb.Int64(12)))
			ctx.Expect(revision).ToEqual(uint64(2))

			// No event, no state update.
			state, revision, err = entities.SendCommand(bg, "account-1", wrapperspb.Int64(0), time.Second)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(state).To(specs.BeNil())
			ctx.Expect(revision).ToEqual(uint64(2))

			_, _, err = entities.SendCommand(bg, "", wrapperspb.Int64(1), time.Second)
			ctx.Expect(err).To(specs.MatchError(runtime.ErrUndefinedEntityID))

			_, _, err = entities.SendCommand(bg, "unknown", wrapperspb.Int64(1), time.Second)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			// A missing entity is not an unsupported operation.
			ctx.Expect(err).To(specs.Not(specs.MatchError(runtime.ErrUnsupported)))
		})
	})
}

// TestDoubleUnsupportedOperations calls every operation the double lacks
// through the capability interface a consumer would hold, and checks the
// contract of design §D4: the error matches ErrUnsupported and
// errors.ErrUnsupported, names the runtime and the operation, and is returned
// before any side effect (the hosted entities are unchanged).
func TestDoubleUnsupportedOperations(t *testing.T) {
	specs.Describe(t, "The double rejects every operation it lacks with an UnsupportedError before any side effect", func(s *specs.Spec) {
		var (
			d           *double
			entities    runtime.Entities
			sagas       runtime.Sagas
			projections runtime.Projections
			events      runtime.Events
			env         command.Envelope
		)
		bg := context.Background()

		s.BeforeEach(func(ctx *specs.Context) {
			d = newDouble()
			spawn(ctx, d, "account-1")
			_, _, err := d.SendCommand(bg, "account-1", wrapperspb.Int64(3), time.Second)
			ctx.Expect(err).To(specs.BeNil())

			entities, sagas, projections, events = d, d, d, d
			env, err = command.NewEnvelope(wrapperspb.Int64(1), command.Metadata{})
			ctx.Expect(err).To(specs.BeNil())
		})

		type operation struct {
			name string
			call func(c *specs.Context) error
		}
		specs.Table(s, []operation{
			{"SpawnDurableState", func(*specs.Context) error { return entities.SpawnDurableState(bg, ledger{}) }},
			{"EntityExists", func(c *specs.Context) error {
				exists, err := entities.EntityExists(bg, "account-1")
				c.Expect(exists).To(specs.BeFalse())
				return err
			}},
			{"Dispatch", func(*specs.Context) error {
				_, err := entities.Dispatch(bg, "account-1", env, time.Second)
				return err
			}},
			{"EraseEntity", func(*specs.Context) error { return entities.EraseEntity(bg, "account-1", true) }},
			{"SpawnSaga", func(*specs.Context) error { return sagas.SpawnSaga(bg, idleSaga{}, time.Second) }},
			{"SagaStatus", func(c *specs.Context) error {
				info, err := sagas.SagaStatus(bg, "saga-1", time.Second)
				c.Expect(info).To(specs.BeNil())
				return err
			}},
			{"StartProjection", func(*specs.Context) error { return projections.StartProjection(bg, "balances") }},
			{"StopProjection", func(*specs.Context) error { return projections.StopProjection(bg, "balances") }},
			{"IsProjectionRunning", func(c *specs.Context) error {
				running, err := projections.IsProjectionRunning(bg, "balances")
				c.Expect(running).To(specs.BeFalse())
				return err
			}},
			{"RebuildProjection", func(*specs.Context) error {
				return projections.RebuildProjection(bg, "balances", time.Time{})
			}},
			{"ProjectionLag", func(c *specs.Context) error {
				lag, err := projections.ProjectionLag(bg, "balances")
				c.Expect(lag).To(specs.BeNil())
				return err
			}},
			{"Subscribe", func(c *specs.Context) error {
				subscriber, err := events.Subscribe()
				c.Expect(subscriber).To(specs.BeNil())
				return err
			}},
		}, func(op operation) string { return op.name }, func(ctx *specs.Context, op operation) {
			before := maps.Clone(d.entities)
			hosted := *d.entities["account-1"]

			err := op.call(ctx)

			ctx.Expect(err).To(specs.MatchError(runtime.ErrUnsupported))
			ctx.Expect(err).To(specs.MatchError(errors.ErrUnsupported))
			var unsupportedErr *runtime.UnsupportedError
			ctx.Expect(err).To(specs.MatchErrorAs(&unsupportedErr))
			ctx.Expect(unsupportedErr.Runtime).ToEqual(doubleRuntime)
			ctx.Expect(unsupportedErr.Operation).ToEqual(op.name)

			// No entity was added or removed.
			ctx.Expect(d.entities).ToEqual(before)
			after := d.entities["account-1"]
			// The hosted entity is unchanged.
			ctx.Expect(after.revision).ToEqual(hosted.revision)
			ctx.Expect(after.state).To(protoEqual(hosted.state))
		})
	})
}
