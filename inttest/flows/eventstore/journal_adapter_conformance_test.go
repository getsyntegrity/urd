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

// provisionExperimentalKind migrates the normal schema, then the TEST schema of the experiment, and returns the
// variant named kind, not connected.
func provisionExperimentalKind(kind, dsn string) (postgres.ExperimentalStore, error) {
	ctx := context.Background()
	migrating, err := postgres.NewExperimentalStore(kind, dsn)
	if err != nil {
		return nil, err
	}
	if c, ok := migrating.(interface{ Connect(context.Context) error }); ok {
		if err := c.Connect(ctx); err != nil {
			return nil, err
		}
	}
	defer func() {
		if d, ok := migrating.(interface{ Disconnect(context.Context) error }); ok {
			_ = d.Disconnect(ctx)
		}
	}()
	if err := migrating.Migrate(ctx); err != nil {
		return nil, err
	}
	if err := migrating.ExperimentalMigrate(ctx); err != nil {
		return nil, err
	}
	return postgres.NewExperimentalStore(kind, dsn)
}

// timestampOffsetChecks are the checks of the real suite that assert the offset of GetShardEvents/ShardOffsets IS
// a timestamp. The experimental store gives it another meaning (a journal position), so these three are expected
// to fail on it: they pin exactly what the SPI gate of #332 would change. They are not isolation failures.
var timestampOffsetChecks = []string{
	"ShardReads/GetShardEventsReturnsOnlyTheScopesEvents",
	"ShardReads/ShardOffsetsCoverOnlyTheScopesShards",
	"ShardReads/UnscopedNeverReadsATenantNamedUnscoped",
}

// TestExperimentalAdapter_Conformance runs the real EventsStore conformance suite, the one in which
// LateVisibleEventsBehindACommittedOffsetAreDelivered is red for #332 on the current adapter, against each
// experimental variant, and pins the outcome exactly: that regression passes, the three timestamp-offset checks
// fail, and every other check passes. Any other result fails this test, so the list above cannot grow silently
// and the real regression is not excused.
func TestExperimentalAdapter_Conformance(t *testing.T) {
	for _, kind := range []string{postgres.KindSerialized, postgres.KindSerializedLate, postgres.KindHorizon} {
		t.Run(kind, func(t *testing.T) {
			results := conformance.CaptureEventsStoreChecks(func() persistence.EventsStore {
				store, err := provisionExperimentalKind(kind, shared.NewDatabase(t))
				if err != nil {
					t.Fatalf("provision the experimental store: %v", err)
				}
				if err := store.(interface{ Connect(context.Context) error }).Connect(context.Background()); err != nil {
					t.Fatalf("connect: %v", err)
				}
				t.Cleanup(func() { _ = store.(interface{ Disconnect(context.Context) error }).Disconnect(context.Background()) })
				return store
			})
			if len(results) == 0 {
				t.Fatalf("the conformance suite returned no result")
			}
			for _, r := range results {
				switch {
				case r.Name == "ShardReads/LateVisibleEventsBehindACommittedOffsetAreDelivered" && r.Failed:
					t.Errorf("the #332 regression must pass on %s: %v", kind, r.Errors)
				case slices.Contains(timestampOffsetChecks, r.Name) && !r.Failed:
					t.Errorf("%s was expected to fail (it asserts a timestamp offset) and passed: the list of expected failures is stale", r.Name)
				case !slices.Contains(timestampOffsetChecks, r.Name) && r.Failed:
					t.Errorf("unexpected failure of %s on %s: %v", r.Name, kind, r.Errors)
				case r.Failed:
					t.Logf("expected-to-fail check %s", r.Name)
				}
			}
		})
	}
}
