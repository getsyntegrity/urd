# tenancy-write-path Specification

## Purpose

Give `tenancy-core`'s ratified, consumer-less contract (`TenantResolver`,
`TenantContext`, `MarshalMetadata`/`UnmarshalMetadata`) real consumers on
ego's write path: persisted `egopb.Event`/`Snapshot`/`DurableState`, and the
`SagaActor` boundaries where `context.Context` does not survive. Tenant
becomes unforgeable data through real dispatch, including a saga hop and an
actor restart.

**PR1 (`#73`) proved this for the event-sourced actor, PR2 (`#77`) proved
it for the durable-state actor, and PR3 (`#78`) proved it for the saga —
including the real-dispatch e2e requirement.** All three PRs are merged;
no requirement group in this spec is open. Status is marked per requirement
below; do not treat a PROVEN tag as speculative.

## Implementation Status

| Requirement | Event-Sourced | Durable-State | Saga |
|---|---|---|---|
| Tenant metadata persisted on record | PROVEN (PR1 `#73`) | PROVEN (PR2 `#77`) | n/a |
| Fail-closed on missing/invalid/cross-tenant (TA3) | PROVEN (PR1) | PROVEN (PR2) | n/a |
| Actor-lifetime tenant identity | PROVEN (PR1) | PROVEN (PR2) | PROVEN (PR3 `#78`) |
| `TenantContext` reconstruction at async boundary | PROVEN (PR1, `recover()`) | PROVEN (PR2, `recoverFromStore`) | PROVEN (PR3, `eventContext`/`recover()`) |
| Tenant-less or cross-tenant event rejected at boundary | PROVEN (PR1) | PROVEN (PR2) | PROVEN (PR3, `bindOrVerify` + SG4 defer-past-`HandleEvent` correction) |
| Real-dispatch end-to-end integrity | n/a | n/a | PROVEN (PR3, `TestTenantWritePathE2E`) |
| No parallel envelope/serialization/resolver | HELD (PR1, PR2, PR3) | — | — |

## Requirements

### Requirement: Tenant Metadata Persisted as Data on Event, Snapshot, and Durable State

Persisted `egopb.Event`, `egopb.Snapshot`, and `egopb.DurableState` MUST
carry tenant identity as an additive metadata field via
`tenancy.MarshalMetadata`. All three are proven (`Event`/`Snapshot` in PR1,
`DurableState` in PR2) — `DurableState` reused the identical field shape
and carrier already shipped by PR1, no new mechanism.

#### Scenario: Event persisted with tenant metadata (PROVEN — PR1)
- GIVEN a command dispatched under a resolved `TenantContext`
- WHEN the resulting event is persisted
- THEN the persisted `egopb.Event` carries tenant metadata via `MarshalMetadata`

#### Scenario: Snapshot persisted with tenant metadata (PROVEN — PR1)
- GIVEN an actor taking a snapshot with an established `actorTenant`
- WHEN the snapshot is persisted
- THEN `egopb.Snapshot.tenant_metadata` carries that tenant via `MarshalMetadata`

#### Scenario: Durable state persisted with tenant metadata (PROVEN — PR2 `#77`)
- GIVEN a command dispatched under a resolved `TenantContext` against a durable-state aggregate
- WHEN the resulting state is persisted
- THEN `egopb.DurableState.tenant_metadata` carries that tenant via `MarshalMetadata`

### Requirement: Fail-Closed on Missing, Malformed, or Cross-Tenant Metadata (TA3)

The system MUST NOT backfill, grandfather, or default-assign a tenant. Any
record recovered/replayed without tenant metadata, or with metadata that
fails to decode, MUST be rejected with `tenancy.ErrInvalid`. A record or
command whose tenant identity mismatches the actor's already-established
identity MUST be rejected with `tenancy.ErrDenied`. This policy is
ratified and applies identically to both actor kinds — no softened variant
for durable-state.

#### Scenario: Missing tenant metadata fails closed at recovery (PROVEN — PR1, event-sourced)
- GIVEN a persisted `Event` or `Snapshot` with no tenant metadata
- WHEN the actor recovers using that record
- THEN recovery fails closed with `ErrInvalid`, no default tenant assigned

#### Scenario: Malformed tenant metadata fails closed at recovery (PROVEN — PR1, event-sourced)
- GIVEN a persisted record whose tenant metadata fails to decode
- WHEN the actor recovers using that record
- THEN recovery fails closed with `ErrInvalid`

#### Scenario: Cross-tenant command rejected, batched and non-batched, read and replay (PROVEN — PR1, event-sourced)
- GIVEN an event-sourced aggregate with an established `actorTenant`
- WHEN a command, a read (`getStateAndReply`), or a replayed event (`applyPersistedEvent`) carries a different tenant
- THEN it is rejected with `ErrDenied`

#### Scenario: Missing/malformed tenant metadata fails closed at durable-state recovery (PROVEN — PR2 `#77`)
- GIVEN a persisted `DurableState` with no or undecodable tenant metadata
- WHEN the actor recovers via `recoverFromStore`
- THEN recovery fails closed with `ErrInvalid`, mirroring the event-sourced actor exactly

#### Scenario: Cross-tenant command rejected against a durable-state aggregate (PROVEN — PR2 `#77`)
- GIVEN a durable-state aggregate with an established tenant identity
- WHEN a command targets it under a different tenant
- THEN it is rejected with `ErrDenied`

### Requirement: Actor-Lifetime Tenant Plus Aggregate Identity

Both actor kinds MUST hold tenant identity for the full actor lifetime,
seeded at recovery, not merely within one batch or command cycle.

#### Scenario: Event-sourced actor retains tenant identity across batches (PROVEN — PR1)
- GIVEN an event-sourced actor recovered with `actorTenant` seeded
- WHEN it processes a later, separate command batch
- THEN `actorTenant` is unchanged from the value seeded at recovery

#### Scenario: Durable-state actor retains tenant identity across commands (PROVEN — PR2 `#77`)
- GIVEN a durable-state actor recovered with a seeded tenant identity
- WHEN it processes a later command
- THEN its bound tenant identity is unchanged from the value seeded at recovery

### Requirement: TenantContext Reconstruction at Saga Async Boundaries

Wherever `context.Context` does not survive a mailbox/goroutine boundary,
the system MUST reconstruct `TenantContext` from carried metadata via
`tenancy.UnmarshalMetadata`. This applies to the five `SagaActor`
`context.Background()` reset sites.

#### Scenario: Saga boundary reconstructs tenant identity after context reset (PROVEN — PR3 `#78`, `eventContext`)
- GIVEN a `SagaActor` step at one of its five `context.Background()` reset sites
- WHEN the saga crosses that boundary using the received event's carried metadata
- THEN `TenantContext` is reconstructed before the step executes

### Requirement: Saga Actor Binds to First Tenant; Tenant-less and Cross-Tenant Events Rejected

A `SagaActor` instance MUST hold tenant identity for its full actor lifetime,
exactly like the event-sourced and durable-state actors (see Requirement:
Actor-Lifetime Tenant Plus Aggregate Identity). Because a saga has no
genesis record to seed from ahead of time, it binds to the tenant of the
first event it validly processes. An event reaching a `SagaActor` reset site
without tenant metadata, or with metadata that fails to decode, MUST be
rejected with `ErrInvalid` before that binding is touched. Once bound, an
event whose tenant differs from the already-bound tenant MUST be rejected
with `ErrDenied` — a saga instance MUST NOT silently process events from
more than one tenant. Replay MUST validate every replayed event against the
tenant established by the first replayed event, not last-wins.

A saga type that must legitimately serve more than one tenant concurrently
is explicitly out of scope for this change. That requires a distinct,
opt-in capability with its own authorization and identity model (for
example, per-`(saga, tenant)` actor instantiation) — never a silent
consequence of the current one-actor-per-`behavior.ID()` spawn model.

#### Scenario: Saga rejects a tenant-less event at a reset site (PROVEN — PR3 `#78`, `TestSagaActorEventContext`)
- GIVEN an event reaching a `SagaActor` boundary with no or undecodable tenant metadata
- WHEN the saga attempts to reconstruct `TenantContext` for that event
- THEN the event is rejected with `ErrInvalid` and no tenant binding is formed or changed

#### Scenario: Saga binds to the first valid tenant it observes (PROVEN — PR3 `#78`, `TestSagaActorBindOnFirstEvent`)
- GIVEN a freshly started `SagaActor` instance with no tenant bound yet
- WHEN it processes its first event carrying valid, decodable tenant metadata
- THEN it binds to that tenant as its actor-lifetime identity

#### Scenario: Saga rejects an event from a different tenant once bound (PROVEN — PR3 `#78`, `TestSagaActorBindOnFirstEvent`)
- GIVEN a `SagaActor` instance already bound to tenant A
- WHEN an event carrying tenant B's metadata reaches a reset site
- THEN it is rejected with `ErrDenied`, with no state mutation and no command dispatch

#### Scenario: Saga replay validates every event against the tenant seeded by the first replayed event (PROVEN — PR3 `#78`, `TestSagaActorRecoverReplayTenantValidation`)
- GIVEN a `SagaActor` recovering by replaying its persisted events
- WHEN a replayed event's tenant differs from the tenant established by the first replayed event
- THEN recovery fails closed, mirroring the event-sourced actor's replay-path gate

### Requirement: Real-Dispatch End-to-End Tenant Integrity

At least one automated test MUST exercise the real dispatch path —
`Engine.SendCommand` through the actor to a saga hop — and prove tenant
identity remains intact end-to-end. `TestSendCommandTenantResolution` does
NOT satisfy this requirement; it stops short of the saga hop.

#### Scenario: Tenant survives a real saga hop (PROVEN — PR3 `#78`, `TestTenantWritePathE2E`)
- GIVEN a command sent via `Engine.SendCommand` under a resolved tenant
- WHEN it traverses the actor and a saga hop that resets context
- THEN the tenant identity observed at the saga hop matches the originating tenant

### Requirement: No Parallel Envelope, Serialization, or Resolution Mechanism

This change MUST reuse `tenancy.MarshalMetadata`/`UnmarshalMetadata` and
`tenancy.TenantResolver` exactly as shipped in PR1. It MUST NOT introduce a
second tenant envelope, a new metadata map, or any resolution mechanism
outside `TenantResolver`, for either PR2 or PR3.

#### Scenario: No new carrier introduced by PR2 or PR3 (HELD — proven PR1, re-checked PR2/PR3)
- GIVEN PR2's `DurableState` persistence and PR3's saga reconstruction
- WHEN their tenant-carrying code is inspected
- THEN both call `tenancy.MarshalMetadata`/`UnmarshalMetadata` and neither defines a competing carrier

### Requirement: Query Envelopes Out of Scope

Since ego has no query-dispatch path today, this change MUST NOT extend
query envelopes with tenant metadata; that is owned by `#75`.

#### Scenario: No query envelope introduced
- GIVEN ego's current absence of a query-dispatch path
- WHEN this change ships (PR1, PR2, or PR3)
- THEN no query envelope or query-tenant-metadata mechanism is introduced

### Requirement: Architecture Conformance for New Pure-Value Packages

If PR2 or PR3 introduces a new pure-value package, an architecture-conformance
test MUST verify it does not import GoAkt or actor-runtime types.
`event_sourced_actor.go`, `durable_state_actor.go`, and `saga_actor.go`
legitimately import GoAkt and are exempt.

#### Scenario: New pure-value package stays GoAkt-independent
- GIVEN a new pure-value package introduced by PR2 or PR3
- WHEN the architecture-conformance test runs
- THEN it fails if the package imports GoAkt or actor-runtime types, and passes otherwise
