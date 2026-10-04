package main

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// rule matches repository paths. Exactly one of the fields is set.
type rule struct {
	exact  string
	prefix string
	suffix string
	reason string
}

func (r rule) matches(p string) bool {
	switch {
	case r.exact != "":
		return p == r.exact
	case r.prefix != "":
		return strings.HasPrefix(p, r.prefix)
	default:
		return strings.HasSuffix(p, r.suffix)
	}
}

func firstMatch(rules []rule, p string) (string, bool) {
	for _, r := range rules {
		if r.matches(p) {
			return r.reason, true
		}
	}
	return "", false
}

// globalRules are the paths whose change can alter every test result: the toolchain, the code generation and the
// tooling that decides what runs. Touching one forces the full scope of the unit and component lanes.
var globalRules = []rule{
	{exact: ".go-version", reason: "the Go toolchain version"},
	{exact: "go.work", reason: "the workspace file"},
	{exact: "go.work.sum", reason: "the workspace file"},
	{exact: "buf.yaml", reason: "protobuf code generation"},
	{exact: "buf.gen.yaml", reason: "protobuf code generation"},
	{exact: "buf.gen.example.yaml", reason: "protobuf code generation"},
	{prefix: "protos/", reason: "protobuf sources"},
	{exact: ".github/workflows/ci.yml", reason: "the CI workflow"},
	{prefix: ".github/actions/go-setup/", reason: "the shared Go setup action"},
	{exact: ".github/scripts/test-matrix.sh", reason: "the shard planner"},
	{exact: ".github/scripts/count-tests.sh", reason: "the test counter"},
	{prefix: ".github/scripts/impact/", reason: "the impact selector"},
}

// ignoredRules are the paths no test depends on. They are listed on purpose and not guessed: a path that is in
// neither list falls back to the module that contains it, so a new kind of file widens the run instead of
// slipping through.
var ignoredRules = []rule{
	{suffix: ".md", reason: "documentation"},
	{prefix: "docs/", reason: "documentation"},
	{prefix: "odd/", reason: "documentation"},
	{prefix: "openspec/", reason: "documentation"},
	{prefix: ".spec-governance/", reason: "documentation"},
	{prefix: "assets/", reason: "static assets"},
	{prefix: "resources/", reason: "reference SQL, not embedded by any package"},
	{prefix: "CHANGELOG/", reason: "changelog"},
	{exact: "OWNERS", reason: "ownership file"},
	{exact: "LICENSE", reason: "license"},
	{exact: ".gitignore", reason: "ignore file"},
	{exact: ".golangci.yml", reason: "lint configuration, checked by the lint job"},
	{exact: ".goreleaser.yaml", reason: "release configuration"},
	{exact: "Makefile", reason: "local developer targets, not used by CI"},
	{suffix: "/Makefile", reason: "local developer targets, not used by CI"},
	{exact: "Dockerfile.ci", reason: "local developer image, not used by CI"},
	{prefix: ".github/ISSUE_TEMPLATE/", reason: "issue templates"},
	{exact: ".github/CODEOWNERS", reason: "ownership file"},
	{exact: ".github/dependabot.yml", reason: "dependency bot configuration"},
	{prefix: ".github/actions/notify/", reason: "notification action"},
	{exact: ".github/workflows/go-sdk-update.yml", reason: "a workflow other than ci"},
	{exact: ".github/workflows/pr-meta.yml", reason: "a workflow other than ci"},
	{exact: ".github/workflows/release.yml", reason: "a workflow other than ci"},
	{exact: ".github/workflows/security.yml", reason: "a workflow other than ci"},
	{exact: ".github/scripts/api-check.sh", reason: "release tooling, not used by the test lanes"},
	{exact: ".github/scripts/changelog.sh", reason: "release tooling, not used by the test lanes"},
	{exact: ".github/scripts/go-sdk-update.sh", reason: "release tooling, not used by the test lanes"},
	{exact: ".github/scripts/labels.sh", reason: "release tooling, not used by the test lanes"},
	{exact: ".github/scripts/next-version.sh", reason: "release tooling, not used by the test lanes"},
	{prefix: ".github/scripts/unitgate/", reason: "the unit-gate job always runs and tests it"},
	{exact: ".github/unit-test-gate-pending.txt", reason: "unit-gate allowlist, always checked by the unit-gate job"},
	{exact: ".github/unit-test-gate-resources.txt", reason: "unit-gate allowlist, always checked by the unit-gate job"},
	{prefix: "example/cluster/k8s/", reason: "deployment manifests of an example, never run by CI"},
	{exact: "example/cluster/Dockerfile", reason: "deployment image of an example, never run by CI"},
	{exact: "example/cluster/kind-config.yaml", reason: "deployment manifest of an example, never run by CI"},
}

// Ignored is a changed file that no lane depends on, with the reason.
type Ignored struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Selection is what a set of changes affects.
type Selection struct {
	Full       bool // the change forces the full scope; FullReason says why
	FullReason string
	Packages   map[string][]string // import path -> why it is affected (changed, or depends on what changed)
	Modules    map[string][]string // module directory -> why it is affected
	Ignored    []Ignored
}

// Select maps the changed files onto the graph. It never returns a partial answer for something it cannot place:
// a path it cannot map to a package falls back to its module, and a path that disappeared, a new module or a
// global file widens the result to Full.
func Select(g *Graph, changes []Change) *Selection {
	sel := &Selection{Packages: map[string][]string{}, Modules: map[string][]string{}}
	seeds := map[string]string{}       // import path -> reason
	moduleSeeds := map[string]string{} // module dir -> reason

	full := func(reason string) *Selection {
		sel.Full, sel.FullReason = true, reason
		return sel
	}

	for _, c := range changes {
		p := c.Path
		if reason, ok := firstMatch(globalRules, p); ok {
			return full(fmt.Sprintf("%s changed (%s)", p, reason))
		}
		if reason, ok := firstMatch(ignoredRules, p); ok {
			sel.Ignored = append(sel.Ignored, Ignored{Path: p, Reason: reason})
			continue
		}
		if c.Status == "D" {
			return full(fmt.Sprintf("%s was deleted or renamed", p))
		}
		base := path.Base(p)
		mod := g.ModuleOf(p)
		if base == "go.mod" && c.Status == "A" {
			return full(fmt.Sprintf("%s is a new module", p))
		}
		if (base == "go.mod" || base == "go.sum") && path.Dir(p) == mod.Dir {
			moduleSeeds[mod.Dir] = "its dependency files changed: " + p
			continue
		}
		if pkg := g.PackageOf(p); pkg != nil {
			if _, dup := seeds[pkg.ImportPath]; !dup {
				seeds[pkg.ImportPath] = "changed: " + p
			}
			continue
		}
		if _, dup := moduleSeeds[mod.Dir]; !dup {
			moduleSeeds[mod.Dir] = "a file outside any package changed: " + p
		}
	}

	for _, m := range g.Modules {
		reason, ok := moduleSeeds[m.Dir]
		if !ok {
			continue
		}
		for _, pkg := range g.PackagesOf(m.Path) {
			if _, dup := seeds[pkg.ImportPath]; !dup {
				seeds[pkg.ImportPath] = reason
			}
		}
		sel.Modules[m.Dir] = append(sel.Modules[m.Dir], reason)
	}

	closure := newClosure(g)
	for _, pkg := range sortedPackages(g) {
		reason, ok := affectedReason(g, closure, pkg, seeds)
		if !ok {
			continue
		}
		sel.Packages[pkg.ImportPath] = []string{reason}
		mod := g.moduleByPath(pkg.Module)
		if len(sel.Modules[mod.Dir]) == 0 {
			sel.Modules[mod.Dir] = []string{reason}
		}
	}
	return sel
}

func sortedPackages(g *Graph) []*Package {
	out := make([]*Package, 0, len(g.Packages))
	for _, p := range g.Packages {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ImportPath < out[j].ImportPath })
	return out
}

func (g *Graph) moduleByPath(modulePath string) Module {
	for _, m := range g.Modules {
		if m.Path == modulePath {
			return m
		}
	}
	return g.Modules[0]
}

// closure memoizes the production dependencies a package is rebuilt from: its imports, and theirs, transitively.
type closure struct {
	g    *Graph
	memo map[string]map[string]bool
}

func newClosure(g *Graph) *closure { return &closure{g: g, memo: map[string]map[string]bool{}} }

// prod returns every package of the graph that importPath imports directly or transitively.
func (c *closure) prod(importPath string) map[string]bool {
	if got, ok := c.memo[importPath]; ok {
		return got
	}
	out := map[string]bool{}
	c.memo[importPath] = out // set before walking, so an import cycle terminates
	pkg, ok := c.g.Packages[importPath]
	if !ok {
		return out
	}
	for _, imp := range pkg.Imports {
		if _, internal := c.g.Packages[imp]; !internal {
			continue
		}
		out[imp] = true
		for dep := range c.prod(imp) {
			out[dep] = true
		}
	}
	return out
}

// affectedReason says whether the test run of pkg depends on a seed, and why. The test run of a package builds
// the package and everything it imports, plus the packages its test files import and what those import. It does
// not build the test files of any other package, so a test import is a single hop, never a chain.
func affectedReason(g *Graph, c *closure, pkg *Package, seeds map[string]string) (string, bool) {
	if reason, ok := seeds[pkg.ImportPath]; ok {
		return reason, true
	}
	if seed := firstSeed(c.prod(pkg.ImportPath), seeds); seed != "" {
		return fmt.Sprintf("depends on %s (%s)", seed, seeds[seed]), true
	}
	viaTests := map[string]bool{}
	for _, imp := range append(append([]string{}, pkg.TestImports...), pkg.XTestImports...) {
		if imp == pkg.ImportPath {
			continue
		}
		if _, internal := g.Packages[imp]; !internal {
			continue
		}
		viaTests[imp] = true
		for dep := range c.prod(imp) {
			viaTests[dep] = true
		}
	}
	if seed := firstSeed(viaTests, seeds); seed != "" {
		return fmt.Sprintf("its tests depend on %s (%s)", seed, seeds[seed]), true
	}
	return "", false
}

func firstSeed(deps map[string]bool, seeds map[string]string) string {
	best := ""
	for dep := range deps {
		if _, ok := seeds[dep]; ok && (best == "" || dep < best) {
			best = dep
		}
	}
	return best
}
