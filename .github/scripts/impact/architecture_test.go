package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/getsyntegrity/go-specs/specs"
)

// archFixture is the repository fixture with architecture tests in the engine (internal test), in testkit (external
// test package) and in the publisher module, plus a helper that is not a test.
func archFixture() fstest.MapFS {
	return with(repoFixture(), map[string]string{
		"engine/arch_test.go":                  "package engine\n\nimport \"testing\"\n\nfunc TestArchitectureCommand(t *testing.T) {}\nfunc TestArchitectureAbout(t *testing.T) {}\nfunc TestEngineWorks(t *testing.T)       {}\nfunc architectureHelper()                {}\n",
		"testkit/testkit_architecture_test.go": "package testkit_test\n\nimport \"testing\"\n\nfunc TestArchitectureTestkit(t *testing.T) {}\n",
		"publisher/arch_test.go":               "package publisher\n\nimport \"testing\"\n\nfunc TestArchitectureClosure(t *testing.T) {}\n",
	})
}

func archPackages(p *Plan) []string {
	out := []string{}
	for _, g := range p.Architecture {
		for _, pkg := range g.Packages {
			out = append(out, g.Dir+":"+pkg.ImportPath[strings.LastIndex(pkg.ImportPath, "urd")+len("urd"):])
		}
	}
	return out
}

func TestImpactArchitectureLane(t *testing.T) {
	specs.Describe(t, "the architecture lane of the impact plan", func(s *specs.Spec) {
		s.It("finds the top-level TestArchitecture* tests of a package, internal or external, and nothing else", func(ctx *specs.Context) {
			g := mustGraph(ctx, archFixture())
			ctx.Expect(g.Packages["example.com/urd/engine"].ArchitectureTests).To(specs.Equal([]string{"TestArchitectureAbout", "TestArchitectureCommand"}))
			ctx.Expect(g.Packages["example.com/urd/testkit"].ArchitectureTests).To(specs.Equal([]string{"TestArchitectureTestkit"}))
			ctx.Expect(g.Packages["example.com/urd/leaf"].ArchitectureTests).To(specs.BeEmpty())
		})

		s.It("takes every architecture test, in every module, when the plan is full", func(ctx *specs.Context) {
			plan := planFor(ctx, Event{Name: "push"}, archFixture(), nil)
			ctx.Expect(archPackages(plan)).To(specs.Equal([]string{
				".:/engine", ".:/testkit", "publisher:/publisher"}))
			ctx.Expect(plan.Lanes["architecture"].Run).To(specs.BeTrue())
		})

		s.It("takes the packages the graph selected, with their reason, and no other", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, archFixture(), changed("port/port.go"))
			ctx.Expect(archPackages(plan)).To(specs.Equal([]string{".:/engine", ".:/testkit", "publisher:/publisher"}))
			ctx.Expect(plan.Architecture[0].Packages[0].Reason).To(specs.StartWith("depends on example.com/urd/port"))
		})

		s.It("leaves the lane off when no package with architecture tests is reached", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, archFixture(), changed("leaf/leaf_test.go"))
			ctx.Expect(plan.Architecture).To(specs.BeEmpty())
			ctx.Expect(plan.Lanes["architecture"].Run).To(specs.BeFalse())
		})

		s.It("leaves the lane off for a documentation-only change", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, archFixture(), changed("docs/guide.md"))
			ctx.Expect(plan.Lanes["architecture"].Run).To(specs.BeFalse())
		})

		s.It("runs a source-sensitive package when a production file changes in a package it does not import", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, archFixture(), changed("leaf/leaf.go"))
			ctx.Expect(archPackages(plan)).To(specs.Equal([]string{".:/engine"}))
			ctx.Expect(plan.Architecture[0].Packages[0].Reason).To(specs.StartWith("leaf/leaf.go changed, and its architecture tests read"))
		})

		s.It("does not run a source-sensitive package for a test file or a document that no scan reads", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, archFixture(), changed("leaf/leaf_test.go", "docs/guide.md"))
			ctx.Expect(plan.Architecture).To(specs.BeEmpty())
		})

		s.It("runs a source-sensitive package for a production file of a nested module", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, archFixture(), changed("example/main.go"))
			ctx.Expect(archPackages(plan)).To(specs.Equal([]string{".:/engine"}))
		})

		s.It("exposes the plan as one line of JSON for the workflow, an empty list and not null when nothing runs", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, archFixture(), changed("docs/guide.md"))
			raw, err := plan.ArchitectureJSON()
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(raw).ToEqual("[]")

			plan = planFor(ctx, Event{Name: "push"}, archFixture(), nil)
			raw, err = plan.ArchitectureJSON()
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(strings.Contains(raw, "\n")).To(specs.BeFalse())
			ctx.Expect(strings.Contains(raw, `"tests":["TestArchitectureAbout","TestArchitectureCommand"]`)).To(specs.BeTrue())
		})

		s.It("lists the architecture packages and why in the summary", func(ctx *specs.Context) {
			plan := planFor(ctx, featurePR, archFixture(), changed("leaf/leaf.go"))
			ctx.Expect(strings.Contains(plan.Summary(), "#### Architecture tests (1 packages)")).To(specs.BeTrue())
			ctx.Expect(strings.Contains(plan.Summary(), "| `.` | `example.com/urd/engine` | 2 |")).To(specs.BeTrue())
		})
	})
}

// repoSourceSensitive guards the list against drift on the real repository: an architecture test that parses or walks
// the sources looks at files its import closure does not contain, so its package must be declared source-sensitive.
func TestSourceSensitiveListMatchesTheRepository(t *testing.T) {
	scanners := []string{"go/ast", "go/parser", "go/token", "io/fs"}
	specs.Describe(t, "the source-sensitive architecture packages", func(s *specs.Spec) {
		s.It("are exactly the architecture packages that parse or walk the sources", func(ctx *specs.Context) {
			g, err := LoadGraph(os.DirFS("../../.."))
			ctx.Expect(err).To(specs.BeNil())
			found := map[string]bool{}
			for _, pkg := range g.Packages {
				if len(pkg.ArchitectureTests) == 0 {
					continue
				}
				for _, imp := range append(append([]string{}, pkg.TestImports...), pkg.XTestImports...) {
					if contains(scanners, imp) {
						found[pkg.Dir] = true
					}
				}
			}
			for dir := range found {
				_, listed := sourceSensitive[dir]
				ctx.Expect(listed).To(specs.BeTrue())
			}
			for dir := range sourceSensitive {
				ctx.Expect(found[dir]).To(specs.BeTrue())
			}
			ctx.Expect(len(found) > 0).To(specs.BeTrue())
		})
	})
}

func group(dir string, tests map[string][]string) ArchitectureGroup {
	g := ArchitectureGroup{Dir: dir, Packages: []ArchitectureTestsPlan{}}
	for pkg, names := range tests {
		g.Packages = append(g.Packages, ArchitectureTestsPlan{ImportPath: pkg, Tests: names, Reason: "test"})
	}
	return g
}

func pass(pkg, test string) testEvent { return testEvent{Action: "pass", Package: pkg, Test: test} }

func TestVerifyArchitecture(t *testing.T) {
	g := group(".", map[string][]string{"p/a": {"TestArchitectureOne", "TestArchitectureTwo"}})
	specs.Describe(t, "the check that the architecture lane ran what the plan expected", func(s *specs.Spec) {
		s.It("accepts a run where every expected test passed", func(ctx *specs.Context) {
			passed, problems := VerifyArchitecture(g, []testEvent{pass("p/a", "TestArchitectureOne"), pass("p/a", "TestArchitectureTwo"), pass("p/a", "")})
			ctx.Expect(passed).ToEqual(2)
			ctx.Expect(problems).To(specs.BeEmpty())
		})

		s.It("ignores subtests, which are covered by their parent", func(ctx *specs.Context) {
			passed, problems := VerifyArchitecture(g, []testEvent{pass("p/a", "TestArchitectureOne"), pass("p/a", "TestArchitectureTwo"), pass("p/a", "TestArchitectureTwo/sub")})
			ctx.Expect(passed).ToEqual(2)
			ctx.Expect(problems).To(specs.BeEmpty())
		})

		s.It("fails when a run selected cases and executed none, which go test itself reports as success", func(ctx *specs.Context) {
			passed, problems := VerifyArchitecture(g, nil)
			ctx.Expect(passed).ToEqual(0)
			ctx.Expect(strings.Join(problems, "\n")).To(specs.MatchRegex("none of the 2 expected architecture tests passed"))
		})

		s.It("fails for the one expected test that did not run, naming it", func(ctx *specs.Context) {
			_, problems := VerifyArchitecture(g, []testEvent{pass("p/a", "TestArchitectureOne")})
			ctx.Expect(problems).To(specs.Equal([]string{"p/a: TestArchitectureTwo was expected and did not run"}))
		})

		s.It("does not count a skipped test as executed", func(ctx *specs.Context) {
			_, problems := VerifyArchitecture(g, []testEvent{pass("p/a", "TestArchitectureOne"), {Action: "skip", Package: "p/a", Test: "TestArchitectureTwo"}})
			ctx.Expect(problems).To(specs.Equal([]string{`p/a: TestArchitectureTwo ended with "skip"`}))
		})

		s.It("fails a test that failed", func(ctx *specs.Context) {
			_, problems := VerifyArchitecture(g, []testEvent{pass("p/a", "TestArchitectureOne"), {Action: "fail", Package: "p/a", Test: "TestArchitectureTwo"}})
			ctx.Expect(problems).To(specs.Equal([]string{`p/a: TestArchitectureTwo ended with "fail"`}))
		})

		s.It("tells the same test name of two packages apart", func(ctx *specs.Context) {
			two := group(".", map[string][]string{"p/a": {"TestArchitectureOne"}, "p/b": {"TestArchitectureOne"}})
			_, problems := VerifyArchitecture(two, []testEvent{pass("p/a", "TestArchitectureOne")})
			ctx.Expect(problems).To(specs.Equal([]string{"p/b: TestArchitectureOne was expected and did not run"}))
		})
	})
}

func TestRunArchitecture(t *testing.T) {
	plan := `[{"dir":".","packages":[{"importPath":"p/a","tests":["TestArchitectureOne"],"reason":"r"}]},` +
		`{"dir":"publisher/kafka","packages":[{"importPath":"p/k","tests":["TestArchitectureTwo"],"reason":"r"}]}]`
	events := func(pkg, test string) []byte {
		return []byte(`{"Action":"run","Package":"` + pkg + `","Test":"` + test + `"}` + "\n" +
			`{"Action":"pass","Package":"` + pkg + `","Test":"` + test + `"}` + "\n")
	}
	specs.Describe(t, "the architecture command", func(s *specs.Spec) {
		s.It("runs one go test per module, from its directory, without -race or coverage, and passes", func(ctx *specs.Context) {
			var calls [][]string
			var dirs []string
			run := func(dir string, args ...string) ([]byte, error) {
				dirs = append(dirs, dir)
				calls = append(calls, args)
				if dir == "." {
					return events("p/a", "TestArchitectureOne"), nil
				}
				return events("p/k", "TestArchitectureTwo"), nil
			}
			var out, logs bytes.Buffer
			ctx.Expect(runArchitecture(nil, plan, &out, &logs, run)).To(specs.BeNil())
			ctx.Expect(dirs).To(specs.Equal([]string{".", "publisher/kafka"}))
			ctx.Expect(calls[0]).To(specs.Equal([]string{"-count=1", "-timeout=8m", "-run", "^TestArchitecture", "-json", "p/a"}))
			for _, c := range calls {
				ctx.Expect(strings.Contains(strings.Join(c, " "), "-race")).To(specs.BeFalse())
				ctx.Expect(strings.Contains(strings.Join(c, " "), "cover")).To(specs.BeFalse())
			}
			ctx.Expect(strings.Contains(out.String(), "| `publisher/kafka` | 1 | 1 | 1 |")).To(specs.BeTrue())
			ctx.Expect(logs.String()).To(specs.BeEmpty())
		})

		s.It("fails when go test exits with an error, and still checks the other modules", func(ctx *specs.Context) {
			run := func(dir string, args ...string) ([]byte, error) {
				if dir == "." {
					return nil, errors.New("exit status 1")
				}
				return events("p/k", "TestArchitectureTwo"), nil
			}
			var out, logs bytes.Buffer
			err := runArchitecture(nil, plan, &out, &logs, run)
			ctx.Expect(err == nil).To(specs.BeFalse())
			ctx.Expect(strings.Contains(logs.String(), "::error::.: go test failed: exit status 1")).To(specs.BeTrue())
			ctx.Expect(strings.Contains(out.String(), "| `publisher/kafka` | 1 | 1 | 1 |")).To(specs.BeTrue())
		})

		s.It("fails when go test succeeds but ran none of the expected tests", func(ctx *specs.Context) {
			run := func(dir string, args ...string) ([]byte, error) {
				return []byte(`{"Action":"output","Package":"p/a","Output":"testing: warning: no tests to run\n"}` + "\n"), nil
			}
			var out, logs bytes.Buffer
			ctx.Expect(runArchitecture(nil, plan, &out, &logs, run) == nil).To(specs.BeFalse())
			ctx.Expect(strings.Contains(logs.String(), "none of the 1 expected architecture tests passed")).To(specs.BeTrue())
		})

		s.It("fails on an empty plan, because the lane only runs when the plan selected a test", func(ctx *specs.Context) {
			err := runArchitecture(nil, "[]", &bytes.Buffer{}, &bytes.Buffer{}, nil)
			ctx.Expect(err == nil).To(specs.BeFalse())
		})

		s.It("fails when the plan is not a plan", func(ctx *specs.Context) {
			err := runArchitecture(nil, "", &bytes.Buffer{}, &bytes.Buffer{}, nil)
			ctx.Expect(err == nil).To(specs.BeFalse())
		})
	})
}
