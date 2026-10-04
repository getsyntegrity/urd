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

package eventsource

import (
	"context"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/internal/extensions"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

// askCreateAccount sends a CreateAccount command and expects it to succeed with a state reply.
func askCreateAccount(ctx *specs.Context, askCtx context.Context, pid *goakt.PID) {
	reply, err := goakt.Ask(askCtx, pid, &testpb.CreateAccount{AccountBalance: 500}, 5*time.Second)
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(reply).To(beOfType[*egopb.CommandReply]())
	ctx.Expect(reply.(*egopb.CommandReply).GetReply()).To(beOfType[*egopb.CommandReply_StateReply]())
}

// TestEventSourcedActorSpawnBindsExactTenantScope covers TENANT-003 T4's
// core guarantee: once PreStart binds entity.scope via resolveScope, every
// store call this actor makes, both the read during recovery and the write
// from a live command, carries the exact tenant scope resolved for this
// spawn, never persistence.Unscoped(). The store is a mock whose expectations
// name the scope explicitly, so a call made with the wrong scope is reported
// as unexpected instead of silently succeeding against some other
// (scope, persistenceID) bucket.
func TestEventSourcedActorSpawnBindsExactTenantScope(t *testing.T) {
	specs.Describe(t, "a tenant-aware actor reads and writes the store with the exact scope resolved for its spawn", func(s *specs.Spec) {
		s.It("binds the spawn-time tenant scope for recovery and for the live write", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			scopeA, err := persistence.NewTenantScope("acme")
			ctx.Expect(err).To(specs.BeNil())

			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			pingAnyTimes(ctrl)
			ctrl.Method("GetLatestEvent").Expect(mock.Any(), scopeA, persistenceID).Times(1).Return(nil, nil)
			ctrl.Method("WriteEvents").Expect(mock.Any(), scopeA, mock.Any(), mock.Any()).Times(1).Return(nil)

			eventStream := newClosingEventStream(ctx)
			system := startEventsSystem(ctx, "TestActorSystem", 3,
				extensions.NewEventsStore(store),
				extensions.NewEventsStream(eventStream),
				extensions.NewTenancyMarker(false))

			// PreStart must succeed and recover using the spawn-bound scope, not Unscoped().
			pid, err := system.Spawn(context.Background(), behavior.ID(), New(),
				goakt.WithDependencies(behavior, extensions.NewEntityTenantScope("acme")),
				goakt.WithLongLived(), goakt.WithStashing())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(pid).To(specs.Not(specs.BeNil()))

			tenantA, err := tenancy.NewTenantContext("acme")
			ctx.Expect(err).To(specs.BeNil())
			ctxA, err := tenancy.Attach(context.Background(), tenantA)
			ctx.Expect(err).To(specs.BeNil())

			// The command must succeed against the mock store bound to the exact
			// tenant scope. A GetLatestEvent or WriteEvents call made with
			// persistence.Unscoped() instead would match no expectation and the
			// controller reports it as unexpected.
			askCreateAccount(ctx, ctxA, pid)
		})
	})
}

// TestEventSourcedActorPreStartFailsClosedWithoutTenantScope covers
// TENANT-003 T4's fail-closed guard: when tenancy is active (the actor
// system carries extensions.TenancyExtensionID) but the per-spawn
// extensions.EntityTenantScope dependency was not injected, PreStart must
// refuse to start the actor, via resolveScope and before validateAndRecover
// ever runs, rather than falling back to persistence.Unscoped(). No store
// method may be invoked at all: not Ping, not GetLatestEvent, not
// WriteEvents. The mock below has no expectation, so any call is reported as
// unexpected, and the case also asserts that it recorded no call.
func TestEventSourcedActorPreStartFailsClosedWithoutTenantScope(t *testing.T) {
	specs.Describe(t, "PreStart refuses to start a tenant-aware actor that was given no tenant scope", func(s *specs.Spec) {
		s.It("fails closed with the typed missing-scope error and never touches the store", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			eventStream := newClosingEventStream(ctx)
			system := startEventsSystem(ctx, "TestActorSystem", 1,
				extensions.NewEventsStore(store),
				extensions.NewEventsStream(eventStream),
				extensions.NewTenancyMarker(false))

			// Deliberately no extensions.NewEntityTenantScope dependency: tenancy is
			// active (extensions.NewTenancyMarker(false) above), but no scope was bound
			// for this spawn.
			pid, err := system.Spawn(context.Background(), behavior.ID(), New(),
				goakt.WithDependencies(behavior),
				goakt.WithLongLived(), goakt.WithStashing())

			// The rejection must be the typed error, not an invented one.
			ctx.Expect(err).To(specs.MatchError(extensions.ErrEntityTenantScopeMissing))
			ctx.Expect(pid).To(specs.BeNil())
			ctx.Expect(ctrl.Calls()).To(specs.BeEmpty())
		})
	})
}

// TestEventSourcedActorLegacyModeAlwaysUsesUnscopedStore is the regression
// guard for existing non-tenant users of TENANT-003 T4: when no
// tenancy.TenantResolver is configured (no extensions.TenancyMarker on the
// actor system, entity.tenantAware ends up false), every store call this
// actor makes must still carry persistence.Unscoped() exactly as it did
// before TENANT-003 T4 introduced entity.scope. The mock store below only
// has expectations registered for persistence.Unscoped(): a call with any
// other scope value is reported as unexpected.
func TestEventSourcedActorLegacyModeAlwaysUsesUnscopedStore(t *testing.T) {
	specs.Describe(t, "an actor on a system without tenancy keeps using the unscoped store", func(s *specs.Spec) {
		s.It("recovers and writes with persistence.Unscoped()", func(ctx *specs.Context) {
			persistenceID := uuid.NewString()
			behavior := enginetest.NewAccountEventSourcedBehavior(persistenceID)

			ctrl := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(ctrl)
			pingAnyTimes(ctrl)
			ctrl.Method("GetLatestEvent").Expect(mock.Any(), persistence.Unscoped(), persistenceID).Times(1).Return(nil, nil)
			ctrl.Method("WriteEvents").Expect(mock.Any(), persistence.Unscoped(), mock.Any(), mock.Any()).Times(1).Return(nil)

			eventStream := newClosingEventStream(ctx)
			// No extensions.NewTenancyMarker(false) here: legacy mode, exactly like the
			// pre-TENANT-003 actor system configuration.
			system := startEventsSystem(ctx, "TestActorSystem", 3,
				extensions.NewEventsStore(store),
				extensions.NewEventsStream(eventStream))

			pid, err := system.Spawn(context.Background(), behavior.ID(), New(),
				goakt.WithDependencies(behavior),
				goakt.WithLongLived(), goakt.WithStashing())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(pid).To(specs.Not(specs.BeNil()))

			askCreateAccount(ctx, context.Background(), pid)
		})
	})
}
