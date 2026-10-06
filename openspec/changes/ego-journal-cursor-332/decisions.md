# Decision record for design.md 14.4 (#332)

Status: **DIRECTION CLOSED, RESOLUTIONS RECORDED** (the owner, on #338). The SPI and the data migration are **not
approved**; the production path and the public SPI are unchanged and nothing here implements either.

**Authorizations.**

| Authorized | Not authorized |
|---|---|
| The batch-publication **experiment** (D8), as an experiment: provisional targets, full reporting | The data **migration** |
| | A **production change of the SPI** |

Each entry keeps the question, the finding and the options as they were put to the owner, then the **Resolution**
and **Still open**. Numbers marked "target" are provisional experimental targets, never an SLA or a requirement of
Urd. Where a decision needs data nobody has yet, the entry says which.

| ID | Resolution | State |
|---|---|---|
| D1 | semantics first; names and types are reviewed with the first conformance slice | DECIDED |
| D2 | the rules are the objective, subject to a caller audit; an unknown outcome after retention is unresolved | DECIDED with an open problem |
| D3 | first delivery: retention with an explicit error and no partial deletion; the reason distinction is deferred | DECIDED, part deferred |
| D4 | no universal bound; each adapter declares a limit and validates its inputs | DECIDED |
| D5 | advance validated at the consumer, plus CAS and epoch; fencing with #93 when ownership is distributed | DECIDED |
| D6 | journal UUID and explicit generation; documented restore and a validation before consumers resume | DECIDED |
| D7 | pending the joint design with #93; do not fix that a migration lands first; do not migrate progress twice | PENDING |
| D8 | experiment authorized; provisional targets; report variability and complete results | AUTHORIZED |
| D9 | visibility of the publication backlog is mandatory; an exact count is not; consumer backlog optional | DECIDED |
| D10 | pending; no issue is opened | PENDING |
| D11 | the regression enters with the implementation that makes it pass | CONFIRMED |
| D12 | small slices: conformance, experimental implementation, measurement, then production integration | DECIDED |
| D13 | option A: logical streams owned by Urd, independent of GoAkt; stable, versioned assignment; existing journals audited, not inferred | DECIDED, adoption pending an audit |
| D14a | nanoseconds on the current SPI with a permanent regression; an observable behavior change; stored offsets need explicit treatment | DECIDED, stored-offset treatment open |
| D14b | deferred; "rebuild from a date" stays a need and its semantics are defined before any interface | DEFERRED |
| D15 | mandatory check in conformant adapters, preceded by an audit; no conformant mode that detects loss and continues | DECIDED |

## D1 SPI shape and names

- **Question:** approve the proposed shape (`StreamReader`, `ReadStream`, `StreamHeads`, `ValidateAdvance`, `Page`,
  `JournalPosition`, `ProgressStore`, the typed errors) now?
- **Finding:** the semantics moved in two review rounds; a shape approved now would freeze names around guarantees
  that changed.
- **Options:** (a) approve the whole shape; (b) approve the semantics and defer names and types until the
  conformance suite is written against them; (c) approve only the read side.
- **Resolution:** (b). Names and types are reviewed with the first conformance slice.
- **Still open:** the names and types themselves.

## D2 Write behavior change

Covers idempotent replay before the precondition, `ErrIdentityConflict`, `ErrSequenceOrder` and the stream rule of
D13.

- **Question:** accept that `WriteEvents` stops skipping duplicate identities silently and stops accepting
  out-of-order sequences, including under `Unconditional()`?
- **Finding:** today an unconditional write skips a duplicate identity whatever its content and accepts any sequence
  order. Non-test callers: `internal/engine/eventsource/events_writer_actor.go`, `internal/engine/saga/saga_actor.go`
  (two `Unconditional()` writes) and `migration/tenant_adoption.go` (`ExpectGenesis`). Replay equality compares
  payload, manifest, metadata and encryption fields and not `Timestamp` (contract G1).
- **Resolution:** the rules are accepted **as the objective, subject to the caller audit** (the audit must confirm
  that the three callers retry the batch they hold, unchanged).
- **Still open (unresolved problem O1):** an unknown outcome **after retention** removed the events. If the batch's
  events are gone, the retry cannot compare identities, and the entity's revision (which retention never lowers) says
  only that some writer reached that sequence, not that this batch did. A configured window ("the retry must arrive
  before the retention delay") does not prove what happened and is not accepted as a resolution by itself.
  Directions to study, none chosen: (i) retention may not remove the events of a write until the writer has
  acknowledged its outcome; (ii) a durable per-entity record of recent batch identities (a digest) that outlives
  retention; (iii) retention never removes the last K sequences of an entity. Each has a cost and none is proposed
  here.

## D3 `DeleteEvents`

- **Question:** how does a consumer tell erasure from retention?
- **Finding:** a consumer must continue past erased events (it must not process erased data) and must stop on
  retention loss; the current `DeleteEvents` cannot tell them apart.
- **Options:** (a) a `Reason` parameter; (b) separate methods; (c) treat every deletion as retention.
- **Resolution:** **defer the distinction of reasons.** First delivery: **retention only, with an explicit error
  and no partial deletion** (`ErrRetentionPending` while pending events are affected; the retention floor and
  `ErrCursorOutsideRetention` for loss; contract G8 as written).
- **Still open:** erasure. It requires defining its effect on projections (what a projection that already consumed
  the erased events must do, and what one that has not must see) before any mechanism is chosen. G8's open question
  about a reason stays open and is no longer on the first delivery's path.

## D4 Cursor size

- **Question:** what bound does the framework put on a serialized cursor?
- **Finding:** no real token has been measured; a PostgreSQL cursor is estimated at a few tens of bytes.
- **Options:** (a) 256 bytes now; (b) a larger bound now; (c) no framework bound.
- **Resolution:** (c). **No universal bound for now. Each adapter must declare a limit and validate its inputs**: a
  cursor that is malformed, truncated, of the wrong version or larger than the adapter's declared limit is rejected
  with a typed error before any allocation or query (contract section 4).
- **Still open:** a framework bound, after real tokens are measured.

## D5 `ValidateAdvance`, progress and ownership

- **Question:** how is "commit only a validated cursor" enforced, and where do ownership and fencing live?
- **Finding:** validation is a rule on consumers; a store holding opaque bytes cannot enforce it on one that skips it.
- **Options:** (a) rule plus conformance of the runtime; (b) co-located validation inside the commit; (c) a signed
  advance token.
- **Resolution:** **advance validation at the consumer, plus CAS and epoch.** Fencing when ownership is distributed,
  coordinated with #93. The co-located validation (b) and the signed token (c) are not adopted.
- **Still open:** the ownership and claim mechanism, which belongs to #93.

## D6 Journal id and generation

- **Question:** how are the journal id and generation created and advanced?
- **Finding:** a restore from backup restores the journal metadata too, so the old generation returns with the data.
- **Options:** (a) an explicit restore step that advances the generation; (b) detect a restore automatically.
- **Resolution:** (a): a UUID and an explicit generation. **The restore procedure is documented, and a validation
  runs before consumers resume** (the stored cursors are checked against the journal's generation and published
  frontier; a mismatch is `ErrCursorInvalidated`, never a silent resume).
- **Still open:** the restore runbook itself.

## D7 Data migration

- **Question:** in what order is existing data migrated?
- **Finding:** the backfill must preserve per-entity order; the legacy offset mapping is conservative; offsets have
  no scope until #93; legacy offsets may be in milliseconds; entities that span streams must be resolved; loss
  before the cutover is undetectable (`postgres-batch-publication.md` section 11).
- **Options:** (a) a strategy-independent migration in a fixed order; (b) a per-adapter migration.
- **Resolution:** **PENDING the joint design with #93.** The earlier proposed order ("#93 first") is **withdrawn**:
  it is not fixed that any migration lands first. The constraint is that **progress must not be migrated twice**:
  the move of offsets to scoped, opaque, epoch-and-revision progress and the mapping of legacy offsets must be one
  step, designed with #93.
- **Still open:** the whole sequence. The audits of section 11 stay as candidates for that design.

## D8 Which PostgreSQL strategy: the experiment

- **Question:** is post-commit batch publication worth building next to the measured variants?
- **Finding:** it has a no-starvation argument per stream and across streams, and is neither implemented nor measured.
- **Options:** (a) run the experiment against fixed bars; (b) with different bars; (c) skip it.
- **Resolution:** **the experiment is authorized**, as an experiment (behind a build tag, not on the production
  path). The numbers below are **provisional experimental targets**, not an SLA and not a requirement of Urd. The
  report must give the **variability** (per-repetition values and ranges, the host-load record) and the **complete
  results**, favorable or not, with the raw output kept next to the design.

| Question | Provisional experimental target |
|---|---|
| Correctness | the conformance suite and the stress pass with no omission, duplicate or deadlock; otherwise stop |
| Writer cost at 300 tx/s offered | write p95 within 2x of `current` |
| Delivery at 300 and 500 tx/s offered | start-to-delivery p95 below 100 ms with `tau = 5 ms` |
| Held publisher, same and other stream | writers unaffected; the stall confined to its stream |
| Recovery after a 10 s stall at 300 tx/s | backlog drained within 30 s at the chosen `N` |
| Cost per event against `current` | bytes and WAL each within 2x; dead tuples reported |

- **Not authorized with it:** the migration, and any production change of the SPI.
- **Still open:** the experiment is not built or run; by D12 it follows the conformance slice.

## D9 Backlog metrics

- **Question:** which backlog metrics are required of an adapter?
- **Finding:** G2's condition cannot be checked without seeing the publication backlog; an exact count can be costly.
- **Options:** (a) both required; (b) publication backlog required and consumer backlog optional; (c) both optional.
- **Resolution:** **visibility of the publication backlog is mandatory; an exact count is not.** An adapter must
  expose at least the age of its oldest pending event or an equivalent signal; the count may be an estimate or
  unknown. **Consumer backlog is optional.**
- **Still open:** none.

## D10 Index issue

- **Question:** open `evidence/issue-draft-timestamp-index.md` as an issue?
- **Resolution:** **PENDING. No issue is opened.**

## D11 #332 regression timing

CONFIRMED by the owner on #338: it enters `develop` together with the implementation that makes it pass; the
experiments may keep an explicit expected failure; the production CI is not changed to accept the omission.

## D12 Delivery plan

- **Question:** how is the work sliced?
- **Options:** (a) documentation, types and conformance, adapter and migration, runner; (b) one large
  implementation; (c) a spike of the batch strategy first.
- **Resolution:** **small slices, in this order: (1) conformance, (2) experimental implementation, (3) measurement,
  and only then (4) production integration.** The production integration needs the public-contract and
  data-migration gates, which are not open.
- **Still open:** the gates.

## D13 Stable stream identity of an entity

- **Question:** what makes an entity's events stay in one stream, so that per-entity order (G5) holds?
- **Finding:** streams carry no order between them; if an entity's events could land in two streams a consumer could
  see `n+1` before `n`. Today `Event.Shard` is `ActorSystem().Partition(persistence id)`, a function of the entity
  name and of GoAkt's partition count, and the revision row records no shard. Tying stream identity to that count
  would make a topology change a change of stream identity. Stream identity and actor placement are different things.
- **Options:** (A) a logical stream key owned by Urd; (B) record the GoAkt partition of the first event; (C) declare
  the partition count immutable.
- **Resolution:** **A: logical partitions of Urd, independent of GoAkt.** Two precisions from the owner:
  1. **Fixing `K` is not enough: the assignment algorithm must also be stable and versioned.** A journal records its
     assignment algorithm and version together with `K`, in its metadata; under one version the same persistence id
     always maps to the same stream, on every node and in every release; `K` and the version are immutable for a
     journal, and a change of either is an explicit re-homing migration, not designed here. The event's stream key is
     recorded with the entity's first event and a later event under another key is rejected.
  2. **For existing journals, `K` is not inferred from the current cluster.** The historical distribution is audited
     first (the distinct `shard_number` values and their counts per scope, how they evolved, the entities that span
     streams, and which assignment produced them), and an adoption is proposed from that evidence for the owners to
     approve.
- **Still open:** the algorithm and its version scheme; how `K` is chosen for a new journal; the audit and the
  adoption proposal for existing journals; re-homing. The audit has not been run: there is no production data here.

## D14 Time-based start

### D14a The unit defect

- **Question:** fix the mismatch on the current SPI?
- **Finding:** the event-sourced actor stamps events with `UnixNano`; `WithStartOffset`, `WithResetOffset`,
  `RebuildProjection(from)` and the runner's pending check convert the time with `UnixMilli`, so the requested time is
  effectively ignored. A throwaway test on the in-memory store (not committed) returned the event for a "from" one
  hour ago and for a "from" one year in the future, both converted with `UnixMilli`. PostgreSQL applies the same
  comparison and was not run.
- **Options:** (a) nanoseconds everywhere; (b) milliseconds everywhere; (c) leave it.
- **Resolution:** **approved: nanoseconds, on the current SPI, with a permanent regression** (committed failing tests
  for the in-memory store and for PostgreSQL, failing before the fix and passing after).
- **This is a behavior change, not a repair that preserves what the defect did.** After the fix a projection
  restarted "from a date" starts from that date: events before it are no longer re-read, and a "from" in the future
  yields nothing until time reaches it. Today every such restart reads from the beginning. Callers and operators
  that relied on that (knowingly or not) will see different results, and the release note must say so.
- **Zero time:** `time.Time{}` has no `UnixNano` (undefined outside 1678-2262); the fix must map the zero time to
  offset 0 explicitly.
- **Stored offsets need explicit treatment.** `offsets_store.current_offset` holds three populations: (1) progress
  committed by the runner, in nanoseconds, correct before and after; (2) values written by `ResetOffset`, in
  milliseconds, which under the nanosecond reading are tiny and mean "from the beginning", as the defect already made
  them behave, but whose intended date was never honored; (3) zero. No automatic rewrite is proposed: converting a
  millisecond value to nanoseconds would honor an old date and make a projection skip events it used to re-read. The
  treatment is an audit by magnitude (a value below 1e15 cannot be the nanosecond time of an event after 1970) and a
  per-row decision by the owners.
- **Still open:** the treatment of stored offsets; the release note.

### D14b A time-positioning interface

- **Question:** must the contract provide time-based starts once the cursor is opaque?
- **Options:** (a) no interface; (b) an optional capability `TimePositioner`; (c) a required method.
- **Resolution:** **deferred.** The need for "rebuild from a date" is kept, and its **semantics are defined before any
  interface is chosen.**
- **Still open:** what "from a date" means: which time (writer time `Timestamp`, publication time, commit time);
  inclusive or exclusive; what it does for late-committed events and for events removed by retention; whether it is a
  rebuild-only operation. `TimePositioner` in `contract.md` is a sketch for the owners to judge after that, not
  an adopted interface. It does not follow from D14a.

## D15 Retention check and existing deployments

- **Question:** how does a deployment adopt `ErrCursorOutsideRetention`?
- **Finding:** the check turns silent loss into a stopped consumer; a mode that detects the loss and then continues
  keeps the silent omission the contract forbids. Loss before the cutover cannot be detected.
- **Options:** (a) enforcing, preceded by a read-only audit; (b) per-projection allowlist; (c) a transitional
  "report-only" switch.
- **Resolution:** **the check is mandatory in conformant adapters, preceded by an audit. There is no conformant mode
  that detects loss and continues.** (c), if ever wanted, is a migration aid labelled non-conformant and
  time-limited, never a mode of the contract.
- **Still open:** the audit's design and who runs it.

## Open problems carried forward

| # | Problem | Owner of the next step |
|---|---|---|
| O1 | An unknown write outcome after retention removed the events (D2) | owners |
| O2 | The effect of erasure on projections (D3) | owners |
| O3 | Names and types of the SPI, with the first conformance slice (D1) | conformance slice |
| O4 | The joint design with #93: ownership, scoped progress, one migration of progress (D5, D7) | #93 |
| O5 | The treatment of stored offsets after the unit fix (D14a) | owners |
| O6 | The assignment algorithm, its versioning, `K` for new journals, and the adoption audit for existing ones (D13) | owners |
| O7 | The semantics of "rebuild from a date" (D14b) | owners |
| O8 | The restore runbook and the pre-resume validation (D6) | owners |
| O9 | `nextval` monotonicity with `CACHE 1`, not quoted in the PostgreSQL page that was read; the experiment's stress must assert it | experiment |
