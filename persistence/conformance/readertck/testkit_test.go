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
	"fmt"
	"slices"
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

// expectation is the exact outcome of one property: its status and the distinct
// violation codes it carries.
type expectation struct {
	status Status
	codes  []string
}

// legacyKnownFailures is what the current timestamp-cursor API is EXPECTED to do
// on testkit, per scenario and per property. It is a measured baseline, not an
// accepted behaviour: each row lists every property that fails with the exact
// set of codes. A property absent from a row must not fail and must carry no
// violation; a scenario absent from the map must pass entirely. The PostgreSQL
// adapter shares the cursor shape and is pending its own run in the integration
// lane.
var legacyKnownFailures = map[string]map[property]expectation{
	ScenarioIssueCase: {
		onSafety:      {Fail, []string{CodeOmission}},
		onEligibility: {Fail, []string{CodeLateVisibility}},
		onProgress:    {Fail, []string{CodeStall}},
	},
	ScenarioLongTransaction: {
		onSafety:      {Fail, []string{CodeOmission}},
		onEligibility: {Fail, []string{CodeLateVisibility}},
		onProgress:    {Fail, []string{CodeStall}},
	},
	ScenarioTieLateCommit: {
		onSafety:   {Fail, []string{CodeOmission}},
		onProgress: {Fail, []string{CodeStall}},
	},
	ScenarioScopeSelection: {
		onSafety:   {Fail, []string{CodeOmission}},
		onProgress: {Fail, []string{CodeStall}},
	},
	ScenarioConditionsBroken: {
		onSafety:   {Fail, []string{CodeOmission}},
		onProgress: {Fail, []string{CodeStall}},
	},
}

func distinctCodes(p PropertyVerdict) []string {
	var codes []string
	for _, v := range p.Violations {
		if !slices.Contains(codes, v.Code) {
			codes = append(codes, v.Code)
		}
	}
	slices.Sort(codes)
	return codes
}

// deviations lists every way v differs from the declared row. Properties the
// row does not mention must not fail and must carry no violation.
func deviations(v Verdict, row map[property]expectation) []string {
	var out []string
	for _, p := range []property{onSafety, onEligibility, onProgress} {
		got := v.of(p)
		want, declared := row[p]
		if !declared {
			if got.Status == Fail || len(got.Violations) > 0 {
				out = append(out, fmt.Sprintf("property %d: unexpected %v %v", p, got.Status, distinctCodes(got)))
			}
			continue
		}
		wantCodes := slices.Clone(want.codes)
		slices.Sort(wantCodes)
		if got.Status != want.status || !slices.Equal(distinctCodes(got), wantCodes) {
			out = append(out, fmt.Sprintf("property %d: got %v %v, want %v %v", p, got.Status, distinctCodes(got), want.status, wantCodes))
		}
	}
	return out
}

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
			ctx.Expect(deviations(v, legacyKnownFailures[sc.Name])).To(specs.BeEmpty())
		})
		s.It("rejects an additional violation next to the known omission", func(ctx *specs.Context) {
			v, err := verdictOf(scenarioNamed(ScenarioIssueCase), testkitFactory(newLegacy))
			ctx.Expect(err).To(specs.BeNil())
			row := legacyKnownFailures[ScenarioIssueCase]
			ctx.Expect(deviations(v, row)).To(specs.BeEmpty())

			v.Safety.Violations = append(slices.Clone(v.Safety.Violations), Violation{CodeUnexpectedError, "extra"})
			ctx.Expect(v.Safety.Has(CodeOmission)).To(specs.BeTrue())
			ctx.Expect(deviations(v, row)).To(specs.HaveLen(1))

			v, err = verdictOf(scenarioNamed(ScenarioTieLateCommit), testkitFactory(newLegacy))
			ctx.Expect(err).To(specs.BeNil())
			v.Eligibility = PropertyVerdict{Status: Fail, Violations: []Violation{{CodeLateVisibility, "extra"}}}
			ctx.Expect(deviations(v, legacyKnownFailures[ScenarioTieLateCommit])).To(specs.HaveLen(1))
		})
	})
}
