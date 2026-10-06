# Batch publication on PostgreSQL: prototype report (#332)

Status: a PROTOTYPE on the real adapter, behind the `journalexp` build tag, off the production path. It is not a
proposal to merge and not an SPI. Branch `exp/332-batch-publication`. Raw results: `evidence/batch/`.

**What it is.** A write persists its events exactly as today, with a pending position. A publisher takes the stream's
row lock, gives the next batch of N pending rows (smallest `pub_seq` first) consecutive positions, and commits. A read
returns only published rows in position order. The offset is a position, not a timestamp.

## Correctness

- Omitted events: **0** in every scenario and repetition, for N=100 (about 443,000 events) and N=1000 (about 437,000).
  The current adapter, the control, omitted 0.07% to 0.17% at 300 to 500 tx/s and 5.9% to 10.1% saturated.
- Tests, real PostgreSQL, `-race`, repeated: late events (the reported late@200 and late@150 sequence and the in-flight
  writer interleaving), per-entity order under concurrent writers, a contended entity and multi-shard batches with
  rollbacks and two background publishers, a crash after persisting and before publishing, a crash inside a
  publisher transaction, restart and resume from a committed cursor, and the real conformance suite (the #332
  regression passes; the three checks that assert a timestamp offset fail, as expected). Zero failures in 5 to 8
  repetitions. Within an entity `pub_seq` grew with the sequence number in every run.

## Throughput and write latency (tx/s, p95 write ms; batch N=100 against the control)

| Load | current | batch |
|---|---|---|
| 300 and 500 tx/s offered | 298 and 498; 3.1 and 3.5 ms | 298 and 498; 2.9 and 2.6 ms |
| W1 saturated | 3386 (3320..3495); 5.4 | 2513 (1817..2960); 8.7, **-26%** |
| W2 saturated | 2330 (1775..2791); 8.9 | 2319 (2283..2373); 8.8, equal |
| W3 saturated, 8 streams | 2636 (2501..2835); 6.7 | 2254 (2097..2366); 9.5, **-14%** |

Saturated ranges are wide and overlap (W2); treat differences below about 25% as within this VM's noise. N=1000 is
no better (W1 2614, W3 1962).

## Delivery latency (start of the write to delivery, p50 / p95 / p99 ms)

| Load | current | batch N=100 |
|---|---|---|
| R300 | 2.4 / 3.6 / 4.1 | 3.9 / 6.1 / 13.9 |
| R500 | 2.6 / 4.3 / 5.5 | 3.6 / 5.7 / 10.8 |
| H1 and H3, a held write transaction | 2.3 / 3.7 / 5.5 and 2.4 / 3.7 / 4.2 | 3.6 / 5.1 / 7.5 and 3.4 / 5.0 / 7.3 |
| Saturated W1 / W2 / W3 | 12 / 345 / 442; 14 / 119 / 208; 7 / 16 / 48 | **17 s / 60 s / 60 s; 6.2 s / 11 s / 12 s; 4.6 s / 6.9 s / 7 s** |

Up to 500 tx/s the delivery cost is about 2 to 3 ms at the median and a slower tail (p99 up to 14 ms against 5 ms). A
held write transaction does not delay delivery. **At saturation the publisher falls behind**: 20,000 to 43,000 events
pending and delivery in seconds to a minute.

**What limits it (partly isolated).** My first explanation, a slow stream-discovery query, was wrong: the corrected run
matches the first. A diagnostic that gives the publisher its own connection pool (the adapter's fixed pool of 20 is
shared by writers, the reader and the publisher) improved saturated W1 from p95 58 s to 8.3 s and from 2407 to 2862
tx/s (a single comparison, 3 repetitions each), so pool contention is a major cause. It is not the only one:
delivery still lags by seconds at about 2,900 tx/s, and the remaining limit was not isolated.

## Backlog recovery (B1: publisher stopped 10 s at 300 tx/s, then resumed)

Backlog 9,000 events at its peak, drained 0.55 to 0.70 s after the publisher resumed (N=100) and 1.0 to 1.85 s
(N=1000). Events written during the stall were delivered after 8.3 s at p95, as expected. Recovery is not the problem.

## Storage and WAL: an explicit cost

| Per event | current | batch N=100 | ratio |
|---|---|---|---|
| table and indexes | 281 to 314 B | 454 to 580 B | 1.5 to 2.0x (N=1000: up to 2.3x) |
| **WAL** | 635 to 779 B | 1,483 to 1,672 B | **2.1 to 2.4x** |

This exceeds the provisional 2x target for WAL (and for N=1000 storage at saturation). It is a cost, stated as such:
every event row is written twice (the insert, then the position update) with index maintenance. It does not by
itself rule the approach out. A variant that never updates event rows (an append-only log table plus a small pending
table) would trade it for an insert, a delete and a join; it was not built or measured. Dead tuples were not measured.

## Limits of this report

- **Held publisher: not measured.** Only a crash inside a publisher transaction was tested (nothing becomes visible).
- Dead tuples not measured; the saturated limit not fully isolated; one 2-vCPU VM shared by generator and database
  with a host load of 2.9 to 4.6; 3 repetitions; saturated ranges overlap.
- The prototype implements none of the contract's extra rules (idempotent replay, identity and sequence checks,
  cursor binding, retention floor, progress store). Unconditional writes can still persist an entity out of sequence
  order, as today.
- The first run is kept and named: it used a slow discovery query and gave the same saturated numbers.

## Minimal contract change (a proposal, not approved)

Documentation of the existing `persistence.EventsStore` methods; **no signature changes**, no new types.

1. **The offset is a position, not a timestamp.** `GetShardEvents` and `ShardOffsets` keep their `int64` offset, but it
   is assigned by the adapter in publication order and callers treat it as opaque: no arithmetic, no time meaning.
   The returned next offset is the position of the last event returned; `ShardOffsets` is the newest *published*
   position.
2. **Events are available after they are published.** `GetShardEvents` returns only published events. Every persisted,
   retained event is eventually published, under conditions the adapter documents (for example that its publisher
   runs). An event published later always has a position above any offset already returned. This is the guarantee #332
   needs (G2 and G3 of the earlier contract, in two sentences).
3. **Adapters document** their publication mechanism, its conditions, and make the publication backlog visible (at
   least the age of the oldest pending event; an exact count is not required), as decided in D9.
4. **The consumer side changes in two places.** Lag is computed from event timestamps and not from the offset (today
   the runner computes `now - offset`). And the time-based starts (`WithStartOffset`, `WithResetOffset`,
   `RebuildProjection(from)`) can no longer pass a time as an offset: until D14b defines "rebuild from a date", only a
   rebuild from the beginning is supported on an adapter with position offsets.
5. **Conformance.** The #332 regression stays and enters with the implementation. The three checks that assert a
   timestamp offset (`GetShardEventsReturnsOnlyTheScopesEvents`, `ShardOffsetsCoverOnlyTheScopesShards`,
   `UnscopedNeverReadsATenantNamedUnscoped`) are rewritten to assert the same isolation without assuming a timestamp.
   The in-repo `testkit` store publishes at write time, so its observable behavior does not change.
6. **Existing offsets.** Stored timestamp offsets need a mapping to positions. That is the data-migration decision
   (D7, pending the joint design with #93) and is not decided here.

**What the write contract does not need.** `WriteEvents` does not change for omission-free reads. The prototype
changes no write semantics. The identity, replay and sequence rules of the earlier contract answer a different
problem (an unknown outcome and out-of-order writes) and are separate from #332.

**Not needed to fix #332:** a cursor type, `Page`, progress with CAS, epochs, generations, cursor binding, a retention
floor, `ValidateAdvance`, or the logical-stream machinery. They stay documented as open questions.

**The change is the same whichever mechanism publishes.** Points 1 to 6 describe what readers may rely on; they hold for
post-commit batch publication and for the xid horizon measured earlier. The choice between them is an operating
decision: batch publication costs WAL and storage and needs a publisher but is not delayed by a long transaction
elsewhere on the server; the horizon costs almost nothing in writes and has no publisher but is delayed by the oldest
open transaction of the whole server.

**Costs to state with the change:** WAL 2.1 to 2.4x and storage 1.5 to 2.0x per event; delivery about 2 to 3 ms slower
at the median up to 500 tx/s; at saturation the publisher can lag by seconds unless it has its own connections.
