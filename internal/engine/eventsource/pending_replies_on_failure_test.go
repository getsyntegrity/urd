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
	"testing"
	"time"

	specmock "github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
)

// failedWriteCase is one entity whose write of A (and B, when batched) is held
// in flight and then fails, with a command C that reached the entity while the
// write was in flight. The entity must stop after such a failure, because it
// cannot tell what the store holds.
type failedWriteCase struct {
	idle          uint64 // actors the system holds before the entity is spawned
	pid           *goakt.PID
	entity        *Actor
	rig           *actorRig
	behavior      eventSourcedBehavior
	config        []extension.Dependency // what the entity is spawned with, again on a restart
	persistenceID string
	gate          *writeGate
	writeEvents   *specmock.Method
	shared        *testpb.CreditAccount
}

// startFailedWriteCase spawns an entity, creates its account, and expects one
// more write that the gate holds and then fails with errStoreFailure. No other
// write is expected: a write after the failure fails the case.
func startFailedWriteCase(ctx *specs.Context, config *extensions.EntityConfig) *failedWriteCase {
	persistenceID := uuid.NewString()
	behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)
	gate := newWriteGate()

	ctrl := specmock.NewController(ctx)
	expectStoreStartup(ctrl, persistenceID)
	writeEvents := ctrl.Method("WriteEvents")
	writeEvents.Expect(specmock.Any(), specmock.Any(), specmock.Any(), specmock.Any()).Times(1) // create
	writeEvents.Expect(specmock.Any(), specmock.Any(), specmock.Any(), specmock.Any()).
		Do(gate.hold(errStoreFailure)).Times(1) // the write that fails

	rig := startActorRigWith(ctx, "TestActorSystem", 1,
		extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)))
	gate.openOnCleanup(ctx)

	entity := New()
	deps := []extension.Dependency{behavior}
	if config != nil {
		deps = append(deps, config)
	}
	idle := rig.system.NumActors()
	pid, err := rig.system.Spawn(context.Background(), behavior.ID(), entity,
		goakt.WithDependencies(deps...), goakt.WithLongLived(), goakt.WithStashing())
	ctx.Expect(err).To(specs.BeNil())
	waitRunning(ctx, pid)

	return &failedWriteCase{
		idle: idle, config: deps, pid: pid, entity: entity, rig: rig, behavior: behavior, persistenceID: persistenceID,
		gate: gate, writeEvents: writeEvents,
		shared: &testpb.CreditAccount{AccountId: persistenceID, Balance: 100},
	}
}

// expectFailed requires the call to be answered with the error of the failed
// write, before its own timeout.
func expectFailed(ctx *specs.Context, call *backgroundAsk, what string) {
	expectErrorReply(ctx, call, what, errStoreFailure)
}

// expectNotRun requires the call to be answered with the error of an entity that
// stopped before it could run the command, before its own timeout.
func expectNotRun(ctx *specs.Context, call *backgroundAsk, what string) {
	expectErrorReply(ctx, call, what, errEntityStopped)
}

// expectErrorReply requires the call to be answered, exactly once, with an error
// reply carrying cause. A state reply, the reply of another request, or no
// answer at all fails the case.
func expectErrorReply(ctx *specs.Context, call *backgroundAsk, what string, cause error) {
	select {
	case <-call.done:
	case <-time.After(askTimeout + time.Second):
		ctx.T.Fatalf("no answer for %s", what)
	}
	if call.err != nil {
		ctx.T.Fatalf("%s was not answered, its call ended with: %v", what, call.err)
	}
	ctx.Expect(errorReplyMessage(ctx, call.await(ctx))).To(containsText(cause.Error()))
}

// expectStoppedAndRestartable requires the entity to have stopped after the
// failure, with no write beyond the create and the one that failed, and a new run
// of the same Actor value to start out of the stopping phase: it answers a read
// instead of an error or nothing.
func (c *failedWriteCase) expectStoppedAndRestartable(ctx *specs.Context) {
	waitStopped(ctx, c.pid)
	ctx.Expect(len(c.writeEvents.Calls())).To(specs.Equal(2))

	// Let the system release the stopped actor, then run the same Actor value again,
	// the way a supervisor restart does.
	ctx.Eventually(func() any { return c.rig.system.NumActors() }, specs.Equal(c.idle),
		specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
	pid, err := c.rig.system.Spawn(context.Background(), c.behavior.ID(), c.entity,
		goakt.WithDependencies(c.config...), goakt.WithLongLived(), goakt.WithStashing())
	ctx.Expect(err).To(specs.BeNil())
	waitRunning(ctx, pid)

	stateReplyOf(ctx, ask(ctx, pid, &egopb.GetStateCommand{}))
	ctx.Expect(len(c.writeEvents.Calls())).To(specs.Equal(2))
}

// When the write of a pending request fails, the entity stops, because it cannot
// tell what the store holds. A command queued behind the pending request was
// accepted by the entity, and it must not be left to its caller's timeout: it
// is answered with an explicit error, without being run, and nothing is written
// for it.
func TestPendingRequestsAreAnsweredWhenTheWriteFails(t *testing.T) {
	specs.Describe(t, "an entity whose write fails with a command queued behind it", func(s *specs.Spec) {
		s.It("answers the pending request and the queued one, each once, on the direct path", func(ctx *specs.Context) {
			c := startFailedWriteCase(ctx, nil)
			ask(ctx, c.pid, &testpb.CreateAccount{AccountBalance: 500})

			// A and C send the very same message object: only the call tells them apart.
			a := askInBackground(callWith("a"), c.pid, c.shared)
			c.gate.awaitStarted(ctx, "the write of A")
			queued := askInBackground(callWith("c"), c.pid, c.shared)
			// The stash holds A and C: C is queued behind the write.
			ctx.Eventually(stashSize(c.pid), specs.Equal(uint64(2)), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			c.gate.open()

			expectFailed(ctx, a, "the reply of A")
			expectNotRun(ctx, queued, "the reply of C, queued behind the failed write")
			c.expectStoppedAndRestartable(ctx)
		})

		s.It("answers the pending requests and the queued one, each once, on the batched path", func(ctx *specs.Context) {
			c := startFailedWriteCase(ctx, &extensions.EntityConfig{BatchThreshold: 2, BatchFlushWindow: time.Hour})
			openBatchedAccountOnce(ctx, c)

			a := askInBackground(callWith("a"), c.pid, c.shared)
			ctx.Eventually(stashSize(c.pid), specs.Equal(uint64(1)), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			b := askInBackground(callWith("b"), c.pid, c.shared)
			c.gate.awaitStarted(ctx, "the write of A and B")
			ctx.Eventually(stashSize(c.pid), specs.Equal(uint64(2)), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			// C arrives while the batch is in flight, so it is stashed with A and B.
			queued := askInBackground(callWith("c"), c.pid, c.shared)
			ctx.Eventually(stashSize(c.pid), specs.Equal(uint64(3)), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
			c.gate.open()

			expectFailed(ctx, a, "the reply of A")
			expectFailed(ctx, b, "the reply of B")
			expectNotRun(ctx, queued, "the reply of C, queued behind the failed batch")
			c.expectStoppedAndRestartable(ctx)
		})
	})
}

// openBatchedAccountOnce creates the account in a batch that reaches the
// threshold with one more event, which the case's first write (the create) lets
// through.
func openBatchedAccountOnce(ctx *specs.Context, c *failedWriteCase) {
	openBatchedAccount(ctx, c.pid, c.persistenceID)
}
