# Logical slices (#350, I-04)

Status: PROPOSAL. Nothing here is a ratified decision. The slice count N, the hash and the key encoding need a maintainer decision (see "Decisions the maintainer must make"). #350 stays open.

Related: #350 (this work), #438 (B2 propagation gap and B4 coverage), #351 (I-05, reader contract and slice range, offset identity), #347 (I-01, ADR on module topology and adapter capabilities), #359 (I-09b, migration), #362 (checkpoint model), #373 (idempotent consumers). PRD: `docs/prd/urd-platform-prd.md`, requirement P-03 (slices stable independently of node count and physical cell; migration preserves logical identities or explicitly invalidates incompatible cursors) and the risk row "choose slice count and mapping explicitly". The PRD lists the slice count among the product decisions still to be recorded in its linked issue or ADR before any guarantee is claimed.

## Place in the ADR

The module-topology ADR (`module-topology-347.md`, merged via #439) registers the pending decisions this work depends on. None of them is decided by this document.

- P1: slice count N (256 or 1024), hash and key encoding. Not ratified. The unexported `sliceOfProvisional` with `provisionalSliceCount = 1024` in #436 is the provisional implementation only, and the 1024 is a placeholder.
- P2: offset migration, cutover and events removed by retention. Owned by #359 (with #351 and #362). The outline below feeds that decision and does not make it.
- P3: the shape of `Scope`, where the TenantID-to-Scope conversion lives, and key validation. Owned by #349.

## What the code does

`persistence/slice.go` contains an unexported, provisional pure function `sliceOfProvisional(scope, entityID)` and constant `provisionalSliceCount = 1024`. It maps the pair (Scope, entity id) to a slice with FNV-1a 64 over an unambiguous key: marker byte, uvarint tenant length, tenant bytes, 0x00, entity id. Unscoped and the invalid zero Scope have their own markers. It takes no cluster, clock or topology input.

- It is not wired to the write path, the actors, `Event.Shard` or `DurableState.Shard`. No schema, no go.mod, no migration code changes.
- It is unexported on purpose. Exporting `SliceOf` and `SliceCount` would put a value in the public API (checked by apidiff) that this decision may still change. Exporting is a one-line follow-up once N is ratified, when a consumer in another package exists (#351 and later). The cost of this choice is that the function has no caller outside tests until then.
- The 1024 in the constant is a placeholder, not an approval. Earlier text in #350 states that N=1024 was ratified by the owner. This is not reflected in #350's acceptance criteria nor in the PRD, which still lists the slice count as pending. That discrepancy needs maintainer confirmation.

## Choosing N: 256 or 1024

The issue allows 256 or 1024. Both are powers of two.

| Aspect | 256 | 1024 |
| --- | --- | --- |
| Checkpoint/offset rows (per #362: one per tenant x projection x slice, for the identity in R-01) | 1x | 4x |
| Cursor and `ShardOffsets`-style maps | up to 256 entries | up to 1024 entries |
| Assignment granularity | about 8 slices per node at 32 nodes; uneven loads when a few slices are hot | about 32 slices per node at 32 nodes; finer rebalancing, smaller move per step |
| Per-slice read polling and stable-prefix bookkeeping (#351, #352) | fewer ranges | more ranges to track |

Consequences of a later change (all are data migrations, not code edits):

- Changing N, the hash, or the key encoding moves entities between slices. Every stored `shard_number` must be recomputed from (scope, persistence id), and every offset keyed by slice becomes meaningless.
- Moving from a larger N to a smaller power of two, with the same hash, is derivable from the stored value (`new = old mod newN`) without rehashing every id, but offsets still have to be merged. Moving from a smaller N to a larger one needs the id of every entity to be rehashed. This favours picking the larger value if refinement is ever likely.
- Changing the hash or key encoding is always a full recompute, whatever N is.

Proposal (not ratified): 1024, on the condition that the workload model (#390, #362) shows that tenants x projections x 1024 offset rows is acceptable. If the measured number of tenants x projections makes the offset table or its write rate a problem, choose 256. The only decisive input is that measurement. Recorded without data, the proposal is a judgement on granularity and on the cheap-coarsening property above, not a measured result.

## Transition to an opaque Scope (#349, ADR T1 and P3)

`sliceHash` currently reads `scope.TenantID()`. ADR finding T1 says #349 makes `Scope` opaque (the persisted key stays `''` for Unscoped and the tenant id otherwise), and P3 leaves the final shape open. Whatever shape #349 chooses, `sliceHash` must produce exactly the same value for the same persisted key:

- Unscoped hashes the Unscoped marker only, with no tenant bytes.
- A tenant hashes the tenant marker, the uvarint length of the same tenant string bytes, those bytes and the separator, as today.
- An invalid zero Scope keeps its own marker.

The existing golden vectors in `persistence/slice_test.go` are the contract that proves this, and they must not change as part of #349. If a vector has to change, that is a hash or key change (P1) and a data migration, not a refactor. This note does not ratify the hash or encoding.

## B2 propagation (#438) versus #350

- The B2 propagation gap, tracked in #438 (the baseline audit #346 is closed), asks whether the slice value really reaches `Event.Shard` and `DurableState.Shard` on the write path. This work does not touch the write path, so B2 is not resolved here.
- #350 asks for a stability criterion: the slice of an entity does not change with the number of nodes. `sliceOfProvisional` meets it by construction since it has no topology input. The test over topologies 1, 3 and 5 checks that contract by construction. It does not execute on a real cluster and does not prove anything about runtime assignment.

## Migration plan outline for #359 / I-09b (OUTLINE, not implemented, not validated)

Nothing in this section exists in code. It lists what #359 has to settle. Its shape depends on #351 (offset identity and cursor format) and #362.

### Preconditions

- N, hash and key encoding ratified (this document's decisions).
- #351 fixes the offset identity and the cursor format, including whether incompatible cursors are rejected (PRD P-03: preserve logical identities or explicitly invalidate cursors).
- A decision on whether the deployment is allowed downtime or must migrate online.
- A backup, and a way to tell which events are still present (retention, see below).

### What is recomputed

`shard_number` of events and durable state is recomputed from (scope, persistence id) with the final function. This is deterministic and can be done by a resumable, idempotent, batched migrator: keyset pagination in a stable order, a durable progress checkpoint, a write that sets the target value only, so a re-run produces the same rows. The old column or table is kept until verification passes.

### Offsets: old per-shard offsets do not map one to one

An old shard holds entities that now spread over many slices, and a slice receives entities from many old shards. Options, none of them proven:

1. Reset and replay. Start slice offsets from the beginning of the AVAILABLE events and rely on idempotent consumers (#373). Safe only if every consumer is idempotent.
2. Minimum offset per slice. Seed each slice with the minimum of the old offsets of the shards that contribute to it. This is an UNPROVEN HYPOTHESIS. It assumes that offset order across old shards is comparable. Historical offsets in the current model are timestamp based (Unix nanoseconds, see `scoped-offsets-retention.md`), and late commits mean an event with an earlier timestamp can become visible after a later one was already processed. A minimum can therefore still skip such an event, or reprocess a large range. It needs a proof or a test against the real commit-order behaviour (#351/#352) before use.
3. Freeze and drain. Stop writers, let every projection catch up, migrate, then resume. This is only safe if the end of the drain is verified, which requires: writers confirmed stopped (no open transactions or prepared transactions that could still commit an older timestamp), and for each projection its offset compared with the real tail of the feed, with no parked or failed events outstanding. If that verification cannot be made, the drain is not proven complete and this option must not be presented as safe.

### Cutover strategy and blocking incompatible writers

Cutover is a single switch after verification, not a gradual one. Writers of the old layout must be unable to write after the switch (for example a schema version check that older binaries refuse, as done by migration 006 in `scoped-offsets-retention.md`, plus an explicit operational step to stop old processes). Mixed old and new writers are not supported. The detail of the guard is a #359 decision.

### Recovery from interruption

The migrator checkpoints progress durably and is idempotent, so an interrupted run resumes or restarts without a different result. Before the switch, rollback means discarding the new data and continuing on the old layout, which was kept. After the switch there is no rollback to the old writers (they are blocked), so the verification below must pass first.

### Verification without omissions

Before the switch, and without trusting the migrator's own counters alone:

- For every scope, the number of events and the set of (persistence id, sequence number) before equals the same after; per-slice counts and a checksum are compared.
- Every row's `shard_number` equals the function applied to its (scope, persistence id), checked by an independent scan.
- For every projection, the new starting position is shown to be at or before the first event not yet handled by the old one, for the chosen offset option. For option 2 this is the part that is not proven.

### Replay versus recovery

Replay from the start covers only the events that still exist. If event retention or deletion has already removed events (see `scoped-offsets-retention.md` and #362), replay from the first AVAILABLE event is not a full recovery: state that depended on removed events cannot be rebuilt from the journal. The migration plan must record, per scope, the first available position, and either rely on snapshots or accept the loss explicitly. This must not be described as full recovery.

## Decisions the maintainer must make

1. Ratify or change N (256 or 1024), with the offset-row measurement from #362/#390, and record it in #350/#351. Confirm or correct the earlier statement in #350 that 1024 was ratified.
2. Ratify the hash (FNV-1a 64) and the key encoding, since changing either is a full migration.
3. Decide when to export the function as public API (`SliceOf`, `SliceCount`), together with #351.
4. Choose the offset migration option (reset and replay, per-slice minimum, freeze and drain) and the online or offline policy, in #359.
5. Decide how the migration handles events already removed by retention.
