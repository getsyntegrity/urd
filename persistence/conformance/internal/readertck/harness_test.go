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
	"errors"
	"fmt"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

func verdictOf(sc Scenario, f Factory) (Verdict, error) {
	tr, err := Run(sc, f)
	if err != nil {
		return Verdict{}, err
	}
	return Evaluate(tr), nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func scenarioNamed(name string) Scenario {
	for _, sc := range Catalogue() {
		if sc.Name == name {
			return sc
		}
	}
	panic("no scenario " + name)
}

type correctRow struct {
	name string
	sc   Scenario
	mk   func(*fakeBackend) Subject
}

func correctRows() []correctRow {
	fakes := []struct {
		name string
		mk   func(*fakeBackend) Subject
	}{
		{"stable-prefix", newOrdReader(ordConfig{})},
		{"delivered-set", newSetReader(0)},
	}
	scenarios := Catalogue()
	for seed := uint64(1); seed <= 40; seed++ {
		scenarios = append(scenarios, Generate(seed))
	}
	var rows []correctRow
	for _, f := range fakes {
		for _, sc := range scenarios {
			rows = append(rows, correctRow{name: f.name + " / " + sc.Name, sc: sc, mk: f.mk})
		}
	}
	return rows
}

func TestCorrectReadersPassEveryScenario(t *testing.T) {
	specs.Describe(t, "two correct readers with different mechanisms pass the whole catalogue and 40 seeded scenarios", func(s *specs.Spec) {
		specs.Table(s, correctRows(), func(r correctRow) string { return r.name }, func(ctx *specs.Context, r correctRow) {
			v, err := verdictOf(r.sc, fakeFactory(r.mk))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(v.Safety.Violations).To(specs.BeEmpty())
			ctx.Expect(v.Eligibility.Violations).To(specs.BeEmpty())
			ctx.Expect(v.Progress.Violations).To(specs.BeEmpty())
			ctx.Expect(v.Failed()).To(specs.BeFalse())
			if r.sc.Asserts&Safety != 0 {
				ctx.Expect(v.Safety.Status).To(specs.Equal(Pass))
			}
			if r.sc.Asserts&Progress != 0 {
				ctx.Expect(v.Progress.Status).To(specs.Equal(Pass))
			}
		})
	})
}

func TestEligibilityIsOnlyClaimedUnderItsConditions(t *testing.T) {
	specs.Describe(t, "a visibility bound is judged only when every condition holds; safety is judged always", func(s *specs.Spec) {
		s.It("claims and passes the bound in the issue case", func(ctx *specs.Context) {
			v, err := verdictOf(scenarioNamed(ScenarioIssueCase), fakeFactory(newOrdReader(ordConfig{})))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(v.Eligibility.Status).To(specs.Equal(Pass))
		})
		s.It("does not claim the bound when the conditions are broken, and safety and progress still hold", func(ctx *specs.Context) {
			v, err := verdictOf(scenarioNamed(ScenarioConditionsBroken), fakeFactory(newOrdReader(ordConfig{})))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(v.Eligibility.Status).To(specs.Equal(NotClaimed))
			ctx.Expect(v.Safety.Status).To(specs.Equal(Pass))
			ctx.Expect(v.Progress.Status).To(specs.Equal(Pass))
		})
		s.It("would fail a reader that withholds behind the held transaction if the bound were claimed anyway", func(ctx *specs.Context) {
			tr, err := Run(scenarioNamed(ScenarioConditionsBroken), fakeFactory(newOrdReader(ordConfig{})))
			ctx.Expect(err).To(specs.BeNil())
			tr.Scenario.Conditions = allConditions
			v := Evaluate(tr)
			ctx.Expect(v.Eligibility.Status).To(specs.Equal(Fail))
			ctx.Expect(v.Eligibility.Has(CodeLateVisibility)).To(specs.BeTrue())
			ctx.Expect(v.Safety.Status).To(specs.Equal(Pass))
		})
		s.It("refuses to run a scenario that claims the bound while a transaction outlives LMax", func(ctx *specs.Context) {
			sc := scenarioNamed(ScenarioConditionsBroken)
			sc.Conditions = allConditions
			_, err := Run(sc, fakeFactory(newOrdReader(ordConfig{})))
			ctx.Expect(errText(err)).To(specs.MatchRegex("over LMax"))
		})
	})
}

func TestRunsAreDeterministic(t *testing.T) {
	specs.Describe(t, "the same scenario and seed always give the same trace and verdict", func(s *specs.Spec) {
		s.It("regenerates identical scenarios from a seed", func(ctx *specs.Context) {
			ctx.Expect(Generate(7)).To(specs.Equal(Generate(7)))
			ctx.Expect(Generate(7).Steps).To(specs.Not(specs.Equal(Generate(8).Steps)))
		})
		s.It("gives the same verdict on every run", func(ctx *specs.Context) {
			for _, sc := range Catalogue() {
				a, errA := verdictOf(sc, fakeFactory(newTSReader(false)))
				b, errB := verdictOf(sc, fakeFactory(newTSReader(false)))
				ctx.Expect(errors.Join(errA, errB)).To(specs.BeNil())
				ctx.Expect(a).To(specs.Equal(b))
			}
		})
	})
}

type invalidRow struct {
	name  string
	build func() Scenario
	want  string
}

func TestScenarioValidation(t *testing.T) {
	base := func() Scenario {
		return Scenario{Name: "x", Asserts: Safety, Consumers: []Consumer{unscopedConsumer()}}
	}
	rows := []invalidRow{
		{"transaction never resolved", func() Scenario { sc := base(); sc.Steps = []Step{Begin("t")}; return sc }, "never resolved"},
		{"append to a closed transaction", func() Scenario {
			sc := base()
			sc.Steps = []Step{Begin("t"), Commit("t"), Append("t", ev("e", scopeUnscoped, "e", 1, 1))}
			return sc
		}, "not open"},
		{"repeated event key", func() Scenario {
			sc := base()
			e := ev("e", scopeUnscoped, "e", 1, 1)
			sc.Steps = []Step{Begin("t"), Append("t", e), Append("t", e), Commit("t")}
			return sc
		}, "repeated"},
		{"unknown consumer", func() Scenario { sc := base(); sc.Steps = []Step{Poll("ghost", 1)}; return sc }, "unknown consumer"},
		{"limit below one", func() Scenario { sc := base(); sc.Steps = []Step{Poll("c", 0)}; return sc }, "limit"},
		{"non-positive tick", func() Scenario { sc := base(); sc.Steps = []Step{Tick(0)}; return sc }, "tick"},
		{"no asserted property", func() Scenario { sc := base(); sc.Asserts = 0; return sc }, "required"},
		{"claimed bound below LMax", func() Scenario {
			sc := base()
			sc.Asserts, sc.Conditions, sc.LMax, sc.T = Eligibility, allConditions, 10, 5
			return sc
		}, "T >= LMax"},
	}
	specs.Describe(t, "Validate rejects malformed scripts and self-contradicting eligibility claims", func(s *specs.Spec) {
		specs.Table(s, rows, func(r invalidRow) string { return r.name }, func(ctx *specs.Context, r invalidRow) {
			ctx.Expect(errText(r.build().Validate())).To(specs.MatchRegex(r.want))
		})
		s.It("accepts every catalogue scenario once, under a unique name", func(ctx *specs.Context) {
			seen := map[string]bool{}
			for _, sc := range Catalogue() {
				ctx.Expect(sc.Validate()).To(specs.BeNil())
				ctx.Expect(seen[sc.Name]).To(specs.BeFalse())
				seen[sc.Name] = true
			}
			ctx.Expect(len(seen)).To(specs.Equal(13))
		})
	})
}

func TestFormatting(t *testing.T) {
	specs.Describe(t, "diagnostics are stable text", func(s *specs.Spec) {
		s.It("renders statuses and events", func(ctx *specs.Context) {
			ctx.Expect(fmt.Sprint(Pass, Fail, NotClaimed, NotApplicable)).To(specs.Equal("pass fail not-claimed not-applicable"))
			ctx.Expect(ev("e", scopeA, "p", 1, 1).Key().String()).To(specs.Equal("tenant:tenant-a/p#1"))
		})
	})
}
