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

package extensions

import (
	"errors"
	"fmt"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"

	"github.com/getsyntegrity/urd/internal/actoridentity"
)

// ErrMissingRequiredExtensions reports that an actor could not find an
// extension it requires on its actor system. The engine package exposes this
// same value as engine.ErrMissingRequiredExtensions, so errors.Is matches it
// whichever package the failing actor lives in.
var ErrMissingRequiredExtensions = errors.New("actor system is missing required urd extensions")

// Require looks up the extension registered under extensionID on the actor
// system reachable through ctx and asserts it to type T.
//
// ctx.Extension returns the plain extension.Extension interface, and an
// unchecked assertion such as ctx.Extension(id).(*EventsStore) panics with an
// unrecoverable "interface conversion" error whenever the extension is
// missing or was registered under a different type. Because PreStart runs on
// the goroutine created for the Spawn/SpawnChild call — which goakt drives
// through a golang.org/x/sync/singleflight.Group so concurrent spawns of the
// same identity coalesce onto one execution — a panic there is deliberately
// re-panicked by singleflight on a fresh, unrecoverable goroutine (see
// golang.org/x/sync/singleflight.(*Group).doCall), which crashes the whole
// process instead of just failing the one Spawn call. This can happen for a
// child actor (the events writer, the events janitor, ...) spawned while its
// required extension was never registered, or a mismatched actor-system setup.
//
// Require turns that panic into a descriptive PreStart error wrapping
// ErrMissingRequiredExtensions instead, which goakt reports as an ordinary
// spawn/init failure.
func Require[T any](ctx *goakt.Context, extensionID string) (T, error) {
	var zero T

	ext := ctx.Extension(extensionID)
	if ext == nil {
		return zero, fmt.Errorf("%w: %s is not registered on the actor system (actor=%q)",
			ErrMissingRequiredExtensions, extensionID, ctx.ActorName())
	}

	typed, ok := ext.(T)
	if !ok {
		return zero, fmt.Errorf("%w: %s was registered with unexpected type %T (actor=%q)",
			ErrMissingRequiredExtensions, extensionID, ext, ctx.ActorName())
	}

	return typed, nil
}

// Optional looks up the extension registered under extensionID on
// the actor system reachable through ctx and asserts it to type T, but,
// unlike Require, treats a missing registration as valid: it
// returns the zero value and a nil error when no extension is registered
// under extensionID at all. This fits PreStart paths for which the
// extension is a genuine optional dependency, e.g. snapshotsWriterActor's
// snapshot store and encryptor extensions.
//
// It still guards against the same crash-the-process failure mode described
// on Require: if an extension IS registered under extensionID but
// under an unexpected concrete type — for example because of a wiring bug
// that registers the wrong extension under an existing ID — an unchecked
// ext.(T) assertion would panic, and because PreStart runs on a goroutine
// driven through golang.org/x/sync/singleflight.Group, that panic would be
// re-panicked by singleflight on a fresh, unrecoverable goroutine and crash
// the whole process (see Require). Optional returns a
// descriptive error instead in that case.
func Optional[T any](ctx *goakt.Context, extensionID string) (T, error) {
	var zero T

	ext := ctx.Extension(extensionID)
	if ext == nil {
		return zero, nil
	}

	typed, ok := ext.(T)
	if !ok {
		return zero, fmt.Errorf("%w: %s was registered with unexpected type %T (actor=%q)",
			ErrMissingRequiredExtensions, extensionID, ext, ctx.ActorName())
	}

	return typed, nil
}

// BehaviorFrom reads a behavior of contract T from a spawn dependency, as an
// actor's PreStart sees it in ctx.Dependencies(): either the behavior itself
// (the pass-through case of spawnDependency) or an LocalBehavior
// that wraps it.
func BehaviorFrom[T any](dependency extension.Dependency) (T, bool) {
	if local, ok := dependency.(*LocalBehavior); ok {
		b, ok := local.Behavior().(T)
		return b, ok
	}
	b, ok := dependency.(T)
	return b, ok
}

// VerifyActorIdentity checks, in an actor's PreStart and before it reads any
// store, that the actor's name is the one its tenant and business ID derive
// (actoridentity.Qualify). It is a no-op when the engine does not qualify
// actor names: legacy and single-tenant engines name the actor after the bare
// ID, which cannot disagree with anything.
//
// The tenant comes from the EntityTenantScope dependency, which GoAkt
// serializes with the actor, so an actor relocated to another node rebuilds
// the same (tenant, ID) identity there and proves it against its own name. An
// actor whose name and carried tenant disagree fails closed here instead of
// recovering another tenant's records.
func VerifyActorIdentity(ctx *goakt.Context, tenantID, businessID string) error {
	if !QualifiedActorNames(ctx) {
		return nil
	}
	return actoridentity.Verify(ctx.ActorName(), tenantID, businessID)
}

// QualifiedActorNames reports whether the engine names actors after their
// tenant and business ID (see TenancyMarker.QualifiedActorNames). It is false
// when tenancy is inactive.
func QualifiedActorNames(ctx *goakt.Context) bool {
	marker, ok := ctx.Extension(TenancyExtensionID).(*TenancyMarker)
	return ok && marker != nil && marker.QualifiedActorNames()
}
