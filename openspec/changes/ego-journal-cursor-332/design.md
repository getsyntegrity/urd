# Journal cursor that never skips a committed event (#332) — design

Status: PROPOSED, revision 3. This is a first step of tests and design: the
definitive solution is NOT approved, and the public-contract and
data-migration gates are PENDING. The branch is not ready to merge: the
`ShardReads/LateVisibleEventsBehindACommittedOffsetAreDelivered` conformance
check is a known, deliberately red regression pending #332 (not skipped). The store
is NOT changed. Code so far, all test-only:

- `ShardReads/LateVisibleEventsBehindACommittedOffsetAreDelivered`
  (`persistence/conformance/events.go`): RED on the in-repo store and on real
  PostgreSQL 17.6 (both verified; it is the only failing check on each).
- `inttest/flows/eventstore/journal_horizon_test.go`: validates the horizon on
  real PostgreSQL against a scratch table, with held transactions. 10 cases,
  green, stable over 5 repetitions. It does not exercise the store.

Run the Postgres lane with a reachable Docker (here Colima:
`DOCKER_HOST=unix://$HOME/.colima/default/docker.sock TESTCONTAINERS_RYUK_DISABLED=true`).

Human gates: **data-migration gate PENDING** (new column, backfill, offset unit
change); **public-contract gate PENDING** (the meaning of an offset changes
without a signature change). Both need the owner before implementation.

## 1. Problem

`GetShardEvents` uses the event `timestamp` as the offset. The timestamp is
stamped by the actor BEFORE the INSERT commits, so it orders events by when
they were created, not by when they became visible. A slower transaction of
another entity on the same shard can become visible behind an offset a
consumer already committed:

1. `a@100`, `b@200` visible; consumer reads both, commits offset 200.
2. `late@200` and `late@150` become visible.
3. `offset > 200` returns nothing; a read from 0 returns all four.

#331 (#330) closed ties visible in ONE snapshot. It cannot close this: the
event is not in the snapshot yet. The defect predates #331 and also affects
legacy and single-tenant use.

## 2. Requirement

A cursor `c` and a read rule such that: if a read returned position `c`, every
event that is or will become visible later has a position `> c`. Equivalently,
the reader must never advance past a position that an in-flight transaction
could still publish.

## 3. Alternatives

| # | Alternative | Sound? | Verdict |
|---|---|---|---|
| A | Keep timestamp, read `offset - grace` and dedupe | No: no bound on commit delay | Rejected (#332 forbids an arbitrary window) |
| B | Auto-increment id as position | No: `nextval` is assigned before commit and is not transactional; ids also commit out of order | Rejected (#332) |
| C | Gap detection with a timeout (skip a hole after N seconds) | No: same unbounded delay, plus silent loss | Rejected |
| D | Serialize writers per shard (lock held to commit; position assigned under it) | Yes: position order = commit order | Sound but costs write parallelism per shard; hot shards queue behind each commit and the lock must be ordered against the revision locks |
| E | **Transaction-id horizon** (below) | Yes | **Recommended** |
| F | `track_commit_timestamp` | No: not unique, not ordered w.r.t. visibility, needs a server setting | Rejected |

## 4. Recommended design (E): xid horizon

PostgreSQL gives a sound visibility bound: in a snapshot, every transaction
with `xid < pg_snapshot_xmin(pg_current_snapshot())` has finished (committed
or aborted). Rows from those transactions are final; no row with a smaller xid
can appear later.

**Position.** New column `journal_pos BIGINT NOT NULL` on `events_store`,
with NO column default (see section 7: the missing default is the fence):

- new rows: the writer sets `journal_pos = 2^62 + pg_current_xact_id()::text::bigint`
  in its INSERT. `pg_current_xact_id()` is the xid8 of the TOP-LEVEL
  transaction (validated, also from inside a savepoint). Every event of one
  transaction therefore shares one position (validated, across entities);
- existing rows: `journal_pos = timestamp`, set by the explicit backfill of
  section 7. Bounds are checked there, not assumed.

What xid8 does and does not give: ids below the snapshot's xmin are finished
(committed or aborted); ids do NOT follow commit order. The design uses only
the first property, as the horizon, and delivers in position (xid) order.

**Read.** One statement, so the rows and the horizon come from the SAME
snapshot (`pg_current_snapshot()` inside the statement; validated in
`journal_horizon_test.go`), filtered and ordered by position:

- visible rows: `journal_pos > $offset AND journal_pos < BASE + pg_snapshot_xmin(pg_current_snapshot())`
  (legacy rows are always below the bound);
- `ORDER BY journal_pos, persistence_id, sequence_number`;
- the batch is extended to the end of the last group of equal `journal_pos`
  exactly as #331 does for timestamps. A group is now one writing transaction
  (one command's events), so the extension is bounded the same way.

`nextOffset = journal_pos` of the last returned row. `ShardOffsets` becomes
`MAX(journal_pos)` under the same horizon bound.

**Why it is sound.** Let a read return position `p`. Every row it returned has
`xid < xmin`, and every row not yet visible belongs to a transaction with
`xid >= xmin`, so its position is `>= BASE + xmin > p`. Aborted transactions
leave no row and no hole to wait for: the cursor is not dense. A later read
has a snapshot whose xmin is not smaller (xmin never goes backwards for a
reader that observed it).

**Per-entity order (demonstrated, and a hazard in today's code).** An entity's
events must still arrive in sequence order. xid order is not lock order: a
transaction that already holds an xid and then waits for an entity lock held
by a transaction with a larger xid commits its event with a LOWER position
than the earlier event of that entity. The test
`TestJournalHorizon_EntityOrder` reproduces it ("lock after xid") and shows
the fix ("lock before xid").

This is not hypothetical here: `writeUnconditional` upserts the revision row
of each id in sorted order, and the first upsert already assigns an xid
before the second id's lock is requested; `writeConditional` and
`DeleteEvents` take `SELECT ... FOR UPDATE`, which also assigns one. The rule
for every writer (conditional, unconditional, delete):

1. `pg_advisory_xact_lock(hashtextextended(<tenant, persistence_id>, 0))` for
   every id of the batch, in sorted order, BEFORE any statement that can
   assign an xid. Advisory locks assign none (validated with
   `pg_current_xact_id_if_assigned() IS NULL` after acquiring them).
2. Only then the revision lock, the inserts and the position.
3. The locks are held to commit. A hash collision only adds contention.

Guard: a Postgres integration test asserts the invariant on the real store,
and the store's write path checks `pg_current_xact_id_if_assigned() IS NULL`
right after step 1 in a test build, so a later refactor that moves a write
above the lock fails loudly.

Cost: a second lock per entity write and a sorted acquisition; deadlock
freedom comes from the sorted order, as it does for the revision rows today.

## 5. Cost and guarantees

- **Visibility lag, not loss.** The horizon is the oldest in-flight xid in the
  whole cluster (observed while writing the tests: a held transaction in one
  database delayed reads in another database of the same server; the spike
  tests had to run serially for that reason). An idle-in-transaction session or a long transaction of ANY
  application on that server holds every projection back. Mitigation:
  `idle_in_transaction_session_timeout`, and a horizon-age gauge. This is the
  real price of E and the main reason D stays documented as the fallback.
- **Storage.** +8 bytes per row and one index `(tenant_id, shard_number,
  journal_pos)` replacing the timestamp scan. The timestamp column stays (event
  time, lag metrics).
- **Batch size / `maxBufferSize`.** Unchanged in practice: the extension adds
  at most one transaction's events past the limit (as in #331).
- **Delivery.** At least once is preserved: a failure before the offset is
  committed re-reads the same rows.
- **Isolation.** Position space is global but every read still filters
  `tenant_id`; an offset of one scope reveals nothing of another.
- **Requirements.** PostgreSQL >= 13 (`pg_current_xact_id`,
  `pg_snapshot_xmin`), reads from the primary (a standby snapshot is not tied
  to the primary's in-flight transactions).
- **In-repo `testkit` store.** Position = a monotonic counter assigned under
  the store lock at write time; a write is visible atomically, so the same rule
  holds with no horizon. Same observable semantics (conformance).
- **Other adapters.** The conformance check becomes the acceptance test; an
  adapter must provide a visibility-safe position.

## 6. SPI and consumers

Owner preference (applied): an explicit type now, not a silent change of what
an `int64` means.

- New `persistence.JournalPosition`: an opaque, comparable position in the
  journal of one scope and shard. It carries no time meaning and offers no
  conversion to or from a timestamp. Zero value = before the first event.
  It exposes `Int64()` / `JournalPositionFromInt64` only for the persistence of
  progress, documented as storage, not arithmetic.
- `GetShardEvents(ctx, scope, shard, after JournalPosition, limit) ([]*egopb.Event, JournalPosition, error)`
  and `ShardOffsets(ctx, scope) (map[uint64]JournalPosition, error)`.
  **This is a breaking SPI change** (every implementer fails to compile, which
  is the intent: none can keep returning a timestamp by accident). It joins the
  public-contract gate.
- The event timestamp stays `egopb.Event.Timestamp`, event time only.
- `egopb.Offset.value` stays an `int64` on the wire and in `offsets_store`; it
  now holds a `JournalPosition`. Its proto comment changes; no wire change.
- `internal/projectionrunner/runner.go`: the lag gauge computes
  `now - currOffset` treating the offset as UnixNano. Under the new type it
  does not compile, by design. Lag must be `now - timestamp of the last
  event delivered` (and 0 when caught up); the runner keeps that timestamp in
  memory, no offset arithmetic. Covered by a runner unit test with the
  deterministic clock.
- `ResetOffset(name, value)`: `0` (rebuild) is unchanged; any other value is a
  position. Signature takes `JournalPosition` when `OffsetStore` is touched
  together with #93 (see section 8).
- External `EventsStore` implementers: behavioural break, caught by the new
  conformance check.

## 7. Migration (explicit; owner gate PENDING)

### 7.1 Transformation and bounds

- Legacy row: `journal_pos := timestamp`. New row: `journal_pos := 2^62 + xid8`.
- Preconditions, checked by the migration and aborting it on violation:
  1. every `events_store.timestamp` is in `[0, 2^62)` (a UnixNano stays below
     2^62 until the year 2116; a test fixture with a huge or negative stamp is
     a hard stop, not a silent overlap);
  2. `journal_pos` has no value outside `[0, 2^63)`; xid8 stays below `2^62`
     for any realistic cluster, enforced by `CHECK (journal_pos >= 0)`.
- Legacy rows therefore sort strictly below every new row, and their relative
  order is exactly today's `(timestamp, persistence_id, sequence_number)`.
  Validated by `TestJournalHorizon_LegacyRowsSortBelowNewOnes`.
- Existing offsets: unchanged value, same reading. An offset `T` (a timestamp)
  is now the position `T`: it resumes inside the legacy range and reaches
  every new row. No `offsets_store` rewrite. Validated by the same test.
- Mixing timestamps and xids is safe only because of the bound above AND the
  coordinated transition below. Without the transition it is not.

### 7.2 Coordinated writer transition (no rolling upgrade)

A fleet where an old writer and a new writer coexist has no ordering
guarantee: the old writer takes its revision row lock after its xid and does
not take the entity advisory lock (section 4). So the transition is a
cutover, fenced by the schema rather than by operator discipline:

1. **Migration A (compatible with the old binary).** Add
   `journal_pos BIGINT` NULL, no default. Old writers keep working and write
   NULL. Old readers keep working (timestamp offsets, the old defect).
2. **Cutover (maintenance window, short).** Stop every old writer and every
   old projection runner. Then, in one migration B:
   - run the precondition checks of 7.1;
   - `UPDATE events_store SET journal_pos = timestamp WHERE journal_pos IS NULL`
     in key-range batches (the writers are stopped, so no row is racing);
   - `ALTER ... SET NOT NULL` via a validated `CHECK (journal_pos IS NOT NULL)`
     first, then `NOT NULL`; `CHECK (journal_pos >= 0)`;
   - `CREATE INDEX CONCURRENTLY idx_events_store_journal ON events_store
     (tenant_id, shard_number, journal_pos)`.
3. **Start the new binaries.** They refuse to start when `journal_pos` is not
   `NOT NULL` (schema version check), so a new binary cannot run before the
   cutover.

**The fence.** There is no column default. After step 2 an old writer's
INSERT, which does not know the column, fails with a NOT NULL violation. An
old writer cannot silently produce an unordered row; it stops loudly. New
writers set the column explicitly.

**Rollback.** Before step 2: drop the column. After it: new rows carry xid
positions that an old reader would interpret as timestamps (and stall on), so
rollback means restoring the previous binary AND resetting offsets (rebuild).
This is a one-way door and the gate must say so.

### 7.3 Compatibility vs recovery

- **Compatibility of offsets: preserved** (7.1).
- **Recovery of events already skipped: possible by rebuild, detection is
  not.** An event that became visible behind a committed offset before the
  cutover is still in the journal (unless retention deleted it), so
  `ResetOffset(name, 0)` and a rebuild deliver it. What the current offset
  cannot tell is WHICH events were skipped, or whether a given projection was
  affected: the design offers no detector, so the safe statement is "every
  projection that ran before the cutover may have skipped events; rebuild
  those whose correctness matters". Events removed by `DeleteEvents` before a
  rebuild are gone for good and cannot be recovered. After the cutover no new
  omission can occur.

### 7.4 Limit of the XID (no overflow of the persisted type)

`journal_pos` is a signed `BIGINT` (max 2^63-1) and a new position is
`2^62 + xid8`, so it is representable only while `xid8 < 2^62`
(4 611 686 018 427 387 904). `xid8` is an unsigned 64-bit, epoch-extended
counter that never wraps; at one million write transactions per second it
reaches 2^62 after about 146 000 years, so the limit is theoretical, but the
behaviour at the limit is defined, not assumed:

- the position is computed with bigint arithmetic, and PostgreSQL raises
  `22003 numeric_value_out_of_range` on overflow instead of wrapping; the
  writer's transaction aborts, so no row with a wrapped (negative or
  colliding) position can be stored;
- the read bound `2^62 + pg_snapshot_xmin(...)` fails the same way, so a read
  stops loudly rather than returning a wrong horizon;
- `CHECK (journal_pos >= 0)` is a second fence on the stored value;
- the migration precondition (7.1) and an operational alert at xid8 >= 2^61
  (half of the margin) are part of the gate.

Validated in `TestJournalHorizon_PositionBoundIsEnforced`: the arithmetic at
the largest representable xid8 succeeds, one above fails with 22003 and the
failing transaction stores nothing; and `xid8::text::bigint` of the live
cluster is far below the limit.

## 8. Coordination with #93

- #93 keys `offsets_store` by scope (identity: who the progress belongs to).
  #332 changes what the value means (a position). The two touch different
  tables and do not conflict in schema, but both change how an offset is read:
  land them as separate migrations and state in #93 that `current_offset` is a
  position after #332.
- Position space is shared across tenants; per-scope isolation comes from the
  `tenant_id` filter, so #93's scoped offsets stay valid unchanged.
- #93 must not add its own offset unit or reset-to-time semantics.
- `JournalPosition` is the type #93's scoped offset identity stores. If #93
  lands first, its `offsets_store` key change must carry an `int64` that this
  design later retypes with no data change; if #332 lands first, #93 inherits
  the type. Either order needs only a signature change, no second migration.
  To be agreed with the #93 owner before either implementation starts.

## 9. Evidence status and test plan

Demonstrated on real PostgreSQL 17.6 (`journal_horizon_test.go`, scratch table,
held transactions, no sleeps to hide races; the only wait is for an advisory
lock request to appear in `pg_locks`):

| Property | Test |
|---|---|
| Inverted commit: the committed-but-late event is NOT delivered while an older xid is in flight, the horizon equals that xid, then both arrive in position order, once | InvertedCommit |
| Rollback leaves no hole and releases the horizon | RollbackReleasesTheHorizon |
| Several events (and entities) per transaction share one position; no group is cut for limits 1..100 | GroupsAreNeverCut |
| Restart/resume from the committed cursor, also across a late commit and from the middle of the stream | RestartResumesFromTheCursor |
| Legacy rows (position = timestamp) sort below new rows; an old offset keeps its meaning | LegacyRowsSortBelowNewOnes |
| Position is the top-level xid8, also inside a savepoint | PositionIsTheTopLevelXid |
| Position arithmetic at the xid limit: last representable value works, the next one aborts with 22003 and stores nothing | PositionBoundIsEnforced |
| Entity order breaks when the lock follows the xid, holds when it precedes it | EntityOrder |
| The horizon is cluster-wide (observed, motivated the serial tests) | all |

Still to do (blocked on the gates, nothing implemented):

1. Store-level: the conformance check is RED on both stores; it must turn green
   with the implementation. Add limit 0, several pages, restart, and the scope
   matrix (tenant A / B / `Unscoped()`).
2. Postgres integration against the real store with held `pgx` transactions
   (same scenarios as the spike, through `WriteEvents`), plus a writer test
   asserting no xid before the advisory locks.
3. Runner: lag from event timestamps (deterministic clock).
4. Migration test: a database populated at the previous schema, then A, then B;
   offsets still resume; an old-style INSERT after B fails; precondition
   violations abort.
5. Evidence on the final HEAD and on integrated `develop` CI, separating
   build/vet of `inttest` from real execution and unit race from integration.

## 10. Decision between horizon (E) and per-shard serialization (D)

Not decided. Evidence so far supports E as correct, with a measurable
operational cost (cluster-wide horizon). To choose with evidence, still
missing: (a) a measurement of write latency/throughput of D vs E on a hot shard
(E adds one advisory lock per entity; D serializes the shard until commit);
(b) the horizon lag under a realistic long-transaction workload and the effect
of `idle_in_transaction_session_timeout`. Both are comparisons to run on the
real store once both are prototyped behind the same conformance check.

## 11. Gates

- Public-contract gate: PENDING (`JournalPosition`, SPI signatures, `OffsetStore`).
- Data-migration gate: PENDING (7.2 cutover, one-way door, rollback needs rebuild).
- Neither is assumed approved by this document.
