# SPEC-TENANT-008: Administrative scope is denied at engine boundaries

## Metadata

| Field | Value |
|---|---|
| spec_id | SPEC-TENANT-008 (repository-defined; change `ego-tenant-008`) |
| status | READY for the engine cut (owner decision of 2026-10-05); not DONE |
| governance verdict | ATOMIC (see Design, "Governance record") |
| parent_epic | #23 |
| tracker | #96, which **stays OPEN** (see "Relation to #96") |
| dependencies | #45 (shipped), TENANT-003 engine boundaries (shipped), #97 (shipped) |
| contracts_produced | invariant: no administrative bypass at engine boundaries |
| contracts_consumed | `tenancy.TenantContext` / `tenancy.Administrative` (`tenancy-core`); `tenancy-runtime` fail-closed gates |
| affected_subsystems | engine (tests and comments only); no production behavior change |
| human_gates | Security (AuthN/AuthZ boundary): APPROVED, approved by owner decision recorded in the session of 2026-10-05; scope = engine boundaries only (the entry points pinned by this spec's tests); does NOT cover read side or publication |
| evidence status | every AC of this spec is PROVEN (see Evidence) and its human gate is approved for the engine cut; #96 as a whole is EVIDENCE_INCOMPLETE (read-side and publisher denial pending #93/#94) |

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
| AC-R1-2 | R1 | Given a tenant-bound entity or durable-state entity, an administrative command fails with `ErrDenied` and behavior is not invoked. |
| AC-R1-3 | R1 | Given a tenant-bound saga, an administrative `SagaStatus` fails with `ErrDenied`. |
| AC-R1-4 | R1 | An administrative `EraseEntity` fails with `ErrDenied` and erases no scope. |
| AC-R2-1 | R2 | A resolver returning the zero `TenantContext` is rejected at spawn and erasure. |
| AC-R2-2 | R2 | A caller resolving to an empty tenant ID is rejected with `ErrInvalid` at erasure. |
| AC-R3-1 | R3 | A `WithSingleTenant` context has scope `ScopeTenant`, `Administrative()` is false, and its erasure leaves another tenant's record intact. |
| AC-R4-1 | R4 | With no resolver, an administrative caller's erasure reaches only the unscoped records and never a tenant's; no administrative path exists. |

## Constraints

- C1: **Obligation for ANY future bypass.** It MUST be per-operation and opt-in, require actor and reason (`tenancy.NewAdministrative`), fail closed, be backed by the persistent audit contract from #31, and be introduced by a new governed spec. No bypass is offered in this cut.
- C2: No boundary may be declared auditable until a persistent audit contract exists (#31).
- C3: No production behavior change in this change; tests and comments only.

## Contracts

Produced: invariant "no administrative bypass at engine boundaries" (R1 to R4).
Consumed: `tenancy-core` TenantContext/Administrative; `tenancy-runtime` fail-closed gates.

## Design

- Reuses existing guards; adds none: `actorNameFor` and `EraseEntity` (`engine/`), spawn tenant resolution, actor `VerifyUnchanged`.
- Tests extend `engine/engine_tenant_administrative_scope_test.go` (go-specs, in-memory stores).
- Governance record: outcome expanded into denial (S1) and no-implicit-privilege (S2); both are verified by the same boundary tests and neither has a meaningful AC outside the outcome, so one spec. The audit contract and the read-side/publisher denial are separate deliverables owned elsewhere and are excluded, not bundled. The Security human gate does not imply a split.
- Human gate record: Security (AuthN/AuthZ boundary) approved by owner decision recorded in the session of 2026-10-05; scope = engine boundaries only (the entry points pinned by this spec's tests); does NOT cover read side or publication.

## Tasks

| Task | Implements | Maps to |
|---|---|---|
| T1 Pin administrative denial at saga/durable-state spawn, `SagaStatus`, durable-state command | R1 | AC-R1-1, AC-R1-2, AC-R1-3 |
| T2 Pin zero context, empty tenant, single-tenant | R2, R3 | AC-R2-1, AC-R2-2, AC-R3-1 |
| T3 Reword stale "TENANT-008 will do this" comments so they point at this decision | C3, R1 (keeps the recorded rule discoverable) | AC-R1-1 (comment text only; no behavior) |
| T5 Pin legacy-mode erasure | R4 | AC-R4-1 |
| T4 Record decision and status | R1 to R4 | all |

Traceability: R1 to AC-R1-1..4 to T1 (AC-R1-4 pre-existing); R2 to AC-R2-1..2 to T2; R3 to AC-R3-1 to T2; R4 to AC-R4-1 to T5.

## Evidence

Revision: the head of `feat/96-administrative-tenant-semantics` as of the commit that adds this text (tests first added in `d85e9fe`, strengthened and extended in that commit). `engine/` is byte-identical between `d85e9fe` and `f8671e7` (empty diff), so the `f8671e7` text edits did not change the evidence.
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
| AC-R4-1 | PROVEN | TEST | same file: `TestNoTenantIdentityGrantsAdministrativePrivilege`, case "legacy mode erasure with an administrative caller reaches only the unscoped records"; spawn is covered by `TestEngineEntitySpawnWithoutResolverStaysUnscoped` |

Negative path: every R1 to R3 AC is itself a denial. Assertions use `errors.Is` against `tenancy.ErrDenied` / `ErrSpawnTenantUndetermined`. Guard check: local mutations were run by hand and are NOT reproducible from the repository (no committed mutation harness): removing the `actorNameFor` denial, the `EraseEntity` non-tenant denial, and making legacy erasure target a tenant scope each made the new assertions fail; the tree was restored afterwards.
Gap check: complete for all ACs of this spec; its required human gate is approved for the engine cut. Evidence is therefore sufficient for the engine cut only. The spec is not marked DONE: #96 stays open and read-side/publisher denial are not covered.

## Relation to #96

**#96 stays OPEN. Documenting this decision does not complete it.** Statuses use the evidence vocabulary. NOT_APPLICABLE is used for exactly two criteria, backed by an approved spec change: the owner decision of 2026-10-05 that no bypass is offered in this cut.

| #96 criterion | Status | Basis |
|---|---|---|
| Administrative context explicit and distinguishable | PROVEN | `tenancy` tests, e.g. `TestNewAdministrativeContext_IsTypeDistinctAndAttributed` (delivered by #45) |
| No empty/default tenant grants admin privilege | PROVEN at engine boundaries | AC-R2-1, AC-R2-2; read side and publisher not covered, see next rows |
| Every supported bypass requires actor/reason | NOT_APPLICABLE | no bypass is offered in this cut (owner decision 2026-10-05); the obligation for any future bypass is C1 |
| Boundaries accepting bypass are deliberate and fail closed | NOT_PROVEN | engine denial PROVEN (R1); read-side denial pending #93; publication denial pending #94 |
| Persistent audit evidence for bypass | NOT_APPLICABLE | no bypass is offered in this cut (owner decision 2026-10-05). #31 (EGO-OBS) still defines no persistent audit contract; it becomes required by C1 before any bypass |
| Single-tenant mode is not administrative mode | PROVEN at engine boundaries | AC-R3-1 |

Overall #96: NOT complete and stays OPEN. Criterion 4 is NOT_PROVEN (read side pending #93, publication pending #94); criteria 1, 2 and 6 are met only for the boundaries listed; criteria 3 and 5 are NOT_APPLICABLE in this cut.

## Risks / Open Questions

- Resolved (engine cut): the Security human gate is approved by owner decision of 2026-10-05, engine boundaries only.
- Resolved: the bypass governance question; the owner decided no bypass is offered in this cut, so criteria 3 and 5 are NOT_APPLICABLE.
- Open, blocking #96 closure: read-side admin denial (#93) and publication admin denial (#94). The gate does not cover them; each needs its own evidence and approval.
- Open, blocking any future bypass: the persistent audit contract (#31) and a new governed spec per C1.
