# Slice cutover proposal for #359 (I-09b), coordinated with #351

Status: PROPOSAL. Nothing here is an approved decision, and no schema, migration or write-path code is added by it. P1 (N = 1024, FNV-1a 64, the key encoding) is ratified and fixes only the calculation; it does not approve this strategy (ADR P2). The schema shape belongs to I-09a (#358) and the position mechanism to Gate A (#352/#387), so those parts are stated as requirements, not as SQL.

Related: `logical-slices.md` (the calculation and the replacement contract), `scoped-offsets-retention.md` (offset identity today, migration 006), #350, #351 (reader contract, cursor, offset identity), #352/#387 (position mechanism), #358 (I-09a schema), #359 (this migration), #362 (checkpoint model), #373/#374 (idempotent consumers, fencing).

## 1. Facts this proposal rests on

Verified in the repository and in GoAkt `v4.5.7-actorof.1` (the version resolved by this module):

- `events_store.shard_number` is `BIGINT NOT NULL` with an index (`001`, `002`). `offsets_store` is keyed by `(tenant_id, projection_name, shard_number)` and stores `current_offset`, a Unix-nanosecond timestamp (`005`, `006`).
- The feed reads `timestamp > offset` per `(scope, shard)` (`persistence/postgres/event_store.go`, `GetShardEvents`). So "handled by the old projection" means `timestamp <= offset` for the event's legacy shard.
- The legacy value is not a function of persisted data. `ActorSystem().Partition` returns 0 when the system is not in a cluster, returns the partition of the actor's key in the distributed map when it is, and returns 0 on any lookup error (`actor/actor_system.go:2075`, `internal/cluster/cluster.go:1157`). One table can therefore hold 0 for many unrelated entities, and the same entity can have had different values in different deployments. Durable state computes it in `PreStart`; whether the actor is already registered in the map at that point is unverified (this is the #438 question).
- Consequence: the legacy `shard_number` of an existing row cannot be recomputed later. If it is overwritten, it is gone. This is the reason for the additive design below.
- Single-node and non-cluster deployments have every row in legacy shard 0 and therefore one offset row per projection and scope. This is the simplest and safest case.
- `persistence/postgres` has only `events_store` and `offsets_store`. There is no durable state table in this module (#435), so the cutover below covers the journal and offsets. Other state stores carry `DurableState.Shard` in their own format and need their own step.
- The migrator serializes with an advisory lock and rejects a schema newer than the binary, but only for processes that run it. An old binary that does not run the migrator is not stopped by that check, which is why migration 006's "upgrade all writers together" is an operational rule only.
- Late commits can make an event visible after a later timestamp was already read (`persistence/events_store.go`). That is an existing omission of the old reader, not something the cutover creates or can repair.

## 2. What the cutover must satisfy from #351

- Offset identity becomes (mode, scope or cell, processor, version, slice) (#362, R-01). New offset rows are keyed by slice; legacy rows are never edited in place (ADR).
- Cursors are opaque, versioned and carry a header (format, cell, selection fingerprint). A cursor produced by the shard reader is rejected by the slice reader (`ErrCursorMismatch` or its final equivalent). No old cursor is silently reused.
- `GetShardEvents` is deprecated with a retirement plan. The legacy column and the legacy offset rows are kept until that plan completes.
- The new reader serves a stable prefix per selection and slice range. Its position type is chosen in #352/#387 and may not be a timestamp (xid8, a counter per slice, or batched publication). Seeds below are therefore defined on legacy timestamps and must be translated into the new position by that mechanism's backfill. That translation cannot be specified before Gate A selects the mechanism. This is a hard dependency.

## 3. Proposal

### 3.1 Principle: additive, then switch once

- Never modify an existing journal column. The slice goes into a new place; the legacy `shard_number` stays untouched until the retirement plan. This keeps the set of `(scope, persistence id, sequence number)` identical by construction and keeps rollback possible.
- Keep a single, atomic switch. Mixed old and new writers are not supported.
- Offline for writers during a short window; backfill and verification run online before it. An online cutover with concurrent writers is not proposed: without dual writes, a concurrent insert cannot be proven to land in the new layout, and designing dual writes would require a protocol change.

### 3.2 Phases

| Phase | Writers | What happens | Reversible |
| --- | --- | --- | --- |
| P0 Preconditions | old | Gates below pass | n/a |
| P1 Prepare | old | Additive schema (per #358), backfill of the slice for existing rows | Yes: drop the additions |
| P2 Verify and plan | old | Independent verification, seed computation into a staging area, replay-volume report | Yes |
| P3 Window | stopped | Fence, quiesce, final backfill, final verification, atomic switch | Exact until step (g) |
| P4 Run | new | New binaries write the slice; legacy kept | Only until the first new-layout write (see 3.7) |
| P5 Retire | new | After the `GetShardEvents` retirement plan, drop legacy | No |

P0 preconditions:
- #358 (additive schema, layout epoch, writer guard) is approved and implemented.
- #351 is approved, including the cursor header and offset identity, and Gate A has chosen the position mechanism so the seed translation is defined.
- A backup exists and its restore was exercised.
- Per scope, the first available position is recorded (see 3.6).
- Each projection declares whether its consumer is idempotent (#373) and which of the paths in 3.5 it uses.
- The deployment state the plan assumes is confirmed: no prepared transactions, a bounded transaction timeout for every role that writes, writers inventoried.

### 3.3 Backfill (P1)

For each `(tenant, persistence id)` the slice is `sliceOf(scope, persistence id)`, a pure function. The migrator:
- Pages with keyset pagination over a stable key and writes only the target column, only where it is NULL. Re-running a batch yields the same result.
- Records durable progress (last key and batch count) in its own row.
- Is killed and restarted at any point without a different outcome: the worst case repeats a batch.
- Runs while old writers insert. Those rows have a NULL slice and are the set closed in P3(c).
- Uses the same code that new writers will use (the exported `SliceOf`), plus the independent check in 3.8, so migrator and writers cannot disagree.

### 3.4 Window (P3), as a state machine

States are recorded durably, transitions are idempotent, and each step has a stated outcome if the process dies there.

| Step | Action | If interrupted |
| --- | --- | --- |
| (a) Fence | Announce, stop old writers and projection runners, record `FENCED` | Resume at (a); nothing changed visibly |
| (b) Quiesce | Wait until no open or prepared transaction on the journal and no in-flight batch exists; record the barrier (a snapshot taken after quiesce) | Resume at (b); if it cannot quiesce within a bound, abort and unfence |
| (c) Final backfill | Fill every row whose slice is still NULL | Idempotent; resume at (c) |
| (d) Final verification | Section 3.8, exhaustive, on quiesced data. Any mismatch aborts | Resume at (d) or abort |
| (e) Switch | In ONE transaction: set the layout epoch, activate the seeded offsets under the new identity, mark the legacy offset rows superseded, record the barrier | Atomic: either the whole switch happened or none of it |
| (f) Start | Start new binaries; they refuse to start unless the epoch says the new layout is active | Operational |
| (g) Smoke | Read back the first new-layout writes from the persisted rows and check scope, slice and revision | Failure here still allows exact rollback (3.7) |

Abort before (e) leaves the old layout fully intact and live; only the additive columns and the staging area remain and can be dropped.

### 3.5 Offsets

Seed computation, per tenant (scope), projection P and new slice `s`:
- `C(s)` is the set of legacy shards that hold at least one event of slice `s` for that tenant. It is computed from the data in P2 and written to staging.
- `seed(P, s) = min over x in C(s) of offset(P, x)`, where a shard with no offset row counts as 0 (never processed). If `C(s)` is empty there is nothing to read.
- The new slice offset starts at `seed(P, s)`, translated into the new position by the Gate A mechanism.

Claim and argument. Let `E` be an event of slice `s` in legacy shard `x` with timestamp `t`.
- Assumption A1: for the old feed, `E` counts as handled exactly when `t <= offset(P, x)`; this is the read predicate in the code, and the late-commit omissions it can contain are pre-existing.
- Assumption A2: after the barrier in (b), no event can commit with a timestamp at or below any seed. The quiesce step and the empty-transaction check exist to make this true.
- Assumption A3: the translation of a seed into the new position preserves "every event with a timestamp greater than the seed is read".
- If `E` was not handled, then `t > offset(P, x) >= seed(P, s)`, so by A3 the new reader returns it. If `E` was handled, it may be returned again only when `seed < t <= offset(P, x)`.

So, under A1 to A3, no event that the old projection had not handled is skipped. The argument does not claim safety against omissions that already existed, nor if A2 is violated. It is a demonstration under stated assumptions, not yet an executable test: A1 and A3 need tests (in #362/#381 and in the Gate A mechanism), and A2 is verified operationally in (b) and (d).

Cost, which is the part that is not free:
- Events with `seed < t <= offset(P, x)` are presented again. The exact count per projection and slice is computed in P2 and reported before the window, so the cost is known, not guessed.
- In non-cluster deployments `C(s) = {0}`, the seed equals the existing offset and nothing is replayed.
- Where a shard that is far behind or never processed contributes to `C(s)`, the seed is far back and the replay is large. That is the reason for the report.

Which path each projection uses:
- S (seed): the consumer is declared idempotent (#373, #374). Duplicates are acceptable; omissions are not, and the argument above covers omissions.
- R (rebuild): the consumer is not idempotent, and the history is complete (3.6). Use a new projection version with fresh cursors and the pointer switch of the ADR; the old version is kept until verified. No duplicates reach the old destination.
- Blocked: not idempotent and the history is incomplete. The cutover for that projection does not proceed without an explicit, audited acceptance recorded in #359.

### 3.6 Retention

Replay and rebuild only cover events that still exist.
- Before the window, record per scope the first available position and, per persistence id, whether the sequence starts at 1 and whether any row is deleted. A gap means events were removed by retention or erasure.
- A gap rules out path R for that scope and forbids calling any replay a full recovery. Aggregate snapshots restore aggregate state; they cannot rebuild a projection, which is derived from events.
- Path S is unaffected by removed events, because it does not need them: it starts from the existing offsets.
- The guarded deletion contract (`RetainedEventsDeleter`) must see the new offset identity before automatic retention is enabled again after cutover.

### 3.7 Writes, `shard_number` and rollback

- New writers write the slice to the new place. The legacy `shard_number` is required by the current `NOT NULL` constraint; I-09a must decide whether it is relaxed for new rows. Until retirement, pre-cutover rows keep their legacy value and post-cutover rows have none that is meaningful.
- Legacy values cannot be recomputed (section 1), so there is no reverse translation for rows written after the switch.
- Rollback guarantee proposed:
  - Exact, with no data loss, until the first new-layout write. Procedure: stop new binaries, set the epoch back, reactivate the superseded offsets, drop the additions. Nothing the old layout depends on was changed.
  - After the first new-layout write, the rows written since the barrier carry no legacy value, so a return to old binaries would see them under wrong or no shard. Rollback after that point is restore from backup, which loses the writes made since the barrier, or roll forward. This limit is stated explicitly instead of promised away.
  - The barrier recorded in (e) is what defines which rows are post-cutover, so the limit can be measured.
- Retirement (P5) is irreversible and happens only after the `GetShardEvents` retirement plan completes.

### 3.8 Writer blocking and independent verification

Blocking incompatible writers has three layers: the operational fence in (a); the epoch check at startup of new binaries; and a database-level guard that rejects journal and offset writes that do not match the active layout, so an old binary that is accidentally started fails loudly instead of corrupting the feed. The guard is a requirement on #358, not a design here.

Verification does not rely on the migrator's own counters:
- V1: for every scope, the set of `(persistence id, sequence number)` and the row count equal the snapshot taken before P1; with additive columns this is also true by construction and is checked anyway.
- V2: every row's slice equals the function applied to its `(scope, persistence id)`, recomputed by a separate tool built on a different implementation of FNV-1a over the specified key (the reference vectors are the contract).
- V3: every seed is recomputed from raw events and offsets by a separate query and compared with staging; the replay-volume report is reproduced.
- V4: no open or prepared transaction older than the barrier, and no writer connected, at (b) and at (d).
- V5: after (f), the first new writes are read back from persisted rows (scope, slice, revision), for event-sourced and, in its own store, durable state.

## 4. What is missing to replace `Partition` safely

Nothing below is implemented by this PR.

| Missing | Owner |
| --- | --- |
| Additive schema: slice stored apart from the legacy column, layout epoch, writer guard, relaxed legacy constraint | #358 (I-09a) |
| Migrator: backfill, staging, state machine, seed computation, rollback | #359 (this proposal, once approved) |
| Upgrade test from the current schema with data, including interrupted and resumed runs | #359, integration lane |
| Slice-range reader, cursor header and rejection of legacy cursors, offset identity | #351, #362 |
| Position mechanism and the translation of seeds into it | #352/#387 (Gate A) |
| Declaration of idempotent consumers per projection | #373/#374 |
| Opaque Scope keeping the hashed bytes | #349 |
| Export of `SliceOf` and `SliceCount` | after #351 |
| The two actor call sites call the slice computation with `(entity.scope, entity.persistenceID)` after identity binding, gated by the layout epoch so old and new never both write | change that lands with the cutover |
| Unit tests for both write paths receiving the slice from scope and id, independently of actor name, namespace and physical partition, with fakes and no real database or cluster | same change |
| Few end-to-end flows in the `inttest` harness: write, persist, read for both paths; restart and continue; migration with an interrupted run | after the persistence core exists |
| Runbook and preflight checks | #359 |

Decisions needed (owners in parentheses):
1. Writer window offline, with online backfill (recommended) versus a design for online cutover (#359).
2. Seed with the minimum for idempotent consumers and rebuild for the others, as in 3.5 (#359, #373).
3. The rollback guarantee in 3.7, exact until the first new write (#359).
4. The policy for a retention gap: block or audited acceptance (#359).
5. The guard and epoch mechanism, and when the legacy column is dropped (#358, #351).

## 5. What this does not do

It does not change a write path, a schema, `go.mod` or GoAkt. It does not declare the minimum-offset seed safe beyond the stated assumptions, and it does not describe replay as full recovery when events were removed.
