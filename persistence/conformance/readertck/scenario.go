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

	"github.com/getsyntegrity/urd/persistence"
)

// Props is the set of properties a scenario asserts.
type Props uint8

const (
	// Safety: zero omissions and no wrong delivery.
	Safety Props = 1 << iota
	// Eligibility: a committed event is visible within T ticks, ONLY under the
	// scenario's Conditions.
	Eligibility
	// Progress: once no writer is open, the reader moves on (liveness).
	Progress
)

// Conditions are the operational conditions under which a bounded-eligibility
// claim (a visibility bound T) may be made. They are the practical means of
// guaranteeing that no writer transaction stays open longer than LMax. When
// any of them fails the bound is NOT claimed, but safety must still hold.
type Conditions struct {
	// DedicatedCluster: nothing foreign shares the database and can hold a
	// transaction open.
	DedicatedCluster bool
	// WriterTimeoutAllRoles: every role that writes (not only the application
	// one) has a transaction timeout.
	WriterTimeoutAllRoles bool
	// NoPreparedTransactions: two-phase-commit transactions are disabled, since
	// a timeout does not bound them.
	NoPreparedTransactions bool
	// OldTransactionAlert: an alert fires on a transaction older than LMax, so
	// a violation of the other three is noticed.
	OldTransactionAlert bool
}

// All reports whether every condition holds.
func (c Conditions) All() bool {
	return c.DedicatedCluster && c.WriterTimeoutAllRoles && c.NoPreparedTransactions && c.OldTransactionAlert
}

// Consumer is one reader client: a selection, a slice range and its own
// persisted cursor. ExpectReject marks a request that must be refused.
type Consumer struct {
	Name         string
	Selection    Selection
	Slices       SliceRange
	Privileged   bool
	ExpectReject bool
}

// Op is the kind of a script step.
type Op uint8

// Script operations.
const (
	OpBegin Op = iota + 1
	OpAppend
	OpCommit
	OpAbort
	OpTick
	OpPoll
	OpDrain
	OpRestart
)

// Step is one scripted action. Use the constructors below.
type Step struct {
	Op        Op
	Tx        string
	Event     Event
	Ticks     int64
	Consumer  string
	Limit     int
	NoPersist bool
}

// Begin opens writer transaction tx.
func Begin(tx string) Step { return Step{Op: OpBegin, Tx: tx} }

// Append adds e to the open transaction tx (invisible until Commit).
func Append(tx string, e Event) Step { return Step{Op: OpAppend, Tx: tx, Event: e} }

// Commit commits tx.
func Commit(tx string) Step { return Step{Op: OpCommit, Tx: tx} }

// Abort rolls tx back.
func Abort(tx string) Step { return Step{Op: OpAbort, Tx: tx} }

// Tick advances the logical clock by n.
func Tick(n int64) Step { return Step{Op: OpTick, Ticks: n} }

// Poll reads one batch for the consumer and persists the returned cursor (the
// handler never fails).
func Poll(consumer string, limit int) Step {
	return Step{Op: OpPoll, Consumer: consumer, Limit: limit}
}

// PollUnpersisted reads one batch but does not persist the returned cursor,
// as if the process crashed between delivery and the cursor commit.
func PollUnpersisted(consumer string, limit int) Step {
	return Step{Op: OpPoll, Consumer: consumer, Limit: limit, NoPersist: true}
}

// Drain polls until the reader has nothing more to give at this tick.
func Drain(consumer string, limit int) Step {
	return Step{Op: OpDrain, Consumer: consumer, Limit: limit}
}

// Restart replaces the reader instance; persisted cursors survive.
func Restart() Step { return Step{Op: OpRestart} }

// Scenario is one reproducible case, as data.
type Scenario struct {
	Name string
	// Doc says what the scenario provokes and which property it targets.
	Doc string
	// Seed is the generator seed for generated scenarios, 0 otherwise.
	Seed uint64
	// Asserts is the set of properties the scenario judges.
	Asserts Props
	// Conditions, LMax and T apply to Eligibility only. LMax is the longest a
	// writer transaction may stay open when Conditions.All() holds; T is the
	// claimed visibility bound in ticks and must be at least LMax.
	Conditions Conditions
	LMax       int64
	T          int64
	// Settle is how many ticks after the last writer resolves a Drain must
	// already see everything (Progress). Zero means immediately.
	Settle    int64
	Consumers []Consumer
	Steps     []Step
}

// Consumer returns the consumer called name.
func (sc Scenario) Consumer(name string) (Consumer, bool) {
	for _, c := range sc.Consumers {
		if c.Name == name {
			return c, true
		}
	}
	return Consumer{}, false
}

// Scopes lists the scopes of the cell in first-appearance order: every scope an
// appended event carries.
func (sc Scenario) Scopes() []persistence.Scope {
	var out []persistence.Scope
	for _, st := range sc.Steps {
		if st.Op != OpAppend {
			continue
		}
		if !slices.ContainsFunc(out, st.Event.Scope.Equal) {
			out = append(out, st.Event.Scope)
		}
	}
	return out
}

// Slices lists the distinct slices of the appended events, ascending.
func (sc Scenario) Slices() []uint32 {
	var out []uint32
	for _, st := range sc.Steps {
		if st.Op == OpAppend && !slices.Contains(out, st.Event.Slice) {
			out = append(out, st.Event.Slice)
		}
	}
	slices.Sort(out)
	return out
}

// Validate reports a malformed scenario: a script that cannot run, or that
// claims bounded eligibility while contradicting its own conditions.
func (sc Scenario) Validate() error {
	if sc.Name == "" || sc.Asserts == 0 {
		return fmt.Errorf("scenario %q: name and asserted properties are required", sc.Name)
	}
	names := map[string]bool{}
	for _, c := range sc.Consumers {
		if c.Name == "" || names[c.Name] {
			return fmt.Errorf("scenario %q: empty or duplicate consumer %q", sc.Name, c.Name)
		}
		names[c.Name] = true
	}
	type txInfo struct {
		begun, ended int64
		open         bool
	}
	txs := map[string]*txInfo{}
	keys := map[EventKey]bool{}
	var clock int64
	for i, st := range sc.Steps {
		at := fmt.Sprintf("scenario %q step %d", sc.Name, i)
		switch st.Op {
		case OpBegin:
			if txs[st.Tx] != nil {
				return fmt.Errorf("%s: transaction %q begun twice", at, st.Tx)
			}
			txs[st.Tx] = &txInfo{begun: clock, open: true}
		case OpAppend:
			if tx := txs[st.Tx]; tx == nil || !tx.open {
				return fmt.Errorf("%s: append to a transaction that is not open (%q)", at, st.Tx)
			}
			if !st.Event.Scope.Valid() || keys[st.Event.Key()] {
				return fmt.Errorf("%s: event scope invalid or key repeated (%s)", at, st.Event.Key())
			}
			keys[st.Event.Key()] = true
		case OpCommit, OpAbort:
			tx := txs[st.Tx]
			if tx == nil || !tx.open {
				return fmt.Errorf("%s: resolve of a transaction that is not open (%q)", at, st.Tx)
			}
			tx.open, tx.ended = false, clock
		case OpTick:
			if st.Ticks <= 0 {
				return fmt.Errorf("%s: tick must be positive", at)
			}
			clock += st.Ticks
		case OpPoll, OpDrain:
			if !names[st.Consumer] || st.Limit < 1 {
				return fmt.Errorf("%s: unknown consumer %q or limit < 1", at, st.Consumer)
			}
		case OpRestart:
		default:
			return fmt.Errorf("%s: unknown op", at)
		}
	}
	for id, tx := range txs {
		if tx.open {
			return fmt.Errorf("scenario %q: transaction %q is never resolved", sc.Name, id)
		}
		if sc.Asserts&Eligibility != 0 && sc.Conditions.All() && tx.ended-tx.begun > sc.LMax {
			return fmt.Errorf("scenario %q: transaction %q lasts %d ticks, over LMax %d, yet the conditions are claimed", sc.Name, id, tx.ended-tx.begun, sc.LMax)
		}
	}
	if sc.Asserts&Eligibility != 0 && sc.Conditions.All() && (sc.LMax <= 0 || sc.T < sc.LMax) {
		return fmt.Errorf("scenario %q: a claimed bound needs LMax > 0 and T >= LMax", sc.Name)
	}
	return nil
}
