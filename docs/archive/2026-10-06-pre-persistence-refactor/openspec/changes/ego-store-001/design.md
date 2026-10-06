# Design — Persistence store contract, canonical form (EGO-STORE-001)

Ratification only. WRITE-004's decisions (`openspec/changes/ego-write-004/design.md`, shipped on `main`) are not restated here — they are cited where this change depends on them.

## D1 — `EventsStore` and `StateStore` stay separate interfaces

**Decision**: do not introduce a merged `Store` (or similarly named) interface embedding both.

**Why**: their read/write shapes are genuinely different, not incidentally different. `EventsStore.WriteEvents` appends an ordered batch to a journal and exposes replay/forward-scan reads (`ReplayEvents`, `GetShardEvents`); `StateStore.WriteState` overwrites a single last-write-wins row and exposes only `GetLatestState`. A merged interface would force one of the two shapes into an artificial common denominator, or grow a lowest-common-subset that neither consumer actually wants. `EventSourcedActor` and `DurableStateActor` already consume these as two distinct dependencies (`WithEventsStore`/`WithStateStore` on `Engine`); nothing in the codebase treats them as interchangeable.

**Rejected alternative**: `type Store interface { EventsStore; StateStore }`. Rejected because no real caller needs to hold "a store that is either kind" — every actor type is wired to exactly one store kind at construction, and forcing embedding would only couple their method sets for no consumer benefit.

## D2 — Concurrency and lifecycle primitives are shared, not duplicated

**Decision**: `persistence.WritePrecondition`, `*persistence.ConflictError`, and the lifecycle triad (`Connect`/`Disconnect`/`Ping`) are the shared correctness primitives across `EventsStore` and `StateStore`. STORE-001 formalizes this sharing; it does not introduce a new shared interface to express it.

**Why**: this is already the shipped shape. `WritePrecondition` and `*ConflictError` live in the `persistence` package (not inside either store's file) and are consumed identically by `WriteEvents(..., precondition)` and `WriteState(..., precondition)` — WRITE-004 already did the work of making these package-level values rather than per-interface types. `expectedRevisionFromContext`/`preconditionFromRevision` (in `event_sourced_actor.go`) are reused unchanged by `durable_state_actor.go`. There is no duplication to fix and no further generalization (e.g. a `Preconditioned` capability interface) that any current or planned consumer needs.

## D3 — Forward reads are the canonical read contract; backward reads are out

**Decision**: `ReplayEvents`, `GetLatestEvent`, `PersistenceIDs`, `GetShardEvents`, `ShardOffsets` are ratified as the complete canonical read contract for `EventsStore`. No backward/descending read method is added.

**Why**: grep across the tree found zero consumers and zero design documents requiring backward iteration. #70's original text asked for "forward/backward" reads as an aspiration, not a requirement traced to any caller. Adding an unused method to a canonical contract makes every future adapter implement dead code. If a real consumer (e.g. a UI needing "most recent N, scrolling backward") appears later, it is a new, independently-evaluated capability — not retrofitted here.

## D4 — What "atomicity" means in this contract, precisely

**Decision**: this contract formalizes exactly one atomicity guarantee: a single `WriteEvents` call (one batch, one `persistence_id`) or a single `WriteState` call is atomic with respect to its `WritePrecondition` check — there is no observable window between checking the precondition against the persisted revision and committing. It does **not** claim, and explicitly disclaims, "multiple independent `WriteEvents`/`WriteState` calls are atomic together" (cross-call, cross-aggregate, or distributed transactions — out of scope, matches epic #13's own stated boundary) or "a multi-event batch is all-or-nothing at the adapter level in the presence of partial adapter failure" (WRITE-006's concern, and one the in-memory testkit implementation — a single `sync.Map` pointer swap — cannot even exercise, since it has no notion of a partially-written batch).

**Evidence**: `TestEventSourcedIntegrationConcurrentGenesisYieldsExactlyOneCommit` (`event_sourced_actor_integration_test.go`) and `TestDurableStateConcurrentGenesisWritersYieldExactlyOneCommit` (`durable_state_actor_expected_revision_test.go`) already prove this under `-race`, using two independent `Engine`/actor-system instances sharing one `testkit` store — the store's own `CompareAndSwap`/`LoadOrStore` path, not caller or mailbox ordering, decides the single winner. This proposal cites these as the conformance evidence for R2 in spec.md; no new production code is written to obtain it. Tasks.md's T3 covers verifying this evidence still holds and, if a genuine gap is found (e.g. neither test exercises `StateStore`/`EventsStore` directly rather than through an actor), adding one narrowly-scoped test directly against the `testkit` store.

## D5 — `persistence_id` is opaque; tenant metadata is not isolation

**Decision**: this contract documents, as a fact about the current implementation, that `persistence_id` (`string`, caller-assigned) is the store's only concurrency-boundary and stream-identity key, and that `tenant_metadata` (`map<string,string>` on `Event`/`Snapshot`/`DurableState`) is carried through the store but never consulted by any `WritePrecondition`/CAS check. Two different tenants presenting the same `persistence_id` are not prevented from colliding by the store today; the only existing safeguard, `tenancy.VerifyUnchanged`, detects drift after the fact on an already-established stream — it does not stop the initial collision.

**Why this is frozen, not resolved**: TENANT-003 (cited by epic #23 as blocked on "no EventStore SPI contract to specify tenant scoping against") is the correct owner of the actual isolation mechanism — whether that becomes a composite physical key, a store-side namespace, or a structural precondition extension is a real design decision with real trade-offs (migration cost for existing streams, cross-store consistency, whether isolation is enforced by the store or by a wrapping layer). Pre-deciding it here, even by accident (e.g. by naming a field `TenantPersistenceID` or suggesting string concatenation), would remove TENANT-003's design freedom before it has even been scoped. This proposal's job is narrower and more useful: put the current fact in writing so TENANT-003 doesn't have to rediscover it by reading actor internals.

**Explicitly not decided here**: any composite ID scheme, namespace convention, or store-level tenant filter. TENANT-003 designs these.

## Boundary summary

| Concern | Owner | This proposal's role |
|---|---|---|
| `WritePrecondition`/CAS/`ConflictError` | WRITE-004 (#65, shipped) | Cite by reference |
| `operation_id`/idempotency/dedup/retry | WRITE-005 (#66) | Not touched |
| Multi-event atomic append, partial-failure on real adapters | WRITE-006 (#67) | Not touched; D4 explicitly disclaims it |
| Tenant isolation of `persistence_id` | TENANT-003 (unfiled) | Document the gap only (D5) |
| Concrete adapters | STORE-007/008/009 | Not touched |
| Conformance suite as a product | STORE-006 | Not touched; this proposal cites existing tests, does not build a suite |
