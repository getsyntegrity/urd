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

package adaptertest_test

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

const (
	adapterPath     = "github.com/getsyntegrity/urd/port/adapter"
	adaptertestPath = adapterPath + "/adaptertest"
)

// Spec scenario "the architecture tests hold the line": the resolved
// import graph of adaptertest (a real `go list -deps`, so transitive
// dependencies count) holds only the standard library and port/adapter,
// so a nested adapter module can run the suite without the runtime
// (ego-arch-004 design §D8).
//
// The rule covers the production build only (`go list -deps .` ignores test
// files), so this test file may use go-specs without breaking it.
func TestArchitectureAdaptertestDependsOnlyOnStdlibAndAdapter(t *testing.T) {
	specs.Describe(t, "the import graph of port/adapter/adaptertest", func(s *specs.Spec) {
		s.It("holds only the standard library and port/adapter besides the package itself", func(ctx *specs.Context) {
			deps := goListDeps(ctx, ".")
			// The guard first: an empty or truncated graph would prove nothing.
			ctx.Expect(deps).To(specs.ContainAllOf(adaptertestPath, adapterPath, "testing"))
			ctx.Expect(deps).To(specs.EveryElement(specs.Satisfy(
				"adaptertest itself, port/adapter or a standard library package: it may import only the standard "+
					"library and port/adapter (openspec/changes/ego-arch-004/design.md §D8)",
				func(dep any) bool {
					return dep == adaptertestPath || dep == adapterPath || isStdlib(dep.(string))
				})))
		})
	})
}

// isStdlib reports whether an import path belongs to the standard library:
// its first path element has no dot.
func isStdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

func goListDeps(ctx *specs.Context, pkg string) []string {
	goBin, err := exec.LookPath("go")
	var lookErr error
	if err != nil {
		lookErr = fmt.Errorf("go toolchain not found on PATH: %w", err)
	}
	ctx.Expect(lookErr).To(specs.BeNil())
	cmd := exec.Command(goBin, "list", "-deps", pkg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	var runErr error
	if err := cmd.Run(); err != nil {
		runErr = fmt.Errorf("go list -deps %s failed: %w\n%s", pkg, err, stderr.String())
	}
	ctx.Expect(runErr).To(specs.BeNil())
	return strings.Fields(stdout.String())
}
