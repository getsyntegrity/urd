# Read-Side Processor Specification (DRAFT, REVIEW_REQUIRED)

## Purpose

Minimal contract for registering and identifying a read-side processor and
binding its consumption to a tenant scope. Everything else about a
processor's behaviour (lifecycle, ordering, delivery, recovery) is whatever
`projection/`, `engine/projections.go` and `internal/projectionrunner`
define, and is unchanged. Note for readers: shard reads are ordered by
timestamp (`GetShardEvents`); any per-entity ordering is an inference from
that, not a guarantee this contract adds.

## Requirements

### Requirement R1: A processor has a name

A processor MUST be registered under a non-empty name.

### Requirement R2: A processor's identity includes a scope

The processor identity MUST be the pair (scope, name), where scope is a
`persistence.Scope`: `Unscoped()` or one tenant. Two processors with the
same name and different scopes MUST be distinct. How the existing keying by
name alone is reconciled is open (Q6).

### Requirement R3: Scope is explicit

The scope MUST be declared by the application or by a fixed single-tenant
resolver. It MUST NOT be taken from event payload or `tenant_metadata`, nor
built by prefixing or parsing the name. A zero-value scope MUST be rejected.
A tenant-aware engine that cannot determine the scope MUST refuse to start
the processor and MUST NOT fall back to `Unscoped()`.

### Requirement R4: Consumption is bound to one scope

A processor's consumption MUST be bound to exactly one scope for its run, and
MUST stay bound to the same scope across restart. How the binding is realised
in storage or reads is not decided here.

### Requirement R5: Unscoped use needs nothing extra

An engine without tenancy, or with a fixed single tenant, MUST run
processors without the application declaring a scope.

## Acceptance Criteria

| AC | Requirement | Observable pass/fail |
|---|---|---|
| AC-R1-1 | R1 | Registering with an empty name fails. |
| AC-R2-1 | R2 | Same name under tenants A and B yields two distinct processors. |
| AC-R3-1 | R3 | A zero-value scope is rejected and nothing starts. |
| AC-R3-2 | R3 | Tenant-aware engine, scope undeterminable: start fails, no fallback to Unscoped. |
| AC-R4-1 | R4 | After restart the processor is still bound to its original scope. |
| AC-R5-1 | R5 | Engine without tenancy starts a processor with no scope declared. |

## Human gates

Public API gate: PENDING.

## Risks / Open Questions

Q1-Q6 in `proposal.md`.
