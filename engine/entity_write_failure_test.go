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
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"

	"github.com/getsyntegrity/urd/egopb"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/testkit"
)

var errInjectedWrite = errors.New("injected write failure")

// failingWriteStore fails the write that reaches it while armed, and delegates
// every other call to the in-memory store.
type failingWriteStore struct {
	*testkit.EventStore
	failNext atomic.Bool
	failed   atomic.Int32
}

func (s *failingWriteStore) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	if s.failNext.CompareAndSwap(true, false) {
		s.failed.Add(1)
		return errInjectedWrite
	}
	return s.EventStore.WriteEvents(ctx, scope, events, precondition)
}

// A failed write answers the command that caused it with the failure, never
// with success, and does not leave that request behind: the entity records the
// request whose write is in flight to tell it from the others, and a failed
// write must clear it as a successful one does.
func TestEntityAnswersTheCommandWhoseWriteFails(t *testing.T) {
	specs.Describe(t, "an event-sourced entity whose write fails", func(s *specs.Spec) {
		bg := context.Background()

		s.It("answers the caller with the failure, promptly", func(ctx *specs.Context) {
			store := &failingWriteStore{EventStore: connectedEventsStore(ctx)}
			engine := startEngine(ctx, "WriteFailure", store)
			id := uuid.NewString()
			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id))).To(specs.BeNil())

			_, _, err := engine.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 10}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())

			store.failNext.Store(true)
			started := time.Now()
			_, _, err = engine.SendCommand(bg, id, &testpb.CreditAccount{AccountId: id, Balance: 1}, 20*time.Second)

			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err.Error()).To(specs.Contain(errInjectedWrite.Error()))
			// an answer, not the caller's own timeout
			ctx.Expect(time.Since(started) < 10*time.Second).To(specs.BeTrue())
			ctx.Expect(int(store.failed.Load())).To(specs.Equal(1))

			// the journal holds the create only: the failed credit never landed
			latest, getErr := store.GetLatestEvent(bg, persistence.Unscoped(), id)
			ctx.Expect(getErr).To(specs.BeNil())
			ctx.Expect(latest.GetSequenceNumber()).To(specs.Equal(uint64(1)))
		})
	})
}

// An entity that GoAkt restarts serves again from what it persisted: PreStart
// drops the write the previous run had in flight, so the restarted entity does
// not stash every command behind a request that no longer exists.
func TestEntityServesAgainAfterARestart(t *testing.T) {
	specs.Describe(t, "a restarted event-sourced entity", func(s *specs.Spec) {
		bg := context.Background()

		s.It("recovers its state and runs new commands", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)
			engine := startEngine(ctx, "Restart", store)
			id := uuid.NewString()
			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			_, _, err := engine.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 10}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())

			pid, err := engine.actorSystem.Load().sys.ActorOf(bg, id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(pid.Restart(bg)).To(specs.BeNil())

			state, _, err := engine.SendCommand(bg, id, &testpb.CreditAccount{AccountId: id, Balance: 5}, 10*time.Second)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(15.0))
		})
	})
}
