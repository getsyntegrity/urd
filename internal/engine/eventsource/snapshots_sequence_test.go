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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

// Store operation names recorded by the snapshot contract tests.
const (
	opWriteEvents     = "WriteEvents"
	opWriteSnapshot   = "WriteSnapshot"
	opDeleteEvents    = "DeleteEvents"
	opDeleteSnapshots = "DeleteSnapshots"
)

// storeCall is one call observed on the events store or the snapshot store:
// which operation, the scope it received and the sequence number it carried
// (the first event of a write, the snapshot's sequence number, or the upper
// bound of a delete).
type storeCall struct {
	op    string
	scope persistence.Scope
	seqNr uint64
}

// storeCalls records, in arrival order, the calls of both stores.
type storeCalls struct {
	mu    sync.Mutex
	calls []storeCall
}

func (s *storeCalls) add(op string, scope persistence.Scope, seqNr uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, storeCall{op: op, scope: scope, seqNr: seqNr})
}

func (s *storeCalls) snapshot() []storeCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]storeCall(nil), s.calls...)
}

// indexOf returns the position of the first call matching op and seqNr, or -1.
func (s *storeCalls) indexOf(op string, seqNr uint64) int {
	for i, call := range s.snapshot() {
		if call.op == op && call.seqNr == seqNr {
			return i
		}
	}
	return -1
}

func (s *storeCalls) has(op string, seqNr uint64) bool {
	return s.indexOf(op, seqNr) >= 0
}

func (s *storeCalls) count(op string) int {
	n := 0
	for _, call := range s.snapshot() {
		if call.op == op {
			n++
		}
	}
	return n
}

// anyRetention reports whether any retention delete was observed.
func (s *storeCalls) anyRetention() bool {
	return s.count(opDeleteEvents) > 0 || s.count(opDeleteSnapshots) > 0
}

// loggingEventsStore records WriteEvents and DeleteEvents into a shared log
// and optionally fails the write.
type loggingEventsStore struct {
	persistence.EventsStore
	calls    *storeCalls
	writeErr error
}

func (x *loggingEventsStore) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	var first uint64
	if len(events) > 0 {
		first = events[0].GetSequenceNumber()
	}
	x.calls.add(opWriteEvents, scope, first)
	if x.writeErr != nil {
		return x.writeErr
	}
	return x.EventsStore.WriteEvents(ctx, scope, events, precondition)
}

func (x *loggingEventsStore) DeleteEvents(ctx context.Context, scope persistence.Scope, persistenceID string, toSequenceNumber uint64) error {
	x.calls.add(opDeleteEvents, scope, toSequenceNumber)
	return x.EventsStore.DeleteEvents(ctx, scope, persistenceID, toSequenceNumber)
}

// loggingSnapshotStore records WriteSnapshot and DeleteSnapshots into the same
// shared log and optionally fails the write.
// hold, when set, keeps a WriteSnapshot from being recorded until it is closed,
// so a retention forwarded too early would be observed ahead of the write.
type loggingSnapshotStore struct {
	persistence.SnapshotStore
	calls    *storeCalls
	writeErr error
	hold     <-chan struct{}
}

func (x *loggingSnapshotStore) WriteSnapshot(ctx context.Context, scope persistence.Scope, snapshot *egopb.Snapshot) error {
	if x.hold != nil {
		select {
		case <-x.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	x.calls.add(opWriteSnapshot, scope, snapshot.GetSequenceNumber())
	if x.writeErr != nil {
		return x.writeErr
	}
	return x.SnapshotStore.WriteSnapshot(ctx, scope, snapshot)
}

func (x *loggingSnapshotStore) DeleteSnapshots(ctx context.Context, scope persistence.Scope, persistenceID string, toSequenceNumber uint64) error {
	x.calls.add(opDeleteSnapshots, scope, toSequenceNumber)
	return x.SnapshotStore.DeleteSnapshots(ctx, scope, persistenceID, toSequenceNumber)
}

// errEventsWrite is what a failing events write returns.
var errEventsWrite = errors.New("events write failed")

// sequenceRig is one entity actor wired to the logging stores.
type sequenceRig struct {
	calls         *storeCalls
	pid           *goakt.PID
	persistenceID string
}

// spawnSequenceRig starts an event-sourced entity with a snapshot interval of 1
// and a retention policy that deletes both events and snapshots. A non-empty
// tenant makes the entity tenant-scoped; a non-nil writeErr fails every events
// write. The actor system and the stores stop when the case ends.
func spawnSequenceRig(ctx *specs.Context, tenant string, writeErr error) *sequenceRig {
	bg := context.Background()

	calls := new(storeCalls)
	baseEvents := connectedEventsStore(ctx)
	baseSnapshots := connectedSnapshotStore(ctx)
	stream := newEventsStream(ctx)

	exts := []extension.Extension{
		extensions.NewEventsStore(retentionFixture{&loggingEventsStore{EventsStore: baseEvents, calls: calls, writeErr: writeErr}}),
		extensions.NewEventsStream(stream),
		extensions.NewSnapshotStore(&loggingSnapshotStore{SnapshotStore: baseSnapshots, calls: calls}),
	}

	persistenceID := uuid.NewString()
	behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)
	entityCfg := &extensions.EntityConfig{
		SnapshotInterval:          1,
		HasRetentionPolicy:        true,
		DeleteEventsOnSnapshot:    true,
		DeleteSnapshotsOnSnapshot: true,
	}
	deps := []extension.Dependency{behavior, entityCfg}
	if tenant != "" {
		exts = append(exts, extensions.NewTenancyMarker(false))
		deps = append(deps, extensions.NewEntityTenantScope(tenant))
	}

	actorSystem := startSnapshotSystemWith(ctx, "SnapshotSequenceSystem", 3, exts...)
	pid, err := actorSystem.Spawn(bg, behavior.ID(), New(),
		goakt.WithDependencies(deps...), goakt.WithLongLived(), goakt.WithStashing())
	ctx.Expect(err).To(specs.BeNil())
	return &sequenceRig{calls: calls, pid: pid, persistenceID: persistenceID}
}

// ask sends msg to the entity and returns its command reply.
func (r *sequenceRig) ask(ctx *specs.Context, callCtx context.Context, msg any) *egopb.CommandReply {
	reply, err := goakt.Ask(callCtx, r.pid, msg, 5*time.Second)
	ctx.Expect(err).To(specs.BeNil())
	commandReply, ok := reply.(*egopb.CommandReply)
	ctx.Expect(ok).To(specs.BeTrue())
	return commandReply
}

// replyKind names the concrete type of a command reply's payload, so a failure
// says which kind of reply came back.
func replyKind(reply *egopb.CommandReply) string { return fmt.Sprintf("%T", reply.GetReply()) }

const (
	stateReplyKind = "*egopb.CommandReply_StateReply"
	errorReplyKind = "*egopb.CommandReply_ErrorReply"
)

// runTwoCommands drives the entity across two snapshot boundaries and waits
// for the asynchronous retention of the second one. It returns every call the
// stores observed.
func (r *sequenceRig) runTwoCommands(ctx *specs.Context, callCtx context.Context) []storeCall {
	first := r.ask(ctx, callCtx, &testpb.CreateAccount{AccountBalance: 500})
	ctx.Expect(replyKind(first)).To(specs.Equal(stateReplyKind))
	second := r.ask(ctx, callCtx, &testpb.CreditAccount{AccountId: r.persistenceID, Balance: 100})
	ctx.Expect(replyKind(second)).To(specs.Equal(stateReplyKind))

	ctx.Eventually(func() any { return r.calls.has(opDeleteEvents, 2) }, specs.BeTrue(), snapshotsPoll...)
	ctx.Eventually(func() any { return r.calls.has(opDeleteSnapshots, 1) }, specs.BeTrue(), snapshotsPoll...)
	return r.calls.snapshot()
}

// expectOrdered asserts, for the second boundary, that the events write
// precedes the snapshot write and that both retention deletes follow it.
func (r *sequenceRig) expectOrdered(ctx *specs.Context) {
	write := r.calls.indexOf(opWriteEvents, 2)
	snapshot := r.calls.indexOf(opWriteSnapshot, 2)
	ctx.Expect(write).To(specs.BeGreaterThanOrEqual(0))
	ctx.Expect(snapshot).To(specs.BeGreaterThan(write))
	ctx.Expect(r.calls.indexOf(opDeleteEvents, 2)).To(specs.BeGreaterThan(snapshot))
	ctx.Expect(r.calls.indexOf(opDeleteSnapshots, 1)).To(specs.BeGreaterThan(snapshot))
}

// TestSnapshotAndRetentionObservableSequence pins what the stores can observe
// of the snapshot path after a command's event write: the events store is
// written first, the snapshot only after it, and the retention deletes only
// after the snapshot, all with the entity's scope. A failed events write never
// produces a snapshot.
//
// The snapshot interval is 1, so every command crosses a boundary; the second
// command is the one whose retention deletes both the events up to sequence 2
// and the previous snapshot (sequence 1).
func TestSnapshotAndRetentionObservableSequence(t *testing.T) {
	specs.Describe(t, "the stores observe the events write, then the snapshot, then the retention", func(s *specs.Spec) {
		s.It("events write, then snapshot write, then the retention deletes", func(ctx *specs.Context) {
			rig := spawnSequenceRig(ctx, "", nil)

			got := rig.runTwoCommands(ctx, context.Background())

			rig.expectOrdered(ctx)
			ctx.Expect(got).To(specs.EveryElement(specs.Project("scope", scopeOf, specs.Equal(persistence.Unscoped()))))
		})

		s.It("a failed events write never writes a snapshot nor deletes", func(ctx *specs.Context) {
			rig := spawnSequenceRig(ctx, "", errEventsWrite)

			reply := rig.ask(ctx, context.Background(), &testpb.CreateAccount{AccountBalance: 500})
			ctx.Expect(replyKind(reply)).To(specs.Equal(errorReplyKind))

			ctx.Expect(rig.calls.has(opWriteEvents, 1)).To(specs.BeTrue())
			ctx.Consistently(func() any {
				return rig.calls.count(opWriteSnapshot) > 0 || rig.calls.anyRetention()
			}, specs.BeFalse(), specs.WithTimeout(time.Second))
		})

		s.It("a tenant scope reaches the events write, the snapshot write and the retention deletes", func(ctx *specs.Context) {
			rig := spawnSequenceRig(ctx, "acme", nil)

			want, err := persistence.NewTenantScope("acme")
			ctx.Expect(err).To(specs.BeNil())
			tenantContext, err := tenancy.NewTenantContext("acme")
			ctx.Expect(err).To(specs.BeNil())
			tenantCtx, err := tenancy.Attach(context.Background(), tenantContext)
			ctx.Expect(err).To(specs.BeNil())

			got := rig.runTwoCommands(ctx, tenantCtx)

			rig.expectOrdered(ctx)
			for _, op := range []string{opWriteEvents, opWriteSnapshot, opDeleteEvents, opDeleteSnapshots} {
				var scopes []persistence.Scope
				for _, call := range got {
					if call.op == op {
						scopes = append(scopes, call.scope)
					}
				}
				// the operation was called, and every call carried the tenant scope
				ctx.Expect(scopes).To(specs.Not(specs.BeEmpty()))
				ctx.Expect(scopes).To(specs.EveryElement(specs.Equal(want)))
			}
		})
	})
}
