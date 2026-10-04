package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

type eventCase struct {
	name        string
	event       Event
	changes     []Change
	wantMode    string
	heavyLanes  bool // inttest, cluster and benchmark
	pullRequest bool // lint and api
}

func TestPlanLanesFollowTheEvent(t *testing.T) {
	leaf := changed("leaf/leaf.go")
	specs.Describe(t, "impact plan by event", func(s *specs.Spec) {
		specs.Table(s, []eventCase{
			{name: "a feature pull request selects and skips the heavy lanes",
				event: featurePR, changes: leaf, wantMode: "selective", heavyLanes: false, pullRequest: true},
			{name: "a hotfix pull request to main selects and skips the heavy lanes",
				event: Event{Name: "pull_request", BaseRef: "main", HeadRef: "hotfix/x"}, changes: leaf, wantMode: "selective", heavyLanes: false, pullRequest: true},
			{name: "the release pull request validates everything and runs the heavy lanes",
				event: Event{Name: "pull_request", BaseRef: "main", HeadRef: "develop"}, changes: leaf, wantMode: "full", heavyLanes: true, pullRequest: true},
			{name: "a push to develop validates everything and runs the heavy lanes, whatever the diff",
				event: Event{Name: "push"}, changes: nil, wantMode: "full", heavyLanes: true, pullRequest: false},
			{name: "a manual run validates everything and runs the heavy lanes",
				event: Event{Name: "workflow_dispatch"}, changes: nil, wantMode: "full", heavyLanes: true, pullRequest: false},
		}, func(c eventCase) string { return c.name }, func(ctx *specs.Context, c eventCase) {
			plan := planFor(ctx, c.event, repoFixture(), c.changes)
			ctx.Expect(plan.Mode).ToEqual(c.wantMode)
			for _, lane := range []string{"inttest", "cluster", "benchmark"} {
				ctx.Expect(plan.Lanes[lane].Run).ToEqual(c.heavyLanes)
			}
			for _, lane := range []string{"lint", "api"} {
				ctx.Expect(plan.Lanes[lane].Run).ToEqual(c.pullRequest)
			}
			for _, lane := range []string{"flow", "unit-gate", "vuln"} {
				ctx.Expect(plan.Lanes[lane].Run).To(specs.BeTrue())
			}
		})

		s.It("runs one full selection and not a second partial one on a push", func(ctx *specs.Context) {
			plan := planFor(ctx, Event{Name: "push"}, repoFixture(), nil)
			ctx.Expect(plan.Mode).ToEqual("full")
			ctx.Expect(plan.RacePackages()).ToEqual("./...")
			ctx.Expect(plan.SelectedPackagesFile()).ToEqual("")
			ctx.Expect(len(plan.RootPackages)).ToEqual(6)
			ctx.Expect(len(plan.Modules)).ToEqual(5)
		})

		s.It("rejects an event it does not know", func(ctx *specs.Context) {
			_, err := BuildPlan(Event{Name: "schedule"}, mustGraph(ctx, repoFixture()), nil)
			ctx.Expect(err == nil).To(specs.BeFalse())
		})

		s.It("omits the test lanes when only an example changed, and keeps the modules lane", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("example/main.go"))
			for _, lane := range []string{"test", "test-report", "race"} {
				ctx.Expect(plan.Lanes[lane].Run).To(specs.BeFalse())
			}
			ctx.Expect(plan.Lanes["modules"].Run).To(specs.BeTrue())
			ctx.Expect(plan.Lanes["tidy"].Run).To(specs.BeTrue())
			ctx.Expect(plan.Lanes["test-min"].Run).To(specs.BeTrue())
		})

		s.It("omits every code lane for a documentation-only pull request, and keeps the always-on jobs", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("docs/guide.md", "readme.md"))
			for _, lane := range []string{"test", "test-report", "race", "modules", "tidy", "test-min", "inttest", "cluster", "benchmark"} {
				ctx.Expect(plan.Lanes[lane].Run).To(specs.BeFalse())
			}
			for _, lane := range []string{"flow", "unit-gate", "vuln", "lint", "api"} {
				ctx.Expect(plan.Lanes[lane].Run).To(specs.BeTrue())
			}
			ctx.Expect(len(plan.Ignored)).ToEqual(2)
		})

		s.It("gives every lane of the gate an entry", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("leaf/leaf.go"))
			for _, lane := range allLanes {
				_, ok := plan.Lanes[lane]
				ctx.Expect(ok).To(specs.BeTrue())
			}
			ctx.Expect(len(plan.Lanes)).ToEqual(len(allLanes))
		})
	})
}

func TestPlanOutputs(t *testing.T) {
	specs.Describe(t, "impact plan outputs for the workflow", func(s *specs.Spec) {
		s.It("serializes a plan with nothing selected with empty lists and not null", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("docs/guide.md"))
			raw, err := json.Marshal(plan)
			ctx.Expect(err).To(specs.BeNil())
			for _, want := range []string{`"modules":[]`, `"rootPackages":[]`} {
				ctx.Expect(strings.Contains(string(raw), want)).To(specs.BeTrue())
			}
			ctx.Expect(strings.Contains(string(raw), "null")).To(specs.BeFalse())
		})

		s.It("exposes the lanes as a JSON map of job to boolean", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("leaf/leaf.go"))
			raw, err := plan.LanesJSON()
			ctx.Expect(err).To(specs.BeNil())
			var lanes map[string]bool
			ctx.Expect(json.Unmarshal([]byte(raw), &lanes)).To(specs.BeNil())
			ctx.Expect(lanes["test"]).To(specs.BeTrue())
			ctx.Expect(lanes["modules"]).To(specs.BeFalse())
			ctx.Expect(strings.Contains(raw, "\n")).To(specs.BeFalse())
		})

		s.It("builds the modules matrix with the vet-only flag, as strings", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("engine/engine.go"))
			raw, err := plan.ModulesMatrix()
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(raw).ToEqual(`{"include":[{"dir":"benchmark","vet-only":"yes"},{"dir":"example","vet-only":"no"},{"dir":"inttest","vet-only":"yes"}]}`)
		})

		s.It("returns a placeholder matrix when no nested module runs, because GitHub rejects an empty one", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("leaf/leaf.go"))
			raw, err := plan.ModulesMatrix()
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(raw).ToEqual(`{"include":[{"dir":"none","vet-only":"yes"}]}`)
		})

		s.It("lists the packages race and the shards run on a selective plan", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("testkit/testkit.go"))
			ctx.Expect(plan.RacePackages()).ToEqual("example.com/urd/engine example.com/urd/testkit")
			ctx.Expect(plan.SelectedPackagesFile()).ToEqual("example.com/urd/engine\nexample.com/urd/testkit")
		})

		s.It("writes a readable summary with the lanes, the packages and the reasons", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("port/port.go", "docs/guide.md"))
			text := plan.Summary()
			for _, want := range []string{
				"### CI plan: selective",
				"| `cluster` | **omitted** | never runs on a feature pull request |",
				"| `example` | build, vet and test |",
				"| `inttest` | build and vet |",
				"| `example.com/urd/app` | depends on example.com/urd/port (changed: port/port.go) |",
				"- `docs/guide.md`: documentation",
			} {
				ctx.Expect(strings.Contains(text, want)).To(specs.BeTrue())
			}
		})
	})
}
