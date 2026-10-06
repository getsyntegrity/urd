# Conformance suite and minimal comparison experiment (#332)

Status: PROPOSED, revision 4 (follows `contract.md` revision 4). Nothing here is implemented. The suite specified in sections 1-3 is what an adapter must pass to
claim `contract.md`; section 4 proposes the smallest experiment that can say whether PostgreSQL batch publication
(`postgres-batch-publication.md`) is worth building next to the variants already measured (`design.md` sections
10-13). The existing real-adapter regression `LateVisibleEventsBehindACommittedOffsetAreDelivered` lives on the experimental
branch and enters `develop` together with the implementation that makes it pass (decided by the owner on #338); the
experiments keep it as an explicit expected failure, and the production CI is not changed to accept that omission.

## 1. Principles

- A check asserts only what a consumer can observe through the contract: which events a read returns, in which
  order, from which cursor, after which failure. It never reads a timestamp offset and never names a mechanism
  (transaction id, lock, sequence, publisher).
- Where observing a guarantee needs control the SPI does not give (wait for publication, restart a process, inject
  a failure), the check uses a harness hook the adapter's test code implements. Hooks live in the conformance
  package, not in the SPI.
- A check that needs an optional hook is reported as NOT RUN with the missing capability, never as passed. A run
  prints the capability matrix. An adapter that cannot provide a hook must reference the evidence that replaces the
  check; that reference is reviewed, the suite cannot judge it.
- Every check also runs against a deliberately broken adapter (negative controls, section 3) so a green suite can
  be trusted.

## 2. Harness

```go
// package conformance (proposal)
type Harness interface {
    // Required.
    NewStore(t TestingT) Store                         // a fresh, empty store (test database/keyspace)
    AwaitPublished(ctx context.Context, s Store, scope Scope) error
        // returns when no event of the scope is pending, or fails when the adapter's declared
        // publication bound (Capabilities.PublicationBound) elapses
    Restart(ctx context.Context, s Store) Store         // a new process over the same data; all in-memory state gone

    Capabilities() Capabilities
}

type Capabilities struct {
    PublicationBound   time.Duration     // the operating condition of G2 for this adapter, in test settings
    HoldWrite          bool              // can park a write inside its commit window (exposes a pending/in-flight writer)
    FailureInjection   []FailurePoint    // subset of: AfterPersistBeforePublish, DuringPublish, AfterPublishBeforeAck,
                                         // AfterCommitBeforeAck (the write committed, the answer is lost: an UNKNOWN outcome)
    ConcurrentPublish  int               // how many publishers can run on one stream (0 = not applicable)
    PauseResumePublish bool              // can stop and restart publication (backlog, lag checks)
    PublicationLag     bool              // implements PublicationBacklog (count and oldest pending age)
}
```

The conformance package ships a harness for the in-memory `testkit` store that includes a deliberately DELAYED
publisher (events stay pending until the harness runs it), so the pending states exist outside any database.

## 3. Checks

`Req` = hooks needed beyond the required ones. All run per scope shape: a tenant scope, and `Unscoped()`.

| ID | Guarantee | Scenario | Assertion | Req |
|---|---|---|---|---|
| C01 | G3 | Persist `a`, `b`; read to cursor `c`. Then persist late events from other entities whose timestamps are older than or equal to everything consumed (the test sets them). `AwaitPublished`. Read from `c`. | every late event is returned exactly once; none is missing; this is the permanent #332 regression, expressed without a timestamp offset | none |
| C01b | G3, G9 | Same, with the earlier writer held in its commit window while the later writer finishes (the original late@200 / late@150 shape), then released | same assertion; a rolled-back held writer leaves no event and no hole | HoldWrite |
| C02 | G1, G2 | Persist, crash before any publication, `Restart`, `AwaitPublished` | all events available, in per-entity order, none duplicated | FailureInjection: AfterPersistBeforePublish |
| C03 | G9 | Crash in the middle of a publication | at every read during and after: for each entity the available events are a prefix (no event available without its predecessors); after `Restart` and `AwaitPublished`, a read from the zero cursor returns each persisted event exactly once; no event appears below a cursor already issued | FailureInjection: DuringPublish |
| C04 | G6 | Read a page, do not commit progress (the "crash after publish, before progress" case), read again from the old cursor | the same events in the same relative order, possibly followed by newer ones; none missing | none |
| C05 | G9 | Several publishers run concurrently on one stream while writers write | each event appears exactly once in the stream; per-entity order holds; all delivered; no read returns a position-order violation | ConcurrentPublish > 1 |
| C06 | G1, G6 | Retried and duplicated writes: the same unconditional batch twice; a conditional write that conflicts; a write that fails midway | duplicates appear once; the conflicting and the failed write leave nothing available after `AwaitPublished` | none |
| C07 | G4, G5 | Several entities, several sequences per write, pages of limit 1, 2, 3, 100 and 0 | each entity's sequences strictly increase across the whole delivery; at every page boundary the available set of each entity is a prefix; limit 0 returns nothing and an unchanged cursor; the delivered multiset equals the persisted one | none |
| C08 | G6, cursor | Serialize the cursor to bytes, `Restart`, deserialize, resume; resume from the zero cursor | resuming loses and duplicates nothing beyond at-least-once; garbage, truncated or wrong-version bytes fail with a typed error; the zero cursor starts at the beginning | none |
| C09 | G7 | Tenant A, tenant B, `Unscoped()` and a tenant named "unscoped"; shards 1 and 2 | events never cross; a cursor of one stream presented for another (scope or shard) fails with `ErrCursorMismatch` and returns nothing; one scope's pending events never delay another's availability beyond the adapter's documented bound | none |
| C10 | G8 | Persist events and call `DeleteEvents` while some of them are still pending | with pending events up to the sequence the call fails with `ErrRetentionPending`, deletes NOTHING (no partial deletion) and publishes nothing as a side effect; after `AwaitPublished` the same call succeeds and no event `<=` the sequence remains; events above it are untouched and delivered | PauseResumePublish |
| C11 | cursor | Zero cursor, cursors from different reads | `IsZero` only for zero; `Equal` is reflexive, symmetric and survives a serialization round trip; two routes to the same frontier serialize to identical bytes (canonical form); different frontiers are not equal; `String()` carries no payload | none |
| C12 | G2, heads | Drain a stream, then persist more; then remove the newest published event by retention | `StreamHeads` is the PUBLISHED FRONTIER as observed: it never moves backwards, it is not necessarily the position of the newest retained event (the check deletes that event and the head does not regress), and `Equal` to the consumer's progress means only "reached the observed frontier": the check persists an event right after and shows the consumer is no longer at it, and pauses publication to show a pending event does not move it | PauseResumePublish for the pending half |
| C13 | G10 | Pause publication, persist, wait, resume; process events with old timestamps | event age grows with the clock whether or not anything is wrong; publication backlog (count and oldest age) grows while paused and returns to zero after publication; none of them is read from a cursor value | PublicationLag + PauseResumePublish |
| C14 | G2-G5, G7 | Seeded randomized interleaving: writes (conditional and unconditional, multi-entity, multi-shard), reads with random limits and cursor persistence, restarts, publications, injected failures, deletions, against an in-memory oracle of the persisted set | the oracle's invariants hold after every step and at the end; the seed of a failure is printed and replays it | whatever hooks exist |
| C15 | G1 | A write whose acknowledgement is lost (`AfterCommitBeforeAck`), then the identical batch is retried; a write cancelled before any commit; a write rejected by its precondition | the lost-ack write surfaces as `ErrOutcomeUnknown`; the identical retry returns success with no duplicate and no second publication, also for a conditional write whose precondition no longer holds (replay before precondition); a pre-commit failure is a definitive rejection and leaves nothing; any unclassified error is treated as unknown by the harness | FailureInjection: AfterCommitBeforeAck |
| C16 | G1 | Same identity, different content; a batch overlapping another one partly; a batch the adapter declares unsupported (multi-entity where it cannot be atomic) | `ErrIdentityConflict` and `ErrUnsupportedBatch` are definitive and leave nothing; a store never writes part of a batch | none |
| C17 | G5 | Unconditional writes: a sequence `<=` the entity's revision, a decreasing batch, a gap, the same after retention deleted older events | `ErrSequenceOrder` for the first two; the gap is accepted and the skipped numbers can never be persisted later; retention does not lower the revision; the entity is delivered in sequence order whatever the precondition | none |
| C18 | G7 | Two journal instances with identical scope and shard labels; a journal restored or rebuilt (journal generation advanced) | a cursor from instance A presented to instance B fails with `ErrCursorMismatch`; a cursor from before the generation change fails with `ErrCursorInvalidated`; the zero cursor works on both | a way to create two instances and to advance the generation |
| C19 | G8 | Retention removes events a slow consumer has not read; erasure removes others (if the reason parameter exists) | the next read of the slow consumer fails with `ErrCursorOutsideRetention` and returns nothing; the zero cursor does not; a consumer already past the removed events is not affected; if erasure is distinguished it does not raise the error; reading from the zero cursor after retention returns only the retained events, so a rebuild is not complete (documented, asserted) | none |
| C20 | G3 | Chains of reads with limits 1, 2, 3 and 100 under concurrent writes and publications, including a source whose page may be empty with a moving cursor | every retained event is delivered by the chain; the chain never stalls while an undelivered event exists; reads never go back to an older view | none |
| P01 | progress | Two committers load the same progress; the second commits first; the first commits afterwards | the stale `Commit` fails with `ErrProgressConflict` carrying the current progress and stores nothing; the winner's value stands | none |
| P02 | progress | Commit, `Reset`, then a `Commit` prepared before the reset | the late commit fails (epoch changed); progress is the zero position under a new epoch; a reset with a stale revision fails | none |
| P03 | progress | `Commit` whose answer is lost, retried, with the resolution rule of `contract.md` section 6 | the caller applies the four-step resolution: epoch changed means start over; `LastCommitID` equal to its own means success; revision unchanged means retry after validating; anything else means reload. Success is never inferred from the position alone: a check where ANOTHER worker has committed the very same position under a different `CommitID` must not be read as the caller's success | failure injection on the progress store |
| P04 | progress | A deposed owner commits with an old fence token while the revision still matches (only if the store supports fences) | the commit is refused by the fence although the CAS would have passed; fencing and CAS are tested separately | fencing support |
| C21 | G2 fairness | Across SEVERAL streams (so the cross-stream bound of `postgres-batch-publication.md` 3.2.2 is tested), entities whose actors stamp timestamps offset by -100 years, -1 year, 0, +1 hour, +1 year and +100 years are written continuously next to many others at about 80% of the publication capacity | for every event the number of publication cycles between its commit and its availability is at most `S * ceil(M / N) + c`, `S` being the number of streams with pending events and `M` the backlog ahead of it in its stream at its commit; positions and availability never depend on a timestamp. Runs in the unit lane with a counted simulated publisher and against PostgreSQL with the real one | PauseResumePublish helps; a way to count cycles |
| P05 | progress | A runtime commits a `Next` older than the loaded position while holding the CURRENT revision; a `Next` beyond the published frontier; a `Next` of another stream instance or generation | `ValidateAdvance` refuses each (`ErrCursorRegression`, `ErrCursorBeyondHead`, `ErrCursorMismatch` / `ErrCursorInvalidated`); a runtime that commits without validating fails the check (for adapters that offer co-located validation, a commit that violates it is refused by the store itself); `from == to` is accepted | none |
| C22 | G5 | An entity persists events in stream 1; a later write for the same entity carries another stream key (as if the actor had been placed elsewhere); a replay of the first batch | the later write fails with `ErrShardMismatch` and leaves nothing; both streams stay free of the entity's later events; the replay with the original shard succeeds; no consumer of either stream can see `n+1` of that entity before `n` | none |
| C23 | positioning | (capability `TimePositioner`) Events with `Timestamp` in arbitrary order, then `PositionAt(t)` for several `t`, including a `t` in the future and one before everything | every available event with `Timestamp >= t` is delivered by a chain from the returned cursor; events with an earlier `Timestamp` MAY appear but none that qualifies is skipped; a `t` in the future returns a cursor from which nothing qualifying is lost and nothing is claimed; the property does not depend on unit conversions | none |

C14 is the strongest evidence for G3 and G5 because the hand-written interleavings above cannot enumerate the
schedules a publisher and a writer can take.

### Negative controls (the suite must fail them)

| Broken adapter | Must fail |
|---|---|
| timestamp cursor (the current behavior): position = event timestamp | C01, C01b, C14 |
| positions assigned at write time from an unlocked counter (can land below an issued cursor) | C01b, C14 |
| a publisher that publishes an entity's `n+1` before `n` | C03, C07, C14 |
| a store that returns events of another scope when handed a foreign cursor | C09 |
| `DeleteEvents` that removes or publishes pending events, or deletes part of the range before failing | C10 |
| a write path that treats an unclassified error as a rollback, or a retry that creates a duplicate | C15 |
| unconditional writes that accept a sequence below the revision | C17 |
| a cursor bound only to adapter, scope and shard (two journals accept each other's cursors) | C18 |
| retention that removes unread events without raising an error | C19 |
| a progress store whose `Commit` overwrites without comparing the revision, or ignores a reset | P01, P02 |
| a publication order derived from event timestamps (the withdrawn `pub_key` design) | C21 |
| a runtime that commits progress without `ValidateAdvance`, or an adapter whose `ValidateAdvance` accepts a regression | P05 |
| a store that accepts an event of an existing entity under another stream | C22 |
| a time-based start that ignores its time or converts it to the wrong unit | C23 |
| a progress store that resolves an unknown commit by comparing positions only | P03 |
| a cursor that changes meaning after restart | C08 |

### Lanes

- Unit: the `testkit` adapter, its delayed-publication harness and the negative controls run in the unit lane with
  go-specs and in-memory state only: no real resources.
- Integration: the PostgreSQL adapter runs the same suite in the Postgres lane of `inttest` through Testcontainers
  (documented here: with Colima, `DOCKER_HOST` pointing at its socket and `TESTCONTAINERS_RYUK_DISABLED=true`).
  Build and vet of `inttest` is not execution; the report separates them, and separates the unit race run from the
  integration run.
- A real `-race` run and the adapter's concurrent stress stay part of the evidence, not replaced by this suite.

## 4. Minimal experiment: batch publication against the measured variants

Precondition: the contract is viable (this document and `portability.md` reviewed) and the owner authorizes an
experiment branch. No production path changes; behind a build tag as before.

### 4.1 What is compared

The existing variants, re-run in the same session so host conditions are shared: `current`, `current+index`
(control), `serialized`, `serialized-late` (kept only as a reference row), `horizon`; and the new `batch`
(strategy A of `postgres-batch-publication.md`) with the in-process publisher, `N = 100` and `tau = 5 ms` plus an
immediate trigger after a local commit.

### 4.2 Scenarios (same harness, same load, through the public store)

| Scenario | What it shows |
|---|---|
| W1-W4 at saturation | write throughput and latency under the same loads as before |
| R100, R300, R500, R500M, offered load below saturation | the cost in ordinary operation |
| H1-H3: a transaction held in the same shard, another scope, another database | the failure modes measured so far |
| HP1, HP2: the PUBLISHER's transaction held (same stream, another stream) | what a stuck publisher costs writers and delivery |
| B1: publisher stopped for 10 s at 300 tx/s offered, then restarted | backlog size, time to drain, delivery latency during recovery |
| F1: fairness with misaligned actor clocks (C21's scenario) at 80% of capacity | wait per event against the backlog ahead of it; whether the bound of `postgres-batch-publication.md` 3.2.1 holds in practice |
| S1: sensitivity of `N` in {10, 100, 1000} and `tau` in {trigger only, 5 ms, 50 ms}, only if the first table warrants it | latency against cost of the knobs |

### 4.3 Measurements per variant and scenario

- throughput (committed transactions and events per second) and write latency p50/p95/p99;
- end-to-end latency from the intended start of the write to delivery, p50/p95/p99, and its split into write latency
  and commit-to-delivery wait; the polling reader's idle floor reported next to it, so polling is not credited to
  the scheme;
- backlog: pending rows over time, maximum, and the rate at which they drain (events per second) after a stall;
- lock waits: the publisher's wait for the stream row and each writer's wait for its locks;
- storage and operation: table and index bytes per event (`pg_total_relation_size`), dead tuples and autovacuum
  runs (`pg_stat_user_tables`), WAL bytes per event (difference of `pg_current_wal_lsn`), connection use;
- omissions: every run counts committed-and-never-delivered events and checks recovery by a read from zero, as
  before. A run of `batch` that omits an event fails.

### 4.4 Configuration, volume, repetitions

PostgreSQL 17.6 in Testcontainers on Colima as before, `fsync=on`; 12 writers; 5 s per run for the saturation and
operating-load scenarios; B1 runs 10 s of stall plus the recovery; 3 repetitions; the host load average gate
before each variant (below 3.0), recorded in a progress file kept with the raw output in `evidence/`. A table of
bytes per event needs a fixed volume, so it is taken after a run of `10^6` events on a separate fixed-size
workload, reported with its own configuration.

### 4.5 Decision rules (to be agreed BEFORE running)

The experiment is only informative if the questions are fixed first. Proposed questions, with the thresholds left
to the owners:

1. Correctness: the contract suite and the stress pass with no omission, duplicate or deadlock. If not, stop.
2. Writer cost at moderate load (R300): is write p95 within an agreed factor of `current`?
3. Delivery cost: is end-to-end p95 below an agreed bound at R300 and R500 with the chosen `tau`?
4. Failure containment: with a held publisher, are writers unaffected, and is the stall confined to its stream?
5. Recovery: does a 10 s stall drain within an agreed time at the chosen `N`?
6. Cost: are bytes per event, WAL per event and dead tuples within an agreed multiple of `current`?

Hypotheses, stated as hypotheses and not as expectations to confirm: writes cost about what `current` costs;
delivery latency is about the publisher cadence plus one batch; storage and WAL grow because every event row is
written twice; a foreign long transaction does not delay publication. Any of them may be false and the experiment
reports it either way.

## 5. Proposed delivery plan (for review; slices stay small and chained)

| Slice | Content | Gate |
|---|---|---|
| 1 | `contract.md`, this suite's specification and the portability evaluation (documentation only) | review of the contract |
| 2 | `JournalPosition`, `Page` and the typed errors in `persistence`, the conformance package and its unit-lane harness for `testkit`, with the negative controls | public-contract gate |
| 3 | PostgreSQL adapter strategy chosen after section 4, with its migration and the Postgres-lane run of the suite | data-migration gate |
| 4 | `OffsetStore` bytes value and the runner (lag from event timestamps, serialized cursors), together with #93 | #93 coordination |
| 5 | deprecate `GetShardEvents` and `ShardOffsets` and update #329 and the #95 matrix with the guarantees actually demonstrated | none |

Slices 2-5 are not authorized by this document.
