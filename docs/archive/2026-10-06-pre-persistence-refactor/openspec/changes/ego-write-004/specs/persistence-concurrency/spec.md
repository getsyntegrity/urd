# Persistence Concurrency Specification

## Purpose

Redesign `persistence.EventsStore.WriteEvents` and `persistence.StateStore.WriteState`
so the compare-and-commit that enforces `ExpectedRevision` happens atomically
inside persistence — never as an in-actor check or a side effect of mailbox
serialization. Produces `CONTRACT-CONDITIONAL-WRITE-v1` (the precondition
type + atomic conditional-write semantics for both SPIs), consumed by
`event-sourced-concurrency` and `durable-state-concurrency`. Consumes
`CONTRACT-EXPECTED-REVISION-v1` from `command-envelope` (the `ExpectedRevision`
metadata contract and its presence semantics); this spec does not redefine
that contract, only the storage-layer precondition it maps onto.

Current signatures this spec breaks (verified in code, not the mandate's
illustrative example):
- `EventsStore.WriteEvents(ctx context.Context, events []*egopb.Event) error`
  (`persistence/events_store.go:40`)
- `StateStore.WriteState(ctx context.Context, state *egopb.DurableState) error`
  (`persistence/state_store.go:40`)

Both gain a required precondition parameter — one SPI, no parallel optional
method. Non-normative migration narrative lives in `design.md`'s
Migration/Rollout section (mirroring WRITE-003's M-1..M-4 pattern); this spec
only requires that narrative to exist (AC12).

## Requirements

### Requirement: Explicit Write Precondition Type (AC3, AC4)

The system MUST define a single precondition type in package `persistence`
representing exactly three states: unconditional, genesis (no prior commit
may exist), and exact-revision(N). The type MUST be constructed only through
named constructors (e.g. an `Unconditional()`/`ExpectGenesis()`/
`ExpectRevision(revision uint64)` family); no bare `uint64` or sentinel
value (`0`, `-1`) may represent "unconditional" or "genesis". This type MUST
be shared, unmodified in meaning, by both `EventsStore` and `StateStore`.

#### Scenario: Genesis and exact-revision-zero are not conflated

- GIVEN the precondition type's three states
- WHEN a genesis precondition and an exact-revision(0) precondition are each
  constructed
- THEN they are distinguishable values, never represented by the same
  underlying sentinel

#### Scenario: No numeric sentinel represents unconditional

- GIVEN the precondition type's declared constructors
- WHEN the type is inspected
- THEN "unconditional" is reachable only via its named constructor, never by
  passing a magic number through the exact-revision constructor

### Requirement: Atomic Conditional Write — EventsStore (AC3)

`EventsStore.WriteEvents` MUST accept a precondition parameter and evaluate
it against the persisted revision for the target `persistenceID`, then
commit the batch, as one atomic operation with no observable window in
which another writer's commit can interleave between the check and the
commit.

#### Scenario: Matching exact-revision precondition commits

- GIVEN a persistenceID persisted through sequence number N
- WHEN `WriteEvents` is called with an exact-revision(N) precondition
- THEN the batch commits and the persisted revision advances accordingly

#### Scenario: Stale exact-revision precondition is rejected atomically

- GIVEN a persistenceID persisted through sequence number N
- WHEN `WriteEvents` is called with an exact-revision(N-1) precondition
- THEN no event in the batch is committed and a concurrency-conflict error is
  returned identifiable by type or `errors.As`, not string matching

### Requirement: Atomic Conditional Write — StateStore (AC4)

`StateStore.WriteState` MUST accept the same precondition type and evaluate
it against the persisted `VersionNumber` for the target `persistenceID`,
then commit the new state, as one atomic operation with the same no-interleave
guarantee as `WriteEvents`.

#### Scenario: Matching exact-revision precondition commits

- GIVEN a persistenceID persisted at version N
- WHEN `WriteState` is called with an exact-revision(N) precondition
- THEN the new state commits and becomes the persisted `VersionNumber`

#### Scenario: Stale exact-revision precondition is rejected atomically

- GIVEN a persistenceID persisted at version N
- WHEN `WriteState` is called with an exact-revision(N-1) precondition
- THEN the state is not committed and a concurrency-conflict error is
  returned identifiable by type, not string matching

### Requirement: `checkPreconditions` Keeps Its Distinct, Narrower Responsibility

`DurableStateActor.checkPreconditions` (`durable_state_actor.go:461`) MUST
remain a behavior-output sanity check — it validates that the domain
handler's returned state type matches the current state type and that
`newVersion` differs from the actor's in-memory `currentVersion` by exactly
one. It MUST NOT be extended, repurposed, or documented as satisfying this
capability's atomic conditional-write guarantee; that guarantee is owned
exclusively by `StateStore.WriteState`'s precondition evaluation.

#### Scenario: Passing checkPreconditions does not imply a storage guarantee

- GIVEN a handler output that satisfies `checkPreconditions` (correct type,
  version step of exactly one)
- WHEN a competing writer has already advanced the persisted `VersionNumber`
  past what this actor observed
- THEN `WriteState`'s precondition still rejects the write with a
  concurrency conflict, independent of `checkPreconditions` having passed

### Requirement: Real Compare-and-Swap in testkit Stores (AC9, AC10)

`testkit/eventstore.go` and `testkit/durablestore.go` MUST implement the
conditional write using an atomic compare-and-swap primitive evaluated
directly against the underlying store on the write path (e.g.
`sync.Map.CompareAndSwap`/`LoadOrStore` at commit time), not a queue,
actor mailbox, external mutex, or any caller-side ordering that serializes
writers before they reach the store. Real concurrent-writer competition
must be possible against these stores without any artificial pre-store
serialization.

#### Scenario: Unserialized goroutines compete directly against the store

- GIVEN two goroutines calling `WriteEvents` (or `WriteState`) concurrently
  against the same testkit store instance, with no shared lock between the
  goroutines
- WHEN both calls race
- THEN the store's own compare-and-swap — not caller ordering — determines
  which one commits

### Requirement: Two Independent EventStore Writers, Same Expected Revision (AC9, T8, T11 — architectural gate)

Two independent writers issuing `WriteEvents` against the same
persistenceID with the same exact-revision(N) precondition, started
concurrently with no shared serialization, MUST yield exactly one success
and exactly one concurrency-conflict error. Because both writers call
`WriteEvents` directly — no actor, no mailbox between them — this scenario
is also the T11 proof that the guarantee is enforced by persistence itself,
not by mailbox/actor serialization; nothing later in the actor-integration
PRs (PR3/PR4) needs to re-demonstrate it.

#### Scenario: Concurrent same-revision writers split exactly 1/1

- GIVEN a persistenceID persisted through sequence number N
- WHEN two independent writers concurrently call `WriteEvents` each with an
  exact-revision(N) precondition
- THEN exactly one call commits and exactly one returns a concurrency
  conflict — never two commits, never two conflicts

### Requirement: Two Independent StateStore Writers, Same Expected Revision (AC10, T9, T11 — architectural gate)

Two independent writers issuing `WriteState` against the same
persistenceID with the same exact-revision(N) precondition, started
concurrently with no shared serialization, MUST yield exactly one success
and exactly one concurrency-conflict error. As with the `EventsStore` half
above, both writers call `WriteState` directly with no actor/mailbox in the
path — this is the T11 proof for `StateStore`.

#### Scenario: Concurrent same-revision writers split exactly 1/1

- GIVEN a persistenceID persisted at version N
- WHEN two independent writers concurrently call `WriteState` each with an
  exact-revision(N) precondition
- THEN exactly one call commits and exactly one returns a concurrency
  conflict — never two commits, never two conflicts

### Requirement: Concurrent Genesis Writers (AC11, T10 — architectural gate)

Two independent writers issuing a genesis precondition against a
persistenceID with no prior commit, started concurrently, MUST yield
exactly one commit and exactly one concurrency-conflict error, for both
`EventsStore` and `StateStore`.

#### Scenario: Concurrent genesis writers never both succeed

- GIVEN a persistenceID with no persisted event or state
- WHEN two independent writers concurrently call `WriteEvents` (or
  `WriteState`) each with a genesis precondition
- THEN exactly one call commits and exactly one returns a concurrency
  conflict — never two initial commits

### Requirement: Revision Model Mapping for Current Adapters (AC3, AC4)

The distinct `ExpectedRevision` (caller precondition, `command-envelope`),
`CurrentRevision` (Ego-observed), and `StorageRevision` (adapter's own CAS
value) concepts MUST be documented for the current testkit adapters, not
deferred:
- `EventsStore`: `StorageRevision` is `egopb.Event.SequenceNumber`, keyed
  per persistenceID in the store. `CurrentRevision` is the owning actor's
  in-memory counter (`eventsCounter`/`currentVersion`), populated from
  `StorageRevision` at recovery and advanced only after `WriteEvents`
  confirms commit.
- `StateStore`: `StorageRevision` is `egopb.DurableState.VersionNumber`.
  `CurrentRevision` is the `DurableStateActor`'s in-memory `currentVersion`,
  advanced only after `WriteState` confirms commit.
- `ExpectedRevision` is never read from `CurrentRevision` directly; the
  precondition evaluation compares the caller's `ExpectedRevision` against
  `StorageRevision` at the atomic instant of commit, never against a
  locally cached `CurrentRevision` that may be stale.

#### Scenario: Conflict is never invented from a stale local counter

- GIVEN an actor whose in-memory `CurrentRevision` has not yet observed a
  competing writer's commit
- WHEN that actor's write is rejected
- THEN the rejection's expected/actual revisions (when reliably known) are
  read from `StorageRevision` at commit time, never fabricated from the
  actor's stale `CurrentRevision`

### Requirement: Precondition Shape Does Not Block Future Atomic Multi-Event Append

The precondition type MUST apply once per `WriteEvents` call — one
precondition per batch, not one per event — so that a future change adding
all-or-nothing multi-event append semantics (out of scope here) can layer
on top of this precondition type without redefining its shape.

#### Scenario: Batch precondition is independent of per-event atomicity

- GIVEN a `WriteEvents` call carrying multiple events and one exact-revision
  precondition
- WHEN the precondition is evaluated
- THEN it governs the whole batch as a single check, leaving whether
  individual events within a committed batch are all-or-nothing to a later,
  separate change

### Requirement: Breaking-Change Declaration and Migration Path (AC12)

Both SPI changes MUST be declared as breaking changes for external
implementers of `EventsStore`/`StateStore`, and `design.md` MUST carry a
Migration/Rollout section naming every affected method, its old and new
signature, and an incremental adoption path — mirroring the M-1..M-4 table
pattern used by WRITE-003's design.md. This spec requires that section to
exist and cover both SPIs; the narrative itself is design.md's content, not
this spec's.

#### Scenario: External implementer fails at compile time, not silently at runtime

- GIVEN an external `EventsStore` or `StateStore` implementation written
  against the pre-WRITE-004 signature
- WHEN it is compiled against the new interface
- THEN compilation fails (missing precondition parameter), rather than
  compiling successfully and silently ignoring the precondition at runtime

## Traceability

| #65 AC | Requirement |
|---|---|
| AC3 | Atomic Conditional Write — EventsStore; Explicit Write Precondition Type; Revision Model Mapping for Current Adapters |
| AC4 | Atomic Conditional Write — StateStore; Explicit Write Precondition Type; Revision Model Mapping for Current Adapters |
| AC9 | Real Compare-and-Swap in testkit Stores; Two Independent EventStore Writers, Same Expected Revision (T8, T11) |
| AC10 | Real Compare-and-Swap in testkit Stores; Two Independent StateStore Writers, Same Expected Revision (T9, T11) |
| AC11 | Concurrent Genesis Writers (T10) |
| AC12 | Breaking-Change Declaration and Migration Path |
