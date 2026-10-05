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

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/projection"
	"github.com/getsyntegrity/urd/tenancy"
)

// countingHandler counts the events a projection handles.
type countingHandler struct{ handled atomic.Int32 }

func (h *countingHandler) Handle(context.Context, string, *anypb.Any, uint64) error {
	h.handled.Inc()
	return nil
}

// TestProjectionAdvancesOnTenantScopedPublication pins the temporary
// projection wake-up (EGO-TENANT-005, removed by #93). The runner subscribes
// through the legacy Unscoped() API, so without withProjectionWake it never
// hears of a tenant-scoped publication. The pull interval is an hour, so the
// only thing that can make the projection advance inside the test is that
// nudge: no sleep, and no timer, stands in for it.
func TestProjectionAdvancesOnTenantScopedPublication(t *testing.T) {
	specs.Describe(t, "a projection in a tenant-aware engine is woken by a tenant-scoped publication", func(s *specs.Spec) {
		bg := context.Background()
		const projectionName = "tenant-wake"
		const shardNumber = uint64(3)

		s.It("handles the journal's events once a tenant-scoped publication is posted", func(ctx *specs.Context) {
			journalStore := connectedEventsStore(ctx)
			offsetStore := connectedOffsetStore(ctx)
			stream := eventstream.New()
			ctx.Cleanup(stream.Close)
			handler := &countingHandler{}

			actorSystem, err := goakt.NewActorSystem("ProjectionWake",
				goakt.WithLogger(newLoggerAdapter(DiscardLogger)),
				goakt.WithExtensions(
					extensions.NewEventsStore(journalStore),
					extensions.NewOffsetStore(offsetStore),
					extensions.NewEventsStream(stream),
					extensions.NewTenancyMarker(false),
					extensions.NewProjectionExtension(map[string]*projection.Options{
						projectionName: {Handler: handler, BufferSize: 500, PullInterval: time.Hour, Recovery: projection.NewRecovery()},
					})),
				goakt.WithActorInitMaxRetries(3))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(actorSystem.Start(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = actorSystem.Stop(context.Background()) })

			// The runner reads the journal itself; the events are in the journal
			// before the projection starts, exactly as after a tenant's write.
			const count = 5
			journals := accountCreditedEventsG4(ctx, uuid.NewString(), shardNumber, count)
			ctx.Expect(journalStore.WriteEvents(bg, persistence.Unscoped(), journals, persistence.Unconditional())).To(specs.BeNil())
			spawnProjectionG4(ctx, actorSystem, projectionName)

			// Publish for a tenant scope, as the events writer does after a write.
			tid, err := tenancy.NewTenantID("tenant-a")
			ctx.Expect(err).To(specs.BeNil())
			scope, err := persistence.NewTenantScope(tid)
			ctx.Expect(err).To(specs.BeNil())
			tc, err := tenancy.NewTenantContext(tid)
			ctx.Expect(err).To(specs.BeNil())
			event := &egopb.Event{PersistenceId: journals[0].GetPersistenceId(), TenantMetadata: tenancy.MarshalMetadata(tc)}
			ctx.Expect(protocol.PublishScoped(stream, scope, protocol.EventsTopic, event, event.GetTenantMetadata())).To(specs.BeNil())

			ctx.Eventually(func() any { return handler.handled.Load() }, specs.Equal(int32(count)),
				specs.WithTimeout(waitTimeout), specs.WithInterval(time.Millisecond))
		})
	})
}
