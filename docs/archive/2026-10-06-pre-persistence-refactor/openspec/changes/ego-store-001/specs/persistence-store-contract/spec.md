# Persistence Store Contract Specification

## Purpose

Ratify `persistence.EventsStore`, `persistence.StateStore`, and
`persistence.SnapshotStore` as ego's canonical, runtime-neutral,
adapter-neutral persistence ports; formalize the concurrency and lifecycle
primitives WRITE-004 (`persistence-concurrency`, `#65`, merged) already
shipped as shared code, by reference rather than restatement; and freeze the
current, undecided boundary that blocks TENANT-003 from designing tenant
isolation. This capability is ratification of already-shipped code plus one
documentation gap-note — it introduces no new runtime behavior.

## Requirements

### Requirement: EventsStore, StateStore, and SnapshotStore Are the Canonical Persistence Ports

`persistence.EventsStore` (`persistence/events_store.go`),
`persistence.StateStore` (`persistence/state_store.go`), and
`persistence.SnapshotStore` (`persistence/snapshot_store.go`) MUST be
recognized as ego's canonical persistence ports: runtime-neutral (no
dependency on any actor-system or `goakt` type) and adapter-neutral (no
assumption of a specific backing store). They remain three separate
interfaces; no merged or embedding interface is introduced.

#### Scenario: EventsStore has no runtime dependency

- GIVEN `persistence.EventsStore`'s declared method set
- WHEN its imports are inspected
- THEN it depends only on `context` and `egopb`, never on `goakt`, `Engine`,
  or any actor type

#### Scenario: StateStore and EventsStore remain independently consumable

- GIVEN `EventSourcedActor` (consumes only `EventsStore`) and
  `DurableStateActor` (consumes only `StateStore`)
- WHEN either actor type is constructed
- THEN it depends on exactly one store interface, never both, and no shared
  `Store` supertype exists for either to implement

### Requirement: WritePrecondition and ConflictError Are Shared, Referenced, Never Redefined

`persistence.WritePrecondition` (`Unconditional()`/`ExpectGenesis()`/
`ExpectRevision(N)`) and `*persistence.ConflictError`, as shipped by
WRITE-004 (`#65`, `persistence-concurrency` capability), are the sole
concurrency contract shared by `EventsStore.WriteEvents` and
`StateStore.WriteState`. This capability MUST NOT redefine, extend, or
introduce an alternative precondition or error type; it only documents that
both stores already consume the same package-level values.

#### Scenario: Both stores take the same precondition type

- GIVEN `EventsStore.WriteEvents(ctx, events, precondition)` and
  `StateStore.WriteState(ctx, state, precondition)`
- WHEN their signatures are compared
- THEN both declare `precondition` as `persistence.WritePrecondition`, the
  same type, with no store-specific variant

#### Scenario: No new conflict type is introduced

- GIVEN this capability's deliverables (spec/design/tasks only)
- WHEN they are reviewed
- THEN no new error type, sentinel, or precondition constructor is added
  anywhere in `persistence/`

### Requirement: Forward Reads Are the Canonical Read Contract; Backward Reads Are Not Required

`EventsStore.ReplayEvents`, `GetLatestEvent`, `PersistenceIDs`,
`GetShardEvents`, and `ShardOffsets` MUST be recognized as the complete
canonical forward-read contract. No backward or descending-order read method
is added by this capability, since no consumer in the current codebase
requires one.

#### Scenario: Forward reads cover existing consumers

- GIVEN `projection_runner.go` and `migration/migration.go`, the tree's
  actual `EventsStore` read consumers
- WHEN their usage is inspected
- THEN every read they perform is expressible via `ReplayEvents`,
  `GetLatestEvent`, `PersistenceIDs`, `GetShardEvents`, or `ShardOffsets`

#### Scenario: No backward-read method exists or is required

- GIVEN the full set of `EventsStore` consumers in the tree
- WHEN searched for any backward/descending iteration need
- THEN none is found, and no such method is added to the interface

### Requirement: Lifecycle Is Already Part of the Store Contract

`Connect`, `Disconnect`, and `Ping` on `EventsStore`, `StateStore`, and
`SnapshotStore` MUST be recognized as the canonical lifecycle contract for a
persistence port. No separate `Lifecycle`/`Health` capability interface is
introduced; a store's lifecycle stays a method set on the store interface
itself, consistent with how ego already treats stores as first-class
extension resources.

#### Scenario: Lifecycle methods are store methods, not a separate interface

- GIVEN `persistence.EventsStore`'s declared method set
- WHEN searched for `Connect`/`Disconnect`/`Ping`
- THEN they are found as methods directly on `EventsStore`, not on any
  wrapping or composed interface

### Requirement: Single-Mutation Atomicity Is the Only Atomicity This Contract Guarantees

A single `WriteEvents` call (one batch, one `persistence_id`) or a single
`WriteState` call MUST be atomic with respect to its `WritePrecondition`
check: no observable window exists between checking the precondition against
the persisted revision and committing. This contract explicitly does NOT
guarantee atomicity across multiple independent `WriteEvents`/`WriteState`
calls, cross-aggregate transactions, or all-or-nothing behavior for a
multi-event batch in the presence of partial adapter failure — those are
WRITE-006's (`#67`) concern and are out of scope here.

#### Scenario: Two independent writers racing genesis yield exactly one commit (EventsStore)

- GIVEN no persisted event exists for a `persistence_id`, and two independent
  `Engine`/actor-system instances sharing one `EventsStore`
- WHEN both dispatch a first command with `ExpectedRevision=0` concurrently
- THEN exactly one commits and the other is rejected with
  `concurrency_conflict`, proven under `-race` by
  `TestEventSourcedIntegrationConcurrentGenesisYieldsExactlyOneCommit`
  (`event_sourced_actor_integration_test.go`)

#### Scenario: Two independent writers racing genesis yield exactly one commit (StateStore)

- GIVEN no persisted state exists for a `persistence_id`, and two independent
  `Engine`/actor-system instances sharing one `StateStore`
- WHEN both dispatch a first command with `ExpectedRevision=0` concurrently
- THEN exactly one commits and the other is rejected with
  `concurrency_conflict`, proven under `-race` by
  `TestDurableStateConcurrentGenesisWritersYieldExactlyOneCommit`
  (`durable_state_actor_expected_revision_test.go`)

#### Scenario: Multi-event batch atomicity is explicitly out of scope

- GIVEN a `WriteEvents` call with more than one event in the batch
- WHEN this contract is consulted for what happens if a real (non-testkit)
  adapter fails partway through persisting the batch
- THEN no guarantee is made here; that guarantee, if any, is WRITE-006's
  (`#67`) to define against a real adapter capable of exhibiting partial
  failure

### Requirement: persistence_id Is an Opaque, Caller-Assigned Identity

`persistence_id` (`string`, on `Event`/`Snapshot`/`DurableState`) MUST be
documented as an opaque identifier assigned entirely by the caller, carrying
no structure the store interprets beyond byte-equality for keying reads,
writes, and `WritePrecondition` checks.

#### Scenario: persistence_id passes through unchanged

- GIVEN `entity.persistenceID` as set at actor construction
- WHEN any `EventsStore`/`StateStore` method is called
- THEN the same string is passed as `persistence_id`/`PersistenceId` with no
  transformation, prefixing, or parsing

### Requirement: Tenant Metadata Is Transported, Not Enforced as Isolation

`tenant_metadata` (`map<string,string>` on `Event`/`Snapshot`/`DurableState`)
MUST be documented as data the store persists and returns unchanged, but
never consults in any `WritePrecondition`/CAS decision. Carrying
`tenant_metadata` does NOT prevent two different tenants from presenting the
same `persistence_id` and colliding in the same store.

#### Scenario: tenant_metadata does not gate a conditional write

- GIVEN two writes with different `tenant_metadata` values but the same
  `persistence_id`
- WHEN both are subject to the same `WritePrecondition`
- THEN the store's accept/reject decision depends only on `persistence_id`
  and the declared precondition — `tenant_metadata` plays no role

#### Scenario: The existing safeguard is after-the-fact, not preventive

- GIVEN `tenancy.VerifyUnchanged` as the only existing tenant-drift check
- WHEN it runs
- THEN it compares the actor's bound tenant against metadata already
  recorded on a previously-written event/state for the same
  `persistence_id` — it detects drift on an established stream, it does not
  prevent an initial collision between two tenants presenting the same
  `persistence_id`

### Requirement: Tenant Isolation Design Is TENANT-003's, Not Pre-Decided Here

This capability MUST NOT introduce any tenant-scoped identity scheme —
composite keys, string-prefixed identities, or store-side namespacing are
explicitly not defined here. It documents the gap in the two requirements
above so that TENANT-003 (blocked per epic `#23` on "no EventStore SPI
contract to specify tenant scoping against") has a written, code-grounded
starting point.

#### Scenario: No composite or prefixed identity type is introduced

- GIVEN this capability's deliverables
- WHEN reviewed for any new identity type or naming convention
- THEN no `TenantPersistenceID`, no colon/prefix composition helper, and no
  namespace convention is introduced anywhere in the tree

## Out of Scope (cross-reference)

Idempotency, deduplication, and retry identity via `operation_id`: WRITE-005
(`#66`). Atomic multi-event append and partial-failure semantics on a real
adapter: WRITE-006 (`#67`). Tenant isolation implementation: TENANT-003 (not
yet filed). Concrete adapters: STORE-007/008/009. A conformance test suite as
a product: STORE-006. None of these are requirements of this spec.
