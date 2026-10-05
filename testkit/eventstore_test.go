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
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
)

// newAccountEvent builds a single-event batch for persistenceID at
// sequenceNumber, for use as the payload of a conditional WriteEvents call.
func newAccountEvent(ctx *specs.Context, persistenceID string, sequenceNumber uint64) []*egopb.Event {
	anyEvent, err := anypb.New(&testpb.AccountCreated{AccountId: persistenceID, AccountBalance: 100})
	ctx.Expect(err).To(specs.BeNil())
	return []*egopb.Event{
		{PersistenceId: persistenceID, SequenceNumber: sequenceNumber, Event: anyEvent, Timestamp: time.Now().UnixMilli(), Shard: 1},
	}
}

// ---------------------------------------------------------------------------
// WriteEvents: conditional-write contract (2.2x)
// ---------------------------------------------------------------------------

func TestEventStore_WriteEvents_InvalidPreconditionIsRejected(t *testing.T) {
	specs.Describe(t, "EventStore.WriteEvents with an invalid precondition", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("is rejected and persists nothing", func(ctx *specs.Context) {
			var zero persistence.WritePrecondition
			err := fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "invalid-precondition", 1), zero)

			ctx.Expect(err).To(specs.MatchError(persistence.ErrInvalidPrecondition))

			latest, getErr := fx.store.GetLatestEvent(bg, persistence.Unscoped(), "invalid-precondition")
			ctx.Expect(getErr).To(specs.BeNil())
			// a rejected precondition must not persist anything
			ctx.Expect(latest).To(specs.BeNil())
		})
	})
}

func TestEventStore_WriteEvents_UnconditionalIsLegacyBehavior(t *testing.T) {
	specs.Describe(t, "EventStore.WriteEvents with an unconditional precondition", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("keeps the legacy behavior of appending every event", func(ctx *specs.Context) {
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "legacy", 1), persistence.Unconditional())).To(specs.BeNil())
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "legacy", 2), persistence.Unconditional())).To(specs.BeNil())

			replayed, err := fx.store.ReplayEvents(bg, persistence.Unscoped(), "legacy", 1, 2, 10)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(replayed).To(specs.HaveLen(2))
		})
	})
}

func TestEventStore_WriteEvents_ExactRevisionSucceedsWhenCurrent(t *testing.T) {
	specs.Describe(t, "EventStore.WriteEvents with an exact revision precondition", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("succeeds when the revision is the current one", func(ctx *specs.Context) {
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "exact-ok", 1), persistence.ExpectGenesis())).To(specs.BeNil())
			err := fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "exact-ok", 2), persistence.ExpectRevision(1))
			ctx.Expect(err).To(specs.BeNil())

			latest, err := fx.store.GetLatestEvent(bg, persistence.Unscoped(), "exact-ok")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.Not(specs.BeNil()))
			ctx.Expect(latest).To(sequenceNumber[*egopb.Event](2))
		})
	})
}

func TestEventStore_WriteEvents_StaleRevisionIsConflict(t *testing.T) {
	specs.Describe(t, "EventStore.WriteEvents with a stale revision precondition", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("reports a conflict and leaves the persisted log untouched", func(ctx *specs.Context) {
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "stale", 1), persistence.ExpectGenesis())).To(specs.BeNil())

			err := fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "stale", 2), persistence.ExpectRevision(99))

			var conflict *persistence.ConflictError
			ctx.Expect(err).To(specs.MatchErrorAs(&conflict))
			ctx.Expect(err).To(specs.MatchError(persistence.ErrConcurrencyConflict))
			ctx.Expect(conflict.PersistenceID()).ToEqual("stale")
			ctx.Expect(conflict.Expected()).ToEqual(persistence.ExpectRevision(99))

			actual, ok := conflict.ActualRevision()
			ctx.Expect(ok).To(specs.BeTrue())
			ctx.Expect(actual).ToEqual(uint64(1))

			latest, err := fx.store.GetLatestEvent(bg, persistence.Unscoped(), "stale")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.Not(specs.BeNil()))
			// a rejected conditional write must not modify the persisted log
			ctx.Expect(latest).To(sequenceNumber[*egopb.Event](1))
		})
	})
}

func TestEventStore_WriteEvents_GenesisSucceedsOnEmpty(t *testing.T) {
	specs.Describe(t, "EventStore.WriteEvents with a genesis precondition", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("succeeds when the entity has no events yet", func(ctx *specs.Context) {
			err := fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "genesis-ok", 1), persistence.ExpectGenesis())
			ctx.Expect(err).To(specs.BeNil())

			latest, err := fx.store.GetLatestEvent(bg, persistence.Unscoped(), "genesis-ok")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.Not(specs.BeNil()))
			ctx.Expect(latest).To(sequenceNumber[*egopb.Event](1))
		})
	})
}

func TestEventStore_WriteEvents_GenesisConflictsOnExisting(t *testing.T) {
	specs.Describe(t, "EventStore.WriteEvents with a genesis precondition on an existing entity", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("reports a conflict and keeps the existing event", func(ctx *specs.Context) {
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "genesis-taken", 1), persistence.ExpectGenesis())).To(specs.BeNil())

			err := fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "genesis-taken", 2), persistence.ExpectGenesis())

			var conflict *persistence.ConflictError
			ctx.Expect(err).To(specs.MatchErrorAs(&conflict))
			actual, ok := conflict.ActualRevision()
			ctx.Expect(ok).To(specs.BeTrue())
			ctx.Expect(actual).ToEqual(uint64(1))

			latest, err := fx.store.GetLatestEvent(bg, persistence.Unscoped(), "genesis-taken")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.Not(specs.BeNil()))
			ctx.Expect(latest).To(sequenceNumber[*egopb.Event](1))
		})
	})
}

// ---------------------------------------------------------------------------
// newEventLog: duplicate SequenceNumber must not accumulate (regression)
// ---------------------------------------------------------------------------
//
// Before the eventLog restructuring, events were keyed by
// EventKey{PersistenceID, SequenceNumber} in a sync.Map: writing the same key
// again silently overwrote the prior entry, so every read path observed
// exactly one event per (PersistenceID, SequenceNumber) pair. These tests
// prove the eventLog-based representation preserves that observable
// semantic through ReplayEvents, GetLatestEvent, GetShardEvents,
// ShardOffsets, and DeleteEvents.

func TestEventStore_WriteEvents_DuplicateSequenceNumberOverwritesNotAccumulates(t *testing.T) {
	specs.Describe(t, "EventStore.WriteEvents with a duplicate sequence number", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("overwrites the earlier event instead of accumulating", func(ctx *specs.Context) {
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "dup-sn", 1), persistence.Unconditional())).To(specs.BeNil())
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "dup-sn", 1), persistence.Unconditional())).To(specs.BeNil())

			replayed, err := fx.store.ReplayEvents(bg, persistence.Unscoped(), "dup-sn", 1, 10, 100)
			ctx.Expect(err).To(specs.BeNil())
			// rewriting SequenceNumber 1 must not produce two visible events
			ctx.Expect(replayed).To(specs.HaveLen(1))

			latest, err := fx.store.GetLatestEvent(bg, persistence.Unscoped(), "dup-sn")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.Not(specs.BeNil()))
			ctx.Expect(latest).To(sequenceNumber[*egopb.Event](1))
		})
	})
}

func TestEventStore_WriteEvents_DuplicateSequenceNumberWithinConditionalWriteOverwrites(t *testing.T) {
	specs.Describe(t, "EventStore.WriteEvents with a duplicate sequence number under a conditional write", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("overwrites the earlier event instead of accumulating", func(ctx *specs.Context) {
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "dup-sn-conditional", 1), persistence.ExpectGenesis())).To(specs.BeNil())
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "dup-sn-conditional", 1), persistence.ExpectRevision(1))).To(specs.BeNil())

			replayed, err := fx.store.ReplayEvents(bg, persistence.Unscoped(), "dup-sn-conditional", 1, 10, 100)
			ctx.Expect(err).To(specs.BeNil())
			// a conditional rewrite of SequenceNumber 1 must not produce two visible events
			ctx.Expect(replayed).To(specs.HaveLen(1))

			latest, err := fx.store.GetLatestEvent(bg, persistence.Unscoped(), "dup-sn-conditional")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.Not(specs.BeNil()))
			ctx.Expect(latest).To(sequenceNumber[*egopb.Event](1))
		})
	})
}

func TestEventStore_WriteEvents_DuplicateSequenceNumberDoesNotDuplicateShardEventsOrOffsets(t *testing.T) {
	specs.Describe(t, "EventStore.WriteEvents with a duplicate sequence number and the shard views", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("does not duplicate shard events or skew shard offsets", func(ctx *specs.Context) {
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "dup-sn-shard", 1), persistence.Unconditional())).To(specs.BeNil())
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "dup-sn-shard", 1), persistence.Unconditional())).To(specs.BeNil())

			shardEvents, _, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 0, 100)
			ctx.Expect(err).To(specs.BeNil())
			// rewriting SequenceNumber 1 must not duplicate its shard's events
			ctx.Expect(shardEvents).To(specs.HaveLen(1))

			offsets, err := fx.store.ShardOffsets(bg, persistence.Unscoped())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(offsets).To(specs.HaveKey(uint64(1)))
			// the duplicate rewrite must not skew the shard's offset beyond its single surviving event
			ctx.Expect(offsets[1]).ToEqual(shardEvents[0].GetTimestamp())
		})
	})
}

func TestEventStore_WriteEvents_DuplicateSequenceNumberThenDeleteEventsLeavesNoResidual(t *testing.T) {
	specs.Describe(t, "EventStore.DeleteEvents after a duplicate sequence number write", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("leaves no residual duplicate", func(ctx *specs.Context) {
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "dup-sn-delete", 1), persistence.Unconditional())).To(specs.BeNil())
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "dup-sn-delete", 1), persistence.Unconditional())).To(specs.BeNil())
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), newAccountEvent(ctx, "dup-sn-delete", 2), persistence.Unconditional())).To(specs.BeNil())

			ctx.Expect(fx.store.DeleteEvents(bg, persistence.Unscoped(), "dup-sn-delete", 1)).To(specs.BeNil())

			replayed, err := fx.store.ReplayEvents(bg, persistence.Unscoped(), "dup-sn-delete", 1, 10, 100)
			ctx.Expect(err).To(specs.BeNil())
			// deleting up to SequenceNumber 1 must leave exactly the surviving event, not a residual duplicate
			ctx.Expect(replayed).To(specs.HaveLen(1))
			ctx.Expect(replayed[0]).To(sequenceNumber[*egopb.Event](2))
		})
	})
}

func TestEventStore_WriteEvents_ConditionalBatchMustShareOnePersistenceID(t *testing.T) {
	specs.Describe(t, "EventStore.WriteEvents with a conditional batch", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("must share one persistence id", func(ctx *specs.Context) {
			mixed := append(newAccountEvent(ctx, "batch-a", 1), newAccountEvent(ctx, "batch-b", 1)...)

			err := fx.store.WriteEvents(bg, persistence.Unscoped(), mixed, persistence.ExpectGenesis())
			ctx.Expect(err).To(specs.MatchError(persistence.ErrPreconditionScope))

			err = fx.store.WriteEvents(bg, persistence.Unscoped(), nil, persistence.ExpectGenesis())
			ctx.Expect(err).To(specs.MatchError(persistence.ErrPreconditionScope))
		})
	})
}
