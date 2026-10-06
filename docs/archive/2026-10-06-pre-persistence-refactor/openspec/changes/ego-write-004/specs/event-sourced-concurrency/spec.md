# Event-Sourced Concurrency Specification

## Purpose

Optimistic-concurrency propagation for `EventSourcedActor`/`eventsWriterActor`
(`event_sourced_actor.go`, `events_writer_actor.go`): a command's
`ExpectedRevision` must actually gate whether its events commit, not merely
be observed in isolation (AC6, mandate T6). This capability owns no new
contract; it CONSUMES `CONTRACT-EXPECTED-REVISION-v1` (produced by
`command-envelope`: the `ExpectedRevision` type, presence semantics, and the
`concurrency_conflict` `Failure` code carried via `OutcomeRejected`) and
`CONTRACT-CONDITIONAL-WRITE-v1` (produced by `persistence-concurrency`: the
atomic conditional-write operation on `EventsStore`). It is `independent` of
`durable-state-concurrency` — neither depends on the other's actor
integration. No domain handler signature changes (`EventSourcedBehavior`,
`EventSourcedEnvelopeBehavior` are untouched); `ExpectedRevision` is a
runtime-level write precondition, never domain data.

Batched-write precondition *merge* policy (multiple stashed commands folded
into one flush) is a `design.md` decision, not specified here — this spec
requires only that whatever precondition applies to a dispatched write
reaches the store unweakened, not the merge algorithm.

## Requirements

### Requirement: ExpectedRevision Extracted for the Persist Request Only (AC6)

The system MUST extract a command's `ExpectedRevision`
(`CONTRACT-EXPECTED-REVISION-v1`) after `dispatchToBehavior` returns, for use
only in constructing the persist request. The system MUST NOT pass
`ExpectedRevision` into `HandleCommand`, `HandleEvent`, or `HandleEnvelope`.

#### Scenario: ExpectedRevision never reaches the domain handler

- GIVEN a command carrying `ExpectedRevision=N`
- WHEN `EventSourcedActor` dispatches it to the behavior
- THEN the handler's received arguments contain no `ExpectedRevision` value,
  observable neither in `HandleCommand`'s nor `HandleEnvelope`'s signature

#### Scenario: Absent ExpectedRevision extracts as unconditional

- GIVEN a legacy command with no `ExpectedRevision` metadata
- WHEN `EventSourcedActor` extracts the precondition for persistence
- THEN it resolves to the contract's unconditional precondition, matching
  `CONTRACT-EXPECTED-REVISION-v1`'s absence semantics exactly

### Requirement: Persist Request Carries the Precondition (AC6)

Every persist request `EventSourcedActor` sends to `eventsWriterActor`
(direct path via `persistAsync`, batched path via `flushBatch`) MUST include
the precondition resolved from the originating command(s). The system MUST
NOT drop the precondition when converting `persistEventsRequest` for
dispatch.

#### Scenario: Direct-path persist request carries the precondition

- GIVEN a single command with `ExpectedRevision=N` dispatched outside batch
  mode
- WHEN `persistAsync` builds the request delivered to `eventsWriterActor`
- THEN the request's precondition equals `N`, not the contract's
  unconditional value

#### Scenario: Batched persist request carries a resolved precondition

- GIVEN a batch flush triggered by `flushBatch`
- WHEN the resulting persist request reaches `eventsWriterActor`
- THEN it carries an explicit precondition consistent with
  `CONTRACT-CONDITIONAL-WRITE-v1` — never an implicit unconditional write
  substituted for a caller-declared one

### Requirement: eventsWriterActor Delegates to the Conditional Write (AC6)

`eventsWriterActor.handlePersistEvents` MUST invoke `EventsStore`'s atomic
conditional-write operation (`CONTRACT-CONDITIONAL-WRITE-v1`) with the
request's precondition, instead of an unconditional write, whenever a
precondition other than "unconditional" is present.

#### Scenario: Conditional precondition reaches the store call

- GIVEN a persist request whose precondition is `ExpectedRevision=N`
- WHEN `eventsWriterActor` processes it
- THEN the store call it issues carries precondition `N`, verifiable by
  inspecting the call, not inferred from a successful reply alone

### Requirement: Store Result Is the Sole Commit-Success Authority (AC6)

`EventSourcedActor` MUST NOT confirm a command's success, call
`applyConfirmedState`, or advance `eventsCounter`/`batchCounter` until the
conditional-write result reports success. Internal counters MUST NOT be
treated as evidence a write committed.

#### Scenario: Conflict prevents state and counter advancement

- GIVEN a conditional write that reports a conflict
- WHEN `EventSourcedActor` processes the persist response
- THEN `currentState` and `eventsCounter` (or `batchState`/`batchCounter` in
  batch mode) remain at their pre-write values

#### Scenario: Success confirmation originates from the store result

- GIVEN a conditional write that reports success
- WHEN `EventSourcedActor` processes the persist response
- THEN the confirmed state and counter come from the store-confirmed write,
  not from a local increment computed before the store replied

### Requirement: Conflict Surfaces as OutcomeRejected/concurrency_conflict (AC6)

A conditional-write conflict reported to `EventSourcedActor` MUST propagate
to the command's caller as `OutcomeRejected` carrying a `Failure` whose code
equals `concurrency_conflict` (`CONTRACT-EXPECTED-REVISION-v1`) — never as a
generic error reply and never as a silent success.

#### Scenario: Caller receives the canonical conflict code

- GIVEN a command rejected by a conditional-write conflict
- WHEN the caller inspects the reply
- THEN `Outcome() == OutcomeRejected` and `Failure.Code == "concurrency_conflict"`,
  checkable programmatically without string matching

#### Scenario: Conflict is distinguishable from an unrelated persistence error

- GIVEN a conditional-write conflict and, separately, an unrelated store
  I/O failure
- WHEN each is propagated
- THEN only the conflict carries `concurrency_conflict`; the I/O failure
  surfaces through its own distinct failure code

### Requirement: End-to-End Propagation Proof (AC6, T6)

An automated integration test MUST exercise the real `EventSourcedActor`
and `eventsWriterActor` against a conditional-write-capable `EventsStore`
(not a mock that only asserts call arguments) and demonstrate that
`ExpectedRevision` changes the store's actual commit outcome, satisfying the
consumer-ownership rule that `event-sourced-concurrency` proves its own
correct consumption of both contracts.

#### Scenario: Exact-revision command commits end-to-end

- GIVEN an aggregate persisted at revision `N`
- WHEN a command declaring `ExpectedRevision=N` is sent through
  `EventSourcedActor`
- THEN the store's persisted revision advances and the reply reports success

#### Scenario: Stale-revision command is rejected end-to-end

- GIVEN an aggregate persisted at revision `N`
- WHEN a command declaring `ExpectedRevision=N-1` is sent through
  `EventSourcedActor`
- THEN the store's persisted revision does not change and the reply is
  `OutcomeRejected` with `Failure.Code == "concurrency_conflict"`

#### Scenario: Concurrent genesis commands yield exactly one success

- GIVEN two independent commands, both declaring `ExpectedRevision=0`,
  dispatched concurrently against a nonexistent aggregate
- WHEN both reach `EventSourcedActor`/`eventsWriterActor`
- THEN exactly one produces a committed genesis event and the other
  receives `concurrency_conflict`

## Traceability

| Mandate AC/Test | Requirement |
|---|---|
| AC6 | ExpectedRevision Extracted for the Persist Request Only |
| AC6 | Persist Request Carries the Precondition |
| AC6 | eventsWriterActor Delegates to the Conditional Write |
| AC6 | Store Result Is the Sole Commit-Success Authority |
| AC6 | Conflict Surfaces as OutcomeRejected/concurrency_conflict |
| AC6, T6 | End-to-End Propagation Proof |
