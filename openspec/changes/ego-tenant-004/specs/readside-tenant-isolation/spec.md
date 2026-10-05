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
R6. Once the runner has an explicit scope and a scoped subscription, the temporary projection wake-up introduced by #94 MUST be removed, and no public path MAY receive it.

## Acceptance Criteria (candidates)

| AC | Req | Observable pass/fail |
|---|---|---|
| AC-1 | R1 | Tenants A and B, same processor name and same entity ID: progress recorded for A is not visible to B, and conversely. |
| AC-2 | R2 | Processor for A over a journal holding A and B events: the handler sees only A's; B's events do not alter A's progress. |
| AC-3 | R1, R3 | A and B at different progress, both restarted: each resumes from its own progress; resetting A leaves B unchanged. |
| AC-4 | R1, R2 | `Unscoped()` and a tenant named "unscoped" do not share progress or events. |
| AC-5 | R4 | Scope undeterminable (or zero): start fails, nothing is read or recorded. |
| AC-6 | R5 | No-tenancy and fixed single-tenant engines run with no scope declared, and progress made before the change is still honoured. |
| AC-7 | R6 | Tenant-aware engine, projection bound to tenant A, wake topic absent: it advances on A's scoped publication, reading A's own journal and not an unscoped one. |
| AC-8 | R6 | Same setup: a publication scoped to tenant B does not advance A's projection. |
| AC-9 | R6 | After removal, nothing public (topic, subscription, option, exported symbol) delivers or can receive the wake. |

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

## Removal of the temporary wake-up (#94) - R6

Owner decision. #94 introduces a temporary wake-up so a tenant-aware
projection advances on tenant-scoped publications: the internal topic
`protocol.ProjectionWakeTopic`, the wrapper the projection actor hands the
runner (`internal/engine/projection/wake_stream.go`, `withProjectionWake`),
and the extra post in `protocol.PublishScoped`. These symbols are in the
#94/#316 line of work and are not on `develop` as of this writing; verify
against the code when the task starts.

**When it is removed:** when #93 gives the runner an explicit scope and a
scoped subscription. Not before.

**Replacement tests** (behavioural, storage-agnostic; AC-7 to AC-9):
- AC-7: a tenant-aware engine runs a projection bound to tenant A and it
  advances on A's scoped publication without the wake topic, reading A's own
  journal, not an unscoped one.
- AC-8: a publication scoped to tenant B does not advance A's projection.
- AC-9: no public path receives the wake after removal.

**Test retirement rule:** `TestProjectionWakeStream` and
`TestProjectionAdvancesOnTenantScopedPublication` (#316) are retired only in
the same change that adds tests proving AC-7 and AC-8, and the retirement
MUST NOT reduce coverage of "a tenant-aware projection still advances".

This does not decide offset identity, shard filtering or migration; those
stay deferred. #93 stays BLOCKED until the owner approves the #71 contract.

## Tasks

| Task | Acceptance |
|---|---|
| T0 Blocker: owner approves SPEC-READ-001 | all |
| T1 Design the deferred items | AC-1..AC-6 |
| T2 Tests first, then implementation | AC-1..AC-6 |
| T3 Add replacement tests, then remove the wake-up and retire the two tests | AC-7, AC-8, AC-9 |
