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
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

// administrativeScopeKey marks a ctx that administrativeScopeResolver
// resolves to an administrative (non-tenant) tenancy.TenantContext.
type administrativeScopeKey struct{}

// administrativeScopeResolver resolves an administrative TenantContext when
// ctx carries administrativeScopeKey and otherwise behaves like
// perCallerTenantResolver. It exposes no fixed tenant.
type administrativeScopeResolver struct{}

var _ tenancy.TenantResolver = administrativeScopeResolver{}

func (administrativeScopeResolver) Resolve(ctx context.Context) (tenancy.TenantContext, error) {
	if ctx.Value(administrativeScopeKey{}) != nil {
		admin, err := tenancy.NewAdministrative("ops-team", "audit")
		if err != nil {
			return tenancy.TenantContext{}, err
		}
		return tenancy.NewAdministrativeContext(admin)
	}
	return perCallerTenantResolver{}.Resolve(ctx)
}

// TestAdministrativeScopeIsNeverAnAggregateTenantScope pins that an
// administrative TenantContext can never stand in for an aggregate's tenant
// scope (no administrative bypass is supported, see openspec/changes/ego-tenant-008): it
// cannot declare a spawn, cannot command a tenant-bound entity, and cannot
// scope an erasure.
func TestAdministrativeScopeIsNeverAnAggregateTenantScope(t *testing.T) {
	specs.Describe(t, "an administrative TenantContext is never an aggregate's tenant scope", func(s *specs.Spec) {
		bg := context.Background()
		adminCtx := context.WithValue(bg, administrativeScopeKey{}, true)

		s.It("an administrative-only resolver cannot bind a spawn", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)

			engine := newSpecsEngine(ctx, "Sample", store, WithTenantResolver(administrativeScopeResolver{}))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())

			err := engine.Entity(adminCtx, newTenancyProbeEventSourcedBehavior(uuid.NewString()))
			ctx.Expect(err).To(specs.MatchError(ErrSpawnTenantUndetermined))
		})

		s.It("an administrative command is rejected by a tenant-bound entity", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)

			engine := newSpecsEngine(ctx, "Sample", store, WithTenantResolver(administrativeScopeResolver{}))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())

			entityID := uuid.NewString()
			probe := newTenancyProbeEventSourcedBehavior(entityID)
			ctx.Expect(engine.Entity(bg, probe, WithTenant(tenancy.TenantID("acme")))).To(specs.BeNil())

			_, _, err := engine.SendCommand(adminCtx, entityID, &testpb.CreateAccount{AccountBalance: 500}, time.Minute)
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrDenied))
			// HandleCommand must never run under an administrative scope
			ctx.Expect(probe.InvocationCount()).To(specs.BeZero())

			acme, err := persistence.NewTenantScope("acme")
			ctx.Expect(err).To(specs.BeNil())
			latest, err := store.GetLatestEvent(bg, acme, entityID)
			ctx.Expect(err).To(specs.BeNil())
			// nothing may be persisted under the entity's tenant
			ctx.Expect(latest).To(specs.BeNil())
		})

		s.It("an administrative erasure is denied and erases nothing", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)

			persistenceID := uuid.NewString()
			eventAny, err := anypb.New(&testpb.AccountCreated{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			acme, err := persistence.NewTenantScope("acme")
			ctx.Expect(err).To(specs.BeNil())
			scopes := []persistence.Scope{persistence.Unscoped(), acme}
			for _, scope := range scopes {
				ctx.Expect(store.WriteEvents(bg, scope, []*egopb.Event{{
					PersistenceId:  persistenceID,
					SequenceNumber: 1,
					Event:          eventAny,
					Timestamp:      time.Now().UnixNano(),
				}}, persistence.Unconditional())).To(specs.BeNil())
			}

			engine := newSpecsEngine(ctx, "Sample", store, WithTenantResolver(administrativeScopeResolver{}))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())

			ctx.Expect(engine.EraseEntity(adminCtx, persistenceID, true)).To(specs.MatchError(tenancy.ErrDenied))

			// an administrative erasure must not touch any scope
			var erased []string
			for _, scope := range scopes {
				latest, err := store.GetLatestEvent(bg, scope, persistenceID)
				ctx.Expect(err).To(specs.BeNil())
				if latest == nil {
					erased = append(erased, scope.String())
				}
			}
			ctx.Expect(erased).To(specs.BeEmpty())
		})
	})
}

// zeroTenantContextResolver is a misbehaving resolver that returns the zero
// value of tenancy.TenantContext alongside a nil error.
type zeroTenantContextResolver struct{}

var _ tenancy.TenantResolver = zeroTenantContextResolver{}

func (zeroTenantContextResolver) Resolve(context.Context) (tenancy.TenantContext, error) {
	return tenancy.TenantContext{}, nil
}

// TestAdministrativeScopeIsDeniedAtEveryEngineEntry pins TENANT-008's
// decision: no engine entry point supports an administrative bypass. An
// administrative TenantContext is denied at saga and durable-state spawn, at
// saga status and at a durable-state command, and none of those paths runs
// behavior or persists anything.
func TestAdministrativeScopeIsDeniedAtEveryEngineEntry(t *testing.T) {
	specs.Describe(t, "an administrative TenantContext is denied at every engine entry", func(s *specs.Spec) {
		bg := context.Background()
		adminCtx := context.WithValue(bg, administrativeScopeKey{}, true)

		s.It("an administrative-only resolver cannot bind a saga spawn", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)
			engine := newSpecsEngine(ctx, "Sample", store, WithTenantResolver(administrativeScopeResolver{}))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())

			saga := &enginetest.CallbackSagaBehavior{SagaID: "saga-" + uuid.NewString()}
			ctx.Expect(engine.Saga(adminCtx, saga, 0)).To(specs.MatchError(ErrSpawnTenantUndetermined))
		})

		s.It("an administrative-only resolver cannot bind a durable-state spawn", func(ctx *specs.Context) {
			engine := startEngine(ctx, "Sample", nil, WithLogger(DiscardLogger),
				WithStateStore(connectedDurableStore(ctx)), WithTenantResolver(administrativeScopeResolver{}))

			err := engine.DurableStateEntity(adminCtx, NewAccountDurableStateBehavior(uuid.NewString()))
			ctx.Expect(err).To(specs.MatchError(ErrSpawnTenantUndetermined))
		})

		s.It("an administrative caller cannot read a tenant-bound saga's status", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)
			engine := newSpecsEngine(ctx, "Sample", store, WithTenantResolver(administrativeScopeResolver{}))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())

			sagaID := "saga-" + uuid.NewString()
			ctx.Expect(engine.Saga(bg, &enginetest.CallbackSagaBehavior{SagaID: sagaID}, 0,
				WithTenant(tenancy.TenantID("acme")))).To(specs.BeNil())

			_, err := engine.SagaStatus(adminCtx, sagaID, 5*time.Second)
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrDenied))
		})

		s.It("an administrative command is rejected by a tenant-bound durable-state entity", func(ctx *specs.Context) {
			engine := startEngine(ctx, "Sample", nil, WithLogger(DiscardLogger),
				WithStateStore(connectedDurableStore(ctx)), WithTenantResolver(administrativeScopeResolver{}))

			entityID := uuid.NewString()
			ctx.Expect(engine.DurableStateEntity(bg, NewAccountDurableStateBehavior(entityID),
				WithTenant(tenancy.TenantID("acme")))).To(specs.BeNil())

			_, _, err := engine.SendCommand(adminCtx, entityID, &testpb.CreateAccount{AccountBalance: 500}, time.Minute)
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrDenied))
		})
	})
}

// TestNoTenantIdentityGrantsAdministrativePrivilege pins that an empty or
// default tenant, a zero-value TenantContext and the single-tenant and legacy
// modes never act as an administrative context.
func TestNoTenantIdentityGrantsAdministrativePrivilege(t *testing.T) {
	specs.Describe(t, "no tenant identity grants administrative privilege", func(s *specs.Spec) {
		bg := context.Background()

		s.It("a resolver returning the zero TenantContext is rejected at spawn and erasure", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)
			engine := newSpecsEngine(ctx, "Sample", store, WithTenantResolver(zeroTenantContextResolver{}))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())

			err := engine.Entity(bg, newTenancyProbeEventSourcedBehavior(uuid.NewString()))
			ctx.Expect(err).To(specs.MatchError(ErrSpawnTenantUndetermined))
			ctx.Expect(engine.EraseEntity(bg, uuid.NewString(), true)).To(specs.MatchError(tenancy.ErrDenied))
		})

		s.It("an empty tenant id from the caller is rejected, never treated as administrative", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)
			engine := newSpecsEngine(ctx, "Sample", store, WithTenantResolver(perCallerTenantResolver{}))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())

			// perCallerTenantResolver resolves an absent tenant id to "".
			err := engine.EraseEntity(bg, uuid.NewString(), true)
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrInvalid))
		})

		s.It("a single-tenant resolver resolves to a tenant scope and its erasure never reaches another tenant", func(ctx *specs.Context) {
			resolver, err := tenancy.WithSingleTenant(tenancy.TenantID("acme"))
			ctx.Expect(err).To(specs.BeNil())
			tc, err := resolver.Resolve(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(tc.Scope()).To(specs.Equal(tenancy.ScopeTenant))
			_, isAdmin := tc.Administrative()
			ctx.Expect(isAdmin).To(specs.BeFalse())

			store := connectedEventsStore(ctx)
			persistenceID := uuid.NewString()
			eventAny, err := anypb.New(&testpb.AccountCreated{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			globex, err := persistence.NewTenantScope("globex")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(store.WriteEvents(bg, globex, []*egopb.Event{{
				PersistenceId: persistenceID, SequenceNumber: 1, Event: eventAny, Timestamp: time.Now().UnixNano(),
			}}, persistence.Unconditional())).To(specs.BeNil())

			engine := newSpecsEngine(ctx, "Sample", store, WithTenantResolver(resolver))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())
			ctx.Expect(engine.EraseEntity(bg, persistenceID, true)).To(specs.BeNil())

			latest, err := store.GetLatestEvent(bg, globex, persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.Not(specs.BeNil()))
		})

		s.It("legacy mode erasure with an administrative caller reaches only the unscoped records", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)
			adminCtx := context.WithValue(bg, administrativeScopeKey{}, true)

			persistenceID := uuid.NewString()
			eventAny, err := anypb.New(&testpb.AccountCreated{AccountId: persistenceID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			acme, err := persistence.NewTenantScope("acme")
			ctx.Expect(err).To(specs.BeNil())
			for _, scope := range []persistence.Scope{persistence.Unscoped(), acme} {
				ctx.Expect(store.WriteEvents(bg, scope, []*egopb.Event{{
					PersistenceId: persistenceID, SequenceNumber: 1, Event: eventAny, Timestamp: time.Now().UnixNano(),
				}}, persistence.Unconditional())).To(specs.BeNil())
			}

			// No resolver: the context is never consulted, so an administrative
			// caller gains nothing over any other caller.
			engine := newSpecsEngine(ctx, "Sample", store)
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())
			ctx.Expect(engine.EraseEntity(adminCtx, persistenceID, true)).To(specs.BeNil())

			unscoped, err := store.GetLatestEvent(bg, persistence.Unscoped(), persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(unscoped).To(specs.BeNil())
			tenant, err := store.GetLatestEvent(bg, acme, persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(tenant).To(specs.Not(specs.BeNil()))
		})
	})
}
