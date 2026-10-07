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
	"fmt"
	"slices"
)

// EmptyPollBudget is how many consecutive polls without a new event a reader
// may spend, with undelivered confirmed events waiting and no writer open,
// before the runner records a stall. Advancing past aborted or filtered ranges
// may legitimately take a poll or two; a reader that keeps answering empty
// without moving is stuck.
const EmptyPollBudget = 2

// Phase says whether a poll came from the script or from the final drain the
// runner adds once every writer has resolved.
type Phase uint8

// Poll phases.
const (
	PhaseScript Phase = iota + 1
	PhaseFinal
)

// DrainReason says why a Drain stopped.
type DrainReason uint8

// Drain outcomes.
const (
	// DrainDone: the reader had nothing more to give.
	DrainDone DrainReason = iota + 1
	// DrainStalled: quiescent, events pending, and the reader kept answering
	// without a new event beyond EmptyPollBudget.
	DrainStalled
	// DrainCapped: the poll cap was reached without the reader settling.
	DrainCapped
)

// DrainEnd closes a Drain on its last poll.
type DrainEnd struct {
	Tick      int64
	Quiescent bool
	Reason    DrainReason
}

// PollRecord is everything observed about one poll.
type PollRecord struct {
	Consumer   string
	Phase      Phase
	Tick       int64
	Commits    int
	Persisted  bool
	Deliveries []Delivery
	NewCount   int
	Err        error
	End        *DrainEnd
}

// Trace is what a run leaves for the oracle: the ground truth and every poll.
type Trace struct {
	Scenario Scenario
	Truth    *Truth
	Polls    []PollRecord
}

type runner struct {
	sc        Scenario
	backend   Backend
	truth     *Truth
	reader    Subject
	persisted map[string][]byte
	delivered map[string]map[EventKey]bool
	trace     *Trace
}

// Run executes sc against a fresh backend from factory and returns the trace.
// It validates the scenario first. The truth log is fed from the script only.
func Run(sc Scenario, factory Factory) (*Trace, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	b := factory(sc)
	r := &runner{
		sc:        sc,
		backend:   b,
		truth:     NewTruth(),
		reader:    b.NewReader(),
		persisted: map[string][]byte{},
		delivered: map[string]map[EventKey]bool{},
	}
	r.trace = &Trace{Scenario: sc, Truth: r.truth}
	for _, c := range sc.Consumers {
		r.delivered[c.Name] = map[EventKey]bool{}
	}
	for _, st := range sc.Steps {
		r.step(st)
	}
	r.final()
	return r.trace, nil
}

func (r *runner) step(st Step) {
	switch st.Op {
	case OpBegin:
		r.truth.Begin(st.Tx)
		r.backend.Begin(st.Tx)
	case OpAppend:
		r.truth.Append(st.Tx, st.Event)
		r.backend.Append(st.Tx, st.Event)
	case OpCommit:
		r.truth.Commit(st.Tx)
		r.backend.Commit(st.Tx)
	case OpAbort:
		r.truth.Abort(st.Tx)
		r.backend.Abort(st.Tx)
	case OpTick:
		r.truth.Tick(st.Ticks)
		r.backend.Tick(st.Ticks)
	case OpRestart:
		r.reader = r.backend.NewReader()
	case OpPoll:
		c, _ := r.sc.Consumer(st.Consumer)
		r.poll(c, st.Limit, !st.NoPersist, PhaseScript)
	case OpDrain:
		r.drain(st)
	}
}

func (r *runner) poll(c Consumer, limit int, persist bool, phase Phase) *PollRecord {
	res, err := r.reader.Poll(Request{
		Selection:  c.Selection,
		Slices:     c.Slices,
		Privileged: c.Privileged,
		Cursor:     slices.Clone(r.persisted[c.Name]),
		Limit:      limit,
	})
	rec := PollRecord{Consumer: c.Name, Phase: phase, Tick: r.truth.Clock(), Commits: r.truth.Commits(), Err: err}
	if err == nil {
		rec.Deliveries = slices.Clone(res.Deliveries)
		for _, d := range rec.Deliveries {
			if !r.delivered[c.Name][d.Event.Key()] {
				rec.NewCount++
				r.delivered[c.Name][d.Event.Key()] = true
			}
		}
		if persist {
			r.persisted[c.Name] = slices.Clone(res.Cursor)
			rec.Persisted = true
		}
	}
	r.trace.Polls = append(r.trace.Polls, rec)
	return &r.trace.Polls[len(r.trace.Polls)-1]
}

func (r *runner) pollCap() int { return 4*len(r.truth.events) + 50 }

func (r *runner) drain(st Step) {
	c, _ := r.sc.Consumer(st.Consumer)
	quiescent := r.truth.Quiescent(r.sc.Settle)
	noNew := 0
	reason := DrainCapped
	var last *PollRecord
	for polls := 0; polls < r.pollCap(); polls++ {
		last = r.poll(c, st.Limit, true, PhaseScript)
		if last.Err != nil {
			reason = DrainDone
			break
		}
		pending := r.truth.PendingNew(c, r.delivered[c.Name])
		if last.NewCount > 0 {
			noNew = 0
		} else {
			noNew++
		}
		if !quiescent {
			if last.NewCount == 0 {
				reason = DrainDone
				break
			}
			continue
		}
		if len(last.Deliveries) == 0 && pending == 0 {
			reason = DrainDone
			break
		}
		if pending > 0 && noNew > EmptyPollBudget {
			reason = DrainStalled
			break
		}
	}
	if last != nil {
		last.End = &DrainEnd{Tick: r.truth.Clock(), Quiescent: quiescent, Reason: reason}
	}
}

// final gives every consumer a generous budget to catch up once no writer is
// open. It judges safety only: how many polls it took is Progress's business,
// measured in the scripted drains.
func (r *runner) final() {
	if r.truth.Open() != 0 {
		return
	}
	for _, c := range r.sc.Consumers {
		if c.ExpectReject {
			r.poll(c, 1, true, PhaseFinal)
			continue
		}
		for polls := 0; polls < 2*r.pollCap(); polls++ {
			rec := r.poll(c, 1, true, PhaseFinal)
			if rec.Err != nil || (len(rec.Deliveries) == 0 && r.truth.PendingNew(c, r.delivered[c.Name]) == 0) {
				break
			}
		}
	}
}

// String summarises a poll for failure messages.
func (p PollRecord) String() string {
	return fmt.Sprintf("%s@%d: %d delivered (%d new), err=%v", p.Consumer, p.Tick, len(p.Deliveries), p.NewCount, p.Err)
}
