# Journal cursor that never skips a committed event (#332) — design

Status: PROPOSED, revision 4 (sections 10-12 compare the horizon with per-shard
serialization on prototypes; the recommendation there is NOT a decision). This is a first step of tests and design: the
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
| D | Serialize writers per (scope, shard) (lock held to commit; position assigned under it) | Yes: position order = commit order | Candidate; prototyped and measured in section 10 (current recommendation, conditional) |
| E | **Transaction-id horizon** (section 4) | Yes | Candidate; compared with D in section 10 |
| F | `track_commit_timestamp` | No: not unique, not ordered w.r.t. visibility, needs a server setting | Rejected |

## 4. Design E: xid horizon (candidate)

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
| The horizon is cluster-wide (observed, motivated the serial tests; measured in 10.3) | all |

Still to do (blocked on the gates, nothing implemented):

1. Store-level: the conformance check is RED on both stores; it must turn green
   with the implementation. Add limit 0, several pages, restart, and the scope
   matrix (tenant A / B / `Unscoped()`).
2. Postgres integration against the real store with held `pgx` transactions
   (same scenarios as the spike and as `journal_compare_test.go`, through `WriteEvents`), plus a writer test
   asserting no xid before the advisory locks.
3. Runner: lag from event timestamps (deterministic clock).
4. Migration test: a database populated at the previous schema, then A, then B;
   offsets still resume; an old-style INSERT after B fails; precondition
   violations abort.
5. Evidence on the final HEAD and on integrated `develop` CI, separating
   build/vet of `inttest` from real execution and unit race from integration.

## 10. Horizon (E) vs per-shard serialization (D): prototypes and measurements

Naming in this section follows the code: **xid-horizon** = E, **shard-serialization** = D.

### 10.1 What was built, and what it proves

Two prototypes behind one interface (`journalVariant`) on SCRATCH tables
(`cmp_a*`, `cmp_b`), in `inttest/flows/eventstore/`:

- `journal_variants_test.go`: the two schemes.
  - xid-horizon: position `2^62 + xid8`, read below the xmin of the reading
    statement's own snapshot, advisory entity locks (sorted, length-prefixed
    keys) before the first xid-assigning statement.
  - shard-serialization: a persistent counter row per `(scope, shard)`; one
    `INSERT .. ON CONFLICT DO UPDATE .. RETURNING` is the lock AND the position
    assignment, taken before any row is inserted and held to commit/rollback;
    a multi-shard write takes all its counter rows in sorted `(scope, shard)`
    order first. No process-local counter: it is safe across processes and
    restarts, and a rollback hands its position to the next writer.
- `journal_compare_test.go`: the SAME correctness suite on both, with explicit
  coordination (parked transactions, observation of `pg_locks`, no sleeps to
  hide races): reader never advances past an in-flight write (holder commits
  and rolls back), groups never cut for limits 1..100, restart/resume with a new
  pool, scope and shard isolation, multi-shard writes without deadlock, plus
  two shard-serialization controls (unsorted lock order DOES deadlock, 40P01;
  positions are exactly 1..N across two processes with rollbacks).
- `internal/measure` (go-specs unit tests, no resources): percentiles and the
  stable lock order.

Both pass the whole suite (3 consecutive runs). **This demonstrates that each
SCHEME is sound. It does not show that the production store is corrected:** the
adapter (`persistence/postgres`) is unchanged, and the real conformance check
`LateVisibleEventsBehindACommittedOffsetAreDelivered` is still RED on testkit
and on PostgreSQL, as intended. Only the adapter plus that check going green is
evidence for #332.

### 10.2 Measurement setup (identical for both variants)

- PostgreSQL 17.6 in a Testcontainers container on Colima (Docker VM: 2 vCPU,
  ~1.9 GiB; host darwin/arm64). The benchmark starts its own container so
  durability is a parameter: `fsync=on` (default) and `fsync=off` (what the
  shared CI container uses). `synchronous_commit=on`, `shared_buffers=128MB`,
  `max_connections=300`. Poll floor (idle empty read, p50): 0.27 ms
  (serialization) / 0.21 ms (horizon).
- Load: 16 writer goroutines, one entity each, 3 events per transaction (one
  position group), a tight-loop reader (no sleep) per measured shard with batch
  limit 100, 5 s per run, **3 repetitions** (throughput is the mean, the range is
  min..max; percentiles pool the 3 runs). Load generator and database share the
  2 vCPUs, so absolute numbers are indicative; compare the variants, not the
  figures with other machines.
- Held transaction (scenarios 3-5) is **part of the experimental load**: it
  holds a transaction open 500 ms, pauses 100 ms and repeats (about 27 per
  run). Held transactions are an emulation of a slow transaction, not a
  workload anyone proposes.
- Scenarios 6-7 add an ARTIFICIAL 5 ms delay (a sleep) inside every writer
  transaction right before its commit, inside its locks, the same for both. It
  stretches the commit; it does not demonstrate how either scheme behaves under
  real synchronous replication, whose cost depends on the network, the
  standby and the commit settings, none of which were exercised.
- Every run drains its reader and checks, on the measured traffic itself,
  that nothing is skipped or duplicated and each entity is delivered in order;
  a violation fails the run. No violation occurred.
- Latency definitions. **Write latency** = begin to commit returned (lock waits
  included). **Lock wait** = the part spent taking entity advisory locks (xid) or
  the position counter row (serialization). **Eligibility delay** = commit
  returned to the moment the polling reader was handed the event: it contains
  the reader's own query time (the poll floor above), so values near 0.3-0.9 ms
  are polling, and anything beyond is waiting for the horizon. Waiting is
  reported separately for writers (lock wait) and readers (eligibility).

Command: `URD_JOURNAL_BENCH=1 URD_JOURNAL_BENCH_FSYNC=on|off go test
./flows/eventstore/ -run TestJournalBench -v` in `inttest` with
`DOCKER_HOST=unix://$HOME/.colima/default/docker.sock
TESTCONTAINERS_RYUK_DISABLED=true`; scenarios 6-7 also with
`URD_JOURNAL_BENCH_SCENARIOS=6,7`. Skipped (visibly) unless
`URD_JOURNAL_BENCH=1`.

### 10.3 Results, fsync=on (3 x 5 s)

| # | Scenario | Variant | tx/s (min..max) | write p50/p95/p99 | lock wait p50/p95/p99 | eligibility p50/p95/p99 |
|---|---|---|---|---|---|---|
| 1 | 1 hot shard | serialization | 874 (829..915) | 13.1 / 50.7 / 75.9 ms | 11.5 / 48.8 / 74.0 ms | 0.28 / 0.37 / 0.49 ms |
| 1 | 1 hot shard | horizon | 5263 (5238..5286) | 2.9 / 4.2 / 5.3 ms | 0.37 / 0.62 / 0.85 ms | 0.51 / 1.75 / 2.95 ms |
| 2 | 8 independent shards | serialization | 3729 (3724..3736) | 4.1 / 5.6 / 6.8 ms | 1.0 / 2.0 / 2.6 ms | 0.66 / 1.46 / 1.92 ms |
| 2 | 8 independent shards | horizon | 4157 (4097..4213) | 3.7 / 5.2 / 6.5 ms | 0.37 / 0.64 / 0.90 ms | 0.83 / 2.20 / 3.09 ms |
| 3 | held tx, SAME shard | serialization | **131** (120..145) | 16.4 / **567** / **603** ms | 14.1 / 562 / 600 ms | 0.31 / 1.41 / 3.05 ms |
| 3 | held tx, SAME shard | horizon | 4150 (3336..5077) | 3.5 / 6.3 / 8.7 ms | 0.38 / 0.78 / 1.28 ms | **251 / 475 / 494 ms** |
| 4 | held tx, OTHER shard | serialization | 873 (846..895) | 13.5 / 48.8 / 73.6 ms | 10.9 / 46.0 / 70.6 ms | 0.35 / 1.61 / 2.43 ms |
| 4 | held tx, OTHER shard | horizon | 5248 (5229..5287) | 2.9 / 4.1 / 5.2 ms | 0.36 / 0.59 / 0.81 ms | **251 / 475 / 495 ms** |
| 5 | held tx, OTHER DATABASE | serialization | 887 (842..921) | 13.0 / 49.5 / 73.6 ms | 11.4 / 47.8 / 71.9 ms | 0.28 / 0.38 / 0.51 ms |
| 5 | held tx, OTHER DATABASE | horizon | 5228 (5183..5277) | 2.9 / 4.2 / 5.2 ms | 0.37 / 0.63 / 0.84 ms | **254 / 476 / 494 ms** |
| 6 | hot shard, commit +5 ms | serialization | **118** (114..126) | 95 / 366 / 554 ms | 87 / 356 / 544 ms | 0.59 / 1.32 / 1.88 ms |
| 6 | hot shard, commit +5 ms | horizon | 2007 (1899..2183) | 7.7 / 9.9 / 10.8 ms | 0.34 / 0.76 / 1.05 ms | 0.34 / 1.83 / 9.75 ms |
| 7 | 8 shards, commit +5 ms | serialization | 1129 (1112..1154) | 13.7 / 17.2 / 19.0 ms | 5.5 / 7.5 / 8.1 ms | 0.71 / 1.58 / 2.49 ms |
| 7 | 8 shards, commit +5 ms | horizon | 1929 (1783..2035) | 8.0 / 10.0 / 12.1 ms | 0.27 / 0.55 / 0.91 ms | 0.69 / 1.81 / 2.95 ms |

Held-tx counts per run were 26-27 in every held scenario. Scenarios 6-7 ran in
a separate invocation with the same configuration (`fsync=on`). The raw
results and the exact configuration are versioned next to this design, in
`evidence/` (see `evidence/README.md`).

### 10.4 Results, fsync=off (3 x 5 s), same configuration

| # | Scenario | serialization tx/s | horizon tx/s | serialization eligibility p50/p99 | horizon eligibility p50/p99 |
|---|---|---|---|---|---|
| 1 | hot shard | 987 (962..1004) | 4073 (3449..4985) | 0.28 / 0.52 ms | 0.88 / 7.8 ms |
| 2 | 8 shards | 2839 (2257..3665) | 2793 (2754..2857) | 0.91 / 3.8 ms | 1.46 / 6.0 ms |
| 3 | held, same shard | **104** (101..107); write p95 580 ms | 3566 (3432..3690) | 0.39 / 7.7 ms | **262 / 494 ms** |
| 4 | held, other shard | 1018 | 3519 | 0.34 / 1.6 ms | **258 / 494 ms** |
| 5 | held, other database | 1011 | 3491 | 0.27 / 0.49 ms | **259 / 495 ms** |

Durability barely moves serialization on a hot shard (874 vs 987 tx/s): the
lock is held for about 1.1 ms of round trips, not for the flush. Run 2 with
fsync=off shows a wide range for serialization (2257..3665): the 2 vCPUs are
saturated, treat the 8-shard figure as noisy.

### 10.5 What the numbers say

1. **Where the cost lands is the real difference.**
   - serialization puts it on the COMMAND path of the same `(scope, shard)`.
     A hot shard is a queue: throughput is about 1 / (time the lock is held),
     here 874-987 tx/s with a ~1.1 ms hold, and 118 tx/s when 5 ms of commit
     latency is added (the lock includes the commit). No reader is blocked: the
     eligibility delay, measured from the moment a commit RETURNED, stays at the
     poll floor in every scenario. That is not the same as "events are always
     available promptly": a writer waiting for the shard lock cannot commit,
     so its events become available only after that wait. The wait shows up as
     write latency (p95 567 ms with a held transaction on the same shard), not
     as eligibility delay. From the point of view of one command the time until
     its events can be read is write latency plus eligibility under BOTH schemes;
     percentiles do not add, and this benchmark did not record that end-to-end
     distribution (command start to delivery), so it is not claimed here. With
     the 500 ms held transaction both schemes show a delay of the same order
     (about 0.5 s at p95): serialization pays it as writer wait, the horizon as
     reader wait.
   - horizon puts it on the READ path: writes stay at 2.9-5.3 ms p50 across all
     scenarios (2007 tx/s with +5 ms commit), and an event becomes eligible only
     after every older in-flight transaction ends. With the 500 ms held
     transaction the eligibility delay is p50 about 250 ms, p99 about 494 ms,
     i.e. bounded by the longest transaction in flight, whatever it is.
2. **Blast radius differs.**
   - serialization: a transaction of the same `(scope, shard)` stalls only
     writers of that `(scope, shard)` (scenario 3: -85% throughput, p95 write
     567 ms). A transaction on another shard, another tenant or another database
     changes nothing (scenarios 4-5 equal scenario 1). The waiter is always an
     Urd writer, and the lock holder is always an Urd transaction.
   - horizon: ANY transaction on the server delays EVERY projection of EVERY
     scope and database (scenarios 3, 4 and 5 are indistinguishable). The
     holder can be foreign to Urd (analytics, a migration, an application
     `idle in transaction`). The lag is bounded only by that transaction. It is
     a delay, never a loss (the suite and every run verify exactly-once
     delivery after release).
3. **Multitenancy.** serialization is per scope by construction (counter keyed
   by `(scope, shard)`; `TestJournalCompare_ScopeAndShardIsolation` shows a
   tenant-a transaction does not touch tenant b's writes or reads). horizon
   couples tenants: tenant a's slow transaction delays tenant b's projections
   (same test). It is a liveness coupling, not a data leak, but it cuts against
   the isolation goal of #23/#93.
4. **Positions.** serialization yields dense positions per `(scope, shard)`
   (rollbacks return theirs; checked across two processes) and has no XID bound.
   horizon positions are sparse and bounded by 2^62 (section 7.4).
5. **Deadlock safety is a property of the lock order, not luck.** An unsorted
   order deadlocks deterministically (40P01, one victim); the sorted order does
   not (`MultiShardWritesDoNotDeadlock`, both variants).
6. **Entity order.** horizon needs the lock-before-xid discipline (section 4);
   serialization gets it for free because the shard lock precedes everything.

### 10.6 Limits of this evidence

- Scratch tables, not the adapter. The adapter's writes carry more statements
  (revision upsert with row locks, `tenant_metadata`, conditional checks), so
  the serialization lock would be held LONGER there than the ~1.1 ms measured:
  the per-shard ceiling above is an upper bound for the current write path.
  Conversely the lock section was not optimized (rows are inserted one
  statement at a time; a pipelined batch or taking the counter last would
  shorten it). That variation was not measured.
- The number of shards is decided by the actor system (GoAkt), not by Urd, so
  the hot-shard ceiling cannot be translated into a deployment figure until the
  shard count and the commands-per-shard rate of a real workload are known.
- 2 vCPU VM shared by the generator and the database; 3 x 5 s per cell. The
  held-transaction scenarios emulate a slow transaction with a fixed 500 ms
  hold; real distributions differ. The +5 ms commit is an emulation, not a
  replica.
- Not covered: a runner behind a long page (reader cost), failover, replicas
  (the horizon needs reads from the primary), connection-pool exhaustion under
  a parked lock (a stalled writer pins a pooled connection while it waits).

### 10.7 Recommendation (not a decision; gates stay PENDING)

**shard-serialization is the main candidate**, because of its isolation, and
**xid-horizon** stays as the alternative. The choice is NOT made: it is pending
the measurement of serialization in the real adapter (next step), which must
include the adapter's current locks, multi-entity writes and multi-shard
writes, not only the single-entity scratch write measured here.

- Why: its worst case is bounded by things Urd controls (the duration of Urd's
  own transaction on the same `(scope, shard)`), it has no global horizon that
  blocks every reader at once (a slow writer still delays the availability of
  its own events and of the writers queued behind it, on that `(scope, shard)`
  only), it isolates scopes (which the multitenancy epic needs), its positions are dense
  and have no XID bound, and its failure is visible at the writer. The horizon's
  worst case is bounded by transactions Urd does not control, it delays every
  tenant at once, and its failure shows up as unexplained projection lag.
- Its price is real and must be accepted explicitly: a per-`(scope, shard)` write
  ceiling of roughly 1 / lock-hold time, and sensitivity to commit latency
  (118 tx/s with the artificial 5 ms commit delay on one shard). A deployment
  with few shards, a high command rate per shard and a slow commit (the real
  cost of replication or storage is not measured here) is the one that would
  regress.
- The condition: implement the lock section in the real adapter behind the same
  conformance check and measure it with the same harness, on the adapter's real
  write path: its current locks (the `events_store_revisions` row locks of
  conditional, unconditional and delete writes, taken after the counter rows),
  multi-entity batches in one shard, writes spanning several shards, and
  concurrent writers of both kinds. Compare with the 874-987 tx/s hot-shard
  figure here.
  If the adapter's ceiling is below what the real shard count and command rate
  need, choose the horizon, with `idle_in_transaction_session_timeout`, a
  horizon-age gauge and a documented dedicated-server requirement.

Migration under serialization (to confirm at the gate; differs from section 7):
no `2^62 + xid8` and no XID limit. The counter row of each `(scope, shard)` is
seeded with `MAX(journal_pos)` of that pair (legacy `journal_pos = timestamp`),
so every new position is strictly greater than every legacy row of the same
pair and a legacy offset keeps its meaning. The cutover, the NOT NULL fence
without column default, and the rollback-needs-reset consequence are unchanged
(7.2-7.3). The write order must be: counter rows (sorted by `(scope, shard)`),
then the existing revision-row locks (sorted by id), then the inserts.

### 10.8 Does not change

`JournalPosition` as an explicit type (section 6), the lag fix, and the rule that
the real regression stays red until the adapter is fixed, are the same under
either choice. Nothing here approves the SPI or the migration.

## 11. Gates

- Public-contract gate: PENDING (`JournalPosition`, SPI signatures, `OffsetStore`).
- Data-migration gate: PENDING (7.2 cutover, one-way door, rollback needs rebuild).
- Neither is assumed approved by this document.
