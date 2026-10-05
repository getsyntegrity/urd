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

// Package goakt is Urd's composition root for the GoAkt runtime
// (openspec/changes/ego-arch-003/design.md). New validates a compose.Spec
// and builds an App without starting anything; App.Start builds and starts,
// in a fixed order, everything the Spec describes; App.Stop releases it in
// the reverse order. Import it under an alias such as urdakt, so it does not
// clash with the GoAkt module itself:
//
//	app, err := urdakt.New(compose.Spec{
//		Name:        "Sample",
//		Families:    compose.EventSourced,
//		EventsStore: eventStore,
//	}, urdakt.WithLogger(logger))
//	if err != nil {
//		// static validation failed; nothing was started
//	}
//	defer app.Stop(ctx) // required once New succeeded
//	if err := app.Start(ctx); err != nil {
//		// a *compose.StartError names the failed step; rollback already ran
//	}
//	err = app.Runtime().SpawnEventSourced(ctx, behavior)
//
// Dependencies are injected explicitly (design §D2): the consumer builds
// every store and publisher and places it in a named Spec field. There is
// no registry, no lookup by type and no reflection-based wiring.
//
// Ownership (design §D5): stores stay the consumer's, who connects them
// before New and closes them after Stop; the App only pings them. The App
// owns the actor system, the engine and the event stream it creates, and,
// once New succeeds, every publisher in the Spec: it closes them on every
// terminal path (Stop, a failed Start, and Stop on an App that never
// started). So once New returns an App, always call Stop and never close a
// publisher yourself.
//
// The manual path — engine.NewConfig, goakt.NewActorSystem, engine.NewEngine —
// stays supported for deployments that need GoAkt settings this package
// does not expose; WithActorSystemOptions covers most of them.
package goakt

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	actor "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/remote"

	"github.com/getsyntegrity/urd/compose"
	"github.com/getsyntegrity/urd/compose/internal/adapters"
	"github.com/getsyntegrity/urd/compose/internal/lifecycle"
	"github.com/getsyntegrity/urd/engine"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/port/adapter"
	runtimeport "github.com/getsyntegrity/urd/port/runtime"
)

// The names of App.Start's five steps (design §D6). A failed Start returns
// a *compose.StartError whose Step is one of them.
const (
	// StepProbeStores pings every configured store.
	StepProbeStores = "probe stores"
	// StepStartActorSystem allocates the event stream, builds the engine's
	// configuration and creates and starts the GoAkt actor system.
	StepStartActorSystem = "start actor system"
	// StepStartEngine creates and starts the engine.Engine.
	StepStartEngine = "start engine"
	// StepAttachPublishers starts and probes the Spec's events and state
	// publishers that implement adapter.Starter or adapter.Pinger, then
	// attaches them.
	StepAttachPublishers = "attach publishers"
	// StepStartProjections starts every projection in the Spec.
	StepStartProjections = "start projections"
)

// ErrNotStartable is returned by App.Start when the App was already
// started, stopped, or failed to start: an App is single-use.
var ErrNotStartable = errors.New("compose/goakt: app cannot be started: an App is single-use")

// App is a GoAkt deployment built from a compose.Spec. It moves once
// through new, starting, running, stopping and stopped, or ends failed when
// Start fails; Start and Stop are serialized, so concurrent calls do not
// interleave.
type App struct {
	spec  compose.Spec
	opts  options
	seq   *lifecycle.Sequence
	hooks hooks

	// published is the engine Engine returns: set once Start succeeded.
	published atomic.Pointer[engine.Engine]

	// The fields below are only touched by the start steps, their undos
	// and the release function, which the lifecycle sequence runs one at
	// a time under its own mutex.
	stream         eventstream.Stream
	config         *engine.Config
	sys            actor.ActorSystem
	engine         *engine.Engine
	eventsAttached bool
	statesAttached bool
	projections    []string // started projections, in start order
}

// hooks are test seams. newEventStream allocates the stream step 2 hands to
// the engine; afterStep, when set, runs at the end of each start step, after
// the step did its work, and its error fails the step, which must then undo
// its own work.
type hooks struct {
	newEventStream func() eventstream.Stream
	afterStep      func(step string) error
}

// New validates spec and opts and returns an App ready to Start. It does no
// I/O and starts nothing (design §D4a). It runs spec.Validate (V1–V8) and
// the GoAkt rules G1 (WithCluster needs a configuration and at least one
// entity kind) and G2 (spec.Name is a valid GoAkt actor-system name), and
// returns every problem at once, joined with errors.Join.
//
// When New fails the consumer keeps owning the publishers in spec. When it
// succeeds they belong to the App, and the consumer must call Stop.
func New(spec compose.Spec, opts ...Option) (*App, error) {
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	var errs []error
	if err := spec.Validate(); err != nil {
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			errs = append(errs, joined.Unwrap()...)
		} else {
			errs = append(errs, err)
		}
	}
	errs = append(errs, o.validate(spec)...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	app := &App{
		spec:  spec,
		opts:  o,
		hooks: hooks{newEventStream: eventstream.New},
	}
	// Copy the slices the App iterates later, so a consumer that changes
	// its own Spec afterwards does not change what the App owns.
	app.spec.EventPublishers = slices.Clone(spec.EventPublishers)
	app.spec.StatePublishers = slices.Clone(spec.StatePublishers)

	seq, err := lifecycle.New(lifecycle.Config{
		Steps: []lifecycle.Step{
			{Name: StepProbeStores, Start: app.probeStores},
			{Name: StepStartActorSystem, Start: app.startActorSystem, Stop: app.stopActorSystem},
			{Name: StepStartEngine, Start: app.startEngine, Stop: app.stopEngine},
			{Name: StepAttachPublishers, Start: app.attachPublishers},
			{Name: StepStartProjections, Start: app.startProjections, Stop: app.stopProjections},
		},
		Release:         app.releasePublishers,
		ShutdownTimeout: spec.ShutdownTimeout,
	})
	if err != nil {
		// Unreachable after Validate (V7 rejects a negative timeout) and
		// with the fixed steps above; kept so a future change fails here.
		return nil, err
	}
	app.seq = seq
	return app, nil
}

// Start runs the five start steps in order (design §D6): probe the stores,
// start the actor system, start the engine, attach the publishers, start
// the projections. Before each step it checks ctx; a done context fails the
// step about to run.
//
// When a step fails, Start undoes the steps that already ran, in reverse,
// then closes every publisher not yet attached, and returns a
// *compose.StartError naming the step. Cleanup runs under
// context.WithoutCancel(ctx) bounded by Spec.ShutdownTimeout (30s when
// zero), so it also runs when ctx was cancelled. The App is then failed:
// Stop is a no-op and Start returns ErrNotStartable.
//
// Start returns ErrNotStartable unless the App is new.
func (a *App) Start(ctx context.Context) error {
	if err := a.seq.Start(ctx); err != nil {
		if errors.Is(err, lifecycle.ErrNotStartable) {
			return fmt.Errorf("%w (%s)", ErrNotStartable, a.seq.State())
		}
		return err
	}
	a.published.Store(a.engine)
	return nil
}

// Stop releases everything Start started (design §D7), under the same
// cleanup context as a failed Start's rollback: it stops the projections,
// then the engine — which from then on refuses new commands, and closes the
// publishers and the event stream — then the actor system. Every step is
// attempted even when an earlier one fails; the errors are joined.
//
// On an App that was never started, Stop only closes the publishers it
// received. Stop after Stop, or after a failed Start, is a no-op. Stores
// are never closed: they belong to the consumer.
//
// Commands are still admitted while projections stop, until the engine
// stops. Anything an actor emits while the actor system stops is not
// delivered to publishers, which closed before; see design §D7 and #24.
func (a *App) Stop(ctx context.Context) error {
	return a.seq.Stop(ctx)
}

// Engine returns the running engine, to spawn entities and send commands
// through. It returns nil until Start has succeeded, and nil for good after
// a failed Start. After Stop it returns the stopped engine, which refuses
// work with engine.ErrEngineNotStarted.
func (a *App) Engine() *engine.Engine {
	return a.published.Load()
}

// Runtime returns the running application through the runtime-neutral
// contract: code written against it runs unchanged on any runtime's
// composition root. It returns nil until Start has succeeded and for good
// after a failed Start; after Stop it returns the stopped runtime, which
// refuses work with runtimeport.ErrEngineNotStarted. It is the same engine
// Engine returns.
func (a *App) Runtime() runtimeport.Runtime {
	// A nil *engine.Engine stored in the interface would not compare equal to
	// nil; return an untyped nil instead (ego-runtime-001 §D6).
	if engine := a.published.Load(); engine != nil {
		return engine
	}
	return nil
}

// probeStores is step 1: it pings every configured store and names each
// one that fails. Ping is part of every store port (CapReady is implied),
// so adapter.PingerOf finds it on every store that is set; a store left
// out is skipped. The stores stay the consumer's: this step only pings.
func (a *App) probeStores(ctx context.Context) error {
	stores := []struct {
		name  string
		store any
	}{
		{"EventsStore", a.spec.EventsStore},
		{"StateStore", a.spec.StateStore},
		{"SnapshotStore", a.spec.SnapshotStore},
		{"OffsetStore", a.spec.OffsetStore},
	}
	var errs []error
	for _, s := range stores {
		pinger, ok := adapter.PingerOf(s.store)
		if !ok {
			continue
		}
		if err := pinger.Ping(ctx); err != nil {
			errs = append(errs, fmt.Errorf("compose/goakt: ping %s: %w", s.name, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return a.afterStep(StepProbeStores)
}

// startActorSystem is step 2. It allocates the event stream, builds the
// engine's configuration with it, and creates and starts the actor system.
// When it fails after allocating anything, it releases that itself — the
// lifecycle never undoes the step that failed: a started actor system is
// stopped and the stream is closed. GoAkt's own Start already cleans up
// after a failed start.
func (a *App) startActorSystem(ctx context.Context) error {
	stream := a.hooks.newEventStream()
	engineOpts := append(a.opts.engineOptions(a.spec), engine.WithEventStream(stream))
	config := engine.NewConfig(a.spec.EventsStore, engineOpts...)

	actorOpts := config.GoaktOptions()
	if a.opts.cluster != nil {
		actorOpts = append(actorOpts, actor.WithCluster(a.opts.cluster.WithKinds(engine.ClusterKinds()...)))
	}
	actorOpts = append(actorOpts, a.opts.actorOptions...)
	if r := a.opts.remoting; r != nil {
		// Last, so that WithRemoting wins over an actor.WithRemote passed through
		// WithActorSystemOptions (see WithRemoting).
		actorOpts = append(actorOpts, actor.WithRemote(remotingConfig(config, r)))
	}

	sys, err := actor.NewActorSystem(a.spec.Name, actorOpts...)
	if err != nil {
		stream.Close()
		return fmt.Errorf("compose/goakt: create actor system: %w", err)
	}
	if err := sys.Start(ctx); err != nil {
		stream.Close()
		return fmt.Errorf("compose/goakt: start actor system: %w", err)
	}
	a.stream, a.config, a.sys = stream, config, sys

	if err := a.afterStep(StepStartActorSystem); err != nil {
		cleanupCtx, cancel := a.cleanupContext(ctx)
		defer cancel()
		return errors.Join(err, a.stopActorSystem(cleanupCtx))
	}
	return nil
}

// stopActorSystem undoes step 2: it stops the actor system, then closes the
// event stream. Once the engine exists its Stop has already closed the
// stream; closing it again is a no-op.
func (a *App) stopActorSystem(ctx context.Context) error {
	var err error
	if a.sys != nil && a.sys.Running() {
		if stopErr := a.sys.Stop(ctx); stopErr != nil {
			err = fmt.Errorf("compose/goakt: stop actor system: %w", stopErr)
		}
	}
	if a.stream != nil {
		a.stream.Close()
	}
	return err
}

// startEngine is step 3: it plugs an engine.Engine into the running actor
// system and starts it. Should it fail after the engine started, it stops
// the engine itself.
func (a *App) startEngine(ctx context.Context) error {
	eng, err := engine.NewEngine(a.sys, a.config)
	if err != nil {
		return fmt.Errorf("compose/goakt: create engine: %w", err)
	}
	if err := eng.Start(ctx); err != nil {
		return fmt.Errorf("compose/goakt: start engine: %w", err)
	}
	a.engine = eng

	if err := a.afterStep(StepStartEngine); err != nil {
		cleanupCtx, cancel := a.cleanupContext(ctx)
		defer cancel()
		return errors.Join(err, a.stopEngine(cleanupCtx))
	}
	return nil
}

// stopEngine undoes step 3. Engine.Stop refuses new work, then closes every
// attached publisher and the event stream.
func (a *App) stopEngine(ctx context.Context) error {
	if a.engine == nil {
		return nil
	}
	if err := a.engine.Stop(ctx); err != nil {
		return fmt.Errorf("compose/goakt: stop engine: %w", err)
	}
	return nil
}

// attachPublishers is step 4 (ego-arch-004 design §D4). It first starts
// and probes every publisher, in Spec order: for each one, Start when it
// implements adapter.Starter, then Ping when it implements adapter.Pinger.
// Only then does it attach each kind in one call, which the engine accepts
// or rejects as a whole. Attached publishers are closed by the engine's
// Stop; those never attached — started or not, including one whose Start
// or Ping failed — are closed by releasePublishers. The step has nothing
// of its own to undo.
func (a *App) attachPublishers(ctx context.Context) error {
	if err := adapters.StartAndProbe(ctx, a.ownedPublishers()); err != nil {
		return fmt.Errorf("compose/goakt: %w", err)
	}
	if len(a.spec.EventPublishers) > 0 {
		if err := a.engine.AddEventPublishers(a.spec.EventPublishers...); err != nil {
			return fmt.Errorf("compose/goakt: attach events publishers: %w", err)
		}
		a.eventsAttached = true
	}
	if len(a.spec.StatePublishers) > 0 {
		if err := a.engine.AddStatePublishers(a.spec.StatePublishers...); err != nil {
			return fmt.Errorf("compose/goakt: attach state publishers: %w", err)
		}
		a.statesAttached = true
	}
	return a.afterStep(StepAttachPublishers)
}

// ownedPublishers lists the Spec's publishers in Spec order, events
// publishers first, named as step 4's errors name them.
func (a *App) ownedPublishers() []adapters.Owned {
	owned := make([]adapters.Owned, 0, len(a.spec.EventPublishers)+len(a.spec.StatePublishers))
	for _, p := range a.spec.EventPublishers {
		owned = append(owned, adapters.Owned{Kind: "events publisher", ID: p.ID(), Value: p})
	}
	for _, p := range a.spec.StatePublishers {
		owned = append(owned, adapters.Owned{Kind: "state publisher", ID: p.ID(), Value: p})
	}
	return owned
}

// startProjections is step 5: it starts every projection in the Spec, in
// name order. Should it fail, it stops the projections it already started.
func (a *App) startProjections(ctx context.Context) error {
	var err error
	for _, name := range projectionNames(a.spec) {
		if err = a.engine.StartProjection(ctx, name); err != nil {
			err = fmt.Errorf("compose/goakt: start projection %q: %w", name, err)
			break
		}
		a.projections = append(a.projections, name)
	}
	if err == nil {
		err = a.afterStep(StepStartProjections)
	}
	if err != nil {
		cleanupCtx, cancel := a.cleanupContext(ctx)
		defer cancel()
		return errors.Join(err, a.stopProjections(cleanupCtx))
	}
	return nil
}

// stopProjections undoes step 5, stopping the started projections in
// reverse order and attempting every one.
func (a *App) stopProjections(ctx context.Context) error {
	var errs []error
	for _, name := range slices.Backward(a.projections) {
		if err := a.engine.StopProjection(ctx, name); err != nil {
			errs = append(errs, fmt.Errorf("compose/goakt: stop projection %q: %w", name, err))
		}
	}
	a.projections = nil
	return errors.Join(errs...)
}

// releasePublishers closes every publisher step 4 did not attach: all of
// them when Start failed before step 4 or the App never started.
func (a *App) releasePublishers(ctx context.Context) error {
	var errs []error
	if !a.eventsAttached {
		for _, p := range a.spec.EventPublishers {
			if err := p.Close(ctx); err != nil {
				errs = append(errs, fmt.Errorf("compose/goakt: close events publisher %q: %w", p.ID(), err))
			}
		}
	}
	if !a.statesAttached {
		for _, p := range a.spec.StatePublishers {
			if err := p.Close(ctx); err != nil {
				errs = append(errs, fmt.Errorf("compose/goakt: close state publisher %q: %w", p.ID(), err))
			}
		}
	}
	return errors.Join(errs...)
}

// afterStep runs the afterStep test hook, if any.
func (a *App) afterStep(step string) error {
	if a.hooks.afterStep == nil {
		return nil
	}
	return a.hooks.afterStep(step)
}

// cleanupContext is the context a failing step undoes its own work under:
// the same shape as the lifecycle's rollback context, caller values kept,
// caller cancellation dropped, bounded by the shutdown timeout.
func (a *App) cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := a.spec.ShutdownTimeout
	if timeout == 0 {
		timeout = lifecycle.DefaultShutdownTimeout
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// remotingConfig builds the remoting configuration of WithRemoting. The
// engine's own options come after the caller's, so the tenant propagator of a
// tenant-aware engine replaces any remote.WithContextPropagator the caller
// passed (GoAkt keeps the last one).
func remotingConfig(config *engine.Config, r *remotingSpec) *remote.Config {
	opts := append(append([]remote.Option{}, r.opts...), config.RemoteOptions()...)
	return remote.NewConfig(r.host, r.port, opts...)
}
