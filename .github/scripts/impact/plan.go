package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Lane names are the job ids of ci.yml, so the gate can match a plan entry to a job result by name.
const (
	laneFlow       = "flow"
	laneLint       = "lint"
	laneTest       = "test"
	laneTestReport = "test-report"
	laneTestMin    = "test-min"
	laneModules    = "modules"
	laneInttest    = "inttest"
	laneBenchmark  = "benchmark"
	laneCluster    = "cluster"
	laneRace       = "race"
	laneUnitGate   = "unit-gate"
	laneTidy       = "tidy"
	laneAPI        = "api"
	laneVuln       = "vuln"
)

// allLanes is every job ci-ok waits for, except plan. The gate refuses a plan that leaves one out, so a job added
// to ci-ok needs without a planning rule fails instead of being accepted whatever it reports.
var allLanes = []string{laneFlow, laneLint, laneTest, laneTestReport, laneTestMin, laneModules, laneInttest,
	laneBenchmark, laneCluster, laneRace, laneUnitGate, laneTidy, laneAPI, laneVuln}

// vetOnlyModules are the modules whose own tests run in a dedicated lane (inttest, benchmark), so the modules job
// only builds and vets them.
var vetOnlyModules = map[string]bool{"inttest": true, "benchmark": true}

// Event is the GitHub event the workflow runs for.
type Event struct {
	Name    string // pull_request, push or workflow_dispatch
	BaseRef string
	HeadRef string
}

// Kind names the pipeline the event belongs to: feature (a pull request to develop), hotfix (a pull request to
// main from anything but develop), release (develop to main), push (a merge into develop) or dispatch (manual).
func (e Event) Kind() string {
	switch e.Name {
	case "push":
		return "push"
	case "workflow_dispatch":
		return "dispatch"
	case "pull_request":
		if e.BaseRef == "main" && e.HeadRef == "develop" {
			return "release"
		}
		if e.BaseRef == "main" {
			return "hotfix"
		}
		return "feature"
	}
	return "unknown"
}

// fullByEvent says whether the event validates everything. The merge into develop, the release and a manual run
// do, so they never run a second, partial selection next to the full one.
func (e Event) fullByEvent() bool {
	switch e.Kind() {
	case "push", "dispatch", "release":
		return true
	}
	return false
}

// LanePlan says whether a job has to run, and why.
type LanePlan struct {
	Run    bool   `json:"run"`
	Reason string `json:"reason"`
}

// ModulePlan is one nested module the modules job has to build, vet and, unless VetOnly, test.
type ModulePlan struct {
	Dir     string `json:"dir"`
	Path    string `json:"path"`
	VetOnly bool   `json:"vetOnly"`
	Reason  string `json:"reason"`
}

// PackagePlan is one root-module package the test and race jobs have to run.
type PackagePlan struct {
	ImportPath string `json:"importPath"`
	Reason     string `json:"reason"`
}

// Plan is the serializable decision: what runs, what does not, and why.
type Plan struct {
	Event        string              `json:"event"`
	Kind         string              `json:"kind"`
	Mode         string              `json:"mode"` // "full" or "selective"
	Reason       string              `json:"reason"`
	Modules      []ModulePlan        `json:"modules"`      // nested modules; the root module is RootPackages
	RootPackages []PackagePlan       `json:"rootPackages"` // the packages of the root module to run
	Lanes        map[string]LanePlan `json:"lanes"`
	Ignored      []Ignored           `json:"ignored"`
}

// BuildPlan decides what a pull request or a push has to run. The event decides the heavy lanes; the changes only
// decide the scope of the unit, component and module lanes, and never turn a full event into a partial one.
func BuildPlan(ev Event, g *Graph, changes []Change) (*Plan, error) {
	kind := ev.Kind()
	if kind == "unknown" {
		return nil, fmt.Errorf("plan: unsupported event %q", ev.Name)
	}
	// Empty slices, not nil ones, so the JSON plan reads [] and not null for a plan with nothing in a section.
	plan := &Plan{Event: ev.Name, Kind: kind, Lanes: map[string]LanePlan{},
		Modules: []ModulePlan{}, RootPackages: []PackagePlan{}, Ignored: []Ignored{}}

	var sel *Selection
	switch {
	case ev.fullByEvent():
		plan.Mode, plan.Reason = "full", fmt.Sprintf("%s validates everything", describeKind(kind))
	default:
		sel = Select(g, changes)
		plan.Ignored = append(plan.Ignored, sel.Ignored...)
		if sel.Full {
			plan.Mode, plan.Reason = "full", sel.FullReason
		} else {
			plan.Mode, plan.Reason = "selective", "the packages the change touches and their consumers"
		}
	}

	rootPath := g.Modules[0].Path
	for _, m := range g.Modules {
		isRoot := m.Dir == "."
		switch {
		case plan.Mode == "full":
			plan.addAll(g, m, isRoot)
		case isRoot:
			for _, pkg := range g.PackagesOf(rootPath) {
				if reasons, ok := sel.Packages[pkg.ImportPath]; ok {
					plan.RootPackages = append(plan.RootPackages, PackagePlan{pkg.ImportPath, strings.Join(reasons, "; ")})
				}
			}
		default:
			if reasons, ok := sel.Modules[m.Dir]; ok {
				plan.Modules = append(plan.Modules, ModulePlan{Dir: m.Dir, Path: m.Path, VetOnly: vetOnlyModules[m.Dir], Reason: strings.Join(reasons, "; ")})
			}
		}
	}
	plan.planLanes(ev)
	return plan, nil
}

func describeKind(kind string) string {
	switch kind {
	case "push":
		return "a push to develop"
	case "dispatch":
		return "a manual run"
	case "release":
		return "the release pull request"
	}
	return kind
}

func (p *Plan) addAll(g *Graph, m Module, isRoot bool) {
	if isRoot {
		for _, pkg := range g.PackagesOf(m.Path) {
			p.RootPackages = append(p.RootPackages, PackagePlan{pkg.ImportPath, "full scope: " + p.Reason})
		}
		return
	}
	p.Modules = append(p.Modules, ModulePlan{Dir: m.Dir, Path: m.Path, VetOnly: vetOnlyModules[m.Dir], Reason: "full scope: " + p.Reason})
}

func (p *Plan) lane(name string, run bool, why string) {
	p.Lanes[name] = LanePlan{Run: run, Reason: why}
}

// planLanes fills the lane table. The always-on jobs and the pull-request-only jobs follow the event; the test
// lanes follow the scope; integration, cluster and benchmark follow the event alone, as agreed in #212 and #210.
func (p *Plan) planLanes(ev Event) {
	isPR := ev.Name == "pull_request"
	impact := p.Mode == "full" || len(p.RootPackages) > 0 || len(p.Modules) > 0

	p.lane(laneFlow, true, "always")
	p.lane(laneUnitGate, true, "always")
	p.lane(laneVuln, true, "always")
	p.lane(laneLint, isPR, ifElse(isPR, "pull request", "lint only runs on pull requests"))
	p.lane(laneAPI, isPR, ifElse(isPR, "pull request", "the API check only runs on pull requests"))

	noImpact := "no file with test impact changed"
	p.lane(laneTidy, impact, ifElse(impact, "a file with test impact changed", noImpact))
	p.lane(laneTestMin, impact, ifElse(impact, "a file with test impact changed", noImpact))

	rootRun := len(p.RootPackages) > 0
	rootWhy := fmt.Sprintf("%d root package(s) to run", len(p.RootPackages))
	rootOff := "no root package is affected"
	p.lane(laneTest, rootRun, ifElse(rootRun, rootWhy, rootOff))
	p.lane(laneTestReport, rootRun, ifElse(rootRun, rootWhy, rootOff))
	p.lane(laneRace, rootRun, ifElse(rootRun, rootWhy, rootOff))

	modRun := len(p.Modules) > 0
	p.lane(laneModules, modRun, ifElse(modRun, fmt.Sprintf("%d nested module(s) to build and vet", len(p.Modules)), "no nested module is affected"))

	heavy := ev.fullByEvent()
	heavyWhy := ifElse(heavy, describeKind(ev.Kind())+" runs it", fmt.Sprintf("never runs on a %s pull request", ev.Kind()))
	p.lane(laneInttest, heavy, heavyWhy)
	p.lane(laneBenchmark, heavy, heavyWhy)
	p.lane(laneCluster, heavy, heavyWhy)
}

func ifElse(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// LaneMap is the compact form of the lanes the workflow reads: a job name to whether it has to run.
func (p *Plan) LaneMap() map[string]bool {
	out := map[string]bool{}
	for name, l := range p.Lanes {
		out[name] = l.Run
	}
	return out
}

// LanesJSON is LaneMap as one line of JSON.
func (p *Plan) LanesJSON() (string, error) {
	b, err := json.Marshal(p.LaneMap())
	return string(b), err
}

// ModulesMatrix is the matrix of the modules job. GitHub rejects an empty matrix, so a plan without nested
// modules returns a placeholder row; the lane is off then and the job is skipped before the row is read.
func (p *Plan) ModulesMatrix() (string, error) {
	type row struct {
		Dir     string `json:"dir"`
		VetOnly string `json:"vet-only"`
	}
	rows := []row{}
	for _, m := range p.Modules {
		rows = append(rows, row{Dir: m.Dir, VetOnly: ifElse(m.VetOnly, "yes", "no")})
	}
	if len(rows) == 0 {
		rows = append(rows, row{Dir: "none", VetOnly: "yes"})
	}
	b, err := json.Marshal(map[string][]row{"include": rows})
	return string(b), err
}

// RacePackages is what the race job runs: everything in a full run, otherwise the selected root packages.
func (p *Plan) RacePackages() string {
	if p.Mode == "full" {
		return "./..."
	}
	return strings.Join(p.rootImportPaths(), " ")
}

func (p *Plan) rootImportPaths() []string {
	out := make([]string, 0, len(p.RootPackages))
	for _, pk := range p.RootPackages {
		out = append(out, pk.ImportPath)
	}
	sort.Strings(out)
	return out
}

// SelectedPackagesFile is the list test-matrix.sh intersects with `go list ./...`. It is empty in a full run,
// where the planner takes everything.
func (p *Plan) SelectedPackagesFile() string {
	if p.Mode == "full" {
		return ""
	}
	return strings.Join(p.rootImportPaths(), "\n")
}

// Summary is the readable plan for the job summary: scope, modules, packages and lanes, with the reasons.
func (p *Plan) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "### CI plan: %s\n\n", p.Mode)
	fmt.Fprintf(&b, "Event: `%s` (%s). Reason: %s.\n\n", p.Event, p.Kind, p.Reason)

	b.WriteString("#### Lanes\n\n| Job | Runs | Why |\n|---|---|---|\n")
	for _, name := range allLanes {
		l := p.Lanes[name]
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", name, ifElse(l.Run, "yes", "**omitted**"), l.Reason)
	}

	fmt.Fprintf(&b, "\n#### Nested modules (%d)\n\n", len(p.Modules))
	if len(p.Modules) == 0 {
		b.WriteString("None.\n")
	} else {
		b.WriteString("| Module | Runs | Why |\n|---|---|---|\n")
		for _, m := range p.Modules {
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", m.Dir, ifElse(m.VetOnly, "build and vet", "build, vet and test"), m.Reason)
		}
	}

	fmt.Fprintf(&b, "\n#### Root module packages (%d)\n\n", len(p.RootPackages))
	if len(p.RootPackages) == 0 {
		b.WriteString("None.\n")
	} else if p.Mode == "full" {
		b.WriteString("All of them (`./...`).\n")
	} else {
		b.WriteString("| Package | Why |\n|---|---|\n")
		for _, pk := range p.RootPackages {
			fmt.Fprintf(&b, "| `%s` | %s |\n", pk.ImportPath, pk.Reason)
		}
	}

	if len(p.Ignored) > 0 {
		fmt.Fprintf(&b, "\n#### Changed files with no test impact (%d)\n\n", len(p.Ignored))
		for _, ig := range p.Ignored {
			fmt.Fprintf(&b, "- `%s`: %s\n", ig.Path, ig.Reason)
		}
	}
	return b.String()
}
