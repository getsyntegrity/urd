package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ArchitecturePrefix names the tests of the architecture lane (issue #208): the tests that read the resolved import
// graph with `go list`, or scan the sources, to enforce a dependency boundary. The shards and the race job skip
// them, test (min) skips them too, and the architecture job is the only one that runs them: one run per event, with
// the minimum Go on a pull request to main (docs/testing/architecture-tests.md).
const ArchitecturePrefix = "TestArchitecture"

// sourceSensitive are the packages whose architecture tests look at files outside their own import closure, so the
// import graph cannot tell when they have to run: a change in any production Go file of the repository can break
// the rule they check, without the package importing the file that changed.
//
//   - engine: TestArchitectureCommand and TestArchitectureTenancy list the dependencies of ./command/... and
//     ./tenancy/..., which the package does not have to import; TestArchitectureKitLoggerIsTheOnlyLoggingBackend
//     reads every production file of the modules.
//   - port/adapter: the contract packages must NOT import port/adapter, so a new such import is a change in a package
//     that port/adapter never reaches; the assertion-site tests parse every production file of every module.
//
// A package that is not listed here is selected only through the graph. The test of this file keeps the list from
// drifting: a package whose architecture tests parse or walk the sources must be in it.
var sourceSensitive = map[string]string{
	"engine":       "its architecture tests read ./command/..., ./tenancy/... and every production file",
	"port/adapter": "its architecture tests parse every production file and check the contract packages",
}

// isProductionGo reports whether a changed path is a Go file the source-reading architecture tests can see.
func isProductionGo(p string) bool {
	return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
}

// ArchitectureTestsPlan is one package whose architecture tests the lane has to run, and why.
type ArchitectureTestsPlan struct {
	ImportPath string   `json:"importPath"`
	Tests      []string `json:"tests"`
	Reason     string   `json:"reason"`
}

// ArchitectureGroup is the architecture packages of one module, run from that module's directory.
type ArchitectureGroup struct {
	Dir      string                  `json:"dir"`
	Packages []ArchitectureTestsPlan `json:"packages"`
}

// planArchitecture selects the architecture tests. A full plan takes every one. A selective plan takes the packages
// the graph selected, with the same reason, and adds the source-sensitive packages when a production Go file changed.
func (p *Plan) planArchitecture(g *Graph, sel *Selection, changes []Change) {
	trigger := ""
	if p.Mode != "full" {
		for _, c := range changes {
			if isProductionGo(c.Path) {
				trigger = c.Path
				break
			}
		}
	}
	for _, m := range g.Modules {
		group := ArchitectureGroup{Dir: m.Dir, Packages: []ArchitectureTestsPlan{}}
		for _, pkg := range g.PackagesOf(m.Path) {
			if len(pkg.ArchitectureTests) == 0 {
				continue
			}
			reason := ""
			switch {
			case p.Mode == "full":
				reason = "full scope: " + p.Reason
			case sel != nil && len(sel.Packages[pkg.ImportPath]) > 0:
				reason = strings.Join(sel.Packages[pkg.ImportPath], "; ")
			case trigger != "" && sourceSensitive[pkg.Dir] != "":
				reason = fmt.Sprintf("%s changed, and %s", trigger, sourceSensitive[pkg.Dir])
			}
			if reason == "" {
				continue
			}
			group.Packages = append(group.Packages, ArchitectureTestsPlan{ImportPath: pkg.ImportPath, Tests: pkg.ArchitectureTests, Reason: reason})
		}
		if len(group.Packages) > 0 {
			p.Architecture = append(p.Architecture, group)
		}
	}
}

// ArchitectureJSON is the architecture lane as one line of JSON, the input of the `impact architecture` command.
func (p *Plan) ArchitectureJSON() (string, error) {
	b, err := json.Marshal(p.Architecture)
	return string(b), err
}

// testEvent is the part of a `go test -json` event the lane needs.
type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

func parseEvents(raw []byte) []testEvent {
	var out []testEvent
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var e testEvent
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Action != "" {
			out = append(out, e)
		}
	}
	return out
}

// VerifyArchitecture checks the events of one group against the tests the plan expected. `go test` reports "no tests
// to run" as a success, so the exit status alone cannot tell a lane that ran nothing: every expected top-level test
// must have a pass event, and a skipped one does not count. It returns how many passed and one message per problem.
func VerifyArchitecture(group ArchitectureGroup, events []testEvent) (int, []string) {
	final := map[string]string{} // "package\ttest" -> last pass, fail or skip
	for _, e := range events {
		if e.Test == "" || strings.Contains(e.Test, "/") {
			continue
		}
		switch e.Action {
		case "pass", "fail", "skip":
			final[e.Package+"\t"+e.Test] = e.Action
		}
	}
	var problems []string
	passed, expected := 0, 0
	for _, pkg := range group.Packages {
		for _, test := range pkg.Tests {
			expected++
			switch got := final[pkg.ImportPath+"\t"+test]; got {
			case "pass":
				passed++
			case "":
				problems = append(problems, fmt.Sprintf("%s: %s was expected and did not run", pkg.ImportPath, test))
			default:
				problems = append(problems, fmt.Sprintf("%s: %s ended with %q", pkg.ImportPath, test, got))
			}
		}
	}
	if expected > 0 && passed == 0 {
		problems = append(problems, fmt.Sprintf("%s: none of the %d expected architecture tests passed", group.Dir, expected))
	}
	return passed, problems
}

// goTest runs `go test` in dir and returns its -json output. A non-zero exit status is an error, with the output.
type goTest func(dir string, args ...string) ([]byte, error)

// goVersion reports the toolchain that runs a module's tests, as `go version` prints it from that directory. The
// go command can switch to the toolchain a go.mod asks for, so the version of the runner is not enough to tell.
type goVersion func(dir string) string

func execGoVersion(dir string) string {
	cmd := exec.Command("go", "version")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	if f := strings.Fields(string(out)); len(f) >= 3 {
		return f[2]
	}
	return "unknown"
}

func execGoTest(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", append([]string{"test"}, args...)...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return stdout.Bytes(), err
}

// runArchitecture writes the Markdown summary to out and the error annotations to logs. It runs the planned architecture tests, one `go test` per module, and fails when any of them fails
// or when an expected test did not pass. Each module runs with the toolchain the environment gives it, and the
// summary names it. There is no -race and no coverage: these tests resolve the import graph
// and scan sources, work the instrumentation only slows down. -count=1 keeps a cached result from standing in.
func runArchitecture(args []string, planJSON string, out, logs io.Writer, run goTest, version goVersion) error {
	fs := flag.NewFlagSet("architecture", flag.ContinueOnError)
	outDir := fs.String("out", "", "directory for the raw `go test -json` output of every module")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var groups []ArchitectureGroup
	if err := json.Unmarshal([]byte(planJSON), &groups); err != nil {
		return fmt.Errorf("ARCHITECTURE is not an architecture plan: %w", err)
	}
	if len(groups) == 0 {
		return errors.New("the architecture lane has nothing to run: the plan requires it only when it selected a test")
	}
	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			return err
		}
	}

	fmt.Fprint(out, "### Architecture tests\n\n| Module | Go | Packages | Tests passed | Expected |\n|---|---|---|---|---|\n")
	var problems []string
	for _, group := range groups {
		pkgs, expected := make([]string, 0, len(group.Packages)), 0
		for _, pkg := range group.Packages {
			pkgs = append(pkgs, pkg.ImportPath)
			expected += len(pkg.Tests)
		}
		goArgs := append([]string{"-count=1", "-timeout=8m", "-run", "^" + ArchitecturePrefix, "-json"}, pkgs...)
		raw, err := run(group.Dir, goArgs...)
		if *outDir != "" {
			name := "architecture-" + strings.NewReplacer("/", "_", ".", "root").Replace(group.Dir) + ".json"
			if werr := os.WriteFile(filepath.Join(*outDir, name), raw, 0o644); werr != nil {
				return werr
			}
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: go test failed: %v", group.Dir, err))
		}
		passed, bad := VerifyArchitecture(group, parseEvents(raw))
		problems = append(problems, bad...)
		fmt.Fprintf(out, "| `%s` | %s | %d | %d | %d |\n", group.Dir, version(group.Dir), len(pkgs), passed, expected)
	}
	for _, p := range problems {
		fmt.Fprintln(logs, "::error::"+p)
	}
	if len(problems) > 0 {
		return fmt.Errorf("%d problem(s) in the architecture lane", len(problems))
	}
	fmt.Fprintln(out, "\nEvery expected architecture test passed.")
	return nil
}
