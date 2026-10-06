//go:build journalexp

package eventstore_test

import (
	"context"
	"slices"
	"testing"

	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/persistence/conformance"
	"github.com/getsyntegrity/urd/persistence/postgres"
)

// The experiment of #332 on the real adapter: ExperimentalShardSerializedStore (persistence/postgres, build tag
// journalexp) implements the CURRENT persistence.EventsStore, signatures unchanged, with the offset meaning a
// journal position. Everything in this file and its siblings needs `-tags journalexp`.

// provisionExperimentalStore migrates the normal schema, then the TEST schema of the experiment.
func provisionExperimentalStore(dsn string) (*postgres.ExperimentalShardSerializedStore, error) {
	ctx := context.Background()
	migrating := postgres.NewExperimentalShardSerializedStore(dsn)
	if err := migrating.Connect(ctx); err != nil {
		return nil, err
	}
	defer func() { _ = migrating.Disconnect(ctx) }()
	if err := migrating.Migrate(ctx); err != nil {
		return nil, err
	}
	if err := migrating.ExperimentalMigrate(ctx); err != nil {
		return nil, err
	}
	return postgres.NewExperimentalShardSerializedStore(dsn), nil
}

// timestampOffsetChecks are the checks of the real suite that assert the offset of GetShardEvents/ShardOffsets IS
// a timestamp. The experimental store gives it another meaning (a journal position), so these three are expected
// to fail on it: they pin exactly what the SPI gate of #332 would change. They are not isolation failures.
var timestampOffsetChecks = []string{
	"ShardReads/GetShardEventsReturnsOnlyTheScopesEvents",
	"ShardReads/ShardOffsetsCoverOnlyTheScopesShards",
	"ShardReads/UnscopedNeverReadsATenantNamedUnscoped",
}

// TestExperimentalShardSerialized_Conformance runs the real EventsStore conformance suite, the one in which
// LateVisibleEventsBehindACommittedOffsetAreDelivered is red for #332 on the current adapter, against the
// experimental store, and pins the outcome exactly: that regression passes, the three timestamp-offset checks
// fail, and every other check passes. Any other result fails this test, so the list above cannot grow silently
// and the real regression is not excused.
func TestExperimentalShardSerialized_Conformance(t *testing.T) {
	results := conformance.CaptureEventsStoreChecks(func() persistence.EventsStore {
		store, err := provisionExperimentalStore(shared.NewDatabase(t))
		if err != nil {
			t.Fatalf("provision the experimental store: %v", err)
		}
		if err := store.Connect(context.Background()); err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { _ = store.Disconnect(context.Background()) })
		return store
	})
	if len(results) == 0 {
		t.Fatalf("the conformance suite returned no result")
	}
	failed := map[string]bool{}
	for _, r := range results {
		if r.Failed {
			failed[r.Name] = true
			t.Logf("expected-to-fail check %s: %v", r.Name, r.Errors)
		}
	}
	for _, r := range results {
		switch {
		case r.Name == "ShardReads/LateVisibleEventsBehindACommittedOffsetAreDelivered" && r.Failed:
			t.Errorf("the #332 regression must pass on the experimental store: %v", r.Errors)
		case slices.Contains(timestampOffsetChecks, r.Name) && !r.Failed:
			t.Errorf("%s was expected to fail (it asserts a timestamp offset) and passed: the list of expected failures is stale", r.Name)
		case !slices.Contains(timestampOffsetChecks, r.Name) && r.Failed:
			t.Errorf("unexpected failure of %s: %v", r.Name, r.Errors)
		}
	}
}
