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

Resolution PROPOSED by the owner on 2026-10-05. It is a proposal: the questions
stay open and this contract stays REVIEW_REQUIRED until the owner approves it
(Public API gate: PENDING). Nothing here is implemented or approved.

| Q | Question | Proposed resolution |
|---|---|---|
| Q1 | "ReadSideProcessor": vocabulary or alias type? | Vocabulary for the existing projection. No alias and no new abstraction. |
| Q2 | Where is the scope declared? | In `projection.Options`, during registration. Immutable once registered and for the whole run. (Representation: see "Omitted versus invalid" below.) |
| Q3 | Per-tenant fan-out inside one processor? | Out of this cut: one instance per (scope, name). |
| Q4 | Do signature changes to existing ports pass the api-check? | Keep public signatures where possible; each port change is evaluated in #93, not decided here. |
| Q5 | Migration number and ownership | Designed in #93 after reviewing the data and the adapters. No number and no schema are fixed here. |
| Q6 | Identity (scope, name) versus keying by name alone | Use (scope, name) in the registry and in local and cluster addressing, with no collision from an ambiguous concatenation. Today the registry is a `map[string]*projection.Options` (`engine/projections.go`, `engine/option.go`) and the cluster singleton and the standalone actor are keyed by the name; re-keying them is implementation work under #93, and the key mechanism is not chosen here. |

### Omitted versus invalid

The contract must tell two inputs apart, and the earlier text did not:

- **Omitted scope** (the application declared nothing). It MAY be resolved
  automatically where no declaration is needed: an engine without tenancy
  binds `Unscoped()`, and an engine with a fixed single-tenant resolver binds
  that tenant. A tenant-aware engine without a fixed tenant refuses to start
  (R3).
- **Explicit invalid scope** (the application declared a scope that is not
  valid, today only the zero value of `persistence.Scope`, which is what a
  discarded `NewTenantScope` error leaves). It MUST be rejected in every
  mode, including a legacy engine and a fixed single-tenant engine. It must
  never fall back to `Unscoped()` or to the fixed tenant.

`persistence.Scope` is a struct with unexported fields, so its zero value is
the only invalid one. If `projection.Options` carried a plain `Scope` value, an
omitted scope and an explicit invalid one would be the same zero value and the
contract could not distinguish them. The declaration therefore has to carry
presence (for example a pointer, or a separate registration argument). That
representation is a public API decision for the owner under the Public API
gate; it is not chosen here. How an explicit valid scope that differs from a
fixed single-tenant resolver's tenant is handled is also not decided here.

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
