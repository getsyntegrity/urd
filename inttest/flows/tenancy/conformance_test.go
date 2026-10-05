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

package tenancy_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"

	"github.com/getsyntegrity/urd/engine"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/inttest/infra/tenantfx"
	"github.com/getsyntegrity/urd/tenancy"
)

const commandTimeout = 30 * time.Second

func multiTenant() engine.Option { return engine.WithTenantResolver(tenantfx.Resolver{}) }

// balance reads the balance of the account state a command returned.
func balance(sc *specs.Context, state engine.State) float64 {
	sc.Helper()
	account, ok := state.(*testpb.Account)
	sc.Expect(ok).To(specs.BeTrue())
	return account.GetAccountBalance()
}

// send runs one command as caller and expects it to succeed.
func send(sc *specs.Context, n *tenantfx.Node, caller context.Context, id string, cmd engine.Command) (float64, uint64) {
	sc.Helper()
	state, revision, err := n.Engine.SendCommand(caller, id, cmd, commandTimeout)
	sc.Expect(err).To(specs.BeNil())
	return balance(sc, state), revision
}

// recovered reads an entity's state without changing it: TestNoEvent produces no event.
func recovered(sc *specs.Context, n *tenantfx.Node, caller context.Context, id string) (float64, uint64) {
	sc.Helper()
	return send(sc, n, caller, id, &testpb.TestNoEvent{})
}

// expectRowsOf checks that the rows under tenantColumn are exactly sequences 1..count and that each of them
// carries attached as its tenant identity (attached "" means: no tenant identity at all).
func expectRowsOf(sc *specs.Context, rows []tenantfx.Row, tenantColumn string, count int, attached string) {
	sc.Helper()
	var got []uint64
	var own []tenantfx.Row
	for _, row := range rows {
		if row.TenantColumn == tenantColumn {
			own = append(own, row)
			got = append(got, row.Sequence)
		}
	}
	want := make([]uint64, 0, count)
	for i := 1; i <= count; i++ {
		want = append(want, uint64(i))
	}
	sc.Expect(got).To(specs.Equal(want))
	for _, row := range own {
		id, ok := row.AttachedTenant()
		sc.Expect(ok).To(specs.Equal(attached != ""))
		sc.Expect(id).To(specs.Equal(attached))
	}
}

// W2: the same entity ID under two tenants is two streams.
func TestConformance_W2_SameIDInTwoTenantsIsTwoStreams(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)

	specs.Describe(t, "one entity ID under two tenants over postgres.EventStore", func(s *specs.Spec) {
		s.It("keeps own rows, own revisions and own state per tenant, and a third tenant sees nothing", func(sc *specs.Context) {
			ctx := context.Background()
			id := uuid.NewString()
			node := tenantfx.StartNode(sc, dsn, multiTenant())

			sc.Expect(node.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id), tenantfx.Spawn("acme"))).To(specs.BeNil())
			sc.Expect(node.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id), tenantfx.Spawn("globex"))).To(specs.BeNil())

			// Interleaved on purpose: any bleed shows as a wrong balance or revision on the other side.
			bal, rev := send(sc, node, tenantfx.Caller("acme"), id, &testpb.CreateAccount{AccountBalance: 10})
			sc.Expect(bal).To(specs.Equal(10.0))
			sc.Expect(rev).To(specs.Equal(uint64(1)))
			bal, rev = send(sc, node, tenantfx.Caller("globex"), id, &testpb.CreateAccount{AccountBalance: 1000})
			sc.Expect(bal).To(specs.Equal(1000.0))
			sc.Expect(rev).To(specs.Equal(uint64(1)))
			bal, rev = send(sc, node, tenantfx.Caller("acme"), id, &testpb.CreditAccount{AccountId: id, Balance: 5})
			sc.Expect(bal).To(specs.Equal(15.0))
			sc.Expect(rev).To(specs.Equal(uint64(2)))

			bal, rev = recovered(sc, node, tenantfx.Caller("globex"), id)
			sc.Expect(bal).To(specs.Equal(1000.0))
			sc.Expect(rev).To(specs.Equal(uint64(1)))

			// The rows landed under the tenant column and carry that tenant's identity, with the business ID.
			rows := tenantfx.Rows(sc, dsn, id)
			sc.Expect(len(rows)).To(specs.Equal(3))
			expectRowsOf(sc, rows, "acme", 2, "acme")
			expectRowsOf(sc, rows, "globex", 1, "globex")

			// A third tenant has no such entity.
			exists, err := node.Engine.EntityExists(tenantfx.Caller("initech"), id)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(exists).To(specs.BeFalse())
		})
	})
}

// W4 and A1: what is rejected persists nothing and never reaches the handler.
func TestConformance_W4_RejectedCommandsPersistNothing(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)

	specs.Describe(t, "commands the tenancy boundary rejects, over postgres.EventStore", func(s *specs.Spec) {
		s.It("rejects another tenant, no tenant and an administrative context without a row or a handler call", func(sc *specs.Context) {
			ctx := context.Background()
			id := uuid.NewString()
			node := tenantfx.StartNode(sc, dsn, multiTenant())
			owner := enginetest.NewTenancyProbeEventSourcedBehavior(id)
			sc.Expect(node.Engine.Entity(ctx, owner, tenantfx.Spawn("acme"))).To(specs.BeNil())

			reject := func() {
				sc.Helper()
				_, _, err := node.Engine.SendCommand(tenantfx.Caller("globex"), id, &testpb.CreateAccount{AccountBalance: 1}, commandTimeout)
				sc.Expect(err).To(specs.Not(specs.BeNil()))
				_, _, err = node.Engine.SendCommand(context.Background(), id, &testpb.CreateAccount{AccountBalance: 1}, commandTimeout)
				sc.Expect(err).To(specs.Not(specs.BeNil()))
				_, _, err = node.Engine.SendCommand(tenantfx.Administrative(), id, &testpb.CreateAccount{AccountBalance: 1}, commandTimeout)
				sc.Expect(err).To(specs.MatchError(tenancy.ErrDenied))
			}

			reject()
			sc.Expect(owner.InvocationCount()).To(specs.BeZero())
			sc.Expect(tenantfx.CountAll(sc, dsn)).To(specs.BeZero())

			// The owner still works, so the rejections above were not a broken harness.
			bal, rev := send(sc, node, tenantfx.Caller("acme"), id, &testpb.CreateAccount{AccountBalance: 7})
			sc.Expect(bal).To(specs.Equal(7.0))
			sc.Expect(rev).To(specs.Equal(uint64(1)))
			sc.Expect(owner.InvocationCount()).To(specs.Equal(1))

			// Rejected again after the entity has state: still one row, still one handler call.
			reject()
			sc.Expect(owner.InvocationCount()).To(specs.Equal(1))
			rows := tenantfx.Rows(sc, dsn, id)
			sc.Expect(len(rows)).To(specs.Equal(1))
			expectRowsOf(sc, rows, "acme", 1, "acme")
			sc.Expect(tenantfx.CountAll(sc, dsn)).To(specs.Equal(1))
		})
	})
}

// W5: concurrent commands on one ID under two tenants never cross.
func TestConformance_W5_ConcurrentSameIDCommandsDoNotCross(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)

	const perTenant = 10
	// Each tenant credits by a different step, so a credit applied to the wrong tenant changes its balance.
	steps := map[string]float64{"acme": 1, "globex": 1000}
	base := map[string]float64{"acme": 100, "globex": 5000}

	specs.Describe(t, "concurrent commands on one entity ID under two tenants over postgres.EventStore", func(s *specs.Spec) {
		s.It("keeps every reply, revision and row with its own tenant for any interleaving", func(sc *specs.Context) {
			ctx := context.Background()
			id := uuid.NewString()
			node := tenantfx.StartNode(sc, dsn, multiTenant())

			for tenant, opening := range base {
				sc.Expect(node.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id), tenantfx.Spawn(tenant))).To(specs.BeNil())
				_, rev := send(sc, node, tenantfx.Caller(tenant), id, &testpb.CreateAccount{AccountBalance: opening})
				sc.Expect(rev).To(specs.Equal(uint64(1)))
			}

			// Every sender waits on the latch, so one close releases all of them together. The asserts below
			// hold whatever order the actors receive the commands in.
			type result struct {
				balance  float64
				revision uint64
				err      error
			}
			results := map[string][]result{"acme": make([]result, perTenant), "globex": make([]result, perTenant)}
			latch := make(chan struct{})
			var wg sync.WaitGroup
			for tenant := range results {
				for i := 0; i < perTenant; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-latch
						state, revision, err := node.Engine.SendCommand(tenantfx.Caller(tenant), id,
							&testpb.CreditAccount{AccountId: id, Balance: steps[tenant]}, commandTimeout)
						r := result{revision: revision, err: err}
						if account, ok := state.(*testpb.Account); ok {
							r.balance = account.GetAccountBalance()
						}
						results[tenant][i] = r
					}()
				}
			}
			close(latch)
			wg.Wait()

			for tenant, got := range results {
				revisions := map[uint64]bool{}
				for _, r := range got {
					sc.Expect(r.err).To(specs.BeNil())
					revisions[r.revision] = true
					// Each reply is the tenant's own account: its base plus a whole number of its own steps.
					sc.Expect(r.balance >= base[tenant]+steps[tenant] && r.balance <= base[tenant]+perTenant*steps[tenant]).To(specs.BeTrue())
				}
				// The replies carry revisions 2..perTenant+1, each exactly once: none is lost or shared.
				sc.Expect(len(revisions)).To(specs.Equal(perTenant))
				for rev := uint64(2); rev <= perTenant+1; rev++ {
					sc.Expect(revisions[rev]).To(specs.BeTrue())
				}

				bal, rev := recovered(sc, node, tenantfx.Caller(tenant), id)
				sc.Expect(bal).To(specs.Equal(base[tenant] + perTenant*steps[tenant]))
				sc.Expect(rev).To(specs.Equal(uint64(perTenant + 1)))
			}

			rows := tenantfx.Rows(sc, dsn, id)
			sc.Expect(len(rows)).To(specs.Equal(2 * (perTenant + 1)))
			expectRowsOf(sc, rows, "acme", perTenant+1, "acme")
			expectRowsOf(sc, rows, "globex", perTenant+1, "globex")
		})
	})
}

// W6: a restart recovers each tenant's own state.
func TestConformance_W6_RestartRecoversEachTenantsOwnState(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)

	specs.Describe(t, "a restart of an engine serving one entity ID under two tenants over postgres.EventStore", func(s *specs.Spec) {
		s.It("rebuilds each tenant's balance and revision from its own rows and continues from there", func(sc *specs.Context) {
			ctx := context.Background()
			id := uuid.NewString()
			opening := map[string]float64{"acme": 10, "globex": 1000}

			first := tenantfx.StartNode(sc, dsn, multiTenant())
			for tenant, amount := range opening {
				sc.Expect(first.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id), tenantfx.Spawn(tenant))).To(specs.BeNil())
				send(sc, first, tenantfx.Caller(tenant), id, &testpb.CreateAccount{AccountBalance: amount})
				send(sc, first, tenantfx.Caller(tenant), id, &testpb.CreditAccount{AccountId: id, Balance: 1})
			}
			first.Stop(sc)

			second := tenantfx.StartNode(sc, dsn, multiTenant())
			// Nothing is alive until each tenant spawns its entity again: the state can only come from Postgres.
			exists, err := second.Engine.EntityExists(tenantfx.Caller("acme"), id)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(exists).To(specs.BeFalse())

			for tenant, amount := range opening {
				sc.Expect(second.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id), tenantfx.Spawn(tenant))).To(specs.BeNil())
				bal, rev := recovered(sc, second, tenantfx.Caller(tenant), id)
				sc.Expect(bal).To(specs.Equal(amount + 1))
				sc.Expect(rev).To(specs.Equal(uint64(2)))

				bal, rev = send(sc, second, tenantfx.Caller(tenant), id, &testpb.CreditAccount{AccountId: id, Balance: 1})
				sc.Expect(bal).To(specs.Equal(amount + 2))
				sc.Expect(rev).To(specs.Equal(uint64(3)))
			}

			rows := tenantfx.Rows(sc, dsn, id)
			sc.Expect(len(rows)).To(specs.Equal(6))
			expectRowsOf(sc, rows, "acme", 3, "acme")
			expectRowsOf(sc, rows, "globex", 3, "globex")
		})
	})
}

// W7: single-tenant and legacy modes.
func TestConformance_W7_SingleTenantAndLegacyModes(t *testing.T) {
	t.Parallel()
	legacyDSN := shared.NewDatabase(t)
	singleDSN := shared.NewDatabase(t)

	specs.Describe(t, "the modes that need no tenant plumbing, over postgres.EventStore", func(s *specs.Spec) {
		s.It("legacy mode (no resolver) stores unscoped rows with no identity and recovers them after a restart", func(sc *specs.Context) {
			ctx := context.Background()
			id := uuid.NewString()

			first := tenantfx.StartNode(sc, legacyDSN)
			sc.Expect(first.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			send(sc, first, ctx, id, &testpb.CreateAccount{AccountBalance: 50})
			send(sc, first, ctx, id, &testpb.CreditAccount{AccountId: id, Balance: 5})
			first.Stop(sc)

			second := tenantfx.StartNode(sc, legacyDSN)
			sc.Expect(second.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			bal, rev := recovered(sc, second, ctx, id)
			sc.Expect(bal).To(specs.Equal(55.0))
			sc.Expect(rev).To(specs.Equal(uint64(2)))

			expectRowsOf(sc, tenantfx.Rows(sc, legacyDSN, id), "", 2, "")
		})

		s.It("single-tenant mode spawns and commands with no tenant argument and stores under its one tenant", func(sc *specs.Context) {
			ctx := context.Background()
			id := uuid.NewString()
			resolver, err := tenancy.WithSingleTenant(tenancy.TenantID("solo"))
			sc.Expect(err).To(specs.BeNil())

			first := tenantfx.StartNode(sc, singleDSN, engine.WithTenantResolver(resolver))
			sc.Expect(first.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			send(sc, first, ctx, id, &testpb.CreateAccount{AccountBalance: 50})
			send(sc, first, ctx, id, &testpb.CreditAccount{AccountId: id, Balance: 5})
			first.Stop(sc)

			second := tenantfx.StartNode(sc, singleDSN, engine.WithTenantResolver(resolver))
			sc.Expect(second.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			bal, rev := recovered(sc, second, ctx, id)
			sc.Expect(bal).To(specs.Equal(55.0))
			sc.Expect(rev).To(specs.Equal(uint64(2)))

			expectRowsOf(sc, tenantfx.Rows(sc, singleDSN, id), "solo", 2, "solo")
		})
	})
}

// W7: data a legacy deployment wrote is not visible to a tenant, and a tenant's data does not touch it.
func TestConformance_W7_LegacyDataIsNotSeenByATenant(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)

	specs.Describe(t, "an entity ID that exists unscoped, then under a tenant, over postgres.EventStore", func(s *specs.Spec) {
		s.It("starts the tenant's entity empty, leaves the unscoped rows alone and keeps both recoverable", func(sc *specs.Context) {
			ctx := context.Background()
			id := uuid.NewString()

			legacy := tenantfx.StartNode(sc, dsn)
			sc.Expect(legacy.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			send(sc, legacy, ctx, id, &testpb.CreateAccount{AccountBalance: 50})
			send(sc, legacy, ctx, id, &testpb.CreditAccount{AccountId: id, Balance: 5})
			legacy.Stop(sc)

			tenanted := tenantfx.StartNode(sc, dsn, multiTenant())
			sc.Expect(tenanted.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id), tenantfx.Spawn("acme"))).To(specs.BeNil())
			// The unscoped history is not acme's: acme's entity starts empty, at revision 0.
			bal, rev := recovered(sc, tenanted, tenantfx.Caller("acme"), id)
			sc.Expect(bal).To(specs.Equal(0.0))
			sc.Expect(rev).To(specs.Equal(uint64(0)))
			bal, rev = send(sc, tenanted, tenantfx.Caller("acme"), id, &testpb.CreateAccount{AccountBalance: 9})
			sc.Expect(bal).To(specs.Equal(9.0))
			sc.Expect(rev).To(specs.Equal(uint64(1)))
			tenanted.Stop(sc)

			rows := tenantfx.Rows(sc, dsn, id)
			sc.Expect(len(rows)).To(specs.Equal(3))
			expectRowsOf(sc, rows, "", 2, "")
			expectRowsOf(sc, rows, "acme", 1, "acme")

			again := tenantfx.StartNode(sc, dsn)
			sc.Expect(again.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			bal, rev = recovered(sc, again, ctx, id)
			sc.Expect(bal).To(specs.Equal(55.0))
			sc.Expect(rev).To(specs.Equal(uint64(2)))
		})
	})
}
