# Proposal - Read-side and offset tenant isolation (EGO-TENANT-004, #93)

| Field | Value |
|---|---|
| Change | `ego-tenant-004` |
| Tracker | #93, epic #23 |
| Status | PROPOSED. Implementation BLOCKED until `ego-read-001` (#71) is approved. |
| Depends on | `ego-read-001` (contract), `ego-tenant-003` (`persistence.Scope`, merged) |
| Governance | `REVIEW_REQUIRED`: depends on an unapproved contract. |

## Outcome

Two tenants running a processor with the same name never share offsets or
events: offset identity and shard reads are scoped, and restart resumes each
tenant from its own position. Single-tenant needs no extra plumbing.

## The gap (verified on `develop`)

- `offsetstore.OffsetStore` identifies an offset by projection name and shard
  only; table `offsets_store` keys `(projection_name, shard_number)`.
- `EventsStore.GetShardEvents` and `ShardOffsets` are unscoped and are marked
  "TENANT-004's concern" in `persistence/events_store.go`. The shard is
  derived from the persistence ID, so two tenants share shards and one
  timestamp cursor.
- `Handler.Handle` receives no tenant.

## Scope and out of scope

In: scope-qualified offset identity, scope-filtered shard reads, one scope
per processor run, restart/resume under the same scope, acceptance cases.
Out: designing the processor API (#71), leases/fencing/claiming, topic
isolation (#94), adapters, administrative context (#96), the conformance
suite (#95).

## Why no implementation yet

The owner's decision: contract first. This change fixes the acceptance cases
against the proposed #71 contract (D4: scope is part of processor identity).
The signature and migration design is written only after #71 is approved.
