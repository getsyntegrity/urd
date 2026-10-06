# Durable-State Concurrency Specification

## Purpose

Canonical requirements for `DurableStateActor`'s consumption of the caller's
`ExpectedRevision` precondition (`CONTRACT-EXPECTED-REVISION-v1`, produced by
`command-envelope`) and the atomic conditional-write SPI
(`CONTRACT-CONDITIONAL-WRITE-v1`, produced by `persistence-concurrency`), so
that `command → ExpectedRevision → DurableStateActor → StateStore conditional
write → persistence result → command.Result` yields exactly one commit and
exactly one `concurrency_conflict` rejection for two independent writers
declaring the same `ExpectedRevision=N` (proposal.md's core guarantee, AC7).
This capability is `independent` of `event-sourced-concurrency` — neither
consumes the other's integration surface. No domain-handler signature
changes; `ExpectedRevision` never reaches `HandleCommand`/`HandleEnvelope`.

## Requirements

### Requirement: ExpectedRevision Is Read as a Write Precondition, Not Domain Input (AC7)

`DurableStateActor` MUST extract `ExpectedRevision` from the dispatched
command's metadata (`CONTRACT-EXPECTED-REVISION-v1`) after
`dispatchToBehavior` returns, and MUST NOT pass it into `HandleCommand` or
`HandleEnvelope`. The extracted value MUST travel only to the persistence
step of command processing (today's `commitState`), never mutating the
`priorVersion`/`priorState` the handler already received.

#### Scenario: Handler signature stays untouched

- GIVEN a `DurableStateBehavior` implementation
- WHEN a command carrying `ExpectedRevision=N` is dispatched
- THEN `HandleCommand`/`HandleEnvelope` receives the same
  `(ctx, cmd, priorVersion, priorState)` shape it received before this
  capability existed

#### Scenario: Precondition reaches persistence, not the handler

- GIVEN a command carrying `ExpectedRevision=N`
- WHEN `DurableStateActor` processes it
- THEN `N` is visible to the conditional write step and never appears as an
  argument to the domain handler

### Requirement: Conditional Write Delegates the Compare-and-Commit to Persistence, Including Genesis (AC7)

`DurableStateActor` MUST translate the extracted `ExpectedRevision` into
`CONTRACT-CONDITIONAL-WRITE-v1`'s precondition type and pass it to
`StateStore`'s conditional-write operation. The actor MUST NOT perform its
own read-compare-write against `entity.currentVersion` as a substitute for
the store-level check; `entity.currentVersion` MAY be stale across a
restart, another node, or a concurrent actor instance and MUST NOT be the
authority for the commit decision — including the `ExpectedRevision=0`
genesis case.

#### Scenario: Exact-match precondition commits

- GIVEN a persisted `StateStore` record at revision N for a persistence ID
- WHEN a command with `ExpectedRevision=N` is processed
- THEN the conditional write succeeds and the new state, at version N+1, is
  durably committed

#### Scenario: Stale precondition is rejected at the store, not the actor cache

- GIVEN a persisted `StateStore` record already advanced past revision N by
  another writer the local actor instance never observed
- WHEN a command with `ExpectedRevision=N` is processed
- THEN the conditional write reports a precondition mismatch even though the
  local actor's own `currentVersion` still reads N

#### Scenario: Concurrent genesis writers against one persistence ID

- GIVEN no persisted `StateStore` record exists for a persistence ID
- WHEN two independent `DurableStateActor` instances each process a first
  command with `ExpectedRevision=0` for that persistence ID
- THEN exactly one conditional write commits and the other is rejected with
  `concurrency_conflict`

### Requirement: `checkPreconditions`/`priorVersion` Stays a Separate, Unmodified Concern

The existing `checkPreconditions` invariant (actor-local: the dispatched
handler's returned version must differ from `entity.currentVersion` by
exactly one) MUST continue to run unmodified and MUST remain conceptually
distinct from the new `ExpectedRevision` precondition. A `checkPreconditions`
failure (handler produced a non-adjacent version) and an `ExpectedRevision`
conflict (persisted revision does not match the caller's declared
precondition) are different failure classes and MUST be distinguishable by
the caller — a `checkPreconditions` failure MUST NOT be reported with
`Failure.Code()=="concurrency_conflict"`.

#### Scenario: Both checks run independently

- GIVEN a command whose handler returns a version adjacent to
  `entity.currentVersion` (satisfies `checkPreconditions`) and an
  `ExpectedRevision` that no longer matches the persisted revision
- WHEN the command is processed
- THEN `checkPreconditions` passes and the command still fails with
  `concurrency_conflict` from the conditional write

#### Scenario: A non-adjacent version is never reported as a concurrency conflict

- GIVEN a handler that returns a version not adjacent to
  `entity.currentVersion`
- WHEN `checkPreconditions` rejects it
- THEN the resulting failure's code is not `concurrency_conflict`

### Requirement: Conflict Surfaces as `OutcomeRejected` / `concurrency_conflict` (AC7)

When the conditional write reports a precondition mismatch, `DurableStateActor`
MUST NOT run the commit step's in-memory mutation (`currentState`,
`currentVersion`, `actorTenant`, cached state marshal stay unchanged) and
MUST NOT publish the rejected state. Once reconstructed on the caller side
as a `command.Result` (per `command-envelope`'s six-outcome taxonomy), the
conflict MUST report `Outcome()==OutcomeRejected` with
`Failure.Code()=="concurrency_conflict"` — never `OutcomeFailed`, the default
classification every other actor error currently receives through the
existing wire-reply mapping.

#### Scenario: No partial commit on conflict

- GIVEN a persisted revision that no longer matches `ExpectedRevision`
- WHEN the conditional write rejects the command
- THEN the actor's in-memory state and version are unchanged and no state is
  published

#### Scenario: Conflict is classified as Rejected, not Failed

- GIVEN a conflicting write
- WHEN the caller inspects the resulting `command.Result`
- THEN `Outcome()` reports `OutcomeRejected` and `Failure.Code()` reports
  `("concurrency_conflict", true)`

### Requirement: Revision Model Mapping for DurableStateActor/StateStore

For the current `DurableStateActor` + testkit `StateStore` adapter, the three
distinct revision concepts (proposal.md C8) map as: `ExpectedRevision` is the
caller's declared precondition from command metadata; `CurrentRevision` is
`entity.currentVersion`, the actor's own last-confirmed committed version
(may be stale relative to storage); `StorageRevision` is the value the
`StateStore` implementation itself tracks per persistence ID and enforces
the conditional write against. The commit decision MUST be made against
`StorageRevision`, never against `CurrentRevision` alone.

#### Scenario: Actor-local revision never substitutes for storage revision

- GIVEN an actor instance whose `currentVersion` has fallen behind the
  store's actual `StorageRevision` (e.g. after another process committed)
- WHEN a command declaring `ExpectedRevision` equal to the actor's stale
  `currentVersion` is processed
- THEN the conditional write is evaluated against `StorageRevision` and
  rejects the command

### Requirement: End-to-End Propagation Proof (T7)

The capability's own correctness demonstration: `ExpectedRevision`,
propagated from command metadata through `DurableStateActor` to the
conditional write, MUST be shown to change the real persisted commit
outcome — not merely to be accepted and ignored.

#### Scenario: ExpectedRevision changes the real commit outcome

- GIVEN two commands against the same persistence ID, identical in every
  field except `ExpectedRevision`: one matches the persisted revision, one
  does not
- WHEN both are processed against the same real `StateStore` (not a stub
  that ignores the precondition)
- THEN the matching command commits and advances the persisted revision, and
  the non-matching command is rejected with `concurrency_conflict` and
  leaves the persisted revision unchanged

## Traceability

| Mandate item | Requirement |
|---|---|
| AC7 (DurableState end-to-end propagation affects the real commit) | ExpectedRevision Is Read as a Write Precondition, Not Domain Input; Conditional Write Delegates the Compare-and-Commit to Persistence, Including Genesis; Conflict Surfaces as `OutcomeRejected` / `concurrency_conflict` |
| T7 (DurableState end-to-end propagation, integration proof) | End-to-End Propagation Proof (T7) |
| Explicit mandate instruction: keep `checkPreconditions`/`priorVersion` conceptually separate | `checkPreconditions`/`priorVersion` Stays a Separate, Unmodified Concern |
| C8 (revision model documented exactly now, not deferred) | Revision Model Mapping for DurableStateActor/StateStore |
