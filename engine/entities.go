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

package engine

import (
	"context"
	"fmt"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"

	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/persistence"
	behaviorport "github.com/getsyntegrity/urd/port/behavior"
	"github.com/getsyntegrity/urd/tenancy"
)

// Entity creates an event-sourced entity that persists its state by storing a history of events.
//
// This entity follows the event sourcing pattern, where changes to its state are driven by events rather than direct mutations.
// It processes incoming commands, validates them, generates corresponding events upon successful validation, and persists those
// events before applying them to update its state.
//
// Key Behavior:
//   - Commands: The entity receives commands (non-persistent messages) that are validated before being processed.
//   - Validation: This can range from simple field checks to interactions with external services.
//   - Events: If validation succeeds, events are derived from the command, persisted in the event store, and then used to update the entity’s state.
//   - Recovery: During recovery, only persisted events are replayed to rebuild the entity’s state, ensuring deterministic behavior.
//   - Command vs. Event: Commands may be rejected if they are invalid, while events—once persisted—cannot fail during replay.
//
// If no new events are generated, the entity simply returns its current state.
// To interact with the entity, use SendCommand to send commands to a durable event-sourced entity.
//
// Parameters:
//   - ctx: Execution context for controlling the lifecycle of the entity.
//   - behavior: Defines the entity’s event-sourced behavior, including command handling and state transitions.
//   - opts: Additional spawning options to configure entity behavior.
//
// Returns an error if the entity fails to initialize or encounters an issue during execution.
// In cluster mode a behavior that GoAkt cannot serialize is rejected before
// anything is spawned, with a *BehaviorPlacementError.
// Returns ErrEventsStoreRequired, before anything is spawned, when the Config
// has no events store.
//
// Deprecated: use [Engine.SpawnEventSourced]. Removed in the next major
// release (#124).
func (engine *Engine) Entity(ctx context.Context, behavior EventSourcedBehavior, opts ...SpawnOption) error {
	return engine.spawnEventSourced(ctx, behavior, opts...)
}

// spawnEventSourced spawns an event-sourced entity for behavior. It is the
// single spawn path behind Entity; guards that every event-sourced spawn
// must enforce belong here.
func (engine *Engine) spawnEventSourced(ctx context.Context, behavior behaviorport.EventSourced, opts ...SpawnOption) error {
	if !engine.Started() {
		return ErrEngineNotStarted
	}

	if err := engine.requireFamily(EventSourcedFamily); err != nil {
		return err
	}

	ref := engine.actorSystem.Load()
	if ref == nil {
		return ErrEngineNotStarted
	}
	actorSystem := ref.sys

	if !engine.hasEventsStore() {
		return ErrEventsStoreRequired
	}

	// Decide what carries the behavior to GoAkt, and reject in cluster mode
	// a behavior GoAkt cannot serialize, before anything is spawned.
	behaviorDep, placementErr := spawnDependency(actorSystem, behavior)
	if placementErr != nil {
		return placementErr
	}

	config := newSpawnConfig(opts...)

	// Tenant-aware mode: determine which tenant this entity belongs to and
	// inject it as a per-spawn dependency the entity's PreStart binds into
	// its persistence.Scope before it ever reads a store (TENANT-003 T4).
	// The engine never calls TenantResolver.Resolve here — see
	// spawnTenantScope's doc comment for why, and where Resolve is actually
	// called instead. Legacy mode (no resolver registered) is byte-identical:
	// nothing is injected.
	tenantScope, tenantErr := engine.spawnTenantScope(config)
	if tenantErr != nil {
		return tenantErr
	}

	sOptions := buildSpawnOptionsFromConfig(config)

	entityConfig := extensions.NewEntityConfig(config.snapshotInterval)
	if config.retentionPolicy != nil {
		entityConfig.HasRetentionPolicy = true
		entityConfig.DeleteEventsOnSnapshot = config.retentionPolicy.DeleteEventsOnSnapshot
		entityConfig.DeleteSnapshotsOnSnapshot = config.retentionPolicy.DeleteSnapshotsOnSnapshot
		entityConfig.EventsRetentionCount = config.retentionPolicy.EventsRetentionCount
	}

	entityConfig.BatchThreshold = config.batchThreshold
	entityConfig.BatchFlushWindow = config.batchFlushWindow
	_ = actorSystem.Inject(entityConfig)

	deps := []extension.Dependency{behaviorDep, entityConfig}
	if tenantScope != nil {
		_ = actorSystem.Inject(tenantScope)
		deps = append(deps, tenantScope)
	}
	sOptions = append(sOptions, goakt.WithDependencies(deps...))

	// The actor is addressed by (tenant, ID), never by the bare ID, so two
	// tenants that share an ID spawn two actors (EGO-TENANT-009).
	actorName, nameErr := engine.spawnActorName(tenantScope, behavior.ID())
	if nameErr != nil {
		return nameErr
	}

	pid, err := actorSystem.SpawnOn(ctx, actorName, new(EventSourcedActor), sOptions...)
	if err != nil {
		return resolveExistingSpawn(ctx, actorSystem, actorName, tenantScope, err)
	}
	return verifySpawnedTenant(ctx, pid, tenantScope)
}

// requireFamily returns an error wrapping ErrEntityFamilyNotDeclared, naming
// family, when the engine declares its entity families (WithEntityFamilies)
// and family is not among them. It is called by the three unexported spawn
// functions, so the deprecated and the runtime-neutral entry points share
// one copy of the guard (ego-arch-003 design §D3).
func (engine *Engine) requireFamily(family EntityFamily) error {
	if engine.entityFamilies == 0 || engine.entityFamilies&family != 0 {
		return nil
	}
	return fmt.Errorf("%w: %s (declared: %s)", ErrEntityFamilyNotDeclared, family, engine.entityFamilies)
}

// hasEventsStore reports whether the engine was configured with an events
// store. Event-sourced entities and sagas persist their events there, and
// their actors would otherwise call it through a nil interface at PreStart.
func (engine *Engine) hasEventsStore() bool {
	engine.mutex.RLock()
	defer engine.mutex.RUnlock()
	return engine.eventsStore != nil
}

// EntityExists reports whether an entity with the given ID is currently alive in the cluster.
//
// The lookup works for both event-sourced and durable state entities, and is purely
// a liveness probe: it does not spawn the entity, recover its state, or replay its
// journal. As a result, an entity that has been persisted but is not currently
// hydrated (for example, one that has been passivated or has not yet received its
// first command after process restart) will report as non-existent.
//
// The method returns:
//   - (true, nil)  if the entity is registered and its underlying actor is running.
//   - (false, nil) if no actor for the given ID is found, or if the actor exists
//     but is no longer running (e.g. stopping or stopped).
//   - (false, ErrEngineNotStarted) if the engine has not been started.
//   - (false, error) if the underlying actor system lookup fails for any other reason.
//
// EntityExists is safe to call concurrently and is intended for callers that need
// to branch on entity presence without incurring the cost or side effects of
// materializing the entity.
func (engine *Engine) EntityExists(ctx context.Context, entityID string) (bool, error) {
	if !engine.Started() {
		return false, ErrEngineNotStarted
	}

	ref := engine.actorSystem.Load()
	if ref == nil {
		return false, ErrEngineNotStarted
	}
	name, err := engine.lookupActorName(ctx, entityID)
	if err != nil {
		return false, err
	}
	exists, err := ref.sys.ActorExists(ctx, name)
	if err != nil {
		return false, fmt.Errorf("failed to check existence of entity %s: %w", entityID, err)
	}
	return exists, nil
}

// DurableStateEntity creates an entity that persists its full state in a durable store without maintaining historical event records.
//
// Unlike an event-sourced entity, a durable state entity does not track past state changes as a sequence of events. Instead, it
// directly stores and updates its current state in a durable store.
//
// Key Behavior:
//   - Commands: The entity receives non-persistent commands that are validated before being processed.
//   - Validation: This can range from simple field checks to complex interactions with external services.
//   - State Updates: If validation succeeds, a new state is derived from the command, persisted, and then applied to update the entity’s state.
//   - Persistence: The latest state is always stored, ensuring that only the most recent version is retained.
//   - Recovery: Upon restart, the entity reloads its last persisted state, rather than replaying a sequence of past events.
//   - Shutdown Handling: During a normal shutdown, the entity ensures that its current state is persisted before termination.
//
// To interact with the entity, use SendCommand to send commands and update the durable state.
//
// Parameters:
//   - ctx: Execution context for controlling the entity’s lifecycle.
//   - behavior: Defines the entity’s behavior, including command handling and state transitions.
//   - opts: Additional spawning options to configure entity behavior.
//
// Returns an error if the entity fails to initialize or encounters an issue during execution.
// In cluster mode a behavior that GoAkt cannot serialize is rejected before
// anything is spawned, with a *BehaviorPlacementError.
//
// Deprecated: use [Engine.SpawnDurableState]. Removed in the next major
// release (#124).
func (engine *Engine) DurableStateEntity(ctx context.Context, behavior DurableStateBehavior, opts ...SpawnOption) error {
	return engine.spawnDurableState(ctx, behavior, opts...)
}

// spawnDurableState spawns a durable-state entity for behavior. It is the
// single spawn path behind DurableStateEntity; guards that every
// durable-state spawn must enforce belong here.
func (engine *Engine) spawnDurableState(ctx context.Context, behavior behaviorport.DurableState, opts ...SpawnOption) error {
	if !engine.Started() {
		return ErrEngineNotStarted
	}

	if err := engine.requireFamily(DurableStateFamily); err != nil {
		return err
	}

	ref := engine.actorSystem.Load()
	if ref == nil {
		return ErrEngineNotStarted
	}
	actorSystem := ref.sys

	engine.mutex.RLock()
	durableStateStore := engine.stateStore
	engine.mutex.RUnlock()

	if durableStateStore == nil {
		return ErrDurableStateStoreRequired
	}

	// Decide what carries the behavior to GoAkt, and reject in cluster mode
	// a behavior GoAkt cannot serialize, before anything is spawned.
	behaviorDep, placementErr := spawnDependency(actorSystem, behavior)
	if placementErr != nil {
		return placementErr
	}

	config := newSpawnConfig(opts...)

	// Tenant-aware mode: determine which tenant this entity belongs to and
	// inject it as a per-spawn dependency, mirroring Entity (TENANT-003 T4).
	// See spawnTenantScope's doc comment for the fail-closed rules (no
	// engine.WithTenant and no resolver-exposed fixed tenant).
	tenantScope, tenantErr := engine.spawnTenantScope(config)
	if tenantErr != nil {
		return tenantErr
	}

	sOptions := buildSpawnOptionsFromConfig(config)
	deps := []extension.Dependency{behaviorDep}
	if tenantScope != nil {
		_ = actorSystem.Inject(tenantScope)
		deps = append(deps, tenantScope)
	}
	sOptions = append(sOptions, goakt.WithDependencies(deps...))

	// The actor is addressed by (tenant, ID), never by the bare ID, so two
	// tenants that share an ID spawn two actors (EGO-TENANT-009).
	actorName, nameErr := engine.spawnActorName(tenantScope, behavior.ID())
	if nameErr != nil {
		return nameErr
	}

	pid, err := actorSystem.SpawnOn(ctx, actorName, new(DurableStateActor), sOptions...)
	if err != nil {
		return resolveExistingSpawn(ctx, actorSystem, actorName, tenantScope, err)
	}
	return verifySpawnedTenant(ctx, pid, tenantScope)
}

// EraseEntity performs GDPR erasure for the given persistence ID.
// When an encryptor backed by a KeyStore is configured, this deletes the encryption
// key (crypto-shredding), making all encrypted events and snapshots irrecoverable.
// Optionally, it also physically deletes events and snapshots from the stores.
func (engine *Engine) EraseEntity(ctx context.Context, persistenceID string, full bool) error {
	if !engine.Started() {
		return ErrEngineNotStarted
	}

	engine.mutex.RLock()
	eventsStore := engine.eventsStore
	snapshotStore := engine.snapshotStore
	engine.mutex.RUnlock()

	// Tenant-aware mode: resolve the caller's tenant identity and scope the
	// erasure to it (TENANT-003 T4). Before this fix, EraseEntity bypassed
	// every actor and called the stores with persistence.Unscoped()
	// unconditionally, so any caller who knew a persistenceID could erase
	// ANY tenant's events and snapshots regardless of who they were
	// resolved to be — the exact isolation hole this ticket closes. A
	// resolver error, or a resolved TenantContext that carries no tenant
	// identity (administrative scope, or an invalid/zero-value context),
	// fails the erasure closed rather than silently falling back to
	// Unscoped(). Legacy mode (no resolver registered) is unchanged: it
	// still erases Unscoped() records, exactly as before.
	scope := persistence.Unscoped()
	if engine.tenantResolver != nil {
		tenantContext, resolveErr := engine.tenantResolver.Resolve(ctx)
		if resolveErr != nil {
			return fmt.Errorf("failed to resolve tenant for erasure: %w", resolveErr)
		}

		tenantID, ok := tenantContext.Tenant()
		if !ok {
			return fmt.Errorf("failed to erase entity: erasure requires a tenant-scoped identity, got %s: %w",
				tenantContext.Scope(), tenancy.ErrDenied)
		}

		var scopeErr error
		scope, scopeErr = persistence.NewTenantScope(tenantID)
		if scopeErr != nil {
			return fmt.Errorf("failed to build erasure scope: %w", scopeErr)
		}
	}

	if full {
		// Get the latest event to find the max sequence number
		latestEvent, err := eventsStore.GetLatestEvent(ctx, scope, persistenceID)
		if err != nil {
			return fmt.Errorf("failed to get latest event for erasure: %w", err)
		}
		if latestEvent != nil {
			if err := eventsStore.DeleteEvents(ctx, scope, persistenceID, latestEvent.GetSequenceNumber()); err != nil {
				return fmt.Errorf("failed to delete events for erasure: %w", err)
			}
		}
		if snapshotStore != nil && latestEvent != nil {
			if err := snapshotStore.DeleteSnapshots(ctx, scope, persistenceID, latestEvent.GetSequenceNumber()); err != nil {
				return fmt.Errorf("failed to delete snapshots for erasure: %w", err)
			}
		}
	}

	return nil
}
