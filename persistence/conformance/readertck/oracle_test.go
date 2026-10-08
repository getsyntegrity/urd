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
	"fmt"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/getsyntegrity/urd/persistence"
)

// The oracle is exercised here on traces written by hand, with no reader and no
// cursor involved: that is the evidence that its expectations come from the
// ground-truth log alone.

func labels(tes []*TruthEvent) []string {
	out := make([]string, 0, len(tes))
	for _, te := range tes {
		out = append(out, te.Label)
	}
	return out
}

func sampleTruth() *Truth {
	t := NewTruth()
	t.Begin("slow")
	t.Append("slow", ev("late", scopeUnscoped, "late", 1, 150))
	t.Begin("fast")
	t.Append("fast", ev("b", scopeUnscoped, "b", 2, 200))
	t.Append("fast", ev("a", scopeUnscoped, "a", 1, 100))
	t.Append("fast", ev("t", scopeA, "t", 1, 120))
	t.Commit("fast")
	t.Begin("gone")
	t.Append("gone", ev("ghost", scopeUnscoped, "ghost", 1, 1))
	t.Abort("gone")
	t.Tick(3)
	t.Commit("slow")
	return t
}

func dlv(e Event) Delivery { return Delivery{Event: e, Token: e.Key().String()} }

func TestOracleExpectedSets(t *testing.T) {
	truth := sampleTruth()
	unscoped := Consumer{Name: "u", Selection: OneScope(scopeUnscoped), Slices: AllSlices()}
	cell := Consumer{Name: "cell", Selection: AllScopesInCell(), Slices: AllSlices(), Privileged: true}
	slice1 := Consumer{Name: "s1", Selection: OneScope(scopeUnscoped), Slices: SliceRange{Lo: 1, Hi: 1}}
	specs.Describe(t, "the ground truth defines what each consumer is entitled to", func(s *specs.Spec) {
		s.It("orders by the ordering key, not by commit order", func(ctx *specs.Context) {
			ctx.Expect(labels(truth.Confirmed(unscoped, truth.Commits()))).To(specs.Equal([]string{"a", "late", "b"}))
		})
		s.It("keeps OneScope(Unscoped()) distinct from the wildcard", func(ctx *specs.Context) {
			ctx.Expect(labels(truth.Confirmed(unscoped, truth.Commits()))).To(specs.Not(specs.Contain("t")))
			ctx.Expect(labels(truth.Confirmed(cell, truth.Commits()))).To(specs.Contain("t"))
		})
		s.It("never expects an aborted event", func(ctx *specs.Context) {
			ctx.Expect(labels(truth.Confirmed(cell, truth.Commits()))).To(specs.Not(specs.Contain("ghost")))
		})
		s.It("honours the slice range", func(ctx *specs.Context) {
			ctx.Expect(labels(truth.Confirmed(slice1, truth.Commits()))).To(specs.Equal([]string{"a", "late"}))
		})
		s.It("expects only what had committed by a given commit number", func(ctx *specs.Context) {
			ctx.Expect(labels(truth.Confirmed(unscoped, 1))).To(specs.Equal([]string{"a", "b"}))
		})
		s.It("tells a wildcard from a zero scope", func(ctx *specs.Context) {
			ctx.Expect(OneScope(persistence.Scope{}).Matches(scopeUnscoped)).To(specs.BeFalse())
			ctx.Expect(OneScope(scopeUnscoped).Matches(scopeA)).To(specs.BeFalse())
			ctx.Expect(AllScopesInCell().Matches(scopeA)).To(specs.BeTrue())
		})
	})
}

func TestOracleJudgesHandWrittenTraces(t *testing.T) {
	unscoped := Consumer{Name: "c", Selection: OneScope(scopeUnscoped), Slices: AllSlices()}
	build := func(polls []PollRecord) *Trace {
		return &Trace{
			Scenario: Scenario{Name: "hand", Asserts: Safety, Consumers: []Consumer{unscoped}},
			Truth:    sampleTruth(),
			Polls:    polls,
		}
	}
	ok := func(es ...Event) PollRecord {
		var ds []Delivery
		for _, e := range es {
			ds = append(ds, dlv(e))
		}
		return PollRecord{Consumer: "c", Phase: PhaseScript, Commits: 2, Deliveries: ds}
	}
	a, b, late := ev("a", scopeUnscoped, "a", 1, 100), ev("b", scopeUnscoped, "b", 2, 200), ev("late", scopeUnscoped, "late", 1, 150)
	tenant := ev("t", scopeA, "t", 1, 120)
	ghost := ev("ghost", scopeUnscoped, "ghost", 1, 1)
	rows := []struct {
		name  string
		polls []PollRecord
		code  string // empty: the trace must pass
	}{
		{"everything delivered", []PollRecord{ok(a, b), ok(late)}, ""},
		{"a late commit never delivered", []PollRecord{ok(a, b)}, CodeOmission},
		{"another scope's event", []PollRecord{ok(a, b, tenant), ok(late)}, CodeWrongSelection},
		{"an aborted event", []PollRecord{ok(a, b, ghost), ok(late)}, CodePhantom},
		{"an event before its commit", []PollRecord{{Consumer: "c", Phase: PhaseScript, Commits: 1, Deliveries: []Delivery{dlv(late)}}, ok(a, b)}, CodePhantom},
		{"an event nobody wrote", []PollRecord{ok(a, b, ev("x", scopeUnscoped, "x", 1, 1)), ok(late)}, CodeUnknownEvent},
		{"the same event twice in a batch", []PollRecord{ok(a, a, b), ok(late)}, CodeDupInBatch},
		{"a redelivery with another token", []PollRecord{ok(a, b), {Consumer: "c", Phase: PhaseScript, Commits: 2, Deliveries: []Delivery{{Event: a, Token: "other"}}}, ok(late)}, CodeDupToken},
		{"no idempotence token", []PollRecord{{Consumer: "c", Phase: PhaseScript, Commits: 2, Deliveries: []Delivery{{Event: a}}}, ok(b, late)}, CodeMissingToken},
		{"a reader error", []PollRecord{{Consumer: "c", Phase: PhaseScript, Commits: 2, Err: fmt.Errorf("boom")}, ok(a, b, late)}, CodeUnexpectedError},
		{"a harmless redelivery", []PollRecord{ok(a, b), ok(a), ok(late)}, ""},
	}
	specs.Describe(t, "Evaluate applies the safety rules to a hand-written trace", func(s *specs.Spec) {
		specs.Table(s, rows, func(r struct {
			name  string
			polls []PollRecord
			code  string
		}) string {
			return r.name
		}, func(ctx *specs.Context, r struct {
			name  string
			polls []PollRecord
			code  string
		}) {
			v := Evaluate(build(r.polls))
			if r.code == "" {
				ctx.Expect(v.Safety.Violations).To(specs.BeEmpty())
				ctx.Expect(v.Safety.Status).To(specs.Equal(Pass))
				return
			}
			ctx.Expect(v.Safety.Status).To(specs.Equal(Fail))
			ctx.Expect(v.Safety.Has(r.code)).To(specs.BeTrue())
		})
		s.It("flags a persistence id delivered out of sequence", func(ctx *specs.Context) {
			tr := build(nil)
			tr.Truth = NewTruth()
			tr.Truth.Begin("t")
			e1, e2 := ev("p1", scopeUnscoped, "p", 1, 1), ev("p2", scopeUnscoped, "p", 1, 2)
			e2.Seq = 2
			tr.Truth.Append("t", e1)
			tr.Truth.Append("t", e2)
			tr.Truth.Commit("t")
			tr.Polls = []PollRecord{{Consumer: "c", Phase: PhaseScript, Commits: 1, Deliveries: []Delivery{dlv(e2), dlv(e1)}}}
			ctx.Expect(Evaluate(tr).Safety.Has(CodeOrder)).To(specs.BeTrue())
		})
		s.It("does not judge omissions while a writer is still open", func(ctx *specs.Context) {
			tr := build([]PollRecord{ok(a, b)})
			tr.Truth = NewTruth()
			tr.Truth.Begin("done")
			tr.Truth.Append("done", a)
			tr.Truth.Append("done", b)
			tr.Truth.Commit("done")
			tr.Truth.Begin("open")
			tr.Truth.Append("open", late)
			ctx.Expect(Evaluate(tr).Safety.Status).To(specs.Equal(Pass))
		})
	})
}

func TestOracleProgressAndEligibilityNeedExercise(t *testing.T) {
	specs.Describe(t, "a scenario cannot claim what it never tests", func(s *specs.Spec) {
		s.It("fails Progress when no drain ran with every writer resolved", func(ctx *specs.Context) {
			tr := &Trace{Scenario: Scenario{Name: "x", Asserts: Progress, Consumers: []Consumer{unscopedConsumer()}}, Truth: NewTruth()}
			v := Evaluate(tr)
			ctx.Expect(v.Progress.Status).To(specs.Equal(Fail))
			ctx.Expect(v.Progress.Has(CodeNotExercised)).To(specs.BeTrue())
		})
		s.It("fails a claimed bound when no event was ever due", func(ctx *specs.Context) {
			tr := &Trace{Scenario: Scenario{Name: "x", Asserts: Eligibility, Conditions: allConditions, LMax: 1, T: 1, Consumers: []Consumer{unscopedConsumer()}}, Truth: NewTruth()}
			v := Evaluate(tr)
			ctx.Expect(v.Eligibility.Status).To(specs.Equal(Fail))
			ctx.Expect(v.Eligibility.Has(CodeNotExercised)).To(specs.BeTrue())
		})
		s.It("leaves unasserted properties not applicable", func(ctx *specs.Context) {
			v := Evaluate(&Trace{Scenario: Scenario{Name: "x", Asserts: Safety, Consumers: []Consumer{unscopedConsumer()}}, Truth: NewTruth()})
			ctx.Expect(v.Eligibility.Status).To(specs.Equal(NotApplicable))
			ctx.Expect(v.Progress.Status).To(specs.Equal(NotApplicable))
		})
	})
}
