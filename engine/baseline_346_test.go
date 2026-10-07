package engine

import (
	"context"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
)

// TestBaseline346 retains the standalone baseline and the regression that
// incompatible B4 spawns fail without replacing the actor.
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
				state, rev, err := e.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 5}, waitTimeout)
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(rev).ToEqual(uint64(1))
				account, ok := state.(*testpb.Account)
				if !ok {
					ctx.T.Fatalf("write %s: state is %T, want *testpb.Account", id, state)
				}
				ctx.Expect(account.GetAccountBalance()).ToEqual(float64(5))
			}

			// The stored shard is 0. This does not prove the shard came from
			// Partition: 0 is also the zero value of the field, so it would
			// read the same if the actors never wrote it.
			event, err := es.GetLatestEvent(bg, persistence.Unscoped(), esID)
			ctx.Expect(err).To(specs.BeNil())
			if event == nil {
				ctx.T.Fatalf("event store has no event for %s", esID)
			}
			ctx.Expect(event.GetShard()).ToEqual(uint64(0))

			durable, err := ds.GetLatestState(bg, persistence.Unscoped(), dsID)
			ctx.Expect(err).To(specs.BeNil())
			if durable == nil {
				ctx.T.Fatalf("durable store has no state for %s", dsID)
			}
			ctx.Expect(durable.GetShard()).ToEqual(uint64(0))
		})

		// B4: the command API cannot distinguish actor families at one ID.
		// Incompatible reuse must return a typed error, preserving the original.
		s.It("B4: incompatible spawns return typed errors and leave the original actor unchanged", func(ctx *specs.Context) {
			es, ds := connectedEventsStore(ctx), connectedDurableStore(ctx)
			e := newTestEngine(ctx.T, "baseline346b4", es, WithLogger(DiscardLogger), WithStateStore(ds))
			ctx.Expect(e.Start(bg)).To(specs.BeNil())

			id := "11111111-2222-3333-4444-555555555555"
			sys := e.actorSystem.Load().sys
			ctx.Expect(e.SpawnEventSourced(bg, &domainOnlyEventSourced{id: id})).To(specs.BeNil())

			// The name resolves to the event-sourced actor. The check is by
			// name, not by a count of the actors in the system, so it does not
			// depend on what else starts or stops there.
			first, err := sys.ActorOf(bg, id)
			ctx.Expect(err).To(specs.BeNil())
			if _, ok := first.Actor().(*EventSourcedActor); !ok {
				ctx.T.Fatalf("actor %s is %T, want *EventSourcedActor", id, first.Actor())
			}

			// Incompatible reuse must fail without altering the first actor.
			ctx.Expect(e.SpawnDurableState(bg, &domainOnlyDurableState{id: id})).To(specs.MatchError(ErrSpawnIdentityMismatch))
			ctx.Expect(e.SpawnSaga(bg, &domainOnlySaga{id: id}, 0)).To(specs.MatchError(ErrSpawnIdentityMismatch))

			// The name still resolves to the same, still running,
			// event-sourced actor: neither spawn replaced it, and neither
			// registered an actor of its own under that name.
			second, err := sys.ActorOf(bg, id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(second.Equals(first)).ToEqual(true)
			ctx.Expect(second.IsRunning()).ToEqual(true)
			if _, ok := second.Actor().(*EventSourcedActor); !ok {
				ctx.T.Fatalf("actor %s is %T after the later spawns, want *EventSourcedActor", id, second.Actor())
			}

			// The command is handled as an event-sourced entity (an event is
			// stored) and the durable store stays empty, which is consistent
			// with the durable-state spawn having created no actor of its own.
			_, rev, err := e.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 3}, waitTimeout)
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
