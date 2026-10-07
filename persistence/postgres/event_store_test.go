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

package postgres

// These tests exercise EventStore's argument validation without a
// database: every scoped method MUST reject an invalid persistence.Scope or
// persistence.WritePrecondition (and a conditional batch that violates the
// single-persistence-id rule) before it ever touches s.pool. A
// *EventStore with a nil pool is used deliberately: if validation
// were to fall through to a query, these tests would panic on the nil pool
// instead of returning the expected sentinel error, making the ordering
// itself testable.

import (
	"context"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
)

// unvalidatedStore returns a *EventStore with a nil pool, so any test
// that reaches an actual query panics instead of silently passing.
func unvalidatedStore() *EventStore {
	return &EventStore{}
}

func singleEvent(persistenceID string, sequenceNumber uint64) *egopb.Event {
	return &egopb.Event{PersistenceId: persistenceID, SequenceNumber: sequenceNumber}
}

func TestEventStore_WriteEvents_InvalidScope(t *testing.T) {
	specs.Describe(t, "postgres.EventStore.WriteEvents scope validation", func(s *specs.Spec) {
		s.It("rejects an invalid scope with ErrInvalidScope before any query", func(ctx *specs.Context) {
			store := unvalidatedStore()
			err := store.WriteEvents(context.Background(), persistence.Scope{}, []*egopb.Event{singleEvent("a", 1)}, persistence.Unconditional())
			ctx.Expect(err).To(specs.MatchError(persistence.ErrInvalidScope))
		})
	})
}

func TestEventStore_WriteEvents_InvalidPrecondition(t *testing.T) {
	specs.Describe(t, "postgres.EventStore.WriteEvents precondition validation", func(s *specs.Spec) {
		s.It("rejects a zero-value precondition with ErrInvalidPrecondition", func(ctx *specs.Context) {
			store := unvalidatedStore()
			err := store.WriteEvents(context.Background(), persistence.Unscoped(), []*egopb.Event{singleEvent("a", 1)}, persistence.WritePrecondition{})
			ctx.Expect(err).To(specs.MatchError(persistence.ErrInvalidPrecondition))
		})
	})
}

func TestEventStore_WriteEvents_EmptyBatchConditional(t *testing.T) {
	specs.Describe(t, "postgres.EventStore.WriteEvents empty batch under a conditional precondition", func(s *specs.Spec) {
		s.It("rejects it with ErrPreconditionScope", func(ctx *specs.Context) {
			store := unvalidatedStore()
			err := store.WriteEvents(context.Background(), persistence.Unscoped(), nil, persistence.ExpectGenesis())
			ctx.Expect(err).To(specs.MatchError(persistence.ErrPreconditionScope))
		})
	})
}

func TestEventStore_WriteEvents_MixedIDBatchConditional(t *testing.T) {
	specs.Describe(t, "postgres.EventStore.WriteEvents single-id rule for conditional batches", func(s *specs.Spec) {
		s.It("rejects a batch that mixes persistence ids with ErrPreconditionScope", func(ctx *specs.Context) {
			store := unvalidatedStore()
			events := []*egopb.Event{singleEvent("a", 1), singleEvent("b", 2)}
			err := store.WriteEvents(context.Background(), persistence.Unscoped(), events, persistence.ExpectRevision(1))
			ctx.Expect(err).To(specs.MatchError(persistence.ErrPreconditionScope))
		})
	})
}

func TestEventStore_WriteEvents_EmptyBatchUnconditionalSucceeds(t *testing.T) {
	specs.Describe(t, "postgres.EventStore.WriteEvents empty batch under an unconditional precondition", func(s *specs.Spec) {
		s.It("succeeds as a no-op without touching the pool", func(ctx *specs.Context) {
			// Unconditional() never declares a per-persistence-id expectation, so an
			// empty batch is a legitimate no-op rather than ErrPreconditionScope. This
			// also exercises the nil-pool guard: writeUnconditional must return before
			// touching s.pool for an empty batch.
			store := unvalidatedStore()
			err := store.WriteEvents(context.Background(), persistence.Unscoped(), nil, persistence.Unconditional())
			ctx.Expect(err).To(specs.BeNil())
		})
	})
}

func TestEventStore_DeleteEvents_InvalidScope(t *testing.T) {
	specs.Describe(t, "postgres.EventStore.DeleteEvents scope validation", func(s *specs.Spec) {
		s.It("rejects an invalid scope with ErrInvalidScope before any query", func(ctx *specs.Context) {
			store := unvalidatedStore()
			err := store.DeleteEvents(context.Background(), persistence.Scope{}, "a", 1)
			ctx.Expect(err).To(specs.MatchError(persistence.ErrInvalidScope))
		})
	})
}

func TestEventStore_ReplayEvents_InvalidScope(t *testing.T) {
	specs.Describe(t, "postgres.EventStore.ReplayEvents scope validation", func(s *specs.Spec) {
		s.It("rejects an invalid scope with ErrInvalidScope and returns no events", func(ctx *specs.Context) {
			store := unvalidatedStore()
			events, err := store.ReplayEvents(context.Background(), persistence.Scope{}, "a", 1, 10, 10)
			ctx.Expect(err).To(specs.MatchError(persistence.ErrInvalidScope))
			ctx.Expect(events).To(specs.BeNil())
		})
	})
}

func TestEventStore_GetLatestEvent_InvalidScope(t *testing.T) {
	specs.Describe(t, "postgres.EventStore.GetLatestEvent scope validation", func(s *specs.Spec) {
		s.It("rejects an invalid scope with ErrInvalidScope and returns no event", func(ctx *specs.Context) {
			store := unvalidatedStore()
			event, err := store.GetLatestEvent(context.Background(), persistence.Scope{}, "a")
			ctx.Expect(err).To(specs.MatchError(persistence.ErrInvalidScope))
			ctx.Expect(event).To(specs.BeNil())
		})
	})
}

func TestEventStore_PersistenceIDs_InvalidScope(t *testing.T) {
	specs.Describe(t, "postgres.EventStore.PersistenceIDs scope validation", func(s *specs.Spec) {
		s.It("rejects an invalid scope with ErrInvalidScope and returns no ids and no next page token", func(ctx *specs.Context) {
			store := unvalidatedStore()
			ids, next, err := store.PersistenceIDs(context.Background(), persistence.Scope{}, 10, "")
			ctx.Expect(err).To(specs.MatchError(persistence.ErrInvalidScope))
			ctx.Expect(ids).To(specs.BeNil())
			ctx.Expect(next).To(specs.BeEmpty())
		})
	})
}

// A zero pageSize is a degenerate page: it lists nothing and returns an empty
// nextPageToken, so an iteration that passes it terminates instead of looping
// on the same empty page. This mirrors testkit's in-memory EventStore. The
// store's nil pool proves no query runs.
func TestEventStore_PersistenceIDs_ZeroPageSize(t *testing.T) {
	specs.Describe(t, "postgres.EventStore.PersistenceIDs zero page size", func(s *specs.Spec) {
		s.It("returns an empty page and no next page token without a query, with or without a page token", func(ctx *specs.Context) {
			store := unvalidatedStore()
			ids, next, err := store.PersistenceIDs(context.Background(), persistence.Unscoped(), 0, "")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(ids).To(specs.BeEmpty())
			ctx.Expect(next).To(specs.BeEmpty())

			ids, next, err = store.PersistenceIDs(context.Background(), persistence.Unscoped(), 0, "some-id")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(ids).To(specs.BeEmpty())
			ctx.Expect(next).To(specs.BeEmpty())
		})
	})
}

func TestEventStore_ImplementsEventsStore(t *testing.T) {
	var _ persistence.EventsStore = (*EventStore)(nil)
}

func TestEventStore_ReplayEvents_EmptyRange(t *testing.T) {
	specs.Describe(t, "replay bounds outside PostgreSQL's int8 domain", func(s *specs.Spec) {
		s.It("returns no events without querying for an unrepresentable lower bound", func(ctx *specs.Context) {
			events, err := unvalidatedStore().ReplayEvents(context.Background(), persistence.Unscoped(), "a", uint64(1)<<63, ^uint64(0), ^uint64(0))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(events).To(specs.BeEmpty())
		})
		s.It("returns no events for a reversed range or a zero limit", func(ctx *specs.Context) {
			for _, bounds := range [][3]uint64{{2, 1, 10}, {1, 10, 0}} {
				events, err := unvalidatedStore().ReplayEvents(context.Background(), persistence.Unscoped(), "a", bounds[0], bounds[1], bounds[2])
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(events).To(specs.BeEmpty())
			}
		})
	})
}
