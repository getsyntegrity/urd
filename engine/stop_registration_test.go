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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/tenancy"
)

// latch blocks one goroutine at a chosen point until the test releases it. The
// test learns the goroutine is parked from entered, so no sleep or timeout
// decides the interleaving.
type latch struct {
	armed   atomic.Bool
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func newLatch() *latch {
	return &latch{entered: make(chan struct{}), release: make(chan struct{})}
}

// wait parks the caller while the latch is armed.
func (l *latch) wait() {
	if !l.armed.Load() {
		return
	}
	l.once.Do(func() { close(l.entered) })
	<-l.release
}

// latchedResolver is a tenant resolver fixed to acme whose FixedTenant blocks on
// a latch: user code every registration method runs after its Started() check
// and before it takes engine.mutex.
type latchedResolver struct {
	perCallerTenantResolver
	gate *latch
}

func (r *latchedResolver) FixedTenant() (tenancy.TenantID, bool) {
	r.gate.wait()
	return "acme", true
}

// stopEventPublisher counts Close calls, and blocks in ID() on a latch.
type stopEventPublisher struct {
	id     string
	gate   *latch
	closes atomic.Int32
}

func (p *stopEventPublisher) ID() string {
	if p.gate != nil {
		p.gate.wait()
	}
	return p.id
}
func (p *stopEventPublisher) Publish(context.Context, *egopb.Event) error { return nil }
func (p *stopEventPublisher) Close(context.Context) error                 { p.closes.Add(1); return nil }

type stopStatePublisher struct {
	id     string
	gate   *latch
	closes atomic.Int32
}

func (p *stopStatePublisher) ID() string {
	if p.gate != nil {
		p.gate.wait()
	}
	return p.id
}
func (p *stopStatePublisher) Publish(context.Context, *egopb.DurableState) error { return nil }
func (p *stopStatePublisher) Close(context.Context) error                        { p.closes.Add(1); return nil }

// blockingStream is a scoped stream whose AddSubscriber blocks on a latch: code
// the register path runs INSIDE engine.mutex.
type blockingStream struct {
	eventstream.ScopedStream
	gate *latch
}

func (s *blockingStream) AddSubscriber() eventstream.Subscriber {
	s.gate.wait()
	return s.ScopedStream.AddSubscriber()
}

// registration is one way to register a publisher or subscriber: it returns the
// error of the call and a cleanup-visible handle.
type registration struct {
	name string
	call func(engine *Engine, ev *stopEventPublisher, st *stopStatePublisher) (eventstream.Subscriber, error)
}

var registrations = []registration{
	{"AddEventPublishers", func(e *Engine, ev *stopEventPublisher, _ *stopStatePublisher) (eventstream.Subscriber, error) {
		return nil, e.AddEventPublishers(ev)
	}},
	{"AddEventPublishersForTenant", func(e *Engine, ev *stopEventPublisher, _ *stopStatePublisher) (eventstream.Subscriber, error) {
		return nil, e.AddEventPublishersForTenant("acme", ev)
	}},
	{"AddStatePublishers", func(e *Engine, _ *stopEventPublisher, st *stopStatePublisher) (eventstream.Subscriber, error) {
		return nil, e.AddStatePublishers(st)
	}},
	{"AddStatePublishersForTenant", func(e *Engine, _ *stopEventPublisher, st *stopStatePublisher) (eventstream.Subscriber, error) {
		return nil, e.AddStatePublishersForTenant("acme", st)
	}},
	{"Subscribe", func(e *Engine, _ *stopEventPublisher, _ *stopStatePublisher) (eventstream.Subscriber, error) {
		sub, err := e.Subscribe()
		return sub, err
	}},
	{"SubscribeForTenant", func(e *Engine, _ *stopEventPublisher, _ *stopStatePublisher) (eventstream.Subscriber, error) {
		sub, err := e.SubscribeForTenant("acme")
		return sub, err
	}},
}

type registrationResult struct {
	err error
	sub eventstream.Subscriber
}

// startRegistration runs r on its own goroutine and returns the channel its
// result arrives on.
func startRegistration(engine *Engine, r registration, ev *stopEventPublisher, st *stopStatePublisher) <-chan registrationResult {
	out := make(chan registrationResult, 1)
	go func() {
		sub, err := r.call(engine, ev, st)
		out <- registrationResult{err, sub}
	}()
	return out
}

// TestStopVersusRegistration pins the mutual exclusion of Engine.Stop and the
// publisher and subscriber registration methods (EGO-TENANT-005 follow-up).
//
// Contract under test: a registration that returns nil before Stop takes the
// engine lock is closed by Stop exactly once; a registration that has not
// yet been accepted when Stop runs, or that is called after it, returns
// ErrEngineNotStarted and registers nothing. Never a publisher registered after
// Stop returns.
func TestStopVersusRegistration(t *testing.T) {
	specs.Describe(t, "Engine.Stop and registration exclude each other", func(s *specs.Spec) {
		bg := context.Background()

		// Every method checks Started() in its public body, then runs user code
		// (the tenant resolver) before taking the lock. Stop runs to completion
		// while the call is parked there; releasing it must not register anything.
		specs.Table(s, registrations, func(r registration) string { return r.name },
			func(ctx *specs.Context, r registration) {
				gate := newLatch()
				engine := startEngine(ctx, "StopVsRegistrationResolver", connectedEventsStore(ctx),
					WithTenantResolver(&latchedResolver{gate: gate}))
				ev, st := &stopEventPublisher{id: "ev"}, &stopStatePublisher{id: "st"}

				gate.armed.Store(true)
				result := startRegistration(engine, r, ev, st)
				<-gate.entered // the call passed Started() and is parked before the lock

				ctx.Expect(engine.Stop(bg)).To(specs.BeNil())
				close(gate.release)
				got := <-result

				ctx.Expect(got.err).To(specs.MatchError(ErrEngineNotStarted))
				ctx.Expect(got.sub).To(specs.BeNil())
				ctx.Expect(engine.eventsStreams.Len()).To(specs.Equal(0))
				ctx.Expect(engine.statesStreams.Len()).To(specs.Equal(0))
				ctx.Expect(ev.closes.Load()).To(specs.Equal(int32(0)))
				ctx.Expect(st.closes.Load()).To(specs.Equal(int32(0)))
			})

		// Same, with the park inside the publisher's own ID(), which the register
		// path called under engine.mutex before the fix.
		type idRow struct {
			name   string
			state  bool
			tenant bool
		}
		specs.Table(s, []idRow{
			{"AddEventPublishers", false, false},
			{"AddEventPublishersForTenant", false, true},
			{"AddStatePublishers", true, false},
			{"AddStatePublishersForTenant", true, true},
		}, func(r idRow) string { return "ID() parked: " + r.name }, func(ctx *specs.Context, r idRow) {
			resolver, err := tenancy.WithSingleTenant("acme")
			ctx.Expect(err).To(specs.BeNil())
			engine := startEngine(ctx, "StopVsRegistrationID", connectedEventsStore(ctx), WithTenantResolver(resolver))
			gate := newLatch()
			ev := &stopEventPublisher{id: "ev", gate: gate}
			st := &stopStatePublisher{id: "st", gate: gate}

			gate.armed.Store(true)
			result := make(chan error, 1)
			go func() {
				switch {
				case r.state && r.tenant:
					result <- engine.AddStatePublishersForTenant("acme", st)
				case r.state:
					result <- engine.AddStatePublishers(st)
				case r.tenant:
					result <- engine.AddEventPublishersForTenant("acme", ev)
				default:
					result <- engine.AddEventPublishers(ev)
				}
			}()
			<-gate.entered

			ctx.Expect(engine.Stop(bg)).To(specs.BeNil())
			close(gate.release)

			ctx.Expect(<-result).To(specs.MatchError(ErrEngineNotStarted))
			ctx.Expect(engine.eventsStreams.Len()).To(specs.Equal(0))
			ctx.Expect(engine.statesStreams.Len()).To(specs.Equal(0))
			ctx.Expect(ev.closes.Load()).To(specs.Equal(int32(0)))
			ctx.Expect(st.closes.Load()).To(specs.Equal(int32(0)))
		})

		// A registration already INSIDE the lock (parked in AddSubscriber of the
		// stream) when Stop is called is accepted, so Stop must close it, once.
		specs.Table(s, []registration{registrations[0], registrations[1], registrations[2], registrations[3]},
			func(r registration) string { return "accepted before Stop is closed once: " + r.name },
			func(ctx *specs.Context, r registration) {
				gate := newLatch()
				stream := &blockingStream{ScopedStream: eventstream.New().(eventstream.ScopedStream), gate: gate}
				resolver, err := tenancy.WithSingleTenant("acme")
				ctx.Expect(err).To(specs.BeNil())
				engine := startEngine(ctx, "StopVsRegistrationAccepted", connectedEventsStore(ctx),
					WithTenantResolver(resolver), WithEventStream(stream))
				ev, st := &stopEventPublisher{id: "ev"}, &stopStatePublisher{id: "st"}

				gate.armed.Store(true)
				result := startRegistration(engine, r, ev, st)
				<-gate.entered // inside the lock, past the started check

				stopped := make(chan error, 1)
				go func() { stopped <- engine.Stop(bg) }()
				close(gate.release)

				ctx.Expect((<-result).err).To(specs.BeNil())
				ctx.Expect(<-stopped).To(specs.BeNil())

				ctx.Expect(ev.closes.Load() + st.closes.Load()).To(specs.Equal(int32(1)))
				ctx.Expect(engine.eventsStreams.Len()).To(specs.Equal(0))
				ctx.Expect(engine.statesStreams.Len()).To(specs.Equal(0))
			})

		// Subscribe and SubscribeForTenant, parked inside subscribeForScope (the
		// stream's AddSubscriber runs while engine.mutex.RLock is held, after the
		// locked Started check), are accepted before Stop: Stop must terminate the
		// subscriber they return, and none may stay active once Stop returns.
		//
		// Honesty note: on the code before the fix these cases are racy (they pass
		// or fail with the scheduling of Stop's goroutine, since the old Stop took
		// no lock). They are guards for the contract, not proof of the fix; the
		// deterministic proof is the two "parked" groups above.
		specs.Table(s, []registration{registrations[4], registrations[5]},
			func(r registration) string { return "accepted before Stop is terminated: " + r.name },
			func(ctx *specs.Context, r registration) {
				gate := newLatch()
				stream := &blockingStream{ScopedStream: eventstream.New().(eventstream.ScopedStream), gate: gate}
				resolver, err := tenancy.WithSingleTenant("acme")
				ctx.Expect(err).To(specs.BeNil())
				engine := startEngine(ctx, "StopVsSubscriptionAccepted", connectedEventsStore(ctx),
					WithTenantResolver(resolver), WithEventStream(stream))

				gate.armed.Store(true)
				result := startRegistration(engine, r, nil, nil)
				<-gate.entered // inside subscribeForScope, past the started check

				stopped := make(chan error, 1)
				go func() { stopped <- engine.Stop(bg) }()
				close(gate.release)

				got := <-result
				ctx.Expect(got.err).To(specs.BeNil())
				ctx.Expect(got.sub).To(specs.Not(specs.BeNil()))
				ctx.Expect(<-stopped).To(specs.BeNil())

				// the subscriber in flight was terminated by Stop, and the stream,
				// closed by Stop, holds no active subscriber on either topic
				ctx.Expect(got.sub.Active()).To(specs.BeFalse())
				ctx.Expect(stream.SubscribersCount(protocol.EventsTopic)).To(specs.Equal(0))
				ctx.Expect(stream.SubscribersCount(protocol.StatesTopic)).To(specs.Equal(0))
				// a second Stop neither panics nor changes that
				ctx.Expect(engine.Stop(bg)).To(specs.BeNil())
				ctx.Expect(got.sub.Active()).To(specs.BeFalse())
			})

		// After Stop, every entry point answers explicitly and registers nothing.
		specs.Table(s, registrations, func(r registration) string { return "after Stop: " + r.name },
			func(ctx *specs.Context, r registration) {
				resolver, err := tenancy.WithSingleTenant("acme")
				ctx.Expect(err).To(specs.BeNil())
				engine := startEngine(ctx, "StopThenRegister", connectedEventsStore(ctx), WithTenantResolver(resolver))
				ctx.Expect(engine.Stop(bg)).To(specs.BeNil())
				ev, st := &stopEventPublisher{id: "ev"}, &stopStatePublisher{id: "st"}

				sub, err := r.call(engine, ev, st)

				ctx.Expect(err).To(specs.MatchError(ErrEngineNotStarted))
				ctx.Expect(sub).To(specs.BeNil())
				ctx.Expect(engine.eventsStreams.Len()).To(specs.Equal(0))
				ctx.Expect(engine.statesStreams.Len()).To(specs.Equal(0))
			})

		s.It("Stop twice closes each publisher once and ends the subscriber", func(ctx *specs.Context) {
			resolver, err := tenancy.WithSingleTenant("acme")
			ctx.Expect(err).To(specs.BeNil())
			engine := startEngine(ctx, "StopTwice", connectedEventsStore(ctx), WithTenantResolver(resolver))
			ev, st := &stopEventPublisher{id: "ev"}, &stopStatePublisher{id: "st"}
			ctx.Expect(engine.AddEventPublishers(ev)).To(specs.BeNil())
			ctx.Expect(engine.AddStatePublishersForTenant("acme", st)).To(specs.BeNil())
			sub, err := engine.SubscribeForTenant("acme")
			ctx.Expect(err).To(specs.BeNil())

			ctx.Expect(engine.Stop(bg)).To(specs.BeNil())
			ctx.Expect(engine.Stop(bg)).To(specs.BeNil())

			ctx.Expect(ev.closes.Load()).To(specs.Equal(int32(1)))
			ctx.Expect(st.closes.Load()).To(specs.Equal(int32(1)))
			ctx.Expect(sub.Active()).To(specs.BeFalse())
		})

		s.It("concurrent Stops close each publisher exactly once", func(ctx *specs.Context) {
			resolver, err := tenancy.WithSingleTenant("acme")
			ctx.Expect(err).To(specs.BeNil())
			engine := startEngine(ctx, "StopConcurrent", connectedEventsStore(ctx), WithTenantResolver(resolver))
			ev, st := &stopEventPublisher{id: "ev"}, &stopStatePublisher{id: "st"}
			ctx.Expect(engine.AddEventPublishers(ev)).To(specs.BeNil())
			ctx.Expect(engine.AddStatePublishers(st)).To(specs.BeNil())

			const stops = 4
			errs := make(chan error, stops)
			var wg sync.WaitGroup
			wg.Add(stops)
			for range stops {
				go func() { defer wg.Done(); errs <- engine.Stop(bg) }()
			}
			wg.Wait()
			close(errs)

			for err := range errs {
				ctx.Expect(err).To(specs.BeNil())
			}
			ctx.Expect(ev.closes.Load()).To(specs.Equal(int32(1)))
			ctx.Expect(st.closes.Load()).To(specs.Equal(int32(1)))
		})
	})
}
