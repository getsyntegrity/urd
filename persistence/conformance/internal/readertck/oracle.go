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
	"fmt"
	"sort"
)

// Status is the outcome of one property for one scenario.
type Status uint8

// Property outcomes.
const (
	// NotApplicable: the scenario does not assert the property.
	NotApplicable Status = iota
	// Pass: asserted and no violation.
	Pass
	// Fail: asserted and violated.
	Fail
	// NotClaimed: eligibility only. The scenario's conditions do not all hold,
	// so no visibility bound is claimed and none is judged. Safety is still
	// judged.
	NotClaimed
)

// String names the status.
func (s Status) String() string {
	return [...]string{"not-applicable", "pass", "fail", "not-claimed"}[s]
}

// Violation codes.
const (
	// Safety.
	CodeOmission        = "omission"         // a confirmed event was never delivered
	CodePhantom         = "phantom"          // delivered before commit, or never committed
	CodeUnknownEvent    = "unknown-event"    // delivered an event nobody wrote
	CodeWrongSelection  = "wrong-selection"  // outside the consumer's scope or slice range
	CodeOrder           = "order"            // per-persistence-id order inverted
	CodeDupInBatch      = "dup-in-batch"     // one batch carries an event twice
	CodeDupToken        = "dup-token"        // idempotence signal not stable or not unique
	CodeMissingToken    = "missing-token"    // no idempotence signal
	CodeNotRejected     = "not-rejected"     // an invalid request was served
	CodeUnexpectedError = "unexpected-error" // an error where none, or another one, was expected
	// Eligibility.
	CodeLateVisibility = "late-visibility" // confirmed event not visible by commit tick + T
	// Progress.
	CodeStall           = "stall"          // quiescent but not moving
	CodeNoConvergence   = "no-convergence" // quiescent drain never settled
	CodeNotExercised    = "not-exercised"  // the scenario never tested what it claims
	codeDeliveredReject = "delivered-on-reject"
)

// Violation is one broken rule with a deterministic explanation.
type Violation struct {
	Code   string
	Detail string
}

// PropertyVerdict is the verdict for one property.
type PropertyVerdict struct {
	Status     Status
	Violations []Violation
}

// Has reports whether the verdict contains a violation with code.
func (p PropertyVerdict) Has(code string) bool {
	for _, v := range p.Violations {
		if v.Code == code {
			return true
		}
	}
	return false
}

// Verdict holds the three properties, kept separate on purpose: a reader can
// be safe and slow, or fast and unsafe.
type Verdict struct {
	Safety      PropertyVerdict
	Eligibility PropertyVerdict
	Progress    PropertyVerdict
}

// Failed reports whether any asserted property failed.
func (v Verdict) Failed() bool {
	return v.Safety.Status == Fail || v.Eligibility.Status == Fail || v.Progress.Status == Fail
}

type consumerState struct {
	delivered map[EventKey]bool
	tokens    map[EventKey]string
	owners    map[string]EventKey
	lastSeq   map[[2]string]uint64
}

// Evaluate judges a trace. Expected deliveries come only from tr.Truth.
func Evaluate(tr *Trace) Verdict {
	sc := tr.Scenario
	var safety, elig, prog []Violation
	states := map[string]*consumerState{}
	for _, c := range sc.Consumers {
		states[c.Name] = &consumerState{
			delivered: map[EventKey]bool{}, tokens: map[EventKey]string{},
			owners: map[string]EventKey{}, lastSeq: map[[2]string]uint64{},
		}
	}
	claimed := sc.Conditions.All()
	dueChecked, quiescentDrains := 0, 0

	for i, rec := range tr.Polls {
		c, _ := sc.Consumer(rec.Consumer)
		st := states[c.Name]
		at := fmt.Sprintf("poll %d (%s)", i, rec)
		if c.ExpectReject {
			switch {
			case rec.Err == nil:
				safety = append(safety, Violation{CodeNotRejected, at + ": an invalid request was served"})
			case !errors.Is(rec.Err, ErrRejected):
				safety = append(safety, Violation{CodeUnexpectedError, at + ": want ErrRejected"})
			}
			if len(rec.Deliveries) > 0 {
				safety = append(safety, Violation{codeDeliveredReject, at})
			}
			continue
		}
		if rec.Err != nil {
			safety = append(safety, Violation{CodeUnexpectedError, at})
			continue
		}
		seenInBatch := map[EventKey]bool{}
		for _, d := range rec.Deliveries {
			key := d.Event.Key()
			te, ok := tr.Truth.Lookup(key)
			switch {
			case !ok:
				safety = append(safety, Violation{CodeUnknownEvent, fmt.Sprintf("%s: %s", at, key)})
				continue
			case te.CommitSeq == 0 || te.CommitSeq > rec.Commits:
				safety = append(safety, Violation{CodePhantom, fmt.Sprintf("%s: %s (%s) was not committed (aborted=%v)", at, te.Label, key, te.Aborted)})
			case !c.Selection.Matches(te.Scope) || !c.Slices.Contains(te.Slice):
				safety = append(safety, Violation{CodeWrongSelection, fmt.Sprintf("%s: %s (%s) is outside the consumer's selection", at, te.Label, key)})
			}
			if seenInBatch[key] {
				safety = append(safety, Violation{CodeDupInBatch, fmt.Sprintf("%s: %s twice", at, key)})
			}
			seenInBatch[key] = true
			if d.Token == "" {
				safety = append(safety, Violation{CodeMissingToken, fmt.Sprintf("%s: %s", at, key)})
			} else {
				if prev, seen := st.tokens[key]; seen && prev != d.Token {
					safety = append(safety, Violation{CodeDupToken, fmt.Sprintf("%s: %s redelivered with a different token (%q then %q)", at, key, prev, d.Token)})
				}
				if owner, seen := st.owners[d.Token]; seen && owner != key {
					safety = append(safety, Violation{CodeDupToken, fmt.Sprintf("%s: token %q reused for %s and %s", at, d.Token, owner, key)})
				}
				st.tokens[key], st.owners[d.Token] = d.Token, key
			}
			if !st.delivered[key] {
				pid := [2]string{key.Scope.String(), key.PersistenceID}
				if last, seen := st.lastSeq[pid]; seen && key.Seq <= last {
					safety = append(safety, Violation{CodeOrder, fmt.Sprintf("%s: %s after sequence %d of the same persistence id", at, key, last)})
				}
				st.lastSeq[pid] = key.Seq
				st.delivered[key] = true
			}
		}
		if rec.End == nil {
			continue
		}
		if rec.End.Quiescent {
			quiescentDrains++
			switch rec.End.Reason {
			case DrainStalled:
				prog = append(prog, Violation{CodeStall, at + ": quiescent, events pending, reader not moving"})
			case DrainCapped:
				prog = append(prog, Violation{CodeNoConvergence, at + ": quiescent drain did not settle"})
			}
		}
		if claimed {
			for _, te := range tr.Truth.Confirmed(c, rec.Commits) {
				if te.CommitTick+sc.T > rec.End.Tick {
					continue
				}
				dueChecked++
				if !st.delivered[te.Key()] {
					elig = append(elig, Violation{CodeLateVisibility, fmt.Sprintf("%s: %s committed at tick %d, due by %d, not visible", at, te.Label, te.CommitTick, te.CommitTick+sc.T)})
				}
			}
		}
	}

	// Omissions: judged once every writer has resolved, over the whole run.
	if tr.Truth.Open() == 0 {
		for _, c := range sc.Consumers {
			if c.ExpectReject {
				continue
			}
			for _, te := range tr.Truth.Confirmed(c, tr.Truth.Commits()) {
				if !states[c.Name].delivered[te.Key()] {
					safety = append(safety, Violation{CodeOmission, fmt.Sprintf("consumer %s: %s (%s, ts %d) committed but never delivered", c.Name, te.Label, te.Key(), te.Timestamp)})
				}
			}
		}
	}

	v := Verdict{}
	v.Safety = judge(sc.Asserts&Safety != 0, safety)
	switch {
	case sc.Asserts&Eligibility == 0:
	case !claimed:
		v.Eligibility.Status = NotClaimed
	default:
		if dueChecked == 0 {
			elig = append(elig, Violation{CodeNotExercised, "no confirmed event was ever due at a drain"})
		}
		v.Eligibility = judge(true, elig)
	}
	if sc.Asserts&Progress != 0 {
		if quiescentDrains == 0 {
			prog = append(prog, Violation{CodeNotExercised, "no drain ran with every writer resolved"})
		}
		v.Progress = judge(true, prog)
	}
	return v
}

func judge(asserted bool, vs []Violation) PropertyVerdict {
	if !asserted {
		return PropertyVerdict{}
	}
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].Code < vs[j].Code })
	if len(vs) > 0 {
		return PropertyVerdict{Status: Fail, Violations: vs}
	}
	return PropertyVerdict{Status: Pass}
}
