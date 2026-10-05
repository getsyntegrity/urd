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
	"errors"
	"fmt"
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"
	gerrors "github.com/tochemey/goakt/v4/errors"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/projection"
)

// StartProjection starts the named projection previously registered on the
// engine's Config via WithProjection.
//
// The projection processes events from the events store applying its own
// registered handler to manage state updates based on incoming events. The
// provided offset store ensures the projection maintains its processing
// position across restarts.
//
// Key behavior:
//   - Projections once created, will persist for the entire lifespan of the running Urd system.
//   - In cluster mode, the projection runs as a singleton on the oldest node. If that node
//     leaves the cluster, the singleton is automatically restarted on the new oldest node.
//   - In standalone (non-cluster) mode, the projection runs as a regular long-lived actor.
//   - A failed store round trip never stops the projection: the runner retries the pull with
//     exponential backoff until the store recovers and then resumes from committed offsets.
//     An event that cannot be processed (a handler error under the Fail or RetryAndFail
//     recovery policy, a failed decryption or event adaptation) stops the projection through
//     supervision, so the failure is visible through IsProjectionRunning.
//
// Parameters:
//   - ctx: Execution context used for cancellation and deadlines.
//   - name: The unique identifier the projection was registered under via WithProjection.
//
// Returns ErrProjectionNotRegistered when the name is unknown, or an error if
// the projection fails to start due to misconfiguration or underlying system issues.
func (engine *Engine) StartProjection(ctx context.Context, name string) error {
	if !engine.Started() {
		return ErrEngineNotStarted
	}

	actor := NewProjectionActor()

	ref := engine.actorSystem.Load()
	if ref == nil {
		return ErrEngineNotStarted
	}
	actorSystem := ref.sys

	// Fail fast on unknown names instead of letting the actor's PreStart
	// (and its retries) discover the missing registration. When the registry
	// extension cannot be resolved (e.g. the actor system is already stopped),
	// fall through and let the spawn surface the actual failure.
	if registry, ok := actorSystem.Extension(extensions.ProjectionExtensionID).(*extensions.ProjectionExtension); ok && registry.Get(name) == nil {
		return fmt.Errorf("%w: %s", ErrProjectionNotRegistered, name)
	}

	if actorSystem.InCluster() {
		// In cluster mode, run the projection as a singleton to avoid
		// duplicate event processing across nodes. The singleton is keyed by
		// the projection name and placed on the oldest node. SpawnSingleton
		// is idempotent when the name is already bound to this singleton, so
		// concurrent StartProjection calls across nodes all succeed; any
		// error it returns is a genuine failure worth surfacing. The
		// projection supervisor travels with the singleton wherever it is
		// placed or relocated.
		if _, err := actorSystem.SpawnSingleton(ctx, name, actor,
			goakt.WithSingletonSupervisor(newProjectionSupervisor())); err != nil {
			return fmt.Errorf("failed to start the projection=(%s): %w", name, err)
		}
		return nil
	}

	// In standalone mode, run as a regular long-lived actor.
	if _, err := actorSystem.Spawn(ctx, name,
		actor,
		goakt.WithLongLived(),
		goakt.WithRelocationDisabled(),
		goakt.WithSupervisor(newProjectionSupervisor())); err != nil {
		return fmt.Errorf("failed to start the projection=(%s): %w", name, err)
	}

	return nil
}

// StopProjection stops and removes the specified projection from the engine.
//
// This function gracefully shuts down the projection identified by `name` and removes it from the system.
// Any in-progress processing will be stopped, and the projection will no longer receive events.
//
// Parameters:
//   - ctx: Execution context for managing cancellation and timeouts.
//   - name: The unique identifier of the projection to be removed.
//
// Returns:
//   - An error if the projection fails to stop or does not exist; otherwise, nil.
func (engine *Engine) StopProjection(ctx context.Context, name string) error {
	if !engine.Started() {
		return ErrEngineNotStarted
	}

	ref := engine.actorSystem.Load()
	if ref == nil {
		return ErrEngineNotStarted
	}
	return ref.sys.Kill(ctx, name)
}

// IsProjectionRunning checks whether the specified projection is currently active and running.
//
// This function returns `true` if the projection identified by `name` is running. However, callers should
// always check the returned error to ensure the result is valid, as an error may indicate an inability to
// determine the projection's status.
//
// Parameters:
//   - ctx: Execution context for managing timeouts and cancellations.
//   - name: The unique identifier of the projection.
//
// Returns:
//   - A boolean indicating whether the projection is running (`true` if active, `false` otherwise).
//   - An error if the status check fails, which may result in a false negative.
func (engine *Engine) IsProjectionRunning(ctx context.Context, name string) (bool, error) {
	if !engine.Started() {
		return false, ErrEngineNotStarted
	}

	ref := engine.actorSystem.Load()
	if ref == nil {
		return false, ErrEngineNotStarted
	}

	pid, err := ref.sys.ActorOf(ctx, name)
	if err != nil {
		return false, fmt.Errorf("failed to get projection %s: %w", name, err)
	}

	if pid != nil {
		return pid.IsRunning(), nil
	}

	return false, nil
}

// RebuildProjection stops the named projection, resets its offset to the
// given timestamp, and restarts it. This causes the projection to
// reprocess all events from that point forward.
//
// Passing engine.ZeroTime replays from the beginning of time.
//
// Parameters:
//   - ctx: Execution context for managing cancellation and timeouts.
//   - name: The unique identifier of the projection to rebuild.
//   - from: The timestamp from which to start reprocessing events.
//
// Returns:
//   - An error if the rebuild process encounters issues; otherwise, nil.
func (engine *Engine) RebuildProjection(ctx context.Context, name string, from time.Time) error {
	if !engine.Started() {
		return ErrEngineNotStarted
	}

	engine.mutex.RLock()
	offsetStore := engine.offsetStore
	engine.mutex.RUnlock()

	if offsetStore == nil {
		return fmt.Errorf("offset store is required to rebuild projection")
	}

	// stop the running projection
	if err := engine.StopProjection(ctx, name); err != nil {
		return fmt.Errorf("failed to stop projection %s for rebuild: %w", name, err)
	}

	// The actor system releases a stopped actor's name asynchronously, through
	// its death watch. Spawning the same name before that happens hands back the
	// stale, stopped instance, and the late cleanup then removes the name again,
	// leaving no projection running after the rebuild.
	engine.awaitProjectionReleased(ctx, name)

	// reset the offset
	if err := offsetStore.ResetOffset(ctx, name, from.UnixMilli()); err != nil {
		return fmt.Errorf("failed to reset offset for projection %s: %w", name, err)
	}

	// restart the projection
	if err := engine.StartProjection(ctx, name); err != nil {
		return fmt.Errorf("failed to restart projection %s after rebuild: %w", name, err)
	}

	return nil
}

const (
	// projectionReleaseTimeout bounds how long a rebuild waits for the actor
	// system to release a stopped projection's name.
	projectionReleaseTimeout = 5 * time.Second
	// projectionReleaseInterval is how often a rebuild checks that release.
	projectionReleaseInterval = 5 * time.Millisecond
)

// awaitProjectionReleased blocks until the actor system no longer knows the
// named projection, ctx ends, or projectionReleaseTimeout elapses. It never
// fails: when the release cannot be confirmed in time the caller carries on and
// the later spawn reports any real failure.
func (engine *Engine) awaitProjectionReleased(ctx context.Context, name string) {
	ref := engine.actorSystem.Load()
	if ref == nil {
		return
	}

	deadline := time.NewTimer(projectionReleaseTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(projectionReleaseInterval)
	defer ticker.Stop()

	for {
		if _, err := ref.sys.ActorOf(ctx, name); errors.Is(err, gerrors.ErrActorNotFound) {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

// ProjectionLag reports, per shard, how far behind the named projection is
// relative to the newest event persisted in that shard.
//
// The returned map is keyed by shard number. For each shard the value is the
// time delta between the timestamp of the most recent event in the journal and
// the offset the projection has committed to the offset store. A value of 0
// means the projection is fully caught up for that shard (either because the
// shard is empty or because the committed offset already covers the latest
// event).
//
// Implementation overview:
//  1. Enumerate every shard known to the events store.
//  2. For each shard, read the projection's committed offset from the offset
//     store. In Urd, this offset is stored as the timestamp (in milliseconds)
//     of the last event processed by the projection for that shard.
//  3. Determine the timestamp of the newest event currently persisted in the
//     shard.
//  4. lag = latestEventTimestamp - committedOffset (clamped at 0 so that a
//     projection running ahead of the probe window is not reported as negative).
//
// The engine must be started and an offset store must be configured; otherwise
// an error is returned.
func (engine *Engine) ProjectionLag(ctx context.Context, projectionName string) (map[uint64]time.Duration, error) {
	if !engine.Started() {
		return nil, ErrEngineNotStarted
	}

	// Snapshot the store references under the mutex so we do not race with a
	// concurrent engine shutdown or reconfiguration while iterating shards.
	engine.mutex.RLock()
	offsetStore := engine.offsetStore
	eventsStore := engine.eventsStore
	engine.mutex.RUnlock()

	// Lag is defined relative to the projection's committed offset, so an
	// offset store is mandatory. Without it there is no meaningful answer.
	if offsetStore == nil {
		return nil, fmt.Errorf("offset store is required to compute projection lag")
	}

	// The shards and newest timestamps are those of the projection's own scope:
	// a global read would report another tenant's backlog as this projection's
	// lag. The scope is the one resolved at registration; a name that is not
	// registered has none, so there is no fallback to an unscoped read.
	scope, err := engine.projectionScope(projectionName)
	if err != nil {
		return nil, err
	}

	// Projections are sharded: each shard is advanced independently by its own
	// runner, so lag is always reported per shard rather than as a single
	// aggregate number. ShardOffsets answers "which shards exist" and "what is
	// the newest event timestamp per shard" in one round trip, so the only
	// per-shard work left is reading the projection's committed offset.
	shardOffsets, err := eventsStore.ShardOffsets(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch shard offsets: %w", err)
	}

	lags := make(map[uint64]time.Duration, len(shardOffsets))
	for shard, latestTimestamp := range shardOffsets {
		projectionID := &egopb.ProjectionId{
			ProjectionName: projectionName,
			ShardNumber:    shard,
		}

		// The committed offset is the "watermark" for this projection on this
		// shard: the timestamp of the last event the projection handler
		// successfully processed. A brand-new projection returns a zero-value
		// offset, which will naturally yield a lag equal to the age of the
		// oldest-to-newest span of events.
		offset, err := offsetStore.GetCurrentOffset(ctx, projectionID)
		if err != nil {
			return nil, fmt.Errorf("failed to get offset for shard %d: %w", shard, err)
		}

		// Both values below are unix **nanoseconds**:
		//   - the ShardOffsets values are event timestamps written by the
		//     entity actors using time.Now().UnixNano() (see
		//     event_sourced_actor.go / durable_state_actor.go /
		//     saga_actor.go). Nanosecond resolution gives projection offsets
		//     a tiebreaker fine enough to make co-timestamp collisions across
		//     two polls astronomically improbable.
		//   - Offset.Value is a copy of a prior event's timestamp, propagated
		//     from EventsStore.GetShardEvents' nextOffset return value through
		//     projectionRunner.commitOffset.
		// Their subtraction therefore yields the lag in nanoseconds, which is
		// the native representation of time.Duration.
		// (Note: Offset.Timestamp is a separate wall-clock audit field written
		// in milliseconds; it is deliberately not used here.)
		//
		// Clamp negative values: the projection runner may have advanced its
		// offset between the two store reads above, which would otherwise
		// surface as a spurious negative lag.
		lagNanos := latestTimestamp - offset.GetValue()
		if lagNanos < 0 {
			lagNanos = 0
		}
		lags[shard] = time.Duration(lagNanos)
	}

	return lags, nil
}

// projectionScope returns the effective persistence scope the engine resolved
// for the named projection at registration. It returns
// ErrProjectionNotRegistered when the name is unknown; there is no fallback to
// persistence.Unscoped().
func (engine *Engine) projectionScope(name string) (persistence.Scope, error) {
	scope, ok := engine.projectionScopes[name]
	if !ok {
		return persistence.Scope{}, fmt.Errorf("%w: %s", ErrProjectionNotRegistered, name)
	}
	return scope, nil
}

// projectionScopesOf extracts the effective scope of each resolved projection.
func projectionScopesOf(resolved map[string]*projection.Options) map[string]persistence.Scope {
	scopes := make(map[string]persistence.Scope, len(resolved))
	for name, options := range resolved {
		scopes[name] = *options.Scope
	}
	return scopes
}
