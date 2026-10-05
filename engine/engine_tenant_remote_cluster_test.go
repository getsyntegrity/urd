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
	"encoding/json"
	"errors"
	"net"
	nethttp "net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/remote"
	"github.com/travisjeffery/go-dynaport"
	"google.golang.org/protobuf/proto"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"

	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

// remoteCluster is two started, clustered nodes that share their stores.
type remoteCluster struct {
	sys1, sys2       goakt.ActorSystem
	engine1, engine2 *Engine
	events           *testkit.EventStore
	states           *testkit.DurableStore
}

// startRemoteCluster starts two clustered nodes. newOptions returns the engine
// options of one node; the remoting config of each node gets the engine's own
// RemoteOptions, exactly as a deployment would pass them.
//
// An optional remoteOptions replaces, for the node it is called with (0 is the
// node that hosts in these tests, 1 the other), the engine's own RemoteOptions.
// It exists for the tests that put a hand-made propagator on one side of the
// wire; every other caller gets the real thing.
func startRemoteCluster(ctx *specs.Context, newOptions func() []Option, remoteOptions ...func(node int, cfg *Config) []remote.Option) *remoteCluster {
	bg := context.Background()
	host := "127.0.0.1"
	ports := dynaport.Get(6)
	gossipAddrs := []string{
		net.JoinHostPort(host, strconv.Itoa(ports[0])),
		net.JoinHostPort(host, strconv.Itoa(ports[3])),
	}
	rc := &remoteCluster{events: connectedEventsStore(ctx), states: connectedDurableStore(ctx)}
	// One in-process stream stands in for a distributed one, so that a saga on
	// one node sees the events of an entity hosted by the other.
	stream := eventstream.New()

	newNode := func(node, gossipPort, peersPort, remotingPort int) (goakt.ActorSystem, *Config) {
		opts := append([]Option{
			WithLogger(DiscardLogger),
			WithStateStore(rc.states),
			WithEventStream(stream),
			WithEntityKinds(new(AccountEventSourcedBehavior), new(AccountDurableStateBehavior), new(creditOnCreateSaga)),
		}, newOptions()...)
		cfg := NewConfig(rc.events, opts...)
		clusterCfg := goakt.NewClusterConfig().
			WithDiscovery(&mockClusterProvider{id: "test", peers: gossipAddrs}).
			WithDiscoveryPort(gossipPort).
			WithPeersPort(peersPort).
			WithMinimumPeersQuorum(1).
			WithReplicaCount(1).
			WithPartitionCount(7).
			WithKinds(ClusterKinds()...)
		remoteOpts := cfg.RemoteOptions()
		if len(remoteOptions) > 0 {
			remoteOpts = remoteOptions[0](node, cfg)
		}
		goaktOpts := append(cfg.GoaktOptions(),
			goakt.WithCluster(clusterCfg),
			goakt.WithRemote(remote.NewConfig(host, remotingPort, remoteOpts...)),
		)
		sys, err := goakt.NewActorSystem("Sample", goaktOpts...)
		ctx.Expect(err).To(specs.BeNil())
		return sys, cfg
	}
	var cfg1, cfg2 *Config
	rc.sys1, cfg1 = newNode(0, ports[0], ports[1], ports[2])
	rc.sys2, cfg2 = newNode(1, ports[3], ports[4], ports[5])

	errs := make(chan error, 2)
	go func() { errs <- rc.sys1.Start(bg) }()
	go func() { errs <- rc.sys2.Start(bg) }()
	ctx.Expect(<-errs).To(specs.BeNil())
	ctx.Expect(<-errs).To(specs.BeNil())
	ctx.Cleanup(func() {
		_ = rc.sys1.Stop(bg)
		_ = rc.sys2.Stop(bg)
	})
	ctx.Eventually(func() any {
		peers1, err1 := rc.sys1.Peers(bg, time.Second)
		peers2, err2 := rc.sys2.Peers(bg, time.Second)
		return err1 == nil && err2 == nil && len(peers1) == 1 && len(peers2) == 1
	}, specs.BeTrue(), specs.WithTimeout(30*time.Second), specs.WithInterval(500*time.Millisecond))

	var err error
	rc.engine1, err = NewEngine(rc.sys1, cfg1)
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(rc.engine1.Start(bg)).To(specs.BeNil())
	rc.engine2, err = NewEngine(rc.sys2, cfg2)
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(rc.engine2.Start(bg)).To(specs.BeNil())
	ctx.Cleanup(func() {
		_ = rc.engine1.Stop(bg)
		_ = rc.engine2.Stop(bg)
	})
	return rc
}

// hostedOn1Only fails the case unless the actor named name runs on node1, so
// that node2 reaches it over the wire.
func (rc *remoteCluster) expectRemoteFromNode2(ctx *specs.Context, name string) {
	ctx.Helper()
	pid, err := rc.sys2.ActorOf(context.Background(), name)
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(pid.IsRemote()).To(specs.BeTrue())
}

// rawSend sends a command straight to the actor system and reports a failure
// of the send or an error reply of the actor as a non-nil error.
func rawSend(sys goakt.ActorSystem, ctx context.Context, name string, cmd any) error {
	reply, err := sys.NoSender().SendSync(ctx, name, cmd.(proto.Message), 5*time.Second)
	if err != nil {
		return err
	}
	commandReply, ok := reply.(*egopb.CommandReply)
	if !ok {
		return errors.New("rejected: the reply is not a CommandReply")
	}
	if rejection := commandReply.GetErrorReply(); rejection != nil {
		return errors.New("rejected by the actor: " + rejection.GetMessage())
	}
	return nil
}

// expectCause fails unless err names cause, so that a rejection for the wrong
// reason (a timeout, a transport failure) does not pass for the right one.
func expectCause(ctx *specs.Context, err error, cause string) {
	ctx.Helper()
	ctx.Expect(err).To(specs.Not(specs.BeNil()))
	ctx.Expect(strings.Contains(err.Error(), cause)).To(specs.BeTrue())
}

func callerFor(tenant string) context.Context {
	return context.WithValue(context.Background(), perCallerTenantKey{}, tenant)
}

func tenantMultiResolverOptions() []Option {
	return []Option{WithTenantResolver(perCallerTenantResolver{})}
}

// TestClusterTenantRemoteCommands proves that a command sent from the node that
// does NOT host the actor reaches the actor of the caller's own tenant, with
// that tenant's scope on what it persists, when two tenants share an entity ID.
func TestClusterTenantRemoteCommands(t *testing.T) {
	specs.Describe(t, "commands from the non-host node of a two-node cluster", func(s *specs.Spec) {
		s.It("reaches each tenant's own actor, persists under its scope and fails closed on a forged identity", func(ctx *specs.Context) {
			bg := context.Background()
			rc := startRemoteCluster(ctx, tenantMultiResolverOptions)

			esID, dsID := uuid.NewString(), uuid.NewString()
			balances := map[string]float64{"acme": 10, "globex": 20}
			for tenant := range balances {
				on := []SpawnOption{WithTenant(tenancy.TenantID(tenant)), WithPlacement(Local)}
				ctx.Expect(rc.engine1.Entity(bg, NewAccountEventSourcedBehavior(esID), on...)).To(specs.BeNil())
				ctx.Expect(rc.engine1.DurableStateEntity(bg, NewAccountDurableStateBehavior(dsID), on...)).To(specs.BeNil())
				rc.expectRemoteFromNode2(ctx, qualifiedName(ctx, tenant, esID))
				rc.expectRemoteFromNode2(ctx, qualifiedName(ctx, tenant, dsID))
			}

			for tenant, balance := range balances {
				for _, id := range []string{esID, dsID} {
					state, _, err := rc.engine2.SendCommand(callerFor(tenant), id, &testpb.CreateAccount{AccountBalance: balance}, time.Minute)
					ctx.Expect(err).To(specs.BeNil())
					ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(balance))
				}
			}

			// what each tenant persisted carries that tenant's scope
			for tenant := range balances {
				scope := tenantScopeOf(ctx, tenant)
				latest, err := rc.events.GetLatestEvent(bg, scope, esID)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(latest).To(specs.Not(specs.BeNil()))
				ctx.Expect(latest.GetSequenceNumber()).To(specs.Equal(uint64(1)))
				stored, err := rc.states.GetLatestState(bg, scope, dsID)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(stored).To(specs.Not(specs.BeNil()))
			}
			none, err := rc.events.GetLatestEvent(bg, tenantScopeOf(ctx, "initech"), esID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(none).To(specs.BeNil())

			// a tenant that no actor was spawned for is not found, not routed
			_, _, err = rc.engine2.SendCommand(callerFor("initech"), esID, &testpb.CreditAccount{AccountId: esID, Balance: 1}, time.Minute)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))

			// Rejections, straight at the actor system so that no engine
			// resolver stands between the forged identity and the wire.
			acmeActor := qualifiedName(ctx, "acme", esID)
			credit := &testpb.CreditAccount{AccountId: esID, Balance: 1}
			send := func(c context.Context) error {
				return rawSend(rc.sys2, c, acmeActor, credit)
			}
			attach := func(tc tenancy.TenantContext, err error) context.Context {
				ctx.Expect(err).To(specs.BeNil())
				attached, attachErr := tenancy.Attach(bg, tc)
				ctx.Expect(attachErr).To(specs.BeNil())
				return attached
			}
			// no identity at all
			expectCause(ctx, send(bg), "no tenant identity attached")
			// another tenant's identity aimed at acme's actor name
			expectCause(ctx, send(attach(tenancy.NewTenantContext("globex"))), "tenant identity changed across a boundary")
			// an administrative scope is never sent to another node (the receive side is
			// covered by TestTenantPropagatorRejectsHostileInput)
			admin, adminErr := tenancy.NewAdministrative("ops", "audit")
			ctx.Expect(adminErr).To(specs.BeNil())
			expectCause(ctx, send(attach(tenancy.NewAdministrativeContext(admin))), "administrative scope is never propagated")

			// none of the rejected commands wrote anything
			for tenant := range balances {
				latest, getErr := rc.events.GetLatestEvent(bg, tenantScopeOf(ctx, tenant), esID)
				ctx.Expect(getErr).To(specs.BeNil())
				ctx.Expect(latest.GetSequenceNumber()).To(specs.Equal(uint64(1)))
			}
			// while the right identity on the same name is accepted
			ctx.Expect(send(attach(tenancy.NewTenantContext("acme")))).To(specs.BeNil())
		})
	})
}

// TestClusterTenantRemoteSaga proves SagaStatus from the non-host node and a
// saga that commands, across nodes, the entity of its own tenant.
func TestClusterTenantRemoteSaga(t *testing.T) {
	specs.Describe(t, "sagas across a two-node cluster", func(s *specs.Spec) {
		s.It("reads a remote saga's status and commands a remote entity of its own tenant", func(ctx *specs.Context) {
			bg := context.Background()
			rc := startRemoteCluster(ctx, tenantMultiResolverOptions)

			// both sagas share an ID and run on node1
			sagaID := "saga-" + uuid.NewString()
			for _, tenant := range []string{"acme", "globex"} {
				ctx.Expect(rc.engine1.Saga(bg, &creditOnCreateSaga{sagaID: sagaID}, 0, WithTenant(tenancy.TenantID(tenant)))).To(specs.BeNil())
				rc.expectRemoteFromNode2(ctx, qualifiedName(ctx, tenant, sagaID))
			}
			for _, tenant := range []string{"acme", "globex"} {
				info, err := rc.engine2.SagaStatus(callerFor(tenant), sagaID, 5*time.Second)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(info.ID).To(specs.Equal(sagaID))
			}
			_, err := rc.engine2.SagaStatus(callerFor("initech"), sagaID, 5*time.Second)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))

			// the accounts live on node2, so each saga's command crosses nodes
			accountID := uuid.NewString()
			for tenant, balance := range map[string]float64{"acme": 100, "globex": 200} {
				ctx.Expect(rc.engine2.Entity(bg, NewAccountEventSourcedBehavior(accountID), WithTenant(tenancy.TenantID(tenant)), WithPlacement(Local))).To(specs.BeNil())
				_, _, sendErr := rc.engine2.SendCommand(callerFor(tenant), accountID, &testpb.CreateAccount{AccountBalance: balance}, time.Minute)
				ctx.Expect(sendErr).To(specs.BeNil())
			}
			for tenant, want := range map[string]float64{"acme": 105, "globex": 205} {
				// create + the saga's single credit, under this tenant's scope
				ctx.Eventually(func() any {
					latest, getErr := rc.events.GetLatestEvent(bg, tenantScopeOf(ctx, tenant), accountID)
					if getErr != nil || latest == nil {
						return uint64(0)
					}
					return latest.GetSequenceNumber()
				}, specs.Equal(uint64(2)), specs.WithTimeout(60*time.Second), specs.WithInterval(100*time.Millisecond))
				state, _, sendErr := rc.engine2.SendCommand(callerFor(tenant), accountID, &testpb.CreditAccount{AccountId: accountID, Balance: 0}, time.Minute)
				ctx.Expect(sendErr).To(specs.BeNil())
				ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(want))
			}
		})
	})
}

// TestClusterTenantRemoteRelocation proves that, after the hosting node leaves,
// the relocated actors keep serving the right tenant through the surviving node.
func TestClusterTenantRemoteRelocation(t *testing.T) {
	specs.Describe(t, "relocation of tenant-aware actors reached from the other node", func(s *specs.Spec) {
		s.It("keeps each tenant's own actor reachable after its host leaves", func(ctx *specs.Context) {
			bg := context.Background()
			rc := startRemoteCluster(ctx, tenantMultiResolverOptions)

			id := uuid.NewString()
			balances := map[string]float64{"acme": 10, "globex": 20}
			for tenant, balance := range balances {
				on := []SpawnOption{WithTenant(tenancy.TenantID(tenant)), WithPlacement(Local), WithRelocation(true)}
				ctx.Expect(rc.engine2.Entity(bg, NewAccountEventSourcedBehavior(id), on...)).To(specs.BeNil())
				// node2 hosts it, node1 reaches it over the wire
				state, _, err := rc.engine1.SendCommand(callerFor(tenant), id, &testpb.CreateAccount{AccountBalance: balance}, time.Minute)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(balance))
			}

			ctx.Expect(rc.engine2.Stop(bg)).To(specs.BeNil())
			ctx.Expect(rc.sys2.Stop(bg)).To(specs.BeNil())

			for tenant, balance := range balances {
				ctx.Eventually(func() any {
					state, _, err := rc.engine1.SendCommand(callerFor(tenant), id, &testpb.CreditAccount{AccountId: id, Balance: 1}, time.Minute)
					if err != nil {
						return err
					}
					return balanceOf(ctx, state)
				}, specs.Equal(balance+1), specs.WithTimeout(60*time.Second), specs.WithInterval(500*time.Millisecond))
			}
		})
	})
}

// TestClusterTenantRemoteSingleTenantAndLegacy proves the compatibility modes:
// a single-tenant cluster accepts only its tenant over the wire, and a legacy
// cluster (no resolver) still works from the non-host node.
func TestClusterTenantRemoteSingleTenantAndLegacy(t *testing.T) {
	specs.Describe(t, "single-tenant and legacy clusters", func(s *specs.Spec) {
		s.It("serves the fixed tenant, rejects another, and leaves legacy untouched", func(ctx *specs.Context) {
			bg := context.Background()

			resolver, err := tenancy.WithSingleTenant("acme")
			ctx.Expect(err).To(specs.BeNil())
			single := startRemoteCluster(ctx, func() []Option { return []Option{WithTenantResolver(resolver)} })
			id := uuid.NewString()
			ctx.Expect(single.engine1.Entity(bg, NewAccountEventSourcedBehavior(id), WithPlacement(Local))).To(specs.BeNil())
			single.expectRemoteFromNode2(ctx, id)
			state, _, err := single.engine2.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 7}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(float64(7)))
			scope, err := persistence.NewTenantScope("acme")
			ctx.Expect(err).To(specs.BeNil())
			latest, err := single.events.GetLatestEvent(bg, scope, id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest.GetSequenceNumber()).To(specs.Equal(uint64(1)))

			other, err := tenancy.NewTenantContext("globex")
			ctx.Expect(err).To(specs.BeNil())
			forged, err := tenancy.Attach(bg, other)
			ctx.Expect(err).To(specs.BeNil())
			err = rawSend(single.sys2, forged, id, &testpb.CreditAccount{AccountId: id, Balance: 1})
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			latest, err = single.events.GetLatestEvent(bg, scope, id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest.GetSequenceNumber()).To(specs.Equal(uint64(1)))
		})

		s.It("keeps a legacy cluster working with no propagation", func(ctx *specs.Context) {
			bg := context.Background()
			legacy := startRemoteCluster(ctx, func() []Option { return nil })
			id := uuid.NewString()
			ctx.Expect(legacy.engine1.Entity(bg, NewAccountEventSourcedBehavior(id), WithPlacement(Local))).To(specs.BeNil())
			legacy.expectRemoteFromNode2(ctx, id)
			state, _, err := legacy.engine2.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 3}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(float64(3)))
		})
	})
}

// forgedHeadersKey marks a context whose outbound headers forgedInjector must
// write verbatim, instead of what the engine's propagator would write.
type forgedHeadersKey struct{}

// forgedInjector is a TEST-ONLY propagator for the SENDING node. The real
// Inject refuses an administrative scope, so the real sender can never emit
// one; to put such a header on the real wire without changing production, this
// writes the headers named by forgedHeadersKey exactly as given, and defers to
// the real tenantPropagator for every other context, so ordinary calls from
// this node are unchanged.
type forgedInjector struct{ real tenantPropagator }

func (f forgedInjector) Inject(ctx context.Context, headers nethttp.Header) error {
	forged, ok := ctx.Value(forgedHeadersKey{}).(map[string]string)
	if !ok {
		return f.real.Inject(ctx, headers)
	}
	for name, value := range forged {
		headers.Set(name, value)
	}
	return nil
}

func (f forgedInjector) Extract(ctx context.Context, headers nethttp.Header) (context.Context, error) {
	return f.real.Extract(ctx, headers)
}

// recordingExtractor is a TEST-ONLY wrapper for the RECEIVING node. It records
// the exact inbound http.Header GoAkt hands to the propagator and then
// delegates to the REAL tenantPropagator.Extract, so the rejection under test
// is the production one.
type recordingExtractor struct {
	real tenantPropagator

	mu       sync.Mutex
	received []receivedHeaders
}

// receivedHeaders is one inbound Extract that carried an Urd header.
type receivedHeaders struct {
	// values maps each Urd header name, as GoAkt canonicalised it, to every
	// value that arrived under it.
	values map[string][]string
	err    error
}

func (r *recordingExtractor) Inject(ctx context.Context, headers nethttp.Header) error {
	return r.real.Inject(ctx, headers)
}

func (r *recordingExtractor) Extract(ctx context.Context, headers nethttp.Header) (context.Context, error) {
	got, err := r.real.Extract(ctx, headers)
	seen := map[string][]string{}
	for name, values := range headers {
		if strings.HasPrefix(strings.ToLower(name), "urd-") {
			seen[name] = append([]string(nil), values...)
		}
	}
	if len(seen) > 0 {
		r.mu.Lock()
		r.received = append(r.received, receivedHeaders{values: seen, err: err})
		r.mu.Unlock()
	}
	return got, err
}

func (r *recordingExtractor) snapshot() []receivedHeaders {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedHeaders(nil), r.received...)
}

// TestClusterTenantRemoteRejectsForgedHeaderOnTheWire proves, over the real
// GoAkt remoting transport between two nodes, that the receiving node rejects a
// forged identity header before any handler runs, writes nothing, and keeps
// serving a valid call afterwards.
//
// What the receiving node's propagator was handed, observed with
// recordingExtractor and asserted below: GoAkt passes the propagator an
// http.Header whose keys are canonicalised (Urd-Wire-Version, Urd-Tenant,
// Urd-Command), one value each. The values arrive byte for byte as sent, so the
// administrative marker arrives intact:
//
//	Urd-Wire-Version: 1
//	Urd-Tenant: {"ego.tenant.admin_actor":"ops","ego.tenant.admin_reason":"audit","ego.tenant.scope":"administrative"}
//
// The rejection is the scope check of decodeTenant, engine/tenant_propagator.go
// lines 208-209 (`tc.Scope() != tenancy.ScopeTenant`), after
// tenancy.UnmarshalMetadata accepted the metadata as a well-formed
// administrative context. GoAkt turns the Extract error into an INVALID_ARGUMENT
// reply, so the caller gets exactly:
//
//	invalid argument: engine: remote tenant identity rejected: administrative scope is rejected on the wire
//
// and the command never reaches the actor.
func TestClusterTenantRemoteRejectsForgedHeaderOnTheWire(t *testing.T) {
	specs.Describe(t, "a forged identity header sent over the real remote transport", func(s *specs.Spec) {
		s.It("is rejected at the hosting node with no handler call and no write, and a valid call still works", func(ctx *specs.Context) {
			bg := context.Background()
			recorder := &recordingExtractor{}
			rc := startRemoteCluster(ctx, func() []Option {
				return append(tenantMultiResolverOptions(), WithEntityKinds(new(tenancyProbeEventSourcedBehavior)))
			}, func(node int, cfg *Config) []remote.Option {
				if node == 0 {
					// the hosting node: the real propagator behind a recorder
					return []remote.Option{remote.WithContextPropagator(recorder)}
				}
				// the sending node: forges on request, real otherwise
				return []remote.Option{remote.WithContextPropagator(forgedInjector{})}
			})

			id := uuid.NewString()
			probe := newTenancyProbeEventSourcedBehavior(id)
			ctx.Expect(rc.engine1.Entity(bg, probe, WithTenant("acme"), WithPlacement(Local))).To(specs.BeNil())
			actorName := qualifiedName(ctx, "acme", id)
			rc.expectRemoteFromNode2(ctx, actorName)

			adminTenant, err := tenancy.NewAdministrative("ops", "audit")
			ctx.Expect(err).To(specs.BeNil())
			adminContext, err := tenancy.NewAdministrativeContext(adminTenant)
			ctx.Expect(err).To(specs.BeNil())
			adminRaw, err := json.Marshal(tenancy.MarshalMetadata(adminContext))
			ctx.Expect(err).To(specs.BeNil())
			acmeContext, err := tenancy.NewTenantContext("acme")
			ctx.Expect(err).To(specs.BeNil())
			acmeRaw, err := json.Marshal(tenancy.MarshalMetadata(acmeContext))
			ctx.Expect(err).To(specs.BeNil())

			forgeries := []struct {
				name    string
				headers map[string]string
				cause   string
			}{
				// the required case: an administrative scope in the tenant header
				{"administrative scope", map[string]string{
					wireHeaderVersion: wireVersion, wireHeaderTenant: string(adminRaw),
				}, "administrative scope is rejected on the wire"},
				// a tenant identity with no version header
				{"missing wire version", map[string]string{
					wireHeaderTenant: string(acmeRaw),
				}, "unsupported or missing wire version"},
				// a tenant identity smuggled in the command header as well
				{"tenant in the command header", map[string]string{
					wireHeaderVersion: wireVersion, wireHeaderTenant: string(acmeRaw),
					wireHeaderCommand: `{"ego.tenant.id":"globex"}`,
				}, "command header must not carry a tenant"},
			}

			noWrites := func() {
				for _, tenant := range []string{"acme", "globex"} {
					latest, getErr := rc.events.GetLatestEvent(bg, tenantScopeOf(ctx, tenant), id)
					ctx.Expect(getErr).To(specs.BeNil())
					ctx.Expect(latest).To(specs.BeNil())
					state, stateErr := rc.states.GetLatestState(bg, tenantScopeOf(ctx, tenant), id)
					ctx.Expect(stateErr).To(specs.BeNil())
					ctx.Expect(state).To(specs.BeNil())
				}
				latest, getErr := rc.events.GetLatestEvent(bg, persistence.Unscoped(), id)
				ctx.Expect(getErr).To(specs.BeNil())
				ctx.Expect(latest).To(specs.BeNil())
				state, stateErr := rc.states.GetLatestState(bg, persistence.Unscoped(), id)
				ctx.Expect(stateErr).To(specs.BeNil())
				ctx.Expect(state).To(specs.BeNil())
			}

			for i, forgery := range forgeries {
				forged := context.WithValue(bg, forgedHeadersKey{}, forgery.headers)
				_, sendErr := rc.sys2.NoSender().SendSync(forged, actorName, &testpb.CreateAccount{AccountBalance: 1}, 5*time.Second)

				// (1) an explicit rejection by the receiver, not a timeout or a
				// transport failure: GoAkt's INVALID_ARGUMENT carrying the cause
				ctx.Expect(sendErr).To(specs.Not(specs.BeNil()))
				ctx.Expect(errors.Is(sendErr, context.DeadlineExceeded)).To(specs.BeFalse())
				expectCause(ctx, sendErr, forgery.cause)
				expectCause(ctx, sendErr, "invalid argument: engine: remote tenant identity rejected")

				// what the server received: exactly this header set, canonical
				// names, one value each, the marker intact, and the real Extract
				// refused it with the expected cause
				received := recorder.snapshot()
				ctx.Expect(len(received)).To(specs.Equal(i + 1))
				last := received[i]
				ctx.Expect(len(last.values)).To(specs.Equal(len(forgery.headers)))
				for name, value := range forgery.headers {
					ctx.Expect(last.values[name]).To(specs.Equal([]string{value}))
				}
				ctx.Expect(last.err).To(specs.Not(specs.BeNil()))
				ctx.Expect(errors.Is(last.err, errRemoteIdentityRejected)).To(specs.BeTrue())
				ctx.Expect(strings.Contains(last.err.Error(), forgery.cause)).To(specs.BeTrue())

				// (2) the handler never ran, (3) nothing was written anywhere
				ctx.Expect(probe.InvocationCount()).To(specs.Equal(0))
				noWrites()
			}

			// (4) the receiver is not broken: a valid call through the real
			// propagator path succeeds and persists under the right scope
			state, _, err := rc.engine2.SendCommand(callerFor("acme"), id, &testpb.CreateAccount{AccountBalance: 5}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(balanceOf(ctx, state)).To(specs.Equal(float64(5)))
			ctx.Expect(probe.InvocationCount()).To(specs.Equal(1))
			latest, err := rc.events.GetLatestEvent(bg, tenantScopeOf(ctx, "acme"), id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest.GetSequenceNumber()).To(specs.Equal(uint64(1)))
			other, err := rc.events.GetLatestEvent(bg, tenantScopeOf(ctx, "globex"), id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(other).To(specs.BeNil())
			unscoped, err := rc.events.GetLatestEvent(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(unscoped).To(specs.BeNil())
		})
	})
}
