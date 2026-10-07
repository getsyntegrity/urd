package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/egopb"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/testkit"
)

// snapshotRead is one GetLatestSnapshot call seen by readRecordingSnapshotStore.
type snapshotRead struct {
	scope persistence.Scope
	id    string
	seq   uint64 // 0 when no snapshot was found
}

// readRecordingSnapshotStore wraps an in-memory snapshot store and records
// what each recovery read asked for and what it got back. A recovery that
// replayed the journal after a miss on the snapshot key would still reach the
// same state, so only the read itself proves the snapshot was found.
type readRecordingSnapshotStore struct {
	*testkit.SnapshotStore

	mu    sync.Mutex
	reads []snapshotRead
}

func (s *readRecordingSnapshotStore) GetLatestSnapshot(ctx context.Context, scope persistence.Scope, persistenceID string) (*egopb.Snapshot, error) {
	snapshot, err := s.SnapshotStore.GetLatestSnapshot(ctx, scope, persistenceID)
	s.mu.Lock()
	s.reads = append(s.reads, snapshotRead{scope: scope, id: persistenceID, seq: snapshot.GetSequenceNumber()})
	s.mu.Unlock()
	return snapshot, err
}

// readsOf returns the recorded reads of the Unscoped snapshot of id.
func (s *readRecordingSnapshotStore) readsOf(id string) []snapshotRead {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []snapshotRead
	for _, read := range s.reads {
		if read.id == id && read.scope.Equal(persistence.Unscoped()) {
			out = append(out, read)
		}
	}
	return out
}

// TestActorPersistedKeys427 pins that the actor address (including an explicit
// namespace) never leaks into persisted keys: data written by an engine with
// the legacy bare-ID address is recovered, under the same scope and ID, by an
// engine that addresses its actors under a namespace.
func TestActorPersistedKeys427(t *testing.T) {
	specs.Describe(t, "persisted keys across actor addressing (#427)", func(s *specs.Spec) {
		bg := context.Background()
		const namespace = "tenant-app"

		balance := func(ctx *specs.Context, state State) float64 {
			account, ok := state.(*testpb.Account)
			if !ok {
				ctx.T.Fatalf("state is %T, want *testpb.Account", state)
			}
			return account.GetAccountBalance()
		}
		snapshotBalance := func(ctx *specs.Context, snapshot *egopb.Snapshot) float64 {
			account := new(testpb.Account)
			ctx.Expect(snapshot.GetState().UnmarshalTo(account)).To(specs.BeNil())
			return account.GetAccountBalance()
		}
		// Snapshots are written by a separate actor, so the latest sequence
		// number is polled rather than read once.
		latestSnapshotSeq := func(snaps persistence.SnapshotStore, id string) func() any {
			return func() any {
				snapshot, err := snaps.GetLatestSnapshot(bg, persistence.Unscoped(), id)
				if err != nil {
					return err
				}
				return snapshot.GetSequenceNumber()
			}
		}
		poll := []specs.PollOption{specs.WithTimeout(10 * time.Second), specs.WithInterval(20 * time.Millisecond)}

		s.It("event-sourced events and snapshots are recovered under the same scope and ID", func(ctx *specs.Context) {
			es := connectedEventsStore(ctx)
			snaps := testkit.NewSnapshotStore()
			ctx.Expect(snaps.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = snaps.Disconnect(bg) })

			id := "27270000-0000-0000-0000-000000000001"

			// newTestEngine registers t.Cleanup to stop each engine and its
			// actor system, so neither engine needs a cleanup here.
			before := newTestEngine(ctx.T, "persistedkeys427esa", es, WithLogger(DiscardLogger), WithSnapshotStore(snaps))
			ctx.Expect(before.Start(bg)).To(specs.BeNil())
			ctx.Expect(before.SpawnEventSourced(bg, NewAccountEventSourcedBehavior(id), WithSnapshotInterval(1))).To(specs.BeNil())
			_, rev, err := before.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 5}, waitTimeout)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(rev).ToEqual(uint64(1))
			_, rev, err = before.SendCommand(bg, id, &testpb.CreditAccount{AccountId: id, Balance: 3}, waitTimeout)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(rev).ToEqual(uint64(2))

			// The first engine addresses its actor by the bare ID.
			beforeName, err := before.actorName("", id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(beforeName).ToEqual(id)
			_, err = before.actorSystem.Load().sys.ActorOf(bg, id)
			ctx.Expect(err).To(specs.BeNil())

			ctx.Eventually(latestSnapshotSeq(snaps, id), specs.Equal(uint64(2)), poll...)
			ctx.Expect(before.Stop(bg)).To(specs.BeNil())

			// The records sit under the scope and persistence ID, with no namespace.
			event, err := es.GetLatestEvent(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			if event == nil {
				ctx.T.Fatalf("no event stored under %s", id)
			}
			ctx.Expect(event.GetPersistenceId()).ToEqual(id)
			ctx.Expect(event.GetSequenceNumber()).ToEqual(uint64(2))
			snapshot, err := snaps.GetLatestSnapshot(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			if snapshot == nil {
				ctx.T.Fatalf("no snapshot stored under %s", id)
			}
			ctx.Expect(snapshot.GetPersistenceId()).ToEqual(id)
			ctx.Expect(snapshot.GetSequenceNumber()).ToEqual(uint64(2))
			ctx.Expect(snapshotBalance(ctx, snapshot)).ToEqual(float64(8))

			spy := &readRecordingSnapshotStore{SnapshotStore: snaps}
			after := newTestEngine(ctx.T, "persistedkeys427esb", es, WithLogger(DiscardLogger),
				WithSnapshotStore(spy), WithActorNamespace(namespace))
			ctx.Expect(after.Start(bg)).To(specs.BeNil())
			ctx.Expect(after.SpawnEventSourced(bg, NewAccountEventSourcedBehavior(id), WithSnapshotInterval(1))).To(specs.BeNil())

			// The second engine addresses its actor under the namespace, and
			// that address differs from the first engine's.
			afterName, err := after.actorName("", id)
			ctx.Expect(err).To(specs.BeNil())
			if afterName == beforeName {
				ctx.T.Fatalf("namespaced address %q equals the legacy address", afterName)
			}
			sys := after.actorSystem.Load().sys
			named, err := sys.ActorOf(bg, afterName)
			ctx.Expect(err).To(specs.BeNil())
			if _, ok := named.Actor().(*EventSourcedActor); !ok {
				ctx.T.Fatalf("actor %s is %T, want *EventSourcedActor", afterName, named.Actor())
			}
			_, err = sys.ActorOf(bg, id)
			ctx.Expect(err == nil).To(specs.BeFalse())

			// Recovery read the snapshot under the original scope and ID, and
			// found the one the first engine wrote. A read under another key
			// would return nothing here, even though replaying the journal
			// would still reach the same state.
			reads := spy.readsOf(id)
			if len(reads) == 0 {
				ctx.T.Fatalf("recovery did not read the snapshot of %s", id)
			}
			for _, read := range reads {
				ctx.Expect(read.seq).ToEqual(uint64(2))
			}
			ctx.Expect(len(spy.readsOf(afterName))).ToEqual(0)

			state, rev, err := after.SendCommand(bg, id, &testpb.CreditAccount{AccountId: id, Balance: 2}, waitTimeout)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(rev).ToEqual(uint64(3))
			ctx.Expect(balance(ctx, state)).ToEqual(float64(10))

			// The next snapshot lands under the same key, with the new state.
			ctx.Eventually(latestSnapshotSeq(snaps, id), specs.Equal(uint64(3)), poll...)
			snapshot, err = snaps.GetLatestSnapshot(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(snapshot.GetPersistenceId()).ToEqual(id)
			ctx.Expect(snapshotBalance(ctx, snapshot)).ToEqual(float64(10))

			// The namespace did not open a second journal or snapshot series.
			latest, err := es.GetLatestEvent(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest.GetPersistenceId()).ToEqual(id)
			ctx.Expect(latest.GetSequenceNumber()).ToEqual(uint64(3))
			namespacedEvent, err := es.GetLatestEvent(bg, persistence.Unscoped(), afterName)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(namespacedEvent == nil).To(specs.BeTrue())
			namespacedSnapshot, err := snaps.GetLatestSnapshot(bg, persistence.Unscoped(), afterName)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(namespacedSnapshot == nil).To(specs.BeTrue())
		})

		s.It("durable state is recovered under the same scope and ID", func(ctx *specs.Context) {
			es, ds := connectedEventsStore(ctx), connectedDurableStore(ctx)

			id := "27270000-0000-0000-0000-000000000002"
			before := newTestEngine(ctx.T, "persistedkeys427dsa", es, WithLogger(DiscardLogger), WithStateStore(ds))
			ctx.Expect(before.Start(bg)).To(specs.BeNil())
			ctx.Expect(before.SpawnDurableState(bg, NewAccountDurableStateBehavior(id))).To(specs.BeNil())
			_, rev, err := before.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 5}, waitTimeout)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(rev).ToEqual(uint64(1))

			beforeName, err := before.actorName("", id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(beforeName).ToEqual(id)
			ctx.Expect(before.Stop(bg)).To(specs.BeNil())

			stored, err := ds.GetLatestState(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			if stored == nil {
				ctx.T.Fatalf("no durable state stored under %s", id)
			}
			ctx.Expect(stored.GetPersistenceId()).ToEqual(id)
			ctx.Expect(stored.GetVersionNumber()).ToEqual(uint64(1))

			after := newTestEngine(ctx.T, "persistedkeys427dsb", es, WithLogger(DiscardLogger),
				WithStateStore(ds), WithActorNamespace(namespace))
			ctx.Expect(after.Start(bg)).To(specs.BeNil())
			ctx.Expect(after.SpawnDurableState(bg, NewAccountDurableStateBehavior(id))).To(specs.BeNil())

			afterName, err := after.actorName("", id)
			ctx.Expect(err).To(specs.BeNil())
			if afterName == beforeName {
				ctx.T.Fatalf("namespaced address %q equals the legacy address", afterName)
			}
			sys := after.actorSystem.Load().sys
			named, err := sys.ActorOf(bg, afterName)
			ctx.Expect(err).To(specs.BeNil())
			if _, ok := named.Actor().(*DurableStateActor); !ok {
				ctx.T.Fatalf("actor %s is %T, want *DurableStateActor", afterName, named.Actor())
			}
			_, err = sys.ActorOf(bg, id)
			ctx.Expect(err == nil).To(specs.BeFalse())

			state, rev, err := after.SendCommand(bg, id, &testpb.CreditAccount{AccountId: id, Balance: 2}, waitTimeout)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(rev).ToEqual(uint64(2))
			ctx.Expect(balance(ctx, state)).ToEqual(float64(7))

			latest, err := ds.GetLatestState(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest.GetPersistenceId()).ToEqual(id)
			ctx.Expect(latest.GetVersionNumber()).ToEqual(uint64(2))
			namespaced, err := ds.GetLatestState(bg, persistence.Unscoped(), afterName)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(namespaced == nil).To(specs.BeTrue())
		})
	})
}
