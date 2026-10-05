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
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/tenancy"
)

// forTenantDelivery is what a per-tenant publisher saw: the tenant its context
// named and the tenant its message's own metadata named.
type forTenantDelivery struct {
	ctxTenant  tenancy.TenantID
	metaTenant tenancy.TenantID
}

func tenantsOf(ctx context.Context, metadata map[string]string) forTenantDelivery {
	var d forTenantDelivery
	if tc, ok := tenancy.From(ctx); ok {
		d.ctxTenant, _ = tc.Tenant()
	}
	if tc, err := tenancy.UnmarshalMetadata(tenancy.Metadata(metadata)); err == nil {
		d.metaTenant, _ = tc.Tenant()
	}
	return d
}

type namedEventPublisher struct {
	id     string
	closed atomic.Bool
	mu     sync.Mutex
	seen   []forTenantDelivery
}

func (p *namedEventPublisher) ID() string { return p.id }
func (p *namedEventPublisher) Publish(ctx context.Context, e *egopb.Event) error {
	p.mu.Lock()
	p.seen = append(p.seen, tenantsOf(ctx, e.GetTenantMetadata()))
	p.mu.Unlock()
	return nil
}
func (p *namedEventPublisher) Close(context.Context) error { p.closed.Store(true); return nil }
func (p *namedEventPublisher) got() []forTenantDelivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]forTenantDelivery(nil), p.seen...)
}

type namedStatePublisher struct {
	id     string
	closed atomic.Bool
	mu     sync.Mutex
	seen   []forTenantDelivery
}

func (p *namedStatePublisher) ID() string { return p.id }
func (p *namedStatePublisher) Publish(ctx context.Context, s *egopb.DurableState) error {
	p.mu.Lock()
	p.seen = append(p.seen, tenantsOf(ctx, s.GetTenantMetadata()))
	p.mu.Unlock()
	return nil
}
func (p *namedStatePublisher) Close(context.Context) error { p.closed.Store(true); return nil }
func (p *namedStatePublisher) got() []forTenantDelivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]forTenantDelivery(nil), p.seen...)
}

func awaitSeen(ctx *specs.Context, got func() []forTenantDelivery, n int) {
	ctx.Eventually(func() any { return len(got()) }, specs.BeGreaterThanOrEqual(n),
		specs.WithTimeout(waitTimeout), specs.WithInterval(time.Millisecond))
}

func queuedEvents(sub eventstream.Subscriber) []*egopb.Event {
	var out []*egopb.Event
	for m := range sub.Iterator() {
		if e, ok := m.Payload().(*egopb.Event); ok {
			out = append(out, e)
		}
	}
	return out
}

// TestPerTenantRegistrationIsolatesTwoTenants runs a multi-tenant engine whose
// resolver resolves each caller's own tenant and so fixes none, with the SAME
// entity id under two tenants, and registers per tenant.
func TestPerTenantRegistrationIsolatesTwoTenants(t *testing.T) {
	specs.Describe(t, "per-tenant publishers and subscribers of a per-caller-resolver engine (EGO-TENANT-005)", func(s *specs.Spec) {
		bg := context.Background()
		acme := WithTenant(tenancy.TenantID("acme"))
		globex := WithTenant(tenancy.TenantID("globex"))

		s.It("delivers each tenant's events to its own publisher and subscriber only, with that tenant in the context", func(ctx *specs.Context) {
			engine := newIdentityEngine(ctx, "PerTenantIsolation", connectedEventsStore(ctx), connectedDurableStore(ctx))
			id := uuid.NewString()
			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id), acme)).To(specs.BeNil())
			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id), globex)).To(specs.BeNil())

			pubA, pubB := &namedEventPublisher{id: "pub-acme"}, &namedEventPublisher{id: "pub-globex"}
			ctx.Expect(engine.AddEventPublishersForTenant("acme", pubA)).To(specs.BeNil())
			ctx.Expect(engine.AddEventPublishersForTenant("globex", pubB)).To(specs.BeNil())
			subA, err := engine.SubscribeForTenant("acme")
			ctx.Expect(err).To(specs.BeNil())
			subB, err := engine.SubscribeForTenant("globex")
			ctx.Expect(err).To(specs.BeNil())
			subNobody, err := engine.SubscribeForTenant("initech")
			ctx.Expect(err).To(specs.BeNil())

			_, _, err = engine.SendCommand(callerOf("acme"), id, &testpb.CreateAccount{AccountBalance: 10}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			_, _, err = engine.SendCommand(callerOf("globex"), id, &testpb.CreateAccount{AccountBalance: 20}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			_, _, err = engine.SendCommand(callerOf("globex"), id, &testpb.CreditAccount{AccountId: id, Balance: 1}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())

			// The subscribers' queues are filled synchronously by the publish
			// that precedes each reply, so this is exact, not a race: the key
			// isolation assertion is that no queue holds another tenant's event.
			for _, e := range queuedEvents(subA) {
				ctx.Expect(tenantsOf(bg, e.GetTenantMetadata()).metaTenant).To(specs.Equal(tenancy.TenantID("acme")))
			}
			ctx.Expect(queuedEvents(subB)).To(specs.HaveLen(0 + 2))
			ctx.Expect(queuedEvents(subNobody)).To(specs.BeEmpty())

			awaitSeen(ctx, pubA.got, 1)
			awaitSeen(ctx, pubB.got, 2)
			ctx.Expect(pubA.got()).To(specs.Equal([]forTenantDelivery{{"acme", "acme"}}))
			ctx.Expect(pubB.got()).To(specs.Equal([]forTenantDelivery{{"globex", "globex"}, {"globex", "globex"}}))
		})

		s.It("delivers a tenant's durable states to its per-tenant state publisher only", func(ctx *specs.Context) {
			engine := newIdentityEngine(ctx, "PerTenantStates", connectedEventsStore(ctx), connectedDurableStore(ctx))
			pubA, pubB := &namedStatePublisher{id: "state-acme"}, &namedStatePublisher{id: "state-globex"}
			ctx.Expect(engine.AddStatePublishersForTenant("acme", pubA)).To(specs.BeNil())
			ctx.Expect(engine.AddStatePublishersForTenant("globex", pubB)).To(specs.BeNil())

			scoped := engine.eventStream.(eventstream.ScopedStream)
			for _, tenant := range []string{"acme", "globex", "globex"} {
				tid, err := tenancy.NewTenantID(tenant)
				ctx.Expect(err).To(specs.BeNil())
				tc, err := tenancy.NewTenantContext(tid)
				ctx.Expect(err).To(specs.BeNil())
				scope, err := eventstream.TenantScope(tid)
				ctx.Expect(err).To(specs.BeNil())
				state := &egopb.DurableState{PersistenceId: "p", TenantMetadata: tenancy.MarshalMetadata(tc)}
				ctx.Expect(scoped.PublishScoped(scope, protocol.StatesTopic, state)).To(specs.BeNil())
			}

			awaitSeen(ctx, pubA.got, 1)
			awaitSeen(ctx, pubB.got, 2)
			ctx.Expect(pubA.got()).To(specs.Equal([]forTenantDelivery{{"acme", "acme"}}))
			ctx.Expect(pubB.got()).To(specs.Equal([]forTenantDelivery{{"globex", "globex"}, {"globex", "globex"}}))
		})

		s.It("never delivers an administrative, unscoped or foreign message to a per-tenant publisher, and keeps running", func(ctx *specs.Context) {
			engine := newIdentityEngine(ctx, "PerTenantNoBypass", connectedEventsStore(ctx), connectedDurableStore(ctx))
			pub := &namedEventPublisher{id: "pub-acme"}
			ctx.Expect(engine.AddEventPublishersForTenant("acme", pub)).To(specs.BeNil())

			scoped := engine.eventStream.(eventstream.ScopedStream)
			a, mdA := scopeFor(ctx, "acme")
			_, mdB := scopeFor(ctx, "globex")
			admin, err := tenancy.NewAdministrative("ops", "audit")
			ctx.Expect(err).To(specs.BeNil())
			adminCtx, err := tenancy.NewAdministrativeContext(admin)
			ctx.Expect(err).To(specs.BeNil())

			ctx.Expect(scoped.PublishScoped(a, protocol.EventsTopic, &egopb.Event{PersistenceId: "admin", TenantMetadata: tenancy.MarshalMetadata(adminCtx)})).To(specs.BeNil())
			ctx.Expect(scoped.PublishScoped(a, protocol.EventsTopic, &egopb.Event{PersistenceId: "foreign", TenantMetadata: mdB})).To(specs.BeNil())
			engine.eventStream.Publish(protocol.EventsTopic, &egopb.Event{PersistenceId: "unscoped"})
			ctx.Expect(scoped.PublishScoped(a, protocol.EventsTopic, &egopb.Event{PersistenceId: "good", TenantMetadata: mdA})).To(specs.BeNil())

			awaitSeen(ctx, pub.got, 1)
			ctx.Expect(pub.got()).To(specs.Equal([]forTenantDelivery{{"acme", "acme"}}))
		})

		s.It("keeps the existing methods failing closed with ErrPublicationTenantUndetermined, registering nothing", func(ctx *specs.Context) {
			engine := newIdentityEngine(ctx, "PerTenantLegacyFails", connectedEventsStore(ctx), connectedDurableStore(ctx))

			ctx.Expect(engine.AddEventPublishers(&namedEventPublisher{id: "e"})).To(specs.MatchError(ErrPublicationTenantUndetermined))
			ctx.Expect(engine.AddStatePublishers(&namedStatePublisher{id: "s"})).To(specs.MatchError(ErrPublicationTenantUndetermined))
			sub, err := engine.Subscribe()
			ctx.Expect(err).To(specs.MatchError(ErrPublicationTenantUndetermined))
			ctx.Expect(sub).To(specs.BeNil())
			ctx.Expect(engine.eventsStreams.Len()).To(specs.Equal(0))
			ctx.Expect(engine.statesStreams.Len()).To(specs.Equal(0))
		})

		s.It("rejects a duplicate publisher id across tenants atomically, and Stop closes every registered publisher", func(ctx *specs.Context) {
			engine := newIdentityEngine(ctx, "PerTenantDuplicates", connectedEventsStore(ctx), connectedDurableStore(ctx))
			first := &namedEventPublisher{id: "shared-id"}
			ctx.Expect(engine.AddEventPublishersForTenant("acme", first)).To(specs.BeNil())

			clash := &namedEventPublisher{id: "shared-id"}
			fresh := &namedEventPublisher{id: "fresh"}
			err := engine.AddEventPublishersForTenant("globex", fresh, clash)
			ctx.Expect(err).To(specs.MatchError(ErrDuplicatePublisherID))
			ctx.Expect(engine.eventsStreams.Len()).To(specs.Equal(1))

			// a second publisher for the same tenant under its own id is fine
			second := &namedEventPublisher{id: "second"}
			ctx.Expect(engine.AddEventPublishersForTenant("acme", second)).To(specs.BeNil())

			ctx.Expect(engine.Stop(bg)).To(specs.BeNil())
			ctx.Expect(first.closed.Load()).To(specs.BeTrue())
			ctx.Expect(second.closed.Load()).To(specs.BeTrue())
			ctx.Expect(fresh.closed.Load()).To(specs.BeFalse())
		})
	})
}

// TestPerTenantRegistrationValidation is the table of what ForTenant accepts
// and refuses, in each kind of engine. Every refusal registers nothing.
func TestPerTenantRegistrationValidation(t *testing.T) {
	specs.Describe(t, "per-tenant registration validates the tenant and the engine (EGO-TENANT-005)", func(s *specs.Spec) {
		type engineKind int
		const (
			perCaller engineKind = iota
			fixedAcme
			legacy
		)
		type row struct {
			name   string
			kind   engineKind
			tenant string
			want   error // nil: accepted
		}
		long := make([]byte, 129)
		for i := range long {
			long[i] = 'a'
		}

		specs.Table(s, []row{
			{"per-caller engine accepts a valid tenant", perCaller, "acme", nil},
			{"per-caller engine accepts an interior-space tenant", perCaller, "Acme Europe", nil},
			{"empty id is invalid, never meaning all tenants", perCaller, "", ErrInvalidPublicationTenant},
			{"surrounding whitespace is invalid", perCaller, " acme", ErrInvalidPublicationTenant},
			{"control character is invalid", perCaller, "ac\x00me", ErrInvalidPublicationTenant},
			{"over-long id is invalid", perCaller, string(long), ErrInvalidPublicationTenant},
			{"invalid UTF-8 is invalid", perCaller, "\xff\xfe", ErrInvalidPublicationTenant},
			{"a fixed-tenant engine accepts its own tenant", fixedAcme, "acme", nil},
			{"a fixed-tenant engine refuses another tenant", fixedAcme, "globex", ErrPublicationTenantMismatch},
			{"a fixed-tenant engine still validates the id", fixedAcme, "", ErrInvalidPublicationTenant},
			{"a legacy engine refuses every tenant", legacy, "acme", ErrPublicationTenantMismatch},
			{"a legacy engine validates the id first", legacy, "", ErrInvalidPublicationTenant},
		}, func(r row) string { return r.name }, func(ctx *specs.Context, r row) {
			store := connectedEventsStore(ctx)
			var engine *Engine
			switch r.kind {
			case perCaller:
				engine = startEngine(ctx, "TableEngine", store, WithTenantResolver(perCallerTenantResolver{}))
			case fixedAcme:
				resolver, err := tenancy.WithSingleTenant("acme")
				ctx.Expect(err).To(specs.BeNil())
				engine = startEngine(ctx, "TableEngine", store, WithTenantResolver(resolver))
			default:
				engine = startEngine(ctx, "TableEngine", store)
			}
			id := tenancy.TenantID(r.tenant)

			errEvents := engine.AddEventPublishersForTenant(id, &namedEventPublisher{id: "e"})
			errStates := engine.AddStatePublishersForTenant(id, &namedStatePublisher{id: "s"})
			sub, errSub := engine.SubscribeForTenant(id)

			if r.want == nil {
				ctx.Expect(errEvents).To(specs.BeNil())
				ctx.Expect(errStates).To(specs.BeNil())
				ctx.Expect(errSub).To(specs.BeNil())
				ctx.Expect(sub).To(specs.Not(specs.BeNil()))
				return
			}
			for _, err := range []error{errEvents, errStates, errSub} {
				ctx.Expect(err).To(specs.MatchError(r.want))
			}
			ctx.Expect(sub).To(specs.BeNil())
			ctx.Expect(engine.eventsStreams.Len()).To(specs.Equal(0))
			ctx.Expect(engine.statesStreams.Len()).To(specs.Equal(0))
			ctx.Expect(engine.eventStream.SubscribersCount(protocol.EventsTopic)).To(specs.Equal(0))
			ctx.Expect(engine.eventStream.SubscribersCount(protocol.StatesTopic)).To(specs.Equal(0))
		})

		s.It("every ForTenant method returns ErrEngineNotStarted before Start", func(ctx *specs.Context) {
			engine := newSpecsEngine(ctx, "NotStarted", connectedEventsStore(ctx), WithTenantResolver(perCallerTenantResolver{}))

			ctx.Expect(engine.AddEventPublishersForTenant("acme", &namedEventPublisher{id: "e"})).To(specs.MatchError(ErrEngineNotStarted))
			ctx.Expect(engine.AddStatePublishersForTenant("acme", &namedStatePublisher{id: "s"})).To(specs.MatchError(ErrEngineNotStarted))
			_, err := engine.SubscribeForTenant("acme")
			ctx.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
		})

		s.It("a legacy engine keeps its zero-plumbing methods working", func(ctx *specs.Context) {
			engine := startEngine(ctx, "LegacyStillWorks", connectedEventsStore(ctx))

			ctx.Expect(engine.AddEventPublishers(&namedEventPublisher{id: "e"})).To(specs.BeNil())
			ctx.Expect(engine.AddStatePublishers(&namedStatePublisher{id: "s"})).To(specs.BeNil())
			_, err := engine.Subscribe()
			ctx.Expect(err).To(specs.BeNil())
		})
	})
}
