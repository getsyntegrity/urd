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
	"fmt"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/encryption"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
)

// connectable is the lifecycle every testkit store shares.
type connectable interface {
	Connect(context.Context) error
	Disconnect(context.Context) error
}

// lifecycleStore is a store that can also be pinged.
type lifecycleStore interface {
	connectable
	Ping(context.Context) error
}

// connectedStore holds the store of the running case. withConnectedStore gives
// every case its own connected store and asserts that it disconnects cleanly
// once the case ends.
type connectedStore[S connectable] struct{ store S }

func withConnectedStore[S connectable](s *specs.Spec, newStore func() S) *connectedStore[S] {
	bg := context.TODO()
	f := &connectedStore[S]{}
	s.BeforeEach(func(ctx *specs.Context) {
		f.store = newStore()
		ctx.Expect(f.store.Connect(bg)).To(specs.BeNil())
	})
	s.AfterEach(func(ctx *specs.Context) {
		ctx.Expect(f.store.Disconnect(bg)).To(specs.BeNil())
	})
	return f
}

// accountPayload and accountStatePayload build the protobuf payloads the
// events and states of these tests carry.
func accountPayload(ctx *specs.Context) *anypb.Any {
	payload, err := anypb.New(&testpb.AccountCreated{AccountId: "acc-1", AccountBalance: 100})
	ctx.Expect(err).To(specs.BeNil())
	return payload
}

func accountStatePayload(ctx *specs.Context, balance float64) *anypb.Any {
	payload, err := anypb.New(&testpb.Account{AccountId: "acc-1", AccountBalance: balance})
	ctx.Expect(err).To(specs.BeNil())
	return payload
}

// eventsOf builds one shard-1 event per sequence number for persistenceID.
func eventsOf(ctx *specs.Context, persistenceID string, sequenceNumbers ...uint64) []*egopb.Event {
	payload := accountPayload(ctx)
	events := make([]*egopb.Event, 0, len(sequenceNumbers))
	for _, sn := range sequenceNumbers {
		events = append(events, &egopb.Event{PersistenceId: persistenceID, SequenceNumber: sn, Event: payload, Timestamp: time.Now().UnixMilli(), Shard: 1})
	}
	return events
}

// sequenceNumber projects the sequence number of an event or snapshot so a
// failure names the field.
func sequenceNumber[T interface{ GetSequenceNumber() uint64 }](want uint64) specs.Matcher {
	return specs.Project("SequenceNumber", T.GetSequenceNumber, specs.Equal(want))
}

// versionNumber projects the version of a durable state so a failure names the
// field.
func versionNumber[T interface{ GetVersionNumber() uint64 }](want uint64) specs.Matcher {
	return specs.Project("VersionNumber", T.GetVersionNumber, specs.Equal(want))
}

// lifecycleRow is one case of a Disconnect or Ping table.
type lifecycleRow struct {
	name      string
	connected bool // whether the store is connected before the call
}

func lifecycleName(r lifecycleRow) string { return r.name }

// lifecycleCases registers one case per row: it builds a store, connects it
// when the row says so, and expects act to succeed.
func lifecycleCases(s *specs.Spec, rows []lifecycleRow, newStore func() lifecycleStore, act func(lifecycleStore) error) {
	bg := context.TODO()
	specs.Table(s, rows, lifecycleName, func(ctx *specs.Context, r lifecycleRow) {
		store := newStore()
		if r.connected {
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
		}
		ctx.Expect(act(store)).To(specs.BeNil())
	})
}

// connectCases registers the Connect cases. Both run against one store, so
// "already connected" really connects a store that is connected already.
func connectCases(s *specs.Spec, newStore func() lifecycleStore) {
	bg := context.TODO()
	store := newStore()
	rows := []lifecycleRow{{name: "fresh connect"}, {name: "already connected"}}
	specs.Table(s, rows, lifecycleName, func(ctx *specs.Context, _ lifecycleRow) {
		ctx.Expect(store.Connect(bg)).To(specs.BeNil())
	})
}

func disconnectCases(s *specs.Spec, newStore func() lifecycleStore) {
	bg := context.TODO()
	rows := []lifecycleRow{{name: "connected store", connected: true}, {name: "already disconnected"}}
	lifecycleCases(s, rows, newStore, func(st lifecycleStore) error { return st.Disconnect(bg) })
}

func pingCases(s *specs.Spec, newStore func() lifecycleStore) {
	bg := context.TODO()
	rows := []lifecycleRow{{name: "when connected", connected: true}, {name: "when not connected auto-connects"}}
	lifecycleCases(s, rows, newStore, func(st lifecycleStore) error { return st.Ping(bg) })
}

func newEventsLifecycle() lifecycleStore   { return NewEventsStore() }
func newDurableLifecycle() lifecycleStore  { return NewDurableStore() }
func newOffsetLifecycle() lifecycleStore   { return NewOffsetStore() }
func newSnapshotLifecycle() lifecycleStore { return NewSnapshotStore() }

// ---------------------------------------------------------------------------
// EventStore tests
// ---------------------------------------------------------------------------

func TestEventStore_NewEventsStore(t *testing.T) {
	specs.Describe(t, "NewEventsStore", func(s *specs.Spec) {
		s.It("returns a store", func(ctx *specs.Context) {
			ctx.Expect(NewEventsStore()).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestEventStore_Connect(t *testing.T) {
	specs.Describe(t, "EventStore.Connect", func(s *specs.Spec) {
		connectCases(s, newEventsLifecycle)
	})
}

func TestEventStore_Disconnect(t *testing.T) {
	specs.Describe(t, "EventStore.Disconnect", func(s *specs.Spec) {
		disconnectCases(s, newEventsLifecycle)
	})
}

func TestEventStore_Ping(t *testing.T) {
	specs.Describe(t, "EventStore.Ping", func(s *specs.Spec) {
		pingCases(s, newEventsLifecycle)
	})
}

func TestEventStore_WriteAndReplayEvents(t *testing.T) {
	specs.Describe(t, "EventStore replays the events written for an entity", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)
		s.BeforeEach(func(ctx *specs.Context) {
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), eventsOf(ctx, "entity-1", 1, 2, 3), persistence.Unconditional())).To(specs.BeNil())
		})

		s.It("replay all events", func(ctx *specs.Context) {
			replayed, err := fx.store.ReplayEvents(bg, persistence.Unscoped(), "entity-1", 1, 3, 10)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(replayed).To(specs.HaveLen(3))
		})

		s.It("replay with limit", func(ctx *specs.Context) {
			replayed, err := fx.store.ReplayEvents(bg, persistence.Unscoped(), "entity-1", 1, 3, 2)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(replayed).To(specs.HaveLen(2))
		})

		s.It("replay non-existent entity", func(ctx *specs.Context) {
			replayed, err := fx.store.ReplayEvents(bg, persistence.Unscoped(), "non-existent", 1, 10, 100)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(replayed).To(specs.BeEmpty())
		})
	})
}

func TestEventStore_GetLatestEvent(t *testing.T) {
	specs.Describe(t, "EventStore.GetLatestEvent", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("no events returns nil", func(ctx *specs.Context) {
			event, err := fx.store.GetLatestEvent(bg, persistence.Unscoped(), "non-existent")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(event).To(specs.BeNil())
		})

		s.It("returns latest by sequence number", func(ctx *specs.Context) {
			events := eventsOf(ctx, "latest-test", 1, 5, 3)
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), events, persistence.Unconditional())).To(specs.BeNil())

			latest, err := fx.store.GetLatestEvent(bg, persistence.Unscoped(), "latest-test")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest).To(specs.Not(specs.BeNil()))
			ctx.Expect(latest).To(sequenceNumber[*egopb.Event](5))
		})
	})
}

func TestEventStore_DeleteEvents(t *testing.T) {
	specs.Describe(t, "EventStore.DeleteEvents", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("removes the events up to the given sequence number", func(ctx *specs.Context) {
			events := eventsOf(ctx, "del-test", 1, 2, 3)
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), events, persistence.Unconditional())).To(specs.BeNil())

			ctx.Expect(fx.store.DeleteEvents(bg, persistence.Unscoped(), "del-test", 2)).To(specs.BeNil())

			replayed, err := fx.store.ReplayEvents(bg, persistence.Unscoped(), "del-test", 1, 3, 10)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(replayed).To(specs.HaveElementsInOrder(sequenceNumber[*egopb.Event](3)))
		})
	})
}

func TestEventStore_PersistenceIDs(t *testing.T) {
	specs.Describe(t, "EventStore.PersistenceIDs", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("empty store", func(ctx *specs.Context) {
			ids, nextToken, err := fx.store.PersistenceIDs(bg, persistence.Unscoped(), 10, "")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(ids).To(specs.BeEmpty())
			ctx.Expect(nextToken).To(specs.BeEmpty())
		})

		s.It("with events and pagination", func(ctx *specs.Context) {
			payload := accountPayload(ctx)
			events := []*egopb.Event{
				{PersistenceId: "pid-a", SequenceNumber: 1, Event: payload, Timestamp: time.Now().UnixMilli(), Shard: 1},
				{PersistenceId: "pid-b", SequenceNumber: 1, Event: payload, Timestamp: time.Now().UnixMilli(), Shard: 1},
				{PersistenceId: "pid-c", SequenceNumber: 1, Event: payload, Timestamp: time.Now().UnixMilli(), Shard: 1},
			}
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), events, persistence.Unconditional())).To(specs.BeNil())

			ids, nextToken, err := fx.store.PersistenceIDs(bg, persistence.Unscoped(), 2, "")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(ids).To(specs.HaveLen(2))
			ctx.Expect(nextToken).To(specs.Not(specs.BeEmpty()))

			ids2, nextToken2, err := fx.store.PersistenceIDs(bg, persistence.Unscoped(), 10, nextToken)
			ctx.Expect(err).To(specs.BeNil())
			// the third id must still be returned by the second page, not skipped at the page boundary
			ctx.Expect(ids2).To(specs.Equal([]string{"pid-c"}))
			// no ids remain, so the token must signal iteration is complete
			ctx.Expect(nextToken2).To(specs.BeEmpty())
		})
	})
}

// TestEventStore_PersistenceIDsPaginationExhaustive is a direct regression
// for the off-by-one at every page boundary: PersistenceIDs used to hand
// back keys[endIndex] (the first key NOT yet returned) as nextPageToken,
// while the following call resumed strictly AFTER that same token. That key
// was therefore never returned by any page. This writes enough persistence
// ids to force several pages at a small page size and asserts that
// iterating to exhaustion returns every id exactly once, matching the
// contract now stated on persistence.EventsStore.PersistenceIDs's doc
// comment. See also persistence/conformance/events.go's
// eventsPersistenceIDsPaginationCoversEveryIDExactlyOnce, which pins the
// same contract for every conforming store implementation.
func TestEventStore_PersistenceIDsPaginationExhaustive(t *testing.T) {
	specs.Describe(t, "EventStore.PersistenceIDs pagination", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("iterating to exhaustion returns every id exactly once", func(ctx *specs.Context) {
			const pageSize = 3
			const total = 10 // forces at least four pages at pageSize

			want := make([]string, 0, total)
			for i := 0; i < total; i++ {
				id := fmt.Sprintf("pagination-exhaustive-%02d", i)
				want = append(want, id)
				ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), eventsOf(ctx, id, 1), persistence.Unconditional())).To(specs.BeNil())
			}

			var got []string
			var pageToken string
			pages := 0
			for {
				ids, nextToken, err := fx.store.PersistenceIDs(bg, persistence.Unscoped(), pageSize, pageToken)
				ctx.Expect(err).To(specs.BeNil())
				pages++
				// pagination must terminate
				ctx.Expect(pages).To(specs.BeLessThanOrEqual(total + 1))
				got = append(got, ids...)
				if nextToken == "" {
					break
				}
				pageToken = nextToken
			}

			// the setup must actually force multiple pages
			ctx.Expect(pages).To(specs.BeGreaterThanOrEqual(4))
			// every written id must be returned exactly once across pages, none skipped at a page boundary
			ctx.Expect(got).To(specs.ContainTheSameElementsAs(want))
		})
	})
}

func TestEventStore_GetShardEvents(t *testing.T) {
	specs.Describe(t, "EventStore.GetShardEvents", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("no events for shard", func(ctx *specs.Context) {
			events, nextOffset, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 99, 0, 10)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(events).To(specs.BeEmpty())
			ctx.Expect(nextOffset).To(specs.Equal(int64(0)))
		})

		s.It("with shard events", func(ctx *specs.Context) {
			payload := accountPayload(ctx)
			ts := time.Now().UnixMilli()
			events := []*egopb.Event{
				{PersistenceId: "shard-test-1", SequenceNumber: 1, Event: payload, Timestamp: ts, Shard: 5},
				{PersistenceId: "shard-test-2", SequenceNumber: 1, Event: payload, Timestamp: ts + 1, Shard: 5},
				{PersistenceId: "shard-test-3", SequenceNumber: 1, Event: payload, Timestamp: ts + 2, Shard: 6},
			}
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), events, persistence.Unconditional())).To(specs.BeNil())

			result, nextOffset, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 5, 0, 10)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(result).To(specs.Not(specs.BeEmpty()))
			ctx.Expect(nextOffset).To(specs.BeGreaterThan(int64(0)))
		})
	})
}

func TestEventStore_ShardOffsets(t *testing.T) {
	specs.Describe(t, "EventStore.ShardOffsets", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		s.It("reports the timestamp of each shard's most recent event", func(ctx *specs.Context) {
			payload := accountPayload(ctx)
			events := []*egopb.Event{
				{PersistenceId: "sn-1", SequenceNumber: 1, Event: payload, Timestamp: 100, Shard: 1},
				{PersistenceId: "sn-2", SequenceNumber: 1, Event: payload, Timestamp: 300, Shard: 2},
				{PersistenceId: "sn-3", SequenceNumber: 1, Event: payload, Timestamp: 200, Shard: 1},
			}
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), events, persistence.Unconditional())).To(specs.BeNil())

			offsets, err := fx.store.ShardOffsets(bg, persistence.Unscoped())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(offsets).To(specs.HaveLen(2))
			// each shard reports the timestamp of its most recent event
			ctx.Expect(offsets).To(specs.HavePair(uint64(1), int64(200)))
			ctx.Expect(offsets).To(specs.HavePair(uint64(2), int64(300)))
		})
	})
}

// ---------------------------------------------------------------------------
// DurableStore tests
// ---------------------------------------------------------------------------

func TestDurableStore_NewDurableStore(t *testing.T) {
	specs.Describe(t, "NewDurableStore", func(s *specs.Spec) {
		s.It("returns a store", func(ctx *specs.Context) {
			ctx.Expect(NewDurableStore()).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestDurableStore_Connect(t *testing.T) {
	specs.Describe(t, "DurableStore.Connect", func(s *specs.Spec) {
		connectCases(s, newDurableLifecycle)
	})
}

func TestDurableStore_Disconnect(t *testing.T) {
	specs.Describe(t, "DurableStore.Disconnect", func(s *specs.Spec) {
		disconnectCases(s, newDurableLifecycle)
	})
}

func TestDurableStore_Ping(t *testing.T) {
	specs.Describe(t, "DurableStore.Ping", func(s *specs.Spec) {
		pingCases(s, newDurableLifecycle)
	})
}

func TestDurableStore_WriteAndGetState(t *testing.T) {
	specs.Describe(t, "DurableStore writes and reads durable state", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewDurableStore)

		s.It("write and read state", func(ctx *specs.Context) {
			state := &egopb.DurableState{
				PersistenceId:  "ds-entity-1",
				ResultingState: accountStatePayload(ctx, 500),
				VersionNumber:  1,
			}
			ctx.Expect(fx.store.WriteState(bg, persistence.Unscoped(), state, persistence.Unconditional())).To(specs.BeNil())
			got, err := fx.store.GetLatestState(bg, persistence.Unscoped(), "ds-entity-1")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.Not(specs.BeNil()))
			ctx.Expect(got.GetPersistenceId()).To(specs.Equal(state.GetPersistenceId()))
			ctx.Expect(got.GetVersionNumber()).To(specs.Equal(uint64(1)))
		})

		s.It("get non-existent state", func(ctx *specs.Context) {
			got, err := fx.store.GetLatestState(bg, persistence.Unscoped(), "non-existent")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.BeNil())
		})
	})
}

// notConnectedMessage matches the error a store returns while it is not
// connected. The stores define no sentinel for it, so the text is the contract.
func notConnectedMessage() specs.Matcher {
	return specs.Project("message", error.Error, specs.Contain("not connected"))
}

func TestDurableStore_WriteState_NotConnected(t *testing.T) {
	specs.Describe(t, "DurableStore.WriteState on a store that is not connected", func(s *specs.Spec) {
		s.It("fails with a not-connected error", func(ctx *specs.Context) {
			bg := context.TODO()
			store := NewDurableStore()
			state := &egopb.DurableState{PersistenceId: "entity-1", ResultingState: accountStatePayload(ctx, 100), VersionNumber: 1}
			err := store.WriteState(bg, persistence.Unscoped(), state, persistence.Unconditional())
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(notConnectedMessage())
		})
	})
}

func TestDurableStore_GetLatestState_NotConnected(t *testing.T) {
	specs.Describe(t, "DurableStore.GetLatestState on a store that is not connected", func(s *specs.Spec) {
		s.It("fails and returns no state", func(ctx *specs.Context) {
			bg := context.TODO()
			store := NewDurableStore()
			got, err := store.GetLatestState(bg, persistence.Unscoped(), "entity-1")
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(got).To(specs.BeNil())
		})
	})
}

// ---------------------------------------------------------------------------
// OffsetStore tests
// ---------------------------------------------------------------------------

func TestOffsetStore_NewOffsetStore(t *testing.T) {
	specs.Describe(t, "NewOffsetStore", func(s *specs.Spec) {
		s.It("returns a store", func(ctx *specs.Context) {
			ctx.Expect(NewOffsetStore()).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestOffsetStore_Connect(t *testing.T) {
	specs.Describe(t, "OffsetStore.Connect", func(s *specs.Spec) {
		connectCases(s, newOffsetLifecycle)
	})
}

func TestOffsetStore_Disconnect(t *testing.T) {
	specs.Describe(t, "OffsetStore.Disconnect", func(s *specs.Spec) {
		disconnectCases(s, newOffsetLifecycle)
	})
}

func TestOffsetStore_Ping(t *testing.T) {
	specs.Describe(t, "OffsetStore.Ping", func(s *specs.Spec) {
		pingCases(s, newOffsetLifecycle)
	})
}

func TestOffsetStore_WriteAndGetOffset(t *testing.T) {
	specs.Describe(t, "OffsetStore writes and reads projection offsets", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewOffsetStore)

		s.It("write and read offset", func(ctx *specs.Context) {
			offset := &egopb.Offset{
				ProjectionName: "proj-1",
				ShardNumber:    1,
				Value:          100,
				Timestamp:      time.Now().UnixMilli(),
			}
			ctx.Expect(fx.store.WriteOffset(bg, offset)).To(specs.BeNil())

			projID := &egopb.ProjectionId{
				ProjectionName: "proj-1",
				ShardNumber:    1,
			}
			got, err := fx.store.GetCurrentOffset(bg, projID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.Not(specs.BeNil()))
			ctx.Expect(got.GetValue()).To(specs.Equal(int64(100)))
		})

		s.It("get non-existent offset", func(ctx *specs.Context) {
			projID := &egopb.ProjectionId{
				ProjectionName: "non-existent",
				ShardNumber:    99,
			}
			got, err := fx.store.GetCurrentOffset(bg, projID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.BeNil())
		})
	})
}

// ---------------------------------------------------------------------------
// SnapshotStore tests
// ---------------------------------------------------------------------------

func TestSnapshotStore_NewSnapshotStore(t *testing.T) {
	specs.Describe(t, "NewSnapshotStore", func(s *specs.Spec) {
		s.It("returns a store", func(ctx *specs.Context) {
			ctx.Expect(NewSnapshotStore()).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestSnapshotStore_Connect(t *testing.T) {
	specs.Describe(t, "SnapshotStore.Connect", func(s *specs.Spec) {
		connectCases(s, newSnapshotLifecycle)
	})
}

func TestSnapshotStore_Disconnect(t *testing.T) {
	specs.Describe(t, "SnapshotStore.Disconnect", func(s *specs.Spec) {
		disconnectCases(s, newSnapshotLifecycle)
	})
}

func TestSnapshotStore_Ping(t *testing.T) {
	specs.Describe(t, "SnapshotStore.Ping", func(s *specs.Spec) {
		pingCases(s, newSnapshotLifecycle)
	})
}

func TestSnapshotStore_WriteAndGetSnapshot(t *testing.T) {
	specs.Describe(t, "SnapshotStore writes and reads snapshots", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewSnapshotStore)
		s.BeforeEach(func(ctx *specs.Context) {
			state := accountStatePayload(ctx, 500)
			for _, sn := range []uint64{1, 5, 3} {
				snap := &egopb.Snapshot{PersistenceId: "snap-entity-1", SequenceNumber: sn, State: state}
				ctx.Expect(fx.store.WriteSnapshot(bg, persistence.Unscoped(), snap)).To(specs.BeNil())
			}
		})

		s.It("returns latest snapshot", func(ctx *specs.Context) {
			got, err := fx.store.GetLatestSnapshot(bg, persistence.Unscoped(), "snap-entity-1")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.Not(specs.BeNil()))
			ctx.Expect(got).To(sequenceNumber[*egopb.Snapshot](5))
		})

		s.It("no snapshot returns nil", func(ctx *specs.Context) {
			got, err := fx.store.GetLatestSnapshot(bg, persistence.Unscoped(), "non-existent")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.BeNil())
		})
	})
}

func TestSnapshotStore_DeleteSnapshots(t *testing.T) {
	specs.Describe(t, "SnapshotStore.DeleteSnapshots", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewSnapshotStore)

		s.It("removes the snapshots up to the given sequence number", func(ctx *specs.Context) {
			state := accountStatePayload(ctx, 100)
			for _, sn := range []uint64{1, 2, 3} {
				snap := &egopb.Snapshot{PersistenceId: "del-snap", SequenceNumber: sn, State: state}
				ctx.Expect(fx.store.WriteSnapshot(bg, persistence.Unscoped(), snap)).To(specs.BeNil())
			}

			ctx.Expect(fx.store.DeleteSnapshots(bg, persistence.Unscoped(), "del-snap", 2)).To(specs.BeNil())

			got, err := fx.store.GetLatestSnapshot(bg, persistence.Unscoped(), "del-snap")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.Not(specs.BeNil()))
			ctx.Expect(got).To(sequenceNumber[*egopb.Snapshot](3))
		})
	})
}

// ---------------------------------------------------------------------------
// KeyStore tests
// ---------------------------------------------------------------------------

func TestKeyStore_NewKeyStore(t *testing.T) {
	specs.Describe(t, "NewKeyStore", func(s *specs.Spec) {
		s.It("returns a store", func(ctx *specs.Context) {
			ctx.Expect(NewKeyStore()).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestKeyStore_GetOrCreateKey(t *testing.T) {
	specs.Describe(t, "KeyStore.GetOrCreateKey", func(s *specs.Spec) {
		bg := context.TODO()
		store := NewKeyStore()

		s.It("creates new key", func(ctx *specs.Context) {
			keyID, key, err := store.GetOrCreateKey(bg, "entity-1")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(keyID).To(specs.Not(specs.BeEmpty()))
			ctx.Expect(key).To(specs.HaveLen(32))
		})

		s.It("returns existing key", func(ctx *specs.Context) {
			keyID1, key1, err := store.GetOrCreateKey(bg, "entity-2")
			ctx.Expect(err).To(specs.BeNil())

			keyID2, key2, err := store.GetOrCreateKey(bg, "entity-2")
			ctx.Expect(err).To(specs.BeNil())

			ctx.Expect(keyID2).ToEqual(keyID1)
			ctx.Expect(key2).ToEqual(key1)
		})
	})
}

func TestKeyStore_GetKey(t *testing.T) {
	specs.Describe(t, "KeyStore.GetKey", func(s *specs.Spec) {
		bg := context.TODO()
		store := NewKeyStore()

		s.It("existing key", func(ctx *specs.Context) {
			keyID, expectedKey, err := store.GetOrCreateKey(bg, "entity-1")
			ctx.Expect(err).To(specs.BeNil())

			key, err := store.GetKey(bg, keyID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(key).ToEqual(expectedKey)
		})

		s.It("non-existent key", func(ctx *specs.Context) {
			_, err := store.GetKey(bg, "non-existent-key-id")
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(encryption.ErrKeyNotFound))
		})
	})
}

func TestKeyStore_DeleteKey(t *testing.T) {
	specs.Describe(t, "KeyStore.DeleteKey", func(s *specs.Spec) {
		bg := context.TODO()
		store := NewKeyStore()

		s.It("delete existing key", func(ctx *specs.Context) {
			keyID, _, err := store.GetOrCreateKey(bg, "entity-to-delete")
			ctx.Expect(err).To(specs.BeNil())

			err = store.DeleteKey(bg, "entity-to-delete")
			ctx.Expect(err).To(specs.BeNil())

			_, err = store.GetKey(bg, keyID)
			ctx.Expect(err).To(specs.MatchError(encryption.ErrKeyNotFound))
		})

		s.It("delete non-existent key is no-op", func(ctx *specs.Context) {
			err := store.DeleteKey(bg, "non-existent")
			ctx.Expect(err).To(specs.BeNil())
		})
	})
}
