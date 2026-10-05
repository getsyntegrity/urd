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
	"reflect"
	"strings"
	"sync"

	kitlog "github.com/pablogore/kit-logger/pkg/logger"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"
	"go.uber.org/atomic"

	"github.com/getsyntegrity/urd/encryption"
	"github.com/getsyntegrity/urd/eventadapter"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/instrumentation"
	"github.com/getsyntegrity/urd/internal/syncmap"
	"github.com/getsyntegrity/urd/offsetstore"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

// Done is a signal that an operation has completed
type Done struct{}

// actorSystemRef bundles the underlying goakt.ActorSystem with its NoSender PID
// so they can be published as a single value via atomic.Pointer. Bundling
// guarantees readers always see a consistent (sys, noSender) pair — they cannot
// observe a half-initialized engine where one is set and the other is not.
type actorSystemRef struct {
	sys      goakt.ActorSystem
	noSender *goakt.PID
}

// Engine represents the engine that empowers the various entities
type Engine struct {
	name          string
	eventsStore   persistence.EventsStore
	stateStore    persistence.StateStore
	offsetStore   offsetstore.OffsetStore
	snapshotStore persistence.SnapshotStore
	actorSystem   atomic.Pointer[actorSystemRef]
	logger        kitlog.Logger
	started       atomic.Bool
	eventStream   eventstream.Stream
	mutex         sync.RWMutex

	eventsStreams *syncmap.Map[string, *eventsStream]
	statesStreams *syncmap.Map[string, *statesStream]
	eventAdapters []eventadapter.EventAdapter
	telemetry     *Telemetry
	metrics       *instrumentation.Instruments
	encryptor     encryption.Encryptor

	// schemaMigration is carried over from Config: Start migrates the schema of
	// every store that implements persistence.SchemaMigrator.
	schemaMigration bool

	// tenantResolver is the effective tenancy.TenantResolver carried over
	// from Config; non-nil means tenant-aware mode is active. NewEngine has
	// already validated there is at most one (ErrAmbiguousTenantResolver).
	// It is consumed on every tenant-aware path: Dispatch (and SendCommand,
	// which goes through it) resolves the caller's tenant and attaches it
	// before the command reaches the actor (commands.go),
	// SagaStatus does the same for its state read (sagas.go), EraseEntity
	// derives the scope to erase from it (entities.go), and spawnTenantScope
	// uses its fixed tenant when a spawn names none (spawn_tenancy.go).
	tenantResolver tenancy.TenantResolver

	// projectionScopes maps each registered projection name to the effective
	// persistence scope NewEngine resolved for it. Read-only after NewEngine.
	projectionScopes map[string]persistence.Scope

	// entityFamilies is the set declared with WithEntityFamilies; zero means
	// nothing was declared and every family may be spawned. Set once by
	// NewEngine and never changed.
	entityFamilies EntityFamily
}

// NewEngine plugs Urd into an already-constructed and started goakt.ActorSystem.
//
// The caller builds a single Config with NewConfig, passes cfg.GoaktOptions()
// to goakt.NewActorSystem, starts the actor system, and then hands the same
// Config to NewEngine. This guarantees the actor system's extensions and the
// engine's view of stores, projections, telemetry, and the in-process event
// stream stay in lockstep.
//
// NewEngine validates the actor system on entry:
//
//   - sys must be non-nil (otherwise returns ErrActorSystemRequired);
//   - sys.Running() must be true (otherwise returns ErrActorSystemNotStarted);
//   - every extension Urd needs based on cfg must be registered on sys
//     (otherwise returns ErrMissingRequiredExtensions with the missing IDs).
//
// NewEngine also registers Urd's internal spawn-configuration dependency
// types and any behavior kinds supplied via WithBehaviorKinds or
// WithEntityKinds on the actor system, so that entity spawn requests routed
// to this node from cluster peers can be deserialized. Every node in a
// cluster must therefore build its engine with the same kinds. A kind that
// is an untyped nil or not a pointer makes NewEngine return a
// *BehaviorPlacementError wrapping ErrBehaviorNotPointer, in single-node and
// cluster mode alike, before any kind is registered. A typed-nil pointer such
// as (*T)(nil) registers T like new(T) does.
//
// The engine does NOT take ownership of the actor system. Engine.Stop will
// not call sys.Stop; the caller stops the actor system on their own
// schedule (typically after Engine.Stop).
//
// Parameters:
//   - actorSys: A running goakt.ActorSystem with Urd's required extensions registered.
//   - config: The Config used to build the actor system's Urd extensions.
//
// Returns:
//   - A pointer to the newly created Engine instance, or an error.
func NewEngine(actorSys goakt.ActorSystem, config *Config) (*Engine, error) {
	if actorSys == nil {
		return nil, ErrActorSystemRequired
	}

	if config == nil {
		return nil, fmt.Errorf("%w: nil Config", ErrMissingRequiredExtensions)
	}

	if !actorSys.Running() {
		return nil, ErrActorSystemNotStarted
	}

	// TenantResolver is a security boundary (DP2): two or more non-nil
	// registrations must fail construction rather than pick one via
	// last-call-wins. The second branch is a defensive guard against a state
	// WithTenantResolver's own inertness rules make unreachable today (a
	// positive count with no effective resolver); it protects future
	// refactors of that invariant.
	if config.tenantResolverCount > 1 || (config.tenantResolverCount > 0 && config.tenantResolver == nil) {
		return nil, ErrAmbiguousTenantResolver
	}

	// Projection scopes are validated against the final tenancy mode, after
	// every option has been applied, so the result does not depend on the
	// order of WithProjection and WithTenantResolver.
	resolvedProjections, err := config.resolveProjections()
	if err != nil {
		return nil, err
	}

	if err := validateActorSystemExtensions(actorSys, config); err != nil {
		return nil, err
	}

	// GoAkt's type registry names a type through a pointer and panics, while
	// holding the actor-system lock, on an untyped nil or a non-pointer.
	// Check every kind before registering any of them. A typed-nil pointer
	// is accepted, as it always was: the registry only reads its pointer type
	// and decodes into a fresh value of that type.
	for _, kind := range config.behaviorKinds {
		if kind == nil || reflect.TypeOf(kind).Kind() != reflect.Pointer {
			return nil, &BehaviorPlacementError{Kind: fmt.Sprintf("%T", kind), Err: ErrBehaviorNotPointer}
		}
	}

	// Register dependency types on this node so spawn requests placed here by
	// peers (SpawnOn placement, relocation) can be deserialized even before
	// this node has spawned such an entity itself. The internal spawn-config
	// types live in internal/extensions and cannot be registered by
	// application code; user behavior kinds come from WithBehaviorKinds and
	// WithEntityKinds.
	dependencies := []extension.Dependency{new(extensions.EntityConfig), new(extensions.SagaConfig), new(extensions.EntityTenantScope)}
	for _, kind := range config.behaviorKinds {
		dependencies = append(dependencies, kind)
	}
	if err := actorSys.Inject(dependencies...); err != nil {
		return nil, err
	}

	e := &Engine{
		name:             actorSys.Name(),
		eventsStore:      config.eventsStore,
		stateStore:       config.stateStore,
		offsetStore:      config.offsetStore,
		snapshotStore:    config.snapshotStore,
		logger:           config.logger,
		eventStream:      config.eventStream,
		eventAdapters:    config.eventAdapters,
		telemetry:        config.telemetry,
		encryptor:        config.encryptor,
		schemaMigration:  config.schemaMigration,
		tenantResolver:   config.tenantResolver,
		projectionScopes: projectionScopesOf(resolvedProjections),
		entityFamilies:   config.entityFamilies,
		eventsStreams:    syncmap.New[string, *eventsStream](),
		statesStreams:    syncmap.New[string, *statesStream](),
	}
	e.actorSystem.Store(&actorSystemRef{
		sys:      actorSys,
		noSender: actorSys.NoSender(),
	})

	return e, nil
}

// validateActorSystemExtensions asserts that every extension Urd needs given
// the engine's configuration is registered on the actor system. Missing
// extensions are collected and reported together so the caller learns about
// the full set of problems at once.
func validateActorSystemExtensions(sys goakt.ActorSystem, cfg *Config) error {
	type check struct {
		id       string
		required bool
	}
	checks := []check{
		{extensions.EventsStoreExtensionID, true},
		{extensions.EventsStreamExtensionID, true},
		{extensions.DurableStateStoreExtensionID, cfg.stateStore != nil},
		{extensions.OffsetStoreExtensionID, cfg.offsetStore != nil},
		{extensions.ProjectionExtensionID, len(cfg.projections) > 0},
		{extensions.SnapshotStoreExtensionID, cfg.snapshotStore != nil},
		{extensions.EventAdaptersExtensionID, len(cfg.eventAdapters) > 0},
		{extensions.TelemetryExtensionID, cfg.telemetry != nil},
		{extensions.EncryptorExtensionID, cfg.encryptor != nil},
		{extensions.TenancyExtensionID, cfg.tenantResolver != nil},
	}

	var missing []string
	for _, c := range checks {
		if c.required && sys.Extension(c.id) == nil {
			missing = append(missing, c.id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s (hint: build the actor system with cfg.GoaktOptions() and pass the same cfg to NewEngine)",
			ErrMissingRequiredExtensions, strings.Join(missing, ", "))
	}
	return nil
}

// Start initializes the Urd engine on top of an actor system that is already
// running.
//
// In the meta-framework design, Start does no actor-system construction —
// the caller has already built and started goakt.NewActorSystem. Start only
// migrates the store schemas (when WithSchemaMigration is configured), wires
// the OpenTelemetry propagator (when WithTelemetry is configured) and flips
// the engine into a "ready to receive entity work" state.
//
// Parameters:
//   - ctx: Execution context. It is passed to the stores' Migrate when
//     WithSchemaMigration is set; otherwise it is unused.
//
// Returns:
//   - An error if the engine is in an inconsistent state; otherwise, nil.
func (engine *Engine) Start(ctx context.Context) error {
	if engine.actorSystem.Load() == nil {
		return ErrActorSystemRequired
	}

	// Migrate first, before anything else changes: a failure must leave the
	// engine as it was, not started and with no telemetry side effects.
	if engine.schemaMigration {
		if err := engine.migrateSchemas(ctx); err != nil {
			return err
		}
	}

	if engine.telemetry != nil {
		engine.metrics = instrumentation.New(engine.telemetry.Meter)
		// Trace context must cross process boundaries for end-to-end
		// distributed tracing; see instrumentation.InstallPropagator.
		instrumentation.InstallPropagator()
	}
	engine.started.Store(true)
	return nil
}

// migrateSchemas runs Migrate on every configured store that implements
// persistence.SchemaMigrator and stops at the first error. Stores that do not
// implement it are skipped.
func (engine *Engine) migrateSchemas(ctx context.Context) error {
	stores := []struct {
		kind  string
		store any
	}{
		{"events store", engine.eventsStore},
		{"state store", engine.stateStore},
		{"offset store", engine.offsetStore},
		{"snapshot store", engine.snapshotStore},
	}
	migrated := false
	for _, s := range stores {
		migrator, ok := s.store.(persistence.SchemaMigrator)
		if !ok {
			continue
		}
		migrated = true
		if err := migrator.Migrate(ctx); err != nil {
			return fmt.Errorf("engine: migrate the schema of the %s: %w", s.kind, err)
		}
	}
	if !migrated {
		// The option was asked for but did nothing: most likely the stores in use
		// are not the ones that own a schema. Start still succeeds.
		engine.logger.Warn("schema migration enabled but no store implements persistence.SchemaMigrator")
	}
	return nil
}

// Stop gracefully shuts down the Urd engine.
//
// Stop terminates all running publishers, closes the local event stream
// adapter, and detaches the engine's reference to the actor system so
// concurrent callers fail fast with ErrEngineNotStarted. The actor system
// itself is NOT stopped — its lifecycle belongs to the caller, who built it
// and is expected to call sys.Stop(ctx) on their own schedule (typically
// after Engine.Stop returns).
//
// Parameters:
//   - ctx: Execution context for managing cancellation and timeouts during
//     publisher shutdown.
//
// Every shutdown step is attempted even when a publisher fails to close: a
// failing publisher never leaves the others, the event stream, or the actor
// system reference behind, because a second Stop returns nil at once.
//
// Stop and the registration methods (AddEventPublishers, AddStatePublishers,
// their ForTenant forms, Subscribe and SubscribeForTenant) exclude each other.
// A registration accepted before Stop is closed by Stop, each publisher exactly
// once and each subscriber terminated; one that reaches the engine after Stop
// began returns ErrEngineNotStarted and registers nothing. No publisher or
// subscriber is registered once Stop has returned. Stop is safe to call twice
// or concurrently, and does not hold the engine lock while closing publishers.
//
// Returns:
//   - The errors of every publisher that failed to close, joined with
//     errors.Join; otherwise, nil.
func (engine *Engine) Stop(ctx context.Context) error {
	// The transition is atomic, so concurrent Stops are safe: exactly one runs the
	// shutdown and the others return nil at once, as a Stop after a Stop does.
	if !engine.started.CompareAndSwap(true, false) {
		return nil
	}

	// Registration is mutually exclusive with this snapshot. started is already
	// false, so a registration that reaches its own locked check afterwards is
	// refused with ErrEngineNotStarted; one that was accepted earlier is in the
	// maps taken here, because Lock waits for it. Nothing is registered once the
	// maps are taken.
	engine.mutex.Lock()
	eventsStreams := engine.eventsStreams.Values()
	statesStreams := engine.statesStreams.Values()
	engine.eventsStreams.Reset()
	engine.statesStreams.Reset()
	eventStream := engine.eventStream
	engine.mutex.Unlock()

	var errs []error

	// Publisher Close is caller code, so it runs outside the lock: a Close that
	// calls back into the engine cannot deadlock against it.
	for _, stream := range eventsStreams {
		stream.done <- Done{}
		stream.subscriber.Shutdown()
		if err := stream.publisher.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close events publisher %q: %w", stream.publisher.ID(), err))
		}
	}

	for _, stream := range statesStreams {
		stream.done <- Done{}
		stream.subscriber.Shutdown()
		if err := stream.publisher.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close durable state publisher %q: %w", stream.publisher.ID(), err))
		}
	}

	if eventStream != nil {
		eventStream.Close()
	}

	// Detach our reference atomically so callers racing with shutdown fail
	// fast. We deliberately do NOT call sys.Stop — the actor system belongs
	// to the caller.
	engine.actorSystem.Store(nil)
	return errors.Join(errs...)
}

// Started returns true when the Urd engine has started
func (engine *Engine) Started() bool {
	return engine.started.Load()
}

// ActorSystem returns the underlying goakt.ActorSystem used by the engine.
//
// Returns nil if the engine has not been started or has already been stopped.
// The returned reference is a snapshot; callers should not retain it across
// engine restarts.
func (engine *Engine) ActorSystem() goakt.ActorSystem {
	ref := engine.actorSystem.Load()
	if ref == nil {
		return nil
	}
	return ref.sys
}
