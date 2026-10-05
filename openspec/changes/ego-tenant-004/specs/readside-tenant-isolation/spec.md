# Read-Side Tenant Isolation Specification (DRAFT, REVIEW_REQUIRED)

## Purpose

Behavioural, storage-agnostic isolation cases for read-side processors.
They are CANDIDATES: they stay candidates until the #71 contract
(`ego-read-001`) is approved, and name no key shape, filter mechanism,
table or migration.

## Requirements

R1. Progress of a processor MUST be independent per tenant scope.
R2. A processor bound to a tenant MUST receive only that tenant's events.
R3. A processor MUST stay bound to its scope across restart.
R4. A tenant-aware engine MUST fail closed when a processor's scope cannot be determined.
R5. Single-tenant and no-tenancy use MUST need no extra application plumbing and MUST keep prior progress.

## Acceptance Criteria (candidates)

| AC | Req | Observable pass/fail |
|---|---|---|
| AC-1 | R1 | Tenants A and B, same processor name and same entity ID: progress recorded for A is not visible to B, and conversely. |
| AC-2 | R2 | Processor for A over a journal holding A and B events: the handler sees only A's; B's events do not alter A's progress. |
| AC-3 | R1, R3 | A and B at different progress, both restarted: each resumes from its own progress; resetting A leaves B unchanged. |
| AC-4 | R1, R2 | `Unscoped()` and a tenant named "unscoped" do not share progress or events. |
| AC-5 | R4 | Scope undeterminable (or zero): start fails, nothing is read or recorded. |
| AC-6 | R5 | No-tenancy and fixed single-tenant engines run with no scope declared, and progress made before the change is still honoured. |

## Deferred until the #71 contract is approved

Not decided and not to be implemented from this document:

- how offset identity is composed;
- how shard reads are restricted to a scope;
- any schema change or migration (and its number), including a data
  migration check for existing rows.

## Human gates

Data-migration gate: PENDING (only if a storage change results). Public API
gate: PENDING (SPEC-READ-001).

## Test plan (for after approval)

AC-1..AC-6 in the unit lane: go-specs, mocks or testkit in-memory stores,
deterministic clock, no sleeps, no external resources. Any storage-level
check goes to the Postgres lane under `inttest`. Conformance additions are #95's.

## Risks / Open Questions

Q1-Q6 live in `openspec/changes/ego-read-001/proposal.md`; Q6 (identity vs
name-keyed registry and singleton) directly affects AC-1 and AC-3.
