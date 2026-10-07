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
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/goaktlog"
	"github.com/getsyntegrity/urd/persistence"
)

// errSnapshotWrite is what a failing snapshot write returns.
var errSnapshotWrite = errors.New("snapshot write failed")

// errEncrypt is what a failing encryptor returns.
var errEncrypt = errors.New("encrypt failed")

// snapshotWriteGate holds a snapshot write until the case opens it, so a case can
// observe what happens while the write is still in flight without sleeping.
type snapshotWriteGate struct {
	once sync.Once
	open chan struct{}
}

func newSnapshotWriteGate() *snapshotWriteGate { return &snapshotWriteGate{open: make(chan struct{})} }

// release lets every held and future write through. It is safe to call twice.
func (g *snapshotWriteGate) release() { g.once.Do(func() { close(g.open) }) }

// snapshotWriterRig wires a snapshots writer and an events janitor on one
// actor system whose stores record into one shared, ordered log.
type snapshotWriterRig struct {
	calls   *storeCalls
	writer  *goakt.PID
	janitor *goakt.PID
}

// newSnapshotWriterRig builds the rig for one case. snapshotErr makes every
// snapshot write fail, encryptor adds an encryptor extension when non-nil and
// gate, when non-nil, holds each snapshot write until it is released. The
// actor system and the stores are stopped when the case ends, after the gate
// is released.
func newSnapshotWriterRig(ctx *specs.Context, snapshotErr error, encryptor *extensions.EncryptorExtension, gate *snapshotWriteGate) *snapshotWriterRig {
	bg := context.Background()

	calls := new(storeCalls)
	baseEvents := connectedEventsStore(ctx)
	baseSnapshots := connectedSnapshotStore(ctx)

	var hold <-chan struct{}
	if gate != nil {
		hold = gate.open
	}
	exts := []extension.Extension{
		extensions.NewEventsStore(retentionFixture{&loggingEventsStore{EventsStore: baseEvents, calls: calls}}),
		extensions.NewSnapshotStore(&loggingSnapshotStore{SnapshotStore: baseSnapshots, calls: calls, writeErr: snapshotErr, hold: hold}),
	}
	if encryptor != nil {
		exts = append(exts, encryptor)
	}

	actorSystem, err := goakt.NewActorSystem("SnapshotWriterContractSystem",
		goakt.WithLogger(goaktlog.New(enginetest.DiscardLogger)),
		goakt.WithExtensions(exts...),
		goakt.WithActorInitMaxRetries(3))
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(actorSystem.Start(bg)).To(specs.BeNil())
	ctx.Cleanup(func() { _ = actorSystem.Stop(bg) })
	if gate != nil {
		// registered after the system's cleanup, so it runs first and a held
		// write cannot keep the system from stopping
		ctx.Cleanup(gate.release)
	}

	rig := &snapshotWriterRig{calls: calls}
	rig.writer, err = actorSystem.Spawn(bg, "snapshots-writer", newSnapshotsWriterActor())
	ctx.Expect(err).To(specs.BeNil())
	rig.janitor, err = actorSystem.Spawn(bg, "events-janitor", newEventsJanitorActor())
	ctx.Expect(err).To(specs.BeNil())
	return rig
}

// persist sends one snapshot at sequence 4 to the writer. With withRetention
// the request carries a retention policy for interval 2, so main computes the
// previous snapshot as sequence 2.
func (r *snapshotWriterRig) persist(ctx *specs.Context, scope persistence.Scope, withRetention bool) {
	state, err := anypb.New(wrapperspb.String("state"))
	ctx.Expect(err).To(specs.BeNil())
	req := &persistSnapshotRequest{
		snapshot: &egopb.Snapshot{PersistenceId: "entity-1", SequenceNumber: 4, State: state},
		scope:    scope,
	}
	if withRetention {
		req.janitor = r.janitor
		req.retentionReq = &applyRetentionRequest{
			persistenceID:             "entity-1",
			eventsCounter:             4,
			snapshotInterval:          2,
			deleteEventsOnSnapshot:    true,
			deleteSnapshotsOnSnapshot: true,
			scope:                     scope,
		}
	}
	ctx.Expect(goakt.Tell(context.Background(), r.writer, req)).To(specs.BeNil())
}

// awaitRetention waits until each retention delete was called once.
func (r *snapshotWriterRig) awaitRetention(ctx *specs.Context) {
	ctx.Eventually(func() any { return r.calls.count(opDeleteEvents) }, specs.Equal(1), snapshotsPoll...)
	ctx.Eventually(func() any { return r.calls.count(opDeleteSnapshots) }, specs.Equal(1), snapshotsPoll...)
}

// retentionOrExtraWrites reports whether a retention delete was observed or
// the snapshot was written more often than the retry budget allows.
func (r *snapshotWriterRig) retentionOrExtraWrites() any {
	return r.calls.anyRetention() || r.calls.count(opWriteSnapshot) > defaultMaxRetries+1
}

// scopeOf projects a recorded call to the scope it received.
func scopeOf(c storeCall) persistence.Scope { return c.scope }

func TestSnapshotsWriterContract(t *testing.T) {
	specs.Describe(t, "snapshotsWriterActor hands the stores and the janitor the right calls", func(s *specs.Spec) {
		s.It("the scope reaches the snapshot write and the retention deletes unchanged", func(ctx *specs.Context) {
			scope, err := persistence.NewTenantScope("tenant-a")
			ctx.Expect(err).To(specs.BeNil())
			rig := newSnapshotWriterRig(ctx, nil, nil, nil)

			rig.persist(ctx, scope, true)

			rig.awaitRetention(ctx)
			ctx.Expect(rig.calls.count(opWriteSnapshot)).To(specs.Equal(1))
			ctx.Expect(rig.calls.snapshot()).To(specs.EveryElement(specs.Project("scope", scopeOf, specs.Equal(scope))))
		})

		s.It("retention runs after the snapshot write", func(ctx *specs.Context) {
			gate := newSnapshotWriteGate()
			rig := newSnapshotWriterRig(ctx, nil, nil, gate)

			rig.persist(ctx, persistence.Unscoped(), true)

			// while the write is held, no retention may be forwarded
			ctx.Consistently(func() any { return rig.calls.anyRetention() }, specs.BeFalse(), specs.WithTimeout(snapshotsSettle))
			gate.release()

			rig.awaitRetention(ctx)
			snapshot := rig.calls.indexOf(opWriteSnapshot, 4)
			ctx.Expect(snapshot).To(specs.BeGreaterThanOrEqual(0))
			ctx.Expect(rig.calls.indexOf(opDeleteEvents, 4)).To(specs.BeGreaterThan(snapshot))
			// the previous snapshot is the current sequence minus the interval
			ctx.Expect(rig.calls.indexOf(opDeleteSnapshots, 2)).To(specs.BeGreaterThan(snapshot))
		})

		s.It("a failed snapshot write is retried and then forwards no retention", func(ctx *specs.Context) {
			rig := newSnapshotWriterRig(ctx, errSnapshotWrite, nil, nil)

			rig.persist(ctx, persistence.Unscoped(), true)

			ctx.Eventually(func() any { return rig.calls.count(opWriteSnapshot) }, specs.Equal(defaultMaxRetries+1), snapshotsPoll...)
			ctx.Consistently(rig.retentionOrExtraWrites, specs.BeFalse(), specs.WithTimeout(time.Second))
		})

		s.It("an encryption failure writes nothing and forwards no retention", func(ctx *specs.Context) {
			encryptorCtrl := mock.NewController(ctx)
			encryptorCtrl.Method("Encrypt").Expect(mock.Any(), "entity-1", mock.Any()).Return(nil, "", errEncrypt)
			rig := newSnapshotWriterRig(ctx, nil, extensions.NewEncryptor(enginetest.NewEncryptorMock(encryptorCtrl)), nil)

			rig.persist(ctx, persistence.Unscoped(), true)

			ctx.Eventually(calls(encryptorCtrl, "Encrypt"), specs.Equal(1), snapshotsPoll...)
			ctx.Consistently(func() any { return rig.calls.snapshot() }, specs.BeEmpty(), specs.WithTimeout(time.Second))
		})

		s.It("a nil retention request writes the snapshot and triggers no deletes", func(ctx *specs.Context) {
			rig := newSnapshotWriterRig(ctx, nil, nil, nil)

			rig.persist(ctx, persistence.Unscoped(), false)

			ctx.Eventually(func() any { return rig.calls.count(opWriteSnapshot) }, specs.Equal(1), snapshotsPoll...)
			ctx.Consistently(func() any { return rig.calls.anyRetention() }, specs.BeFalse(), specs.WithTimeout(time.Second))
		})
	})
}
