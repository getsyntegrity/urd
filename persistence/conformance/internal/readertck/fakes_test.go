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
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
)

// This file holds the reference readers the tests feed to the harness. They are
// FAKES over an in-memory backend. Two of them are correct and use different
// mechanisms on purpose; the others are mutants, each with one planted defect.
// None is a proposal for a production reader.

type fbEvent struct {
	ev         Event
	tx         string
	ord        int // insertion order, assigned at Append
	commitSeq  int
	commitTick int64
	aborted    bool
}

// fakeBackend is an in-memory journal with explicit writer transactions.
type fakeBackend struct {
	events    []*fbEvent
	open      map[string]bool
	commits   int
	clock     int64
	newReader func(*fakeBackend) Subject
}

func fakeFactory(mk func(*fakeBackend) Subject) Factory {
	return func(Scenario) Backend {
		return &fakeBackend{open: map[string]bool{}, newReader: mk}
	}
}

func (b *fakeBackend) Begin(tx string) { b.open[tx] = true }

func (b *fakeBackend) Append(tx string, e Event) {
	b.events = append(b.events, &fbEvent{ev: e, tx: tx, ord: len(b.events) + 1})
}

func (b *fakeBackend) Commit(tx string) {
	b.commits++
	for _, e := range b.events {
		if e.tx == tx {
			e.commitSeq, e.commitTick = b.commits, b.clock
		}
	}
	delete(b.open, tx)
}

func (b *fakeBackend) Abort(tx string) {
	for _, e := range b.events {
		if e.tx == tx {
			e.aborted = true
		}
	}
	delete(b.open, tx)
}

func (b *fakeBackend) Tick(n int64)       { b.clock += n }
func (b *fakeBackend) NewReader() Subject { return b.newReader(b) }

// horizon is the lowest insertion order held by a still-open transaction.
func (b *fakeBackend) horizon() int {
	h := math.MaxInt
	for _, e := range b.events {
		if b.open[e.tx] && e.ord < h {
			h = e.ord
		}
	}
	return h
}

// admit refuses the requests every reader must refuse.
func admit(req Request) error {
	switch {
	case req.Selection.Kind == KindOneScope && !req.Selection.Scope.Valid():
		return fmt.Errorf("zero scope: %w", ErrRejected)
	case req.Selection.Kind == KindAllScopesInCell && !req.Privileged:
		return fmt.Errorf("all scopes without privilege: %w", ErrRejected)
	case req.Selection.Kind != KindOneScope && req.Selection.Kind != KindAllScopesInCell:
		return fmt.Errorf("no selection: %w", ErrRejected)
	}
	return nil
}

func delivery(e *fbEvent, token string) Delivery {
	if token == "" {
		token = e.ev.Key().String()
	}
	return Delivery{Event: e.ev, Token: token}
}

func mustJSON(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}

// ordConfig selects the behaviour of ordReader. The zero value is the correct
// reader "A" (stable prefix of the insertion order); every flag plants one
// defect.
type ordConfig struct {
	noHorizon   bool // read past writers still open, skipping their events: loses late commits
	uncommitted bool // with noHorizon: deliver those events instead of skipping them
	ignoreScope bool // no scope validation and no scope filter
	ignoreSlice bool // no slice filter
	resumeSkip  bool // the first poll of a restarted instance starts one event past the cursor
	countTokens bool // a new idempotence token on every delivery
	stall       int  // answer this many empty polls before every batch
}

// ordReader is correct reader A when cfg is zero. Mechanism: events are read in
// insertion order up to the lowest event still held by an open writer (the
// "stable prefix"); the cursor is the last position examined. It withholds
// events behind an open writer, which is allowed but means eligibility depends
// on the writers closing.
type ordReader struct {
	b       *fakeBackend
	cfg     ordConfig
	fresh   bool
	stalled int
	counter int
}

func newOrdReader(cfg ordConfig) func(*fakeBackend) Subject {
	return func(b *fakeBackend) Subject { return &ordReader{b: b, cfg: cfg, fresh: true} }
}

func (r *ordReader) Poll(req Request) (Result, error) {
	if !r.cfg.ignoreScope {
		if err := admit(req); err != nil {
			return Result{}, err
		}
	}
	var cur int
	if len(req.Cursor) > 0 {
		var c struct{ O int }
		if err := json.Unmarshal(req.Cursor, &c); err != nil {
			return Result{}, err
		}
		cur = c.O
	}
	if r.fresh {
		r.fresh = false
		if r.cfg.resumeSkip && req.Cursor != nil {
			cur++
		}
	}
	horizon := math.MaxInt
	if !r.cfg.noHorizon {
		horizon = r.b.horizon()
	}
	last := cur
	var out []Delivery
	for _, e := range r.b.events {
		if e.ord <= cur {
			continue
		}
		if e.ord >= horizon || len(out) >= req.Limit {
			break
		}
		last = e.ord
		committed := e.commitSeq > 0
		inScope := r.cfg.ignoreScope || req.Selection.Matches(e.ev.Scope)
		inSlice := r.cfg.ignoreSlice || req.Slices.Contains(e.ev.Slice)
		if e.aborted || !(committed || r.cfg.uncommitted) || !inScope || !inSlice {
			continue
		}
		token := ""
		if r.cfg.countTokens {
			r.counter++
			token = fmt.Sprintf("%s@%d", e.ev.Key(), r.counter)
		}
		out = append(out, delivery(e, token))
	}
	if len(out) > 0 && r.stalled < r.cfg.stall {
		r.stalled++
		return Result{Cursor: req.Cursor}, nil
	}
	if len(out) > 0 {
		r.stalled = 0
	}
	return Result{Deliveries: out, Cursor: mustJSON(struct{ O int }{last})}, nil
}

// setReader is correct reader "B", a different mechanism: the cursor is the set
// of delivered insertion positions, so a late commit is delivered as soon as it
// commits and nothing is ever held back. delay > 0 turns it into the
// eligibility mutant that makes every event wait that many ticks.
type setReader struct {
	b     *fakeBackend
	delay int64
}

func newSetReader(delay int64) func(*fakeBackend) Subject {
	return func(b *fakeBackend) Subject { return &setReader{b: b, delay: delay} }
}

func (r *setReader) Poll(req Request) (Result, error) {
	if err := admit(req); err != nil {
		return Result{}, err
	}
	var seen struct{ D []int }
	if len(req.Cursor) > 0 {
		if err := json.Unmarshal(req.Cursor, &seen); err != nil {
			return Result{}, err
		}
	}
	var cands []*fbEvent
	for _, e := range r.b.events {
		if e.commitSeq == 0 || e.aborted || slices.Contains(seen.D, e.ord) ||
			!req.Selection.Matches(e.ev.Scope) || !req.Slices.Contains(e.ev.Slice) ||
			e.commitTick+r.delay > r.b.clock {
			continue
		}
		cands = append(cands, e)
	}
	slices.SortFunc(cands, func(a, b *fbEvent) int {
		return cmp.Or(cmp.Compare(a.commitSeq, b.commitSeq), cmp.Compare(a.ord, b.ord))
	})
	if len(cands) > req.Limit {
		cands = cands[:req.Limit]
	}
	var out []Delivery
	for _, e := range cands {
		out = append(out, delivery(e, ""))
		seen.D = append(seen.D, e.ord)
	}
	slices.Sort(seen.D)
	return Result{Deliveries: out, Cursor: mustJSON(seen)}, nil
}

// tsReader orders by the writers' timestamp, as GetShardEvents does: the cursor
// is the highest timestamp delivered and the next read is strictly after it.
// With cutTie it also stops exactly at the batch limit even inside a group of
// equal timestamps. Both flavours lose events and are mutants.
type tsReader struct {
	b      *fakeBackend
	cutTie bool
}

func newTSReader(cutTie bool) func(*fakeBackend) Subject {
	return func(b *fakeBackend) Subject { return &tsReader{b: b, cutTie: cutTie} }
}

func (r *tsReader) Poll(req Request) (Result, error) {
	if err := admit(req); err != nil {
		return Result{}, err
	}
	cur := int64(math.MinInt64)
	if len(req.Cursor) > 0 {
		var c struct{ T int64 }
		if err := json.Unmarshal(req.Cursor, &c); err != nil {
			return Result{}, err
		}
		cur = c.T
	}
	var cands []*fbEvent
	for _, e := range r.b.events {
		if e.commitSeq > 0 && !e.aborted && e.ev.Timestamp > cur &&
			req.Selection.Matches(e.ev.Scope) && req.Slices.Contains(e.ev.Slice) {
			cands = append(cands, e)
		}
	}
	slices.SortFunc(cands, func(a, b *fbEvent) int {
		return cmp.Or(cmp.Compare(a.ev.Timestamp, b.ev.Timestamp), cmp.Compare(a.ev.PersistenceID, b.ev.PersistenceID), cmp.Compare(a.ev.Seq, b.ev.Seq))
	})
	if len(cands) > req.Limit {
		end := req.Limit
		for !r.cutTie && end < len(cands) && cands[end].ev.Timestamp == cands[end-1].ev.Timestamp {
			end++
		}
		cands = cands[:end]
	}
	var out []Delivery
	for _, e := range cands {
		out = append(out, delivery(e, ""))
		cur = e.ev.Timestamp
	}
	return Result{Deliveries: out, Cursor: mustJSON(struct{ T int64 }{cur})}, nil
}
