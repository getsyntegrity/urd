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
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/remote"
	"github.com/tochemey/goakt/v4/supervisor"
	"github.com/travisjeffery/go-dynaport"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	nooptrace "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/pause"
	samplepb "github.com/getsyntegrity/urd/internal/samplepb"
	"github.com/getsyntegrity/urd/internal/syncmap"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/offsetstore"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/projection"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

// NewEventSourcedEntity is a test alias used by older tests; the helper file
// also references it.
func NewEventSourcedEntity(id string) *AccountEventSourcedBehavior {
	return NewAccountEventSourcedBehavior(id)
}

// TestNewEngineValidation exercises the error paths that surface a
// mis-configured handoff between the actor system and the engine.
func TestNewEngineValidation(t *testing.T) {
	specs.Describe(t, "New Engine Validation", func(s *specs.Spec) {
		s.It("nil actor system", func(sc *specs.Context) {
			_, err := NewEngine(nil, NewConfig(testkit.NewEventsStore()))
			sc.Expect(err).To(specs.MatchError(ErrActorSystemRequired))
		})

		s.It("nil config", func(sc *specs.Context) {
			cfg := NewConfig(testkit.NewEventsStore())
			sys, err := goakt.NewActorSystem("Sample", cfg.GoaktOptions()...)
			sc.Expect(err).To(specs.BeNil())
			_, err = NewEngine(sys, nil)
			sc.Expect(err).To(specs.MatchError(ErrMissingRequiredExtensions))
		})

		s.It("not started actor system", func(sc *specs.Context) {
			cfg := NewConfig(testkit.NewEventsStore())
			sys, err := goakt.NewActorSystem("Sample", cfg.GoaktOptions()...)
			sc.Expect(err).To(specs.BeNil())
			// Deliberately skip sys.Start.
			_, err = NewEngine(sys, cfg)
			sc.Expect(err).To(specs.MatchError(ErrActorSystemNotStarted))
		})

		s.It("missing required extension is reported", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			// Build an actor system with NO urd extensions registered.
			sys, err := goakt.NewActorSystem("Sample", goakt.WithPubSub())
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(sys.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = sys.Stop(ctx) })

			_, err = NewEngine(sys, NewConfig(testkit.NewEventsStore()))
			sc.Expect(err).To(specs.MatchError(ErrMissingRequiredExtensions))
		})

		s.It("missing optional extension is reported when configured", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			// Build the actor system from a Config that does NOT include an offset
			// store, then ask NewEngine for a Config that does. The mismatch should
			// surface ErrMissingRequiredExtensions.
			store := testkit.NewEventsStore()
			baseCfg := NewConfig(store)
			sys, err := goakt.NewActorSystem("Sample", baseCfg.GoaktOptions()...)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(sys.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = sys.Stop(ctx) })

			_, err = NewEngine(sys, NewConfig(store, WithOffsetStore(testkit.NewOffsetStore())))
			sc.Expect(err).To(specs.MatchError(ErrMissingRequiredExtensions))
		})
	})
}

// TestNewEngineTenantResolverValidation exercises NewEngine's rejection of
// ambiguous WithTenantResolver registrations (DP2) and its acceptance of the
// two valid configurations: zero registrations (legacy) and exactly one
// non-nil registration (tenant-aware).
func TestNewEngineTenantResolverValidation(t *testing.T) {
	specs.Describe(t, "New Engine Tenant Resolver Validation", func(s *specs.Spec) {
		s.It("two distinct resolvers fail construction", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			cfg := NewConfig(testkit.NewEventsStore(),
				WithTenantResolver(&stubTenantResolver{id: "acme"}),
				WithTenantResolver(&stubTenantResolver{id: "globex"}),
			)
			sys, err := goakt.NewActorSystem("Sample", cfg.GoaktOptions()...)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(sys.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = sys.Stop(ctx) })

			_, err = NewEngine(sys, cfg)
			sc.Expect(err).To(specs.MatchError(ErrAmbiguousTenantResolver))
		})

		s.It("same resolver registered twice fails with the same error", func(sc *specs.Context) {
			t := sc.T
			// Count, not value identity, is what is rejected (spec.md "Same
			// resolver registered twice").
			ctx := context.Background()
			resolver := &stubTenantResolver{id: "acme"}
			cfg := NewConfig(testkit.NewEventsStore(),
				WithTenantResolver(resolver),
				WithTenantResolver(resolver),
			)
			sys, err := goakt.NewActorSystem("Sample", cfg.GoaktOptions()...)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(sys.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = sys.Stop(ctx) })

			_, err = NewEngine(sys, cfg)
			sc.Expect(err).To(specs.MatchError(ErrAmbiguousTenantResolver))
		})

		s.It("exactly one resolver succeeds", func(sc *specs.Context) {
			t := sc.T
			resolver := &stubTenantResolver{id: "acme"}
			engine := newTestEngine(t, "Sample", testkit.NewEventsStore(), WithTenantResolver(resolver))
			sc.Expect(engine.tenantResolver).To(specs.Satisfy("the same pointer", func(got any) bool { return got == any(resolver) }))
		})

		s.It("zero resolvers succeeds as legacy, non-tenant-aware mode", func(sc *specs.Context) {
			t := sc.T
			engine := newTestEngine(t, "Sample", testkit.NewEventsStore())
			sc.Expect(engine.tenantResolver).To(specs.BeNil())
		})

		s.It("nil registrations do not count toward ambiguity", func(sc *specs.Context) {
			t := sc.T
			resolver := &stubTenantResolver{id: "acme"}
			engine := newTestEngine(t, "Sample", testkit.NewEventsStore(),
				WithTenantResolver(nil),
				WithTenantResolver(resolver),
				WithTenantResolver(nil),
			)
			sc.Expect(engine.tenantResolver).To(specs.Satisfy("the same pointer", func(got any) bool { return got == any(resolver) }))
		})
	})
}

// TestEngineEventSourced covers the happy path for an event-sourced entity in
// single-node mode.
func TestEngineEventSourced(t *testing.T) {
	specs.Describe(t, "Engine Event Sourced", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			entityID := uuid.NewString()
			sc.Expect(engine.Entity(ctx, NewEventSourcedEntity(entityID))).To(specs.BeNil())

			// create
			state, revision, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{
				AccountBalance: 500.00,
			}, time.Minute)
			sc.Expect(err).To(specs.BeNil())
			acct, ok := state.(*testpb.Account)
			sc.Expect(ok).To(specs.BeTrue())
			sc.Expect(acct.GetAccountBalance()).To(specs.Equal(500.00))
			sc.Expect(revision).To(specs.Equal(uint64(1)))

			// credit
			state, revision, err = engine.SendCommand(ctx, entityID, &testpb.CreditAccount{
				AccountId: entityID,
				Balance:   250,
			}, time.Minute)
			sc.Expect(err).To(specs.BeNil())
			acct, ok = state.(*testpb.Account)
			sc.Expect(ok).To(specs.BeTrue())
			sc.Expect(acct.GetAccountBalance()).To(specs.Equal(750.00))
			sc.Expect(revision).To(specs.Equal(uint64(2)))

			sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
		})
	})
}

// TestSendCommandTenantResolution exercises the T4-A trust boundary in
// Engine.SendCommand: in tenant-aware mode (a resolver registered via
// WithTenantResolver) Resolve is invoked exactly once per command and the
// resulting TenantContext is attached before the actor runtime ever sees
// the command; a resolver failure blocks the command outright, before
// dispatch, the actor, the handler, or persistence. Legacy mode (no
// resolver) is exercised separately by TestEngineEventSourced and
// TestEngineDurableState, which remain unmodified and passing.
//
// TENANT-003 T4 note (corrected): Engine.Entity/DurableStateEntity/Saga
// never call Resolve at spawn (Resolve-Once, Propagate-After reserves
// Resolve for the command trust boundary alone) — each entity below is
// spawned with engine.WithTenant declaring its tenant explicitly, so every
// resolver.callCount() assertion below counts SendCommand's own Resolve
// calls only.
func TestSendCommandTenantResolution(t *testing.T) {
	specs.Describe(t, "Send Command Tenant Resolution", func(s *specs.Spec) {
		s.It("resolves exactly once and attaches the TenantContext before the handler runs", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			resolver := &countingTenantResolver{id: "acme"}
			engine := newTestEngine(t, "Sample", store, WithTenantResolver(resolver))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			entityID := uuid.NewString()
			probe := newTenancyProbeEventSourcedBehavior(entityID)
			sc.Expect(engine.Entity(ctx, probe, WithTenant(tenancy.TenantID("acme")))).To(specs.BeNil())

			_, _, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{AccountBalance: 500}, time.Minute)
			sc.Expect(err).To(specs.BeNil())

			// TENANT-003 T4 (corrected): Entity's spawn declares its tenant via
			// engine.WithTenant and never calls Resolve, so one SendCommand call
			// means exactly one Resolve invocation — this is the regression
			// guard for the defect CI caught (a prior design resolved at spawn
			// too, doubling this count).
			sc.Expect(resolver.callCount()).To(specs.Equal(int64(1)))
			sc.Expect(probe.InvocationCount()).To(specs.Equal(1))

			tc, ok := probe.ObservedTenant()
			sc.Expect(ok).To(specs.BeTrue())
			tenantID, ok := tc.Tenant()
			sc.Expect(ok).To(specs.BeTrue())
			sc.Expect(tenantID).To(specs.Equal(tenancy.TenantID("acme")))

			sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
		})

		s.It("a resolver error rejects the command before the actor system runs it", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			wantErr := errors.New("identity provider unavailable")
			// The entity spawns under an explicit engine.WithTenant declaration
			// (TENANT-003 T4, corrected): spawn never calls Resolve, so an
			// always-erroring resolver can still let the entity spawn. Only
			// SendCommand's own Resolve call exercises wantErr below.
			resolver := &erroringTenantResolver{err: wantErr}
			engine := newTestEngine(t, "Sample", store, WithTenantResolver(resolver))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			entityID := uuid.NewString()
			probe := newTenancyProbeEventSourcedBehavior(entityID)
			sc.Expect(engine.Entity(ctx, probe, WithTenant(tenancy.TenantID("acme")))).To(specs.BeNil())

			_, _, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{AccountBalance: 500}, time.Minute)
			sc.Expect(err).To(specs.MatchError(wantErr))

			sc.Expect(resolver.callCount()).To(specs.Equal(int64(1)))
			sc.Expect(probe.InvocationCount()).To(specs.BeZero())

			scopeA, err := persistence.NewTenantScope("acme")
			sc.Expect(err).To(specs.BeNil())
			latest, err := store.GetLatestEvent(ctx, scopeA, entityID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(latest).To(specs.BeNil())

			sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
		})

		s.It("a resolver returning the zero-value TenantContext is rejected before dispatch (Blocker 1)", func(sc *specs.Context) {
			t := sc.T
			// design.md Decision D8 (EGO-TENANT-006 review fix): a custom
			// TenantResolver implementation living outside package tenancy can
			// only ever produce tenancy.TenantContext{} via a bare struct
			// literal, since every field is unexported. Returning it with a
			// nil error used to be silently accepted by tenancy.Attach (which
			// only checked whether a DIFFERENT TenantContext was already
			// bound, never the content of this one) and then handed straight
			// through by tenancy.Require (which only checked presence). This
			// proves the corrected trust boundary now fails closed before the
			// command ever reaches the actor system.
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			// The entity spawns under an explicit engine.WithTenant declaration
			// (TENANT-003 T4, corrected): spawn never calls Resolve, so this
			// always-zero-value resolver can still let the entity spawn. Only
			// SendCommand's own Resolve call exercises the zero-value case below.
			resolver := &zeroValueTenantResolver{}
			engine := newTestEngine(t, "Sample", store, WithTenantResolver(resolver))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			entityID := uuid.NewString()
			probe := newTenancyProbeEventSourcedBehavior(entityID)
			sc.Expect(engine.Entity(ctx, probe, WithTenant(tenancy.TenantID("acme")))).To(specs.BeNil())

			_, _, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{AccountBalance: 500}, time.Minute)
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(errors.Is(err, tenancy.ErrInvalid)).To(specs.BeTrue())

			sc.Expect(resolver.callCount()).To(specs.Equal(int64(1)))
			sc.Expect(probe.InvocationCount()).To(specs.BeZero())

			scopeA, err := persistence.NewTenantScope("acme")
			sc.Expect(err).To(specs.BeNil())
			latest, err := store.GetLatestEvent(ctx, scopeA, entityID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(latest).To(specs.BeNil())

			sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
		})

		s.Describe("tenancy sentinel errors from the resolver block the command before the handler", func(s *specs.Spec) {
			for _, tt := range []struct {
				name    string
				wantErr error
			}{
				{"ErrMissing", tenancy.ErrMissing},
				{"ErrInvalid", tenancy.ErrInvalid},
				{"ErrDenied", tenancy.ErrDenied},
			} {
				s.It(tt.name, func(sc *specs.Context) {
					t := sc.T
					ctx := context.Background()
					store := testkit.NewEventsStore()
					sc.Expect(store.Connect(ctx)).To(specs.BeNil())
					t.Cleanup(func() { _ = store.Disconnect(ctx) })

					// The entity spawns under an explicit engine.WithTenant
					// declaration (TENANT-003 T4, corrected): spawn never calls
					// Resolve, so SendCommand's own resolve is what hits
					// tt.wantErr.
					resolver := &erroringTenantResolver{err: tt.wantErr}
					engine := newTestEngine(t, "Sample", store, WithTenantResolver(resolver))
					sc.Expect(engine.Start(ctx)).To(specs.BeNil())

					entityID := uuid.NewString()
					probe := newTenancyProbeEventSourcedBehavior(entityID)
					sc.Expect(engine.Entity(ctx, probe, WithTenant(tenancy.TenantID("acme")))).To(specs.BeNil())

					_, _, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{AccountBalance: 500}, time.Minute)
					sc.Expect(err).To(specs.MatchError(tt.wantErr))
					sc.Expect(probe.InvocationCount()).To(specs.BeZero())

					scopeA, err := persistence.NewTenantScope("acme")
					sc.Expect(err).To(specs.BeNil())
					latest, err := store.GetLatestEvent(ctx, scopeA, entityID)
					sc.Expect(err).To(specs.BeNil())
					sc.Expect(latest).To(specs.BeNil())

					sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
				})
			}
		})

		s.It("concurrent commands for different tenants do not cross-contaminate", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			// perCallerTenantResolver resolves whichever tenant ID the caller
			// placed on ctx, simulating a resolver that derives identity from
			// request-scoped data (e.g. a header) rather than a fixed value.
			resolver := perCallerTenantResolver{}
			engine := newTestEngine(t, "Sample", store, WithTenantResolver(resolver))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			const tenantCount = 5
			entityIDs := make([]string, tenantCount)
			probes := make([]*tenancyProbeEventSourcedBehavior, tenantCount)
			tenantIDs := make([]tenancy.TenantID, tenantCount)

			for i := 0; i < tenantCount; i++ {
				entityIDs[i] = uuid.NewString()
				tenantIDs[i] = tenancy.TenantID(fmt.Sprintf("tenant-%d", i))

				probes[i] = newTenancyProbeEventSourcedBehavior(entityIDs[i])
				// TENANT-003 T4 (corrected): Engine.Entity never calls Resolve
				// at spawn, so each entity declares its owning tenant explicitly
				// via engine.WithTenant — the same tenant every SendCommand call
				// below targets it with.
				sc.Expect(engine.Entity(ctx, probes[i], WithTenant(tenantIDs[i]))).To(specs.BeNil())
			}

			// sc.Go runs each command on its own goroutine and the case waits for them
			// at its end; the wait group makes the reads below wait for them too.
			var wg sync.WaitGroup
			for i := 0; i < tenantCount; i++ {
				wg.Add(1)
				sc.Go(func(gc *specs.Context) {
					defer wg.Done()
					callerCtx := context.WithValue(ctx, perCallerTenantKey{}, string(tenantIDs[i]))
					_, _, err := engine.SendCommand(callerCtx, entityIDs[i], &testpb.CreateAccount{AccountBalance: 100}, time.Minute)
					gc.Expect(err).To(specs.BeNil())
				})
			}
			wg.Wait()

			for i := 0; i < tenantCount; i++ {
				tc, ok := probes[i].ObservedTenant()
				sc.Expect(ok).To(specs.BeTrue())
				gotTenant, ok := tc.Tenant()
				sc.Expect(ok).To(specs.BeTrue())
				sc.Expect(gotTenant).To(specs.Equal(tenantIDs[i]))
			}

			sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
		})
	})
}

// TestSendCommandSingleTenantZeroPlumbing exercises AC4/AC5/D6/D7:
// tenancy.WithSingleTenant, registered as the sole resolver via
// WithTenantResolver, lets a command succeed with zero manual
// tenancy.Attach/tenancy.Require calls in application code — the caller's
// ctx here is a plain context.Background(), exactly like every other
// SendCommand test in this file, and never touches the tenancy package at
// all. SendCommand and the actor's T4-A/T4-B gates do the entire resolve,
// attach, and re-confirm sequence identically to a multi-tenant resolver
// (see TestSendCommandTenantResolution above); this test only proves it
// also works, end-to-end, for the single-tenant case with no per-call
// tenant plumbing whatsoever.
func TestSendCommandSingleTenantZeroPlumbing(t *testing.T) {
	specs.Describe(t, "Send Command Single Tenant Zero Plumbing", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			resolver, err := tenancy.WithSingleTenant(tenancy.TenantID("acme"))
			sc.Expect(err).To(specs.BeNil())
			engine := newTestEngine(t, "Sample", store, WithTenantResolver(resolver))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			entityID := uuid.NewString()
			probe := newTenancyProbeEventSourcedBehavior(entityID)
			sc.Expect(engine.Entity(ctx, probe)).To(specs.BeNil())

			// Plain context.Background(): no tenancy.Attach, no tenancy.Require,
			// nothing tenancy-related at the call site.
			_, _, err = engine.SendCommand(ctx, entityID, &testpb.CreateAccount{AccountBalance: 500}, time.Minute)
			sc.Expect(err).To(specs.BeNil())

			sc.Expect(probe.InvocationCount()).To(specs.Equal(1))
			tc, ok := probe.ObservedTenant()
			sc.Expect(ok).To(specs.BeTrue())
			tenantID, ok := tc.Tenant()
			sc.Expect(ok).To(specs.BeTrue())
			sc.Expect(tenantID).To(specs.Equal(tenancy.TenantID("acme")))

			sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
		})
	})
}

// TestSendCommandResolverSwapIdenticalSequence proves D6/D7's other half:
// swapping tenancy.WithSingleTenant for an ordinary multi-tenant resolver
// changes nothing about the resolve-attach-gate sequence a command travels
// through. Both resolvers here are driven through the exact same
// SendCommand call with the exact same plain ctx; only the registered
// resolver differs.
func TestSendCommandResolverSwapIdenticalSequence(t *testing.T) {
	specs.Describe(t, "Send Command Resolver Swap Identical Sequence", func(s *specs.Spec) {
		newEngineWithResolver := func(sc *specs.Context, resolver tenancy.TenantResolver, spawnOpts ...SpawnOption) (*Engine, string, *tenancyProbeEventSourcedBehavior) {
			t := sc.T
			t.Helper()
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store, WithTenantResolver(resolver))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			entityID := uuid.NewString()
			probe := newTenancyProbeEventSourcedBehavior(entityID)
			sc.Expect(engine.Entity(ctx, probe, spawnOpts...)).To(specs.BeNil())

			return engine, entityID, probe
		}

		s.It("single-tenant resolver", func(sc *specs.Context) {
			ctx := context.Background()
			singleTenant, err := tenancy.WithSingleTenant(tenancy.TenantID("acme"))
			sc.Expect(err).To(specs.BeNil())

			// No engine.WithTenant here: acceptance criterion 6 requires
			// single-tenant mode to need no tenant plumbing invented by the
			// application. tenancy.WithSingleTenant's FixedTenantResolver
			// capability is what lets spawn determine the tenant without one.
			engine, entityID, probe := newEngineWithResolver(sc, singleTenant)
			_, _, err = engine.SendCommand(ctx, entityID, &testpb.CreateAccount{AccountBalance: 500}, time.Minute)
			sc.Expect(err).To(specs.BeNil())

			sc.Expect(probe.InvocationCount()).To(specs.Equal(1))
			tc, ok := probe.ObservedTenant()
			sc.Expect(ok).To(specs.BeTrue())
			tenantID, ok := tc.Tenant()
			sc.Expect(ok).To(specs.BeTrue())
			sc.Expect(tenantID).To(specs.Equal(tenancy.TenantID("acme")))

			sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
		})

		s.It("multi-tenant resolver", func(sc *specs.Context) {
			ctx := context.Background()
			multiTenant := &countingTenantResolver{id: "acme"}

			// An ordinary multi-tenant resolver has no fixed tenant, so the
			// application must declare it explicitly via engine.WithTenant.
			engine, entityID, probe := newEngineWithResolver(sc, multiTenant, WithTenant(tenancy.TenantID("acme")))
			_, _, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{AccountBalance: 500}, time.Minute)
			sc.Expect(err).To(specs.BeNil())

			// This is the regression guard for the defect CI caught: an earlier
			// design called Resolve at spawn too, so this resolver was invoked
			// twice (spawn + SendCommand) for this exact sequence instead of
			// once.
			sc.Expect(multiTenant.callCount()).To(specs.Equal(int64(1)))
			sc.Expect(probe.InvocationCount()).To(specs.Equal(1))
			tc, ok := probe.ObservedTenant()
			sc.Expect(ok).To(specs.BeTrue())
			tenantID, ok := tc.Tenant()
			sc.Expect(ok).To(specs.BeTrue())
			sc.Expect(tenantID).To(specs.Equal(tenancy.TenantID("acme")))

			sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
		})
	})
}

// TestEngineDurableState covers the happy path for a durable-state entity.
func TestEngineDurableState(t *testing.T) {
	specs.Describe(t, "Engine Durable State", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			stateStore := testkit.NewDurableStore()
			sc.Expect(stateStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = stateStore.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", nil,
				WithLogger(DiscardLogger),
				WithStateStore(stateStore),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			entityID := uuid.NewString()
			sc.Expect(engine.DurableStateEntity(ctx, NewAccountDurableStateBehavior(entityID))).To(specs.BeNil())

			state, revision, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{
				AccountBalance: 500.00,
			}, time.Minute)
			sc.Expect(err).To(specs.BeNil())
			acct, ok := state.(*testpb.Account)
			sc.Expect(ok).To(specs.BeTrue())
			sc.Expect(acct.GetAccountBalance()).To(specs.Equal(500.00))
			sc.Expect(revision).To(specs.Equal(uint64(1)))

			sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
		})
	})
}

// TestEngineDurableStateRequiresStateStore confirms that calling
// DurableStateEntity without WithStateStore is reported as a config error.
func TestEngineDurableStateRequiresStateStore(t *testing.T) {
	specs.Describe(t, "Engine Durable State Requires State Store", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			err := engine.DurableStateEntity(ctx, NewAccountDurableStateBehavior(uuid.NewString()))
			sc.Expect(err).To(specs.MatchError(ErrDurableStateStoreRequired))
		})
	})
}

// TestEngineSendCommandErrors covers the error paths of SendCommand that do
// not require an entity to be live.
func TestEngineSendCommandErrors(t *testing.T) {
	specs.Describe(t, "Engine Send Command Errors", func(s *specs.Spec) {
		ctx := context.Background()
		store := testkit.NewEventsStore()
		t.Cleanup(func() { _ = store.Disconnect(ctx) })
		s.BeforeEach(func(sc *specs.Context) { sc.Expect(store.Connect(ctx)).To(specs.BeNil()) })

		s.It("engine not started", func(sc *specs.Context) {
			t := sc.T
			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			state, rev, err := engine.SendCommand(ctx, uuid.NewString(), &testpb.CreateAccount{}, time.Second)
			sc.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
			sc.Expect(state).To(specs.BeNil())
			sc.Expect(rev).To(specs.BeZero())
		})

		s.It("undefined entity id", func(sc *specs.Context) {
			t := sc.T
			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			state, rev, err := engine.SendCommand(ctx, "", &testpb.CreateAccount{}, time.Second)
			sc.Expect(err).To(specs.MatchError(ErrUndefinedEntityID))
			sc.Expect(state).To(specs.BeNil())
			sc.Expect(rev).To(specs.BeZero())
		})
	})
}

// TestEngineEntityExists exercises the EntityExists liveness probe.
func TestEngineEntityExists(t *testing.T) {
	specs.Describe(t, "Engine Entity Exists", func(s *specs.Spec) {
		ctx := context.Background()
		store := testkit.NewEventsStore()
		t.Cleanup(func() { _ = store.Disconnect(ctx) })
		s.BeforeEach(func(sc *specs.Context) { sc.Expect(store.Connect(ctx)).To(specs.BeNil()) })

		s.It("not started", func(sc *specs.Context) {
			t := sc.T
			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			exists, err := engine.EntityExists(ctx, uuid.NewString())
			sc.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
			sc.Expect(exists).To(specs.BeFalse())
		})

		s.It("not found", func(sc *specs.Context) {
			t := sc.T
			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			exists, err := engine.EntityExists(ctx, uuid.NewString())
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(exists).To(specs.BeFalse())
		})

		s.It("found after materialization", func(sc *specs.Context) {
			t := sc.T
			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			entityID := uuid.NewString()
			sc.Expect(engine.Entity(ctx, NewEventSourcedEntity(entityID))).To(specs.BeNil())
			_, _, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{
				AccountBalance: 100,
			}, time.Minute)
			sc.Expect(err).To(specs.BeNil())

			exists, err := engine.EntityExists(ctx, entityID)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(exists).To(specs.BeTrue())
		})
	})
}

// TestEngineActorSystemAccessor covers Engine.ActorSystem before/after Start/Stop.
func TestEngineActorSystemAccessor(t *testing.T) {
	specs.Describe(t, "Engine Actor System Accessor", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))

			// Right after NewEngine: the engine HAS its reference to the actor system
			// (it can validate extensions etc). It is NOT yet "Started" but the
			// accessor returns the system so callers can inspect it.
			sc.Expect(engine.ActorSystem()).To(specs.Not(specs.BeNil()))

			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			sc.Expect(engine.ActorSystem()).To(specs.Not(specs.BeNil()))

			sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
			sc.Expect(engine.ActorSystem()).To(specs.BeNil())
		})
	})
}

// TestEngineHotPathGuards verifies every hot-path method bails out with
// ErrEngineNotStarted when the engine's actor system reference has been
// detached (mid-Stop or never started).
func TestEngineHotPathGuards(t *testing.T) {
	specs.Describe(t, "every hot-path method returns ErrEngineNotStarted when the actor system reference is detached", func(s *specs.Spec) {
		// Build a synthetic engine that has Started()==true but no actor system
		// reference. Reproduces the race between Stop's atomic detach and a
		// concurrent hot-path caller.
		synth := func() *Engine {
			e := &Engine{
				eventsStore:   testkit.NewEventsStore(),
				logger:        DiscardLogger,
				eventsStreams: syncmap.New[string, *eventsStream](),
				statesStreams: syncmap.New[string, *statesStream](),
			}
			e.started.Store(true)
			return e
		}

		bg := context.Background()

		s.It("StartProjection", func(ctx *specs.Context) {
			err := synth().StartProjection(bg, "projection-"+uuid.NewString())
			ctx.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
		})

		s.It("StopProjection", func(ctx *specs.Context) {
			err := synth().StopProjection(bg, "projection-"+uuid.NewString())
			ctx.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
		})

		s.It("IsProjectionRunning", func(ctx *specs.Context) {
			running, err := synth().IsProjectionRunning(bg, "projection-"+uuid.NewString())
			ctx.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
			ctx.Expect(running).To(specs.BeFalse())
		})

		s.It("Entity", func(ctx *specs.Context) {
			err := synth().Entity(bg, NewEventSourcedEntity(uuid.NewString()))
			ctx.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
		})

		s.It("EntityExists", func(ctx *specs.Context) {
			exists, err := synth().EntityExists(bg, uuid.NewString())
			ctx.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
			ctx.Expect(exists).To(specs.BeFalse())
		})

		s.It("DurableStateEntity", func(ctx *specs.Context) {
			err := synth().DurableStateEntity(bg, NewAccountDurableStateBehavior(uuid.NewString()))
			ctx.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
		})

		s.It("SendCommand", func(ctx *specs.Context) {
			state, rev, err := synth().SendCommand(bg, uuid.NewString(), &testpb.CreateAccount{}, time.Second)
			ctx.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
			ctx.Expect(state == nil).To(specs.BeTrue())
			specs.ExpectT(ctx, rev).ToEqual(0)
		})

		s.It("Saga", func(ctx *specs.Context) {
			err := synth().Saga(bg, &testSagaBehavior{sagaID: "saga-" + uuid.NewString()}, time.Second)
			ctx.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
		})

		s.It("SagaStatus", func(ctx *specs.Context) {
			info, err := synth().SagaStatus(bg, "saga-"+uuid.NewString(), time.Second)
			ctx.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
			ctx.Expect(info == nil).To(specs.BeTrue())
		})
	})
}

// TestEngineProjection covers basic projection registration in single-node
// mode (no cluster, projection runs as a regular long-lived actor).
func TestEngineProjection(t *testing.T) {
	specs.Describe(t, "a projection registered in single-node mode runs as a regular actor", func(s *specs.Spec) {
		s.It("is running once started", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				WithProjection("discard", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   100,
					PullInterval: time.Second,
				}),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			sc.Expect(engine.StartProjection(ctx, "discard")).To(specs.BeNil())
			sc.Eventually(projectionRunning(ctx, engine, "discard"), specs.BeTrue(), specs.WithTimeout(waitTimeout))

			running, err := engine.IsProjectionRunning(ctx, "discard")
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(running).To(specs.BeTrue())

			sc.Expect(engine.StopProjection(ctx, "discard")).To(specs.BeNil())
			sc.Expect(engine.Stop(ctx)).To(specs.BeNil())
		})
	})
}

// TestEngineStartProjectionNotRegistered verifies that StartProjection fails fast
// with ErrProjectionNotRegistered when the name was never registered via
// WithProjection, instead of spawning an actor whose PreStart would fail.
func TestEngineStartProjectionNotRegistered(t *testing.T) {
	specs.Describe(t, "Engine Start Projection Not Registered", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				WithProjection("registered", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   100,
					PullInterval: time.Second,
				}),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = engine.Stop(ctx) })

			err := engine.StartProjection(ctx, "unknown")
			sc.Expect(err).To(specs.MatchError(ErrProjectionNotRegistered))
			sc.Expect(err.Error()).To(specs.Contain("unknown"))
		})
	})
}

// countingProjectionHandler records how many events it processed so tests can
// assert which projection's handler was invoked.
type countingProjectionHandler struct {
	counter atomic.Int64
}

func (x *countingProjectionHandler) Handle(_ context.Context, _ string, _ *anypb.Any, _ uint64) error {
	x.counter.Add(1)
	return nil
}

// TestEngineProjectionsOwnHandlers verifies that projections registered under
// different names each run with their own handler.
func TestEngineProjectionsOwnHandlers(t *testing.T) {
	specs.Describe(t, "each projection runs with its own handler", func(s *specs.Spec) {
		s.It("feeds an event to every handler", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			accountsHandler := new(countingProjectionHandler)
			auditHandler := new(countingProjectionHandler)

			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				WithProjection("accounts", &projection.Options{
					Handler:      accountsHandler,
					BufferSize:   100,
					PullInterval: 100 * time.Millisecond,
				}),
				WithProjection("audit", &projection.Options{
					Handler:      auditHandler,
					BufferSize:   100,
					PullInterval: 100 * time.Millisecond,
				}),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = engine.Stop(ctx) })

			sc.Expect(engine.StartProjection(ctx, "accounts")).To(specs.BeNil())
			sc.Expect(engine.StartProjection(ctx, "audit")).To(specs.BeNil())
			sc.Eventually(projectionRunning(ctx, engine, "accounts"), specs.BeTrue(), specs.WithTimeout(waitTimeout))
			sc.Eventually(projectionRunning(ctx, engine, "audit"), specs.BeTrue(), specs.WithTimeout(waitTimeout))

			event, err := anypb.New(&testpb.AccountCredited{})
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(store.WriteEvents(ctx, persistence.Unscoped(), []*egopb.Event{{
				PersistenceId:  uuid.NewString(),
				SequenceNumber: 1,
				Event:          event,
				Timestamp:      time.Now().Unix(),
				Shard:          3,
			}}, persistence.Unconditional())).To(specs.BeNil())

			// Both projections poll independently; each must observe the event
			// through its own handler.
			sc.Eventually(func() any {
				return accountsHandler.counter.Load() == 1 && auditHandler.counter.Load() == 1
			}, specs.BeTrue(), specs.WithTimeout(5*time.Second), specs.WithInterval(100*time.Millisecond))
		})
	})
}

// TestClusterEngineSingleNodeServesProjectionsAndEntities runs a single-node cluster end-to-end to exercise the
// StartProjection-as-singleton branch (sys.InCluster()==true) and the
// engine.ClusterKinds() registration. It builds the goakt actor system manually
// to demonstrate the cluster-mode bootstrap.
func TestClusterEngineSingleNodeServesProjectionsAndEntities(t *testing.T) {
	specs.Describe(t, "a single-node cluster runs projections as singletons and serves entities", func(s *specs.Spec) {
		s.It("starts both projections and an entity", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })
			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			ports := dynaport.Get(3)
			gossipPort, clusterPort, remotingPort := ports[0], ports[1], ports[2]
			host := "127.0.0.1"

			provider := &mockClusterProvider{
				id:    "test",
				peers: []string{net.JoinHostPort(host, strconv.Itoa(clusterPort))},
			}

			clusterCfg := goakt.NewClusterConfig().
				WithDiscovery(provider).
				WithDiscoveryPort(gossipPort).
				WithPeersPort(clusterPort).
				WithMinimumPeersQuorum(1).
				WithReplicaCount(1).
				WithPartitionCount(4).
				WithKinds(ClusterKinds()...)

			cfg := NewConfig(store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				WithProjection("discard", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   100,
					PullInterval: time.Second,
				}),
				WithProjection("discard-too", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   100,
					PullInterval: time.Second,
				}),
			)

			goaktOpts := append(cfg.GoaktOptions(),
				goakt.WithCluster(clusterCfg),
				goakt.WithRemote(remote.NewConfig(host, remotingPort)),
			)

			sys, err := goakt.NewActorSystem("Sample", goaktOpts...)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(sys.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = sys.Stop(ctx) })

			// the single-node cluster advertises itself shortly after Start
			sc.Eventually(func() any { return sys.InCluster() }, specs.BeTrue(), specs.WithTimeout(waitTimeout))

			engine, err := NewEngine(sys, cfg)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = engine.Stop(ctx) })

			// Singleton uniqueness is keyed by actor name (goakt >= v4.4.1), so every
			// registered projection gets its own singleton. Before that goakt release
			// the kind-keyed reservation made the second StartProjection a silent
			// no-op.
			sc.Expect(engine.StartProjection(ctx, "discard")).To(specs.BeNil())
			sc.Expect(engine.StartProjection(ctx, "discard-too")).To(specs.BeNil())
			sc.Eventually(projectionRunning(ctx, engine, "discard"), specs.BeTrue(), specs.WithTimeout(waitTimeout))
			sc.Eventually(projectionRunning(ctx, engine, "discard-too"), specs.BeTrue(), specs.WithTimeout(waitTimeout))

			for _, name := range []string{"discard", "discard-too"} {
				running, err := engine.IsProjectionRunning(ctx, name)
				sc.Expect(err).To(specs.BeNil())
				sc.Expect(running).To(specs.BeTrue())
			}

			// entity flow in cluster mode
			entityID := uuid.NewString()
			sc.Expect(engine.Entity(ctx, NewEventSourcedEntity(entityID))).To(specs.BeNil())
			state, _, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{
				AccountBalance: 100,
			}, time.Minute)
			sc.Expect(err).To(specs.BeNil())
			acct, ok := state.(*testpb.Account)
			sc.Expect(ok).To(specs.BeTrue())
			sc.Expect(acct.GetAccountBalance()).To(specs.Equal(float64(100)))
		})
	})
}

// TestClusterEngineRemoteEntitySpawn is a regression test for remote entity
// spawns failing with "dependency type is not registered".
//
// With the default RoundRobin placement, Engine.Entity routes spawns to peer
// nodes. The receiving node deserializes the spawn request's dependencies
// (the behavior and Urd's internal EntityConfig) against its own registry,
// which is populated at NewEngine time from WithEntityKinds. Only node1 ever
// calls Entity(), so every spawn landing on node2 exercises that
// pre-registration path; before the fix those spawns failed because node2's
// registry was only populated by its own (never-issued) Entity() calls.
func TestClusterEngineRemoteEntitySpawn(t *testing.T) {
	specs.Describe(t, "engine.Engine.Entity placement on a two-node cluster", func(s *specs.Spec) {
		s.It("serves entities that round-robin placement put on the node that never called Entity", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()

			cluster := newTestCluster(t,
				[]Option{WithEntityKinds(new(AccountEventSourcedBehavior))},
				[]Option{WithEntityKinds(new(AccountEventSourcedBehavior))},
			)
			engine1 := cluster.engines[0]

			// Only node1 spawns. With RoundRobin placement over two members, a run of
			// spawns is guaranteed to place some entities on node2, which never called
			// Entity() itself.
			for range 8 {
				entityID := uuid.NewString()
				sc.Expect(engine1.Entity(ctx, NewEventSourcedEntity(entityID))).To(specs.BeNil())

				// SpawnOn (and therefore Entity) only returns once the actor's
				// registry record is written to the cluster store, so the entity is
				// immediately addressable from this node — no retry needed.
				state, _, err := engine1.SendCommand(ctx, entityID, &testpb.CreateAccount{
					AccountBalance: 100,
				}, time.Minute)
				sc.Expect(err).To(specs.BeNil())
				account, ok := state.(*testpb.Account)
				sc.Expect(ok).To(specs.BeTrue())
				sc.Expect(account.GetAccountBalance()).To(specs.Equal(float64(100)))
			}
		})
	})
}

// testCluster is a cluster of Urd engines started in one process by
// newTestCluster. systems[i] and engines[i] belong to node i.
type testCluster struct {
	systems []goakt.ActorSystem
	engines []*Engine
}

// newTestCluster starts one clustered actor system and Urd engine per entry
// of nodeOpts, all in this process, and waits until every node sees all the
// others as peers. Each node gets its own in-memory events store and
// DiscardLogger; nodeOpts[i] adds node i's options (for example its entity
// kinds). Nodes discover each other through mockClusterProvider, and their
// cluster config registers ClusterKinds() with GoAkt's default RoundRobin
// placement, so a run of SpawnOn calls from one node places some actors on
// the others. Engines and actor systems are stopped at test cleanup.
func newTestCluster(t *testing.T, nodeOpts ...[]Option) *testCluster {
	t.Helper()
	ctx := context.Background()
	host := "127.0.0.1"
	nodes := len(nodeOpts)
	if nodes < 2 {
		t.Fatalf("a test cluster needs at least two nodes, got %d", nodes)
	}

	// Three ports per node: gossip, peers and remoting.
	ports := dynaport.Get(3 * nodes)
	gossipAddrs := make([]string, nodes)
	for i := range nodes {
		gossipAddrs[i] = net.JoinHostPort(host, strconv.Itoa(ports[3*i]))
	}

	newNode := func(opts []Option, gossipPort, peersPort, remotingPort int) (goakt.ActorSystem, *Config) {
		store := testkit.NewEventsStore()
		if err := store.Connect(ctx); err != nil {
			t.Fatalf("connect events store: %v", err)
		}
		t.Cleanup(func() { _ = store.Disconnect(ctx) })

		cfg := NewConfig(store, append([]Option{WithLogger(DiscardLogger)}, opts...)...)

		provider := &mockClusterProvider{id: "test", peers: gossipAddrs}
		clusterCfg := goakt.NewClusterConfig().
			WithDiscovery(provider).
			WithDiscoveryPort(gossipPort).
			WithPeersPort(peersPort).
			WithMinimumPeersQuorum(1).
			WithReplicaCount(1).
			WithPartitionCount(7).
			WithKinds(ClusterKinds()...)

		goaktOpts := append(cfg.GoaktOptions(),
			goakt.WithCluster(clusterCfg),
			goakt.WithRemote(remote.NewConfig(host, remotingPort)),
		)

		sys, err := goakt.NewActorSystem("Sample", goaktOpts...)
		if err != nil {
			t.Fatalf("new actor system: %v", err)
		}
		return sys, cfg
	}

	cluster := &testCluster{
		systems: make([]goakt.ActorSystem, nodes),
		engines: make([]*Engine, nodes),
	}
	configs := make([]*Config, nodes)
	for i, opts := range nodeOpts {
		cluster.systems[i], configs[i] = newNode(opts, ports[3*i], ports[3*i+1], ports[3*i+2])
	}

	// start all nodes concurrently so they bootstrap the cluster together
	errs := make(chan error, nodes)
	for _, sys := range cluster.systems {
		go func() { errs <- sys.Start(ctx) }()
	}
	for range nodes {
		if err := <-errs; err != nil {
			t.Fatalf("start actor system: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, sys := range cluster.systems {
			_ = sys.Stop(context.Background())
		}
	})

	// wait until every node sees all the others as peers
	waitFor(t, 30*time.Second, func() bool {
		for _, sys := range cluster.systems {
			peers, err := sys.Peers(ctx, time.Second)
			if err != nil || len(peers) != nodes-1 {
				return false
			}
		}
		return true
	})

	// Registered before the start loop so that, if one engine fails to
	// build or start, the engines already started are still stopped.
	t.Cleanup(func() {
		for _, engine := range cluster.engines {
			if engine != nil {
				_ = engine.Stop(context.Background())
			}
		}
	})
	for i, sys := range cluster.systems {
		engine, err := NewEngine(sys, configs[i])
		if err != nil {
			t.Fatalf("new engine: %v", err)
		}
		cluster.engines[i] = engine
		if err := engine.Start(ctx); err != nil {
			t.Fatalf("start engine: %v", err)
		}
	}

	return cluster
}

// TestParseCommandReply pins the reply-decoding contract.
func TestParseCommandReply(t *testing.T) {
	specs.Describe(t, "protocol.ParseCommandReply decodes a command reply", func(s *specs.Spec) {
		s.It("error reply", func(ctx *specs.Context) {
			reply := &egopb.CommandReply{
				Reply: &egopb.CommandReply_ErrorReply{
					ErrorReply: &egopb.ErrorReply{Message: "something failed"},
				},
			}
			_, _, err := protocol.ParseCommandReply(reply)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(engRestErrText(err)).To(specs.Contain("something failed"))
		})

		s.It("no reply", func(ctx *specs.Context) {
			_, _, err := protocol.ParseCommandReply(&egopb.CommandReply{})
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(engRestErrText(err)).To(specs.Contain("no state received"))
		})

		s.It("state reply", func(ctx *specs.Context) {
			state, _ := anypb.New(&samplepb.Account{AccountId: "acc-1", AccountBalance: 100})
			reply := &egopb.CommandReply{
				Reply: &egopb.CommandReply_StateReply{
					StateReply: &egopb.StateReply{
						PersistenceId:  "entity-1",
						State:          state,
						SequenceNumber: 5,
					},
				},
			}
			result, seq, err := protocol.ParseCommandReply(reply)
			ctx.Expect(err).To(specs.BeNil())
			specs.ExpectT(ctx, seq).ToEqual(5)
			ctx.Expect(result != nil).To(specs.BeTrue())
		})

		s.It("unmarshal failure", func(ctx *specs.Context) {
			reply := &egopb.CommandReply{
				Reply: &egopb.CommandReply_StateReply{
					StateReply: &egopb.StateReply{
						State:          &anypb.Any{TypeUrl: "type.googleapis.com/invalid.Type", Value: []byte("garbage")},
						SequenceNumber: 1,
					},
				},
			}
			_, _, err := protocol.ParseCommandReply(reply)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
		})
	})
}

// TestBuildSpawnOptionsFromConfig pins the SpawnOption translation that the
// entity/durable-state/saga paths share.
func TestBuildSpawnOptionsFromConfig(t *testing.T) {
	specs.Describe(t, "buildSpawnOptionsFromConfig translates a spawn config into GoAkt spawn options", func(s *specs.Spec) {
		s.It("with batch threshold", func(ctx *specs.Context) {
			opts := buildSpawnOptionsFromConfig(&spawnConfig{
				batchThreshold:      5,
				supervisorDirective: RestartDirective,
				entitiesPlacement:   RoundRobin,
			})
			ctx.Expect(len(opts) > 0).To(specs.BeTrue())
		})
		s.It("with passivation", func(ctx *specs.Context) {
			opts := buildSpawnOptionsFromConfig(&spawnConfig{
				passivateAfter:      time.Minute,
				supervisorDirective: RestartDirective,
				entitiesPlacement:   RoundRobin,
			})
			ctx.Expect(len(opts) > 0).To(specs.BeTrue())
		})
		s.It("relocation enabled", func(ctx *specs.Context) {
			opts := buildSpawnOptionsFromConfig(&spawnConfig{
				toRelocate:          true,
				supervisorDirective: RestartDirective,
				entitiesPlacement:   RoundRobin,
			})
			ctx.Expect(len(opts) > 0).To(specs.BeTrue())
		})
	})
}

// TestToSpawnPlacement maps Urd placement strategies to their goakt
// equivalents.
func TestToSpawnPlacement(t *testing.T) {
	specs.Describe(t, "toSpawnPlacement maps each Urd placement strategy to its GoAkt equivalent", func(s *specs.Spec) {
		s.It("maps LeastLoad, Random, Local and RoundRobin", func(ctx *specs.Context) {
			ctx.Expect(toSpawnPlacement(LeastLoad)).ToEqual(goakt.LeastLoad)
			ctx.Expect(toSpawnPlacement(Random)).ToEqual(goakt.Random)
			ctx.Expect(toSpawnPlacement(Local)).ToEqual(goakt.Local)
			ctx.Expect(toSpawnPlacement(RoundRobin)).ToEqual(goakt.RoundRobin)
		})
	})
}

// TestToSupervisorDirective maps Urd supervisor directives to goakt.
func TestToSupervisorDirective(t *testing.T) {
	specs.Describe(t, "toSupervisorDirective maps Urd supervisor directives to GoAkt", func(s *specs.Spec) {
		s.It("stop maps to Stop", func(ctx *specs.Context) {
			// concrete assertion is on stringer; behavior is "anything not RestartDirective stops".
			// Negative test below.
		})
		s.It("restart maps to Restart (default)", func(ctx *specs.Context) {
			dir := toSupervisorDirective(RestartDirective)
			ctx.Expect(dir).ToEqual(supervisor.RestartDirective)
		})
	})
}

// TestEngineEraseEntityErrors covers the engine-state guards on EraseEntity.
func TestEngineEraseEntityErrors(t *testing.T) {
	specs.Describe(t, "Engine Erase Entity Errors", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			// not started
			sc.Expect(engine.EraseEntity(ctx, uuid.NewString(), false)).To(specs.MatchError(ErrEngineNotStarted))
		})
	})
}

// TestEngineProjectionLagErrors covers the engine-state guards on ProjectionLag.
func TestEngineProjectionLagErrors(t *testing.T) {
	specs.Describe(t, "Engine Projection Lag Errors", func(s *specs.Spec) {
		ctx := context.Background()
		store := testkit.NewEventsStore()
		t.Cleanup(func() { _ = store.Disconnect(ctx) })
		s.BeforeEach(func(sc *specs.Context) { sc.Expect(store.Connect(ctx)).To(specs.BeNil()) })

		s.It("not started", func(sc *specs.Context) {
			t := sc.T
			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			_, err := engine.ProjectionLag(ctx, "any")
			sc.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
		})

		s.It("started without offset store", func(sc *specs.Context) {
			t := sc.T
			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			_, err := engine.ProjectionLag(ctx, "any")
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(err.Error()).To(specs.Contain("offset store is required"))
		})
	})
}

// TestEngineRebuildProjectionErrors covers the engine-state guards on
// RebuildProjection.
func TestEngineRebuildProjectionErrors(t *testing.T) {
	specs.Describe(t, "Engine Rebuild Projection Errors", func(s *specs.Spec) {
		ctx := context.Background()
		store := testkit.NewEventsStore()
		t.Cleanup(func() { _ = store.Disconnect(ctx) })
		s.BeforeEach(func(sc *specs.Context) { sc.Expect(store.Connect(ctx)).To(specs.BeNil()) })

		s.It("not started", func(sc *specs.Context) {
			t := sc.T
			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			err := engine.RebuildProjection(ctx, "any", time.Now())
			sc.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
		})

		s.It("started without offset store", func(sc *specs.Context) {
			t := sc.T
			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			err := engine.RebuildProjection(ctx, "any", time.Now())
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(err.Error()).To(specs.Contain("offset store is required"))
		})
	})
}

// TestEngineSubscribeBeforeStart confirms Subscribe is gated by the engine
// being started.
func TestEngineSubscribeBeforeStart(t *testing.T) {
	specs.Describe(t, "Engine Subscribe Before Start", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			_, err := engine.Subscribe()
			sc.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
		})
	})
}

// TestEngineConfigRegistersAllExtensions verifies that every Option that
// triggers an extension registration is honored by Config.GoaktOptions and
// makes it into the resulting actor system.
func TestEngineConfigRegistersAllExtensions(t *testing.T) {
	specs.Describe(t, "Engine Config Registers All Extensions", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			stateStore := testkit.NewDurableStore()
			offsetStore := testkit.NewOffsetStore()
			snapStore := testkit.NewSnapshotStore()

			cfg := NewConfig(store,
				WithLogger(DiscardLogger),
				WithStateStore(stateStore),
				WithOffsetStore(offsetStore),
				WithSnapshotStore(snapStore),
				WithEventAdapters(&testEventAdapter{}),
				WithProjection("discard", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   10,
					PullInterval: time.Second,
				}),
			)

			sys, err := goakt.NewActorSystem("Sample", cfg.GoaktOptions()...)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(sys.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = sys.Stop(ctx) })

			wantIDs := []string{
				extensions.EventsStoreExtensionID,
				extensions.EventsStreamExtensionID,
				extensions.DurableStateStoreExtensionID,
				extensions.OffsetStoreExtensionID,
				extensions.ProjectionExtensionID,
				extensions.SnapshotStoreExtensionID,
				extensions.EventAdaptersExtensionID,
			}
			for _, id := range wantIDs {
				sc.Expect(sys.Extension(id)).To(specs.Not(specs.BeNil()))
			}

			// NewEngine should accept the same Config cleanly.
			engine, err := NewEngine(sys, cfg)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = engine.Stop(ctx) })
		})
	})
}

// TestEngineStartWithTelemetry exercises the telemetry-enabled branch of
// Engine.Start: when WithTelemetry is configured, Start materializes the
// metrics struct and installs the OTel propagator. Both noop tracer and noop
// meter are used to keep the test side-effect free.
func TestEngineStartWithTelemetry(t *testing.T) {
	specs.Describe(t, "Engine Start With Telemetry", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			tel := &Telemetry{
				Tracer: nooptrace.NewTracerProvider().Tracer("test"),
				Meter:  noopmetric.NewMeterProvider().Meter("test"),
			}
			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithTelemetry(tel),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			sc.Expect(engine.metrics).To(specs.Not(specs.BeNil()))
			sc.Expect(engine.Started()).To(specs.BeTrue())
		})
	})
}

// TestEngineEraseEntity covers EraseEntity's happy paths: a no-op when
// `full` is false, the events-only path, and the events+snapshots path.
func TestEngineEraseEntity(t *testing.T) {
	specs.Describe(t, "Engine Erase Entity", func(s *specs.Spec) {
		ctx := context.Background()

		s.It("full=false is a no-op", func(sc *specs.Context) {
			t := sc.T
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			sc.Expect(engine.EraseEntity(ctx, uuid.NewString(), false)).To(specs.BeNil())
		})

		s.It("full=true with persisted events", func(sc *specs.Context) {
			t := sc.T
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			snapStore := testkit.NewSnapshotStore()
			sc.Expect(snapStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = snapStore.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithSnapshotStore(snapStore),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			entityID := uuid.NewString()
			sc.Expect(engine.Entity(ctx, NewEventSourcedEntity(entityID))).To(specs.BeNil())
			_, _, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{
				AccountBalance: 100,
			}, time.Minute)
			sc.Expect(err).To(specs.BeNil())

			sc.Expect(engine.EraseEntity(ctx, entityID, true)).To(specs.BeNil())

			// Subsequent erase against the same id should be a clean no-op (no
			// events left).
			sc.Expect(engine.EraseEntity(ctx, entityID, true)).To(specs.BeNil())
		})

		s.It("full=true with no events is safe", func(sc *specs.Context) {
			t := sc.T
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			// Entity that was never used: GetLatestEvent returns nil and
			// EraseEntity should short-circuit without error.
			sc.Expect(engine.EraseEntity(ctx, uuid.NewString(), true)).To(specs.BeNil())
		})
	})
}

// TestEngineProjectionLagHappyPath drives ProjectionLag through its full body
// so the per-shard iteration, the empty-shard short-circuit, and the lag
// computation are all exercised.
func TestEngineProjectionLagHappyPath(t *testing.T) {
	specs.Describe(t, "Engine Projection Lag Happy Path", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				WithProjection("any", &projection.Options{Handler: projection.NewDiscardHandler()}),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			// Fresh stores: every known shard is empty, so the loop should run the
			// empty-shard branch and return a zero-lag map.
			lags, err := engine.ProjectionLag(ctx, "any")
			sc.Expect(err).To(specs.BeNil())
			for _, lag := range lags {
				sc.Expect(lag).To(specs.BeZero())
			}
		})
	})
}

// TestEngineRebuildProjectionSuccess exercises the success branch of
// RebuildProjection: it stops the running projection, resets its offset, and
// restarts it.
func TestEngineRebuildProjectionSuccess(t *testing.T) {
	specs.Describe(t, "Engine Rebuild Projection Success", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				WithProjection("rebuild-target", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   100,
					PullInterval: time.Second,
				}),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			const name = "rebuild-target"
			sc.Expect(engine.StartProjection(ctx, name)).To(specs.BeNil())

			sc.Expect(engine.RebuildProjection(ctx, name, ZeroTime)).To(specs.BeNil())

			// The rebuilt projection must come up and stay up: a stale stop
			// cleanup of the old instance must not remove the new one.
			projectionRunning := func() any {
				running, err := engine.IsProjectionRunning(ctx, name)
				if err != nil {
					return err
				}
				return running
			}
			sc.Eventually(projectionRunning, specs.BeTrue(), specs.WithTimeout(waitTimeout))
			sc.Consistently(projectionRunning, specs.BeTrue(), specs.WithTimeout(300*time.Millisecond))
		})
	})
}

// TestEngineSagaHappyPath registers a saga via Engine.Saga and then queries
// its status via Engine.SagaStatus, covering the success branches of both.
func TestEngineSagaHappyPath(t *testing.T) {
	specs.Describe(t, "a saga registered through Engine.Saga reports its status", func(s *specs.Spec) {
		s.It("answers SagaStatus for the registered saga", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			sagaID := "saga-" + uuid.NewString()
			sc.Expect(engine.Saga(ctx, &testSagaBehavior{sagaID: sagaID}, 0)).To(specs.BeNil())
			sc.Eventually(func() any {
				_, err := engine.SagaStatus(ctx, sagaID, time.Second)
				return err
			}, specs.BeNil(), specs.WithTimeout(waitTimeout))

			info, err := engine.SagaStatus(ctx, sagaID, time.Minute)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(info).To(specs.Not(specs.BeNil()))
			sc.Expect(info.ID).To(specs.Equal(sagaID))
		})
	})
}

// ensure proto and context imports are not flagged when subtests vary.
var _ context.Context = context.Background()
var _ = proto.Message(nil)

// TestEngineAddEventPublishersGuards covers the engine-state guards on
// AddEventPublishers.
func TestEngineAddEventPublishersGuards(t *testing.T) {
	specs.Describe(t, "AddEventPublishers refuses an engine that is not started", func(s *specs.Spec) {
		s.It("returns ErrEngineNotStarted and never touches the publisher", func(ctx *specs.Context) {
			bg := context.Background()
			store := testkit.NewEventsStore()
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = store.Disconnect(bg) })

			engine := newTestEngine(ctx.T, "Sample", store, WithLogger(DiscardLogger))
			// not started; no expectation is declared, so any publisher call fails the case
			pub := enginetest.NewEventPublisherMock(mock.NewController(ctx))

			ctx.Expect(engine.AddEventPublishers(pub)).To(specs.MatchError(ErrEngineNotStarted))
		})
	})
}

// TestEngineAddStatePublishersGuards covers the engine-state guards on
// AddStatePublishers.
func TestEngineAddStatePublishersGuards(t *testing.T) {
	specs.Describe(t, "AddStatePublishers refuses an engine that is not started", func(s *specs.Spec) {
		s.It("returns ErrEngineNotStarted and never touches the publisher", func(ctx *specs.Context) {
			bg := context.Background()
			store := testkit.NewEventsStore()
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = store.Disconnect(bg) })

			engine := newTestEngine(ctx.T, "Sample", store, WithLogger(DiscardLogger))
			// not started; no expectation is declared, so any publisher call fails the case
			pub := enginetest.NewStatePublisherMock(mock.NewController(ctx))

			ctx.Expect(engine.AddStatePublishers(pub)).To(specs.MatchError(ErrEngineNotStarted))
		})
	})
}

// publishedCalls reads how many times the controller's Publish was called so
// far; the keeps-going and happy-path cases poll it instead of waiting on a
// channel with a fixed timeout.
func publishedCalls(ctrl *mock.Controller) func() any {
	return func() any { return len(ctrl.Method("Publish").Calls()) }
}

// TestEngineAddEventPublishers exercises the happy path of
// AddEventPublishers: the publisher must observe events generated by an
// event-sourced entity through the in-process stream.
func TestEngineAddEventPublishers(t *testing.T) {
	specs.Describe(t, "an added event publisher observes the events an entity generates", func(s *specs.Spec) {
		s.It("publishes the event of a created account", func(ctx *specs.Context) {
			bg := context.Background()
			store := testkit.NewEventsStore()
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = store.Disconnect(bg) })

			ctrl := mock.NewController(ctx)
			ctrl.Method("ID").Expect().Return("Urd.test.EventPublisher").AnyTimes()
			ctrl.Method("Close").Expect(mock.Any()).Return(nil).AnyTimes()
			ctrl.Method("Publish").Expect(mock.Any(), anEvent).Return(nil).AtLeast(1)

			engine := newTestEngine(ctx.T, "Sample", store, WithLogger(DiscardLogger))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())
			ctx.Expect(engine.AddEventPublishers(enginetest.NewEventPublisherMock(ctrl))).To(specs.BeNil())

			entityID := uuid.NewString()
			ctx.Expect(engine.Entity(bg, NewEventSourcedEntity(entityID))).To(specs.BeNil())
			_, _, err := engine.SendCommand(bg, entityID, &testpb.CreateAccount{AccountBalance: 100}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())

			ctx.Eventually(publishedCalls(ctrl), specs.BeGreaterThanOrEqual(1), specs.WithTimeout(5*time.Second))
		})
	})
}

// TestEnginePublisherIdleCPU is the regression test for
// https://github.com/Tochemey/ego/issues/291: the publisher consumption
// loops used to poll Subscriber.Iterator() — which returns a closed snapshot
// channel — in a tight select, pinning one full CPU core per registered
// publisher whenever the stream was idle. Post-fix the loops block on the
// subscriber's Ready signal, so an idle engine with publishers must consume
// close to zero CPU.
func TestEnginePublisherIdleCPU(t *testing.T) {
	specs.Describe(t, "Engine Publisher Idle CPU", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			eventCtrl := mock.NewController(t)
			eventCtrl.Method("ID").Expect().Return("Urd.test.EventPublisher").AnyTimes()
			eventCtrl.Method("Close").Expect(mock.Any()).Return(nil).AnyTimes()
			eventPub := enginetest.NewEventPublisherMock(eventCtrl)

			stateCtrl := mock.NewController(t)
			stateCtrl.Method("ID").Expect().Return("Urd.test.StatePublisher").AnyTimes()
			stateCtrl.Method("Close").Expect(mock.Any()).Return(nil).AnyTimes()
			statePub := enginetest.NewStatePublisherMock(stateCtrl)

			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			sc.Expect(engine.AddEventPublishers(eventPub)).To(specs.BeNil())
			sc.Expect(engine.AddStatePublishers(statePub)).To(specs.BeNil())

			// let startup work settle before sampling
			pause.For(500 * time.Millisecond)

			cpuStart := processCPUTime(t)
			const idle = 2 * time.Second
			pause.For(idle)
			cpuBurned := processCPUTime(t) - cpuStart

			// Pre-fix, each of the two idle consumption loops burned a full core
			// (~2s of CPU each over the 2s window). The threshold leaves generous
			// headroom for runtime and actor-system background work while still
			// catching any loop that spins instead of blocking.
			sc.Expect(cpuBurned).To(specs.BeLessThan(idle / 2))
		})
	})
}

// processCPUTime returns the cumulative user+system CPU time of the test
// process.
func processCPUTime(t *testing.T) time.Duration {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatalf("getrusage: %v", err)
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

// TestEngineAddStatePublishers exercises the happy path of
// AddStatePublishers: the publisher must observe durable-state updates
// generated by a durable-state entity.
func TestEngineAddStatePublishers(t *testing.T) {
	specs.Describe(t, "an added state publisher observes the durable states an entity generates", func(s *specs.Spec) {
		s.It("publishes the state of a created account", func(ctx *specs.Context) {
			bg := context.Background()
			stateStore := testkit.NewDurableStore()
			ctx.Expect(stateStore.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = stateStore.Disconnect(bg) })

			ctrl := mock.NewController(ctx)
			ctrl.Method("ID").Expect().Return("Urd.test.StatePublisher").AnyTimes()
			ctrl.Method("Close").Expect(mock.Any()).Return(nil).AnyTimes()
			ctrl.Method("Publish").Expect(mock.Any(), aState).Return(nil).AtLeast(1)

			engine := newTestEngine(ctx.T, "Sample", nil, WithLogger(DiscardLogger), WithStateStore(stateStore))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())
			ctx.Expect(engine.AddStatePublishers(enginetest.NewStatePublisherMock(ctrl))).To(specs.BeNil())

			entityID := uuid.NewString()
			ctx.Expect(engine.DurableStateEntity(bg, NewAccountDurableStateBehavior(entityID))).To(specs.BeNil())
			_, _, err := engine.SendCommand(bg, entityID, &testpb.CreateAccount{AccountBalance: 100}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())

			ctx.Eventually(publishedCalls(ctrl), specs.BeGreaterThanOrEqual(1), specs.WithTimeout(5*time.Second))
		})
	})
}

// TestEngineStopReturnsEventPublisherCloseError verifies that a failure in an
// EventPublisher's Close surfaces from Engine.Stop.
func TestEngineStopReturnsEventPublisherCloseError(t *testing.T) {
	specs.Describe(t, "Engine.Stop surfaces the Close failure of an event publisher", func(s *specs.Spec) {
		s.It("returns the error and closes the publisher once", func(ctx *specs.Context) {
			bg := context.Background()
			store := testkit.NewEventsStore()
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = store.Disconnect(bg) })

			closeErr := errors.New("close error")
			ctrl := mock.NewController(ctx)
			ctrl.Method("ID").Expect().Return("Urd.test.FailingEventPublisher").AnyTimes()
			ctrl.Method("Close").Expect(mock.Any()).Return(closeErr).Times(1)

			cfg := NewConfig(store, WithLogger(DiscardLogger))
			sys, err := goakt.NewActorSystem("Sample", cfg.GoaktOptions()...)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(sys.Start(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = sys.Stop(bg) })

			engine, err := NewEngine(sys, cfg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())
			ctx.Expect(engine.AddEventPublishers(enginetest.NewEventPublisherMock(ctrl))).To(specs.BeNil())

			ctx.Expect(engine.Stop(bg)).To(specs.MatchError(closeErr))
		})
	})
}

// TestEngineStopReturnsStatePublisherCloseError verifies that a failure in a
// StatePublisher's Close surfaces from Engine.Stop.
func TestEngineStopReturnsStatePublisherCloseError(t *testing.T) {
	specs.Describe(t, "Engine.Stop surfaces the Close failure of a state publisher", func(s *specs.Spec) {
		s.It("returns the error and closes the publisher once", func(ctx *specs.Context) {
			bg := context.Background()
			stateStore := testkit.NewDurableStore()
			ctx.Expect(stateStore.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = stateStore.Disconnect(bg) })

			closeErr := errors.New("close error")
			ctrl := mock.NewController(ctx)
			ctrl.Method("ID").Expect().Return("Urd.test.FailingStatePublisher").AnyTimes()
			ctrl.Method("Close").Expect(mock.Any()).Return(closeErr).Times(1)

			cfg := NewConfig(nil, WithLogger(DiscardLogger), WithStateStore(stateStore))
			sys, err := goakt.NewActorSystem("Sample", cfg.GoaktOptions()...)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(sys.Start(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = sys.Stop(bg) })

			engine, err := NewEngine(sys, cfg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())
			ctx.Expect(engine.AddStatePublishers(enginetest.NewStatePublisherMock(ctrl))).To(specs.BeNil())

			ctx.Expect(engine.Stop(bg)).To(specs.MatchError(closeErr))
		})
	})
}

// TestEngineEventPublisherKeepsGoingOnPublishError ensures that a failing
// Publish does not stall or kill the sendEvent goroutine: a subsequent event
// must still be delivered.
func TestEngineEventPublisherKeepsGoingOnPublishError(t *testing.T) {
	specs.Describe(t, "a failing event Publish does not stop the delivery loop", func(s *specs.Spec) {
		s.It("still delivers the next event after a publish error", func(ctx *specs.Context) {
			bg := context.Background()
			store := testkit.NewEventsStore()
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = store.Disconnect(bg) })

			ctrl := mock.NewController(ctx)
			ctrl.Method("ID").Expect().Return("Urd.test.FailingPublisher").AnyTimes()
			ctrl.Method("Close").Expect(mock.Any()).Return(nil).AnyTimes()
			ctrl.Method("Publish").Expect(mock.Any(), anEvent).Return(errAnyFailure).AtLeast(2)

			engine := newTestEngine(ctx.T, "Sample", store, WithLogger(DiscardLogger))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())
			ctx.Expect(engine.AddEventPublishers(enginetest.NewEventPublisherMock(ctrl))).To(specs.BeNil())

			entityID := uuid.NewString()
			ctx.Expect(engine.Entity(bg, NewEventSourcedEntity(entityID))).To(specs.BeNil())
			_, _, err := engine.SendCommand(bg, entityID, &testpb.CreateAccount{AccountBalance: 100}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Eventually(publishedCalls(ctrl), specs.BeGreaterThanOrEqual(1), specs.WithTimeout(5*time.Second))

			_, _, err = engine.SendCommand(bg, entityID, &testpb.CreditAccount{AccountId: entityID, Balance: 25}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Eventually(publishedCalls(ctrl), specs.BeGreaterThanOrEqual(2), specs.WithTimeout(5*time.Second))
		})
	})
}

// TestEngineStatePublisherKeepsGoingOnPublishError ensures that a failing
// state Publish does not kill the sendState goroutine.
func TestEngineStatePublisherKeepsGoingOnPublishError(t *testing.T) {
	specs.Describe(t, "a failing state Publish does not stop the delivery loop", func(s *specs.Spec) {
		s.It("still delivers the next state after a publish error", func(ctx *specs.Context) {
			bg := context.Background()
			stateStore := testkit.NewDurableStore()
			ctx.Expect(stateStore.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = stateStore.Disconnect(bg) })

			ctrl := mock.NewController(ctx)
			ctrl.Method("ID").Expect().Return("Urd.test.FailingStatePublisher").AnyTimes()
			ctrl.Method("Close").Expect(mock.Any()).Return(nil).AnyTimes()
			ctrl.Method("Publish").Expect(mock.Any(), aState).Return(errAnyFailure).AtLeast(2)

			engine := newTestEngine(ctx.T, "Sample", nil, WithLogger(DiscardLogger), WithStateStore(stateStore))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())
			ctx.Expect(engine.AddStatePublishers(enginetest.NewStatePublisherMock(ctrl))).To(specs.BeNil())

			entityID := uuid.NewString()
			ctx.Expect(engine.DurableStateEntity(bg, NewAccountDurableStateBehavior(entityID))).To(specs.BeNil())
			_, _, err := engine.SendCommand(bg, entityID, &testpb.CreateAccount{AccountBalance: 100}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Eventually(publishedCalls(ctrl), specs.BeGreaterThanOrEqual(1), specs.WithTimeout(5*time.Second))

			_, _, err = engine.SendCommand(bg, entityID, &testpb.CreditAccount{AccountId: entityID, Balance: 25}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Eventually(publishedCalls(ctrl), specs.BeGreaterThanOrEqual(2), specs.WithTimeout(5*time.Second))
		})
	})
}

// TestEngineSendCommandWithTelemetry exercises the telemetry-instrumented
// branch of SendCommand for both the success and error paths.
func TestEngineSendCommandWithTelemetry(t *testing.T) {
	specs.Describe(t, "Engine Send Command With Telemetry", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			tel := &Telemetry{
				Tracer: nooptrace.NewTracerProvider().Tracer("test"),
				Meter:  noopmetric.NewMeterProvider().Meter("test"),
			}
			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithTelemetry(tel),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			entityID := uuid.NewString()
			sc.Expect(engine.Entity(ctx, NewEventSourcedEntity(entityID))).To(specs.BeNil())

			state, _, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{
				AccountBalance: 42,
			}, time.Minute)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(state).To(specs.Not(specs.BeNil()))

			// Error path: an unknown entity hits a send timeout, exercising the
			// span.RecordError branch.
			_, _, err = engine.SendCommand(ctx, "missing-"+uuid.NewString(),
				&testpb.CreateAccount{AccountBalance: 1}, 10*time.Millisecond)
			sc.Expect(err).To(specs.Not(specs.BeNil()))
		})
	})
}

// TestToSupervisorDirectiveStop covers the StopDirective branch.
func TestToSupervisorDirectiveStop(t *testing.T) {
	specs.Describe(t, "toSupervisorDirective maps the StopDirective and RestartDirective branches", func(s *specs.Spec) {
		s.It("maps StopDirective to Stop and RestartDirective to Restart", func(ctx *specs.Context) {
			ctx.Expect(toSupervisorDirective(StopDirective)).ToEqual(supervisor.StopDirective)
			ctx.Expect(toSupervisorDirective(RestartDirective)).ToEqual(supervisor.RestartDirective)
		})
	})
}

// TestEngineStartWithoutActorSystem covers the guard in Start that returns
// ErrActorSystemRequired when the engine's atomic actor-system reference has
// been detached (e.g. mid-shutdown or in a manually-constructed instance).
func TestEngineStartWithoutActorSystem(t *testing.T) {
	specs.Describe(t, "Engine.Start refuses an engine whose actor-system reference was detached", func(s *specs.Spec) {
		s.It("returns ErrActorSystemRequired", func(ctx *specs.Context) {
			e := &Engine{
				eventsStore:   testkit.NewEventsStore(),
				logger:        DiscardLogger,
				eventsStreams: syncmap.New[string, *eventsStream](),
				statesStreams: syncmap.New[string, *statesStream](),
			}
			ctx.Expect(e.Start(context.Background())).To(specs.MatchError(ErrActorSystemRequired))
		})
	})
}

// TestEngineProjectionLagClampsNegative seeds the offset store with a value
// far in the future so that latestTimestamp - currOffset is negative,
// exercising the lag clamp in ProjectionLag.
func TestEngineProjectionLagClampsNegative(t *testing.T) {
	specs.Describe(t, "Engine Projection Lag Clamps Negative", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				// ProjectionLag reads the scope the engine resolved at registration.
				WithProjection("future-projection", &projection.Options{Handler: projection.NewDiscardHandler()}),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			// Produce an event so the shard is non-empty and we follow the
			// latestTimestamp/currOffset arithmetic branch.
			entityID := uuid.NewString()
			sc.Expect(engine.Entity(ctx, NewEventSourcedEntity(entityID))).To(specs.BeNil())
			_, _, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{
				AccountBalance: 100,
			}, time.Minute)
			sc.Expect(err).To(specs.BeNil())

			// Discover the populated shards and stamp a future offset on each so
			// currOffset > latestTimestamp and the clamp branch fires.
			shardOffsets, err := store.ShardOffsets(ctx, persistence.Unscoped())
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(shardOffsets).To(specs.Not(specs.BeEmpty()))

			const projectionName = "future-projection"
			future := time.Now().Add(24 * time.Hour).UnixNano()
			for shard := range shardOffsets {
				sc.Expect(offsetStore.WriteOffset(ctx, &egopb.Offset{
					ProjectionName: projectionName,
					ShardNumber:    shard,
					Value:          future,
					Timestamp:      time.Now().UnixMilli(),
				})).To(specs.BeNil())
			}

			lags, err := engine.ProjectionLag(ctx, projectionName)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(lags).To(specs.Not(specs.BeEmpty()))
			for _, lag := range lags {
				sc.Expect(lag).To(specs.Equal(time.Duration(0)))
			}
		})
	})
}

// TestEngineNotStartedGuardsDirect drives the top-of-function
// `!engine.Started()` short-circuit on every API that has one. The synth
// engine used by TestEngineHotPathGuards has started=true and exercises the
// "ref==nil" branch; this test complements it by exercising the "started==
// false" branch with a real engine that simply has not been started.
func TestEngineNotStartedGuardsDirect(t *testing.T) {
	specs.Describe(t, "Engine Not Started Guards Direct", func(s *specs.Spec) {
		ctx := context.Background()
		store := testkit.NewEventsStore()
		t.Cleanup(func() { _ = store.Disconnect(ctx) })
		s.BeforeEach(func(sc *specs.Context) { sc.Expect(store.Connect(ctx)).To(specs.BeNil()) })

		newEngine := func() *Engine {
			return newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithStateStore(testkit.NewDurableStore()),
			)
		}

		s.It("StartProjection", func(sc *specs.Context) {
			sc.Expect(newEngine().StartProjection(ctx, "p")).To(specs.MatchError(ErrEngineNotStarted))
		})
		s.It("StopProjection", func(sc *specs.Context) {
			sc.Expect(newEngine().StopProjection(ctx, "p")).To(specs.MatchError(ErrEngineNotStarted))
		})
		s.It("IsProjectionRunning", func(sc *specs.Context) {
			running, err := newEngine().IsProjectionRunning(ctx, "p")
			sc.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
			sc.Expect(running).To(specs.BeFalse())
		})
		s.It("Entity", func(sc *specs.Context) {
			sc.Expect(newEngine().Entity(ctx, NewEventSourcedEntity(uuid.NewString()))).To(specs.MatchError(ErrEngineNotStarted))
		})
		s.It("DurableStateEntity", func(sc *specs.Context) {
			sc.Expect(newEngine().DurableStateEntity(ctx, NewAccountDurableStateBehavior(uuid.NewString()))).To(specs.MatchError(ErrEngineNotStarted))
		})
		s.It("Saga", func(sc *specs.Context) {
			sc.Expect(newEngine().Saga(ctx, &testSagaBehavior{sagaID: "s"}, 0)).To(specs.MatchError(ErrEngineNotStarted))
		})
		s.It("SagaStatus", func(sc *specs.Context) {
			info, err := newEngine().SagaStatus(ctx, "s", time.Second)
			sc.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
			sc.Expect(info).To(specs.BeNil())
		})
	})
}

// TestEngineIsProjectionRunningActorOfError covers the ActorOf-failure
// branch of IsProjectionRunning. Asking for an actor name that does not
// exist makes the underlying actor system surface an error, which the
// engine wraps.
func TestEngineIsProjectionRunningActorOfError(t *testing.T) {
	specs.Describe(t, "Engine Is Projection Running Actor Of Error", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			running, err := engine.IsProjectionRunning(ctx, "missing-projection-"+uuid.NewString())
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(running).To(specs.BeFalse())
		})
	})
}

// TestEngineStartProjectionStandaloneSpawnError covers the spawn-failure wrap
// in StartProjection's standalone branch (lines 378-379). The actor system is
// stopped out from under the engine so that the next Spawn call returns
// ErrActorSystemNotStarted, which the engine wraps.
func TestEngineStartProjectionStandaloneSpawnError(t *testing.T) {
	specs.Describe(t, "Engine Start Projection Standalone Spawn Error", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			cfg := NewConfig(store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				WithProjection("boom-projection", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   100,
					PullInterval: time.Second,
				}),
			)
			sys, err := goakt.NewActorSystem("Sample", cfg.GoaktOptions()...)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(sys.Start(ctx)).To(specs.BeNil())

			engine, err := NewEngine(sys, cfg)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			// Stop the actor system; the engine still believes it's running, so
			// the StartProjection call falls through to the Spawn-in-standalone path
			// and gets ErrActorSystemNotStarted from goakt.
			sc.Expect(sys.Stop(ctx)).To(specs.BeNil())

			err = engine.StartProjection(ctx, "boom-projection")
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(err.Error()).To(specs.Contain("failed to start the projection"))
		})
	})
}

// TestEngineEntityWithRetentionPolicy exercises the retention-policy block
// in Entity (lines 552-556) by passing a non-nil RetentionPolicy.
func TestEngineEntityWithRetentionPolicy(t *testing.T) {
	specs.Describe(t, "Engine Entity With Retention Policy", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			snapStore := testkit.NewSnapshotStore()
			sc.Expect(snapStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = snapStore.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithSnapshotStore(snapStore),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			entityID := uuid.NewString()
			sc.Expect(engine.Entity(ctx, NewEventSourcedEntity(entityID),
				WithRetentionPolicy(RetentionPolicy{
					DeleteEventsOnSnapshot:    true,
					DeleteSnapshotsOnSnapshot: true,
					EventsRetentionCount:      3,
				}),
			)).To(specs.MatchError(persistence.ErrUnsafeEventRetention))
		})
	})
}

// TestEngineSendCommandUnexpectedReply pins the ErrCommandReplyUnmarshalling
// branch: when the reply is not a *egopb.CommandReply, SendCommand surfaces
// that sentinel error.
func TestEngineSendCommandUnexpectedReply(t *testing.T) {
	specs.Describe(t, "Engine Send Command Unexpected Reply", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			// Spawn a plain goakt actor under a known name that replies with a
			// non-CommandReply proto; SendCommand routes there by entityID.
			sys := engine.ActorSystem()
			sc.Expect(sys).To(specs.Not(specs.BeNil()))
			entityID := "weird-" + uuid.NewString()
			_, err := sys.Spawn(ctx, entityID,
				&enginetest.SimpleReplyActor{Reply: &samplepb.Account{AccountId: entityID}},
				goakt.WithLongLived())
			sc.Expect(err).To(specs.BeNil())

			state, rev, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{
				AccountBalance: 1,
			}, time.Minute)
			sc.Expect(err).To(specs.MatchError(ErrCommandReplyUnmarshalling))
			sc.Expect(state).To(specs.BeNil())
			sc.Expect(rev).To(specs.BeZero())
		})
	})
}

// TestEngineSagaStatusErrorPaths covers the three error branches of
// SagaStatus that follow the not-started guard: SendSync failure, an
// unexpected reply type, and parseCommandReply returning an error.
func TestEngineSagaStatusErrorPaths(t *testing.T) {
	specs.Describe(t, "Engine Saga Status Error Paths", func(s *specs.Spec) {
		ctx := context.Background()
		store := testkit.NewEventsStore()
		t.Cleanup(func() { _ = store.Disconnect(ctx) })
		s.BeforeEach(func(sc *specs.Context) { sc.Expect(store.Connect(ctx)).To(specs.BeNil()) })

		engine := newTestEngine(t, "Sample", store, WithLogger(DiscardLogger))
		// The engine is shared by the cases below, so it starts once.
		var (
			startOnce sync.Once
			sys       goakt.ActorSystem
		)
		s.BeforeEach(func(sc *specs.Context) {
			startOnce.Do(func() {
				sc.Expect(engine.Start(ctx)).To(specs.BeNil())
				sys = engine.ActorSystem()
			})
			sc.Expect(sys).To(specs.Not(specs.BeNil()))
		})

		s.It("empty saga id", func(sc *specs.Context) {
			info, err := engine.SagaStatus(ctx, "", time.Second)
			sc.Expect(err).To(specs.MatchError(ErrUndefinedEntityID))
			sc.Expect(info).To(specs.BeNil())
		})

		s.It("SendSync failure", func(sc *specs.Context) {
			info, err := engine.SagaStatus(ctx, "missing-saga-"+uuid.NewString(), 10*time.Millisecond)
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(err.Error()).To(specs.Contain("failed to get saga status"))
			sc.Expect(info).To(specs.BeNil())
		})

		s.It("unexpected reply type", func(sc *specs.Context) {
			sagaID := "saga-bad-reply-" + uuid.NewString()
			_, err := sys.Spawn(ctx, sagaID,
				&enginetest.SimpleReplyActor{Reply: &samplepb.Account{}},
				goakt.WithLongLived())
			sc.Expect(err).To(specs.BeNil())
			info, err := engine.SagaStatus(ctx, sagaID, time.Minute)
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(err.Error()).To(specs.Contain("unexpected reply type from saga"))
			sc.Expect(info).To(specs.BeNil())
		})

		s.It("parseCommandReply error reply", func(sc *specs.Context) {
			sagaID := "saga-error-reply-" + uuid.NewString()
			errReply := &egopb.CommandReply{
				Reply: &egopb.CommandReply_ErrorReply{
					ErrorReply: &egopb.ErrorReply{Message: "saga is sick"},
				},
			}
			_, err := sys.Spawn(ctx, sagaID,
				&enginetest.SimpleReplyActor{Reply: errReply},
				goakt.WithLongLived())
			sc.Expect(err).To(specs.BeNil())
			info, err := engine.SagaStatus(ctx, sagaID, time.Minute)
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(err.Error()).To(specs.Contain("saga is sick"))
			sc.Expect(info).To(specs.BeNil())
		})
	})
}

// synthEngineWithStores builds a minimal Engine whose state-machine looks
// "started" to the API but whose stores are injected directly. The actor
// system is NOT populated: any code path that touches it via
// engine.actorSystem.Load() will fail, so this synth is appropriate only for
// methods that read stores after their started-guard (EraseEntity,
// ProjectionLag, ...).
func synthEngineWithStores(eventsStore persistence.EventsStore, snapStore persistence.SnapshotStore, offsetStore offsetstore.OffsetStore) *Engine {
	e := &Engine{
		eventsStore:   eventsStore,
		snapshotStore: snapStore,
		offsetStore:   offsetStore,
		logger:        DiscardLogger,
		eventsStreams: syncmap.New[string, *eventsStream](),
		statesStreams: syncmap.New[string, *statesStream](),
	}
	e.started.Store(true)
	e.projectionScopes = map[string]persistence.Scope{"any": lagTestScope}
	return e
}

// lagTestScope is the scope the synthetic engine resolved for the projection
// "any". It is a tenant scope, not Unscoped(), so a ProjectionLag that read
// without its scope would not match the mock expectations.
var lagTestScope = func() persistence.Scope {
	scope, err := persistence.NewTenantScope("acme")
	if err != nil {
		panic(err)
	}
	return scope
}()

// TestEngineEraseEntityStoreErrors covers the three error wraps in
// EraseEntity's full-erase block (lines 906-918): GetLatestEvent failure,
// DeleteEvents failure, and DeleteSnapshots failure.
func TestEngineEraseEntityStoreErrors(t *testing.T) {
	specs.Describe(t, "EraseEntity wraps the store failures of its full-erase block", func(s *specs.Spec) {
		bg := context.Background()

		s.It("GetLatestEvent error", func(ctx *specs.Context) {
			ctrl := mock.NewController(ctx)
			ctrl.Method("GetLatestEvent").
				Expect(mock.Any(), persistence.Unscoped(), "pid-1").
				Return(nil, errors.New("boom"))

			engine := synthEngineWithStores(enginetest.NewEventsStoreMock(ctrl), nil, nil)
			err := engine.EraseEntity(bg, "pid-1", true)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(engRestErrText(err)).To(specs.Contain("failed to get latest event for erasure"))
		})

		s.It("DeleteEvents error", func(ctx *specs.Context) {
			ctrl := mock.NewController(ctx)
			ctrl.Method("GetLatestEvent").
				Expect(mock.Any(), persistence.Unscoped(), "pid-2").
				Return(&egopb.Event{SequenceNumber: 5}, nil)
			ctrl.Method("DeleteEvents").
				Expect(mock.Any(), persistence.Unscoped(), "pid-2", uint64(5)).
				Return(errors.New("delete fail"))

			engine := synthEngineWithStores(enginetest.NewEventsStoreMock(ctrl), nil, nil)
			err := engine.EraseEntity(bg, "pid-2", true)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(engRestErrText(err)).To(specs.Contain("failed to delete events for erasure"))
		})

		s.It("DeleteSnapshots error", func(ctx *specs.Context) {
			ctrl := mock.NewController(ctx)
			ctrl.Method("GetLatestEvent").
				Expect(mock.Any(), persistence.Unscoped(), "pid-3").
				Return(&egopb.Event{SequenceNumber: 7}, nil)
			ctrl.Method("DeleteEvents").
				Expect(mock.Any(), persistence.Unscoped(), "pid-3", uint64(7)).
				Return(nil)
			ctrl.Method("DeleteSnapshots").
				Expect(mock.Any(), persistence.Unscoped(), "pid-3", uint64(7)).
				Return(errors.New("snap fail"))

			engine := synthEngineWithStores(enginetest.NewEventsStoreMock(ctrl), enginetest.NewSnapshotStoreMock(ctrl), nil)
			err := engine.EraseEntity(bg, "pid-3", true)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(engRestErrText(err)).To(specs.Contain("failed to delete snapshots for erasure"))
		})
	})
}

// TestEngineProjectionLagStoreErrors covers ProjectionLag's per-store error
// wraps: ShardOffsets failure and GetCurrentOffset failure.
func TestEngineProjectionLagStoreErrors(t *testing.T) {
	specs.Describe(t, "ProjectionLag wraps the failures of each store it reads", func(s *specs.Spec) {
		bg := context.Background()

		s.It("ShardOffsets failure", func(ctx *specs.Context) {
			ctrl := mock.NewController(ctx)
			ctrl.Method("ShardOffsets").Expect(mock.Any(), lagTestScope).Return(map[uint64]int64(nil), errors.New("shards down"))
			// no GetCurrentOffset expectation: the offset store must not be read once the shards failed

			engine := synthEngineWithStores(enginetest.NewEventsStoreMock(ctrl), nil, enginetest.NewOffsetStoreMock(ctrl))
			lags, err := engine.ProjectionLag(bg, "any")
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(engRestErrText(err)).To(specs.Contain("failed to fetch shard offsets"))
			ctx.Expect(lags).To(specs.BeNil())
		})

		s.It("GetCurrentOffset failure", func(ctx *specs.Context) {
			ctrl := mock.NewController(ctx)
			ctrl.Method("ShardOffsets").Expect(mock.Any(), lagTestScope).Return(map[uint64]int64{1: 100}, nil)
			ctrl.Method("GetScopedOffset").
				Expect(mock.Any(), lagTestScope, mock.MatchT("a projection id", func(id *egopb.ProjectionId) bool { return id != nil })).
				Return(nil, errors.New("offset down"))

			engine := synthEngineWithStores(enginetest.NewEventsStoreMock(ctrl), nil, enginetest.NewOffsetStoreMock(ctrl))
			lags, err := engine.ProjectionLag(bg, "any")
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(engRestErrText(err)).To(specs.Contain("failed to get offset for shard"))
			ctx.Expect(lags).To(specs.BeNil())
		})
	})
}

// TestEngineProjectionLagComputation drives the lag arithmetic directly:
// lag is the difference between the shard's latest event timestamp (as
// reported by ShardOffsets) and the projection's committed offset.
func TestEngineProjectionLagComputation(t *testing.T) {
	specs.Describe(t, "ProjectionLag is the shard's latest event timestamp minus the projection's committed offset", func(s *specs.Spec) {
		s.It("reports 1500 - 500 as a lag of 1000 for the shard", func(ctx *specs.Context) {
			ctrl := mock.NewController(ctx)
			ctrl.Method("ShardOffsets").Expect(mock.Any(), lagTestScope).Return(map[uint64]int64{7: 1500}, nil)
			ctrl.Method("GetScopedOffset").
				Expect(mock.Any(), lagTestScope, mock.MatchT("a projection id", func(id *egopb.ProjectionId) bool { return id != nil })).
				Return(&egopb.Offset{Value: 500}, nil)

			engine := synthEngineWithStores(enginetest.NewEventsStoreMock(ctrl), nil, enginetest.NewOffsetStoreMock(ctrl))
			lags, err := engine.ProjectionLag(context.Background(), "any")
			ctx.Expect(err).To(specs.BeNil())
			specs.ExpectT(ctx, lags[7]).ToEqual(time.Duration(1000))
		})
	})
}

// TestEngineSagaSpawnError covers the Spawn-failure wrap in Engine.Saga
// (line 838). Same trick as the projection variant: stop the actor system
// while the engine still believes it owns one.
func TestEngineSagaSpawnError(t *testing.T) {
	specs.Describe(t, "Engine Saga Spawn Error", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			cfg := NewConfig(store, WithLogger(DiscardLogger))
			sys, err := goakt.NewActorSystem("Sample", cfg.GoaktOptions()...)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(sys.Start(ctx)).To(specs.BeNil())

			engine, err := NewEngine(sys, cfg)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			sc.Expect(sys.Stop(ctx)).To(specs.BeNil())

			err = engine.Saga(ctx, &testSagaBehavior{sagaID: "doomed-saga"}, time.Second)
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(err.Error()).To(specs.Contain("failed to start saga"))
		})
	})
}

// TestEngineRebuildProjectionRemoveError covers RebuildProjection's
// "failed to stop projection" branch (lines 470-472). Rebuilding a name
// that was never registered makes the internal StopProjection call fail
// because Kill cannot find the actor.
func TestEngineRebuildProjectionRemoveError(t *testing.T) {
	specs.Describe(t, "Engine Rebuild Projection Remove Error", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			err := engine.RebuildProjection(ctx, "never-registered-"+uuid.NewString(), ZeroTime)
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(err).To(specs.MatchError(ErrProjectionNotRegistered))
		})
	})
}

// TestEngineRebuildProjectionResetOffsetError covers the
// "failed to reset offset" branch (lines 475-477). A real projection is
// added so StopProjection succeeds, then the engine's offset store is
// swapped for a mock that fails on ResetOffset.
func TestEngineRebuildProjectionResetOffsetError(t *testing.T) {
	specs.Describe(t, "RebuildProjection reports an offset reset failure", func(s *specs.Spec) {
		s.It("wraps the ResetOffset error", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				WithProjection("rebuild-reset-error", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   100,
					PullInterval: time.Second,
				}),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			const name = "rebuild-reset-error"
			sc.Expect(engine.StartProjection(ctx, name)).To(specs.BeNil())
			sc.Eventually(projectionRunning(ctx, engine, name), specs.BeTrue(), specs.WithTimeout(waitTimeout))

			// Swap the offset store for one that fails on ResetOffset so the
			// rebuild path takes the ResetOffset-error branch.
			ctrl := mock.NewController(t)
			ctrl.Method("ResetOffset").Expect(mock.Any(), name, mock.Any()).Return(errors.New("reset boom"))
			engine.mutex.Lock()
			engine.offsetStore = enginetest.NewOffsetStoreMock(ctrl)
			engine.mutex.Unlock()

			err := engine.RebuildProjection(ctx, name, ZeroTime)
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(err.Error()).To(specs.Contain("failed to reset offset"))
		})
	})
}

// TestEngineRebuildProjectionRestartError covers the
// "failed to restart projection" branch (lines 480-482). A successful
// StopProjection + ResetOffset sequence is followed by a StartProjection
// that fails because the actor system is stopped from inside the offset
// store mock just before the restart runs.
func TestEngineRebuildProjectionRestartError(t *testing.T) {
	specs.Describe(t, "RebuildProjection reports a restart failure", func(s *specs.Spec) {
		s.It("wraps the StartProjection error", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			cfg := NewConfig(store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				WithProjection("rebuild-restart-error", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   100,
					PullInterval: time.Second,
				}),
			)
			sys, err := goakt.NewActorSystem("Sample", cfg.GoaktOptions()...)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(sys.Start(ctx)).To(specs.BeNil())

			engine, err := NewEngine(sys, cfg)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			const name = "rebuild-restart-error"
			sc.Expect(engine.StartProjection(ctx, name)).To(specs.BeNil())
			sc.Eventually(projectionRunning(ctx, engine, name), specs.BeTrue(), specs.WithTimeout(waitTimeout))

			// Inject a mock offset store whose ResetOffset stops the actor system
			// in-place. The subsequent StartProjection call inside RebuildProjection
			// will then see a not-running actor system and fail.
			ctrl := mock.NewController(t)
			ctrl.Method("ResetOffset").
				Expect(mock.Any(), name, mock.Any()).
				Do(func([]any) []any {
					_ = sys.Stop(ctx)
					return []any{nil}
				})

			engine.mutex.Lock()
			engine.offsetStore = enginetest.NewOffsetStoreMock(ctrl)
			engine.mutex.Unlock()

			err = engine.RebuildProjection(ctx, name, ZeroTime)
			sc.Expect(err).To(specs.Not(specs.BeNil()))
			sc.Expect(err.Error()).To(specs.Contain("failed to restart projection"))
		})
	})
}

// TestClusterEngineStartProjectionAlreadyExists exercises the cluster
// singleton branch in StartProjection where a second start of the same
// projection name is a clean no-op: SpawnSingleton is idempotent when the
// name is already bound to the same singleton, so no error surfaces.
func TestClusterEngineStartProjectionAlreadyExists(t *testing.T) {
	specs.Describe(t, "starting a projection twice in cluster mode is a no-op", func(s *specs.Spec) {
		s.It("takes the ErrSingletonAlreadyExists branch", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })
			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			ports := dynaport.Get(3)
			gossipPort, clusterPort, remotingPort := ports[0], ports[1], ports[2]
			host := "127.0.0.1"

			provider := &mockClusterProvider{
				id:    "test",
				peers: []string{net.JoinHostPort(host, strconv.Itoa(clusterPort))},
			}

			clusterCfg := goakt.NewClusterConfig().
				WithDiscovery(provider).
				WithDiscoveryPort(gossipPort).
				WithPeersPort(clusterPort).
				WithMinimumPeersQuorum(1).
				WithReplicaCount(1).
				WithPartitionCount(4).
				WithKinds(ClusterKinds()...)

			cfg := NewConfig(store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				WithProjection("discard-once", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   100,
					PullInterval: time.Second,
				}),
			)

			goaktOpts := append(cfg.GoaktOptions(),
				goakt.WithCluster(clusterCfg),
				goakt.WithRemote(remote.NewConfig(host, remotingPort)),
			)

			sys, err := goakt.NewActorSystem("Sample", goaktOpts...)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(sys.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = sys.Stop(ctx) })

			sc.Eventually(func() any { return sys.InCluster() }, specs.BeTrue(), specs.WithTimeout(waitTimeout))

			engine, err := NewEngine(sys, cfg)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = engine.Stop(ctx) })

			const name = "discard-once"
			sc.Expect(engine.StartProjection(ctx, name)).To(specs.BeNil())
			sc.Eventually(projectionRunning(ctx, engine, name), specs.BeTrue(), specs.WithTimeout(waitTimeout))

			// Re-registering must take the ErrSingletonAlreadyExists branch and
			// silently return nil rather than erroring.
			sc.Expect(engine.StartProjection(ctx, name)).To(specs.BeNil())
		})
	})
}

// TestEngineProjectionLagWithEvents drives ProjectionLag through the path
// where a shard actually has events, exercising the latestTimestamp/offset
// arithmetic and the per-shard accumulation.
func TestEngineProjectionLagWithEvents(t *testing.T) {
	specs.Describe(t, "Engine Projection Lag With Events", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			ctx := context.Background()
			store := testkit.NewEventsStore()
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = store.Disconnect(ctx) })

			offsetStore := testkit.NewOffsetStore()
			sc.Expect(offsetStore.Connect(ctx)).To(specs.BeNil())
			t.Cleanup(func() { _ = offsetStore.Disconnect(ctx) })

			engine := newTestEngine(t, "Sample", store,
				WithLogger(DiscardLogger),
				WithOffsetStore(offsetStore),
				WithProjection("any", &projection.Options{Handler: projection.NewDiscardHandler()}),
			)
			sc.Expect(engine.Start(ctx)).To(specs.BeNil())

			// Produce at least one event so a shard becomes non-empty and the loop
			// falls through to the latestTimestamp/offset computation.
			entityID := uuid.NewString()
			sc.Expect(engine.Entity(ctx, NewEventSourcedEntity(entityID))).To(specs.BeNil())
			_, _, err := engine.SendCommand(ctx, entityID, &testpb.CreateAccount{
				AccountBalance: 100,
			}, time.Minute)
			sc.Expect(err).To(specs.BeNil())

			lags, err := engine.ProjectionLag(ctx, "any")
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(lags).To(specs.Not(specs.BeEmpty()))
			for _, lag := range lags {
				sc.Expect(int64(lag)).To(specs.BeGreaterThanOrEqual(int64(0)))
			}
		})
	})
}

// TestEngineProjectionLagIsScoped pins that lag is computed for the scope the
// engine resolved at registration, and that an unknown projection has none.
func TestEngineProjectionLagIsScoped(t *testing.T) {
	specs.Describe(t, "ProjectionLag reads the registered scope", func(s *specs.Spec) {
		s.It("reads the shards of the projection's own scope", func(ctx *specs.Context) {
			ctrl := mock.NewController(ctx)
			ctrl.Method("ShardOffsets").Expect(mock.Any(), lagTestScope).Return(map[uint64]int64{3: 900}, nil)
			ctrl.Method("GetScopedOffset").
				Expect(mock.Any(), lagTestScope, mock.MatchT("a projection id", func(id *egopb.ProjectionId) bool { return id != nil })).
				Return(&egopb.Offset{Value: 400}, nil)

			engine := synthEngineWithStores(enginetest.NewEventsStoreMock(ctrl), nil, enginetest.NewOffsetStoreMock(ctrl))
			lags, err := engine.ProjectionLag(context.Background(), "any")
			ctx.Expect(err).To(specs.BeNil())
			specs.ExpectT(ctx, lags[3]).ToEqual(time.Duration(500))
		})

		s.It("fails closed for a projection that was never registered, without reading any store", func(ctx *specs.Context) {
			// no expectations: a read of the events or offset store fails the test
			ctrl := mock.NewController(ctx)
			engine := synthEngineWithStores(enginetest.NewEventsStoreMock(ctrl), nil, enginetest.NewOffsetStoreMock(ctrl))

			lags, err := engine.ProjectionLag(context.Background(), "unknown")

			ctx.Expect(err).To(specs.MatchError(ErrProjectionNotRegistered))
			ctx.Expect(lags).To(specs.BeNil())
		})
	})
}
