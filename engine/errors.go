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
	"errors"
	"fmt"
	"time"

	"github.com/getsyntegrity/urd/internal/extensions"
	runtimeport "github.com/getsyntegrity/urd/port/runtime"
)

var (
	// ErrEngineNotStarted is returned when the urd engine has not started
	ErrEngineNotStarted = runtimeport.ErrEngineNotStarted
	// ErrUndefinedEntityID is returned when sending a command to an undefined entity
	ErrUndefinedEntityID = runtimeport.ErrUndefinedEntityID
	// ErrCommandReplyUnmarshalling is returned when the unmarshalling command reply failed
	ErrCommandReplyUnmarshalling = errors.New("failed to parse command reply")
	// ErrDurableStateStoreRequired is returned when the Urd engine durable store is not set
	ErrDurableStateStoreRequired = runtimeport.ErrDurableStateStoreRequired
	// ErrEventsStoreRequired is returned by Entity and Saga when the engine's
	// Config has no events store (NewConfig was given a nil
	// persistence.EventsStore, which is valid for a durable-state-only
	// deployment). Nothing is spawned.
	ErrEventsStoreRequired = runtimeport.ErrEventsStoreRequired
	// ErrDuplicatePublisherID is returned by AddEventPublishers and
	// AddStatePublishers when a publisher's ID is already registered for
	// that kind, or appears more than once in the same call. The error
	// names the duplicate IDs. The whole call is rejected: no publisher
	// from it is registered, subscribed or started.
	ErrDuplicatePublisherID = errors.New("duplicate publisher id")
	// ErrProjectionNotRegistered is returned by StartProjection when the given
	// name was never registered on the engine's Config via WithProjection.
	ErrProjectionNotRegistered = runtimeport.ErrProjectionNotRegistered
	// ErrActorSystemRequired is returned when NewEngine is called with a nil
	// actor system. The caller must construct and start the actor system
	// themselves before plugging Urd in.
	ErrActorSystemRequired = errors.New("actor system is required")
	// ErrAmbiguousTenantResolver is returned when NewEngine finds that a
	// Config recorded more than one non-nil WithTenantResolver registration.
	// TenantResolver is a security boundary (DP2): the engine never picks
	// one of several candidates, and refuses to start instead. Registering
	// exactly one non-nil resolver, or never registering one at all
	// (legacy, non-tenant-aware mode), are both valid.
	ErrAmbiguousTenantResolver = errors.New("ambiguous tenant resolver: more than one non-nil WithTenantResolver was registered")
	// ErrActorSystemNotStarted is returned when NewEngine is given an actor
	// system whose Start has not yet been called or has not yet succeeded.
	ErrActorSystemNotStarted = errors.New("actor system must be started before NewEngine")
	// ErrMissingRequiredExtensions is returned when NewEngine validates the
	// actor system and finds that one or more extensions Urd needs are
	// absent. The error message lists the missing extension IDs. Callers
	// typically hit this when the actor system was built from a different
	// Config than the one passed to NewEngine, or when cfg.GoaktOptions()
	// was not applied at construction time. Actors that cannot find an
	// extension they require in PreStart wrap this same value.
	ErrMissingRequiredExtensions = extensions.ErrMissingRequiredExtensions
	// ErrPublicationTenantUndetermined is returned by AddEventPublishers,
	// AddStatePublishers and Subscribe when tenancy is active (a
	// tenancy.TenantResolver is registered via WithTenantResolver) but the
	// resolver exposes no fixed tenant (tenancy.FixedTenantResolver), so the
	// engine cannot tell which tenant's events the publisher or subscriber is
	// for. Registration fails closed and registers nothing: the engine never
	// subscribes to every tenant, and has no administrative bypass for it
	// (EGO-TENANT-005, #96). A resolver whose FixedTenant() reports a tenant, as
	// tenancy.WithSingleTenant's does, makes registration succeed for that
	// tenant. A per-tenant registration API is an open owner decision.
	ErrPublicationTenantUndetermined = errors.New("urd: publication tenant undetermined: a tenant-aware engine needs a fixed tenant to register a publisher or subscriber")
	// ErrSpawnTenantUndetermined is returned by Entity, DurableStateEntity,
	// and Saga when tenancy is active (a tenancy.TenantResolver is
	// registered via WithTenantResolver) but the engine cannot determine
	// which tenant to bind the spawned actor to: the caller did not pass
	// engine.WithTenant, and the registered resolver does not expose a fixed
	// tenant via tenancy.FixedTenantResolver (TENANT-003 T4, corrected after
	// CI caught a Resolve-Once, Propagate-After violation in an earlier
	// design that called TenantResolver.Resolve at spawn — see
	// openspec/specs/tenancy-core/spec.md). The engine never falls back to
	// persistence.Unscoped() in this case: that would silently defeat the
	// isolation TENANT-003 exists to enforce. The caller must either pass
	// engine.WithTenant(id) at spawn, or register a resolver whose FixedTenant()
	// reports one (as tenancy.WithSingleTenant's does).
	ErrSpawnTenantUndetermined = runtimeport.ErrSpawnTenantUndetermined
	// ErrSpawnTenantMismatch is returned by Entity, DurableStateEntity, and
	// Saga in tenant-aware mode when the actor that holds the requested id is
	// bound to a different tenant than the one this spawn declared (TENANT-003
	// T4). Actor names are not tenant-qualified, so a second tenant cannot
	// use an id that is already live for another tenant; the spawn fails
	// visibly instead of returning the other tenant's actor as a success. The
	// error also matches tenancy.ErrDenied and carries a *tenancy.Error.
	// Re-spawning a live id under the SAME tenant stays an idempotent success.
	ErrSpawnTenantMismatch = runtimeport.ErrSpawnTenantMismatch
	// ErrSpawnTenantUnverified is returned by Entity, DurableStateEntity, and
	// Saga in tenant-aware mode when the actor a spawn returned did not
	// answer the engine's TenantBindingQuery — for a remote PID, the node that
	// owns it did not reply in time — or answered that it holds no tenant
	// binding. The spawn fails closed, but unlike ErrSpawnTenantMismatch it
	// asserts no cross-tenant conflict; retrying the spawn is safe, since a
	// same-tenant re-spawn is idempotent.
	ErrSpawnTenantUnverified = runtimeport.ErrSpawnTenantUnverified
	// ErrNotACommand is returned by Dispatch and SendCommand when the payload
	// is an engine-internal control message (egopb.TenantBindingQuery) rather
	// than a command. Rejecting it keeps a caller from asking an actor
	// whether it belongs to an arbitrary tenant.
	ErrNotACommand = runtimeport.ErrNotACommand
	// ErrEntityTenantScopeMissing is returned by an actor's PreStart when
	// tenancy is active (extensions.TenancyExtensionID is registered) but no
	// valid extensions.EntityTenantScope dependency was injected at spawn
	// (TENANT-003 T4). This is a fail-closed guard: a tenant-aware actor must
	// never start without a bound persistence.Scope, since that is exactly
	// the condition that would let it silently read or write Unscoped()
	// records across tenants.
	ErrEntityTenantScopeMissing = extensions.ErrEntityTenantScopeMissing
	// ErrBehaviorNotSerializable is the cause carried by a
	// *BehaviorPlacementError when a behavior without MarshalBinary and
	// UnmarshalBinary is spawned in cluster mode. In cluster mode GoAkt
	// serializes every spawn's dependencies, so it can place the behavior on,
	// or relocate it to, another node. Outside cluster mode such a behavior
	// runs on the local node.
	ErrBehaviorNotSerializable = errors.New("urd: behavior must implement encoding.BinaryMarshaler and encoding.BinaryUnmarshaler to be spawned in cluster mode")
	// ErrBehaviorNotPointer is the cause carried by a *BehaviorPlacementError
	// when a behavior cannot be handed to GoAkt's type registry, which names
	// a type through a pointer and panics on anything else. A spawned
	// behavior must be a non-nil pointer in cluster mode (and non-nil in any
	// mode, since the spawn reads its ID). A kind registered with
	// WithBehaviorKinds or WithEntityKinds only needs a pointer type: a typed
	// nil such as (*T)(nil) registers T, while an untyped nil or a value type
	// is rejected.
	ErrBehaviorNotPointer = errors.New("urd: a behavior must be non-nil to be spawned, and a pointer to be spawned in cluster mode; a behavior kind registered with WithBehaviorKinds or WithEntityKinds must be a pointer type (a typed nil is allowed)")
	// ErrEntityFamilyNotDeclared is returned by SpawnEventSourced,
	// SpawnDurableState and SpawnSaga, and by their deprecated predecessors
	// Entity, DurableStateEntity and Saga, when the engine's Config declares
	// its entity families with WithEntityFamilies and the spawned behavior's
	// family is not among them. The error names the family. Nothing is
	// spawned.
	ErrEntityFamilyNotDeclared = runtimeport.ErrEntityFamilyNotDeclared
	// ZeroTime is the zero time
	ZeroTime = time.Time{}
)

// BehaviorPlacementError reports why a behavior cannot be placed by the GoAkt
// runtime: spawned in cluster mode, where GoAkt serializes it, or registered
// as a kind with GoAkt's type registry. Despite the name, it covers
// registration as well as spawning. EntityID is empty for a registration
// error and for a nil or typed-nil behavior, which has no readable ID.
//
// Err is ErrBehaviorNotSerializable or ErrBehaviorNotPointer, so callers can
// test the cause with errors.Is and read the details with errors.As. The
// engine returns this error before it spawns anything.
type BehaviorPlacementError struct {
	// Kind is the Go type of the behavior, for example "*main.AccountBehavior".
	Kind string
	// EntityID is the spawn's entity or saga ID; empty for a registration
	// error or a nil behavior.
	EntityID string
	// Err is the cause: ErrBehaviorNotSerializable or ErrBehaviorNotPointer.
	Err error
}

// Error names the behavior's type, the entity or saga ID when there is one,
// and the cause.
func (e *BehaviorPlacementError) Error() string {
	if e.EntityID == "" {
		// Kind registration, or a nil behavior with no readable ID.
		return fmt.Sprintf("urd: cannot register or place behavior %s: %v", e.Kind, e.Err)
	}
	return fmt.Sprintf("urd: cannot place behavior %s for %q: %v", e.Kind, e.EntityID, e.Err)
}

// Unwrap returns the cause, ErrBehaviorNotSerializable or ErrBehaviorNotPointer.
func (e *BehaviorPlacementError) Unwrap() error { return e.Err }
