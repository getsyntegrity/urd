# Proposal - ReadSideProcessor canonical contract (EGO-READ-001, #71)

| Field | Value |
|---|---|
| Change | `ego-read-001` |
| Tracker | #71, epic #14 (EGO-READ) |
| Status | PROPOSED, awaiting owner approval. No production code. |
| Blocks | `ego-tenant-004` (#93) |
| Governance | `REVIEW_REQUIRED`: the contract is a proposal; implementation-readiness needs the owner's review (spec-authoring section 1). |

## Outcome

One reviewed definition of what a read side is in urd: how it is
registered, how it is identified, and what it consumes. Contract only; it
changes no code.

## Why now

#93 needs a contract to isolate. Today there is no `ReadSideProcessor`
anywhere in the repo (verified by search on `develop`). What exists is the
projection machinery, which already behaves like a durable, resumable
processor but names none of it as a contract:

- `projection.Options` and `projection.Handler` (`projection/`).
- `engine.WithProjection(name, *projection.Options)`, `StartProjection`,
  `StopProjection`, `IsProjectionRunning`, `RebuildProjection`
  (`engine/option.go`, `engine/projections.go`).
- `internal/engine/projection/projection_actor.go` hosts one
  `projectionrunner.Runner` per started projection.
- `internal/projectionrunner/runner.go` pulls events shard by shard from
  `persistence.EventsStore`, calls the handler, and commits an offset per
  shard batch through `offsetstore.OffsetStore`.

This proposal names that behaviour and fixes the vocabulary. It does not
design anything new beyond one explicit element (tenant scope, see D4).

## Owned by #71 vs left out

| Owned here | Left out (other issues) |
|---|---|
| Registration, identity, consumption vocabulary | Offset/checkpoint store design (READ-003) |
| Handler contract: ordering, delivery, error path | Claiming, leases, fencing, partitioning, failover (READ-006/007/008) |
| Lifecycle: register, start, stop, rebuild | Concrete adapters, Postgres (READ-014) |
| Where a tenant scope enters, as one declared element | Canonical EventEnvelope (READ-002) |
| | Topic/publication isolation (#94) |

## Approach

Specify the existing behaviour as the contract (spec delta
`specs/readside-processor/spec.md`) and mark the single new element as a
proposal for the owner to accept or reject. `design.md` records the
decisions and the alternatives considered.
