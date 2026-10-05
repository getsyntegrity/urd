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

package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/supervisor"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/extensions"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/offsetstore"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/projection"
)

// startProjectionSystemG4 starts a real actor system wired with the given
// stores and one projection named name, and stops it when the case ends.
func startProjectionSystemG4(ctx *specs.Context, name string, eventsStore persistence.EventsStore,
	offsetStore offsetstore.OffsetStore, handler projection.Handler) goakt.ActorSystem {
	bg := context.Background()
	actorSystem, err := goakt.NewActorSystem("TestActorSystem",
		goakt.WithLogger(newLoggerAdapter(DiscardLogger)),
		goakt.WithExtensions(
			extensions.NewEventsStore(eventsStore),
			extensions.NewOffsetStore(offsetStore),
			extensions.NewProjectionExtension(map[string]*projection.Options{
				name: {Handler: handler, BufferSize: 500, PullInterval: 100 * time.Millisecond, Recovery: projection.NewRecovery(), Scope: unscopedPtr()},
			})),
		goakt.WithActorInitMaxRetries(3))
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(actorSystem.Start(bg)).To(specs.BeNil())
	ctx.Cleanup(func() { _ = actorSystem.Stop(context.Background()) })
	return actorSystem
}

// accountCreditedEventsG4 builds count journal events of one persistence ID on
// the given shard.
func accountCreditedEventsG4(ctx *specs.Context, persistenceID string, shard uint64, count int) []*egopb.Event {
	event, err := anypb.New(&testpb.AccountCredited{})
	ctx.Expect(err).To(specs.BeNil())

	timestamp := timestamppb.Now()
	journals := make([]*egopb.Event, count)
	for i := range count {
		journals[i] = &egopb.Event{
			PersistenceId:  persistenceID,
			SequenceNumber: uint64(i + 1),
			IsDeleted:      false,
			Event:          event,
			Timestamp:      timestamp.AsTime().Unix(),
			Shard:          shard,
		}
	}
	return journals
}

// spawnProjectionG4 spawns the projection the way StartProjection does in
// standalone mode.
func spawnProjectionG4(ctx *specs.Context, actorSystem goakt.ActorSystem, name string) *goakt.PID {
	pid, err := actorSystem.Spawn(context.Background(), name, NewProjectionActor(),
		goakt.WithLongLived(),
		goakt.WithSupervisor(newProjectionSupervisor()))
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(pid != nil).To(specs.BeTrue())
	return pid
}

func TestProjectionActorRunnerFailure(t *testing.T) {
	specs.Describe(t, "the projection actor reacts to a failing runner", func(s *specs.Spec) {
		bg := context.Background()
		const projectionName = "db-writer"
		const shardNumber = uint64(9)

		s.It("recovers from transient store failure without restarting", func(ctx *specs.Context) {
			journalStore := connectedEventsStore(ctx)
			offsetStore := connectedOffsetStore(ctx)

			// fail the first ShardOffsets round trip, then recover
			eventsStore := &flakyEventsStore{EventsStore: journalStore, failures: atomic.NewInt32(1)}
			actorSystem := startProjectionSystemG4(ctx, projectionName, eventsStore, offsetStore, projection.NewDiscardHandler())

			// persist events before the projection pulls for the first time
			const count = 10
			journals := accountCreditedEventsG4(ctx, uuid.NewString(), shardNumber, count)
			ctx.Expect(journalStore.WriteEvents(bg, persistence.Unscoped(), journals, persistence.Unconditional())).To(specs.BeNil())

			pid := spawnProjectionG4(ctx, actorSystem, projectionName)

			// the first pull fails; the runner retries in place with backoff and
			// replays the stalled backlog once the store recovers, with no actor
			// restart involved
			projectionID := &egopb.ProjectionId{ProjectionName: projectionName, ShardNumber: shardNumber}
			ctx.Eventually(func() any {
				actual, err := offsetStore.GetCurrentOffset(bg, projectionID)
				if err != nil {
					return int64(-1)
				}
				return actual.GetValue()
			}, specs.Equal(journals[count-1].GetTimestamp()), specs.WithTimeout(waitTimeout), specs.WithInterval(50*time.Millisecond))

			ctx.Expect(pid.IsRunning()).To(specs.BeTrue())
			ctx.Expect(pid.RestartCount()).To(specs.BeZero())
		})

		s.It("stops on unprocessable event", func(ctx *specs.Context) {
			journalStore := connectedEventsStore(ctx)
			offsetStore := connectedOffsetStore(ctx)

			// failingProjectionHandler always fails and the default recovery policy is Fail
			actorSystem := startProjectionSystemG4(ctx, projectionName, journalStore, offsetStore, failingProjectionHandler{})

			journals := accountCreditedEventsG4(ctx, uuid.NewString(), shardNumber, 1)
			ctx.Expect(journalStore.WriteEvents(bg, persistence.Unscoped(), journals, persistence.Unconditional())).To(specs.BeNil())

			pid := spawnProjectionG4(ctx, actorSystem, projectionName)

			// the unprocessable event escalates to the actor and supervision
			// stops it, making the failure visible instead of leaving a
			// healthy-looking actor with a dead runner
			ctx.Eventually(func() any { return pid.IsRunning() }, specs.BeFalse(),
				specs.WithTimeout(waitTimeout), specs.WithInterval(50*time.Millisecond))
		})
	})
}

func TestProjectionSupervisorContract(t *testing.T) {
	specs.Describe(t, "the projection supervisor stops the actor when the runner fails", func(s *specs.Spec) {
		s.It("keys the stop directive by the engine error type name", func(ctx *specs.Context) {
			// goakt ships directive rules to peer nodes by type name with
			// singleton spawns: the name must not change across versions.
			var rule *supervisor.DirectiveRule
			for _, candidate := range newProjectionSupervisor().Rules() {
				if candidate.ErrorType == "engine.projectionRunnerError" {
					rule = &candidate
				}
			}
			ctx.Expect(rule != nil).To(specs.BeTrue())
			ctx.Expect(rule.Directive).ToEqual(supervisor.StopDirective)
		})
		s.It("stops on the error the runner failure is escalated with", func(ctx *specs.Context) {
			cause := errors.New("damn")
			err := &projectionRunnerError{err: cause}

			directive, ok := newProjectionSupervisor().Directive(err)
			ctx.Expect(ok).To(specs.BeTrue())
			ctx.Expect(directive).ToEqual(supervisor.StopDirective)
			ctx.Expect(engRestErrText(err)).ToEqual("damn")
			ctx.Expect(err).To(specs.MatchError(cause))
		})
	})
}

// failingProjectionHandler always fails to handle an event.
type failingProjectionHandler struct{}

var _ projection.Handler = failingProjectionHandler{}

func (failingProjectionHandler) Handle(context.Context, string, *anypb.Any, uint64) error {
	return errors.New("damn")
}

// flakyEventsStore delegates to the wrapped events store but fails ShardOffsets
// a configured number of times to simulate a transient store outage.
type flakyEventsStore struct {
	persistence.EventsStore
	failures *atomic.Int32
}

func (x *flakyEventsStore) ShardOffsets(ctx context.Context, scope persistence.Scope) (map[uint64]int64, error) {
	if x.failures.Sub(1) >= 0 {
		return nil, errors.New("shard offsets round trip failed")
	}

	return x.EventsStore.ShardOffsets(ctx, scope)
}
