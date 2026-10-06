# Batch publication on PostgreSQL: prototype report (#332)

Status: a PROTOTYPE on the real adapter, behind the `journalexp` build tag, off the production path. This document is
**evidence and a proposal**: it does not approve the mechanism, a migration or any production change, and it is not an
SPI. Code and tests: branch `exp/332-batch-publication`, published for review and not
for merge, at exactly commit `e596ee66332eaa499fbbd551d08b83a589f91460`. Raw results: `evidence/batch/`.

**What it is.** A write persists its events exactly as today, with a pending position. A publisher takes the stream's
row lock, gives the next batch of N pending rows (smallest `pub_seq` first) consecutive positions, and commits. A read
returns only published rows in position order. The offset is a position, not a timestamp.

## Correctness

- Omitted events: **0** in every scenario and repetition, for N=100 (about 443,000 events) and N=1000 (about 437,000).
  "Omitted" is counted after the run's final drain: every event is eventually delivered, which at saturation means up to
  a minute late (see delivery latency). It is not a claim of timeliness.
  The current adapter, the control, omitted 0.07% to 0.17% at 300 to 500 tx/s and 5.9% to 10.1% saturated.
- Tests, real PostgreSQL, `-race`, repeated: late events (the reported late@200 and late@150 sequence and the in-flight
  writer interleaving), per-entity order under concurrent writers, a contended entity and multi-shard batches with
  rollbacks and two background publishers, a crash after persisting and before publishing, a crash inside a
  publisher transaction, restart and resume from a committed cursor, and the real conformance suite (the #332
  regression passes; the three checks that assert a timestamp offset fail, as expected). Zero failures in 5 to 8
  repetitions. In the stress test, within an entity `pub_seq` grew with the sequence number in every repetition (the
  benchmark runs do not check it).

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

Up to 500 tx/s delivery is slower by **1.0 to 1.5 ms at the median, 1.4 to 2.4 ms at p95 and 2 to 9.8 ms at p99**
(R300: p99 13.9 ms against 4.1 ms). An earlier version of this report said "2 to 3 ms at the median"; that overstated the
median and is corrected here. A
held write transaction does not delay delivery. **At saturation the publisher falls behind**: 20,000 to 43,000 events
pending and delivery in seconds to a minute.

**Saturation is not solved.** My first explanation, a slow stream-discovery query, was wrong: the corrected run matches
the first. A diagnostic that gives the publisher its own connection pool (the adapter's fixed pool of 20 is shared by
writers, the reader and the publisher) **reduced the lag but it remained seconds**: saturated W1 went from p95 58 s to
8.3 s (median 16 s to 6.2 s) and from 2407 to 2862 tx/s, a single comparison of 3 repetitions each. So pool contention
is a significant cause and not the only one, and a separate pool does not by itself make saturated load acceptable:
delivery still lags by about 8 s at about 2,900 tx/s, and the remaining limit was not isolated.

## Backlog recovery (B1: publisher stopped 10 s at 300 tx/s, then resumed)

Backlog 9,000 events at its peak, drained 0.55 to 0.70 s after the publisher resumed (N=100) and 1.0 to 1.85 s
(N=1000). Events written during the stall were delivered after 8.3 s at p95, as expected. Recovery is not the problem.

## Storage and WAL: an explicit cost

| Per event | current | batch N=100 | ratio |
|---|---|---|---|
| table and indexes | 281 to 314 B | 454 to 580 B | 1.5 to 2.0x (N=1000: up to 2.3x) |
| **WAL** | 635 to 779 B | 1,483 to 1,672 B | **2.1 to 2.4x** |

This exceeds the provisional 2x target for WAL (and for N=1000 storage at saturation). It is a cost, stated as such.
Its attribution is a hypothesis, not a measurement: every event row is written twice (the insert, then the position
update), and the batch variant also carries two extra indexes, a sequence and a counter table that the control does not;
the share of each was not separated (a control with the indexes and no update would do it). It does not by
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

## Minimal contract change and transition (a proposal, not approved)

**This is a change to the public contract even though no signature changes.** Every existing adapter, including any
external implementation, interprets the `int64` offset of `GetShardEvents` and `ShardOffsets` as an event timestamp;
stored offsets are timestamps; the runner computes lag from the offset and passes dates as offsets. Changing a comment
would reinterpret all of it silently. The change therefore needs an explicit transition, written below. The proposal
keeps `int64` for the current adapters, with its meaning documented. It does not claim to settle future cursor types,
such as the change-feed cursors of #333.

### The guarantee: a safe frontier, with no publication order imposed

An offset returned by a read is a **safe frontier**. Continuing to read from the offsets that reads return (starting
from 0 or from any offset previously returned) never omits an event that was confirmed and is retained: every such
event is delivered by the chain of reads, including one confirmed after the offsets were returned, however late. It is
delivered eventually, under operating conditions the adapter documents. The proposal imposes no order of publication,
no order between independent entities and no relation between an offset and a time; how an adapter keeps its
frontier safe (post-commit publication, a visibility horizon, or something else) is its own matter. The per-entity
order that exists today is neither strengthened nor weakened by this proposal.

### Explicit transition

1. **An adapter opts in.** An adapter declares that its offsets are positions with the safe-frontier guarantee through a
   small optional capability. An adapter that does not declare it is a **legacy timestamp-offset adapter**: nothing
   about it changes, it keeps its current behavior, and it is documented as not providing the guarantee (#332). The
   addition is compatible; the `api` check would classify it as adding API.
2. **The runner behaves by capability.** For a legacy adapter, everything stays as it is today, including the lag
   gauge and the time-based starts (their separate unit defect, D14a, is its own change). For a position adapter, see
   items 3 and 4.
3. **Lag is not renamed.** The runner stops computing `now - offset` for a position adapter and this proposal defines
   **no replacement called lag**. A timestamp-based number is the *age of the last processed event*: it grows while
   the projection is fully up to date, so it is named age and is informational. "The projection is caught up" is
   determined by a read, not by arithmetic: a read from the committed offset returns nothing. Real delivery latency and
   backlog measures stay with D9 (publication backlog visibility mandatory, exact count not; consumer backlog optional)
   and are not defined here.
4. **A date-based start is an error, never ignored and never converted.** With a position adapter, a configuration that
   carries a date (`WithStartOffset`, `WithResetOffset`, `RebuildProjection(from)` with a non-zero `from`) fails with
   an explicit error when it is configured or started. It is not silently dropped and the date is not converted into an
   offset. Absent or zero means "from the beginning" and is supported. Dates stay unsupported with positions until D14b
   defines "rebuild from a date".
5. **Stored offsets are not reinterpreted.** An offset written under timestamp semantics must not be used under position
   semantics. The first deployment that switches an adapter to positions therefore needs an explicit operator step:
   either the data-migration decision (D7, pending the joint design with #93) or a reset of the projection's offsets
   (a rebuild from the beginning). This proposal cannot detect a mixed offset by itself, because the offsets table
   carries no marker of its semantics and adding one is a schema change under the migration gate. It states the
   requirement and leaves the mechanism to D7.
6. **Two conformance suites during the transition.** The legacy suite is today's. The position suite asserts the safe
   frontier and the isolation guarantees without assuming a timestamp: the three checks that assert a timestamp offset
   (`GetShardEventsReturnsOnlyTheScopesEvents`, `ShardOffsetsCoverOnlyTheScopesShards`,
   `UnscopedNeverReadsATenantNamedUnscoped`) are rewritten for it, and the #332 regression belongs to it and enters
   with the implementation that makes it pass. An adapter claims one suite. Legacy semantics would be deprecated for
   removal in a later major release.
7. **The in-repo `testkit` store changes its observable behavior, and that is the fix.** As a position adapter, an event
   with an old timestamp that is persisted after a consumer's committed offset **will appear after that offset**,
   where today it does not. Tests that depend on timestamp ordering of its offsets must change with it.

### What does not change, and what is not claimed

- `WriteEvents` does not change for omission-free reads; the prototype changes no write semantics. The identity, replay
  and sequence rules of the earlier contract answer a different problem and stay separate from #332.
- Not needed to fix #332 and deferred, not withdrawn: a cursor type, `Page`, progress with CAS, epochs, generations,
  cursor binding, a retention floor, `ValidateAdvance` and the logical-stream machinery.
- The change is the same whichever mechanism keeps the frontier safe. It holds for post-commit batch publication and
  for the xid horizon measured earlier. The choice is an operating decision: batch publication costs WAL and storage and
  needs a publisher but is not delayed by a long transaction elsewhere on the server; the horizon costs almost nothing
  in writes and has no publisher but is delayed by the oldest open transaction of the whole server.
- Keeping `int64` is a choice for the current adapters. It does not resolve the cursor of a change feed (#333), whose
  position may not fit a single integer.

### Costs to state with the change

WAL 2.1 to 2.4x and storage 1.5 to 2.0x per event (batch publication); delivery slower by 1.0 to 1.5 ms at the median
and up to 9.8 ms at p99 up to 500 tx/s; at saturation the publisher lags by seconds, and a separate connection pool
reduces that lag without eliminating it.
