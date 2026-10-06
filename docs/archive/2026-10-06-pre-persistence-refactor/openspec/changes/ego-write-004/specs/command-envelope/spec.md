# Delta for command-envelope

Extends the promoted `openspec/specs/command-envelope/spec.md` with
`CONTRACT-EXPECTED-REVISION-v1`: an optional, explicit-presence write
precondition on `command.Metadata`, its `ego.cmd.*` carrier key, and the
`concurrency_conflict` Failure code surfaced through the existing
`OutcomeRejected` kind (proposal.md C1–C4, C6, C8; mandate #65). No existing
requirement's normative text changes — every item below is additive. The
promoted spec's "Canonical Result Envelope and Outcome Taxonomy" requirement
(exactly six mutually exclusive kinds) is unchanged and MUST NOT be
reinterpreted by anything in this delta.

## ADDED Requirements

### Requirement: Expected Revision Metadata Field (AC1)

`command.Metadata` MUST expose an optional `ExpectedRevision` field using
the same explicit-presence pattern already used for `CausationID` and
`Deadline` (a boolean presence flag paired with the value, never a sentinel
value standing in for "unspecified"). The field MUST be settable only
through a dedicated `MetadataOption`, consistent with every other optional
field on `Metadata`.

#### Scenario: Expected revision is absent by default

- GIVEN a `Metadata` built without setting an expected revision
- WHEN its expected-revision accessor is queried
- THEN presence reports false and no revision value is defined

#### Scenario: Expected revision is present when set

- GIVEN a `Metadata` built with an expected revision of `N`
- WHEN its expected-revision accessor is queried
- THEN presence reports true and the value equals `N`, including when `N`
  is `0`

### Requirement: Expected Revision Semantics — Absent, Genesis, Exact (AC2)

The system MUST define exactly three states for `ExpectedRevision`, never
collapsed into one another: absent (no precondition — legacy unconditional
write), present with value `0` (genesis — no prior commit may exist for the
target), and present with value `N > 0` (the persisted revision MUST equal
exactly `N`). A value of zero MUST NEVER be produced or interpreted as
meaning "unspecified."

#### Scenario: Genesis is distinguishable from absence

- GIVEN two `Metadata` values, one with expected revision absent and one
  with expected revision present at `0`
- WHEN their precondition semantics are compared
- THEN the two are distinct states, never conflated by any accessor or
  serialization path

#### Scenario: Exact-match precondition carries its value

- GIVEN a `Metadata` with expected revision present at `N > 0`
- WHEN the precondition is read by a consumer
- THEN the consumer observes exactly `N`, not a rounded, clamped, or
  reinterpreted value

### Requirement: Expected Revision Carrier Propagation (AC1)

`Carrier` MUST propagate `ExpectedRevision` losslessly under a new canonical
key in the reserved `ego.cmd.*` namespace, alongside the existing
`operation_id`/`correlation_id`/`causation_id`/`timestamp`/`deadline`/
`principal_id`/`principal_kind` keys. The key MUST be included in a marshal
only when the field is present, and MUST reconstruct the same presence/value
pair on unmarshal — mirroring `causation_id`'s and `deadline`'s
already-optional carrier behavior, not `operation_id`'s always-present one.

#### Scenario: Present expected revision round-trips through Carrier

- GIVEN a `Metadata` with expected revision present at `N`
- WHEN it is marshaled to a `Carrier` and unmarshaled back
- THEN the reconstructed `Metadata` reports expected revision present at
  exactly `N`

#### Scenario: Absent expected revision round-trips as absent

- GIVEN a `Metadata` with no expected revision set
- WHEN it is marshaled to a `Carrier` and unmarshaled back
- THEN the reconstructed `Metadata` reports expected revision absent, and
  the carrier contains no expected-revision key

### Requirement: Concurrency Conflict Failure Code (AC5)

The system MUST define a stable, canonical `Failure` code, exactly
`concurrency_conflict`, surfaced through the existing `OutcomeRejected`
outcome kind via `NewRejected`. This delta MUST NOT introduce a seventh
`Outcome` kind; the promoted six-kind taxonomy is unchanged. A caller MUST
be able to test for this condition programmatically by comparing
`Failure.Code()`'s value, not by inspecting a message string. When the
persisted revision that caused the conflict is reliably known at the point
of failure, it SHOULD be carried on the `Failure`; it MUST NOT be fabricated
from a stale, non-authoritative local counter when it is not reliably known.

#### Scenario: Concurrency conflict is a Rejected outcome with a stable code

- GIVEN a write precondition that was not satisfied at commit
- WHEN the resulting `Result` is constructed
- THEN its `Outcome()` is `OutcomeRejected` and its `Failure.Code()` equals
  exactly `concurrency_conflict`

#### Scenario: Conflict is classifiable without string inspection

- GIVEN a `Result` carrying a `concurrency_conflict` failure
- WHEN a caller checks `Failure.Code() == "concurrency_conflict"`
- THEN the check succeeds independent of the `Failure`'s message text

#### Scenario: No seventh outcome kind is introduced

- GIVEN the codebase after this delta
- WHEN the `Outcome` type's constants are inspected
- THEN exactly six kinds exist, identical to the promoted spec's taxonomy

### Requirement: Legacy Caller Compatibility (AC8)

A command whose `Metadata` never sets `ExpectedRevision` MUST behave exactly
as it did before this delta: an unconditional write, with no precondition
evaluated and no `concurrency_conflict` failure ever produced for it. This
capability MUST NOT provide any code path, default, or carrier
reconstruction rule that turns an absent expected revision into a `0`
(genesis) precondition at any point between `Metadata`, `Carrier`, and
`Envelope`.

#### Scenario: Absent expected revision never becomes genesis

- GIVEN a command built and carried with no `ExpectedRevision` set
- WHEN it passes through `Metadata`, `MarshalMetadata`/`UnmarshalMetadata`,
  and `Envelope` construction
- THEN expected revision reports absent at every stage; it is never
  observed as present with value `0`

#### Scenario: Pre-existing caller is unaffected

- GIVEN a caller written before this delta, which never references
  `ExpectedRevision`
- WHEN it constructs and dispatches a command exactly as before
- THEN its resulting envelope and metadata are unchanged in every other
  respect, and no `concurrency_conflict` outcome can occur for that command

## Traceability

Scoped to AC1, AC2, AC5, AC8 only — #65's other ACs are owned by
`persistence-concurrency`, `event-sourced-concurrency`, and
`durable-state-concurrency` (see proposal.md's AC placement table).

| #65 AC | Requirement |
|---|---|
| AC1 | Expected Revision Metadata Field; Expected Revision Carrier Propagation |
| AC2 | Expected Revision Semantics — Absent, Genesis, Exact |
| AC5 | Concurrency Conflict Failure Code |
| AC8 | Legacy Caller Compatibility |
