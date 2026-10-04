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

package engine

import (
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

// commandArchitectureAllowedModules names the only module-qualified import
// path prefixes command/ may depend on (design.md's package-level import
// allowlist, EGO-WRITE-003): google.golang.org/protobuf and this repo's
// own tenancy/, which command composes rather than reimplements (W3). No
// GoAkt, transport, auth library or other first-party runtime package.
var commandArchitectureAllowedModules = []string{
	"google.golang.org/protobuf",
	"github.com/getsyntegrity/urd/tenancy",
}

// TestArchitectureCommand enforces design.md's import allowlist for
// command/ (EGO-WRITE-003): stdlib, google.golang.org/protobuf and
// tenancy only — no GoAkt, no engine, no transport or auth
// library. It mirrors TestArchitectureTenancy's mechanism (a real `go
// list -deps ./command/...` subprocess, not a source-text scan) so it
// also catches transitive dependencies.
func TestArchitectureCommand(t *testing.T) {
	specs.Describe(t, "Command Architecture", func(s *specs.Spec) {
		s.It("holds", func(sc *specs.Context) {
			t := sc.T
			goBin, err := tenancyArchitectureGoBinary()
			sc.Expect(err).To(specs.BeNil())

			root := architectureModuleRoot(t)

			// The command package itself is always present in its own `-deps`
			// output; it is the subject under test, not a dependency it
			// acquired, so it must be excluded from the allowlist check below
			// rather than trivially failing it.
			commandPackages := tenancyArchitectureGoList(t, goBin, root, "list", "./command/...")
			sc.Expect(commandPackages).To(specs.Not(specs.BeEmpty()))

			self := make(map[string]struct{}, len(commandPackages))
			for _, pkg := range commandPackages {
				self[pkg] = struct{}{}
			}

			deps := tenancyArchitectureGoList(t, goBin, root, "list", "-deps", "./command/...")
			sc.Expect(deps).To(specs.Not(specs.BeEmpty()))

			var (
				checked    int
				disallowed []string
			)
			for _, dep := range deps {
				if _, isSelf := self[dep]; isSelf {
					continue
				}
				checked++

				first, _, _ := strings.Cut(dep, "/")
				if !strings.Contains(first, ".") {
					continue // standard library
				}

				var allowed bool
				for _, module := range commandArchitectureAllowedModules {
					if dep == module || strings.HasPrefix(dep, module+"/") {
						allowed = true
						break
					}
				}
				if !allowed {
					disallowed = append(disallowed, dep)
				}
			}
			// command/ is a leaf package: only the standard library, google.golang.org/protobuf
			// and tenancy are allowed (design.md's import allowlist, EGO-WRITE-003), with
			// no GoAkt, transport, auth or other first-party runtime dependency.
			sc.Expect(disallowed).To(specs.BeEmpty())
			sc.Expect(checked).To(specs.Not(specs.BeZero()))
		})
	})
}

// tenancyArchitectureGoList and tenancyArchitectureGoBinary are defined in
// tenancy_architecture_test.go and reused here unchanged.
