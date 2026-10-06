# Journal cursor that never skips a committed event (#332) — design

Status: PROPOSED, revision 8 (owner review of the contract applied: write outcomes and idempotent retry, per-stream order and chains of reads,
enforced per-entity sequence rules, retention without partial deletion and detectable loss, three lag measures, cursor bound to the journal
instance and generation, progress with compare-and-set and generation; the SPI is NOT approved). Revision 7 stands: **Direction change (agreed with the owner): Urd defines a PORTABLE JOURNAL
CONTRACT; each adapter implements its guarantees with its own mechanisms. The earlier selection of per-write
serialization, and every later recommendation in this document (10.7, 12.8, 13.9), is SUSPENDED.** The experiments
below stay as evidence about three PostgreSQL strategies; none is the framework's mandatory architecture.

This is a proposal for review, not an approved design. The public-contract gate and the data-migration gate are PENDING and nothing here
approves them. The production path is unchanged. The `ShardReads/LateVisibleEventsBehindACommittedOffsetAreDelivered`
conformance check is a known, deliberately red regression against the current adapter (not skipped).

### Scope of this change: documentation only

This directory is the whole content of the change. **The code, tests and benchmarks cited in sections 1-13 are NOT
part of it and do not exist in `develop`.** They live on the local branch `exp/332-adapter-shard-serialization`
(`6d03c1d`, which also contains the two earlier commits `1aef84b` and `fb21b34`): the conformance check
`LateVisibleEventsBehindACommittedOffsetAreDelivered`, the scratch-table prototypes, the `journalexp` build-tag
adapter variants and the benchmark harness. The raw results those experiments produced are included here under
`evidence/`, so the numbers can be read without the code. When this document says a check is "red" it refers to
that branch; adding the check to `develop` while the adapter still fails it is a decision of its own (it would turn
CI red), listed in section 14.4.

### How to read this document set

| Question | Where |
|---|---|
| What must ANY adapter guarantee, and what is the cursor? | [`contract.md`](contract.md) (framework level) |
| Could Postgres, Oracle and Cassandra meet it, and what changes? | [`portability.md`](portability.md) |
| One concrete PostgreSQL strategy: post-commit batch publication | [`postgres-batch-publication.md`](postgres-batch-publication.md) |
| How is it tested, and what is the next experiment? | [`conformance-and-experiment.md`](conformance-and-experiment.md) |
| What was measured so far, with its limits? | sections 1-13 below and [`evidence/`](evidence/) |
| What is decided, suspended and pending? | section 14 |

### Evidence so far (kept as is)

Code, all test-only or behind the `journalexp` build tag on branch `exp/332-adapter-shard-serialization`
(`6d03c1d`, preserved untouched):

- `ShardReads/LateVisibleEventsBehindACommittedOffsetAreDelivered` (`persistence/conformance/events.go`, on that
  branch only): RED on the in-repo store and on real PostgreSQL 17.6.
- `inttest/flows/eventstore/journal_horizon_test.go`, `journal_compare_test.go`, `journal_bench_test.go`:
  scratch-table prototypes of the xid horizon and per-write serialization with a correctness suite and a benchmark.
- `persistence/postgres/event_store_journalexp*.go` and `inttest/flows/eventstore/journal_adapter_*_test.go`:
  the three variants on the REAL adapter, with the real conformance suite, a two-process stress, and a benchmark
  matrix of five adapters across saturated, offered-load and held-transaction scenarios.

Run the Postgres lane with a reachable Docker (here Colima:
`DOCKER_HOST=unix://$HOME/.colima/default/docker.sock TESTCONTAINERS_RYUK_DISABLED=true`).

Human gates: **data-migration gate PENDING**; **public-contract gate PENDING**. Both need the owner before any
implementation.

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

> **Revision 7: this is one ADAPTER STRATEGY for PostgreSQL, measured in sections 10-13. It is not the framework architecture and carries no recommendation.**

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

> **Revision 7: SUPERSEDED by `contract.md` (opaque `JournalPosition`, `Page`, `StreamHeads`, no `int64`, no `Compare`). The `JournalPosition`-over-`int64` sketch below predates the portability review and is kept for history.**

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

> **Revision 7: written for the xid-position mechanism. The cutover, fence and recovery-versus-compatibility reasoning carries over to any strategy; the transformation and bounds do not. A strategy-independent migration is a pending decision (section 14).**

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

> **Revision 7: SUSPENDED. The selection of serialization is withdrawn; see section 14.**

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

## 12. Serialization on the real adapter (experiment, branch `exp/332-adapter-shard-serialization`)

Authorized as an experiment only: nothing here enters the production path, no public signature changed, and the
SPI and migration decisions remain pending. The code is behind the `journalexp` build tag
(`persistence/postgres/event_store_journalexp.go`, tests in `inttest/flows/eventstore/journal_adapter_*_test.go`);
no tracked file of the production adapter was modified, and a build without the tag does not contain it.

### 12.1 What it is

`ExperimentalShardSerializedStore` implements the CURRENT `persistence.EventsStore` (signatures unchanged) with
the int64 "offset" of `GetShardEvents`/`ShardOffsets` meaning an internal journal position. Test-only schema,
applied by `ExperimentalMigrate`, not by the schema migrator: nullable `events_store.journal_pos`, an index
`(tenant_id, shard_number, journal_pos)` and `journal_shard_positions(tenant_id, shard_number, last)`.

Global lock acquisition order, the one that keeps the existing protocol deadlock-free with the new locks:

1. `events_store_revisions` rows, ordered by `persistence_id` within a scope (unchanged: `lockRevision` for a
   conditional write, the sorted upserts of an unconditional one, `lockRevisionIfExists` for `DeleteEvents`);
2. `journal_shard_positions` rows, ordered by shard within a scope, each taken with one
   `INSERT .. ON CONFLICT DO UPDATE .. RETURNING` (lock and position at once, held to commit or rollback);
3. event inserts, then commit.

No writer takes a revision row after a counter row and each class is taken in a total order, so no cycle can form.
Revision rows come first so that a conditional write whose precondition fails returns before touching a counter
and a conflict never serializes the shard. `DeleteEvents` takes only step 1: it creates no row, needs no
position, and is unchanged. A negative control for this order IN THE ADAPTER (a deliberately wrong order) was
not built; the prototype showed that an unsorted shard order deadlocks (section 10.1).

### 12.2 Correctness evidence (real adapter, real PostgreSQL 17.6, `-race`)

| Evidence | Result |
|---|---|
| The real conformance suite (`RunEventsStoreConformance`) against the experimental store | `ShardReads/LateVisibleEventsBehindACommittedOffsetAreDelivered` **PASSES** (it is RED on the current adapter). 3 checks fail: `GetShardEventsReturnsOnlyTheScopesEvents` (events.go:355), `ShardOffsetsCoverOnlyTheScopesShards` (546) and `UnscopedNeverReadsATenantNamedUnscoped` (578). All three assert that the offset IS a timestamp (e.g. expect 400 / 100 where the position is 2 / 1): they pin the meaning the SPI gate would change, they are not isolation failures. A failing check stops at its first assertion, so later assertions of those three checks were not exercised. |
| In-flight writer vs reader, conditional and unconditional holder, commit and rollback | nothing above an in-flight write is delivered; the late writer waits for the shard counter (observed in `pg_locks`); a rollback hands its position back (counter == committed writes) |
| Cancellation of a writer waiting for the counter | it fails with `context.Canceled`, leaks no revision lock (the same entity writes again with `ExpectGenesis`) and takes no position |
| Parked writer + writer of another entity + `DeleteEvents` of the parked entity | all complete after release: no lock cycle |
| Two scopes, same shard number | a parked write of tenant a does not block tenant b; each reads only its own events |
| Stress: two processes, conditional single-entity writers, unconditional multi-entity writers on overlapping shared entities, multi-shard batches listing shards in opposite orders, every 7th write of one process rolled back, readers polling by position throughout | no write failed (no 40P01), every committed event delivered exactly once, nothing uncommitted delivered, positions dense per shard (distinct positions == counter), per-entity order kept for owned entities. 2 runs, ~1620 committed events and ~56 rollbacks each |

The benchmark repeats the omission check on every measured run: `serialized` omitted 0 events in all 24 runs.

### 12.3 Measurement setup

Same harness, same load, driven through the public `EventsStore` of each adapter (`WriteEvents` with its real
preconditions, `GetShardEvents`): PostgreSQL 17.6 in Testcontainers on Colima (2 vCPU VM), own container,
`fsync=on` and `fsync=off`; 12 writers; 5 s per run; 3 repetitions (throughput is the mean with min..max,
percentiles pool the repetitions); a tight-loop reader per measured shard, batch limit 100. The adapter opens a
fixed pool of 20 connections shared by writers and readers. Event timestamps are taken just before each write
starts, as the actor does. Three adapters:

- `current`: the adapter as it is, reading by timestamp offset;
- `current+index`: the same plus a composite read index over the timestamp, a CONTROL: the experimental store
  carries its own composite index, and without this control part of `current`'s read lag is the missing index;
- `serialized`: the experiment.

Workloads: W1 conditional (`ExpectGenesis`/`ExpectRevision`), one owned entity per writer, 3 events per
transaction, one hot shard; W2 unconditional batch over 3 owned entities, hot shard; W3 unconditional batches
spanning 2 of 8 shards, listed in opposite orders by alternate writers; W4 unconditional batches over 3 of 12
SHARED entities, hot shard (overlapping revision rows). Held transactions and artificial commit delays were not
re-run on the adapter; the prototype measured them (section 10).

"Start-to-delivery" is from the START of the write call to the moment the polling reader was handed the event, so
it includes the write, every lock wait and the reader's query time. "Omitted" is an event whose write returned
success and that the polling reader was never handed; each omitted event was then verified to be returned by a
read from zero (a rebuild), so none is lost from the journal. Raw output: `evidence/adapter_fsync_on.txt`,
`evidence/adapter_fsync_off.txt`.

### 12.4 Results, fsync=on (3 x 5 s, 12 writers)

| Workload | Adapter | tx/s (min..max) | write p50 / p95 / p99 | start-to-delivery p50 / p95 / p99 | omitted events |
|---|---|---|---|---|---|
| W1 conditional, hot shard | current | 2520 (2031..3024) | 4.3 / 8.4 / 12.0 ms | 17.9 / 158 / 250 ms | 9.9 % |
| | current+index | 2756 (2293..3052) | 4.1 / 6.8 / 9.2 ms | 4.9 / 10.8 / 15.7 ms | 24.0 % |
| | serialized | **661** (557..720) | 13.0 / 50.9 / 76.3 ms | 13.2 / 51.3 / 76.6 ms | **0** |
| W2 multi-entity, hot shard | current | 2749 (2701..2774) | 4.1 / 6.7 / 8.5 ms | 13.1 / 46.3 / 121 ms | 9.7 % |
| | current+index | 2838 (2740..2928) | 4.0 / 6.1 / 8.1 ms | 5.4 / 21.1 / 52.4 ms | 17.3 % |
| | serialized | **812** (767..859) | 10.4 / 42.0 / 64.9 ms | 10.7 / 42.2 / 65.2 ms | **0** |
| W3 multi-shard, 8 shards | current | 2382 (2320..2427) | 4.9 / 6.9 / 8.6 ms | 7.2 / 13.7 / 19.3 ms | 5.4 % |
| | current+index | 2442 (2301..2549) | 4.6 / 7.4 / 9.4 ms | 5.7 / 9.0 / 11.4 ms | 8.3 % |
| | serialized | **1273** (1249..1305) | 8.0 / 20.0 / 31.1 ms | 8.4 / 20.5 / 31.7 ms | **0** |
| W4 overlapping entities, hot shard | current | 970 (963..979) | 6.1 / 46.3 / 83.6 ms | 4.8 / 8.7 / 10.8 ms | 51.8 % |
| | current+index | 1051 (1037..1070) | 5.6 / 42.4 / 80.6 ms | 3.6 / 6.4 / 8.4 ms | 53.5 % |
| | serialized | **751** (746..759) | 7.8 / 59.3 / 112.8 ms | 8.1 / 59.7 / 113.1 ms | **0** |

fsync=off, same configuration (tx/s current / current+index / serialized): W1 2658 / 3027 / 690; W2 2747 / 2672 /
739; W3 2644 / 2561 / 1164; W4 961 / 999 / 714. Omitted: current 8.3 / 8.4 / 6.9 / 51.3 %, current+index 20.5 /
24.6 / 8.5 / 52.0 %, serialized 0 in every workload. Durability moves nothing materially.

### 12.5 What the numbers say

1. **The defect is large and measurable in the current adapter, and it is recoverable only by rebuild.** Under
   this concurrency the timestamp cursor omitted 5-54 % of the committed events (all recoverable by a read from
   zero, none lost from the journal). The rate grows with writer concurrency and with the reader's speed: with
   the control index the reader is faster, stays closer to the head, and omits MORE (24 % vs 10 % in W1). This
   is an extreme, closed-loop load (12 writers saturating a shard with a reader polling without pause); it
   shows that the mechanism is easy to trigger, not what a production deployment omits.
2. **Serialization removes every omission (0 of about 270k events across the 24 serialized runs) at a high throughput
   cost on a single shard.** Against `current+index`, the fair control: W1 -76 %, W2 -71 %, W3 -48 %, W4 -29 %;
   against `current`: -74 / -70 / -47 / -23 %. Write p50 goes from about 4 ms to 10-13 ms and p95 from 6-8 ms to
   42-51 ms (W1, W2). The counter wait is most of it (W1: 11.0 ms of the 13.0 ms p50): the writers queue on the
   shard. The lock is held about 1.5 ms per transaction (1 / 661 tx/s), longer than the 1.1 ms of the scratch
   prototype, because the real write path inside it has more statements.
3. **Where the contention already lives.** W4 is dominated by the existing revision-row protocol: the counter
   wait is 0.75 ms p50 but write p95 is 59 ms for `serialized` and 42-46 ms for `current`: the same order,
   so the new lock adds little there.
4. **Delivery latency is not made worse by the scheme itself, the write is.** For `serialized`, start-to-delivery
   equals write latency plus about 0.3 ms: the reader never waits. Compared with `current+index`
   the extra delay is the queueing of the writers.
5. **The `current` adapter's own read lag is mostly the missing index**, not the scheme: its W1 delivery p95 is
   158 ms and drops to 11 ms with the control index. That index (or the experimental one) is a separate,
   cheap improvement independent of #332, and is worth its own issue.

### 12.6 Impact of the topology: how many shards does Urd have?

`event.Shard` is `ActorSystem().Partition(persistenceID)`, and in GoAkt that is `0` for every actor when the
system is NOT in a cluster (`actor_system.go`: `if x.InCluster() { return cluster.GetPartition(name) }
return 0`). So:

- a single-node deployment has ONE shard per scope, i.e. the "hot shard" workloads W1, W2, W4 are its normal
  case, and the serialization ceiling (about 660-810 tx/s here) bounds the whole tenant's write rate;
- in a cluster the shard is the cluster partition; the internal cluster config of GoAkt carries `shardCount:
  271` (`internal/cluster/config.go`), and the repository's own cluster example sets `WithPartitionCount(4)`.
  I did not verify that `Partition()` uses that default; the count depends on configuration, and a tenant's
  events spread over the partitions its entities hash to. W3 (8 shards, -48 %) is the more favorable shape.

The ceilings above are closed-loop saturation figures on a 2 vCPU VM. At a lower offered rate the queueing,
hence p50/p95, would be much smaller; that open-loop behavior was not measured.

### 12.7 Limits of this evidence

- One variant of one scheme on the adapter. The xid horizon was NOT built in the adapter, so the two schemes
  were not compared like-for-like on the real write path; the horizon's adapter cost is unmeasured (the
  scratch prototype put it near the unprotected writer: 5263 vs 874 tx/s on one shard).
- The lock section was not optimized. Taking the counter LAST (insert rows without position, take the counter,
  then set the position of this transaction's rows), a multi-row insert, or fewer round trips would shorten the
  time the shard lock is held and raise the ceiling. Not measured.
- Test schema with a nullable column and no backfill; no migration, no legacy rows, no `NOT NULL` fence.
- Held transactions on the adapter, replicas and real replication, long reader pages, pool exhaustion under a
  parked lock (the adapter's pool is a fixed 20): not covered. The 3 conformance checks that assume a timestamp
  offset were not rewritten.

### 12.8 Impact and recommendation (not a decision)

> **Revision 7: SUSPENDED, as 10.7. The measurements above stay.**

- **Correctness is settled for this scheme**: the real conformance regression passes and the stress and the
  benchmark found no omission, duplicate or deadlock. The cost is what remains in question.
- **Cost, for the default topology, is large**: single-shard write throughput falls by roughly three quarters.
  That moves the evidence: in section 10.7 serialization was the main candidate for its isolation; on the
  real adapter its price lands exactly on the single-node topology that is Urd's default. I no longer recommend
  adopting it as is.
- **Recommendation**: keep both candidates open and run two more experiments on the same harness before the
  gates: (1) the horizon in the adapter, to compare like-for-like with `serialized` and `current+index`;
  (2) serialization with the shortened lock section (counter last), to find out how much of the ceiling is
  recoverable. Decide with the target workload in hand (commands per second per scope, clustered or not),
  since that, not the scheme, sets which side of the ceiling a deployment sits on. Independently, open an
  issue for the timestamp read index: it explains most of the current adapter's read lag at no semantic risk.
- Pending, unchanged: the SPI gate (`JournalPosition`; the 3 failing checks above are the exact places where the
  meaning of the offset is pinned), the data-migration gate, and the coordination with #93.

## 13. Horizon on the real adapter, counter-last serialization, operating load (same branch)

Still an experiment behind `journalexp`; nothing here is on the production path, no public signature changed,
and the SPI, migration and scheme decisions stay pending.

### 13.1 What was added

- `ExperimentalHorizonStore` (`event_store_journalexp_horizon.go`): position `2^62 + xid8`, reads below the xmin
  of the reading statement's own snapshot. Acquisition order: advisory entity locks first, sorted by
  `(tenant, persistence_id)` and length-prefixed, BEFORE any statement that can assign a transaction id (the
  adapter's revision row locks do assign one); then the existing revision locks, the inserts and the commit.
  Tests turn on `AssertNoXidBeforeLocks`, which fails a write if an xid exists right after its locks.
  `DeleteEvents` is unchanged.
- Serialization with the counter taken LAST (`CounterLast`, variant `serialized-late`): rows are inserted with a
  NULL position, then the counter rows are taken (lock and position at once, held to commit or rollback) and the
  positions of this transaction's rows are stamped by one `UPDATE .. FROM unnest(..)`, restricted to rows whose
  position is still NULL so an ignored duplicate keeps its old one.
- One interface (`ExperimentalStore`) and one factory, so the same tests and the same benchmark drive every
  variant.

### 13.2 Correctness evidence (real adapter, real PostgreSQL 17.6, `-race`, 3 consecutive runs)

The same tests run on `serialized`, `serialized-late` and `horizon`; where the schemes differ on purpose the
expectation is stated per variant.

| Evidence | Result |
|---|---|
| Real conformance suite against each variant | `LateVisibleEventsBehindACommittedOffsetAreDelivered` **passes** on all three (red on the current adapter). The same three checks that assert a timestamp offset fail on all three, pinned as expected; any other change fails the test |
| In-flight writer vs reader, conditional and unconditional holder, commit and rollback | no variant delivers anything above an in-flight write. Under serialization the later writer waits (observed in `pg_locks`); under the horizon it COMMITS first (an inverted commit) and is delivered after the holder; a rollback leaves no hole in either |
| Cancellation of a waiting writer | fails with `context.Canceled`, leaks no lock (the same write then succeeds with the same precondition), commits nothing |
| Parked writer + writer of another entity + `DeleteEvents` | all finish after release, in every variant |
| Another scope in flight | a tenant-b write never waits in any variant. Reads differ: tenant b reads at once under serialization and waits for tenant a's transaction under the horizon |
| A transaction in ANOTHER DATABASE of the server | holds the horizon reader back and releases it at its end (delayed, not lost); it does not touch serialization |
| Restart/resume | a new process writes after the restart, positions keep growing, resuming from the committed cursor loses nothing and does not cut a group (all variants) |
| Counter-last demonstration | a writer parked AFTER its rows are inserted and BEFORE it takes the counter; another writer takes the counter, commits and is delivered; the first resumes and gets a position above the cursor the reader already reached; its uncommitted rows were invisible meanwhile; no committed row ever has a NULL position |
| Stress: two processes, conditional single-entity writers, unconditional multi-entity writers on overlapping shared entities, multi-shard batches in opposite shard order, every 7th write rolled back, readers polling | per variant: ~1630 committed events, 57 rollbacks, no failed write (no `40P01`), no omission, no duplicate, no uncommitted event delivered, no NULL position, per-entity order kept, positions dense per shard under serialization |

The benchmark also checks omissions on every run: no experimental variant omitted a single event in any of its
33 measured runs (11 scenarios x 3 repetitions each; 99 runs over the three experimental variants).

**What this does and does not show about deadlocks.** The stable acquisition orders (entity advisory locks, then
revision rows, then counter rows, each class ascending) remove cycles between THESE paths: conditional,
unconditional, multi-entity, multi-shard and delete writes against each other and against the reader. No
deadlock appeared in the stress or in any benchmark run, and the prototype showed that a wrong order does
deadlock. That is evidence about these routes under these loads; it is not a proof that no other interleaving,
statement or future code path can deadlock.

### 13.3 Measurement setup

Same harness, same load, through the public `EventsStore` of each adapter; PostgreSQL 17.6 in Testcontainers on
Colima (2 vCPU VM), `fsync=on` (it moved nothing earlier, section 12.4); 12 writers; 5 s per run; 3 repetitions;
a tight-loop reader per measured shard, limit 100; the adapter's fixed pool of 20 connections. Five adapters:
`current`; `current+index` (control: a composite index over the timestamp, equivalent to the one the
experimental variants carry, with the same extra write cost); `serialized`; `serialized-late`; `horizon`.

Scenarios: W1-W4 at saturation (as in 12.3); R100, R300, R500: W1 at an OFFERED rate of 100/300/500 tx/s (open
loop: each writer follows a fixed schedule and latency is measured from the intended start, so a queue is not
hidden); R500M: the 8-shard multi-shard workload at 500 tx/s; H1-H3: W1 at 300 tx/s with a transaction held
500 ms (100 ms gap, repeated, 9 per 5 s run, 26-27 over the 3 repetitions) in the same shard, in another scope, or in another database of
the server. The held transaction is part of the experimental load: the experimental variants hold a real adapter
write parked before its commit; the current adapter has no hook, so its holder is a raw transaction inserting
the same revision and event rows. Start-to-delivery is from the intended start of the write to the moment the
polling reader was handed the event. The omission percentages are those of THIS experimental load (a reader that
polls without pause, saturated or paced writers): they show how easily the mechanism triggers, not what a
deployment would omit. Raw output and caveats: `evidence/adapter_matrix_fsync_on.txt`, `evidence/README.md`.

### 13.4 Results at saturation (tx/s, min..max; write and start-to-delivery in ms p50/p95/p99)

| | current | current+index | serialized | serialized-late | horizon |
|---|---|---|---|---|---|
| **W1** conditional, hot shard: tx/s | 3430 (3345..3516) | 3443 | 736 (719..771) | 719 | 3080 (3013..3144) |
| write | 3.3 / 5.5 / 7.2 | 3.2 / 5.4 / 7.3 | 11.9 / 44 / 68 | 10.9 / 49 / 78 | 3.6 / 5.9 / 7.9 |
| start-to-delivery | 21 / 1128 / 1313 | 23 / 290 / 406 | 12.6 / 45 / 69 | 11.9 / 50 / 80 | 17 / 201 / 289 |
| omitted | 6.2 % | 10.4 % | 0 | 0 | 0 |
| **W2** multi-entity, hot shard: tx/s | 2683 | 2941 | 793 | 672 | 2023 (1461..2314) |
| start-to-delivery | 16 / 142 / 366 | 8 / 113 / 239 | 11 / 44 / 71 | 13 / 53 / 88 | 13 / 27 / 44 |
| omitted | 7.4 % | 18.0 % | 0 | 0 | 0 |
| **W3** multi-shard (8): tx/s | 2175 (1621..2552) | 1682 (1305..2321) | 1093 (815..1238) | 603 | 1823 (1254..2146) |
| start-to-delivery | 8 / 17 / 23 | 9 / 21 / 37 | 9 / 27 / 47 | 15 / 51 / 98 | 10 / 18 / 22 |
| omitted | 7.0 % | 9.3 % | 0 | 0 | 0 |
| **W4** overlapping entities: tx/s | 1005 | 818 | 786 | 457 | 708 |
| start-to-delivery | 4.8 / 9 / 12 | 4.9 / 9.6 / 13.6 | 8 / 57 / 108 | 13 / 97 / 193 | 11 / 51 / 65 |
| omitted | 52.0 % | 53.6 % | 0 | 0 | 0 |

Run-to-run variation is large for `current` (W1 2520..3430 across the two valid runs of section 12 and here), so
deltas below roughly 30 % are within the noise of this VM. Omission rates of the current adapter also vary
widely between runs (W1 6-25 %).

### 13.5 Results below saturation (offered load, 3 x 5 s; start-to-delivery p50 / p95 / p99 ms; omitted)

| Scenario | current | current+index | serialized | serialized-late | horizon |
|---|---|---|---|---|---|
| R100 (100 tx/s) | 1.7 / 2.4 / 3.1; 0 | 1.6 / 2.9 / 5.8; 0 | 1.7 / 2.4 / 4.0; 0 | 2.1 / 2.8 / 4.2; 0 | 1.8 / 2.5 / 3.1; 0 |
| R300 (300 tx/s) | 2.4 / 3.5 / 4.1; **0.11 %** | 1.7 / 2.6 / 4.3; **0.18 %** | 1.8 / 2.4 / 3.7; 0 | 2.4 / 3.3 / 5.3; 0 | 1.9 / 3.4 / 4.4; 0 |
| R500 (500 tx/s) | 2.8 / 4.9 / 7.2; **0.51 %** | 1.7 / 2.9 / 4.5; **0.29 %** | 2.1 / **34 / 86**; 0 | 2.5 / 4.2 / 8.6; 0 | 2.5 / 4.8 / 8.4; 0 |
| R500M (8 shards, 500 tx/s) | 2.9 / 4.1 / 6.0; **0.30 %** | 4.3 / 7.4 / 13.8; **0.53 %** | 4.1 / 6.9 / 20.5; 0 | 5.6 / **743 / 2260** (457 tx/s); 0 | 3.5 / 5.2 / 12.5; 0 |

1. **Up to 300 tx/s offered on a hot shard, every variant is indistinguishable within a few milliseconds** on
   this VM: the cost of serialization is not visible in ordinary operation at that rate.
2. **The defect is already present at moderate load in this load: 0.1-0.5 % of the events were omitted by the
   current adapter at 300-500 tx/s** (none at 100 tx/s), with and without the control index. A fraction of a
   percent is small, and it is silent loss of projection input.
3. **Serialization starts to queue before it saturates.** At 500 tx/s offered, which is 68 % of its ~735 tx/s
   ceiling, `serialized` already shows write p95 16 ms and delivery p95 34 ms / p99 86 ms.
4. `serialized-late` is better than `serialized` on one hot shard at 500 tx/s (p95 4.2 ms) and clearly worse on
   multi-shard (R500M: 457 of 498 tx/s achieved, delivery p99 2.3 s).

### 13.6 Held transactions at 300 tx/s offered (start-to-delivery p50 / p95 / p99 ms; achieved tx/s)

| Held transaction in | current | current+index | serialized | serialized-late | horizon |
|---|---|---|---|---|---|
| H1 the SAME shard | 1.7 / 2.7 / 3.3 | 1.6 / 2.0 / 2.8 | **1570 / 2951 / 3195**; 124 tx/s | **1267 / 2329 / 2809**; 175 tx/s | **214 / 475 / 497**; 298 tx/s |
| H2 ANOTHER SCOPE | 1.7 / 2.4 / 3.1 | 1.6 / 2.7 / 4.3 | 1.9 / 2.5 / 3.5 | 2.3 / 3.0 / 4.3 | **214 / 475 / 497** |
| H3 ANOTHER DATABASE | 1.7 / 3.0 / 4.8 | 1.6 / 2.2 / 2.9 | 2.0 / 3.2 / 13.5 | 2.6 / 4.1 / 7.4 | **214 / 475 / 497** |

- **Horizon**: writes are never affected (p95 3-4 ms in H1-H3), but the start-to-delivery latency is the same in
  all three held scenarios: about 214 ms p50, 475 ms p95, 497 ms p99. Whether the held transaction belongs to the
  same shard, another scope or another database makes no difference. The figures are set by the 500 ms hold and
  the 100 ms gap I chose (a transaction is open about 5/6 of the time): they show the mechanism, not a duration
  to expect in production, where it equals the age of the oldest open transaction of the whole server.
- **Serialization**: a held transaction in ANOTHER scope or ANOTHER database costs nothing measurable. A held
  transaction in the SAME (scope, shard) is the failure case: the achieved rate drops to 124-175 of 300 tx/s
  and delivery goes to seconds, a backlog that keeps growing while the transaction recurs. The holder here is
  one of Urd's own writes parked for 500 ms, which an Urd command should not normally be.
- The current adapter shows no effect from held transactions on its writers (it takes no shared lock), but
  it keeps omitting events (0.1-0.3 %).

### 13.7 Reading across the evidence

- **The two schemes fail in opposite places.** Horizon: negligible write overhead (-10 % at W1 saturation;
  -30 % in W2, within noise), no per-scope ceiling, its cost is read latency bounded by the oldest open
  transaction of the WHOLE SERVER, across scopes and databases. Serialization: a ceiling of about 735 tx/s per
  `(scope, shard)` on this VM (-79 % at W1 saturation against `current+index`), no cost at moderate load, no
  cross-scope or cross-database effect, and a collapse when something holds the same `(scope, shard)`.
- **Counter-last did not deliver what it promised.** At saturation it is equal or worse than counter-first
  (W1 719 vs 736, W2 672 vs 793, W3 603 vs 1093, W4 457 vs 786), and much worse on multi-shard under paced load.
  It is better only on one hot shard at 500 tx/s offered. Moving the inserts out of the lock saved little
  because the stamping statement and the extra round trip take about as long under the lock. I checked that the
  stamping statement uses the primary-key index (a nested loop, about 0.3 ms on a 200k-row table), so it is not
  a bad plan; what else costs time in the multi-shard case I did not isolate. I do not recommend it as is.
- **The index does not fix #332.** `current+index` is faster to read (W1 start-to-delivery p95 290 ms vs
  1128 ms) and omits MORE (10-18 % vs 6-7 % in W1/W2), because a faster reader reaches the head sooner.
  `evidence/issue-draft-timestamp-index.md` is the draft of a separate issue for it.
- **Single-node default.** `Partition()` is `0` outside a cluster (section 12.6): one shard per scope.
  Serialization's ceiling then bounds each scope's write rate; the horizon has none per scope but couples scopes.

### 13.8 Limits of this evidence

- 2 vCPU VM shared by generator and database; host load not zero (see `evidence/README.md`); a first matrix run
  was discarded for host load; run-to-run variation of `current` reaches 30 %; held-transaction durations and
  duty cycle are chosen, not measured from a workload.
- No real replica, replication or failover; no long reader pages; no pool exhaustion study (the adapter's pool
  is fixed at 20 connections and a writer waiting for a lock holds one).
- The unknown that decides the choice is the target workload: commands per second per scope, clustered or not,
  and whether Urd shares its PostgreSQL server with other workloads that hold long transactions.
- Test schema (nullable column, no backfill, no `NOT NULL` fence): no migration is evaluated here. The three
  conformance checks that assume a timestamp offset were not rewritten.
- The 0.1-0.5 % omission at moderate load, and the 6-54 % at saturation, belong to this experimental load.

### 13.9 Recommendation (not a decision)

> **Revision 7: SUSPENDED. Its two operating questions are still the right ones for choosing among PostgreSQL strategies, but the framework no longer chooses a mechanism; see section 14.**

I withdraw the preference for serialization stated in 10.7 and refined in 12.8: with the horizon measured on the
same write path, neither scheme dominates, and the choice reduces to two operating facts that Urd's owners must
state before the gates:

1. **Will Urd share its PostgreSQL server with workloads that can hold a transaction open for long** (analytics,
   migrations, other applications)? If yes, the horizon turns that into projection lag for every scope; use
   serialization, or the horizon only with `idle_in_transaction_session_timeout` plus a horizon-age gauge as a
   hard operating requirement.
2. **Does any `(scope, shard)` need more than about 700 commands/s** (the ceiling measured here, on a VM, for the
   current write path)? If yes, serialization cannot serve it; use the horizon.

If neither is known, serialization is the safer failure (a visible, local backpressure on the writer that
caused it) as long as the per-shard rate stays well under the ceiling (the cost already shows at about 70 % of
it); the horizon is the safer choice for throughput and the riskier for tail latency. Counter-last serialization
should not be pursued further without a hypothesis for the multi-shard result. Pending, unchanged: the SPI gate
(`JournalPosition`; the three failing checks mark exactly where the meaning of the offset is pinned), the
data-migration gate and the coordination with #93.

## 14. Revision 7: contract, strategies and pending decisions

### 14.1 Decided in this revision (direction, not code)

- Urd defines a **portable journal contract** (`contract.md`). Adapters implement it with their own mechanisms.
- The **selection of per-write serialization is suspended**; so are the recommendations of 10.7, 12.8 and 13.9.
- The experiments are **evidence**, not the mandatory architecture. Preserved: branch
  `exp/332-adapter-shard-serialization` at `6d03c1d` and everything under `evidence/`.
- The contract separates the **persisted** event from the **available** event, makes the cursor an **opaque
  `JournalPosition`** separate from the event timestamp, guarantees eventual availability under documented
  conditions, and demands no permanent omission when a cursor advances.
- The SPI does **not** require: a global order across independent entities, dense positions or arithmetic, a
  comparison of cursors, any database mechanism (transaction ids, WAL, advisory locks, `SKIP LOCKED`, SQL
  transactions, sequences, counters), or a single publication mechanism.

### 14.2 Framework contract (portable; see `contract.md`, revision 2)

Guarantees G1-G10, as revised after the owner review:

- **G1** three write outcomes (success, definitive rejection, unknown), event identity and idempotent replay checked
  before the precondition, atomicity not weakened for any store (an adapter that cannot make a batch atomic rejects
  it with `ErrUnsupportedBatch`).
- **G2** eventual availability under documented operating conditions.
- **G3** safe advance stated over chains of reads with limits: no skipped event, no stall, monotonic reads.
- **G4** stable order per stream; a composite cursor is admissible if it is a safe frontier; the core never compares.
- **G5** per-entity order enforced for every write including `Unconditional()`: sequences strictly increasing,
  greater than the revision, gaps accepted and permanent, `ErrSequenceOrder` and `ErrIdentityConflict` otherwise.
- **G6** at-least-once delivery. **G7** isolation, with the cursor bound to adapter, journal instance, generation,
  scope and shard; the zero cursor is unbound.
- **G8** `DeleteEvents` fails with `ErrRetentionPending` without partial deletion or publication while pending
  events are affected; a per-stream retention floor makes loss of unread events detectable (`ErrCursorOutsideRetention`).
- **G9** concurrency, aborts, retries. **G10** event age, delivery latency, publication backlog and consumer backlog
  as separate measures.

Cursor operations in the contract: zero value, `IsZero`, `Equal` (canonical-form equality, not a completeness test),
versioned serialization with binding. Out of the contract: `Compare`, arithmetic, `int64`, a fixed size. Progress is
proposed as a store with `Load`, `Commit(expectedRevision, generation, next, fence)` and `Reset(expectedRevision)`
(CAS and generation; fencing is separate and belongs to #93).

### 14.3 Adapter strategies (not mandated)

| Adapter | Strategy | Status |
|---|---|---|
| PostgreSQL | per-write serialization (stream counter row lock held to commit) | measured on the real adapter; suspended as the selection |
| PostgreSQL | xid8 position with an xmin horizon | measured on the real adapter; suspended as the selection |
| PostgreSQL | post-commit batch publication | designed (`postgres-batch-publication.md`), not implemented, not measured |
| PostgreSQL | logical decoding of the journal | noted in `portability.md`, not evaluated |
| Oracle | batch publication; Advanced Queuing with commit-time ordering | argued in `portability.md`; not prototyped |
| Cassandra | LWT-claimed publication log with a barrier | sketched in `portability.md`; two facts unverified; not prototyped |

### 14.4 Pending decisions

1. **Public-contract gate (SPI NOT approved):** the shape (`StreamReader`, `ReadStream`, `StreamHeads`, `Page`,
   `JournalPosition`, `ProgressStore`, the typed errors), replace or extend the old methods, and the names.
2. **Behavior change of writes:** idempotent replay before the precondition, `ErrIdentityConflict`, `ErrSequenceOrder`
   (today unconditional writes skip duplicates silently and accept any order), and the audit of every caller (the
   event-sourced actor, the saga actor, the tenant-adoption migration). Open edge: a retry arriving after retention
   removed the committed events.
3. **`DeleteEvents` reason (retention versus erasure)** and what a consumer does on `ErrCursorOutsideRetention`
   (stop and report; rebuild is explicit). Erasure must not stall consumers.
4. **Cursor size:** no limit until the real tokens of the candidate adapters are measured.
5. **Progress and ownership:** `OffsetStore` becomes `ProgressStore` (bytes, revision, generation), the fencing token
   and the ownership mechanism, together with #93; migration of legacy `int64` offsets.
6. **Journal instance and generation:** how they are created, and what advances the generation (a restore, a
   recreation, a re-publication of history).
7. **Data-migration gate:** a strategy-independent migration of rows and legacy offsets, the cutover and its
   one-way-door rollback.
8. **Whether and which PostgreSQL strategy to adopt**, decided with the experiment of `conformance-and-experiment.md`
   section 4 after the owners agree its decision rules, and after the operating facts of 13.9 are known.
9. **Backlog metrics** as required or optional capabilities.
10. **The index issue:** `evidence/issue-draft-timestamp-index.md` is a draft, not opened; it speeds reads and does
    not fix #332.
11. **How and when the #332 regression lands in `develop`.** It fails against the current adapter by design, so
    adding it now would turn CI red; options are to land it with the first conforming adapter, or as a declared
    expected failure that cannot be silently skipped. Not decided.
12. **Delivery plan** (`conformance-and-experiment.md` section 5): documentation review first, then types and the
    conformance suite behind the public-contract gate, then the adapter and its migration, then runner and offsets
    with #93. Nothing is implemented until the documentation review closes.

### 14.5 What is NOT asserted

No performance claim for batch publication. No claim that Oracle or Cassandra adapters work: Oracle is an argument
from documentation, Cassandra is a sketch with unverified facts and no prototype. No claim that the stable lock
orders prove the absence of every deadlock: they remove cycles among the paths examined.
