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

package durablestate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/command"
	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/instrumentation"
	"github.com/getsyntegrity/urd/internal/runner"
	"github.com/getsyntegrity/urd/persistence"
	behaviorport "github.com/getsyntegrity/urd/port/behavior"
	runtimeport "github.com/getsyntegrity/urd/port/runtime"
	"github.com/getsyntegrity/urd/tenancy"
)

// Actor is a durable state based actor
type Actor struct {
	behavior        behaviorport.DurableState
	stateStore      persistence.StateStore
	currentState    State
	cachedStateAny  *anypb.Any // cached marshal of currentState, invalidated on state change
	currentVersion  uint64
	lastCommandTime time.Time
	eventsStream    eventstream.Stream
	actorSystem     goakt.ActorSystem
	persistenceID   string
	tracer          trace.Tracer
	metrics         *instrumentation.Instruments

	// Cached values computed once at startup to avoid per-command allocations.
	shardNumber uint64

	// tenantAware reports whether the actor system was built from a Config
	// with a tenancy.TenantResolver registered (extensions.TenancyMarker
	// present). It is presence-only: the actor never holds a resolver and
	// never calls Resolve — see PreStart. When true, the pre-handler gate in
	// processCommand requires a TenantContext to already be attached to the
	// incoming context (set by Engine.SendCommand at the trust boundary)
	// before HandleCommand runs.
	tenantAware bool

	// actorTenant records this actor's tenant identity for its full
	// lifetime (DS1/DS2, EGO-TENANT-002 PR2), mirroring
	// EventSourcedActor.actorTenant position-for-position. Seeded once in
	// recoverFromStore (DS2) when recovering a committed (version > 0)
	// record, or committed together with the first successful command's
	// state and version in commitState (PR2 review round 2 P1 fix) when
	// starting at genesis — never before HandleCommand and checkPreconditions
	// both succeed, and never before the durable write itself succeeds. Every
	// subsequent command is checked against it via tenancy.VerifyUnchanged.
	// Holds noTenantContext (the zero value, declared
	// event_sourced_actor.go:96, same package) until seeded or committed.
	// No-op in legacy mode.
	actorTenant tenancy.TenantContext

	// scope is the persistence.Scope this actor's store reads and writes
	// are bound to (TENANT-003 T4), mirroring EventSourcedActor.scope
	// position-for-position. Bound once in PreStart via resolveScope, right
	// after tenantAware is set and BEFORE any store read (including
	// recoverFromStore()): persistence.Unscoped() when tenantAware is
	// false, or the tenant scope carried by the per-spawn
	// extensions.EntityTenantScope dependency Engine.DurableStateEntity
	// injects when tenantAware is true.
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

// implements the goakt.Actor interface
var _ goakt.Actor = (*Actor)(nil)

// New creates an instance of an actor provided the DurableStateBehavior
func New() *Actor {
	return &Actor{}
}

// PreStart pre-starts the actor
func (entity *Actor) PreStart(ctx *goakt.Context) error {
	stateStoreExt, err := extensions.Require[*extensions.DurableStateStore](ctx, extensions.DurableStateStoreExtensionID)
	if err != nil {
		return err
	}
	eventsStreamExt, err := extensions.Require[*extensions.EventsStream](ctx, extensions.EventsStreamExtensionID)
	if err != nil {
		return err
	}
	entity.stateStore = stateStoreExt.Underlying()
	entity.eventsStream = eventsStreamExt.Underlying()
	entity.persistenceID = ctx.ActorName()
	// Presence-only signal: tenant-aware mode is active when the engine
	// registered the tenancy marker extension. The marker carries no
	// resolver (internal/extensions.TenancyMarker), so the actor can never
	// reach a TenantResolver through it.
	entity.tenantAware = ctx.Extension(extensions.TenancyExtensionID) != nil

	if err := entity.resolveScope(ctx.Dependencies()); err != nil {
		return err
	}

	// Computed here rather than in PostStart's Receive: PreStart runs on the
	// actor's spawning goroutine before it is registered as running, so it
	// happens-before any concurrent PostStop triggered by an early Shutdown/
	// Kill. Reading shardNumber in PostStop (via persistStateAndPublish) while
	// it was still being written from the dispatcher's PostStart handling was
	// a genuine data race caught by -race.
	for _, dependency := range ctx.Dependencies() {
		if dependency != nil {
			if behavior, ok := extensions.BehaviorFrom[behaviorport.DurableState](dependency); ok {
				entity.behavior = behavior
				break
			}
		}
	}

	if err := entity.bindIdentity(ctx); err != nil {
		return err
	}
	entity.shardNumber = ctx.ActorSystem().Partition(entity.persistenceID)

	telemetryExt, err := extensions.Optional[*extensions.TelemetryExtension](ctx, extensions.TelemetryExtensionID)
	if err != nil {
		return err
	}
	if telemetryExt != nil {
		entity.tracer = telemetryExt.Tracer()
		entity.metrics = instrumentation.New(telemetryExt.Meter())
	}

	if err := runner.
		New(runner.WithFailFast()).
		AddRunner(entity.durableStateRequired).
		AddRunner(func() error {
			if entity.behavior == nil {
				return fmt.Errorf("behavior is required")
			}
			return nil
		}).
		AddRunner(func() error { return entity.stateStore.Ping(ctx.Context()) }).
		AddRunner(func() error { return entity.recoverFromStore(ctx.Context()) }).
		Run(); err != nil {
		return err
	}

	entity.metrics.EntityStarted(ctx.Context())

	return nil
}

// bindIdentity makes the behavior's ID the persistence ID, and in a
// tenant-aware engine proves, before any store read, that this actor's name is
// the one its tenant and that ID derive (EGO-TENANT-009). The actor's name is
// only its address: in a multi-tenant engine it is qualified with the tenant,
// and the records keep the ID the behavior declares.
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

// Receive processes any message dropped into the actor mailbox.
func (entity *Actor) Receive(ctx *goakt.ReceiveContext) {
	switch message := ctx.Message().(type) {
	case *goakt.PostStart:
		entity.actorSystem = ctx.ActorSystem()
	case *egopb.GetStateCommand:
		entity.getStateAndReply(ctx)
	case *egopb.TenantBindingQuery:
		ctx.Response(protocol.AnswerTenantBinding(entity.tenantAware, entity.scope, message))
	default:
		msg := message.(Command)
		entity.processCommand(ctx, msg)
	}
}

// PostStop prepares the actor to gracefully shutdown.
//
// T4-B exclusion (design.md D4): this lifecycle flush deliberately carries
// no verifyTenantForPersist gate. It persists whatever state the actor
// already holds in memory — state that only ever landed there via
// commitState's single commit point. There is no new tenant identity to
// re-confirm here, and ctx.Context() at shutdown is not the per-command
// context T4-B reasons about; adding a gate here would just fail closed on a
// shutdown path for no defensive benefit.
//
// Version-gated skip (DS3, EGO-TENANT-002 PR2, PR2 review round 2 P1 fix):
// currentVersion is the durable discriminant for "has this actor ever
// committed a command" — commitState only ever advances it together with
// actorTenant, in the same in-memory assignment, so checking
// currentVersion > 0 here is equivalent to checking actorTenant was
// committed, without relying on actorTenant's zero value as a stand-in for
// that fact. An actor that starts at genesis and receives no command is
// exactly the actor still at version 0. Flushing that state would persist a
// DurableState whose tenant_metadata is an empty map, which recoverFromStore
// then refuses to recover from — bricking the persistence ID permanently.
// Skipping the flush here loses nothing (in-memory state is still exactly
// InitialState() at version 0) and keeps fail-closed in the strong
// direction: no tenant-less record is ever written. Legacy mode is
// unaffected — it keeps today's unconditional flush.
func (entity *Actor) PostStop(ctx *goakt.Context) error {
	entity.metrics.EntityStopped(ctx.Context())
	chain := runner.
		New(runner.WithFailFast()).
		AddRunner(func() error { return entity.stateStore.Ping(ctx.Context()) })
	if !entity.tenantAware || entity.currentVersion > 0 {
		chain = chain.AddRunner(func() error { return entity.persistStateAndPublish(ctx.Context()) })
	}
	return chain.Run()
}

// recoverFromStore reset the persistent actor to the latest state in case there is one
// this is vital when the entity actor is restarting.
func (entity *Actor) recoverFromStore(ctx context.Context) error {
	durableState, err := entity.stateStore.GetLatestState(ctx, entity.scope, entity.persistenceID)
	if err != nil {
		return fmt.Errorf("failed to get the latest state: %w", err)
	}

	// Genesis: no record at all. Nothing to recover.
	if durableState == nil {
		entity.currentState = entity.behavior.InitialState()
		return nil
	}

	// Legacy genesis (DS2, EGO-TENANT-002 PR2 review round 2 P1 fix): in
	// tenant-aware mode, a record at version 0 carries no committed tenant to
	// recover and none to enforce. commitState only ever advances the version
	// together with a committed tenant, and checkPreconditions only ever
	// admits a version exactly 1 above the prior one, so a durable record can
	// only be at version 0 by way of a legacy installation's PostStop, which
	// used to flush InitialState() unconditionally even when the actor never
	// received a command. Treat it exactly like no record at all, discarding
	// its payload and any tenant_metadata rather than attempting to unmarshal
	// either. version > 0 is never legacy-genesis and always requires valid
	// tenant metadata below. Non-tenant-aware installations never carried
	// this ambiguity: they keep validating whatever payload is on record
	// regardless of version, unchanged from before this fix.
	if entity.tenantAware && durableState.GetVersionNumber() == 0 {
		entity.currentState = entity.behavior.InitialState()
		return nil
	}

	// Cross-check this actor's lifetime tenant identity against the
	// recovered record's carried metadata (DS2, EGO-TENANT-002 PR2;
	// strengthened by TENANT-003 T4). entity.actorTenant is already
	// pre-seeded from the spawn-bound tenant by resolveScope, before
	// recoverFromStore ever runs, so recovered tenant_metadata is no longer
	// the SOURCE of actorTenant — it is verified against the spawn-bound
	// value via tenancy.VerifyUnchanged, and a mismatch fails closed. Absent
	// or malformed metadata on a committed (version > 0) tenant-aware record
	// is not tolerated: it means this record predates tenancy or was
	// corrupted, and the actor must refuse to start rather than silently run
	// without an identity (fail-closed, UnmarshalMetadata's own ErrInvalid
	// is the rejection).
	if entity.tenantAware {
		tc, err := tenancy.UnmarshalMetadata(tenancy.Metadata(durableState.GetTenantMetadata()))
		if err != nil {
			return fmt.Errorf("failed to unmarshal durable state tenant metadata: %w", err)
		}
		if entity.actorTenant != noTenantContext {
			if verifyErr := tenancy.VerifyUnchanged(entity.actorTenant, tc); verifyErr != nil {
				return fmt.Errorf("recovered tenant metadata does not match the spawn-bound tenant: %w", verifyErr)
			}
		} else {
			entity.actorTenant = tc
		}
	}

	currentState := entity.behavior.InitialState()
	if resultingState := durableState.GetResultingState(); resultingState != nil {
		if err := resultingState.UnmarshalTo(currentState); err != nil {
			return fmt.Errorf("failed to unmarshal the latest state: %w", err)
		}
	}

	entity.currentState = currentState
	entity.currentVersion = durableState.GetVersionNumber()
	return nil
}

// processCommand processes the incoming command
func (entity *Actor) processCommand(receiveContext *goakt.ReceiveContext, command Command) {
	ctx := receiveContext.Context()
	startTime := time.Now()

	if entity.tracer != nil {
		var span trace.Span
		ctx, span = instrumentation.StartCommandSpan(ctx, entity.tracer, entity.persistenceID, command)
		defer span.End()
	}

	if entity.metrics != nil {
		entity.metrics.CommandReceived(ctx)
		defer func() { entity.metrics.CommandCompleted(ctx, startTime) }()
	}

	// Pre-handler gate (T4-A): in tenant-aware mode, HandleCommand must never
	// run without a TenantContext already attached by Engine.SendCommand.
	// This reuses tenancy.Require, a read-only check of the context already
	// in hand; it never calls a resolver and never re-resolves.
	//
	// candidateTenant is deliberately NOT written into entity.actorTenant
	// here (PR2 review round 2 P1 fix): a command that fails HandleCommand
	// or checkPreconditions below must never appropriate the actor for a
	// tenant that accepted no mutation. It is carried through to commitState,
	// the single point — immediately after WriteState confirms the durable
	// write, before Publish — where committed state, committed version, and
	// owning tenant land together.
	//
	// Cross-tenant extension (DS1, EGO-TENANT-002 PR2): once actorTenant is
	// seeded (DS2, recovery) or committed (a prior successful command on this
	// actor), a command resolving to a different tenant is rejected here —
	// before HandleCommand runs and before entity.currentState/currentVersion
	// are ever mutated, which is exactly why this identity check cannot live
	// in commitState instead: by that point the foreign tenant's state would
	// already be resident in memory.
	var candidateTenant tenancy.TenantContext
	if entity.tenantAware {
		tc, err := tenancy.Require(ctx)
		if err != nil {
			entity.sendErrorReply(receiveContext, err)
			return
		}
		if entity.actorTenant != noTenantContext {
			if verifyErr := tenancy.VerifyUnchanged(entity.actorTenant, tc); verifyErr != nil {
				entity.sendErrorReply(receiveContext, verifyErr)
				return
			}
		}
		candidateTenant = tc
	}

	// Deadline pre-handler gate: ctx carries the effective deadline
	// Engine.Dispatch computed (min of ctx's own deadline, the envelope
	// Metadata's deadline, and the caller's timeout). If it has already
	// expired or been canceled, fail closed before the handler runs — the
	// handler must never execute past the deadline.
	if err := protocol.CheckDeadline(ctx, "before handler execution"); err != nil {
		entity.sendErrorReply(receiveContext, err)
		return
	}

	newState, newVersion, err := entity.dispatchToBehavior(ctx, command, entity.currentVersion, entity.currentState)
	if err != nil {
		entity.sendErrorReply(receiveContext, err)
		return
	}

	// check whether the pre-conditions have met
	if err := entity.checkPreconditions(newState, newVersion); err != nil {
		entity.sendErrorReply(receiveContext, err)
		return
	}

	// Deadline post-handler/pre-persist gate: the handler above may have run
	// long enough for the deadline to expire while it was in flight —
	// context.WithDeadline does not preempt a handler that ignores its
	// context, so this re-check is the actual barrier that stops a late
	// handler output from ever reaching commitState. newState/newVersion are
	// discarded here, never applied to entity.currentState/currentVersion or
	// persisted: commitState (DS1/DS-DUR, #77) is the only place those fields
	// are mutated, and only after WriteState confirms the durable write, so
	// there is nothing to unwind on this gate's error path.
	if err := protocol.CheckDeadline(ctx, "before persistence"); err != nil {
		entity.sendErrorReply(receiveContext, err)
		return
	}

	// Defensive persistence invariant (T4-B, design.md D4): re-confirm a
	// valid tenant identity is still present before this state is committed.
	// This reuses tenancy.Require — a read-only check of the context already
	// validated by the pre-handler gate above — and never re-invokes
	// TenantResolver.Resolve. It is deliberately not the sole enforcement
	// point: T4-A above already blocks HandleCommand itself.
	if err := entity.verifyTenantForPersist(ctx); err != nil {
		entity.sendErrorReply(receiveContext, err)
		return
	}

	// ExpectedRevision (CONTRACT-EXPECTED-REVISION-v1) is read from command
	// metadata here, immediately before the conditional write, and only
	// travels to commitState/WriteState — it never reached dispatchToBehavior
	// above and never mutated entity.currentVersion/entity.currentState
	// (spec: "ExpectedRevision Is Read as a Write Precondition, Not Domain
	// Input"). Mirrors EventSourcedActor.processCommandAndReply's equivalent
	// extraction point (design.md D4/D8).
	revision, hasRevision := protocol.ExpectedRevisionFromContext(ctx)
	precondition := protocol.PreconditionFromRevision(revision, hasRevision)

	if err := entity.commitState(ctx, newState, newVersion, time.Now(), candidateTenant, precondition); err != nil {
		// D10 for Actor (no shutdown/restart path, unlike
		// EventSourcedActor): re-run recovery in place, best-effort, rather
		// than tearing the actor down, whenever this actor cannot prove its
		// in-memory currentVersion is still in sync with StorageRevision.
		//
		// Run BEFORE sendErrorReply, not after: sendErrorReply completes the
		// ask/reply protocol and unblocks the caller while this goroutine's
		// mailbox turn is still technically in flight, so any in-memory
		// mutation performed after it races a caller-triggered shutdown
		// (observed via -race). Recovering first means the actor is fully
		// settled in memory by the time the caller can act on the reply.
		entity.recoverFromConflictIfNeeded(ctx, err)
		entity.sendErrorReply(receiveContext, err)
		return
	}

	entity.sendStateReply(receiveContext)
}

// recoverFromConflictIfNeeded implements D10's post-conflict handling for
// Actor. EventSourcedActor's equivalent (shouldStayAliveAfterConflict)
// shuts itself down whenever it is not provably in sync, letting the
// supervisor restart it into recover(); Actor has no such
// shutdown/restart path, so it re-runs recoverFromStore in place instead,
// best-effort, under the same "not provably in sync" condition.
//
// Called before sendErrorReply (see processCommand) so the in-memory
// recovery is fully settled before the caller can observe the reply and act
// on it (e.g. trigger a shutdown) — see the -race note there.
//
// A recoverFromStore failure here is deliberately swallowed (best-effort,
// matching this file's other _ = call sites): the failed command has
// already been replied to, and the next command's own recovery attempt (or
// its own conflict) will retry the same corrective read.
func (entity *Actor) recoverFromConflictIfNeeded(ctx context.Context, err error) {
	if entity.provablyInSyncAfterConflict(err) {
		return
	}
	_ = entity.recoverFromStore(ctx)
}

// provablyInSyncAfterConflict mirrors EventSourcedActor.shouldStayAliveAfterConflict's
// structure: it reports whether err is a *persistence.ConflictError whose
// reported ActualRevision is known and equal to entity.currentVersion — the
// only case where entity.currentVersion is already known to match
// StorageRevision without a corrective read.
func (entity *Actor) provablyInSyncAfterConflict(err error) bool {
	var conflictErr *persistence.ConflictError
	if !errors.As(err, &conflictErr) {
		return false
	}
	actual, ok := conflictErr.ActualRevision()
	return ok && actual == entity.currentVersion
}

// dispatchToBehavior invokes entity.behavior against cmd, preferring
// HandleEnvelope over HandleCommand when both entity.behavior implements
// behaviorport.DurableStateEnvelope (DurableStateEnvelopeBehavior is one)
// and a command.Metadata is available on ctx (#60, M-3). See
// EventSourcedActor.dispatchToBehavior for the full rationale — this
// mirrors it for the durable-state path.
func (entity *Actor) dispatchToBehavior(ctx context.Context, cmd Command, priorVersion uint64, priorState State) (State, uint64, error) {
	envBehavior, ok := entity.behavior.(behaviorport.DurableStateEnvelope)
	if !ok {
		return entity.behavior.HandleCommand(ctx, cmd, priorVersion, priorState)
	}
	md, ok := protocol.MetadataFromContext(ctx)
	if !ok {
		return entity.behavior.HandleCommand(ctx, cmd, priorVersion, priorState)
	}
	env, err := command.NewEnvelope(cmd, md)
	if err != nil {
		return entity.behavior.HandleCommand(ctx, cmd, priorVersion, priorState)
	}
	return envBehavior.HandleEnvelope(ctx, env, priorVersion, priorState)
}

// currentStateAny returns the cached anypb.Any of currentState, computing it
// only when the state has changed since the last call.
func (entity *Actor) currentStateAny() *anypb.Any {
	if entity.cachedStateAny == nil {
		entity.cachedStateAny, _ = anypb.New(entity.currentState)
	}
	return entity.cachedStateAny
}

// getStateAndReply returns the last committed state of the entity without
// processing any command. Mirrors EventSourcedActor.getStateAndReply's gate
// (DS4, EGO-TENANT-002 PR2): Receive dispatches *egopb.GetStateCommand here
// directly, bypassing processCommand and its T4-A gate entirely, so without
// its own check any resolved tenant could read another tenant's full
// committed durable state.
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

// sendStateReply sends a state reply message
func (entity *Actor) sendStateReply(ctx *goakt.ReceiveContext) {
	ctx.Response(&egopb.CommandReply{
		Reply: &egopb.CommandReply_StateReply{
			StateReply: &egopb.StateReply{
				PersistenceId:  entity.persistenceID,
				State:          entity.currentStateAny(),
				SequenceNumber: entity.currentVersion,
				Timestamp:      entity.lastCommandTime.UnixNano(),
			},
		},
	})
}

// sendErrorReply sends an error as a reply message
func (entity *Actor) sendErrorReply(ctx *goakt.ReceiveContext, err error) {
	ctx.Response(&egopb.CommandReply{
		Reply: &egopb.CommandReply_ErrorReply{
			ErrorReply: &egopb.ErrorReply{
				Message: err.Error(),
			},
		},
	})
}

// checkAndSetPreconditions validates the newState and the newVersion
func (entity *Actor) checkPreconditions(newState State, newVersion uint64) error {
	currentState := entity.currentState
	currentStateType := currentState.ProtoReflect().Descriptor().FullName()
	latestStateType := newState.ProtoReflect().Descriptor().FullName()
	if currentStateType != latestStateType {
		return fmt.Errorf("mismatch state types: %s != %s", currentStateType, latestStateType)
	}

	proceed := int(math.Abs(float64(newVersion-entity.currentVersion))) == 1
	if !proceed {
		return fmt.Errorf("%s received version=(%d) while current version is (%d)",
			entity.persistenceID,
			newVersion,
			entity.currentVersion)
	}
	return nil
}

// checks whether the durable state store is set or not
func (entity *Actor) durableStateRequired() error {
	if entity.stateStore == nil {
		return runtimeport.ErrDurableStateStoreRequired
	}
	return nil
}

// resolveScope binds entity.scope (and, in tenant-aware mode,
// entity.actorTenant) from deps before any store read (TENANT-003 T4),
// mirroring EventSourcedActor.resolveScope.
//
// tenantAware == false binds persistence.Unscoped() and leaves actorTenant
// untouched (noTenantContext) — legacy mode is byte-identical to before
// this field existed.
//
// tenantAware == true looks for the per-spawn extensions.EntityTenantScope
// dependency Engine.DurableStateEntity injects and fails closed with
// extensions.ErrEntityTenantScopeMissing when it is absent or carries an invalid
// tenant id. On success it also pre-seeds entity.actorTenant with the
// corresponding tenancy.TenantContext, BEFORE recoverFromStore() runs. This
// turns recoverFromStore's tenant handling from a first-seed (direct
// assignment) into a cross-check against the spawn-bound tenant (DS2/D6):
// recovered tenant_metadata that disagrees with the tenant this actor was
// actually spawned for now fails closed via tenancy.VerifyUnchanged.
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

// verifyTenantForPersist re-confirms, from ctx alone, that a valid tenant
// identity is present before this command's state is committed (T4-B,
// design.md D4). It is a no-op in legacy mode (tenantAware == false) and,
// in tenant-aware mode, does nothing but read the context already attached
// by Engine.SendCommand and validated by the pre-handler gate: it never
// invokes a TenantResolver and is never the sole enforcement point for
// fail-closed behavior.
func (entity *Actor) verifyTenantForPersist(ctx context.Context) error {
	if !entity.tenantAware {
		return nil
	}
	_, err := tenancy.Require(ctx)
	return err
}

// commitState is the single logical commit point (PR2 review round 2 P1
// fix): committed state, committed version, and owning tenant land together,
// in that order, only once WriteState confirms the durable write succeeded.
//
// It never reads entity.currentState/currentVersion/actorTenant to build the
// record — newState, newVersion, and candidateTenant are the record, passed
// in explicitly by processCommand once HandleCommand and checkPreconditions
// have both already succeeded — so a WriteState failure here leaves entity
// exactly as it was before the command: no state mutated, no tenant
// appropriated.
//
// The in-memory commit (currentState/cachedStateAny/currentVersion/
// actorTenant) happens immediately after WriteState succeeds and before
// Publish, so a Publish failure can never un-appropriate what the store
// already durably committed.
//
// precondition is the caller's ExpectedRevision (CONTRACT-EXPECTED-REVISION-v1),
// already translated to a persistence.WritePrecondition by processCommand,
// and is the sole authority WriteState's conditional write is decided
// against — never entity.currentVersion, which may be stale relative to the
// store's own StorageRevision (spec: "Conditional Write Delegates the
// Compare-and-Commit to Persistence"). On a *persistence.ConflictError from
// WriteState, this returns before any of the in-memory mutation below or the
// Publish call runs, so a rejected write leaves currentState/currentVersion/
// actorTenant/cachedStateAny exactly as they were (spec: "No partial commit
// on conflict").
func (entity *Actor) commitState(ctx context.Context, newState State, newVersion uint64, commandTime time.Time, candidateTenant tenancy.TenantContext, precondition persistence.WritePrecondition) error {
	newStateAny, err := anypb.New(newState)
	if err != nil {
		return err
	}

	durableState := &egopb.DurableState{
		PersistenceId:  entity.persistenceID,
		VersionNumber:  newVersion,
		ResultingState: newStateAny,
		Timestamp:      commandTime.UnixNano(),
		Shard:          entity.shardNumber,
	}
	if entity.tenantAware {
		durableState.TenantMetadata = tenancy.MarshalMetadata(candidateTenant)
	}

	if err := entity.stateStore.WriteState(ctx, entity.scope, durableState, precondition); err != nil {
		return err
	}

	entity.currentState = newState
	entity.cachedStateAny = newStateAny
	entity.lastCommandTime = commandTime
	entity.currentVersion = newVersion
	if entity.tenantAware {
		entity.actorTenant = candidateTenant
	}

	entity.eventsStream.Publish(protocol.StatesTopic, durableState)
	return nil
}

// persistStateAndPublish flushes the actor's already-committed in-memory
// state to the store. Used only by PostStop's lifecycle flush (T4-B
// exclusion, design.md D4): there is no new candidate tenant to commit here,
// only whatever state/version/tenant a prior successful command already
// committed via commitState, or, in legacy mode, whatever is in memory.
func (entity *Actor) persistStateAndPublish(ctx context.Context) error {
	durableState := &egopb.DurableState{
		PersistenceId:  entity.persistenceID,
		VersionNumber:  entity.currentVersion,
		ResultingState: entity.currentStateAny(),
		Timestamp:      entity.lastCommandTime.UnixNano(),
		Shard:          entity.shardNumber,
	}

	// DS3 (EGO-TENANT-002 PR2): write from entity.actorTenant, never from
	// ctx — PostStop's ctx.Context() is a shutdown context that never
	// carried a TenantContext, and actorTenant is already committed by the
	// time PostStop reaches here (commitState, or legacy mode's unconditional
	// flush).
	if entity.tenantAware {
		durableState.TenantMetadata = tenancy.MarshalMetadata(entity.actorTenant)
	}

	if err := entity.stateStore.WriteState(ctx, entity.scope, durableState, persistence.Unconditional()); err != nil {
		return err
	}

	entity.eventsStream.Publish(protocol.StatesTopic, durableState)
	return nil
}

// noTenantContext is the zero value of tenancy.TenantContext. Neither
// tenancy.NewTenantContext nor tenancy.NewAdministrativeContext can ever
// produce it (tenancy/tenant_context.go), so it safely marks "not yet
// seeded" for an actor's tenant, distinct from any real resolved identity.
var noTenantContext tenancy.TenantContext
