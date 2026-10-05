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

package testkit

import (
	"context"
	"sort"
	"sync"

	"go.uber.org/atomic"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/port/adapter"
)

// EventKey identified an individual event record in the old, per-event
// keying scheme.
//
// Deprecated: EventStore now keys its internal map by persistenceID alone,
// storing an immutable *eventLog per persistenceID so that conditional
// writes can be evaluated and committed with a single sync.Map
// CompareAndSwap/LoadOrStore. EventKey is unused and kept only for source
// compatibility.
type EventKey struct {
	PersistenceID  string
	SequenceNumber uint64
}

// eventLog is the immutable value published for a given (scope, persistenceID).
// revision is the StorageRevision (the highest committed SequenceNumber);
// it is deliberately tracked independently of len(events) so that
// DeleteEvents can truncate events for retention without resetting the
// revision a conditional write compares against.
type eventLog struct {
	revision uint64
	events   []*egopb.Event
}

// eventStoreKey is the structural (scope, persistenceID) pair EventStore
// keys its internal map by. persistence.Scope is a comparable value type
// (see its doc comment), so this struct is directly usable as a map key;
// no field is ever derived from Scope.String() or from concatenating scope
// and persistenceID into one string.
type eventStoreKey struct {
	scope         persistence.Scope
	persistenceID string
}

type EventStore struct {
	// db maps eventStoreKey{scope, persistenceID} -> *eventLog.
	db        *sync.Map
	connected *atomic.Bool
}

var _ persistence.EventsStore = (*EventStore)(nil)

// Describe implements adapter.Describer. It declares no capability:
// CapReady is implied by the store port, whose interface already has Ping.
func (x *EventStore) Describe() adapter.Descriptor {
	return adapter.Descriptor{Ports: []adapter.Port{persistence.PortEventsStore}, Name: "testkit-memory"}
}

func NewEventsStore() *EventStore {
	return &EventStore{
		db:        &sync.Map{},
		connected: atomic.NewBool(false),
	}
}

func (x *EventStore) Connect(_ context.Context) error {
	if x.connected.Load() {
		return nil
	}
	x.connected.Store(true)
	return nil
}

func (x *EventStore) Disconnect(_ context.Context) error {
	if !x.connected.Load() {
		return nil
	}
	x.db.Range(func(key interface{}, _ interface{}) bool {
		x.db.Delete(key)
		return true
	})
	x.connected.Store(false)
	return nil
}

// WriteEvents implements persistence.EventsStore. See that interface's doc
// comment for the full contract; the conditional path is evaluated and
// committed via sync.Map's own CompareAndSwap/LoadOrStore so that two
// unserialized goroutines racing against the same (scope, persistenceID)
// compete directly against the store, never against caller-side ordering.
//
// An invalid scope is rejected with ErrInvalidScope before anything else is
// validated or touched, per persistence.Scope's contract.
func (x *EventStore) WriteEvents(_ context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	if !scope.Valid() {
		return persistence.ErrInvalidScope
	}

	if !precondition.Valid() {
		return persistence.ErrInvalidPrecondition
	}

	if precondition.IsUnconditional() {
		return x.writeUnconditional(scope, events)
	}

	if len(events) == 0 {
		return persistence.ErrPreconditionScope
	}
	persistenceID := events[0].GetPersistenceId()
	for _, event := range events[1:] {
		if event.GetPersistenceId() != persistenceID {
			return persistence.ErrPreconditionScope
		}
	}
	return x.writeConditional(scope, persistenceID, events, precondition)
}

// writeConditional evaluates a genesis or exact-revision precondition for
// (scope, persistenceID) and commits events as one atomic operation. A
// single LoadOrStore (genesis) or CompareAndSwap (exact-revision) attempt
// decides the outcome — a failed attempt is a terminal conflict, never
// retried, since retrying would silently convert a declared, no-longer-valid
// expectation into success. The persisted revision compared against belongs
// to (scope, persistenceID) alone: two different scopes writing the same
// persistenceID never observe or affect each other's revision.
func (x *EventStore) writeConditional(scope persistence.Scope, persistenceID string, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	key := eventStoreKey{scope: scope, persistenceID: persistenceID}
	raw, exists := x.db.Load(key)
	var old *eventLog
	if exists {
		old = raw.(*eventLog)
	}

	if precondition.IsGenesis() {
		if exists {
			return persistence.NewConflictError(scope, persistenceID, precondition, persistence.WithActualRevision(old.revision))
		}
		newLog := newEventLog(nil, events)
		if actual, loaded := x.db.LoadOrStore(key, newLog); loaded {
			return persistence.NewConflictError(scope, persistenceID, precondition, persistence.WithActualRevision(actual.(*eventLog).revision))
		}
		return nil
	}

	expectedRevision, _ := precondition.Revision()
	if !exists || old.revision != expectedRevision {
		if exists {
			return persistence.NewConflictError(scope, persistenceID, precondition, persistence.WithActualRevision(old.revision))
		}
		return persistence.NewConflictError(scope, persistenceID, precondition)
	}

	newLog := newEventLog(old, events)
	if !x.db.CompareAndSwap(key, old, newLog) {
		if actual, ok := x.db.Load(key); ok {
			return persistence.NewConflictError(scope, persistenceID, precondition, persistence.WithActualRevision(actual.(*eventLog).revision))
		}
		return persistence.NewConflictError(scope, persistenceID, precondition)
	}
	return nil
}

// writeUnconditional preserves legacy, precondition-free write semantics:
// every event commits, grouped by its own PersistenceId within scope.
// Concurrent unconditional writers targeting the same (scope, persistenceID)
// still compete against the store's own CompareAndSwap/LoadOrStore rather
// than losing events to a race, but a losing attempt here simply retries
// against the latest state instead of returning a conflict, since
// Unconditional() never declares an expectation that can go stale.
func (x *EventStore) writeUnconditional(scope persistence.Scope, events []*egopb.Event) error {
	if len(events) == 0 {
		return nil
	}

	grouped := make(map[string][]*egopb.Event, len(events))
	order := make([]string, 0, len(events))
	for _, event := range events {
		id := event.GetPersistenceId()
		if _, seen := grouped[id]; !seen {
			order = append(order, id)
		}
		grouped[id] = append(grouped[id], event)
	}

	for _, persistenceID := range order {
		group := grouped[persistenceID]
		key := eventStoreKey{scope: scope, persistenceID: persistenceID}
		for {
			raw, exists := x.db.Load(key)
			var old *eventLog
			if exists {
				old = raw.(*eventLog)
			}
			newLog := newEventLog(old, group)

			if !exists {
				if _, loaded := x.db.LoadOrStore(key, newLog); !loaded {
					break
				}
				continue
			}
			if x.db.CompareAndSwap(key, old, newLog) {
				break
			}
		}
	}
	return nil
}

// newEventLog builds the successor log for old (nil for a nonexistent
// persistenceID) by merging newEvents into old.events, advancing revision to
// the highest SequenceNumber seen. old is never mutated; a wholly new
// *eventLog is published so that CompareAndSwap's pointer-identity comparison
// stays meaningful.
//
// A newEvent whose SequenceNumber collides with one already in the log
// replaces that entry in place rather than appending a duplicate, matching
// the pre-eventLog representation's behavior: events were keyed by
// EventKey{PersistenceID, SequenceNumber} in a sync.Map, so writing the same
// key again silently overwrote the prior entry and Range() always observed
// exactly one event per (PersistenceID, SequenceNumber) pair.
func newEventLog(old *eventLog, newEvents []*egopb.Event) *eventLog {
	var revision uint64
	var existing []*egopb.Event
	if old != nil {
		revision = old.revision
		existing = old.events
	}

	merged := make([]*egopb.Event, len(existing), len(existing)+len(newEvents))
	copy(merged, existing)

	indexBySequence := make(map[uint64]int, len(merged))
	for i, event := range merged {
		indexBySequence[event.GetSequenceNumber()] = i
	}

	for _, event := range newEvents {
		sn := event.GetSequenceNumber()
		if i, ok := indexBySequence[sn]; ok {
			merged[i] = event
		} else {
			indexBySequence[sn] = len(merged)
			merged = append(merged, event)
		}
		if sn > revision {
			revision = sn
		}
	}

	return &eventLog{revision: revision, events: merged}
}

func (x *EventStore) Ping(ctx context.Context) error {
	if !x.connected.Load() {
		return x.Connect(ctx)
	}
	return nil
}

// DeleteEvents truncates (scope, persistenceID)'s retained events up to and
// including toSequenceNumber, while preserving the log's revision: retention
// must never reset StorageRevision, or a stale writer whose exact-revision
// precondition predates the deleted events would incorrectly win a
// conditional write. An invalid scope is rejected with ErrInvalidScope
// before anything is touched, and this never affects a record in another
// scope.
func (x *EventStore) DeleteEvents(_ context.Context, scope persistence.Scope, persistenceID string, toSequenceNumber uint64) error {
	if !scope.Valid() {
		return persistence.ErrInvalidScope
	}

	key := eventStoreKey{scope: scope, persistenceID: persistenceID}
	for {
		raw, exists := x.db.Load(key)
		if !exists {
			return nil
		}
		old := raw.(*eventLog)

		retained := make([]*egopb.Event, 0, len(old.events))
		for _, event := range old.events {
			if event.GetSequenceNumber() > toSequenceNumber {
				retained = append(retained, event)
			}
		}
		if len(retained) == len(old.events) {
			return nil
		}

		newLog := &eventLog{revision: old.revision, events: retained}
		if x.db.CompareAndSwap(key, old, newLog) {
			return nil
		}
	}
}

// ReplayEvents reads events for (scope, persistenceID). An invalid scope is
// rejected with ErrInvalidScope before anything is read, and this never
// returns a record that belongs to another scope.
func (x *EventStore) ReplayEvents(_ context.Context, scope persistence.Scope, persistenceID string, fromSequenceNumber, toSequenceNumber uint64, limit uint64) ([]*egopb.Event, error) {
	if !scope.Valid() {
		return nil, persistence.ErrInvalidScope
	}

	raw, exists := x.db.Load(eventStoreKey{scope: scope, persistenceID: persistenceID})
	if !exists {
		return nil, nil
	}
	log := raw.(*eventLog)

	var events []*egopb.Event
	for _, event := range log.events {
		sn := event.GetSequenceNumber()
		if sn >= fromSequenceNumber && sn <= toSequenceNumber {
			events = append(events, event)
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		return events[i].GetSequenceNumber() < events[j].GetSequenceNumber()
	})

	if len(events) > int(limit) {
		events = events[:int(limit)]
	}

	return events, nil
}

// GetLatestEvent reads the latest event for (scope, persistenceID). An
// invalid scope is rejected with ErrInvalidScope before anything is read,
// and this never returns a record that belongs to another scope.
func (x *EventStore) GetLatestEvent(_ context.Context, scope persistence.Scope, persistenceID string) (*egopb.Event, error) {
	if !scope.Valid() {
		return nil, persistence.ErrInvalidScope
	}

	raw, exists := x.db.Load(eventStoreKey{scope: scope, persistenceID: persistenceID})
	if !exists {
		return nil, nil
	}
	log := raw.(*eventLog)
	if len(log.events) == 0 {
		return nil, nil
	}

	latest := log.events[0]
	for _, event := range log.events[1:] {
		if event.GetSequenceNumber() > latest.GetSequenceNumber() {
			latest = event
		}
	}
	return latest, nil
}

// PersistenceIDs enumerates only the persistence ids within scope. An
// invalid scope is rejected with ErrInvalidScope before anything is read,
// and this never enumerates a persistenceID that belongs to a different
// scope.
//
// Token resolution (see persistence.EventsStore.PersistenceIDs's doc
// comment for the full pagination contract this satisfies): nextPageToken
// is the LAST key actually RETURNED on this page, never the first key held
// back for the next one. The prior implementation returned
// keys[endIndex] — the first key NOT yet returned — as the token, while
// startIndex on the following call resumed strictly AFTER that same token
// (key > pageToken). Those two facts together meant the key stored as the
// token was never itself returned by any page: it was silently skipped at
// every page boundary. Making the token a cursor over what the caller has
// already CONSUMED (keys[endIndex-1]), while keeping the same strict `>`
// comparison on the next call, makes the two agree: resuming strictly
// after "the last thing I've seen" is exactly the cursor semantics callers
// expect, and no id is ever skipped or duplicated across a page boundary.
func (x *EventStore) PersistenceIDs(_ context.Context, scope persistence.Scope, pageSize uint64, pageToken string) (persistenceIDs []string, nextPageToken string, err error) {
	if !scope.Valid() {
		return nil, "", persistence.ErrInvalidScope
	}

	// step 1: collect the persistence ids that belong to scope
	keys := make([]string, 0)
	x.db.Range(func(key, _ any) bool {
		k := key.(eventStoreKey)
		if k.scope.Equal(scope) {
			keys = append(keys, k.persistenceID)
		}
		return true
	})

	// step 2: sort them
	sort.Strings(keys)

	// step 3: paginate the sorted keys
	startIndex := 0
	if pageToken != "" {
		// Find the index of the pageToken in the sorted keys
		for i, key := range keys {
			if key > pageToken {
				startIndex = i
				break
			}
		}
	}

	// Collect up to pageSize items starting from startIndex
	endIndex := startIndex + int(pageSize)
	if endIndex > len(keys) {
		endIndex = len(keys)
	}
	persistenceIDs = keys[startIndex:endIndex]

	// step 4: determine the nextPageToken. Guarded on len(persistenceIDs) > 0
	// so a degenerate pageSize == 0 call (which returns no items and thus
	// cannot advance startIndex on the next call) terminates instead of
	// looping forever on the same empty page.
	switch {
	case endIndex < len(keys) && len(persistenceIDs) > 0:
		nextPageToken = persistenceIDs[len(persistenceIDs)-1]
	default:
		nextPageToken = ""
	}

	return persistenceIDs, nextPageToken, nil
}

func (x *EventStore) GetShardEvents(_ context.Context, shardNumber uint64, offset int64, limit uint64) ([]*egopb.Event, int64, error) {
	var shardEvents []*egopb.Event
	x.db.Range(func(_ any, value any) bool {
		log := value.(*eventLog)
		for _, event := range log.events {
			if event.GetShard() == shardNumber {
				shardEvents = append(shardEvents, event)
			}
		}
		return true
	})

	if len(shardEvents) == 0 {
		return nil, 0, nil
	}

	var events []*egopb.Event
	for _, event := range shardEvents {
		if event.GetTimestamp() > offset {
			events = append(events, event)
		}
	}

	if len(events) == 0 || limit == 0 {
		return nil, 0, nil
	}

	// The map iteration order above is random, so the order must be total:
	// timestamp first, then persistence ID and sequence number as tie-breakers.
	// The limit applies to the sorted result, as in the Postgres store.
	sort.Slice(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.GetTimestamp() != b.GetTimestamp() {
			return a.GetTimestamp() < b.GetTimestamp()
		}
		if a.GetPersistenceId() != b.GetPersistenceId() {
			return a.GetPersistenceId() < b.GetPersistenceId()
		}
		return a.GetSequenceNumber() < b.GetSequenceNumber()
	})
	if uint64(len(events)) > limit {
		events = events[:limit]
	}

	nextOffset := events[len(events)-1].GetTimestamp()
	return events, nextOffset, nil
}

func (x *EventStore) ShardOffsets(context.Context) (map[uint64]int64, error) {
	offsets := make(map[uint64]int64)
	x.db.Range(func(_ any, value any) bool {
		log := value.(*eventLog)
		for _, event := range log.events {
			if event.GetTimestamp() > offsets[event.GetShard()] {
				offsets[event.GetShard()] = event.GetTimestamp()
			}
		}
		return true
	})
	return offsets, nil
}
