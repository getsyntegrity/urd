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
	"fmt"
	"time"

	kitlog "github.com/pablogore/kit-logger/pkg/logger"
	goakt "github.com/tochemey/goakt/v4/actor"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/goaktlog"
	"github.com/getsyntegrity/urd/internal/instrumentation"
	"github.com/getsyntegrity/urd/persistence"
)

// persistEventsRequest is sent from the entity actor to the events writer, through askEventsWriter, to
// persist a batch of event envelopes and publish them to the event stream.
//
// scope carries the owning entity actor's bound persistence.Scope
// (TENANT-003 T4). The writer is a separate child actor with no PreStart
// access to the parent's dependencies, so the scope must travel on this
// request rather than be re-derived here.
type persistEventsRequest struct {
	envelopes    []*egopb.Event
	topic        string
	precondition persistence.WritePrecondition
	scope        persistence.Scope
}

// persistEventsResponse is sent from the events writer back to the entity
// actor after an attempt to persist events. A nil Err indicates success; a
// non-nil Err carries the store write failure, or the transport failure
// askEventsWriter observed, so the parent can decide whether to stop itself.
type persistEventsResponse struct {
	Err error
}

// eventsWriterActor persists events to the events store and publishes them to the event
// stream. Events are published only after the store write succeeds, ensuring
// that downstream consumers never observe events that failed to persist.
//
// This actor is spawned as a child of the entity actor. It receives persistEventsRequest
// messages via askEventsWriter and replies with a persistEventsResponse indicating success or failure.
type eventsWriterActor struct {
	eventsStore  persistence.EventsStore
	eventsStream eventstream.Stream
	logger       kitlog.Logger
	metrics      *instrumentation.Instruments
}

var _ goakt.Actor = (*eventsWriterActor)(nil)

// newEventsWriterActor creates an events writer actor, to be spawned as a child of the entity
// actor whose events it writes.
func newEventsWriterActor() *eventsWriterActor {
	return &eventsWriterActor{}
}

// PreStart loads the events store and event stream from the actor system extensions.
func (a *eventsWriterActor) PreStart(ctx *goakt.Context) error {
	eventsStoreExt, err := extensions.Require[*extensions.EventsStore](ctx, extensions.EventsStoreExtensionID)
	if err != nil {
		return err
	}
	eventsStreamExt, err := extensions.Require[*extensions.EventsStream](ctx, extensions.EventsStreamExtensionID)
	if err != nil {
		return err
	}

	a.eventsStore = eventsStoreExt.Underlying()
	a.eventsStream = eventsStreamExt.Underlying()
	a.logger = goaktlog.Backend(ctx.Logger())

	telemetryExt, err := extensions.Optional[*extensions.TelemetryExtension](ctx, extensions.TelemetryExtensionID)
	if err != nil {
		return err
	}
	if telemetryExt != nil {
		a.metrics = instrumentation.New(telemetryExt.Meter())
	}
	return nil
}

// Receive handles incoming messages. Only persistEventsRequest is expected.
func (a *eventsWriterActor) Receive(ctx *goakt.ReceiveContext) {
	switch msg := ctx.Message().(type) {
	case *goakt.PostStart:
		// no-op
	case *persistEventsRequest:
		a.handlePersistEvents(ctx, msg)
	default:
		ctx.Unhandled()
	}
}

// PostStop performs cleanup when the actor is stopped.
func (a *eventsWriterActor) PostStop(_ *goakt.Context) error {
	return nil
}

// handlePersistEvents writes events to the store and publishes them to the stream
// only after the write succeeds. The result including any error is returned via
// persistEventsResponse so the parent receives the reply through its Ask call.
func (a *eventsWriterActor) handlePersistEvents(ctx *goakt.ReceiveContext, req *persistEventsRequest) {
	if err := a.eventsStore.WriteEvents(ctx.Context(), req.scope, req.envelopes, req.precondition); err != nil {
		ctx.Response(&persistEventsResponse{Err: err})
		return
	}

	for _, envelope := range req.envelopes {
		// EGO-TENANT-005: publish for the scope the owning actor is bound to,
		// and only when the envelope's tenant identity agrees with it. The
		// events are already persisted, so a rejected publication is dropped,
		// logged and counted, never delivered, and never fails the write.
		if err := protocol.PublishScoped(a.eventsStream, req.scope, req.topic, envelope, envelope.GetTenantMetadata()); err != nil {
			a.metrics.PublicationRejected(ctx.Context())
			a.logger.ErrorContext(ctx.Context(), "event not published: tenant identity check failed",
				"persistence_id", envelope.GetPersistenceId(),
				"sequence_number", envelope.GetSequenceNumber(),
				"scope", req.scope.String(),
				"error", err)
		}
	}

	ctx.Response(&persistEventsResponse{})
}

// askEventsWriter sends envelopes to the events writer over a plain goakt.Ask call — safe
// to run inside a plain goroutine via ctx.PipeTo, unlike ctx.Ask, which blocks
// the calling dispatcher worker (see the entity actor's persistAsync and
// flushBatch). Any transport-level failure is embedded in the returned
// *persistEventsResponse's Err field rather than returned as a Go error, so PipeTo always
// delivers a persistEventsResponse message that the entity actor already knows how to
// route.
func askEventsWriter(writer *goakt.PID, envelopes []*egopb.Event, topic string, timeout time.Duration, precondition persistence.WritePrecondition, scope persistence.Scope) (*persistEventsResponse, error) {
	reply, err := goakt.Ask(context.Background(), writer, &persistEventsRequest{
		envelopes:    envelopes,
		topic:        topic,
		precondition: precondition,
		scope:        scope,
	}, timeout)

	if err != nil {
		return &persistEventsResponse{Err: err}, nil
	}

	if reply == nil {
		return &persistEventsResponse{Err: fmt.Errorf("event writer returned no response")}, nil
	}

	resp, ok := reply.(*persistEventsResponse)
	if !ok {
		return &persistEventsResponse{Err: fmt.Errorf("unexpected response type %T from event writer", reply)}, nil
	}
	return resp, nil
}
