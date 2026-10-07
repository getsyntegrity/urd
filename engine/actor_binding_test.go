package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/egopb"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
)

type namedAccountDefinition struct {
	domainOnlyEventSourced
	definition string
}

func (b *namedAccountDefinition) DefinitionID() string { return b.definition }

func TestSpawnVerifiesDefinition(t *testing.T) {
	specs.Describe(t, "spawn identity includes the behavior definition", func(s *specs.Spec) {
		s.It("reuses a matching definition and rejects another without replacing the actor", func(sc *specs.Context) {
			bg := context.Background()
			store := connectedEventsStore(sc)
			e := newTestEngine(sc.T, "binding-definition", store, WithLogger(DiscardLogger))
			sc.Expect(e.Start(bg)).To(specs.BeNil())
			id := "definition-1"
			a := &namedAccountDefinition{domainOnlyEventSourced: domainOnlyEventSourced{id: id}, definition: "accounts/v1"}
			sc.Expect(e.SpawnEventSourced(bg, a)).To(specs.BeNil())
			first, err := e.actorSystem.Load().sys.ActorOf(bg, id)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(e.SpawnEventSourced(bg, a)).To(specs.BeNil())
			other := &namedAccountDefinition{domainOnlyEventSourced: domainOnlyEventSourced{id: id}, definition: "orders/v1"}
			err = e.SpawnEventSourced(bg, other)
			var collision *ActorIdentityError
			sc.Expect(errors.As(err, &collision)).To(specs.BeTrue())
			sc.Expect(err).To(specs.MatchError(ErrSpawnIdentityMismatch))
			after, err := e.actorSystem.Load().sys.ActorOf(bg, id)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(after.Equals(first)).To(specs.BeTrue())
			_, _, err = e.SendCommand(bg, id, &egopb.ActorBindingQuery{}, waitTimeout)
			sc.Expect(err).To(specs.MatchError(ErrNotACommand))
		})
	})
}

func TestActorNamespacesOnSharedSystem(t *testing.T) {
	specs.Describe(t, "explicit namespaces separate engine addresses", func(s *specs.Spec) {
		s.It("allows distinct families in different namespaces and keeps persistence IDs unchanged", func(sc *specs.Context) {
			bg := context.Background()
			es, ds := connectedEventsStore(sc), connectedDurableStore(sc)
			a := newTestEngine(sc.T, "shared-binding", es, WithLogger(DiscardLogger), WithStateStore(ds), WithActorNamespace("accounts"))
			sc.Expect(a.Start(bg)).To(specs.BeNil())
			cfg := NewConfig(es, WithLogger(DiscardLogger), WithStateStore(ds), WithActorNamespace("payments"))
			b, err := NewEngine(a.actorSystem.Load().sys, cfg)
			sc.Expect(err).To(specs.BeNil())
			sc.Cleanup(func() { _ = b.Stop(bg) })
			sc.Expect(b.Start(bg)).To(specs.BeNil())
			id := "shared-id"
			sc.Expect(a.SpawnEventSourced(bg, &domainOnlyEventSourced{id: id})).To(specs.BeNil())
			sc.Expect(b.SpawnDurableState(bg, &domainOnlyDurableState{id: id})).To(specs.BeNil())
			_, rev, err := a.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 5}, waitTimeout)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(rev).ToEqual(uint64(1))
			_, rev, err = b.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 50}, waitTimeout)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(rev).ToEqual(uint64(1))
			event, err := es.GetLatestEvent(bg, persistence.Unscoped(), id)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(event.GetPersistenceId()).ToEqual(id)
			state, err := ds.GetLatestState(bg, persistence.Unscoped(), id)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(state.GetPersistenceId()).ToEqual(id)
			nameA, err := a.actorName("", id)
			sc.Expect(err).To(specs.BeNil())
			nameB, err := b.actorName("", id)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(nameA == nameB).To(specs.BeFalse())
		})
	})
}
