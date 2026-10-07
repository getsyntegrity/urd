package engine

import (
	"context"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"

	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
)

// TestBaseline346 is characterization evidence for #346 (I-00). It records
// what develop does today; it does not endorse it. The fix for B4 is tracked
// separately and will change the assertions of the B4 case.
func TestBaseline346(t *testing.T) {
	specs.Describe(t, "the baseline of develop (#346)", func(s *specs.Spec) {
		bg := context.Background()

		// B2: Urd calls ActorSystem.Partition from the entity actors' start
		// path. In a standalone (non-cluster) GoAkt system the fork returns 0,
		// so the engine runs the full write/read path with in-memory stores
		// and no error.
		s.It("B2: Partition outside a cluster returns 0 and the write path works", func(ctx *specs.Context) {
			es, ds := connectedEventsStore(ctx), connectedDurableStore(ctx)
			e := newTestEngine(ctx.T, "baseline346partition", es, WithLogger(DiscardLogger), WithStateStore(ds))
			ctx.Expect(e.Start(bg)).To(specs.BeNil())

			sys := e.actorSystem.Load().sys
			ctx.Expect(sys.InCluster()).ToEqual(false)
			ctx.Expect(sys.Partition("any-name")).ToEqual(uint64(0))

			esID := "aaaaaaaa-0000-0000-0000-000000000001"
			dsID := "aaaaaaaa-0000-0000-0000-000000000002"
			ctx.Expect(e.SpawnEventSourced(bg, &domainOnlyEventSourced{id: esID})).To(specs.BeNil())
			ctx.Expect(e.SpawnDurableState(bg, &domainOnlyDurableState{id: dsID})).To(specs.BeNil())

			for _, id := range []string{esID, dsID} {
				state, rev, err := e.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 5}, time.Minute)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(rev).ToEqual(uint64(1))
				account, ok := state.(*testpb.Account)
				if !ok {
					ctx.T.Fatalf("write %s: state is %T, want *testpb.Account", id, state)
				}
				ctx.Expect(account.GetAccountBalance()).ToEqual(float64(5))
			}
		})

		// B4: with no tenant resolver the actor name is the bare entity ID, so
		// a durable-state entity spawned with the ID of an event-sourced entity
		// returns no error and creates no durable-state state. The saga spawn
		// with the same ID is only checked for the absence of an error; what
		// it creates is not verified here.
		s.It("B4: a durable-state spawn with the ID of an event-sourced entity returns no error and creates no state", func(ctx *specs.Context) {
			es, ds := connectedEventsStore(ctx), connectedDurableStore(ctx)
			e := newTestEngine(ctx.T, "baseline346b4", es, WithLogger(DiscardLogger), WithStateStore(ds))
			ctx.Expect(e.Start(bg)).To(specs.BeNil())

			id := "11111111-2222-3333-4444-555555555555"
			ctx.Expect(e.SpawnEventSourced(bg, &domainOnlyEventSourced{id: id})).To(specs.BeNil())
			// Observed today: the later spawns return nil instead of a typed error.
			ctx.Expect(e.SpawnDurableState(bg, &domainOnlyDurableState{id: id})).To(specs.BeNil())
			// Saga: only the absence of an error is asserted; no actor is checked.
			ctx.Expect(e.SpawnSaga(bg, &domainOnlySaga{id: id}, 0)).To(specs.BeNil())

			// The command is handled as an event-sourced entity (an event is
			// stored) and the durable store stays empty, which is indirect
			// evidence that the durable-state spawn created no actor of its own.
			_, rev, err := e.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 3}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(rev).ToEqual(uint64(1))

			event, err := es.GetLatestEvent(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			if event == nil {
				ctx.T.Fatalf("event store has no event for %s", id)
			}

			durable, err := ds.GetLatestState(bg, persistence.Unscoped(), id)
			ctx.Expect(err).To(specs.BeNil())
			if durable != nil {
				ctx.T.Fatalf("durable store has state for %s; the durable-state actor handled the command", id)
			}
		})
	})
}
