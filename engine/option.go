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
	"encoding"
	"fmt"
	"reflect"
	"strings"

	kitlog "github.com/pablogore/kit-logger/pkg/logger"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"
	"github.com/tochemey/goakt/v4/supervisor"

	"github.com/getsyntegrity/urd/encryption"
	"github.com/getsyntegrity/urd/eventadapter"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/goaktlog"
	"github.com/getsyntegrity/urd/offsetstore"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/projection"
	"github.com/getsyntegrity/urd/tenancy"
)

// Config captures every option an Engine needs.
//
// A Config is built once with NewConfig and reused twice: passed to
// goakt.NewActorSystem via GoaktOptions, then to NewEngine. Callers should
// not construct Config zero-values; use NewConfig and Option functions to
// populate it.
type Config struct {
	actorNamespace string
	eventsStore    persistence.EventsStore
	stateStore     persistence.StateStore
	offsetStore    offsetstore.OffsetStore
	snapshotStore  persistence.SnapshotStore
	logger         kitlog.Logger
	projections    []projectionRegistration
	eventAdapters  []eventadapter.EventAdapter
	telemetry      *Telemetry
	encryptor      encryption.Encryptor
	behaviorKinds  []BehaviorKind

	// schemaMigration is set by WithSchemaMigration: Engine.Start then runs
	// Migrate on every configured store that implements
	// persistence.SchemaMigrator.
	schemaMigration bool

	// entityFamilies is the set declared with WithEntityFamilies; zero
	// means nothing was declared and every family may be spawned.
	entityFamilies EntityFamily

	// tenantResolver is the effective tenancy.TenantResolver, if any. Its
	// non-nil-ness IS tenant-aware mode (design.md D1) — there is no
	// separate boolean flag. It is set once, by the first non-nil
	// registration WithTenantResolver sees; later non-nil registrations are
	// counted in tenantResolverCount but never replace it (first-wins, not
	// last-call-wins — DP2).
	tenantResolver tenancy.TenantResolver

	// tenantResolverCount counts non-nil WithTenantResolver registrations
	// only. NewEngine rejects count > 1 with ErrAmbiguousTenantResolver:
	// TenantResolver is a security boundary and must not depend on Option
	// ordering the way WithLogger/WithTelemetry do (DP2).
	tenantResolverCount int

	// eventStream is the in-process pub/sub stream Urd's entity actors
	// publish to and the engine's publishers/subscribers consume from. It is
	// allocated by NewConfig and the same instance is wired into the actor
	// system (as the EventsStream extension) by GoaktOptions and into the
	// engine by NewEngine.
	eventStream eventstream.Stream
}

// NewConfig builds a Config from a list of Options.
//
// eventsStore is the events store Urd persists event-sourced state to; pass
// nil for durable-state-only deployments that never host event-sourced
// entities.
//
// The returned Config is then passed to both goakt.NewActorSystem (via
// GoaktOptions) and NewEngine, so an actor system and the engine that plugs
// into it always share the same configuration.
func NewConfig(eventsStore persistence.EventsStore, opts ...Option) *Config {
	c := &Config{
		eventsStore: eventsStore,
		eventStream: eventstream.New(),
	}

	for _, opt := range opts {
		opt.Apply(c)
	}

	// Options may have set a nil or typed-nil logger, which would panic on the
	// first log call. Resolving after the loop covers every option path.
	c.logger = ResolveLogger(c.logger)
	return c
}

// GoaktOptions returns the goakt.Options Urd requires when the caller
// constructs the actor system that hosts it.
//
// Pass the returned slice to goakt.NewActorSystem alongside any other
// goakt.Options the deployment needs (cluster, remote, TLS, custom
// extensions, …). Urd cannot register its extensions on an
// already-constructed actor system, so this handoff at construction time is
// the only supported entry point.
//
// The returned list always includes the events store, event stream, logger
// adapter, pub/sub option, and a default resume-on-error supervisor. It
// additionally contains the state store, offset store, projection, snapshot
// store, event adapters, telemetry, encryptor, and tenancy marker extensions
// whenever the corresponding Option was set on this Config.
func (c *Config) GoaktOptions() []goakt.Option {
	opts := []goakt.Option{
		goakt.WithLogger(goaktlog.New(c.logger)),
		goakt.WithActorInitMaxRetries(5),
		goakt.WithPubSub(),
		goakt.WithDefaultSupervisor(
			supervisor.NewSupervisor(supervisor.WithAnyErrorDirective(supervisor.ResumeDirective)),
		),
		goakt.WithExtensions(
			extensions.NewEventsStore(c.eventsStore),
			extensions.NewEventsStream(c.eventStream),
		),
	}

	if c.stateStore != nil {
		opts = append(opts, goakt.WithExtensions(extensions.NewDurableStateStore(c.stateStore)))
	}

	if c.offsetStore != nil {
		opts = append(opts, goakt.WithExtensions(extensions.NewOffsetStore(c.offsetStore)))
	}

	if len(c.projections) > 0 {
		// resolveProjections normalizes into copies so defaulting Recovery never
		// mutates the caller-owned Options values. An invalid registration is
		// reported by NewEngine, which validates before anything starts, so the
		// extension is simply left out here.
		if projections, err := c.resolveProjections(); err == nil {
			opts = append(opts, goakt.WithExtensions(extensions.NewProjectionExtension(projections)))
		}
	}

	if c.snapshotStore != nil {
		opts = append(opts, goakt.WithExtensions(extensions.NewSnapshotStore(c.snapshotStore)))
	}

	if len(c.eventAdapters) > 0 {
		opts = append(opts, goakt.WithExtensions(extensions.NewEventAdapters(c.eventAdapters)))
	}

	if c.telemetry != nil {
		opts = append(opts, goakt.WithExtensions(
			extensions.NewTelemetryExtension(c.telemetry.Tracer, c.telemetry.Meter)))
	}

	if c.encryptor != nil {
		opts = append(opts, goakt.WithExtensions(extensions.NewEncryptor(c.encryptor)))
	}

	if c.tenantResolver != nil {
		opts = append(opts, goakt.WithExtensions(extensions.NewTenancyMarker(qualifiesActorNames(c.tenantResolver))))
	}

	return opts
}

// Option configures a Config.
//
// Options are applied by NewConfig and surface through both GoaktOptions and
// NewEngine, ensuring the actor system and the engine share one source of
// truth.
type Option interface {
	Apply(c *Config)
}

var _ Option = OptionFunc(nil)

// OptionFunc is an adapter that turns a plain function into an Option.
type OptionFunc func(c *Config)

// Apply implements Option.
func (f OptionFunc) Apply(c *Config) {
	f(c)
}

// WithLogger sets the kit-logger Logger used by the engine and the goakt
// actor system it sits on.
//
// When unset, or when the given logger is nil or a typed-nil pointer, Urd
// logs through DefaultLogger(): kit-logger's process-wide logger. The same
// logger is adapted into the goakt logger so the actor system, Urd's
// internals, and the caller log through one backend.
func WithLogger(logger kitlog.Logger) Option {
	return OptionFunc(func(c *Config) {
		c.logger = logger
	})
}

// WithStateStore configures the store used to persist durable-state entities.
//
// A state store is mandatory for engines that host DurableStateBehavior
// entities. Event-sourced-only engines may omit it.
func WithStateStore(stateStore persistence.StateStore) Option {
	return OptionFunc(func(c *Config) {
		c.stateStore = stateStore
	})
}

// WithSchemaMigration makes Engine.Start bring the schema of the configured
// stores up to date before the engine accepts any command.
//
// Start calls Migrate on each store that implements persistence.SchemaMigrator,
// in this order: events store, state store, offset store, snapshot store. A
// store that does not implement it is left alone. The first Migrate error
// stops Start, which returns it, wrapped with the kind of store that failed,
// and the engine does not start.
//
// The option is off by default, so an engine never changes a database schema
// unless asked to. The stores must be connected before Start: Urd does not
// connect them.
func WithSchemaMigration() Option {
	return OptionFunc(func(c *Config) {
		c.schemaMigration = true
	})
}

// WithOffsetStore configures the offset store used by projections to durably
// track their processing position.
//
// An offset store is mandatory whenever WithProjection is used.
func WithOffsetStore(offsetStore offsetstore.OffsetStore) Option {
	return OptionFunc(func(c *Config) {
		c.offsetStore = offsetStore
	})
}

// WithProjection registers a named projection with its own handler and
// runtime options. The option is repeatable: call it once per projection the
// engine hosts, each with a distinct name and its own projection.Options.
//
// The name is the projection's unique identifier — the same name is later
// passed to Engine.StartProjection to start it, and it keys the projection's
// committed offsets in the offset store. Registering the same name twice
// under the same effective scope is rejected by NewEngine with
// ErrProjectionDuplicate.
//
// The projection's scope comes from options.Scope (see projection.Options): the
// registry identity is (scope, name), checked by NewEngine against the
// engine's tenancy mode independently of the order of the options. The same
// name under different scopes is a distinct registration, but start, stop and
// addressing still resolve by name alone, so NewEngine rejects such a name with
// ErrProjectionNameAmbiguous until scoped addressing exists.
//
// The supplied projection.Options carries the projection handler and
// runtime knobs:
//
//   - Handler is required; it processes each event this projection observes.
//   - BufferSize bounds the in-flight event window.
//   - StartOffset is the timestamp from which a new projection begins reading.
//   - ResetOffset is the fallback offset used when recovery rewinds the projection.
//   - PullInterval is how often the runner polls for new events.
//   - Recovery controls retry behavior; nil falls back to projection.NewRecovery().
//   - DeadLetterHandler receives events the projection cannot process after retries.
//
// A nil options pointer is ignored. Projections also require an offset store
// (WithOffsetStore) to track progress durably.
//
// In cluster mode every node must register the same projections: the
// projection runs as a cluster singleton that can be (re)spawned on any node,
// and the hosting node resolves the handler from its own registration — the
// same contract WithBehaviorKinds establishes for entity behaviors.
func WithProjection(name string, options *projection.Options) Option {
	return OptionFunc(func(c *Config) {
		if options == nil {
			return
		}
		c.projections = append(c.projections, newProjectionRegistration(name, options))
	})
}

// WithSnapshotStore configures the store used to persist entity snapshots.
//
// When paired with WithSnapshotInterval on an entity, the engine
// periodically saves a snapshot of the entity's state so recovery can skip
// ahead instead of replaying the full event history. Event-sourced entities
// recover by full replay when no snapshot store is set.
func WithSnapshotStore(snapshotStore persistence.SnapshotStore) Option {
	return OptionFunc(func(c *Config) {
		c.snapshotStore = snapshotStore
	})
}

// WithTelemetry enables OpenTelemetry instrumentation on the engine.
//
// When configured, the engine emits trace spans and metrics for command
// processing, event persistence, and projection handling. Pass nil to
// disable instrumentation.
func WithTelemetry(telemetry *Telemetry) Option {
	return OptionFunc(func(c *Config) {
		c.telemetry = telemetry
	})
}

// WithEventAdapters registers one or more EventAdapter instances.
//
// Adapters transform persisted events from older schema versions into the
// shape the current code expects. They run transparently during entity
// recovery and projection consumption.
//
// Adapters are applied in registration order. When an event matches more
// than one adapter, the output of one feeds the next.
func WithEventAdapters(adapters ...eventadapter.EventAdapter) Option {
	return OptionFunc(func(c *Config) {
		c.eventAdapters = append(c.eventAdapters, adapters...)
	})
}

// EntityKind is the common contract satisfied by every behavior Urd spawns
// as an entity: EventSourcedBehavior, DurableStateBehavior, and SagaBehavior
// values are all EntityKinds.
//
// It aliases goakt's extension.Dependency because behaviors ride along in
// spawn requests as dependencies: when a spawn is placed on a remote node,
// the behavior is serialized on the calling side and reconstructed on the
// receiving side from goakt's dependency type registry. WithEntityKinds is
// how that registry learns about behavior types ahead of time.
//
// Deprecated: use [BehaviorKind]. Removed in the next major release (#124).
type EntityKind = extension.Dependency

// WithEntityKinds pre-registers the behavior types of the entity kinds this
// node can host, so their spawn requests can be deserialized on arrival.
//
// In cluster mode, Engine.Entity, Engine.DurableStateEntity, and Engine.Saga
// serialize the behavior as a spawn dependency and may place the actor on a
// remote node. The receiving node reconstructs the behavior against its own
// type registry, and fails with a "dependency type is not registered" error
// when the type is unknown there. Registering behaviors lazily at spawn time
// only covers the calling node, so every node must list every kind it may
// receive — the same contract ClusterKinds establishes for actor kinds.
//
// Pass one value per behavior type (a zero value is fine; only its concrete
// type is registered): event-sourced behaviors, durable-state behaviors, and
// saga behaviors all qualify. NewEngine registers them, alongside Urd's
// internal spawn-configuration types, on the node's actor system.
//
// Single-node deployments may omit this option; the lazy registration done by
// SpawnEventSourced, SpawnDurableState, and SpawnSaga (and their deprecated
// predecessors Entity, DurableStateEntity, and Saga) is sufficient when
// spawns never leave the local node.
//
// WithEntityKinds and WithBehaviorKinds append to the same registration list,
// so the two can be mixed on one node and across nodes.
//
// Deprecated: use [WithBehaviorKinds]. Removed in the next major release
// (#124).
func WithEntityKinds(kinds ...EntityKind) Option {
	return OptionFunc(func(c *Config) {
		// A []EntityKind cannot be appended to a []BehaviorKind in one call:
		// the element types differ, even though each value is assignable.
		for _, kind := range kinds {
			c.behaviorKinds = append(c.behaviorKinds, kind)
		}
	})
}

// BehaviorKind is a behavior type a node registers so that, in cluster mode,
// it can reconstruct behaviors that peers place on it. It has the same method
// set as EntityKind, spelled with the standard library only: any EntityKind
// value is a BehaviorKind and any BehaviorKind value is an EntityKind.
//
// A BehaviorKind value must be a pointer, because the runtime's type registry
// names a type through a pointer. Its MarshalBinary and
// UnmarshalBinary carry the behavior's state between nodes.
type BehaviorKind interface {
	// ID returns the behavior's identifier.
	ID() string
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
}

// WithBehaviorKinds pre-registers the behavior types this node can host, so
// spawn requests that peers place on it can be deserialized on arrival. It is
// the successor of WithEntityKinds, takes the same values, and appends to the
// same registration list, so the two options can be mixed.
//
// Pass one pointer per behavior type (a zero value such as
// new(AccountBehavior) is fine, and so is a typed-nil pointer; only its
// concrete type is registered). NewEngine registers every kind on the node's
// actor system and returns a *BehaviorPlacementError wrapping
// ErrBehaviorNotPointer, before registering anything, when a kind is an
// untyped nil or not a pointer.
//
// In cluster mode every node must list every kind it may receive. Single-node
// deployments may omit this option.
func WithBehaviorKinds(kinds ...BehaviorKind) Option {
	return OptionFunc(func(c *Config) {
		c.behaviorKinds = append(c.behaviorKinds, kinds...)
	})
}

// EntityFamily is a bit set of the entity families an engine hosts:
// event-sourced entities, durable-state entities and sagas. Declare the
// families with WithEntityFamilies; combine several with |, for example
// EventSourcedFamily|SagaFamily.
type EntityFamily uint8

const (
	// EventSourcedFamily covers SpawnEventSourced and its deprecated
	// predecessor Entity.
	EventSourcedFamily EntityFamily = 1 << iota
	// DurableStateFamily covers SpawnDurableState and its deprecated
	// predecessor DurableStateEntity.
	DurableStateFamily
	// SagaFamily covers SpawnSaga and its deprecated predecessor Saga.
	SagaFamily
)

// knownEntityFamilies is every family bit the engine knows; other bits are
// ignored by WithEntityFamilies.
const knownEntityFamilies = EventSourcedFamily | DurableStateFamily | SagaFamily

// String names the families in f, joined with "|" in declaration order,
// for example "EventSourced|Saga". A value with no known family bit, or
// with an unknown bit, prints as EntityFamily(n).
func (f EntityFamily) String() string {
	if f == 0 || f&^knownEntityFamilies != 0 {
		return fmt.Sprintf("EntityFamily(%d)", uint8(f))
	}
	var names []string
	for _, known := range []struct {
		family EntityFamily
		name   string
	}{
		{EventSourcedFamily, "EventSourced"},
		{DurableStateFamily, "DurableState"},
		{SagaFamily, "Saga"},
	} {
		if f&known.family != 0 {
			names = append(names, known.name)
		}
	}
	return strings.Join(names, "|")
}

// WithEntityFamilies declares the entity families the engine hosts. Once
// declared, a spawn of any other family — through the SpawnEventSourced,
// SpawnDurableState and SpawnSaga methods or their deprecated predecessors
// Entity, DurableStateEntity and Saga — returns an error wrapping
// ErrEntityFamilyNotDeclared, naming the family, before anything is spawned.
//
// The option is repeatable; the declared set is the union of every call.
// Bits other than the three families above are ignored. Without the option,
// or when no known family bit is ever passed, nothing is declared and every
// family spawns as before, so existing configurations are unchanged.
// compose/goakt passes a Spec's declared families through this option.
func WithEntityFamilies(families ...EntityFamily) Option {
	return OptionFunc(func(c *Config) {
		for _, family := range families {
			c.entityFamilies |= family & knownEntityFamilies
		}
	})
}

// WithEventStream sets the in-process event stream the actor system and the
// engine share, instead of the one NewConfig allocates. The caller that
// supplies it hands it over: Engine.Stop closes it, as it closes the
// default one. A nil or typed-nil stream is ignored and the default stays.
//
// compose/goakt uses it so the composition root owns the stream from the
// start and can close it itself when startup fails before an engine exists.
func WithEventStream(stream eventstream.Stream) Option {
	return OptionFunc(func(c *Config) {
		if stream == nil {
			return
		}
		if v := reflect.ValueOf(stream); v.Kind() == reflect.Pointer && v.IsNil() {
			return
		}
		c.eventStream = stream
	})
}

// WithEncryptor configures transparent encryption for event and snapshot
// payloads.
//
// Events are encrypted before persistence and decrypted during recovery and
// projection consumption. Combined with a key store, this enables
// GDPR-compliant crypto-shredding: deleting an entity's encryption key
// makes its events unrecoverable.
func WithEncryptor(encryptor encryption.Encryptor) Option {
	return OptionFunc(func(c *Config) {
		c.encryptor = encryptor
	})
}

// isNilResolver returns true when r is nil or a typed-nil (e.g.
// (*MyResolver)(nil), a nil named function, or a nil map/slice/chan value
// implementing tenancy.TenantResolver). It mirrors isNilLogger
// (internal/logging/logging.go:62): a typed-nil interface value is non-nil
// at the interface level but wraps a nil concrete value, which would
// misfire tenant-aware mode and any resolver call made against it.
//
// reflect.Value.IsNil panics on kinds that cannot be nil, so it is only
// called for the nil-capable kinds (Chan, Func, Interface, Map, Pointer,
// Slice); every other kind (e.g. a struct value) cannot be a typed-nil and
// is reported as non-nil without calling IsNil.
func isNilResolver(r tenancy.TenantResolver) bool {
	if r == nil {
		return true
	}
	v := reflect.ValueOf(r)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// WithTenantResolver registers the tenancy.TenantResolver the engine
// resolves and attaches a tenant identity through. Registering a non-nil
// resolver — and only that — activates tenant-aware mode
// (design.md D1); an engine that never calls this Option, or only ever
// calls it with nil, keeps today's legacy, non-tenant-aware behavior.
//
// Passing nil is inert: it registers no resolver, does not activate
// tenant-aware mode, and does not reset a resolver a previous call already
// registered. Nil is therefore not a mechanism to disable tenancy once
// configured — the effective resolver is decided by the first non-nil
// registration, not the last call (first-wins, not last-call-wins). A
// typed-nil resolver value is treated exactly like nil.
//
// Unlike WithLogger and WithTelemetry, which are last-call-wins,
// WithTenantResolver deliberately is not: a TenantResolver is a security
// boundary and registering more than one non-nil resolver must not depend
// on Option application order. NewEngine rejects two or more non-nil
// registrations with ErrAmbiguousTenantResolver rather than silently
// picking one.
//
// Known limitation: a Saga step dispatched through NoSender resets
// context.Context (getsyntegrity/urd#54) and therefore loses any
// TenantContext SendCommand attached upstream. In tenant-aware mode this
// fails closed at the actor's pre-handler gate (T4-A) rather than silently
// running without an identity, but a saga cannot currently complete a
// tenant-aware command on its own — see saga_test.go for the documented
// end-to-end demonstration. This will be resolved once #54 lets a saga
// reconstruct and reattach a TenantContext from carried Metadata.
func WithTenantResolver(resolver tenancy.TenantResolver) Option {
	return OptionFunc(func(c *Config) {
		if isNilResolver(resolver) {
			return
		}
		c.tenantResolverCount++
		if c.tenantResolver == nil {
			c.tenantResolver = resolver
		}
	})
}

// WithActorNamespace separates entity addresses of engines sharing an
// ActorSystem. Use the same stable value on all nodes of one engine. It does
// not partition persisted data: stores and scopes remain the data boundary.
// The empty default preserves the addresses of existing applications.
func WithActorNamespace(namespace string) Option {
	return OptionFunc(func(c *Config) { c.actorNamespace = namespace })
}
