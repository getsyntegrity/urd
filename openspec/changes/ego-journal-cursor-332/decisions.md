# Decision record for design.md 14.4 (#332)

Status: PROPOSED RESOLUTIONS, awaiting the owners. Nothing here is decided by this document. Each entry states the
question, what the review found, the options, a recommendation, and who decides. The SPI stays not approved and
nothing is implemented. Items marked NEW were found in the second review round of `contract.md`.

| ID | Question | Recommendation | Decider | Blocks |
|---|---|---|---|---|
| D1 | SPI shape and names | Agree the semantics first (G1-G10, progress), then the shape | owners | public-contract gate |
| D2 | Write behavior change | Accept, after a caller audit | owners | slice 2 |
| D3 | Reason on `DeleteEvents` | Add `Reason` {Retention, Erasure}; floor advances for Retention only | owners | G8 |
| D4 | Cursor size | No limit until real tokens are measured | owners | cursor format |
| D5 | `ValidateAdvance` enforcement; progress and ownership | Rule plus conformance now; optional co-located validation; ownership with #93 | owners, #93 | progress |
| D6 | Journal id and generation | UUID at schema creation; generation advanced by an explicit restore step, plus the cursor-ahead-of-head safety net | owners | G7 |
| D7 | Data migration | Order: #93 scope, audits, backfill, offset mapping, cutover window | owners | migration gate |
| D8 | Which PostgreSQL strategy | Run the experiment; thresholds below proposed for the owners to set first | owners | adapter |
| D9 | Backlog metrics | Publication backlog required, consumer backlog optional | owners | G10 |
| D10 | Index issue | Open it now; independent of #332 | owner | none |
| D11 | #332 regression timing | DECIDED: enters with the implementation | owner | - |
| D12 | Delivery plan | Documentation review first, then slices in order | owners | all |
| D13 NEW | Shard of an entity fixed for life | Adopt; audit the engine; audit existing journals | owners | G5, migration |
| D14 NEW | Time-based start | Add an optional `TimePositioner`; file the existing unit defect separately | owners | rebuild-from |
| D15 NEW | Rollout of the retention check | Report-only first, enforce later; loss before cutover is undetectable | owners | G8 rollout |

## D1 SPI shape and names

Review found: the semantics are still moving (two review rounds changed G1, G3, G5, progress), so approving a
shape now would freeze names around guarantees that changed. Options: (a) approve the shape now; (b) approve the
semantics, defer names until the conformance suite is written against them. Recommendation: (b). The conformance
suite is the best test of whether the semantics are implementable, and it should exist before the types do.

## D2 Write behavior change (idempotent replay, `ErrIdentityConflict`, `ErrSequenceOrder`, `ErrShardMismatch`)

Review found: today unconditional writes skip duplicate identities whatever their content and accept any sequence
order. Callers (non-test): `internal/engine/eventsource/events_writer_actor.go` (conditional or unconditional by
configuration), `internal/engine/saga/saga_actor.go` (two `Unconditional()` writes), `migration/tenant_adoption.go`
(`ExpectGenesis`). Equality for replay compares payload, manifest, metadata and encryption fields and not
`Timestamp` (contract G1). Open edge: a retry after retention removed the committed events reports
`ErrSequenceOrder` for a write that did succeed.
Options for the edge: (a) accept it and require the retry window to be shorter than the retention delay;
(b) keep a bounded window of recent batch ids in the revision row. Recommendation: accept the change; take (a) now
and revisit with (b) only if a measured retry window approaches the retention delay. The audit must confirm that the
three callers retry the batch they hold, unchanged (G1 "retry the batch you hold").

## D3 `DeleteEvents` reason

Review found: retention and erasure both remove events, but a consumer must continue past erased events (it must not
process erased data) and must stop on retention loss. The current signature cannot tell them apart.
Options: (a) a `Reason` parameter; (b) separate methods; (c) treat every deletion as retention and let erasure stall
lagging consumers. Recommendation: (a). Under (c) an erasure would force every lagging projection to rebuild.

## D4 Cursor size

Review found: no token has been measured. A Postgres cursor is an estimated few tens of bytes. Recommendation: no
number; measure the real serialized tokens of the first adapter and of a Cassandra sketch before fixing any bound.
The `offsets_store` column is `bytea` without a bound in the meantime.

## D5 `ValidateAdvance` enforcement, progress and ownership

Review found: validation is a rule on consumers; a store cannot enforce it on a consumer that skips it. Options:
(a) rule plus conformance of the runtime; (b) co-located validation inside the commit when progress and journal share
a database; (c) a signed advance token required by `Commit`. Recommendation: (a) now, (b) as an optional capability,
(c) rejected as too heavy. The progress epoch is named apart from the journal generation. Ownership and fencing stay
with #93; `Commit` carries an optional fence.

## D6 Journal id and generation

Review found: a restore from backup restores the journal metadata too, so the old generation comes back with the old
data. Options: (a) an explicit restore step that advances the generation; (b) detect a restore automatically.
Recommendation: (a), plus the safety net that a cursor ahead of the published frontier is `ErrCursorInvalidated`;
(b) is not reliable on a plain Postgres.

## D7 Data migration

Review found, see `postgres-batch-publication.md` section 11: the backfill must preserve per-entity order (running
maximum of timestamp, ordered by sequence); the legacy offset mapping is conservative (it can redeliver, never skip);
offsets have no scope until #93; legacy offsets may be in milliseconds and map to the zero cursor, as they already
behaved; entities that span shards must be resolved first; loss before the cutover is undetectable.
Recommendation: order the work as (1) #93 gives progress its scope, (2) run the three audits, (3) backfill, (4) map
offsets, (5) cutover window with the one-way-door rollback (restore plus reset).

## D8 Which PostgreSQL strategy

Review found: the batch strategy now has a no-starvation argument per stream and across streams, but is neither
implemented nor measured. The experiment's decision rules must be fixed before it runs. Proposed thresholds, for the
owners to accept or change (these are proposals, not findings):

| Question | Proposed bar |
|---|---|
| Correctness | the conformance suite (C01-C23, P01-P05) and the stress pass with no omission, duplicate or deadlock; otherwise stop |
| Writer cost at 300 tx/s offered | write p95 within 2x of `current` |
| Delivery at 300 and 500 tx/s offered | start-to-delivery p95 below 100 ms with `tau = 5 ms` |
| Held publisher, same and other stream | writers unaffected; the stall confined to its stream |
| Recovery after a 10 s stall at 300 tx/s | backlog drained within 30 s at the chosen `N` |
| Cost per event against `current` | bytes and WAL each within 2x; dead tuples reported |

## D9 Backlog metrics

Recommendation: publication backlog (count and oldest age) required, because G2's condition cannot be checked
without it; consumer backlog optional, because it can be expensive and "unknown" is allowed.

## D10 Index issue

Recommendation: open `evidence/issue-draft-timestamp-index.md` as an issue now. It is independent of #332, states
that it speeds reads and does not fix it, and shows the control omitted more events. Creating an issue is the owner's
call; nothing was opened.

## D11 #332 regression timing

DECIDED by the owner on #338: it enters `develop` with the implementation that makes it pass; the experiments keep it
as an explicit expected failure; the production CI is not changed to accept the omission.

## D12 Delivery plan

Recommendation unchanged: documentation review, then types and the conformance suite behind the public-contract gate,
then the adapter and its migration behind the migration gate, then runner and offsets with #93.

## D13 (NEW) The shard of an entity is fixed for its lifetime

Review found: G5 says availability order is sequence order, but streams are per `(scope, shard)` and carry no order
between them. The shard is `ActorSystem().Partition(persistence id)` (`event_sourced_actor.go`), which depends on the
cluster's partition count; a change of that count would put later events of an entity in another stream, and a
consumer reading both could see `n+1` before `n`. The store records no shard per entity today (the revisions table
has none). Options: (a) fix the shard with the entity's first event and reject others (`ErrShardMismatch`); (b)
declare the partition count immutable and leave it unenforced. Recommendation: (a). It needs a column on the
revision row, an engine change to use the recorded shard, and the migration audit of entities that already span
shards.

## D14 (NEW) Time-based start

Review found: the engine offers `WithStartOffset`, `WithResetOffset` and `RebuildProjection(from time)`, and a cursor
with no time meaning cannot express them. Separately, they do not work as intended today: they convert the time with
`UnixMilli` while events are stamped with `UnixNano`, so the time is effectively ignored. Evidence: a throwaway test
on the in-memory store (not committed) wrote an event stamped in nanoseconds and read with a "from" one hour ago and
a "from" one year in the future, both converted with `UnixMilli`, and both returned the event. PostgreSQL applies the
same comparison (`timestamp > offset`) and was not run. Recommendation: add an optional `TimePositioner` with a
conservative contract (delivers every available event with `Timestamp >= t`, may include earlier ones), and file the
existing defect as its own issue with a committed failing test; do not fix it inside #332.

## D15 (NEW) Rollout of the retention check

Review found: `ErrCursorOutsideRetention` turns silent loss into a stopped consumer, and the janitor can delete
everything after a snapshot, so lagging consumers will hit it at once. Loss before the cutover cannot be detected (the
floor starts at zero). Options: (a) enforce from the first release; (b) report-only first (counter and log per
projection and stream), enforce after the owners have seen who is affected. Recommendation: (b), as an operational
switch of the adapter; the contract's guarantee holds only in enforcing mode.
