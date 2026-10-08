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

package readertck

import (
	"errors"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

type property uint8

const (
	onSafety property = iota + 1
	onEligibility
	onProgress
)

func (v Verdict) of(p property) PropertyVerdict {
	switch p {
	case onSafety:
		return v.Safety
	case onEligibility:
		return v.Eligibility
	default:
		return v.Progress
	}
}

type mutantRow struct {
	name     string
	mk       func(*fakeBackend) Subject
	scenario string
	fails    property
	code     string
	// stillPasses is a property the defect must NOT trip, proving the three
	// properties are judged separately. Zero means no claim.
	stillPasses property
}

func mutantRows() []mutantRow {
	return []mutantRow{
		{"skips a late commit (timestamp cursor)", newTSReader(false), ScenarioIssueCase, onSafety, CodeOmission, 0},
		{"skips a late commit (insertion-id cursor)", newOrdReader(ordConfig{noHorizon: true}), ScenarioIDOrderLate, onSafety, CodeOmission, 0},
		{"skips a tie group completed by a later commit", newTSReader(false), ScenarioTieLateCommit, onSafety, CodeOmission, 0},
		{"cuts a tie group at the batch limit", newTSReader(true), ScenarioTieBatchCut, onSafety, CodeOmission, 0},
		{"duplicates with a new idempotence token on every delivery", newOrdReader(ordConfig{countTokens: true}), ScenarioRetry, onSafety, CodeDupToken, 0},
		{"ignores the scope selection (unscoped read leaks tenants)", newOrdReader(ordConfig{ignoreScope: true}), ScenarioScopeSelection, onSafety, CodeWrongSelection, 0},
		{"ignores scope validation (serves the wildcard unprivileged)", newOrdReader(ordConfig{ignoreScope: true}), ScenarioRejection, onSafety, CodeNotRejected, 0},
		{"ignores the slice range", newOrdReader(ordConfig{ignoreSlice: true}), ScenarioSliceRange, onSafety, CodeWrongSelection, 0},
		{"resumes past the persisted cursor after a restart", newOrdReader(ordConfig{resumeSkip: true}), ScenarioRestartResume, onSafety, CodeOmission, 0},
		{"delivers events that never committed", newOrdReader(ordConfig{noHorizon: true, uncommitted: true}), ScenarioAbortReleases, onSafety, CodePhantom, 0},
		{"stalls before every batch although no writer is open", newOrdReader(ordConfig{stall: 3}), ScenarioIssueCase, onProgress, CodeStall, onSafety},
		{"makes committed events wait one tick past T", newSetReader(6), ScenarioIssueCase, onEligibility, CodeLateVisibility, onSafety},
	}
}

func TestHarnessFailsEveryMutant(t *testing.T) {
	specs.Describe(t, "each deliberately faulty reader is caught, by the right property and rule", func(s *specs.Spec) {
		specs.Table(s, mutantRows(), func(r mutantRow) string { return r.name }, func(ctx *specs.Context, r mutantRow) {
			v, err := verdictOf(scenarioNamed(r.scenario), fakeFactory(r.mk))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(v.Failed()).To(specs.BeTrue())
			ctx.Expect(v.of(r.fails).Status).To(specs.Equal(Fail))
			ctx.Expect(v.of(r.fails).Has(r.code)).To(specs.BeTrue())
			if r.stillPasses != 0 {
				ctx.Expect(v.of(r.stillPasses).Status).To(specs.Equal(Pass))
			}
		})
	})
}

// rejectLeaker is a reader that refuses an invalid request with ErrRejected but
// serves an event next to the error.
type rejectLeaker struct{ inner Subject }

func (l rejectLeaker) Poll(req Request) (Result, error) {
	res, err := l.inner.Poll(req)
	if errors.Is(err, ErrRejected) {
		e := ev("a1", scopeA, "a1", 1, 20)
		return Result{Deliveries: []Delivery{{Event: e, Token: e.Key().String()}}}, err
	}
	return res, err
}

// tamperer delivers the right events, with the right identity and token, but
// with a slice or timestamp that differs from the journal.
type tamperer struct {
	inner Subject
	slice bool
	ts    bool
}

func (t tamperer) Poll(req Request) (Result, error) {
	res, err := t.inner.Poll(req)
	for i := range res.Deliveries {
		if t.slice {
			res.Deliveries[i].Event.Slice += 7
		}
		if t.ts {
			res.Deliveries[i].Event.Timestamp += 7
		}
	}
	return res, err
}

func TestRunKeepsDeliveriesServedWithAnError(t *testing.T) {
	specs.Describe(t, "events served next to a rejection are not hidden from the oracle", func(s *specs.Spec) {
		s.It("flags delivered-on-reject for a reader that leaks an event with ErrRejected", func(ctx *specs.Context) {
			leak := func(b *fakeBackend) Subject { return rejectLeaker{inner: newOrdReader(ordConfig{})(b)} }
			tr, err := Run(scenarioNamed(ScenarioRejection), fakeFactory(leak))
			ctx.Expect(err).To(specs.BeNil())
			recorded := 0
			for _, rec := range tr.Polls {
				if rec.Err != nil {
					recorded += len(rec.Deliveries)
				}
			}
			ctx.Expect(recorded > 0).To(specs.BeTrue())
			v := Evaluate(tr)
			ctx.Expect(v.Safety.Status).To(specs.Equal(Fail))
			ctx.Expect(v.Safety.Has(codeDeliveredReject)).To(specs.BeTrue())
			ctx.Expect(v.Safety.Has(CodeUnexpectedError)).To(specs.BeFalse())
		})
		s.It("keeps passing the reader that rejects correctly", func(ctx *specs.Context) {
			v, err := verdictOf(scenarioNamed(ScenarioRejection), fakeFactory(newOrdReader(ordConfig{})))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(v.Failed()).To(specs.BeFalse())
		})
	})
}

func TestOracleComparesDeliveredFieldsWithTheTruth(t *testing.T) {
	type row struct {
		name  string
		slice bool
		ts    bool
		want  bool
	}
	specs.Describe(t, "a delivery whose slice or timestamp differs from the journal violates safety", func(s *specs.Spec) {
		specs.Table(s, []row{
			{"altered slice", true, false, true},
			{"altered timestamp", false, true, true},
			{"faithful delivery", false, false, false},
		}, func(r row) string { return r.name }, func(ctx *specs.Context, r row) {
			mk := func(b *fakeBackend) Subject {
				return tamperer{inner: newSetReader(0)(b), slice: r.slice, ts: r.ts}
			}
			v, err := verdictOf(scenarioNamed(ScenarioIssueCase), fakeFactory(mk))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(v.Safety.Has(CodeAlteredEvent)).To(specs.Equal(r.want))
			ctx.Expect(v.Failed()).To(specs.Equal(r.want))
		})
	})
}

func TestMutantsAreCaughtWithoutTheCursor(t *testing.T) {
	specs.Describe(t, "the oracle's expectations do not come from the reader's cursor", func(s *specs.Spec) {
		s.It("names the omitted event from the ground truth alone", func(ctx *specs.Context) {
			v, err := verdictOf(scenarioNamed(ScenarioIssueCase), fakeFactory(newTSReader(false)))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(v.Safety.Violations).To(specs.HaveLen(1))
			ctx.Expect(v.Safety.Violations[0].Detail).To(specs.MatchRegex("late@150"))
		})
		s.It("gives the same verdict for a reader whose cursor is garbage but whose deliveries are right", func(ctx *specs.Context) {
			scrambled := func(b *fakeBackend) Subject { return cursorScrambler{inner: &setReader{b: b}} }
			v, err := verdictOf(scenarioNamed(ScenarioIssueCase), fakeFactory(scrambled))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(v.Failed()).To(specs.BeFalse())
		})
	})
}

// cursorScrambler lets the inner reader keep its real state in a side table and
// hands the harness a meaningless cursor: the harness must treat it as opaque.
type cursorScrambler struct{ inner *setReader }

func (c cursorScrambler) Poll(req Request) (Result, error) {
	res, err := c.inner.Poll(Request{Selection: req.Selection, Slices: req.Slices, Privileged: req.Privileged, Cursor: restoreSet(req.Cursor), Limit: req.Limit})
	res.Cursor = append([]byte("opaque:"), res.Cursor...)
	return res, err
}

func restoreSet(c []byte) []byte {
	const prefix = "opaque:"
	if len(c) < len(prefix) {
		return nil
	}
	return c[len(prefix):]
}
