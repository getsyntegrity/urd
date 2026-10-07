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
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/engine/saga"
	"github.com/getsyntegrity/urd/internal/extensions"
	behaviorport "github.com/getsyntegrity/urd/port/behavior"
	"github.com/getsyntegrity/urd/tenancy"
)

// Saga creates a saga/process manager that coordinates multiple entities.
//
// A saga is a long-running business process that reacts to events from the event
// stream, sends commands to entities, persists its own events for recovery, and
// supports compensation logic for rollback on failures.
//
// Parameters:
//   - ctx: Execution context for controlling the saga lifecycle.
//   - behavior: Defines the saga's logic including event handling, command dispatch, and compensation.
//   - timeout: Maximum duration for the saga. Zero means no timeout.
//   - opts: Additional spawning options. In tenant-aware mode, engine.WithTenant
//     declares which tenant this saga belongs to (TENANT-003 T4); every
//     other SpawnOption is not applicable to a saga spawn and is ignored, a
//     saga already fixes its own supervision/placement/relocation behavior.
//
// Returns an error if the saga fails to initialize. In cluster mode a
// behavior that GoAkt cannot serialize is rejected before anything is
// spawned, with a *BehaviorPlacementError.
// Returns ErrEventsStoreRequired, before anything is spawned, when the Config
// has no events store.
//
// Deprecated: use [Engine.SpawnSaga]. Removed in the next major release
// (#124).
func (engine *Engine) Saga(ctx context.Context, behavior SagaBehavior, timeout time.Duration, opts ...SpawnOption) error {
	return engine.spawnSaga(ctx, behavior, timeout, opts...)
}

// spawnSaga spawns a saga for behavior. It is the single spawn path behind
// Saga; guards that every saga spawn must enforce belong here.
func (engine *Engine) spawnSaga(ctx context.Context, behavior behaviorport.Saga, timeout time.Duration, opts ...SpawnOption) error {
	if !engine.Started() {
		return ErrEngineNotStarted
	}

	if err := engine.requireFamily(SagaFamily); err != nil {
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
	// Relocation to a peer requires the type to be registered there via
	// WithEntityKinds.
	behaviorDep, placementErr := spawnDependency(actorSystem, behavior)
	if placementErr != nil {
		return placementErr
	}

	config := newSpawnConfig(opts...)

	// Tenant-aware mode: determine which tenant this saga belongs to and
	// inject it as a per-spawn dependency, mirroring Entity/
	// DurableStateEntity (TENANT-003 T4).
	tenantScope, tenantErr := engine.spawnTenantScope(config)
	if tenantErr != nil {
		return tenantErr
	}

	sagaCfg := extensions.NewSagaConfig(timeout)
	_ = actorSystem.Inject(sagaCfg)
	actor := newSagaActor()

	deps := []extension.Dependency{behaviorDep, sagaCfg, &extensions.ActorNamespace{Namespace: engine.actorNamespace}}
	if tenantScope != nil {
		_ = actorSystem.Inject(tenantScope)
		deps = append(deps, tenantScope)
	}

	actorName, nameErr := engine.spawnActorName(tenantScope, behavior.ID())
	if nameErr != nil {
		return nameErr
	}

	pid, err := actorSystem.Spawn(ctx, actorName,
		actor,
		goakt.WithLongLived(),
		goakt.WithDependencies(deps...),
		goakt.WithSupervisor(newSupervisor(RestartDirective)))
	if err != nil {
		if resolved := resolveIdentitySpawn(ctx, actorSystem, actorName, tenantScope, engine.bindingQuery("saga", behavior, tenantScope), err); resolved != err { //nolint:errorlint // identity check: detects whether resolveExistingSpawn replaced err
			return resolved
		}
		return fmt.Errorf("failed to start saga %s: %w", behavior.ID(), err)
	}

	return verifySpawnedIdentity(ctx, pid, tenantScope, engine.bindingQuery("saga", behavior, tenantScope))
}

// SagaStatus returns the current status and state of the named saga.
//
// SagaInfo.Status is the lifecycle status the saga actor holds when it
// answers: SagaRunning while it runs, SagaCompleted once an action completes
// it or its compensation succeeds, and SagaFailed when its compensation
// fails or cannot run. SagaCompensating is held only while the actor runs a
// compensation, and the actor answers the query only after that compensation
// finishes, so in practice a caller sees SagaCompleted or SagaFailed instead. A finished
// saga actor keeps running, so it keeps answering with its final status. The
// status is not persisted: a saga actor that restarts (after a crash or a
// relocation) recovers its state from its events but reports SagaRunning
// again.
//
// Parameters:
//   - ctx: Execution context for managing timeouts and cancellations.
//   - sagaID: The unique identifier of the saga.
//   - timeout: The duration within which the query must be processed.
//
// Returns:
//   - SagaInfo containing the saga's current status and state.
//   - An error if the saga is not found or the query fails.
func (engine *Engine) SagaStatus(ctx context.Context, sagaID string, timeout time.Duration) (*SagaInfo, error) {
	if !engine.Started() {
		return nil, ErrEngineNotStarted
	}

	if sagaID == "" {
		return nil, ErrUndefinedEntityID
	}

	ref := engine.actorSystem.Load()
	if ref == nil {
		return nil, ErrEngineNotStarted
	}

	// Tenant-aware mode: resolve the caller's tenant identity at this trust
	// boundary and attach it to ctx before the query reaches the saga actor,
	// mirroring SendCommand. Without this, SagaActor.checkStateReadTenant
	// would see a tenant-less ctx and reject every tenant-aware caller with
	// ErrMissing, instead of enforcing isolation against a foreign tenant.
	actorName := sagaID
	if !qualifiesActorNames(engine.tenantResolver) {
		var nameErr error
		actorName, nameErr = engine.actorName("", sagaID)
		if nameErr != nil {
			return nil, nameErr
		}
	}
	if engine.tenantResolver != nil {
		tenantContext, resolveErr := engine.tenantResolver.Resolve(ctx)
		if resolveErr != nil {
			return nil, resolveErr
		}

		attachedCtx, attachErr := tenancy.Attach(ctx, tenantContext)
		if attachErr != nil {
			return nil, attachErr
		}
		ctx = attachedCtx

		// The saga is addressed by (tenant, ID): the caller reaches the saga
		// of its own tenant, and never one of another tenant's.
		var nameErr error
		if actorName, nameErr = engine.actorNameFor(tenantContext, sagaID); nameErr != nil {
			return nil, nameErr
		}
	}

	reply, err := ref.noSender.SendSync(ctx, actorName, new(egopb.GetStateCommand), timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to get saga status for %s: %w", sagaID, err)
	}

	commandReply, ok := reply.(*egopb.CommandReply)
	if !ok {
		return nil, fmt.Errorf("unexpected reply type from saga %s", sagaID)
	}

	state, _, err := protocol.ParseCommandReply(commandReply)
	if err != nil {
		return nil, err
	}

	return &SagaInfo{
		ID:     sagaID,
		Status: saga.StatusFromProto(commandReply.GetStateReply().GetSagaStatus()),
		State:  state,
	}, nil
}
