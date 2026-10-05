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
	"reflect"
	"sync"
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"
	"github.com/tochemey/goakt/v4/supervisor"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/command"
	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/encryption"
	"github.com/getsyntegrity/urd/eventadapter"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/instrumentation"
	"github.com/getsyntegrity/urd/internal/runner"
	"github.com/getsyntegrity/urd/persistence"
	behaviorport "github.com/getsyntegrity/urd/port/behavior"
	"github.com/getsyntegrity/urd/tenancy"
)

const (
	eventsWriterChildName    = "events-writer"
	snapshotsWriterChildName = "snapshots-writer"
	eventsJanitorChildName   = "events-janitor"
	defaultPersistTimeout    = 30 * time.Second
	defaultBatchFlushWindow  = 5 * time.Millisecond
)

// persistPhase tracks the batch processing state of an [Actor].
type persistPhase int

const (
	// phaseProcessing accepts commands and accumulates their events into
	// the batch buffer.
	phaseProcessing persistPhase = iota
	// phaseFlushing indicates that a batch write is in flight. Incoming
	// commands are stashed until the writer responds.
	phaseFlushing
	// phaseReplying sends pre-computed replies to stashed commands after
	// the batch write has been confirmed (or failed).
	phaseReplying
	// phasePersisting indicates a single (non-batched) command's persist
	// write is in flight. Incoming commands are stashed until the writer
	// responds. Mirrors phaseFlushing for the direct (batchThreshold == 0)
	// path; see persistAsync.
	phasePersisting
	// phaseDirectReplying sends the reply to the single stashed command
	// after its persist write has been confirmed (or failed). Mirrors
	// phaseReplying for the direct path; see replyDirect.
	phaseDirectReplying
	// phaseStopping answers the commands released from the stash with an error
	// until the actor shuts down, after a failed write left it unable to tell
	// what the store holds; see stopAfterAnswering.
	phaseStopping
)

// batchFlushTick is an internal timer message sent to self to trigger
// a batch flush when the flush window expires before the batch threshold
// is reached.
type batchFlushTick struct{}

// stashDrained is an internal message sent to self after the stash has been
// released, to stop the actor once every command released with it has been
// answered; see stopAfterAnswering.
type stashDrained struct{}

// errEntityStopped is the error a command receives when the entity stops after
// a failed write before it could run the command. The command was not run and
// nothing was persisted for it.
var errEntityStopped = errors.New("entity stopped after a failed write: command not executed")

// noTenantContext is the zero value of tenancy.TenantContext. Neither
// tenancy.NewTenantContext nor tenancy.NewAdministrativeContext can ever
// produce it (tenancy/tenant_context.go), so it safely marks "not yet
// seeded" for Actor.actorTenant, distinct from any real
// resolved identity.
var noTenantContext tenancy.TenantContext

// batchEntry holds the pre-computed reply and observability context for
// a single command that has been optimistically processed and stashed
// while awaiting batch persistence.
type batchEntry struct {
	reply     *egopb.CommandReply
	startTime time.Time
	span      trace.Span
	// request identifies the request this entry answers: replyFromBatch
	// hands the reply to that request only. See directRequest.
	request directRequest
}

// Actor persists state changes as a sequence of immutable events.
//
// Command processing generates events that are persisted through a child
// [eventsWriterActor] before the in-memory state is updated. This guarantees
// that the actor state always matches what is stored.
//
// The persistence write is dispatched asynchronously via goakt's PipeTo, so
// the actor's dispatcher worker is never blocked waiting on the writer child
// (see persistAsync and flushBatch). The originating command is stashed and
// redelivered once the write completes, preserving command ordering and
// preventing concurrent state mutations without holding a worker idle.
//
// Snapshots and retention cleanup are handled asynchronously by dedicated child
// actors and never add latency to command processing.
type Actor struct {
	behavior         behaviorport.EventSourced
	eventsStore      persistence.EventsStore
	snapshotStore    persistence.SnapshotStore
	currentState     State
	cachedStateAny   *anypb.Any // cached marshal of currentState, invalidated on state change
	eventsCounter    uint64
	lastCommandTime  time.Time
	eventsStream     eventstream.Stream
	persistenceID    string
	eventAdapters    []eventadapter.EventAdapter
	snapshotInterval uint64
	retentionPolicy  *retentionPolicy
	encryptor        encryption.Encryptor
	tracer           trace.Tracer
	metrics          *instrumentation.Instruments

	eventsWriter    *goakt.PID
	snapshotsWriter *goakt.PID
	eventsJanitor   *goakt.PID
	persistTimeout  time.Duration

	// Cached values computed once at startup to avoid per-command allocations.
	shardNumber uint64

	// Event batching fields. Active only when batchThreshold > 0.
	batchThreshold   int
	batchFlushWindow time.Duration
	phase            persistPhase
	batchBuffer      []*egopb.Event
	batchEntries     []batchEntry
	batchState       State
	batchCounter     uint64
	batchTime        time.Time
	batchNumEvents   int
	remainingReplies int
	shutdownOnDrain  bool
	flushTimer       *time.Timer
	batchMu          sync.Mutex

	// batchBase and batchHasPrecondition implement D9's base-anchored
	// precondition for the batched path. batchBase is set once, when a new
	// batch opens (the founding command folded into an otherwise empty
	// batchEntries): to the founder's own declared ExpectedRevision when it
	// declares one, else to eventsCounter. Either way it is the physical
	// store revision the eventual flush appends onto, and it never moves
	// again for the rest of that batch cycle — a later admitted command's
	// own declared revision was already checked against the running logical
	// counter by the admission gate, so it must not overwrite batchBase with
	// a logical mid-batch value the store was never actually at.
	// batchHasPrecondition records whether ANY command admitted into this
	// batch cycle declared an ExpectedRevision at all — not only the
	// founder; see resolveBatchPrecondition for how the two combine into the
	// single WritePrecondition the flush's one atomic write carries. Both
	// are cleared by resetBatch alongside the rest of the batch-cycle state.
	batchBase            uint64
	batchHasPrecondition bool

	// Direct (non-batched) persist-in-flight fields. Active only while phase
	// is phasePersisting or phaseDirectReplying. Mutually exclusive with the
	// batching fields above: an entity is batch-mode or direct-mode for its
	// entire lifetime (batchThreshold is static config, see batchEnabled),
	// so the two families of fields are never both in use. Mirror
	// batchState/batchCounter/batchTime/batchNumEvents and batchEntry's
	// span/startTime for the single in-flight command (see persistAsync,
	// handleDirectPersistResponse, replyDirect).
	directPendingState   State
	directPendingCounter uint64
	directPendingTime    time.Time
	directNumEvents      int
	directStartTime      time.Time
	directSpan           trace.Span
	directErr            error
	directShutdown       bool
	// directRequest identifies the request whose write is in flight: the one
	// persistAsync stashed, and the only one replyDirect may answer from the
	// stash. See directRequest.
	directRequest directRequest

	// tenantAware reports whether the actor system was built from a Config
	// with a tenancy.TenantResolver registered (extensions.TenancyMarker
	// present). It is presence-only: the actor never holds a resolver and
	// never calls Resolve — see PreStart. When true, the pre-handler gates
	// in processCommandAndReply and processAndBatch require a TenantContext
	// to already be attached to the incoming context (set by
	// Engine.SendCommand at the trust boundary) before HandleCommand runs.
	tenantAware bool

	// actorTenant records this actor's tenant identity for its full
	// lifetime (D6, EGO-TENANT-002) — widened from the prior per-batch-cycle
	// "batchTenant" scope (EGO-TENANT-006 T4-B). It is seeded from recover()
	// when persisted event/snapshot metadata exists (Phase 3), or
	// established on this actor's first successful persist otherwise (see
	// establishActorTenant, called from processCommandAndReply and
	// processAndBatch after a successful buildEnvelopes). Every later
	// command's resolved TenantContext is compared against it via
	// tenancy.VerifyUnchanged in both paths' pre-handler gates, rejecting a
	// mismatch with ErrDenied (Phase 4) — the cross-tenant guard for the
	// full actor lifetime, not merely one batch cycle. Read and written
	// only through tenancy.Require/tenancy.VerifyUnchanged — never
	// re-resolved. Holds noTenantContext (the zero value) when unset. Unlike
	// the prior batchTenant, resetBatch does NOT clear this field: an
	// actor's tenant identity must survive every batch cycle for its whole
	// lifetime (D6 — actor-lifetime scope strictly subsumes batch-cycle
	// scope; keeping a separate per-cycle field would be two checks proving
	// one invariant).
	//
	// Blocker 3 fix (EGO-TENANT-006 review), still in effect: the
	// homogeneity check against actorTenant runs in processAndBatch's
	// pre-handler gate, BEFORE entity.behavior.HandleCommand executes — not
	// only afterward, at buffer-append time. latestState() returns
	// entity.batchState (this batch's accumulated, unpersisted state)
	// whenever a batch is already open, regardless of which tenant is
	// calling; running HandleCommand against that state under a different
	// tenant's identity is itself the leak, whether or not the handler goes
	// on to produce any events.
	actorTenant tenancy.TenantContext

	// scope is the persistence.Scope this actor's store reads and writes
	// are bound to (TENANT-003 T4). Bound once in PreStart via
	// resolveScope, right after tenantAware is set and BEFORE any store
	// read (including recover()): persistence.Unscoped() when
	// tenantAware is false, or the tenant scope carried by the per-spawn
	// extensions.EntityTenantScope dependency Engine.Entity injects when
	// tenantAware is true. Threaded through to the child writer/janitor
	// actors on their request structs (the events writer request scope,
	// persistSnapshotRequest.scope, applyRetentionRequest.scope) rather
	// than re-derived there, since those are separate actors that never
	// see PreStart's dependencies.
	//
	// # Actor identity across tenants
	//
	// In a multi-tenant engine the actor's name is qualified with its tenant
	// (actoridentity.Qualify), so two tenants that share an entityID are two
	// actors with two scopes and two actorTenant values; neither can reach or
	// lock out the other (EGO-TENANT-009). PreStart checks that its name is
	// the one its tenant and ID derive before it reads a store, and the
	// persistence ID stays the entityID the behavior declares. An engine with
	// exactly one tenant, or none, keeps the bare entityID as the actor's
	// name, where a spawn under another tenant is still ErrSpawnTenantMismatch
	// (engine's verifySpawnedTenant) and the actorTenant cross-check stays as
	// an additional defense.
	scope persistence.Scope
}

var _ goakt.Actor = (*Actor)(nil)

// New creates an instance of Actor.
// The constructor takes no arguments to support cluster relocation. Per-entity
// configuration is injected via the entityConfig dependency at startup.
func New() *Actor {
	return &Actor{}
}

// PreStart loads extensions and dependencies, validates configuration, and
// recovers the actor state from the events and snapshot stores. Child actors
// are spawned in PostStart where [goakt.ReceiveContext] is available.
func (entity *Actor) PreStart(ctx *goakt.Context) error {
	// A restart reuses this value: drop the batch cycle the previous run left,
	// and the write it had in flight. A run that stopped while persisting left
	// the phase and the request it was waiting on behind; the next run would
	// stash every command forever, and could take a later request for that one.
	entity.resetBatch()
	entity.resetDirect()
	entity.stopFlushTimer()
	eventsStoreExt, err := extensions.Require[*extensions.EventsStore](ctx, extensions.EventsStoreExtensionID)
	if err != nil {
		return err
	}
	eventsStreamExt, err := extensions.Require[*extensions.EventsStream](ctx, extensions.EventsStreamExtensionID)
	if err != nil {
		return err
	}
	entity.eventsStore = eventsStoreExt.Underlying()
	entity.eventsStream = eventsStreamExt.Underlying()
	entity.persistenceID = ctx.ActorName()
	entity.persistTimeout = defaultPersistTimeout
	// Presence-only signal: tenant-aware mode is active when the engine
	// registered the tenancy marker extension. The marker carries no
	// resolver (internal/extensions.TenancyMarker), so the actor can never
	// reach a TenantResolver through it.
	entity.tenantAware = ctx.Extension(extensions.TenancyExtensionID) != nil

	if err := entity.resolveScope(ctx.Dependencies()); err != nil {
		return err
	}

	if err := entity.loadOptionalExtensions(ctx); err != nil {
		return err
	}
	entity.setConfig(ctx)

	if err := entity.bindIdentity(ctx); err != nil {
		return err
	}

	if err := entity.validateAndRecover(ctx); err != nil {
		return err
	}

	entity.metrics.EntityStarted(ctx.Context())

	return nil
}

// Receive is the default message handler for the actor mailbox.
//
// When event batching is enabled (batchThreshold > 0) additional internal
// message types are handled: batchFlushTick triggers a timer-based flush,
// and persistEventsResponse carries the result of an asynchronous batch write.
// For non-batched entities, persistEventsResponse instead carries the result
// of the single in-flight direct-path write (see persistAsync).
func (entity *Actor) Receive(ctx *goakt.ReceiveContext) {
	switch msg := ctx.Message().(type) {
	case *goakt.PostStart:
		entity.shardNumber = ctx.ActorSystem().Partition(entity.persistenceID)
		entity.spawnChildren(ctx)
	case *egopb.GetStateCommand:
		entity.handleGetStateCommand(ctx)
	case *egopb.TenantBindingQuery:
		ctx.Response(protocol.AnswerTenantBinding(entity.tenantAware, entity.scope, msg))
	case *batchFlushTick:
		entity.handleBatchFlushTick(ctx)
	case *stashDrained:
		if entity.phase == phaseStopping {
			ctx.Shutdown()
		}
	case *persistEventsResponse:
		if entity.batchEnabled() {
			entity.handleBatchPersistResponse(ctx, msg)
		} else {
			entity.handleDirectPersistResponse(ctx, msg)
		}
	default:
		command, ok := msg.(Command)
		if !ok {
			ctx.Unhandled()
			return
		}
		if entity.phase == phaseStopping {
			entity.sendErrorReply(ctx, errEntityStopped)
			return
		}
		if entity.batchEnabled() {
			entity.handleCommandBatched(ctx, command)
			return
		}
		switch entity.phase {
		case phasePersisting:
			ctx.Stash()
		case phaseDirectReplying:
			// Only the request whose write just finished is answered here. Any
			// other command was already queued behind the write's confirmation,
			// ahead of the stashed request GoAkt re-delivers at the tail of the
			// mailbox: answering it from the stash would send its caller the
			// current state without running the command, and run the stashed
			// request a second time. It waits in the stash until replyDirect
			// releases it.
			if !entity.directRequest.isRequest(ctx) {
				ctx.Stash()
				return
			}
			entity.replyDirect(ctx)
		default:
			entity.processCommandAndReply(ctx, command)
		}
	}
}

// PostStop releases resources when the actor shuts down.
//
// GoAkt may run PostStop on the shutdown goroutine while a Receive turn is
// still in flight (a stopping parent frees its children concurrently). The
// Receive path reads the batch fields without a lock, so PostStop must not
// write them: it only stops the flush timer, which is guarded by batchMu. The
// batch accumulation state is cleared by PreStart, which runs before any turn
// of a restarted actor.
// nolint
func (entity *Actor) PostStop(ctx *goakt.Context) error {
	entity.metrics.EntityStopped(ctx.Context())
	entity.stopFlushTimer()
	return nil
}

// loadOptionalExtensions reads optional extensions from the actor system.
// Missing extensions are silently skipped, but a present-but-mismatched-type
// registration under any of these extension IDs is reported as an error
// instead of letting the runtime panic (see optionalExtension in
// extension_lookup.go and issue #99).
func (entity *Actor) loadOptionalExtensions(ctx *goakt.Context) error {
	snapshotStoreExt, err := extensions.Optional[*extensions.SnapshotStoreExt](ctx, extensions.SnapshotStoreExtensionID)
	if err != nil {
		return err
	}
	if snapshotStoreExt != nil {
		entity.snapshotStore = snapshotStoreExt.Underlying()
	}

	eventAdaptersExt, err := extensions.Optional[*extensions.EventAdapters](ctx, extensions.EventAdaptersExtensionID)
	if err != nil {
		return err
	}
	if eventAdaptersExt != nil {
		entity.eventAdapters = eventAdaptersExt.Adapters()
	}

	encryptorExt, err := extensions.Optional[*extensions.EncryptorExtension](ctx, extensions.EncryptorExtensionID)
	if err != nil {
		return err
	}
	if encryptorExt != nil {
		entity.encryptor = encryptorExt.Encryptor()
	}

	telemetryExt, err := extensions.Optional[*extensions.TelemetryExtension](ctx, extensions.TelemetryExtensionID)
	if err != nil {
		return err
	}
	if telemetryExt != nil {
		entity.tracer = telemetryExt.Tracer()
		entity.metrics = instrumentation.New(telemetryExt.Meter())
	}

	return nil
}

// bindIdentity makes the behavior's ID the persistence ID, and in a
// tenant-aware engine proves, before any store read, that this actor's name is
// the one its tenant and that ID derive (EGO-TENANT-009).
//
// The actor's name is only its address. In a multi-tenant engine it is
// qualified with the tenant, so it is no longer the entity's ID, and the
// records the actor reads and writes keep the ID the behavior declares. Until
// the behavior is found the persistence ID stays the actor's name, which is
// the ID itself in legacy and single-tenant engines.
func (entity *Actor) bindIdentity(ctx *goakt.Context) error {
	if entity.behavior == nil {
		return nil
	}
	entity.persistenceID = entity.behavior.ID()
	if !entity.tenantAware {
		return nil
	}
	return extensions.VerifyActorIdentity(ctx, string(entity.scope.TenantID()), entity.persistenceID)
}

// setConfig reads the behavior and entity configuration from the
// injected dependencies.
func (entity *Actor) setConfig(ctx *goakt.Context) {
	for _, dependency := range ctx.Dependencies() {
		if dependency == nil {
			continue
		}

		if behavior, ok := extensions.BehaviorFrom[behaviorport.EventSourced](dependency); ok {
			entity.behavior = behavior
		}

		if config, ok := dependency.(*extensions.EntityConfig); ok {
			entity.snapshotInterval = config.SnapshotInterval
			if config.HasRetentionPolicy {
				entity.retentionPolicy = &retentionPolicy{
					DeleteEventsOnSnapshot:    config.DeleteEventsOnSnapshot,
					DeleteSnapshotsOnSnapshot: config.DeleteSnapshotsOnSnapshot,
					EventsRetentionCount:      config.EventsRetentionCount,
				}
			}
			entity.batchThreshold = config.BatchThreshold
			entity.batchFlushWindow = config.BatchFlushWindow
			if entity.batchThreshold > 0 && entity.batchFlushWindow == 0 {
				entity.batchFlushWindow = defaultBatchFlushWindow
			}
		}
	}
}

// resolveScope binds entity.scope (and, in tenant-aware mode,
// entity.actorTenant) from deps before any store read (TENANT-003 T4).
//
// tenantAware == false binds persistence.Unscoped() and leaves actorTenant
// untouched (noTenantContext) — legacy mode is byte-identical to before
// this field existed.
//
// tenantAware == true looks for the per-spawn extensions.EntityTenantScope
// dependency Engine.Entity injects (engine.go's spawnTenantScope) and
// fails closed with extensions.ErrEntityTenantScopeMissing when it is absent or
// carries an invalid tenant id: a tenant-aware actor must never start
// without a bound scope. On success it also pre-seeds entity.actorTenant
// with the corresponding tenancy.TenantContext, BEFORE recover() runs. This
// is what turns seedActorTenant's later calls (recoverFromSnapshot, recover)
// from a first-seed into a cross-check against the spawn-bound tenant (D6):
// recovered tenant_metadata that disagrees with the tenant this actor was
// actually spawned for now fails closed via tenancy.VerifyUnchanged, rather
// than being trusted as the source of actorTenant.
func (entity *Actor) resolveScope(deps []extension.Dependency) error {
	if !entity.tenantAware {
		entity.scope = persistence.Unscoped()
		return nil
	}

	for _, dependency := range deps {
		dep, ok := dependency.(*extensions.EntityTenantScope)
		if !ok || dep == nil {
			continue
		}

		scope, err := persistence.NewTenantScope(tenancy.TenantID(dep.TenantID))
		if err != nil {
			return fmt.Errorf("%w: %w", extensions.ErrEntityTenantScopeMissing, err)
		}

		tenantContext, err := tenancy.NewTenantContext(scope.TenantID())
		if err != nil {
			return fmt.Errorf("%w: %w", extensions.ErrEntityTenantScopeMissing, err)
		}

		entity.scope = scope
		entity.actorTenant = tenantContext
		return nil
	}

	return extensions.ErrEntityTenantScopeMissing
}

// validateAndRecover ensures required dependencies are present, pings the
// backing stores, and replays persisted state.
func (entity *Actor) validateAndRecover(ctx *goakt.Context) error {
	chain := runner.
		New(runner.WithFailFast()).
		AddRunner(func() error {
			if entity.behavior == nil {
				return fmt.Errorf("behavior is required")
			}
			return nil
		}).
		AddRunner(func() error { return entity.eventsStore.Ping(ctx.Context()) })

	if entity.snapshotStore != nil {
		chain = chain.AddRunner(func() error { return entity.snapshotStore.Ping(ctx.Context()) })
	}

	chain = chain.AddRunner(func() error { return entity.recover(ctx.Context()) })
	return chain.Run()
}

// childSpawnOptions returns the shared spawn options for persistence child
// actors. Children are long-lived and supervised with a restart directive so
// they recover automatically on transient failures.
func childSpawnOptions() []goakt.SpawnOption {
	return []goakt.SpawnOption{
		goakt.WithLongLived(),
		goakt.WithSupervisor(
			supervisor.NewSupervisor(
				supervisor.WithAnyErrorDirective(supervisor.RestartDirective),
			),
		),
	}
}

// spawnChildren creates the child actors responsible for event persistence,
// snapshot writes, and retention cleanup. Each child accesses its backing store
// through the actor system extensions.
func (entity *Actor) spawnChildren(ctx *goakt.ReceiveContext) {
	opts := childSpawnOptions()

	entity.eventsWriter = ctx.Spawn(eventsWriterChildName, newEventsWriterActor(), opts...)

	if entity.snapshotStore != nil {
		entity.snapshotsWriter = ctx.Spawn(snapshotsWriterChildName, newSnapshotsWriterActor(), opts...)
	}

	if entity.retentionPolicy != nil {
		entity.eventsJanitor = ctx.Spawn(eventsJanitorChildName, newEventsJanitorActor(), opts...)
	}
}

// recover rebuilds the actor state from the snapshot and events stores.
//
// The recovery strategy is:
//  1. Load the latest snapshot (if a snapshot store is configured) to seed state.
//  2. Determine the latest persisted event sequence number.
//  3. Replay all events after the snapshot point to bring state up to date.
func (entity *Actor) recover(ctx context.Context) error {
	state := entity.behavior.InitialState()
	replayFrom := uint64(1)

	if entity.snapshotStore != nil {
		var err error
		state, replayFrom, err = entity.recoverFromSnapshot(ctx, state)
		if err != nil {
			return err
		}
	}

	latestEvent, err := entity.eventsStore.GetLatestEvent(ctx, entity.scope, entity.persistenceID)
	if err != nil {
		return fmt.Errorf("failed to get latest event: %w", err)
	}

	if latestEvent == nil {
		entity.currentState = state
		return nil
	}

	latestSeqNr := latestEvent.GetSequenceNumber()

	// Seed/verify this actor's lifetime tenant identity from the latest
	// event's carried metadata (D5/D6, EGO-TENANT-002). When
	// recoverFromSnapshot already seeded actorTenant above, seedActorTenant
	// cross-checks this event's tenant against it instead of overwriting —
	// a mismatch here means the snapshot and the latest event disagree on
	// tenant, which is exactly the data-integrity violation VerifyUnchanged
	// exists to catch (ErrDenied). Absent or malformed metadata on a
	// tenant-aware actor's persisted event is not tolerated: it means this
	// event predates tenancy or was corrupted, and the actor must refuse to
	// start rather than silently run without an identity (fail-closed,
	// design.md D3 — UnmarshalMetadata's own ErrInvalid is the rejection).
	if entity.tenantAware {
		eventTenant, err := tenancy.UnmarshalMetadata(tenancy.Metadata(latestEvent.GetTenantMetadata()))
		if err != nil {
			return fmt.Errorf("failed to unmarshal event tenant metadata: %w", err)
		}
		if err := entity.seedActorTenant(eventTenant); err != nil {
			return err
		}
	}

	if replayFrom <= latestSeqNr {
		state, err = entity.replayEvents(ctx, state, replayFrom, latestSeqNr)
		if err != nil {
			return err
		}
		entity.eventsCounter = latestSeqNr
	}

	entity.currentState = state
	return nil
}

// recoverFromSnapshot loads the latest snapshot and returns the restored state
// together with the sequence number to replay from. When no snapshot exists the
// initial state and a replayFrom of 1 are returned unchanged.
func (entity *Actor) recoverFromSnapshot(ctx context.Context, initial State) (State, uint64, error) {
	snapshot, err := entity.snapshotStore.GetLatestSnapshot(ctx, entity.scope, entity.persistenceID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to load snapshot: %w", err)
	}

	if snapshot == nil || snapshot.GetState() == nil {
		return initial, 1, nil
	}

	// Seed this actor's lifetime tenant identity from the snapshot's carried
	// metadata (D5, EGO-TENANT-002). This runs before the latest-event seed
	// in recover() below, so with DeleteEventsOnSnapshot/EventsRetentionCount
	// configured — where GetLatestEvent can return nil and a snapshot is the
	// sole surviving record — the actor still has a tenant identity rather
	// than recovering open (fail-closed, D3). Absent or malformed metadata on
	// a tenant-aware actor's snapshot is refused the same way.
	if entity.tenantAware {
		snapshotTenant, err := tenancy.UnmarshalMetadata(tenancy.Metadata(snapshot.GetTenantMetadata()))
		if err != nil {
			return nil, 0, fmt.Errorf("failed to unmarshal snapshot tenant metadata: %w", err)
		}
		if err := entity.seedActorTenant(snapshotTenant); err != nil {
			return nil, 0, err
		}
	}

	snapshotState, err := entity.decryptPayload(ctx, snapshot.GetState(), snapshot.GetIsEncrypted(), snapshot.GetEncryptionKeyId())
	if err != nil {
		return nil, 0, fmt.Errorf("failed to decrypt snapshot: %w", err)
	}

	if err := snapshotState.UnmarshalTo(initial); err != nil {
		return nil, 0, fmt.Errorf("failed to unmarshal snapshot state: %w", err)
	}

	seqNr := snapshot.GetSequenceNumber()
	entity.eventsCounter = seqNr
	return initial, seqNr + 1, nil
}

// replayEvents applies persisted events to the given state in sequence order
// and returns the resulting state.
func (entity *Actor) replayEvents(ctx context.Context, state State, from, to uint64) (State, error) {
	events, err := entity.eventsStore.ReplayEvents(ctx, entity.scope, entity.persistenceID, from, to, to-from+1)
	if err != nil {
		return nil, fmt.Errorf("failed to replay events: %w", err)
	}

	for _, envelope := range events {
		state, err = entity.applyPersistedEvent(ctx, envelope, state)
		if err != nil {
			return nil, err
		}
	}

	return state, nil
}

// applyPersistedEvent decrypts, adapts, and applies a single persisted event
// envelope to the given state.
//
// PR1 review-comment regression: recover() validated only latestEvent's
// tenant metadata before this fix (D3's fail-closed intent applied to the
// wrong event); every event strictly between the snapshot point and
// latestSeqNr was replayed unchecked. An intermediate event belonging to
// another tenant, or missing tenant metadata, would silently contaminate
// recovered state as long as the *latest* event still carried the actor's
// own tenant. By the time replayEvents runs, entity.actorTenant is already
// seeded (recover() seeds it from the snapshot and/or latestEvent before
// replaying), so every replayed event is cross-checked against it here,
// fail-closed exactly like seedActorTenant.
func (entity *Actor) applyPersistedEvent(ctx context.Context, envelope *egopb.Event, state State) (State, error) {
	seqNr := envelope.GetSequenceNumber()

	if entity.tenantAware {
		eventTenant, err := tenancy.UnmarshalMetadata(tenancy.Metadata(envelope.GetTenantMetadata()))
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal event tenant metadata at sequence %d: %w", seqNr, err)
		}
		if err := tenancy.VerifyUnchanged(entity.actorTenant, eventTenant); err != nil {
			return nil, fmt.Errorf("tenant mismatch replaying event at sequence %d: %w", seqNr, err)
		}
	}

	evt, err := entity.decryptPayload(ctx, envelope.GetEvent(), envelope.GetIsEncrypted(), envelope.GetEncryptionKeyId())
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt event at sequence %d: %w", seqNr, err)
	}

	if len(entity.eventAdapters) > 0 {
		evt, err = eventadapter.Chain(entity.eventAdapters, evt, seqNr)
		if err != nil {
			return nil, fmt.Errorf("failed to adapt event at sequence %d: %w", seqNr, err)
		}
	}

	eventMsg, err := evt.UnmarshalNew()
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal event at sequence %d: %w", seqNr, err)
	}

	state, err = entity.behavior.HandleEvent(ctx, eventMsg, state)
	if err != nil {
		return nil, fmt.Errorf("failed to handle event at sequence %d: %w", seqNr, err)
	}

	return state, nil
}

// decryptPayload decrypts an [anypb.Any] payload when encryption is enabled.
// Unencrypted payloads are returned as-is.
func (entity *Actor) decryptPayload(ctx context.Context, payload *anypb.Any, encrypted bool, keyID string) (*anypb.Any, error) {
	if !encrypted || entity.encryptor == nil {
		return payload, nil
	}

	plaintext, err := entity.encryptor.Decrypt(ctx, entity.persistenceID, payload.GetValue(), keyID)
	if err != nil {
		return nil, err
	}

	decrypted := &anypb.Any{TypeUrl: payload.GetTypeUrl()}
	if err := proto.Unmarshal(plaintext, decrypted); err != nil {
		return nil, fmt.Errorf("failed to unmarshal decrypted payload: %w", err)
	}

	return decrypted, nil
}

// currentStateAny returns the cached anypb.Any of currentState, computing it
// only when the state has changed since the last call.
func (entity *Actor) currentStateAny() *anypb.Any {
	if entity.cachedStateAny == nil {
		entity.cachedStateAny, _ = anypb.New(entity.currentState)
	}
	return entity.cachedStateAny
}

// dispatchToBehavior invokes entity.behavior against cmd, preferring
// HandleEnvelope over HandleCommand when both entity.behavior implements
// behaviorport.EventSourcedEnvelope (EventSourcedEnvelopeBehavior is one)
// and a command.Metadata is available on goCtx (#60, M-3). goCtx must
// already be unwrapped from the receiving ReceiveContext via
// ctx.Context(): on a local dispatch this is the same
// context.Context Engine.Dispatch attached a command.Carrier to (see
// command_context.go), so protocol.MetadataFromContext rematerializes it here
// without any wire-format change. Any behavior that does not implement the
// optional interface, or any command reached without Metadata (e.g. a
// caller that bypasses Engine.Dispatch/SendCommand), falls back to
// HandleCommand unchanged — this method is called from both the
// non-batched (processCommandAndReply) and batched (processAndBatch) paths
// so both dispatch identically.
func (entity *Actor) dispatchToBehavior(goCtx context.Context, cmd Command, priorState State) ([]Event, error) {
	envBehavior, ok := entity.behavior.(behaviorport.EventSourcedEnvelope)
	if !ok {
		return entity.behavior.HandleCommand(goCtx, cmd, priorState)
	}
	md, ok := protocol.MetadataFromContext(goCtx)
	if !ok {
		return entity.behavior.HandleCommand(goCtx, cmd, priorState)
	}
	env, err := command.NewEnvelope(cmd, md)
	if err != nil {
		return entity.behavior.HandleCommand(goCtx, cmd, priorState)
	}
	return envBehavior.HandleEnvelope(goCtx, env, priorState)
}

// shouldStayAliveAfterConflict implements design.md D10: after a failed
// persist, the actor may keep running only when it can prove its own
// in-memory state still matches the store. That is true exactly when err
// unwraps to a *persistence.ConflictError whose ActualRevision() is known
// and equals entity.eventsCounter — the actor's own last-confirmed
// revision, unchanged by a failed write. In that case nothing this actor
// didn't already know about landed between its last confirmed write and
// this rejected attempt, so discarding the pending write and continuing is
// safe. Any other outcome (a non-conflict failure, or a conflict whose
// actual revision is unknown or disagrees with eventsCounter) means this
// actor's view may already be stale relative to the store, so it must shut
// down and let the supervisor rebuild it via recover().
func (entity *Actor) shouldStayAliveAfterConflict(err error) bool {
	var conflictErr *persistence.ConflictError
	if !errors.As(err, &conflictErr) {
		return false
	}
	actual, ok := conflictErr.ActualRevision()
	return ok && actual == entity.eventsCounter
}

// sendErrorReply sends a [egopb.CommandReply] containing the given error.
func (entity *Actor) sendErrorReply(ctx *goakt.ReceiveContext, err error) {
	ctx.Response(&egopb.CommandReply{
		Reply: &egopb.CommandReply_ErrorReply{
			ErrorReply: &egopb.ErrorReply{
				Message: err.Error(),
			},
		},
	})
}

// sendStateReply sends a [egopb.CommandReply] containing the current state.
func (entity *Actor) sendStateReply(ctx *goakt.ReceiveContext) {
	ctx.Response(&egopb.CommandReply{
		Reply: &egopb.CommandReply_StateReply{
			StateReply: &egopb.StateReply{
				PersistenceId:  entity.persistenceID,
				State:          entity.currentStateAny(),
				SequenceNumber: entity.eventsCounter,
				Timestamp:      entity.lastCommandTime.UnixNano(),
			},
		},
	})
}

// handleGetStateCommand dispatches a GetStateCommand from Receive, deferring
// it via ctx.Stash() while a direct (non-batched) command's persist write is
// unsettled (phasePersisting: entity.currentState is still the pre-write
// value; phaseDirectReplying: the write is confirmed but the originating
// command has not yet been replied to). Deferring in both phases guarantees
// a read never observes stale state and never overtakes the write it raced
// with (issue #64 P1 follow-up).
//
// Batch mode is untouched: entity.currentState there is likewise only
// mutated on confirmed batch writes (see handleBatchPersistResponse), so
// getStateAndReply already returns a consistent value regardless of
// batchThreshold's flush phase.
func (entity *Actor) handleGetStateCommand(ctx *goakt.ReceiveContext) {
	if entity.phase == phaseStopping {
		entity.sendErrorReply(ctx, errEntityStopped)
		return
	}
	if !entity.batchEnabled() && (entity.phase == phasePersisting || entity.phase == phaseDirectReplying) {
		ctx.Stash()
		return
	}
	entity.getStateAndReply(ctx)
}

// getStateAndReply returns the last committed state of the entity without
// processing any command. When event batching is enabled, this returns the
// state as of the last confirmed batch write, not any optimistic pending state.
//
// PR1 review-comment regression: GetStateCommand is dispatched here directly
// from Receive, bypassing processCommandAndReply/processAndBatch entirely.
// Without its own gate this let any resolved tenant read another tenant's
// full committed state. This mirrors processCommandAndReply's T4-A gate
// exactly: require a resolved TenantContext and reject one that mismatches
// this actor's already-seeded actorTenant.
func (entity *Actor) getStateAndReply(ctx *goakt.ReceiveContext) {
	if entity.tenantAware {
		tc, err := tenancy.Require(ctx.Context())
		if err != nil {
			entity.sendErrorReply(ctx, err)
			return
		}
		if entity.actorTenant != noTenantContext {
			if verifyErr := tenancy.VerifyUnchanged(entity.actorTenant, tc); verifyErr != nil {
				entity.sendErrorReply(ctx, verifyErr)
				return
			}
		}
	}
	entity.sendStateReply(ctx)
}

// processCommandAndReply handles an incoming command by generating events and
// dispatching them to the events writer asynchronously; state changes
// are applied only after persistence is confirmed (see persistAsync,
// handleDirectPersistResponse, replyDirect).
//
// On persistence failure the actor replies with an error and shuts itself down
// so the supervisor can restart it with clean state recovered from the store.
func (entity *Actor) processCommandAndReply(ctx *goakt.ReceiveContext, command Command) {
	goCtx := ctx.Context()
	startTime := time.Now()

	goCtx, span := instrumentation.StartCommandSpan(goCtx, entity.tracer, entity.persistenceID, command)
	entity.metrics.CommandReceived(goCtx)

	// Pre-handler gate (T4-A): in tenant-aware mode, HandleCommand must never
	// run without a TenantContext already attached by Engine.SendCommand.
	// This reuses tenancy.Require, a read-only check of the context already
	// in hand; it never calls a resolver and never re-resolves. A saga or
	// any other caller that bypasses SendCommand (e.g. via NoSender with a
	// fresh context.Background()) fails closed here instead of silently
	// running without an identity.
	//
	// The resolved tc is captured (not merely checked for a non-error) so
	// buildEnvelopes below can serialize it into each persisted event's
	// tenant_metadata (EGO-TENANT-002 Phase 2) and establishActorTenant can
	// seed this actor's lifetime identity from it (Phase 2/3).
	//
	// Net-new enforcement (Phase 4, design.md risk #3): before this fix,
	// this gate only proved presence via tenancy.Require, never identity
	// match against the actor — unlike processAndBatch's equivalent gate,
	// which already compared against actorTenant. A cross-tenant command on
	// a seeded, non-batched actor would run HandleCommand and persist
	// against the wrong tenant's state. Mirrors processAndBatch's gate
	// exactly: verify BEFORE HandleCommand runs, not only after
	// buildEnvelopes.
	var tc tenancy.TenantContext
	if entity.tenantAware {
		var err error
		tc, err = tenancy.Require(goCtx)
		if err != nil {
			entity.endCommandSpan(goCtx, span, startTime)
			entity.sendErrorReply(ctx, err)
			return
		}
		if entity.actorTenant != noTenantContext {
			if verifyErr := tenancy.VerifyUnchanged(entity.actorTenant, tc); verifyErr != nil {
				entity.endCommandSpan(goCtx, span, startTime)
				entity.sendErrorReply(ctx, verifyErr)
				return
			}
		}
	}

	// Deadline pre-handler gate: goCtx carries the effective deadline
	// Engine.Dispatch computed (min of ctx's own deadline, the envelope
	// Metadata's deadline, and the caller's timeout). If it has already
	// expired or been canceled, fail closed before the handler runs — the
	// handler must never execute past the deadline.
	if err := protocol.CheckDeadline(goCtx, "before handler execution"); err != nil {
		entity.sendErrorReply(ctx, err)
		return
	}

	events, err := entity.dispatchToBehavior(goCtx, command, entity.currentState)
	if err != nil {
		entity.endCommandSpan(goCtx, span, startTime)
		entity.sendErrorReply(ctx, err)
		return
	}

	if len(events) == 0 {
		entity.endCommandSpan(goCtx, span, startTime)
		entity.sendStateReply(ctx)
		return
	}

	envelopes, pendingState, pendingCounter, commandTime, err := entity.buildEnvelopes(goCtx, events, tc, entity.currentState, entity.eventsCounter)
	if err != nil {
		entity.endCommandSpan(goCtx, span, startTime)
		entity.sendErrorReply(ctx, err)
		return
	}

	// Deadline post-handler/pre-persist gate: the handler above may have run
	// long enough for the deadline to expire while it was in flight —
	// context.WithDeadline does not preempt a handler that ignores its
	// context, so this re-check is the actual barrier that stops a late
	// handler output from mutating currentState or reaching the store.
	// envelopes/pendingState/pendingCounter are discarded, never persisted.
	if err := protocol.CheckDeadline(goCtx, "before persistence"); err != nil {
		entity.sendErrorReply(ctx, err)
		return
	}

	// Defensive persistence invariant (T4-B, design.md D4): re-confirm a
	// valid tenant identity is present before these events are handed to
	// persistEvents. This reuses tenancy.Require — a read-only check of the
	// context already validated by the pre-handler gate above — and never
	// re-invokes TenantResolver.Resolve. It is deliberately not the sole
	// enforcement point: T4-A above already blocks HandleCommand itself.
	if err := entity.verifyTenantForPersist(goCtx); err != nil {
		entity.endCommandSpan(goCtx, span, startTime)
		entity.sendErrorReply(ctx, err)
		return
	}

	// Establish this actor's lifetime tenant identity (D6) on its first
	// successful persist, mirroring processAndBatch's equivalent step below.
	// A no-op once actorTenant is already seeded (by recover(), Phase 3, or
	// an earlier command on this actor).
	entity.establishActorTenant(tc)

	revision, hasRevision := protocol.ExpectedRevisionFromContext(goCtx)
	precondition := protocol.PreconditionFromRevision(revision, hasRevision)

	entity.persistAsync(ctx, envelopes, pendingState, pendingCounter, commandTime, startTime, span, precondition)
}

// endCommandSpan ends span (if tracing is enabled) and records the
// commandsDuration metric (if metrics are enabled), measured from startTime.
// Factored out so every processCommandAndReply exit path — early returns and
// the async completion in replyDirect — records identically, regardless of
// whether the command finished synchronously or after a persistAsync round
// trip.
func (entity *Actor) endCommandSpan(goCtx context.Context, span trace.Span, startTime time.Time) {
	if span != nil {
		span.End()
	}
	entity.metrics.CommandCompleted(goCtx, startTime)
}

// persistAsync dispatches envelopes to the eventsWriter asynchronously via
// ctx.PipeTo, mirroring flushBatch's approach for the batched path (see its
// doc comment). Blocking the calling worker with a synchronous ctx.Ask here
// let the entity's own eventsWriter child — which needs a worker from that
// same shared dispatcher pool to reply — starve alongside every other
// concurrently-persisting entity once the pool was exhausted (issue #64).
// The originating command is stashed and redelivered by
// handleDirectPersistResponse once the write completes.
func (entity *Actor) persistAsync(ctx *goakt.ReceiveContext, envelopes []*egopb.Event, pendingState State, pendingCounter uint64, commandTime time.Time, startTime time.Time, span trace.Span, precondition persistence.WritePrecondition) {
	writer := entity.eventsWriter
	timeout := entity.persistTimeout
	scope := entity.scope

	entity.directPendingState = pendingState
	entity.directPendingCounter = pendingCounter
	entity.directPendingTime = commandTime
	entity.directNumEvents = len(envelopes)
	entity.directStartTime = startTime
	entity.directSpan = span
	entity.directRequest = newDirectRequest(ctx)

	ctx.Stash()

	ctx.PipeTo(ctx.Self(), func() (any, error) {
		return askEventsWriter(writer, envelopes, protocol.EventsTopic, timeout, precondition, scope)
	})

	entity.phase = phasePersisting
}

// handleDirectPersistResponse processes the result PipeTo delivers after the
// eventsWriter completes a single (non-batched) command's write, mirroring
// handleBatchPersistResponse for the batched path.
func (entity *Actor) handleDirectPersistResponse(ctx *goakt.ReceiveContext, resp *persistEventsResponse) {
	if entity.phase != phasePersisting {
		return
	}

	if resp.Err != nil {
		entity.directErr = resp.Err
		entity.directShutdown = !entity.shouldStayAliveAfterConflict(resp.Err)
		entity.phase = phaseDirectReplying
		ctx.UnstashAll()
		return
	}

	entity.applyConfirmedState(ctx.Context(), entity.directPendingState, entity.directPendingCounter, entity.directPendingTime, entity.directNumEvents)
	entity.triggerSnapshotAndRetention(ctx)

	entity.directErr = nil
	entity.phase = phaseDirectReplying
	ctx.UnstashAll()
}

// directRequest identifies one request to the entity: the message, the context
// of the call that sent it, and the sender. An actor cannot tell requests apart
// by the message alone, because a caller may send the same message object more
// than once, and two callers may share one: what separates the requests is the
// call. GoAkt's Ask hands the actor the context of the caller's call, the
// message as sent and the sender, and a stashed message keeps all three, so a
// re-delivered request compares equal to itself and to no other. Each caller
// derives its own context per call (Engine.Dispatch does), so two calls that
// send the same message object still differ.
type directRequest struct {
	set     bool
	message any
	ctx     context.Context
	sender  *goakt.PID
}

// newDirectRequest records the request ctx is delivering.
func newDirectRequest(ctx *goakt.ReceiveContext) directRequest {
	return directRequest{set: true, message: ctx.Message(), ctx: ctx.Context(), sender: ctx.Sender()}
}

// isRequest reports whether ctx is delivering the recorded request.
func (r directRequest) isRequest(ctx *goakt.ReceiveContext) bool {
	return r.matches(ctx.Message(), ctx.Context(), ctx.Sender())
}

// matches reports whether ctx is delivering the recorded request. A value that
// cannot be compared cannot tell requests apart, and is taken to match, which is
// what the actor assumed for every request before it recorded any.
func (r directRequest) matches(message any, ctx context.Context, sender *goakt.PID) bool {
	if !r.set {
		return true
	}
	return r.sender == sender && sameValue(r.message, message) && sameValue(r.ctx, ctx)
}

// sameValue reports whether a and b are the same value, without panicking on a
// type that cannot be compared. Values of such a type are taken to be the same.
func sameValue(a, b any) bool {
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	if a == nil || !reflect.TypeOf(a).Comparable() {
		return true
	}
	return a == b
}

// replyDirect delivers the reply to the stashed command whose persist write
// handleDirectPersistResponse has confirmed (or failed), mirroring
// replyFromBatch for the batched path. Other commands may be stashed behind it
// (see Receive); they are not answered here, and UnstashAll below releases them
// once the reply is out.
//
// A GetStateCommand that arrives while phase is phaseDirectReplying stashes
// itself (see handleGetStateCommand) rather than reading stale state, and so
// does any command other than the one whose write just finished (see Receive
// and directRequest). UnstashAll here is what redelivers them once phase
// drops back to phaseProcessing.
//
// GoAkt's unstashAll re-delivers through the mailbox's normal enqueue, that is
// at its tail, in the order the messages were stashed; its documentation says
// it prepends, but it does not. So the stashed command is not necessarily the
// next message Receive sees: a command that was already queued behind the
// write's confirmation comes first. Treating whatever arrived first as the
// stashed command answered that command with the current state without running
// it, and ran the stashed command a second time later. A PID only ever runs one
// turn at a time, so nothing this actor unstashes can be dequeued before the
// current Receive call returns. We still send the reply before calling
// UnstashAll() so the ordering is explicit in the code. It is a no-op when
// nothing stashed during the window. When the failed write leaves the actor
// unable to tell what the store holds, it stops instead, once it has answered
// the commands the stash holds (see stopAfterAnswering).
func (entity *Actor) replyDirect(ctx *goakt.ReceiveContext) {
	entity.endCommandSpan(ctx.Context(), entity.directSpan, entity.directStartTime)
	entity.directSpan = nil
	entity.directRequest = directRequest{}
	entity.phase = phaseProcessing

	if entity.directErr != nil {
		err := entity.directErr
		shutdown := entity.directShutdown
		entity.directErr = nil
		entity.directShutdown = false
		entity.sendErrorReply(ctx, err)
		if shutdown {
			entity.stopAfterAnswering(ctx)
		} else {
			ctx.UnstashAll()
		}
		return
	}

	entity.sendStateReply(ctx)
	ctx.UnstashAll()
}

// buildEnvelopes computes the pending state from the given events and creates
// the protobuf envelopes ready for persistence. The actor state is not modified.
// startState and startCounter specify the base state and sequence number to
// apply events against, allowing callers to chain calls across batched commands.
// tc is the TenantContext resolved by the caller's pre-handler gate (zero
// value in legacy mode); it is serialized into each envelope by marshalEvent
// (EGO-TENANT-002 Phase 2) but never used to mutate actor state here.
// Returns the envelopes, pending state, pending counter, and command timestamp.
func (entity *Actor) buildEnvelopes(goCtx context.Context, events []Event, tc tenancy.TenantContext, startState State, startCounter uint64) ([]*egopb.Event, State, uint64, time.Time, error) {
	pendingState := startState
	pendingCounter := startCounter
	commandTime := time.Now()

	// Stack-allocate for the common single-event case to avoid a heap allocation.
	var buf [1]*egopb.Event
	envelopes := buf[:0]
	if len(events) > len(buf) {
		envelopes = make([]*egopb.Event, 0, len(events))
	}
	for _, event := range events {
		resultingState, err := entity.behavior.HandleEvent(goCtx, event, pendingState)
		if err != nil {
			return nil, nil, 0, time.Time{}, err
		}

		pendingCounter++
		pendingState = resultingState

		envelope, err := entity.marshalEvent(goCtx, event, tc, pendingCounter, commandTime, entity.shardNumber)
		if err != nil {
			return nil, nil, 0, time.Time{}, err
		}

		envelopes = append(envelopes, envelope)
	}

	return envelopes, pendingState, pendingCounter, commandTime, nil
}

// marshalEvent serializes a domain event into a protobuf envelope, applying
// encryption when an encryptor is configured. In tenant-aware mode, tc is
// serialized onto the envelope's TenantMetadata field via
// tenancy.MarshalMetadata (D9 carrier reuse, EGO-TENANT-002 Phase 2);
// legacy mode writes no tenant metadata (D2).
func (entity *Actor) marshalEvent(ctx context.Context, event Event, tc tenancy.TenantContext, seqNr uint64, ts time.Time, shard uint64) (*egopb.Event, error) {
	eventAny, _ := anypb.New(event)

	var encKeyID string
	var isEncrypted bool

	if entity.encryptor != nil {
		eventBytes, err := proto.Marshal(eventAny)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal event for encryption: %w", err)
		}

		ciphertext, keyID, err := entity.encryptor.Encrypt(ctx, entity.persistenceID, eventBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt event: %w", err)
		}

		eventAny = &anypb.Any{
			TypeUrl: eventAny.GetTypeUrl(),
			Value:   ciphertext,
		}
		encKeyID = keyID
		isEncrypted = true
	}

	envelope := &egopb.Event{
		PersistenceId:   entity.persistenceID,
		SequenceNumber:  seqNr,
		IsDeleted:       false,
		Event:           eventAny,
		Timestamp:       ts.UnixNano(),
		Shard:           shard,
		EncryptionKeyId: encKeyID,
		IsEncrypted:     isEncrypted,
	}

	if entity.tenantAware {
		envelope.TenantMetadata = tenancy.MarshalMetadata(tc)
	}

	return envelope, nil
}

// verifyTenantForPersist re-confirms, from goCtx alone, that a valid tenant
// identity is present before this command's events are committed (T4-B,
// design.md D4). It is a no-op in legacy mode (tenantAware == false) and,
// in tenant-aware mode, does nothing but read the context already attached
// by Engine.SendCommand and validated by the pre-handler gate: it never
// invokes a TenantResolver and is never the sole enforcement point for
// fail-closed behavior.
func (entity *Actor) verifyTenantForPersist(goCtx context.Context) error {
	if !entity.tenantAware {
		return nil
	}
	_, err := tenancy.Require(goCtx)
	return err
}

// establishActorTenant seeds entity.actorTenant with tc when it has not yet
// been seeded (D6, EGO-TENANT-002). It is a no-op in legacy mode and a
// no-op once actorTenant already holds a value — whether seeded earlier by
// recover() (Phase 3) or by an earlier successful persist on this same
// actor. This performs no comparison: cross-tenant rejection against an
// already-seeded actorTenant is a separate, explicit gate in
// processCommandAndReply and processAndBatch (Phase 4), not a side effect
// of establishing it here.
func (entity *Actor) establishActorTenant(tc tenancy.TenantContext) {
	if entity.tenantAware && entity.actorTenant == noTenantContext {
		entity.actorTenant = tc
	}
}

// seedActorTenant seeds entity.actorTenant with tc during recovery
// (recoverFromSnapshot, recover; D5/D6, EGO-TENANT-002). Unlike
// establishActorTenant, it is not a silent no-op once actorTenant already
// holds a value: recovery can seed from two sources in sequence (a
// snapshot, then the latest event), so a second call here cross-checks tc
// against the value the first call seeded via tenancy.VerifyUnchanged,
// surfacing ErrDenied when the snapshot and the latest event disagree on
// tenant. It is a no-op in legacy mode (tenantAware == false).
func (entity *Actor) seedActorTenant(tc tenancy.TenantContext) error {
	if !entity.tenantAware {
		return nil
	}
	if entity.actorTenant == noTenantContext {
		entity.actorTenant = tc
		return nil
	}
	return tenancy.VerifyUnchanged(entity.actorTenant, tc)
}

// applyConfirmedState updates the actor state after the events store has
// confirmed the write and records persistence metrics.
func (entity *Actor) applyConfirmedState(goCtx context.Context, state State, counter uint64, ts time.Time, numEvents int) {
	entity.eventsCounter = counter
	entity.currentState = state
	entity.cachedStateAny, _ = anypb.New(state) // eagerly cache for the reply that follows
	entity.lastCommandTime = ts

	entity.metrics.EventsPersisted(goCtx, numEvents)
}

// triggerSnapshotAndRetention fires an asynchronous snapshot request to the
// dedicated child actor when the snapshot interval is reached. If a retention
// policy is active, the retention request is bundled into the snapshot request
// so the snapshot writer forwards it to the janitor only after the snapshot is
// confirmed persisted. This prevents the race where retention could delete old
// data before the new snapshot is safely written.
func (entity *Actor) triggerSnapshotAndRetention(ctx *goakt.ReceiveContext) {
	if entity.snapshotsWriter == nil || entity.snapshotInterval == 0 || entity.eventsCounter%entity.snapshotInterval != 0 {
		return
	}
	entity.snapshotAndRetain(ctx)
}

// snapshotAndRetain creates a snapshot of the current confirmed state and
// sends it to the snapshot writer child. If retention is enabled, the
// retention request is bundled so cleanup occurs only after the snapshot
// is confirmed persisted.
func (entity *Actor) snapshotAndRetain(ctx *goakt.ReceiveContext) {
	req := &persistSnapshotRequest{
		snapshot: entity.newSnapshotEnvelope(entity.currentStateAny()),
		scope:    entity.scope,
	}

	if entity.eventsJanitor != nil && entity.retentionPolicy != nil {
		req.retentionReq = &applyRetentionRequest{
			persistenceID:             entity.persistenceID,
			eventsCounter:             entity.eventsCounter,
			snapshotInterval:          entity.snapshotInterval,
			deleteEventsOnSnapshot:    entity.retentionPolicy.DeleteEventsOnSnapshot,
			deleteSnapshotsOnSnapshot: entity.retentionPolicy.DeleteSnapshotsOnSnapshot,
			eventsRetentionCount:      entity.retentionPolicy.EventsRetentionCount,
			scope:                     entity.scope,
		}
		req.janitor = entity.eventsJanitor
	}

	ctx.Tell(entity.snapshotsWriter, req)
}

// newSnapshotEnvelope creates a [egopb.Snapshot] with unencrypted state from
// the current entity. Encryption is handled by the [snapshotsWriterActor].
// In tenant-aware mode, the actor's established entity.actorTenant is
// serialized onto the snapshot's TenantMetadata field (D5, EGO-TENANT-002
// Phase 2): a snapshot may be taken with no in-flight command context (e.g.
// after an asynchronous batch flush), so it cannot rely on a per-command
// TenantContext the way marshalEvent does.
func (entity *Actor) newSnapshotEnvelope(state *anypb.Any) *egopb.Snapshot {
	snapshot := &egopb.Snapshot{
		PersistenceId:  entity.persistenceID,
		SequenceNumber: entity.eventsCounter,
		State:          state,
		Timestamp:      entity.lastCommandTime.UnixNano(),
	}

	if entity.tenantAware {
		snapshot.TenantMetadata = tenancy.MarshalMetadata(entity.actorTenant)
	}

	return snapshot
}

// batchEnabled reports whether event batching is active for this entity.
func (entity *Actor) batchEnabled() bool {
	return entity.batchThreshold > 0
}

// latestState returns the most recent state, which may be an unconfirmed
// pending state from the current batch or the last committed state.
func (entity *Actor) latestState() State {
	if len(entity.batchEntries) > 0 {
		return entity.batchState
	}
	return entity.currentState
}

// latestCounter returns the most recent sequence number, which may include
// unconfirmed events from the current batch.
func (entity *Actor) latestCounter() uint64 {
	if len(entity.batchEntries) > 0 {
		return entity.batchCounter
	}
	return entity.eventsCounter
}

// handleCommandBatched dispatches an incoming command according to the
// current batch processing phase.
func (entity *Actor) handleCommandBatched(ctx *goakt.ReceiveContext, command Command) {
	switch entity.phase {
	case phaseProcessing:
		entity.processAndBatch(ctx, command)
	case phaseFlushing:
		ctx.Stash()
	case phaseReplying:
		entity.replyFromBatch(ctx)
	}
}

// processAndBatch processes a command optimistically against the latest
// (possibly unconfirmed) state, appends the resulting event envelopes to
// the batch buffer, stashes the command so its response channel is
// preserved, and triggers a flush when the accumulated event count
// reaches the batch threshold.
func (entity *Actor) processAndBatch(ctx *goakt.ReceiveContext, command Command) {
	goCtx := ctx.Context()
	startTime := time.Now()

	goCtx, span := instrumentation.StartCommandSpan(goCtx, entity.tracer, entity.persistenceID, command)
	entity.metrics.CommandReceived(goCtx)

	state := entity.latestState()
	counter := entity.latestCounter()

	// Pre-handler gate (T4-A): same reused read-only check as
	// processCommandAndReply — see its comment for the rationale. The
	// batched path must fail closed here too, before flushBatch's own
	// context.Background() call is ever reached.
	//
	// Blocker 3 fix (EGO-TENANT-006 review), widened to actor-lifetime scope
	// (D6, EGO-TENANT-002): this gate also captures the resolved
	// TenantContext (tc) and, if actorTenant is already seeded (by
	// recover(), Phase 3, or an earlier command on this actor — not merely
	// a batch already open), verifies it against tc BEFORE
	// entity.behavior.HandleCommand runs below — not only after
	// buildEnvelopes, as this used to. state (via latestState()) is
	// entity.batchState whenever a batch is open: another tenant's
	// accumulated, unpersisted data. Deferring the homogeneity check until
	// after the handler ran meant a cross-tenant call already executed
	// against the wrong tenant's batchState, and — if it happened to
	// produce zero events — the len(events)==0 branch below would reply
	// with that wrong-tenant state before the (then-later) homogeneity
	// check was ever reached. See design.md Decision D4/D6/D8.
	var tc tenancy.TenantContext
	if entity.tenantAware {
		var requireErr error
		tc, requireErr = tenancy.Require(goCtx)
		if requireErr != nil {
			if span != nil {
				span.End()
			}
			entity.sendErrorReply(ctx, requireErr)
			return
		}
		if entity.actorTenant != noTenantContext {
			if verifyErr := tenancy.VerifyUnchanged(entity.actorTenant, tc); verifyErr != nil {
				if span != nil {
					span.End()
				}
				entity.sendErrorReply(ctx, verifyErr)
				return
			}
		}
	}

	// Deadline pre-handler gate: same barrier as processCommandAndReply — see
	// its comment for the rationale. Must fail closed here too, before the
	// handler ever runs against batchState.
	if deadlineErr := protocol.CheckDeadline(goCtx, "before handler execution"); deadlineErr != nil {
		if span != nil {
			span.End()
		}
		entity.sendErrorReply(ctx, deadlineErr)
		return
	}

	// Admission gate (design.md D9): a command declaring ExpectedRevision=E
	// joins the currently open batch only if E == counter (the revision
	// the batch reaches immediately before this command's own events). A
	// command declaring nothing always joins. This only matters once a
	// batch is already open (batchEntries non-empty) — the command that
	// opens a fresh batch always founds it, since there is nothing yet to
	// be inconsistent with; see batchBase's field comment. This is a
	// batching-boundary decision only, never a commit decision: an
	// inadmissible command forces an early flush of what is already
	// staged, then stashes itself to be reprocessed as a fresh command
	// once that flush's reply drains (replyFromBatch's surplus-message
	// path). The store's CAS remains the sole authority over whether any
	// given precondition actually holds.
	revision, hasRevision := protocol.ExpectedRevisionFromContext(goCtx)
	if len(entity.batchEntries) > 0 && hasRevision && revision != counter {
		if span != nil {
			span.End()
		}
		entity.flushBatch(ctx)
		ctx.Stash()
		return
	}

	// A non-founding command that reaches this point has, by the admission
	// gate just above, declared ExpectedRevision == counter (or declared
	// nothing at all). Record "this batch cycle owes a real precondition"
	// right here, unconditionally, rather than deferring it to the
	// events-seeding block further down: that block sits after the
	// len(events)==0 early return below, so a command that is admitted with
	// a declared revision but goes on to produce zero events of its own
	// (e.g. an idempotent no-op) would otherwise never reach it, silently
	// losing its declared guarantee exactly like the bug this whole
	// mechanism exists to prevent — the fact that it produced no events of
	// its own does not mean the batch's physical CAS may stop honoring the
	// revision it was admitted under. The founder's own case is unaffected
	// (len(entity.batchEntries) is still 0 for it here) and continues to be
	// seeded only once its events are confirmed, per batchBase's field
	// comment below.
	if len(entity.batchEntries) > 0 && hasRevision {
		entity.batchHasPrecondition = true
	}

	events, err := entity.dispatchToBehavior(goCtx, command, state)
	if err != nil {
		if span != nil {
			span.End()
		}
		entity.sendErrorReply(ctx, err)
		return
	}

	if len(events) == 0 {
		if span != nil {
			span.End()
		}
		// When no batch entries exist, state == currentState so the cache applies.
		var stateAny *anypb.Any
		if len(entity.batchEntries) == 0 {
			stateAny = entity.currentStateAny()
		} else {
			stateAny, _ = anypb.New(state)
		}
		ctx.Response(&egopb.CommandReply{
			Reply: &egopb.CommandReply_StateReply{
				StateReply: &egopb.StateReply{
					PersistenceId:  entity.persistenceID,
					State:          stateAny,
					SequenceNumber: counter,
					Timestamp:      entity.lastCommandTime.UnixNano(),
				},
			},
		})
		return
	}

	envelopes, pendingState, pendingCounter, commandTime, err := entity.buildEnvelopes(goCtx, events, tc, state, counter)
	if err != nil {
		if span != nil {
			span.End()
		}
		entity.sendErrorReply(ctx, err)
		return
	}

	// Deadline post-handler/pre-batch-append gate: the handler above may
	// have run long enough for the deadline to expire while it was in
	// flight. This is the barrier that keeps an expired command's output
	// out of the shared batch state entirely — envelopes/pendingState/
	// pendingCounter are discarded here, never appended to batchBuffer/
	// batchState/batchEntries, so a later flushBatch never persists them and
	// a subsequent valid command on this actor is unaffected.
	if deadlineErr := protocol.CheckDeadline(goCtx, "before persistence"); deadlineErr != nil {
		if span != nil {
			span.End()
		}
		entity.sendErrorReply(ctx, deadlineErr)
		return
	}

	// Establish this actor's lifetime tenant identity (D6) on its first
	// successful persist, mirroring processCommandAndReply's equivalent
	// step. A no-op once actorTenant is already seeded (by recover(),
	// Phase 3, or an earlier command on this actor) — homogeneity itself
	// was already verified above, in the pre-handler gate, before
	// HandleCommand ran (Blocker 3 fix). Unlike the prior per-batch-cycle
	// "batchTenant", this is never cleared by resetBatch: actor-lifetime
	// scope strictly subsumes batch-cycle scope (D6).
	entity.establishActorTenant(tc)

	// Seed the batch's base revision (design.md D9) the moment a fresh
	// batch opens (batchEntries still empty at this point in the call).
	// batchBase anchors the eventual flush's precondition to the physical
	// store revision the batch's events actually append onto: the founding
	// command's own declared revision when it declares one — mirroring
	// preconditionFromRevision's D4 mapping for the single-command (direct)
	// path exactly, just deferred to flush time — or, when the founder
	// declares nothing, `counter` (== entity.eventsCounter at this point,
	// since batchEntries is still empty), the real pre-batch store revision.
	// A founding command is always admitted regardless of what it declares
	// (see the admission gate above), but its own stale declared revision is
	// still caught: the store's CAS on flush validates batchBase against the
	// real store revision, exactly as it would for a non-batched command
	// declaring the same value.
	//
	// batchBase is set only here, once, and never moves afterward — every
	// later admitted command's own declared revision (if any) was already
	// validated by the admission gate above against the running logical
	// counter, which is exactly equivalent to re-checking it against
	// batchBase at flush time (design.md D9 step 3's equivalence argument),
	// so a later command's declared revision must never overwrite batchBase:
	// doing so would anchor the physical CAS to a logical mid-batch revision
	// the store was never at, guaranteeing every such flush fails.
	//
	// batchHasPrecondition, separately, must reflect whether ANY admitted
	// command in this batch cycle declared a revision — not merely the
	// founder — since design.md D9 step 3 resolves to Unconditional() only
	// when "no admitted command declared one". A batch founded unconditional
	// still owes a later admitted command's explicit ExpectedRevision a real
	// CAS precondition; leaving batchHasPrecondition pinned to the founder's
	// own hasRevision would silently downgrade that later command's declared
	// guarantee to Unconditional() at flush, which is the bug this comment
	// block prevents. The non-founder case is handled above, immediately
	// after the admission gate (and before the len(events)==0 early return),
	// so only the founder's own seeding remains here.
	if len(entity.batchEntries) == 0 {
		entity.batchHasPrecondition = hasRevision
		if hasRevision {
			entity.batchBase = revision
		} else {
			entity.batchBase = counter
		}
	}

	entity.batchBuffer = append(entity.batchBuffer, envelopes...)
	entity.batchState = pendingState
	entity.batchCounter = pendingCounter
	entity.batchTime = commandTime
	entity.batchNumEvents += len(envelopes)

	stateAny, _ := anypb.New(pendingState)
	entry := batchEntry{
		reply: &egopb.CommandReply{
			Reply: &egopb.CommandReply_StateReply{
				StateReply: &egopb.StateReply{
					PersistenceId:  entity.persistenceID,
					State:          stateAny,
					SequenceNumber: pendingCounter,
					Timestamp:      commandTime.UnixNano(),
				},
			},
		},
		startTime: startTime,
		span:      span,
		request:   newDirectRequest(ctx),
	}
	// batchMu guards the slice header so it can be observed from outside the
	// receive goroutine (tests, PreStart) without racing the append.
	entity.batchMu.Lock()
	entity.batchEntries = append(entity.batchEntries, entry)
	entity.batchMu.Unlock()

	ctx.Stash()

	if entity.batchNumEvents >= entity.batchThreshold {
		entity.flushBatch(ctx)
		return
	}

	entity.startFlushTimer(ctx)
}

// flushBatch sends the accumulated event envelopes to the events writer
// asynchronously via PipeTo and transitions to phaseFlushing. The writer
// is called through goakt.Ask inside a goroutine so the actor remains
// responsive while the write is in flight.
func (entity *Actor) flushBatch(ctx *goakt.ReceiveContext) {
	entity.stopFlushTimer()

	topic := protocol.EventsTopic
	envelopes := entity.batchBuffer
	writer := entity.eventsWriter
	timeout := entity.persistTimeout
	precondition := entity.resolveBatchPrecondition()
	scope := entity.scope

	ctx.PipeTo(ctx.Self(), func() (any, error) {
		return askEventsWriter(writer, envelopes, topic, timeout, precondition, scope)
	})

	entity.phase = phaseFlushing
}

// resolveBatchPrecondition implements design.md D9's resolution step: the
// one atomic write a flush performs carries a single WritePrecondition
// covering every command folded into the batch. When no admitted command
// declared a revision at all, the flush is unconditional. Otherwise the
// batch's base revision (batchBase, seeded when the batch opened) decides
// between ExpectGenesis (an empty store) and ExpectRevision(batchBase) —
// mirroring preconditionFromRevision's D4 mapping, but anchored to the
// batch's base rather than a single command's own declared revision.
func (entity *Actor) resolveBatchPrecondition() persistence.WritePrecondition {
	if !entity.batchHasPrecondition {
		return persistence.Unconditional()
	}
	if entity.batchBase == 0 {
		return persistence.ExpectGenesis()
	}
	return persistence.ExpectRevision(entity.batchBase)
}

// handleBatchFlushTick is invoked when the flush window timer expires.
// If the actor is still in phaseProcessing with a non-empty batch, the
// batch is flushed immediately.
func (entity *Actor) handleBatchFlushTick(ctx *goakt.ReceiveContext) {
	entity.batchMu.Lock()
	entity.flushTimer = nil
	entity.batchMu.Unlock()
	if entity.phase != phaseProcessing || len(entity.batchEntries) == 0 {
		return
	}
	entity.flushBatch(ctx)
}

// handleBatchPersistResponse processes the result delivered by PipeTo after
// the events writer completes a batch write.
//
// On success: the confirmed state is committed, snapshot and retention are
// triggered when applicable, and all stashed commands are unstashed so each
// caller receives its pre-computed reply.
//
// On failure: all pre-computed replies are replaced with error replies, the
// stashed commands are unstashed so callers are notified, and the actor
// shuts down after all replies have been sent so the supervisor can restart
// it with clean state.
func (entity *Actor) handleBatchPersistResponse(ctx *goakt.ReceiveContext, resp *persistEventsResponse) {
	if entity.phase != phaseFlushing {
		return
	}

	if resp.Err != nil {
		errReply := &egopb.CommandReply{
			Reply: &egopb.CommandReply_ErrorReply{
				ErrorReply: &egopb.ErrorReply{
					Message: resp.Err.Error(),
				},
			},
		}
		for i := range entity.batchEntries {
			if entity.batchEntries[i].span != nil {
				entity.batchEntries[i].span.End()
				entity.batchEntries[i].span = nil
			}
			entity.batchEntries[i].reply = errReply
		}
		entity.remainingReplies = len(entity.batchEntries)
		entity.shutdownOnDrain = !entity.shouldStayAliveAfterConflict(resp.Err)
		entity.phase = phaseReplying
		ctx.UnstashAll()
		return
	}

	previousCounter := entity.eventsCounter
	entity.applyConfirmedState(ctx.Context(), entity.batchState, entity.batchCounter, entity.batchTime, entity.batchNumEvents)

	if entity.crossedSnapshotBoundary(previousCounter) {
		entity.snapshotAndRetain(ctx)
	}

	entity.remainingReplies = len(entity.batchEntries)
	entity.phase = phaseReplying
	ctx.UnstashAll()
}

// replyFromBatch sends the next pre-computed reply to the current (unstashed)
// caller. When all stashed commands have been answered, the actor resets its
// batch state and returns to phaseProcessing, or shuts down if the preceding
// batch write failed.
//
// If more messages were unstashed than there are pending replies (e.g. commands
// that arrived during phaseFlushing), the surplus messages are processed as new
// commands in a fresh batch cycle.
func (entity *Actor) replyFromBatch(ctx *goakt.ReceiveContext) {
	if entity.remainingReplies <= 0 {
		entity.phase = phaseProcessing
		command, ok := ctx.Message().(Command)
		if !ok {
			ctx.Unhandled()
			return
		}
		entity.processAndBatch(ctx, command)
		return
	}

	idx := len(entity.batchEntries) - entity.remainingReplies
	entry := &entity.batchEntries[idx]

	// Only the request this entry was computed for is answered with it. GoAkt
	// re-delivers the stashed requests at the tail of the mailbox, so a command
	// already queued behind the write's confirmation (another saga's, say) can
	// arrive first: answering it with this entry would send its caller the
	// reply of another request without running it, and the stashed request
	// would then run a second time as a new command. It waits in the stash
	// until the last reply releases it.
	if !entry.request.isRequest(ctx) {
		ctx.Stash()
		return
	}

	if entry.span != nil {
		entry.span.End()
		entry.span = nil
	}

	entity.metrics.CommandCompleted(ctx.Context(), entry.startTime)

	ctx.Response(entry.reply)
	entity.remainingReplies--

	if entity.remainingReplies == 0 {
		shouldShutdown := entity.shutdownOnDrain
		entity.resetBatch()
		entity.phase = phaseProcessing
		// Release the commands set aside above. On the error path they are
		// answered before the actor is torn down.
		if shouldShutdown {
			entity.stopAfterAnswering(ctx)
		} else {
			ctx.UnstashAll()
		}
	}
}

// stopAfterAnswering stops the actor after it has answered every command that
// reached it before the stop. The commands in the stash, and any queued ahead of
// them, were accepted, but the actor cannot run them: after a failed write it
// cannot tell what the store holds. Each is answered with errEntityStopped, so
// no caller is left to its own timeout.
//
// UnstashAll followed by Shutdown would not do it. UnstashAll re-delivers the
// stash at the tail of the mailbox, and Shutdown does not process what the
// mailbox still holds: those commands would be dropped unanswered. So the actor
// releases the stash, then sends itself stashDrained, which lands behind every
// released command, and shuts down on receiving it.
func (entity *Actor) stopAfterAnswering(ctx *goakt.ReceiveContext) {
	entity.phase = phaseStopping
	ctx.UnstashAll()
	ctx.Tell(ctx.Self(), new(stashDrained))
}

// crossedSnapshotBoundary reports whether the range
// (previousCounter, entity.eventsCounter] contains at least one multiple
// of snapshotInterval.
func (entity *Actor) crossedSnapshotBoundary(previousCounter uint64) bool {
	if entity.snapshotsWriter == nil || entity.snapshotInterval == 0 {
		return false
	}
	return entity.eventsCounter/entity.snapshotInterval > previousCounter/entity.snapshotInterval
}

// startFlushTimer arms the batch flush timer if it is not already running.
// When the timer fires, a batchFlushTick is delivered to the actor mailbox.
func (entity *Actor) startFlushTimer(ctx *goakt.ReceiveContext) {
	entity.batchMu.Lock()
	defer entity.batchMu.Unlock()
	if entity.flushTimer != nil {
		return
	}
	self := ctx.Self()
	entity.flushTimer = time.AfterFunc(entity.batchFlushWindow, func() {
		_ = goakt.Tell(context.Background(), self, new(batchFlushTick))
	})
}

// stopFlushTimer cancels a running flush timer, if any.
// It is safe to call from both the actor's message-processing goroutine
// and the shutdown goroutine (PostStop).
func (entity *Actor) stopFlushTimer() {
	entity.batchMu.Lock()
	defer entity.batchMu.Unlock()
	if entity.flushTimer != nil {
		entity.flushTimer.Stop()
		entity.flushTimer = nil
	}
}

// resetDirect clears the state of a direct (non-batched) write in flight and
// returns the actor to phaseProcessing. It runs in PreStart, before the first
// turn of a run, so no Receive call reads these fields while it writes them.
func (entity *Actor) resetDirect() {
	entity.phase = phaseProcessing
	entity.directPendingState = nil
	entity.directPendingCounter = 0
	entity.directPendingTime = time.Time{}
	entity.directNumEvents = 0
	entity.directStartTime = time.Time{}
	entity.directSpan = nil
	entity.directErr = nil
	entity.directShutdown = false
	entity.directRequest = directRequest{}
}

// resetBatch clears all batch accumulation state, preparing the actor for
// a new batch cycle. The mutex prevents a data race between the
// message-processing goroutine (replyFromBatch) and the shutdown
// goroutine (PostStop).
func (entity *Actor) resetBatch() {
	entity.batchMu.Lock()
	defer entity.batchMu.Unlock()
	entity.batchBuffer = nil
	entity.batchEntries = nil
	entity.batchState = nil
	entity.batchCounter = 0
	entity.batchTime = time.Time{}
	entity.batchNumEvents = 0
	entity.remainingReplies = 0
	entity.shutdownOnDrain = false
	entity.batchBase = 0
	entity.batchHasPrecondition = false
	// entity.actorTenant (D6, EGO-TENANT-002) is deliberately NOT cleared
	// here: it records the actor's tenant identity for its full lifetime,
	// not merely the batch cycle that just ended. See its field doc comment
	// above for the superseded per-batch-cycle "batchTenant" rationale this
	// replaced.
}
