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

package queue

import (
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/getsyntegrity/go-specs/specs"
)

// An Enqueue links its node and only then advances tail, so another producer can
// observe a tail that lags behind the last node, and a consumer can dequeue past
// it in between. The queue must not hand the node tail still points at to anyone
// else: a producer that loaded that tail would link its value to a node nobody
// reads from, or, if the node is reused as its own successor, loop forever.
func TestQueueEnqueueAfterDequeuePassedALaggingTail(t *testing.T) {
	specs.Describe(t, "a queue whose tail lags behind a dequeued node", func(s *specs.Spec) {
		s.It("keeps delivering what is enqueued, in order", func(ctx *specs.Context) {
			q := NewQueue()

			// A producer that linked "a" after the sentinel and was descheduled
			// before advancing tail: the sentinel is still the tail.
			sentinel := (*item)(atomic.LoadPointer(&q.tail))
			linked := &item{v: "a"}
			atomic.StorePointer(&sentinel.next, unsafe.Pointer(linked))
			atomic.AddInt64(&q.len, 1)

			ctx.Expect(q.Dequeue()).To(specs.Equal("a"))

			// A queue that gets this wrong spins in Enqueue forever, so the call
			// runs apart to let the spec report that instead of hanging.
			enqueued := make(chan struct{})
			go func() {
				defer close(enqueued)
				q.Enqueue("b")
				q.Enqueue("c")
			}()
			finished := false
			select {
			case <-enqueued:
				finished = true
			case <-time.After(5 * time.Second):
			}
			ctx.Expect(finished).To(specs.BeTrue())

			ctx.Expect(q.Dequeue()).To(specs.Equal("b"))
			ctx.Expect(q.Dequeue()).To(specs.Equal("c"))
			ctx.Expect(q.Dequeue()).To(specs.BeNil())
			ctx.Expect(q.Length()).ToEqual(uint64(0))
		})
	})
}
