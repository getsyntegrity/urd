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

package behavior_test

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

// allowedDependencies lists every non-standard-library package that
// port/behavior may depend on: the command contract, the tenancy contract that
// command imports, and the protobuf runtime. It is intentionally narrower than
// the former architecture checker's contract-allowlist rule (ADR ego-arch-001 design.md §3), which
// would also admit egopb and other contracts: port/behavior needs none of them,
// and any new dependency should be a reviewed change to this list. An
// allowlist, rather than a denylist of GoAkt or OpenTelemetry, also catches a
// dependency nobody thought to forbid.
var allowedDependencies = []string{
	"github.com/getsyntegrity/urd/command",
	"github.com/getsyntegrity/urd/tenancy",
	"google.golang.org/protobuf/",
}

// TestArchitectureBehaviorDependsOnlyOnContracts walks the resolved import graph of
// port/behavior with a real `go list -deps` subprocess, so transitive
// dependencies are checked too, not only the direct imports.
func TestArchitectureBehaviorDependsOnlyOnContracts(t *testing.T) {
	specs.Describe(t, "the import graph of port/behavior", func(s *specs.Spec) {
		s.It("holds only the standard library, the allowed contracts and the protobuf runtime", func(ctx *specs.Context) {
			self := goList(ctx, "list", ".")
			ctx.Expect(self).To(specs.HaveLen(1))

			external := nonStdlibDeps(goList(ctx, "list", "-deps", "."), self[0])
			// The guard first: with nothing to check, the test would prove nothing.
			ctx.Expect(external).To(specs.Not(specs.BeEmpty()))
			ctx.Expect(external).To(specs.EveryElement(specs.Satisfy(
				"an allowed dependency of port/behavior: contracts may import only the standard library, other contracts, egopb and the protobuf runtime (openspec/changes/ego-arch-001/design.md §3)",
				func(dep any) bool { return isAllowed(dep.(string)) })))
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
