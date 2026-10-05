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

//go:build stress

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

// Stress scenario of #306: 400 accounts on batched entities that flush on every
// event (T=1, W=1 ms), three sagas crediting every account they see created.
// Every journal must be the create followed by three credits, and every saga
// must be answered once per account.
//
// It is NOT part of the regular suite (build tag stress) and is not a
// regression gate. The deterministic regression of the defect is
// TestBatchedReplyGoesToTheRequestItWasComputedFor in internal/engine/eventsource.
// This scenario also meets a different, still open defect: now and then a
// batched entity never receives the confirmation of its write, and its command
// times out (#308). Run it on demand:
//
//	go test -tags stress -run TestStressBatchedEntityRunsEachRequestOnce -count=N ./engine
func TestStressBatchedEntityRunsEachRequestOnce(t *testing.T) {
	specs.Describe(t, "sagas that credit every account created on a batched entity", func(s *specs.Spec) {
		const (
			accounts = 400
			sagas    = 3
		)

		s.It("runs each request once and answers each caller once", func(ctx *specs.Context) {
			bg := context.Background()
			store := connectedEventsStore(ctx)
			engine := startEngine(ctx, "BatchedEventTriggeredCommand", store)

			var replies sync.Map // account id -> number of replies the sagas received
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
							Command:  &testpb.CreditAccount{AccountId: created.GetAccountId(), Balance: 5},
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
			for i := range sagas {
				ctx.Expect(engine.Saga(bg, saga("batched-"+string(rune('a'+i))), 0)).To(specs.BeNil())
			}

			ids := make([]string, accounts)
			errs := make([]error, accounts)
			var wg sync.WaitGroup
			for i := range ids {
				ids[i] = uuid.NewString()
				ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(ids[i]),
					WithBatchThreshold(1), WithBatchFlushWindow(time.Millisecond))).To(specs.BeNil())
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
				// the create and one credit per saga: no more, no fewer
				ctx.Eventually(func() any {
					latest, err := store.GetLatestEvent(bg, persistence.Unscoped(), id)
					if err != nil || latest == nil {
						return uint64(0)
					}
					return latest.GetSequenceNumber()
				}, specs.Equal(uint64(1+sagas)), specs.WithTimeout(30*time.Second), specs.WithInterval(5*time.Millisecond))

				ctx.Eventually(func() any {
					count, ok := replies.Load(id)
					if !ok {
						return int32(0)
					}
					return count.(*atomic.Int32).Load()
				}, specs.Equal(int32(sagas)), specs.WithTimeout(30*time.Second), specs.WithInterval(5*time.Millisecond))

				// the sequence number settles at 1+sagas: nothing is written later
				state, _, err := engine.SendCommand(bg, id, &testpb.CreditAccount{AccountId: id, Balance: 0}, time.Minute)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(10.0 + 5.0*sagas))
			}
		})
	})
}
