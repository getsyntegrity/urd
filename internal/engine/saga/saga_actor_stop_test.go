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
	"sync/atomic"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"

	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
)

// GoAkt builds a relocated actor from its registered type, so the saga actor
// that stops on the node that received it is the zero value, not the result of
// New. PostStop used to close a stop channel only New created, and the node
// panicked when it stopped a relocated saga. A restart reuses one value for
// every incarnation, so each one needs its own stop signal.
func TestSagaActorStopLifecycle(t *testing.T) {
	specs.Describe(t, "the stop signal of the event consumption loop", func(s *specs.Spec) {
		s.It("PostStop of a zero-value actor, as GoAkt builds it on relocation, does not panic", func(ctx *specs.Context) {
			ctx.Expect((&Actor{}).PostStop(nil)).To(specs.BeNil())
		})

		s.It("PostStop of an actor from New that never started does not panic", func(ctx *specs.Context) {
			ctx.Expect(New().PostStop(nil)).To(specs.BeNil())
		})

		s.It("PostStop twice, as after a start that failed past PreStart, does not panic", func(ctx *specs.Context) {
			actor := &Actor{stop: newStopSignal()}
			ctx.Expect(actor.PostStop(nil)).To(specs.BeNil())
			ctx.Expect(actor.PostStop(nil)).To(specs.BeNil())
		})

		s.It("PostStop ends the consumption loop, so no goroutine outlives the actor", func(ctx *specs.Context) {
			stream := eventstream.New()
			ctx.Cleanup(stream.Close)
			actor := &Actor{stop: newStopSignal(), subscriber: stream.AddSubscriber()}

			done := make(chan struct{})
			go func() {
				actor.consumeEvents(actor.stop.ch, actor.subscriber)
				close(done)
			}()
			ctx.Expect(actor.PostStop(nil)).To(specs.BeNil())

			ctx.Eventually(func() any {
				select {
				case <-done:
					return true
				default:
					return false
				}
			}, specs.BeTrue(), specs.WithTimeout(signalTimeout), specs.WithInterval(pollEvery))
		})

		s.It("a restart gives the new incarnation its own stop signal, and its loop keeps consuming", func(ctx *specs.Context) {
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
			pid := rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0))

			rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))
			awaitCalls(ctx, &handled, 1, signalTimeout)

			// Restart stops the first incarnation (PostStop closes its signal) and
			// starts another on the same Actor value (PreStart makes a new one).
			// Reusing the closed signal would end the new loop at once, and a
			// second close would panic.
			ctx.Expect(pid.Restart(context.Background())).To(specs.BeNil())
			ctx.Expect(pid.IsRunning()).To(specs.BeTrue())

			// the new incarnation consumes events: it handles one published now
			ctx.Eventually(func() any {
				rig.stream.Publish(protocol.EventsTopic, foreignEvent(ctx))
				return handled.Load()
			}, specs.BeGreaterThanOrEqual(int32(2)), specs.WithTimeout(signalTimeout), specs.WithInterval(pollEvery))
		})
	})
}
