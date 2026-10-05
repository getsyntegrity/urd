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
	"context"
	"sync"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/goaktlog"
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
		s.It("registers exactly one scoped subscription for its tenant on the events topic, and no legacy one", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			store := newTestkitStore(ctx)
			spy := &subscribeSpy{ScopedStream: eventstream.New().(eventstream.ScopedStream)}
			ctx.Cleanup(spy.Close)

			system, err := goakt.NewActorSystem("SagaScopedSubscribe",
				goakt.WithLogger(goaktlog.New(enginetest.DiscardLogger)),
				goakt.WithExtensions(extensions.NewEventsStore(store), extensions.NewEventsStream(spy), extensions.NewTenancyMarker(false)),
				goakt.WithActorInitMaxRetries(1))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(system.Start(context.Background())).To(specs.BeNil())
			ctx.Cleanup(func() { ctx.Expect(system.Stop(context.Background())).To(specs.BeNil()) })

			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, _ Event, _ State) (*sagaAction, error) {
					return &sagaAction{}, nil
				},
			}
			_, err = system.Spawn(context.Background(), sagaID, New(), goakt.WithLongLived(),
				goakt.WithDependencies(behavior, extensions.NewSagaConfig(0), extensions.NewEntityTenantScope("tenant-a")))
			ctx.Expect(err).To(specs.BeNil())

			// Registration: exactly one scoped subscription, for tenant-a on the
			// events topic, and no legacy Subscribe. An unscoped subscription
			// would show up in legacy (or with Unscoped()) and fail here.
			scopes, topics, legacy := spy.snapshot()
			a, err := eventstream.TenantScope("tenant-a")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(scopes).To(specs.Equal([]eventstream.Scope{a}))
			ctx.Expect(topics).To(specs.Equal([]string{protocol.EventsTopic}))
			ctx.Expect(legacy).To(specs.BeEmpty())
		})

		s.It("only the saga's tenant reaches HandleEvent: tenant-b's event is not delivered, tenant-a's is", func(ctx *specs.Context) {
			sagaID := uuid.NewString()
			store := newTestkitStore(ctx)
			rig := newSagaRig(ctx, store, extensions.NewTenancyMarker(false))

			seen := make(chan string, 4)
			behavior := &enginetest.CallbackSagaBehavior{
				SagaID: sagaID,
				HandleEventFn: func(_ context.Context, event Event, _ State) (*sagaAction, error) {
					if acct, ok := event.(interface{ GetAccountId() string }); ok {
						seen <- acct.GetAccountId()
					}
					return &sagaAction{}, nil
				},
			}
			rig.spawnSaga(ctx, sagaID, behavior, extensions.NewSagaConfig(0), extensions.NewEntityTenantScope("tenant-a"))

			publish := func(tenant, account string) {
				tid, err := tenancy.NewTenantID(tenant)
				ctx.Expect(err).To(specs.BeNil())
				tc, err := tenancy.NewTenantContext(tid)
				ctx.Expect(err).To(specs.BeNil())
				event := accountCreatedEvent(ctx, account, tenancy.MarshalMetadata(tc))
				scope, err := protocol.StreamScope(mustTenantScope(ctx, tid))
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(rig.stream.(eventstream.ScopedStream).PublishScoped(scope, protocol.EventsTopic, event)).To(specs.BeNil())
			}

			// tenant-b first, tenant-a second: delivery to one subscriber is FIFO,
			// so once tenant-a's event was handled, anything tenant-b's earlier
			// event would have caused has already happened.
			publish("tenant-b", "acct-b")
			publish("tenant-a", "acct-a")

			ctx.Eventually(func() any { return len(seen) }, specs.BeGreaterThanOrEqual(1),
				specs.WithTimeout(signalTimeout), specs.WithInterval(pollEvery))
			ctx.Expect(<-seen).To(specs.Equal("acct-a"))
			ctx.Expect(len(seen)).To(specs.Equal(0))
		})
	})
}

func mustTenantScope(ctx *specs.Context, id tenancy.TenantID) persistence.Scope {
	scope, err := persistence.NewTenantScope(id)
	ctx.Expect(err).To(specs.BeNil())
	return scope
}

func accountCreatedEvent(ctx *specs.Context, account string, md map[string]string) *egopb.Event {
	return newAnyEvent(ctx, uuid.NewString(), 1, &testpb.AccountCreated{AccountId: account}, md)
}
