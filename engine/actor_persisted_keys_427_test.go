package engine

import (
	"context"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/testkit"
)

// TestActorPersistedKeys427 pins that the actor address (including an explicit
// namespace) never leaks into persisted keys: data written by an engine with
// the legacy bare-ID address is recovered, under the same scope and ID, by an
// engine that addresses its actors under a namespace.
func TestActorPersistedKeys427(t *testing.T) {
	specs.Describe(t, "persisted keys across actor addressing (#427)", func(s *specs.Spec) {
		bg := context.Background()
		balance := func(ctx *specs.Context, state State) float64 {
			account, ok := state.(*testpb.Account)
			if !ok {
				ctx.T.Fatalf("state is %T, want *testpb.Account", state)
			}
			return account.GetAccountBalance()
		}

		s.It("event-sourced events and snapshots are recovered under the same scope and ID", func(ctx *specs.Context) {
			es := connectedEventsStore(ctx)
			snaps := testkit.NewSnapshotStore()
			ctx.Expect(snaps.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = snaps.Disconnect(bg) })

			id := "27270000-0000-0000-0000-000000000001"
			before := newTestEngine(ctx.T, "persistedkeys427esa", es, WithLogger(DiscardLogger), WithSnapshotStore(snaps))
			ctx.Expect(before.Start(bg)).To(specs.BeNil())
			ctx.Expect(before.SpawnEventSourced(bg, NewAccountEventSourcedBehavior(id), WithSnapshotInterval(1))).To(specs.BeNil())
			_, rev, err := before.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 5}, waitTimeout)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(rev).ToEqual(uint64(1))
			_, rev, err = before.SendCommand(bg, id, &testpb.CreditAccount{AccountId: id, Balance: 3}, waitTimeout)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(rev).ToEqual(uint64(2))
			ctx.Expect(before.Stop(bg)).To(specs.BeNil())

			// The records sit under the persistence ID, with no namespace.
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

			after := newTestEngine(ctx.T, "persistedkeys427esb", es, WithLogger(DiscardLogger),
				WithSnapshotStore(snaps), WithActorNamespace("tenant-app"))
			ctx.Expect(after.Start(bg)).To(specs.BeNil())
			ctx.Expect(after.SpawnEventSourced(bg, NewAccountEventSourcedBehavior(id), WithSnapshotInterval(1))).To(specs.BeNil())
			state, rev, err := after.SendCommand(bg, id, &testpb.CreditAccount{AccountId: id, Balance: 2}, waitTimeout)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(rev).ToEqual(uint64(3))
			ctx.Expect(balance(ctx, state)).ToEqual(float64(10))

			// The namespace did not open a second journal.
			latest, err := es.GetLatestEvent(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest.GetPersistenceId()).ToEqual(id)
			ctx.Expect(latest.GetSequenceNumber()).ToEqual(uint64(3))
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
			ctx.Expect(before.Stop(bg)).To(specs.BeNil())

			stored, err := ds.GetLatestState(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			if stored == nil {
				ctx.T.Fatalf("no durable state stored under %s", id)
			}
			ctx.Expect(stored.GetPersistenceId()).ToEqual(id)
			ctx.Expect(stored.GetVersionNumber()).ToEqual(uint64(1))

			after := newTestEngine(ctx.T, "persistedkeys427dsb", es, WithLogger(DiscardLogger),
				WithStateStore(ds), WithActorNamespace("tenant-app"))
			ctx.Expect(after.Start(bg)).To(specs.BeNil())
			ctx.Expect(after.SpawnDurableState(bg, NewAccountDurableStateBehavior(id))).To(specs.BeNil())
			state, rev, err := after.SendCommand(bg, id, &testpb.CreditAccount{AccountId: id, Balance: 2}, waitTimeout)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(rev).ToEqual(uint64(2))
			ctx.Expect(balance(ctx, state)).ToEqual(float64(7))

			latest, err := ds.GetLatestState(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latest.GetPersistenceId()).ToEqual(id)
			ctx.Expect(latest.GetVersionNumber()).ToEqual(uint64(2))
		})
	})
}
