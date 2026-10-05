# Tenancy Administrative Scope Specification

## Purpose

State how an administrative `tenancy.TenantContext` is treated by boundaries
that resolve a tenant. Decision rationale: `../../proposal.md`.

## Requirements

### Requirement: Administrative Scope Is Denied Where A Tenant Is Required

No engine entry point supports an administrative bypass. Where a boundary
needs a tenant, a `TenantContext` that is not `ScopeTenant` MUST be refused
before any behavior runs or any record is read, written or erased. Refusal is
`ErrSpawnTenantUndetermined` at spawn and `tenancy.ErrDenied` elsewhere.

#### Scenario: Spawn

- GIVEN a resolver that yields only an administrative context and no fixed tenant
- WHEN an entity, saga or durable-state entity is spawned without `WithTenant`
- THEN the spawn fails with `ErrSpawnTenantUndetermined`

#### Scenario: Command, status and erasure

- GIVEN a tenant-bound entity, saga or durable-state entity
- WHEN an administrative caller sends a command, reads saga status or erases
- THEN the call fails with `tenancy.ErrDenied`, behavior is not invoked and no scope is modified

### Requirement: No Tenant Identity Grants Administrative Privilege

An empty tenant ID, the zero-value `TenantContext`, single-tenant mode and
legacy (no resolver) mode MUST NOT be treated as administrative.

#### Scenario: Empty or zero identity

- GIVEN a resolver that yields an empty tenant ID or the zero `TenantContext`
- WHEN a spawn or erasure is attempted
- THEN it is rejected

#### Scenario: Single-tenant mode

- GIVEN `tenancy.WithSingleTenant("acme")`
- WHEN its context is resolved and an erasure runs
- THEN the scope is `ScopeTenant`, `Administrative()` reports false, and another tenant's records are untouched

### Requirement: Future Bypass Is Explicit And Audited

Any future bypass MUST be per-operation and opt-in, require actor and reason
(`tenancy.NewAdministrative`), fail closed, and not be declared auditable until
a persistent audit contract exists (#31).

## Evidence

| Requirement | Status | Where |
|---|---|---|
| Denied at entity spawn, command, erasure | PROVEN | `TestAdministrativeScopeIsNeverAnAggregateTenantScope` |
| Denied at saga/durable-state spawn, saga status, durable-state command | PROVEN | `TestAdministrativeScopeIsDeniedAtEveryEngineEntry` |
| Empty/zero/single-tenant grant nothing | PROVEN | `TestNoTenantIdentityGrantsAdministrativePrivilege`, `tenancy` tests |
| Read-side and publisher denial | NOT_PROVEN | blocked on #93, #94 |
| Persistent audit evidence | NOT_PROVEN | blocked on #31 |
