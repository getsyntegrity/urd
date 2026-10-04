package main

import (
	"sort"
	"testing/fstest"

	"github.com/getsyntegrity/go-specs/specs"
)

// repoFixture is a small repository shaped like urd: a root module, nested modules that consume it, and a module
// that consumes a nested module. Dependencies, from consumer to dependency:
//
//	app            -> engine -> port
//	engine (xtest) -> testkit -> port
//	store          -> port           (store/schema/001.sql is embedded)
//	leaf           -> nothing, and nothing imports it
//	publisher      -> port           (nested module)
//	test/compat    -> publisher      (nested module that consumes another nested module)
//	example        -> engine         (nested module)
//	inttest        -> engine         (nested module, vet-only)
//	benchmark      -> app            (nested module, vet-only)
func repoFixture() fstest.MapFS {
	return fstest.MapFS{
		"go.mod":                     file("module example.com/urd\n\ngo 1.26\n"),
		"port/port.go":               file("package port\n\ntype Contract interface{}\n"),
		"engine/engine.go":           file("package engine\n\nimport _ \"example.com/urd/port\"\n"),
		"engine/engine_test.go":      file("package engine_test\n\nimport _ \"example.com/urd/testkit\"\n"),
		"testkit/testkit.go":         file("package testkit\n\nimport _ \"example.com/urd/port\"\n"),
		"app/app.go":                 file("package app\n\nimport _ \"example.com/urd/engine\"\n"),
		"store/store.go":             file("package store\n\nimport _ \"example.com/urd/port\"\n"),
		"store/schema/001.sql":       file("CREATE TABLE t ();\n"),
		"store/testdata/fixture.txt": file("x\n"),
		"leaf/leaf.go":               file("package leaf\n"),
		"docs/guide.md":              file("# guide\n"),
		".github/scripts/tool/t.go":  file("package main\n\nimport _ \"example.com/urd/leaf\"\n"),

		"publisher/go.mod":       file("module example.com/urd/publisher\n"),
		"publisher/publisher.go": file("package publisher\n\nimport _ \"example.com/urd/port\"\n"),

		"test/compat/go.mod":          file("module example.com/urd/test/compat\n"),
		"test/compat/compat_test.go":  file("package compat\n\nimport _ \"example.com/urd/publisher\"\n"),
		"example/go.mod":              file("module example.com/urd/example\n"),
		"example/main.go":             file("package main\n\nimport _ \"example.com/urd/engine\"\n"),
		"example/cluster/k8s/app.yml": file("kind: Deployment\n"),

		"inttest/go.mod":                     file("module example.com/urd/inttest\n"),
		"inttest/flows/restart/flow_test.go": file("package restart_test\n\nimport _ \"example.com/urd/engine\"\n"),
		"benchmark/go.mod":                   file("module example.com/urd/benchmark\n"),
		"benchmark/bench_test.go":            file("package benchmark\n\nimport _ \"example.com/urd/app\"\n"),
	}
}

func file(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }

// with returns a copy of the fixture with the given files added or replaced.
func with(base fstest.MapFS, files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range files {
		out[k] = file(v)
	}
	return out
}

func mustGraph(ctx *specs.Context, fsys fstest.MapFS) *Graph {
	g, err := LoadGraph(fsys)
	ctx.Expect(err).To(specs.BeNil())
	return g
}

// changed builds a change list of modified files.
func changed(paths ...string) []Change {
	out := make([]Change, 0, len(paths))
	for _, p := range paths {
		out = append(out, Change{Status: "M", Path: p})
	}
	return out
}

// rootNames is the short names (without the module prefix) of the root packages a plan runs.
func rootNames(p *Plan) []string {
	out := []string{}
	for _, pk := range p.RootPackages {
		out = append(out, trimRoot(pk.ImportPath))
	}
	sort.Strings(out)
	return out
}

func moduleDirs(p *Plan) []string {
	out := []string{}
	for _, m := range p.Modules {
		out = append(out, m.Dir)
	}
	sort.Strings(out)
	return out
}

func trimRoot(importPath string) string {
	const prefix = "example.com/urd/"
	if importPath == "example.com/urd" {
		return "."
	}
	return importPath[len(prefix):]
}

func planFor(ctx *specs.Context, ev Event, fsys fstest.MapFS, changes []Change) *Plan {
	p, err := BuildPlan(ev, mustGraph(ctx, fsys), changes)
	ctx.Expect(err).To(specs.BeNil())
	return p
}

var featurePR = Event{Name: "pull_request", BaseRef: "develop", HeadRef: "feat/x"}
