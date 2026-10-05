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

	"github.com/getsyntegrity/urd/internal/syncmap"
)

type Stream interface {
	// AddSubscriber adds a subscriber
	AddSubscriber() Subscriber
	// RemoveSubscriber removes a subscriber
	RemoveSubscriber(sub Subscriber)
	// SubscribersCount returns the number of subscribers for a given topic
	SubscribersCount(topic string) int
	// Subscribe subscribes a subscriber to a topic
	Subscribe(sub Subscriber, topic string)
	// Unsubscribe removes a subscriber from a topic
	Unsubscribe(sub Subscriber, topic string)
	// Publish publishes a message to a topic
	Publish(topic string, msg any)
	// Broadcast notifies all subscribers of a given topic of a new message
	Broadcast(msg any, topics []string)
	// Close closes the stream
	Close()
}

// EventsStream defines the stream broker
type EventsStream struct {
	subscribers *syncmap.Map[string, Subscriber]
	topics      *syncmap.Map[route, *syncmap.Map[string, Subscriber]]

	// mu makes every change to the bookkeeping of subscribers and topics atomic:
	// AddSubscriber, RemoveSubscriber, Subscribe, Unsubscribe and Close all take
	// it, so none of them sees the maps half changed by another. Subscribe has
	// to look the topic up and create it when it is missing, and without the
	// lock two subscribers that arrive together at a new topic each create its
	// map and the second replaces the first, which silently drops the first
	// subscriber. Publish does not take it: it only reads, and the maps it
	// reads are themselves safe for concurrent use.
	mu sync.Mutex
}

// enforce a compilation error
var _ Stream = (*EventsStream)(nil)

// EventsStream also implements the optional ScopedStream.
var _ ScopedStream = (*EventsStream)(nil)

// New creates an instance of EventsStream
func New() Stream {
	return &EventsStream{
		subscribers: syncmap.New[string, Subscriber](),
		topics:      syncmap.New[route, *syncmap.Map[string, Subscriber]](),
	}
}

// AddSubscriber adds a subscriber
func (b *EventsStream) AddSubscriber() Subscriber {
	b.mu.Lock()
	defer b.mu.Unlock()

	subscriber := newSubscriber()
	b.subscribers.Set(subscriber.ID(), subscriber)
	return subscriber
}

// RemoveSubscriber removes a subscriber
func (b *EventsStream) RemoveSubscriber(sub Subscriber) {
	b.mu.Lock()
	// remove subscriber to the broker.
	//unsubscribe to all topics which s is subscribed to.
	for _, r := range sub.routes() {
		b.unsubscribeRoute(sub, r)
	}
	b.subscribers.Delete(sub.ID())
	b.mu.Unlock()

	sub.Shutdown()
}

// Broadcast notifies all subscribers of a given topic of a new message
func (b *EventsStream) Broadcast(msg any, topics []string) {
	for _, topic := range topics {
		b.publishToRoute(legacyScope(), topic, msg)
	}
}

// SubscribersCount returns the number of subscribers for a given topic
func (b *EventsStream) SubscribersCount(topic string) int {
	if subscribers, ok := b.topics.Get(unscopedRoute(topic)); ok {
		return subscribers.Len()
	}
	return 0
}

// Subscribe subscribes a subscriber to a topic of the Unscoped() scope. It is
// the single-tenant path: a subscription made here never receives a message
// published for a tenant scope.
func (b *EventsStream) Subscribe(subscriber Subscriber, topic string) {
	b.subscribeRoute(subscriber, unscopedRoute(topic))
}

// subscribeRoute registers subscriber on r. Only active consumers subscribe.
func (b *EventsStream) subscribeRoute(subscriber Subscriber, r route) {
	if !subscriber.Active() {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	subscriber.subscribeRoute(r)
	if subscribers, ok := b.topics.Get(r); ok && subscribers.Len() != 0 {
		subscribers.Set(subscriber.ID(), subscriber)
		return
	}

	// here the route does not exist
	subscribers := syncmap.New[string, Subscriber]()
	subscribers.Set(subscriber.ID(), subscriber)
	b.topics.Set(r, subscribers)
}

// Unsubscribe removes a subscriber from a topic of the Unscoped() scope.
func (b *EventsStream) Unsubscribe(subscriber Subscriber, topic string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.unsubscribeRoute(subscriber, unscopedRoute(topic))
}

// unsubscribeRoute removes subscriber from r. The caller holds b.mu.
func (b *EventsStream) unsubscribeRoute(subscriber Subscriber, r route) {
	subscriber.unsubscribeRoute(r)
	if subscribers, ok := b.topics.Get(r); ok && subscribers.Len() != 0 {
		subscribers.Delete(subscriber.ID())
	}
}

// Publish publishes a message to a topic of the Unscoped() scope.
func (b *EventsStream) Publish(topic string, msg any) {
	b.publishToRoute(legacyScope(), topic, msg)
}

// Close closes the stream
func (b *EventsStream) Close() {
	// Close replaces the topic bookkeeping, so it takes the same lock as
	// Subscribe and Unsubscribe: a Subscribe that overlapped it could otherwise
	// add its subscriber to a map that Reset is about to discard.
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, subscriber := range b.subscribers.Values() {
		if subscriber.Active() {
			subscriber.Shutdown()
		}
	}
	b.subscribers.Reset()
	b.topics.Reset()
}

// publishToRoute delivers the message to the active subscribers of the
// (scope, topic) route. It
// performs a single message allocation per publish. Delivery is
// synchronous: signal only enqueues on a lock-free queue and does a
// non-blocking wake-up, so it never blocks the caller, and enqueueing before
// Publish returns keeps the order of consecutive Publish calls from one
// producer. Handing each delivery to its own goroutine let two messages
// published in order be enqueued swapped.
func (b *EventsStream) publishToRoute(scope Scope, topic string, msg any) {
	message := newScopedMessage(scope, topic, msg)
	deliver := func(r route) {
		subscribers, ok := b.topics.Get(r)
		if !ok || subscribers.Len() == 0 {
			return
		}
		subscribers.Range(func(_ string, sub Subscriber) {
			if sub.Active() {
				sub.signal(message)
			}
		})
	}
	deliver(route{scope: scope, topic: topic})
}
