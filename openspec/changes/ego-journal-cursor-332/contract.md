# Portable journal contract (#332) — framework level

Status: PROPOSED, revision 6 (the owner's resolutions of D1-D15 recorded in `decisions.md`: semantics approved first, publication-backlog visibility mandatory, per-adapter cursor limit with input validation, consumer-side advance validation, an assignment algorithm that is stable and versioned; the SPI and the migration remain NOT approved). Revision 5: the stream identity of an entity is stable and separate from actor placement; revision 4 added: replay equality defined, progress epoch renamed, enforcement limit of `ValidateAdvance`, time-based start). Revision 3 applied the owner review of #338. Design only: no production code, SPI, schema or migration
changes. The SPI is NOT approved. The public-contract gate and the data-migration gate are PENDING and nothing here
approves them. Names and types are open; the semantics are what is under review.

Scope: what Urd requires of ANY journal adapter so that a projection never permanently misses a persisted event,
and so that a writer can always tell what happened to a write. How an adapter achieves it (locks, transaction ids,
batch publication, change feeds, conditional writes) is the adapter's business and is NOT part of this contract.
`portability.md` checks whether Postgres, Oracle and Cassandra could meet it; `postgres-batch-publication.md` is
one strategy; `conformance-and-experiment.md` is how it is tested; `design.md` keeps the experiments.

## 1. Vocabulary

| Term | Meaning |
|---|---|
| **stream** | the events of one `(scope, shard)` of one journal instance. A cursor, a read and a guarantee are per stream |
| **journal instance** | one physical journal (one database, keyspace or schema). Two instances can carry identical scope and shard labels and are different streams |
| **persisted** | `WriteEvents` returned success. The event is durable and visible to entity-level reads (`ReplayEvents`, `GetLatestEvent`) |
| **available** (published) | the event can be returned by a stream read (`ReadStream`). Becoming available is the adapter's step after persisting |
| **pending** | persisted and not yet available |
| **event identity** | `(scope, persistence id, sequence number)` |
| **JournalPosition** | an opaque cursor into one stream; separate from `egopb.Event.Timestamp` |
| **page** | what one stream read returns: some available events (possibly none) and the cursor to pass to the next read |
| **progress** | the committed cursor of one consumer of one stream, held by a store that is not necessarily the journal |

The old contract conflated "persisted" and "available", and used the event timestamp as the cursor. A timestamp is
stamped by the writer before its transaction commits, so it says when an event was created, not when it became
readable. That is the defect of #332.

## 2. Guarantees

Each guarantee is phrased so a conformance test can observe it without knowing the adapter.

### G1 Atomic persistence, and a writer that can always resolve the outcome

**Three outcomes.** Every `WriteEvents` ends in exactly one of:

- **success** (`nil`): the whole batch is persisted and the obligation to make it available is durable;
- **definitive rejection**: the adapter knows nothing was persisted, and the batch left no event and no evidence that
  could later make a phantom event available. Typed and classified (for example the existing
  `ConflictError`, `ErrInvalidScope`, `ErrInvalidPrecondition`, `ErrPreconditionScope`, plus the new
  `ErrSequenceOrder`, `ErrShardMismatch`, `ErrIdentityConflict`, `ErrUnsupportedBatch` below);
- **unknown outcome**: the adapter cannot tell whether the commit happened (cancellation or timeout around the
  commit, a lost connection, a lost response). Typed as `ErrOutcomeUnknown`, wrapping the cause.

An error or a cancellation does NOT prove a rollback: the write may have committed and the answer been lost. An
adapter returns a definitive rejection only when it can prove nothing was committed (for example a precondition
failure, or a cancellation before any commit was issued). **Every error that is not classified as definitive MUST
be treated by the caller as unknown** (fail safe).

**Observable atomicity.** No read of any kind observes a partially committed batch. An entity-level read
(`ReplayEvents`, `GetLatestEvent`, `PersistenceIDs`) sees all the events of a batch or none of them, whatever
outcome the writer was told. The "nothing was left" statement belongs to a **definitive rejection** only: after an
**unknown outcome** the batch is, or will be, either persisted entirely or not persisted at all, and no read can
distinguish a third state; until the retry resolves it, a read may see the batch absent and later present, never
partly. After a success the batch is persisted entirely.

Availability is a separate, later step and is not partial commitment. The events of a persisted batch may become
available in different pages and at different times (G2, G5); each of them is already persisted and none can be
lost, and the contract promises their order, not their simultaneity.

An adapter whose store cannot make a given batch atomic in this observable sense MUST reject it with
`ErrUnsupportedBatch` (definitive) instead of weakening the guarantee, and declares which batches it supports (a
multi-entity batch is the usual case). The contract does not relax G1 for any store.

**Identity and idempotence resolve the retry.** The retry of an unknown outcome is the same call with the same
batch. Therefore:

1. A batch whose every event identity is already persisted with the same content is an **idempotent replay**: it
   returns success with no new effect and no second publication. "Same content" compares the payload, the manifest,
   the metadata and the encryption fields, byte for byte. It does NOT compare the event's `Timestamp`: a retry that
   rebuilt its batch carries a new clock stamp, and the stamp that was persisted stays. The replay check comes BEFORE
   the precondition, so the retry of a conditional write that did commit succeeds instead of reporting a revision
   conflict.
2. An event identity that already exists with different content is a definitive `ErrIdentityConflict`, never a
   silent skip. Overlap that is neither an identical replay nor disjoint is also `ErrIdentityConflict`.
3. Otherwise the batch is new and is judged by its precondition and by G5.

A caller facing an unknown outcome retries the identical batch until it gets success or a definitive rejection. A
definitive rejection after an unknown outcome means the earlier attempt did not commit (or conflicts with something
that did); the caller then reads the entity to decide.

**Retry the batch you hold, unchanged.** A batch recomposed between attempts (further events merged in) overlaps the
persisted one only partly and is rejected as `ErrIdentityConflict`; that rejection does not mean the first part was
lost. The engine's writers must retry the request they hold, not rebuild it (an audit item).

**Giving up is not learning.** A writer that stops retrying has not learned the outcome. Before it writes that entity
again it must read the entity's persisted state (`ReplayEvents`, `GetLatestEvent`) and compare the identities and the
content it attempted; it may not assume either outcome. This changes the current behavior of unconditional writes,
which skip duplicate identities silently whatever their content: public-contract gate.

### G2 Eventual availability

Every persisted event that is retained becomes available, under operating conditions the adapter documents (for
example: its publisher is running, its database is reachable, no transaction it depends on is stuck beyond a stated
bound). "Eventually" is bounded only by those documented conditions; the contract does not promise a latency. The
adapter exposes whether it is meeting them (G10).

**Fairness.** Availability must not depend on the event's `Timestamp` or on any clock of the writer. Under
continuous load that stays below the adapter's publication capacity, how long a pending event waits is bounded by
the backlog that was ahead of it when it was persisted, never by its timestamp: an entity whose actor clock runs
hours or years ahead cannot be kept pending indefinitely behind newer writes of other entities. An adapter states
the bound and the selection rule that gives it, and the conformance suite tests it with misaligned clocks.

### G3 Safe advance: successive reads deliver every retained event after the cursor

A read with a limit does not return all pending or available events, and the contract does not ask it to. What it
asks is about the chain of reads a consumer actually performs.

**The frontier.** A cursor `c` issued by a read, or the zero cursor, stands for a **frontier** in the stream:
`before(c)` is the set of events that lie at or before it in stream order when it was issued (for the zero cursor,
the empty set). Everything else that is retained is `after(c)`: the events available beyond the frontier **and the
events that were not available yet when `c` was issued**, whether still pending or committed late. A late-committed
event that has not received a position is therefore always `after` every cursor already issued: that is the whole
point of the guarantee.

A **chain** starts at `c` and each next read starts at the cursor the previous read returned, with any limit `>= 1`.
Then:

- **No skipped event after the frontier:** every retained event of `after(c)` is returned by some read of the chain,
  including events that become available long after `c` was issued. The chain is not required to return events of
  `before(c)`; a read may or may not repeat them (G6). Equivalently: committing the cursor of a page never discards
  an event that was not returned by the chain, nor an event still waiting for its place.
- **No stall:** a chain cannot loop forever without progress while an available, undelivered, retained event of
  `after(c)` exists: each read either returns an event or returns a cursor that moves the chain forward.
- **Monotonic reads:** a read from a cursor is at least as fresh as the read that issued it, so an eventually
  consistent store cannot hand back an older view after a newer one (an adapter on such a store must say which
  consistency it reads at).

This is the guarantee the timestamp offset breaks.

### G4 Stable order, per stream

The available events of one stream have one fixed order, the **stream order**: the order in which a chain delivers
them. Re-reading from a cursor a read issued yields the events previously returned after that cursor in the same
relative order, possibly followed by newer ones; events removed by retention are the only exception.

The order is defined per stream. The contract says nothing about the order between streams, between independent
entities, or about its relation to wall-clock time or to commit order.

A cursor may be composite (a bucket and a position, a token, a vector of sources). That is admissible if it
represents a **safe frontier**: the events delivered by the chain up to it are exactly what lies before the
frontier, and nothing that becomes available later can fall before it. The core never compares two cursors; the
adapter alone knows how its frontiers relate.

### G5 Per-entity order, enforced for every write

For one entity, availability order is sequence-number order. If sequence `n+1` of an entity is available, every
retained sequence `<= n` of that entity is available and appears earlier in the stream order. Several sequences
written by one `WriteEvents` are ordered the same way.

**This holds for every write, whatever its precondition.** `Unconditional()` means "no expectation about the
current revision", not "no order invariant". The store enforces the entity's sequence protocol:

| Rule | Accepted | Rejected |
|---|---|---|
| Within a batch, one entity's sequences | strictly increasing | equal or decreasing |
| Against the entity's **revision** (the highest sequence ever persisted for it, which retention never lowers) | every new sequence is greater than the revision | a new sequence `<=` the revision: `ErrSequenceOrder` (definitive) |
| Gaps (a sequence `> revision + 1`) | accepted; the skipped numbers are permanently unavailable: a sequence below the revision can never be persisted later | |
| An identity that already exists | same content: idempotent replay (G1) | different content: `ErrIdentityConflict` |
| The event's stream (`Shard` field) | the same stream as every earlier event of the entity (the first persisted event fixes it) | another stream: `ErrShardMismatch` (definitive; the name is open) |
| `ExpectGenesis` / `ExpectRevision(r)` | additionally require the entity's revision to be absent / equal to `r`, as today | |

**The stream of an entity is fixed for its lifetime.** Streams are per `(scope, shard)`, and G4 gives an order per
stream only. If an entity's events could land in two streams, a consumer reading both could see `n+1` before `n`, and
G5 would be false. The contract therefore requires a **stable stream identity** per entity: it is fixed when the
entity's first event is persisted and does not change afterwards. It must not depend on where an actor runs: the
placement of actors may change with the cluster's topology, and the stream identity may not follow it. Streams are
**logical partitions owned by Urd** (decided, D13): the number of streams and the **assignment algorithm are stable
and versioned**, recorded with the journal, so that under one version the same persistence id always maps to the same
stream on every node and in every release; changing either is an explicit re-homing migration, not designed here. How
the algorithm is defined and how the number of streams is chosen are open (D13). Today the `Shard` field is filled
from the actor system's partition, which does not meet this requirement across a change of partition count. For an
existing journal the number of streams is NOT inferred from the current cluster: the historical distribution is
audited first and an adoption is proposed from that evidence (D13). Entities that already span streams are part of
that audit.

Gaps are accepted because retention and conditional writes already produce them, and because the guarantee is
about order, not density: a missing number is simply never available. What a store must not do is accept a late
number below the revision, which is exactly how an entity could be published out of order.

The engine's writers already follow this: one actor per entity writes consecutive sequences, including when it
uses `Unconditional()` and in the saga actor. A caller that persists sequences out of order today gets silent
acceptance; under this rule it gets `ErrSequenceOrder`. That is a behavior change: public-contract gate, and an
audit of every caller (the event-sourced actor, the saga actor, the tenant-adoption migration).

### G6 At-least-once delivery

A read never consumes. Progress is explicit: a consumer commits the cursor of a page after processing it. A failure
before the commit makes the next read start from the previous committed cursor and return the same events again
(G4). The contract never promises exactly-once; consumers are idempotent on the event identity.

### G7 Isolation and binding to the stream instance

A read of a stream returns only events of that `(scope, shard)`. `Unscoped()` and a tenant scope never share a
stream.

A cursor is **bound to the stream instance**, not to labels. The binding identifies: the adapter kind, the **journal
instance** (a unique identifier created with the journal and stored in it), the **generation** of that journal
(advanced whenever positions are invalidated: a restore from backup, a recreation, a re-publication of the whole
history, any operation that reassigns positions), the scope and the shard. Two databases with the same labels have
different journal instances; a restored or rebuilt journal has a new generation.

A read given a cursor whose binding does not match fails closed with a typed error and returns nothing:

- another stream, scope, shard, journal instance or adapter: `ErrCursorMismatch`;
- the same journal at an older generation: `ErrCursorInvalidated` (the consumer must restart from zero).

**The zero cursor is the exception:** it is unbound and means "the beginning of the retained history of whichever
stream it is presented to". That is a statement about the cursor, not about completeness: after retention has
removed events, reading from zero returns only what is still retained, so it does NOT guarantee that a projection
can be rebuilt in full. Whether a rebuild is complete depends on the retention policy, which this contract does not
set (G8).

### G8 Retention barrier and detectable loss

**Pending barrier.** `DeleteEvents(scope, id, toSequence)` is all or nothing with respect to pending events. If any
event of that entity with sequence `<= toSequence` is still pending, the call fails with `ErrRetentionPending`: it
deletes nothing (no partial deletion) and it publishes nothing as a side effect of the deletion. The caller retries
later; by G2 the pending events become available, after which the deletion can proceed. After a `DeleteEvents` that
returns success, no event of that entity with a sequence `<= toSequence` remains. A flow that must complete the
deletion (an erasure) retries on `ErrRetentionPending` and its completion time includes the publication delay; that
dependency is documented, not hidden.

Why the barrier exists: an event deleted before it is published can never reach even a perfectly current consumer,
so "persisted implies consumable" would depend on retention timing.

**Detecting that a cursor fell outside retention.** Retention can still remove events that are available but that a
slow consumer has not read. That must be loud, not silent. The adapter keeps, per stream, a **retention floor**: the
newest position among events removed by retention. A read from a cursor that precedes the floor means at least one
event after the cursor was removed before the consumer returned it, and fails with `ErrCursorOutsideRetention`
instead of skipping over the hole. The comparison happens inside the adapter (the core still never compares
cursors) and is exact: the floor is the maximum position removed, so the error is raised if and only if an unread
event was removed. Stores whose positions themselves expire (a change feed whose log is reclaimed by space, a TTL)
raise the same error when the cursor is older than the oldest retained position. The consumer's reaction is to stop
advancing and report; it never skips silently. Rebuilding from the zero cursor is an explicit decision.

**First delivery is retention only** (decided, D3): an explicit error and no partial deletion, as above. Retention
and erasure both remove events but should not look the same to a consumer, which must not process erased data and so
should continue past an erased hole while it must stop on retention loss. That distinction, and any reason parameter
on `DeleteEvents`, are **deferred**: erasure first needs its effect on projections defined (open, `decisions.md`
O2).

### G9 Concurrent writers, aborts, retries and out-of-order publication

Any interleaving of concurrent writes, aborted writes, unknown-outcome retries, publisher retries and publisher
crashes preserves G1-G8. Events may become available in a different order than they were persisted or than their
timestamps, and that is allowed; G3 and G5 are what bound it.

### G10 Age, latency and backlog are three different measures

The cursor carries no time meaning and no lag is derived from it. The old `now - timestamp of the last event` is an
**age**, not a lag: it grows while nothing is wrong and nothing is pending. The contract separates:

| Measure | Definition | Owner of the data |
|---|---|---|
| **Event age** | `now - Timestamp` of an event, a property of the data | the consumer, from the event; informational |
| **Delivery / processing latency** | time from when an event became available (or, lacking that, from when the consumer read the page) to the end of its handler | the consumer; the availability time is an optional adapter capability |
| **Publication backlog** | events persisted and not yet available. **Visibility is mandatory** (decided, D9): at least the age of the oldest pending event or an equivalent signal. An exact count is not required; it may be an estimate or unknown | the adapter, through a required reporter |
| **Consumer backlog** | how many available events lie after a consumer's progress | the adapter, through an optional capability; an estimate or "unknown" is allowed |

A consumer is "caught up" in the sense of section 4 (`Equal` to the observed head), which is NOT a statement about
backlog.

## 3. Explicitly NOT in the contract

| Not required | Why |
|---|---|
| Global order across independent entities, or across streams | G4 fixes one stream order only |
| Dense positions, arithmetic or distance between positions | not every store can number densely; gaps are legitimate; consumers must not compute with cursors |
| A comparison of two cursors (`Compare`) | see section 4; a composite cursor needs only to be a safe frontier |
| Atomic visibility of one write's events | G5 orders them; an adapter may offer atomic visibility but consumers cannot rely on it |
| A bounded publication latency | G2 is conditional on documented operating conditions |
| `Events` empty implies `Next` unchanged | a change-feed source can move over changes that are not Urd events; G3's no-stall covers it |
| A fixed cursor size | to be set only after the tokens of the candidate adapters are measured |
| Any mechanism: transaction ids, WAL, advisory locks, `SKIP LOCKED`, SQL transactions, sequences or counters | adapter strategies; requiring one would exclude stores |
| One publication mechanism for all adapters | the contract states invariants; the adapter chooses how |
| `int64` as the cursor type | see section 4 |
| Retention that waits for consumers | G8 detects loss, it does not prevent it; consumer-aware retention is its own issue |

## 4. The cursor

| Operation | In the contract? | Justification |
|---|---|---|
| **Zero value** | Yes | unbound; "beginning of the retained history" (G7); a projection with no progress starts here and a rebuild returns here |
| **IsZero** | Yes | the runner distinguishes "never consumed" from a committed cursor |
| **Equal** | Yes, with the semantics below | idempotent progress writes, the "reached the observed head" test, conformance checks |
| **Serialization** (`MarshalBinary` / `UnmarshalBinary`) | Yes | progress lives outside the journal and must survive restart, upgrades and process moves; the bytes are opaque to the consumer |
| **Binding validation** | Yes (G7) | adapter kind, journal instance, generation, scope, shard; typed errors |
| **Compare / Before / After** | **No** | the adapter alone relates two frontiers; exposing it would force every adapter to order composite or vector cursors and would invite consumers to treat the order as meaningful. Refusing a regression is the job of the journal adapter's `ValidateAdvance` (section 5), which answers yes or no without exposing an order; stale writers are the job of CAS and deposed owners the job of fencing (section 6) |
| **Arithmetic, successor, distance** | No | undefined for some stores; gaps are legitimate |
| **Text** (`String()`) | Adapter, for logs | stable, no payload, no parseable meaning |
| **Validating an advance** (`ValidateAdvance(from, to)`) | Yes, on the journal side | the adapter answers whether `to` is a valid continuation of `from` (same stream instance and generation, not before `from`, not beyond the published frontier). The comparison stays inside the adapter; the core sees only an error. Section 5 |
| **Mapping from the legacy `int64` offset** | Adapter hook, migration only | one-way `LegacyOffset(int64) -> cursor` for the data-migration gate |

### Equal: what it means, and what it does not

**What it compares.** Two cursors are `Equal` when they denote the same frontier of the same stream instance. The
contract fixes this as equality of the **canonical serialized form**: an adapter MUST serialize each frontier in
exactly one byte string, so byte equality and logical equality coincide. An adapter that cannot canonicalize must
not claim the contract. Conformance checks that two cursors from different routes to the same frontier serialize
identically.

**What it does not prove.** A published head `Equal` to the progress means only that the consumer **reached the
available head as it was observed by that read**. It does NOT mean that publication has finished, that no persisted
event is pending, or that no write happened since. It must not be used as a completeness test. A stream head is a
**published frontier**: it marks how far publication has gone, and it need not be the position of the newest event
that is still retained, because that event may since have been removed by retention.

### Why not `int64`, and the size of a cursor

`int64` would pin every adapter to a single monotone integer per stream and exclude composite cursors, change-feed
tokens, per-node commit-log offsets and vectors, and it tempts consumers into arithmetic. The contract therefore
requires a **bounded, documented size per adapter** and fixes no universal number (decided, D4): the tokens of the
candidate adapters have not been measured. **Each adapter declares its limit and validates every cursor it is
given**: a malformed, truncated, wrong-version or oversized value is rejected with a typed error before any
allocation or query. A design-time estimate for PostgreSQL (version, adapter tag, 16-byte journal instance, 8-byte
generation, scope and shard binding, 8-byte position) is a few tens of bytes; that is an estimate, not a validation,
and a framework bound is to be taken only after real tokens are measured.

## 5. Stream and write operations (proposal, not approved)

The additions to the journal SPI, as methods of a new interface that `EventsStore` would embed (no redeclaration of
`EventsStore`):

```go
// JournalPosition is an opaque cursor into one stream instance. Adapters construct it; consumers store and pass it.
type JournalPosition struct { /* opaque */ }

func (p JournalPosition) IsZero() bool
func (p JournalPosition) Equal(q JournalPosition) bool // canonical-form equality, see section 4
func (p JournalPosition) MarshalBinary() ([]byte, error)
func (p *JournalPosition) UnmarshalBinary(b []byte) error

// Page is what one stream read returns. Events may be empty while Next differs: the source advanced
// over changes that are not Urd events. Whether any given source does that is the adapter's property.
type Page struct {
    Events []*egopb.Event
    Next   JournalPosition
}

// StreamReader is the stream half of the journal SPI; EventsStore embeds it.
type StreamReader interface {
    // ReadStream returns available events of (scope, shard) after the cursor: G3, G4, G5, G6, G7, G8.
    // Errors: ErrCursorMismatch, ErrCursorInvalidated, ErrCursorOutsideRetention, scope errors.
    ReadStream(ctx context.Context, scope Scope, shard uint64, after JournalPosition, limit uint64) (Page, error)

    // StreamHeads returns, per shard of scope, the PUBLISHED FRONTIER as observed by the call: how far publication
    // has gone. It is not necessarily the position of the newest retained event (that event may have been
    // removed), and it never moves backwards. Equal to a consumer's progress it means "reached the observed
    // frontier", nothing more (section 4).
    StreamHeads(ctx context.Context, scope Scope) (map[uint64]JournalPosition, error)

    // ValidateAdvance reports whether `to` is a valid continuation of `from` for this stream: both bound to the
    // same stream instance and generation (ErrCursorMismatch / ErrCursorInvalidated otherwise), `to` not before
    // `from` (ErrCursorRegression) and not beyond the published frontier (ErrCursorBeyondHead). `from == to` is
    // valid. The adapter makes the comparison; the caller sees only the error. A consumer commits progress only for
    // a cursor this accepted against the position it loaded.
    ValidateAdvance(ctx context.Context, scope Scope, shard uint64, from, to JournalPosition) error
}

// Required visibility of the publication backlog (G10, D9). OldestPendingAge (or an equivalent signal) is mandatory;
// PendingCount may be an estimate or unknown.
type PublicationReporter interface {
    PublicationBacklog(ctx context.Context, scope Scope) (PublicationBacklog, error)
}

// Optional (G10): how many available events lie after a consumer's progress; an estimate or unknown is allowed.
type ConsumerBacklogReporter interface {
    ConsumerBacklog(ctx context.Context, scope Scope, shard uint64, after JournalPosition) (ConsumerBacklog, error) // count or unknown
}

// Optional capability: position a consumer by time (a rebuild or a start "from a moment").
type TimePositioner interface {
    // PositionAt returns a cursor from which EVERY available event whose Timestamp is >= t will be delivered. It
    // may also deliver events with an earlier Timestamp that lie after it in stream order; it never skips one that
    // qualifies. Conservative on purpose: Timestamp is writer time, not stream order (G4).
    PositionAt(ctx context.Context, scope Scope, shard uint64, t time.Time) (JournalPosition, error)
}

// Typed error set (names open): ErrOutcomeUnknown, ErrSequenceOrder, ErrShardMismatch, ErrIdentityConflict, ErrUnsupportedBatch,
// ErrRetentionPending, ErrCursorMismatch, ErrCursorInvalidated, ErrCursorOutsideRetention, ErrCursorRegression,
// ErrCursorBeyondHead, ErrProgressConflict, plus the existing ones.
```

Notes:

- `limit` is a target; the timestamp-tie extension of #331 disappears for adapters whose positions are unique per
  event and stays an adapter detail.
- Replacing `GetShardEvents` and `ShardOffsets` breaks every implementer at compile time, which is the intent;
  whether to keep them for one release is a public-contract decision.
- `WriteEvents`' signature does not change; the outcome taxonomy is carried by the error types (`errors.Is`).
- `DeleteEvents` may gain a reason parameter (G8 open question).
- `TimePositioner` is a proposal for one question only: whether time-based starts must survive the move to opaque
  cursors (D14b). It is independent of the unit defect those features have today, which is decided separately
  (D14a).

## 6. Progress (proposal, not approved)

Progress is the committed cursor of one consumer of one stream. Its identity is #93's (who the progress belongs
to); the contract owns what the stored value means and how it is updated safely. Three different things can go
wrong with a bare `Commit(id, position)`, and each has its own mechanism:

1. a **delayed or concurrent update** overwrites newer progress (lost update);
2. a **reset** (rebuild) is undone by a commit prepared before it;
3. the cursor being committed is **not a continuation** of the stored one: it is before it (a regression), beyond
   the published frontier, or from another stream instance or generation. A current revision does not prevent
   this: a consumer that commits an older page's `Next` after a newer commit of its own still holds a valid
   revision.

Names and types are open. The word **epoch** is used here on purpose: the progress epoch counts the resets of one
consumer's progress, and it is unrelated to the **journal generation** of G7, which invalidates the cursors of a
whole journal.

```go
// ProgressID is owned by #93: projection identity, scope, shard.
type ProgressID struct { Projection string; Scope Scope; Shard uint64 }

type Revision uint64   // changes on every successful Commit or Reset; the CAS token
type Epoch uint64      // changes on every Reset; invalidates commits made before it (not the journal generation of G7)
type CommitID [16]byte // chosen by the caller, unique per commit attempt; stored with the progress

type Progress struct {
    Position     JournalPosition // zero when nothing was committed
    Revision     Revision
    Epoch        Epoch
    LastCommitID CommitID        // the CommitID of the commit that produced this revision; zero after Reset or when none
}

type ProgressStore interface {
    // Load never fails for an unknown id: it returns the zero position with revision 0.
    Load(ctx context.Context, id ProgressID) (Progress, error)

    // Commit stores next iff the stored revision equals expectedRevision AND the stored epoch equals
    // epoch, recording commitID as the progress's LastCommitID. On success it returns the new revision.
    // Otherwise it fails with ErrProgressConflict carrying the current Progress, and stores nothing.
    // The store does not interpret `next`: it cannot tell whether it continues the stored position (see below).
    // fence is optional and used only when ownership is distributed.
    Commit(ctx context.Context, id ProgressID, expectedRevision Revision, epoch Epoch,
           commitID CommitID, next JournalPosition, fence Fence) (Revision, error)

    // Reset returns the progress to the zero position under a NEW epoch, iff the stored revision equals
    // expectedRevision. Commits prepared under an earlier epoch then fail.
    Reset(ctx context.Context, id ProgressID, expectedRevision Revision) (Epoch, error)
}
```

### Who validates that `next` continues the loaded position

The progress store holds opaque bytes and has no public order to apply, and the core must not gain one. The party
that can answer is the **journal adapter**, which already relates its own frontiers. The rule is:

> A consumer commits only a cursor that `StreamReader.ValidateAdvance(scope, shard, loaded, next)` accepted, where
> `loaded` is the position it read with `Load` under the revision it passes to `Commit`.

This composition is sound without making the two operations atomic: `ValidateAdvance` depends only on `(loaded,
next)`, and the compare-and-set on `expectedRevision` guarantees that `loaded` is still the stored position when the
commit lands. If the revision moved, the commit fails and the validation is moot; if it did not, the validated
relation still holds. The consumer runtime performs the validation; the conformance suite tests the adapter's
`ValidateAdvance` (a regression, a cursor beyond the frontier, and one of another stream instance or generation are
refused) and tests the runtime's use of it (a runtime that commits an unvalidated older `Next` fails the check).

**The limit of this rule, and the decision.** It binds consumers: a store holding opaque bytes cannot enforce it
against a consumer that skips the validation. **Decided (D5): validation is done at the consumer, together with CAS and
the epoch**, and the conformance suite tests that the runtime does it. Validation inside the commit, for an adapter
whose progress lives in the journal's database, and a signed advance token were considered and are **not adopted**.
Fencing is added when ownership is distributed, coordinated with #93.

### What each mechanism solves, and what it does not

| Mechanism | Solves | Does not solve |
|---|---|---|
| **Revision (CAS)** | a delayed or concurrent `Commit` overwriting newer progress; lost updates | a committed cursor that regresses or is invalid while the revision is current; a deposed owner that loaded fresh progress and keeps working |
| **Epoch** | a `Commit` prepared before a `Reset` landing after it and restoring old progress | who is allowed to write; the validity of `next` |
| **`ValidateAdvance`** (journal side) | `next` before the loaded position, beyond the published frontier, or from another stream instance or generation, even with a current revision | stale writers (CAS) and ownership (fence) |
| **Fence** (monotone owner token from the ownership mechanism of #93, checked by the store) | a deposed or zombie owner committing at all | stale values, invalid cursors, nor duplicate processing |

**Fencing and CAS solve different problems and one does not replace the other**, and neither validates the cursor.
None of them prevents a zombie from processing events twice; handlers stay idempotent (G6). If ownership is not
distributed, `fence` is the zero value and is ignored. The ownership and claim mechanism itself belongs to #93.

### Resolving a `Commit` whose outcome is unknown

`Equal(next)` is not enough: another worker, or a later epoch, can hold the same position. The caller
attaches a fresh `CommitID` to each attempt, and on an unknown outcome it `Load`s and applies, in order:

1. `current.Epoch != epoch`: a reset intervened. Our commit is void; start again from the loaded state.
2. `current.LastCommitID == commitID`: our commit was applied. Success.
3. `current.Revision == expectedRevision`: it was not applied. Validate again (`ValidateAdvance`) and retry.
4. Otherwise (the revision moved and the last commit is not ours): someone else committed. Our commit may have been
   applied before theirs and then superseded, or never applied; it does not matter, because progress is a state.
   Reload, recompute from the current position, and continue; events between are redelivered (G6).

Matching on position alone is never used to claim success.

### Empty pages

A consumer may commit the `Next` of a page with no events when the source advanced (after `ValidateAdvance`); it
must not assume that an empty page leaves `Next` unchanged.

### Storage

`offsets_store.current_offset` and `egopb.Offset.value` become bytes carrying the serialized cursor, plus revision,
epoch and last-commit-id columns, keyed by the identity of #93. Migration of legacy `int64` offsets is the
data-migration gate.

## 7. How each guarantee is observed

`conformance-and-experiment.md` maps G1-G10 and the progress rules to checks and lists the harness hooks an adapter
provides (await publication, restart, failure injection including a lost acknowledgement). Nothing in the checks
reads a timestamp offset or names a mechanism of any one database.

## 8. Decisions and open points

The owner's resolutions of D1-D15 are recorded in `decisions.md` and summarized here. The SPI and the data migration
are **not approved**; the batch-publication experiment is authorized as an experiment.

**Resolved (direction):**

- D1 semantics first; names and types are reviewed with the first conformance slice.
- D2 the write rules (idempotent replay, `ErrIdentityConflict`, `ErrSequenceOrder`, the stream rule) are the objective,
  subject to a caller audit.
- D3 first delivery is retention with an explicit error and no partial deletion; the reason distinction is deferred.
- D4 no universal cursor bound; each adapter declares a limit and validates its inputs.
- D5 advance validation at the consumer, plus CAS and epoch; fencing with #93 when ownership is distributed.
- D6 journal UUID and explicit generation; a documented restore and a validation before consumers resume.
- D9 publication-backlog visibility is mandatory, an exact count is not; consumer backlog is optional.
- D11 the #332 regression enters with the implementation that makes it pass.
- D12 slices: conformance, experimental implementation, measurement, then production integration.
- D13 logical streams owned by Urd, independent of GoAkt, with a stable and versioned assignment algorithm.
- D14a the unit defect is fixed to nanoseconds on the current SPI with a permanent regression; it changes observable
  behavior and stored offsets need explicit treatment.
- D15 the retention check is mandatory in conformant adapters, preceded by an audit; no conformant mode detects loss
  and continues.

**Open or pending:**

- O1 an unknown write outcome after retention removed the events (D2).
- O2 the effect of erasure on projections (D3).
- O3 names and types of the SPI (D1).
- O4 the joint design with #93: ownership and scoped progress (D5).
- D7 the migration, pending the joint design with #93, with the constraint that progress is migrated once.
- O5 the treatment of stored offsets after the unit fix (D14a).
- O6 the assignment algorithm, `K` for new journals, and the adoption audit for existing journals (D13).
- D14b the semantics of "rebuild from a date", before any interface (`TimePositioner` above is a sketch only).
- O8 the restore runbook (D6).
- D10 the index issue: pending, no issue opened.
