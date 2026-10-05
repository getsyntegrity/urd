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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	specmock "github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
)

// queuedBehindConfirmation wraps an Actor and, on the first write confirmation
// it receives once armed, puts one command in its own mailbox before it hands the
// confirmation to the Actor. The Actor then releases its stash, which GoAkt
// re-delivers at the tail of the mailbox: the command is ahead of the stashed
// requests, which is the order a command that arrived while the confirmation
// was queued meets them in. Tell enqueues before it returns, so the order does
// not depend on timing.
type queuedBehindConfirmation struct {
	*Actor
	command any
	armed   atomic.Bool
	once    sync.Once
}

func (a *queuedBehindConfirmation) Receive(ctx *goakt.ReceiveContext) {
	if _, ok := ctx.Message().(*persistEventsResponse); ok && a.armed.Load() {
		a.once.Do(func() { _ = goakt.Tell(context.Background(), ctx.Self(), a.command) })
	}
	a.Actor.Receive(ctx)
}

// spawnQueuedBehindConfirmation spawns a batched entity whose first write
// confirmation after the create finds command already queued behind it.
func (r *actorRig) spawnQueuedBehindConfirmation(ctx *specs.Context, behavior eventSourcedBehavior, command any) (*goakt.PID, *queuedBehindConfirmation) {
	entity := &queuedBehindConfirmation{Actor: New(), command: command}
	pid, err := r.system.Spawn(context.Background(), behavior.ID(), entity,
		goakt.WithDependencies(behavior, &extensions.EntityConfig{BatchThreshold: 2, BatchFlushWindow: time.Hour}),
		goakt.WithLongLived(), goakt.WithStashing())
	ctx.Expect(err).To(specs.BeNil())
	waitRunning(ctx, pid)
	return pid, entity
}

// openBatchedAccount creates the account and credits it in one batch of two
// events, which reaches the threshold and is written at once. The create must
// come first: an account credited before it exists loses the credit, so the
// credit is sent only once the create is stashed in the open batch.
func openBatchedAccount(ctx *specs.Context, pid *goakt.PID, persistenceID string) {
	create := askInBackground(callWith("open-create"), pid, &testpb.CreateAccount{AccountBalance: 500})
	ctx.Eventually(stashSize(pid), specs.Equal(uint64(1)), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
	credit := askInBackground(callWith("open-credit"), pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 10})
	stateReplyOf(ctx, create.await(ctx))
	credit.await(ctx)
}

// callWith derives the context one caller gives one call.
func callWith(id string) context.Context { return callCtx(id) }

// A batched entity answers each request it set aside with the reply computed
// for it, and only for it. Requests A and B are pending in a batch; when the
// write is confirmed a command C is already queued behind the confirmation, so
// it reaches the entity before the re-delivered A and B. C must not be given
// A's or B's reply: each of A and B must receive its own, C must run after them,
// and nothing may run twice or be lost. A and B send the very same message
// object, so only the call tells them apart.
func TestBatchedReplyGoesToTheRequestItWasComputedFor(t *testing.T) {
	specs.Describe(t, "a batch whose confirmation has a command queued behind it", func(s *specs.Spec) {
		s.It("answers A and B with their own reply and runs C afterwards, once", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)
			gate := newWriteGate()

			ctrl := specmock.NewController(ctx)
			expectStoreStartup(ctrl, persistenceID)
			ctrl.Method("WriteEvents").Expect(specmock.Any(), specmock.Any(), specmock.Any(), specmock.Any()).Times(1) // create
			ctrl.Method("WriteEvents").Expect(specmock.Any(), specmock.Any(), specmock.Any(), specmock.Any()).
				Do(gate.hold(nil)).Times(1) // A and B
			ctrl.Method("WriteEvents").Expect(specmock.Any(), specmock.Any(), specmock.Any(), specmock.Any()).Times(1) // C

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)))
			gate.openOnCleanup(ctx)
			pid, entity := rig.spawnQueuedBehindConfirmation(ctx, behavior, &testpb.CreditAccount{AccountId: persistenceID, Balance: 50})

			openBatchedAccount(ctx, pid, persistenceID)
			entity.armed.Store(true)

			shared := &testpb.CreditAccount{AccountId: persistenceID, Balance: 100}
			a := askInBackground(callWith("a"), pid, shared)
			ctx.Eventually(stashSize(pid), specs.Equal(uint64(1)), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			b := askInBackground(callWith("b"), pid, shared)
			gate.awaitStarted(ctx, "the write of A and B")
			ctx.Eventually(stashSize(pid), specs.Equal(uint64(2)), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			gate.open()

			expectAccountState(ctx, stateReplyOf(ctx, a.await(ctx)), 3, &testpb.Account{AccountId: persistenceID, AccountBalance: 610})
			expectAccountState(ctx, stateReplyOf(ctx, b.await(ctx)), 4, &testpb.Account{AccountId: persistenceID, AccountBalance: 710})

			// C ran once, after A and B, and waits in a new batch for one more event
			// to reach the threshold: E completes it, and its reply carries C's
			// effect, so C was neither lost nor run twice.
			expectAccountState(ctx, stateReplyOf(ctx, ask(ctx, pid, &testpb.CreditAccount{AccountId: persistenceID, Balance: 5})),
				6, &testpb.Account{AccountId: persistenceID, AccountBalance: 765})
			ctx.Eventually(callCount(ctrl.Method("WriteEvents")), specs.Equal(3), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			state := stateReplyOf(ctx, ask(ctx, pid, &egopb.GetStateCommand{}))
			expectAccountState(ctx, state, 6, &testpb.Account{AccountId: persistenceID, AccountBalance: 765})
		})

		s.It("answers A and B with the error of their write, each once, when the write fails", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)
			gate := newWriteGate()

			ctrl := specmock.NewController(ctx)
			expectStoreStartup(ctrl, persistenceID)
			ctrl.Method("WriteEvents").Expect(specmock.Any(), specmock.Any(), specmock.Any(), specmock.Any()).Times(1) // create
			ctrl.Method("WriteEvents").Expect(specmock.Any(), specmock.Any(), specmock.Any(), specmock.Any()).
				Do(gate.hold(errStoreFailure)).Times(1) // A and B

			rig := startActorRigWith(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)))
			gate.openOnCleanup(ctx)
			pid, entity := rig.spawnQueuedBehindConfirmation(ctx, behavior, &testpb.CreditAccount{AccountId: persistenceID, Balance: 50})

			openBatchedAccount(ctx, pid, persistenceID)
			entity.armed.Store(true)

			shared := &testpb.CreditAccount{AccountId: persistenceID, Balance: 100}
			a := askInBackground(callWith("a"), pid, shared)
			ctx.Eventually(stashSize(pid), specs.Equal(uint64(1)), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			b := askInBackground(callWith("b"), pid, shared)
			gate.awaitStarted(ctx, "the write of A and B")
			ctx.Eventually(stashSize(pid), specs.Equal(uint64(2)), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			gate.open()

			// C, queued ahead of them, is not theirs to answer: both get the error,
			// not a state and not silence. C is answered too: see TestPendingRequestsAreAnsweredWhenTheWriteFails.
			for _, call := range []*backgroundAsk{a, b} {
				reply := call.await(ctx)
				failure, ok := reply.GetReply().(*egopb.CommandReply_ErrorReply)
				ctx.Expect(ok).To(specs.BeTrue())
				ctx.Expect(failure.ErrorReply.GetMessage()).To(containsText(errStoreFailure.Error()))
			}
		})
	})
}

// A restart keeps the Actor value, and PreStart clears the batch: the entries of
// the previous run and the replies they owed must not be answered by the next
// run, whose requests are other calls.
func TestResetBatchDropsTheRequestsOfThePreviousRun(t *testing.T) {
	specs.Describe(t, "Actor.resetBatch", func(s *specs.Spec) {
		s.It("leaves no entry, and no reply owed, for the next run to match", func(ctx *specs.Context) {
			entity := &Actor{
				phase:            phaseReplying,
				remainingReplies: 2,
				batchEntries: []batchEntry{
					{request: directRequest{set: true, message: &testpb.CreditAccount{}, ctx: callCtx("a")}},
					{request: directRequest{set: true, message: &testpb.CreditAccount{}, ctx: callCtx("b")}},
				},
			}

			entity.resetBatch()
			entity.resetDirect()

			ctx.Expect(entity.batchEntries).To(specs.BeNil())
			ctx.Expect(entity.remainingReplies).To(specs.Equal(0))
			ctx.Expect(entity.phase).To(specs.Equal(phaseProcessing))
		})
	})
}
