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
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

const rejectedCounter = "urd.publication.rejected.total"

// rejectedCount returns the sum of the rejection counter in reader, 0 when the
// instrument has recorded nothing.
func rejectedCount(ctx *specs.Context, reader sdkmetric.Reader) int64 {
	var rm metricdata.ResourceMetrics
	ctx.Expect(reader.Collect(context.Background(), &rm)).To(specs.BeNil())
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != rejectedCounter {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			ctx.Expect(ok).To(specs.BeTrue())
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
		}
	}
	return total
}

func TestWriterCountsRejectedPublications(t *testing.T) {
	specs.Describe(t, "the events writer counts each dropped publication in urd.publication.rejected.total (EGO-TENANT-005)", func(s *specs.Spec) {
		type rig struct {
			pid    *goakt.PID
			sub    eventstream.Subscriber
			reader *sdkmetric.ManualReader
			scope  persistence.Scope
		}
		newRig := func(ctx *specs.Context, envelopes []*egopb.Event) rig {
			scope, err := persistence.NewTenantScope(tenancy.TenantID("tenant-a"))
			ctx.Expect(err).To(specs.BeNil())
			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			stream := newClosingEventStream(ctx)
			sub := stream.AddSubscriber()
			ctx.Expect(stream.(eventstream.ScopedStream).SubscribeScoped(sub, streamScopeOf(ctx, scope), contractTopic)).To(specs.BeNil())
			reader := sdkmetric.NewManualReader()
			meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
			expectWrite(ctrl, contractWriteArgs{scope, envelopes, persistence.Unconditional()}).Return(nil)
			system := startEventsSystem(ctx, "WriterRejectionCounter", 1,
				extensions.NewEventsStore(store), extensions.NewEventsStream(stream),
				extensions.NewTelemetryExtension(tracenoop.NewTracerProvider().Tracer("test"), meter))
			pid, err := system.Spawn(context.Background(), "events-writer", newEventsWriterActor())
			ctx.Expect(err).To(specs.BeNil())
			return rig{pid: pid, sub: sub, reader: reader, scope: scope}
		}
		write := func(ctx *specs.Context, r rig, envelopes []*egopb.Event) {
			resp, err := askEventsWriter(r.pid, envelopes, contractTopic, 5*time.Second, persistence.Unconditional(), r.scope)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(resp.Err).To(specs.BeNil())
		}

		s.It("counts exactly one for an envelope with absent tenant metadata, and publishes nothing", func(ctx *specs.Context) {
			envelopes := noReplyEnvelopes(ctx, "entity-1", 1)
			r := newRig(ctx, envelopes)

			write(ctx, r, envelopes)

			ctx.Expect(rejectedCount(ctx, r.reader)).To(specs.Equal(int64(1)))
			ctx.Expect(drainStream(r.sub)).To(specs.BeEmpty())
		})

		s.It("counts exactly one for an envelope naming another tenant, and none for the valid one beside it", func(ctx *specs.Context) {
			bad := tenantEnvelopes(ctx, "entity-1", 1, "tenant-b")
			good := tenantEnvelopes(ctx, "entity-1", 1, "tenant-a")
			good[0].SequenceNumber = 2
			envelopes := []*egopb.Event{bad[0], good[0]}
			r := newRig(ctx, envelopes)

			write(ctx, r, envelopes)

			ctx.Expect(rejectedCount(ctx, r.reader)).To(specs.Equal(int64(1)))
			ctx.Expect(drainStream(r.sub)).To(specs.HaveLen(1))
		})

		s.It("keeps counting through the lazily created instruments: two rejections make two", func(ctx *specs.Context) {
			a := noReplyEnvelopes(ctx, "entity-1", 2)
			r := newRig(ctx, a)

			write(ctx, r, a)

			ctx.Expect(rejectedCount(ctx, r.reader)).To(specs.Equal(int64(2)))
		})

		s.It("records nothing when every envelope is valid", func(ctx *specs.Context) {
			envelopes := tenantEnvelopes(ctx, "entity-1", 2, "tenant-a")
			r := newRig(ctx, envelopes)

			write(ctx, r, envelopes)

			ctx.Expect(rejectedCount(ctx, r.reader)).To(specs.Equal(int64(0)))
			ctx.Expect(drainStream(r.sub)).To(specs.HaveLen(2))
		})
	})
}
