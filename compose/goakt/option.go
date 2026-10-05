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

package goakt

import (
	"errors"
	"regexp"
	"slices"

	kitlog "github.com/pablogore/kit-logger/pkg/logger"
	actor "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/remote"

	"github.com/getsyntegrity/urd/compose"
	"github.com/getsyntegrity/urd/engine"
)

var (
	// ErrClusterConfigRequired is reported by New when WithCluster is given
	// a nil cluster configuration (rule G1).
	ErrClusterConfigRequired = errors.New("compose/goakt: WithCluster needs a non-nil cluster configuration (G1)")
	// ErrClusterKindsRequired is reported by New when WithCluster is given
	// no entity kinds (rule G1): a node could not rebuild a behavior a peer
	// places on it, so the first remote spawn would fail.
	ErrClusterKindsRequired = errors.New("compose/goakt: WithCluster needs at least one entity kind (G1)")
)

// Option configures the GoAkt-specific settings of an App: the parts a
// runtime-neutral compose.Spec deliberately does not carry (design §D2).
type Option func(*options)

type options struct {
	logger       kitlog.Logger
	telemetry    *engine.Telemetry
	clusterSet   bool
	cluster      *actor.ClusterConfig
	kinds        []engine.BehaviorKind
	actorOptions []actor.Option
	remoting     *remotingSpec
}

type remotingSpec struct {
	host string
	port int
	opts []remote.Option
}

// WithLogger sets the logger the engine and the actor system log through.
// Without it, Urd's default logger is used (see engine.WithLogger).
func WithLogger(logger kitlog.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTelemetry enables OpenTelemetry instrumentation (see engine.WithTelemetry).
// The consumer keeps owning the telemetry providers. Starting the engine
// sets the process-wide OpenTelemetry propagator, as engine.Engine.Start does.
func WithTelemetry(telemetry *engine.Telemetry) Option {
	return func(o *options) { o.telemetry = telemetry }
}

// WithCluster runs the actor system in cluster mode with cfg. It registers
// engine.ClusterKinds() on cfg and the given behavior kinds with
// engine.WithBehaviorKinds, the two registrations a cluster node needs
// (design §5.1). cfg is modified when App.Start runs, not by New.
//
// kinds are behavior prototypes, one pointer per behavior type this node
// may host, for example new(AccountBehavior); at least one is required, or
// New fails with ErrClusterKindsRequired (rule G1). A value of the
// deprecated engine.EntityKind type is assignable to engine.BehaviorKind; a
// []engine.EntityKind slice has to be converted element by element.
//
// Cluster mode also needs remoting, which is passed through
// WithActorSystemOptions, for example
// WithActorSystemOptions(actor.WithRemote(remote.NewConfig(host, port))).
func WithCluster(cfg *actor.ClusterConfig, kinds ...engine.BehaviorKind) Option {
	return func(o *options) {
		o.clusterSet = true
		o.cluster = cfg
		o.kinds = append(o.kinds, kinds...)
	}
}

// WithActorSystemOptions appends GoAkt options to the ones the App derives
// from the Spec, for settings it does not model itself (remoting, TLS,
// custom extensions, supervision). They are applied after Urd's own
// options, so an option that GoAkt applies last-wins overrides Urd's value.
func WithActorSystemOptions(opts ...actor.Option) Option {
	return func(o *options) { o.actorOptions = append(o.actorOptions, opts...) }
}

// WithRemoting enables GoAkt remoting on host and port and adds the engine's
// own engine.Config.RemoteOptions, so that in a tenant-aware App the caller's
// tenant identity survives a hop to an actor hosted on another node. opts are
// further GoAkt remote options, for example remote.WithTLS: a cluster without
// mutual TLS trusts every peer that can reach its remoting port.
//
// It is the composition counterpart of
// actor.WithRemote(remote.NewConfig(host, port, cfg.RemoteOptions()...)); use
// it instead of passing actor.WithRemote through WithActorSystemOptions.
func WithRemoting(host string, port int, opts ...remote.Option) Option {
	return func(o *options) { o.remoting = &remotingSpec{host: host, port: port, opts: opts} }
}

// actorSystemName is GoAkt's own naming rule, checked by
// actor.NewActorSystem (goakt v4.5.4, actor/actor_system.go). It is
// repeated here so New can reject a bad name without constructing
// anything; a test compares both on the same names so a drift fails.
var actorSystemName = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9-_]*$")

// validate returns the GoAkt-specific problems (design §D4a, G1 and G2).
func (o *options) validate(spec compose.Spec) []error {
	var errs []error
	if o.clusterSet {
		if o.cluster == nil {
			errs = append(errs, ErrClusterConfigRequired)
		}
		if len(o.kinds) == 0 {
			errs = append(errs, ErrClusterKindsRequired)
		}
	}
	switch {
	case spec.Name == "":
		errs = append(errs, &compose.ValidationError{Rule: "G2", Field: "Name", Problem: "required: it names the GoAkt actor system"})
	case !actorSystemName.MatchString(spec.Name):
		errs = append(errs, &compose.ValidationError{Rule: "G2", Field: "Name", Problem: "not a valid GoAkt actor-system name: letters, digits, '-' and '_' only, starting with a letter or digit"})
	}
	return errs
}

// engineOptions translates the Spec and these options into the engine.Config
// options step 2 builds the engine's configuration from.
func (o *options) engineOptions(spec compose.Spec) []engine.Option {
	var opts []engine.Option
	if o.logger != nil {
		opts = append(opts, engine.WithLogger(o.logger))
	}
	if o.telemetry != nil {
		opts = append(opts, engine.WithTelemetry(o.telemetry))
	}
	if len(o.kinds) > 0 {
		opts = append(opts, engine.WithBehaviorKinds(o.kinds...))
	}
	opts = append(opts, engine.WithEntityFamilies(entityFamilies(spec.Families)))
	if spec.StateStore != nil {
		opts = append(opts, engine.WithStateStore(spec.StateStore))
	}
	if spec.SnapshotStore != nil {
		opts = append(opts, engine.WithSnapshotStore(spec.SnapshotStore))
	}
	if spec.OffsetStore != nil {
		opts = append(opts, engine.WithOffsetStore(spec.OffsetStore))
	}
	for _, name := range projectionNames(spec) {
		opts = append(opts, engine.WithProjection(name, spec.Projections[name]))
	}
	if len(spec.EventAdapters) > 0 {
		opts = append(opts, engine.WithEventAdapters(spec.EventAdapters...))
	}
	if spec.Encryptor != nil {
		opts = append(opts, engine.WithEncryptor(spec.Encryptor))
	}
	if spec.TenantResolver != nil {
		opts = append(opts, engine.WithTenantResolver(spec.TenantResolver))
	}
	return opts
}

// entityFamilies maps compose's runtime-neutral families onto the engine's.
func entityFamilies(families compose.Family) engine.EntityFamily {
	var out engine.EntityFamily
	if families&compose.EventSourced != 0 {
		out |= engine.EventSourcedFamily
	}
	if families&compose.DurableState != 0 {
		out |= engine.DurableStateFamily
	}
	if families&compose.Saga != 0 {
		out |= engine.SagaFamily
	}
	return out
}

// projectionNames returns the Spec's projection names in sorted order, so
// projections are registered and started deterministically.
func projectionNames(spec compose.Spec) []string {
	names := make([]string, 0, len(spec.Projections))
	for name := range spec.Projections {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
