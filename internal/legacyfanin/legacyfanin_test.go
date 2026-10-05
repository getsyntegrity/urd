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

package legacyfanin

import (
	"bytes"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

const self = "github.com/getsyntegrity/urd/internal/legacyfanin"

// TestArchitectureLegacyFanInHasOnlyTheTwoKnownImporters pins the claim that
// the temporary fan-in is unreachable from the public Subscribe,
// AddEventPublishers and AddStatePublishers paths: the grant is imported only
// by the stream that checks it and by the projection actor that uses it, and
// the package is internal, so nothing outside this module can name it.
func TestArchitectureLegacyFanInHasOnlyTheTwoKnownImporters(t *testing.T) {
	specs.Describe(t, "the importers of internal/legacyfanin", func(s *specs.Spec) {
		s.It("are the events stream and the projection actor, nothing else", func(ctx *specs.Context) {
			goBin, err := exec.LookPath("go")
			ctx.Expect(err).To(specs.BeNil())
			cmd := exec.Command(goBin, "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "github.com/getsyntegrity/urd/...")
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			ctx.Expect(cmd.Run()).To(specs.BeNil())

			var importers []string
			for _, line := range strings.Split(out.String(), "\n") {
				fields := strings.Fields(line)
				for _, imp := range fields[min(1, len(fields)):] {
					if imp == self {
						importers = append(importers, fields[0])
					}
				}
			}
			sort.Strings(importers)
			ctx.Expect(importers).To(specs.Equal([]string{
				"github.com/getsyntegrity/urd/eventstream",
				"github.com/getsyntegrity/urd/internal/engine/projection",
			}))
		})
	})
}
