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

package logging

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	kitlog "github.com/pablogore/kit-logger/pkg/logger"
	"github.com/pablogore/kit-logger/pkg/logger/kitlogtest"
)

func TestDefaultLoggerIsKitLoggerGlobal(t *testing.T) {
	specs.Describe(t, "DefaultLogger resolves the kit-logger global on every call", func(s *specs.Spec) {
		s.It("returns the current global logger, looked up per call and never cached", func(ctx *specs.Context) {
			ctx.Expect(kitlog.L() == DefaultLogger()).To(specs.BeTrue())

			previous := kitlog.L()
			ctx.Cleanup(func() { kitlog.SetGlobal(previous) })

			custom := kitlogtest.NewMockLogger()
			kitlog.SetGlobal(custom)
			ctx.Expect(DefaultLogger() == kitlog.Logger(custom)).To(specs.BeTrue())
		})
	})
}

func TestResolveLogger(t *testing.T) {
	specs.Describe(t, "ResolveLogger falls back to the default for an unusable logger", func(s *specs.Spec) {
		s.It("nil falls back to the default", func(ctx *specs.Context) {
			ctx.Expect(ResolveLogger(nil) == DefaultLogger()).To(specs.BeTrue())
		})

		s.It("typed nil falls back to the default", func(ctx *specs.Context) {
			var typedNil *kitlogtest.MockLogger
			ctx.Expect(ResolveLogger(typedNil) == DefaultLogger()).To(specs.BeTrue())
		})

		s.It("a usable logger is returned as-is", func(ctx *specs.Context) {
			logger := kitlogtest.NewMockLogger()
			ctx.Expect(ResolveLogger(logger) == kitlog.Logger(logger)).To(specs.BeTrue())
		})
	})
}

// TestArchitectureLoggingStaysRuntimeNeutral guards the reason this package exists:
// migration resolves its logger here precisely because the dependency
// closure carries no actor runtime. The architecture-checker rules only see direct
// imports, so this asserts the transitive closure via go list -deps.
func TestArchitectureLoggingStaysRuntimeNeutral(t *testing.T) {
	specs.Describe(t, "the import graph of internal/logging", func(s *specs.Spec) {
		s.It("reaches neither GoAkt nor the GoAkt logging seam", func(ctx *specs.Context) {
			goBin, err := exec.LookPath("go")
			if err != nil {
				ctx.T.Skip("the go tool is not on PATH")
			}

			out, err := exec.Command(goBin, "list", "-deps", ".").CombinedOutput()
			var listErr error
			if err != nil {
				listErr = fmt.Errorf("go list -deps failed: %w\n%s", err, out)
			}
			ctx.Expect(listErr).To(specs.BeNil())

			deps := strings.Fields(string(out))
			// The guard first: an empty or truncated graph would prove nothing.
			ctx.Expect(deps).To(specs.Contain("github.com/getsyntegrity/urd/internal/logging"))
			ctx.Expect(deps).To(specs.NoElement(specs.Satisfy(
				"a GoAkt package: internal/logging must not depend on GoAkt, directly or transitively",
				func(dep any) bool { return strings.HasPrefix(dep.(string), "github.com/tochemey/goakt") })))
			ctx.Expect(deps).To(specs.NoElement(specs.Satisfy(
				"the GoAkt logging seam: internal/logging must not depend on it",
				func(dep any) bool { return strings.HasSuffix(dep.(string), "/internal/goaktlog") })))
		})
	})
}
