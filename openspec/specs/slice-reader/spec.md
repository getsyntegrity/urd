# Portable reader contract: explicit selection, versioned cursor and stable prefix (#351, I-05)

| Field | Value |
|---|---|
| Status | **Proposed.** Drafted for review; not approved. Approval is a maintainer decision recorded on #351, and it needs a human gate: it defines a public persistence contract and a data contract. Nothing here is delivered code, and no acceptance criterion of #351 is declared met by this document. |
| Consolidates | The single specification of #351. It merges `docs/decisions/reader-contract-351.md` (PR #441) and the first draft of this file (PR #443). Sections 1 to 15 start from the #441 text and were edited during the consolidation: the passages marked **Updated** show the main changes, the references to the transitional reader, the layout epoch and sections 16 to 18 are conditional as the introduction of section 16 states, and the history of PR #443 is the exact record of every edit. Sections 16 to 18 carry what only the #443 draft had, aligned with the cutover procedure proposed in PR #444. |
| Date | 2026-10-07 |
| Tracker | #351 (I-05), epic #342, phase 0, P0 |
| Depends on | #348 (I-02, reader TCK: safety, eligibility, progress; open, implemented in a sibling change under `persistence/conformance`, not part of this PR). The module-topology ADR `docs/decisions/module-topology-347.md` (**Proposed**, #347 open; its presence on `develop` is **not** approval). |
| Feeds | #352 / #387 (Gate A, mechanism), #350 (slices), #359 (offset migration, cutover, retention), #362 / #373 / #374 (checkpoint, applied marks, fencing), #384 (capability enforcement), #424 (single tenant) |
| Baseline | Code inspected: `origin/develop` at `b615449` and re-verified at `fde51c4` (the merge of #436, which adds only the unexported slice function, its tests and decision documents; the files and lines cited in section 3 are unchanged). Statements tagged **Current** refer to that snapshot, with file and line. |
| Scope | Documentation only. No code, schema, `go.mod`, GoAkt, hook or archived-spec change. Nothing under `persistence/conformance` is touched. Location: `openspec/specs/slice-reader/spec.md`, the active specification location (`openspec/README.md`); no archived spec is reactivated. |

How to read this document. **Current** is verified on `b615449`. **Proposed** is what this contract suggests and needs a maintainer decision (section 13). **Unverified** is a claim this draft could not check. Property names in section 7 follow the SAFETY / ELIGIBILITY / PROGRESS split of #348; they are provisional until the #348 change merges, and section 14 must then be reconciled with its real scenario names. Error and type names are descriptive, not a final API.

## 1. Context

The only feed read in the repository is `GetShardEvents(ctx, scope, shard, offset int64, limit)`. Its cursor is a writer-stamped timestamp. Its own documentation admits the gap: an event that becomes visible after its offset was committed, with a timestamp at or below that offset, is never returned (**Current**: `persistence/events_store.go:166-171`). That is gap B1 of the baseline audit and requirement P-02 of the PRD (`docs/prd/urd-platform-prd.md:103`): explicit selection, opaque validated cursor, stable prefix, zero omissions in the oracle, eligibility and progress tested separately, and no timestamp-only reader accepted as a correction.

This document specifies the contract of the reader that replaces it. It says **what** a conforming reader promises and what composition rejects. It does **not** choose the mechanism (xid8, per-slice counter, batched publication): that is Gate A (#352, #387). It does not fix the slice count, hash or key encoding (P1 of the ADR: ratified in #350 and consumed here, see L4). It does not decide the offset cutover (P2, #359): sections 16 and 17 state the reader-side rules that any approved cutover must satisfy, and they are conditional on P2 (see the introduction of section 16).

## 2. Decisions in brief

| # | Proposed decision | Section |
|---|---|---|
| R1 | A read names an explicit **selection**: `OneScope(scope)` or `AllScopesInCell`. `OneScope(Unscoped())` is valid single-tenant. `Unscoped` is never a wildcard. `AllScopesInCell` is privileged and distinct. | 4 |
| R2 | A read covers a **range of logical slices**. Logical slices are independent of physical assignment (nodes, partitions, executors). | 5 |
| R3 | The cursor is **opaque, durable and versioned**, bound to cell, selection and a compatible range. Malformed, unknown-version and incompatible cursors are rejected before any data is returned. | 6 |
| R4 | Reads honor a **stable prefix**: after cursor `c` is returned, nothing at or before `c` becomes visible later. This is a safety property (zero omissions) and holds even when eligibility or progress degrade. | 7 |
| R5 | **Eligibility** (bounded time to visibility) and **progress** are separate, conditional properties with declared operating conditions. | 7 |
| R6 | The **checkpoint lives in the destination backend**, keyed by the full identity; applied marks and checkpoint commit with the effect in the destination transaction when supported. | 8 |
| R7 | Each feed adapter declares mandatory capabilities, proven by the TCK; composition rejects listed combinations before anything operates. | 9 |
| R8 | `GetShardEvents` is deprecated with a staged plan, **without** changing its behavior or removing it in this change. | 10 |
| R9 | Offset migration, cutover and events removed by retention belong to #359. No reconstruction of absent data is promised. | 11 |

## 3. What the code does today (Current)

| Fact | Evidence |
|---|---|
| The read is per shard, takes a scope and an `int64` offset and returns `(events, lastTimestamp, err)`. The cursor is the timestamp of the last returned event. | `persistence/events_store.go:151-178` |
| Order is `(timestamp, persistence_id, sequence_number)`. The batch is at least `limit` and is extended to the end of the group sharing the last timestamp, so a tie group is never cut. | `persistence/events_store.go:151-165`; PostgreSQL `persistence/postgres/event_store.go:548-567`; memory `testkit/eventstore.go:493-509` |
| An invalid scope returns `ErrInvalidScope`; an `Unscoped()` read does not return a tenant's events; "there is no cross-scope read". | `persistence/events_store.go:174-177`; `persistence/postgres/event_store.go:538-541` (`scopeKey`, line 50) ; filter `tenant_id=$1` at line 560 |
| The late-commit gap is acknowledged: the cursor is a timestamp, not a commit position. | `persistence/events_store.go:166-171` |
| Delivery is at-least-once: a crash between delivery and offset commit redelivers the batch. | `persistence/events_store.go:173-175`; `internal/projectionrunner/runner.go:676-685` |
| The runner is the only non-test caller. It calls `ShardOffsets` and `GetShardEvents`; it caches committed offsets in memory and a configured `startingOffset` overrides them on every pull. | `internal/projectionrunner/runner.go:500`, `:602`, `:649-670` |
| The runner persists the offset after the whole batch is handled, with the timestamp as value. Tenant projections go through `offsetstore.ForScope` and fail closed with `ErrScopeUnsupported` when the adapter lacks `ScopedOffsetStore`. | `internal/projectionrunner/runner.go:812-831`; `offsetstore/scoped.go:13`, `:18`, `:42-63` |
| Offsets are keyed `(tenant_id, projection_name, shard_number)` and hold a bare `current_offset`. There is no version, no mode, no cursor header. | `persistence/postgres/schema/006_scoped_offsets.sql:4` |
| No `OneScope`, `AllScopesInCell`, cursor type or `ErrCursorMismatch` exists in code. | grep over non-archived Go files; the symbols appear only in `docs/prd` |
| `compose.requiredCapabilities` is an empty map: no minimum is enforced. | `compose/spec.go:327` |
| Automatic retention is rejected without a `RetainedEventsDeleter`. | `persistence/events_retention.go:10-18` |

## 4. Selection

### 4.1 Rules (Proposed)

- **S1.** A read never infers its scope. It is given a selection value that is one of:
  - `OneScope(scope)`: events of exactly one persisted scope key.
  - `AllScopesInCell`: events of every scope in the cell addressed by the reader.
- **S2.** `OneScope(Unscoped())` is a valid, ordinary selection: the single-tenant read. It reads rows whose persisted scope key is the empty key, exactly as `Unscoped()` reads today (**Current**: `persistence/postgres/event_store.go:50-55`). It needs no tenancy configuration, resolver or catalog (ADR section 9). The cursor binds this selection (section 6).
- **S3.** `Unscoped` is **not** a wildcard. `OneScope(Unscoped())` never returns a tenant's events, and no value of `OneScope` means "all". Omitting tenancy configuration does not turn any read into `AllScopesInCell`.
- **S4.** `AllScopesInCell` is **privileged** and requires, cumulatively:
  1. an explicit opt-in in the composition (the SharedCell processing mode of PRD R-01, or an administrative function that states it),
  2. an authorization decision that is not derived from the absence of tenancy and is re-checked on every read (a cursor is a position, not a credential),
  3. an adapter that declares the cell-wide-selection capability (section 9).
  Missing any of the three is a composition or startup rejection, not a silent downgrade to `OneScope`.
- **S5.** An invalid or zero-value selection returns the invalid-scope error and reads nothing (**Current** behavior for `Scope`: `ErrInvalidScope`, `persistence/scope.go:34`).
- **S6.** The shape of `Scope` and where the tenant-to-scope conversion lives are decided in #349 (P3). This contract talks only about the persisted scope key and the selection kind, so it survives either shape.

### 4.2 Why explicit

Today the scope is a parameter of the method, so the selection is implicit and cross-scope reads are impossible by omission. The target needs a cell-wide read for SharedCell, and an implicit design would let a missing tenant identity widen a read. Making the selection a value that the cursor binds is what keeps single-tenant (`OneScope(Unscoped())`) and cell-wide (`AllScopesInCell`) from ever being confused.

## 5. Logical slices versus physical assignment

- **L1.** A read is addressed to a **range of logical slices**: a set of slice identifiers over the logical slice space. A slice is a logical grouping of entities derived from `(scope, entity id)`; it does not depend on node count, partitions or the physical cell (PRD P-03).
- **L2.** Physical assignment (which node, executor or partition reads which range) is a separate concern, owned by the ownership and fencing work (#374). The reader contract takes a range as input and does not know who asked. A cursor is valid for the range it was issued for regardless of which executor holds it later, which is what makes handover possible.
- **L3.** "Slices grouped into partitions" is an allocation detail: several logical slices may be served by one physical partition or one range read. It must not change the cursor's meaning or the identity of a checkpoint, which is per slice (section 8).
- **L4. Updated.** This contract does not define the slice count N, the hash function or the key encoding; it consumes them. P1 is ratified by the maintainer and recorded in #350 and #351: N = 1024, FNV-1a 64 and the key encoding of `docs/decisions/logical-slices.md`, implemented by the unexported `persistence.sliceOf(scope, persistenceID)` (merged in #436). The ratification fixes the calculation only: it does not approve the migration strategy (P2) and the write path still uses `ActorSystem().Partition`. The stored slice of an event is the one computed from `(scope, persistenceID)`; a read filters on the STORED value and never recomputes it. The cursor header still carries a range descriptor and a slice-space identifier (section 6.2), so that a later change of N, hash or encoding makes old cursors **incompatible and rejected** instead of silently misread.
- **L5.** A feed that cannot read a sub-range (no range reads; optional in ADR 7.2) serves only the full range. Composition rejects an assignment that requires a sub-range on such a feed (section 9).

## 6. The cursor

### 6.1 Properties (Proposed)

| Property | Meaning |
|---|---|
| Opaque | Callers pass it back verbatim. They never parse, compare or construct one (the same discipline as `PersistenceIDs` page tokens, **Current**: `persistence/events_store.go:141-150`). Only a distinguished "start of range" value exists, and it is not a zero-length cursor. |
| Durable | It is a byte string that survives process restart and can be stored in the destination checkpoint (section 8). A cursor produced by version `v` is readable by every later release that still supports `v`. |
| Versioned | A header carries a format identifier and version. A reader supports a declared set of versions. |
| Bound | The header carries the cell, a fingerprint of the selection (kind and persisted scope key), and the range descriptor and slice-space identifier it was issued for. |
| Not a credential | Holding a cursor grants nothing. Authorization is checked on each read (S4). |
| Monotonic | Within one binding, a later cursor is never earlier than a previous one it replaced (checked by the checkpoint write, section 8). |

### 6.2 Header (Proposed content, encoding open)

format identifier, format version, cell identifier, selection fingerprint, range descriptor and slice-space identifier, mechanism identifier (so a cursor of the timestamp feed is never accepted by a commit-order feed), then a mechanism-defined body. Whether the header carries a checksum, and the body layout (a single position or one component per slice), are decisions D8 and the outcome of Gate A. The contract requires only that the header is validated before the body is trusted.

### 6.3 Range compatibility (Proposed rule)

Let cursor `c` be issued for range `R` and a read ask for range `R'` with the same cell, selection and slice space:

- `R' == R`: accepted.
- `R'` strictly contained in `R`: accepted. A stable prefix over `R` is a stable prefix over any subset, so this is safe. The returned cursor is bound to `R'`.
- `R'` contains slices not in `R` (superset or partial overlap): **rejected**. The cursor says nothing about the missing slices, so accepting it would skip their history.

The stricter alternative, equality only, is in section 12 (decision D7).

### 6.4 Rejection rules (Proposed; typed errors described, not final API)

Rejection happens **before any event is returned**, mutates nothing, and never falls back to "start from the beginning". Choosing to restart is the caller's explicit act.

| # | Condition | Error described | Notes |
|---|---|---|---|
| C1 | Bytes do not parse: truncated, bad format identifier, failed integrity check | malformed-cursor error | Includes every cursor the shard reader produced or consumed: a legacy offset (a bare `int64` timestamp, or a per-shard map of timestamps) has no v1 header, so the SLICE reader rejects it HERE, as malformed, and never as a mismatch. The mismatch classes C3 to C6 need a parsed header. The transitional catch-up reader of section 17 is not the slice reader: it reads the legacy offsets by design, only as the starting snapshot of its own transient cursors (section 17, item 2). |
| C2 | Format identifier is known but the version is unknown (newer or older than supported) | unsupported-cursor-version error | Names the offending version and the supported set. Never "best effort" decoding. |
| C3 | Cell differs from the reader's cell | cursor-mismatch error, reason `cell` | PRD `ErrCursorMismatch` "or its final approved equivalent" (`docs/prd/urd-platform-prd.md:114`). |
| C4 | Selection fingerprint differs (other kind, or other scope key) | cursor-mismatch, reason `selection` | Covers `OneScope(Unscoped())` cursor used with a tenant or with `AllScopesInCell`, and the reverse. |
| C5 | Range incompatible under section 6.3, or slice-space identifier differs | cursor-mismatch, reason `range` | A change of N, hash or key encoding lands here. |
| C6 | Mechanism identifier differs from the feed's | cursor-mismatch, reason `mechanism` | A cursor that has a header but belongs to ANOTHER mechanism (for example one produced by a counter feed and presented to a transaction-id feed) is never silently interpreted. A legacy shard offset has no header and is C1, not C6. |
| C7 | Position is beyond anything the feed can have produced (for example the journal was restored from an older backup) | cursor-ahead error | Reading from it would hide events written after the restore. Resolution is an explicit operator or #359 act. |
| C8 | Selection not authorized | authorization error | Distinct from mismatch so a mismatch never reveals whether another scope exists. |

Not an error: a cursor pointing before events that retention removed. The reader returns what exists and promises no reconstruction (section 11).

Typed shape (Proposed): a sentinel family usable with `errors.Is` for each class and a struct carrying the reason for `errors.As`. The set above is the minimum that #348 or the TCK must exercise once the reader exists.

## 7. Read semantics and properties

Terminology: a **position** is the feed's total order over events of one cell. The mechanism defines it; the contract constrains it.

### 7.1 Position constraints (Proposed)

- **P1.** Each event has one position that never changes while the event exists.
- **P2.** For events of one entity, position order equals sequence-number order.
- **P3.** Positions are totally ordered and comparisons are deterministic. If the mechanism cannot give every event a unique position (for example a transaction-level number shared by several events), it must define a tie-breaker; the xid8 candidate needs one for several events of one transaction (`#352` body). Otherwise a cursor could split a tie group (7.5).

### 7.2 SAFETY (zero omissions)

Safety means: for the accepted selection and range, **no committed event is ever omitted from the sequence a reader observes**, however commits interleave.

- **SAFE-1 stable prefix.** If a read returns cursor `c`, then no event with position at or before `c` that has not been delivered by that read or an earlier one can become visible later. Equivalent to the PRD wording (`docs/prd/urd-platform-prd.md:114`).
- **SAFE-2 no early advance.** A cursor never moves past a position where a transaction that could still commit an earlier position is unresolved. The unresolved part is withheld even if later events are already committed.
- **SAFE-3 no leak.** A read returns only events of the accepted selection and range.
- **SAFE-4 failure keeps safety.** If an operating condition of section 7.3 is violated, SAFE-1..3 still hold. What degrades is eligibility and progress, never silent loss.

The #348 oracle scenario is the concrete case: events `a@100` and `b@200` committed and a slower concurrent transaction commits `late@150` afterwards. A reader that already passed `b@200` must not have done so while `late@150` was still pending. The "known failure" criterion of #348 marks the current PostgreSQL adapter as failing this check.

### 7.3 ELIGIBILITY (bounded time to visibility, conditional)

An event committed at time `t` becomes readable no later than `t + T`, **provided the declared operating conditions hold**. `T` and the conditions are mechanism-specific and come from #352. The contract requires only that a feed **declares** its eligibility rule and its conditions, or declares that it gives no bound.

If the xid8-style bound is selected, its conditions (PRD `docs/prd/urd-platform-prd.md:219`, #352) must all be declared and, where possible, verified at startup:

1. a **dedicated PostgreSQL cluster per cell**, because the oldest in-progress transaction of any database in the cluster holds the horizon;
2. `transaction_timeout` (or equivalent) set for **every role that can write**, administrative and migration roles included. A request-context timeout alone does not enforce it, and a database transaction timeout can terminate the connection;
3. `max_prepared_transactions = 0`, since a prepared transaction holds the horizon until resolved;
4. an **alert on the oldest transaction identifier** so an old transaction is noticed before it silently stalls every reader in the cluster.

If any condition is broken: **safety is unaffected** (7.2, SAFE-4); the bounded-eligibility claim is void; readers stall behind the old transaction until it ends, so `T` is not met and progress may be delayed without bound. The feed must surface this (a metric or health signal) rather than appear healthy. Whether xid8 is chosen at all is Gate A; this paragraph is conditional text, not a choice.

Other mechanisms (a per-slice counter, batched publication) will have their own condition list; the contract needs only that each is declared and that the claim "eligible within `T`" is never made without it.

### 7.4 PROGRESS

- **PROG-1.** Under stated conditions (transactions terminate, capacity is above zero) a cursor advances after a blocking transaction is released, even if the handler never fails.
- **PROG-2.** Progress is eventual, not a latency bound. Eligibility and progress tests do not promise application latency (the #348 criterion).
- **PROG-3.** A read may return no events with an unchanged cursor: "nothing eligible now", not "caught up forever".
- **PROG-4.** A read may return no events with an advanced cursor, only to a position that is a stable prefix.
- **PROG-5.** Notification is a latency aid. The recovery source is polling plus the durable checkpoint (ADR 7.4).

Handler failure, parking and retry are the runner's concern (I-22, PRD R-02), not this contract.

### 7.5 Order, duplicates, pagination

- **Order.** Within a read and across successive reads from a cursor, events arrive in strictly increasing position, so per-entity sequence order is preserved (P2). The contract promises no ordering across entities beyond position order.
- **Duplicates.** A read from cursor `c` returns only events with position greater than `c`, and each at most once per read. At-least-once delivery arises only because a checkpoint advance may not have committed when a consumer crashed (**Current**: `persistence/events_store.go:173-175`). De-duplication is by applied marks (section 8).
- **Pagination.** `limit` is a target, not a cut point. A batch boundary is placed only at a position that the cursor can represent. Where the position is unique per event (P3), the batch may stop exactly at `limit`. Where a tie group exists, the batch is extended to the end of the group, never cut. **Current** behavior is exactly this for the timestamp tie group (`persistence/postgres/event_store.go:548-555`). A hard upper bound for oversize groups is decision D9.
- **Restart.** The reader holds no per-consumer state. After a restart, any process resumes from the checkpoint's cursor. The runner's in-memory offset cache and `startingOffset` override (**Current**: `internal/projectionrunner/runner.go:649-670`) are optimizations and are never the source of truth. An explicit start position is expressed as the range's start value, not as a hand-built cursor.

## 8. Checkpoint, applied marks, coherence with #362, #373, #374

- **K1. Location.** The cursor is stored in the **destination backend**, next to the effect, never in the journal backend as consumer state. The journal backend does not need to know its consumers. (This keeps cross-database setups honest: there is no atomicity between a journal and a different destination, ADR 7.4.)
- **K2. Identity** (PRD R-01, #362): `(mode, scope or cell, processor, projection version, slice)`. PerScope uses scope; SharedCell uses cell. **Current** key is `(tenant_id, projection_name, shard_number)` (`persistence/postgres/schema/006_scoped_offsets.sql:4`), so mode, version and slice-as-logical-slice are new. A new version starts with a fresh cursor and fresh marks (#362). Changing mode invalidates incompatible cursors (#362 single-tenant clarification, #424). A PerScope key with scope `Unscoped` is valid and is not a wildcard.
- **K3. Cursor field.** The checkpoint stores the cursor bytes verbatim and re-validates the header when loading (section 6.4). It is written with a compare-and-set against the stored cursor, which rejects: a different cell, selection fingerprint, mechanism identifier or slice-space identifier (the fields of the header of section 6.2, with the error classes C3 to C6); a range descriptor that is not equal to or contained in the stored one (a narrowed range of section 6.3 is accepted and the checkpoint then takes the narrower range; a widened range is rejected as in C5); and, within the same binding, a position earlier than the stored one (monotonic). The layout epoch is NOT a cursor field and is not compared here: it is a state of the reader (section 16), which refuses to read when the layout it serves is not active. The comparison is made per slice, because the checkpoint identity is per slice (K2): narrowing a range leaves the checkpoints of the dropped slices untouched.
- **K4. Applied marks** (PRD `:116`): `(processor, version, scope, entity) -> lastSeqNr`. Two processors never share marks (#373). A mark never advances over unresolved work (PRD R-02).
- **K5. One transaction.** Where the destination supports it, effect, applied mark and checkpoint advance commit in one destination transaction, with the ownership fence validated at the same write, not before it (#373, #374). Rollback leaves no partial progress. A stale executor is rejected on effect, mark and checkpoint alike (#374).
- **K6. No common transaction.** A destination without one may serve only features whose contract permits a declared idempotent-upsert strategy with fencing and a recoverable checkpoint protocol. The effect guarantee is then at-least-once. Integration and Workflow reject such a destination (ADR 7.3). Nothing claims cross-database atomicity or external exactly-once.
- **K7. Open interaction.** Automatic retention must know the lowest durable progress of every required consumer (**Current**: `docs/decisions/scoped-offsets-retention.md`, `persistence/events_retention.go:10-18`). With K1 that progress lives outside the journal backend. How the deleter learns it (a registry, an explicit report, a declared minimum) is not decided here (D12, with #359 and the consumer-registry work named in `scoped-offsets-retention.md`).

## 9. Mandatory capabilities and rejected combinations

### 9.1 Capabilities of the feed role

This reuses, without changing, the role table of ADR 7.2. The names are illustrative (P4, #384).

| Capability | Mandatory for the feed role | Relation to ADR 7.2 |
|---|---|---|
| Declared stable prefix (SAFE-1..4), proven by the TCK | Yes | "declared stable prefix" |
| Declared eligibility rule and operating conditions, or declared "no bound" | Yes | "declared eligibility rule" |
| Durable, validated, versioned opaque cursor (6.4) | Yes | "durable, validated, versioned opaque cursor" |
| `OneScope` selection, including `Unscoped()` | Yes | consequence of the PRD single-tenant rule |
| Per-slice range reads (L5) | Optional | "per-slice range reads" optional |
| Cell-wide `AllScopesInCell` selection | Optional (required only by SharedCell) | new row under "optional"; does not contradict ADR 7.2 |
| Transitional legacy catch-up reader (section 17) | Optional; required only for the barrier and catch-up form of the no-rebuild transition (section 17); the minimum-seed form does not need it (D16) | new row under "optional"; does not contradict ADR 7.2 |
| Wakeup notification | Optional, latency only | "wakeup notification" |

Destination role (mandatory common transaction or declared upsert strategy, fencing, checkpoint identity in the destination) is as in ADR 7.2 and 7.3 and is not repeated.

### 9.2 Combinations composition must reject (Proposed)

Rejection is before the adapter operates (ADR 7.3: static `Spec.Validate`, startup probe, TCK selection).

| # | Combination | Why |
|---|---|---|
| X1 | A projection, integration or workflow on a feed that does not declare a stable prefix | SAFE-1 absent; today's `GetShardEvents` falls here (T7 and P7 of the ADR decide any grandfather window) |
| X2 | `AllScopesInCell` without explicit opt-in, authorization (S4) or the adapter capability | S3, S4 |
| X3 | SharedCell on a feed without cell-wide selection, or on independent destinations under one shared checkpoint | S4, PRD C-05 |
| X4 | A sub-range assignment (several executors) on a feed without range reads | L5 |
| X5 | Several executors on a destination without fencing validated at the write | #374 ("without fencing forces one-executor mode") |
| X6 | Integration or workflow on an upsert-only destination, or with the checkpoint outside the destination transaction | ADR 7.3 |
| X7 | A bounded-latency promise (`T`) on a feed that declares no eligibility bound, or whose operating conditions (7.3) are neither verified nor attested | 7.3 |
| X8 | A stored checkpoint whose cursor version, mechanism or slice space the feed does not support, or a stored legacy offset presented as a slice cursor | 6.4 C1, C2, C5, C6; surface at startup, not at the first read. This row concerns a projection that is to read from the SLICE feed, which can only happen from `ACTIVE_NEW`; in `LEGACY`, `PREPARED` and `FENCED` a projection keeps using its legacy offsets with the shard reader (section 16) and this row does not apply. For a projection that is to read from the slice feed, a stored legacy offset is never a slice cursor: with a validated transition (section 17) it is the read-only starting snapshot of the transient catch-up cursors of the transitional reader (barrier and catch-up form) or an input to the seed computation of the cutover (minimum-seed form), and with no validated transition it is rejected at startup as C1 |
| X9 | A tenant-scoped (non-`Unscoped`) projection on an offset store without scoped support | **Current** behavior kept (`offsetstore/scoped.go:13`, `:42-63`) |

## 10. Deprecation and transition of `GetShardEvents`

Nothing below changes `GetShardEvents` or removes it in this change. It is a plan, with one maintainer decision (D11).

| Stage | What happens | Condition |
|---|---|---|
| 0, this document | `GetShardEvents` and `ShardOffsets` keep their behavior and their documented gap (**Current**: `persistence/events_store.go:151-187`). The #348 check records the omission as a **known failure** of the PostgreSQL adapter. | none |
| 1 | The new reader is introduced **additively** as a separate optional interface, not as a new method of `EventsStore`. A new method on `EventsStore` would break every external implementer (the interface already documents earlier breaking changes, `persistence/events_store.go:31-87`) and the `api` CI job would flag it. | ADR approved; mechanism chosen at Gate A; implementation issue opened (PRD P-02 traces #360; to be confirmed) |
| 2 | `// Deprecated:` godoc on `GetShardEvents` and `ShardOffsets` naming the replacement, PR labeled `kind/deprecation`. Behavior and callers unchanged. | the replacement exists and passes the TCK on memory and PostgreSQL |
| 3 | The runner (`internal/projectionrunner/runner.go:500`, `:602`, the only non-test callers) moves to the new reader behind the capability. A legacy path remains only if the legacy-adapter policy (ADR P7) grandfathers it. | #362, #373 done; #359 cutover plan accepted |
| 4 | Removal in a major release, after the stated number of releases with the deprecation (D11), with no in-repo caller left and a migration note. | maintainer decision |

The stage 1 caveat also applies to `testkit/eventstore.go:454` and `persistence/postgres/event_store.go:537`: both keep implementing the method until stage 4.

## 11. Offsets, cutover and retention (not decided here)

- Existing offsets are Unix-nanosecond timestamps (**Current**: `docs/decisions/scoped-offsets-retention.md`) and are **not** valid v1 cursors (C1). Converting or invalidating them, the cutover strategy, blocking old writers and the verification are P2 and belong to #359, with #351 only fixing the cursor format and the rejection rules. Converting a timestamp to a commit-order position cannot be claimed safe against late commits.
- **Updated.** A projection does NOT have to rebuild because the mechanism has no function that maps a legacy timestamp to a position. Section 17 defines the transitions that avoid it, and the cutover procedure proposed for #359 (`docs/decisions/slice-cutover-359.md` in PR #444, strategy S2; a proposal, not approved) is the form the first of them is meant to take. Both are conditional on P2.
- A reader returns only events that exist. If retention or explicit deletion removed events, a read from an earlier cursor returns what remains. **No reconstruction of absent data is promised**, and replay from the first available event is not a full recovery. How to record the first available position per scope is #359 (see `docs/decisions/logical-slices.md`, merged in #436). **Updated.**
- **Updated.** P1 is ratified (L4). What remains conditional is anything that depends on P2 (cutover, retention), P3 (`Scope` shape) or on the Gate A mechanism.

## 12. Alternatives considered

| Alternative | Why not chosen |
|---|---|
| Keep the timestamp cursor and re-read an overlap window | No bound on lateness; it reduces omissions without a guarantee. PRD P-02 refuses a timestamp-only reader. |
| Keep a bare `int64` cursor and add checks elsewhere | No binding to selection, cell or range; silent cross-selection reuse stays possible. |
| Treat `Unscoped` as "all scopes" | Breaks isolation (PRD P-03, T-01); `Unscoped` would silently widen a read. |
| Make `AllScopesInCell` implied when tenancy is not configured | Privilege by omission. |
| Store consumer progress in the journal backend | Couples the journal to its consumers and defeats the destination transaction (K1, ADR 7.4). Open consequence for retention: K7. |
| Add the reader as a new method of `EventsStore` | Breaks external implementers (section 10). |
| Fix xid8 as the mechanism now | Gate A (#387, #352) decides with measurements; xid8 is a candidate. |
| Fix N, hash and key encoding here | **Updated.** P1 is ratified in #350 and implemented by `sliceOf`; this contract consumes it and only makes a later change detectable (L4). |
| Notification as the recovery source | Lossy; polling and the durable checkpoint are the source (PROG-5). |
| Range compatibility by equality only (D7) | Simpler, but forces a new cursor on every reassignment that narrows a range. Not excluded. |
| Silent restart on a rejected cursor | Hides omissions or causes massive replay without the caller choosing. |

## 13. Decisions that need maintainer approval

None of these is silently chosen. The draft states the working assumption and does not block on it.

| ID | Decision | Owner and issue | Resolves when | Working assumption here |
|---|---|---|---|---|
| D1 | Approve this contract as the reader contract | Maintainers, #351 | Recorded on #351 | Proposed; no criterion declared met |
| D2 | Approve the module-topology ADR (the feed role table, P4, P7) | Maintainers, #347 | Recorded on #347 | Section 9 reuses ADR 7.2 as is; if the ADR changes, section 9 is revised |
| D3 | Reconcile property and scenario names with the #348 change | #348 author | #348 merged | Section 14 uses provisional names |
| D4 | Reader mechanism and its `T` and operating conditions | Maintainers, #352 / #387 (Gate A) | Gate A recorded | Not chosen; 7.3 is conditional |
| D5 | Slice count N, hash, key encoding (P1) | Maintainers, #350 | **Resolved**: N = 1024, FNV-1a 64, the key encoding of `logical-slices.md`, recorded in #350 and #351 (PR #436 merged) | Header still carries a slice-space identifier, so a later change is detected |
| D6 | `Scope` shape and where tenant-to-scope conversion lives (P3) | #349 | #349 design | Contract uses the persisted scope key |
| D7 | Range compatibility: contained subset (6.3) or equality only | Maintainers, #351 | Decision on #351 | Subset accepted |
| D8 | Final error names, header fields, integrity check, and the body layout (single position or per slice) | #351 and its implementation issue | Implementation design | Section 6 described, not final |
| D9 | Policy when a tie group exceeds the batch ceiling | #351 and implementation | Implementation design | Group never cut; ceiling policy open |
| D10 | Authorization mechanism for `AllScopesInCell` (S4) | #379, #368; #424 for single tenant | Their designs | Required, mechanism open |
| D11 | Deprecation window, removal release, legacy-adapter grandfathering (ADR P7) | Maintainers, #351 and #384 | Decision recorded | Stages in section 10 |
| D12 | How retention learns consumer progress when checkpoints live in the destination (K7) | #359 with the consumer-registry work | #359 design | Not decided |
| D13 | Conversion or invalidation of existing offsets, cutover, retention gaps (P2) | #359 | #359 design | Out of scope here |
| D14 | Issue that implements the reader (PRD P-02 traces #360) | Maintainers | Issue confirmed | To be confirmed |
| D15 | Location of the active specification | Maintainers | Reply on #351 | **Proposed resolved**: `openspec/specs/slice-reader/spec.md`, the location `openspec/README.md` names as the active one; no archived spec is reactivated. Needs the maintainers' confirmation |
| D16 | Whether the transitional legacy reader of section 17 is an adapter capability, and who owns it | Maintainers, #351 and #359 | Decision recorded | Required for the barrier and catch-up form of the no-rebuild transition (the minimum-seed form does not use it), including its separately persisted transient cursors (section 17, item 2); without a validated form a projection rebuilds or is invalidated. Traced to criterion 5 and to rows 9 and 10 of section 14 |
| D17 | How the reader reports that the layout is not active (a typed error or a capability probe) | #351 and its implementation issue | Implementation design | Section 16 requires only that it read nothing and say why. Traced to criterion 3 and to row 9 of section 14 |

## 14. Acceptance traceability

Status vocabulary: **Documented** means the draft contains the content. **Pending** means it cannot be satisfied by this document. No row is declared met. Where a criterion depends on the ADR (#347) being approved, the dependency is stated and the proposal advances.

Live criteria of #351 and where they land:

| # | Criterion (#351, live body) | Section | #348 scenario / property (provisional) | Status |
|---|---|---|---|---|
| 1 | New contract approved in the current specification location | whole document; D1, D15 | n/a | **Pending**: approval is a maintainer act. Depends on the location decision (D15) and on ADR approval (D2) |
| 2 | Archived specs are not reactivated automatically | header, 3, 11 | n/a | Addressed by construction: this change adds one file under `openspec/specs` and no archived path; nothing from `docs/archive` is reused as normative. Reviewer confirms |
| 3 | Cursor rejection cases defined | 6.4 (C1..C8) | none in #348 (its scope is safety, eligibility, progress); needs a cursor TCK once a reader exists | **Documented**, Proposed. Test pending (D8, D14) |
| 4 | Conditions of the `<= T` bound declared (dedicated cluster, timeout for all writer roles, no prepared transactions, old-XID alert) and what happens if broken | 7.3, SAFE-4, X7 | ELIGIBILITY: event readable within `T` after commit with a bounded long transaction | **Documented**, conditional on the mechanism (D4). Depends on ADR approval; not met |
| 5 | Portability rules: offset in destination backend, destination Tx, slices grouped into partitions, capabilities per adapter | 5 (L1..L5), 8 (K1, K5, K6), 9 | none yet (destination Tx tests are #373) | **Documented**. Capability enforcement is #384; depends on ADR approval; not met |
| 6 | Identity of the offset and of the applied mark | 8 (K2, K4), coherent with #362, #373, #374 | none in #348 | **Documented**. Pending #362/#373/#374 and D13 |
| 7 | `GetShardEvents` deprecated with a retirement plan | 10 | #348 marks the current adapter's omission check as known failure | Plan **Documented**; the deprecation itself is **Pending** (no code here, D11) |
| 8 | Single tenant: `OneScope(Unscoped())` valid and cursor binds it; `AllScopesInCell` privileged and distinct; omitting tenancy does not enable it | 4 (S1..S4), 6.4 C4, X2 | none in #348; coverage via #424 | **Documented**. Test pending |

Additional rows. They are not criteria of #351; they trace the sections added by the consolidation to the issue that owns them, so no section is left without a source:

| # | Content | Section | #348 scenario / property (provisional) | Status |
|---|---|---|---|---|
| 9 | What the readers do in each layout epoch state; the public shard reader versus the transitional catch-up reader (from #350 and #359) | 16, 9.1 | none in #348 | **Documented**, conditional on the approval of the cutover (D13, D16, D17). Test pending |
| 10 | Transition from legacy offsets without rebuild (from #359, strategies S2 and S1 of PR #444) | 17, 9.1, D16 | none in #348; the property test of section 17, item 4 for the barrier and catch-up form, including independent per-slice transient cursors | **Documented**, conditional on D13 and D16. Evidence pending. Rollback of catch-up effects (section 17) is open for #444 |
| 11 | Observable cases for criteria 3 (cursor rejection), 6 (identity) and 8 (single tenant), and for rows 9 and 10 | 18 | cases to add to the conformance suite once a reader exists | **Documented**. Test pending |

Mapping of #348 checks to contract clauses (names provisional, to be reconciled when the #348 change merges):

| #348 check (from its live description) | Contract clause | Notes |
|---|---|---|
| SAFETY: `a@100`, `b@200`, `late@150` with two concurrent transactions; zero omissions; deterministic failure on the current PostgreSQL adapter, marked as a known failure | SAFE-1, SAFE-2 | The check is expected to fail today (section 3) |
| ELIGIBILITY: an event is readable within `T` after commit with a bounded long transaction; makes no application-latency promise | 7.3 | Conditional on declared conditions |
| PROGRESS: the cursor advances when the transaction is released, with a handler that never fails | PROG-1, PROG-2 | No latency promise |
| Runs also against `testkit` | 9.1 (declared properties are proven by the same cases on every adapter) | Memory adapter also fails the safety check if it keeps the timestamp cursor |
| Runner errors tested in I-22 | out of scope | PRD R-02 |

## 15. Evidence and unverified claims

**Verified on `b615449` and re-verified on `fde51c4`:** every row of section 3 (read directly from the cited file and lines: `persistence/events_store.go:166-171`, `persistence/postgres/event_store.go:537`, `internal/projectionrunner/runner.go:500` and `:602`, `compose/spec.go:327`, `persistence/events_retention.go:10`). The PRD, the ADR and `docs/decisions/logical-slices.md` (merged) were read.

**Unverified in this draft:**

- The scenario and property names of the #348 change (not available when this was written); section 14 is provisional.
- Whether any external implementer of `EventsStore` exists, which affects the severity of the stage 1 and stage 4 steps.
- That the eligibility conditions in 7.3 are sufficient for any mechanism: they are the conditions stated for the xid8 candidate and must be validated by Gate A.
- That the checkpoint compare-and-set of K3 is expressible in every destination backend; the destination capability matrix is #384.
- That `AllScopesInCell` is needed at all in V1 beyond SharedCell.
- No test, build or benchmark was run: the change is documentation. The repository's existing checks are the validation.
- Gate A has no recorded result: the criteria of #387 are unchecked, `benchmark/` holds no mechanism result, and the omission case of #348 is implemented in PRs #442 and #445 but not merged.

## 16. Layout compatibility (added from the #443 draft)

The previous layout is the shard reader, the legacy `shard_number` and the legacy offset rows keyed `(scope, projection name, shard)`. The layout epoch (`LEGACY`, `PREPARED`, `FENCED`, `ACTIVE_NEW`) is defined by the cutover procedure (#359) and stored by #358; this contract only fixes what a reader does in each state. The rules of this section and of section 17 are CONDITIONAL on P2: they describe what any approved cutover must satisfy and become normative together with the approval of the cutover (#359; the procedure is proposed in PR #444, strategy S2). If the approved cutover differs, these two sections are revised. Wherever a rule in sections 1 to 15 refers to the transitional reader, to the layout epoch or to sections 16 to 18 (and likewise the decisions D16 and D17 and the traceability rows 9 to 11), that reference applies only if the approved cutover provides what it names; if it does not, the reference is void and the rest of that rule stands.

- **While the epoch is `LEGACY`, `PREPARED` or `FENCED`** the slice reader MUST read nothing and MUST report that the layout is not active (D17). The public shard reader (`GetShardEvents`, `ShardOffsets`) and its offsets keep their current behavior. Legacy offset rows MUST NOT be reinterpreted as slice offsets: a legacy shard is not a slice (section 5), and the legacy shard of an old row is not recomputable.
- **When the epoch is `ACTIVE_NEW`** the PUBLIC shard reader MUST refuse to read, for every caller, because a row written under the new layout has no meaningful legacy shard. The legacy column and rows stay until the retirement plan of `GetShardEvents` (section 10) completes.
- **Delivery driven by a legacy offset (legacy delivery) in `ACTIVE_NEW` is allowed only to the transitional catch-up reader of section 17, in the barrier and catch-up form.** A slice reader that starts from a seed derived from legacy offsets (the minimum-seed form) is not legacy delivery: it is driven by a slice cursor. The transitional reader is not the public shard reader: it is an internal capability of the adapter (D16), read-only on the journal, available only in `ACTIVE_NEW`, only for events that precede the barrier, and only for a projection whose catch-up of that slice is incomplete. It never writes the legacy offset rows: its progress lives in its own transient cursors (section 17, item 2). Once no projection has a pending catch-up it refuses too, and it is removed with the retirement of `GetShardEvents`.
- **Who touches the legacy offset rows in `ACTIVE_NEW`.** The original legacy offset rows are a **starting snapshot** and no reader advances them: not the slice reader, not the public shard reader and not the transitional reader. The transitional reader may read them only to initialize its transient cursors (section 17, item 2) and persists its progress elsewhere. The cutover tooling reads them to compute and verify its plan (the thresholds of the barrier and catch-up form, the seeds of the minimum-seed form). The rows become **superseded** only under the rule below; marking them superseded is the only change made to them, and only the cutover tooling makes it. In `LEGACY`, `PREPARED` and `FENCED` the runner keeps writing them as it does today.
- **When an original legacy offset row is superseded.** In the minimum-seed form, at the switch, because the slice cursors are seeded from the rows and there is no catch-up. In the barrier and catch-up form, a row `(scope, projection, legacyShard)` is superseded only when **every** transient catch-up cursor initialized from it (one per slice that holds pre-barrier events of that legacy shard, for every version of the processor that uses it) has finished AND its final checkpoint is confirmed as committed in the destination. The switch of the layout epoch to `ACTIVE_NEW` by itself does not prove that: the catch-ups only start there. Until then a row stays unsuperseded, and an unfinished or unconfirmed catch-up keeps it so.
- **Persisted keys do not change.** The scope key stays `''` for Unscoped and the tenant id otherwise, and the hash input stays `(scope, persistenceID)` as the ratified `sliceOf` consumes it (#349 must keep those bytes).
- The slice stored with an event is written by the writer from `(scope, persistenceID)` and is never derived from the actor name, the actor namespace or the physical partition.

## 17. Transition without rebuild (added; aligned with the cutover procedure of #444)

Section 11 says that converting a timestamp to a commit-order position cannot be claimed safe. That does not oblige a projection to rebuild. Like section 16, this section is conditional on P2. A projection continues from its legacy offsets, with no rebuild and no silent reset, if and only if the adapter provides a **validated transition**. Two forms are defined. The **barrier and catch-up form** is described by the four items below. The **minimum-seed form** (strategy S1 of #444) starts each slice cursor at the minimum of the legacy offsets that contribute to it, and exists only if the mechanism provides `PositionAtOrBefore(T)` and the conditions that #444 states for it hold; it does not use items 1 to 3 and is validated only by its own evidence. The barrier and catch-up form is:

1. **Barrier.** The cutover records a barrier, after quiescing the writers, that separates pre-barrier events from post-barrier events by a persisted value, not by a writer-stamped timestamp.
2. **Transitional reader and transient cursors.** The adapter declares a capability that delivers the pre-barrier events of a slice from a legacy position, per legacy shard: the old read with one added predicate (scope, stored slice, legacy shard, timestamp greater than the cursor position). The original legacy offsets are a starting snapshot and are **never advanced** by this reader. Each catch-up has its own **transient cursor** identified by `(scope, processor, version, slice, legacyShard)`. It is initialized from the snapshot of the corresponding legacy offset `(scope, processor, legacyShard)`, and it is persisted separately, in the destination backend (K1), with the effect where the destination supports it (K5). Advancing the transient cursor of one slice never advances another, even when they share scope, processor, version and legacy shard: a legacy offset is a bound over a whole shard and is not progress of any single slice. In `ACTIVE_NEW` it is the only reader that performs legacy delivery, that is, delivery driven by a legacy position (section 16); it is bounded, and it is removed at the retirement of `GetShardEvents`.
3. **Handover.** When every legacy shard that holds pre-barrier events of the slice has none left (every transient cursor of that slice has finished and its checkpoint is confirmed), the slice cursor is created at the barrier and the new reader takes over. The new reader MUST NOT deliver the post-barrier events of a slice before that slice's catch-up is complete, so one entity's pre-barrier events precede its post-barrier ones.
4. **Evidence.** The transition is validated only with: a model-based property test over generated histories showing that the delivered set equals exactly the set the old projection had not handled, which fails under mutation, and which includes the case that two slices of the same scope, processor, version and legacy shard progress independently (section 18); and an adapter conformance check that after quiesce no event can commit with a pre-barrier identity.

If the adapter has neither validated form, a legacy offset is invalid as a slice cursor (C1) and the projection MUST rebuild under a new version, or be blocked, never reset silently. The absence of `PositionAtOrBefore(T)`, a mechanism function that maps a legacy timestamp to a position such that every later event is after it, does NOT oblige a rebuild when the barrier and catch-up form is validated. The exact assumption of the transition (the old read predicate) and its limits are in `docs/decisions/slice-cutover-359.md`, section 3.5, in the version proposed in PR #444 (not approved); this contract does not restate them.

**Rollback (for #444).** In the barrier and catch-up form, the catch-up delivers pre-barrier events, so effects in the destination and transient checkpoints exist before the slice cursors do. The rollback procedure of #444 must account for those effects and checkpoints too, not only the layout epoch and the slice state. Until a reversal of them is demonstrated, this contract does not claim that a rollback after catch-up effects restores the previous state exactly: the original legacy offsets are intact (never advanced), but they do not describe what the destination already applied.

## 18. Scenarios (added from the #443 draft)

These are the observable cases a conformance suite for the slice reader must cover once the reader exists. They add to, and do not replace, the #348 oracle scenarios.

- **Slice range filters on the stored slice.** Events of one scope stored with slices 3, 700 and 1023; a read of `[0, 512)` returns only the one with slice 3.
- **Unscoped is not a wildcard.** Events of `Unscoped()` and of tenant `acme`; `OneScope(Unscoped())` returns none of `acme`'s, and no selection reads every tenant except a privileged `AllScopesInCell`.
- **Per-entity order inside a slice.** Two events of one persistence id with sequence numbers 4 and 5 in one slice are delivered 4 before 5 whatever the page size.
- **A late commit does not appear behind the cursor.** A transaction that stamped an earlier time and has not committed, and a later event that did: the cursor does not pass the later event until the earlier transaction resolves.
- **Resume after a crash.** A consumer that handled a page and crashed before committing its cursor receives the same page again and handles it by event identity.
- **A legacy offset is not a slice cursor.** An offset written by the shard reader, when presented to the slice reader, is rejected with the malformed-cursor error (C1) and reads nothing; it is never reported as a mismatch. Only the transitional reader uses it, as the starting snapshot of its transient cursors.
- **A cursor of another mechanism is a mismatch.** A cursor that has a header but belongs to another mechanism is rejected with the cursor-mismatch error, reason `mechanism` (C6), and reads nothing.
- **Narrowing a range is accepted, widening is not.** A cursor issued for `[0, 512)` is accepted for `[0, 256)` and the checkpoint then takes the narrower range, leaving the dropped slices' checkpoints untouched; the same cursor for `[0, 1024)` is rejected with the cursor-mismatch error, reason `range` (C5).
- **The same projection and slice in two tenants.** Each commits its own offset; reading or resetting one never changes the other.
- **Layout not active.** With the epoch `LEGACY`, `PREPARED` or `FENCED` the slice reader reads nothing and says so. With `ACTIVE_NEW` the public shard reader refuses, while the transitional catch-up reader still delivers the pre-barrier events of a slice whose catch-up is pending, through its own transient cursor and not the legacy row, and refuses once none is pending.
- **Catch-up cursors are independent per slice.** Same scope, projection and legacy shard; the legacy offset is the snapshot S. Slice A holds a pre-barrier event @200 and slice B one @150, both after S. The transitional reader processes A and saves A's transient cursor at @200: B's cursor is still at S and B still receives @150. The original legacy offset row is unchanged. After a restart both cursors are reloaded as saved, A at @200 and B at S (or wherever B's own progress had reached), and neither is recomputed from the other or from the legacy row. This is the specification of a future test, not a claim about an implemented reader.
- **Superseding the legacy offsets.** After the switch to `ACTIVE_NEW` the legacy row is not superseded. It becomes superseded only when every transient cursor initialized from it has finished and its checkpoint is confirmed; with one catch-up unfinished or unconfirmed, the row stays unsuperseded.
- **Transition without rebuild.** With a validated transition, a projection that was behind in a legacy shard receives exactly its unhandled pre-barrier events, then the post-barrier ones, with no event skipped, and the original legacy offsets are never advanced by that delivery.
