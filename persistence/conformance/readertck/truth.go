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
	"slices"
)

// TruthEvent is one appended event with what the ground truth knows about its
// fate. CommitSeq is 0 until the event commits; it then counts commits in the
// order they happened (the real commit order, which the writers' timestamps
// and append order need not follow).
type TruthEvent struct {
	Event
	Tx         string
	AppendIdx  int
	CommitSeq  int
	CommitTick int64
	Aborted    bool
}

// Truth is the ground-truth log. The scenario script feeds it, event by event,
// while it drives the backend; it never learns anything from a reader or from
// a cursor. It is the only source the oracle trusts.
type Truth struct {
	clock        int64
	commits      int
	lastResolve  int64
	open         map[string]bool
	events       []*TruthEvent
	eventsByTx   map[string][]*TruthEvent
	appendsTotal int
}

// NewTruth returns an empty log.
func NewTruth() *Truth {
	return &Truth{open: map[string]bool{}, eventsByTx: map[string][]*TruthEvent{}}
}

// Begin records that tx opened.
func (t *Truth) Begin(tx string) { t.open[tx] = true }

// Append records an uncommitted event of tx.
func (t *Truth) Append(tx string, e Event) {
	te := &TruthEvent{Event: e, Tx: tx, AppendIdx: t.appendsTotal}
	t.appendsTotal++
	t.events = append(t.events, te)
	t.eventsByTx[tx] = append(t.eventsByTx[tx], te)
}

// Commit records that tx committed now: its events become confirmed.
func (t *Truth) Commit(tx string) {
	t.commits++
	for _, te := range t.eventsByTx[tx] {
		te.CommitSeq, te.CommitTick = t.commits, t.clock
	}
	delete(t.open, tx)
	t.lastResolve = t.clock
}

// Abort records that tx rolled back: its events are never confirmed.
func (t *Truth) Abort(tx string) {
	for _, te := range t.eventsByTx[tx] {
		te.Aborted = true
	}
	delete(t.open, tx)
	t.lastResolve = t.clock
}

// Tick advances the logical clock.
func (t *Truth) Tick(n int64) { t.clock += n }

// Clock is the current logical time.
func (t *Truth) Clock() int64 { return t.clock }

// Commits is the number of commits so far.
func (t *Truth) Commits() int { return t.commits }

// Open is the number of unresolved writer transactions.
func (t *Truth) Open() int { return len(t.open) }

// Quiescent reports whether no writer is open and settle ticks have passed
// since the last one resolved.
func (t *Truth) Quiescent(settle int64) bool {
	return len(t.open) == 0 && t.clock >= t.lastResolve+settle
}

// Lookup returns the ground-truth record of key.
func (t *Truth) Lookup(key EventKey) (*TruthEvent, bool) {
	for _, te := range t.events {
		if te.Key() == key {
			return te, true
		}
	}
	return nil, false
}

// Confirmed returns the events a consumer is entitled to, committed at or
// before commit number upToCommit (use Commits() for "so far"), in the
// canonical ordering-key order (timestamp, persistence id, sequence). That
// order is for set comparison and messages only: the properties do not demand
// a reader deliver in it (a late commit legitimately arrives after later
// timestamps).
func (t *Truth) Confirmed(c Consumer, upToCommit int) []*TruthEvent {
	var out []*TruthEvent
	for _, te := range t.events {
		if te.CommitSeq == 0 || te.CommitSeq > upToCommit || te.Aborted {
			continue
		}
		if c.Selection.Matches(te.Scope) && c.Slices.Contains(te.Slice) {
			out = append(out, te)
		}
	}
	slices.SortFunc(out, func(a, b *TruthEvent) int {
		return cmp.Or(
			cmp.Compare(a.Timestamp, b.Timestamp),
			cmp.Compare(a.PersistenceID, b.PersistenceID),
			cmp.Compare(a.Seq, b.Seq),
		)
	})
	return out
}

// PendingNew counts the confirmed events of c that are not in delivered.
func (t *Truth) PendingNew(c Consumer, delivered map[EventKey]bool) int {
	n := 0
	for _, te := range t.Confirmed(c, t.commits) {
		if !delivered[te.Key()] {
			n++
		}
	}
	return n
}
