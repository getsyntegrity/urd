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

package eventstream

import (
	"sync"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

// Subscribe used to read "does the topic have subscribers?" and then create
// the topic's map, as two separate steps. Two subscribers that arrived together
// at a topic with no subscribers each created a map, the second assignment
// replaced the first, and the loser stayed subscribed to a map nobody
// published to: it received nothing, and nothing reported it. Sagas that
// restart together on a node with no other subscriber, as when GoAkt relocates
// them, hit it.
func TestStreamSubscribeIsAtomicPerTopic(t *testing.T) {
	specs.Describe(t, "subscribing concurrently to a topic nobody subscribed to yet", func(s *specs.Spec) {
		const (
			rounds      = 300
			subscribers = 8
		)

		s.It("keeps every subscriber, so each one receives what is published", func(ctx *specs.Context) {
			for range rounds {
				stream := New()
				subs := make([]Subscriber, subscribers)
				for i := range subs {
					subs[i] = stream.AddSubscriber()
				}

				var ready, done sync.WaitGroup
				start := make(chan struct{})
				for _, sub := range subs {
					ready.Add(1)
					done.Add(1)
					go func() {
						defer done.Done()
						ready.Done()
						<-start
						stream.Subscribe(sub, "events")
					}()
				}
				ready.Wait()
				close(start)
				done.Wait()

				ctx.Expect(stream.SubscribersCount("events")).To(specs.Equal(subscribers))

				stream.Publish("events", "msg")
				for _, sub := range subs {
					awaitQueued(ctx, sub, 1)
				}
				stream.Close()
			}
		})
	})
}

// Every change to the bookkeeping of subscribers and topics takes one lock, so
// they can run together without deadlock and without losing a subscriber: the
// count of a topic after the dust settles is exactly the subscribers that were
// subscribed to it and not removed.
func TestStreamBookkeepingChangesCoordinate(t *testing.T) {
	specs.Describe(t, "subscribing, unsubscribing, removing and closing at the same time", func(s *specs.Spec) {
		const (
			rounds   = 200
			keepers  = 6
			leavers  = 6
			removals = 6
		)

		s.It("ends with exactly the subscribers that stayed subscribed", func(ctx *specs.Context) {
			for range rounds {
				stream := New()
				keep := make([]Subscriber, keepers)
				leave := make([]Subscriber, leavers)
				remove := make([]Subscriber, removals)
				for i := range keep {
					keep[i] = stream.AddSubscriber()
				}
				for i := range leave {
					leave[i] = stream.AddSubscriber()
					stream.Subscribe(leave[i], "events")
				}
				for i := range remove {
					remove[i] = stream.AddSubscriber()
					stream.Subscribe(remove[i], "events")
				}

				var ready, done sync.WaitGroup
				start := make(chan struct{})
				run := func(f func()) {
					ready.Add(1)
					done.Add(1)
					go func() {
						defer done.Done()
						ready.Done()
						<-start
						f()
					}()
				}
				for _, sub := range keep {
					run(func() { stream.Subscribe(sub, "events") })
				}
				for _, sub := range leave {
					run(func() { stream.Unsubscribe(sub, "events") })
				}
				for _, sub := range remove {
					run(func() { stream.RemoveSubscriber(sub) })
				}
				run(func() { _ = stream.AddSubscriber() })
				ready.Wait()
				close(start)

				finished := make(chan struct{})
				go func() { done.Wait(); close(finished) }()
				ctx.Eventually(func() any {
					select {
					case <-finished:
						return true
					default:
						return false
					}
				}, specs.BeTrue(), specs.WithTimeout(waitTimeout), specs.WithInterval(waitInterval))

				ctx.Expect(stream.SubscribersCount("events")).To(specs.Equal(keepers))
				stream.Close()
			}
		})

		s.It("closes while subscribers arrive without deadlock", func(ctx *specs.Context) {
			for range rounds {
				stream := New()
				var ready, done sync.WaitGroup
				start := make(chan struct{})
				for range 8 {
					ready.Add(1)
					done.Add(1)
					go func() {
						defer done.Done()
						ready.Done()
						<-start
						stream.Subscribe(stream.AddSubscriber(), "events")
					}()
				}
				ready.Add(1)
				done.Add(1)
				go func() {
					defer done.Done()
					ready.Done()
					<-start
					stream.Close()
				}()
				ready.Wait()
				close(start)

				finished := make(chan struct{})
				go func() { done.Wait(); close(finished) }()
				ctx.Eventually(func() any {
					select {
					case <-finished:
						return true
					default:
						return false
					}
				}, specs.BeTrue(), specs.WithTimeout(waitTimeout), specs.WithInterval(waitInterval))
			}
		})
	})
}
