# Portability of the journal contract (#332): Postgres, Oracle, Cassandra

Status: PROPOSED evaluation, revision 3 (follows `contract.md` revision 3: observable atomicity, chains of reads from any frontier, `ValidateAdvance`, fairness independent of clocks; earlier revision 2 covered outcomes and idempotent retry, retention floor,
cursor bound to the journal instance and generation, progress with CAS and an epoch). No adapter beyond PostgreSQL is implemented or prototyped; "could comply" below is a
design argument from official documentation, not a demonstration. Nothing here approves an SPI or a migration.

The contract being evaluated is `contract.md` (G1-G10). The question per store is: how would each invariant be
preserved, and which one forces the contract to change?

## 1. Evidence and its quality

Sources are official documentation only: postgresql.org/docs/17, docs.oracle.com (Oracle Database 19c) and
cassandra.apache.org/doc. The page-reading tool returned some pages as quotations and others as summaries, so every
fact carries its status:

- **Q** = quoted verbatim by the reader; **P** = paraphrased by the reader (the page was summarized, not quoted);
  **NV** = not verified (the reader could not read the text, or answered from general knowledge). NV facts are not
  used to support a claim; they are listed so they get checked before an adapter is built.

### PostgreSQL 17

| Fact | Status | Source (postgresql.org/docs/17/...) |
|---|---|---|
| "the value obtained by `nextval` is not reclaimed for re-use if the calling transaction later aborts." and "transaction aborts or database crashes can result in gaps in the sequence of assigned values." | Q | functions-sequence.html |
| The sequence page says nothing about how the order in which sessions obtain values relates to commit order | Q (absence) | functions-sequence.html |
| "Skipping locked rows provides an inconsistent view of the data, so this is not suitable for general purpose work, but can be used to avoid lock contention with multiple consumers accessing a queue-like table." | Q | sql-select.html (locking clause) |
| With `ORDER BY` and a locking clause at READ COMMITTED, rows can come back out of order: "This is because ORDER BY is applied first." | Q | sql-select.html |
| "All transaction IDs less than xmin are either committed and visible, or rolled back and dead." / `xid8` "does not wrap around during the life of an installation" | Q | functions-info.html (snapshot functions) |
| NOTIFY: "the notify events are not delivered until and unless the transaction is committed"; messages from different transactions are delivered in commit order; if the queue is full "transactions calling NOTIFY will fail at commit"; only clients that already ran LISTEN receive the event (no persistence for absent listeners) | Q / Q / Q / P | sql-notify.html |
| Logical slots: "Replication slots persist across crashes and know nothing about the state of their consumer(s). They will prevent removal of required resources even when there is no connection using them."; "A logical slot will emit each change just once in normal operation."; after a crash the slot "might return to an earlier LSN", so clients must tolerate repeats | Q | logicaldecoding-explanation.html |
| Deadlocks are detected and one transaction is aborted; the advice is to acquire locks on multiple objects in the same order in every transaction | P | explicit-locking.html |
| Advisory locks: session-level ones are held until released or the session ends and ignore transaction rollback; transaction-level ones release at the end of the transaction | P | explicit-locking.html |
| SERIALIZABLE and REPEATABLE READ: applications must be prepared to retry on SQLSTATE 40001 | P | transaction-iso.html |
| `idle_in_transaction_session_timeout`: "A value of zero (the default) disables the timeout."; it exists so idle sessions "do not hold locks for an unreasonable amount of time", and an open transaction "prevents vacuuming away recently-dead tuples" | Q | runtime-config-client.html |
| `synchronous_commit = off` can lose "some recent allegedly-committed transactions" on a crash without inconsistency | Q | runtime-config-wal.html |

### Oracle Database 19c

| Fact | Status | Source (docs.oracle.com/en/database/oracle/oracle-database/19/...) |
|---|---|---|
| Advanced Queuing: "enqueues and dequeues can be incorporated in database transactions without requiring distributed transactions"; messages can be sorted "by priority, enqueue time, or commit time"; a consumed message is kept "only if a retention time is specified" | Q | adque/aq-introduction.html |
| "Oracle Database always enforces statement-level read consistency, which guarantees that data returned by a single query is committed and consistent for a single point in time."; "A reader never blocks a writer."; "A writer never blocks a reader." | Q | cncpt/data-concurrency-and-consistency.html |
| Deadlocks are detected and resolved by rolling back one statement ("releasing one set of the conflicting row locks") | Q | same |
| Serializable: the database errors when a serializable transaction touches data changed by a transaction that committed after it began (ORA-08177) | Q | same |
| Sequences: values are independent of transactions and are not rolled back; with `CACHE` values can be lost on failure, creating gaps; `ORDER` guarantees request order, mainly relevant in RAC | P | sqlrf/CREATE-SEQUENCE.html |
| `ORA_ROWSCN` is a conservative upper bound of the last change's SCN, tracked per block by default and per row only with `ROWDEPENDENCIES`, and not guaranteed to be the exact commit SCN | P | sqlrf/ORA_ROWSCN-Pseudocolumn.html |
| `FOR UPDATE` has `NOWAIT`, `WAIT n` and `SKIP LOCKED`; it cannot be combined with the row-limiting clause (`FETCH FIRST`), with `DISTINCT`, set operators, `GROUP BY` or aggregates | P | sqlrf/SELECT.html |
| Exact `SKIP LOCKED` wording and its ordering behavior | NV | sqlrf/SELECT.html |
| COMMIT durability options (`WRITE WAIT/NOWAIT`, `IMMEDIATE/BATCH`) and the claim that `NOWAIT`/`BATCH` can lose an acknowledged commit | **NV** (the reader answered from general knowledge) | sqlrf/COMMIT.html |

### Apache Cassandra

| Fact | Status | Source (cassandra.apache.org/doc/latest/...) |
|---|---|---|
| "By default, all operations in the batch are performed as logged, to ensure all mutations eventually complete (or none will)." | Q | cassandra/developing/cql/dml.html |
| "Batches are not a full analogue for SQL transactions."; "operations are only isolated within a single partition" | Q | same |
| "If the UNLOGGED option is used, a failed batch might leave the batch only partly applied."; "There is a performance penalty for batch atomicity when a batch spans multiple partitions." | Q | same |
| Lightweight transactions use Paxos for linearizable compare-and-set; Cassandra favors availability and is eventually consistent; a batch is eventually applied to all its tables or none, with a replayable batchlog | P | cassandra/architecture/guarantees.html |
| `counter` updates are not idempotent: a retried update after a timeout can over-count; a counter table can contain only counters | P | cassandra/developing/cql/types.html |
| `gc_grace_seconds` default is 864000 (ten days); a node down longer than it can resurrect deleted data ("zombie data") unless repair finished within the grace period; TTL expiry becomes a tombstone | P | cassandra/managing/operating/compaction/tombstones.html and cql-commands/create-table.html |
| CDC: enabled per table; segments hard-linked into `cdc_raw`; a consumer must read only up to the durable offset in the `_cdc.idx` file and delete the links; writes to CDC tables are rejected when the CDC space limit is reached | P | cassandra/managing/operating/cdc.html |
| Accord (general-purpose multi-partition transactions): no user documentation found at the official docs; the CEP-15 page lists its state as "Accepted" and its last modification as early 2023 | P (absence in docs; wiki state) | cwiki.apache.org CEP-15 |
| `timeuuid` is a version 1 UUID; its ordering and uniqueness across nodes and clocks | **NV** | cassandra/developing/cql/types.html |
| QUORUM write plus QUORUM read overlap guarantee | **NV** (verbatim not obtained) | cassandra/architecture/guarantees.html |
| The guarantee-by-guarantee reasoning below for Cassandra | design argument, no prototype | |

## 2. What each store gives, against what the contract needs

| Concern | PostgreSQL | Oracle | Cassandra |
|---|---|---|---|
| **Atomicity available, and its limit** | Multi-table, multi-row transaction: all or nothing, durable at commit (with `synchronous_commit=on`; `off` can lose recent commits but stays consistent, Q) | Same model (multi-table transaction). Durability options at COMMIT exist; their exact semantics are NV | Single partition: atomic and isolated. Several partitions: a logged batch "eventually completes (or none will)" and is NOT isolated across partitions (Q). No cross-partition ACID without Accord, which is not documented for use |
| **Persistence of pending, recovery** | A pending marker committed with the event (same transaction) survives a crash; recovery is "a later publisher finds it" | Same | Pending evidence must live in the SAME partition as the event to be atomic with it. A stream-level pending index needs a cross-partition write and is only a discovery aid that an anti-entropy scan can rebuild (see 4.1); it is never the authoritative evidence. Recovery relies on idempotent re-application keyed by the event identity |
| **Per-entity order** | Existing revision-row lock serializes an entity's writers; a commit of `n+1` is after the commit of `n`; a snapshot that sees `n+1` sees `n` | Same via row locks; readers never block writers and see statement-level consistent data (Q) | Clustering order inside the entity partition (P); but two cross-partition batches may complete in any order, so the publisher must check the entity's published sequence before publishing `n+1` |
| **Safe assignment of positions** | Sequences are not usable as positions (non-transactional, gaps, no commit-order link, Q). A publisher that assigns positions under a stream row lock is safe; a horizon (`xmin`) is safe but cluster-wide; per-write serialization is safe but costly (experiments, `design.md`) | Same reasoning: sequences are non-transactional and cached (P); `ORA_ROWSCN` is an approximate upper bound and cannot be a position (P); a stream-row-lock publisher works the same way | A position needs a single logical assigner per stream: a linearizable claim of a position range (LWT/Paxos, P) plus a barrier (`committed_through`) that the reader may not pass |
| **Publication and coordination** | One publisher transaction per stream at a time (row lock); publishers of other streams run in parallel; `SKIP LOCKED` only avoids idling (Q: "inconsistent view") and gives neither order nor recovery | Same pattern. AQ can enqueue in the same transaction as the write (Q) and sort by commit time (Q), a possible Oracle-native strategy, but AQ dequeues consume messages and its replay/browse model was not evaluated | Publisher takes a lease through an LWT row; a claimed range carries a durable manifest so a successor can finish it; entries are written idempotently; the barrier advance is an LWT. More moving parts than the SQL stores |
| **Retention** | `DeleteEvents` fails with `ErrRetentionPending`, deleting and publishing nothing, while pending rows are affected; a retention floor makes removed unread events detectable (G8) | Same | TTL and tombstones cannot be told to wait for publication: a TTL on events or on pending marks can silently delete a pending event (P). The adapter must not use TTL on events or must make the pending-mark TTL exceed the documented publication bound; deletes need repair within `gc_grace_seconds` (P) |
| **Operational requirements** | Publisher sessions with timeouts; `idle_in_transaction_session_timeout` is off by default (Q); autovacuum for the hot pending rows; READ COMMITTED for the publisher | Statement-level read consistency means no snapshot-too-old risk for the short publisher statements (not verified for long reads); ORA-08177 if SERIALIZABLE is used | QUORUM (or LOCAL_QUORUM) reads and writes for the stream; regular repair; no counters as allocators (not idempotent, P); batch size discipline; clock skew affects last-write-wins |
| **Limits that decide fitness** | One publisher transaction at a time per stream bounds a stream's publication rate; a stuck publisher stalls its stream (not others) | Same | Publication latency is several round trips including Paxos; the pending-discovery path and the LWT cost have not been measured |

## 3. How each invariant would be preserved

| Guarantee | PostgreSQL (batch publication, `postgres-batch-publication.md`) | Oracle (same pattern) | Cassandra (design argument) |
|---|---|---|---|
| **G1** atomic persistence, outcomes, idempotent retry | the event row and its pending state are one row, one transaction; a commit that raised an error after being sent is an UNKNOWN outcome, resolved by the idempotent replay check under the entity row lock | same | the event and its pending evidence are ONE single-partition write (atomic and isolated, Q); a batch spanning entities cannot be atomic and is rejected with `ErrUnsupportedBatch`; a write timeout is an unknown outcome; the clustering key `(sequence)` makes the identical replay naturally idempotent |
| **G2** eventual availability | the publisher's transaction commits batches; documented condition: a publisher runs and its transaction is not stuck beyond its timeout | same | lease holder publishes; condition: lease acquisition works, QUORUM available, repair current |
| **G3** safe advance (chains of reads, no stall, monotonic reads) | positions are assigned only inside a publisher transaction holding the stream row lock, increasing across transactions; a late-committing event simply waits for the next batch and gets a higher position; every read of a bounded page advances because pages are cut in position order | same | readers read only up to `committed_through`; only the claim holder writes entries below it; a hole is impossible below the barrier; reads at QUORUM or stronger give monotonic reads |
| **G4** stable order | a position never changes once committed | same | entries are immutable at `(stream, position)` |
| **G5** per-entity order | batch selection keeps each entity's pending prefix; the order key is monotone in the sequence | same | the publisher publishes `n+1` only after `n`, checked against the entity's published sequence |
| **G6** at-least-once | reads do not consume; consumer commits | same | same; reads at QUORUM |
| **G7** isolation, binding to the instance | the cursor carries the journal instance id (a row created with the schema), the generation, the scope and the shard | same | the instance id and generation live in a metadata partition; the partition key includes the stream |
| **G8** retention barrier, detectable loss | `DeleteEvents` checks under the entity row lock whether pending rows `<=` the sequence exist and returns `ErrRetentionPending` without deleting; the per-stream retention floor is the newest deleted position | same | explicit deletes only, no TTL on events; a retention floor kept in the stream state; a cursor older than it fails with `ErrCursorOutsideRetention` |
| **G9** concurrency, aborts | stream row lock serializes publishers; a rolled-back publisher assigns nothing | same | LWT claims serialize publishers; crash leaves a manifest to complete |
| **G10** age, latency, backlog | publication backlog from the pending index (count and oldest age); consumer backlog by counting positions after the cursor, which may be expensive and may be reported as unknown | same | the pending index partition; consumer backlog generally `unknown` |

Oracle's column is identical to PostgreSQL's by design: both offer multi-row transactions, row locks and
non-blocking consistent reads. That is an argument about the mechanism's ingredients, not a tested adapter.

## 4. What this review changed, and what still forces a decision

### 4.1 Corrections applied after the owner review

1. **G1 is NOT weakened for Cassandra.** The first revision allowed a store with only "eventually all or none"
   cross-partition atomicity to claim a weaker G1. That is withdrawn, and the contract no longer says "eventually":
   it requires OBSERVABLE atomicity, so that no read ever sees a partially committed batch. A logged batch across
   partitions is not isolated (Q), so a read can see part of it; such a batch is therefore unsupported. A Cassandra
   adapter meets G1 only with an event and its pending evidence in the SAME partition (atomic and isolated, Q), and
   rejects with
   `ErrUnsupportedBatch` any batch it cannot make atomic (a multi-entity batch spans partitions). A stream-level
   index of pending events, if used, is only a discovery aid: the authoritative evidence is in the entity's
   partition and an anti-entropy scan must be able to rebuild the index, so a late or missing index entry delays
   publication (G2) and never loses an event.
2. **Unknown outcomes exist in every store.** A Cassandra write timeout, a Postgres connection lost after the
   commit was sent, an Oracle session killed around the commit: each is an unknown outcome. The identity of an event
   `(scope, persistence id, sequence)` and the idempotent replay rule (G1) resolve all of them the same way.
   Cassandra's counters are excluded from any allocation role because their updates are not idempotent (P).
3. **Retention loss must be detectable.** Postgres and Oracle keep a retention floor in stream state. A store
   whose positions expire by themselves (a change feed reclaimed by space, TTL) raises `ErrCursorOutsideRetention`
   when the cursor is older than the oldest retained position; a TTL on events is not allowed for an adapter that
   cannot keep the floor exact.
4. **Cursors are bound to the journal instance and the generation.** Every store needs a stored, unique journal
   identifier; a restore from backup, a recreation or a re-publication of history advances the generation.
5. **Empty pages.** A change-feed source (Cassandra CDC, Oracle redo or Postgres logical decoding) moves over
   changes that are not Urd events, so a page can be empty while the cursor advances. The contract no longer
   promises that an empty page leaves the cursor unchanged; G3 asks for no stall instead.
6. **Progress needs compare-and-set and an epoch.** Every store has a conditional update: a Postgres
   `UPDATE .. WHERE revision = $1`, the same in Oracle, and an LWT `IF revision = ?` in Cassandra (Paxos, P). The
   fencing token of an ownership mechanism is separate and belongs to #93. A conditional update does not stop a
   regression: that is `ValidateAdvance` (next item).
7. **A comparison can stay inside the adapter.** `ValidateAdvance` (does `to` continue `from`?) needs each adapter to
   relate its own frontiers: two integers in Postgres and Oracle, a (bucket, position) pair for a Cassandra log, a
   per-source vector for a change feed. The core sees only an error, so a composite cursor stays admissible.
8. **Fairness must not use clocks.** Publication order cannot be derived from event or write timestamps. In
   Cassandra that rules out ordering a publication queue by write timestamp or `timeuuid` (writer clocks, last write
   wins, P); the claimed-range log must order by an allocator the publisher owns. `timeuuid`'s cross-node behavior is
   unverified (NV), another reason to keep it out of the ordering.

9. **The stream of an entity is recorded with the entity.** A Postgres or Oracle revision row, or a Cassandra entity
   partition, holds the stream key fixed by the first event; an event under another key is `ErrShardMismatch`.
   Without it G5 cannot hold, because streams carry no order between them. The key is a logical stream identity and
   does not follow where an actor is placed (decisions D13).

### 4.2 What still forces the contract to be stated carefully

1. **G3 needs read consistency stated.** On an eventually consistent store a cursor is a safe point only if the next
   read is at least as fresh as the one that issued it (monotonic reads). The Cassandra adapter must read at QUORUM
   or stronger; the conformance harness cannot verify that on a single node, so it is an adapter documentation duty.
2. **G2 is conditional.** Every store needs a running publisher and has documented ways to stall. Each adapter
   documents its own conditions and exposes its publication backlog.
3. **The cursor cannot be assumed totally ordered across adapters.** A Cassandra cursor may be a pair (bucket,
   position) and a CDC design a per-node vector. This is why `Compare` is not in the contract and why G4 asks only
   for a safe frontier.
4. **`Equal` is canonical-form equality and says nothing about pending events** (`contract.md` section 4).

No guarantee found makes the contract unimplementable on these three stores in principle. The Cassandra column
remains the least certain: it depends on two NV facts (QUORUM overlap wording, `timeuuid` behavior), on the Accord
question (not documented), on the stream-index rebuild argument above, and carries the highest complexity.
Portability is claimed for PostgreSQL (demonstrated for the older mechanisms by the experiments, designed for batch
publication), argued for Oracle, and only sketched for Cassandra.

## 5. Strategies per store (not mandated)

| Store | Candidate strategies | Notes |
|---|---|---|
| PostgreSQL | batch publication (stream row lock); per-write serialization; xid horizon; logical decoding | measured: serialization and horizon (`design.md` sections 10-13); batch publication is to be measured; logical decoding gives commit-ordered changes through a slot that persists across crashes and "will prevent removal of required resources" (Q): WAL retention is its operational risk |
| Oracle | batch publication (stream row lock); Advanced Queuing with commit-time ordering | AQ replay semantics unverified |
| Cassandra | LWT-claimed publication log with a barrier; CDC as a source | needs prototype and the NV facts checked before any claim |
