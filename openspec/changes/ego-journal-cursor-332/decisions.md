# Decision record for design.md 14.4 (#332)

Status: PROPOSED RESOLUTIONS, revision 2, awaiting the owners. Nothing here is decided by this document except D11,
which the owner decided. The SPI stays not approved and nothing is implemented. This revision applies the owner's
objections to D13, D14 and D15 and does not add anything to the contract.

Every entry has the same parts: **Question**, **Finding** (what the review found, with its source), **Options**,
**Recommendation**, **Threshold** (a number or test that would settle it, or "none" when none is proposed) and
**Decider**. A threshold is a proposal for the owners to accept or change; it is not a finding. Where a decision
needs data nobody has yet, the entry says which.

| ID | Question | Recommendation | Decider |
|---|---|---|---|
| D1 | SPI shape and names | approve the semantics first, defer names and types | owners |
| D2 | Write behavior change | adopt, after a caller audit | owners |
| D3 | Reason on `DeleteEvents` | add a reason; the retention floor moves for retention only | owners |
| D4 | Cursor size | no framework bound until real tokens are measured | owners |
| D5 | `ValidateAdvance` enforcement, progress, ownership | rule plus conformance; optional co-located validation; ownership with #93 | owners, #93 |
| D6 | Journal id and generation | UUID at creation; generation advanced by an explicit restore step | owners |
| D7 | Data migration | #93 scope first, then audits, backfill, offset mapping, cutover | owners |
| D8 | Which PostgreSQL strategy | run the experiment against the bars below | owners |
| D9 | Backlog metrics | publication backlog required, consumer backlog optional | owners |
| D10 | Index issue | open it now | owner |
| D11 | #332 regression timing | DECIDED: enters with the implementation | owner |
| D12 | Delivery plan | documentation review, then slices in order | owners |
| D13 | Stable stream identity of an entity | a logical stream key owned by Urd, separate from actor placement | owners |
| D14 | Time-based start | D14a fix the unit defect on the current SPI; D14b decide an interface separately | owners |
| D15 | Retention check and existing deployments | enforcing from the start; a read-only audit beforehand; no conformant "report-only" mode | owners |

## D1 SPI shape and names

- **Question:** approve the proposed shape (`StreamReader`, `ReadStream`, `StreamHeads`, `ValidateAdvance`, `Page`,
  `JournalPosition`, `ProgressStore`, the typed errors) now?
- **Finding:** the semantics moved in two review rounds (G1, G3, G5, progress, the epoch rename). Approving a shape
  now would freeze names around guarantees that changed.
- **Options:** (a) approve the whole shape now; (b) approve the semantics and defer names and types until the
  conformance suite is written against them; (c) approve only the read side (`ReadStream`, `StreamHeads`, `Page`,
  `JournalPosition`) and defer progress and the write outcomes.
- **Recommendation:** (b). The conformance suite is the best test that the semantics can be implemented, and it
  should exist before the types do.
- **Threshold:** none.
- **Decider:** owners. Blocks the public-contract gate.

## D2 Write behavior change

Covers: idempotent replay before the precondition, `ErrIdentityConflict`, `ErrSequenceOrder`, and the stream-identity
rule of D13.

- **Question:** accept that `WriteEvents` stops skipping duplicate identities silently and stops accepting out-of-order
  sequences, including under `Unconditional()`?
- **Finding:** today an unconditional write skips a duplicate identity whatever its content and accepts any sequence
  order. Non-test callers: `internal/engine/eventsource/events_writer_actor.go` (conditional or unconditional by
  configuration), `internal/engine/saga/saga_actor.go` (two `Unconditional()` writes) and
  `migration/tenant_adoption.go` (`ExpectGenesis`). Replay equality compares payload, manifest, metadata and
  encryption fields and not `Timestamp` (contract G1). Open edge: a retry that arrives after retention removed the
  committed events reports `ErrSequenceOrder` for a write that did succeed.
- **Options for the change:** (a) adopt all the rules under one gate; (b) adopt replay and identity first and the
  sequence and stream rules after the audit; (c) keep unconditional writes permissive and scope G5 to conditional
  writers (the review rejected this: "unconditional" must not mean "no ordering invariant").
- **Options for the edge:** (i) require the retry window to be shorter than the retention delay; (ii) keep a bounded
  window of recent batch ids in the revision row.
- **Recommendation:** (a), after the audit confirms that the three callers retry the batch they hold unchanged; (i)
  now, revisiting (ii) only if a measured retry window approaches the retention delay.
- **Threshold:** the audit covers the three callers; the measured retry window must stay below the configured
  retention delay (no number proposed: the retention delay is configuration).
- **Decider:** owners. Blocks the slice that changes `WriteEvents`.

## D3 `DeleteEvents` reason

- **Question:** how does a consumer tell erasure from retention?
- **Finding:** a consumer must continue past erased events (it must not process erased data) and must stop on retention
  loss. The current `DeleteEvents` cannot tell them apart.
- **Options:** (a) a `Reason` parameter; (b) separate methods; (c) treat every deletion as retention and let an
  erasure stall lagging consumers.
- **Recommendation:** (a). Under (c) an erasure would force every lagging projection to rebuild.
- **Threshold:** none.
- **Decider:** owners. Blocks G8.

## D4 Cursor size

- **Question:** what bound does the framework put on a serialized cursor?
- **Finding:** no real token has been measured. A PostgreSQL cursor is estimated at a few tens of bytes (version,
  adapter tag, 16-byte journal id, epoch, binding, position).
- **Options:** (a) fix 256 bytes now; (b) fix a larger bound now; (c) no framework bound: each adapter documents its
  own and the framework fixes one after measuring.
- **Recommendation:** (c). `offsets_store` uses an unbounded `bytea` meanwhile.
- **Threshold:** none until the serialized size of the first adapter's token and of a Cassandra sketch are measured.
- **Decider:** owners.

## D5 `ValidateAdvance` enforcement, progress and ownership

- **Question:** how is "commit only a validated cursor" enforced, and where do ownership and fencing live?
- **Finding:** validation is a rule on consumers; a store holding opaque bytes cannot enforce it on a consumer that
  skips it. The progress epoch is named apart from the journal generation (G7).
- **Options:** (a) rule plus conformance of the runtime; (b) co-located validation inside the commit when progress
  and the journal share a database; (c) a signed advance token returned by `ValidateAdvance` and required by `Commit`.
- **Recommendation:** (a) now, (b) as an optional capability, (c) rejected as too heavy. Ownership and the fence stay
  with #93; `Commit` carries an optional fence.
- **Threshold:** the runtime conformance check P05 must fail a runtime that commits an unvalidated older `Next`.
- **Decider:** owners and #93.

## D6 Journal id and generation

- **Question:** how are the journal instance id and generation created and advanced?
- **Finding:** a restore from backup restores the journal metadata too, so the old generation returns with the old
  data.
- **Options:** (a) an explicit restore step that advances the generation; (b) detect a restore automatically.
- **Recommendation:** (a), plus the safety net that a cursor ahead of the published frontier is
  `ErrCursorInvalidated`. (b) is not reliable on plain PostgreSQL.
- **Threshold:** none.
- **Decider:** owners.

## D7 Data migration

- **Question:** in what order is existing data migrated?
- **Finding** (`postgres-batch-publication.md` section 11): the backfill must preserve per-entity order (running
  maximum of timestamp, ordered by sequence); the legacy offset mapping is conservative (it can redeliver, never
  skip); offsets have no scope until #93; legacy offsets may be in milliseconds and map to the zero cursor, as they
  already behaved; entities that span streams must be resolved first; loss before the cutover is undetectable.
- **Options:** (a) one strategy-independent migration (the order below); (b) a per-adapter migration owned by each
  adapter.
- **Recommendation:** (a): #93 gives progress its scope; run the audits; backfill; map offsets; cutover window with
  the one-way-door rollback (restore plus reset).
- **Threshold:** the audits must report zero unexplained entities spanning streams, and the list of offsets by
  magnitude must be reviewed by the owners, before the cutover.
- **Decider:** owners. Blocks the data-migration gate.

## D8 Which PostgreSQL strategy

- **Question:** is post-commit batch publication worth building next to the measured variants?
- **Finding:** it has a no-starvation argument per stream and across streams, and is neither implemented nor
  measured.
- **Options:** (a) run the experiment against fixed bars; (b) run it with different bars; (c) skip it and choose by
  the operating facts of `design.md` 13.9.
- **Recommendation:** (a), with the owners fixing the bars before it runs. Proposed bars:

| Question | Proposed bar |
|---|---|
| Correctness | the conformance suite and the stress pass with no omission, duplicate or deadlock; otherwise stop |
| Writer cost at 300 tx/s offered | write p95 within 2x of `current` |
| Delivery at 300 and 500 tx/s offered | start-to-delivery p95 below 100 ms with `tau = 5 ms` |
| Held publisher, same and other stream | writers unaffected; the stall confined to its stream |
| Recovery after a 10 s stall at 300 tx/s | backlog drained within 30 s at the chosen `N` |
| Cost per event against `current` | bytes and WAL each within 2x; dead tuples reported |

- **Threshold:** the table above. These numbers are proposals, not findings.
- **Decider:** owners. Blocks the adapter slice.

## D9 Backlog metrics

- **Question:** which backlog metrics are required of an adapter?
- **Finding:** G2's condition cannot be checked without publication backlog; consumer backlog can be expensive.
- **Options:** (a) both required; (b) publication backlog required, consumer backlog optional (an estimate or
  "unknown" is allowed); (c) both optional.
- **Recommendation:** (b).
- **Threshold:** none.
- **Decider:** owners.

## D10 Index issue

- **Question:** open `evidence/issue-draft-timestamp-index.md` as an issue?
- **Finding:** independent of #332; it speeds reads and does not fix it (the control omitted more events).
- **Options:** (a) open it now; (b) open it together with D14; (c) do not open it.
- **Recommendation:** (a). Nothing has been opened.
- **Threshold:** none.
- **Decider:** owner. The owner has asked that no issue be opened yet.

## D11 #332 regression timing

DECIDED by the owner on #338: it enters `develop` together with the implementation that makes it pass; the
experiments may keep an explicit expected failure; the production CI is not changed to accept the omission.

## D12 Delivery plan

- **Question:** how is the work sliced?
- **Options:** (a) documentation review, then types and the conformance suite behind the public-contract gate, then
  the adapter and its migration behind the migration gate, then the runner and offsets with #93; (b) one large
  implementation; (c) a spike of the batch strategy first.
- **Recommendation:** (a). Nothing is implemented until the documentation review closes.
- **Threshold:** none.
- **Decider:** owners.

## D13 Stable stream identity of an entity

- **Question:** what makes an entity's events stay in one stream, so that per-entity order (G5) holds?
- **Finding:** streams are per `(scope, shard)` and carry no order between them. If an entity's events could land in
  two streams, a consumer reading both could see `n+1` before `n`. Today `Event.Shard` is
  `ActorSystem().Partition(persistence id)` (`event_sourced_actor.go`), a function of the entity name and of GoAkt's
  partition count, and the revision row records no shard. **Tying stream identity to GoAkt's partition count would
  make a change of topology a change of stream identity** and could block topology changes. Stream identity and the
  placement of an actor are two different things and should be separate.
- **Options:**
  - (A) **A logical stream key owned by Urd.** Urd assigns each entity a stream key when its first event is
    persisted, derived from the persistence id with a number of logical streams `K` that is a property of the
    journal, fixed at creation and stored in the journal metadata. `Event.Shard` carries the stream key. The cluster
    can change its partition count freely; actors are placed however GoAkt places them. Changing `K` is an explicit
    re-homing migration, not designed here. The revision row records the key, and a later event with another key is
    rejected (`ErrShardMismatch`; the name is open).
  - (B) Record the GoAkt partition of the first event and reject any other. Existing entities keep their stream, but
    new entities follow the new partition count, so the set of streams grows silently, and an engine that recomputes
    instead of reading the record is rejected on a topology change.
  - (C) Declare GoAkt's partition count immutable for a journal and leave it unenforced.
- **Recommendation:** (A). For an existing journal, `K` would be adopted from the partition count in force when it was
  written (a single-node deployment is `K = 1`, stream 0), so existing `Event.Shard` values stay valid. Choosing `K`
  for a new journal, and whether it can ever change, are open questions inside this option.
- **Threshold:** the migration audit finds no entity whose events already span two streams (otherwise each is resolved
  explicitly); conformance C22 passes.
- **Decider:** owners. Blocks G5 and the migration.

## D14 Time-based start

Two separate decisions. Fixing the unit defect does not oblige the owners to approve a new interface.

### D14a The unit defect (independent of the contract)

- **Question:** fix the existing mismatch on the current SPI?
- **Finding:** the event-sourced actor stamps events with `UnixNano`; `WithStartOffset`, `WithResetOffset`,
  `RebuildProjection(from)` and the runner's pending check convert the time with `UnixMilli`, so the requested time is
  effectively ignored. Evidence: a throwaway test on the in-memory store (not committed) wrote an event stamped in
  nanoseconds and read with a "from" one hour ago and a "from" one year in the future, both converted with
  `UnixMilli`; both returned the event. PostgreSQL applies the same comparison and was not run.
- **Options:** (a) nanoseconds everywhere (convert with `UnixNano`); (b) milliseconds everywhere (change what the actor
  stamps, which is much wider); (c) leave it.
- **Recommendation:** (a) as its own change, with a committed failing test on the in-memory store and on PostgreSQL.
  Compatibility: values stored by earlier resets are in milliseconds and already behave as "from the beginning"; after
  the fix they still do; committed progress is in nanoseconds and is read correctly.
- **Threshold:** the failing test must fail before the fix and pass after, for both stores.
- **Decider:** owner. No issue is opened until it is decided.

### D14b A time-positioning interface (a decision of its own)

- **Question:** must the contract provide time-based starts once the cursor is opaque?
- **Finding:** an opaque cursor has no time meaning, so a start "from a moment" needs something that maps a time to a
  cursor. Whether the feature must survive the move to opaque cursors is a product question, not a consequence of
  D14a.
- **Options:** (a) no interface: time-based starts are dropped or stay on adapters that have timestamp cursors;
  (b) an optional capability `TimePositioner` with a conservative contract (every available event with
  `Timestamp >= t` is delivered, earlier ones may appear); (c) a required method.
- **Recommendation:** decide after D1 and after the owners say whether `RebuildProjection(from)` must be kept; if it
  is kept, (b). Not needed for D14a.
- **Threshold:** none.
- **Decider:** owners.

## D15 Retention check and existing deployments

- **Question:** how does a deployment adopt `ErrCursorOutsideRetention`?
- **Finding:** the check turns silent loss into a stopped consumer, and the janitor can delete after a snapshot, so a
  lagging consumer will hit it at once. A mode that detects the loss and then continues keeps the silent omission
  the contract forbids, so **it is not a conformant mode**. Loss before the cutover cannot be detected: the retention
  floor starts at zero.
- **Options:**
  - (a) Enforcing from the adapter's first release, preceded by a **read-only audit** run before enabling: for each
    projection and stream, how far its progress lags and how much has been deleted since. The audit informs the
    owners and changes nothing.
  - (b) Enforcement per projection through an allowlist: a projection is conformant only once enabled, and the
    others are not claimed conformant.
  - (c) A transitional "report-only" switch. If the owners ever want it, it is a migration aid labelled
    **non-conformant**, time-limited, and never described as a mode of the contract.
- **Recommendation:** (a). Not (c) as a conformant mode.
- **Threshold:** the audit must list every projection whose progress precedes events no longer retained, as far as the
  data allows; the owners review that list before enabling. The adapter has no report-only runtime mode.
- **Decider:** owners.
