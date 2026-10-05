# Proposal: Administrative and system tenant context semantics (EGO-TENANT-008, #96)

Parent epic: #23. Consumes the TenantContext primitives of #45 (`tenancy-core`).
Spec: `specs/tenancy-administrative/spec.md` (SPEC-TENANT-008).

**#96 stays OPEN.** This change records a decision and pins part of it with
tests; it does not satisfy #96. Criterion-by-criterion status is in the spec
under "Relation to #96".

## Problem

#96 asks how an administrative `tenancy.TenantContext` is consumed by
tenant-aware boundaries "when a real bypass need exists". The primitives exist
(`ScopeAdministrative`, `NewAdministrative(actor, reason)`); no boundary
consumes them.

## Decision and status

Decision taken by the owner for the engine cut (recorded in the session of 2026-10-05): no administrative bypass is offered in this cut. Recon found no operation that must act
across tenants:

- `Engine.EraseEntity` erases inside the caller's resolved tenant (GDPR is
  per-tenant).
- `migration.Migrator` and `TenantAdopter` take an explicit `persistence.Scope`
  per run; they use no `TenantContext`.
- Read side (#93) and publication (#94) are not tenant-aware yet, so there is
  nothing to bypass.

Command, saga status and erasure therefore deny the administrative scope (fail
closed), and an administrative-only resolver without `WithTenant` cannot bind a
spawn. Spawn itself does not consult or deny the caller's context: it binds the
tenant the application declares with `WithTenant` (or the resolver's fixed
tenant). That is an authorization question of the caller code, not a bypass;
see the spec section "Spawn: what is and is not pinned" (including what was only
demonstrated locally, and the remote spawn gap tracked in #305). No admin store
scope, bypass option or audit type is added.

## Scope

In: the spec and its evidence table, deterministic tests in
`engine/engine_tenant_administrative_scope_test.go`, comment-only rewording.
The test name `TestAdministrativeScopeIsDeniedAtEveryEngineEntry` overstates
what it proves for spawn (resolver-only); it is not renamed because this change
has no code change.

Out: any bypass; IAM; a persistent audit log; read-side and publisher denial.

## Status

| Item | State |
|---|---|
| Spec status | READY for the engine cut; not DONE |
| Security human gate | APPROVED by owner decision recorded in the session of 2026-10-05; scope = engine entry points pinned by tests only; does NOT cover read side, publication, spawn with `WithTenant`, or the remote spawn path |
| Governance verdict | ATOMIC (record in spec Design) |
| Engine-boundary ACs | PROVEN for what each states, except AC-R1-5 (spawn with a caller context plus `WithTenant`), NOT_PROVEN; gate approved for the pinned entry points only |
| #96 overall | NOT complete; stays OPEN |

## #96 criteria

- NOT_APPLICABLE (approved spec change, owner decision 2026-10-05): every supported bypass requires actor/reason; persistent audit evidence for bypass. No bypass is offered in this cut.
- NOT_PROVEN: boundaries accepting bypass are deliberate and fail closed. Read-side denial pending #93; publication denial pending #94.
- PROVEN at engine boundaries only: explicit admin context, no privilege from empty/default tenant, single-tenant is not admin mode.
- NOT_PROVEN (narrowed): the "deliberate and fail closed" criterion also lacks proof for a caller context combined with `WithTenant` at spawn, and for remote spawn (tracked in #305).

## Future bypass rule (prominent)

ANY future bypass MUST be per-operation and opt-in, require actor and reason,
fail closed, be backed by the persistent audit contract from #31, and be
introduced by a new governed spec.
