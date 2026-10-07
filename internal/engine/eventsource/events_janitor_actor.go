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
	kitlog "github.com/pablogore/kit-logger/pkg/logger"
	goakt "github.com/tochemey/goakt/v4/actor"

	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/goaktlog"
	"github.com/getsyntegrity/urd/persistence"
)

// applyRetentionRequest is sent from the [snapshotsWriterActor] to the
// [eventsJanitorActor] to delete old events and snapshots according to the
// configured retention policy.
//
// scope carries the owning Actor's bound persistence.Scope
// (TENANT-003 T4). eventsJanitorActor is a separate child actor with no
// PreStart access to the parent's dependencies, so the scope must travel on
// this request rather than be re-derived here.
type applyRetentionRequest struct {
	persistenceID             string
	eventsCounter             uint64
	snapshotInterval          uint64
	deleteEventsOnSnapshot    bool
	deleteSnapshotsOnSnapshot bool
	eventsRetentionCount      uint64
	scope                     persistence.Scope
}

// eventsJanitorActor handles cleanup of old events and snapshots after a successful
// snapshot write. It receives applyRetentionRequest messages via Tell
// (fire-and-forget) and operates independently of the command processing path.
//
// Failures are logged but do not propagate to the parent actor or the command
// caller. This decouples storage cleanup from command latency.
type eventsJanitorActor struct {
	eventsStore   persistence.EventsStore
	snapshotStore persistence.SnapshotStore
	logger        kitlog.Logger
}

var _ goakt.Actor = (*eventsJanitorActor)(nil)

// newEventsJanitorActor creates an instance of [eventsJanitorActor].
func newEventsJanitorActor() *eventsJanitorActor {
	return &eventsJanitorActor{}
}

// PreStart loads the events store and snapshot store from the actor system
// extensions. The snapshot store is genuinely optional here: a missing
// registration is not an error, but a mismatched type registered under its
// extension ID is (see optionalExtension in extension_lookup.go and
// issue #99).
func (a *eventsJanitorActor) PreStart(ctx *goakt.Context) error {
	a.logger = goaktlog.Backend(ctx.Logger())

	eventsStoreExt, err := extensions.Require[*extensions.EventsStore](ctx, extensions.EventsStoreExtensionID)
	if err != nil {
		return err
	}
	a.eventsStore = eventsStoreExt.Underlying()

	snapshotStoreExt, err := extensions.Optional[*extensions.SnapshotStoreExt](ctx, extensions.SnapshotStoreExtensionID)
	if err != nil {
		return err
	}
	if snapshotStoreExt != nil {
		a.snapshotStore = snapshotStoreExt.Underlying()
	}
	return nil
}

// Receive handles incoming messages. Only applyRetentionRequest is expected.
func (a *eventsJanitorActor) Receive(ctx *goakt.ReceiveContext) {
	switch msg := ctx.Message().(type) {
	case *goakt.PostStart:
		// no-op
	case *applyRetentionRequest:
		a.handleApplyRetention(ctx, msg)
	default:
		ctx.Unhandled()
	}
}

// PostStop performs cleanup when the actor is stopped.
func (a *eventsJanitorActor) PostStop(_ *goakt.Context) error {
	return nil
}

// handleApplyRetention deletes old events and snapshots based on the retention
// policy. Each delete operation is retried with exponential backoff and
// executed independently so that a failure in one does not prevent the other
// from running.
func (a *eventsJanitorActor) handleApplyRetention(ctx *goakt.ReceiveContext, req *applyRetentionRequest) {
	if req.deleteEventsOnSnapshot {
		deleteUpTo := req.eventsCounter
		if req.eventsRetentionCount > 0 {
			deleteUpTo -= min(deleteUpTo, req.eventsRetentionCount)
		}

		if deleteUpTo > 0 {
			guarded, ok := a.eventsStore.(persistence.RetainedEventsDeleter)
			if !ok {
				a.logger.ErrorContext(ctx.Context(), "automatic retention refused", "error", persistence.ErrUnsafeEventRetention)
				return
			}
			if err := retryWithBackoff(ctx.Context(), defaultBackoff(), defaultMaxRetries, func() error {
				return guarded.DeleteRetainedEvents(ctx.Context(), req.scope, req.persistenceID, deleteUpTo)
			}); err != nil {
				a.logger.ErrorContext(ctx.Context(), "failed to delete events for retention policy",
					"persistence_id", req.persistenceID,
					"delete_up_to", deleteUpTo,
					"error", err)
			}
		}
	}

	if req.deleteSnapshotsOnSnapshot && a.snapshotStore != nil && req.eventsCounter > req.snapshotInterval {
		previousSnapshotSeqNr := req.eventsCounter - req.snapshotInterval
		if err := retryWithBackoff(ctx.Context(), defaultBackoff(), defaultMaxRetries, func() error {
			return a.snapshotStore.DeleteSnapshots(ctx.Context(), req.scope, req.persistenceID, previousSnapshotSeqNr)
		}); err != nil {
			a.logger.ErrorContext(ctx.Context(), "failed to delete old snapshots for retention policy",
				"persistence_id", req.persistenceID,
				"sequence_number", previousSnapshotSeqNr,
				"error", err)
		}
	}
}
