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
	"errors"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/encryption"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/goaktlog"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/testkit"
)

// snapshotsPoll bounds every wait of this file. The writer retries a failing
// store with real backoff (retryBaseDelay doubling, up to defaultMaxRetries),
// so a poll that follows a failing write needs a few seconds of headroom.
var snapshotsPoll = []specs.PollOption{specs.WithTimeout(10 * time.Second), specs.WithInterval(10 * time.Millisecond)}

var (
	errSnapshotStoreDown = errors.New("snapshot store down")
	errEncryptionDown    = errors.New("encryption down")
)

// snapshotsSettle is how long a "nothing else happens" check keeps watching.
const snapshotsSettle = 500 * time.Millisecond

// startSnapshotSystem starts a real goakt actor system with the given
// extensions and stops it when the case ends. The events store and the events
// stream every case needs are added by the caller through exts.
func startSnapshotSystem(ctx *specs.Context, name string, exts ...extension.Extension) goakt.ActorSystem {
	return startSnapshotSystemWith(ctx, name, 1, exts...)
}

// startSnapshotSystemWith is startSnapshotSystem with an explicit number of
// actor initialization retries.
func startSnapshotSystemWith(ctx *specs.Context, name string, initRetries int, exts ...extension.Extension) goakt.ActorSystem {
	bg := context.Background()
	system, err := goakt.NewActorSystem(name,
		goakt.WithLogger(goaktlog.New(enginetest.DiscardLogger)),
		goakt.WithExtensions(exts...),
		goakt.WithActorInitMaxRetries(initRetries))
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(system.Start(bg)).To(specs.BeNil())
	ctx.Cleanup(func() { _ = system.Stop(bg) })
	return system
}

// newEventsStream returns an events stream that is closed when the case ends.
func newEventsStream(ctx *specs.Context) eventstream.Stream {
	stream := eventstream.New()
	ctx.Cleanup(stream.Close)
	return stream
}

// spawnSnapshotsWriter spawns a snapshots writer under name.
func spawnSnapshotsWriter(ctx *specs.Context, system goakt.ActorSystem, name string) *goakt.PID {
	pid, err := system.Spawn(context.Background(), name, newSnapshotsWriterActor())
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(pid).To(specs.Not(specs.BeNil()))
	return pid
}

// sampleSnapshot returns a snapshot of "entity-1" at the given sequence number.
func sampleSnapshot(ctx *specs.Context, seqNr uint64) *egopb.Snapshot {
	state, err := anypb.New(&egopb.NoReply{})
	ctx.Expect(err).To(specs.BeNil())
	return &egopb.Snapshot{
		PersistenceId:  "entity-1",
		SequenceNumber: seqNr,
		State:          state,
		Timestamp:      time.Now().Unix(),
	}
}

// retentionRequest returns the retention request the retention cases send
// along with the snapshot at sequence 10.
func retentionRequest() *applyRetentionRequest {
	return &applyRetentionRequest{
		scope:                  persistence.Unscoped(),
		persistenceID:          "entity-1",
		eventsCounter:          10,
		snapshotInterval:       5,
		deleteEventsOnSnapshot: true,
	}
}

// latestSnapshot reads the latest snapshot of "entity-1" from store.
func latestEntitySnapshot(store persistence.SnapshotStore) *egopb.Snapshot {
	latest, _ := store.GetLatestSnapshot(context.Background(), persistence.Unscoped(), "entity-1")
	return latest
}

// calls returns a poll function counting the recorded calls of method.
func calls(ctrl *mock.Controller, method string) func() any {
	return func() any { return len(ctrl.Method(method).Calls()) }
}

func TestSnapshotsWriterActor(t *testing.T) {
	specs.Describe(t, "snapshotsWriterActor persists snapshots and forwards retention", func(s *specs.Spec) {
		s.It("persists snapshot to store on success", func(ctx *specs.Context) {
			snapshotStore := connectedSnapshotStore(ctx)
			system := startSnapshotSystem(ctx, "TestSnapshotSystem",
				extensions.NewEventsStore(connectedEventsStore(ctx)),
				extensions.NewEventsStream(newEventsStream(ctx)),
				extensions.NewSnapshotStore(snapshotStore))
			pid := spawnSnapshotsWriter(ctx, system, "snapshot-writer-test")

			ctx.Expect(goakt.Tell(context.Background(), pid,
				&persistSnapshotRequest{snapshot: sampleSnapshot(ctx, 10), scope: persistence.Unscoped()})).To(specs.BeNil())

			ctx.Eventually(func() any { return latestEntitySnapshot(snapshotStore).GetSequenceNumber() },
				specs.Equal(uint64(10)), snapshotsPoll...)
		})

		s.It("encrypts snapshot state before writing when encryptor is configured", func(ctx *specs.Context) {
			snapshotStore := connectedSnapshotStore(ctx)
			system := startSnapshotSystem(ctx, "TestSnapshotSystem",
				extensions.NewEventsStore(connectedEventsStore(ctx)),
				extensions.NewEventsStream(newEventsStream(ctx)),
				extensions.NewSnapshotStore(snapshotStore),
				extensions.NewEncryptor(encryption.NewAESEncryptor(testkit.NewKeyStore())))
			pid := spawnSnapshotsWriter(ctx, system, "snapshot-writer-test")

			ctx.Expect(goakt.Tell(context.Background(), pid,
				&persistSnapshotRequest{snapshot: sampleSnapshot(ctx, 5), scope: persistence.Unscoped()})).To(specs.BeNil())

			ctx.Eventually(func() any { return latestEntitySnapshot(snapshotStore).GetSequenceNumber() },
				specs.Equal(uint64(5)), snapshotsPoll...)
			latest := latestEntitySnapshot(snapshotStore)
			ctx.Expect(latest.GetIsEncrypted()).To(specs.BeTrue())
			ctx.Expect(latest.GetEncryptionKeyId()).To(specs.Not(specs.BeEmpty()))
		})

		s.It("forwards retention request to janitor after successful write", func(ctx *specs.Context) {
			eventsCtrl := mock.NewController(ctx)
			eventsCtrl.Method("DeleteEvents").
				Expect(mock.Any(), persistence.Unscoped(), "entity-1", uint64(10)).Return(nil)
			system := startSnapshotSystem(ctx, "TestSnapshotSystem",
				extensions.NewEventsStore(retentionFixture{enginetest.NewEventsStoreMock(eventsCtrl)}),
				extensions.NewEventsStream(newEventsStream(ctx)),
				extensions.NewSnapshotStore(connectedSnapshotStore(ctx)))
			writer := spawnSnapshotsWriter(ctx, system, "snapshot-writer-test")
			janitor, err := system.Spawn(context.Background(), "janitor-test", newEventsJanitorActor())
			ctx.Expect(err).To(specs.BeNil())

			ctx.Expect(goakt.Tell(context.Background(), writer, &persistSnapshotRequest{
				scope:        persistence.Unscoped(),
				snapshot:     sampleSnapshot(ctx, 10),
				retentionReq: retentionRequest(),
				janitor:      janitor,
			})).To(specs.BeNil())

			// the controller verifies at the end of the case that DeleteEvents
			// was called exactly once, with the expected arguments
			ctx.Eventually(calls(eventsCtrl, "DeleteEvents"), specs.Equal(1), snapshotsPoll...)
		})

		s.It("does not forward retention when snapshot write fails", func(ctx *specs.Context) {
			eventsCtrl := mock.NewController(ctx)
			eventsCtrl.Method("DeleteEvents").
				Expect(mock.Any(), mock.Any(), mock.Any(), mock.Any()).Never()
			snapshotCtrl := mock.NewController(ctx)
			snapshotCtrl.Method("WriteSnapshot").
				Expect(mock.Any(), persistence.Unscoped(), mock.Any()).Times(defaultMaxRetries + 1).Return(errSnapshotStoreDown)
			system := startSnapshotSystem(ctx, "TestSnapshotSystem",
				extensions.NewEventsStore(retentionFixture{enginetest.NewEventsStoreMock(eventsCtrl)}),
				extensions.NewEventsStream(newEventsStream(ctx)),
				extensions.NewSnapshotStore(enginetest.NewSnapshotStoreMock(snapshotCtrl)))
			writer := spawnSnapshotsWriter(ctx, system, "snapshot-writer-test")
			janitor, err := system.Spawn(context.Background(), "janitor-test", newEventsJanitorActor())
			ctx.Expect(err).To(specs.BeNil())

			ctx.Expect(goakt.Tell(context.Background(), writer, &persistSnapshotRequest{
				scope:        persistence.Unscoped(),
				snapshot:     sampleSnapshot(ctx, 10),
				retentionReq: retentionRequest(),
				janitor:      janitor,
			})).To(specs.BeNil())

			ctx.Eventually(calls(snapshotCtrl, "WriteSnapshot"), specs.Equal(defaultMaxRetries+1), snapshotsPoll...)
			ctx.Consistently(calls(eventsCtrl, "DeleteEvents"), specs.Equal(0), specs.WithTimeout(snapshotsSettle))
			ctx.Expect(writer.IsRunning()).To(specs.BeTrue())
		})

		s.It("logs error and continues when store write fails", func(ctx *specs.Context) {
			snapshotCtrl := mock.NewController(ctx)
			snapshotCtrl.Method("WriteSnapshot").
				Expect(mock.Any(), persistence.Unscoped(), mock.Any()).Times(defaultMaxRetries + 1).Return(errSnapshotStoreDown)
			system := startSnapshotSystem(ctx, "TestSnapshotSystem",
				extensions.NewEventsStore(connectedEventsStore(ctx)),
				extensions.NewEventsStream(newEventsStream(ctx)),
				extensions.NewSnapshotStore(enginetest.NewSnapshotStoreMock(snapshotCtrl)))
			pid := spawnSnapshotsWriter(ctx, system, "snapshot-writer-test")

			ctx.Expect(goakt.Tell(context.Background(), pid,
				&persistSnapshotRequest{snapshot: sampleSnapshot(ctx, 10), scope: persistence.Unscoped()})).To(specs.BeNil())

			ctx.Eventually(calls(snapshotCtrl, "WriteSnapshot"), specs.Equal(defaultMaxRetries+1), snapshotsPoll...)
			ctx.Consistently(func() any { return pid.IsRunning() }, specs.BeTrue(), specs.WithTimeout(snapshotsSettle))
		})

		s.It("logs error and continues when encryption fails", func(ctx *specs.Context) {
			snapshotStore := connectedSnapshotStore(ctx)
			encryptorCtrl := mock.NewController(ctx)
			encryptorCtrl.Method("Encrypt").
				Expect(mock.Any(), "entity-1", mock.Any()).Return(nil, "", errEncryptionDown)
			system := startSnapshotSystem(ctx, "TestSnapshotSystem",
				extensions.NewEventsStore(connectedEventsStore(ctx)),
				extensions.NewEventsStream(newEventsStream(ctx)),
				extensions.NewSnapshotStore(snapshotStore),
				extensions.NewEncryptor(enginetest.NewEncryptorMock(encryptorCtrl)))
			pid := spawnSnapshotsWriter(ctx, system, "snapshot-writer-test")

			ctx.Expect(goakt.Tell(context.Background(), pid,
				&persistSnapshotRequest{snapshot: sampleSnapshot(ctx, 10), scope: persistence.Unscoped()})).To(specs.BeNil())

			ctx.Eventually(calls(encryptorCtrl, "Encrypt"), specs.Equal(1), snapshotsPoll...)
			ctx.Consistently(func() any { return pid.IsRunning() }, specs.BeTrue(), specs.WithTimeout(snapshotsSettle))
			// no snapshot was written since encryption failed
			ctx.Expect(latestEntitySnapshot(snapshotStore)).To(specs.BeNil())
		})

		s.It("handles nil snapshot store gracefully", func(ctx *specs.Context) {
			system := startSnapshotSystem(ctx, "TestSnapshotSystem",
				extensions.NewEventsStore(connectedEventsStore(ctx)),
				extensions.NewEventsStream(newEventsStream(ctx)))
			pid := spawnSnapshotsWriter(ctx, system, "snapshot-writer-test")

			ctx.Expect(goakt.Tell(context.Background(), pid,
				&persistSnapshotRequest{snapshot: sampleSnapshot(ctx, 10), scope: persistence.Unscoped()})).To(specs.BeNil())

			ctx.Consistently(func() any { return pid.IsRunning() }, specs.BeTrue(), specs.WithTimeout(snapshotsSettle))
		})

		// The two mistyped-extension cases differ only in the extension slot
		// that holds the wrong type.
		type mistypedCase struct {
			name, system, actor, extensionID string
		}
		specs.Table(s, []mistypedCase{
			{
				name:        "returns an error instead of panicking when the snapshot store extension is registered with an unexpected type",
				system:      "TestSnapshotMistypedStoreSystem",
				actor:       "snapshot-writer-mistyped-store",
				extensionID: extensions.SnapshotStoreExtensionID,
			},
			{
				name:        "returns an error instead of panicking when the encryptor extension is registered with an unexpected type",
				system:      "TestSnapshotMistypedEncSystem",
				actor:       "snapshot-writer-mistyped-enc",
				extensionID: extensions.EncryptorExtensionID,
			},
		}, func(c mistypedCase) string { return c.name }, func(ctx *specs.Context, c mistypedCase) {
			system := startSnapshotSystem(ctx, c.system,
				extensions.NewEventsStore(connectedEventsStore(ctx)),
				extensions.NewEventsStream(newEventsStream(ctx)),
				&enginetest.MistypedExtension{Name: c.extensionID})

			pid, err := system.Spawn(context.Background(), c.actor, newSnapshotsWriterActor())

			ctx.Expect(err).To(specs.MatchError(extensions.ErrMissingRequiredExtensions))
			ctx.Expect(pid).To(specs.BeNil())
		})

		s.It("marks unhandled messages as unhandled", func(ctx *specs.Context) {
			system := startSnapshotSystem(ctx, "TestSnapshotSystem",
				extensions.NewEventsStore(connectedEventsStore(ctx)),
				extensions.NewEventsStream(newEventsStream(ctx)))
			pid := spawnSnapshotsWriter(ctx, system, "snapshot-writer-test")

			reply, err := goakt.Ask(context.Background(), pid, &egopb.NoReply{}, 250*time.Millisecond)

			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(reply).To(specs.BeNil())
		})
	})
}
