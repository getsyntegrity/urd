package engine

import (
	"context"
	"testing"
	"time"

	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/testkit"
)

// These two tests are characterization evidence for #346 (I-00). They record
// what develop does today; they do not endorse it. The fix for B4 is tracked
// separately and will change the assertions in
// TestBaseline346CrossKindSpawnSharesOneActorName.

// TestBaseline346PartitionStandalone records B2: Urd calls
// ActorSystem.Partition from the entity actors' start path. In a standalone
// (non-cluster) GoAkt system the fork returns 0, so the engine runs the full
// write/read path with in-memory stores and no error.
func TestBaseline346PartitionStandalone(t *testing.T) {
	bg := context.Background()
	es, ds := testkit.NewEventsStore(), testkit.NewDurableStore()
	if err := es.Connect(bg); err != nil {
		t.Fatal(err)
	}
	if err := ds.Connect(bg); err != nil {
		t.Fatal(err)
	}
	e := newTestEngine(t, "baseline346partition", es, WithLogger(DiscardLogger), WithStateStore(ds))
	if err := e.Start(bg); err != nil {
		t.Fatal(err)
	}
	if e.actorSystem.Load().sys.InCluster() {
		t.Fatal("expected a standalone actor system")
	}
	if got := e.actorSystem.Load().sys.Partition("any-name"); got != 0 {
		t.Fatalf("Partition outside a cluster = %d, want 0", got)
	}

	esID := "aaaaaaaa-0000-0000-0000-000000000001"
	dsID := "aaaaaaaa-0000-0000-0000-000000000002"
	if err := e.SpawnEventSourced(bg, &domainOnlyEventSourced{id: esID}); err != nil {
		t.Fatalf("spawn event-sourced: %v", err)
	}
	if err := e.SpawnDurableState(bg, &domainOnlyDurableState{id: dsID}); err != nil {
		t.Fatalf("spawn durable-state: %v", err)
	}
	for _, id := range []string{esID, dsID} {
		state, rev, err := e.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 5}, time.Minute)
		if err != nil || rev != 1 {
			t.Fatalf("write %s: rev=%d err=%v", id, rev, err)
		}
		account, ok := state.(*testpb.Account)
		if !ok {
			t.Fatalf("write %s: state is %T, want *testpb.Account", id, state)
		}
		if got := account.GetAccountBalance(); got != 5 {
			t.Fatalf("write %s: balance=%v", id, got)
		}
	}
}

// TestBaseline346CrossKindSpawnSharesOneActorName records B4: with no tenant
// resolver the actor name is the bare entity ID, so an event-sourced entity,
// a durable-state entity and a saga with the same ID address one actor.
func TestBaseline346CrossKindSpawnSharesOneActorName(t *testing.T) {
	bg := context.Background()
	es, ds := testkit.NewEventsStore(), testkit.NewDurableStore()
	if err := es.Connect(bg); err != nil {
		t.Fatal(err)
	}
	if err := ds.Connect(bg); err != nil {
		t.Fatal(err)
	}
	e := newTestEngine(t, "baseline346b4", es, WithLogger(DiscardLogger), WithStateStore(ds))
	if err := e.Start(bg); err != nil {
		t.Fatal(err)
	}

	id := "11111111-2222-3333-4444-555555555555"
	if err := e.SpawnEventSourced(bg, &domainOnlyEventSourced{id: id}); err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	// Observed today: both later spawns return nil instead of a typed error.
	if err := e.SpawnDurableState(bg, &domainOnlyDurableState{id: id}); err != nil {
		t.Fatalf("durable-state spawn under an event-sourced name: %v", err)
	}
	if err := e.SpawnSaga(bg, &domainOnlySaga{id: id}, 0); err != nil {
		t.Fatalf("saga spawn under an event-sourced name: %v", err)
	}

	// One actor exists under the name, so the command is answered by the
	// first (event-sourced) one; the durable-state entity was never created.
	_, rev, err := e.SendCommand(bg, id, &testpb.CreateAccount{AccountBalance: 3}, time.Minute)
	if err != nil || rev != 1 {
		t.Fatalf("command to the shared ID: revision=%d err=%v", rev, err)
	}
	event, err := es.GetLatestEvent(bg, persistence.Unscoped(), id)
	if err != nil || event == nil {
		t.Fatalf("event store has no event for %s: event=%v err=%v", id, event, err)
	}
	durable, err := ds.GetLatestState(bg, persistence.Unscoped(), id)
	if err != nil {
		t.Fatalf("durable store lookup for %s: %v", id, err)
	}
	if durable != nil {
		t.Fatalf("durable store has state for %s; the durable-state actor handled the command", id)
	}
}
