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
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/projection"
)

// telemetryDumpEnv names an optional file the contract test writes its
// normalized observation to, so two revisions can be diffed byte for byte.
const telemetryDumpEnv = "URD_TELEMETRY_CONTRACT_DUMP"

// legacyTelemetryDumpEnv is the deprecated former name of telemetryDumpEnv.
// It is still read when the new name is unset.
const legacyTelemetryDumpEnv = "EGO_TELEMETRY_CONTRACT_DUMP"

// telemetryDumpPath returns the dump file path, preferring the URD_ variable
// and falling back to the deprecated EGO_ one. legacy reports the fallback.
func telemetryDumpPath() (path string, legacy bool) {
	if path = os.Getenv(telemetryDumpEnv); path != "" {
		return path, false
	}
	path = os.Getenv(legacyTelemetryDumpEnv)
	return path, path != ""
}

// recordingMeter wraps the no-op meter and records every instrument the
// engine creates and every measurement it takes. It only overrides the
// instrument kinds the engine uses.
type recordingMeter struct {
	noopmetric.Meter

	mu           sync.Mutex
	instruments  map[string]string // name -> "kind|description|unit"
	creations    map[string]int    // name -> number of creation calls
	measurements map[string]map[string]int
}

func newRecordingMeter() *recordingMeter {
	return &recordingMeter{
		instruments:  make(map[string]string),
		creations:    make(map[string]int),
		measurements: make(map[string]map[string]int),
	}
}

func (m *recordingMeter) created(name, kind, description, unit string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.instruments[name] = kind + "|" + description + "|" + unit
	m.creations[name]++
}

func (m *recordingMeter) measured(name string, set attribute.Set) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, set.Len())
	for _, kv := range set.ToSlice() {
		keys = append(keys, string(kv.Key))
	}
	sort.Strings(keys)
	if m.measurements[name] == nil {
		m.measurements[name] = make(map[string]int)
	}
	m.measurements[name]["{"+strings.Join(keys, ",")+"}"]++
}

func (m *recordingMeter) count(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, n := range m.measurements[name] {
		total += n
	}
	return total
}

func (m *recordingMeter) Int64Counter(name string, opts ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	cfg := metric.NewInt64CounterConfig(opts...)
	m.created(name, "Int64Counter", cfg.Description(), cfg.Unit())
	return &recInt64Counter{meter: m, name: name}, nil
}

func (m *recordingMeter) Int64UpDownCounter(name string, opts ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	cfg := metric.NewInt64UpDownCounterConfig(opts...)
	m.created(name, "Int64UpDownCounter", cfg.Description(), cfg.Unit())
	return &recInt64UpDownCounter{meter: m, name: name}, nil
}

func (m *recordingMeter) Int64Gauge(name string, opts ...metric.Int64GaugeOption) (metric.Int64Gauge, error) {
	cfg := metric.NewInt64GaugeConfig(opts...)
	m.created(name, "Int64Gauge", cfg.Description(), cfg.Unit())
	return &recInt64Gauge{meter: m, name: name}, nil
}

func (m *recordingMeter) Float64Histogram(name string, opts ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	cfg := metric.NewFloat64HistogramConfig(opts...)
	m.created(name, "Float64Histogram", cfg.Description(), cfg.Unit())
	return &recFloat64Histogram{meter: m, name: name}, nil
}

type recInt64Counter struct {
	noopmetric.Int64Counter
	meter *recordingMeter
	name  string
}

func (c *recInt64Counter) Add(_ context.Context, _ int64, opts ...metric.AddOption) {
	c.meter.measured(c.name, metric.NewAddConfig(opts).Attributes())
}

type recInt64UpDownCounter struct {
	noopmetric.Int64UpDownCounter
	meter *recordingMeter
	name  string
}

func (c *recInt64UpDownCounter) Add(_ context.Context, _ int64, opts ...metric.AddOption) {
	c.meter.measured(c.name, metric.NewAddConfig(opts).Attributes())
}

type recInt64Gauge struct {
	noopmetric.Int64Gauge
	meter *recordingMeter
	name  string
}

func (g *recInt64Gauge) Record(_ context.Context, _ int64, opts ...metric.RecordOption) {
	g.meter.measured(g.name, metric.NewRecordConfig(opts).Attributes())
}

type recFloat64Histogram struct {
	noopmetric.Float64Histogram
	meter *recordingMeter
	name  string
}

func (h *recFloat64Histogram) Record(_ context.Context, _ float64, opts ...metric.RecordOption) {
	h.meter.measured(h.name, metric.NewRecordConfig(opts).Attributes())
}

// telemetryObservation is the normalized, order-independent view of what the
// engine emitted. Values that vary run to run (IDs, timings, counts) are left
// out; the shape of every signal is kept.
type telemetryObservation struct {
	Spans        []string            `json:"spans"`
	Instruments  map[string]string   `json:"instruments"`
	Creations    map[string]int      `json:"creations"`
	Measurements map[string][]string `json:"measurements"`
	Propagator   []string            `json:"propagator"`
}

func describeSpan(span tracetest.SpanStub, names map[string]string) string {
	keys := make([]string, 0, len(span.Attributes))
	for _, kv := range span.Attributes {
		switch kv.Key {
		case "urd.command_type":
			keys = append(keys, string(kv.Key)+"="+kv.Value.AsString())
		default:
			keys = append(keys, string(kv.Key))
		}
	}
	sort.Strings(keys)

	events := make([]string, 0, len(span.Events))
	for _, event := range span.Events {
		events = append(events, event.Name)
	}

	parent := "<root>"
	if span.Parent.IsValid() {
		parent = names[span.Parent.SpanID().String()]
		if parent == "" {
			parent = "<unexported>"
		}
	}

	return fmt.Sprintf("%s kind=%s parent=%s attrs=[%s] status=%s events=[%s]",
		span.Name, span.SpanKind, parent, strings.Join(keys, ","),
		span.Status.Code, strings.Join(events, ","))
}

// TestTelemetryContract pins the observable telemetry of commands and
// projections: span names, attributes, parentage and error status, the
// instrument catalog (name, kind, description, unit) with the attribute keys
// of every measurement, and the global propagator the engine installs.
func TestTelemetryContract(t *testing.T) {
	specs.Describe(t, "the engine emits the pinned telemetry for commands and projections", func(s *specs.Spec) {
		s.It("keeps spans, instruments, measurements and the propagator unchanged", func(ctx *specs.Context) {
			bg := context.Background()

			previous := otel.GetTextMapPropagator()
			ctx.Cleanup(func() { otel.SetTextMapPropagator(previous) })
			otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(
				sdktrace.WithSyncer(exporter),
				sdktrace.WithSampler(sdktrace.AlwaysSample()),
			)
			ctx.Cleanup(func() { _ = provider.Shutdown(bg) })
			tracer := provider.Tracer("urd-contract")
			meter := newRecordingMeter()

			engine := newTestEngine(ctx.T, "Sample", connectedEventsStore(ctx),
				WithLogger(DiscardLogger),
				WithOffsetStore(connectedOffsetStore(ctx)),
				WithStateStore(connectedDurableStore(ctx)),
				WithTelemetry(&Telemetry{Tracer: tracer, Meter: meter}),
				WithProjection("discard", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   100,
					PullInterval: 50 * time.Millisecond,
				}),
			)
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())

			propagator := otel.GetTextMapPropagator().Fields()
			sort.Strings(propagator)

			parentCtx, parent := tracer.Start(bg, "test.parent")

			eventSourcedID := uuid.NewString()
			ctx.Expect(engine.Entity(bg, NewEventSourcedEntity(eventSourcedID))).To(specs.BeNil())
			_, _, err := engine.SendCommand(parentCtx, eventSourcedID, &testpb.CreateAccount{AccountBalance: 42}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())

			durableID := uuid.NewString()
			ctx.Expect(engine.DurableStateEntity(bg, NewAccountDurableStateBehavior(durableID))).To(specs.BeNil())
			_, _, err = engine.SendCommand(parentCtx, durableID, &testpb.CreateAccount{AccountBalance: 7}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())

			_, _, err = engine.SendCommand(parentCtx, "missing-"+uuid.NewString(), &testpb.CreateAccount{AccountBalance: 1}, 10*time.Millisecond)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			parent.End()

			ctx.Expect(engine.StartProjection(bg, "discard")).To(specs.BeNil())
			// the projection should handle the persisted event
			ctx.Eventually(func() any { return meter.count("urd.projection.events.processed.total") },
				specs.BeGreaterThan(0), specs.WithTimeout(waitTimeout), specs.WithInterval(20*time.Millisecond))
			// the projection should record shard gauges
			ctx.Eventually(func() any { return meter.count("urd.projection.lag_ms") },
				specs.BeGreaterThan(1), specs.WithTimeout(waitTimeout), specs.WithInterval(20*time.Millisecond))
			ctx.Expect(engine.StopProjection(bg, "discard")).To(specs.BeNil())
			ctx.Expect(engine.Stop(bg)).To(specs.BeNil())
			ctx.Expect(provider.ForceFlush(bg)).To(specs.BeNil())

			spans := exporter.GetSpans()
			names := make(map[string]string, len(spans))
			for _, span := range spans {
				names[span.SpanContext.SpanID().String()] = span.Name
			}
			seen := make(map[string]int)
			for _, span := range spans {
				seen[describeSpan(span, names)]++
			}

			meter.mu.Lock()
			observation := telemetryObservation{
				Instruments:  meter.instruments,
				Creations:    meter.creations,
				Measurements: make(map[string][]string),
				Propagator:   propagator,
			}
			for name, sets := range meter.measurements {
				for set := range sets {
					observation.Measurements[name] = append(observation.Measurements[name], set)
				}
				sort.Strings(observation.Measurements[name])
			}
			meter.mu.Unlock()
			for span, n := range seen {
				observation.Spans = append(observation.Spans, fmt.Sprintf("%dx %s", n, span))
			}
			sort.Strings(observation.Spans)

			if path, legacy := telemetryDumpPath(); path != "" {
				if legacy {
					t.Logf("deprecated: %s is set; use %s instead", legacyTelemetryDumpEnv, telemetryDumpEnv)
				}
				raw, err := json.MarshalIndent(observation, "", "  ")
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(os.WriteFile(path, raw, 0o600)).To(specs.BeNil())
			}

			const command = "urd.command_type=testpb.CreateAccount"
			ctx.Expect(observation.Spans).ToEqual([]string{
				"1x test.parent kind=internal parent=<root> attrs=[] status=Unset events=[]",
				"1x urd.send_command kind=internal parent=test.parent attrs=[" + command + ",urd.entity_id] status=Error events=[exception]",
				"2x urd.command kind=internal parent=urd.send_command attrs=[" + command + ",urd.persistence_id] status=Unset events=[]",
				"2x urd.send_command kind=internal parent=test.parent attrs=[" + command + ",urd.entity_id] status=Unset events=[]",
			})

			ctx.Expect(observation.Instruments).ToEqual(map[string]string{
				"urd.commands.total":                    "Int64Counter|Total number of commands processed|",
				"urd.commands.duration":                 "Float64Histogram|Duration of command processing in milliseconds|",
				"urd.events.persisted.total":            "Int64Counter|Total number of events persisted|",
				"urd.projection.events.processed.total": "Int64Counter|Total number of events processed by projections|",
				"urd.entities.active":                   "Int64UpDownCounter|Number of currently active entities|",
				"urd.projections.active":                "Int64UpDownCounter|Number of currently active projections|",
				"urd.projection.lag_ms":                 "Int64Gauge|Projection lag in milliseconds per shard|",
				"urd.projection.latest_offset":          "Int64Gauge|Current projection offset timestamp per shard|",
				"urd.projection.events_behind":          "Int64Gauge|Approximate number of unprocessed events per shard|",
				// EGO-TENANT-005: additive, recorded only when a publication is dropped.
				"urd.publication.rejected.total": "Int64Counter|Total number of publications dropped because their tenant identity was absent, invalid or mismatched|",
			})

			shard := []string{"{projection_name,shard}"}
			none := []string{"{}"}
			ctx.Expect(observation.Measurements).ToEqual(map[string][]string{
				"urd.commands.total":                    none,
				"urd.commands.duration":                 none,
				"urd.events.persisted.total":            none,
				"urd.projection.events.processed.total": none,
				"urd.entities.active":                   none,
				"urd.projections.active":                none,
				"urd.projection.lag_ms":                 shard,
				"urd.projection.latest_offset":          shard,
				"urd.projection.events_behind":          shard,
			})

			// One instrument set per Engine.Start, per entity actor and per
			// projection actor: construction happens where it always has. The
			// whole map is compared, so a failure names the instrument.
			wantCreations := make(map[string]int, len(observation.Creations))
			for name := range observation.Creations {
				wantCreations[name] = 4
			}
			ctx.Expect(observation.Creations).ToEqual(wantCreations)

			ctx.Expect(observation.Propagator).ToEqual([]string{"baggage", "traceparent", "tracestate"})
		})
	})
}

// TestTelemetryDisabled pins the behavior without WithTelemetry: commands
// and projections run, and the engine leaves the global propagator alone.
func TestTelemetryDisabled(t *testing.T) {
	specs.Describe(t, "an engine built without WithTelemetry leaves telemetry and the global propagator alone", func(s *specs.Spec) {
		s.It("runs commands and projections and installs no propagator", func(ctx *specs.Context) {
			bg := context.Background()

			previous := otel.GetTextMapPropagator()
			ctx.Cleanup(func() { otel.SetTextMapPropagator(previous) })
			otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

			engine := newTestEngine(ctx.T, "Sample", connectedEventsStore(ctx),
				WithLogger(DiscardLogger),
				WithOffsetStore(connectedOffsetStore(ctx)),
				WithProjection("discard", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   100,
					PullInterval: 50 * time.Millisecond,
				}),
			)
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())
			ctx.Expect(engine.metrics == nil).To(specs.BeTrue())

			entityID := uuid.NewString()
			ctx.Expect(engine.Entity(bg, NewEventSourcedEntity(entityID))).To(specs.BeNil())
			_, _, err := engine.SendCommand(bg, entityID, &testpb.CreateAccount{AccountBalance: 42}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			_, _, err = engine.SendCommand(bg, "missing-"+uuid.NewString(), &testpb.CreateAccount{AccountBalance: 1}, 10*time.Millisecond)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))

			ctx.Expect(engine.StartProjection(bg, "discard")).To(specs.BeNil())
			// the projection should run without telemetry
			ctx.Eventually(projectionRunning(bg, engine, "discard"), specs.BeTrue(),
				specs.WithTimeout(waitTimeout), specs.WithInterval(20*time.Millisecond))
			ctx.Expect(engine.StopProjection(bg, "discard")).To(specs.BeNil())
			ctx.Expect(engine.Stop(bg)).To(specs.BeNil())

			// the engine must not install a propagator without telemetry
			ctx.Expect(otel.GetTextMapPropagator().Fields()).To(specs.BeEmpty())
		})
	})
}
