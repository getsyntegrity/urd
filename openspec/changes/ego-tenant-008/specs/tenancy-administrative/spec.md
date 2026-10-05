# SPEC-TENANT-008: Administrative scope is denied at engine boundaries

## Metadata

| Field | Value |
|---|---|
| spec_id | SPEC-TENANT-008 (repository-defined; change `ego-tenant-008`) |
| status | READY for review and implementation of the approved engine cut: every ACTIVE criterion (AC-R1-1..4, AC-R2-1..2, AC-R3-1, AC-R4-1) corresponds to the approved scope and the required human gate is approved for it. READY does NOT mean VERIFIED, DONE, or that #96 is complete: the spec is NOT VERIFIED, NOT DONE and #96 is NOT complete. No blocking open question remains (spec-authoring section 17, SKILL.md:287, and section 18, SKILL.md:308-310). The one pending item, the authorization of remote spawn (#305), is outside the approved scope, tracked outside this spec and does NOT block this cut |
| governance verdict | ATOMIC (see Design, "Governance record") |
| parent_epic | #23 |
| tracker | #96, which **stays OPEN** (see "Relation to #96") |
| dependencies | #45 (shipped), TENANT-003 engine boundaries (shipped), #97 (shipped) |
| contracts_produced | invariant: no administrative bypass at engine boundaries |
| contracts_consumed | `tenancy.TenantContext` / `tenancy.Administrative` (`tenancy-core`); `tenancy-runtime` fail-closed gates |
| affected_subsystems | engine (tests and comments only); no production behavior change |
| human_gates | Security (AuthN/AuthZ boundary): APPROVED, approved by owner decision recorded in the session of 2026-10-05; scope = engine entry points pinned by this spec's tests only; does NOT cover read side, publication, spawn with `WithTenant`, or the remote spawn path |
| evidence status | every AC of this spec is PROVEN for what it states (spawn ACs: resolver-only cases, see Spawn section); the human gate is approved for the engine entry points pinned by tests; the spec is not VERIFIED; #96 as a whole is EVIDENCE_INCOMPLETE (read-side and publisher denial pending #93/#94; spawn with `WithTenant` is a decided behaviour of this cut, and remote spawn authorization is pending in #305 outside this cut) |

## Outcome

The engine rejects an administrative `tenancy.TenantContext` at command, saga
status and erasure, never lets an administrative-only resolver bind a spawn
(spawn does not consult the caller's context; see "Spawn: what is and is not
pinned"), and rejects an empty, zero-value, single-tenant or
legacy identity as a source of administrative privilege, with deterministic
tests that fail if any of those guards is removed.

## Context

`tenancy-core` ships the administrative scope and its actor/reason attribution
but no boundary consumes it. Recon (proposal.md) found no operation that must
cross tenants, so the decision is to support no bypass rather than build one.

## Scope

- S1: denial of the administrative scope at command, saga status and erasure, and no spawn binding from an administrative-only resolver (spawn does not consult or deny the caller's context; see "Spawn: what is and is not pinned").
- S2: no privilege from empty, zero-value, single-tenant or legacy identity.
- S3: the rule a future bypass must satisfy, recorded as a constraint.

## Out of Scope

- Any bypass implementation, admin store scope or bypass option.
- A persistent audit log or audit type (#31 owns the contract).
- Read-side (#93) and publisher (#94) denial: not pinned until those boundaries are tenant-aware.
- IAM, users, sessions.

## Requirements

- R1: A boundary that resolves the caller's `TenantContext` (command, saga status, erasure) MUST refuse one whose scope is not `ScopeTenant` before running behavior or touching any record, with `tenancy.ErrDenied`. Spawn does not resolve the caller's context: it takes the tenant from `WithTenant` or the resolver's fixed tenant and returns `ErrSpawnTenantUndetermined` when neither exists, so an administrative-only resolver without `WithTenant` cannot bind a spawn. R1 makes no claim about a caller context combined with `WithTenant` at spawn; that behaviour is a decided contract of this cut (see "Approved decisions for this cut"), not a requirement. (S1)
- R2: An empty tenant ID or the zero-value `TenantContext` MUST be rejected and MUST NOT be treated as administrative. (S2)
- R3: Single-tenant mode MUST resolve to `ScopeTenant`, never to the administrative scope, and MUST NOT reach another tenant's records. (S2)
- R4: Legacy mode (no resolver) MUST NOT be a source of administrative privilege; it keeps its existing unscoped behavior. (S2)

## Acceptance Criteria

| AC | Verifies | Statement |
|---|---|---|
| AC-R1-1 | R1 | Given an administrative-only resolver and no `WithTenant`, spawning an entity, saga or durable-state entity fails with `ErrSpawnTenantUndetermined`. (Resolver-only case; says nothing about a caller context combined with `WithTenant`.) |
| AC-R1-2 | R1 | Given a tenant-bound entity or durable-state entity, an administrative command fails with `ErrDenied` and behavior is not invoked. |
| AC-R1-3 | R1 | Given a tenant-bound saga, an administrative `SagaStatus` fails with `ErrDenied`. |
| AC-R1-4 | R1 | An administrative `EraseEntity` fails with `ErrDenied` and erases no scope. |
| AC-R2-1 | R2 | A resolver returning the zero `TenantContext`, with no `WithTenant`, cannot bind a spawn, and its erasure is rejected. |
| AC-R2-2 | R2 | A caller resolving to an empty tenant ID is rejected with `ErrInvalid` at erasure. |
| AC-R3-1 | R3 | A `WithSingleTenant` context has scope `ScopeTenant`, `Administrative()` is false, and its erasure leaves another tenant's record intact. |
| AC-R4-1 | R4 | With no resolver, an administrative caller's erasure reaches only the unscoped records and never a tenant's; no administrative path exists. |

## Spawn: what is and is not pinned

Spawn does not consult the caller's context. `spawnTenantScope`
(`engine/spawn_tenancy.go`) takes the tenant from `WithTenant(id)` (declared by
the application) or from the resolver's fixed tenant; with neither it returns
`ErrSpawnTenantUndetermined`. Spawn behaviour is a DECIDED contract of this cut, not an open question (see "Approved decisions for this cut"): `WithTenant` declares the actor's tenant at creation and does not authorize the caller, so spawn authority sits in the application-declared `WithTenant`. It is not an administrative bypass, because the bound tenant is the one the application declared. Earlier wording that the engine "denies the administrative scope at spawn" was imprecise; the test name `TestAdministrativeScopeIsDeniedAtEveryEngineEntry` also overstates what it proves (it is not renamed in this change, which has no code change).

**Pinned by committed tests**
- An administrative-only resolver without `WithTenant` cannot bind an entity, saga or durable-state spawn (`ErrSpawnTenantUndetermined`): `engine/engine_tenant_administrative_scope_test.go`, "an administrative-only resolver cannot bind a spawn" and the saga and durable-state cases of `TestAdministrativeScopeIsDeniedAtEveryEngineEntry`.
- A resolver returning the zero `TenantContext`, without `WithTenant`, cannot bind a spawn (`TestNoTenantIdentityGrantsAdministrativePrivilege`).

**DEMONSTRATED LOCALLY** (throwaway proof of concept; not committed, not run in Actions; no committed test)
- An administrative or other-tenant context passed to `Entity()`/spawn together with `WithTenant("acme")` is not denied; the entity is bound to the declared tenant (6/6 runs). Accepted by the decision above; not a requirement and not an AC.
- A raw `RemoteSpawn` with the correct qualified actor name and a matching `EntityTenantScope` but no tenant header pre-creates the actor, and a later legitimate `Entity()` for that tenant succeeds idempotently on it.
- A raw `TenantBindingQuery` from an unauthenticated peer is answered with `{tenant_aware, matches}`.
- Raw spawns with a wrong tenant, an unqualified name, a missing scope, a duplicate name or an ID mismatch fail at `PreStart` in a multi-tenant engine.

**HYPOTHETICAL IMPACT** (NOT executed, NOT demonstrated)
- A peer-chosen `EntityConfig` (retention or snapshot settings, for example `DeleteEventsOnSnapshot`) on a pre-created actor could delete events of the victim tenant.

**PENDING**
- Remote spawn of sagas and durable-state entities.
- Behavior with mTLS.
- An unexplained RoundRobin placement failure with an administrative context (0/6).

The remote pre-spawn is the authorization/configuration gap of remote spawn tracked in #305 (see #305 and the comment posted there); #96 does not fix it. PENDING DECISION, tracked outside this spec in #305 and not an AC, a task or a blocker: the authorization of REMOTE spawn. (a) which peers may create the actors of each tenant; (b) who may choose their retention and snapshot configuration; (c) mTLS authenticates the peer but does not by itself authorize those operations. Trusting the network explains the exposure but does not show that any peer should choose any tenant's retention. #305 is OUTSIDE the approved scope of #312 and does NOT block this cut. The Security gate covers only the engine entry points pinned by tests, not read side, publication, spawn with `WithTenant` (a decided behaviour, not a denied or proven one) or the remote spawn path. No bypass of any kind is proposed here.

## Constraints

- C1: **Obligation for ANY future bypass.** It MUST be per-operation and opt-in, require actor and reason (`tenancy.NewAdministrative`), fail closed, be backed by the persistent audit contract from #31, and be introduced by a new governed spec. No bypass is offered in this cut.
- C2: No boundary may be declared auditable until a persistent audit contract exists (#31).
- C3: No production behavior change in this change; tests and comments only.

## Contracts

Produced: invariant "no administrative bypass at engine boundaries" (R1 to R4).
Consumed: `tenancy-core` TenantContext/Administrative; `tenancy-runtime` fail-closed gates.

## Design

- Reuses existing guards; adds none: `actorNameFor` and `EraseEntity` (`engine/`), spawn tenant resolution (`spawnTenantScope`, which reads `WithTenant` or the resolver's fixed tenant and never the caller's context), actor `VerifyUnchanged`.
- Tests extend `engine/engine_tenant_administrative_scope_test.go` (go-specs, in-memory stores).
- Governance record: outcome expanded into denial (S1) and no-implicit-privilege (S2); both are verified by the same boundary tests and neither has a meaningful AC outside the outcome, so one spec. The audit contract and the read-side/publisher denial are separate deliverables owned elsewhere and are excluded, not bundled. The Security human gate does not imply a split.
- Human gate record: Security (AuthN/AuthZ boundary) approved by owner decision recorded in the session of 2026-10-05; scope = engine entry points pinned by this spec's tests only; does NOT cover read side, publication, spawn with `WithTenant`, or the remote spawn path.
- Spawn contract: decided, see "Approved decisions for this cut".

## Approved decisions for this cut

Placement rule followed: spec-authoring section 4 ("names may be adapted, semantic separation must stay clear") and section 14 (a design should "document decisions consistently"; a decision that would trigger a governance human gate "must be explicit here"). The template has no dedicated Decisions section, so this one is added.

- D1 (owner, session of 2026-10-05): no administrative bypass is offered in this cut.
- D2 (owner, approved for this cut): "WithTenant declara el tenant del actor durante su creación; no autoriza al llamador. El spawn local conserva ese comportamiento y no añade una validación del contexto del llamador." Translation: `WithTenant` declares the actor's tenant at creation; it does not authorize the caller. Local spawn keeps that behaviour and adds no validation of the caller's context. The former question "should an administrative or foreign caller context combined with `WithTenant` be refused at spawn?" is therefore DECIDED: not to add that validation in this cut. The locally demonstrated fact that such a context is accepted and bound to the declared tenant is kept (Spawn section). It is neither a requirement nor an AC, and it is not claimed as denied or proven.

Pending, outside this spec: PENDING DECISION, tracked outside this spec in #305 and not an AC, a task or a blocker: the authorization of REMOTE spawn. (a) which peers may create the actors of each tenant; (b) who may choose their retention and snapshot configuration; (c) mTLS authenticates the peer but does not by itself authorize those operations.

## Tasks

| Task | Implements | Maps to |
|---|---|---|
| T1 Pin that an administrative-only resolver cannot bind a saga/durable-state spawn, plus administrative denial at `SagaStatus` and durable-state command | R1 | AC-R1-1, AC-R1-2, AC-R1-3 |
| T2 Pin zero context, empty tenant, single-tenant | R2, R3 | AC-R2-1, AC-R2-2, AC-R3-1 |
| T3 Reword stale "TENANT-008 will do this" comments so they point at this decision | C3, R1 (keeps the recorded rule discoverable) | AC-R1-1 (comment text only; no behavior) |
| T5 Pin legacy-mode erasure | R4 | AC-R4-1 |
| T4 Record decision, status and the spawn limits | R1 to R4 | all (the spawn behaviour is recorded under "Approved decisions for this cut", not as an AC) |

Traceability: R1 to AC-R1-1..4 to T1 (AC-R1-4 pre-existing); R2 to AC-R2-1..2 to T2; R3 to AC-R3-1 to T2; R4 to AC-R4-1 to T5.

## Evidence

Revision: the head of `feat/96-administrative-tenant-semantics` as of the commit that adds this text (tests first added in `d85e9fe`, strengthened and extended in that commit). `engine/` is byte-identical between `d85e9fe` and `f8671e7` (empty diff), so the `f8671e7` text edits did not change the evidence.
Not proof by themselves: an agent claim, task completion, or the merged PR.

| AC | Status | Class | Evidence (file: test) |
|---|---|---|---|
| AC-R1-1 | PROVEN (resolver-only case) | TEST | `engine/engine_tenant_administrative_scope_test.go`: `TestAdministrativeScopeIsNeverAnAggregateTenantScope` (entity), `TestAdministrativeScopeIsDeniedAtEveryEngineEntry` (saga, durable-state) |
| AC-R1-2 | PROVEN | TEST | same file: `...NeverAnAggregateTenantScope` (entity), `...DeniedAtEveryEngineEntry` (durable-state) |
| AC-R1-3 | PROVEN | TEST | same file: `TestAdministrativeScopeIsDeniedAtEveryEngineEntry` |
| AC-R1-4 | PROVEN | TEST | same file: `TestAdministrativeScopeIsNeverAnAggregateTenantScope` |
| AC-R2-1 | PROVEN (resolver-only, no `WithTenant`) | TEST | same file: `TestNoTenantIdentityGrantsAdministrativePrivilege` |
| AC-R2-2 | PROVEN | TEST | same file: `TestNoTenantIdentityGrantsAdministrativePrivilege` |
| AC-R3-1 | PROVEN | TEST | same file: `TestNoTenantIdentityGrantsAdministrativePrivilege` |
| AC-R4-1 | PROVEN | TEST | same file: `TestNoTenantIdentityGrantsAdministrativePrivilege`, case "legacy mode erasure with an administrative caller reaches only the unscoped records"; spawn is covered by `TestEngineEntitySpawnWithoutResolverStaysUnscoped` |

Negative path: every R1 to R3 AC is itself a denial. Assertions use `errors.Is` against `tenancy.ErrDenied` / `ErrSpawnTenantUndetermined`. Guard check: local mutations were run by hand and are NOT reproducible from the repository (no committed mutation harness): removing the `actorNameFor` denial, the `EraseEntity` non-tenant denial, and making legacy erasure target a tenant scope each made the new assertions fail; the tree was restored afterwards.
Gap check: AC-R1-1..4, AC-R2-1..2, AC-R3-1 and AC-R4-1 have evidence for what they state. The spawn behaviour outside the resolver-only cases is not an AC and has no committed test (see Spawn section). The human gate is approved for the engine entry points pinned by tests only; it does not cover spawn with `WithTenant`, the remote spawn path, the read side or publication. The spec is not marked DONE: #96 stays open.

## Relation to #96

**#96 stays OPEN. Documenting this decision does not complete it.** Statuses use the evidence vocabulary. NOT_APPLICABLE is used for exactly two criteria, backed by an approved spec change: the owner decision of 2026-10-05 that no bypass is offered in this cut.

| #96 criterion | Status | Basis |
|---|---|---|
| Administrative context explicit and distinguishable | PROVEN | `tenancy` tests, e.g. `TestNewAdministrativeContext_IsTypeDistinctAndAttributed` (delivered by #45) |
| No empty/default tenant grants admin privilege | PROVEN at engine boundaries | AC-R2-1, AC-R2-2; read side and publisher not covered, see next rows |
| Every supported bypass requires actor/reason | NOT_APPLICABLE | no bypass is offered in this cut (owner decision 2026-10-05); the obligation for any future bypass is C1 |
| Boundaries accepting bypass are deliberate and fail closed | NOT_PROVEN | denial PROVEN at command, saga status and erasure, and for a resolver-only spawn; not covered: a caller context combined with `WithTenant` at spawn (decided behaviour kept in this cut, D2; neither denied nor proven) and remote spawn (authorization pending in #305, outside the approved scope); read-side denial pending #93; publication denial pending #94 |
| Persistent audit evidence for bypass | NOT_APPLICABLE | no bypass is offered in this cut (owner decision 2026-10-05). #31 (EGO-OBS) still defines no persistent audit contract; it becomes required by C1 before any bypass |
| Single-tenant mode is not administrative mode | PROVEN at engine boundaries | AC-R3-1 |

Overall #96: NOT complete and stays OPEN. Criterion 4 is NOT_PROVEN (read side pending #93, publication pending #94); criteria 1, 2 and 6 are met only for the boundaries listed; criteria 3 and 5 are NOT_APPLICABLE in this cut.

## Risks / Open Questions

- Resolved (engine cut): the Security human gate is approved by owner decision of 2026-10-05, engine boundaries only.
- Resolved: the bypass governance question; the owner decided no bypass is offered in this cut, so criteria 3 and 5 are NOT_APPLICABLE.
- DECIDED (no longer open): spawn with a caller context plus `WithTenant` keeps the current behaviour, D2 in "Approved decisions for this cut".
- PENDING, non-blocking, outside the approved scope, tracked in #305 (not an AC, task or blocker): authorization of remote spawn: (a) which peers may create the actors of each tenant; (b) who may choose their retention and snapshot configuration; (c) mTLS authenticates the peer but does not by itself authorize those operations.
- Open, blocking #96 closure: read-side admin denial (#93) and publication admin denial (#94). The gate does not cover them; each needs its own evidence and approval.
- Open, blocking any future bypass: the persistent audit contract (#31) and a new governed spec per C1.
