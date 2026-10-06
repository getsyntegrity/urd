# Tenancy Core Specification

## Purpose

Canonical, transport-neutral, GoAkt-independent tenant identity contract
(`TenantID`, `TenantContext`, `TenantResolver`, `WithSingleTenant`)
consumed by TENANT-002…008. Identity and boundary discipline only — no
remote propagation, no persistence namespace format, no engine wiring
(see `proposal.md` Scope).

## Requirements

### Requirement: TenantID Identity Type

`TenantID` MUST be a defined type over `string` (not an alias), and MUST
be non-empty. The system MUST NOT mandate any specific format (e.g. UUID).

#### Scenario: Empty TenantID rejected

- GIVEN an attempt to construct a `TenantID` from an empty string
- WHEN the constructor runs
- THEN construction fails

#### Scenario: Arbitrary non-empty identifier accepted

- GIVEN a non-empty, non-UUID string
- WHEN a `TenantID` is constructed from it
- THEN construction succeeds

### Requirement: TenantResolver Core Independence

The `TenantResolver` core contract MUST NOT depend on GoAkt or any
actor-runtime type. An automated import-graph check MUST enforce this.

#### Scenario: Conformance check blocks a forbidden import

- GIVEN the tenancy core package
- WHEN it imports a GoAkt or actor-runtime package
- THEN the import-graph conformance test fails; it passes on the
  initial implementation shipped by this change

### Requirement: Resolve-Once, Propagate-After Discipline

`TenantResolver` MUST resolve tenant identity exactly once, at the trust
boundary, producing a `TenantContext`. Direct `context.Context`
propagation is valid only on paths proven to preserve context
end-to-end. A saga boundary MUST NOT rely on implicit propagation; it
MUST reconstruct `TenantContext` explicitly from tenant-aware metadata
carried with the saga event/command.

#### Scenario: Same-node propagation preserves tenant identity

- GIVEN a `TenantContext` attached at a trust boundary
- WHEN execution proceeds along a path that preserves `context.Context`
- THEN the same tenant identity is observable downstream

#### Scenario: Saga boundary does not silently lose tenant identity

- GIVEN a saga step that resets to `context.Background()`
- WHEN the saga crosses that boundary
- THEN tenant identity is reconstructed from carried metadata, never
  silently dropped or inferred

### Requirement: Tenant Plus Aggregate Effective Identity

Tenant and aggregate identity together MUST form the execution's
effective identity. The system MUST NOT permit an aggregate's tenant to
vary per command.

#### Scenario: Per-command tenant switch is rejected

- GIVEN an aggregate already associated with a tenant
- WHEN a command targets it under a different tenant identity
- THEN the system rejects the command

### Requirement: Unified Single/Multi-Tenant Resolver Machinery

`WithSingleTenant(TenantID)` MUST be a built-in `TenantResolver`, not a
separate execution path. Single- and multi-tenant execution MUST
produce a `TenantContext` indistinguishable in kind.

#### Scenario: Single-tenant mode produces a standard TenantContext

- GIVEN an engine configured with `WithSingleTenant(id)`
- WHEN a resolver runs at a trust boundary
- THEN the resulting `TenantContext` has the same shape and guarantees
  as one produced by any other `TenantResolver`

### Requirement: TenantContext Validity and Administrative Scope

`TenantContext` MUST NOT be constructible in an invalid state: no empty
`TenantID`, no zero-value at a boundary. Administrative (non-tenant)
execution MUST be a distinct, explicit scope — never nil, empty, or a
magic tenant value — carrying attribution for who is acting and why.

#### Scenario: Constructing TenantContext with an empty TenantID fails

- GIVEN an empty `TenantID`
- WHEN a `TenantContext` is constructed with it
- THEN construction fails

#### Scenario: Administrative scope is type-distinct and attributed

- GIVEN an administrative (non-tenant) execution
- WHEN its `TenantContext` is constructed
- THEN the scope is a distinct type, not a sentinel, and carries
  attribution for who is acting and why

### Requirement: Tenant Identity Immutability Across Boundaries

Once an execution has a tenant identity, the system MUST NOT permit
that identity to change implicitly while crossing command, saga, event,
persistence, or projection boundaries.

#### Scenario: Identity survives a persistence, event, or projection boundary

- GIVEN an execution with an attached tenant identity
- WHEN it crosses a persistence, event, or projection boundary
- THEN the same tenant identity is observed on the other side, never
  silently changed or dropped
