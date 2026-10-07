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

package readertck

import (
	"context"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/testkit"
)

// The scenarios also run against testkit's in-memory EventsStore. testkit has no
// transactions, so the backend below keeps a writer's events aside and writes
// them to the store at Commit: "committed" means "visible to the store", which
// is exactly what a reader built on the store can observe.

type testkitBackend struct {
	store   *testkit.EventStore
	scopes  []persistence.Scope
	slices  []uint32
	pending map[string][]Event
	mk      func(*testkitBackend) Subject
}

func testkitFactory(mk func(*testkitBackend) Subject) Factory {
	return func(sc Scenario) Backend {
		store := testkit.NewEventsStore()
		if err := store.Connect(context.Background()); err != nil {
			panic(err)
		}
		return &testkitBackend{store: store, scopes: sc.Scopes(), slices: sc.Slices(), pending: map[string][]Event{}, mk: mk}
	}
}

func (b *testkitBackend) Begin(tx string) { b.pending[tx] = nil }

func (b *testkitBackend) Append(tx string, e Event) { b.pending[tx] = append(b.pending[tx], e) }

func (b *testkitBackend) Commit(tx string) {
	for _, e := range b.pending[tx] {
		payload, err := anypb.New(&testpb.AccountCreated{AccountId: e.PersistenceID})
		if err != nil {
			panic(err)
		}
		pb := &egopb.Event{PersistenceId: e.PersistenceID, SequenceNumber: e.Seq, Timestamp: e.Timestamp, Shard: uint64(e.Slice), Event: payload}
		if err := b.store.WriteEvents(context.Background(), e.Scope, []*egopb.Event{pb}, persistence.Unconditional()); err != nil {
			panic(err)
		}
	}
	delete(b.pending, tx)
}

func (b *testkitBackend) Abort(tx string)    { delete(b.pending, tx) }
func (b *testkitBackend) Tick(int64)         {}
func (b *testkitBackend) NewReader() Subject { return b.mk(b) }

// The two subjects are the exported readers of reference.go, so the integration
// lane runs exactly the same code over PostgreSQL.
func newLegacy(b *testkitBackend) Subject {
	return LegacyReader{Store: b.store, Scopes: b.scopes, Slices: b.slices}
}

func newStoreSet(b *testkitBackend) Subject { return StoreSetReader{Store: b.store, Scopes: b.scopes} }

func TestScenariosRunAgainstTestkit(t *testing.T) {
	specs.Describe(t, "every scenario runs against testkit's EventsStore", func(s *specs.Spec) {
		specs.Table(s, append(Catalogue(), Generate(1), Generate(2), Generate(3)), func(sc Scenario) string { return "store-backed reference reader / " + sc.Name }, func(ctx *specs.Context, sc Scenario) {
			v, err := verdictOf(sc, testkitFactory(newStoreSet))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(v.Failed()).To(specs.BeFalse())
			ctx.Expect(v.Safety.Violations).To(specs.BeEmpty())
		})
		specs.Table(s, Catalogue(), func(sc Scenario) string { return "legacy GetShardEvents / " + sc.Name }, func(ctx *specs.Context, sc Scenario) {
			v, err := verdictOf(sc, testkitFactory(newLegacy))
			ctx.Expect(err).To(specs.BeNil())
			if LegacySafetyFailure(sc.Name) {
				ctx.Expect(v.of(onSafety).Status).To(specs.Equal(Fail))
				ctx.Expect(v.of(onSafety).Has(CodeOmission)).To(specs.BeTrue())
				return
			}
			ctx.Expect(v.Failed()).To(specs.BeFalse())
		})
	})
}
