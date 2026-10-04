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
	goakt "github.com/tochemey/goakt/v4/actor"

	"github.com/getsyntegrity/urd/command"
	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/actoridentity"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

// This file proves the actor identity of EGO-TENANT-009: in a multi-tenant
// engine an actor is identified by (tenant, entity ID), so two tenants that
// share an ID get two independent actors, with independent state, while the
// ID the records carry stays the business ID.

// callerOf returns a context that perCallerTenantResolver resolves to tenant.
func callerOf(tenant string) context.Context {
	return context.WithValue(context.Background(), perCallerTenantKey{}, tenant)
}

// newIdentityEngine starts a multi-tenant engine over the given stores.
func newIdentityEngine(ctx *specs.Context, name string, events *testkit.EventStore, states *testkit.DurableStore, opts ...Option) *Engine {
	all := append([]Option{WithTenantResolver(perCallerTenantResolver{}), WithStateStore(states)}, opts...)
	return startEngine(ctx, name, events, all...)
}

// balanceOf reads the balance of the account state SendCommand returned.
func balanceOf(ctx *specs.Context, state State) float64 {
	ctx.Helper()
	account, ok := state.(*testpb.Account)
	ctx.Expect(ok).To(specs.BeTrue())
	return account.GetAccountBalance()
}

// actorAlive reports whether the actor system holds an actor named name.
func actorAlive(ctx *specs.Context, engine *Engine, name string) bool {
	ctx.Helper()
	alive, err := engine.actorSystem.Load().sys.ActorExists(context.Background(), name)
	ctx.Expect(err).To(specs.BeNil())
	return alive
}

// qualifiedName is the actor name of id in tenant.
func qualifiedName(ctx *specs.Context, tenant, id string) string {
	ctx.Helper()
	name, err := actoridentity.Qualify(tenant, id)
	ctx.Expect(err).To(specs.BeNil())
	return name
}

func tenantScopeOf(ctx *specs.Context, tenant string) persistence.Scope {
	ctx.Helper()
	scope, err := persistence.NewTenantScope(tenancy.TenantID(tenant))
	ctx.Expect(err).To(specs.BeNil())
	return scope
}

func TestEngineSameIDUnderTwoTenantsIsTwoIndependentActors(t *testing.T) {
	specs.Describe(t, "the same entity id under two tenants", func(s *specs.Spec) {
		bg := context.Background()
		acme := WithTenant(tenancy.TenantID("acme"))
		globex := WithTenant(tenancy.TenantID("globex"))

		s.It("spawns two independent event-sourced actors that keep the business id", func(ctx *specs.Context) {
			events, states := connectedEventsStore(ctx), connectedDurableStore(ctx)
			engine := newIdentityEngine(ctx, "IdentityES", events, states)
			id := uuid.NewString()

			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id), acme)).To(specs.BeNil())
			// the second tenant no longer collides with the first
			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id), globex)).To(specs.BeNil())
			// a same-tenant respawn stays idempotent
			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id), acme)).To(specs.BeNil())

			// the actors are addressed by (tenant, id), never by the bare id
			ctx.Expect(actorAlive(ctx, engine, qualifiedName(ctx, "acme", id))).To(specs.BeTrue())
			ctx.Expect(actorAlive(ctx, engine, qualifiedName(ctx, "globex", id))).To(specs.BeTrue())
			ctx.Expect(actorAlive(ctx, engine, id)).To(specs.BeFalse())

			state, _, err := engine.SendCommand(callerOf("acme"), id, &testpb.CreateAccount{AccountBalance: 10}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(10.0))
			state, _, err = engine.SendCommand(callerOf("globex"), id, &testpb.CreateAccount{AccountBalance: 20}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(20.0))

			state, _, err = engine.SendCommand(callerOf("acme"), id, &testpb.CreditAccount{AccountId: id, Balance: 5}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(15.0))
			// acme's credit did not touch globex's account
			state, _, err = engine.SendCommand(callerOf("globex"), id, &testpb.CreditAccount{AccountId: id, Balance: 0}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(20.0))

			// each tenant sees only its own entity
			for tenant, want := range map[string]bool{"acme": true, "globex": true, "initech": false} {
				exists, existsErr := engine.EntityExists(callerOf(tenant), id)
				ctx.Expect(existsErr).To(specs.BeNil())
				ctx.Expect(exists).To(specs.Equal(want))
			}

			// the journal of each tenant is keyed by the business id, and the
			// actor name never reaches the records
			for _, tenant := range []string{"acme", "globex"} {
				latest, getErr := events.GetLatestEvent(bg, tenantScopeOf(ctx, tenant), id)
				ctx.Expect(getErr).To(specs.BeNil())
				ctx.Expect(latest.GetPersistenceId()).To(specs.Equal(id))
				// the create and the credit, whichever tenant's
				ctx.Expect(latest.GetSequenceNumber()).To(specs.Equal(uint64(2)))
			}
		})

		s.It("spawns two independent durable-state actors that keep the business id", func(ctx *specs.Context) {
			events, states := connectedEventsStore(ctx), connectedDurableStore(ctx)
			engine := newIdentityEngine(ctx, "IdentityDS", events, states)
			id := uuid.NewString()

			ctx.Expect(engine.DurableStateEntity(bg, NewAccountDurableStateBehavior(id), acme)).To(specs.BeNil())
			ctx.Expect(engine.DurableStateEntity(bg, NewAccountDurableStateBehavior(id), globex)).To(specs.BeNil())
			ctx.Expect(engine.DurableStateEntity(bg, NewAccountDurableStateBehavior(id), globex)).To(specs.BeNil())

			ctx.Expect(actorAlive(ctx, engine, qualifiedName(ctx, "acme", id))).To(specs.BeTrue())
			ctx.Expect(actorAlive(ctx, engine, qualifiedName(ctx, "globex", id))).To(specs.BeTrue())
			ctx.Expect(actorAlive(ctx, engine, id)).To(specs.BeFalse())

			state, _, err := engine.SendCommand(callerOf("acme"), id, &testpb.CreateAccount{AccountBalance: 10}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(10.0))
			state, _, err = engine.SendCommand(callerOf("globex"), id, &testpb.CreateAccount{AccountBalance: 20}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(20.0))
			state, _, err = engine.SendCommand(callerOf("acme"), id, &testpb.CreditAccount{AccountId: id, Balance: 5}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(15.0))

			for tenant, want := range map[string]bool{"acme": true, "globex": true, "initech": false} {
				exists, existsErr := engine.EntityExists(callerOf(tenant), id)
				ctx.Expect(existsErr).To(specs.BeNil())
				ctx.Expect(exists).To(specs.Equal(want))
			}

			// each tenant's state is stored under the business id
			for tenant, want := range map[string]uint64{"acme": 2, "globex": 1} {
				stored, getErr := states.GetLatestState(bg, tenantScopeOf(ctx, tenant), id)
				ctx.Expect(getErr).To(specs.BeNil())
				ctx.Expect(stored.GetPersistenceId()).To(specs.Equal(id))
				ctx.Expect(stored.GetVersionNumber()).To(specs.Equal(want))
			}
		})

		s.It("spawns two independent sagas, and each commands only its own tenant's entities", func(ctx *specs.Context) {
			events, states := connectedEventsStore(ctx), connectedDurableStore(ctx)
			engine := newIdentityEngine(ctx, "IdentitySaga", events, states)
			accountID := uuid.NewString()
			sagaID := "saga-" + uuid.NewString()

			// on every AccountCreated, credit that account by 5 in the saga's own tenant
			newSaga := func() *enginetest.CallbackSagaBehavior {
				return &enginetest.CallbackSagaBehavior{
					SagaID: sagaID,
					HandleEventFn: func(_ context.Context, event Event, _ State) (*SagaAction, error) {
						created, ok := event.(*testpb.AccountCreated)
						if !ok {
							return &SagaAction{}, nil
						}
						return &SagaAction{Commands: []SagaCommand{{
							EntityID: created.GetAccountId(),
							Command:  &testpb.CreditAccount{AccountId: created.GetAccountId(), Balance: 5},
							Timeout:  5 * time.Second,
						}}}, nil
					},
					HandleResultFn: func(context.Context, string, Event, State) (*SagaAction, error) {
						return &SagaAction{}, nil
					},
				}
			}

			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(accountID), acme)).To(specs.BeNil())
			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(accountID), globex)).To(specs.BeNil())
			ctx.Expect(engine.Saga(bg, newSaga(), 0, acme)).To(specs.BeNil())
			ctx.Expect(engine.Saga(bg, newSaga(), 0, globex)).To(specs.BeNil())
			ctx.Expect(engine.Saga(bg, newSaga(), 0, globex)).To(specs.BeNil())

			ctx.Expect(actorAlive(ctx, engine, qualifiedName(ctx, "acme", sagaID))).To(specs.BeTrue())
			ctx.Expect(actorAlive(ctx, engine, qualifiedName(ctx, "globex", sagaID))).To(specs.BeTrue())
			ctx.Expect(actorAlive(ctx, engine, sagaID)).To(specs.BeFalse())

			// each tenant reaches its own saga, and no other tenant's
			for tenant, wantErr := range map[string]bool{"acme": false, "globex": false, "initech": true} {
				info, err := engine.SagaStatus(callerOf(tenant), sagaID, 5*time.Second)
				if wantErr {
					ctx.Expect(err).To(specs.Not(specs.BeNil()))
					continue
				}
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(info.ID).To(specs.Equal(sagaID))
			}

			_, _, err := engine.SendCommand(callerOf("acme"), accountID, &testpb.CreateAccount{AccountBalance: 100}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			_, _, err = engine.SendCommand(callerOf("globex"), accountID, &testpb.CreateAccount{AccountBalance: 200}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())

			// every saga credited the account of its own tenant, once
			balance := func(tenant string) any {
				state, _, sendErr := engine.SendCommand(callerOf(tenant), accountID, &testpb.CreditAccount{AccountId: accountID, Balance: 0}, time.Minute)
				if sendErr != nil {
					return sendErr
				}
				return balanceOf(ctx, state)
			}
			ctx.Eventually(func() any { return balance("acme") }, specs.Equal(105.0), specs.WithTimeout(30*time.Second), specs.WithInterval(100*time.Millisecond))
			ctx.Eventually(func() any { return balance("globex") }, specs.Equal(205.0), specs.WithTimeout(30*time.Second), specs.WithInterval(100*time.Millisecond))
		})
	})
}

func TestEngineKeepsRejectingAccessToAnotherTenantsActor(t *testing.T) {
	specs.Describe(t, "access to another tenant's actor", func(s *specs.Spec) {
		bg := context.Background()
		acme := WithTenant(tenancy.TenantID("acme"))

		s.It("is not found through the engine, and never reaches HandleCommand", func(ctx *specs.Context) {
			engine := newIdentityEngine(ctx, "IdentityForeign", connectedEventsStore(ctx), connectedDurableStore(ctx))
			id := uuid.NewString()
			owner := newTenancyProbeEventSourcedBehavior(id)
			ctx.Expect(engine.Entity(bg, owner, acme)).To(specs.BeNil())

			_, _, err := engine.SendCommand(callerOf("globex"), id, &testpb.CreateAccount{AccountBalance: 1}, time.Minute)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(owner.InvocationCount()).To(specs.BeZero())
		})

		s.It("is denied for a caller that carries no tenant", func(ctx *specs.Context) {
			resolver := administrativeScopeResolver{}
			engine := startEngine(ctx, "IdentityAdmin", connectedEventsStore(ctx), WithTenantResolver(resolver))
			id := uuid.NewString()
			ctx.Expect(engine.Entity(bg, newTenancyProbeEventSourcedBehavior(id), acme)).To(specs.BeNil())

			adminCtx := context.WithValue(bg, administrativeScopeKey{}, true)
			_, _, err := engine.SendCommand(adminCtx, id, &testpb.CreateAccount{AccountBalance: 1}, time.Minute)
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrDenied))
			_, err = engine.EntityExists(adminCtx, id)
			ctx.Expect(err).To(specs.MatchError(tenancy.ErrDenied))
		})

		s.It("is rejected by the actor's own tenant gate when it is sent to it directly", func(ctx *specs.Context) {
			engine := newIdentityEngine(ctx, "IdentityDirect", connectedEventsStore(ctx), connectedDurableStore(ctx))
			id := uuid.NewString()
			owner := newTenancyProbeEventSourcedBehavior(id)
			ctx.Expect(engine.Entity(bg, owner, acme)).To(specs.BeNil())
			name := qualifiedName(ctx, "acme", id)
			noSender := engine.actorSystem.Load().noSender

			send := func(tenant string) error {
				tc, err := tenancy.NewTenantContext(tenancy.TenantID(tenant))
				ctx.Expect(err).To(specs.BeNil())
				sendCtx, err := tenancy.Attach(bg, tc)
				ctx.Expect(err).To(specs.BeNil())
				env := buildEnvelope(ctx, &testpb.CreateAccount{AccountBalance: 1})
				sendCtx = protocol.AttachCarrier(sendCtx, command.MarshalMetadata(env.Metadata()))
				reply, err := noSender.SendSync(sendCtx, name, env.Payload(), time.Minute)
				ctx.Expect(err).To(specs.BeNil())
				// the actor's refusal travels inside its CommandReply
				commandReply, ok := reply.(*egopb.CommandReply)
				ctx.Expect(ok).To(specs.BeTrue())
				_, _, replyErr := protocol.ParseCommandReply(commandReply)
				return replyErr
			}

			// VerifyUnchanged stays as a defense: a foreign identity that
			// reaches acme's actor is refused before HandleCommand runs
			ctx.Expect(send("globex")).To(specs.Not(specs.BeNil()))
			ctx.Expect(owner.InvocationCount()).To(specs.BeZero())
			// the owner is let through
			ctx.Expect(send("acme")).To(specs.BeNil())
			ctx.Expect(owner.InvocationCount()).To(specs.Equal(1))
		})

		s.It("cannot start an actor whose name is not its tenant's and id's", func(ctx *specs.Context) {
			engine := newIdentityEngine(ctx, "IdentityName", connectedEventsStore(ctx), connectedDurableStore(ctx))
			sys := engine.actorSystem.Load().sys
			id := uuid.NewString()

			names := map[string]string{
				"another tenant's name": qualifiedName(ctx, "globex", id),
				"the bare id":           id,
			}
			for label, name := range names {
				es, err := spawnDependency(sys, NewAccountEventSourcedBehavior(id))
				ctx.Expect(err).To(specs.BeNil())
				_, err = sys.Spawn(bg, "es-"+name, new(EventSourcedActor), goakt.WithDependencies(es,
					extensions.NewEntityConfig(0), extensions.NewEntityTenantScope("acme")))
				ctx.Expect(err).To(specs.Not(specs.BeNil()))
				_ = label
			}

			// the identical spawn under its own qualified name starts
			own := qualifiedName(ctx, "acme", id)
			es, err := spawnDependency(sys, NewAccountEventSourcedBehavior(id))
			ctx.Expect(err).To(specs.BeNil())
			_, err = sys.Spawn(bg, own, new(EventSourcedActor), goakt.WithDependencies(es,
				extensions.NewEntityConfig(0), extensions.NewEntityTenantScope("acme")))
			ctx.Expect(err).To(specs.BeNil())
		})
	})
}

func TestEngineSingleTenantKeepsItsActorNames(t *testing.T) {
	specs.Describe(t, "an engine with exactly one tenant", func(s *specs.Spec) {
		bg := context.Background()

		s.It("names the actor after the bare id in single-tenant mode", func(ctx *specs.Context) {
			resolver, err := tenancy.WithSingleTenant("acme")
			ctx.Expect(err).To(specs.BeNil())
			engine := startEngine(ctx, "IdentitySingle", connectedEventsStore(ctx), WithTenantResolver(resolver))
			id := uuid.NewString()

			// zero plumbing: no WithTenant, no tenant in the context
			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			ctx.Expect(actorAlive(ctx, engine, id)).To(specs.BeTrue())
			ctx.Expect(actorAlive(ctx, engine, qualifiedName(ctx, "acme", id))).To(specs.BeFalse())

			state, _, err := engine.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 7}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(7.0))
			exists, err := engine.EntityExists(bg, id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(exists).To(specs.BeTrue())

			// the single-tenant gate stays: another declared tenant is refused
			err = engine.Entity(bg, NewAccountEventSourcedBehavior(id), WithTenant("globex"))
			expectSpawnTenantMismatch(ctx, err)
		})

		s.It("names the actor after the bare id with no resolver at all", func(ctx *specs.Context) {
			engine := startEngine(ctx, "IdentityLegacy", connectedEventsStore(ctx))
			id := uuid.NewString()

			ctx.Expect(engine.Entity(bg, NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			ctx.Expect(actorAlive(ctx, engine, id)).To(specs.BeTrue())
			_, _, err := engine.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 1}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
		})
	})
}

func TestEngineRestartRecoversEachTenantsOwnState(t *testing.T) {
	specs.Describe(t, "an engine restarted over the same stores", func(s *specs.Spec) {
		bg := context.Background()
		acme := WithTenant(tenancy.TenantID("acme"))
		globex := WithTenant(tenancy.TenantID("globex"))

		restart := func(ctx *specs.Context, name string, first *Engine, events *testkit.EventStore, states *testkit.DurableStore) *Engine {
			sys := first.actorSystem.Load().sys
			ctx.Expect(first.Stop(bg)).To(specs.BeNil())
			ctx.Expect(sys.Stop(bg)).To(specs.BeNil())
			return newIdentityEngine(ctx, name, events, states)
		}

		s.It("recovers event-sourced and durable-state actors from their own tenant's scope", func(ctx *specs.Context) {
			events, states := connectedEventsStore(ctx), connectedDurableStore(ctx)
			first := newIdentityEngine(ctx, "RestartOne", events, states)
			esID, dsID := uuid.NewString(), uuid.NewString()

			for tenant, balance := range map[string]float64{"acme": 10, "globex": 20} {
				opt := WithTenant(tenancy.TenantID(tenant))
				ctx.Expect(first.Entity(bg, NewAccountEventSourcedBehavior(esID), opt)).To(specs.BeNil())
				ctx.Expect(first.DurableStateEntity(bg, NewAccountDurableStateBehavior(dsID), opt)).To(specs.BeNil())
				_, _, err := first.SendCommand(callerOf(tenant), esID, &testpb.CreateAccount{AccountBalance: balance}, time.Minute)
				ctx.Expect(err).To(specs.BeNil())
				_, _, err = first.SendCommand(callerOf(tenant), dsID, &testpb.CreateAccount{AccountBalance: balance}, time.Minute)
				ctx.Expect(err).To(specs.BeNil())
			}

			second := restart(ctx, "RestartTwo", first, events, states)

			// nothing is alive until each tenant spawns its own actor again
			exists, err := second.EntityExists(callerOf("acme"), esID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(exists).To(specs.BeFalse())

			for tenant, balance := range map[string]float64{"acme": 10, "globex": 20} {
				opt := WithTenant(tenancy.TenantID(tenant))
				ctx.Expect(second.Entity(bg, NewAccountEventSourcedBehavior(esID), opt)).To(specs.BeNil())
				ctx.Expect(second.DurableStateEntity(bg, NewAccountDurableStateBehavior(dsID), opt)).To(specs.BeNil())

				// the recovered state is the tenant's own, and it keeps going
				state, _, sendErr := second.SendCommand(callerOf(tenant), esID, &testpb.CreditAccount{AccountId: esID, Balance: 1}, time.Minute)
				ctx.Expect(sendErr).To(specs.BeNil())
				ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(balance + 1))
				state, _, sendErr = second.SendCommand(callerOf(tenant), dsID, &testpb.CreditAccount{AccountId: dsID, Balance: 1}, time.Minute)
				ctx.Expect(sendErr).To(specs.BeNil())
				ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(balance + 1))
			}
		})

		s.It("recovers a saga's tenant, so it commands only that tenant's entities", func(ctx *specs.Context) {
			events, states := connectedEventsStore(ctx), connectedDurableStore(ctx)
			first := newIdentityEngine(ctx, "RestartSagaOne", events, states)
			sagaID := "saga-" + uuid.NewString()
			newSaga := func() *enginetest.CallbackSagaBehavior {
				return &enginetest.CallbackSagaBehavior{
					SagaID: sagaID,
					HandleEventFn: func(_ context.Context, event Event, _ State) (*SagaAction, error) {
						created, ok := event.(*testpb.AccountCreated)
						if !ok {
							return &SagaAction{}, nil
						}
						return &SagaAction{Commands: []SagaCommand{{
							EntityID: created.GetAccountId(),
							Command:  &testpb.CreditAccount{AccountId: created.GetAccountId(), Balance: 5},
							Timeout:  5 * time.Second,
						}}}, nil
					},
					HandleResultFn: func(context.Context, string, Event, State) (*SagaAction, error) {
						return &SagaAction{}, nil
					},
				}
			}
			ctx.Expect(first.Saga(bg, newSaga(), 0, acme)).To(specs.BeNil())
			ctx.Expect(first.Saga(bg, newSaga(), 0, globex)).To(specs.BeNil())

			second := restart(ctx, "RestartSagaTwo", first, events, states)
			ctx.Expect(second.Saga(bg, newSaga(), 0, acme)).To(specs.BeNil())
			ctx.Expect(second.Saga(bg, newSaga(), 0, globex)).To(specs.BeNil())

			accountID := uuid.NewString()
			for tenant, balance := range map[string]float64{"acme": 100, "globex": 200} {
				ctx.Expect(second.Entity(bg, NewAccountEventSourcedBehavior(accountID), WithTenant(tenancy.TenantID(tenant)))).To(specs.BeNil())
				_, _, err := second.SendCommand(callerOf(tenant), accountID, &testpb.CreateAccount{AccountBalance: balance}, time.Minute)
				ctx.Expect(err).To(specs.BeNil())
			}
			for tenant, want := range map[string]float64{"acme": 105, "globex": 205} {
				ctx.Eventually(func() any {
					state, _, sendErr := second.SendCommand(callerOf(tenant), accountID, &testpb.CreditAccount{AccountId: accountID, Balance: 0}, time.Minute)
					if sendErr != nil {
						return sendErr
					}
					return balanceOf(ctx, state)
				}, specs.Equal(want), specs.WithTimeout(30*time.Second), specs.WithInterval(100*time.Millisecond))
			}
		})
	})
}

func TestEngineStoppingOneTenantsActorLeavesTheOthersAlive(t *testing.T) {
	specs.Describe(t, "stopping and respawning the actor of one tenant", func(s *specs.Spec) {
		bg := context.Background()
		acme := WithTenant(tenancy.TenantID("acme"))
		globex := WithTenant(tenancy.TenantID("globex"))

		// stop stops the actor named by (tenant, id) the way GoAkt addresses it:
		// by its qualified name. The engine has no stop-by-id API, so this is
		// the path passivation and shutdown take.
		stop := func(ctx *specs.Context, engine *Engine, tenant, id string) {
			ctx.Helper()
			ctx.Expect(engine.actorSystem.Load().sys.Kill(bg, qualifiedName(ctx, tenant, id))).To(specs.BeNil())
		}

		type entityKind struct {
			label string
			spawn func(engine *Engine, id string, opt SpawnOption) error
		}
		kinds := []entityKind{
			{"event-sourced", func(engine *Engine, id string, opt SpawnOption) error {
				return engine.Entity(bg, NewAccountEventSourcedBehavior(id), opt)
			}},
			{"durable-state", func(engine *Engine, id string, opt SpawnOption) error {
				return engine.DurableStateEntity(bg, NewAccountDurableStateBehavior(id), opt)
			}},
		}

		for _, kind := range kinds {
			s.It(kind.label+": the other tenant's actor stays alive, and the stopped one is respawned with its own state", func(ctx *specs.Context) {
				engine := newIdentityEngine(ctx, "IdentityStop", connectedEventsStore(ctx), connectedDurableStore(ctx))
				id := uuid.NewString()
				ctx.Expect(kind.spawn(engine, id, acme)).To(specs.BeNil())
				ctx.Expect(kind.spawn(engine, id, globex)).To(specs.BeNil())
				for tenant, balance := range map[string]float64{"acme": 10, "globex": 20} {
					_, _, err := engine.SendCommand(callerOf(tenant), id, &testpb.CreateAccount{AccountBalance: balance}, time.Minute)
					ctx.Expect(err).To(specs.BeNil())
				}

				stop(ctx, engine, "acme", id)

				ctx.Eventually(func() any {
					exists, err := engine.EntityExists(callerOf("acme"), id)
					return err == nil && !exists
				}, specs.BeTrue(), specs.WithTimeout(30*time.Second), specs.WithInterval(100*time.Millisecond))
				// the stopped actor is not found for its own tenant, and the command
				// is never delivered to the other tenant's actor with the same id
				_, _, err := engine.SendCommand(callerOf("acme"), id, &testpb.CreditAccount{AccountId: id, Balance: 1}, time.Minute)
				ctx.Expect(err).To(specs.Not(specs.BeNil()))
				// the other tenant's actor was not touched
				exists, err := engine.EntityExists(callerOf("globex"), id)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(exists).To(specs.BeTrue())
				state, _, err := engine.SendCommand(callerOf("globex"), id, &testpb.CreditAccount{AccountId: id, Balance: 1}, time.Minute)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(21.0))

				// respawn under the same identity recovers acme's own state
				ctx.Expect(kind.spawn(engine, id, acme)).To(specs.BeNil())
				state, _, err = engine.SendCommand(callerOf("acme"), id, &testpb.CreditAccount{AccountId: id, Balance: 1}, time.Minute)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(11.0))
			})
		}

		s.It("saga: the other tenant's saga stays alive, and the stopped one is respawned", func(ctx *specs.Context) {
			engine := newIdentityEngine(ctx, "IdentityStopSaga", connectedEventsStore(ctx), connectedDurableStore(ctx))
			sagaID := "saga-" + uuid.NewString()
			newSaga := func() *enginetest.CallbackSagaBehavior {
				return &enginetest.CallbackSagaBehavior{SagaID: sagaID}
			}
			ctx.Expect(engine.Saga(bg, newSaga(), 0, acme)).To(specs.BeNil())
			ctx.Expect(engine.Saga(bg, newSaga(), 0, globex)).To(specs.BeNil())

			stop(ctx, engine, "acme", sagaID)

			// wait for the actor to be gone from the system, not merely failing a
			// query: while it stops it is still registered, and a respawn would
			// find that dying instance
			ctx.Eventually(func() any {
				exists, err := engine.EntityExists(callerOf("acme"), sagaID)
				return err == nil && !exists
			}, specs.BeTrue(), specs.WithTimeout(30*time.Second), specs.WithInterval(100*time.Millisecond))
			_, statusErr := engine.SagaStatus(callerOf("acme"), sagaID, 5*time.Second)
			ctx.Expect(statusErr).To(specs.Not(specs.BeNil()))
			info, err := engine.SagaStatus(callerOf("globex"), sagaID, 5*time.Second)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(info.ID).To(specs.Equal(sagaID))

			ctx.Expect(engine.Saga(bg, newSaga(), 0, acme)).To(specs.BeNil())
			info, err = engine.SagaStatus(callerOf("acme"), sagaID, 5*time.Second)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(info.ID).To(specs.Equal(sagaID))
		})
	})
}
