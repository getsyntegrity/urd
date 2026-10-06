# Design — EventStore tenant isolation (EGO-TENANT-003)

Covers this slice (T1, `persistence.Scope` itself) and states the design
commitments the follow-up slices (T2–T5, see `tasks.md`) are bound by, so
that introducing the type today does not accidentally foreclose or
pre-decide work those slices still have to do. `ego-write-004`'s decisions
(`openspec/changes/ego-write-004/design.md`) are cited, not restated:
`WritePrecondition`, `ConflictError`, and compare-and-swap are unchanged
authority.

## D1 — Effective identity is the pair `(Scope, persistence_id)`

**Decision**: from this slice forward, the concept "which record is this"
is defined as the pair `(Scope, persistence_id)`, never `persistence_id`
alone. `persistence_id` keeps its existing meaning and shape exactly as
`ego-store-001` ratified it — an opaque, caller-assigned string the store
never parses. `Scope` is the new, independent half of the key.

**Why a pair and not a richer identity type**: a pair is the minimal
change that removes the collision. Introducing some new composite
`ScopedID` struct that bundles both values into one caller-facing type was
considered and rejected for this slice: it would mean touching every call
site that constructs a `persistence_id` today, for no expressive gain over
simply adding one more parameter. `persistence.WritePrecondition` already
established the pattern this repository uses for "one more independent
argument the store must consider before allowing a write" — a small,
separately-constructed value type passed alongside the existing arguments,
not folded into an existing one. `Scope` follows the identical shape:
unexported fields, an invalid zero value, named constructors
(`Unconditional`/`ExpectGenesis`/`ExpectRevision` for the precondition;
`Unscoped`/`NewTenantScope` for scope), a `Valid()` method, a `String()`
method for diagnostics only. Future slices (T2) extend the store method
signatures with `Scope` as a further explicit parameter, exactly the way
`WritePrecondition` was added as a trailing parameter to `WriteEvents` and
`WriteState` by `ego-write-004` D2, rather than composed into
`persistence_id` or into the event/state payload.

## D2 — `Unscoped()` is the backward-compatibility anchor

**Decision**: `Unscoped()` is not merely "no tenant" as a concept — it is
defined to be exactly the historical, pre-TENANT-003 behavior, byte for
byte. A store that always receives `Unscoped()` (which is what every
current call site does today, implicitly, by not passing any scope at
all) must behave identically to a pre-TENANT-003 store. This is what makes
the type additive rather than a breaking migration: an operator who never
turns tenancy on for their deployment is never asked to re-key existing
data.

**Consequence for T2**: when the follow-up slice adds `Scope` to the store
method signatures, every existing production call site that does not yet
resolve a real tenant (i.e., every call site before T4 wires the engine to
pass a genuinely resolved tenant) must pass `Unscoped()`, not some other
placeholder, and the store's keying behavior under `Unscoped()` must
remain provably unchanged. That equivalence is exactly what
`persistence/scope_test.go`'s non-collision assertions are designed to
make checkable in isolation, ahead of T2's larger, riskier interface
change.

**Non-collision guarantee**: `Unscoped()` must never be `Equal()` to any
`Scope` returned by `NewTenantScope`, including a tenant literally named
`"unscoped"`. This is enforced structurally (`Equal` compares the
unexported `kind` discriminator before ever looking at the tenant id), not
lexically — see D3.

## D3 — `Scope.String()` is diagnostic only; a store must key structurally

**Decision**: `Scope` exposes a `String()` method for error messages and
logs, rendering `"unscoped"` or `"tenant:<id>"`. This method is explicitly
documented as unsafe for use as a storage key, a cache key, or any other
value a store or wrapping layer might be tempted to build a
per-tenant-namespace out of by string concatenation.

**Why this matters even though this slice does not touch a store**: this
slice's doc comments are the contract T2's store implementations (and any
external `EventsStore`/`StateStore` implementer) are written against. If
`String()` were left ambiguous about whether it is key-safe, a future
implementer could reasonably read `"tenant:" + id` and use it to build a
composite key by hand — reintroducing, one layer later, exactly the
string-composition hazard the proposal's rejected alternative describes
(a tenant id containing the separator or another tenant's rendered prefix
could forge a collision). The doc comment on `String()` states this
explicitly so the guidance survives independent of this design document.
A store must instead key on the pair structurally: the `Scope` value (or
equivalently its kind plus `TenantID()`) held alongside `persistence_id`,
never a single concatenated string derived from either.

## D4 — Why `Scope` is a call parameter, not a field on the event/state payload

**Decision**: `Scope` is designed to travel as an explicit function
parameter to store methods (T2), the same way `WritePrecondition` already
does — not as a new field added to `egopb.Event`, `egopb.Snapshot`, or
`egopb.DurableState`.

**Why**: `tenant_metadata` already exists as a payload field, and
`ego-store-001` already documented, as a ratified fact, exactly what is
wrong with relying on it: it is data on the message, not a value the
store's compare-and-swap machinery inspects. A payload field is
attacker-and-bug-controlled in a way a call parameter typed and validated
at the API boundary is not — nothing stops a caller (or a bug in a
serialization path) from constructing an `egopb.Event` with an arbitrary
`tenant_metadata` map, but nothing upstream of the store call is in a
position to check that map against the caller's actually-resolved
`tenancy.TenantContext` before persistence sees it. A `WritePrecondition`
can be evaluated atomically against `StorageRevision` at the instant of
commit precisely because it arrives as a typed parameter the store
itself receives and enforces, not as a value it has to trust the payload
to report honestly. `Scope` is designed to be enforced the identical way:
passed in by the caller (in T4, resolved from `tenancy.TenantContext`,
never taken from `tenant_metadata`) and checked by the store as part of
the same atomic operation that already checks `WritePrecondition` — so
that "wrong scope" and "stale revision" fail through the same kind of
guarded, store-owned check rather than one of them being an
easily-bypassed payload convention.

`tenant_metadata` is not removed or deprecated by this decision. It
remains exactly what `ego-store-001` already ratified it to be:
descriptive, transported, non-authoritative. `Scope` does not change its
meaning; `Scope` is simply never derived from it.

## D5 — Import direction: `persistence` may depend on `tenancy`; `tenancy` stays stdlib-only

**Decision**: `persistence/scope.go` imports `github.com/pablogore/ego/v4/tenancy`
for `tenancy.TenantID`. This is a one-directional dependency and is the
correct direction: `tenancy` is the source of tenant identity and must stay
independently reusable and minimal, while `persistence` is a consumer that
already sits above lower-level packages.

**Why this is safe and intended**: `tenancy`'s own architecture test,
`TestTenancyArchitecture` (`tenancy_architecture_test.go`, run via
`go list -deps ./tenancy/...`), enforces that `tenancy` itself gains no
non-stdlib dependency — the constraint is that `tenancy` cannot depend on
`persistence` (or on anything else outside the standard library), not that
nothing may depend on `tenancy`. `persistence` importing `tenancy` does not
touch `tenancy`'s own dependency list at all, so this slice does not, and
structurally cannot, cause that architecture test to fail. This was
verified directly, not assumed: `go test -run TestTenancyArchitecture .`
passes after this slice's `scope.go` is added (see `tasks.md` T1 evidence).

**Why the direction matters for TENANT-003 as a whole**: if `tenancy` ever
depended back on `persistence`, the two packages would form a cycle the
moment a later slice needed both a tenant concept and a persistence
concept in the same file — which T4 (engine/actor wiring) is exactly the
kind of slice that would need. Keeping `tenancy` stdlib-only and letting
`persistence` be the one that reaches down to it is what keeps that later
wiring possible without a package reshuffle.

## What this slice deliberately leaves open for T2–T5

- The exact new signatures for `WriteEvents`/`WriteState`/read methods —
  whether `Scope` becomes a single trailing parameter (mirroring
  `WritePrecondition`'s placement) or is threaded some other way, is T2's
  decision to make and record, informed by the same call-site-count and
  compile-time-break tradeoffs `ego-write-004` D2 already reasoned
  through for `WritePrecondition`.
- Whether cross-tenant isolation is proven by a new conformance suite
  against the `testkit` stores directly, an integration test through real
  actors, or both — T3.
- How the engine resolves a real `Scope` from `tenancy.TenantContext` at
  the point an actor is constructed or recovers, and what happens for an
  actor running under `tenancy.ScopeAdministrative` — T4.
- What an external store adapter author needs to change, and how that
  upgrade is documented — T5, in the style of the WRITE-004
  breaking-change block already present at the top of
  `persistence/events_store.go`.

None of these are resolved by `persistence/scope.go` today; the type only
supplies the vocabulary those decisions will be expressed in.

## CI correction: the tenant is declared at spawn, never resolved there

**What shipped first, and what was wrong with it.** The first cut of T4
made `Engine.Entity`, `Engine.DurableStateEntity`, and `Engine.Saga` call
`engine.tenantResolver.Resolve(ctx)` at spawn time (`resolveSpawnTenantScope`),
so the spawned actor's `persistence.Scope` could be bound before recovery.
That directly violates this repository's own normative
`openspec/specs/tenancy-core/spec.md` requirement, **Resolve-Once,
Propagate-After**: `TenantResolver` MUST be invoked exactly once, at the
trust boundary, never at spawn. CI caught it, not a manual review:
`TestSendCommandResolverSwapIdenticalSequence`'s multi-tenant subtest
asserted the resolver is invoked exactly once across one `engine.Entity`
call plus one `SendCommand` call, and observed two. A second real failure,
`TestSagaFailsClosed`, surfaced a related but independent gap in that same
test: it spawned its target entity and saga directly through
`actorSystem.Spawn`, bypassing `Engine.Entity`/`Engine.Saga` entirely, and
never supplied the per-spawn `extensions.EntityTenantScope` dependency —
so once tenancy is active, PreStart's own fail-closed guard
(`ErrEntityTenantScopeMissing`) now blocks the spawn before the test ever
reaches the saga-dispatch gate it was written to prove.

**The fix.** `Engine.Entity`/`DurableStateEntity`/`Saga` no longer call
`Resolve` at spawn at all. Instead:

- The application declares an entity's tenant at spawn with a new
  functional `SpawnOption`, `ego.WithTenant(id tenancy.TenantID)`
  (`spawn_config.go`). This is the correct owner of that decision: the
  application already knows which tenant a given entity/durable-state
  entity/saga belongs to (e.g. it just read the tenant off an
  authenticated request that is now creating that entity) — the engine
  has no business inferring it by calling the resolver a second time.
- `tenancy.WithSingleTenant`'s returned resolver additionally implements a
  small new capability interface, `tenancy.FixedTenantResolver`
  (`FixedTenant() (TenantID, bool)`), so a single-tenant deployment can
  still spawn without ever passing `WithTenant` — reading a statically
  known tenant off a resolver is not the same operation as invoking
  `Resolve`, and doing so costs nothing. This preserves issue #92's
  acceptance criterion 6 ("single-tenant mode keeps working without
  tenant plumbing invented by the application"), which the earlier design
  never put at risk in the first place, but which the interface is what
  makes possible without ever calling `Resolve` at spawn.
- `engine.go`'s `spawnTenantScope(config *spawnConfig)` (renamed from
  `resolveSpawnTenantScope`, and no longer takes a `ctx`) determines the
  spawn's tenant purely from these two non-resolving sources, in order:
  `config.tenantID` (set by `WithTenant`), then the registered resolver's
  `FixedTenant()` when it implements `tenancy.FixedTenantResolver` and
  reports one. If neither yields a tenant, the spawn fails closed with the
  new `ErrSpawnTenantUndetermined` — never a silent fall-through to
  `persistence.Unscoped()`, which would defeat the isolation this ticket
  exists to enforce.
- `ErrAdministrativeScopeEntitySpawn` is removed: it existed only to name
  the outcome of resolving an administrative `tenancy.TenantContext` at
  spawn, and spawn no longer resolves anything. An administrative-only
  resolver (one with no fixed tenant) now simply falls into the same
  `ErrSpawnTenantUndetermined` fail-closed path as any other resolver with
  no fixed tenant and no `WithTenant` — a coarser but still fail-closed
  outcome; TENANT-008 remains the ticket that would give administrative
  entity/saga access its own, dedicated shape.
- `Engine.Saga` gained a trailing `opts ...SpawnOption` parameter — an
  additive, non-breaking signature change — purely as `WithTenant`'s
  carrier; a saga still hardcodes its own supervision/placement/relocation
  behavior, so no other `SpawnOption` has any effect on it.

**What did not change.** `Dispatch`, `SagaStatus`, and `EraseEntity` are
untouched: they are the actual command/query/administrative trust
boundaries, and each already called `Resolve` exactly once, there, before
this correction and after it. The actor side —
`EventSourcedActor`/`DurableStateActor`/`SagaActor`'s `resolveScope`,
`PreStart`'s ordering, and the `EntityTenantScope` dependency type itself
— is also untouched: it never called `Resolve`, and it still just reads
the same per-spawn dependency, now populated from `WithTenant`/
`FixedTenant()` instead of from a spawn-time `Resolve` call.

## Known limitation: a shared entity id still maps to one actor, across tenants

T4 binds every `EventSourcedActor`, `DurableStateActor`, and `SagaActor` to
a `persistence.Scope` at spawn, resolved once from the caller's
`tenancy.TenantContext` and carried through to every store read and write
that actor makes for its whole lifetime (`resolveScope`, called from
`PreStart` before any recovery read). That closes the isolation hole this
ticket exists to close: a tenant-aware actor can no longer read or write a
record belonging to a different tenant, whether through recovery, a live
command, a snapshot, or retention cleanup.

What T4 does **not** change is how an actor gets its name. A GoAkt actor's
identity is still the caller-supplied `entityID` (for `Engine.Entity` /
`Engine.DurableStateEntity`) or `sagaID` (for `Engine.Saga`), exactly as
before this ticket — it is not tenant-qualified, and this ticket does not
introduce any tenant-qualification of it. GoAkt itself has no notion of
"the same name under two different tenants": one actor system position can
only ever hold one live actor for a given name.

**What this means for a user in practice**: if tenant A and tenant B both
call, say, `engine.Entity(ctx, behavior)` for an entity behavior whose
`ID()` returns the same string (e.g. both happen to use the customer's
external order number as the entity id), only the tenant whose spawn
attempt reaches `PreStart` first actually gets an actor. `resolveScope`
binds that actor's `scope` (and `actorTenant`) to whichever tenant won the
race, permanently for that actor's lifetime. Every later spawn attempt or
command for that same id, from the *other* tenant, is rejected:

- A second spawn attempt for the same id under a different tenant fails
  with `ErrSpawnTenantMismatch` (also `tenancy.ErrDenied`). GoAkt's local
  `Spawn` returns an already-running actor's PID with a nil error, and
  concurrent spawns of one name coalesce onto one execution, so the engine
  checks the *returned* actor's own spawn binding — its
  `EntityTenantScope` dependency, the value its `PreStart` bound into its
  `scope` — after `Spawn` returns (`engine.go`'s `verifySpawnedTenant`).
  Checking the actor that actually holds the name, rather than a pre-check
  before spawning, leaves no window: two concurrent spawns under different
  tenants produce exactly one winner. A spawn under the *same* tenant is an
  idempotent success. For a remote PID the binding is read from the owning
  node (goakt's `RemoteDependencies`); a lookup that stays unanswered fails
  closed with `ErrSpawnTenantUnverified`, which claims no conflict.
- A command from the non-owning tenant against the already-running actor
  is rejected by the existing `actorTenant` cross-check in
  `processCommandAndReply` / `processAndBatch` (`EventSourcedActor`),
  `processCommand` (`DurableStateActor`), and the saga's own dispatch
  gate — the same mechanism that already rejects a cross-tenant command
  today, unchanged by this ticket.

**This is fail-closed and leak-free, not a security hole.** At no point
does the non-owning tenant read or write any data belonging to the actor's
bound tenant: every rejection happens before a store is ever touched. What
the non-owning tenant experiences instead is a plain **availability**
problem — its own spawn or command for that entity id simply does not
work, with no data ever crossing the boundary in either direction. This is
a functional limitation on which ids a tenant may use, not a violation of
tenant isolation.

**Follow-up**: the real fix is tenant-qualified actor identity — deriving
the actor's GoAkt name from `(tenant, entityID)` rather than `entityID`
alone, so two tenants using the same logical id get two independent
actors instead of contending for one. That is explicitly out of scope for
TENANT-003 T4 and is left for a follow-up ticket; it likely also touches
cluster placement/rebalancing and any external tooling that currently
addresses an actor by bare entity id, so it deserves its own design pass
rather than being folded into this slice.
