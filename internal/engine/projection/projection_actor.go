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

package projection

import (
	"context"
	"fmt"

	goakt "github.com/tochemey/goakt/v4/actor"

	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/goaktlog"
	"github.com/getsyntegrity/urd/internal/instrumentation"
	"github.com/getsyntegrity/urd/internal/projectionrunner"
)

// runnerFailed is the internal message the projection actor sends itself when
// the runner's processing loop stops permanently on an unprocessable event.
type runnerFailed struct {
	err error
}

// Actor hosts one projection: it runs a projectionrunner.Runner against the
// events store and reports a runner that stopped permanently.
// Only a single instance of this will run throughout the cluster
type Actor struct {
	runner   *projectionrunner.Runner
	metrics  *instrumentation.Instruments
	escalate func(cause error) error
}

// implements the Actor contract
var _ goakt.Actor = (*Actor)(nil)

// New creates an instance of Actor.
// The constructor takes no arguments to support cluster relocation.
func New() *Actor {
	return &Actor{}
}

// SetEscalation sets the function that builds the error the actor fails with
// when the runner stops permanently on an unprocessable event. The engine sets
// it in its Actor wrapper, because GoAkt keys supervisor directives
// by the error type's name and that type must stay in package engine. Without
// it the actor fails with the runner's cause unchanged.
func (x *Actor) SetEscalation(escalate func(cause error) error) {
	x.escalate = escalate
}

// PreStart prepares the projection
func (x *Actor) PreStart(ctx *goakt.Context) error {
	offsetStoreExt, err := extensions.Require[*extensions.OffsetStore](ctx, extensions.OffsetStoreExtensionID)
	if err != nil {
		return err
	}
	eventsStoreExt, err := extensions.Require[*extensions.EventsStore](ctx, extensions.EventsStoreExtensionID)
	if err != nil {
		return err
	}
	registry, err := extensions.Require[*extensions.ProjectionExtension](ctx, extensions.ProjectionExtensionID)
	if err != nil {
		return err
	}
	offsetStore := offsetStoreExt.Underlying()
	eventsStore := eventsStoreExt.Underlying()

	// The actor name is the projection name: resolve this projection's own
	// handler and options from the registry built by WithProjection.
	options := registry.Get(ctx.ActorName())
	if options == nil {
		return fmt.Errorf("projection %q is not registered: register it with engine.WithProjection on every node", ctx.ActorName())
	}

	opts := []projectionrunner.Option{
		projectionrunner.WithLogger(goaktlog.Backend(ctx.ActorSystem().Logger())),
		projectionrunner.WithRecoveryStrategy(options.Recovery),
		projectionrunner.WithStartOffset(options.StartOffset),
		projectionrunner.WithResetOffset(options.ResetOffset),
		projectionrunner.WithMaxBufferSize(options.BufferSize),
		projectionrunner.WithPullInterval(options.PullInterval),
	}

	if options.DeadLetterHandler != nil {
		opts = append(opts, projectionrunner.WithDeadLetterHandler(options.DeadLetterHandler))
	}

	eventAdaptersExt, err := extensions.Optional[*extensions.EventAdapters](ctx, extensions.EventAdaptersExtensionID)
	if err != nil {
		return err
	}
	if eventAdaptersExt != nil {
		opts = append(opts, projectionrunner.WithEventAdapters(eventAdaptersExt.Adapters()))
	}

	// Events persisted on this node trigger an immediate pull instead of
	// waiting for the next pull interval.
	eventsStreamExt, err := extensions.Optional[*extensions.EventsStream](ctx, extensions.EventsStreamExtensionID)
	if err != nil {
		return err
	}
	if eventsStreamExt != nil {
		opts = append(opts, projectionrunner.WithEventsStream(withProjectionWake(eventsStreamExt.Underlying()), protocol.EventsTopic))
	}

	encryptorExt, err := extensions.Optional[*extensions.EncryptorExtension](ctx, extensions.EncryptorExtensionID)
	if err != nil {
		return err
	}
	if encryptorExt != nil {
		opts = append(opts, projectionrunner.WithEncryptor(encryptorExt.Encryptor()))
	}

	telemetryExt, err := extensions.Optional[*extensions.TelemetryExtension](ctx, extensions.TelemetryExtensionID)
	if err != nil {
		return err
	}
	if telemetryExt != nil {
		x.metrics = instrumentation.New(telemetryExt.Meter())
		if x.metrics != nil {
			opts = append(opts, projectionrunner.WithMetrics(x.metrics))
		}
	}

	// The engine resolved the effective scope at registration. A registration
	// that reaches here without one fails closed instead of reading unscoped.
	if options.Scope == nil {
		return fmt.Errorf("projection %q has no resolved scope: %w", ctx.ActorName(), projectionrunner.ErrScopeRequired)
	}
	opts = append(opts, projectionrunner.WithScope(*options.Scope))

	x.runner = projectionrunner.New(ctx.ActorName(), options.Handler, eventsStore, offsetStore, opts...)

	// Use context.Background() instead of ctx.Context() because PreStart's
	// context is ephemeral — goakt wraps it in context.WithTimeout and cancels
	// it immediately after PreStart returns. The runner's Start performs store
	// pings and offset resets that must not be tied to that short-lived context.
	if err := x.runner.Start(context.Background()); err != nil {
		return err
	}

	x.metrics.ProjectionStarted(context.Background())

	return nil
}

// Receive handle the message sent to the projection actor
func (x *Actor) Receive(ctx *goakt.ReceiveContext) {
	switch msg := ctx.Message().(type) {
	case *goakt.PostStart:
		// Hand the runner a way back to this actor before the processing loop
		// starts: a loop that dies on an unprocessable event sends
		// runnerFailed back so the actor fails through the normal supervision
		// path instead of staying healthy-looking with a dead runner.
		pid := ctx.Self()
		x.runner.Run(ctx.Context(), func(cause error) {
			// A failed delivery means the host actor is already stopping, in
			// which case the projection is going down anyway.
			_ = goakt.Tell(context.Background(), pid, &runnerFailed{err: x.escalation(cause)})
		})
	case *runnerFailed:
		ctx.Err(msg.err)
	default:
		ctx.Unhandled()
	}
}

// PostStop prepares the actor to gracefully shutdown
func (x *Actor) PostStop(ctx *goakt.Context) error {
	x.metrics.ProjectionStopped(ctx.Context())
	return x.runner.Stop()
}

// escalation returns the error the actor fails with for a runner cause.
func (x *Actor) escalation(cause error) error {
	if x.escalate == nil {
		return cause
	}
	return x.escalate(cause)
}
