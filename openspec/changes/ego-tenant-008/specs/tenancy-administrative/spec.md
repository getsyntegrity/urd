# SPEC-TENANT-008: Administrative scope is denied at engine boundaries

## Metadata

| Field | Value |
|---|---|
| spec_id | SPEC-TENANT-008 (repository-defined; change `ego-tenant-008`) |
| status | DRAFT (human gate pending, see below) |
| governance verdict | ATOMIC (see Design, "Governance record") |
| parent_epic | #23 |
| tracker | #96, which **stays OPEN** (see "Relation to #96") |
| dependencies | #45 (shipped), TENANT-003 engine boundaries (shipped), #97 (shipped) |
| contracts_produced | invariant: no administrative bypass at engine boundaries |
| contracts_consumed | `tenancy.TenantContext` / `tenancy.Administrative` (`tenancy-core`); `tenancy-runtime` fail-closed gates |
| affected_subsystems | engine (tests and comments only); no production behavior change |
| human_gates | Security (AuthN/AuthZ boundary): **PENDING**, no approval reference recorded |
| evidence status | EVIDENCE_BLOCKED for #96 as a whole; EVIDENCE_INCOMPLETE for this spec's non-engine ACs (see Evidence) |

## Outcome

The engine rejects an administrative `tenancy.TenantContext` at every entry
point that needs a tenant, and rejects an empty, zero-value, single-tenant or
legacy identity as a source of administrative privilege, with deterministic
tests that fail if any of those guards is removed.

## Context

`tenancy-core` ships the administrative scope and its actor/reason attribution
but no boundary consumes it. Recon (proposal.md) found no operation that must
cross tenants, so the decision is to support no bypass rather than build one.

## Scope

- S1: denial of the administrative scope at spawn, command, saga status and erasure.
- S2: no privilege from empty, zero-value, single-tenant or legacy identity.
- S3: the rule a future bypass must satisfy, recorded as a constraint.

## Out of Scope

- Any bypass implementation, admin store scope or bypass option.
- A persistent audit log or audit type (#31 owns the contract).
- Read-side (#93) and publisher (#94) denial: not pinned until those boundaries are tenant-aware.
- IAM, users, sessions.

## Requirements

- R1: A boundary that needs a tenant MUST refuse a `TenantContext` whose scope is not `ScopeTenant` before running behavior or touching any record. Refusal is `ErrSpawnTenantUndetermined` at spawn and `tenancy.ErrDenied` elsewhere. (S1)
- R2: An empty tenant ID or the zero-value `TenantContext` MUST be rejected and MUST NOT be treated as administrative. (S2)
- R3: Single-tenant mode MUST resolve to `ScopeTenant`, never to the administrative scope, and MUST NOT reach another tenant's records. (S2)
- R4: Legacy mode (no resolver) MUST NOT be a source of administrative privilege; it keeps its existing unscoped behavior. (S2)

## Acceptance Criteria

| AC | Verifies | Statement |
|---|---|---|
| AC-R1-1 | R1 | Given an administrative-only resolver, spawning an entity, saga or durable-state entity without `WithTenant` fails with `ErrSpawnTenantUndetermined`. |
| AC-R1-2 | R1 | Given a tenant-bound entity or durable-state entity, an administrative command fails and behavior is not invoked (`ErrDenied` asserted for the durable-state entity; the entity test asserts a non-nil error and zero invocations). |
| AC-R1-3 | R1 | Given a tenant-bound saga, an administrative `SagaStatus` fails with `ErrDenied`. |
| AC-R1-4 | R1 | An administrative `EraseEntity` fails with `ErrDenied` and erases no scope. |
| AC-R2-1 | R2 | A resolver returning the zero `TenantContext` is rejected at spawn and erasure. |
| AC-R2-2 | R2 | A caller resolving to an empty tenant ID is rejected with `ErrInvalid` at erasure. |
| AC-R3-1 | R3 | A `WithSingleTenant` context has scope `ScopeTenant`, `Administrative()` is false, and its erasure leaves another tenant's record intact. |
| AC-R4-1 | R4 | With no resolver, spawn and erasure behave unscoped as before; no administrative path exists. |

## Constraints

- C1: A future bypass MUST be per-operation and opt-in, require actor and reason (`tenancy.NewAdministrative`), fail closed, and be introduced by a new governed spec.
- C2: No boundary may be declared auditable until a persistent audit contract exists (#31).
- C3: No production behavior change in this change; tests and comments only.

## Contracts

Produced: invariant "no administrative bypass at engine boundaries" (R1 to R4).
Consumed: `tenancy-core` TenantContext/Administrative; `tenancy-runtime` fail-closed gates.

## Design

- Reuses existing guards; adds none: `actorNameFor` and `EraseEntity` (`engine/`), spawn tenant resolution, actor `VerifyUnchanged`.
- Tests extend `engine/engine_tenant_administrative_scope_test.go` (go-specs, in-memory stores).
- Governance record: outcome expanded into denial (S1) and no-implicit-privilege (S2); both are verified by the same boundary tests and neither has a meaningful AC outside the outcome, so one spec. The audit contract and the read-side/publisher denial are separate deliverables owned elsewhere and are excluded, not bundled. The Security human gate does not imply a split.

## Tasks

| Task | Implements | Maps to |
|---|---|---|
| T1 Pin administrative denial at saga/durable-state spawn, `SagaStatus`, durable-state command | R1 | AC-R1-1, AC-R1-2, AC-R1-3 |
| T2 Pin zero context, empty tenant, single-tenant | R2, R3 | AC-R2-1, AC-R2-2, AC-R3-1 |
| T3 Reword stale "TENANT-008 will do this" comments | C3 | none (no behavior) |
| T4 Record decision and status | R1 to R4 | all |

Traceability: R1 to AC-R1-1..4 to T1 (AC-R1-4 pre-existing); R2 to AC-R2-1..2 to T2; R3 to AC-R3-1 to T2; R4 to AC-R4-1 (pre-existing tests only).

## Evidence

Revision: tests added at commit `d85e9fe` on `feat/96-administrative-tenant-semantics`; the PR head may be later.
Not proof by themselves: an agent claim, task completion, or the merged PR.

| AC | Status | Class | Evidence (file: test) |
|---|---|---|---|
| AC-R1-1 | PROVEN | TEST | `engine/engine_tenant_administrative_scope_test.go`: `TestAdministrativeScopeIsNeverAnAggregateTenantScope` (entity), `TestAdministrativeScopeIsDeniedAtEveryEngineEntry` (saga, durable-state) |
| AC-R1-2 | PROVEN | TEST | same file: `...NeverAnAggregateTenantScope` (entity), `...DeniedAtEveryEngineEntry` (durable-state) |
| AC-R1-3 | PROVEN | TEST | same file: `TestAdministrativeScopeIsDeniedAtEveryEngineEntry` |
| AC-R1-4 | PROVEN | TEST | same file: `TestAdministrativeScopeIsNeverAnAggregateTenantScope` |
| AC-R2-1 | PROVEN | TEST | same file: `TestNoTenantIdentityGrantsAdministrativePrivilege` |
| AC-R2-2 | PROVEN | TEST | same file: `TestNoTenantIdentityGrantsAdministrativePrivilege` |
| AC-R3-1 | PROVEN | TEST | same file: `TestNoTenantIdentityGrantsAdministrativePrivilege` |
| AC-R4-1 | NOT_PROVEN | TEST | existing legacy-mode tests (`TestEngineEntitySpawnWithoutResolverStaysUnscoped`) cover spawn only; no test asserts erasure or that no administrative path exists in legacy mode. Gap, not hidden. |

Negative path: every R1 to R3 AC is itself a denial. Guard check: removing the denial in `actorNameFor` made `TestAdministrativeScopeIsDeniedAtEveryEngineEntry` fail (manual mutation, not recorded as an automated artifact).
Gap check: complete for AC-R1-1..4, AC-R2-1..2, AC-R3-1; missing for AC-R4-1. This spec is therefore not VERIFIED, and not DONE.

## Relation to #96

**#96 stays OPEN. Documenting this decision does not complete it.** Statuses use the evidence vocabulary; NOT_APPLICABLE is not used because no approved spec change retired any criterion.

| #96 criterion | Status | Basis |
|---|---|---|
| Administrative context explicit and distinguishable | PROVEN | `tenancy` tests, e.g. `TestNewAdministrativeContext_IsTypeDistinctAndAttributed` (delivered by #45) |
| No empty/default tenant grants admin privilege | PROVEN at engine boundaries | AC-R2-1, AC-R2-2; read side and publisher not covered, see next rows |
| Every supported bypass requires actor/reason | BLOCKED | no bypass exists to evaluate; construction-level actor and reason are required and tested in `tenancy` |
| Boundaries accepting bypass are deliberate and fail closed | NOT_PROVEN | engine denial PROVEN (R1); read-side denial BLOCKED on #93; publisher denial BLOCKED on #94 |
| Persistent audit evidence exists | BLOCKED | #31 (EGO-OBS) defines no persistent audit contract |
| Single-tenant mode is not administrative mode | PROVEN at engine boundaries | AC-R3-1 |

Overall #96: EVIDENCE_BLOCKED (criteria 3, 4 and 5). Criteria 1, 2 and 6 are met only for the boundaries listed.

## Risks / Open Questions

- Blocking: Security human gate approval reference is missing; status cannot reach READY.
- Blocking for #96 closure: audit contract (#31); read-side (#93) and publisher (#94) denial.
- Non-blocking: AC-R4-1 needs an erasure-in-legacy-mode assertion, or an approved decision that existing coverage suffices.
- Unresolved governance finding: whether "no bypass" is the owner's accepted resolution of #96's bypass criteria (a spec-change decision) or leaves them BLOCKED; this spec records them as BLOCKED.
