# Slice Reader and Offset Identity Specification

Status: DRAFT. Not approved. This specification needs a human gate before it is normative: it defines a public persistence contract (reader and cursor) and a data contract (offset identity), both gate categories in the spec governance rules. It is written for #351 (I-05). Nothing here is implemented.

## Purpose

Specify the read that replaces `GetShardEvents(scope, shard, offset int64, limit)`: a read by slice, a stable-prefix cursor, and the identity of the offsets and applied marks that sit on top of it. It depends on three things that are not decided and that this document does not decide:

- The position type of the reader, chosen by Gate A (#352, #387). This specification states what ANY position MUST satisfy and defines no concrete one.
- The additive schema that stores the slice and the layout epoch (#358).
- The cutover procedure (#359, `docs/decisions/slice-cutover-359.md`).

Ratified inputs (P1, recorded in #350 and #351): `slice = FNV-1a64(key) mod 1024` with the key encoding in `docs/decisions/logical-slices.md`, computed by `persistence.sliceOf` from `(scope, persistenceID)`. This specification preserves that input and the persisted keys (`''` for Unscoped, the tenant id otherwise).

## Facts about the current reader (baseline, verified in the code)

- `GetShardEvents(ctx, scope, shardNumber, offset, limit)` returns events strictly after `offset`, in the total order `(timestamp, persistence id, sequence number)`, with the timestamp of the last event as the whole cursor, and extends a batch to the end of a group sharing the last timestamp. Delivery is at-least-once.
- The cursor is a timestamp, not a commit position: an event that commits late with a timestamp at or below a committed offset is never returned. `persistence/events_store.go` records this gap as pre-existing and out of scope of that contract.
- `ShardOffsets(ctx, scope)` maps each shard that holds events of the scope to the maximum timestamp.
- Offsets are stored per `(scope, projection name, shard)` (`ScopedOffsetStore`, migration 006). An invalid scope is rejected; an adapter without scoped offsets fails closed for tenant scopes.

## Definitions

- **Selection**: `OneScope(scope)` or `AllScopesInCell`. `OneScope(Unscoped())` is a valid single-tenant read. `Unscoped()` is never a wildcard. `AllScopesInCell` is privileged, distinct, and is not enabled by omitting tenancy.
- **Slice range**: a half-open interval `[lo, hi)` within `[0, 1024)`.
- **Position**: an opaque, per-slice, totally ordered value defined by the adapter's mechanism. Comparable only within the same format, cell and slice.
- **Event identity**: `(scope, persistenceID, sequenceNumber)`. It does not depend on any position.

## Requirements

### Requirement: Read by slice with an explicit selection

The reader MUST take a selection, a slice range, a cursor (empty to start) and a page size. It MUST return events of that selection whose STORED slice is inside the range, in position order per slice, plus a next cursor. The reader MUST filter on the persisted slice value and MUST NOT recompute the slice from the event at read time. The slice stored with an event is the one computed by the ratified function from `(scope, persistenceID)` at write time.

A read with the zero-value scope returns `ErrInvalidScope` and reads nothing. A read in one scope MUST NOT return an event of another scope. `AllScopesInCell` MUST require an explicit privileged capability and MUST NOT be reachable through `Unscoped()`.

#### Scenario: Slice range filters on the stored slice

- GIVEN events of one scope stored with slices 3, 700 and 1023
- WHEN the range is `[0, 512)`
- THEN only the event with slice 3 is returned

#### Scenario: Unscoped is not a wildcard

- GIVEN events of `Unscoped()` and of tenant `acme`
- WHEN `OneScope(Unscoped())` is read
- THEN no event of `acme` is returned, and no selection reads every tenant unless it is `AllScopesInCell` with its capability

### Requirement: Order

Within one slice, for one selection, events MUST be delivered in position order, and the events of one persistence id MUST be delivered in ascending sequence number. No order is promised between different slices or between different scopes of an `AllScopesInCell` read. A consumer MUST NOT infer ordering across slices from cursor contents.

#### Scenario: Per-entity order inside a slice

- GIVEN two events of the same persistence id with sequence numbers 4 and 5 in one slice
- WHEN the slice is read in pages of any size
- THEN event 4 is delivered before event 5, in the same or an earlier page

### Requirement: Stable prefix

After a next cursor `c` is returned for a selection and slice range, no event whose position is at or before `c` MAY become visible later for that same selection and range. The reader MUST NOT deliver an event that is not yet eligible, even if it has committed. The mechanism that makes this true, and its operational conditions (dedicated cluster, bounded transaction timeout for every writer role, no prepared transactions, alert on an old transaction id), are decided by Gate A and are recorded in #351 when it is. What happens when those conditions are broken MUST be declared by the adapter's capabilities, and a reader whose conditions cannot be verified MUST fail closed.

#### Scenario: A late commit does not appear behind the cursor

- GIVEN a transaction that stamped an earlier time and has not committed, and a later event that committed
- WHEN the reader returns a cursor past the later event
- THEN the earlier transaction's event is not eligible before that cursor is reached, or the cursor does not pass the later event

(This is the safety case of #348. The existing PostgreSQL adapter fails it today, which is a known failure, not a property of this contract.)

### Requirement: Pagination and progress

The page size is a target. A page MAY be shorter than the target, and MAY be empty with an advanced cursor, so that progress is observable while the tail is ineligible. A position that can tie MUST define a deterministic rule that never splits a tie across pages, as the current reader does for equal timestamps. A cursor MUST be committed by the consumer only after it has handled the events it covers; delivery is at-least-once. A crash between delivery and commit redelivers the batch.

#### Scenario: Resuming after a crash

- GIVEN a consumer that handled a page and crashed before committing its cursor
- WHEN it restarts from the last committed cursor
- THEN it receives the page again, the same events in the same order, and handles them again by event identity

### Requirement: Cursor format and rejection

The cursor MUST be opaque to callers, versioned, integrity-protected against accidental corruption, and MAY have several components (for example one per slice or partition). Its header MUST bind: format version, cell, selection fingerprint, slice-range fingerprint and layout epoch. The reader MUST reject a cursor with `ErrCursorMismatch` (or its final approved equivalent) when:

- the format version is unknown;
- the cell differs;
- the selection fingerprint differs (a cursor of `OneScope(a)` MUST NOT be used for `OneScope(b)` or for `AllScopesInCell`);
- the slice range is not compatible with the cursor's range;
- the layout epoch differs from the active one;
- the cursor fails its integrity check;
- the value is a legacy timestamp offset or a legacy shard cursor presented as a slice cursor.

A rejected cursor MUST NOT be silently reset to the start, and MUST NOT advance any state.

#### Scenario: A legacy offset is not a slice cursor

- GIVEN a projection offset created by the shard reader
- WHEN it is presented to the slice reader
- THEN the reader returns `ErrCursorMismatch` and reads nothing

### Requirement: Offset identity

An offset MUST be identified by the tuple (mode, scope or cell, processor, version, slice), stored in columns of the destination backend, not in an actor name (#362, R-01). In `PerScope` mode the scope component is the persisted scope key, unchanged (`''` for Unscoped, the tenant id otherwise). In `SharedCell` mode the component is the cell. A new version starts with new cursors and new applied marks. `ResetOffset` MUST receive the full identity. Two scopes with the same projection and slice MUST NOT collide. A change of mode MUST invalidate incompatible cursors.

The applied mark of an event for a destination is identified by the offset identity's processor and version plus the event identity `(scope, persistenceID, sequenceNumber)`. It MUST NOT depend on a position, so it survives a change of position mechanism.

The stored offset value is a cursor (or its components) as defined above. Where the destination supports it, the effect, the applied mark and the offset advance commit in one destination transaction that validates the ownership fence (#373, #374).

#### Scenario: Same projection and slice in two tenants

- GIVEN projection `p` in tenants `a` and `b`, both at slice 7
- WHEN each commits its own offset
- THEN reading and resetting one never changes the other

### Requirement: Compatibility with the previous layout

The previous layout is the shard reader, the legacy `shard_number` and the legacy offset rows keyed by `(scope, projection name, shard)`. Until the layout epoch records the new layout as active:

- the slice reader MUST report that the layout is not active and read nothing;
- the shard reader and its offsets MUST keep their current behavior;
- legacy offset rows MUST NOT be reinterpreted as slice offsets, because a legacy shard is not a slice.

After the epoch records the new layout as active, the shard reader MUST refuse to read (it would silently miss or mislabel events whose legacy shard is not meaningful) and legacy offset rows MUST NOT be written. The legacy rows and column are kept until the retirement plan of `GetShardEvents` completes. The mapping from a legacy offset to a slice position is defined by the cutover procedure (#359) and exists only if the position mechanism can provide the function `PositionAtOrBefore(timestamp)` below.

#### Requirement-level condition: legacy offset translation

For a legacy timestamp `T` the mechanism MAY define `PositionAtOrBefore(T)`: a position `P` such that every event with timestamp greater than `T` is after `P`. If the mechanism cannot define it, legacy offsets MUST be invalidated, and a projection MUST rebuild under a new version (never a silent reset).

### Requirement: Capabilities and portability

An adapter MUST declare its capabilities (stable prefix, eligibility bound, scoped offsets, privileged selection). A missing capability MUST fail closed for the selections that need it, as `ForScope` does for tenant scopes today. The offset and the applied mark live in the destination's backend and, when a transaction is used, in the destination's transaction. Adapters MAY group slices into partitions: with a power-of-two partition count `P` that divides 1024, `(hash mod 1024) mod P = hash mod P`, so grouping never splits a slice.

### Requirement: Deprecation of the shard reader

`GetShardEvents` MUST be deprecated with a retirement plan: the version in which it is marked, the condition under which it is refused (new layout active), and the version in which it is removed. The retirement is a #359 step and MUST NOT happen before every projection has moved to a slice cursor or has been rebuilt.

## What this specification does not decide

| Open item | Owner |
| --- | --- |
| Concrete position type (xid8, counter per slice, batched publication) and its tie rule | Gate A (#352, #387) |
| Eligibility bound T and its operational conditions | Gate A (#352, #387) |
| Concrete schema: slice column, index, cursor bytes, applied-marks table, layout epoch | #358 (I-09a) |
| Whether `PositionAtOrBefore` exists for the chosen mechanism | Gate A (#352, #387) |
| Final error names, Go signature of the reader, capability type | the implementation of this contract, after approval |
| Export of `SliceOf` and `SliceCount` | the change that integrates the writer, after the cutover is approved |

## Acceptance traceability (#351)

| #351 criterion | Where it is addressed | Status |
| --- | --- | --- |
| Contract approved in the current spec location | this file, in `openspec/specs/` | Drafted, human gate pending |
| Do not reactivate archived specs | nothing from `docs/archive` is reused as normative | Met |
| Cursor rejection cases | Requirement: Cursor format and rejection | Drafted |
| Conditions of the bound ≤ T and what happens if broken | Requirement: Stable prefix (conditions decided by Gate A) | Open on Gate A |
| Portability rules | Requirement: Capabilities and portability | Drafted |
| Offset and applied-mark identity | Requirement: Offset identity | Drafted |
| `GetShardEvents` deprecated with a retirement plan | Requirements: Compatibility, Deprecation | Drafted, plan owned by #359 |
| `OneScope(Unscoped())` valid single-tenant read | Requirement: Read by slice | Drafted |
