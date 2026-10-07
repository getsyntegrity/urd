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
	"github.com/tochemey/goakt/v4/extension"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/persistence"
)

// janitorSentinelID names the request every retention case sends last. The
// janitor handles its mailbox in order, so once the sentinel's deletes reach
// the stores the case's own request has been handled completely, including its
// retries. That is what lets a case assert that something was skipped without
// sleeping to give a skipped delete time to show up.
const janitorSentinelID = "sentinel"

// retentionCase is one applyRetentionRequest and the deletes the janitor must
// issue for it. Deletes are listed by the sequence number they delete up to.
type retentionCase struct {
	name string
	req  applyRetentionRequest
	// withSnapshotStore registers a snapshot store extension.
	withSnapshotStore bool
	eventDeletes      []uint64
	snapshotDeletes   []uint64
	// eventsErr and snapshotsErr make every attempt of the matching delete fail,
	// so the janitor retries it until the attempts run out.
	eventsErr    error
	snapshotsErr error
}

// sentinelCalls counts the calls the janitor made on method for the sentinel request.
func sentinelCalls(ctrl *mock.Controller, method string) int {
	count := 0
	for _, call := range ctrl.Method(method).Calls() {
		if call.Args[2] == janitorSentinelID {
			count++
		}
	}
	return count
}

// expectDeletes declares the deletes of one persistence ID on method. A failing
// delete is attempted once more than the retry budget before the janitor gives up.
func expectDeletes(ctrl *mock.Controller, method, persistenceID string, upTo []uint64, failure error) {
	attempts := 1
	if failure != nil {
		attempts = defaultMaxRetries + 1
	}
	for _, n := range upTo {
		ctrl.Method(method).
			Expect(mock.Any(), persistence.Unscoped(), persistenceID, n).
			Times(attempts).Return(failure)
	}
}

func TestEventsJanitorActor(t *testing.T) {
	specs.Describe(t, "eventsJanitorActor deletes old events and snapshots per the retention request and survives a failing store", func(s *specs.Spec) {
		specs.Table(s, []retentionCase{
			{
				name:         "deletes events on snapshot with no retention count",
				req:          applyRetentionRequest{eventsCounter: 10, snapshotInterval: 5, deleteEventsOnSnapshot: true},
				eventDeletes: []uint64{10},
			},
			{
				// eventsCounter=10, retentionCount=3 => deleteUpTo = 10-3 = 7
				name:         "deletes events with retention count",
				req:          applyRetentionRequest{eventsCounter: 10, snapshotInterval: 5, deleteEventsOnSnapshot: true, eventsRetentionCount: 3},
				eventDeletes: []uint64{7},
			},
			{
				// eventsCounter=2, retentionCount=5 => deleteUpTo=0, no delete call
				name: "skips event deletion when retention count exceeds counter",
				req:  applyRetentionRequest{eventsCounter: 2, snapshotInterval: 5, deleteEventsOnSnapshot: true, eventsRetentionCount: 5},
			},
			{
				// eventsCounter=10, snapshotInterval=5 => previousSnapshotSeqNr = 10-5 = 5
				name:              "deletes snapshots on snapshot",
				req:               applyRetentionRequest{eventsCounter: 10, snapshotInterval: 5, deleteSnapshotsOnSnapshot: true},
				withSnapshotStore: true,
				snapshotDeletes:   []uint64{5},
			},
			{
				// eventsCounter=5, snapshotInterval=5 => 5 > 5 is false, skip
				name:              "skips snapshot deletion when counter does not exceed interval",
				req:               applyRetentionRequest{eventsCounter: 5, snapshotInterval: 5, deleteSnapshotsOnSnapshot: true},
				withSnapshotStore: true,
			},
			{
				name:         "logs error and continues when event deletion fails after retries",
				req:          applyRetentionRequest{eventsCounter: 10, snapshotInterval: 5, deleteEventsOnSnapshot: true},
				eventDeletes: []uint64{10},
				eventsErr:    errEventsStoreDown,
			},
			{
				name:              "logs error and continues when snapshot deletion fails after retries",
				req:               applyRetentionRequest{eventsCounter: 10, snapshotInterval: 5, deleteSnapshotsOnSnapshot: true},
				withSnapshotStore: true,
				snapshotDeletes:   []uint64{5},
				snapshotsErr:      errEventsStoreDown,
			},
			{
				name:              "handles both event and snapshot deletion together",
				req:               applyRetentionRequest{eventsCounter: 10, snapshotInterval: 5, deleteEventsOnSnapshot: true, deleteSnapshotsOnSnapshot: true},
				withSnapshotStore: true,
				eventDeletes:      []uint64{10},
				snapshotDeletes:   []uint64{5},
			},
		}, func(c retentionCase) string { return c.name }, func(ctx *specs.Context, c retentionCase) {
			ctrl := mock.NewController(ctx)
			eventStore := enginetest.NewEventsStoreMock(ctrl)
			pingAnyTimes(ctrl)
			expectDeletes(ctrl, "DeleteEvents", "entity-1", c.eventDeletes, c.eventsErr)
			expectDeletes(ctrl, "DeleteEvents", janitorSentinelID, []uint64{1}, nil)

			eventStream := newClosingEventStream(ctx)
			exts := []extension.Extension{
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewEventsStream(eventStream),
			}
			var snapshotCtrl *mock.Controller
			if c.withSnapshotStore {
				snapshotCtrl = mock.NewController(ctx)
				snapshotStore := enginetest.NewSnapshotStoreMock(snapshotCtrl)
				snapshotCtrl.Method("Ping").Expect(mock.Any()).AnyTimes().Return(nil)
				expectDeletes(snapshotCtrl, "DeleteSnapshots", "entity-1", c.snapshotDeletes, c.snapshotsErr)
				expectDeletes(snapshotCtrl, "DeleteSnapshots", janitorSentinelID, []uint64{1}, nil)
				exts = append(exts, extensions.NewSnapshotStore(snapshotStore))
			}
			system := startEventsSystem(ctx, "TestRetentionSystem", 1, exts...)

			pid, err := system.Spawn(context.Background(), "retention-test", newEventsJanitorActor())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(pid).To(specs.Not(specs.BeNil()))

			req := c.req
			req.scope = persistence.Unscoped()
			req.persistenceID = "entity-1"
			ctx.Expect(goakt.Tell(context.Background(), pid, &req)).To(specs.BeNil())

			sentinel := applyRetentionRequest{
				scope:                     persistence.Unscoped(),
				persistenceID:             janitorSentinelID,
				eventsCounter:             1,
				deleteEventsOnSnapshot:    true,
				deleteSnapshotsOnSnapshot: true,
			}
			ctx.Expect(goakt.Tell(context.Background(), pid, &sentinel)).To(specs.BeNil())
			waitForSentinel(ctx, ctrl, "DeleteEvents")
			if snapshotCtrl != nil {
				waitForSentinel(ctx, snapshotCtrl, "DeleteSnapshots")
			}

			ctx.Expect(pid.IsRunning()).To(specs.BeTrue())
		})

		s.It("returns an error instead of panicking when the snapshot store extension is registered with an unexpected type", func(ctx *specs.Context) {
			ctrl := mock.NewController(ctx)
			eventStore := enginetest.NewEventsStoreMock(ctrl)
			pingAnyTimes(ctrl)
			eventStream := newClosingEventStream(ctx)
			system := startEventsSystem(ctx, "TestJanitorMistypedSnapshotSystem", 1,
				extensions.NewEventsStore(retentionFixture{eventStore}),
				extensions.NewEventsStream(eventStream),
				&enginetest.MistypedExtension{Name: extensions.SnapshotStoreExtensionID})

			pid, err := system.Spawn(context.Background(), "retention-mistyped-snapshot", newEventsJanitorActor())
			ctx.Expect(err).To(specs.MatchError(extensions.ErrMissingRequiredExtensions))
			ctx.Expect(pid).To(specs.BeNil())
		})

		s.It("marks unhandled messages as unhandled", func(ctx *specs.Context) {
			ctrl := mock.NewController(ctx)
			eventStore := enginetest.NewEventsStoreMock(ctrl)
			pingAnyTimes(ctrl)
			eventStream := newClosingEventStream(ctx)
			system := startEventsSystem(ctx, "TestRetentionSystem", 1,
				extensions.NewEventsStore(retentionFixture{eventStore}), extensions.NewEventsStream(eventStream))

			pid, err := system.Spawn(context.Background(), "retention-test", newEventsJanitorActor())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(pid).To(specs.Not(specs.BeNil()))

			reply, err := goakt.Ask(context.Background(), pid, &egopb.NoReply{}, 500*time.Millisecond)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(reply).To(specs.BeNil())
		})
	})
}

// waitForSentinel waits until the janitor has issued the sentinel's delete on
// method. A failing delete retries with the production backoff, which is real
// time of about a second, so the wait is bounded generously.
func waitForSentinel(ctx *specs.Context, ctrl *mock.Controller, method string) {
	ctx.Eventually(func() any { return sentinelCalls(ctrl, method) }, specs.Equal(1),
		specs.WithTimeout(10*time.Second), specs.WithInterval(10*time.Millisecond))
}
