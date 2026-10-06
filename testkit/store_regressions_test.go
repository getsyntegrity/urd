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

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
)

// TestOffsetStore_ResetOffset pins the contract of OffsetStore.ResetOffset:
// it rewinds every shard of the named projection and nothing else. It used to
// panic as soon as one offset existed.
func TestOffsetStore_ResetOffset(t *testing.T) {
	specs.Describe(t, "OffsetStore.ResetOffset", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewOffsetStore)

		write := func(ctx *specs.Context, name string, shard uint64, value int64) *egopb.Offset {
			offset := &egopb.Offset{ProjectionName: name, ShardNumber: shard, Value: value, Timestamp: 1}
			ctx.Expect(fx.store.WriteOffset(bg, offset)).To(specs.BeNil())
			return offset
		}
		current := func(ctx *specs.Context, name string, shard uint64) *egopb.Offset {
			got, err := fx.store.GetCurrentOffset(bg, &egopb.ProjectionId{ProjectionName: name, ShardNumber: shard})
			ctx.Expect(err).To(specs.BeNil())
			return got
		}

		s.It("is a no-op on an empty store", func(ctx *specs.Context) {
			ctx.Expect(fx.store.ResetOffset(bg, "proj-a", 7)).To(specs.BeNil())
			ctx.Expect(current(ctx, "proj-a", 1)).To(specs.BeNil())
		})

		s.It("does not panic when offsets exist", func(ctx *specs.Context) {
			write(ctx, "proj-a", 1, 100)
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				_ = fx.store.ResetOffset(bg, "proj-a", 7)
			}()
			ctx.Expect(panicked).To(specs.BeFalse())
		})

		s.It("resets every shard of the named projection", func(ctx *specs.Context) {
			write(ctx, "proj-a", 1, 100)
			write(ctx, "proj-a", 2, 200)

			ctx.Expect(fx.store.ResetOffset(bg, "proj-a", 7)).To(specs.BeNil())

			for _, shard := range []uint64{1, 2} {
				got := current(ctx, "proj-a", shard)
				ctx.Expect(got).To(specs.Not(specs.BeNil()))
				ctx.Expect(got.GetValue()).To(specs.Equal(int64(7)))
				ctx.Expect(got.GetProjectionName()).To(specs.Equal("proj-a"))
				ctx.Expect(got.GetShardNumber()).To(specs.Equal(shard))
				ctx.Expect(got.GetTimestamp()).To(specs.BeGreaterThan(int64(1)))
			}
		})

		s.It("leaves other projections untouched", func(ctx *specs.Context) {
			write(ctx, "proj-a", 1, 100)
			other := write(ctx, "proj-b", 1, 300)

			ctx.Expect(fx.store.ResetOffset(bg, "proj-a", 7)).To(specs.BeNil())

			got := current(ctx, "proj-b", 1)
			ctx.Expect(got.GetValue()).To(specs.Equal(int64(300)))
			ctx.Expect(got.GetTimestamp()).To(specs.Equal(int64(1)))
			ctx.Expect(other.GetValue()).To(specs.Equal(int64(300)))
		})

		s.It("does not modify the offset value the caller wrote", func(ctx *specs.Context) {
			written := write(ctx, "proj-a", 1, 100)

			ctx.Expect(fx.store.ResetOffset(bg, "proj-a", 7)).To(specs.BeNil())

			ctx.Expect(written.GetValue()).To(specs.Equal(int64(100)))
			ctx.Expect(written.GetTimestamp()).To(specs.Equal(int64(1)))
		})

		s.It("does not create offsets for a projection that has none", func(ctx *specs.Context) {
			write(ctx, "proj-a", 1, 100)

			ctx.Expect(fx.store.ResetOffset(bg, "proj-missing", 7)).To(specs.BeNil())

			ctx.Expect(current(ctx, "proj-missing", 1)).To(specs.BeNil())
			ctx.Expect(current(ctx, "proj-a", 1).GetValue()).To(specs.Equal(int64(100)))
		})
	})
}

// TestEventStore_GetShardEventsLimitAndOrder pins the GetShardEvents contract
// the Postgres store already follows: events strictly after the offset, in a
// total order, at most limit of them, and the next offset is the timestamp of
// the last one returned.
func TestEventStore_GetShardEventsLimitAndOrder(t *testing.T) {
	specs.Describe(t, "EventStore.GetShardEvents limit and order", func(s *specs.Spec) {
		bg := context.TODO()
		fx := withConnectedStore(s, NewEventsStore)

		write := func(ctx *specs.Context, shard uint64, timestamps ...int64) {
			payload := accountPayload(ctx)
			events := make([]*egopb.Event, 0, len(timestamps))
			for i, ts := range timestamps {
				events = append(events, &egopb.Event{
					PersistenceId:  fmt.Sprintf("pid-%02d", i),
					SequenceNumber: 1,
					Event:          payload,
					Timestamp:      ts,
					Shard:          shard,
				})
			}
			ctx.Expect(fx.store.WriteEvents(bg, persistence.Unscoped(), events, persistence.Unconditional())).To(specs.BeNil())
		}

		s.It("returns exactly limit events, the oldest ones", func(ctx *specs.Context) {
			write(ctx, 1, 50, 40, 30, 20, 10)

			got, next, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 0, 2)

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.HaveLen(2))
			ctx.Expect(got[0].GetTimestamp()).To(specs.Equal(int64(10)))
			ctx.Expect(got[1].GetTimestamp()).To(specs.Equal(int64(20)))
			ctx.Expect(next).To(specs.Equal(int64(20)))
		})

		s.It("returns everything when the limit exceeds the matches", func(ctx *specs.Context) {
			write(ctx, 1, 10, 20, 30)

			got, next, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 0, 10)

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.HaveLen(3))
			ctx.Expect(next).To(specs.Equal(int64(30)))
		})

		s.It("returns exactly limit events when the limit equals the matches", func(ctx *specs.Context) {
			write(ctx, 1, 10, 20, 30)

			got, _, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 0, 3)

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.HaveLen(3))
		})

		s.It("returns nothing for a zero limit", func(ctx *specs.Context) {
			write(ctx, 1, 10, 20, 30)

			got, next, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 0, 0)

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.BeEmpty())
			ctx.Expect(next).To(specs.Equal(int64(0)))
		})

		s.It("returns events strictly after the offset", func(ctx *specs.Context) {
			write(ctx, 1, 10, 20, 30)

			got, next, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 20, 10)

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.HaveLen(1))
			ctx.Expect(got[0].GetTimestamp()).To(specs.Equal(int64(30)))
			ctx.Expect(next).To(specs.Equal(int64(30)))
		})

		s.It("returns nothing once the offset reaches the last event", func(ctx *specs.Context) {
			write(ctx, 1, 10, 20, 30)

			got, next, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 30, 10)

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.BeEmpty())
			ctx.Expect(next).To(specs.Equal(int64(0)))
		})

		s.It("only returns the requested shard", func(ctx *specs.Context) {
			write(ctx, 1, 10, 20)
			write(ctx, 2, 15, 25)

			got, _, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 2, 0, 10)

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.HaveLen(2))
			for _, event := range got {
				ctx.Expect(event.GetShard()).To(specs.Equal(uint64(2)))
			}
		})

		s.It("orders ties by persistence ID and never cuts a timestamp group, on every call", func(ctx *specs.Context) {
			// same timestamp for every event: only the tie-break orders them, and
			// the limit must not leave part of the group behind, because the
			// returned offset is that timestamp and the next read is after it.
			timestamps := make([]int64, 12)
			for i := range timestamps {
				timestamps[i] = 100
			}
			write(ctx, 1, timestamps...)

			for range 25 {
				got, next, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 0, 5)

				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(got).To(specs.HaveLen(12))
				for i, event := range got {
					ctx.Expect(event.GetPersistenceId()).To(specs.Equal(fmt.Sprintf("pid-%02d", i)))
				}
				ctx.Expect(next).To(specs.Equal(int64(100)))
			}
		})

		s.It("applies the limit between timestamp groups and extends it to finish the last one", func(ctx *specs.Context) {
			write(ctx, 1, 100, 100, 100, 200, 200, 300)

			got, next, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 0, 2)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.HaveLen(3))
			ctx.Expect(next).To(specs.Equal(int64(100)))

			got, next, err = fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, next, 2)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.HaveLen(2))
			ctx.Expect(next).To(specs.Equal(int64(200)))

			got, next, err = fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, next, 2)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.HaveLen(1))
			ctx.Expect(next).To(specs.Equal(int64(300)))
		})

		s.It("returns nothing for a zero limit", func(ctx *specs.Context) {
			write(ctx, 1, 100, 100)

			got, next, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 0, 0)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.BeEmpty())
			ctx.Expect(next).To(specs.Equal(int64(0)))
		})

		s.It("does not modify the stored events", func(ctx *specs.Context) {
			write(ctx, 1, 30, 10, 20)

			_, _, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 0, 2)
			ctx.Expect(err).To(specs.BeNil())

			offsets, err := fx.store.ShardOffsets(bg, persistence.Unscoped())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(offsets).To(specs.HavePair(uint64(1), int64(30)))
			all, _, err := fx.store.GetShardEvents(bg, persistence.Unscoped(), 1, 0, 10)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(all).To(specs.HaveLen(3))
		})
	})
}
