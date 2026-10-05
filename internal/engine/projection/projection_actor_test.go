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
	"errors"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"
	"go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/encryption"
	"github.com/getsyntegrity/urd/eventadapter"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/goaktlog"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	egoprojection "github.com/getsyntegrity/urd/projection"
	"github.com/getsyntegrity/urd/testkit"
)

const (
	projectionName = "db-writer"
	shardNumber    = uint64(9)
	eventCount     = 10

	// The runner pulls every PullInterval (one second), so the polls below
	// allow several pulls before they give up.
	settleTimeout  = 15 * time.Second
	settleInterval = 20 * time.Millisecond
)

// journalEvents builds eventCount consecutive events for one persistence ID on
// the shard the projection reads.
func journalEvents(ctx *specs.Context, persistenceID string) []*egopb.Event {
	event, err := anypb.New(&testpb.AccountCredited{})
	ctx.Expect(err).To(specs.BeNil())

	timestamp := timestamppb.Now().AsTime().Unix()
	journals := make([]*egopb.Event, eventCount)
	for i := range eventCount {
		journals[i] = &egopb.Event{
			PersistenceId:  persistenceID,
			SequenceNumber: uint64(i + 1),
			Event:          event,
			Timestamp:      timestamp,
			Shard:          shardNumber,
		}
	}
	return journals
}

// startSystem builds and starts an in-process actor system with the given
// extensions and registers its shutdown with the case, so it stops even when an
// expectation fails.
func startSystem(ctx *specs.Context, name string, retries int, exts ...extension.Extension) goakt.ActorSystem {
	bg := context.Background()
	system, err := goakt.NewActorSystem(name,
		goakt.WithLogger(goaktlog.New(enginetest.DiscardLogger)),
		goakt.WithExtensions(exts...),
		goakt.WithActorInitMaxRetries(retries))
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(system.Start(bg)).To(specs.BeNil())
	ctx.Cleanup(func() { ctx.Expect(system.Stop(bg)).To(specs.BeNil()) })
	return system
}

// projectionOptions returns the options registered for the "db-writer"
// projection extension, applying tweak when given.
func projectionOptions(tweak func(*egoprojection.Options)) *extensions.ProjectionExtension {
	scope := persistence.Unscoped()
	options := &egoprojection.Options{
		Handler:      egoprojection.NewDiscardHandler(),
		BufferSize:   500,
		PullInterval: time.Second,
		Recovery:     egoprojection.NewRecovery(),
		// the engine resolves the effective scope at registration
		Scope: &scope,
	}
	if tweak != nil {
		tweak(options)
	}
	return extensions.NewProjectionExtension(map[string]*egoprojection.Options{projectionName: options})
}

// projectionFixture is an in-memory journal and offset store pair connected for
// the duration of the case.
type projectionFixture struct {
	journal *testkit.EventStore
	offsets *testkit.OffsetStore
}

func newProjectionFixture(ctx *specs.Context) projectionFixture {
	bg := context.Background()
	f := projectionFixture{journal: testkit.NewEventsStore(), offsets: testkit.NewOffsetStore()}
	ctx.Expect(f.journal).To(specs.Not(specs.BeNil()))
	ctx.Expect(f.offsets).To(specs.Not(specs.BeNil()))
	ctx.Expect(f.journal.Connect(bg)).To(specs.BeNil())
	ctx.Expect(f.offsets.Connect(bg)).To(specs.BeNil())
	ctx.Cleanup(func() {
		ctx.Expect(f.journal.Disconnect(bg)).To(specs.BeNil())
		ctx.Expect(f.offsets.Disconnect(bg)).To(specs.BeNil())
	})
	return f
}

// expectOffsetReaches waits until the projection has stored the timestamp of
// the last journal event as its offset, which is the observable proof that it
// consumed the whole batch.
func (f projectionFixture) expectOffsetReaches(ctx *specs.Context, last *egopb.Event) {
	projectionID := &egopb.ProjectionId{ProjectionName: projectionName, ShardNumber: shardNumber}
	ctx.Eventually(func() any {
		offset, err := f.offsets.GetCurrentOffset(context.Background(), projectionID)
		if err != nil || offset == nil {
			return int64(-1)
		}
		return offset.GetValue()
	}, specs.Equal(last.GetTimestamp()), specs.WithTimeout(settleTimeout), specs.WithInterval(settleInterval))
}

func TestProjection(t *testing.T) {
	specs.Describe(t, "Projection actor", func(s *specs.Spec) {
		s.It("With happy path", func(ctx *specs.Context) {
			bg := context.Background()
			f := newProjectionFixture(ctx)
			system := startSystem(ctx, "TestActorSystem", 3,
				extensions.NewEventsStore(f.journal),
				extensions.NewOffsetStore(f.offsets),
				projectionOptions(nil))

			pid, err := system.Spawn(bg, projectionName, New(), goakt.WithLongLived())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(pid).To(specs.Not(specs.BeNil()))

			journals := journalEvents(ctx, uuid.NewString())
			ctx.Expect(f.journal.WriteEvents(bg, persistence.Unscoped(), journals, persistence.Unconditional())).To(specs.BeNil())

			// The offset is written once the runner has pulled and handled the
			// batch, which is eventually consistent.
			f.expectOffsetReaches(ctx, journals[eventCount-1])
		})

		s.It("With unhandled message result in deadletter", func(ctx *specs.Context) {
			bg := context.Background()
			f := newProjectionFixture(ctx)
			system := startSystem(ctx, "TestActorSystem", 3,
				extensions.NewEventsStore(f.journal),
				extensions.NewOffsetStore(f.offsets),
				projectionOptions(nil))

			pid, err := system.Spawn(bg, projectionName, New(), goakt.WithLongLived())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(pid).To(specs.Not(specs.BeNil()))

			ctx.Expect(goakt.Tell(bg, pid, &testpb.CreateAccount{})).To(specs.BeNil())

			ctx.Eventually(func() any { return int64(pid.Metric(bg).DeadlettersCount()) }, specs.Equal(int64(1)),
				specs.WithTimeout(settleTimeout), specs.WithInterval(settleInterval))
		})

		s.It("With dead letter handler", func(ctx *specs.Context) {
			bg := context.Background()
			f := newProjectionFixture(ctx)
			system := startSystem(ctx, "TestActorSystem", 3,
				extensions.NewEventsStore(f.journal),
				extensions.NewOffsetStore(f.offsets),
				projectionOptions(func(o *egoprojection.Options) {
					o.DeadLetterHandler = egoprojection.NewDiscardDeadLetterHandler()
				}))

			journals := journalEvents(ctx, uuid.NewString())
			ctx.Expect(f.journal.WriteEvents(bg, persistence.Unscoped(), journals, persistence.Unconditional())).To(specs.BeNil())

			pid, err := system.Spawn(bg, projectionName, New(), goakt.WithLongLived())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(pid).To(specs.Not(specs.BeNil()))

			f.expectOffsetReaches(ctx, journals[eventCount-1])
		})

		s.It("With event adapters extension", func(ctx *specs.Context) {
			bg := context.Background()
			f := newProjectionFixture(ctx)
			system := startSystem(ctx, "TestActorSystem", 3,
				extensions.NewEventsStore(f.journal),
				extensions.NewOffsetStore(f.offsets),
				projectionOptions(nil),
				extensions.NewEventAdapters([]eventadapter.EventAdapter{passthroughEventAdapter{}}))

			journals := journalEvents(ctx, uuid.NewString())
			ctx.Expect(f.journal.WriteEvents(bg, persistence.Unscoped(), journals, persistence.Unconditional())).To(specs.BeNil())

			pid, err := system.Spawn(bg, projectionName, New(), goakt.WithLongLived())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(pid).To(specs.Not(specs.BeNil()))

			f.expectOffsetReaches(ctx, journals[eventCount-1])
		})

		s.It("With encryptor extension", func(ctx *specs.Context) {
			bg := context.Background()
			f := newProjectionFixture(ctx)
			system := startSystem(ctx, "TestActorSystem", 3,
				extensions.NewEventsStore(f.journal),
				extensions.NewOffsetStore(f.offsets),
				projectionOptions(nil),
				extensions.NewEncryptor(encryption.NewAESEncryptor(testkit.NewKeyStore())))

			journals := journalEvents(ctx, uuid.NewString())
			ctx.Expect(f.journal.WriteEvents(bg, persistence.Unscoped(), journals, persistence.Unconditional())).To(specs.BeNil())

			pid, err := system.Spawn(bg, projectionName, New(), goakt.WithLongLived())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(pid).To(specs.Not(specs.BeNil()))

			f.expectOffsetReaches(ctx, journals[eventCount-1])
		})

		s.It("With telemetry extension", func(ctx *specs.Context) {
			bg := context.Background()
			f := newProjectionFixture(ctx)
			system := startSystem(ctx, "TestActorSystem", 3,
				extensions.NewEventsStore(f.journal),
				extensions.NewOffsetStore(f.offsets),
				projectionOptions(nil),
				extensions.NewTelemetryExtension(tracenoop.NewTracerProvider().Tracer("test"), noop.NewMeterProvider().Meter("test")))

			journals := journalEvents(ctx, uuid.NewString())
			ctx.Expect(f.journal.WriteEvents(bg, persistence.Unscoped(), journals, persistence.Unconditional())).To(specs.BeNil())

			pid, err := system.Spawn(bg, projectionName, New(), goakt.WithLongLived())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(pid).To(specs.Not(specs.BeNil()))

			// Stopping the system in the case cleanup exercises the PostStop
			// metrics path.
			f.expectOffsetReaches(ctx, journals[eventCount-1])
		})
	})
}

type mistypedCase struct {
	name        string
	system      string
	extensionID string
}

func TestProjectionActorPreStartFailure(t *testing.T) {
	specs.Describe(t, "Projection actor PreStart", func(s *specs.Spec) {
		s.It("fails when runner Start returns an error", func(ctx *specs.Context) {
			bg := context.Background()
			resetAt := time.Now().UTC()
			errResetFailed := errors.New("reset offset failed")

			// Ping succeeds so the store-connectivity retrier passes immediately.
			ctrl := mock.NewController(ctx)
			ctrl.Method("Ping").Expect(mock.Any()).Return(nil).AnyTimes()
			eventsStore := enginetest.NewEventsStoreMock(ctrl)

			// Ping succeeds but ResetOffset returns an error, causing preStart
			// and therefore runner.Start to fail.
			offsetCtrl := mock.NewController(ctx)
			offsetCtrl.Method("Ping").Expect(mock.Any()).Return(nil).AnyTimes()
			offsetCtrl.Method("ResetOffset").
				Expect(mock.Any(), projectionName, resetAt.UnixMilli()).
				Return(errResetFailed).AtLeast(1)
			offsetStore := enginetest.NewOffsetStoreMock(offsetCtrl)

			system := startSystem(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(eventsStore),
				extensions.NewOffsetStore(offsetStore),
				projectionOptions(func(o *egoprojection.Options) { o.ResetOffset = resetAt }))

			_, err := system.Spawn(bg, projectionName, New(), goakt.WithLongLived())

			ctx.Expect(err).To(specs.MatchError(errResetFailed))
		})

		specs.Table(s, []mistypedCase{
			{
				name:        "returns an error instead of panicking when the event adapters extension is registered with an unexpected type",
				system:      "TestProjectionMistypedEventAdaptersSystem",
				extensionID: extensions.EventAdaptersExtensionID,
			},
			{
				name:        "returns an error instead of panicking when the events stream extension is registered with an unexpected type",
				system:      "TestProjectionMistypedEventsStreamSystem",
				extensionID: extensions.EventsStreamExtensionID,
			},
			{
				name:        "returns an error instead of panicking when the encryptor extension is registered with an unexpected type",
				system:      "TestProjectionMistypedEncryptorSystem",
				extensionID: extensions.EncryptorExtensionID,
			},
			{
				name:        "returns an error instead of panicking when the telemetry extension is registered with an unexpected type",
				system:      "TestProjectionMistypedTelemetrySystem",
				extensionID: extensions.TelemetryExtensionID,
			},
		}, func(c mistypedCase) string { return c.name }, func(ctx *specs.Context, c mistypedCase) {
			bg := context.Background()
			ctrl := mock.NewController(ctx)
			ctrl.Method("Ping").Expect(mock.Any()).Return(nil).AnyTimes()
			offsetCtrl := mock.NewController(ctx)
			offsetCtrl.Method("Ping").Expect(mock.Any()).Return(nil).AnyTimes()

			system := startSystem(ctx, c.system, 1,
				extensions.NewEventsStore(enginetest.NewEventsStoreMock(ctrl)),
				extensions.NewOffsetStore(enginetest.NewOffsetStoreMock(offsetCtrl)),
				projectionOptions(nil),
				&enginetest.MistypedExtension{Name: c.extensionID})

			pid, err := system.Spawn(bg, projectionName, New(), goakt.WithLongLived())

			ctx.Expect(err).To(specs.MatchError(extensions.ErrMissingRequiredExtensions))
			ctx.Expect(pid).To(specs.BeNil())
		})
	})
}

// passthroughEventAdapter is an event adapter that passes events through unchanged
type passthroughEventAdapter struct{}

func (p passthroughEventAdapter) Adapt(event *anypb.Any, _ uint64) (*anypb.Any, error) {
	return event, nil
}
