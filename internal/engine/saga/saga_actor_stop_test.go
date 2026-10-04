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
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

// GoAkt builds a relocated actor from its registered type, so the saga actor
// that stops on the node that received it is the zero value, not the result of
// New. PostStop used to close a stop channel only New created, and the node
// panicked when it stopped a relocated saga.
func TestPostStopOfAnActorGoAktBuiltWithoutNew(t *testing.T) {
	specs.Describe(t, "Actor.PostStop", func(s *specs.Spec) {
		s.It("stops a zero-value actor, as built on relocation, without panicking", func(ctx *specs.Context) {
			ctx.Expect((&Actor{}).PostStop(nil)).To(specs.BeNil())
		})

		s.It("stops an actor from New that never started", func(ctx *specs.Context) {
			ctx.Expect(New().PostStop(nil)).To(specs.BeNil())
		})

		s.It("can run twice, as when a restart stops an actor that already stopped", func(ctx *specs.Context) {
			actor := New()
			actor.stopCh = make(chan struct{})
			ctx.Expect(actor.PostStop(nil)).To(specs.BeNil())
			ctx.Expect(actor.PostStop(nil)).To(specs.BeNil())
		})
	})
}
