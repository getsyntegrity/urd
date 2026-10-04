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

package runtime_test

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

// allowedDependencies lists every non-standard-library package that
// port/runtime may depend on (openspec/changes/ego-runtime-001/design.md §D9):
// the command, tenancy, eventstream and port/behavior contracts, the packages
// eventstream brings in (internal/queue, internal/syncmap, uuid, atomic) and
// the protobuf runtime. The list covers the interfaces that arrive in S4-3, so
// it does not change then. It is intentionally narrower than the former architecture checker's
// contract-allowlist rule: any new dependency should be a reviewed change to
// this list. An allowlist, rather than a denylist of GoAkt, also catches a
// dependency nobody thought to forbid.
var allowedDependencies = []string{
	"github.com/getsyntegrity/urd/command",
	"github.com/getsyntegrity/urd/tenancy",
	"github.com/getsyntegrity/urd/eventstream",
	"github.com/getsyntegrity/urd/port/behavior",
	"github.com/getsyntegrity/urd/internal/queue",
	"github.com/getsyntegrity/urd/internal/syncmap",
	"github.com/google/uuid",
	"go.uber.org/atomic",
	"google.golang.org/protobuf/",
}

// TestArchitectureRuntimeDependsOnlyOnContracts walks the resolved import graph of
// port/runtime's production build with a real `go list -deps` subprocess, so
// transitive dependencies are checked too, not only the direct imports.
func TestArchitectureRuntimeDependsOnlyOnContracts(t *testing.T) {
	specs.Describe(t, "the production import graph of port/runtime", func(s *specs.Spec) {
		s.It("holds only the standard library and the allowed contracts", func(ctx *specs.Context) {
			self := goList(ctx, "list", ".")
			ctx.Expect(self).To(specs.HaveLen(1))

			external := nonStdlibDeps(goList(ctx, "list", "-deps", "."), self[0])
			// The guard first: with nothing to check, the test would prove nothing.
			ctx.Expect(external).To(specs.Not(specs.BeEmpty()))
			ctx.Expect(external).To(specs.EveryElement(specs.Satisfy(
				"an allowed dependency of port/runtime: it is a contract and may import only the standard library "+
					"and other contracts (openspec/changes/ego-runtime-001/design.md §D9)",
				func(dep any) bool { return isAllowed(dep.(string)) })))
		})
	})
}

// enginePackage is the engine package, the GoAkt adapter; goaktPrefix covers every
// GoAkt package.
const (
	enginePackage = "github.com/getsyntegrity/urd/engine"
	goaktPrefix   = "github.com/tochemey/goakt/"
	externalTests = "github.com/getsyntegrity/urd/port/runtime_test"
)

// TestArchitectureRuntimeTestClosureExcludesGoAktAndRoot walks the test build of
// port/runtime, which includes the runtime double of double_test.go
// (design §D7, §D9), and rejects GoAkt and the engine package in it: the double
// implements runtime.Runtime with neither.
func TestArchitectureRuntimeTestClosureExcludesGoAktAndRoot(t *testing.T) {
	specs.Describe(t, "the test build of port/runtime", func(s *specs.Spec) {
		var pkgs []string
		s.BeforeEach(func(ctx *specs.Context) {
			for _, line := range goList(ctx, "list", "-deps", "-test", "-f", "{{.ImportPath}}", ".") {
				// A package recompiled for the test binary is listed as
				// "path [path.test]"; strings.Fields has already split that suffix off.
				if !strings.HasPrefix(line, "[") {
					pkgs = append(pkgs, line)
				}
			}
			// The guard first: without the external test package holding the runtime double,
			// the test would prove nothing.
			ctx.Expect(pkgs).To(specs.Contain(externalTests))
		})

		s.It("never reaches the engine package (design §D7)", func(ctx *specs.Context) {
			ctx.Expect(pkgs).To(specs.NoElement(specs.Equal(enginePackage)))
		})

		s.It("never reaches a GoAkt package (design §D7)", func(ctx *specs.Context) {
			ctx.Expect(pkgs).To(specs.NoElement(specs.Satisfy("a GoAkt package", func(pkg any) bool {
				return strings.HasPrefix(pkg.(string), goaktPrefix)
			})))
		})
	})
}

func isAllowed(dep string) bool {
	for _, allowed := range allowedDependencies {
		if dep == allowed || (strings.HasSuffix(allowed, "/") && strings.HasPrefix(dep, allowed)) {
			return true
		}
	}
	return false
}

// nonStdlibDeps keeps the dependencies outside the standard library, leaving
// out the package itself.
func nonStdlibDeps(deps []string, self string) []string {
	var out []string
	for _, dep := range deps {
		first, _, _ := strings.Cut(dep, "/")
		if dep != self && strings.Contains(first, ".") {
			out = append(out, dep)
		}
	}
	return out
}

// goList runs `go` with args and returns the fields of its output.
func goList(ctx *specs.Context, args ...string) []string {
	goBin, err := exec.LookPath("go")
	var lookErr error
	if err != nil {
		lookErr = fmt.Errorf("go toolchain not found on PATH: %w", err)
	}
	ctx.Expect(lookErr).To(specs.BeNil())
	cmd := exec.Command(goBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	var runErr error
	if err := cmd.Run(); err != nil {
		runErr = fmt.Errorf("go %s failed: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	ctx.Expect(runErr).To(specs.BeNil())
	return strings.Fields(stdout.String())
}
