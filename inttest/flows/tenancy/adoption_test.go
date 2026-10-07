// MIT License
//
// # Copyright (c) 2022-2026 Arsene Tochemey Gandote
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
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"

	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/inttest/infra/tenantfx"
	"github.com/getsyntegrity/urd/migration"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/persistence/postgres"
	"github.com/getsyntegrity/urd/tenancy"
)

// soleWriterFence is enough here: the legacy node is stopped before the adoption runs, so nothing else writes.
type soleWriterFence struct{}

func (soleWriterFence) Acquire(context.Context, persistence.Scope, string) (func(), error) {
	return func() {}, nil
}

// #346 / #428: characterization. Adopting legacy rows over a real Postgres fails today, because the adopter
// replays with the sequence bound math.MaxUint64 and the sequence column is int8. The legacy rows are untouched
// and still recoverable by a legacy node. When #428 is fixed this test becomes the single-tenant recovery check:
// the adoption completes and a single-tenant node recovers balance 15 at revision 2. The in-memory twin that
// passes today is migration.TestTenantAdopterEndToEndRecoveryThroughRealActor.
func TestAdoptionOfLegacyDataFailsOnPostgres(t *testing.T) {
	t.Parallel()
	dsn := shared.NewDatabase(t)

	specs.Describe(t, "legacy rows adopted into a single tenant, over postgres.EventStore", func(s *specs.Spec) {
		s.It("fails to read the source events, copies nothing and leaves the legacy rows recoverable", func(sc *specs.Context) {
			ctx := context.Background()
			id := uuid.NewString()

			legacy := tenantfx.StartNode(sc, dsn)
			sc.Expect(legacy.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			_, _ = send(sc, legacy, ctx, id, &testpb.CreateAccount{AccountBalance: 10})
			_, _ = send(sc, legacy, ctx, id, &testpb.CreditAccount{AccountId: id, Balance: 5})
			legacy.Stop(sc)

			store := postgres.NewEventStore(dsn)
			sc.Expect(store.Connect(ctx)).To(specs.BeNil())
			sc.Cleanup(func() { _ = store.Disconnect(ctx) })
			adopter, err := migration.NewTenantAdopter(
				func(context.Context, string) (tenancy.TenantID, bool, error) { return "acme", true, nil },
				migration.WithEventsStore(store),
				migration.WithWriteEnabled(), migration.WithAdoptionFence(soleWriterFence{}),
			)
			sc.Expect(err).To(specs.BeNil())
			report, err := adopter.Run(ctx)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(report.Copied).To(specs.Equal(0))
			sc.Expect(report.Failed).To(specs.Equal(1))
			sc.Expect(strings.Contains(report.String(), "greater than maximum value for int64")).To(specs.BeTrue())

			// No adopted row was written, and a legacy node still recovers the data.
			sc.Expect(len(tenantfx.Rows(sc, dsn, id))).To(specs.Equal(2))
			node := tenantfx.StartNode(sc, dsn)
			sc.Expect(node.Engine.Entity(ctx, enginetest.NewAccountEventSourcedBehavior(id))).To(specs.BeNil())
			bal, rev := recovered(sc, node, ctx, id)
			sc.Expect(bal).To(specs.Equal(15.0))
			sc.Expect(rev).To(specs.Equal(uint64(2)))
		})
	})
}
