package main

import (
	"testing"
	"testing/fstest"

	"github.com/getsyntegrity/go-specs/specs"
)

type graphErrorCase struct {
	name  string
	files fstest.MapFS
}

func TestLoadGraph(t *testing.T) {
	specs.Describe(t, "impact import graph", func(s *specs.Spec) {
		s.It("discovers every module from its go.mod, the root first", func(ctx *specs.Context) {
			g := mustGraph(ctx, repoFixture())
			dirs := []string{}
			for _, m := range g.Modules {
				dirs = append(dirs, m.Dir)
			}
			ctx.Expect(dirs).ToEqual([]string{".", "benchmark", "example", "inttest", "publisher", "test/compat"})
		})

		s.It("finds a module that was added without listing it anywhere", func(ctx *specs.Context) {
			g := mustGraph(ctx, with(repoFixture(), map[string]string{"extra/go.mod": "module example.com/urd/extra\n", "extra/e.go": "package extra\n"}))
			ctx.Expect(len(g.Modules)).ToEqual(7)
			_, ok := g.Packages["example.com/urd/extra"]
			ctx.Expect(ok).To(specs.BeTrue())
		})

		s.It("keeps the packages of a nested module out of the root module", func(ctx *specs.Context) {
			g := mustGraph(ctx, repoFixture())
			ctx.Expect(g.Packages["example.com/urd/publisher"].Module).ToEqual("example.com/urd/publisher")
			_, inRoot := g.Packages["example.com/urd/publisher/publisher"]
			ctx.Expect(inRoot).To(specs.BeFalse())
		})

		s.It("separates production, test and external test imports", func(ctx *specs.Context) {
			fsys := with(repoFixture(), map[string]string{
				"pkg/p.go":        "package pkg\nimport _ \"example.com/urd/port\"\n",
				"pkg/p_test.go":   "package pkg\nimport _ \"example.com/urd/leaf\"\n",
				"pkg/p_x_test.go": "package pkg_test\nimport _ \"example.com/urd/store\"\n",
			})
			p := mustGraph(ctx, fsys).Packages["example.com/urd/pkg"]
			ctx.Expect(p.Imports).ToEqual([]string{"example.com/urd/port"})
			ctx.Expect(p.TestImports).ToEqual([]string{"example.com/urd/leaf"})
			ctx.Expect(p.XTestImports).ToEqual([]string{"example.com/urd/store"})
		})

		s.It("ignores directories the go tool ignores, so a dot directory adds no edge", func(ctx *specs.Context) {
			g := mustGraph(ctx, repoFixture())
			for _, p := range g.Packages {
				ctx.Expect(p.Dir).To(specs.Not(specs.Equal(".github/scripts/tool")))
			}
			ctx.Expect(g.PackageOf(".github/scripts/tool/t.go") == nil).To(specs.BeTrue())
		})

		s.It("attributes a file in a subdirectory that is not a package to the nearest package above it", func(ctx *specs.Context) {
			g := mustGraph(ctx, repoFixture())
			ctx.Expect(g.PackageOf("store/schema/001.sql").ImportPath).ToEqual("example.com/urd/store")
			ctx.Expect(g.PackageOf("inttest/flows/restart/flow_test.go").Module).ToEqual("example.com/urd/inttest")
		})

		s.It("does not attribute a file of a nested module to a package of the root module", func(ctx *specs.Context) {
			g := mustGraph(ctx, repoFixture())
			ctx.Expect(g.ModuleOf("publisher/readme.txt").Dir).ToEqual("publisher")
			ctx.Expect(g.ModuleOf("test/compat/x.txt").Dir).ToEqual("test/compat")
			ctx.Expect(g.ModuleOf("test/other.txt").Dir).ToEqual(".")
		})

		s.It("terminates on an import cycle", func(ctx *specs.Context) {
			fsys := with(repoFixture(), map[string]string{
				"a/a.go": "package a\nimport _ \"example.com/urd/b\"\n",
				"b/b.go": "package b\nimport _ \"example.com/urd/a\"\n",
			})
			plan := planFor(ctx, featurePR, fsys, changed("a/a.go"))
			ctx.Expect(rootNames(plan)).ToEqual([]string{"a", "b"})
		})
	})
}

func TestLoadGraphFailsInsteadOfReturningAPartialGraph(t *testing.T) {
	specs.Describe(t, "impact import graph errors", func(s *specs.Spec) {
		specs.Table(s, []graphErrorCase{
			{name: "a Go file that does not parse", files: with(repoFixture(), map[string]string{"leaf/broken.go": "package leaf\nimport (\n"})},
			{name: "a missing root go.mod", files: fstest.MapFS{"nested/go.mod": file("module example.com/n\n"), "nested/n.go": file("package n\n")}},
			{name: "a go.mod without a module line", files: with(repoFixture(), map[string]string{"bad/go.mod": "go 1.26\n"})},
			{name: "two modules with the same path", files: with(repoFixture(), map[string]string{"dup/go.mod": "module example.com/urd/publisher\n"})},
			{name: "an empty repository", files: fstest.MapFS{}},
		}, func(c graphErrorCase) string { return c.name }, func(ctx *specs.Context, c graphErrorCase) {
			g, err := LoadGraph(c.files)
			ctx.Expect(err == nil).To(specs.BeFalse())
			ctx.Expect(g == nil).To(specs.BeTrue())
		})
	})
}
