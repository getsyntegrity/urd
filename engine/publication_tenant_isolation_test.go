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
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

// delivery is what a publisher saw: the message and the tenant its context named.
type delivery struct {
	id     string
	tenant tenancy.TenantID
	hasCtx bool
}

type ctxEventPublisher struct {
	mu   sync.Mutex
	seen []delivery
}

func (p *ctxEventPublisher) ID() string { return "ctx-events" }
func (p *ctxEventPublisher) Publish(ctx context.Context, e *egopb.Event) error {
	d := delivery{id: e.GetPersistenceId()}
	if tc, ok := tenancy.From(ctx); ok {
		d.hasCtx = true
		d.tenant, _ = tc.Tenant()
	}
	p.mu.Lock()
	p.seen = append(p.seen, d)
	p.mu.Unlock()
	return nil
}
func (p *ctxEventPublisher) Close(context.Context) error { return nil }
func (p *ctxEventPublisher) got() []delivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]delivery(nil), p.seen...)
}

type ctxStatePublisher struct {
	mu   sync.Mutex
	seen []delivery
}

func (p *ctxStatePublisher) ID() string { return "ctx-states" }
func (p *ctxStatePublisher) Publish(ctx context.Context, s *egopb.DurableState) error {
	d := delivery{id: s.GetPersistenceId()}
	if tc, ok := tenancy.From(ctx); ok {
		d.hasCtx = true
		d.tenant, _ = tc.Tenant()
	}
	p.mu.Lock()
	p.seen = append(p.seen, d)
	p.mu.Unlock()
	return nil
}
func (p *ctxStatePublisher) Close(context.Context) error { return nil }
func (p *ctxStatePublisher) got() []delivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]delivery(nil), p.seen...)
}

func scopeFor(ctx *specs.Context, id string) (eventstream.Scope, map[string]string) {
	tid, err := tenancy.NewTenantID(id)
	ctx.Expect(err).To(specs.BeNil())
	scope, err := eventstream.TenantScope(tid)
	ctx.Expect(err).To(specs.BeNil())
	tc, err := tenancy.NewTenantContext(tid)
	ctx.Expect(err).To(specs.BeNil())
	return scope, tenancy.MarshalMetadata(tc)
}

func untilSeen(ctx *specs.Context, got func() []delivery, n int) {
	ctx.Eventually(func() any { return len(got()) }, specs.BeGreaterThanOrEqual(n),
		specs.WithTimeout(10*time.Second), specs.WithInterval(time.Millisecond))
}

func TestPublicationTenantIsolation(t *testing.T) {
	specs.Describe(t, "publishers and subscribers of an engine are isolated by tenant (EGO-TENANT-005)", func(s *specs.Spec) {
		bg := context.Background()

		s.It("a fixed-tenant engine delivers its tenant's events with that tenant in the context, and drops every other identity without stopping", func(ctx *specs.Context) {
			resolver, err := tenancy.WithSingleTenant("tenant-a")
			ctx.Expect(err).To(specs.BeNil())
			store := testkit.NewEventsStore()
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			engine := startEngine(ctx, "PubIsolationFixed", store, WithTenantResolver(resolver))
			pub := &ctxEventPublisher{}
			ctx.Expect(engine.AddEventPublishers(pub)).To(specs.BeNil())

			scoped := engine.eventStream.(eventstream.ScopedStream)
			a, mdA := scopeFor(ctx, "tenant-a")
			b, mdB := scopeFor(ctx, "tenant-b")
			var zero eventstream.Scope
			event := func(id string, md map[string]string) *egopb.Event {
				return &egopb.Event{PersistenceId: id, TenantMetadata: md}
			}

			// Another tenant's scope is never routed to this publisher.
			ctx.Expect(scoped.PublishScoped(b, protocol.EventsTopic, event("other-tenant", mdB))).To(specs.BeNil())
			// Reaching the publisher's route with identity that fails the delivery check.
			ctx.Expect(scoped.PublishScoped(a, protocol.EventsTopic, event("absent", nil))).To(specs.BeNil())
			ctx.Expect(scoped.PublishScoped(a, protocol.EventsTopic, event("mismatch", mdB))).To(specs.BeNil())
			ctx.Expect(scoped.PublishScoped(a, protocol.EventsTopic, event("garbage", map[string]string{"ego.tenant.scope": "x"}))).To(specs.BeNil())
			ctx.Expect(scoped.PublishScoped(zero, protocol.EventsTopic, event("zero", mdA))).To(specs.MatchError(eventstream.ErrInvalidScope))
			// The loop is still alive: a valid event after the bad ones arrives.
			ctx.Expect(scoped.PublishScoped(a, protocol.EventsTopic, event("good", mdA))).To(specs.BeNil())

			untilSeen(ctx, pub.got, 1)
			ctx.Expect(pub.got()).To(specs.Equal([]delivery{{id: "good", tenant: "tenant-a", hasCtx: true}}))
		})

		s.It("a fixed-tenant engine delivers its tenant's durable states the same way", func(ctx *specs.Context) {
			resolver, err := tenancy.WithSingleTenant("tenant-a")
			ctx.Expect(err).To(specs.BeNil())
			store := testkit.NewEventsStore()
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			engine := startEngine(ctx, "PubIsolationFixedState", store, WithTenantResolver(resolver))
			pub := &ctxStatePublisher{}
			ctx.Expect(engine.AddStatePublishers(pub)).To(specs.BeNil())

			scoped := engine.eventStream.(eventstream.ScopedStream)
			a, mdA := scopeFor(ctx, "tenant-a")
			b, mdB := scopeFor(ctx, "tenant-b")
			ctx.Expect(scoped.PublishScoped(b, protocol.StatesTopic, &egopb.DurableState{PersistenceId: "other", TenantMetadata: mdB})).To(specs.BeNil())
			ctx.Expect(scoped.PublishScoped(a, protocol.StatesTopic, &egopb.DurableState{PersistenceId: "mismatch", TenantMetadata: mdB})).To(specs.BeNil())
			ctx.Expect(scoped.PublishScoped(a, protocol.StatesTopic, &egopb.DurableState{PersistenceId: "absent"})).To(specs.BeNil())
			ctx.Expect(scoped.PublishScoped(a, protocol.StatesTopic, &egopb.DurableState{PersistenceId: "good", TenantMetadata: mdA})).To(specs.BeNil())

			ctx.Eventually(func() any { return len(pub.got()) }, specs.BeGreaterThanOrEqual(1),
				specs.WithTimeout(10*time.Second), specs.WithInterval(time.Millisecond))
			ctx.Expect(pub.got()).To(specs.Equal([]delivery{{id: "good", tenant: "tenant-a", hasCtx: true}}))
		})

		s.It("Engine.Subscribe in a fixed-tenant engine receives only that tenant's events and states", func(ctx *specs.Context) {
			resolver, err := tenancy.WithSingleTenant("tenant-a")
			ctx.Expect(err).To(specs.BeNil())
			store := testkit.NewEventsStore()
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			engine := startEngine(ctx, "PubIsolationSubscribe", store, WithTenantResolver(resolver))
			sub, err := engine.Subscribe()
			ctx.Expect(err).To(specs.BeNil())

			scoped := engine.eventStream.(eventstream.ScopedStream)
			a, mdA := scopeFor(ctx, "tenant-a")
			b, mdB := scopeFor(ctx, "tenant-b")
			ctx.Expect(scoped.PublishScoped(b, protocol.EventsTopic, &egopb.Event{PersistenceId: "b", TenantMetadata: mdB})).To(specs.BeNil())
			ctx.Expect(scoped.PublishScoped(b, protocol.StatesTopic, &egopb.DurableState{PersistenceId: "b", TenantMetadata: mdB})).To(specs.BeNil())
			engine.eventStream.Publish(protocol.EventsTopic, &egopb.Event{PersistenceId: "unscoped"})
			ctx.Expect(scoped.PublishScoped(a, protocol.EventsTopic, &egopb.Event{PersistenceId: "a", TenantMetadata: mdA})).To(specs.BeNil())

			var ids []string
			for m := range sub.Iterator() {
				ids = append(ids, m.Payload().(*egopb.Event).GetPersistenceId())
				ctx.Expect(m.Scope().Equal(a)).To(specs.BeTrue())
			}
			ctx.Expect(ids).To(specs.Equal([]string{"a"}))
		})

		s.It("a tenant-aware engine without a fixed tenant registers nothing and fails closed with a typed error", func(ctx *specs.Context) {
			store := testkit.NewEventsStore()
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			engine := startEngine(ctx, "PubIsolationUndetermined", store, WithTenantResolver(&stubTenantResolver{id: "acme"}))

			ctx.Expect(engine.AddEventPublishers(&ctxEventPublisher{})).To(specs.MatchError(ErrPublicationTenantUndetermined))
			ctx.Expect(engine.AddStatePublishers(&ctxStatePublisher{})).To(specs.MatchError(ErrPublicationTenantUndetermined))
			sub, err := engine.Subscribe()
			ctx.Expect(err).To(specs.MatchError(ErrPublicationTenantUndetermined))
			ctx.Expect(sub).To(specs.BeNil())

			ctx.Expect(engine.eventsStreams.Len()).To(specs.Equal(0))
			ctx.Expect(engine.statesStreams.Len()).To(specs.Equal(0))
			ctx.Expect(engine.eventStream.SubscribersCount(protocol.EventsTopic)).To(specs.Equal(0))
			ctx.Expect(engine.eventStream.SubscribersCount(protocol.StatesTopic)).To(specs.Equal(0))
		})

		s.It("single-tenant mode stays zero-plumbing: unscoped events reach publishers with no tenant context", func(ctx *specs.Context) {
			store := testkit.NewEventsStore()
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			engine := startEngine(ctx, "PubIsolationLegacy", store)
			pub := &ctxEventPublisher{}
			ctx.Expect(engine.AddEventPublishers(pub)).To(specs.BeNil())
			sub, err := engine.Subscribe()
			ctx.Expect(err).To(specs.BeNil())

			engine.eventStream.Publish(protocol.EventsTopic, &egopb.Event{PersistenceId: "legacy"})
			// An unscoped message that carries tenant metadata is a mismatch, not a delivery.
			_, mdA := scopeFor(ctx, "tenant-a")
			engine.eventStream.Publish(protocol.EventsTopic, &egopb.Event{PersistenceId: "tagged", TenantMetadata: mdA})
			engine.eventStream.Publish(protocol.EventsTopic, &egopb.Event{PersistenceId: "legacy-2"})

			ctx.Eventually(func() any { return len(pub.got()) }, specs.BeGreaterThanOrEqual(2),
				specs.WithTimeout(10*time.Second), specs.WithInterval(time.Millisecond))
			ctx.Expect(pub.got()).To(specs.Equal([]delivery{{id: "legacy"}, {id: "legacy-2"}}))
			ctx.Expect(len(drainAll(sub))).To(specs.Equal(3))
		})
	})
}

func drainAll(sub eventstream.Subscriber) []*eventstream.Message {
	var out []*eventstream.Message
	for m := range sub.Iterator() {
		out = append(out, m)
	}
	return out
}
