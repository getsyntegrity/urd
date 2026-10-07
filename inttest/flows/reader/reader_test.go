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

package reader_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/persistence/conformance/readertck"
)

// issueCase returns the catalogue scenario of the late-commit case of #348: a@100
// and b@200 commit, then late@150, appended earlier by a concurrent transaction,
// commits after the reader already passed 200. It is taken from the catalogue by
// name; this package defines no scenario.
func issueCase(ctx *specs.Context) readertck.Scenario {
	ctx.Helper()
	for _, sc := range readertck.Catalogue() {
		if sc.Name == readertck.ScenarioIssueCase {
			return sc
		}
	}
	ctx.Expect(readertck.ScenarioIssueCase).To(specs.Equal("a scenario of the catalogue"))
	return readertck.Scenario{}
}

// backendError is the first error any backend of a run recorded.
func backendError(made []*pgBackend) error {
	var errs []error
	for _, b := range made {
		errs = append(errs, b.err)
		if open := b.openTxs(); len(open) > 0 {
			errs = append(errs, fmt.Errorf("writer transactions left open at the end of the run: %v", open))
		}
	}
	return errors.Join(errs...)
}

// The unit tests of readertck already run every scenario against fakes and
// testkit. These two tests add what only a real database can show, and each one
// proves a different thing about the same scenario.
func TestLateCommitOverPostgreSQL(t *testing.T) {
	specs.Describe(t, "the late-commit case of #348 over a real PostgreSQL", func(s *specs.Spec) {
		s.It("the current GetShardEvents omits the late commit: a known failure, asserted so that it turns red when the reader is replaced", func(ctx *specs.Context) {
			var made []*pgBackend
			tr, err := readertck.Run(issueCase(ctx), pgFactory(t, newLegacy, &made))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(backendError(made)).To(specs.BeNil())

			v := readertck.Evaluate(tr)
			ctx.Expect(v.Safety.Status).To(specs.Equal(readertck.Fail))
			ctx.Expect(v.Safety.Has(readertck.CodeOmission)).To(specs.BeTrue())
		})

		s.It("a correct reader over the same adapter and the same writers delivers every confirmed event, so the omission belongs to the timestamp cursor and not to the backend", func(ctx *specs.Context) {
			var made []*pgBackend
			tr, err := readertck.Run(issueCase(ctx), pgFactory(t, newStoreSet, &made))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(backendError(made)).To(specs.BeNil())

			v := readertck.Evaluate(tr)
			ctx.Expect(v.Failed()).To(specs.BeFalse())
			ctx.Expect(v.Safety.Violations).To(specs.BeEmpty())
		})
	})
}
