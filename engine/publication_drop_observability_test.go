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
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	kitlog "github.com/pablogore/kit-logger/pkg/logger"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/tenancy"
)

// capturedRecord is one log record the engine wrote: its message, level and
// attributes rendered as strings.
type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// captureHandler is the slog sink of the engine's kit-logger in these tests.
type captureHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *captureHandler) WithGroup(string) slog.Handler            { return h }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = fmt.Sprint(a.Value.Any())
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
	h.mu.Unlock()
	return nil
}

// withMessage returns the records whose message is msg.
func (h *captureHandler) withMessage(msg string) []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []capturedRecord
	for _, r := range h.records {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

const droppedMessage = "publication dropped: tenant identity check failed"

// rejectedTotal sums urd.publication.rejected.total in reader.
func rejectedTotal(ctx *specs.Context, reader sdkmetric.Reader) int64 {
	var rm metricdata.ResourceMetrics
	ctx.Expect(reader.Collect(context.Background(), &rm)).To(specs.BeNil())
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "urd.publication.rejected.total" {
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

// TestPublicationDropIsLoggedAndCounted proves that a message which reaches the
// delivery boundary with a tenant identity that does not match its scope is
// dropped for real: never handed to the publisher, logged once at error level,
// counted exactly once in urd.publication.rejected.total, and followed by the
// normal delivery of a valid message.
//
// The bad message is published through the scoped stream, so it passes the
// publish-site check (which a real writer would apply) and reaches the engine's
// delivery loop. A publisher's loop consumes its queue in publish order and one
// message at a time, and the drop (log and counter) happens before the next
// message is taken: once the valid message that follows is seen by the
// publisher, every earlier message has already been processed, so the counter
// and the log are read without waiting.
func TestPublicationDropIsLoggedAndCounted(t *testing.T) {
	specs.Describe(t, "a publication dropped at delivery is logged and counted (EGO-TENANT-005)", func(s *specs.Spec) {
		type drop struct {
			name     string
			metadata func(ctx *specs.Context) map[string]string
		}
		adminMetadata := func(ctx *specs.Context) map[string]string {
			admin, err := tenancy.NewAdministrative("ops", "audit")
			ctx.Expect(err).To(specs.BeNil())
			adminCtx, err := tenancy.NewAdministrativeContext(admin)
			ctx.Expect(err).To(specs.BeNil())
			return tenancy.MarshalMetadata(adminCtx)
		}
		drops := []drop{
			{"administrative metadata", adminMetadata},
			{"another tenant's metadata", func(ctx *specs.Context) map[string]string { _, md := scopeFor(ctx, "globex"); return md }},
			{"absent metadata", func(*specs.Context) map[string]string { return nil }},
			{"garbage metadata", func(*specs.Context) map[string]string { return map[string]string{"ego.tenant.scope": "x"} }},
		}

		type kind int
		const (
			events kind = iota
			states
		)
		type registration int
		const (
			perTenant registration = iota // AddXPublishersForTenant on a per-caller-resolver engine
			implicit                      // AddXPublishers on a fixed-tenant engine
		)
		type row struct {
			name string
			kind kind
			reg  registration
			drop []drop
			want int
		}
		var rows []row
		for _, k := range []kind{events, states} {
			for _, reg := range []registration{perTenant, implicit} {
				prefix := map[kind]string{events: "event publisher", states: "state publisher"}[k] + ", " +
					map[registration]string{perTenant: "per-tenant registration", implicit: "fixed-tenant registration"}[reg] + ": "
				for _, d := range drops {
					rows = append(rows, row{prefix + d.name, k, reg, []drop{d}, 1})
				}
				rows = append(rows, row{prefix + "all four reasons in a row are each counted once", k, reg, drops, len(drops)})
			}
		}

		specs.Table(s, rows, func(r row) string { return r.name }, func(ctx *specs.Context, r row) {
			capture := &captureHandler{}
			logger := kitlog.New(kitlog.Config{Sink: capture})
			reader := sdkmetric.NewManualReader()
			meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
			telemetry := WithTelemetry(&Telemetry{Tracer: tracenoop.NewTracerProvider().Tracer("test"), Meter: meter})

			var engine *Engine
			if r.reg == perTenant {
				engine = newIdentityEngine(ctx, "DropObservability", connectedEventsStore(ctx), connectedDurableStore(ctx),
					WithLogger(logger), telemetry)
			} else {
				resolver, err := tenancy.WithSingleTenant("acme")
				ctx.Expect(err).To(specs.BeNil())
				engine = startEngine(ctx, "DropObservability", connectedEventsStore(ctx),
					WithTenantResolver(resolver), WithLogger(logger), telemetry)
			}

			const publisherID = "drop-observer"
			eventPub, statePub := &namedEventPublisher{id: publisherID}, &namedStatePublisher{id: publisherID}
			var got func() []forTenantDelivery
			switch {
			case r.kind == events && r.reg == perTenant:
				ctx.Expect(engine.AddEventPublishersForTenant("acme", eventPub)).To(specs.BeNil())
				got = eventPub.got
			case r.kind == events:
				ctx.Expect(engine.AddEventPublishers(eventPub)).To(specs.BeNil())
				got = eventPub.got
			case r.reg == perTenant:
				ctx.Expect(engine.AddStatePublishersForTenant("acme", statePub)).To(specs.BeNil())
				got = statePub.got
			default:
				ctx.Expect(engine.AddStatePublishers(statePub)).To(specs.BeNil())
				got = statePub.got
			}

			scoped := engine.eventStream.(eventstream.ScopedStream)
			acme, validMetadata := scopeFor(ctx, "acme")
			topic := protocol.EventsTopic
			if r.kind == states {
				topic = protocol.StatesTopic
			}
			publish := func(persistenceID string, metadata map[string]string) {
				var msg any = &egopb.Event{PersistenceId: persistenceID, TenantMetadata: metadata}
				if r.kind == states {
					msg = &egopb.DurableState{PersistenceId: persistenceID, TenantMetadata: metadata}
				}
				ctx.Expect(scoped.PublishScoped(acme, topic, msg)).To(specs.BeNil())
			}

			ctx.Expect(rejectedTotal(ctx, reader)).To(specs.Equal(int64(0)))

			for _, d := range r.drop {
				publish("bad-"+d.name, d.metadata(ctx))
			}
			publish("valid", validMetadata)

			// The valid message arrives last, so every drop before it has been
			// processed: the loop is alive after the drops.
			awaitSeen(ctx, got, 1)

			// (1) zero deliveries of the bad messages: only the valid one arrived.
			ctx.Expect(got()).To(specs.Equal([]forTenantDelivery{{"acme", "acme"}}))

			// (2) the counter rose by exactly the number of drops, not for the valid one.
			ctx.Expect(rejectedTotal(ctx, reader)).To(specs.Equal(int64(r.want)))

			// (3) one error log line per drop, naming publisher, persistence id and scope.
			lines := capture.withMessage(droppedMessage)
			ctx.Expect(lines).To(specs.HaveLen(r.want))
			for i, d := range r.drop {
				ctx.Expect(lines[i].level).To(specs.Equal(slog.LevelError))
				ctx.Expect(lines[i].attrs["publisher"]).To(specs.Equal(publisherID))
				ctx.Expect(lines[i].attrs["persistence_id"]).To(specs.Equal("bad-" + d.name))
				ctx.Expect(lines[i].attrs["scope"]).To(specs.Contain("acme"))
				ctx.Expect(lines[i].attrs["error"]).To(specs.Not(specs.Equal("")))
			}
		})
	})
}
