# Proposal - ReadSideProcessor minimal contract (EGO-READ-001, #71)

## Metadata

| Field | Value |
|---|---|
| spec_id | SPEC-READ-001 (change `ego-read-001`) |
| status | REVIEW_REQUIRED (contract proposal; not READY) |
| parent_epic | #14 (EGO-READ); consumed by #23 (EGO-TENANT) |
| dependencies | `ego-tenant-003` (`persistence.Scope`, merged) |
| contracts_produced | ReadSideProcessor registration and identity vocabulary; tenant binding element |
| contracts_consumed | `persistence.Scope`; existing projection API (`projection/`, `engine/projections.go`) |
| affected_subsystems | none yet (docs/spec only); candidates: `projection/`, `engine/` |
| human_gates | Public API gate: PENDING (owner approval of this contract). |

## Outcome

One reviewed, minimal definition of how a read-side processor is registered,
identified, and bound to a tenant scope when it consumes. It changes no code.

## Context

There is no `ReadSideProcessor` in the repo (search on `develop`). The
nearest thing is the projection machinery: `projection.Options` and
`projection.Handler`; `engine.WithProjection(name, ...)`, `StartProjection`
and siblings in `engine/projections.go`; `internal/projectionrunner`. Its
lifecycle, delivery and recovery behaviour is defined by that code and is
NOT restated here.

## Scope

Registration, processor identity, how consumption is bound to a tenant scope.

## Out of Scope

Offset/checkpoint store (READ-003), claiming, leases, fencing, partitioning,
failover (READ-006/007/008), adapters (READ-014), canonical envelope
(READ-002), topic isolation (#94), lifecycle and recovery redesign.

## Open questions (blocking; owner decides)

- Q1. "ReadSideProcessor": vocabulary for the existing projection, or an alias type?
- Q2. Where is the scope declared: option on `WithProjection`, field of `projection.Options`, or argument to `StartProjection`? (The first two touch `engine/option.go`.)
- Q3. Per-tenant fan-out inside one processor: in or out? Draft: out.
- Q4. Do signature changes to existing ports pass the api-check? (TENANT-003 changed `EventsStore`.)
- Q5. Migration number and ownership for any storage change.
- Q6. Identity (scope, name) conflicts with today's keying by name alone: the registry is a `map[string]*projection.Options` (`engine/projections.go`, `engine/option.go`) and the cluster singleton and the standalone actor are keyed by the name. Re-keying them (or composing a key) is a design decision for the owner; this draft does not make it.

## Tasks

| Task | Requirement |
|---|---|
| T1 Owner review of the contract and Q1-Q6 | R1, R2, R3, R4 |
| T2 After approval, cut implementation as a separate change | none authorized here |
