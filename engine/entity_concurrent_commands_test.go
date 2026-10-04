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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"

	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
)

// A command that reaches an entity while another command's write is being
// confirmed must still run, exactly once. The entity answers the command that
// is waiting for its write from the stash; GoAkt re-delivers a stashed message
// at the tail of the mailbox, so a command that was already queued behind the
// write's confirmation is dequeued first, while the entity is replying. It used
// to be answered with the current state and dropped, and the stashed command
// then ran a second time.
//
// The window opens only for a command that the write itself triggers: the
// events writer publishes the event before it confirms the write, so a saga
// that reacts to the event can send its command while the confirmation is
// still queued. Clients that wait for their own reply never open it. So the
// test has a saga credit every account it sees created, and checks, for every
// account, that the credit ran once: a swallowed credit leaves the balance at
// its first value, and a create that ran twice leaves an extra event.
func TestEntityRunsACommandItsOwnEventTriggers(t *testing.T) {
	specs.Describe(t, "a saga that credits every account it sees created", func(s *specs.Spec) {
		const accounts = 600

		s.It("credits each account once: no credit is lost and no create runs twice", func(ctx *specs.Context) {
			bg := context.Background()
			store := connectedEventsStore(ctx)
			engine := startEngine(ctx, "EventTriggeredCommand", store)

			credited := func() *enginetest.CallbackSagaBehavior {
				return &enginetest.CallbackSagaBehavior{
					SagaID: "credit-" + uuid.NewString(),
					HandleEventFn: func(_ context.Context, event Event, _ State) (*SagaAction, error) {
						created, ok := event.(*testpb.AccountCreated)
						if !ok {
							return &SagaAction{}, nil
						}
						return &SagaAction{Commands: []SagaCommand{{
							EntityID: created.GetAccountId(),
							Command:  &testpb.CreditAccount{AccountId: created.GetAccountId(), Balance: 5},
							Timeout:  time.Minute,
						}}}, nil
					},
					HandleResultFn: func(context.Context, string, Event, State) (*SagaAction, error) {
						return &SagaAction{}, nil
					},
				}
			}
			ctx.Expect(engine.Saga(bg, credited(), 0)).To(specs.BeNil())

			ids := make([]string, accounts)
			var wg sync.WaitGroup
			errs := make([]error, accounts)
			for i := range ids {
				ids[i] = uuid.NewString()
				ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(ids[i]))).To(specs.BeNil())
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _, errs[i] = engine.SendCommand(bg, ids[i], &testpb.CreateAccount{AccountBalance: 10}, time.Minute)
				}()
			}
			wg.Wait()
			for _, sendErr := range errs {
				ctx.Expect(sendErr).To(specs.BeNil())
			}

			// every account ends at 15 with exactly its create and its credit
			for _, id := range ids {
				ctx.Eventually(func() any {
					latest, err := store.GetLatestEvent(bg, persistence.Unscoped(), id)
					if err != nil || latest == nil {
						return uint64(0)
					}
					return latest.GetSequenceNumber()
				}, specs.Equal(uint64(2)), specs.WithTimeout(30*time.Second), specs.WithInterval(5*time.Millisecond))

				state, _, err := engine.SendCommand(bg, id, &testpb.CreditAccount{AccountId: id, Balance: 0}, time.Minute)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(15.0))
			}
		})
	})
}

// Two callers may send the very same message object. The entity tells requests
// apart by the call, not by the message, so each of them runs once and is
// answered once, even when one is answered from the stash while the other is
// queued behind it.
//
// Two sagas react to every account created and credit it with one shared
// message object each: the same pointer reaches the entity from two callers, at
// the moment the first caller's write is being confirmed.
func TestEntityRunsTwoCallersThatShareAMessageObjectOnce(t *testing.T) {
	specs.Describe(t, "two sagas that credit an account with the same message object", func(s *specs.Spec) {
		const accounts = 300

		s.It("runs each credit once and answers each caller once", func(ctx *specs.Context) {
			bg := context.Background()
			store := connectedEventsStore(ctx)
			engine := startEngine(ctx, "SharedMessage", store)

			var mu sync.Mutex
			shared := map[string]*testpb.CreditAccount{} // one message object per account, used by both sagas
			messageFor := func(id string) *testpb.CreditAccount {
				mu.Lock()
				defer mu.Unlock()
				if shared[id] == nil {
					shared[id] = &testpb.CreditAccount{AccountId: id, Balance: 5}
				}
				return shared[id]
			}

			var replies sync.Map // account id -> number of replies the two sagas received
			saga := func(name string) *enginetest.CallbackSagaBehavior {
				return &enginetest.CallbackSagaBehavior{
					SagaID: name + "-" + uuid.NewString(),
					HandleEventFn: func(_ context.Context, event Event, _ State) (*SagaAction, error) {
						created, ok := event.(*testpb.AccountCreated)
						if !ok {
							return &SagaAction{}, nil
						}
						return &SagaAction{Commands: []SagaCommand{{
							EntityID: created.GetAccountId(),
							Command:  messageFor(created.GetAccountId()),
							Timeout:  time.Minute,
						}}}, nil
					},
					HandleResultFn: func(_ context.Context, id string, _ Event, _ State) (*SagaAction, error) {
						count, _ := replies.LoadOrStore(id, new(atomic.Int32))
						count.(*atomic.Int32).Add(1)
						return &SagaAction{}, nil
					},
				}
			}
			ctx.Expect(engine.Saga(bg, saga("first"), 0)).To(specs.BeNil())
			ctx.Expect(engine.Saga(bg, saga("second"), 0)).To(specs.BeNil())

			ids := make([]string, accounts)
			var wg sync.WaitGroup
			errs := make([]error, accounts)
			for i := range ids {
				ids[i] = uuid.NewString()
				ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(ids[i]))).To(specs.BeNil())
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _, errs[i] = engine.SendCommand(bg, ids[i], &testpb.CreateAccount{AccountBalance: 10}, time.Minute)
				}()
			}
			wg.Wait()
			for _, sendErr := range errs {
				ctx.Expect(sendErr).To(specs.BeNil())
			}

			for _, id := range ids {
				// the create and one credit per saga: three events, no more, no fewer
				ctx.Eventually(func() any {
					latest, err := store.GetLatestEvent(bg, persistence.Unscoped(), id)
					if err != nil || latest == nil {
						return uint64(0)
					}
					return latest.GetSequenceNumber()
				}, specs.Equal(uint64(3)), specs.WithTimeout(30*time.Second), specs.WithInterval(5*time.Millisecond))

				// each saga received exactly one answer for its credit
				ctx.Eventually(func() any {
					count, ok := replies.Load(id)
					if !ok {
						return int32(0)
					}
					return count.(*atomic.Int32).Load()
				}, specs.Equal(int32(2)), specs.WithTimeout(30*time.Second), specs.WithInterval(5*time.Millisecond))

				state, _, err := engine.SendCommand(bg, id, &testpb.CreditAccount{AccountId: id, Balance: 0}, time.Minute)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(20.0))
			}
		})
	})
}
