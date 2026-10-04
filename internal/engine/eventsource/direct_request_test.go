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

	"github.com/getsyntegrity/go-specs/specs"
	goakt "github.com/tochemey/goakt/v4/actor"

	testpb "github.com/getsyntegrity/urd/internal/testpb"
)

type callKey struct{}

// callCtx stands for the context one caller derives for one call.
func callCtx(id string) context.Context {
	return context.WithValue(context.Background(), callKey{}, id)
}

// A request is told apart by the call that sent it, not by its message: a
// caller may send one message object twice, and two callers may share it. The
// request GoAkt re-delivers from the stash is the same call, so it keeps the
// message, the caller's context and the sender, and no other request does.
func TestDirectRequestIsTheCallNotTheMessage(t *testing.T) {
	specs.Describe(t, "directRequest.matches", func(s *specs.Spec) {
		sender := new(goakt.PID)
		other := new(goakt.PID)
		message := &testpb.CreditAccount{AccountId: "a", Balance: 1}

		s.It("matches the request it recorded, as GoAkt re-delivers it", func(ctx *specs.Context) {
			callA := callCtx("a")
			request := directRequest{set: true, message: message, ctx: callA, sender: sender}

			ctx.Expect(request.matches(message, callA, sender)).To(specs.BeTrue())
		})

		s.It("does not match the same message object sent by another caller", func(ctx *specs.Context) {
			request := directRequest{set: true, message: message, ctx: callCtx("a"), sender: sender}

			// the very same message object, another call
			ctx.Expect(request.matches(message, callCtx("b"), sender)).To(specs.BeFalse())
		})

		s.It("does not match the same message object sent again by the same caller in another call", func(ctx *specs.Context) {
			request := directRequest{set: true, message: message, ctx: callCtx("a"), sender: sender}

			ctx.Expect(request.matches(message, callCtx("a"), sender)).To(specs.BeFalse())
		})

		s.It("does not match an equal message in another object, or another sender", func(ctx *specs.Context) {
			callA := callCtx("a")
			request := directRequest{set: true, message: message, ctx: callA, sender: sender}

			equal := &testpb.CreditAccount{AccountId: "a", Balance: 1}
			ctx.Expect(request.matches(equal, callA, sender)).To(specs.BeFalse())
			ctx.Expect(request.matches(message, callA, other)).To(specs.BeFalse())
		})

		s.It("matches anything when no request is recorded, as before it recorded any", func(ctx *specs.Context) {
			ctx.Expect(directRequest{}.matches(message, callCtx("a"), sender)).To(specs.BeTrue())
		})

		s.It("does not panic on a value that cannot be compared", func(ctx *specs.Context) {
			type incomparable struct{ values []int }
			value := incomparable{values: []int{1}}
			request := directRequest{set: true, message: value, ctx: callCtx("a"), sender: sender}

			ctx.Expect(request.matches(value, callCtx("b"), sender)).To(specs.BeFalse())
			ctx.Expect(sameValue(value, value)).To(specs.BeTrue())
		})
	})
}

// A run that stops while it persists leaves its phase and the request it was
// waiting on behind, and GoAkt reuses the actor value for the next run. PreStart
// clears both, so the next run neither stashes every command forever nor takes
// a later request for the one that no longer exists.
func TestResetDirectDropsTheWriteInFlight(t *testing.T) {
	specs.Describe(t, "Actor.resetDirect", func(s *specs.Spec) {
		s.It("returns a run that stopped while persisting to phaseProcessing, with no request recorded", func(ctx *specs.Context) {
			entity := &Actor{
				phase:                phasePersisting,
				directPendingCounter: 7,
				directNumEvents:      2,
				directShutdown:       true,
				directRequest:        directRequest{set: true, message: &testpb.CreditAccount{}, ctx: callCtx("a")},
			}

			entity.resetDirect()

			ctx.Expect(entity.phase).To(specs.Equal(phaseProcessing))
			ctx.Expect(entity.directRequest.set).To(specs.BeFalse())
			ctx.Expect(entity.directPendingCounter).To(specs.Equal(uint64(0)))
			ctx.Expect(entity.directNumEvents).To(specs.Equal(0))
			ctx.Expect(entity.directShutdown).To(specs.BeFalse())
			ctx.Expect(entity.directErr).To(specs.BeNil())
		})

		s.It("also leaves a run that stopped while replying in phaseProcessing", func(ctx *specs.Context) {
			entity := &Actor{phase: phaseDirectReplying, directRequest: directRequest{set: true}}

			entity.resetDirect()

			ctx.Expect(entity.phase).To(specs.Equal(phaseProcessing))
			ctx.Expect(entity.directRequest.set).To(specs.BeFalse())
		})
	})
}
