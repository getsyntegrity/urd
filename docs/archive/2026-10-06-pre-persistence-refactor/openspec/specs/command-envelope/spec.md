# Command Envelope Specification

## Purpose

Canonical, runtime-independent Go contract for command/result envelopes and
their metadata (operation identity, correlation, causation, tenant slot,
principal slot, governed custom metadata, temporal fields, six-kind outcome
taxonomy) — the shared vocabulary WRITE-005 and #54 extend instead of
duplicating (proposal.md §Problem). Contract-first only: `Engine.SendCommand`,
its 69 in-repo call sites, the dispatch path, and `SagaActor` are untouched
(W1); this spec does not describe their behavior. Decisions W1–W7 are closed and are not
restated here — see `proposal.md`.

## Requirements

### Requirement: Canonical Command Envelope (AC1, AC9, AC14)

The system MUST define a command envelope type pairing a caller-supplied
command payload with canonical metadata (operation identity, correlation,
causation, tenant slot, principal slot, custom metadata, timestamp,
deadline). The envelope package MUST compile with no import of GoAkt,
`engine.go`, or any transport/auth library. The envelope MUST be constructed
by the calling application, never synthesized internally by Ego from
metadata it did not receive.

#### Scenario: Envelope package has no infrastructure imports

- GIVEN the envelope package's import graph
- WHEN it is inspected
- THEN it contains no goakt, `engine.go`, or transport/auth import

#### Scenario: Consumer constructs the envelope

- GIVEN an application invoking Ego
- WHEN it issues a command
- THEN it supplies a fully-formed envelope; Ego does not default or infer
  metadata the caller did not provide

### Requirement: Canonical Result Envelope and Outcome Taxonomy (AC8)

The system MUST define a result/outcome envelope representing exactly six
mutually exclusive kinds: success with payload, success without payload,
domain rejection, application/runtime failure, deadline/timeout, and
cancellation. Each kind MUST be identifiable at the type level, not by
string inspection. `protos/` and `egopb.ErrorReply` remain unmodified (W5).

#### Scenario: Each outcome kind is representable and identifiable

- GIVEN each of the six outcome kinds
- WHEN a result envelope is constructed for it
- THEN its kind is identifiable without inspecting an error string

#### Scenario: Outcome kinds are mutually exclusive

- GIVEN a constructed result envelope
- WHEN its kind is queried
- THEN exactly one of the six kinds is reported

### Requirement: Operation Identity Semantics (AC2)

Every command envelope MUST carry an `operation_id` (this operation
instance), a `correlation_id` (the logical flow it belongs to, persists
across the flow), and a `causation_id` (the operation that directly caused
it, absent for a root operation). These MUST be named and documented as
distinct from `tenancy.Administrative.CorrelationID()`, an unrelated
administrative-attribution concept, to prevent conflation.

#### Scenario: Root operation carries no causation

- GIVEN a root operation with no parent
- WHEN its envelope is constructed
- THEN `causation_id` is absent while `operation_id` and `correlation_id`
  are present

#### Scenario: New correlation concept is documented as distinct

- GIVEN the envelope's `correlation_id` and `tenancy.Administrative.CorrelationID()`
- WHEN both appear in code or docs
- THEN each states its own distinct purpose; neither is presented as the other

### Requirement: Operation Identity Is Not an Idempotency Key (AC3, W4)

`operation_id` MUST NOT double as, or be required to double as, an
idempotency key. Nothing in this contract forces WRITE-005's future
`op_key` to reuse `operation_id`.

#### Scenario: Idempotency remains separable

- GIVEN a command envelope with an `operation_id`
- WHEN idempotency handling is considered
- THEN no part of this contract requires or implies reusing `operation_id`
  as an idempotency key

### Requirement: Child-Operation Derivation Rule (AC2, AC10)

The system MUST provide a derivation rule producing a child operation's
identity from a parent's: `correlation_id` is inherited unchanged, a new
`operation_id` is generated, and `causation_id` is set to the parent's
`operation_id`. This rule MUST be exercised by an automated test.

#### Scenario: Derivation preserves correlation and chains causation

- GIVEN a parent operation's identity
- WHEN a child operation is derived from it
- THEN the child's `correlation_id` equals the parent's, its `operation_id`
  is new, and its `causation_id` equals the parent's `operation_id`

### Requirement: Tenant Metadata Slot Composes `tenancy/` (AC4, W3)

Command metadata MUST expose a tenant slot typed as `tenancy.TenantContext`
(or an equivalent type from `tenancy/`). The system MUST NOT define a new
or duplicate tenant-identity or tenant-metadata type; `tenancy/`'s own
validation rules remain authoritative.

#### Scenario: Tenant slot holds tenancy/'s own type

- GIVEN a command envelope carrying tenant context
- WHEN the tenant slot's type is inspected
- THEN it is `tenancy.TenantContext`, not a locally defined equivalent

#### Scenario: No duplicate tenant type is introduced

- GIVEN the new envelope package's declared types
- WHEN inspected
- THEN none duplicates `TenantID`, `TenantContext`, or `tenancy.Metadata`

### Requirement: Principal Metadata Slot (AC5)

Command metadata MUST expose an abstract principal/security-identity slot
(who is issuing the command) that carries no concrete auth mechanism,
credential, token, or wire protocol detail.

#### Scenario: Principal slot stays abstract

- GIVEN the principal slot's declared type
- WHEN inspected
- THEN it carries only an opaque identity reference — no credential, token,
  or protocol-specific type

### Requirement: Governed Custom Metadata (AC6, W6)

Command metadata MUST expose a custom-metadata mechanism, not an ungoverned
`map[string]any`, that: reserves a namespaced key prefix for canonical
fields; rejects an attempt to overwrite a canonical field via custom
metadata; and rejects use of a reserved key as a custom key.

#### Scenario: Reserved key is rejected

- GIVEN an attempt to set custom metadata under a reserved key
- WHEN the envelope validates it
- THEN the attempt is rejected and the canonical field is unaffected

#### Scenario: Non-reserved application key is accepted

- GIVEN a non-reserved key with a well-typed value
- WHEN set as custom metadata
- THEN it is accepted and retrievable unchanged

### Requirement: Temporal Fields — Timestamp and Deadline Semantics (AC7)

Command metadata MUST carry a creation timestamp and an optional deadline,
with defined semantics for recognizing an already-elapsed deadline.
Deadline *enforcement* is out of scope (follow-up runtime integration).

#### Scenario: Elapsed deadline is recognized

- GIVEN an envelope whose deadline is earlier than its timestamp
- WHEN the deadline is evaluated against that semantics
- THEN it is recognized as already expired, independent of any enforcement

### Requirement: Contract Test Coverage (AC13)

The envelope/metadata package's test suite MUST cover: a root operation, a
derived child operation, tenant slot present/absent, the principal slot,
custom metadata (accepted and rejected keys), an elapsed deadline, and each
of the six outcome kinds.

#### Scenario: Coverage matrix is exercised

- GIVEN the contract test suite
- WHEN it runs
- THEN each listed case has at least one passing assertion exercising it
  directly, not only as a side effect of an unrelated test

### Requirement: Single Canonical Envelope (AC11)

This contract MUST be the sole command/result envelope. A future change
introducing tenant-aware or aggregate-identity propagation (#54) MUST
extend this contract's slots rather than define a parallel envelope type.

#### Scenario: No duplicate envelope type exists

- GIVEN the codebase after this change
- WHEN command/result envelope types are inspected
- THEN exactly one of each exists

### Requirement: Written Migration and Compatibility Strategy (AC12, W7)

A written migration/compatibility document MUST exist, inventorying every
affected API and dispatch path (at minimum `Engine.SendCommand` and
`SagaActor`), classifying each as breaking or non-breaking, and proposing
an incremental adoption sequence (overloads, adapters, constructors, or
deprecations). It MUST document a sequence only — it MUST NOT include or
require any executed code change, adapter, or shim landed by this change.

#### Scenario: Document inventories known affected surfaces

- GIVEN the migration/compatibility document
- WHEN reviewed
- THEN it names `SendCommand` and `SagaActor` and classifies each surface's
  breaking-change status

#### Scenario: No migration code ships with the document

- GIVEN the same document
- WHEN reviewed against this change's actual diff
- THEN the diff contains no adapter, shim, or caller migration — only the
  envelope/metadata contract and its tests

## Traceability

| #59 AC | Requirement |
|---|---|
| 1 | Canonical Command Envelope |
| 2 | Operation Identity Semantics; Child-Operation Derivation Rule |
| 3 | Operation Identity Is Not an Idempotency Key |
| 4 | Tenant Metadata Slot Composes `tenancy/` |
| 5 | Principal Metadata Slot |
| 6 | Governed Custom Metadata |
| 7 | Temporal Fields — Timestamp and Deadline Semantics |
| 8 | Canonical Result Envelope and Outcome Taxonomy |
| 9 | Canonical Command Envelope (consumer-constructed clause) |
| 10 | Child-Operation Derivation Rule |
| 11 | Single Canonical Envelope |
| 12 | Written Migration and Compatibility Strategy |
| 13 | Contract Test Coverage |
| 14 | Canonical Command Envelope (no-infrastructure-import clause) |
