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

PROPOSED representation (not approved; Public API gate PENDING): the
declaration is `Scope *persistence.Scope`. `nil` means omitted; a non-nil
pointer to an invalid scope is an explicit invalid scope. At registration the
value MUST be validated and copied; the pointer MUST NOT be kept as the
processor identity, and later changes to the pointed-to variable MUST NOT alter
the processor's scope.

An OMITTED scope and an EXPLICIT scope MUST be told apart:

- an explicit invalid scope MUST be rejected in every mode, including a
  legacy engine and a fixed single-tenant engine, and MUST NOT fall back to
  `Unscoped()` or to the fixed tenant;
- on any tenant-aware engine an explicit `Unscoped()` MUST be rejected, with
  no fallback;
- on an engine with a fixed single-tenant resolver, an explicit scope that
  differs from that tenant MUST be rejected, with no fallback (PROPOSED);
- on an engine without tenancy, only `Unscoped()` is admitted (omitted or
  explicit); an explicit tenant scope MUST be rejected, and a projection MUST
  NOT enable tenancy implicitly (PROPOSED);
- an omitted scope is resolved only where R5 allows it.

A tenant-aware engine without a fixed tenant that cannot determine the scope
(omitted) MUST refuse to start the processor and MUST NOT fall back to
`Unscoped()`: a multi-tenant engine requires an explicit declaration.

### Requirement R4: Consumption is bound to one scope

A processor's consumption MUST be bound to exactly one scope for its run, and
MUST stay bound to the same scope across restart. The scope declared at
registration is immutable for the run: nothing may change the scope of an
instance that is already registered or active, including across restart.
Registering the same name under a different scope is NOT such a change: the
identity is (scope, name), so it is a different instance (R2). How the binding is realised in
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
| AC-R3-4 | R3 | Fixed single-tenant resolver for tenant T, explicit scope for tenant U (U differs from T): rejected, nothing starts, no fallback to T (PROPOSED). |
| AC-R3-5 | R3 | Any tenant-aware engine (fixed single-tenant or multi-tenant), explicit `Unscoped()`: rejected, nothing starts, no fallback (PROPOSED). |
| AC-R3-6 | R3 | A processor is registered with a pointer to a valid scope A; the pointed-to variable is then assigned scope B: the processor's effective scope is still A, in registration, in addressing and after restart (PROPOSED representation). |
| AC-R3-7 | R3 | Engine without tenancy, explicit tenant scope: rejected, nothing starts, tenancy is not enabled. Explicit `Unscoped()` is admitted and binds `Unscoped()` (PROPOSED). |
| AC-R4-1 | R4 | After restart the processor is still bound to its original scope. |
| AC-R4-2 | R4 | With the existing registration, start, stop and restart paths, the effective scope of a registered or active instance never differs from the one validated at its registration. Registering the same name under another scope yields a separate instance and leaves the first one's scope, registration and progress unchanged. No modification API is assumed or added to test this. |
| AC-R5-1 | R5 | Legacy engine (no tenancy): a processor starts with no scope declared and no extra plumbing, bound to `Unscoped()`. |
| AC-R5-2 | R5 | Engine with a fixed single-tenant resolver: a processor starts with no scope declared by hand, bound to that tenant. |

## Human gates

Public API gate: PENDING (owner approval of this contract, including the
proposed `Scope *persistence.Scope` representation in `projection.Options`).
The resolutions in `proposal.md`, the fixed-resolver rule and the explicit
`Unscoped()` rule are PROPOSED, not approved.

## Risks / Open Questions

Q1-Q6 in `proposal.md`.
