# Logical slices (#350, I-04)

Status: PROPOSAL. N, the hash and the key encoding are NOT decided. #350 stays open.

Related: #350 (this work), #351 (I-05, reader contract, slice range, offset identity), #349 (opaque Scope), #347 (ADR), #359 (I-09b, migration), #362 (checkpoint model), #373 (idempotent consumers), #438 (propagation and coverage follow-up), #390 (workload model). PRD: `docs/prd/urd-platform-prd.md`, requirement P-03, and the "Remaining product decisions" paragraph, which lists the slice count as still to be recorded. ADR: `docs/decisions/module-topology-347.md`, pending decisions P1 (N, hash, encoding), P2 (offset migration, cutover, retention) and P3 (Scope shape).

## Three different things

Do not read one as the other.

| Level | What | Where | State |
| --- | --- | --- | --- |
| Provisional function | `sliceOfProvisional(scope, entityID)` with `provisionalSliceCount = 1024`, FNV-1a 64 over a specified key. Unexported. | `persistence/slice.go`, PR #436 | Exists, unit-tested. Not an approval of anything. |
| Approved decision | N, hash, key encoding, offset migration option, retention handling | #350, #351, #359, ADR P1/P2 | None approved. All pending a maintainer decision. |
| Productive integration | Using the slice as `Event.Shard` / `DurableState.Shard`, replacing `ActorSystem().Partition` | `internal/engine/eventsource/event_sourced_actor.go:355`, `internal/engine/durablestate/durable_state_actor.go:169` | Not done. Writes are unchanged. |

#350's objective is to REPLACE `ActorSystem().Partition(id)` with `hash(scope, entity) mod N`. This PR does not do that: both call sites above still call `Partition(entity.persistenceID)`. The 1024 in the constant is a placeholder. An earlier comment on #350 claimed N=1024 was ratified; it was corrected and N is still pending (256 or 1024).

#350's migration criterion asks for a PLAN that feeds #359, not for executing the migration inside #350. The plan below is that input. It is not code.

## Candidate algorithm (what the tests pin)

Key bytes, built from `(scope, entityID)`:

```text
unscoped: 0x00 || id
tenant:   0x01 || uvarint(len(tenant)) || tenant || 0x00 || id
invalid:  0xFF || id            (the zero Scope; callers are expected to reject it earlier)
hash  = FNV-1a 64 (offset 14695981039346656037, prime 1099511628211) over the key
slice = hash mod N
```

`len(tenant)` counts bytes, not runes. `NewTenantID` allows 1 to 128 bytes of valid UTF-8 with no control runes, so a valid Scope yields a one-byte prefix (up to 127 bytes) or a two-byte prefix (128 bytes). A three-byte prefix is not reachable with a valid Scope.

`persistence/slice_test.go` pins this candidate. It does NOT ratify it.

- Slice vectors at N=1024 (the earlier golden vectors, unchanged).
- Full 64-bit hash vectors, each compared with the standard library `hash/fnv` over a key built independently from the layout above (literal markers, `encoding/binary`), covering 127- and 128-byte tenants (both uvarint widths), Unicode tenants and ids, a 128-byte tenant whose rune count differs from its byte length, an id with a NUL byte, and the invalid scope. Tenants follow the real `NewTenantScope` validation.
- Unambiguity: every corpus key decodes to exactly one `(kind, tenant, id)`, including ids that imitate the layout. This is a property of the bytes.
- Collisions are expected: the tests show that distinct keys in distinct scopes can share a slice, and that equal `hash mod 1024` does not imply equal hash.

What these tests do not say: that distinct scopes never share a hash or a slice. They can. Isolation depends on keeping the Scope next to the persistence id wherever the pair is stored or compared, never on the slice.

### Contract for the opaque Scope (#349)

When `Scope` becomes opaque, `sliceHash` must keep consuming the same bytes: for Unscoped the marker alone, for a tenant the marker, uvarint byte length, tenant bytes, separator, then the id. The check is the FULL 64-bit hash against the reference vectors. A result that is identical modulo 1024 proves nothing, because many different hashes share a slice. If a full-hash vector must change, that is a hash or encoding change (P1) and a data migration, not a refactor.

## Proposal for N, hash and encoding (not ratified)

### N

Both candidates are powers of two.

| Aspect | 256 | 1024 |
| --- | --- | --- |
| Checkpoint rows if identity is tenant x projection x slice (#362, R-01) | 1x | 4x |
| Cursor and offset maps | up to 256 entries | up to 1024 entries |
| Assignment granularity | fewer, coarser units per node | more, finer units; smaller move per rebalance step |
| Per-slice polling and stable-prefix bookkeeping (#351, #352) | fewer ranges | more ranges |
| Later change | going to a larger N needs every id rehashed | going to a smaller power of two with the same hash is `new = old mod newN` from the stored value, but offsets still need merging |

Proposal: 1024, conditional. Rationale: finer assignment units and the cheaper later coarsening. Cost: 4x checkpoint rows and 4x range bookkeeping versus 256.

Limits of this proposal: it is a judgement, not a measurement. No tenants x projections count, offset write rate, polling cost or per-node slice load has been measured. If tenants x projections x 1024 makes the offset table or its write rate a problem, 256 is the answer.

Missing data to decide (owners: #390, #362, #351/#352):
- Expected tenants and projections per tenant (offset row count at 256 and at 1024).
- Checkpoint write rate and its cost on the destination backend.
- Per-slice read cost under the chosen stable-prefix mechanism.
- Node counts to support, and how many slices per node are acceptable.
- Whether any case needs more than 1024 slices, since refining later means rehashing every id.

### Hash

Candidate FNV-1a 64: in the standard library (no new module, no go.mod change), specified, unseeded, stable across processes and architectures, trivial to reimplement in another adapter language.

Limits: not cryptographic and not collision resistant against an adversary. A caller who controls entity ids can aim many ids at one slice and create a hot slice. Whether that matters depends on who assigns persistence ids, which is a threat-model input not yet recorded. Alternatives (for example a seeded or stronger hash) would add a dependency or a persisted secret, and need go.mod and compatibility decisions that are gated. A hash change is a full data migration whatever N is.

### Encoding

Candidate as above. Benefits: unambiguous bytes, Unscoped kept apart from a tenant named `unscoped`. Limits: the layout depends on the tenant being compared by its persisted bytes; it must stay byte-identical under #349. Any change is a full migration.

Missing data: whether other adapters or languages need a different canonical form, and how `Scope` and key validation (P3) interact with it.

## B2 propagation (#438) versus #350

#438 covers whether a non-nil partition reaches `Event.Shard` and `DurableState.Shard` and is verified from the persisted row or envelope, and which identity feeds `Partition` after #430. #350's slice stability test does not replace that. This PR does not touch the write path, so it resolves neither. Both call sites pass only `entity.persistenceID` to `Partition` today, so the scope is not an input there; whether it should become one is part of what the follow-up change must settle.

The 1, 3 and 5 node test is satisfied by construction: the function has no topology input. That is all it shows. It does not run on a real cluster and says nothing about runtime assignment.

## Closure matrix for #350

Nothing here adds a closing requirement beyond #350's own criteria and objective.

| #350 criterion or objective | Evidence today | Pending |
| --- | --- | --- |
| Objective: replace `ActorSystem().Partition` with `hash(scope, entity) mod N` as a pure function separate from physical cell assignment | The pure function exists, unexported, with vectors and unambiguity tests. | The replacement in event-sourced and durable state is not done (see next section). Depends on the decisions below. |
| N decided and documented | This document: comparison, proposal, missing data. | A maintainer decision (256 or 1024) recorded in #350. |
| Hash and key encoding (P1) | Candidate pinned by full 64-bit vectors against an independent FNV-1a. | A maintainer decision recorded in #350. |
| Slice does not change with 1, 3 and 5 nodes | Unit test, by construction (no topology input). | Nothing required by the criterion as worded. Real-cluster evidence belongs to the end-to-end flow listed below. |
| Plan for `shard_number` and existing offsets, feeding I-09b | The plan section below, with resolved items, hypotheses and pending items kept apart. | Its hypotheses and pending items are resolved in #359 with #351/#362. The plan feeds #359; #350 does not execute it. |

Not part of closing #350: #438 (coverage follow-up, kept open on its own track), executing the migration (#359), the reader contract (#351).

## Change after the decisions

Once P1 is recorded (and in step with #351 and #359), a separate change does the following. It is not done here, and writes must not change before its gates.

1. Export `SliceOf(scope, entityID)` and `SliceCount` from `persistence`, with the ratified values and the full-hash vectors as their contract. Exporting is public API (checked by apidiff), which is why it waits for P1 and for a real consumer (#351 and later).
2. Replace `ctx.ActorSystem().Partition(entity.persistenceID)` in the event-sourced actor and in the durable state actor with `SliceOf`. This needs the entity's scope at those call sites, which depends on #349/#430.
3. Coordinate with #359. Switching the write path changes the meaning of `shard_number` for new rows while old rows keep the GoAkt value. Flipping writes before the migration and cutover plan exists would leave a mixed layout in one column. The write change and the migration cutover are one coordinated step, not two independent ones.
4. Verify by reading persisted records, not by observing the publisher or the actor: write, read the stored `Event` / `DurableState`, and check that `Shard` equals `SliceOf(scope, persistenceID)`, with the scope and the revision preserved. #438 stays as the coverage follow-up and is not closed by this change.

Dependency recorded: this change waits on P1 (maintainer decision), #351 (slice range and offset identity, listed as #350's dependency), #349/#430 (scope and identity at the call site) and #359 (cutover). The gates for schema, go.mod and integration still apply.

## Plan input for #359 / I-09b

Not implemented, not validated. No schema or migration is added before its gates.

### Resolved (established by existing code, ADR or PRD)

- A slice is a pure function of `(scope, persistence id)` once P1 is fixed, so `shard_number` can be recomputed deterministically from the stored pair. This is conditional on P1.
- PRD P-03: migration preserves logical identities or explicitly invalidates incompatible cursors.
- ADR: a new checkpoint identity is introduced additively; `Offset` and `ProjectionId` are not edited in place. Version cutover pauses the old processor and validates the new offsets against its per-slice barrier in the pointer-switch transaction.
- Today's offsets are timestamp based (see `scoped-offsets-retention.md`), and `persistence/events_store.go` acknowledges that a late commit can become visible after a later timestamp was already read.
- The old `shard_number` came from GoAkt `Partition`, which depends on the partition count configured at write time.

### Hypotheses (unproven; do not rely on them)

- H1. A minimum of the old offsets of the shards contributing to a slice is a safe starting offset for that slice. Unproven: it assumes offsets are comparable across old shards, and late commits can make it skip an event (or reprocess a large range). It must not be presented as safe, and there is no safe lower bound derivable from historical timestamps alone.
- H2. Freeze and drain (stop writers, let projections catch up, migrate, resume) works. It holds only if the end of the drain can be verified: writers confirmed stopped, no open or prepared transaction that could still commit an older timestamp, and each projection's position compared with the real tail of the feed with nothing parked or failed. Without that check the drain is not proven complete.
- H3. A single, non-gradual cutover is acceptable. The alternative (gradual or dual-read) has not been compared.

### Pending (must be defined and decided in #359, with #351 and #362)

Reset or replay needs more than idempotent consumers (#373). Each of these must be defined:

- Initial state: what each projection destination contains when replay starts (emptied, versioned side by side, or left as is).
- Events available: the first available position per scope, given retention (`scoped-offsets-retention.md`, #362). Replay covers only events that still exist and is not full recovery when events were removed. Aggregate snapshots restore aggregate state; they do not let any projection be rebuilt, because a projection is derived from events, so they are not a substitute for removed events.
- Order: which order the replay uses (per persistence id sequence, and what is promised across entities), and how it relates to the stable-prefix mechanism of #351/#352.
- Fencing: old processors and old writers must be unable to act during replay, with ownership validated at the destination.
- Checkpoints: the new per-slice identity (R-01), where the starting position is recorded, and that it is committed with the effect where the destination supports it.
- External effects: replay re-presents events, so effects with outside consequences (publishers, integrations, workflow commands) need suppression or de-duplication; an idempotent consumer only covers idempotent effects.

Keeping the old layout and allowing rollback:

- Overwriting `shard_number` in place destroys the old value, so it is not a rollback plan. The old per-row value and the old offset rows must be preserved by an explicit mechanism, to be chosen in #359: for example an additive column or a shadow copy kept until verification, or backup and restore as the only rollback.
- Rows written after the switch carry only the new slice. The old GoAkt value for them is not reproducible without the original partition count and an actor system, so after the first new-layout write a rollback cannot be assumed unless the new writers also record the old value, which has to be decided. State explicitly which rows a rollback covers and from when it stops being possible.

Preconditions, cutover, writers, interruption, verification (shape to decide, not decided):

- Preconditions: P1 and P2 recorded; #351 fixes offset identity and cursor format and whether incompatible cursors are rejected; online or offline policy decided; a backup exists; the available-event position per scope is recorded; and the deployment is confirmed to have the state the plan assumes (partition count, no prepared transactions).
- Cutover: the exact switch and what makes it atomic, relating to the pointer-switch transaction in the ADR. The switch is gated on verification having passed.
- Blocking incompatible writers: old-layout writers must be unable to write after the switch, for example by a schema version check older binaries refuse (migration 006 in `scoped-offsets-retention.md` is a precedent), plus an explicit operational step to stop old processes. Mixed writers are not supported. The mechanism is a #359 decision.
- Interruption: a resumable, idempotent, batched migrator with durable progress, keyset pagination in a stable order, writing only the target value. Where an interrupted run leaves the system, and what is safe to retry, must be stated per phase.
- Independent verification, not relying on the migrator's own counters: per scope, the set of `(persistence id, sequence number)` before equals the set after, with per-slice counts and a checksum; every row's slice equals the function applied to its `(scope, persistence id)` by a separate scan; and for every projection the new starting position is shown to be at or before the first event not yet handled by the old one. For H1 the last point is the part that is not proven.

## End-to-end evidence still pending (not part of this PR)

#436 adds unit tests only: the pure function and its encoding, with no database, network or cluster. Reading real persisted records is an end-to-end flow for when the persistence core is implemented. It will live in the explicit integration lane (the `inttest` module), reuse the existing harness, and keep to a few flows, each proving a different guarantee, none duplicating unit or conformance coverage:

1. Write, persist, read: the stored record's scope, slice and revision match `SliceOf(scope, persistenceID)` and the expected revision (this is the #438 evidence).
2. Restart and recovery, then continue: after a restart the same entity maps to the same slice, keeps its scope, and continues from the right revision. Topologies of 1, 3 and 5 nodes belong here, where real nodes exist.
3. Migration, once #359 is implemented: rows from the old layout end up with the verified slice and no omission.

This evidence is recorded as pending and does not block #436.

## Decisions the maintainer must make

1. N (256 or 1024), with the offset-row measurement (#362/#390); record it in #350/#351.
2. The hash (FNV-1a 64 or another) and the key encoding; changing either is a full migration.
3. When to export `SliceOf` and `SliceCount` (with #351).
4. The offset migration option and the online or offline policy (#359).
5. How the migration treats events already removed by retention (#359).
6. The rollback guarantee and the preserved old layout (#359).
