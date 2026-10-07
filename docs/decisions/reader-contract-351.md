# Portable reader contract: explicit selection, versioned cursor and stable prefix (#351, I-05)

| Field | Value |
|---|---|
| Status | **Proposed.** Drafted for review; not approved. Approval is a maintainer decision recorded on #351. Nothing here is delivered code, and no acceptance criterion of #351 is declared met by this document. |
| Date | 2026-10-07 |
| Tracker | #351 (I-05), epic #342, phase 0, P0 |
| Depends on | #348 (I-02, reader TCK: safety, eligibility, progress; open, implemented in a sibling change under `persistence/conformance`, not part of this PR). The module-topology ADR `docs/decisions/module-topology-347.md` (**Proposed**, #347 open; its presence on `develop` is **not** approval). |
| Feeds | #352 / #387 (Gate A, mechanism), #350 (slices), #359 (offset migration, cutover, retention), #362 / #373 / #374 (checkpoint, applied marks, fencing), #384 (capability enforcement), #424 (single tenant) |
| Baseline | Code inspected: `origin/develop` at `b615449`. Statements tagged **Current** refer to that snapshot, with file and line. |
| Scope | Documentation only. No code, schema, `go.mod`, GoAkt, hook or archived-spec change. Nothing under `persistence/conformance` is touched. |

How to read this document. **Current** is verified on `b615449`. **Proposed** is what this contract suggests and needs a maintainer decision (section 13). **Unverified** is a claim this draft could not check. Property names in section 7 follow the SAFETY / ELIGIBILITY / PROGRESS split of #348; they are provisional until the #348 change merges, and section 14 must then be reconciled with its real scenario names. Error and type names are descriptive, not a final API.

## 1. Context

The only feed read in the repository is `GetShardEvents(ctx, scope, shard, offset int64, limit)`. Its cursor is a writer-stamped timestamp. Its own documentation admits the gap: an event that becomes visible after its offset was committed, with a timestamp at or below that offset, is never returned (**Current**: `persistence/events_store.go:166-171`). That is gap B1 of the baseline audit and requirement P-02 of the PRD (`docs/prd/urd-platform-prd.md:103`): explicit selection, opaque validated cursor, stable prefix, zero omissions in the oracle, eligibility and progress tested separately, and no timestamp-only reader accepted as a correction.

This document specifies the contract of the reader that replaces it. It says **what** a conforming reader promises and what composition rejects. It does **not** choose the mechanism (xid8, per-slice counter, batched publication): that is Gate A (#352, #387). It does not fix the slice count, hash or key encoding (P1 of the ADR, #350), nor the offset cutover (P2, #359).

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
- **L4.** This contract does **not** fix the slice count N, the hash function or the key encoding. They are P1 of the ADR, tracked in #350 and the draft decision document of PR #436, and **not ratified**. The provisional unexported function in that draft is no contract. The cursor header carries a range descriptor and a slice-space identifier (section 6.2), so that a later change of N, hash or encoding makes old cursors **incompatible and rejected** instead of silently misread.
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
| C1 | Bytes do not parse: truncated, bad format identifier, failed integrity check | malformed-cursor error | Includes a raw legacy timestamp value (a bare `int64` is not a v1 cursor). |
| C2 | Format identifier is known but the version is unknown (newer or older than supported) | unsupported-cursor-version error | Names the offending version and the supported set. Never "best effort" decoding. |
| C3 | Cell differs from the reader's cell | cursor-mismatch error, reason `cell` | PRD `ErrCursorMismatch` "or its final approved equivalent" (`docs/prd/urd-platform-prd.md:114`). |
| C4 | Selection fingerprint differs (other kind, or other scope key) | cursor-mismatch, reason `selection` | Covers `OneScope(Unscoped())` cursor used with a tenant or with `AllScopesInCell`, and the reverse. |
| C5 | Range incompatible under section 6.3, or slice-space identifier differs | cursor-mismatch, reason `range` | A change of N, hash or key encoding lands here. |
| C6 | Mechanism identifier differs from the feed's | cursor-mismatch, reason `mechanism` | A cursor from the legacy feed is never silently interpreted by a new one. |
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
- **K3. Cursor field.** The checkpoint stores the cursor bytes verbatim and re-validates the header when loading (section 6.4). It is written with a compare-and-set that rejects a cursor with a different header or an earlier position (monotonic).
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
| X8 | A stored checkpoint whose cursor version, mechanism or slice space the feed does not support | 6.4 C2, C5, C6; surface at startup, not at the first read |
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
- A reader returns only events that exist. If retention or explicit deletion removed events, a read from an earlier cursor returns what remains. **No reconstruction of absent data is promised**, and replay from the first available event is not a full recovery. How to record the first available position per scope is #359 (see the draft logical-slices document).
- N, hash and key encoding are not ratified (P1). Anything depending on them is conditional (L4).

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
| Fix N, hash and key encoding here | P1 is not ratified; this document only makes their change detectable (L4). |
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
| D5 | Slice count N, hash, key encoding (P1) | Maintainers, #350 (PR #436 is a draft) | Decision recorded in #350 | Not fixed; header carries a slice-space identifier |
| D6 | `Scope` shape and where tenant-to-scope conversion lives (P3) | #349 | #349 design | Contract uses the persisted scope key |
| D7 | Range compatibility: contained subset (6.3) or equality only | Maintainers, #351 | Decision on #351 | Subset accepted |
| D8 | Final error names, header fields, integrity check, and the body layout (single position or per slice) | #351 and its implementation issue | Implementation design | Section 6 described, not final |
| D9 | Policy when a tie group exceeds the batch ceiling | #351 and implementation | Implementation design | Group never cut; ceiling policy open |
| D10 | Authorization mechanism for `AllScopesInCell` (S4) | #379, #368; #424 for single tenant | Their designs | Required, mechanism open |
| D11 | Deprecation window, removal release, legacy-adapter grandfathering (ADR P7) | Maintainers, #351 and #384 | Decision recorded | Stages in section 10 |
| D12 | How retention learns consumer progress when checkpoints live in the destination (K7) | #359 with the consumer-registry work | #359 design | Not decided |
| D13 | Conversion or invalidation of existing offsets, cutover, retention gaps (P2) | #359 | #359 design | Out of scope here |
| D14 | Issue that implements the reader (PRD P-02 traces #360) | Maintainers | Issue confirmed | To be confirmed |
| D15 | Location of the active specification (this `docs/decisions` ADR directory versus another location) | Maintainers | Reply on #351 | `docs/decisions`, like the ADR; no archived spec is reactivated |

## 14. Acceptance traceability

Status vocabulary: **Documented** means the draft contains the content. **Pending** means it cannot be satisfied by this document. No row is declared met. Where a criterion depends on the ADR (#347) being approved, the dependency is stated and the proposal advances.

Live criteria of #351 and where they land:

| # | Criterion (#351, live body) | Section | #348 scenario / property (provisional) | Status |
|---|---|---|---|---|
| 1 | New contract approved in the current specification location | whole document; D1, D15 | n/a | **Pending**: approval is a maintainer act. Depends on the location decision (D15) and on ADR approval (D2) |
| 2 | Archived specs are not reactivated automatically | header, 3, 11 | n/a | Addressed by construction: this change adds one file under `docs/decisions` and touches no archived path or `openspec`. Reviewer confirms |
| 3 | Cursor rejection cases defined | 6.4 (C1..C8) | none in #348 (its scope is safety, eligibility, progress); needs a cursor TCK once a reader exists | **Documented**, Proposed. Test pending (D8, D14) |
| 4 | Conditions of the `<= T` bound declared (dedicated cluster, timeout for all writer roles, no prepared transactions, old-XID alert) and what happens if broken | 7.3, SAFE-4, X7 | ELIGIBILITY: event readable within `T` after commit with a bounded long transaction | **Documented**, conditional on the mechanism (D4). Depends on ADR approval; not met |
| 5 | Portability rules: offset in destination backend, destination Tx, slices grouped into partitions, capabilities per adapter | 5 (L1..L5), 8 (K1, K5, K6), 9 | none yet (destination Tx tests are #373) | **Documented**. Capability enforcement is #384; depends on ADR approval; not met |
| 6 | Identity of the offset and of the applied mark | 8 (K2, K4), coherent with #362, #373, #374 | none in #348 | **Documented**. Pending #362/#373/#374 and D13 |
| 7 | `GetShardEvents` deprecated with a retirement plan | 10 | #348 marks the current adapter's omission check as known failure | Plan **Documented**; the deprecation itself is **Pending** (no code here, D11) |
| 8 | Single tenant: `OneScope(Unscoped())` valid and cursor binds it; `AllScopesInCell` privileged and distinct; omitting tenancy does not enable it | 4 (S1..S4), 6.4 C4, X2 | none in #348; coverage via #424 | **Documented**. Test pending |

Mapping of #348 checks to contract clauses (names provisional, to be reconciled when the #348 change merges):

| #348 check (from its live description) | Contract clause | Notes |
|---|---|---|
| SAFETY: `a@100`, `b@200`, `late@150` with two concurrent transactions; zero omissions; deterministic failure on the current PostgreSQL adapter, marked as a known failure | SAFE-1, SAFE-2 | The check is expected to fail today (section 3) |
| ELIGIBILITY: an event is readable within `T` after commit with a bounded long transaction; makes no application-latency promise | 7.3 | Conditional on declared conditions |
| PROGRESS: the cursor advances when the transaction is released, with a handler that never fails | PROG-1, PROG-2 | No latency promise |
| Runs also against `testkit` | 9.1 (declared properties are proven by the same cases on every adapter) | Memory adapter also fails the safety check if it keeps the timestamp cursor |
| Runner errors tested in I-22 | out of scope | PRD R-02 |

## 15. Evidence and unverified claims

**Verified on `b615449`:** every row of section 3 (read directly from the cited file and lines). The PRD, ADR and draft logical-slices document were read; the latter from `origin/feat/350-logical-slices` (PR #436, unchanged by this work).

**Unverified in this draft:**

- The scenario and property names of the #348 change (not available when this was written); section 14 is provisional.
- Whether any external implementer of `EventsStore` exists, which affects the severity of the stage 1 and stage 4 steps.
- That the eligibility conditions in 7.3 are sufficient for any mechanism: they are the conditions stated for the xid8 candidate and must be validated by Gate A.
- That the checkpoint compare-and-set of K3 is expressible in every destination backend; the destination capability matrix is #384.
- That `AllScopesInCell` is needed at all in V1 beyond SharedCell.
- No test, build or benchmark was run: the change is documentation. The repository's existing checks are the validation.
