# Proposal - Read-side and offset tenant isolation (EGO-TENANT-004, #93)

## Metadata

| Field | Value |
|---|---|
| spec_id | SPEC-TENANT-004 (change `ego-tenant-004`) |
| status | REVIEW_REQUIRED; implementation BLOCKED |
| parent_epic | #23 (EGO-TENANT) |
| dependencies | `ego-read-001` (#71) contract approval; `ego-tenant-003` (merged) |
| contracts_produced | none until #71 is approved |
| contracts_consumed | SPEC-READ-001 (processor identity and tenant binding, proposed); `persistence.Scope` |
| affected_subsystems | none decided; offset and read paths are deferred (see spec.md) |
| human_gates | Data-migration gate: PENDING (applies if a storage change results). Public API gate: PENDING, owned by SPEC-READ-001. |

## Outcome

Two tenants running a processor with the same name keep independent
progress and never receive each other's events, across restart.

## Context (verified on `develop`)

`offsetstore.OffsetStore` and `EventsStore.GetShardEvents`/`ShardOffsets`
carry no tenant, and `persistence/events_store.go` defers read-side
isolation to this issue. `Handler.Handle` receives no tenant.

## Scope

Behavioural acceptance cases (candidates) in `spec.md`.

## Out of Scope

Designing the processor API (#71), leases/fencing/claiming, topic isolation
(#94), adapters, administrative context (#96), the conformance suite (#95).

## Deferred until the #71 contract is approved

Offset identity, shard read filtering, schema/migration. Not decided here.
See `spec.md`.

## Tasks

| Task | Acceptance |
|---|---|
| T0 Blocker: owner approves SPEC-READ-001 | all |
| T1 Design deferred items | all |
| T2 Tests first, then implementation | AC-1..AC-6 |

Questions Q1-Q6: see `openspec/changes/ego-read-001/proposal.md`.
