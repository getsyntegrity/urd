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
	"math"

	"github.com/getsyntegrity/urd/persistence"
)

// ErrRejected is the provisional sentinel a Subject wraps when it refuses a
// request: a selection built on the invalid zero Scope, or AllScopesInCell
// without the privilege. A rejected request delivers nothing.
var ErrRejected = errors.New("readertck: request rejected")

// SelectionKind says which events a consumer asks for.
type SelectionKind uint8

const (
	// KindOneScope selects exactly one scope. OneScope(Unscoped()) is a valid
	// single-tenant selection and is NOT a wildcard: it never returns a
	// tenant's events (#351, #424).
	KindOneScope SelectionKind = iota + 1
	// KindAllScopesInCell selects every scope of the cell. It is privileged and
	// distinct from every OneScope; omitting tenancy does not grant it.
	KindAllScopesInCell
)

// Selection is the explicit scope selection of a read.
type Selection struct {
	Kind  SelectionKind
	Scope persistence.Scope
}

// OneScope selects a single scope. The invalid zero Scope is not a valid
// argument and a reader must reject it.
func OneScope(scope persistence.Scope) Selection {
	return Selection{Kind: KindOneScope, Scope: scope}
}

// AllScopesInCell is the privileged wildcard selection.
func AllScopesInCell() Selection { return Selection{Kind: KindAllScopesInCell} }

// Matches reports whether an event of scope belongs to the selection.
func (s Selection) Matches(scope persistence.Scope) bool {
	switch s.Kind {
	case KindOneScope:
		return s.Scope.Valid() && s.Scope.Equal(scope)
	case KindAllScopesInCell:
		return true
	default:
		return false
	}
}

// SliceRange is an inclusive range of logical slices.
type SliceRange struct{ Lo, Hi uint32 }

// AllSlices covers every slice.
func AllSlices() SliceRange { return SliceRange{Lo: 0, Hi: math.MaxUint32} }

// Contains reports whether slice is inside the range.
func (r SliceRange) Contains(slice uint32) bool { return slice >= r.Lo && slice <= r.Hi }

// Event is the harness's view of one journal event. Label is for failure
// messages only; identity is Key. Timestamp is the ordering key writers stamp
// before committing, so it can lag behind the commit order.
type Event struct {
	Label         string
	Scope         persistence.Scope
	PersistenceID string
	Seq           uint64
	Slice         uint32
	Timestamp     int64
}

// EventKey is the identity of an event: (scope, persistence id, sequence).
type EventKey struct {
	Scope         persistence.Scope
	PersistenceID string
	Seq           uint64
}

// Key returns the identity of e.
func (e Event) Key() EventKey {
	return EventKey{Scope: e.Scope, PersistenceID: e.PersistenceID, Seq: e.Seq}
}

// String is a diagnostic rendering, never a storage key.
func (k EventKey) String() string {
	return fmt.Sprintf("%s/%s#%d", k.Scope, k.PersistenceID, k.Seq)
}

// Delivery is one event handed to the consumer plus the reader's idempotence
// signal. Token MUST be identical every time the same event is delivered and
// different for different events, so a consumer can discard duplicates.
type Delivery struct {
	Event Event
	Token string
}

// Request is one poll. Cursor is opaque: nil on the first poll, afterwards
// the bytes of a previously returned Result.Cursor.
type Request struct {
	Selection  Selection
	Slices     SliceRange
	Privileged bool
	Cursor     []byte
	Limit      int
}

// Result is the answer to a poll.
type Result struct {
	Deliveries []Delivery
	Cursor     []byte
}

// Subject is one reader instance under evaluation. PROVISIONAL: it stands in
// for the reader SPI that #351 has not approved.
type Subject interface {
	Poll(req Request) (Result, error)
}

// Backend is the system under evaluation as the script sees it: the write
// side the script drives plus a factory of reader instances. NewReader
// returns a fresh instance over the same data (a process restart). Tick moves
// the backend's logical clock when it keeps one.
type Backend interface {
	Begin(tx string)
	Append(tx string, e Event)
	Commit(tx string)
	Abort(tx string)
	Tick(n int64)
	NewReader() Subject
}

// Factory builds a fresh, empty Backend for a scenario. The scenario is passed
// so a backend can learn, for example, the scopes and slices of the cell.
type Factory func(sc Scenario) Backend
