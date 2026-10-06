# PostgreSQL strategy: post-commit batch publication (#332)

Status: PROPOSED design of ONE adapter strategy, revision 4 (second review round: the entity's shard is recorded and fixed, no claim about reuse of `pub_seq`, fairness across streams, migration audits and rollout; revision 3 followed `contract.md` revision 3: arrival-order publication key independent of clocks, a proof of no starvation, the sequence rules enforced for every write, a backfill that preserves per-entity order). Not implemented, not
measured, no performance claim. It implements `contract.md` (G1-G10 and the progress rules) for the PostgreSQL adapter and is one of several strategies (`design.md` keeps
the measured alternatives: per-write serialization, xid horizon). Nothing here approves the SPI or the migration.

## 1. Idea in one paragraph

A writer persists its events exactly as today, with a "pending" state that is part of the same committed row. A
separate publisher later walks the committed pending rows of one stream, assigns them consecutive positions in one
short transaction, and commits. Positions are assigned only by that publisher, under a lock on the stream, in
increasing order across transactions. An event that commits late is not "behind" anything: it is simply pending
until the next batch, where it receives a higher position than anything a reader has seen.

Writers take no new lock and never wait for the publisher. The cost moves to a second write per event, and to the
time an event spends pending.

## 2. Schema (adapter-internal)

```sql
-- events_store gains two columns (names illustrative)
journal_pos BIGINT        NULL,      -- NULL = pending; set once, by the publisher
pub_seq     BIGINT        NOT NULL,  -- arrival order of the row, from a sequence; no default: the writer sets it (3.2)

CREATE SEQUENCE journal_pub_seq CACHE 1 NO CYCLE;   -- CACHE 1 is a requirement, see 3.2

-- events_store_revisions gains shard_number BIGINT: the shard of the entity, fixed by its first event (G5)

-- pending set, ordered for the publisher
CREATE INDEX events_pending ON events_store (tenant_id, shard_number, pub_seq)
    WHERE journal_pos IS NULL;
-- stream read
CREATE INDEX events_journal ON events_store (tenant_id, shard_number, journal_pos)
    WHERE journal_pos IS NOT NULL;

-- one row per stream: the last position handed out, and the lock the publisher takes
CREATE TABLE journal_streams (
    tenant_id TEXT NOT NULL, shard_number BIGINT NOT NULL, last_pos BIGINT NOT NULL,
    PRIMARY KEY (tenant_id, shard_number));

-- the identity of this journal instance and its generation (G7): one row, created with the schema
CREATE TABLE journal_meta (journal_id UUID NOT NULL, generation BIGINT NOT NULL);

-- the retention floor of each stream (G8): newest position removed by retention; written only by DeleteEvents
CREATE TABLE journal_retention (
    tenant_id TEXT NOT NULL, shard_number BIGINT NOT NULL, floor_pos BIGINT NOT NULL,
    PRIMARY KEY (tenant_id, shard_number));
```

`journal_pos` is the cursor payload; the contract keeps it opaque. The adapter serializes a cursor canonically as a
version byte, the adapter tag, the `journal_id`, the `generation`, the scope and shard binding and the position, so
byte equality is logical equality (`Equal`). A stream position is unique per event, so there are no groups to keep whole and a
read's limit is exact.

## 3. Write path

### 3.1 Same lock protocol as today; the contract's checks run under it

The existing lock protocol is unchanged: conditional writes lock the entity's `events_store_revisions` row
(`lockRevision`), unconditional writes upsert the revision rows of their ids in sorted order, `DeleteEvents` locks
the row with `lockRevisionIfExists`. Once the revision rows of the batch are held, and before anything is inserted,
the write evaluates in this order (all under the entity row lock, so no other writer of the entity interleaves):

1. **Idempotent replay (G1).** For each event identity of the batch, look it up. If every identity exists with
   byte-identical payload, manifest and metadata, return success with no effect: no new row, no second publication,
   and the revision is not touched. If some exist and others do not, or one exists with different content, return
   `ErrIdentityConflict` (definitive).
2. **Sequence protocol (G5).** Within the batch an entity's sequences must strictly increase; each must be greater
   than the entity's `revision` (the highest sequence ever persisted, which retention never lowers); otherwise
   `ErrSequenceOrder` (definitive). Gaps above `revision + 1` are accepted.
   The entity's **shard** is checked in the same step: the first event of an entity records its shard in the revision
   row; any later event carrying a different shard is `ErrShardMismatch` (definitive). Replay (step 1) compares
   content, not `Timestamp`.
3. **Precondition** (`ExpectGenesis`, `ExpectRevision`), as today.
4. **Insert** the rows with `journal_pos = NULL` and `pub_seq = nextval('journal_pub_seq')`, one row at a time in
   the entity's sequence order; advance the revision row. A replayed batch allocates nothing.
5. **Commit.**

A failed or rolled-back write leaves no row, hence no pending evidence (G1). **Outcome classification.** An error
before the `COMMIT` statement was issued is a definitive rejection. An error, cancellation or timeout while the
commit is in flight or after it was sent is `ErrOutcomeUnknown`: the transaction may have committed. Only a server
error that proves the transaction aborted (a serialization failure, a constraint violation raised by the commit) is
definitive. The retry of an unknown outcome is the identical batch, resolved by step 1. Known limit: if retention
removes the events of a committed write before the retry arrives, step 2 reports `ErrSequenceOrder` for a write that
did succeed; the retry window must be shorter than the retention delay, or the caller must read the entity, and an
idempotency window of recent batches is an open design point.

### 3.2 The publication key: arrival order, independent of every clock

`pub_seq` is allocated from a database sequence at insert time. It orders the pending rows for the publisher and
nothing else; the event `Timestamp` plays no part in publication.

**An earlier draft keyed publication on the actor timestamps and is withdrawn.** A key derived from `Timestamp`
lets a writer's clock decide when an event is published. An entity whose actor clock runs ahead is listed after the
newer events of every other entity for as long as the skew lasts, and a monotone per-entity key (`GREATEST` with the
previous key) would even carry one bad timestamp forward to every later event of that entity. "Oldest first" by
timestamp therefore does not prove G2: under continuous load a skewed entity can wait indefinitely.

`pub_seq` has three properties, and each is used below:

1. **Monotone per entity.** A writer of sequence `n+1` of an entity allocates its `pub_seq` after it took the entity
   revision lock, which it only gets after the writer of `n` committed (3.3), and `n`'s `pub_seq` was allocated
   before that commit. The sequence is created with `CACHE 1`: with a cache each session reserves a block, so a later
   allocation in one session can be lower than an earlier one in another, which would break the property. Within a
   batch the writer inserts an entity's rows in sequence order.
   *Verification status:* that non-overlapping `nextval` calls return increasing values with `CACHE 1` is the
   standard behavior, but the PostgreSQL page read for this review documents only that concurrent calls "safely
   receive a distinct sequence value" and that values are not reclaimed on abort; the monotonic-in-time wording is
   not quoted. The stress test of the experiment asserts it (per-entity `pub_seq` increasing with the sequence number).
2. **Independent of clocks.** Nothing about a timestamp enters it.
3. **Not required to be unique or gapless.** Gaps (rollbacks, crashes) are harmless: the publisher reads whichever
   rows exist, and the design relies only on property 1. Were two rows ever to share a value, ties are broken by
   `(persistence_id, sequence_number)`, which keeps an entity's rows in sequence order.

The batch is the `N` rows with the smallest `pub_seq` among the committed pending rows of the stream, a **prefix of
every entity it touches** (property 1): if `(entity, n+1)` is selected, `(entity, n)` has a smaller `pub_seq`, so it
is either already published or selected too. The query reads `N` index entries, not the whole backlog.

### 3.2.1 No starvation (the fairness argument)

Let `r` be a row with `pub_seq = k`. The rows that can ever be selected before `r` are those with `pub_seq < k`, a
finite set fixed when `r` was inserted: it contains the backlog ahead of `r` and the writers that were still in
flight with older allocations. Rows inserted after `r` have larger `pub_seq` and are never ahead of it, whatever
their timestamps, their entity or how fast they arrive.

While `r` is committed and pending, every successful publisher cycle that does not select `r` selects `N` rows with
`pub_seq < k` (otherwise `r` would be among the `N` smallest). Each such row is published once. So `r` is selected
after at most `ceil(M / N)` successful cycles, where `M` is the number of rows with `pub_seq < k` that are still
unpublished when `r` becomes visible, counting those that commit later. `M` does not grow with the load that arrives
after `r`. Together with the documented condition of G2 (the publisher keeps running and its cycles commit), the wait
of any event is bounded by the backlog ahead of it, independently of its timestamp. If offered load exceeds the
publisher's capacity the backlog grows and waits grow with it, but each event's wait stays bounded by the backlog
that preceded it: nothing is passed over indefinitely.

**How it is tested** (`conformance-and-experiment.md` C21): entities whose actors stamp timestamps with offsets of
-100 years, -1 year, 0, +1 hour, +1 year and +100 years are written continuously alongside many other entities at
about 80% of the publisher's capacity, with `N = 100`. For every event the harness records the number of publisher
cycles between its commit and its availability and asserts it is no larger than `ceil(M / N) + c` for the `M`
observed at its commit. It runs deterministically in the unit lane (a counted, simulated publisher in the testkit
harness) and against PostgreSQL with the real publisher, and a negative control that orders by timestamp must fail
it.

### 3.2.2 Fairness across streams

The argument above is per stream. A publisher that serves several streams must also visit each stream that has
pending rows within bounded time; a loop that keeps returning to a busy stream would starve a quiet one. The
scheduler therefore serves the streams that have pending rows in round-robin order (or oldest pending `pub_seq`
first, which gives the same bound), taking one cycle of at most `N` rows per visit. With `S` streams holding pending
rows, a stream is visited within `S` cycles, and an event waits at most `S * ceil(M / N)` cycles, `M` being the
backlog ahead of it in its own stream. Several publishers lower the constant, not the bound. C21 includes several
streams so the bound is tested across them, not only inside one.

Cost of the key: one `nextval` per event written; the sequence is a single shared counter. Not measured.

### 3.3 Why a snapshot that sees `n+1` sees `n`

A writer of `n+1` takes the entity row lock only after the writer of `n` committed and released it; so `n`'s commit
precedes `n+1`'s commit, and any snapshot containing `n+1` contains `n`. This is the property the whole design
rests on for G5; it needs the existing revision-lock protocol and nothing new.

## 4. Publisher cycle (one stream)

Run at `READ COMMITTED`. Pseudocode of one transaction:

```
BEGIN
  last := SELECT last_pos FROM journal_streams WHERE (tenant, shard) FOR UPDATE      -- row lock; create the row if absent
  rows := SELECT pk FROM events_store
          WHERE tenant=$t AND shard=$s AND journal_pos IS NULL
          ORDER BY pub_seq LIMIT N
  UPDATE events_store SET journal_pos = last + rank(row)   -- rank 1..n in the order above
  UPDATE journal_streams SET last_pos = last + n
COMMIT
```

- The stream row lock is the only coordination. It is held for the duration of this short transaction, not by
  writers.
- `FOR UPDATE` under READ COMMITTED returns the latest committed version of the row after any wait, so `last` is
  never stale. The publisher must not run at REPEATABLE READ or SERIALIZABLE (it would raise serialization failures
  that carry no information here).
- Positions of a rolled-back publisher transaction are never visible: nothing was committed.
- Batch size `N` bounds the lock hold time and the position block. Smaller `N` lowers the stall a stuck publisher
  can cause; larger `N` amortizes the per-transaction cost.

### 4.1 Why a reader can never be handed a cursor with a lower position arriving later (G3)

1. Positions are assigned only inside publisher transactions holding the stream row lock.
2. Each transaction assigns `(last, last+n]` where `last` was read under the lock, and advances `last_pos` in the
   same transaction. So the position blocks of committed transactions are disjoint and increase in lock order.
3. Lock order is commit order: a publisher takes the lock only after the previous holder committed or rolled back.
4. A reader's snapshot contains a prefix of the committed publisher transactions of the stream (each later one
   began after the earlier one committed). Everything a read returns is below `last_pos` of the newest transaction in
   its snapshot, and every future position is above it.
5. A persisted event that is not in the snapshot's published set is pending; when it is published it gets a
   position above `last_pos` of every transaction already committed, hence above any cursor already issued.

No transaction id, horizon or commit timestamp takes part. The argument needs: row locks held to commit,
read-committed visibility of committed row versions, and atomic commit.

### 4.2 Per-entity order (G5)

G5 is enforced when the write is accepted (3.1 step 2), whatever its precondition: an entity's sequences are
strictly increasing in the batch and each is greater than the entity's revision, so a row of `n+1` can never be
persisted before `n`, and a late number below the revision is rejected with `ErrSequenceOrder`. There is therefore
no entity written out of sequence order to publish in some other order. Given that, `pub_seq` order within an
entity is sequence order (3.2), the batch is a prefix of each entity it touches, and a row of `n+1` is only visible
to the publisher if `n` was committed (3.3), so `n` is already published or earlier in the same batch. Availability
order of an entity is its sequence order.

## 5. Publisher lifecycle and scheduling

The strategy needs something to call the cycle. Options, none mandatory:

| Option | How | Notes |
|---|---|---|
| In-process loop | each node scans streams with pending rows (the partial index makes `SELECT DISTINCT tenant_id, shard_number WHERE journal_pos IS NULL` cheap) every `tau`, and also right after a local commit | the proposed default for the experiment; `tau` bounds publication latency |
| Publish-on-read | the stream read runs a cycle first | no background component, but a read path that writes, and it needs a consumer to be polling |
| `NOTIFY` as a hint | the write transaction issues `NOTIFY` (delivered only if it commits, Q) so publishers wake early | a hint only: notifications are not persisted for absent listeners and can fail when the queue is full (Q), so correctness never depends on them |

`FOR UPDATE SKIP LOCKED` may be used on the `journal_streams` row so that a second publisher moves on to another
stream instead of idling. It must NOT be used on event rows to let two publishers take disjoint batches of the same
stream: they would then commit their position blocks in an order unrelated to lock order and break G3.

## 6. Coordination of several publishers: limits and guarantees

| Statement | Holds because |
|---|---|
| Two publishers of the SAME stream never overlap | the stream row lock serializes their transactions |
| Any number may run, on any node, with no election | the lock is the transaction; no lease, epoch or heartbeat |
| Publishers of DIFFERENT streams run in parallel | different rows |
| A crashed publisher loses nothing and blocks no one for long | its transaction rolls back when the session dies; pending rows stay NULL |
| A stuck publisher (hung session, long transaction) stalls ITS stream only, writers and other streams unaffected | it holds one row lock; the bound is the session's `statement_timeout` / `idle_in_transaction_session_timeout` / `lock_timeout`, which must be set for publisher sessions (the default of the last one is off, Q) |
| Throughput of one stream is bounded by `N` rows per publisher transaction | one transaction at a time per stream |
| No ordering or progress guarantee between streams | none is needed |
| A foreign long transaction elsewhere on the server does not delay publication | no snapshot horizon is used (contrast the xid scheme) |
| A DBA that holds a lock on a `journal_streams` row, or deletes it, can stall publication | operational requirement to document |

## 7. Failure matrix

| Failure point | State left | Recovery | Contract view |
|---|---|---|---|
| Writer fails before commit | nothing | none | G1 |
| Crash after persist, before any publish | committed row with NULL position | next publisher cycle publishes it | G2: needs a running publisher |
| Crash during the publisher transaction | rolled back, nothing visible | next cycle redoes the batch | G9 |
| Publisher commits, consumer crashes before saving progress | events available | consumer re-reads from its old cursor, same events | G6 at-least-once |
| Progress write lost | same | same | G6 |
| Database failover during publication | transaction rolled back or committed atomically | next cycle | G9 |
| Publisher cannot keep up | backlog (pending rows) grows | catches up when load drops; metric: oldest pending age | G2 holds under the documented condition "publisher capacity >= write rate" |
| Cursor from another stream, scope, shard or journal instance presented | `ErrCursorMismatch`, nothing read | none | G7 |
| Cursor from before a restore or a re-publication | `ErrCursorInvalidated` | restart from the zero cursor | G7 |
| Commit sent, answer lost (unknown outcome) | the batch is committed or not | the identical retry: replay returns success, a fresh batch proceeds | G1 |
| `DeleteEvents` while its entity has pending events | nothing deleted | caller retries after publication | G8 |
| Retention removed events a slow consumer had not read | floor above the consumer's cursor | the next read fails `ErrCursorOutsideRetention`; consumer stops and reports | G8 |

## 8. Retention, `DeleteEvents` and detectable loss (G8)

`DeleteEvents(scope, id, toSequence)` follows the contract's preferred form: **all or nothing with respect to pending
events, and no publication as a side effect.** In one transaction:

1. Take the entity revision row (`lockRevisionIfExists`, as today). No writer of the entity can add a pending row
   now, so the entity's pending set can only shrink (the publisher publishes it).
2. If any row of the entity with `sequence_number <= toSequence` has `journal_pos IS NULL`, return
   `ErrRetentionPending`. Nothing was deleted and nothing was published. The caller retries; by G2 the rows become
   available and a later call proceeds.
3. Otherwise every row `<= toSequence` is available. Compute the newest position among them per stream,
   `DELETE` them, and in the same transaction raise the stream's `journal_retention.floor_pos` to
   `GREATEST(floor_pos, that position)`.
4. Commit. A publisher that publishes other rows in the meantime does not conflict: it updates only pending rows and
   step 3 touches only available ones.

A flow that has to finish (an erasure) retries on `ErrRetentionPending`; its completion time therefore includes the
publication delay, and the engine's retry with backoff already exists. This replaces the earlier idea of publishing
inline from the delete.

**How a consumer detects that its cursor fell outside retention.** `ReadStream(after c)`, in the same statement
snapshot that reads the events, compares the stream's `floor_pos` with the position inside `c`. If `floor_pos`
is greater than that position, an event after the cursor was removed before it was returned, and the read fails with
`ErrCursorOutsideRetention` and returns nothing. The check is exact (the floor is the maximum removed position, so it
exceeds the cursor if and only if some removed event lay after it) and needs no cursor comparison in the core. The
zero cursor never raises it: it means the beginning of the retained history. The consumer stops advancing and
reports; rebuilding from zero is an explicit decision. Floor and deletion commit together, so a reader never sees a
deleted event without the floor that explains it.

Open (owner): the floor should advance for retention and not for erasure, because a consumer must continue past
erased events; today's `DeleteEvents` cannot tell them apart and would need a reason parameter.

**Lock classes of the whole strategy (acyclic by construction).** Entity revision rows (ascending id): taken by
writers and by `DeleteEvents`. Stream rows (`journal_streams`): taken only by publishers. Retention rows
(`journal_retention`): taken only by `DeleteEvents`, after its revision row, and never held while waiting for
anything else. No path takes a revision row while holding a stream or retention row, so no cycle can form among
writers, publishers and deletes. That is an argument about these paths; it does not prove the absence of every
possible deadlock, and the experiment keeps a concurrent stress with injected failures for them.

Retention against slow consumers is not prevented here: it is made detectable (G8).

## 9. Reads and heads

- `ReadStream`: first the binding of the cursor (journal id, generation, scope, shard: `ErrCursorMismatch` or
  `ErrCursorInvalidated`; a cursor whose position is ahead of the stream head also yields `ErrCursorInvalidated`,
  which catches a journal restored to an earlier point), then the retention floor (section 8), then
  `journal_pos > cursor AND journal_pos IS NOT NULL ORDER BY journal_pos LIMIT n`. No horizon, no group extension,
  no dependence on a snapshot beyond the statement's own. Positions are unique per event, so successive pages with a
  limit `>= 1` cannot stall. A page of events is never empty while the cursor moves, in this adapter; the contract
  does not rely on that.
- `StreamHeads`: the `last_pos` of each `journal_streams` row of the scope, a constant-time read that does not scan
  events. It is the **published frontier**: how far publication has gone, as observed by the call. It is not
  necessarily the position of the newest retained event, because the event that received `last_pos` may since have
  been deleted by retention; it never moves backwards. A consumer whose progress is `Equal` to it has reached the
  observed frontier; that says nothing about events still pending or written since.
- `ValidateAdvance(from, to)`: checks the binding of both cursors, that `to`'s position is not below `from`'s, and
  that it is not above `last_pos`; the comparison is of two integers inside the adapter.
- The zero cursor reads the retained history from its beginning. After retention has removed events that is all it
  can return; it does not promise to rebuild a projection in full.
- Publication backlog: the count and the age of the oldest pending row, from the pending index. Consumer backlog:
  a count of rows with `journal_pos` after the cursor, which is not free and may be reported as unknown.
- A restore of the database to an earlier point restores `journal_meta` too, so it keeps the old generation: the
  restore procedure must advance the generation explicitly (an operational requirement), and the cursor-ahead-of-head
  check is a safety net, not a guarantee.

## 10. Costs to measure (no claim made)

| Cost | Why it exists | Mitigation if it hurts |
|---|---|---|
| A second write per event (the position update) | a new row version plus index entries; the `journal_pos` index prevents a HOT update | a separate append-only `journal_log` table and a small `journal_pending` table, so event rows are never updated (variant B below) |
| Autovacuum pressure on dead versions | one dead version per event | tune autovacuum for the table; variant B |
| 16 more bytes per row, two partial indexes, one `nextval` per event | `journal_pos`, `pub_seq` | none needed unless measured as a problem |
| Event latency to availability = publisher cadence + batch time | events wait pending | in-process trigger after commit; `NOTIFY` hint |
| One publisher transaction per stream at a time | row lock | batch size; more streams |
| Backlog under overload | publisher capacity below write rate | more publishers do not help one stream; reduce `N` stalls, raise `N`, or shard |

**Variant B (to evaluate only if A's amplification dominates):** the writer inserts the event row (immutable) and a
row in `journal_pending`; the publisher inserts into `journal_log(tenant, shard, pos, persistence_id,
sequence_number)` and deletes the pending rows in one transaction; reads join `journal_log` to `events_store`. It
trades the update amplification for an insert and a delete of small rows and a join on reads.

## 11. Migration notes (gates PENDING)

Existing rows must enter the new order without breaking G5, and existing consumer offsets must keep their meaning.

**Backfill that preserves per-entity order.** Ordering the legacy rows by timestamp is not safe: an actor clock that
stepped backwards left an entity whose sequence `n+1` has a smaller timestamp than `n`, and a timestamp order would
publish them out of sequence. The backfill computes, per entity and in sequence order, a **legacy key**
`max(timestamp) OVER (PARTITION BY tenant_id, persistence_id ORDER BY sequence_number)`, which is non-decreasing in
the sequence by construction. It assigns `pub_seq` to the legacy rows in the order `(legacy key, persistence_id,
sequence_number)`, then lets the publisher assign positions in `pub_seq` order. An entity is therefore published in
sequence order even if its history was committed in another order; whether a legacy entity was ever committed out of
order cannot be detected, and the backfill does not need to. The sequence `journal_pub_seq` is advanced above the
largest legacy `pub_seq` before writers restart, so every new row sorts after every legacy row.

**Legacy offsets.** An old offset `T` is a timestamp cursor: "everything with a timestamp up to `T` was delivered".
`LegacyOffset(T)` for a stream is the position of the last legacy row whose **legacy key** is `<= T`; because
positions follow the legacy-key order, those rows are a prefix. This is conservative in the right direction: every
row with a legacy key `<= T` has a timestamp `<= T` and counts as consumed, as it did before; a row whose timestamp
is `<= T` but whose legacy key is above `T` (an entity whose clock stepped back) receives a later position and is
delivered again, which at-least-once allows. The mapping never skips an event that the old cursor had not skipped.
Events that the old cursor omitted remain unrecoverable except by rebuild from the zero cursor, as before. The legacy
key is kept in a temporary column until every offset has been mapped, then dropped; offsets are mapped in the same
maintenance step as the backfill, per `(projection, shard)` and, once #93 lands, per scope.

**Audits to run before the migration (they decide whether it can proceed).** They read the data; none was run for
this document, there is no production data here.

1. *Entities that span shards.* `SELECT tenant_id, persistence_id FROM events_store GROUP BY 1, 2 HAVING
   COUNT(DISTINCT shard_number) > 1`. The shard of an entity is fixed by its first event (G5), and an entity whose
   events already sit in two shards has no single stream, so no cross-stream order exists for it. Each such entity is
   either re-homed (its earlier events are moved to the shard of its first event, which changes stream membership
   and needs its own offset mapping) or the migration refuses to proceed until the owners decide (D13).
2. *Units of the legacy offsets.* The event-sourced actor stamps events in UnixNano, while the runner's
   `WithStartOffset`, `WithResetOffset` and `RebuildProjection(from)` store UnixMilli (verified in the code, and
   demonstrated on the in-memory store: a "from" one year in the future, in milliseconds, still returned an event
   stamped in nanoseconds; not run against PostgreSQL, whose query applies the same comparison). So
   `offsets_store.current_offset` can hold nanoseconds (committed progress) or milliseconds (a reset or a time-based
   start). The mapping handles both without special cases: a millisecond value is below every nanosecond timestamp,
   so `LegacyOffset` returns the zero cursor, which is exactly what such an offset already meant in practice (read
   from the beginning). The audit lists the magnitude of every stored offset so the owners see which projections are
   affected.
3. *Offsets have no scope today.* `offsets_store` is keyed by `(projection_name, shard_number)`; a scope arrives with
   #93. A legacy offset can be mapped only into a stream, so the offsets must be migrated after #93 gives progress its
   scope (or each is assigned the scope its projection is registered under, with the ambiguity of a name used under
   several scopes). The migration depends on #93.
4. *Retention history.* Events deleted before the cutover cannot be reconstructed: the retention floor starts at
   zero and loss before the cutover stays undetectable. State it in the runbook.

**Rollout of the retention check.** Raising `ErrCursorOutsideRetention` turns silent loss into a stopped consumer.
Today the janitor deletes events after snapshots (retention count may be zero), so a consumer that lags will hit the
error on its first read. The adapter ships the check in a **report-only mode** first: it records and logs every read
that would have failed (a counter per projection and stream) and continues; enforcement is switched on once the
owners have seen which projections are affected. This is an operational switch of the adapter, not part of the
contract, and the contract's guarantee holds only in enforcing mode (D15).

- A new `journal_id` is created with the schema and `generation` starts at 1; cursors issued after the cutover carry
  both. Positions assigned by the backfill belong to generation 1.
- `pub_seq NOT NULL` with no default fences old writers: after the cutover an old binary fails loudly instead of
  writing rows that skip the sequence protocol and the identity checks. The cutover is a one-way door and needs the
  maintenance window already described in `design.md` section 7.2.

## 12. What this strategy does not claim

No throughput, latency, or "better than" statement. The measurements of per-write serialization and of the xid
horizon are in `design.md`; whether batch publication changes the picture is the question of the experiment in
`conformance-and-experiment.md`.
