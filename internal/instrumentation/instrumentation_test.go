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

package instrumentation

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// measurement is one recorded value with its attribute set.
type measurement struct {
	value float64
	attrs attribute.Set
}

// fakeMeter records the instruments created on it and every measurement.
type fakeMeter struct {
	noopmetric.Meter

	mu           sync.Mutex
	catalog      map[string]string
	measurements map[string][]measurement
}

func newFakeMeter() *fakeMeter {
	return &fakeMeter{catalog: map[string]string{}, measurements: map[string][]measurement{}}
}

func (m *fakeMeter) record(name string, value float64, set attribute.Set) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.measurements[name] = append(m.measurements[name], measurement{value: value, attrs: set})
}

func (m *fakeMeter) Int64Counter(name string, opts ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	cfg := metric.NewInt64CounterConfig(opts...)
	m.catalog[name] = "Int64Counter|" + cfg.Description() + "|" + cfg.Unit()
	return fakeInt64{meter: m, name: name}, nil
}

func (m *fakeMeter) Int64UpDownCounter(name string, opts ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	cfg := metric.NewInt64UpDownCounterConfig(opts...)
	m.catalog[name] = "Int64UpDownCounter|" + cfg.Description() + "|" + cfg.Unit()
	return fakeInt64UpDown{meter: m, name: name}, nil
}

func (m *fakeMeter) Int64Gauge(name string, opts ...metric.Int64GaugeOption) (metric.Int64Gauge, error) {
	cfg := metric.NewInt64GaugeConfig(opts...)
	m.catalog[name] = "Int64Gauge|" + cfg.Description() + "|" + cfg.Unit()
	return fakeGauge{meter: m, name: name}, nil
}

func (m *fakeMeter) Float64Histogram(name string, opts ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	cfg := metric.NewFloat64HistogramConfig(opts...)
	m.catalog[name] = "Float64Histogram|" + cfg.Description() + "|" + cfg.Unit()
	return fakeHistogram{meter: m, name: name}, nil
}

type fakeInt64 struct {
	noopmetric.Int64Counter
	meter *fakeMeter
	name  string
}

func (c fakeInt64) Add(_ context.Context, v int64, opts ...metric.AddOption) {
	c.meter.record(c.name, float64(v), metric.NewAddConfig(opts).Attributes())
}

type fakeInt64UpDown struct {
	noopmetric.Int64UpDownCounter
	meter *fakeMeter
	name  string
}

func (c fakeInt64UpDown) Add(_ context.Context, v int64, opts ...metric.AddOption) {
	c.meter.record(c.name, float64(v), metric.NewAddConfig(opts).Attributes())
}

type fakeGauge struct {
	noopmetric.Int64Gauge
	meter *fakeMeter
	name  string
}

func (g fakeGauge) Record(_ context.Context, v int64, opts ...metric.RecordOption) {
	g.meter.record(g.name, float64(v), metric.NewRecordConfig(opts).Attributes())
}

type fakeHistogram struct {
	noopmetric.Float64Histogram
	meter *fakeMeter
	name  string
}

func (h fakeHistogram) Record(_ context.Context, v float64, opts ...metric.RecordOption) {
	h.meter.record(h.name, v, metric.NewRecordConfig(opts).Attributes())
}

func newTracer(t testing.TB) (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return exporter, provider
}

// measurementValue and measurementAttributeCount project one recorded measurement for the matchers.
func measurementValue(m measurement) float64 { return m.value }

func measurementAttributeCount(m measurement) int { return m.attrs.Len() }

// spansNamed returns the exported spans carrying name.
func spansNamed(exporter *tracetest.InMemoryExporter, name string) tracetest.SpanStubs {
	var out tracetest.SpanStubs
	for _, span := range exporter.GetSpans() {
		if span.Name == name {
			out = append(out, span)
		}
	}
	return out
}

// recovered runs fn and returns the value it panicked with, or nil.
func recovered(fn func()) (value any) {
	defer func() { value = recover() }()
	fn()
	return nil
}

func TestNewWithoutMeterDisablesMetrics(t *testing.T) {
	specs.Describe(t, "New without a meter disables metrics", func(s *specs.Spec) {
		s.It("returns nil instruments", func(ctx *specs.Context) {
			ctx.Expect(New(nil)).To(specs.BeNil())
		})
	})
}

func TestNewCreatesTheCatalog(t *testing.T) {
	specs.Describe(t, "New registers the full instrument catalog on the meter", func(s *specs.Spec) {
		s.It("creates every instrument with its kind, description and unit", func(ctx *specs.Context) {
			meter := newFakeMeter()
			ctx.Expect(New(meter)).To(specs.Not(specs.BeNil()))

			ctx.Expect(meter.catalog).ToEqual(map[string]string{
				"urd.commands.total":                    "Int64Counter|Total number of commands processed|",
				"urd.commands.duration":                 "Float64Histogram|Duration of command processing in milliseconds|",
				"urd.events.persisted.total":            "Int64Counter|Total number of events persisted|",
				"urd.projection.events.processed.total": "Int64Counter|Total number of events processed by projections|",
				"urd.entities.active":                   "Int64UpDownCounter|Number of currently active entities|",
				"urd.projections.active":                "Int64UpDownCounter|Number of currently active projections|",
				"urd.projection.lag_ms":                 "Int64Gauge|Projection lag in milliseconds per shard|",
				"urd.projection.latest_offset":          "Int64Gauge|Current projection offset timestamp per shard|",
				"urd.projection.events_behind":          "Int64Gauge|Approximate number of unprocessed events per shard|",
				"urd.publication.rejected.total":        "Int64Counter|Total number of publications dropped because their tenant identity was absent, invalid or mismatched|",
			})
		})
	})
}

func TestNilInstrumentsRecordNothing(t *testing.T) {
	specs.Describe(t, "nil Instruments accept every recording call without panicking", func(s *specs.Spec) {
		s.It("records nothing and does not panic", func(ctx *specs.Context) {
			base := context.Background()
			var instruments *Instruments

			panicked := recovered(func() {
				instruments.CommandReceived(base)
				instruments.CommandCompleted(base, time.Now())
				instruments.EventsPersisted(base, 3)
				instruments.EntityStarted(base)
				instruments.EntityStopped(base)
				instruments.ProjectionStarted(base)
				instruments.ProjectionStopped(base)
				instruments.ProjectionEventHandled(base)
				instruments.Shard("orders", 7).Record(base, 1, 2, 3)
			})
			ctx.Expect(panicked).To(specs.BeNil())
		})
	})
}

func TestRecordingMethods(t *testing.T) {
	specs.Describe(t, "recording methods emit one measurement per call on the matching instrument", func(s *specs.Spec) {
		s.It("records counters, the duration histogram and the up-down counters without attributes", func(ctx *specs.Context) {
			base := context.Background()
			meter := newFakeMeter()
			instruments := New(meter)

			instruments.CommandReceived(base)
			instruments.CommandCompleted(base, time.Now().Add(-1500*time.Millisecond))
			instruments.EventsPersisted(base, 3)
			instruments.EntityStarted(base)
			instruments.EntityStopped(base)
			instruments.ProjectionStarted(base)
			instruments.ProjectionStopped(base)
			instruments.ProjectionEventHandled(base)

			commands := meter.measurements["urd.commands.total"]
			duration := meter.measurements["urd.commands.duration"]
			persisted := meter.measurements["urd.events.persisted.total"]
			entities := meter.measurements["urd.entities.active"]
			projections := meter.measurements["urd.projections.active"]
			processed := meter.measurements["urd.projection.events.processed.total"]

			// Every instrument carries no attributes: the failure names the offending measurement.
			noAttributes := specs.EveryElement(specs.Project("attributes", measurementAttributeCount, specs.Equal(0)))
			for _, recorded := range [][]measurement{commands, duration, persisted, entities, projections, processed} {
				ctx.Expect(recorded).To(noAttributes)
			}

			valueOf := func(want ...float64) specs.Matcher {
				ms := make([]specs.Matcher, len(want))
				for i, w := range want {
					ms[i] = specs.Project("value", measurementValue, specs.Equal(w))
				}
				return specs.HaveElementsInOrder(ms...)
			}
			ctx.Expect(commands).To(valueOf(1))
			ctx.Expect(duration).To(specs.HaveLen(1))
			// Duration is recorded in whole milliseconds, at least the elapsed 1500.
			ctx.Expect(duration[0].value).To(specs.BeGreaterThanOrEqual(float64(1500)))
			// Duration is truncated to whole milliseconds.
			ctx.Expect(duration[0].value).ToEqual(float64(int64(duration[0].value)))
			ctx.Expect(persisted).To(valueOf(3))
			ctx.Expect(entities).To(valueOf(1, -1))
			ctx.Expect(projections).To(valueOf(1, -1))
			ctx.Expect(processed).To(valueOf(1))
		})
	})
}

func TestPublicationRejectedCountsOne(t *testing.T) {
	specs.Describe(t, "PublicationRejected counts one dropped publication, and is a no-op on nil instruments", func(s *specs.Spec) {
		s.It("records one measurement", func(ctx *specs.Context) {
			meter := newFakeMeter()
			New(meter).PublicationRejected(context.Background())
			ctx.Expect(meter.measurements["urd.publication.rejected.total"]).To(specs.HaveLen(1))
			ctx.Expect(meter.measurements["urd.publication.rejected.total"][0].value).To(specs.Equal(float64(1)))
		})

		s.It("does nothing without instruments", func(ctx *specs.Context) {
			var none *Instruments
			none.PublicationRejected(context.Background())
		})
	})
}

func TestShardRecordsTheGaugesWithProjectionAttributes(t *testing.T) {
	specs.Describe(t, "a shard recorder writes each gauge with the projection and shard attributes", func(s *specs.Spec) {
		want := attribute.NewSet(
			attribute.String("projection_name", "orders"),
			attribute.Int64("shard", 7),
		)
		type gauge struct {
			name  string
			value float64
		}
		specs.Table(s, []gauge{
			{"urd.projection.lag_ms", 11},
			{"urd.projection.latest_offset", 22},
			{"urd.projection.events_behind", 33},
		}, func(g gauge) string { return g.name }, func(ctx *specs.Context, g gauge) {
			meter := newFakeMeter()
			New(meter).Shard("orders", 7).Record(context.Background(), 11, 22, 33)

			ctx.Expect(meter.measurements[g.name]).To(specs.HaveElementsInOrder(specs.All(
				specs.Project("value", measurementValue, specs.Equal(g.value)),
				specs.Project("attributes", func(m measurement) attribute.Set { return m.attrs }, specs.Equal(want)),
			)))
		})
	})
}

func TestStartCommandSpan(t *testing.T) {
	specs.Describe(t, "StartCommandSpan starts the command span only when a tracer is present", func(s *specs.Spec) {
		s.It("without a tracer the context is unchanged and there is no span", func(ctx *specs.Context) {
			base := context.Background()
			got, span := StartCommandSpan(base, nil, "pid", nil)
			ctx.Expect(got).ToEqual(base)
			ctx.Expect(span).To(specs.BeNil())
		})

		s.It("with a tracer the span is a child carrying the command attributes", func(ctx *specs.Context) {
			exporter, provider := newTracer(ctx.T)
			tracer := provider.Tracer("test")
			parentCtx, parent := tracer.Start(context.Background(), "parent")

			spanCtx, span := StartCommandSpan(parentCtx, tracer, "pid-1", wrapperspb.String("x"))
			ctx.Expect(span).To(specs.Not(specs.BeNil()))
			ctx.Expect(span.SpanContext()).ToEqual(trace.SpanContextFromContext(spanCtx))
			span.End()
			parent.End()

			commandSpans := spansNamed(exporter, "urd.command")
			ctx.Expect(commandSpans).To(specs.HaveLen(1))
			stub := commandSpans[0]
			ctx.Expect(stub.Parent.SpanID()).ToEqual(parent.SpanContext().SpanID())
			ctx.Expect(stub.Attributes).ToEqual([]attribute.KeyValue{
				attribute.String("urd.persistence_id", "pid-1"),
				attribute.String("urd.command_type", "google.protobuf.StringValue"),
			})
		})
	})
}

func TestSendCommandSpan(t *testing.T) {
	specs.Describe(t, "the send-command span records success and failure", func(s *specs.Spec) {
		s.It("ends one span cleanly and one with the error status and exception event", func(ctx *specs.Context) {
			exporter, provider := newTracer(ctx.T)
			tracer := provider.Tracer("test")

			_, ok := StartSendCommandSpan(context.Background(), tracer, "entity-1", wrapperspb.String("x"))
			EndSendCommandSpan(ok, nil)

			_, failed := StartSendCommandSpan(context.Background(), tracer, "entity-2", wrapperspb.String("x"))
			EndSendCommandSpan(failed, errors.New("boom"))

			// The syncer exports spans in the order they end: the clean one first, then the failed one.
			spans := exporter.GetSpans()
			ctx.Expect(spans).To(specs.HaveLen(2))

			ctx.Expect(spans[0].Name).ToEqual("urd.send_command")
			ctx.Expect(spans[0].Status.Code).ToEqual(codes.Unset)
			ctx.Expect(spans[0].Events).To(specs.BeEmpty())
			ctx.Expect(spans[0].Attributes).ToEqual([]attribute.KeyValue{
				attribute.String("urd.entity_id", "entity-1"),
				attribute.String("urd.command_type", "google.protobuf.StringValue"),
			})

			ctx.Expect(spans[1].Status.Code).ToEqual(codes.Error)
			ctx.Expect(spans[1].Status.Description).ToEqual("boom")
			ctx.Expect(spans[1].Events).To(specs.HaveLen(1))
			ctx.Expect(spans[1].Events[0].Name).ToEqual("exception")
		})
	})
}

func TestInstallPropagator(t *testing.T) {
	specs.Describe(t, "InstallPropagator installs the trace-context and baggage propagator", func(s *specs.Spec) {
		s.It("exposes the traceparent, tracestate and baggage fields", func(ctx *specs.Context) {
			previous := otel.GetTextMapPropagator()
			ctx.Cleanup(func() { otel.SetTextMapPropagator(previous) })
			otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

			InstallPropagator()

			ctx.Expect(otel.GetTextMapPropagator().Fields()).
				To(specs.ContainTheSameElementsAs([]string{"baggage", "traceparent", "tracestate"}))
		})
	})
}

// TestArchitectureInstrumentationStaysRuntimeNeutral guards the package boundary: the
// telemetry contract must never pull in the engine, the GoAkt adapter's
// internals or GoAkt itself. the former architecture checker only sees direct imports, so this
// asserts the transitive closure via go list -deps.
func TestArchitectureInstrumentationStaysRuntimeNeutral(t *testing.T) {
	specs.Describe(t, "the import graph of internal/instrumentation", func(s *specs.Spec) {
		s.It("reaches neither GoAkt, the engine nor the GoAkt adapter internals", func(ctx *specs.Context) {
			goBin, err := exec.LookPath("go")
			if err != nil {
				ctx.T.Skip("the go tool is not on PATH")
			}

			out, err := exec.Command(goBin, "list", "-deps", ".").CombinedOutput()
			var listErr error
			if err != nil {
				listErr = fmt.Errorf("go list -deps failed: %w\n%s", err, out)
			}
			ctx.Expect(listErr).To(specs.BeNil())

			deps := strings.Fields(string(out))
			// The guard first: an empty or truncated graph would prove nothing.
			ctx.Expect(deps).To(specs.Contain("github.com/getsyntegrity/urd/internal/instrumentation"))
			ctx.Expect(deps).To(specs.NoElement(specs.Satisfy(
				"a GoAkt package: internal/instrumentation must not depend on GoAkt",
				func(dep any) bool { return strings.HasPrefix(dep.(string), "github.com/tochemey/goakt") })))
			ctx.Expect(deps).To(specs.NoElement(specs.Satisfy(
				"the engine package: internal/instrumentation must not depend on it",
				func(dep any) bool { return strings.HasSuffix(dep.(string), "/urd/engine") })))
			ctx.Expect(deps).To(specs.NoElement(specs.Satisfy(
				"the GoAkt adapter internals (internal/extensions): internal/instrumentation must not depend on them",
				func(dep any) bool { return strings.HasSuffix(dep.(string), "/internal/extensions") })))
		})
	})
}
