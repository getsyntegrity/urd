# Proposal: Administrative and system tenant context semantics (EGO-TENANT-008, #96)

Parent epic: #23. Consumes the TenantContext primitives of #45 (`tenancy-core`).

## Problem

#96 asks how an administrative `tenancy.TenantContext` is consumed by
tenant-aware boundaries "when a real bypass need exists". The primitives exist
(`ScopeAdministrative`, `NewAdministrative(actor, reason)`); no boundary
consumes them.

## Decision (closed)

**No administrative bypass is supported.** Recon found no operation that must
act across tenants:

- `Engine.EraseEntity` erases inside the caller's resolved tenant (GDPR is
  per-tenant).
- `migration.Migrator` and `TenantAdopter` take an explicit `persistence.Scope`
  per run; they use no `TenantContext`.
- Read side (#93) and publication (#94) are not tenant-aware yet, so there is
  nothing to bypass.

Every boundary that resolves a `TenantContext` therefore denies the
administrative scope (fail closed). This change records that decision and pins
it with tests. It adds no admin store scope, no bypass option and no audit type.

## Scope

In: the requirement and its evidence (`specs/tenancy-administrative/spec.md`),
deterministic tests in `engine/engine_tenant_administrative_scope_test.go`,
comment-only rewording of stale "TENANT-008 will do this" notes.

Out: any bypass implementation; IAM; a persistent audit log; read-side and
publisher denial tests (follow-up once #93 and #94 land).

## Criteria status (#96)

| Criterion | Status |
|---|---|
| Administrative context explicit and distinguishable | PROVEN (`tenancy`, existing tests) |
| Empty/default tenant grants no admin privilege | PROVEN (this change) |
| Supported bypass requires actor/reason | NOT_APPLICABLE: no bypass is supported; construction still requires both |
| Boundaries accepting bypass are deliberate and fail closed | NOT_APPLICABLE for acceptance; denial PROVEN at engine boundaries |
| Persistent audit evidence | NOT_PROVEN, BLOCKED on #31 (EGO-OBS defines no persistent audit contract) |
| Single-tenant mode is not administrative mode | PROVEN (this change) |

## Future bypass rule

A later change introducing a bypass MUST be per-operation and opt-in, require
actor and reason, fail closed, and be gated on the audit contract from #31.

## Follow-ups

- Pin administrative denial on the read side after #93 and on publication after #94.
- Persistent audit contract (#31) before the epic's auditability criterion can close.
