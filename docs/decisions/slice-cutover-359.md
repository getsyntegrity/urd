# Slice cutover procedure for #359 (I-09b), coordinated with #351

Status: PROPOSAL, pending approval. Nothing here is an approved decision, and no schema, migration or write-path code is added by it. P1 (N = 1024, FNV-1a 64, the key encoding) is ratified and fixes only the calculation; it does not approve this procedure (ADR P2). The schema belongs to I-09a (#358), the position mechanism to Gate A (#352/#387) and the reader contract to #351 (draft: `openspec/specs/slice-reader/spec.md`, not approved), so those parts are stated as requirements, not as SQL.

Related: `logical-slices.md` (the calculation and the replacement contract), `scoped-offsets-retention.md` (offset identity today, migration 006), #350, #351, #352/#387, #358, #359, #362, #373/#374.

## 1. Facts this procedure rests on

Verified in the repository and in GoAkt `v4.5.7-actorof.1` (the version resolved by this module):

- `events_store.shard_number` is `BIGINT NOT NULL` with an index. `offsets_store` is keyed by `(tenant_id, projection_name, shard_number)` and stores `current_offset`, a Unix-nanosecond timestamp (migrations 001, 002, 005, 006).
- The old feed delivers an event of `(scope, shard)` when `timestamp > offset` (`persistence/postgres/event_store.go`, `GetShardEvents`). So "handled by the old projection" means `timestamp <= offset` for the event's legacy shard (assumption A1 below).
- The legacy value is not a function of persisted data. `ActorSystem().Partition` returns 0 outside a cluster, the partition of the actor's key in the distributed map inside one, and 0 on any lookup error (`actor/actor_system.go:2075`, `internal/cluster/cluster.go:1157`). One table can hold 0 for many unrelated entities, and the same entity can have had different values in different deployments. Consequence: the legacy `shard_number` of an existing row cannot be recomputed later, so it must never be overwritten.
- A deployment outside a cluster has every row in legacy shard 0, hence one offset row per projection and scope. Simplest case.
- `persistence/postgres` has only `events_store` and `offsets_store`. There is no durable state table in this module (#435): this procedure covers the journal and offsets. Other state stores carry `DurableState.Shard` in their own format and need their own step.
- The migrator serializes with an advisory lock and rejects a schema newer than the binary, but only for processes that run it. An old binary that does not run the migrator is not stopped by that check; "upgrade all writers together" (migration 006) is an operational rule only.
- Late commits can make an event visible after a later timestamp was already read (`persistence/events_store.go`). That is a pre-existing omission of the old reader. The procedure neither creates nor repairs it, except where quiescing makes it impossible for the pre-barrier data (step b).
- The legacy shard is not an entity key: an entity can have rows under more than one legacy shard (previous point). The old feed never ordered events of one entity across shards, so the transitional catch-up below inherits that and does not claim more.

## 2. What the procedure needs from #351 and Gate A

- Offset identity (mode, scope or cell, processor, version, slice) (#362, R-01); new rows keyed by slice, legacy rows never edited in place.
- Cursors are opaque and versioned; a legacy offset presented to the slice reader is rejected, never reused.
- `GetShardEvents` is deprecated with a retirement plan; the legacy column and legacy offset rows stay until it completes.
- A reader with a stable prefix per selection and slice range. Its position type is chosen by Gate A and may not be a timestamp. Gate A has no recorded result (#387's criteria are unchecked, `benchmark/` holds no result, the omission checks of #348 are not in `persistence/conformance`). This procedure therefore must not depend on a function that maps a legacy timestamp to a new position unless Gate A provides it. The recommended strategy (3.5, S2) does not need it.

## 3. Procedure

### 3.1 Definitions and the single rollback rule

- **Layout epoch**: a durable value with the states `LEGACY`, `PREPARED`, `FENCED`, `ACTIVE_NEW`. Every transition is a transaction. Rollback sets it back to `LEGACY`.
- **Barrier**: the point recorded at step (b), after quiescing, that separates pre-barrier rows from post-barrier rows. The two sets MUST be distinguishable by a persisted value written with each row (not by the writer-stamped timestamp alone). Which value (a per-row epoch or a monotone position) is a #358 requirement.
- **R-point**: the earlier of (1) the commit of the first REAL write by a new-layout binary and (2) the first time any consumer (projection runner, publisher, integration relay, workflow consumer) processes any new-layout row, the synthetic smoke row included. This is the one rollback rule, and every place below refers to it:
  - Before the R-point, rollback is EXACT with no data loss (3.8).
  - From the R-point on, rollback is not exact: restore from backup, which loses the writes made since the barrier, or roll forward.
  - Starting the new binaries (step f) does NOT end exact rollback. The single synthetic smoke row does not either, on one condition that the procedure enforces: no consumer runs from step (a) until the row has been discarded (step g), so no effect, applied mark or offset can exist for it (V9). A real write, or a consumer processing a new-layout row, does end it.

### 3.2 Phases

| Phase | Writers | What happens | Exit check |
| --- | --- | --- | --- |
| P0 Preconditions | old | Section 4 checklist, a backup whose restore was exercised, snapshot S0 of the journal taken, and automatic retention and explicit erasure disabled from S0 (or every deletion logged) | All items evidenced |
| P1 Prepare | old | Additive schema (#358), backfill of the slice | V1a, V1c, V2 on the data so far |
| P2 Plan | old | Preview of the per-projection plan, thresholds and replay report into staging; the binding plan is recomputed at step (c) | none: the preview is informative |
| P3 Window | stopped | Steps (a) to (h) | V1b to V9 as listed per step |
| P4 Run | new | New binaries write the slice; legacy kept | Before the R-point rollback is exact |
| P5 Retire | new | After the `GetShardEvents` retirement plan, drop legacy | Irreversible |

Abort in P1 or P2 drops the additions and nothing else: the old layout is untouched and live.

### 3.3 Backfill (P1)

For each `(scope, persistence id)` the slice is `sliceOf(scope, persistence id)`, a pure function. The migrator pages by keyset over a stable key and writes only the new slice column, only where it is NULL; it records durable progress (last key, batch count); a batch repeated after a crash gives the same result. It runs while old writers insert, and the rows they add have a NULL slice, closed at step (c). It uses the same code new writers use (the exported `SliceOf`), and V2 recomputes it with an independent implementation, so migrator and writers cannot silently agree on a wrong value.

### 3.4 Window (P3) as a checked state machine

Each step records its state durably, is idempotent, and has a stated outcome if the process dies there.

| Step | Precondition | Action | Verification | If interrupted or failed |
| --- | --- | --- | --- | --- |
| (a) Fence | Epoch `PREPARED` | Stop old writers and projection runners; record `FENCED`, from which the database guard rejects journal writes by every role except the migration role (V8) | V4a: no old writer or runner connection | Resume at (a); nothing visible changed |
| (b) Quiesce | `FENCED` | Wait until no open or prepared transaction on the journal and no in-flight batch; record the barrier | V4: none older than the barrier, none prepared | Resume; if it cannot quiesce within the bound, abort and unfence |
| (c) Final backfill and final plan | Barrier recorded, runners stopped | Fill every NULL slice, and recompute the plan items (the S2 thresholds, the S1 seeds, the replay report) from the now frozen legacy offsets into staging: the plan of P2 is a preview made while the runners were still advancing the offsets, and is not the binding one. Record a checksum of the legacy offset rows | V2 (exhaustive) | Idempotent; resume |
| (d) Verify | (c) done | Run V1a, V1b, V1c, V2, V3, V6 on quiesced data | All pass | Any mismatch aborts; unfence |
| (e) Switch | (d) passed | In ONE transaction: first re-check that nothing was inserted since the barrier and that the legacy offset rows did not change (the row count and the highest persisted insertion value equal those recorded at (b), and the checksum of the legacy offset rows equals the one of (c); otherwise abort), then set `ACTIVE_NEW`, activate the offsets per 3.5, mark superseded the legacy offset rows of the projections that use S1 (those of S2 projections stay live as the catch-up cursor and are marked superseded when that projection's catch-up completes), and bind the barrier recorded at (b) to the new epoch | V7: the transaction's effects are all present or none, and the re-check passed | Atomic; either `FENCED` with nothing switched, or `ACTIVE_NEW` complete |
| (f) Start | `ACTIVE_NEW` | Start the new write path only. Projection runners, publishers, integration relays and workflow consumers stay stopped. New binaries refuse to start unless the epoch is `ACTIVE_NEW` | V8 startup refusal checked once with a binary in the wrong epoch | Operational; exact rollback still available |
| (g) Smoke | New write path up; every consumer and every other writer stopped | One synthetic write through the new write path, with a persistence id generated for the run and recorded in the plan. Read it back from the persisted row and check scope, slice and revision. Then DISCARD it with an explicit erasure, in a transaction that also checks that nothing references it | V5, V9 | A failed V5 keeps rollback exact: discard the row and roll back. A discard interrupted midway is re-run before anything else. A non-zero V9 means a consumer or a writer touched it: that is the R-point |
| (h) Open | Smoke row discarded and V9 zero | Start projection runners, publishers, relays and consumers, then admit the other writers. Their first commit is the first real write | none new | Exact rollback ends here (3.8) |

Consumers and other writers are admitted only at (h), after the smoke row is discarded. The synthetic row is therefore never visible to a consumer, which is what keeps its removal from leaving effects, applied marks or offsets behind.

### 3.5 Offsets: two strategies

Notation: scope `a`, projection `P`, new slice `s`, legacy shard `x`; `C(s)` is the set of legacy shards that hold at least one pre-barrier event of slice `s` in scope `a`; `offset(P, x)` is the legacy offset (0 if no row).

Assumptions, each with how it is enforced:
- **A1**: for the old feed, a pre-barrier event `E` in shard `x` with timestamp `t` counts as handled exactly when `t <= offset(P, x)`. Source: the read predicate in the code. The late-commit omissions of the old reader are pre-existing and out of this procedure.
- **A2**: after the barrier no event can commit with a pre-barrier identity. Enforced operationally by step (b) and V4, and structurally by the writer block (the guard of V8 from `FENCED`, and the stopped writers) until (h), where the first writers admitted are the new-layout ones.
- **A3** (S1 only): the Gate A mechanism provides `PositionAtOrBefore(T)`, a position `P` such that every event with timestamp greater than `T` is after `P`.

**S2, recommended: barrier plus legacy catch-up.**
1. For each projection and slice, pre-barrier events are delivered by a transitional reader that is the old read with one added predicate: scope `a`, `slice = s`, legacy `shard = x`, `timestamp > offset(P, x)`, for each `x` in `C(s)`, in the old order. The cursor of this phase is the legacy offset itself, per shard.
2. When every shard of `C(s)` has no more pre-barrier events, the slice cursor is created at the barrier and the new reader (Gate A positions) takes over. Post-barrier events have positions after the barrier by construction.
3. The new reader MUST NOT deliver a slice's post-barrier events before that slice's catch-up is complete, so one entity's pre-barrier events precede its post-barrier ones.
- Why it is exact: the set delivered in phase 1 is, by A1, exactly the set the old projection had not handled, with the old reader's own guarantees; nothing is skipped and nothing handled is repeated, apart from the at-least-once redelivery of a crash between delivery and commit. A2 makes the pre-barrier set closed. Post-barrier is the new reader's contract.
- It needs no `PositionAtOrBefore`, so it does not depend on what Gate A chooses. It needs a bounded transitional code path (the old query with a slice predicate) that is deleted at P5.
- Limits: ordering between shards inside phase 1 is the old reader's (none); it does not repair omissions that already existed; a transitional reader must exist in the adapter (a capability the adapter declares).

**S1, fallback: minimum seed.**
- `seed(P, s) = min over x in C(s) of offset(P, x)`, translated into a new-reader position with `PositionAtOrBefore`. The slice cursor starts there.
- Conditions under which it is safe: A1, A2 and A3. Under them, an unhandled event `E` has `t > offset(P, x) >= seed`, so by A3 the new reader returns it: nothing unhandled is skipped. The proof stops there: if A3 does not hold for the chosen mechanism, S1 MUST NOT be used.
- Cost: a handled event with `seed < t <= offset(P, x)` is presented again. The exact count per projection and slice is computed in P2 and reported before the window.
- Use only for a projection whose consumer is idempotent by event identity and has no external effect (3.7).

**Required evidence before either strategy is enabled** (to be implemented with the migration, with fakes, no real database):
- A model-based property test over generated histories (shards with timestamps, offsets per projection, several entities, slices computed by `sliceOf`): for S2, the delivered set equals exactly the unhandled set; for S1, it is a superset of the unhandled set and the extra events satisfy `seed < t <= offset`. The test fails if a mutation (for example `max` instead of `min` for S1, or a skipped shard of `C(s)`) is introduced.
- A conformance check of A2 for the adapter: after quiesce, an insert stamped before the barrier is impossible.

### 3.6 Retention

- Before the window record, per scope, the first available position and, per persistence id, whether the sequence starts at 1 and whether any row is deleted. A gap means events were removed by retention or erasure.
- S2 and S1 do not need removed events: they start from existing offsets. The rebuild path (new projection version, fresh cursors, the ADR's pointer switch) needs the complete history, so a gap rules it out for that scope and a replay is never called a full recovery. Aggregate snapshots restore aggregate state; they cannot rebuild a projection.
- Automatic retention (`RetainedEventsDeleter`) stays disabled until it validates the new offset identity, and is enabled again only after the cutover is verified.

### 3.7 Duplicate effects

- S2 repeats nothing except the at-least-once window of a crash, which already exists. Consumers keep handling by event identity `(scope, persistence id, sequence number)`.
- S1 repeats events in `(seed, offset]`. Old projections have no applied marks (the table is new, #358), so a repeat cannot be detected by a mark: the destination must be idempotent by upsert, and there must be no external effect (publisher, integration, workflow command) for that projection, or the effect must be de-duplicated by event identity. Without both, S1 is blocked for that projection and S2 or a rebuild is used.
- Whatever the strategy, a projection declares its effect class before the cutover (3.9 V3 reports it).

### 3.8 Rollback

- Before the R-point (exact): stop new binaries, discard the synthetic smoke row if it still exists (it has no effect, applied mark or offset, because no consumer ran: V9), set the epoch back to `LEGACY`, reactivate the legacy offset rows that were superseded, drop the additions. The legacy columns and offsets were never changed, so the old layout resumes with no data loss.
- From the R-point: the rows written since the barrier carry no meaningful legacy shard; an old binary would see them under a wrong or no shard. The options are restore from backup (losing the writes since the barrier, identified by the persisted barrier value) or roll forward. If a consumer processed a new-layout row, including the synthetic one, this procedure does not undo what that consumer produced: applied marks and offset advances in the destination could be removed by event identity, but external effects and projection rows derived from the row cannot be assumed removable, so that case is the R-point, not a rollback case. This is stated, not promised away.
- P5 is irreversible and happens only after the `GetShardEvents` retirement plan completes.

### 3.9 Verification checks

None relies on the migrator's own counters.
- **V1a Nothing lost or altered.** Old writers keep inserting during P1 and P2, so the journal at the barrier is not equal to the snapshot S0 taken before P1; it can only have grown. Every `(scope, persistence id, sequence number)` of S0 is present, with the same payload checksum, at the barrier and after (e), and the row count is never lower than S0's.
- **V1b Frozen set.** The set, the per-scope counts and a checksum taken at (b), after quiescing, equal the state after (c) and after (e). Between the barrier and the switch nothing can be inserted (writers stopped, V4) and the migration changes only the slice column.
- **V1c No unaccounted deletion.** Retention and erasure are disabled from S0 until the switch, or every deletion in that interval is logged and subtracted in V1a. A deletion that is not accounted for fails V1.
- **V2** Every row's slice equals the ratified function applied to its `(scope, persistence id)`, recomputed by a separate tool with its own FNV-1a over the specified key (the reference vectors are the contract).
- **V3** Every plan item (thresholds for S2, seeds for S1, per-projection effect class and path) of the FINAL plan of step (c) is recomputed from raw events and the frozen offsets by a separate query and compared with staging; the replay report is reproduced. The preview of P2 is not compared: it can legitimately differ, because the offsets were still advancing.
- **V4** No open or prepared transaction older than the barrier and no writer connected, checked at (b) and again at (d).
- **V5** The smoke row, read back from the persisted record, has the expected scope, slice and revision, for event-sourced and, in its own store, durable state.
- **V6** Every projection has a declared path (S2, S1 or rebuild) or an audited exception; none is "unknown".
- **V7** After (e), the epoch, the activated offsets, the legacy rows superseded for the S1 projections and the barrier are all present, or none of them; and the re-check of nothing inserted since the barrier passed inside the same transaction.
- **V8** A new binary refuses to start under epoch `LEGACY`, `PREPARED` or `FENCED`. The database-level guard (a requirement on #358) rejects journal writes by any role that is not the migration role under `FENCED`, so an old writer restarted by an orchestrator during the window fails loudly instead of inserting behind the barrier, and rejects an old binary under `ACTIVE_NEW`. Legacy offset rows are not blocked by the guard: they are protected by the stopped runners (V4a) and by the checksum that step (e) re-checks.
- **V9 Smoke leaves no trace.** After the discard, the synthetic persistence id has no row in the journal and is referenced by no applied mark, no offset, no outbox or publisher intent and no projection row, and no consumer or other writer was connected from (a) to (h). A non-zero result means the synthetic row was processed, which is the R-point.

### 3.10 Interruption and resumption

| Where it dies | State left | Resume or abort |
| --- | --- | --- |
| P1 backfill batch | Some rows have a slice, others NULL | Resume from the recorded key; same result |
| P2 plan | Staging partly written | Rebuild the preview from raw data |
| (a) | Some writers stopped | Resume (a); unfence to abort |
| (b) | `FENCED`, barrier not recorded | Resume (b) or unfence |
| (c) | Barrier recorded, some NULL slices | Resume (c) |
| (d) | Nothing changed | Resume or abort |
| (e) | Atomic | Either `FENCED` unchanged or `ACTIVE_NEW` complete |
| S2 phase 1, per projection and shard | Legacy cursor advanced up to the last committed batch | Resume from the legacy cursor; a crash repeats at most one batch |
| (f), (g) | New write path partly up, consumers stopped | Exact rollback still available until the R-point; an interrupted discard of the smoke row is re-run before anything else |
| (h) | Consumers or writers partly admitted | Past the R-point if any real write committed or any consumer processed a new-layout row; otherwise still exact |

## 4. Preconditions for integrating the writer, and what is still missing

The writer integration (export `SliceOf` and `SliceCount`, replace the two `ActorSystem().Partition` calls, gated by the epoch) MUST NOT land, and writes MUST NOT use the new calculation, until ALL of these hold:

| Precondition | Owner | State today |
| --- | --- | --- |
| This procedure approved (decisions in section 5) | maintainer, #359 | Not approved |
| Slice reader contract approved | #351 | Draft, human gate pending |
| Gate A recorded: position mechanism, eligibility bound, results | #352, #387 | No result; #348's checks absent |
| Additive schema: slice column apart from the legacy one, per-row barrier value, layout epoch, applied-marks table, database-level writer guard, relaxed legacy `NOT NULL` for new rows | #358 | Not started; depends on #351, #352, #387 |
| Migrator implemented and tested (backfill, state machine, S2 transitional reader, rollback), with an upgrade test from the current schema with data including an interrupted and resumed run | #359 | Not started |
| Per-projection effect class and path declared | #373, #374 | Not started |
| Opaque Scope keeps the hashed bytes | #349 | Not started |

When they hold, the integration change calls the slice computation with `(entity.scope, entity.persistenceID)` taken after identity binding in both actors (durable state in `PreStart`, event-sourced in its start handler), never with the actor name, namespace or physical partition, and starts only under epoch `ACTIVE_NEW`. Its evidence: unit tests (go-specs, fakes, no real resources) that both write paths receive the slice computed from scope and persisted id regardless of actor name and partition; and a few `inttest` flows reusing the harness: write, persist, read for both paths (scope, slice, revision from the persisted record); restart, recovery and continuation; migration with an interrupted and resumed run.

## 5. Decisions that need approval before the cutover can be enabled

1. Offline writer window with online backfill (recommended), versus designing an online cutover with dual writes (a protocol change).
2. Offset strategy: S2 (recommended, no dependency on Gate A) versus S1 (needs A3 and idempotent consumers), or S2 with S1 as an audited exception.
3. The single rollback rule in 3.1: exact until the first real new-layout write or the first time a consumer processes a new-layout row; the smoke row is synthetic, written with every consumer stopped, and discarded before consumers start.
4. The policy for a retention gap: block the rebuild path or accept it with an audit record.
5. The writer guard, the per-row barrier value and the layout epoch (with #358), and when the legacy column is dropped.
6. Whether the transitional reader of S2 is an adapter capability and who owns it.

## 6. What this does not do

It does not change a write path, a schema, `go.mod` or GoAkt, and it does not enable or approve the cutover. It does not claim S1 safe without A3, it does not describe a replay as full recovery when events were removed, and it does not promise exact rollback after the first real new-layout write.
