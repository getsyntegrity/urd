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
	"github.com/google/uuid"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

// This file pins the spawn half of the spawn-binding contract. Since
// EGO-TENANT-009 a multi-tenant engine addresses an actor by (tenant, entity
// ID), so the same ID under two tenants is two independent actors; that half
// lives in engine_tenant_actor_identity_test.go. What stays here is the part
// that still holds wherever one actor name can be claimed by two tenants:
// re-spawning a live id under the SAME declared tenant is idempotent, while
// re-spawning it under a DIFFERENT tenant is rejected with
// ErrSpawnTenantMismatch instead of silently returning the other tenant's
// actor. That is the case in single-tenant mode, which keeps the bare-ID
// actor names it always had. GoAkt's local Spawn returns an already-running
// actor's PID with a nil error, so the engine checks the returned actor's own
// spawn binding (its EntityTenantScope dependency) after Spawn returns.

func newRespawnTestEngine(ctx *specs.Context) *Engine {
	bg := context.Background()
	eventsStore := connectedEventsStore(ctx)
	stateStore := testkit.NewDurableStore()
	ctx.Expect(stateStore.Connect(bg)).To(specs.BeNil())
	ctx.Cleanup(func() { _ = stateStore.Disconnect(bg) })

	engine := newSpecsEngine(ctx, "Respawn", eventsStore,
		WithTenantResolver(perCallerTenantResolver{}),
		WithStateStore(stateStore))
	ctx.Expect(engine.Start(bg)).To(specs.BeNil())
	return engine
}

// newSingleTenantRespawnEngine starts an engine in single-tenant mode for
// "acme": its actors keep the bare entity ID as their name, so a second tenant
// claiming a live ID is the collision the spawn binding exists to refuse.
func newSingleTenantRespawnEngine(ctx *specs.Context) *Engine {
	bg := context.Background()
	resolver, err := tenancy.WithSingleTenant("acme")
	ctx.Expect(err).To(specs.BeNil())
	engine := newSpecsEngine(ctx, "RespawnSingle", connectedEventsStore(ctx),
		WithTenantResolver(resolver),
		WithStateStore(connectedDurableStore(ctx)))
	ctx.Expect(engine.Start(bg)).To(specs.BeNil())
	return engine
}

func expectSpawnTenantMismatch(ctx *specs.Context, err error) {
	ctx.Helper()
	ctx.Expect(err).To(specs.MatchError(ErrSpawnTenantMismatch))
	ctx.Expect(err).To(specs.MatchError(tenancy.ErrDenied))
	// the tenancy mismatch must be recoverable via errors.As
	var tenancyErr *tenancy.Error
	ctx.Expect(err).To(specs.MatchErrorAs(&tenancyErr))
}

func TestEngineRespawnInSingleTenantModeRejectsAnotherTenant(t *testing.T) {
	specs.Describe(t, "respawning a live id under another tenant in single-tenant mode is rejected", func(s *specs.Spec) {
		bg := context.Background()
		globex := WithTenant(tenancy.TenantID("globex"))

		s.It("EventSourced entity", func(ctx *specs.Context) {
			engine := newSingleTenantRespawnEngine(ctx)
			id := uuid.NewString()
			// a new actor spawns under the single tenant
			ctx.Expect(engine.Entity(bg, newTenancyProbeEventSourcedBehavior(id))).To(specs.BeNil())
			// a same-tenant respawn is idempotent
			ctx.Expect(engine.Entity(bg, newTenancyProbeEventSourcedBehavior(id), WithTenant("acme"))).To(specs.BeNil())

			expectSpawnTenantMismatch(ctx, engine.Entity(bg, newTenancyProbeEventSourcedBehavior(id), globex))
		})

		s.It("DurableStateEntity", func(ctx *specs.Context) {
			engine := newSingleTenantRespawnEngine(ctx)
			id := uuid.NewString()
			ctx.Expect(engine.DurableStateEntity(bg, newTenancyProbeDurableStateBehavior(id))).To(specs.BeNil())
			ctx.Expect(engine.DurableStateEntity(bg, newTenancyProbeDurableStateBehavior(id), WithTenant("acme"))).To(specs.BeNil())

			expectSpawnTenantMismatch(ctx, engine.DurableStateEntity(bg, newTenancyProbeDurableStateBehavior(id), globex))
		})

		s.It("Saga", func(ctx *specs.Context) {
			engine := newSingleTenantRespawnEngine(ctx)
			id := "saga-" + uuid.NewString()
			saga := func() *enginetest.CallbackSagaBehavior {
				return &enginetest.CallbackSagaBehavior{SagaID: id, HandleEventFn: func(context.Context, Event, State) (*SagaAction, error) {
					return &SagaAction{}, nil
				}}
			}
			ctx.Expect(engine.Saga(bg, saga(), 0)).To(specs.BeNil())
			ctx.Expect(engine.Saga(bg, saga(), 0, WithTenant("acme"))).To(specs.BeNil())

			expectSpawnTenantMismatch(ctx, engine.Saga(bg, saga(), 0, globex))
		})
	})
}

// TestEngineConcurrentCrossTenantSpawnHasExactlyOneWinner races two spawns
// of the same entity id under different tenants in single-tenant mode, where
// both claim one actor name. Exactly one must succeed, the other must be
// rejected, and the loser's behavior must never run.
func TestEngineConcurrentCrossTenantSpawnHasExactlyOneWinner(t *testing.T) {
	specs.Describe(t, "two concurrent spawns of one id under different tenants in single-tenant mode", func(s *specs.Spec) {
		s.It("have exactly one winner and the loser's behavior never runs", func(ctx *specs.Context) {
			bg := context.Background()
			engine := newSingleTenantRespawnEngine(ctx)
			tenants := []string{"acme", "globex"}

			for range 20 {
				id := uuid.NewString()
				probes := []*tenancyProbeEventSourcedBehavior{newTenancyProbeEventSourcedBehavior(id), newTenancyProbeEventSourcedBehavior(id)}
				errs := make([]error, len(tenants))

				var ready, done sync.WaitGroup
				start := make(chan struct{})
				for i, tenant := range tenants {
					ready.Add(1)
					done.Add(1)
					go func() {
						defer done.Done()
						ready.Done()
						<-start
						errs[i] = engine.Entity(bg, probes[i], WithTenant(tenancy.TenantID(tenant)))
					}()
				}
				ready.Wait()
				close(start)
				done.Wait()

				winners := 0
				loser := -1
				for i, err := range errs {
					if err == nil {
						winners++
						continue
					}
					expectSpawnTenantMismatch(ctx, err)
					loser = i
				}
				// exactly one tenant must win the spawn race
				ctx.Expect(winners).To(specs.Equal(1))

				// the single tenant's command reaches the actor only when that
				// actor is bound to it; the loser's behavior never runs
				_, _, _ = engine.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 1}, time.Minute)
				ctx.Expect(probes[loser].InvocationCount()).To(specs.BeZero())
			}
		})
	})
}

// TestEngineConcurrentSpawnOfOneIDUnderTwoTenantsHasTwoWinners is the
// multi-tenant counterpart: two tenants racing for one id each get their own
// actor, every time.
func TestEngineConcurrentSpawnOfOneIDUnderTwoTenantsHasTwoWinners(t *testing.T) {
	specs.Describe(t, "two concurrent spawns of one id under different tenants in a multi-tenant engine", func(s *specs.Spec) {
		s.It("both win, and each tenant's commands reach only its own actor", func(ctx *specs.Context) {
			bg := context.Background()
			engine := newRespawnTestEngine(ctx)
			tenants := []string{"acme", "globex"}

			for range 20 {
				id := uuid.NewString()
				probes := []*tenancyProbeEventSourcedBehavior{newTenancyProbeEventSourcedBehavior(id), newTenancyProbeEventSourcedBehavior(id)}
				errs := make([]error, len(tenants))

				var ready, done sync.WaitGroup
				start := make(chan struct{})
				for i, tenant := range tenants {
					ready.Add(1)
					done.Add(1)
					go func() {
						defer done.Done()
						ready.Done()
						<-start
						errs[i] = engine.Entity(bg, probes[i], WithTenant(tenancy.TenantID(tenant)))
					}()
				}
				ready.Wait()
				close(start)
				done.Wait()

				for i, err := range errs {
					ctx.Expect(err).To(specs.BeNil())

					callerCtx := context.WithValue(bg, perCallerTenantKey{}, tenants[i])
					_, _, sendErr := engine.SendCommand(callerCtx, id, &testpb.CreateAccount{AccountBalance: 1}, time.Minute)
					ctx.Expect(sendErr).To(specs.BeNil())
				}
				// each command ran in the behavior of its own tenant's actor
				ctx.Expect(probes[0].InvocationCount()).To(specs.Equal(1))
				ctx.Expect(probes[1].InvocationCount()).To(specs.Equal(1))
			}
		})
	})
}

func TestEngineRespawnInLegacyModeIsUnchanged(t *testing.T) {
	specs.Describe(t, "respawning a live id without a resolver", func(s *specs.Spec) {
		s.It("stays a no-op success and ignores WithTenant", func(ctx *specs.Context) {
			bg := context.Background()
			store := connectedEventsStore(ctx)

			engine := newSpecsEngine(ctx, "RespawnLegacy", store)
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())

			id := uuid.NewString()
			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			// legacy respawn of a live id stays a no-op success
			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			// WithTenant is ignored in legacy mode, exactly as before.
			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id), WithTenant(tenancy.TenantID("acme")))).To(specs.BeNil())
		})
	})
}

// TestClassifyTenantBinding pins how the engine maps the owning actor's
// TenantBindingReply to the spawn outcome: a match is success, a different
// binding is ErrSpawnTenantMismatch, and an actor holding no binding is
// ErrSpawnTenantUnverified, which asserts no conflict.
func TestClassifyTenantBinding(t *testing.T) {
	specs.Describe(t, "classifyTenantBinding maps the owning actor's TenantBindingReply to the spawn outcome", func(s *specs.Spec) {
		s.It("treats a match as success, a different binding as a mismatch and no binding as unverified", func(ctx *specs.Context) {
			requested := extensions.NewEntityTenantScope("acme")

			ctx.Expect(classifyTenantBinding("order-1", requested, &egopb.TenantBindingReply{TenantAware: true, Matches: true})).To(specs.BeNil())

			mismatch := classifyTenantBinding("order-1", requested, &egopb.TenantBindingReply{TenantAware: true})
			ctx.Expect(mismatch).To(specs.Not(specs.BeNil()))
			ctx.Expect(mismatch).To(specs.MatchError(ErrSpawnTenantMismatch))
			ctx.Expect(mismatch).To(specs.MatchError(tenancy.ErrDenied))
			// the tenancy mismatch must be recoverable via errors.As
			var tenancyErr *tenancy.Error
			ctx.Expect(mismatch).To(specs.MatchErrorAs(&tenancyErr))

			err := classifyTenantBinding("order-1", requested, &egopb.TenantBindingReply{})
			ctx.Expect(err).To(specs.MatchError(ErrSpawnTenantUnverified))
			ctx.Expect(err).To(specs.Not(specs.MatchError(ErrSpawnTenantMismatch)))
		})
	})
}

// TestDispatchRejectsTenantBindingQuery pins that the control message can
// never be sent as a command, so a caller cannot probe which tenant owns an
// entity id.
func TestDispatchRejectsTenantBindingQuery(t *testing.T) {
	specs.Describe(t, "the tenant binding query", func(s *specs.Spec) {
		s.It("is never accepted as a command", func(ctx *specs.Context) {
			bg := context.Background()
			engine := newRespawnTestEngine(ctx)
			id := uuid.NewString()
			ctx.Expect(engine.Entity(bg, newTenancyProbeEventSourcedBehavior(id), WithTenant(tenancy.TenantID("acme")))).To(specs.BeNil())

			globexCtx := context.WithValue(bg, perCallerTenantKey{}, "globex")
			_, _, err := engine.SendCommand(globexCtx, id, &egopb.TenantBindingQuery{TenantId: "acme"}, time.Minute)
			ctx.Expect(err).To(specs.MatchError(ErrNotACommand))
		})
	})
}
