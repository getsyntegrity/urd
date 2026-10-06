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

package persistence

import (
	"context"

	"github.com/getsyntegrity/urd/egopb"
)

// EventsStore defines the API to write to the events store
//
// # Breaking change: WriteEvents gained a required precondition parameter
//
// WRITE-004 changed WriteEvents's signature from
// WriteEvents(ctx, events []*egopb.Event) error to
// WriteEvents(ctx, events []*egopb.Event, precondition WritePrecondition) error
// (design.md M-1/M-4). Any external implementation of EventsStore written
// against the pre-WRITE-004 signature fails to compile against this
// interface. Manual verification: temporarily assign a value of such an
// implementation to a var of type EventsStore (e.g.
// var _ EventsStore = (*oldStyleStore)(nil) where oldStyleStore's
// WriteEvents takes only (ctx, events)) and observe the compiler reject it
// with "missing method WriteEvents" / a wrong-signature error. M-3 documents
// the upgrade recipe.
//
// # Breaking change: every record-addressing method gained a required Scope parameter
//
// TENANT-003 (T2) changed every record-addressing method's signature to take
// a persistence.Scope as the parameter immediately after ctx:
//
//	WriteEvents(ctx, events []*egopb.Event, precondition WritePrecondition) error                                        ->
//	WriteEvents(ctx, scope Scope, events []*egopb.Event, precondition WritePrecondition) error
//	DeleteEvents(ctx, persistenceID string, toSequenceNumber uint64) error                                                ->
//	DeleteEvents(ctx, scope Scope, persistenceID string, toSequenceNumber uint64) error
//	ReplayEvents(ctx, persistenceID string, fromSequenceNumber, toSequenceNumber uint64, limit uint64) ([]*egopb.Event, error) ->
//	ReplayEvents(ctx, scope Scope, persistenceID string, fromSequenceNumber, toSequenceNumber uint64, limit uint64) ([]*egopb.Event, error)
//	GetLatestEvent(ctx, persistenceID string) (*egopb.Event, error)                                                      ->
//	GetLatestEvent(ctx, scope Scope, persistenceID string) (*egopb.Event, error)
//	PersistenceIDs(ctx, pageSize uint64, pageToken string) ([]string, string, error)                                     ->
//	PersistenceIDs(ctx, scope Scope, pageSize uint64, pageToken string) ([]string, string, error)
//
//	GetShardEvents(ctx, shardNumber uint64, offset int64, limit uint64) ([]*egopb.Event, int64, error)                ->
//	GetShardEvents(ctx, scope Scope, shardNumber uint64, offset int64, limit uint64) ([]*egopb.Event, int64, error)
//	ShardOffsets(ctx) (map[uint64]int64, error)                                                                        ->
//	ShardOffsets(ctx, scope Scope) (map[uint64]int64, error)
//
// GetShardEvents and ShardOffsets gained the same leading scope parameter in
// the read-side tenant isolation slice (TENANT-004, #93): a store that
// ignored it would hand one tenant's events to another tenant's projection,
// so there is no unscoped variant and no fallback to a global read. Any
// external implementation of EventsStore fails to compile
// against this interface. Manual verification: temporarily assign a value
// of such an implementation to a var of type EventsStore (e.g.
// var _ EventsStore = (*oldStyleStore)(nil) where oldStyleStore's methods
// still take the pre-TENANT-003 argument lists) and observe the compiler
// reject it with "missing method WriteEvents" / a wrong-signature error for
// each changed method.
//
// Upgrade recipe: accept the new scope Scope parameter and include it
// structurally in the record key (e.g. a Go map or table key built from
// the pair (scope, persistenceID), never from Scope.String() or a
// persistenceID transformation — see persistence.Scope's doc comment). An
// adapter that wants pre-TENANT-003 behavior for existing rows maps
// Unscoped() to its current key layout unchanged, so no data migration is
// needed for non-tenant deployments: a store that always receives
// Unscoped() behaves exactly as it did before Scope existed.
type EventsStore interface {
	// Connect connects to the journal store
	Connect(ctx context.Context) error
	// Disconnect disconnect the journal store
	Disconnect(ctx context.Context) error
	// WriteEvents persist event in batches for a given (scope, persistenceID), subject to
	// precondition.
	// Note: persistence id and the sequence number make a record in the journal store unique
	// WITHIN a scope. Failure to ensure that can lead to some un-wanted behaviors and data
	// inconsistency
	//
	// The precondition is evaluated against the persisted revision for the batch's target
	// (scope, persistenceID) (see WritePrecondition) and the batch is committed as one atomic
	// operation: there is no observable window in which another writer's commit can interleave
	// between the precondition check and the commit. Implementations MUST NOT implement this as a
	// separate read-then-compare followed by an unconditional write.
	//
	// scope and persistenceID together form the record's effective identity (see
	// persistence.Scope's doc comment). An invalid (zero-value) scope returns ErrInvalidScope and
	// nothing is read or written. A write performed in one scope MUST NOT modify a record that
	// belongs to another scope, even when both share the same persistenceID.
	//
	// When precondition is not Unconditional(), every event in events MUST share one
	// PersistenceId (read from events[0]); a batch that violates this, including an empty slice,
	// returns ErrPreconditionScope. An invalid precondition (the zero value of WritePrecondition)
	// returns ErrInvalidPrecondition. When the precondition does not hold against the persisted
	// revision, no event in the batch is committed and a *ConflictError is returned, identifiable
	// via errors.As or errors.Is(err, ErrConcurrencyConflict).
	WriteEvents(ctx context.Context, scope Scope, events []*egopb.Event, precondition WritePrecondition) error
	// Ping verifies a connection to the database is still alive, establishing a connection if
	// necessary. Deliberately unscoped: connection lifecycle is not record-addressing, so it
	// carries no tenant boundary.
	Ping(ctx context.Context) error
	// DeleteEvents deletes events from the store, for the given scope and persistenceID, up to a
	// given sequence number (inclusive). scope and persistenceID together form the record's
	// effective identity: an invalid (zero-value) scope returns ErrInvalidScope and nothing is
	// deleted, and a delete performed in one scope MUST NOT affect a record in another scope.
	DeleteEvents(ctx context.Context, scope Scope, persistenceID string, toSequenceNumber uint64) error
	// ReplayEvents fetches events for a given (scope, persistenceID) from a given sequence
	// number(inclusive) to a given sequence number(inclusive) with a maximum of journals to be
	// replayed. scope and persistenceID together form the record's effective identity: an invalid
	// (zero-value) scope returns ErrInvalidScope and nothing is read, and a read performed in one
	// scope MUST NOT return a record that belongs to another scope.
	ReplayEvents(ctx context.Context, scope Scope, persistenceID string, fromSequenceNumber, toSequenceNumber uint64, limit uint64) ([]*egopb.Event, error)
	// GetLatestEvent fetches the latest event for the given (scope, persistenceID). scope and
	// persistenceID together form the record's effective identity: an invalid (zero-value) scope
	// returns ErrInvalidScope and nothing is read, and a read performed in one scope MUST NOT
	// return a record that belongs to another scope.
	GetLatestEvent(ctx context.Context, scope Scope, persistenceID string) (*egopb.Event, error)
	// PersistenceIDs returns the distinct list of all the persistence ids in the journal store
	// that belong to scope. An invalid (zero-value) scope returns ErrInvalidScope. This never
	// enumerates a persistenceID that belongs to a different scope.
	//
	// Pagination contract (normative — every implementation, in this repo or external, MUST
	// satisfy this): nextPageToken is opaque to the caller. A caller MUST NOT interpret,
	// construct, or compare it, only pass it back verbatim as pageToken on the following call.
	// Starting from pageToken == "" and calling repeatedly, each time with the previously
	// returned nextPageToken, until an empty nextPageToken is returned, MUST yield every
	// persistence id in scope EXACTLY ONCE — no id skipped, and no id returned twice. An empty
	// returned nextPageToken means the iteration is complete; it MUST NOT be returned while ids
	// in scope remain unlisted. persistence/conformance's Enumeration group pins this contract
	// with a check that forces multiple pages and asserts exact, duplicate-free coverage.
	PersistenceIDs(ctx context.Context, scope Scope, pageSize uint64, pageToken string) (persistenceIDs []string, nextPageToken string, err error)
	// GetShardEvents returns the next (limit) events of the given scope after the offset in the
	// journal for a given shard, strictly after offset, in the total order (timestamp,
	// persistence ID, sequence number); the second result is the timestamp of the last event
	// returned (0 when there is none). That timestamp is the whole cursor: the caller commits it
	// and passes it back as offset, so the contract is that no event of the scope and shard with a
	// timestamp at or before the returned offset is left undelivered. limit is therefore a
	// minimum-batch target, not a hard cap: at least limit events are returned when that many are
	// pending, and the batch is extended to the end of the group of events sharing the timestamp of
	// the limit-th one, because a batch that stopped inside such a group would return its timestamp
	// as the offset and the rest of the group would be skipped by the next, strictly-after, read.
	// Events written by one command share a timestamp, so a group is as large as that command's
	// events plus any other event of the shard stamped with the same instant. A limit of 0 returns
	// nothing. A crash between delivering a batch and committing its offset redelivers the whole
	// batch, including every event of a group: delivery stays at-least-once. An invalid
	// (zero-value) scope returns ErrInvalidScope and nothing is read. A read performed in one scope
	// MUST NOT return an event that belongs to another scope, and an Unscoped() read does not
	// return a tenant's events: there is no cross-scope read.
	GetShardEvents(ctx context.Context, scope Scope, shardNumber uint64, offset int64, limit uint64) ([]*egopb.Event, int64, error)
	// ShardOffsets returns every distinct shard that holds events of the given scope mapped to the
	// offset (timestamp) of that scope's most recent event in the shard. Compared against the
	// offsets a projection has committed, it tells which shards have pending events without
	// scanning every shard. A scope with no events yields an empty map. An invalid (zero-value)
	// scope returns ErrInvalidScope. Shards and timestamps that only other scopes hold MUST NOT
	// appear. SQL-backed stores implement it with a single query:
	//
	//	SELECT shard_number, MAX(timestamp) FROM events_store WHERE tenant_id = $1 GROUP BY shard_number
	ShardOffsets(ctx context.Context, scope Scope) (map[uint64]int64, error)
}
