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

package publishingtest

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

const (
	rootModule         = "github.com/getsyntegrity/urd"
	publishingPath     = rootModule + "/port/publishing"
	publishingtestPath = publishingPath + "/publishingtest"
	egopbPath          = rootModule + "/egopb"
	adapterPath        = rootModule + "/port/adapter"
	protobufRuntime    = "google.golang.org/protobuf"
)

// Spec scenario "the architecture tests hold the line": the resolved
// import graph of publishingtest (a real `go list -deps`, so transitive
// dependencies count) holds only the standard library, port/publishing,
// egopb and the protobuf runtime egopb needs, and never port/adapter, so
// the package can move with port/publishing into the ego-arch-006
// contracts module (ego-arch-004 design §D8).
func TestArchitecturePublishingtestDependsOnlyOnStdlibPublishingAndEgopb(t *testing.T) {
	specs.Describe(t, "the import graph of port/publishing/publishingtest", func(s *specs.Spec) {
		var deps []string
		s.BeforeEach(func(ctx *specs.Context) {
			deps = goListDeps(ctx, ".")
			// The guard first: an empty or truncated graph would prove nothing.
			ctx.Expect(deps).To(specs.ContainAllOf(publishingtestPath, publishingPath, egopbPath, "testing"))
		})

		s.It("never reaches port/adapter", func(ctx *specs.Context) {
			ctx.Expect(deps).To(specs.NoElement(specs.Satisfy(
				"port/adapter or one of its packages: it would create a module cycle once port/publishing moves "+
					"(openspec/changes/ego-arch-004/design.md §D8)",
				func(dep any) bool {
					path := dep.(string)
					return path == adapterPath || strings.HasPrefix(path, adapterPath+"/")
				})))
		})

		s.It("holds only the standard library, port/publishing, egopb and the protobuf runtime", func(ctx *specs.Context) {
			ctx.Expect(deps).To(specs.EveryElement(specs.Satisfy(
				"publishingtest itself, port/publishing, egopb, the protobuf runtime or a standard library package: "+
					"it may import only the standard library, port/publishing and egopb (openspec/changes/ego-arch-004/design.md §D8)",
				func(dep any) bool {
					path := dep.(string)
					return path == publishingtestPath || path == publishingPath || path == egopbPath ||
						path == protobufRuntime || strings.HasPrefix(path, protobufRuntime+"/") ||
						isStdlib(path)
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
