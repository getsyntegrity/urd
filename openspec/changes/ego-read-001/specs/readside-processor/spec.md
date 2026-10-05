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
same name and different scopes MUST be distinct, in the registry and in local
and cluster addressing, and no two different pairs may collide through an
ambiguous concatenation of scope and name. How the existing keying by name
alone is reconciled, and the key mechanism, are open (Q6).

### Requirement R3: Scope is explicit

The scope MUST be declared by the application or by a fixed single-tenant
resolver. It MUST NOT be taken from event payload or `tenant_metadata`, nor
built by prefixing or parsing the name. The scope is declared in
`projection.Options` at registration (proposed, Q2).

An OMITTED scope (nothing declared) and an EXPLICIT INVALID scope (declared,
but not a valid `persistence.Scope`) MUST be told
apart:

- an explicit invalid scope MUST be rejected in every mode, including a
  legacy engine and a fixed single-tenant engine, and MUST NOT fall back to
  `Unscoped()` or to the fixed tenant;
- an omitted scope MAY be resolved automatically only where R5 allows it.

A tenant-aware engine that cannot determine the scope MUST refuse to start
the processor and MUST NOT fall back to `Unscoped()`. How presence is
represented in the declaration is an open public API decision (see
`proposal.md`).

### Requirement R4: Consumption is bound to one scope

A processor's consumption MUST be bound to exactly one scope for its run, and
MUST stay bound to the same scope across restart. The scope declared at
registration is immutable for the run. How the binding is realised in
storage or reads is not decided here. Per-tenant fan-out inside one
processor is out of this cut: one instance per (scope, name).

### Requirement R5: Unscoped use needs nothing extra

An engine without tenancy MUST run processors with the scope omitted, bound to
`Unscoped()`. An engine with a fixed single-tenant resolver MUST run
processors with the scope omitted, bound to that fixed tenant. Neither
requires the application to declare a scope. This applies only to an omitted
scope; R3 still rejects an explicit invalid one.

## Acceptance Criteria

| AC | Requirement | Observable pass/fail |
|---|---|---|
| AC-R1-1 | R1 | Registering with an empty name fails. |
| AC-R2-1 | R2 | The same name under tenants A and B yields two independent instances: each keeps its own scope and its own registration, and acting on one does not act on the other. |
| AC-R2-2 | R2 | Two different (scope, name) pairs whose plain concatenation would be equal stay distinct: no collision in the registry or in addressing. |
| AC-R3-1 | R3 | A scope declared explicitly but not valid is rejected and nothing starts. |
| AC-R3-2 | R3 | Tenant-aware engine, scope omitted and undeterminable: start fails, no fallback to Unscoped. |
| AC-R3-3 | R3 | An explicit invalid scope is rejected on a legacy engine and on a fixed single-tenant engine too: nothing starts, no fallback to `Unscoped()` or to the fixed tenant. |
| AC-R4-1 | R4 | After restart the processor is still bound to its original scope. |
| AC-R5-1 | R5 | Legacy engine (no tenancy): a processor starts with no scope declared and no extra plumbing, bound to `Unscoped()`. |
| AC-R5-2 | R5 | Engine with a fixed single-tenant resolver: a processor starts with no scope declared by hand, bound to that tenant. |

## Human gates

Public API gate: PENDING (owner approval of this contract, including how
scope presence is represented in `projection.Options`). The resolutions in
`proposal.md` are PROPOSED, not approved.

## Risks / Open Questions

Q1-Q6 in `proposal.md`.
