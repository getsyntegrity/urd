package main

import (
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

type selectCase struct {
	name        string
	changes     []Change
	wantRoot    []string // short names of the root packages that run
	wantModules []string // nested module directories that run
}

func TestSelectionOfAFeaturePullRequest(t *testing.T) {
	specs.Describe(t, "impact selection of a pull request to develop", func(s *specs.Spec) {
		specs.Table(s, []selectCase{
			{name: "a leaf package nobody imports runs alone",
				changes: changed("leaf/leaf.go"), wantRoot: []string{"leaf"}, wantModules: []string{}},
			{name: "a shared contract runs its transitive consumers, in this module and in the nested ones",
				changes:  changed("port/port.go"),
				wantRoot: []string{"app", "engine", "port", "store", "testkit"},
				// publisher imports port; test/compat only imports publisher; example and inttest import
				// engine; benchmark imports app: every one of them is a transitive consumer.
				wantModules: []string{"benchmark", "example", "inttest", "publisher", "test/compat"}},
			{name: "an intermediate package runs the packages above it and not the ones below",
				changes:  changed("engine/engine.go"),
				wantRoot: []string{"app", "engine"}, wantModules: []string{"benchmark", "example", "inttest"}},
			{name: "a package that is only imported by the tests of another runs that other package, once, and no further",
				changes: changed("testkit/testkit.go"), wantRoot: []string{"engine", "testkit"}, wantModules: []string{}},
			{name: "a change in a test file of a package runs that package and its consumers",
				changes:  changed("engine/engine_test.go"),
				wantRoot: []string{"app", "engine"}, wantModules: []string{"benchmark", "example", "inttest"}},
			{name: "a change in an embedded file runs the package that embeds it",
				changes: changed("store/schema/001.sql"), wantRoot: []string{"store"}, wantModules: []string{}},
			{name: "a change in testdata runs the package that owns the directory",
				changes: changed("store/testdata/fixture.txt"), wantRoot: []string{"store"}, wantModules: []string{}},
			{name: "a publisher change runs the publisher module and the module that consumes it, and no root package",
				changes: changed("publisher/publisher.go"), wantRoot: []string{}, wantModules: []string{"publisher", "test/compat"}},
			{name: "a change only in an example builds the example module and nothing else",
				changes: changed("example/main.go"), wantRoot: []string{}, wantModules: []string{"example"}},
			{name: "a change only in inttest builds the inttest module and nothing else",
				changes: changed("inttest/flows/restart/flow_test.go"), wantRoot: []string{}, wantModules: []string{"inttest"}},
			{name: "a deployment manifest of an example changes nothing that runs",
				changes: changed("example/cluster/k8s/app.yml"), wantRoot: []string{}, wantModules: []string{}},
			{name: "documentation changes nothing that runs",
				changes: changed("docs/guide.md", "port/README.md"), wantRoot: []string{}, wantModules: []string{}},
			{name: "a file that belongs to no package widens to the module that contains it",
				changes:     changed("tools/generate.sh"),
				wantRoot:    []string{"app", "engine", "leaf", "port", "store", "testkit"},
				wantModules: []string{"benchmark", "example", "inttest", "publisher", "test/compat"}},
			{name: "the dependency files of a nested module run that module and its consumers",
				changes: changed("publisher/go.sum"), wantRoot: []string{}, wantModules: []string{"publisher", "test/compat"}},
			{name: "the dependency files of the root module run everything that builds on it",
				changes:     changed("go.mod"),
				wantRoot:    []string{"app", "engine", "leaf", "port", "store", "testkit"},
				wantModules: []string{"benchmark", "example", "inttest", "publisher", "test/compat"}},
			{name: "two unrelated changes run the union of both",
				changes: changed("leaf/leaf.go", "example/main.go"), wantRoot: []string{"leaf"}, wantModules: []string{"example"}},
		}, func(c selectCase) string { return c.name }, func(ctx *specs.Context, c selectCase) {
			plan := planFor(ctx, featurePR, repoFixture(), c.changes)
			ctx.Expect(plan.Mode).ToEqual("selective")
			ctx.Expect(rootNames(plan)).ToEqual(c.wantRoot)
			ctx.Expect(moduleDirs(plan)).ToEqual(c.wantModules)
		})

		s.It("never runs a package the change does not reach", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("leaf/leaf.go"))
			ctx.Expect(rootNames(plan)).To(specs.Not(specs.Contain("app")))
			ctx.Expect(rootNames(plan)).To(specs.Not(specs.Contain("port")))
			ctx.Expect(plan.Lanes["modules"].Run).To(specs.BeFalse())
		})

		s.It("says why each package runs", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("port/port.go"))
			reasons := map[string]string{}
			for _, pk := range plan.RootPackages {
				reasons[trimRoot(pk.ImportPath)] = pk.Reason
			}
			ctx.Expect(reasons["port"]).ToEqual("changed: port/port.go")
			ctx.Expect(reasons["engine"]).ToEqual("depends on example.com/urd/port (changed: port/port.go)")
			ctx.Expect(reasons["app"]).ToEqual("depends on example.com/urd/port (changed: port/port.go)")
		})

		s.It("distinguishes a test import from a production import in the reason", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("testkit/testkit.go"))
			for _, pk := range plan.RootPackages {
				if trimRoot(pk.ImportPath) == "engine" {
					ctx.Expect(pk.Reason).ToEqual("its tests depend on example.com/urd/testkit (changed: testkit/testkit.go)")
				}
			}
		})

		s.It("flags the nested modules that only build and vet", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed("port/port.go"))
			vetOnly := map[string]bool{}
			for _, m := range plan.Modules {
				vetOnly[m.Dir] = m.VetOnly
			}
			ctx.Expect(vetOnly["inttest"]).To(specs.BeTrue())
			ctx.Expect(vetOnly["benchmark"]).To(specs.BeTrue())
			ctx.Expect(vetOnly["example"]).To(specs.BeFalse())
			ctx.Expect(vetOnly["publisher"]).To(specs.BeFalse())
		})
	})
}

type widenCase struct {
	name    string
	changes []Change
	reason  string
}

func TestSelectionWidensToTheFullScope(t *testing.T) {
	specs.Describe(t, "impact selection that cannot be narrowed", func(s *specs.Spec) {
		specs.Table(s, []widenCase{
			{name: "a new module", changes: []Change{{Status: "A", Path: "newmod/go.mod"}},
				reason: "newmod/go.mod is a new module"},
			{name: "a deleted file", changes: []Change{{Status: "D", Path: "leaf/old.go"}},
				reason: "leaf/old.go was deleted or renamed"},
			{name: "a deleted test file", changes: []Change{{Status: "D", Path: "engine/old_test.go"}},
				reason: "engine/old_test.go was deleted or renamed"},
			{name: "a deleted package", changes: []Change{{Status: "D", Path: "gone/gone.go"}, {Status: "M", Path: "leaf/leaf.go"}},
				reason: "gone/gone.go was deleted or renamed"},
			{name: "the Go version", changes: changed(".go-version"), reason: ".go-version changed (the Go toolchain version)"},
			{name: "a protobuf source", changes: changed("protos/ego/ego.proto"), reason: "protos/ego/ego.proto changed (protobuf sources)"},
			{name: "the buf configuration", changes: changed("buf.gen.yaml"), reason: "buf.gen.yaml changed (protobuf code generation)"},
			{name: "the CI workflow", changes: changed(".github/workflows/ci.yml"), reason: ".github/workflows/ci.yml changed (the CI workflow)"},
			{name: "the shard planner", changes: changed(".github/scripts/test-matrix.sh"), reason: ".github/scripts/test-matrix.sh changed (the shard planner)"},
			{name: "the selector itself", changes: changed(".github/scripts/impact/select.go"), reason: ".github/scripts/impact/select.go changed (the impact selector)"},
			{name: "the shared Go setup action", changes: changed(".github/actions/go-setup/action.yml"), reason: ".github/actions/go-setup/action.yml changed (the shared Go setup action)"},
		}, func(c widenCase) string { return c.name }, func(ctx *specs.Context, c widenCase) {
			fsys := repoFixture()
			if c.changes[0].Path == "newmod/go.mod" {
				fsys = with(fsys, map[string]string{"newmod/go.mod": "module example.com/urd/newmod\n", "newmod/n.go": "package newmod\n"})
			}
			plan := planFor(ctx, featurePR, fsys, c.changes)
			ctx.Expect(plan.Mode).ToEqual("full")
			ctx.Expect(plan.Reason).ToEqual(c.reason)
			ctx.Expect(len(plan.RootPackages)).ToEqual(6)
			ctx.Expect(len(plan.Modules) >= 5).To(specs.BeTrue())
		})

		s.It("keeps the integration, cluster and benchmark lanes off on a feature pull request even for a global change", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), changed(".go-version"))
			ctx.Expect(plan.Mode).ToEqual("full")
			for _, lane := range []string{"inttest", "cluster", "benchmark"} {
				ctx.Expect(plan.Lanes[lane].Run).To(specs.BeFalse())
			}
		})

		s.It("treats a rename as a delete plus an add, so it widens", func(ctx *specs.Context) {
			changes, err := ParseChanges("R100\x00leaf/a.go\x00leaf/b.go\x00")
			ctx.Expect(err).To(specs.BeNil())
			plan := planFor(ctx, featurePR, repoFixture(), changes)
			ctx.Expect(plan.Mode).ToEqual("full")
			ctx.Expect(plan.Reason).ToEqual("leaf/a.go was deleted or renamed")
		})

		s.It("does not widen for a deleted document", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, repoFixture(), []Change{{Status: "D", Path: "docs/old.md"}})
			ctx.Expect(plan.Mode).ToEqual("selective")
			ctx.Expect(plan.Lanes["test"].Run).To(specs.BeFalse())
		})
	})
}
