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
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	kitlog "github.com/pablogore/kit-logger/pkg/logger"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"
	"google.golang.org/protobuf/proto"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/goaktlog"
	"github.com/getsyntegrity/urd/persistence"
	behaviorport "github.com/getsyntegrity/urd/port/behavior"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

// The component cases in this package start a real goakt actor system, so they
// cannot be unit tests. They still must not sleep: every wait polls an
// observable condition with ctx.Eventually, bounded by these limits.
const (
	pollTimeout  = 5 * time.Second
	pollInterval = 10 * time.Millisecond
	askTimeout   = 5 * time.Second
)

// durableBehavior is a DurableState behavior that can also travel as a spawn
// dependency, which every behavior the test suite builds does.
type durableBehavior interface {
	behaviorport.DurableState
	extension.Dependency
}

// actorRig is a goakt actor system wired with the durable state extensions and
// started for one go-specs case. Its teardown is registered with ctx.Cleanup,
// so a case never stops the system by hand.
type actorRig struct {
	system goakt.ActorSystem
	stream eventstream.Stream
}

// startActorRig starts an actor system whose durable state store is store. extra
// carries the extensions a case adds, for example extensions.NewTenancyMarker(false).
func startActorRig(ctx *specs.Context, store persistence.StateStore, extra ...extension.Extension) *actorRig {
	bg := context.Background()
	stream := eventstream.New()
	guardian := newGuardianStartedHandler()

	exts := append([]extension.Extension{
		extensions.NewDurableStateStore(store),
		extensions.NewEventsStream(stream),
	}, extra...)

	system, err := goakt.NewActorSystem("TestActorSystem",
		goakt.WithLogger(goaktlog.New(kitlog.New(kitlog.Config{Sink: guardian}))),
		goakt.WithExtensions(exts...),
		goakt.WithActorInitMaxRetries(3))
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(system.Start(bg)).To(specs.BeNil())

	ctx.Cleanup(func() {
		stream.Close()
		if stopErr := system.Stop(bg); stopErr != nil {
			ctx.Errorf("stopping the actor system: %v", stopErr)
		}
	})
	guardian.await(ctx)
	return &actorRig{system: system, stream: stream}
}

// guardianStartedHandler is the logging sink of a rig's actor system. It lets
// the rig wait until the system's user guardian has handled its PostStart
// message. goakt (v4.5.4) delivers PostStart to the guardian asynchronously and
// the guardian reads the logger that message installs when it handles a
// Terminated message, so an actor that stops before the guardian is up (a
// restart or a kill right after the system starts) panics the guardian with a
// nil dereference. The root guardian then shuts the whole system down, and a
// restarting actor fails PreStart with "EgoStatesStoreExtension is not
// registered". The guardian logs "started successfully" only after installing
// the logger, so that record is the observable signal that it is safe to stop
// actors. Everything is dropped, as with enginetest.DiscardLogger.
type guardianStartedHandler struct {
	once    sync.Once
	started chan struct{}
}

func newGuardianStartedHandler() *guardianStartedHandler {
	return &guardianStartedHandler{started: make(chan struct{})}
}

func (h *guardianStartedHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h *guardianStartedHandler) Handle(_ context.Context, record slog.Record) error {
	if strings.Contains(record.Message, "GoAktUserGuardian started successfully") {
		h.once.Do(func() { close(h.started) })
	}
	return nil
}

func (h *guardianStartedHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *guardianStartedHandler) WithGroup(string) slog.Handler { return h }

// await fails the case unless the user guardian reports itself started within
// pollTimeout.
func (h *guardianStartedHandler) await(ctx *specs.Context) {
	ctx.Eventually(func() any {
		select {
		case <-h.started:
			return true
		default:
			return false
		}
	}, specs.BeTrue(), specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
}

// connectedDurableStore returns an in-memory testkit store that is connected
// now and disconnected when the case ends, after the actor system stops.
func connectedDurableStore(ctx *specs.Context) *testkit.DurableStore {
	bg := context.Background()
	store := testkit.NewDurableStore()
	ctx.Expect(store.Connect(bg)).To(specs.BeNil())
	ctx.Cleanup(func() {
		if err := store.Disconnect(bg); err != nil {
			ctx.Errorf("disconnecting the durable store: %v", err)
		}
	})
	return store
}

// spawn spawns a durable state actor for behavior, long lived, with the given
// extra dependencies, and waits until it reports itself running.
func (r *actorRig) spawn(ctx *specs.Context, behavior durableBehavior, deps ...extension.Dependency) *goakt.PID {
	pid, err := r.trySpawn(behavior, deps...)
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(pid).To(specs.Not(specs.BeNil()))
	waitRunning(ctx, pid)
	return pid
}

// trySpawn spawns like spawn but hands back the spawn error, for the cases that
// expect PreStart to refuse the actor.
func (r *actorRig) trySpawn(behavior durableBehavior, deps ...extension.Dependency) (*goakt.PID, error) {
	all := append([]extension.Dependency{behavior}, deps...)
	return r.system.Spawn(context.Background(), behavior.ID(), New(),
		goakt.WithDependencies(all...), goakt.WithLongLived())
}

// spawnForTenant spawns behavior bound to the named tenant, as
// Engine.DurableStateEntity does in tenant-aware mode.
func (r *actorRig) spawnForTenant(ctx *specs.Context, behavior durableBehavior, tenant string) *goakt.PID {
	return r.spawn(ctx, behavior, extensions.NewEntityTenantScope(tenant))
}

// kill stops the named actor and waits until it reports itself stopped.
// ActorSystem.Kill already returns after PostStop ran; the poll makes the
// condition the next assertion relies on explicit.
func (r *actorRig) kill(ctx *specs.Context, pid *goakt.PID, name string) {
	ctx.Expect(r.system.Kill(context.Background(), name)).To(specs.BeNil())
	waitStopped(ctx, pid)
}

// killForRestart is kill for a case that spawns the same name again. The actor
// system drops a stopped actor from its tree asynchronously, through the death
// watch, so a Spawn right after Kill can get the stale, stopped instance back
// with no error ("duplicate spawn detected ... returning the canonical
// instance"). The actor count falling by one is the public signal that the
// name was released and a respawn creates a fresh actor.
func (r *actorRig) killForRestart(ctx *specs.Context, pid *goakt.PID, name string) {
	before := r.system.NumActors()
	r.kill(ctx, pid, name)
	ctx.Eventually(func() any { return r.system.NumActors() }, specs.Equal(before-1),
		specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
}

func waitRunning(ctx *specs.Context, pid *goakt.PID) {
	ctx.Eventually(func() any { return pid.IsRunning() }, specs.BeTrue(),
		specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
}

func waitStopped(ctx *specs.Context, pid *goakt.PID) {
	ctx.Eventually(func() any { return pid.IsRunning() }, specs.BeFalse(),
		specs.WithTimeout(pollTimeout), specs.WithInterval(pollInterval))
}

// latestState reads the durable record the store holds for id in scope. A store
// error is returned as the observed value so a failing poll reports it.
func latestState(store persistence.StateStore, scope persistence.Scope, id string) func() any {
	return func() any {
		state, err := store.GetLatestState(context.Background(), scope, id)
		if err != nil {
			return err
		}
		return state
	}
}

// ask sends msg to pid and returns the CommandReply. The Ask itself must
// succeed: a rejected command is carried inside the reply.
func ask(ctx *specs.Context, callCtx context.Context, pid *goakt.PID, msg proto.Message) *egopb.CommandReply {
	reply, err := goakt.Ask(callCtx, pid, msg, askTimeout)
	ctx.Expect(err).To(specs.BeNil())
	commandReply, ok := reply.(*egopb.CommandReply)
	ctx.Expect(ok).To(specs.BeTrue())
	return commandReply
}

// isStateReply reports whether reply carries a state reply, the success shape.
func isStateReply(reply *egopb.CommandReply) bool {
	_, ok := reply.GetReply().(*egopb.CommandReply_StateReply)
	return ok
}

// errorReplyMessage returns the message of an error reply, or false when reply
// is not an error reply.
func errorReplyMessage(reply *egopb.CommandReply) (string, bool) {
	errReply, ok := reply.GetReply().(*egopb.CommandReply_ErrorReply)
	if !ok {
		return "", false
	}
	return errReply.ErrorReply.GetMessage(), true
}

// attachedTo returns a context carrying tc, as Engine.SendCommand attaches it
// at the trust boundary.
func attachedTo(ctx *specs.Context, tc tenancy.TenantContext) context.Context {
	attached, err := tenancy.Attach(context.Background(), tc)
	ctx.Expect(err).To(specs.BeNil())
	return attached
}

// bePersisted matches the value latestState observes once the store holds a
// durable record. A store error or a missing record does not match, and a
// failing poll reports the last value it saw.
func bePersisted() specs.Matcher {
	return specs.Satisfy("is a persisted durable state", func(v any) bool {
		state, ok := v.(*egopb.DurableState)
		return ok && state != nil
	})
}
