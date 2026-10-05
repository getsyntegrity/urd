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
	"sync"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

// subscribeSpy records how the saga registers on the stream: through the
// scoped path (which scope, which topic) or through the legacy one.
type subscribeSpy struct {
	eventstream.ScopedStream

	mu     sync.Mutex
	scoped []eventstream.Scope
	topics []string
	legacy []string
}

func (s *subscribeSpy) Subscribe(sub eventstream.Subscriber, topic string) {
	s.mu.Lock()
	s.legacy = append(s.legacy, topic)
	s.mu.Unlock()
	s.ScopedStream.Subscribe(sub, topic)
}

func (s *subscribeSpy) SubscribeScoped(sub eventstream.Subscriber, scope eventstream.Scope, topic string) error {
	s.mu.Lock()
	s.scoped = append(s.scoped, scope)
	s.topics = append(s.topics, topic)
	s.mu.Unlock()
	return s.ScopedStream.SubscribeScoped(sub, scope, topic)
}

func (s *subscribeSpy) snapshot() (scoped []eventstream.Scope, topics, legacy []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]eventstream.Scope(nil), s.scoped...), append([]string(nil), s.topics...), append([]string(nil), s.legacy...)
}

func TestSagaActorSubscribesForItsTenantScope(t *testing.T) {
	specs.Describe(t, "a tenant-aware saga subscribes to the events topic for its bound scope only (EGO-TENANT-005)", func(s *specs.Spec) {
		// The saga is built directly and subscribeToEvents is the PreStart step
		// under test, so no actor system is started.
		newSaga := func(ctx *specs.Context, tenant string) (*Actor, *subscribeSpy) {
			spy := &subscribeSpy{ScopedStream: eventstream.New().(eventstream.ScopedStream)}
			ctx.Cleanup(spy.Close)
			return &Actor{eventsStream: spy, scope: mustTenantScope(ctx, tenancy.TenantID(tenant))}, spy
		}

		s.It("registers exactly one scoped subscription for its tenant on the events topic, and no legacy one", func(ctx *specs.Context) {
			saga, spy := newSaga(ctx, "tenant-a")

			ctx.Expect(saga.subscribeToEvents()).To(specs.BeNil())

			scopes, topics, legacy := spy.snapshot()
			a, err := eventstream.TenantScope("tenant-a")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(scopes).To(specs.Equal([]eventstream.Scope{a}))
			ctx.Expect(topics).To(specs.Equal([]string{protocol.EventsTopic}))
			ctx.Expect(legacy).To(specs.BeEmpty())
		})

		s.It("its subscriber is handed tenant-a's event and never tenant-b's", func(ctx *specs.Context) {
			saga, spy := newSaga(ctx, "tenant-a")
			ctx.Expect(saga.subscribeToEvents()).To(specs.BeNil())

			publish := func(tenant, account string) {
				tid, err := tenancy.NewTenantID(tenant)
				ctx.Expect(err).To(specs.BeNil())
				tc, err := tenancy.NewTenantContext(tid)
				ctx.Expect(err).To(specs.BeNil())
				event := accountCreatedEvent(ctx, account, tenancy.MarshalMetadata(tc))
				scope, err := protocol.StreamScope(mustTenantScope(ctx, tid))
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(spy.PublishScoped(scope, protocol.EventsTopic, event)).To(specs.BeNil())
			}
			publish("tenant-b", "acct-b")
			publish("tenant-a", "acct-a")

			// Publication is synchronous, so what is queued is everything the
			// stream routed to the saga: this checks routing itself, not the
			// saga's own tenant check that would also drop a foreign event.
			var got []string
			for msg := range saga.subscriber.Iterator() {
				got = append(got, msg.Payload().(*egopb.Event).GetPersistenceId())
			}
			ctx.Expect(got).To(specs.Equal([]string{"saga-scope-acct-a"}))
		})

		s.It("fails closed and leaves no subscriber when the scope is invalid", func(ctx *specs.Context) {
			spy := &subscribeSpy{ScopedStream: eventstream.New().(eventstream.ScopedStream)}
			ctx.Cleanup(spy.Close)
			saga := &Actor{eventsStream: spy}

			ctx.Expect(saga.subscribeToEvents()).To(specs.MatchError(persistence.ErrInvalidScope))
			ctx.Expect(spy.SubscribersCount(protocol.EventsTopic)).To(specs.Equal(0))
		})
	})
}

func mustTenantScope(ctx *specs.Context, id tenancy.TenantID) persistence.Scope {
	scope, err := persistence.NewTenantScope(id)
	ctx.Expect(err).To(specs.BeNil())
	return scope
}

func accountCreatedEvent(ctx *specs.Context, account string, md map[string]string) *egopb.Event {
	return newAnyEvent(ctx, "saga-scope-"+account, 1, &testpb.AccountCreated{AccountId: account}, md)
}
