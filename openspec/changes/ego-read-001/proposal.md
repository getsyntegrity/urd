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

Resolution PROPOSED by the owner on 2026-10-05. It is a proposal, not an
approval: no explicit approval exists in the session or on GitHub (no review or
comment on #315; #71 has only the stale-bot notice). The questions stay open
and this contract stays REVIEW_REQUIRED until the owner approves it (Public API
gate: PENDING). Nothing here is implemented or approved. REVIEW_REQUIRED is not
READY, and neither means verified, #71 delivered or #93 delivered.

| Q | Question | Proposed resolution |
|---|---|---|
| Q1 | "ReadSideProcessor": vocabulary or alias type? | Vocabulary for the existing projection. No alias and no new abstraction. |
| Q2 | Where is the scope declared? | In `projection.Options`, during registration: `Scope *persistence.Scope` (PROPOSED API, see "Omitted versus invalid" below). Registration validates and copies the value. Immutable once registered and for the whole run. |
| Q3 | Per-tenant fan-out inside one processor? | Out of this cut: one instance per (scope, name). |
| Q4 | Do signature changes to existing ports pass the api-check? | Keep public signatures where possible; each port change is evaluated in #93, not decided here. |
| Q5 | Migration number and ownership | Designed in #93 after reviewing the data and the adapters. No number and no schema are fixed here. |
| Q6 | Identity (scope, name) versus keying by name alone | Use (scope, name) in the registry and in local and cluster addressing, with no collision from an ambiguous concatenation. Today the registry is a `map[string]*projection.Options` (`engine/projections.go`, `engine/option.go`) and the cluster singleton and the standalone actor are keyed by the name; re-keying them is implementation work under #93, and the key mechanism is not chosen here. |

### Omitted versus invalid

The contract must tell two inputs apart:

- **Omitted scope** (the application declared nothing). Resolved as follows:
  an engine without tenancy binds `Unscoped()`; an engine with a fixed
  single-tenant resolver binds that tenant; a tenant-aware engine without a
  fixed tenant refuses to start (R3), with no fallback.
- **Explicit scope** (the application declared one). An invalid scope is
  rejected in every mode, including a legacy engine and a fixed single-tenant
  engine, and never falls back to `Unscoped()` or to the fixed tenant. On any
  tenant-aware engine an explicit `Unscoped()` is rejected, with no fallback.
  On an engine with a fixed single-tenant resolver, an explicit scope that
  differs from that tenant is rejected.

**Proposed API representation (NOT approved; Public API gate PENDING):**
`Scope *persistence.Scope` in `projection.Options`.

- `nil` means the scope is omitted.
- A non-nil pointer to an invalid scope (`persistence.Scope` has unexported
  fields, so its zero value is the only invalid one) is an explicit invalid
  scope and is rejected.
- At registration the value is validated and COPIED. The pointer is not kept
  as the processor identity: assigning to the pointed-to variable after
  registration does not change the effective scope (AC-R3-6).

Rationale: with a plain `Scope` value, an omitted scope and an explicit
invalid one would be the same zero value and could not be told apart. The
pointer makes presence observable without a new type. This is a proposal from
the review of this PR; it needs the owner's approval before anything is
implemented.

Still open: how an explicit valid tenant scope is handled on an engine
without tenancy (not decided here).

### What approving this does not prove

Approving this contract fixes vocabulary, registration, identity and the
omitted-versus-invalid rule. It does not show isolation of the journal or of
the offsets: that is the implementation of #93 and its own acceptance
criteria. This change contains no code.

## Tasks

| Task | Requirement |
|---|---|
| T1 Owner review of the contract and Q1-Q6 | R1, R2, R3, R4 |
| T2 After approval, cut implementation as a separate change | none authorized here |

## Cross-reference

`ego-tenant-004` R6 removes the temporary projection wake-up from #94 once
the runner has an explicit scope and a scoped subscription. It depends on R4
(consumption bound to one scope) here.
