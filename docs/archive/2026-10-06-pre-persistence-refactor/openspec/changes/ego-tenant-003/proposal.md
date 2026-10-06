# Proposal — EventStore tenant isolation (EGO-TENANT-003)

| Field | Value |
|---|---|
| Change | `ego-tenant-003` |
| Date | 2026-09-22 |
| Phase | `sdd-propose` — proposed |
| Tracker | [`#92`](https://github.com/getsyntegrity/ego/issues/92), epic [`#23`](https://github.com/getsyntegrity/ego/issues/23) |
| Depends on | `ego-store-001` (`#70`, ratified) — froze the exact gap this change closes; `ego-write-004` (`#65`, shipped) — `WritePrecondition`/`ConflictError`/CAS, unchanged and reused |
| Blocks | none filed yet |

## The problem, in plain terms

Every record ego persists — an event, a snapshot, a durable-state row — is
keyed in the store by one bare string: `persistence_id`
(`persistence/events_store.go`, `persistence/state_store.go`,
`persistence/snapshot_store.go`). That string is not something ego invents;
it is the GoAkt actor name the caller supplied when the actor was
constructed. It is caller-supplied, opaque, and, critically, it says
nothing about which tenant the actor belongs to.

Ego already resolves "which tenant is this" for every request: the
`tenancy` package's `TenantContext` (`tenancy/tenant_context.go`) carries a
validated `tenancy.TenantID` through `context.Context` for the whole
lifetime of a call. But that resolved identity lives only in `ctx`. It is
never combined with `persistence_id` before the store sees it. The store's
read and write paths take `persistence_id` alone.

The consequence: if two different tenants — say `tenant-a` and `tenant-b`
— construct an actor with the same caller-chosen name (a very ordinary
thing to happen: aggregate names are often derived from a business key like
an order number or an email address, and two tenants can legitimately reuse
the same business key), they collide in the same store. `tenant-b`'s writes
land on `tenant-a`'s events, and `tenant-a`'s reads can return `tenant-b`'s
data. `ego-store-001`'s `persistence-store-contract` spec already documents
this as a known, frozen gap (its Requirement "Tenant Metadata Is
Transported, Not Enforced as Isolation" and "Tenant Isolation Design Is
TENANT-003's, Not Pre-Decided Here" — see
`openspec/changes/ego-store-001/specs/persistence-store-contract/spec.md`).
This proposal is the change that closes it.

One more fact worth stating plainly, because it is easy to assume
otherwise: `tenant_metadata` (`map<string,string>` on `egopb.Event`,
`egopb.Snapshot`, `egopb.DurableState`) already exists on the wire today.
It looks like it should be the answer. It is not: it is transported
alongside the payload and returned unchanged on reads, but no
`WritePrecondition`/compare-and-swap check anywhere in `persistence`
consults it. Carrying a tenant name in the payload does not stop two
tenants from colliding on `persistence_id` — it just means the collision
now carries two different, silently-overwritten `tenant_metadata` values
instead of one.

## What changes

This slice — the first of a four-slice chain (see `tasks.md`) — introduces
the vocabulary the rest of the chain builds on: a new value type,
`persistence.Scope` (`persistence/scope.go`), with two constructors:

- `persistence.Unscoped()` — the scope used when tenancy is not activated
  on the engine; behaves exactly like today's un-isolated behavior.
- `persistence.NewTenantScope(tenancy.TenantID) (Scope, error)` — the
  scope used when a record belongs to one specific tenant.

The normative claim this type makes is simple to state and important to
get right: the effective identity of a persisted aggregate becomes the
**pair** `(Scope, persistence_id)`, not `persistence_id` alone. Two
different `Scope` values presented with the same `persistence_id` name two
different records.

This slice does **not** change any store interface signature yet.
`EventsStore.WriteEvents`, `StateStore.WriteState`, and every other method
on the three store interfaces keep their current signatures. No actor, no
engine wiring, no testkit store, and no mock changes in this slice — those
are named explicitly as follow-up slices in `tasks.md` (T2–T5), because
wiring `Scope` into every call site is exactly the kind of interface-wide
breaking change `ego-write-004` already showed needs its own reviewed,
chained PR, not a change bundled invisibly into "introduce the type."

## Why this design, and what was rejected

**Rejected alternative: compose a tenant-prefixed `persistence_id` inside
the engine.** Concretely, this would mean something like the engine
silently rewriting a caller's `persistence_id` of `"order-42"` into
`"acme:order-42"` before it ever reaches the store, so the existing
single-string key still "just works." This was rejected for three
concrete reasons:

1. **It breaks the opacity `persistence_id` is supposed to have.** Today,
   nothing outside the caller is allowed to parse or reinterpret
   `persistence_id` — `ego-store-001`'s ratified contract states this
   explicitly ("persistence_id passes through unchanged... no
   transformation, prefixing, or parsing"). String concatenation is
   exactly that transformation. It would also make the scheme fragile: a
   tenant id that happens to contain the chosen separator character (a
   colon, in the example above) could forge or collide with another
   tenant's composed key.
2. **It silently changes stored key bytes for existing, already-persisted
   data.** Every unscoped deployment running today has events keyed by the
   bare `persistence_id` it already used. If tenancy activation started
   composing that key, every existing stream would need a migration (or
   would simply become unreachable under its old key) the moment tenancy
   was turned on — a correctness hazard for a feature framed as additive.
3. **It leaves the store with nothing to verify.** A composed string key
   is exactly one more caller-chosen string as far as the store is
   concerned; nothing stops a caller from constructing the composed string
   directly and forging a cross-tenant key by hand. A store cannot enforce
   isolation against a scheme it cannot structurally distinguish from an
   ordinary `persistence_id`.

The chosen design instead keeps `Scope` and `persistence_id` as two
separate values, passed to the store as a pair (from slice T2 onward, once
the store signatures change). `design.md` covers the full architecture
rationale, including why `Scope` is a call parameter rather than a payload
field.

## In scope (this slice, T1 only)

- `persistence.Scope` value type, `persistence.Unscoped()`,
  `persistence.NewTenantScope`, `persistence.ErrInvalidScope`
  (`persistence/scope.go`).
- Test coverage for the type (`persistence/scope_test.go`), written first
  per this repository's strict TDD mode.
- This proposal, `design.md`, `tasks.md`, and
  `specs/persistence-tenant-isolation/spec.md`.

## Out of scope (this slice; see `tasks.md` for the named follow-ups)

- Changing `EventsStore`/`StateStore`/`SnapshotStore` method signatures to
  accept `Scope` (T2).
- Any cross-tenant conformance test that exercises isolation end to end
  (T3).
- Engine/actor wiring so a real resolved tenant reaches the store instead
  of `Unscoped()` (T4).
- Migration/compatibility documentation for external store adapter authors
  (T5).
- Anything already ratified as WRITE-004's or STORE-001's authority:
  `WritePrecondition`, `ConflictError`, CAS semantics, and the shape of the
  two store interfaces are unchanged and un-reopened here.

## Success criteria for this slice

`persistence/scope.go` exists, its zero value is invalid, `Unscoped()` is
byte-compatible with current behavior and never equal to any tenant scope,
`persistence/scope_test.go` passes and was written and observed failing
first, `go vet` and `go build` are clean, and the `tenancy` package gained
no new dependency (`TestTenancyArchitecture` still passes).
