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

// Package instrumentation owns Urd's OpenTelemetry contract: the metric
// instruments (names, kinds, descriptions), the span names and attribute
// keys, and the global propagator the engine installs. It builds the
// instruments from a configured meter and records operations on them.
//
// A nil *Instruments is valid and records nothing, which is how the engine
// runs when no meter is configured. The package is runtime-neutral: it never
// imports the engine, the GoAkt adapter's internals or GoAkt.
package instrumentation

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
)

// Instruments holds the pre-created metric instruments so the hot path never
// allocates one.
type Instruments struct {
	commandsTotal     metric.Int64Counter
	commandsDuration  metric.Float64Histogram
	eventsPersisted   metric.Int64Counter
	projectionHandled metric.Int64Counter
	entitiesActive    metric.Int64UpDownCounter
	projectionsActive metric.Int64UpDownCounter
	projectionLag     metric.Int64Gauge
	projectionOffset  metric.Int64Gauge
	projectionBehind  metric.Int64Gauge
	publicationReject metric.Int64Counter
}

// New creates the metric instruments from the given meter.
// Returns nil if meter is nil.
func New(meter metric.Meter) *Instruments {
	if meter == nil {
		return nil
	}

	commandsTotal, _ := meter.Int64Counter("urd.commands.total",
		metric.WithDescription("Total number of commands processed"),
	)

	commandsDuration, _ := meter.Float64Histogram("urd.commands.duration",
		metric.WithDescription("Duration of command processing in milliseconds"),
	)

	eventsPersisted, _ := meter.Int64Counter("urd.events.persisted.total",
		metric.WithDescription("Total number of events persisted"),
	)

	projectionHandled, _ := meter.Int64Counter("urd.projection.events.processed.total",
		metric.WithDescription("Total number of events processed by projections"),
	)

	entitiesActive, _ := meter.Int64UpDownCounter("urd.entities.active",
		metric.WithDescription("Number of currently active entities"),
	)

	projectionsActive, _ := meter.Int64UpDownCounter("urd.projections.active",
		metric.WithDescription("Number of currently active projections"),
	)

	projectionLag, _ := meter.Int64Gauge("urd.projection.lag_ms",
		metric.WithDescription("Projection lag in milliseconds per shard"),
	)

	projectionOffset, _ := meter.Int64Gauge("urd.projection.latest_offset",
		metric.WithDescription("Current projection offset timestamp per shard"),
	)

	projectionBehind, _ := meter.Int64Gauge("urd.projection.events_behind",
		metric.WithDescription("Approximate number of unprocessed events per shard"),
	)

	publicationReject, _ := meter.Int64Counter("urd.publication.rejected.total",
		metric.WithDescription("Total number of publications dropped because their tenant identity was absent, invalid or mismatched"),
	)

	return &Instruments{
		publicationReject: publicationReject,
		commandsTotal:     commandsTotal,
		commandsDuration:  commandsDuration,
		eventsPersisted:   eventsPersisted,
		projectionHandled: projectionHandled,
		entitiesActive:    entitiesActive,
		projectionsActive: projectionsActive,
		projectionLag:     projectionLag,
		projectionOffset:  projectionOffset,
		projectionBehind:  projectionBehind,
	}
}

// CommandReceived counts one command entering an entity.
func (x *Instruments) CommandReceived(ctx context.Context) {
	if x == nil {
		return
	}
	x.commandsTotal.Add(ctx, 1)
}

// CommandCompleted records the command duration since startTime, in whole
// milliseconds.
func (x *Instruments) CommandCompleted(ctx context.Context, startTime time.Time) {
	if x == nil {
		return
	}
	duration := float64(time.Since(startTime).Milliseconds())
	x.commandsDuration.Record(ctx, duration)
}

// EventsPersisted counts events confirmed by the events store.
func (x *Instruments) EventsPersisted(ctx context.Context, count int) {
	if x == nil {
		return
	}
	x.eventsPersisted.Add(ctx, int64(count))
}

// PublicationRejected counts one event or state dropped by the tenant
// isolation checks of the publication path (EGO-TENANT-005).
func (x *Instruments) PublicationRejected(ctx context.Context) {
	if x == nil {
		return
	}
	x.publicationReject.Add(ctx, 1)
}

// EntityStarted increments the number of active entities.
func (x *Instruments) EntityStarted(ctx context.Context) {
	if x == nil {
		return
	}
	x.entitiesActive.Add(ctx, 1)
}

// EntityStopped decrements the number of active entities.
func (x *Instruments) EntityStopped(ctx context.Context) {
	if x == nil {
		return
	}
	x.entitiesActive.Add(ctx, -1)
}

// ProjectionStarted increments the number of active projections.
func (x *Instruments) ProjectionStarted(ctx context.Context) {
	if x == nil {
		return
	}
	x.projectionsActive.Add(ctx, 1)
}

// ProjectionStopped decrements the number of active projections.
func (x *Instruments) ProjectionStopped(ctx context.Context) {
	if x == nil {
		return
	}
	x.projectionsActive.Add(ctx, -1)
}

// ProjectionEventHandled counts one event handled by a projection.
func (x *Instruments) ProjectionEventHandled(ctx context.Context) {
	if x == nil {
		return
	}
	x.projectionHandled.Add(ctx, 1)
}

// ShardGauges records the per-shard projection gauges under one attribute
// set, built once per pull.
type ShardGauges struct {
	instruments *Instruments
	attrs       metric.MeasurementOption
}

// Shard returns the gauges of one projection shard. On nil Instruments the
// attribute set is never built and Record does nothing.
func (x *Instruments) Shard(projectionName string, shard uint64) ShardGauges {
	if x == nil {
		return ShardGauges{}
	}
	return ShardGauges{
		instruments: x,
		attrs: metric.WithAttributeSet(attribute.NewSet(
			attribute.String("projection_name", projectionName),
			attribute.Int64("shard", int64(shard)),
		)),
	}
}

// Record sets the shard's lag in milliseconds, its latest offset and the
// approximate number of events behind.
func (x ShardGauges) Record(ctx context.Context, lagMs, offset, behind int64) {
	if x.instruments == nil {
		return
	}
	x.instruments.projectionLag.Record(ctx, lagMs, x.attrs)
	x.instruments.projectionOffset.Record(ctx, offset, x.attrs)
	x.instruments.projectionBehind.Record(ctx, behind, x.attrs)
}

// StartCommandSpan starts the "urd.command" span an entity opens while it
// handles a command. With a nil tracer it returns ctx unchanged and a nil
// span, and never inspects command.
func StartCommandSpan(ctx context.Context, tracer trace.Tracer, persistenceID string, command proto.Message) (context.Context, trace.Span) {
	if tracer == nil {
		return ctx, nil
	}
	return tracer.Start(ctx, "urd.command",
		trace.WithAttributes(
			attribute.String("urd.persistence_id", persistenceID),
			attribute.String("urd.command_type", string(command.ProtoReflect().Descriptor().FullName())),
		))
}

// StartSendCommandSpan starts the "urd.send_command" span that links the
// caller's context to the command dispatch. With a nil tracer it returns ctx
// unchanged and a nil span.
func StartSendCommandSpan(ctx context.Context, tracer trace.Tracer, entityID string, command proto.Message) (context.Context, trace.Span) {
	if tracer == nil {
		return ctx, nil
	}
	return tracer.Start(ctx, "urd.send_command",
		trace.WithAttributes(
			attribute.String("urd.entity_id", entityID),
			attribute.String("urd.command_type", string(command.ProtoReflect().Descriptor().FullName())),
		))
}

// EndSendCommandSpan records err on span, when set, and ends it. A nil span
// is ignored.
func EndSendCommandSpan(span trace.Span, err error) {
	if span == nil {
		return
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// InstallPropagator sets the global OTel text map propagator (W3C trace
// context and baggage) so trace context crosses process boundaries: HTTP
// headers, gRPC metadata and GoAkt remote calls. Without it every service
// or node starts a new root trace.
func InstallPropagator() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}
