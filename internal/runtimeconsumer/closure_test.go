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

package runtimeconsumer

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

// hermeticGoEnv returns the process environment with GOWORK=off and an
// empty GOFLAGS, so the child `go list` below never inherits a stray
// go.work file or ambient flags (mirrors migration/closure_test.go).
func hermeticGoEnv() []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, "GOWORK=") || strings.HasPrefix(kv, "GOFLAGS=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GOWORK=off", "GOFLAGS=")
}

// modulePrefix is the root module's import path; every first-party package
// starts with it.
const modulePrefix = "github.com/getsyntegrity/urd"

// allowedFirstParty is the closed set of first-party packages the
// consumer's production build may contain besides itself: the two
// contracts it is written against, the contracts they import (command,
// tenancy, eventstream), eventstream's runtime-free internal utilities, and
// the testpb messages. It mirrors port/runtime's architecture test
// (ego-runtime-001 §D9). A new first-party dependency must be added here in
// review, which is the point.
var allowedFirstParty = []any{
	modulePrefix + "/port/runtime",
	modulePrefix + "/port/behavior",
	modulePrefix + "/command",
	modulePrefix + "/tenancy",
	modulePrefix + "/eventstream",
	modulePrefix + "/internal/queue",
	modulePrefix + "/internal/syncmap",
	modulePrefix + "/internal/testpb",
}

// isGoAkt reports whether dep is the GoAkt runtime module or one of its packages.
func isGoAkt(dep string) bool {
	return dep == "github.com/tochemey/goakt/v4" || strings.HasPrefix(dep, "github.com/tochemey/goakt/v4/")
}

// productionDeps lists the import paths in the production build of the
// package under test (`go list -deps .`, so no test-only imports).
func productionDeps(ctx *specs.Context) []string {
	cmd := exec.Command("go", "list", "-deps", ".")
	cmd.Env = hermeticGoEnv()
	out, err := cmd.CombinedOutput()
	var runErr error
	if err != nil {
		runErr = fmt.Errorf("go list -deps .: %w\n%s", err, out)
	}
	ctx.Expect(runErr).To(specs.BeNil())
	return strings.Fields(string(out))
}

// firstPartyDeps keeps the first-party packages of deps other than the
// package under test itself.
func firstPartyDeps(deps []string) []string {
	var own []string
	for _, dep := range deps {
		if strings.HasPrefix(dep, modulePrefix+"/") && dep != modulePrefix+"/internal/runtimeconsumer" {
			own = append(own, dep)
		}
	}
	return own
}

// TestArchitectureRuntimeConsumerProductionClosureExcludesRootAndGoAkt is #147's closure criterion
// (ego-runtime-001 §D8): the consumer's production build reaches only
// port/runtime, port/behavior and contracts — every first-party package in
// it must be on allowedFirstParty — and never the engine package or any
// GoAkt package. Only the production build is checked; the end-to-end test
// that needs GoAkt lives in compose/goakt.
func TestArchitectureRuntimeConsumerProductionClosureExcludesRootAndGoAkt(t *testing.T) {
	specs.Describe(t, "the production closure of runtimeconsumer", func(s *specs.Spec) {
		var deps []string
		s.BeforeEach(func(ctx *specs.Context) { deps = productionDeps(ctx) })

		s.It("never reaches the GoAkt runtime", func(ctx *specs.Context) {
			ctx.Expect(deps).To(specs.NoElement(specs.Satisfy("a GoAkt package", func(dep any) bool {
				return isGoAkt(dep.(string))
			})))
		})

		s.It("never reaches the engine package", func(ctx *specs.Context) {
			ctx.Expect(deps).To(specs.NoElement(specs.Equal(modulePrefix + "/engine")))
		})

		s.It("contains only port/runtime, port/behavior and contracts as first-party packages", func(ctx *specs.Context) {
			ctx.Expect(firstPartyDeps(deps)).To(specs.EveryElement(specs.BeOneOf(allowedFirstParty...)))
		})

		s.It("contains the two contracts it is written against", func(ctx *specs.Context) {
			ctx.Expect(deps).To(specs.ContainAllOf(modulePrefix+"/port/runtime", modulePrefix+"/port/behavior"))
		})
	})
}
