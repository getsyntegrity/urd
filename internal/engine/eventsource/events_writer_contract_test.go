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

package eventsource

import (
	"context"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	goakt "github.com/tochemey/goakt/v4/actor"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

const contractTopic = "topic.events"

// startWriter starts an events writer over store and stream and returns its PID.
func startWriter(ctx *specs.Context, store persistence.EventsStore, stream eventstream.Stream) *goakt.PID {
	system := startEventsSystem(ctx, "WriterContractSystem", 1,
		extensions.NewEventsStore(store), extensions.NewEventsStream(stream))
	pid, err := system.Spawn(context.Background(), "events-writer", newEventsWriterActor())
	ctx.Expect(err).To(specs.BeNil())
	return pid
}

// contractWriteArgs are the arguments of the WriteEvents call the writer makes.
// ctx and the batch are matched loosely or exactly depending on the case.
type contractWriteArgs struct {
	scope        persistence.Scope
	batch        any
	precondition persistence.WritePrecondition
}

// expectWrite declares the one WriteEvents call the writer must make.
func expectWrite(ctrl *mock.Controller, a contractWriteArgs) *mock.Expectation {
	return ctrl.Method("WriteEvents").Expect(mock.Any(), a.scope, a.batch, a.precondition).Times(1)
}

// expectNoPublication forbids any publication on the stream.
func expectNoPublication(ctrl *mock.Controller) {
	ctrl.Method("Publish").Expect(mock.Any(), mock.Any()).Never()
}

// tenantEnvelopes returns count envelopes stamped with tenantID's tenant
// metadata, as a tenant-aware entity stamps them before the write.
func tenantEnvelopes(ctx *specs.Context, persistenceID string, count int, tenantID string) []*egopb.Event {
	tid, err := tenancy.NewTenantID(tenantID)
	ctx.Expect(err).To(specs.BeNil())
	tc, err := tenancy.NewTenantContext(tid)
	ctx.Expect(err).To(specs.BeNil())
	envelopes := noReplyEnvelopes(ctx, persistenceID, count)
	for _, envelope := range envelopes {
		envelope.TenantMetadata = tenancy.MarshalMetadata(tc)
	}
	return envelopes
}

// drainStream returns the messages queued for sub.
func drainStream(sub eventstream.Subscriber) []*eventstream.Message {
	var got []*eventstream.Message
	for msg := range sub.Iterator() {
		got = append(got, msg)
	}
	return got
}

func TestWriterContract(t *testing.T) {
	specs.Describe(t, "the events writer writes a batch once, publishes it only after the store accepted it, and replies every failure unchanged", func(s *specs.Spec) {
		s.It("a successful write reaches the store once and publishes every envelope in order on the request topic", func(ctx *specs.Context) {
			envelopes := noReplyEnvelopes(ctx, "entity-1", 2)
			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			stream := enginetest.NewEventStreamMock(ctrl)
			write := expectWrite(ctrl, contractWriteArgs{persistence.Unscoped(), envelopes, persistence.ExpectRevision(0)}).Return(nil)
			first := ctrl.Method("Publish").Expect(contractTopic, envelopes[0]).Times(1)
			second := ctrl.Method("Publish").Expect(contractTopic, envelopes[1]).Times(1)
			ctrl.InOrder(write, first, second)
			pid := startWriter(ctx, store, stream)

			resp, err := askEventsWriter(pid, envelopes, contractTopic, 5*time.Second, persistence.ExpectRevision(0), persistence.Unscoped())

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(resp.Err).To(specs.BeNil())
		})

		s.It("a revision conflict is replied unchanged and publishes nothing", func(ctx *specs.Context) {
			conflict := persistence.NewConflictError(persistence.Unscoped(), "entity-1", persistence.ExpectRevision(0),
				persistence.WithActualRevision(3))
			envelopes := noReplyEnvelopes(ctx, "entity-1", 2)
			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			stream := enginetest.NewEventStreamMock(ctrl)
			expectWrite(ctrl, contractWriteArgs{persistence.Unscoped(), envelopes, persistence.ExpectRevision(0)}).Return(conflict)
			expectNoPublication(ctrl)
			pid := startWriter(ctx, store, stream)

			resp, err := askEventsWriter(pid, envelopes, contractTopic, 5*time.Second, persistence.ExpectRevision(0), persistence.Unscoped())

			ctx.Expect(err).To(specs.BeNil())
			var got *persistence.ConflictError
			ctx.Expect(resp.Err).To(specs.MatchErrorAs(&got))
			ctx.Expect(got).To(specs.Satisfy("be the very conflict the store returned", func(v any) bool { return v == conflict }))
			actual, ok := got.ActualRevision()
			ctx.Expect(ok).To(specs.BeTrue())
			ctx.Expect(actual).To(specs.Equal(uint64(3)))
		})

		s.It("a store failure is replied unchanged and publishes nothing", func(ctx *specs.Context) {
			envelopes := noReplyEnvelopes(ctx, "entity-1", 2)
			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			stream := enginetest.NewEventStreamMock(ctrl)
			expectWrite(ctrl, contractWriteArgs{persistence.Unscoped(), envelopes, persistence.Unconditional()}).Return(errEventsStoreDown)
			expectNoPublication(ctrl)
			pid := startWriter(ctx, store, stream)

			resp, err := askEventsWriter(pid, envelopes, contractTopic, 5*time.Second, persistence.Unconditional(), persistence.Unscoped())

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(resp.Err).To(specs.MatchError(errEventsStoreDown))
		})

		s.It("the tenant scope of the request reaches the store unchanged", func(ctx *specs.Context) {
			scope, err := persistence.NewTenantScope(tenancy.TenantID("tenant-a"))
			ctx.Expect(err).To(specs.BeNil())
			envelopes := tenantEnvelopes(ctx, "entity-1", 2, "tenant-a")
			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			stream := newClosingEventStream(ctx)
			sub := stream.AddSubscriber()
			ctx.Expect(stream.(eventstream.ScopedStream).SubscribeScoped(sub, scope, contractTopic)).To(specs.BeNil())
			expectWrite(ctrl, contractWriteArgs{scope, envelopes, persistence.Unconditional()}).Return(nil)
			pid := startWriter(ctx, store, stream)

			resp, err := askEventsWriter(pid, envelopes, contractTopic, 5*time.Second, persistence.Unconditional(), scope)

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(resp.Err).To(specs.BeNil())
			ctx.Expect(drainStream(sub)).To(specs.HaveLen(2))
		})

		s.It("publishes only to the request's tenant: another tenant and an unscoped subscriber see nothing", func(ctx *specs.Context) {
			scope, err := persistence.NewTenantScope(tenancy.TenantID("tenant-a"))
			ctx.Expect(err).To(specs.BeNil())
			other, err := persistence.NewTenantScope(tenancy.TenantID("tenant-b"))
			ctx.Expect(err).To(specs.BeNil())
			envelopes := tenantEnvelopes(ctx, "entity-1", 1, "tenant-a")
			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			stream := newClosingEventStream(ctx)
			scoped := stream.(eventstream.ScopedStream)
			subOther, subLegacy := stream.AddSubscriber(), stream.AddSubscriber()
			ctx.Expect(scoped.SubscribeScoped(subOther, other, contractTopic)).To(specs.BeNil())
			stream.Subscribe(subLegacy, contractTopic)
			expectWrite(ctrl, contractWriteArgs{scope, envelopes, persistence.Unconditional()}).Return(nil)
			pid := startWriter(ctx, store, stream)

			resp, err := askEventsWriter(pid, envelopes, contractTopic, 5*time.Second, persistence.Unconditional(), scope)

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(resp.Err).To(specs.BeNil())
			ctx.Expect(drainStream(subOther)).To(specs.BeEmpty())
			ctx.Expect(drainStream(subLegacy)).To(specs.BeEmpty())
		})

		s.It("an envelope whose tenant identity is absent or mismatched is persisted but never published, and the write still succeeds", func(ctx *specs.Context) {
			scope, err := persistence.NewTenantScope(tenancy.TenantID("tenant-a"))
			ctx.Expect(err).To(specs.BeNil())
			absent := noReplyEnvelopes(ctx, "entity-1", 1)
			mismatched := tenantEnvelopes(ctx, "entity-1", 1, "tenant-b")
			mismatched[0].SequenceNumber = 2
			good := tenantEnvelopes(ctx, "entity-1", 1, "tenant-a")
			good[0].SequenceNumber = 3
			envelopes := []*egopb.Event{absent[0], mismatched[0], good[0]}
			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			stream := newClosingEventStream(ctx)
			sub := stream.AddSubscriber()
			ctx.Expect(stream.(eventstream.ScopedStream).SubscribeScoped(sub, scope, contractTopic)).To(specs.BeNil())
			expectWrite(ctrl, contractWriteArgs{scope, envelopes, persistence.Unconditional()}).Return(nil)
			pid := startWriter(ctx, store, stream)

			resp, err := askEventsWriter(pid, envelopes, contractTopic, 5*time.Second, persistence.Unconditional(), scope)

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(resp.Err).To(specs.BeNil())
			got := drainStream(sub)
			ctx.Expect(got).To(specs.HaveLen(1))
			ctx.Expect(got[0].Payload().(*egopb.Event).GetSequenceNumber()).To(specs.Equal(uint64(3)))
		})

		s.It("a zero-value scope publishes nothing", func(ctx *specs.Context) {
			envelopes := noReplyEnvelopes(ctx, "entity-1", 1)
			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			stream := newClosingEventStream(ctx)
			sub := stream.AddSubscriber()
			stream.Subscribe(sub, contractTopic)
			var zero persistence.Scope
			expectWrite(ctrl, contractWriteArgs{zero, envelopes, persistence.Unconditional()}).Return(nil)
			pid := startWriter(ctx, store, stream)

			resp, err := askEventsWriter(pid, envelopes, contractTopic, 5*time.Second, persistence.Unconditional(), zero)

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(resp.Err).To(specs.BeNil())
			ctx.Expect(drainStream(sub)).To(specs.BeEmpty())
		})

		s.It("a transport failure is embedded in the response, never returned", func(ctx *specs.Context) {
			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			stream := enginetest.NewEventStreamMock(ctrl)
			ctrl.Method("WriteEvents").Expect(mock.Any(), mock.Any(), mock.Any(), mock.Any()).Never()
			expectNoPublication(ctrl)
			pid := startWriter(ctx, store, stream)
			ctx.Expect(pid.Shutdown(context.Background())).To(specs.BeNil())

			resp, err := askEventsWriter(pid, noReplyEnvelopes(ctx, "entity-1", 2), contractTopic, time.Second, persistence.Unconditional(), persistence.Unscoped())

			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(resp).To(specs.Not(specs.BeNil()))
			ctx.Expect(resp.Err).To(specs.Not(specs.BeNil()))
		})
	})
}
