# Proposal - Read-side and offset tenant isolation (EGO-TENANT-004, #93)

## Metadata

| Field | Value |
|---|---|
| spec_id | SPEC-TENANT-004 (change `ego-tenant-004`) |
| status | READY for implementation design (contract approved); storage, ports and migration design PENDING; not VERIFIED, not DONE |
| parent_epic | #23 (EGO-TENANT) |
| dependencies | `ego-read-001` (#71) contract, APPROVED 2026-10-05; `ego-tenant-003` (merged) |
| contracts_produced | none yet; the #93 design may produce port or storage contracts |
| contracts_consumed | SPEC-READ-001 (processor identity and tenant binding, approved); `persistence.Scope` |
| affected_subsystems | none decided; offset and read paths are deferred (see spec.md) |
| human_gates | Data-migration gate: PENDING (applies if a storage change results). Public API gate: APPROVED (SPEC-READ-001, 2026-10-05). |

## Outcome

Two tenants running a processor with the same name keep independent
progress and never receive each other's events, across restart.

## Context (verified on `develop`)

`offsetstore.OffsetStore` and `EventsStore.GetShardEvents`/`ShardOffsets`
carry no tenant, and `persistence/events_store.go` defers read-side
isolation to this issue. `Handler.Handle` receives no tenant.

## Scope

Behavioural acceptance cases in `spec.md`.

## Out of Scope

Designing the processor API (#71), leases/fencing/claiming, topic isolation
(#94), adapters, administrative context (#96), the conformance suite (#95).

## Deferred to the #93 design (PENDING)

Offset identity, shard read filtering, port changes, schema/migration. Not
decided here; designed in #93 after reviewing existing data and adapters. See
`spec.md`.

## Tasks

| Task | Acceptance |
|---|---|
| T0 Owner approves SPEC-READ-001 | DONE: approved 2026-10-05 |
| T1 Design deferred items | all |
| T2 Tests first, then implementation | AC-1..AC-6 |

Q1-Q6 are decided (approved) in `openspec/changes/ego-read-001/proposal.md`.

## Temporary wake-up from #94

#93 also removes the temporary projection wake-up (R6, AC-7..AC-9, task T3
in `spec.md`) once the runner has an explicit scope and a scoped
subscription, and only together with the AC-7/AC-8 replacement tests. #93 is
no longer blocked by the #71 contract; it stays open until implemented and
demonstrated.
