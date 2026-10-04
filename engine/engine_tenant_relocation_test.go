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
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/remote"
	"github.com/travisjeffery/go-dynaport"

	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/tenancy"
)

// TestClusterEngineRelocatedActorRecoversItsOwnTenant proves how an actor that
// GoAkt relocates to another node gets its tenant back before it reads a
// store (EGO-TENANT-009). Relocation rebuilds the actor from what GoAkt
// serialized with it: its name, the behavior, and the EntityTenantScope
// dependency. The new incarnation derives its identity from that tenant and
// the behavior's ID, checks it against the name it was relocated under, and
// only then recovers, from its own tenant's scope. Two tenants share every
// entity ID here, so recovering from the wrong scope, or binding the wrong
// tenant, would surface as the other tenant's balance or as a refused start.
func TestClusterEngineRelocatedActorRecoversItsOwnTenant(t *testing.T) {
	specs.Describe(t, "relocating tenant-aware actors to the surviving node", func(s *specs.Spec) {
		s.It("restarts each actor under its own (tenant, id) and recovers that tenant's state", func(ctx *specs.Context) {
			bg := context.Background()
			host := "127.0.0.1"

			ports := dynaport.Get(6)
			gossipAddrs := []string{
				net.JoinHostPort(host, strconv.Itoa(ports[0])),
				net.JoinHostPort(host, strconv.Itoa(ports[3])),
			}

			// the stores are shared: relocated actors recover from them
			events, states := connectedEventsStore(ctx), connectedDurableStore(ctx)

			newNode := func(gossipPort, peersPort, remotingPort int) (goakt.ActorSystem, *Config) {
				cfg := NewConfig(events,
					WithLogger(DiscardLogger),
					WithStateStore(states),
					WithEntityKinds(new(AccountEventSourcedBehavior), new(AccountDurableStateBehavior), new(enginetest.CallbackSagaBehavior)),
					WithTenantResolver(perCallerTenantResolver{}),
				)

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
				ctx.Expect(err).To(specs.BeNil())
				return sys, cfg
			}

			sys1, cfg1 := newNode(ports[0], ports[1], ports[2])
			sys2, cfg2 := newNode(ports[3], ports[4], ports[5])

			errs := make(chan error, 2)
			go func() { errs <- sys1.Start(bg) }()
			go func() { errs <- sys2.Start(bg) }()
			ctx.Expect(<-errs).To(specs.BeNil())
			ctx.Expect(<-errs).To(specs.BeNil())
			ctx.Cleanup(func() {
				_ = sys1.Stop(bg)
				_ = sys2.Stop(bg)
			})

			ctx.Eventually(func() any {
				peers1, err1 := sys1.Peers(bg, time.Second)
				peers2, err2 := sys2.Peers(bg, time.Second)
				return err1 == nil && err2 == nil && len(peers1) == 1 && len(peers2) == 1
			}, specs.BeTrue(), specs.WithTimeout(30*time.Second), specs.WithInterval(500*time.Millisecond))

			engine1, err := NewEngine(sys1, cfg1)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(engine1.Start(bg)).To(specs.BeNil())
			engine2, err := NewEngine(sys2, cfg2)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(engine2.Start(bg)).To(specs.BeNil())
			ctx.Cleanup(func() {
				_ = engine1.Stop(bg)
				_ = engine2.Stop(bg)
			})

			type actorKind struct {
				label string
				spawn func(tenant, id string) error
			}
			relocatable := func(tenant string) []SpawnOption {
				return []SpawnOption{WithTenant(tenancy.TenantID(tenant)), WithRelocation(true)}
			}
			kinds := []actorKind{
				{"event-sourced", func(tenant, id string) error {
					return engine1.Entity(bg, NewAccountEventSourcedBehavior(id), relocatable(tenant)...)
				}},
				{"durable-state", func(tenant, id string) error {
					return engine1.DurableStateEntity(bg, NewAccountDurableStateBehavior(id), relocatable(tenant)...)
				}},
				{"saga", func(tenant, id string) error {
					return engine1.Saga(bg, &enginetest.CallbackSagaBehavior{SagaID: id}, 0, relocatable(tenant)...)
				}},
			}
			tenants := map[string]float64{"acme": 10, "globex": 20}

			// the same id for both tenants, per kind
			ids := map[string]string{}
			onNode2 := 0
			for _, kind := range kinds {
				ids[kind.label] = uuid.NewString()
				if kind.label == "saga" {
					ids[kind.label] = "saga-" + ids[kind.label]
				}
				for tenant := range tenants {
					ctx.Expect(kind.spawn(tenant, ids[kind.label])).To(specs.BeNil())
				}
			}

			// write each tenant's state through the node that hosts its actor
			hostOf := func(tenant, id string) (*Engine, bool) {
				for _, node := range []struct {
					sys    goakt.ActorSystem
					engine *Engine
				}{{sys1, engine1}, {sys2, engine2}} {
					pid, lookupErr := node.sys.ActorOf(bg, qualifiedName(ctx, tenant, id))
					if lookupErr == nil && !pid.IsRemote() {
						return node.engine, true
					}
				}
				return nil, false
			}
			for _, label := range []string{"event-sourced", "durable-state"} {
				for tenant, balance := range tenants {
					host, found := hostOf(tenant, ids[label])
					ctx.Expect(found).To(specs.BeTrue())
					if host == engine2 {
						onNode2++
					}
					_, _, sendErr := host.SendCommand(callerOf(tenant), ids[label], &testpb.CreateAccount{AccountBalance: balance}, time.Minute)
					ctx.Expect(sendErr).To(specs.BeNil())
				}
			}
			// RoundRobin over two members puts some of these actors on node2,
			// the node that is about to leave
			ctx.Expect(onNode2).To(specs.BeGreaterThan(0))

			// node2 leaves: GoAkt relocates what it hosts to node1
			ctx.Expect(engine2.Stop(bg)).To(specs.BeNil())
			ctx.Expect(sys2.Stop(bg)).To(specs.BeNil())

			for _, label := range []string{"event-sourced", "durable-state"} {
				for tenant, balance := range tenants {
					// the relocated actor recovered its own tenant's state, and
					// no other tenant's, and keeps serving under the same id
					ctx.Eventually(func() any {
						state, _, sendErr := engine1.SendCommand(callerOf(tenant), ids[label], &testpb.CreditAccount{AccountId: ids[label], Balance: 1}, time.Minute)
						if sendErr != nil {
							return sendErr
						}
						return balanceOf(ctx, state)
					}, specs.Equal(balance+1), specs.WithTimeout(60*time.Second), specs.WithInterval(500*time.Millisecond))
				}
			}

			// a relocated saga restarts under its own tenant, and only that
			// tenant reads it
			for tenant := range tenants {
				ctx.Eventually(func() any {
					info, statusErr := engine1.SagaStatus(callerOf(tenant), ids["saga"], 5*time.Second)
					if statusErr != nil {
						return statusErr
					}
					return info.ID
				}, specs.Equal(ids["saga"]), specs.WithTimeout(60*time.Second), specs.WithInterval(500*time.Millisecond))
			}
			_, statusErr := engine1.SagaStatus(callerOf("initech"), ids["saga"], 5*time.Second)
			ctx.Expect(statusErr).To(specs.Not(specs.BeNil()))
		})
	})
}
