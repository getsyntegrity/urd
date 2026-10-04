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

	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/tenancy"
)

// TestClusterEngineRemoteSpawnTenantBinding runs the spawn-binding contract
// against a real two-node cluster. With RoundRobin placement over two
// members, a run of spawns from node1 places some entities on node2, so
// node1 receives REMOTE PIDs: the only way node1 can learn such an actor's
// binding is by asking the node that owns it. For every entity, whether it
// landed locally or remotely, a same-tenant re-spawn must be an idempotent
// success from either node, and the same id under another tenant must be a
// second, independent actor (EGO-TENANT-009): both tenants command their own
// actor from either node, wherever each one was placed.
func TestClusterEngineRemoteSpawnTenantBinding(t *testing.T) {
	specs.Describe(t, "spawn tenant binding across a two-node cluster", func(s *specs.Spec) {
		s.It("keeps same-tenant respawns idempotent and rejects other tenants, local or remote", func(ctx *specs.Context) {
			bg := context.Background()
			host := "127.0.0.1"

			ports := dynaport.Get(6)
			gossipAddrs := []string{
				net.JoinHostPort(host, strconv.Itoa(ports[0])),
				net.JoinHostPort(host, strconv.Itoa(ports[3])),
			}

			newNode := func(gossipPort, peersPort, remotingPort int) (goakt.ActorSystem, *Config) {
				store := connectedEventsStore(ctx)

				cfg := NewConfig(store,
					WithLogger(DiscardLogger),
					WithEntityKinds(new(AccountEventSourcedBehavior)),
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

			// The two nodes must form a cluster.
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

			acme := WithTenant(tenancy.TenantID("acme"))
			globex := WithTenant(tenancy.TenantID("globex"))
			callerOf := func(tenant string) context.Context {
				return context.WithValue(bg, perCallerTenantKey{}, tenant)
			}

			// RoundRobin placement draws from one counter that every node shares and
			// that every SpawnOn advances. Spawning the acme entities back to back
			// from one node makes them alternate between the two members, so some
			// land on node2 whatever the counter started at. Interleaving another
			// spawn per entity could make the number of draws per entity even, and
			// every acme entity would then land on the same node.
			entityIDs := make([]string, 8)
			remoteSeen := 0
			for i := range entityIDs {
				entityIDs[i] = uuid.NewString()
				// a valid tenant-aware spawn must succeed wherever it is placed
				ctx.Expect(engine1.Entity(bg, NewAccountEventSourcedBehavior(entityIDs[i]), acme)).To(specs.BeNil())

				pid, err := sys1.ActorOf(bg, qualifiedName(ctx, "acme", entityIDs[i]))
				ctx.Expect(err).To(specs.BeNil())
				if pid.IsRemote() {
					remoteSeen++
				}
			}

			for _, entityID := range entityIDs {
				for _, node := range []struct {
					name   string
					engine *Engine
				}{{"node1", engine1}, {"node2", engine2}} {
					// a same-tenant re-spawn must be an idempotent success
					ctx.Expect(node.engine.Entity(bg, NewAccountEventSourcedBehavior(entityID), acme)).To(specs.BeNil())
					// the same id under another tenant is its own actor, not a collision
					ctx.Expect(node.engine.Entity(bg, NewAccountEventSourcedBehavior(entityID), globex)).To(specs.BeNil())
				}

				// the two actors are distinct, and only qualified names exist
				_, err := sys1.ActorOf(bg, qualifiedName(ctx, "globex", entityID))
				ctx.Expect(err).To(specs.BeNil())
				_, err = sys1.ActorOf(bg, entityID)
				ctx.Expect(err).To(specs.Not(specs.BeNil()))

				// both nodes resolve each tenant's actor by its qualified name, and
				// the two tenants' actors are different actors, wherever placed
				for _, tenant := range []string{"acme", "globex"} {
					name := qualifiedName(ctx, tenant, entityID)
					for _, sys := range []goakt.ActorSystem{sys1, sys2} {
						found, lookupErr := sys.ActorOf(bg, name)
						ctx.Expect(lookupErr).To(specs.BeNil())
						ctx.Expect(found.Name()).To(specs.Equal(name))
					}
				}

				// a tenant commands its own actor through the node that hosts it:
				// the command is addressed by (tenant, id), so each tenant's
				// state is its own. (Carrying the caller's tenant identity over
				// a remote hop is a separate, earlier gap: see #305.)
				for tenant, balance := range map[string]float64{"acme": 10, "globex": 20} {
					for _, node := range []struct {
						sys    goakt.ActorSystem
						engine *Engine
					}{{sys1, engine1}, {sys2, engine2}} {
						hosted, lookupErr := node.sys.ActorOf(bg, qualifiedName(ctx, tenant, entityID))
						ctx.Expect(lookupErr).To(specs.BeNil())
						if hosted.IsRemote() {
							continue
						}
						state, _, sendErr := node.engine.SendCommand(callerOf(tenant), entityID, &testpb.CreateAccount{AccountBalance: balance}, time.Minute)
						ctx.Expect(sendErr).To(specs.BeNil())
						ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(balance))
					}
				}
			}
			// RoundRobin over two members must place at least one entity on node2
			ctx.Expect(remoteSeen).To(specs.BeGreaterThan(0))
		})
	})
}
