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

## Decision (closed)

No administrative bypass is supported. Recon found no operation that must act
across tenants:

- `Engine.EraseEntity` erases inside the caller's resolved tenant (GDPR is
  per-tenant).
- `migration.Migrator` and `TenantAdopter` take an explicit `persistence.Scope`
  per run; they use no `TenantContext`.
- Read side (#93) and publication (#94) are not tenant-aware yet, so there is
  nothing to bypass.

Engine boundaries therefore deny the administrative scope (fail closed). No
admin store scope, bypass option or audit type is added.

## Scope

In: the spec and its evidence table, deterministic tests in
`engine/engine_tenant_administrative_scope_test.go`, comment-only rewording.

Out: any bypass; IAM; a persistent audit log; read-side and publisher denial.

## Status

| Item | State |
|---|---|
| Spec status | DRAFT; Security human gate has no recorded approval |
| Governance verdict | ATOMIC (record in spec Design) |
| Engine-boundary ACs | PROVEN except AC-R4-1 (NOT_PROVEN) |
| #96 overall | EVIDENCE_BLOCKED |

## Pending for #96

- Persistent audit evidence: BLOCKED on #31 (no audit contract).
- Read-side admin denial: BLOCKED on #93. Publisher admin denial: BLOCKED on #94.
- Bypass criteria (actor/reason, deliberate fail-closed acceptance): no bypass exists; whether the owner accepts "no bypass" as resolving them is an open governance decision.

## Future bypass rule

Per-operation, opt-in, actor and reason required, fail closed, gated on the
audit contract from #31, and introduced by a new governed spec.
