package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

// allNeeds returns a result for every job the gate waits for, all successful.
func allNeeds() map[string]string {
	needs := map[string]string{"plan": "success"}
	for _, lane := range allLanes {
		needs[lane] = "success"
	}
	return needs
}

// allLanesOn requires every job.
func allLanesOn() map[string]bool {
	lanes := map[string]bool{}
	for _, lane := range allLanes {
		lanes[lane] = true
	}
	return lanes
}

type gateCase struct {
	name   string
	needs  func() map[string]string
	lanes  func() map[string]bool
	expect string // a substring of the one expected problem; empty means the run is acceptable
}

func TestCheckGate(t *testing.T) {
	specs.Describe(t, "ci-ok gate against the plan", func(s *specs.Spec) {
		specs.Table(s, []gateCase{
			{name: "accepts a run where every required job succeeded",
				needs: allNeeds, lanes: allLanesOn},
			{name: "accepts a skipped job the plan did not require",
				needs: func() map[string]string { n := allNeeds(); n["cluster"] = "skipped"; return n },
				lanes: func() map[string]bool { l := allLanesOn(); l["cluster"] = false; return l }},
			{name: "rejects a required job that was skipped",
				needs:  func() map[string]string { n := allNeeds(); n["test"] = "skipped"; return n },
				lanes:  allLanesOn,
				expect: "test: required by the plan but its result is \"skipped\""},
			{name: "rejects a required job that failed",
				needs:  func() map[string]string { n := allNeeds(); n["race"] = "failure"; return n },
				lanes:  allLanesOn,
				expect: "race: result is \"failure\""},
			{name: "rejects a cancelled job even when the plan did not require it",
				needs:  func() map[string]string { n := allNeeds(); n["inttest"] = "cancelled"; return n },
				lanes:  func() map[string]bool { l := allLanesOn(); l["inttest"] = false; return l },
				expect: "inttest: result is \"cancelled\""},
			{name: "rejects a run whose plan job did not succeed",
				needs:  func() map[string]string { n := allNeeds(); n["plan"] = "failure"; return n },
				lanes:  allLanesOn,
				expect: "plan: result is \"failure\""},
			{name: "rejects a run whose plan job was skipped",
				needs:  func() map[string]string { n := allNeeds(); n["plan"] = "skipped"; return n },
				lanes:  allLanesOn,
				expect: "plan: result is \"skipped\""},
			{name: "rejects a run with no plan at all",
				needs:  allNeeds,
				lanes:  func() map[string]bool { return nil },
				expect: "the plan has no entry for this job"},
			{name: "rejects a plan that leaves a job out",
				needs:  allNeeds,
				lanes:  func() map[string]bool { l := allLanesOn(); delete(l, "vuln"); return l },
				expect: "vuln: the plan has no entry for this job"},
			{name: "rejects a job the planner does not know",
				needs:  func() map[string]string { n := allNeeds(); n["newjob"] = "success"; return n },
				lanes:  allLanesOn,
				expect: "newjob: ci-ok waits for this job but the planner does not know it"},
			{name: "rejects a planned job that ci-ok does not wait for",
				needs:  func() map[string]string { n := allNeeds(); delete(n, "tidy"); return n },
				lanes:  allLanesOn,
				expect: "tidy: the plan lists it but ci-ok does not wait for it"},
		}, func(c gateCase) string { return c.name }, func(ctx *specs.Context, c gateCase) {
			problems := CheckGate(c.needs(), c.lanes())
			if c.expect == "" {
				ctx.Expect(problems).To(specs.BeEmpty())
				return
			}
			found := false
			for _, p := range problems {
				found = found || strings.Contains(p, c.expect)
			}
			ctx.Expect(found).To(specs.BeTrue())
		})
	})
}

func TestRunGate(t *testing.T) {
	specs.Describe(t, "impact gate command", func(s *specs.Spec) {
		s.It("fails with a message per problem when a required job was skipped", func(ctx *specs.Context) {
			var out bytes.Buffer
			needs := `{"plan":{"result":"success"},"test":{"result":"skipped"}}`
			err := runGate(needs, `{"test":true}`, &out)
			ctx.Expect(err == nil).To(specs.BeFalse())
			ctx.Expect(strings.Contains(out.String(), "::error::test: required by the plan but its result is \"skipped\"")).To(specs.BeTrue())
		})

		s.It("fails when the needs context is not JSON", func(ctx *specs.Context) {
			ctx.Expect(runGate("not json", "{}", &bytes.Buffer{}) == nil).To(specs.BeFalse())
		})

		s.It("fails when the lanes output is missing, which is what a failed plan job leaves", func(ctx *specs.Context) {
			var out bytes.Buffer
			err := runGate(`{"plan":{"result":"failure"}}`, "", &out)
			ctx.Expect(err == nil).To(specs.BeFalse())
		})
	})
}
