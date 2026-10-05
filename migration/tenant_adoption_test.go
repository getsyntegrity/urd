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

package migration

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/engine"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

// corruptingEventsStore wraps a persistence.EventsStore and, for writes
// targeting corruptScope only, runs mangle on a clone of every event before
// delegating the write. It exists to prove the events verification in
// adoptEvents (tenant_adoption.go) catches a corrupted write that the OLD
// verification (a bare len(written) != len(sourceEvents) count check) could
// not: mangle preserves the event count and every SequenceNumber, changing
// only what a count-only check cannot see.
type corruptingEventsStore struct {
	persistence.EventsStore
	corruptScope persistence.Scope
	mangle       func(*egopb.Event)
}

func (c *corruptingEventsStore) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	if !scope.Equal(c.corruptScope) {
		return c.EventsStore.WriteEvents(ctx, scope, events, precondition)
	}
	corrupted := make([]*egopb.Event, len(events))
	for i, e := range events {
		clone, ok := proto.Clone(e).(*egopb.Event)
		if !ok {
			return errors.New("corruptingEventsStore: clone failed")
		}
		c.mangle(clone)
		corrupted[i] = clone
	}
	return c.EventsStore.WriteEvents(ctx, scope, corrupted, precondition)
}

// corruptingSnapshotStore is corruptingEventsStore's snapshot-store
// counterpart: it corrupts a write's SNAPSHOT while preserving its
// SequenceNumber, which is all the OLD verification
// (written.GetSequenceNumber() != snapshot.GetSequenceNumber()) ever
// checked.
type corruptingSnapshotStore struct {
	persistence.SnapshotStore
	corruptScope persistence.Scope
	mangle       func(*egopb.Snapshot)
}

func (c *corruptingSnapshotStore) WriteSnapshot(ctx context.Context, scope persistence.Scope, snapshot *egopb.Snapshot) error {
	if !scope.Equal(c.corruptScope) {
		return c.SnapshotStore.WriteSnapshot(ctx, scope, snapshot)
	}
	clone, ok := proto.Clone(snapshot).(*egopb.Snapshot)
	if !ok {
		return errors.New("corruptingSnapshotStore: clone failed")
	}
	c.mangle(clone)
	return c.SnapshotStore.WriteSnapshot(ctx, scope, clone)
}

// corruptingStateStore is corruptingEventsStore's durable-state-store
// counterpart: it corrupts a write's STATE while preserving its
// VersionNumber, which is all the OLD verification
// (written.GetVersionNumber() != state.GetVersionNumber()) ever checked.
type corruptingStateStore struct {
	persistence.StateStore
	corruptScope persistence.Scope
	mangle       func(*egopb.DurableState)
}

func (c *corruptingStateStore) WriteState(ctx context.Context, scope persistence.Scope, state *egopb.DurableState, precondition persistence.WritePrecondition) error {
	if !scope.Equal(c.corruptScope) {
		return c.StateStore.WriteState(ctx, scope, state, precondition)
	}
	clone, ok := proto.Clone(state).(*egopb.DurableState)
	if !ok {
		return errors.New("corruptingStateStore: clone failed")
	}
	c.mangle(clone)
	return c.StateStore.WriteState(ctx, scope, clone, precondition)
}

// newLegacyEvent builds a plain (non-tenant) event for persistenceID at
// seqNr, exactly as pre-tenancy code would have written it: no
// TenantMetadata at all.
func newLegacyEvent(ctx *specs.Context, persistenceID string, seqNr uint64, ts int64) *egopb.Event {
	payload, err := anypb.New(timestamppb.New(time.Unix(ts, 0)))
	ctx.Expect(err).To(specs.BeNil())
	return &egopb.Event{
		PersistenceId:  persistenceID,
		SequenceNumber: seqNr,
		Event:          payload,
		Timestamp:      ts,
	}
}

func newLegacySnapshot(ctx *specs.Context, persistenceID string, seqNr uint64, ts int64) *egopb.Snapshot {
	payload, err := anypb.New(timestamppb.New(time.Unix(ts, 0)))
	ctx.Expect(err).To(specs.BeNil())
	return &egopb.Snapshot{
		PersistenceId:  persistenceID,
		SequenceNumber: seqNr,
		State:          payload,
		Timestamp:      ts,
	}
}

func newLegacyDurableState(ctx *specs.Context, persistenceID string, version uint64, ts int64) *egopb.DurableState {
	payload, err := anypb.New(timestamppb.New(time.Unix(ts, 0)))
	ctx.Expect(err).To(specs.BeNil())
	return &egopb.DurableState{
		PersistenceId:  persistenceID,
		VersionNumber:  version,
		ResultingState: payload,
		Timestamp:      ts,
	}
}

// fixedAssignment returns a TenantAssignment that maps every id present in
// assignments to its tenant, and reports ok=false for anything else.
func fixedAssignment(assignments map[string]tenancy.TenantID) TenantAssignment {
	return func(_ context.Context, persistenceID string) (tenancy.TenantID, bool, error) {
		id, ok := assignments[persistenceID]
		return id, ok, nil
	}
}

// errText is the message of err, or "" for a nil error, so a text expectation
// on a missing error fails as an assertion instead of panicking.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func connectedEventsStore(ctx *specs.Context) *testkit.EventStore {
	store := testkit.NewEventsStore()
	err := store.Connect(context.Background())
	ctx.Expect(err).To(specs.BeNil())
	return store
}

func adoptionSnapshotStore(ctx *specs.Context) *testkit.SnapshotStore {
	store := testkit.NewSnapshotStore()
	err := store.Connect(context.Background())
	ctx.Expect(err).To(specs.BeNil())
	return store
}

func connectedStateStore(ctx *specs.Context) *testkit.DurableStore {
	store := testkit.NewDurableStore()
	err := store.Connect(context.Background())
	ctx.Expect(err).To(specs.BeNil())
	return store
}

func tenantScope(ctx *specs.Context, tenant string) persistence.Scope {
	scope, err := persistence.NewTenantScope(tenancy.TenantID(tenant))
	ctx.Expect(err).To(specs.BeNil())
	return scope
}

func tenantContextOf(ctx *specs.Context, tenant string) tenancy.TenantContext {
	tenantID, err := tenancy.NewTenantID(tenant)
	ctx.Expect(err).To(specs.BeNil())
	tenantContext, err := tenancy.NewTenantContext(tenantID)
	ctx.Expect(err).To(specs.BeNil())
	return tenantContext
}

func seedEvents(ctx *specs.Context, store persistence.EventsStore, scope persistence.Scope, events ...*egopb.Event) {
	err := store.WriteEvents(context.Background(), scope, events, persistence.Unconditional())
	ctx.Expect(err).To(specs.BeNil())
}

func seedSnapshot(ctx *specs.Context, store persistence.SnapshotStore, scope persistence.Scope, snapshot *egopb.Snapshot) {
	err := store.WriteSnapshot(context.Background(), scope, snapshot)
	ctx.Expect(err).To(specs.BeNil())
}

func seedState(ctx *specs.Context, store persistence.StateStore, scope persistence.Scope, state *egopb.DurableState) {
	err := store.WriteState(context.Background(), scope, state, persistence.Unconditional())
	ctx.Expect(err).To(specs.BeNil())
}

func newAdopter(ctx *specs.Context, assignment TenantAssignment, opts ...AdoptionOption) *TenantAdopter {
	adopter, err := NewTenantAdopter(assignment, opts...)
	ctx.Expect(err).To(specs.BeNil())
	return adopter
}

// mustAdopt runs adopter as a setup step: the run itself must not error.
func mustAdopt(ctx *specs.Context, adopter *TenantAdopter) *AdoptionReport {
	report, err := adopter.Run(context.Background())
	ctx.Expect(err).To(specs.BeNil())
	return report
}

func latestEvent(ctx *specs.Context, store persistence.EventsStore, scope persistence.Scope, id string) *egopb.Event {
	event, err := store.GetLatestEvent(context.Background(), scope, id)
	ctx.Expect(err).To(specs.BeNil())
	return event
}

func latestSnapshot(ctx *specs.Context, store persistence.SnapshotStore, scope persistence.Scope, id string) *egopb.Snapshot {
	snapshot, err := store.GetLatestSnapshot(context.Background(), scope, id)
	ctx.Expect(err).To(specs.BeNil())
	return snapshot
}

func latestState(ctx *specs.Context, store persistence.StateStore, scope persistence.Scope, id string) *egopb.DurableState {
	state, err := store.GetLatestState(context.Background(), scope, id)
	ctx.Expect(err).To(specs.BeNil())
	return state
}

func replayEvents(ctx *specs.Context, store persistence.EventsStore, scope persistence.Scope, id string, from, to, max uint64) []*egopb.Event {
	events, err := store.ReplayEvents(context.Background(), scope, id, from, to, max)
	ctx.Expect(err).To(specs.BeNil())
	return events
}

// panics reports whether fn panics.
func panics(fn func()) (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	fn()
	return false
}

// adoptionAccountBehavior is a minimal engine.EventSourcedBehavior used only by
// TestTenantAdopterEndToEndRecoveryThroughRealActor to prove a real,
// tenant-bound EventSourcedActor recovers migrated data. It is not exported
// from the root package, so this test defines its own copy rather than
// reusing the root package's internal test helper.
type adoptionAccountBehavior struct {
	id string
}

var _ engine.EventSourcedBehavior = (*adoptionAccountBehavior)(nil) //nolint:staticcheck // exercises the deprecated API on purpose (#124)

func (x *adoptionAccountBehavior) ID() string { return x.id }

func (x *adoptionAccountBehavior) InitialState() engine.State { return new(testpb.Account) }

func (x *adoptionAccountBehavior) HandleCommand(_ context.Context, command engine.Command, _ engine.State) ([]engine.Event, error) {
	cmd, ok := command.(*testpb.CreditAccount)
	if !ok || cmd.GetAccountId() != x.id {
		return nil, errors.New("unhandled command")
	}
	return []engine.Event{
		&testpb.AccountCredited{AccountId: cmd.GetAccountId(), AccountBalance: cmd.GetBalance()},
	}, nil
}

func (x *adoptionAccountBehavior) HandleEvent(_ context.Context, event engine.Event, priorState engine.State) (engine.State, error) {
	switch evt := event.(type) {
	case *testpb.AccountCreated:
		return &testpb.Account{AccountId: evt.GetAccountId(), AccountBalance: evt.GetAccountBalance()}, nil
	case *testpb.AccountCredited:
		account, _ := priorState.(*testpb.Account)
		return &testpb.Account{
			AccountId:      evt.GetAccountId(),
			AccountBalance: account.GetAccountBalance() + evt.GetAccountBalance(),
		}, nil
	default:
		return nil, errors.New("unhandled event")
	}
}

func (x *adoptionAccountBehavior) MarshalBinary() ([]byte, error) {
	return []byte(x.id), nil
}

func (x *adoptionAccountBehavior) UnmarshalBinary(data []byte) error {
	x.id = string(data)
	return nil
}

func TestNewTenantAdopter(t *testing.T) {
	specs.Describe(t, "NewTenantAdopter validates its configuration and applies its defaults", func(s *specs.Spec) {
		s.It("requires a TenantAssignment", func(ctx *specs.Context) {
			_, err := NewTenantAdopter(nil, WithEventsStore(testkit.NewEventsStore()))
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(ErrAssignmentRequired))
		})

		s.It("requires at least one store", func(ctx *specs.Context) {
			_, err := NewTenantAdopter(fixedAssignment(nil))
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
			ctx.Expect(err).To(specs.MatchError(ErrNoStoresConfigured))
		})

		s.It("defaults to dry-run, Unscoped source, and a resolved logger", func(ctx *specs.Context) {
			a, err := NewTenantAdopter(fixedAssignment(nil), WithEventsStore(testkit.NewEventsStore()))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(a.write).To(specs.BeFalse())
			ctx.Expect(a.sourceScope.IsUnscoped()).To(specs.BeTrue())
			ctx.Expect(a.logger).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestTenantAdopterDryRunWritesNothing(t *testing.T) {
	specs.Describe(t, "a dry run reports what a real run would do and writes nothing to the target", func(s *specs.Spec) {
		s.It("counts the copy without writing to any store", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := connectedEventsStore(ctx)
			snapshotStore := adoptionSnapshotStore(ctx)
			stateStore := connectedStateStore(ctx)

			const id = "dry-run-1"
			seedEvents(ctx, eventsStore, persistence.Unscoped(), newLegacyEvent(ctx, id, 1, 100))
			seedSnapshot(ctx, snapshotStore, persistence.Unscoped(), newLegacySnapshot(ctx, id, 1, 100))
			seedState(ctx, stateStore, persistence.Unscoped(), newLegacyDurableState(ctx, id, 1, 100))

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(eventsStore),
				WithSnapshotStore(snapshotStore),
				WithStateStore(stateStore),
			)

			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report).To(specs.Not(specs.BeNil()))

			ctx.Expect(report.DryRun).To(specs.BeTrue())
			ctx.Expect(report.Scanned).ToEqual(1)
			ctx.Expect(report.Assigned).ToEqual(1)
			// A dry run reports exactly what a real run would do, yet performs
			// no write, so nothing was actually verified.
			ctx.Expect(report.Copied).ToEqual(1)
			ctx.Expect(report.Verified).ToEqual(0)
			ctx.Expect(report.SourceDeleted).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(0)

			target := tenantScope(ctx, "acme")
			ctx.Expect(latestEvent(ctx, eventsStore, target, id)).To(specs.BeNil())
			ctx.Expect(latestSnapshot(ctx, snapshotStore, target, id)).To(specs.BeNil())
			ctx.Expect(latestState(ctx, stateStore, target, id)).To(specs.BeNil())
		})
	})
}

func TestTenantAdopterRealRunCopiesAndKeepsSource(t *testing.T) {
	specs.Describe(t, "a real run copies every record kind to the target and keeps the source by default", func(s *specs.Spec) {
		s.It("copies events, snapshot and state and leaves the source intact", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := connectedEventsStore(ctx)
			snapshotStore := adoptionSnapshotStore(ctx)
			stateStore := connectedStateStore(ctx)

			const id = "real-run-1"
			source := persistence.Unscoped()
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 2, 200))
			seedSnapshot(ctx, snapshotStore, source, newLegacySnapshot(ctx, id, 2, 200))
			seedState(ctx, stateStore, source, newLegacyDurableState(ctx, id, 1, 200))

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(eventsStore),
				WithSnapshotStore(snapshotStore),
				WithStateStore(stateStore),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)

			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())

			ctx.Expect(report.DryRun).To(specs.BeFalse())
			ctx.Expect(report.Copied).ToEqual(1)
			ctx.Expect(report.Verified).ToEqual(1)
			// No delete option was requested.
			ctx.Expect(report.SourceDeleted).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(0)

			target := tenantScope(ctx, "acme")
			ctx.Expect(replayEvents(ctx, eventsStore, target, id, 1, 2, 10)).To(specs.HaveLen(2))

			targetSnap := latestSnapshot(ctx, snapshotStore, target, id)
			ctx.Expect(targetSnap).To(specs.Not(specs.BeNil()))
			ctx.Expect(targetSnap.GetSequenceNumber()).ToEqual(uint64(2))

			targetState := latestState(ctx, stateStore, target, id)
			ctx.Expect(targetState).To(specs.Not(specs.BeNil()))
			ctx.Expect(targetState.GetVersionNumber()).ToEqual(uint64(1))

			// The source scope keeps its originals: no delete by default.
			ctx.Expect(replayEvents(ctx, eventsStore, source, id, 1, 2, 10)).To(specs.HaveLen(2))
			ctx.Expect(latestSnapshot(ctx, snapshotStore, source, id)).To(specs.Not(specs.BeNil()))
			ctx.Expect(latestState(ctx, stateStore, source, id)).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestTenantAdopterStampsTargetTenantMetadata(t *testing.T) {
	specs.Describe(t, "adopted records carry the target tenant metadata stamped like the actors stamp it", func(s *specs.Spec) {
		s.It("stamps the event, the snapshot and the durable state", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := connectedEventsStore(ctx)
			snapshotStore := adoptionSnapshotStore(ctx)
			stateStore := connectedStateStore(ctx)

			const id = "stamp-1"
			source := persistence.Unscoped()
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, 100))
			seedSnapshot(ctx, snapshotStore, source, newLegacySnapshot(ctx, id, 1, 100))
			seedState(ctx, stateStore, source, newLegacyDurableState(ctx, id, 1, 100))

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(eventsStore),
				WithSnapshotStore(snapshotStore),
				WithStateStore(stateStore),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)
			_, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())

			target := tenantScope(ctx, "acme")
			wantContext := tenantContextOf(ctx, "acme")
			wantMetadata := tenancy.MarshalMetadata(wantContext)

			evt := latestEvent(ctx, eventsStore, target, id)
			ctx.Expect(evt).To(specs.Not(specs.BeNil()))
			gotEventTenant, err := tenancy.UnmarshalMetadata(tenancy.Metadata(evt.GetTenantMetadata()))
			ctx.Expect(err).To(specs.BeNil())
			// The copied event carries tenant_metadata stamped exactly like the
			// actors stamp it.
			ctx.Expect(gotEventTenant).ToEqual(wantContext)
			// The copy carries exactly the actor-style tenant stamp plus one
			// adoption receipt key, nothing else.
			for key, value := range wantMetadata {
				ctx.Expect(evt.GetTenantMetadata()).To(specs.HavePair(key, value))
			}
			ctx.Expect(evt.GetTenantMetadata()).To(specs.HaveLen(len(wantMetadata) + 1))
			// Every adopted record carries an adoption receipt.
			ctx.Expect(evt.GetTenantMetadata()).To(specs.HaveKey(adoptionReceiptKey))
			ctx.Expect(evt.GetTenantMetadata()[adoptionReceiptKey]).To(specs.Not(specs.BeEmpty()))

			snap := latestSnapshot(ctx, snapshotStore, target, id)
			ctx.Expect(snap).To(specs.Not(specs.BeNil()))
			gotSnapTenant, err := tenancy.UnmarshalMetadata(tenancy.Metadata(snap.GetTenantMetadata()))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(gotSnapTenant).ToEqual(wantContext)

			state := latestState(ctx, stateStore, target, id)
			ctx.Expect(state).To(specs.Not(specs.BeNil()))
			gotStateTenant, err := tenancy.UnmarshalMetadata(tenancy.Metadata(state.GetTenantMetadata()))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(gotStateTenant).ToEqual(wantContext)
		})
	})
}

// TestTenantAdopterEndToEndRecoveryThroughRealActor is the test that proves
// the migration actually produces usable data: it writes legacy (unscoped,
// no tenant_metadata) events directly to the store, adopts the aggregate
// into tenant "acme", then spawns a REAL tenant-aware EventSourcedActor
// (through a real Engine, exactly as production code does) bound to "acme"
// and proves it recovers the migrated event and can keep applying commands
// on top of it. If the migrated tenant_metadata were missing or wrong, the
// actor's own seedActorTenant/tenancy.VerifyUnchanged cross-check (T4) would
// refuse to recover at all.
func TestTenantAdopterEndToEndRecoveryThroughRealActor(t *testing.T) {
	specs.Describe(t, "a real tenant-bound actor recovers the data an adoption migrated", func(s *specs.Spec) {
		s.It("recovers the migrated event and keeps applying commands on top of it", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := testkit.NewEventsStore()
			ctx.Expect(eventsStore.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = eventsStore.Disconnect(bg) })

			entityID := "acct-" + uuid.NewString()
			source := persistence.Unscoped()

			// Legacy data: written exactly as a pre-tenancy deployment would have,
			// with no tenant_metadata at all.
			createdPayload, err := anypb.New(&testpb.AccountCreated{AccountId: entityID, AccountBalance: 100})
			ctx.Expect(err).To(specs.BeNil())
			legacyEvent := &egopb.Event{
				PersistenceId:  entityID,
				SequenceNumber: 1,
				Event:          createdPayload,
				Timestamp:      time.Now().Unix(),
			}
			ctx.Expect(eventsStore.WriteEvents(bg, source, []*egopb.Event{legacyEvent}, persistence.Unconditional())).To(specs.BeNil())

			adopter, err := NewTenantAdopter(
				fixedAssignment(map[string]tenancy.TenantID{entityID: "acme"}),
				WithEventsStore(eventsStore),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)
			ctx.Expect(err).To(specs.BeNil())
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.Copied).ToEqual(1)
			ctx.Expect(report.Verified).ToEqual(1)

			resolver, err := tenancy.WithSingleTenant("acme")
			ctx.Expect(err).To(specs.BeNil())

			cfg := engine.NewConfig(eventsStore, engine.WithTenantResolver(resolver))
			sys, err := goakt.NewActorSystem("TenantAdoptionE2E-"+uuid.NewString(), cfg.GoaktOptions()...)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(sys.Start(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = sys.Stop(context.Background()) })

			eng, err := engine.NewEngine(sys, cfg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(eng.Start(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = eng.Stop(context.Background()) })

			behavior := &adoptionAccountBehavior{id: entityID}
			ctx.Expect(eng.Entity(bg, behavior)).To(specs.BeNil()) //nolint:staticcheck // exercises the deprecated API on purpose (#124)

			// The recovered state must reflect the migrated event (balance 100)
			// BEFORE any new command is applied: crediting 50 on top of it must
			// yield 150, which is only possible if recovery actually replayed the
			// migrated AccountCreated event rather than starting from a blank slate.
			// An error here means the tenant-bound actor failed its tenant_metadata
			// cross-check while recovering.
			resultingState, _, err := eng.SendCommand(bg, entityID, &testpb.CreditAccount{AccountId: entityID, Balance: 50}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())

			account, ok := resultingState.(*testpb.Account)
			ctx.Expect(ok).To(specs.BeTrue())
			// 150 == migrated balance (100) + credited amount (50), proving recovery used the migrated data.
			ctx.Expect(account.GetAccountBalance()).ToEqual(float64(150))
		})
	})
}

func TestTenantAdopterTwoTenantsAreIsolated(t *testing.T) {
	specs.Describe(t, "aggregates assigned to different tenants never leak into each other's scope", func(s *specs.Spec) {
		s.It("keeps each tenant's aggregate out of the other tenant's scope", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := connectedEventsStore(ctx)

			const idA = "multi-a"
			const idB = "multi-b"
			source := persistence.Unscoped()
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, idA, 1, 100))
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, idB, 1, 200))

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{idA: "acme", idB: "globex"}),
				WithEventsStore(eventsStore),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.Copied).ToEqual(2)

			acme := tenantScope(ctx, "acme")
			globex := tenantScope(ctx, "globex")

			// acme sees its own aggregate, never globex's.
			ctx.Expect(latestEvent(ctx, eventsStore, acme, idA)).To(specs.Not(specs.BeNil()))
			ctx.Expect(latestEvent(ctx, eventsStore, acme, idB)).To(specs.BeNil())

			// globex sees its own aggregate, never acme's.
			ctx.Expect(latestEvent(ctx, eventsStore, globex, idB)).To(specs.Not(specs.BeNil()))
			ctx.Expect(latestEvent(ctx, eventsStore, globex, idA)).To(specs.BeNil())
		})
	})
}

func TestTenantAdopterAssignmentOkFalseLeavesUntouched(t *testing.T) {
	specs.Describe(t, "an aggregate the assignment declines (ok=false) is skipped and left untouched", func(s *specs.Spec) {
		s.It("counts it as skipped by assignment and keeps its source event", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := connectedEventsStore(ctx)

			const id = "unassigned-1"
			source := persistence.Unscoped()
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, 100))

			adopter := newAdopter(ctx,
				fixedAssignment(nil), // ok=false for everything
				WithEventsStore(eventsStore),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())

			ctx.Expect(report.Scanned).ToEqual(1)
			ctx.Expect(report.SkippedByAssignment).ToEqual(1)
			ctx.Expect(report.Assigned).ToEqual(0)
			ctx.Expect(report.Copied).ToEqual(0)

			// The source event is untouched.
			ctx.Expect(latestEvent(ctx, eventsStore, source, id)).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestTenantAdopterReRunIsANoOp(t *testing.T) {
	specs.Describe(t, "re-running an adoption reports the aggregate as already migrated and writes nothing", func(s *specs.Spec) {
		s.It("does not duplicate the write on the second run", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := connectedEventsStore(ctx)

			const id = "idempotent-1"
			source := persistence.Unscoped()
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, 100))

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(eventsStore),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)

			first, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(first.Copied).ToEqual(1)
			ctx.Expect(first.Failed).ToEqual(0)

			second, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			// Re-running is not an error, does not duplicate the write, and
			// reports the aggregate as already migrated.
			ctx.Expect(second.Failed).ToEqual(0)
			ctx.Expect(second.Copied).ToEqual(0)
			ctx.Expect(second.AlreadyPresent).ToEqual(1)

			target := tenantScope(ctx, "acme")
			// No duplicate event was written.
			ctx.Expect(replayEvents(ctx, eventsStore, target, id, 1, 10, 10)).To(specs.HaveLen(1))
		})
	})
}

func TestTenantAdopterAlreadyPresentInTargetIsNeverOverwritten(t *testing.T) {
	specs.Describe(t, "a non-equivalent record already in the target fails closed and is never overwritten", func(s *specs.Spec) {
		s.It("reports a failure, keeps the target record and keeps the source", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := connectedEventsStore(ctx)

			const id = "conflict-1"
			source := persistence.Unscoped()
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, 100))

			// Some other process already wrote a DIFFERENT record for this id under
			// the target tenant, before adoption ran.
			target := tenantScope(ctx, "acme")
			preexistingEvent := newLegacyEvent(ctx, id, 1, 999)
			preexistingEvent.TenantMetadata = tenancy.MarshalMetadata(tenantContextOf(ctx, "acme"))
			seedEvents(ctx, eventsStore, target, preexistingEvent)

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(eventsStore),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())

			// The target already holds a record under this id, but it is NOT the
			// record this adoption would write (timestamp 999, not the source's
			// 100): that is a collision, not a completed migration, so it fails
			// closed rather than being reported as already_present (#98 review).
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Copied).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errTargetNotEquivalent))

			// The pre-existing target record must be untouched (still timestamp 999,
			// not overwritten by the legacy copy's timestamp 100), and the source is
			// kept: a failed classification never deletes it.
			evt := latestEvent(ctx, eventsStore, target, id)
			ctx.Expect(evt).To(specs.Not(specs.BeNil()))
			ctx.Expect(evt.GetTimestamp()).ToEqual(int64(999))
			ctx.Expect(latestEvent(ctx, eventsStore, source, id)).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestTenantAdopterSourceDeletionOnlyAfterVerification(t *testing.T) {
	specs.Describe(t, "the source is deleted only on an explicit opt-in and only after a verified copy", func(s *specs.Spec) {
		const id = "delete-1"
		source := persistence.Unscoped()
		seeded := func(ctx *specs.Context) *testkit.EventStore {
			eventsStore := connectedEventsStore(ctx)
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, 100))
			return eventsStore
		}

		s.It("without the opt-in, source is kept", func(ctx *specs.Context) {
			eventsStore := seeded(ctx)
			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "keepme"}),
				WithEventsStore(eventsStore),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.SourceDeleted).ToEqual(0)

			// The source is kept without the explicit opt-in.
			ctx.Expect(latestEvent(ctx, eventsStore, source, id)).To(specs.Not(specs.BeNil()))
		})

		s.It("with the opt-in, source is removed only after a verified copy", func(ctx *specs.Context) {
			eventsStore := seeded(ctx)
			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(eventsStore),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
				WithSourceDeletion(),
			)
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.Copied).ToEqual(1)
			ctx.Expect(report.Verified).ToEqual(1)
			ctx.Expect(report.SourceDeleted).ToEqual(1)

			// The source is removed once the copy was verified, and the target
			// copy remains.
			ctx.Expect(latestEvent(ctx, eventsStore, source, id)).To(specs.BeNil())
			ctx.Expect(latestEvent(ctx, eventsStore, tenantScope(ctx, "acme"), id)).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestTenantAdopterPerAggregateFailureDoesNotAbortRun(t *testing.T) {
	specs.Describe(t, "a per-aggregate failure is reported without aborting the rest of the run", func(s *specs.Spec) {
		s.It("still migrates the good aggregate and reports the bad one", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := connectedEventsStore(ctx)

			const goodID = "good-1"
			const badID = "bad-1"
			source := persistence.Unscoped()
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, goodID, 1, 100))
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, badID, 1, 200))

			assignErr := errors.New("boom: cannot decide tenant for bad-1")
			assign := func(_ context.Context, persistenceID string) (tenancy.TenantID, bool, error) {
				if persistenceID == badID {
					return "", false, assignErr
				}
				return "acme", true, nil
			}

			adopter := newAdopter(ctx, assign, WithEventsStore(eventsStore), WithWriteEnabled(), WithAdoptionFence(newTestFence()))

			// A per-aggregate failure must not abort the whole run.
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())

			ctx.Expect(report.Scanned).ToEqual(2)
			// The good aggregate is still migrated.
			ctx.Expect(report.Copied).ToEqual(1)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0].PersistenceID).ToEqual(badID)
			ctx.Expect(report.Failures[0].Err).To(specs.MatchError(assignErr))

			target := tenantScope(ctx, "acme")
			ctx.Expect(latestEvent(ctx, eventsStore, target, goodID)).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestTenantAdopterExplicitPersistenceIDsForDurableStateOnly(t *testing.T) {
	specs.Describe(t, "a durable-state-only adoption enumerates the persistence ids the operator supplies", func(s *specs.Spec) {
		s.It("copies the state of an explicitly listed id", func(ctx *specs.Context) {
			bg := context.Background()
			stateStore := connectedStateStore(ctx)

			const id = "state-only-1"
			source := persistence.Unscoped()
			seedState(ctx, stateStore, source, newLegacyDurableState(ctx, id, 1, 100))

			// No events store at all: StateStore has no enumeration method, so the
			// operator must supply the id explicitly.
			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithStateStore(stateStore),
				WithPersistenceIDs(id),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())

			ctx.Expect(report.Scanned).ToEqual(1)
			ctx.Expect(report.Copied).ToEqual(1)

			target := tenantScope(ctx, "acme")
			ctx.Expect(latestState(ctx, stateStore, target, id)).To(specs.Not(specs.BeNil()))
		})
	})
}

// TestTenantAdopterEventsVerificationCatchesCorruptedWrite is the
// adversarial proof for the second review defect: a verification that only
// compares len(written) != len(sourceEvents) cannot detect a write that
// dropped the payload and tenant_metadata while preserving the event count
// and every sequence number. corruptingEventsStore simulates exactly that
// faulty adapter. The fixed verification (verifyEventsMatchBySequence) must
// fail this aggregate, name its persistence id, and — critically — never
// delete the source, even though WithSourceDeletion was requested.
func TestTenantAdopterEventsVerificationCatchesCorruptedWrite(t *testing.T) {
	specs.Describe(t, "events verification catches a corrupted target write that a bare count check would miss", func(s *specs.Spec) {
		s.It("fails the aggregate, names its id and keeps the source", func(ctx *specs.Context) {
			bg := context.Background()
			base := connectedEventsStore(ctx)

			const id = "corrupt-events-1"
			source := persistence.Unscoped()
			seedEvents(ctx, base, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 2, 200))

			target := tenantScope(ctx, "acme")

			corrupting := &corruptingEventsStore{
				EventsStore:  base,
				corruptScope: target,
				mangle: func(e *egopb.Event) {
					// Right count, right sequence number, wrong everything else:
					// the payload is dropped and tenant_metadata never lands.
					e.TenantMetadata = nil
					e.Event = nil
				},
			}

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(corrupting),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
				WithSourceDeletion(),
			)

			// A per-aggregate verification failure must not abort the whole run.
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())

			// A corrupted target write is reported as a failure, not silently
			// verified.
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0].PersistenceID).ToEqual(id)
			// The failure names the persistence id that differed.
			ctx.Expect(errText(report.Failures[0].Err)).To(specs.Contain(id))
			// A failed verification never deletes the source, even with
			// WithSourceDeletion.
			ctx.Expect(report.SourceDeleted).ToEqual(0)

			// The source copy remains fully intact after a failed verification.
			ctx.Expect(replayEvents(ctx, base, source, id, 1, 2, 10)).To(specs.HaveLen(2))
		})
	})
}

// TestTenantAdopterSnapshotVerificationCatchesCorruptedWrite is the
// snapshot-store counterpart of the events test above: a verification that
// only compares SequenceNumber cannot detect a snapshot whose STATE payload
// was corrupted while its sequence number was preserved.
func TestTenantAdopterSnapshotVerificationCatchesCorruptedWrite(t *testing.T) {
	specs.Describe(t, "snapshot verification catches a corrupted state payload behind a matching sequence number", func(s *specs.Spec) {
		s.It("fails the aggregate, names its id and keeps the source snapshot", func(ctx *specs.Context) {
			bg := context.Background()
			base := adoptionSnapshotStore(ctx)

			const id = "corrupt-snapshot-1"
			source := persistence.Unscoped()
			seedSnapshot(ctx, base, source, newLegacySnapshot(ctx, id, 5, 100))

			target := tenantScope(ctx, "acme")

			corrupting := &corruptingSnapshotStore{
				SnapshotStore: base,
				corruptScope:  target,
				mangle: func(s *egopb.Snapshot) {
					// Right sequence number, wrong state payload entirely.
					payload, err := anypb.New(&testpb.Account{AccountId: id, AccountBalance: 999})
					ctx.Expect(err).To(specs.BeNil())
					s.State = payload
				},
			}

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(corrupting),
				WithPersistenceIDs(id), // no events store: SnapshotStore has no enumeration method
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
				WithSourceDeletion(),
			)

			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())

			// A snapshot whose payload differs from the source must fail
			// verification even though its sequence number matches.
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0].PersistenceID).ToEqual(id)
			ctx.Expect(errText(report.Failures[0].Err)).To(specs.Contain(id))
			ctx.Expect(report.SourceDeleted).ToEqual(0)

			// The source snapshot remains intact after a failed verification.
			ctx.Expect(latestSnapshot(ctx, base, source, id)).To(specs.Not(specs.BeNil()))
		})
	})
}

// TestTenantAdopterStateVerificationCatchesCorruptedWrite is the
// durable-state counterpart: a verification that only compares
// VersionNumber cannot detect a durable state whose payload was corrupted
// while its version number was preserved. adoptState never deletes (there
// is no delete method on persistence.StateStore), but its verification
// still feeds AdoptionReport.Verified/Failed, so it must be held to the
// same standard.
func TestTenantAdopterStateVerificationCatchesCorruptedWrite(t *testing.T) {
	specs.Describe(t, "durable state verification catches a corrupted payload behind a matching version number", func(s *specs.Spec) {
		s.It("fails the aggregate and names its id", func(ctx *specs.Context) {
			bg := context.Background()
			base := connectedStateStore(ctx)

			const id = "corrupt-state-1"
			source := persistence.Unscoped()
			seedState(ctx, base, source, newLegacyDurableState(ctx, id, 3, 100))

			target := tenantScope(ctx, "acme")

			corrupting := &corruptingStateStore{
				StateStore:   base,
				corruptScope: target,
				mangle: func(s *egopb.DurableState) {
					// Right version number, wrong resulting-state payload entirely.
					payload, err := anypb.New(&testpb.Account{AccountId: id, AccountBalance: 999})
					ctx.Expect(err).To(specs.BeNil())
					s.ResultingState = payload
				},
			}

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithStateStore(corrupting),
				WithPersistenceIDs(id), // no events store: StateStore has no enumeration method
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)

			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())

			// A durable state whose payload differs from the source must fail
			// verification even though its version number matches.
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0].PersistenceID).ToEqual(id)
			ctx.Expect(errText(report.Failures[0].Err)).To(specs.Contain(id))
		})
	})
}

// TestTenantAdopterAdoptsEveryAggregateAcrossMultiplePages is the
// migration-level regression for the first review defect: PersistenceIDs
// pagination used to silently skip one id at every page boundary (see
// testkit/eventstore.go's PersistenceIDs doc comment), and
// collectPersistenceIDs (tenant_adoption.go) drives this tool's entire scan
// off that enumeration — a real run could report success while leaving
// aggregates unadopted. This writes more aggregates than fit in one page at
// a small configured page size and asserts every single one is scanned and
// adopted, none skipped at a page boundary.
func TestTenantAdopterAdoptsEveryAggregateAcrossMultiplePages(t *testing.T) {
	specs.Describe(t, "every aggregate is scanned and adopted across PersistenceIDs page boundaries", func(s *specs.Spec) {
		s.It("skips no aggregate at a page boundary", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := connectedEventsStore(ctx)

			const pageSize = 4
			const total = 3*pageSize + 1 // forces at least four PersistenceIDs pages
			source := persistence.Unscoped()
			assignments := make(map[string]tenancy.TenantID, total)
			ids := make([]string, 0, total)
			for i := 0; i < total; i++ {
				id := fmt.Sprintf("adopt-page-%03d", i)
				ids = append(ids, id)
				seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, int64(i)))
				assignments[id] = "acme"
			}

			adopter := newAdopter(ctx,
				fixedAssignment(assignments),
				WithEventsStore(eventsStore),
				WithScanPageSize(pageSize),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)

			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())

			// Every persistence id is scanned exactly once; none is skipped at a
			// PersistenceIDs page boundary.
			ctx.Expect(report.Scanned).ToEqual(total)
			ctx.Expect(report.Copied).ToEqual(total)
			ctx.Expect(report.Failed).ToEqual(0)

			target := tenantScope(ctx, "acme")
			var notAdopted []string
			for _, id := range ids {
				if latestEvent(ctx, eventsStore, target, id) == nil {
					notAdopted = append(notAdopted, id)
				}
			}
			// Empty means every persistence id was adopted, none skipped at a page
			// boundary; otherwise the failure prints the skipped ids in order.
			ctx.Expect(notAdopted).To(specs.BeEmpty())
		})
	})
}

// duplicatingEventsStore wraps a persistence.EventsStore and, for reads of
// duplicateScope only, prepends a corrupted duplicate of the first returned
// event. The distinct-sequence-number count and every sequence number still
// match what was written; only the raw row count reveals the extra row. It
// proves verifyEventsMatchBySequence rejects a read-back that carries more
// than one row for a sequence number.
type duplicatingEventsStore struct {
	persistence.EventsStore
	duplicateScope persistence.Scope
}

func (d *duplicatingEventsStore) ReplayEvents(ctx context.Context, scope persistence.Scope, persistenceID string, fromSequenceNumber, toSequenceNumber, maxNumber uint64) ([]*egopb.Event, error) {
	events, err := d.EventsStore.ReplayEvents(ctx, scope, persistenceID, fromSequenceNumber, toSequenceNumber, maxNumber)
	if err != nil || len(events) == 0 || !scope.Equal(d.duplicateScope) {
		return events, err
	}
	dup, ok := proto.Clone(events[0]).(*egopb.Event)
	if !ok {
		return nil, errors.New("duplicatingEventsStore: clone failed")
	}
	dup.Event = nil
	return append([]*egopb.Event{dup}, events...), nil
}

func TestTenantAdopterEventsVerificationRejectsDuplicateSequenceRows(t *testing.T) {
	specs.Describe(t, "events verification rejects a read-back that carries a duplicated sequence row", func(s *specs.Spec) {
		s.It("fails the aggregate and keeps the source intact", func(ctx *specs.Context) {
			bg := context.Background()
			base := connectedEventsStore(ctx)

			const id = "duplicate-rows-1"
			source := persistence.Unscoped()
			seedEvents(ctx, base, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 2, 200))

			target := tenantScope(ctx, "acme")

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(&duplicatingEventsStore{EventsStore: base, duplicateScope: target}),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
				WithSourceDeletion(),
			)

			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			// A read-back with a duplicated sequence row must not verify.
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.SourceDeleted).ToEqual(0)

			// The source remains intact after a failed verification.
			ctx.Expect(replayEvents(ctx, base, source, id, 1, 2, 10)).To(specs.HaveLen(2))
		})
	})
}

// TestTenantAdopterSourceDeletingReRunIsIdempotent covers the #98 review
// finding that a second identical run after WithSourceDeletion reported
// every migrated aggregate as failed ("no source record") instead of as a
// no-op. testkit.EventStore keeps the persistence id's log entry after
// DeleteEvents, so the id is still enumerated with an empty source.
func TestTenantAdopterSourceDeletingReRunIsIdempotent(t *testing.T) {
	specs.Describe(t, "a second identical run after a source-deleting run is a no-op, not a failure", func(s *specs.Spec) {
		s.It("reports the emptied source as already migrated and writes nothing", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := connectedEventsStore(ctx)
			snapshotStore := adoptionSnapshotStore(ctx)

			const id = "delete-rerun-1"
			source := persistence.Unscoped()
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 2, 200))
			seedSnapshot(ctx, snapshotStore, source, newLegacySnapshot(ctx, id, 2, 200))

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(eventsStore),
				WithSnapshotStore(snapshotStore),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
				WithSourceDeletion(),
			)

			first, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(first.Copied).ToEqual(1)
			ctx.Expect(first.SourceDeleted).ToEqual(1)
			ctx.Expect(first.Failed).ToEqual(0)

			ids, _, err := eventsStore.PersistenceIDs(bg, source, 10, "")
			ctx.Expect(err).To(specs.BeNil())
			// Precondition: this store keeps enumerating the emptied source id.
			ctx.Expect(ids).To(specs.Contain(id))

			second, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(second.Scanned).ToEqual(1)
			// An equivalent target with a deleted source is already migrated.
			ctx.Expect(second.AlreadyPresent).ToEqual(1)
			ctx.Expect(second.Failed).ToEqual(0)
			ctx.Expect(second.Failures).To(specs.BeEmpty())
			ctx.Expect(second.Copied).ToEqual(0)
			ctx.Expect(second.Verified).ToEqual(0)
			ctx.Expect(second.SourceDeleted).ToEqual(0)
			ctx.Expect(second.Aggregates).To(specs.HaveLen(1))
			ctx.Expect(second.Aggregates[0].Events.Status).ToEqual(StatusAlreadyPresent)
			ctx.Expect(second.Aggregates[0].Snapshot.Status).ToEqual(StatusAlreadyPresent)

			// The second run wrote nothing.
			target := tenantScope(ctx, "acme")
			ctx.Expect(replayEvents(ctx, eventsStore, target, id, 1, 10, 10)).To(specs.HaveLen(2))
		})
	})
}

func TestTenantAdopterSnapshotOnlyReRunAfterDeletionIsIdempotent(t *testing.T) {
	specs.Describe(t, "a snapshot-only re-run after source deletion is already present, not a failure", func(s *specs.Spec) {
		s.It("reports the aggregate as already present", func(ctx *specs.Context) {
			bg := context.Background()
			snapshotStore := adoptionSnapshotStore(ctx)

			const id = "snapshot-rerun-1"
			source := persistence.Unscoped()
			seedSnapshot(ctx, snapshotStore, source, newLegacySnapshot(ctx, id, 3, 300))

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(snapshotStore),
				WithPersistenceIDs(id),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
				WithSourceDeletion(),
			)

			first, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(first.SourceDeleted).ToEqual(1)

			second, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(second.AlreadyPresent).ToEqual(1)
			ctx.Expect(second.Failed).ToEqual(0)
			ctx.Expect(second.Failures).To(specs.BeEmpty())
		})
	})
}

// TestTenantAdopterMissingSourceClassification pins how an assigned
// aggregate whose source is absent is classified: an equivalent target is
// a no-op, a non-equivalent target fails closed, and neither side holding
// anything is the genuine missing-source failure.
func TestTenantAdopterMissingSourceClassification(t *testing.T) {
	specs.Describe(t, "an assigned aggregate whose source is absent is classified by what the target holds", func(s *specs.Spec) {
		var target persistence.Scope
		var acme tenancy.TenantContext
		var globex tenancy.TenantContext
		s.BeforeEach(func(ctx *specs.Context) {
			target = tenantScope(ctx, "acme")
			acme = tenantContextOf(ctx, "acme")
			globex = tenantContextOf(ctx, "globex")
		})

		run := func(ctx *specs.Context, eventsStore persistence.EventsStore, snapshotStore persistence.SnapshotStore, id string) *AdoptionReport {
			opts := []AdoptionOption{WithWriteEnabled(), WithAdoptionFence(newTestFence()), WithSourceDeletion(), WithPersistenceIDs(id)}
			if eventsStore != nil {
				opts = append(opts, WithEventsStore(eventsStore))
			}
			if snapshotStore != nil {
				opts = append(opts, WithSnapshotStore(snapshotStore))
			}
			return mustAdopt(ctx, newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}), opts...))
		}

		s.It("target events owned by another tenant fail closed", func(ctx *specs.Context) {
			eventsStore := connectedEventsStore(ctx)
			const id = "foreign-target-events"
			evt := newLegacyEvent(ctx, id, 1, 100)
			evt.TenantMetadata = tenancy.MarshalMetadata(globex)
			seedEvents(ctx, eventsStore, target, evt)

			report := run(ctx, eventsStore, nil, id)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errTargetNotEquivalent))
		})

		s.It("target snapshot without tenant metadata fails closed", func(ctx *specs.Context) {
			snapshotStore := adoptionSnapshotStore(ctx)
			const id = "untagged-target-snapshot"
			seedSnapshot(ctx, snapshotStore, target, newLegacySnapshot(ctx, id, 1, 100))

			report := run(ctx, nil, snapshotStore, id)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errTargetNotEquivalent))
		})

		s.It("unrelated target owned by the assigned tenant is not proof of adoption", func(ctx *specs.Context) {
			snapshotStore := adoptionSnapshotStore(ctx)
			const id = "owned-target-snapshot"
			snap := newLegacySnapshot(ctx, id, 1, 100)
			snap.TenantMetadata = tenancy.MarshalMetadata(acme)
			seedSnapshot(ctx, snapshotStore, target, snap)

			report := run(ctx, nil, snapshotStore, id)
			// Same-tenant data with no adoption receipt never counts as adopted.
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errTargetNotEquivalent))
		})

		s.It("owned target events without a receipt are not proof of adoption", func(ctx *specs.Context) {
			eventsStore := connectedEventsStore(ctx)
			const id = "owned-target-events"
			evt := newLegacyEvent(ctx, id, 1, 100)
			evt.TenantMetadata = tenancy.MarshalMetadata(acme)
			seedEvents(ctx, eventsStore, target, evt)

			report := run(ctx, eventsStore, nil, id)
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errTargetNotEquivalent))
		})

		s.It("neither source nor target is a missing-source failure", func(ctx *specs.Context) {
			eventsStore := connectedEventsStore(ctx)
			snapshotStore := adoptionSnapshotStore(ctx)

			report := run(ctx, eventsStore, snapshotStore, "nowhere")
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errNoSourceRecords))
		})
	})
}

// TestTenantAdopterTargetExtendedByLiveWritesIsAlreadyPresent covers a
// re-run after the tenant-bound actor has already appended to the adopted
// stream: the target still contains the source records exactly and every
// target event carries the assigned tenant, so it is already migrated.
func TestTenantAdopterTargetExtendedByLiveWritesIsAlreadyPresent(t *testing.T) {
	specs.Describe(t, "a target extended by live writes after adoption is still reported as already present", func(s *specs.Spec) {
		s.It("keeps the adopted prefix proving the migration", func(ctx *specs.Context) {
			bg := context.Background()
			eventsStore := connectedEventsStore(ctx)

			const id = "extended-1"
			source := persistence.Unscoped()
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, 100))

			adopter := newAdopter(ctx,
				fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(eventsStore),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()),
			)
			first, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(first.Copied).ToEqual(1)

			target := tenantScope(ctx, "acme")
			live := newLegacyEvent(ctx, id, 2, 200)
			live.TenantMetadata = tenancy.MarshalMetadata(tenantContextOf(ctx, "acme"))
			seedEvents(ctx, eventsStore, target, live)

			second, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(second.AlreadyPresent).ToEqual(1)
			ctx.Expect(second.Failed).ToEqual(0)
		})
	})
}

// TestTenantAdopterLaterSameTenantTargetIsNotEquivalent pins that a single
// latest snapshot or durable-state record cannot prove it descends from the
// source: a same-tenant target at a later position with an unrelated
// payload must fail closed, never count as already migrated.
func TestTenantAdopterLaterSameTenantTargetIsNotEquivalent(t *testing.T) {
	specs.Describe(t, "a same-tenant target at a later position with an unrelated payload fails closed", func(s *specs.Spec) {
		bg := context.Background()
		var target persistence.Scope
		var acme tenancy.TenantContext
		s.BeforeEach(func(ctx *specs.Context) {
			target = tenantScope(ctx, "acme")
			acme = tenantContextOf(ctx, "acme")
		})
		source := persistence.Unscoped()

		s.It("snapshot", func(ctx *specs.Context) {
			snapshotStore := adoptionSnapshotStore(ctx)
			const id = "later-snapshot"
			seedSnapshot(ctx, snapshotStore, source, newLegacySnapshot(ctx, id, 5, 500))
			unrelated := newLegacySnapshot(ctx, id, 6, 999)
			unrelated.TenantMetadata = tenancy.MarshalMetadata(acme)
			seedSnapshot(ctx, snapshotStore, target, unrelated)

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(snapshotStore), WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(newTestFence()), WithSourceDeletion())
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errTargetNotEquivalent))

			// The source is never deleted on a failed classification.
			ctx.Expect(latestSnapshot(ctx, snapshotStore, source, id)).To(specs.Not(specs.BeNil()))
		})

		s.It("durable state", func(ctx *specs.Context) {
			stateStore := connectedStateStore(ctx)
			const id = "later-state"
			seedState(ctx, stateStore, source, newLegacyDurableState(ctx, id, 5, 500))
			unrelated := newLegacyDurableState(ctx, id, 6, 999)
			unrelated.TenantMetadata = tenancy.MarshalMetadata(acme)
			seedState(ctx, stateStore, target, unrelated)

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithStateStore(stateStore), WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(newTestFence()))
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errTargetNotEquivalent))
		})
	})
}

// TestTenantAdopterSamePositionTargetClassification pins that a target at
// the source's exact position is already migrated only when it is the exact
// record this adoption writes, and fails closed when anything differs.
func TestTenantAdopterSamePositionTargetClassification(t *testing.T) {
	specs.Describe(t, "a target at the source's exact position is already migrated only when it is the exact adoption record", func(s *specs.Spec) {
		bg := context.Background()
		var target persistence.Scope
		var acme tenancy.TenantContext
		s.BeforeEach(func(ctx *specs.Context) {
			target = tenantScope(ctx, "acme")
			acme = tenantContextOf(ctx, "acme")
		})
		source := persistence.Unscoped()

		s.It("snapshot with a different payload at the same position fails", func(ctx *specs.Context) {
			snapshotStore := adoptionSnapshotStore(ctx)
			const id = "same-seq-snapshot"
			seedSnapshot(ctx, snapshotStore, source, newLegacySnapshot(ctx, id, 5, 500))
			different := newLegacySnapshot(ctx, id, 5, 999)
			different.TenantMetadata = tenancy.MarshalMetadata(acme)
			seedSnapshot(ctx, snapshotStore, target, different)

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(snapshotStore), WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(newTestFence()))
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
		})

		s.It("durable state with a different payload at the same version fails", func(ctx *specs.Context) {
			stateStore := connectedStateStore(ctx)
			const id = "same-version-state"
			seedState(ctx, stateStore, source, newLegacyDurableState(ctx, id, 5, 500))
			different := newLegacyDurableState(ctx, id, 5, 999)
			different.TenantMetadata = tenancy.MarshalMetadata(acme)
			seedState(ctx, stateStore, target, different)

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithStateStore(stateStore), WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(newTestFence()))
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
		})

		s.It("exact snapshot and durable state records are already present", func(ctx *specs.Context) {
			snapshotStore := adoptionSnapshotStore(ctx)
			stateStore := connectedStateStore(ctx)
			const id = "exact-records"
			seedSnapshot(ctx, snapshotStore, source, newLegacySnapshot(ctx, id, 5, 500))
			seedState(ctx, stateStore, source, newLegacyDurableState(ctx, id, 5, 500))

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(snapshotStore), WithStateStore(stateStore), WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(newTestFence()))
			first, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(first.Copied).ToEqual(1)

			second, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(second.AlreadyPresent).ToEqual(1)
			ctx.Expect(second.Failed).ToEqual(0)
		})
	})
}

// TestTenantAdopterReceiptProvesAdoptionAfterSourceDeletion pins the
// contract for a re-run whose source is gone: the target is already
// migrated only when it still carries a valid adoption receipt, which
// binds its exact content and the source scope it was adopted from. A
// receipt whose record was altered afterwards, or one written for a
// different source scope, is not proof.
func TestTenantAdopterReceiptProvesAdoptionAfterSourceDeletion(t *testing.T) {
	specs.Describe(t, "after source deletion only a valid adoption receipt proves the target was adopted", func(s *specs.Spec) {
		bg := context.Background()
		var target persistence.Scope
		s.BeforeEach(func(ctx *specs.Context) {
			target = tenantScope(ctx, "acme")
		})
		source := persistence.Unscoped()

		adoptAndDelete := func(ctx *specs.Context, snapshotStore *testkit.SnapshotStore, id string) {
			seedSnapshot(ctx, snapshotStore, source, newLegacySnapshot(ctx, id, 5, 500))
			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(snapshotStore), WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(newTestFence()), WithSourceDeletion())
			ctx.Expect(mustAdopt(ctx, adopter).SourceDeleted).ToEqual(1)
		}

		s.It("a record altered after adoption no longer matches its receipt", func(ctx *specs.Context) {
			snapshotStore := adoptionSnapshotStore(ctx)
			const id = "tampered-receipt"
			adoptAndDelete(ctx, snapshotStore, id)

			adopted := latestSnapshot(ctx, snapshotStore, target, id)
			tampered, ok := proto.Clone(adopted).(*egopb.Snapshot)
			ctx.Expect(ok).To(specs.BeTrue())
			tampered.Timestamp = 999
			seedSnapshot(ctx, snapshotStore, target, tampered)

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(snapshotStore), WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(newTestFence()), WithSourceDeletion())
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
		})

		s.It("a receipt for a different source scope is not proof", func(ctx *specs.Context) {
			snapshotStore := adoptionSnapshotStore(ctx)
			const id = "other-source-scope"
			adoptAndDelete(ctx, snapshotStore, id)

			otherSource := tenantScope(ctx, "legacy-partition")
			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(snapshotStore), WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(newTestFence()), WithSourceScope(otherSource))
			report, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
		})

		s.It("adopted events followed by live writes are still proven", func(ctx *specs.Context) {
			eventsStore := connectedEventsStore(ctx)
			const id = "events-then-live"
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 2, 200))
			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(eventsStore), WithWriteEnabled(), WithAdoptionFence(newTestFence()), WithSourceDeletion())
			first, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(first.SourceDeleted).ToEqual(1)

			live := newLegacyEvent(ctx, id, 3, 300)
			live.TenantMetadata = tenancy.MarshalMetadata(tenantContextOf(ctx, "acme"))
			seedEvents(ctx, eventsStore, target, live)

			second, err := adopter.Run(bg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(second.AlreadyPresent).ToEqual(1)
			ctx.Expect(second.Failed).ToEqual(0)
		})
	})
}

func TestNewTenantAdopterRejectsZeroScanPageSize(t *testing.T) {
	specs.Describe(t, "NewTenantAdopter rejects a zero scan page size", func(s *specs.Spec) {
		s.It("fails instead of scanning nothing and reporting success", func(ctx *specs.Context) {
			_, err := NewTenantAdopter(fixedAssignment(nil), WithEventsStore(testkit.NewEventsStore()), WithScanPageSize(0))
			ctx.Expect(err).To(specs.MatchError(ErrInvalidScanPageSize))
		})
	})
}

// racingEventsStore appends a new source event at a chosen moment, simulating
// a legacy writer that is still accepting writes while the adopter runs.
type racingEventsStore struct {
	persistence.EventsStore
	source persistence.Scope
	target persistence.Scope
	late   *egopb.Event
	// onTargetRead appends late during the target read-back verification;
	// otherwise it is appended at the start of the source DeleteEvents call.
	onTargetRead bool
	fired        bool
}

func (r *racingEventsStore) fire(ctx context.Context) error {
	if r.fired {
		return nil
	}
	r.fired = true
	return r.WriteEvents(ctx, r.source, []*egopb.Event{r.late}, persistence.Unconditional())
}

func (r *racingEventsStore) ReplayEvents(ctx context.Context, scope persistence.Scope, persistenceID string, from, to, maxNumber uint64) ([]*egopb.Event, error) {
	events, err := r.EventsStore.ReplayEvents(ctx, scope, persistenceID, from, to, maxNumber)
	if err == nil && r.onTargetRead && scope.Equal(r.target) && len(events) > 0 {
		if fireErr := r.fire(ctx); fireErr != nil {
			return nil, fireErr
		}
	}
	return events, err
}

func (r *racingEventsStore) DeleteEvents(ctx context.Context, scope persistence.Scope, persistenceID string, toSequenceNumber uint64) error {
	if !r.onTargetRead && scope.Equal(r.source) {
		if err := r.fire(ctx); err != nil {
			return err
		}
	}
	return r.EventsStore.DeleteEvents(ctx, scope, persistenceID, toSequenceNumber)
}

// TestTenantAdopterSourceDeletionRefusesSuccessUnderConcurrentWrites covers
// a source that keeps accepting writes during a deleting run: the run must
// never report source_deleted while an event exists only in the source.
func TestTenantAdopterSourceDeletionRefusesSuccessUnderConcurrentWrites(t *testing.T) {
	specs.Describe(t, "a source that keeps accepting writes during a deleting run is never reported as deleted", func(s *specs.Spec) {
		source := persistence.Unscoped()
		var target persistence.Scope
		s.BeforeEach(func(ctx *specs.Context) {
			target = tenantScope(ctx, "acme")
		})

		type tableCase struct {
			name         string
			onTargetRead bool
			wantSource   int
		}
		specs.Table(s, []tableCase{
			{name: "a write after verification but before deletion prevents the deletion", onTargetRead: true, wantSource: 3},
			{name: "a write racing the deletion is detected and not reported as success", onTargetRead: false, wantSource: 1},
		}, func(tc tableCase) string { return tc.name }, func(ctx *specs.Context, tc tableCase) {
			base := connectedEventsStore(ctx)
			id := "racing-" + uuid.NewString()
			seedEvents(ctx, base, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 2, 200))

			store := &racingEventsStore{EventsStore: base, source: source, target: target,
				late: newLegacyEvent(ctx, id, 3, 300), onTargetRead: tc.onTargetRead}
			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(store), WithWriteEnabled(), WithAdoptionFence(newTestFence()), WithSourceDeletion())

			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			// A source that changed during the run is not reported as deleted.
			ctx.Expect(report.SourceDeleted).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errSourceChangedDuringAdoption))

			// The event written during the run still exists in the source.
			ctx.Expect(replayEvents(ctx, base, source, id, 1, 10, 10)).To(specs.HaveLen(tc.wantSource))
		})
	})
}

// racingSnapshotStore writes a newer source snapshot as the adopter deletes
// the adopted one.
type racingSnapshotStore struct {
	persistence.SnapshotStore
	source persistence.Scope
	late   *egopb.Snapshot
	fired  bool
}

func (r *racingSnapshotStore) DeleteSnapshots(ctx context.Context, scope persistence.Scope, persistenceID string, toSequenceNumber uint64) error {
	if !r.fired && scope.Equal(r.source) {
		r.fired = true
		if err := r.WriteSnapshot(ctx, r.source, r.late); err != nil {
			return err
		}
	}
	return r.SnapshotStore.DeleteSnapshots(ctx, scope, persistenceID, toSequenceNumber)
}

func TestTenantAdopterSnapshotDeletionRefusesSuccessUnderConcurrentWrites(t *testing.T) {
	specs.Describe(t, "a snapshot written to the source while it is deleted is never reported as deleted", func(s *specs.Spec) {
		s.It("fails the aggregate and keeps the newer snapshot", func(ctx *specs.Context) {
			source := persistence.Unscoped()
			base := adoptionSnapshotStore(ctx)
			const id = "racing-snapshot"
			seedSnapshot(ctx, base, source, newLegacySnapshot(ctx, id, 2, 200))

			store := &racingSnapshotStore{SnapshotStore: base, source: source, late: newLegacySnapshot(ctx, id, 3, 300)}
			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(store), WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(newTestFence()), WithSourceDeletion())

			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.SourceDeleted).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errSourceChangedDuringAdoption))

			// The snapshot written during the run must survive.
			latest := latestSnapshot(ctx, base, source, id)
			ctx.Expect(latest).To(specs.Not(specs.BeNil()))
			ctx.Expect(latest.GetSequenceNumber()).ToEqual(uint64(3))
		})
	})
}

// TestTenantAdopterDeletesSourceOfVerifiedExistingTarget covers a run that
// enables WithSourceDeletion after an earlier run copied without it: the
// target is proven to be the exact adoption, so the source is deleted under
// the same guard as a fresh copy, and nothing is reported as copied.
func TestTenantAdopterDeletesSourceOfVerifiedExistingTarget(t *testing.T) {
	specs.Describe(t, "enabling source deletion after an earlier copy deletes the source of the verified target", func(s *specs.Spec) {
		s.It("deletes the source and reports nothing as copied", func(ctx *specs.Context) {
			source := persistence.Unscoped()
			eventsStore := connectedEventsStore(ctx)
			snapshotStore := adoptionSnapshotStore(ctx)

			const id = "copy-then-delete"
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 2, 200))
			seedSnapshot(ctx, snapshotStore, source, newLegacySnapshot(ctx, id, 2, 200))

			assignment := fixedAssignment(map[string]tenancy.TenantID{id: "acme"})
			keep := newAdopter(ctx, assignment, WithEventsStore(eventsStore), WithSnapshotStore(snapshotStore), WithWriteEnabled(), WithAdoptionFence(newTestFence()))
			first, err := keep.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(first.Copied).ToEqual(1)
			ctx.Expect(first.SourceDeleted).ToEqual(0)

			deleting := newAdopter(ctx, assignment, WithEventsStore(eventsStore), WithSnapshotStore(snapshotStore), WithWriteEnabled(), WithAdoptionFence(newTestFence()), WithSourceDeletion())
			second, err := deleting.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(second.Failed).ToEqual(0)
			// Nothing was copied: the target was already the exact adoption.
			ctx.Expect(second.AlreadyPresent).ToEqual(1)
			ctx.Expect(second.Copied).ToEqual(0)
			ctx.Expect(second.Verified).ToEqual(0)
			// The verified source is now deleted.
			ctx.Expect(second.SourceDeleted).ToEqual(1)
			ctx.Expect(second.Aggregates).To(specs.HaveLen(1))
			ctx.Expect(second.Aggregates[0].Events.Status).ToEqual(StatusSourceDeleted)
			ctx.Expect(second.Aggregates[0].Snapshot.Status).ToEqual(StatusSourceDeleted)

			ctx.Expect(replayEvents(ctx, eventsStore, source, id, 1, 10, 10)).To(specs.BeEmpty())
			ctx.Expect(latestSnapshot(ctx, snapshotStore, source, id)).To(specs.BeNil())
		})
	})
}

// replacingSnapshotStore overwrites the source snapshot at the SAME sequence
// number while the adopter reads back the target, the way a concurrent
// writer can with a store that keys snapshots by (scope, id, sequence).
type replacingSnapshotStore struct {
	persistence.SnapshotStore
	source, target persistence.Scope
	replacement    *egopb.Snapshot
	targetReads    int
}

func (r *replacingSnapshotStore) GetLatestSnapshot(ctx context.Context, scope persistence.Scope, persistenceID string) (*egopb.Snapshot, error) {
	snapshot, err := r.SnapshotStore.GetLatestSnapshot(ctx, scope, persistenceID)
	if err == nil && scope.Equal(r.target) && snapshot != nil {
		r.targetReads++
		if r.targetReads == 1 {
			if writeErr := r.WriteSnapshot(ctx, r.source, r.replacement); writeErr != nil {
				return nil, writeErr
			}
		}
	}
	return snapshot, err
}

func TestTenantAdopterRefusesDeletionOfReplacedSameSequenceSnapshot(t *testing.T) {
	specs.Describe(t, "a source snapshot replaced at the same sequence during the run is never deleted", func(s *specs.Spec) {
		s.It("fails the aggregate and keeps the replacement", func(ctx *specs.Context) {
			source := persistence.Unscoped()
			target := tenantScope(ctx, "acme")
			base := adoptionSnapshotStore(ctx)

			const id = "replaced-snapshot"
			seedSnapshot(ctx, base, source, newLegacySnapshot(ctx, id, 4, 400))
			replacement := newLegacySnapshot(ctx, id, 4, 444)

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(&replacingSnapshotStore{SnapshotStore: base, source: source, target: target, replacement: replacement}),
				WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(newTestFence()), WithSourceDeletion())
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.SourceDeleted).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errSourceChangedDuringAdoption))

			// The replacement snapshot must survive.
			kept := latestSnapshot(ctx, base, source, id)
			ctx.Expect(kept).To(specs.Not(specs.BeNil()))
			ctx.Expect(proto.Equal(replacement, kept)).To(specs.BeTrue())
		})
	})
}

// replacingEventsStore rewrites an existing source event in place (same
// sequence number) while the adopter reads back the target.
type replacingEventsStore struct {
	persistence.EventsStore
	source, target persistence.Scope
	replacement    *egopb.Event
	fired          bool
}

func (r *replacingEventsStore) ReplayEvents(ctx context.Context, scope persistence.Scope, persistenceID string, from, to, maxNumber uint64) ([]*egopb.Event, error) {
	events, err := r.EventsStore.ReplayEvents(ctx, scope, persistenceID, from, to, maxNumber)
	if err == nil && !r.fired && scope.Equal(r.target) && len(events) > 0 {
		r.fired = true
		if writeErr := r.WriteEvents(ctx, r.source, []*egopb.Event{r.replacement}, persistence.Unconditional()); writeErr != nil {
			return nil, writeErr
		}
	}
	return events, err
}

func TestTenantAdopterRefusesDeletionOfRewrittenSourceEvent(t *testing.T) {
	specs.Describe(t, "a source event rewritten in place during the run is never deleted", func(s *specs.Spec) {
		s.It("fails the aggregate and keeps the source events", func(ctx *specs.Context) {
			source := persistence.Unscoped()
			target := tenantScope(ctx, "acme")
			base := connectedEventsStore(ctx)

			const id = "rewritten-event"
			seedEvents(ctx, base, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 2, 200))

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(&replacingEventsStore{EventsStore: base, source: source, target: target, replacement: newLegacyEvent(ctx, id, 2, 222)}),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()), WithSourceDeletion())
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.SourceDeleted).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errSourceChangedDuringAdoption))

			// A source that was rewritten must not be deleted.
			ctx.Expect(replayEvents(ctx, base, source, id, 1, 10, 10)).To(specs.HaveLen(2))
		})
	})
}

// testFence is an AdoptionFence for tests: one blocking lock per (scope,
// persistence id), with mock.Spy recorders for every acquire and release
// (which also give the order locks were taken in), and optional failure
// injection. A writer in a test that honors the fence
// calls Acquire exactly like the adopter does.
type testFence struct {
	mu       sync.Mutex
	locks    map[string]chan struct{}
	acquires *mock.Spy
	releases *mock.Spy
	failOn   string
}

var errTestFenceUnavailable = errors.New("test fence: unavailable")

func newTestFence() *testFence {
	return &testFence{locks: make(map[string]chan struct{}), acquires: mock.NewSpy(), releases: mock.NewSpy()}
}

func testFenceKey(scope persistence.Scope, persistenceID string) string {
	return scope.String() + "|" + persistenceID
}

func (f *testFence) lockFor(key string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.locks[key]
	if !ok {
		ch = make(chan struct{}, 1)
		f.locks[key] = ch
	}
	return ch
}

func (f *testFence) Acquire(ctx context.Context, scope persistence.Scope, persistenceID string) (func(), error) {
	key := testFenceKey(scope, persistenceID)
	f.mu.Lock()
	fail := f.failOn == key
	f.mu.Unlock()
	if fail {
		return nil, errTestFenceUnavailable
	}
	ch := f.lockFor(key)
	select {
	case ch <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	f.acquires.Call(key)
	var once sync.Once
	return func() {
		once.Do(func() {
			<-ch
			f.releases.Call(key)
		})
	}, nil
}

func (f *testFence) isHeld(scope persistence.Scope, persistenceID string) bool {
	return len(f.lockFor(testFenceKey(scope, persistenceID))) == 1
}

func (f *testFence) counts() (acquired, released int) {
	return f.acquires.CallCount(), f.releases.CallCount()
}

// order returns the keys of the acquired fences, in the order they were taken.
func (f *testFence) order() []string {
	calls := f.acquires.Calls()
	keys := make([]string, len(calls))
	for i, c := range calls {
		keys[i], _ = c.Args[0].(string)
	}
	return keys
}

func TestNewTenantAdopterRequiresAFenceToWrite(t *testing.T) {
	specs.Describe(t, "NewTenantAdopter requires an adoption fence for any write-enabled configuration", func(s *specs.Spec) {
		s.It("rejects writes and source deletion without a fence and lets a dry run go without one", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)

			// A write-enabled adoption must hold a fence.
			_, err := NewTenantAdopter(fixedAssignment(nil), WithEventsStore(store), WithWriteEnabled())
			ctx.Expect(err).To(specs.MatchError(ErrAdoptionFenceRequired))

			// Source deletion never relies on an informal quiescence promise.
			_, err = NewTenantAdopter(fixedAssignment(nil), WithEventsStore(store), WithWriteEnabled(), WithSourceDeletion())
			ctx.Expect(err).To(specs.MatchError(ErrAdoptionFenceRequired))

			// A dry run reads only and needs no fence.
			dryRun, err := NewTenantAdopter(fixedAssignment(nil), WithEventsStore(store))
			ctx.Expect(err).To(specs.BeNil())
			_, err = dryRun.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
		})
	})
}

// fencedSnapshotWriter simulates another writer of the target snapshot that
// honors the fence. It fires when the adopter first finds the target
// empty: if the fence is not held for the target, it writes right there,
// between the adopter's existence check and its write; if it is held, it
// queues behind the fence and writes after the adopter releases it.
type fencedSnapshotWriter struct {
	persistence.SnapshotStore
	fence   *testFence
	target  persistence.Scope
	payload *egopb.Snapshot
	fired   bool
	done    chan struct{}
}

func (w *fencedSnapshotWriter) GetLatestSnapshot(ctx context.Context, scope persistence.Scope, persistenceID string) (*egopb.Snapshot, error) {
	snapshot, err := w.SnapshotStore.GetLatestSnapshot(ctx, scope, persistenceID)
	if err != nil || w.fired || snapshot != nil || !scope.Equal(w.target) {
		return snapshot, err
	}
	w.fired = true
	write := func() {
		defer close(w.done)
		release, err := w.fence.Acquire(context.Background(), w.target, persistenceID)
		if err != nil {
			return
		}
		defer release()
		_ = w.WriteSnapshot(context.Background(), w.target, w.payload)
	}
	if w.fence.isHeld(w.target, persistenceID) {
		go write()
	} else {
		write()
	}
	return snapshot, err
}

// TestTenantAdopterNeverOverwritesAConcurrentlyCreatedTargetSnapshot covers
// the snapshot SPI's missing write precondition: a target snapshot another
// writer creates after the adopter found the target empty must never be
// overwritten by the stale source snapshot.
func TestTenantAdopterNeverOverwritesAConcurrentlyCreatedTargetSnapshot(t *testing.T) {
	specs.Describe(t, "a target snapshot another writer creates mid-run is never overwritten by the stale source snapshot", func(s *specs.Spec) {
		s.It("keeps the other writer's snapshot", func(ctx *specs.Context) {
			source := persistence.Unscoped()
			target := tenantScope(ctx, "acme")
			base := adoptionSnapshotStore(ctx)

			const id = "raced-target-snapshot"
			seedSnapshot(ctx, base, source, newLegacySnapshot(ctx, id, 4, 400))
			live := newLegacySnapshot(ctx, id, 4, 999)
			live.TenantMetadata = tenancy.MarshalMetadata(tenantContextOf(ctx, "acme"))

			fence := newTestFence()
			writer := &fencedSnapshotWriter{SnapshotStore: base, fence: fence, target: target, payload: live, done: make(chan struct{})}
			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(writer), WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(fence))
			_, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			<-writer.done

			final := latestSnapshot(ctx, base, target, id)
			ctx.Expect(final).To(specs.Not(specs.BeNil()))
			ctx.Expect(proto.Equal(live, final)).To(specs.BeTrue())
		})
	})
}

// raceTargetStateStore creates the target durable state between the
// adopter's existence check and its write, bypassing any fence, to show that
// durable state does not share the snapshot race: WriteState with
// ExpectGenesis is an atomic compare-and-swap.
type raceTargetStateStore struct {
	persistence.StateStore
	target  persistence.Scope
	payload *egopb.DurableState
	fired   bool
}

func (r *raceTargetStateStore) GetLatestState(ctx context.Context, scope persistence.Scope, persistenceID string) (*egopb.DurableState, error) {
	state, err := r.StateStore.GetLatestState(ctx, scope, persistenceID)
	if err == nil && state == nil && !r.fired && scope.Equal(r.target) {
		r.fired = true
		if writeErr := r.WriteState(ctx, r.target, r.payload, persistence.ExpectGenesis()); writeErr != nil {
			return nil, writeErr
		}
	}
	return state, err
}

func TestTenantAdopterDurableStateTargetRaceIsStoppedByItsPrecondition(t *testing.T) {
	specs.Describe(t, "a durable state created in the target mid-run is stopped by the write precondition", func(s *specs.Spec) {
		s.It("fails the aggregate and keeps the racing writer's state", func(ctx *specs.Context) {
			source := persistence.Unscoped()
			target := tenantScope(ctx, "acme")
			base := connectedStateStore(ctx)

			const id = "raced-target-state"
			seedState(ctx, base, source, newLegacyDurableState(ctx, id, 3, 300))
			live := newLegacyDurableState(ctx, id, 1, 999)
			live.TenantMetadata = tenancy.MarshalMetadata(tenantContextOf(ctx, "acme"))

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithStateStore(&raceTargetStateStore{StateStore: base, target: target, payload: live}),
				WithPersistenceIDs(id), WithWriteEnabled(), WithAdoptionFence(newTestFence()))
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			// The raced target is not this adoption.
			ctx.Expect(report.Failed).ToEqual(1)

			// The racing writer's durable state must never be overwritten.
			final := latestState(ctx, base, target, id)
			ctx.Expect(proto.Equal(live, final)).To(specs.BeTrue())
		})
	})
}

// fencedSourceWriter appends a source event while the adopter verifies the
// target, honoring the fence: under the fence it can only write after the
// adopter released, so it can never interleave with the deletion.
type fencedSourceWriter struct {
	persistence.EventsStore
	fence          *testFence
	source, target persistence.Scope
	late           *egopb.Event
	fired          bool
	done           chan struct{}
}

func (w *fencedSourceWriter) ReplayEvents(ctx context.Context, scope persistence.Scope, persistenceID string, from, to, maxNumber uint64) ([]*egopb.Event, error) {
	events, err := w.EventsStore.ReplayEvents(ctx, scope, persistenceID, from, to, maxNumber)
	if err != nil || w.fired || len(events) == 0 || !scope.Equal(w.target) {
		return events, err
	}
	w.fired = true
	write := func() {
		defer close(w.done)
		release, err := w.fence.Acquire(context.Background(), w.source, persistenceID)
		if err != nil {
			return
		}
		defer release()
		_ = w.WriteEvents(context.Background(), w.source, []*egopb.Event{w.late}, persistence.Unconditional())
	}
	if w.fence.isHeld(w.source, persistenceID) {
		go write()
	} else {
		write()
	}
	return events, err
}

func TestTenantAdopterFencedSourceWriterCannotInterleaveWithDeletion(t *testing.T) {
	specs.Describe(t, "a fence-honoring source writer is held off until the source deletion completed", func(s *specs.Spec) {
		s.It("lands the held-off write only after the deletion", func(ctx *specs.Context) {
			source := persistence.Unscoped()
			target := tenantScope(ctx, "acme")
			base := connectedEventsStore(ctx)

			const id = "fenced-source-writer"
			seedEvents(ctx, base, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 2, 200))

			fence := newTestFence()
			writer := &fencedSourceWriter{EventsStore: base, fence: fence, source: source, target: target, late: newLegacyEvent(ctx, id, 3, 300), done: make(chan struct{})}
			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(writer), WithWriteEnabled(), WithSourceDeletion(), WithAdoptionFence(fence))
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			<-writer.done

			// The verified source is deleted while the writer is held off.
			ctx.Expect(report.SourceDeleted).ToEqual(1)
			ctx.Expect(report.Failed).ToEqual(0)
			// The held-off write lands only after the deletion completed.
			remaining := replayEvents(ctx, base, source, id, 1, 10, 10)
			ctx.Expect(remaining).To(specs.HaveLen(1))
			ctx.Expect(remaining[0].GetSequenceNumber()).ToEqual(uint64(3))
		})
	})
}

// panickingEventsStore panics on target writes, to prove the fence is
// released even when adoption panics.
type panickingEventsStore struct {
	persistence.EventsStore
	target persistence.Scope
}

func (p *panickingEventsStore) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	if scope.Equal(p.target) {
		panic("panickingEventsStore: target write")
	}
	return p.EventsStore.WriteEvents(ctx, scope, events, precondition)
}

func TestTenantAdopterReleasesItsFencesOnEveryPath(t *testing.T) {
	specs.Describe(t, "every fence the adopter acquires is released on every exit path", func(s *specs.Spec) {
		bg := context.Background()
		source := persistence.Unscoped()
		var target persistence.Scope
		s.BeforeEach(func(ctx *specs.Context) {
			target = tenantScope(ctx, "acme")
		})

		seeded := func(ctx *specs.Context, id string) *testkit.EventStore {
			store := connectedEventsStore(ctx)
			seedEvents(ctx, store, source, newLegacyEvent(ctx, id, 1, 100))
			return store
		}
		run := func(ctx *specs.Context, fence *testFence, store persistence.EventsStore, id string, runCtx context.Context) *AdoptionReport {
			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(store), WithWriteEnabled(), WithSourceDeletion(), WithAdoptionFence(fence))
			report, err := adopter.Run(runCtx)
			ctx.Expect(err).To(specs.BeNil())
			return report
		}
		expectBalanced := func(ctx *specs.Context, fence *testFence, wantAcquired int) {
			acquired, released := fence.counts()
			ctx.Expect(acquired).ToEqual(wantAcquired)
			// Every acquired fence must be released.
			ctx.Expect(released).ToEqual(acquired)
		}

		s.It("success", func(ctx *specs.Context) {
			fence := newTestFence()
			report := run(ctx, fence, seeded(ctx, "ok"), "ok", bg)
			ctx.Expect(report.SourceDeleted).ToEqual(1)
			expectBalanced(ctx, fence, 2)
		})

		s.It("failed verification", func(ctx *specs.Context) {
			fence := newTestFence()
			store := &corruptingEventsStore{EventsStore: seeded(ctx, "corrupt"), corruptScope: target, mangle: func(e *egopb.Event) { e.Event = nil }}
			report := run(ctx, fence, store, "corrupt", bg)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.SourceDeleted).ToEqual(0)
			expectBalanced(ctx, fence, 2)
		})

		s.It("second fence unavailable", func(ctx *specs.Context) {
			fence := newTestFence()
			fence.failOn = testFenceKey(target, "unavailable")
			store := seeded(ctx, "unavailable")
			report := run(ctx, fence, store, "unavailable", bg)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errTestFenceUnavailable))
			expectBalanced(ctx, fence, 1)
			// Nothing may be written without both fences.
			ctx.Expect(latestEvent(ctx, store, target, "unavailable")).To(specs.BeNil())
		})

		s.It("cancelled while waiting for a fence", func(ctx *specs.Context) {
			fence := newTestFence()
			hold, err := fence.Acquire(bg, target, "waiting")
			ctx.Expect(err).To(specs.BeNil())
			defer hold()
			store := seeded(ctx, "waiting")
			// The deadline has already passed, so the wait ends at once instead of after a real delay.
			waitCtx, cancel := context.WithDeadline(bg, time.Now().Add(-time.Second))
			defer cancel()
			report := run(ctx, fence, store, "waiting", waitCtx)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(context.DeadlineExceeded))
			acquired, released := fence.counts()
			// Only the test's own hold may remain.
			ctx.Expect(released).ToEqual(acquired - 1)
		})

		s.It("panic", func(ctx *specs.Context) {
			fence := newTestFence()
			store := &panickingEventsStore{EventsStore: seeded(ctx, "panic"), target: target}
			ctx.Expect(panics(func() { run(ctx, fence, store, "panic", bg) })).To(specs.BeTrue())
			expectBalanced(ctx, fence, 2)
		})
	})
}

// TestTenantAdopterAcquiresFencesInDeterministicOrder pins that the two
// fences of an aggregate are always taken in the same global order,
// whichever scope is the source, so two runs moving data in opposite
// directions can never deadlock each other.
func TestTenantAdopterAcquiresFencesInDeterministicOrder(t *testing.T) {
	specs.Describe(t, "the two fences of an aggregate are taken in one global order whichever scope is the source", func(s *specs.Spec) {
		s.It("uses the same lock order for opposite moves", func(ctx *specs.Context) {
			north := tenantScope(ctx, "north")
			south := tenantScope(ctx, "south")

			orderFor := func(from persistence.Scope, to tenancy.TenantID) []string {
				store := connectedEventsStore(ctx)
				fence := newTestFence()
				adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{"shared": to}),
					WithEventsStore(store), WithPersistenceIDs("shared"), WithSourceScope(from), WithWriteEnabled(), WithAdoptionFence(fence))
				mustAdopt(ctx, adopter)
				return fence.order()
			}

			northToSouth := orderFor(north, "south")
			southToNorth := orderFor(south, "north")
			ctx.Expect(northToSouth).To(specs.HaveLen(2))
			// The lock order must not depend on the direction of the move.
			ctx.Expect(southToNorth).ToEqual(northToSouth)
		})
	})
}

func TestTenantAdopterRejectsATargetEqualToTheSource(t *testing.T) {
	specs.Describe(t, "an adoption whose target scope equals its source scope fails before taking any fence", func(s *specs.Spec) {
		s.It("reports errTargetIsSource and acquires no fence", func(ctx *specs.Context) {
			acme := tenantScope(ctx, "acme")
			store := connectedEventsStore(ctx)

			fence := newTestFence()
			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{"self": "acme"}),
				WithEventsStore(store), WithPersistenceIDs("self"), WithSourceScope(acme), WithWriteEnabled(), WithAdoptionFence(fence))
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errTargetIsSource))
			ctx.Expect(fence.acquires.WasCalled()).To(specs.BeFalse())
		})
	})
}

// scopedLegacyAccountEvent builds a legacy event in a tenant scope: an
// AccountCreated payload plus the pre-snapshot resulting_state (field 5),
// carrying metadata as its tenant_metadata.
func scopedLegacyAccountEvent(ctx *specs.Context, id string, balance float64, metadata map[string]string) *egopb.Event {
	created, err := anypb.New(&testpb.AccountCreated{AccountId: id, AccountBalance: balance})
	ctx.Expect(err).To(specs.BeNil())
	state, err := anypb.New(&testpb.Account{AccountId: id, AccountBalance: balance})
	ctx.Expect(err).To(specs.BeNil())
	evt := new(egopb.Event)
	ctx.Expect(proto.Unmarshal(legacyEventBytes(ctx, id, 1, created, state, time.Now().Unix(), 0), evt)).To(specs.BeNil())
	evt.TenantMetadata = metadata
	return evt
}

// TestScopedMigratorSnapshotRecoversThroughTenantAwareActor proves a
// snapshot the Migrator writes in a tenant scope carries that tenant's
// metadata: a real tenant-aware EventSourcedActor loads it first and must
// recover from it rather than reject it.
func TestScopedMigratorSnapshotRecoversThroughTenantAwareActor(t *testing.T) {
	specs.Describe(t, "a snapshot the scoped Migrator writes is accepted by a tenant-aware actor", func(s *specs.Spec) {
		s.It("recovers from the migrated snapshot and keeps applying commands", func(ctx *specs.Context) {
			bg := context.Background()
			acme, err := persistence.NewTenantScope("acme")
			ctx.Expect(err).To(specs.BeNil())
			acmeContext, err := tenancy.NewTenantContext("acme")
			ctx.Expect(err).To(specs.BeNil())

			eventsStore := testkit.NewEventsStore()
			ctx.Expect(eventsStore.Connect(bg)).To(specs.BeNil())
			snapshotStore := testkit.NewSnapshotStore()
			ctx.Expect(snapshotStore.Connect(bg)).To(specs.BeNil())

			entityID := "acct-" + uuid.NewString()
			ctx.Expect(eventsStore.WriteEvents(bg, acme, []*egopb.Event{
				scopedLegacyAccountEvent(ctx, entityID, 100, tenancy.MarshalMetadata(acmeContext)),
			}, persistence.Unconditional())).To(specs.BeNil())

			migrator, err := New(eventsStore, snapshotStore, WithScope(acme))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(migrator.Run(bg)).To(specs.BeNil())

			snapshot, err := snapshotStore.GetLatestSnapshot(bg, acme, entityID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(snapshot).To(specs.Not(specs.BeNil()))
			// The scoped snapshot must carry tenant metadata.
			snapshotTenant, err := tenancy.UnmarshalMetadata(tenancy.Metadata(snapshot.GetTenantMetadata()))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(snapshotTenant).ToEqual(acmeContext)

			resolver, err := tenancy.WithSingleTenant("acme")
			ctx.Expect(err).To(specs.BeNil())
			cfg := engine.NewConfig(eventsStore, engine.WithTenantResolver(resolver), engine.WithSnapshotStore(snapshotStore))
			sys, err := goakt.NewActorSystem("ScopedMigratorE2E-"+uuid.NewString(), cfg.GoaktOptions()...)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(sys.Start(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = sys.Stop(context.Background()) })
			eng, err := engine.NewEngine(sys, cfg)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(eng.Start(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = eng.Stop(context.Background()) })

			// A tenant-aware actor must recover from the migrated snapshot.
			ctx.Expect(eng.Entity(bg, &adoptionAccountBehavior{id: entityID})).To(specs.BeNil()) //nolint:staticcheck // exercises the deprecated API on purpose (#124)
			state, _, err := eng.SendCommand(bg, entityID, &testpb.CreditAccount{AccountId: entityID, Balance: 50}, time.Minute)
			ctx.Expect(err).To(specs.BeNil())
			account, ok := state.(*testpb.Account)
			ctx.Expect(ok).To(specs.BeTrue())
			// 150 == migrated snapshot balance (100) + credit (50).
			ctx.Expect(account.GetAccountBalance()).ToEqual(float64(150))
		})
	})
}

// TestScopedMigratorFailsClosedOnUnprovableTenantMetadata pins that a
// scoped run never writes a snapshot a tenant-aware actor would reject, or
// one stamped for another tenant: the source event must carry exactly the
// selected scope's tenant.
func TestScopedMigratorFailsClosedOnUnprovableTenantMetadata(t *testing.T) {
	specs.Describe(t, "a scoped migrator run never writes a snapshot without proven tenant metadata for its scope", func(s *specs.Spec) {
		bg := context.Background()
		var acme persistence.Scope
		var globex tenancy.TenantContext
		s.BeforeEach(func(ctx *specs.Context) {
			acme = tenantScope(ctx, "acme")
			globex = tenantContextOf(ctx, "globex")
		})

		type tableCase struct {
			name     string
			metadata func() map[string]string // evaluated inside the case: globex is set by BeforeEach
			wantErr  error
		}
		specs.Table(s, []tableCase{
			{name: "missing tenant metadata", metadata: func() map[string]string { return nil }, wantErr: tenancy.ErrInvalid},
			{name: "another tenant's metadata", metadata: func() map[string]string { return tenancy.MarshalMetadata(globex) }, wantErr: tenancy.ErrDenied},
		}, func(tc tableCase) string { return tc.name }, func(ctx *specs.Context, tc tableCase) {
			eventsStore := connectedEventsStore(ctx)
			snapshotStore := adoptionSnapshotStore(ctx)
			const id = "unprovable"
			seedEvents(ctx, eventsStore, acme, scopedLegacyAccountEvent(ctx, id, 100, tc.metadata()))

			migrator, err := New(eventsStore, snapshotStore, WithScope(acme))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(migrator.Run(bg)).To(specs.MatchError(tc.wantErr))

			// No snapshot may be written without proven tenant metadata.
			ctx.Expect(latestSnapshot(ctx, snapshotStore, acme, id)).To(specs.BeNil())
		})
	})
}

// recreatingEventsStore writes a deleted source event straight back right
// after the delete, the way a writer that ignores the adoption fence (or a
// store that did not really delete) leaves the source still holding a
// record at the verified position.
type recreatingEventsStore struct {
	persistence.EventsStore
	source   persistence.Scope
	recreate *egopb.Event
}

func (r *recreatingEventsStore) DeleteEvents(ctx context.Context, scope persistence.Scope, persistenceID string, toSequenceNumber uint64) error {
	if err := r.EventsStore.DeleteEvents(ctx, scope, persistenceID, toSequenceNumber); err != nil {
		return err
	}
	if scope.Equal(r.source) {
		return r.WriteEvents(ctx, scope, []*egopb.Event{r.recreate}, persistence.Unconditional())
	}
	return nil
}

// recreatingSnapshotStore is recreatingEventsStore for snapshots.
type recreatingSnapshotStore struct {
	persistence.SnapshotStore
	source   persistence.Scope
	recreate *egopb.Snapshot
}

func (r *recreatingSnapshotStore) DeleteSnapshots(ctx context.Context, scope persistence.Scope, persistenceID string, toSequenceNumber uint64) error {
	if err := r.SnapshotStore.DeleteSnapshots(ctx, scope, persistenceID, toSequenceNumber); err != nil {
		return err
	}
	if scope.Equal(r.source) {
		return r.WriteSnapshot(ctx, scope, r.recreate)
	}
	return nil
}

// TestTenantAdopterNeverReportsDeletionOfASourceThatStillExists pins that a
// record found in the source after the delete — even at the verified
// sequence, not only a newer one — is a failure, never source_deleted.
func TestTenantAdopterNeverReportsDeletionOfASourceThatStillExists(t *testing.T) {
	specs.Describe(t, "a record found in the source after the delete is a failure, never source_deleted", func(s *specs.Spec) {
		source := persistence.Unscoped()

		s.It("events recreated at the same sequence", func(ctx *specs.Context) {
			base := connectedEventsStore(ctx)
			const id = "recreated-event"
			seedEvents(ctx, base, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 2, 200))

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(&recreatingEventsStore{EventsStore: base, source: source, recreate: newLegacyEvent(ctx, id, 2, 200)}),
				WithWriteEnabled(), WithSourceDeletion(), WithAdoptionFence(newTestFence()))
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())

			// A source that still holds a record is never reported as deleted.
			ctx.Expect(report.SourceDeleted).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errSourceChangedDuringAdoption))
			ctx.Expect(report.Aggregates).To(specs.HaveLen(1))
			ctx.Expect(report.Aggregates[0].Events.Status).To(specs.NotEqual(StatusSourceDeleted))
		})

		s.It("snapshot recreated at the same sequence", func(ctx *specs.Context) {
			base := adoptionSnapshotStore(ctx)
			const id = "recreated-snapshot"
			seedSnapshot(ctx, base, source, newLegacySnapshot(ctx, id, 3, 300))

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithSnapshotStore(&recreatingSnapshotStore{SnapshotStore: base, source: source, recreate: newLegacySnapshot(ctx, id, 3, 300)}),
				WithPersistenceIDs(id), WithWriteEnabled(), WithSourceDeletion(), WithAdoptionFence(newTestFence()))
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())

			// A source that still holds a snapshot is never reported as deleted.
			ctx.Expect(report.SourceDeleted).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errSourceChangedDuringAdoption))
			ctx.Expect(report.Aggregates).To(specs.HaveLen(1))
			ctx.Expect(report.Aggregates[0].Snapshot.Status).To(specs.NotEqual(StatusSourceDeleted))
		})
	})
}

// eventsStoreMock, snapshotStoreMock and stateStoreMock forward every call to a
// mock.Controller. A case that declares no expectation proves the code under
// test never touches the store: an unexpected call fails the case at once and
// names the method and its arguments.
type eventsStoreMock struct{ c *mock.Controller }

func (m eventsStoreMock) Connect(ctx context.Context) error {
	return m.c.Method("Connect").Call(ctx).Err(0)
}
func (m eventsStoreMock) Disconnect(ctx context.Context) error {
	return m.c.Method("Disconnect").Call(ctx).Err(0)
}
func (m eventsStoreMock) Ping(ctx context.Context) error { return m.c.Method("Ping").Call(ctx).Err(0) }
func (m eventsStoreMock) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	return m.c.Method("WriteEvents").Call(ctx, scope, events, precondition).Err(0)
}
func (m eventsStoreMock) DeleteEvents(ctx context.Context, scope persistence.Scope, persistenceID string, toSequenceNumber uint64) error {
	return m.c.Method("DeleteEvents").Call(ctx, scope, persistenceID, toSequenceNumber).Err(0)
}
func (m eventsStoreMock) ReplayEvents(ctx context.Context, scope persistence.Scope, persistenceID string, from, to, limit uint64) ([]*egopb.Event, error) {
	r := m.c.Method("ReplayEvents").Call(ctx, scope, persistenceID, from, to, limit)
	return mock.Value[[]*egopb.Event](r, 0), r.Err(1)
}
func (m eventsStoreMock) GetLatestEvent(ctx context.Context, scope persistence.Scope, persistenceID string) (*egopb.Event, error) {
	r := m.c.Method("GetLatestEvent").Call(ctx, scope, persistenceID)
	return mock.Value[*egopb.Event](r, 0), r.Err(1)
}
func (m eventsStoreMock) PersistenceIDs(ctx context.Context, scope persistence.Scope, pageSize uint64, pageToken string) ([]string, string, error) {
	r := m.c.Method("PersistenceIDs").Call(ctx, scope, pageSize, pageToken)
	return mock.Value[[]string](r, 0), mock.Value[string](r, 1), r.Err(2)
}
func (m eventsStoreMock) GetShardEvents(ctx context.Context, scope persistence.Scope, shardNumber uint64, offset int64, limit uint64) ([]*egopb.Event, int64, error) {
	r := m.c.Method("GetShardEvents").Call(ctx, scope, shardNumber, offset, limit)
	return mock.Value[[]*egopb.Event](r, 0), mock.Value[int64](r, 1), r.Err(2)
}
func (m eventsStoreMock) ShardOffsets(ctx context.Context, scope persistence.Scope) (map[uint64]int64, error) {
	r := m.c.Method("ShardOffsets").Call(ctx, scope)
	return mock.Value[map[uint64]int64](r, 0), r.Err(1)
}

type snapshotStoreMock struct{ c *mock.Controller }

func (m snapshotStoreMock) Connect(ctx context.Context) error {
	return m.c.Method("Connect").Call(ctx).Err(0)
}
func (m snapshotStoreMock) Disconnect(ctx context.Context) error {
	return m.c.Method("Disconnect").Call(ctx).Err(0)
}
func (m snapshotStoreMock) Ping(ctx context.Context) error {
	return m.c.Method("Ping").Call(ctx).Err(0)
}
func (m snapshotStoreMock) WriteSnapshot(ctx context.Context, scope persistence.Scope, snapshot *egopb.Snapshot) error {
	return m.c.Method("WriteSnapshot").Call(ctx, scope, snapshot).Err(0)
}
func (m snapshotStoreMock) GetLatestSnapshot(ctx context.Context, scope persistence.Scope, persistenceID string) (*egopb.Snapshot, error) {
	r := m.c.Method("GetLatestSnapshot").Call(ctx, scope, persistenceID)
	return mock.Value[*egopb.Snapshot](r, 0), r.Err(1)
}
func (m snapshotStoreMock) DeleteSnapshots(ctx context.Context, scope persistence.Scope, persistenceID string, toSequenceNumber uint64) error {
	return m.c.Method("DeleteSnapshots").Call(ctx, scope, persistenceID, toSequenceNumber).Err(0)
}

type stateStoreMock struct{ c *mock.Controller }

func (m stateStoreMock) Connect(ctx context.Context) error {
	return m.c.Method("Connect").Call(ctx).Err(0)
}
func (m stateStoreMock) Disconnect(ctx context.Context) error {
	return m.c.Method("Disconnect").Call(ctx).Err(0)
}
func (m stateStoreMock) Ping(ctx context.Context) error { return m.c.Method("Ping").Call(ctx).Err(0) }
func (m stateStoreMock) WriteState(ctx context.Context, scope persistence.Scope, state *egopb.DurableState, precondition persistence.WritePrecondition) error {
	return m.c.Method("WriteState").Call(ctx, scope, state, precondition).Err(0)
}
func (m stateStoreMock) GetLatestState(ctx context.Context, scope persistence.Scope, persistenceID string) (*egopb.DurableState, error) {
	r := m.c.Method("GetLatestState").Call(ctx, scope, persistenceID)
	return mock.Value[*egopb.DurableState](r, 0), r.Err(1)
}

func TestNewTenantAdopterRejectsAnInvalidSourceScope(t *testing.T) {
	specs.Describe(t, "NewTenantAdopter rejects the zero-value source scope when the adopter is built", func(s *specs.Spec) {
		s.It("returns ErrInvalidScope and no adopter, without touching any store", func(ctx *specs.Context) {
			// No expectation is declared, so any call on a store fails the case.
			ctrl := mock.NewController(ctx)
			adopter, err := NewTenantAdopter(fixedAssignment(nil),
				WithEventsStore(eventsStoreMock{ctrl}),
				WithSnapshotStore(snapshotStoreMock{ctrl}),
				WithStateStore(stateStoreMock{ctrl}),
				WithPersistenceIDs("order-1"),
				WithSourceScope(persistence.Scope{}),
				WithWriteEnabled(), WithAdoptionFence(newTestFence()))
			ctx.Expect(err).To(specs.MatchError(persistence.ErrInvalidScope))
			// No adopter may be returned for an invalid configuration.
			ctx.Expect(adopter).To(specs.BeNil())
		})
	})
}

// TestMaxReplayLimitFitsInAnInt pins that the "read everything" replay limit
// never overflows when a store converts it to int, as
// testkit.EventStore.ReplayEvents does, on any architecture.
func TestMaxReplayLimitFitsInAnInt(t *testing.T) {
	specs.Describe(t, "the read-everything replay limit never overflows when a store converts it to int", func(s *specs.Spec) {
		s.It("equals math.MaxInt and converts to a non-negative int", func(ctx *specs.Context) {
			ctx.Expect(maxReplayLimit).ToEqual(uint64(math.MaxInt))
			ctx.Expect(int(maxReplayLimit)).To(specs.BeGreaterThanOrEqual(0))
		})
	})
}

// TestTenantAdopterReplaysSequencesBeyondTheLimitValue pins that the replay
// RANGE covers every sequence number: maxReplayLimit only caps how many
// events come back, so an event whose sequence is above math.MaxInt must
// still be read, copied, and verified — never silently left behind while the
// run reports a verified migration.
func TestTenantAdopterReplaysSequencesBeyondTheLimitValue(t *testing.T) {
	specs.Describe(t, "the replay range covers every sequence number, including one above math.MaxInt", func(s *specs.Spec) {
		s.It("reads, copies and verifies an event above math.MaxInt", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)

			const id = "high-sequence"
			high := uint64(math.MaxInt) + 1
			source := persistence.Unscoped()
			seedEvents(ctx, store, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, high, 200))

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(store), WithWriteEnabled(), WithAdoptionFence(newTestFence()))
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(report.Verified).ToEqual(1)

			target := tenantScope(ctx, "acme")
			// The event above math.MaxInt must be adopted too.
			adopted := replayEvents(ctx, store, target, id, 1, math.MaxUint64, 10)
			ctx.Expect(adopted).To(specs.HaveLen(2))
			ctx.Expect(adopted[1].GetSequenceNumber()).ToEqual(high)
		})
	})
}

// hidingEventsStore drops one sequence number from target reads, the way a
// target that lost an adopted event would look.
type hidingEventsStore struct {
	persistence.EventsStore
	target persistence.Scope
	hide   uint64
}

func (h *hidingEventsStore) ReplayEvents(ctx context.Context, scope persistence.Scope, persistenceID string, from, to, maxNumber uint64) ([]*egopb.Event, error) {
	events, err := h.EventsStore.ReplayEvents(ctx, scope, persistenceID, from, to, maxNumber)
	if err != nil || !scope.Equal(h.target) {
		return events, err
	}
	kept := events[:0:0]
	for _, e := range events {
		if e.GetSequenceNumber() != h.hide {
			kept = append(kept, e)
		}
	}
	return kept, nil
}

// TestTenantAdopterChainedEventReceipts pins how a re-run proves an events
// adoption once WithSourceDeletion removed the source: each adopted event's
// receipt records the previous adopted sequence (0 for the first), so the
// chain proves the adopted run is complete even when the sequence numbers
// are legitimately sparse, and any missing or tampered link fails closed.
func TestTenantAdopterChainedEventReceipts(t *testing.T) {
	specs.Describe(t, "a re-run proves an events adoption through its chained receipts and fails closed on any broken link", func(s *specs.Spec) {
		source := persistence.Unscoped()
		var target persistence.Scope
		var acme tenancy.TenantContext
		s.BeforeEach(func(ctx *specs.Context) {
			target = tenantScope(ctx, "acme")
			acme = tenantContextOf(ctx, "acme")
		})

		adoptSparse := func(ctx *specs.Context, id string) *testkit.EventStore {
			store := connectedEventsStore(ctx)
			seedEvents(ctx, store, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 5, 500), newLegacyEvent(ctx, id, 9, 900))
			// The sparse stream must be adopted and its source deleted.
			ctx.Expect(rerun(ctx, store, id).SourceDeleted).ToEqual(1)
			return store
		}

		s.It("a sparse stream re-run after source deletion is already present", func(ctx *specs.Context) {
			store := adoptSparse(ctx, "sparse")
			report := rerun(ctx, store, "sparse")
			ctx.Expect(report.AlreadyPresent).ToEqual(1)
			ctx.Expect(report.Failed).ToEqual(0)
		})

		s.It("live events after the adopted chain are not mistaken for part of it", func(ctx *specs.Context) {
			store := adoptSparse(ctx, "sparse-live")
			live := newLegacyEvent(ctx, "sparse-live", 12, 1200)
			live.TenantMetadata = tenancy.MarshalMetadata(acme)
			seedEvents(ctx, store, target, live)

			report := rerun(ctx, store, "sparse-live")
			ctx.Expect(report.AlreadyPresent).ToEqual(1)
			ctx.Expect(report.Failed).ToEqual(0)
		})

		s.It("a missing first adopted event fails", func(ctx *specs.Context) {
			store := adoptSparse(ctx, "sparse-first")
			report := rerun(ctx, &hidingEventsStore{EventsStore: store, target: target, hide: 1}, "sparse-first")
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errTargetNotEquivalent))
		})

		s.It("a missing middle adopted event fails", func(ctx *specs.Context) {
			store := adoptSparse(ctx, "sparse-middle")
			report := rerun(ctx, &hidingEventsStore{EventsStore: store, target: target, hide: 5}, "sparse-middle")
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
			ctx.Expect(report.Failures).To(specs.HaveLen(1))
			ctx.Expect(report.Failures[0]).To(specs.MatchError(errTargetNotEquivalent))
		})

		s.It("a receipt whose recorded predecessor was altered fails", func(ctx *specs.Context) {
			store := adoptSparse(ctx, "sparse-tampered")
			adopted := replayEvents(ctx, store, target, "sparse-tampered", 1, math.MaxUint64, 10)
			ctx.Expect(adopted).To(specs.HaveLen(3))
			tampered, ok := proto.Clone(adopted[2]).(*egopb.Event)
			ctx.Expect(ok).To(specs.BeTrue())
			receipt := tampered.GetTenantMetadata()[adoptionReceiptKey]
			parts := strings.SplitN(receipt, ":", 3)
			// An event receipt records its predecessor: v1:<previous>:<digest>.
			ctx.Expect(parts).To(specs.HaveLen(3))
			tampered.TenantMetadata[adoptionReceiptKey] = parts[0] + ":1:" + parts[2]
			seedEvents(ctx, store, target, tampered)

			report := rerun(ctx, store, "sparse-tampered")
			ctx.Expect(report.AlreadyPresent).ToEqual(0)
			ctx.Expect(report.Failed).ToEqual(1)
		})
	})
}

// rerun runs a write-enabled, source-deleting adoption of id to "acme".
func rerun(ctx *specs.Context, store persistence.EventsStore, id string) *AdoptionReport {
	adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
		WithEventsStore(store), WithPersistenceIDs(id), WithWriteEnabled(), WithSourceDeletion(), WithAdoptionFence(newTestFence()))
	return mustAdopt(ctx, adopter)
}

// TestTenantAdopterCountsSideEffectsOfAFailedAggregate pins that the report
// never hides an irreversible side effect: when an aggregate's events were
// copied and their source deleted, and a LATER record kind then fails, the
// aggregate counts as Failed and still toward Copied and SourceDeleted.
func TestTenantAdopterCountsSideEffectsOfAFailedAggregate(t *testing.T) {
	specs.Describe(t, "the report never hides an irreversible side effect of an aggregate that later failed", func(s *specs.Spec) {
		s.It("counts a failed aggregate still toward Copied and SourceDeleted", func(ctx *specs.Context) {
			source := persistence.Unscoped()
			target := tenantScope(ctx, "acme")

			eventsStore := connectedEventsStore(ctx)
			snapshotStore := adoptionSnapshotStore(ctx)
			const id = "partial-side-effects"
			seedEvents(ctx, eventsStore, source, newLegacyEvent(ctx, id, 1, 100))
			seedSnapshot(ctx, snapshotStore, source, newLegacySnapshot(ctx, id, 1, 100))

			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(eventsStore),
				WithSnapshotStore(&corruptingSnapshotStore{SnapshotStore: snapshotStore, corruptScope: target, mangle: func(s *egopb.Snapshot) { s.State = nil }}),
				WithWriteEnabled(), WithSourceDeletion(), WithAdoptionFence(newTestFence()))
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())

			// The snapshot verification failed.
			ctx.Expect(report.Failed).ToEqual(1)
			// The events were written to the target before the snapshot failed.
			ctx.Expect(report.Copied).ToEqual(1)
			// The events' source was irreversibly deleted before the snapshot failed.
			ctx.Expect(report.SourceDeleted).ToEqual(1)
			// The aggregate as a whole did not verify.
			ctx.Expect(report.Verified).ToEqual(0)
			ctx.Expect(report.Aggregates).To(specs.HaveLen(1))
			ctx.Expect(report.Aggregates[0].Events.Status).ToEqual(StatusSourceDeleted)
			ctx.Expect(report.Aggregates[0].Snapshot.Status).ToEqual(StatusFailed)

			// Precondition: the side effect the report must show really happened.
			ctx.Expect(replayEvents(ctx, eventsStore, source, id, 1, math.MaxUint64, 10)).To(specs.BeEmpty())
		})
	})
}

// reorderingEventsStore returns source replays after the first one in
// reverse order. The SPI does not promise any order for ReplayEvents.
type reorderingEventsStore struct {
	persistence.EventsStore
	source        persistence.Scope
	sourceReplays int
}

func (r *reorderingEventsStore) ReplayEvents(ctx context.Context, scope persistence.Scope, persistenceID string, from, to, maxNumber uint64) ([]*egopb.Event, error) {
	events, err := r.EventsStore.ReplayEvents(ctx, scope, persistenceID, from, to, maxNumber)
	if err != nil || !scope.Equal(r.source) {
		return events, err
	}
	r.sourceReplays++
	if r.sourceReplays == 1 {
		return events, nil
	}
	reversed := make([]*egopb.Event, len(events))
	for i, e := range events {
		reversed[len(events)-1-i] = e
	}
	return reversed, nil
}

// TestTenantAdopterPreDeleteCheckIgnoresReplayOrder pins that the pre-delete
// re-read compares the source by sequence number, not by position: the
// same verified events returned in another order are unchanged.
func TestTenantAdopterPreDeleteCheckIgnoresReplayOrder(t *testing.T) {
	specs.Describe(t, "the pre-delete re-read compares the source by sequence number, not by position", func(s *specs.Spec) {
		s.It("treats the same events in another order as unchanged", func(ctx *specs.Context) {
			source := persistence.Unscoped()
			base := connectedEventsStore(ctx)
			const id = "reordered"
			seedEvents(ctx, base, source, newLegacyEvent(ctx, id, 1, 100), newLegacyEvent(ctx, id, 2, 200), newLegacyEvent(ctx, id, 3, 300))

			store := &reorderingEventsStore{EventsStore: base, source: source}
			adopter := newAdopter(ctx, fixedAssignment(map[string]tenancy.TenantID{id: "acme"}),
				WithEventsStore(store), WithWriteEnabled(), WithSourceDeletion(), WithAdoptionFence(newTestFence()))
			report, err := adopter.Run(context.Background())
			ctx.Expect(err).To(specs.BeNil())

			// Precondition: the pre-delete re-read saw the reordered replay.
			ctx.Expect(store.sourceReplays).To(specs.BeGreaterThanOrEqual(2))
			// A reordered replay of the same events is not a source change.
			ctx.Expect(report.Failed).ToEqual(0)
			ctx.Expect(report.SourceDeleted).ToEqual(1)
		})
	})
}
