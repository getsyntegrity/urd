# Logical slices (#350, I-04)

Status: P1 RATIFIED by the maintainer (recorded in #350 and #351): N = 1024, FNV-1a 64 and the key encoding below. The calculation is fixed. The ratification does NOT approve the migration strategy (P2, #359) and does NOT enable the new write calculation: `ActorSystem().Partition` still feeds `Event.Shard` and `DurableState.Shard`. #350 stays open until the objective (replacing `Partition`) is met. The cutover in `slice-cutover-359.md` is a PROPOSAL, not an approved decision.

Related: #350 (this work), #351 (I-05, reader contract, slice range, offset identity), #349 (opaque Scope), #347 (ADR), #359 (I-09b, migration), #362 (checkpoint model), #373 (idempotent consumers), #438 (propagation and coverage follow-up), #355 (workload model, limits and SLO of a cell). #390 covers resource selection (pools per Scope, cell and role), not the workload model. PRD: `docs/prd/urd-platform-prd.md`, requirement P-03, and the "Remaining product decisions" paragraph, which lists the slice count as still to be recorded. ADR: `docs/decisions/module-topology-347.md`, decisions P1 (N, hash, encoding: ratified), P2 (offset migration, cutover, retention: pending) and P3 (Scope shape: pending).

## Three different things

Do not read one as the other.

| Level | What | Where | State |
| --- | --- | --- | --- |
| Implemented function | `sliceOf(scope, entityID)` with `sliceCount = 1024`, FNV-1a 64 over the specified key. Unexported. | `persistence/slice.go`, PR #436 | Exists, unit-tested, the ratified calculation. Not wired to anything. |
| Approved decision | P1: N = 1024, FNV-1a 64, the key encoding | #350, #351, ADR P1 | Ratified. P2 (offset migration, cutover, retention) and P3 (Scope shape) are NOT approved. |
| Productive integration | Using the slice as `Event.Shard` / `DurableState.Shard`, replacing `ActorSystem().Partition` | `internal/engine/eventsource/event_sourced_actor.go:355`, `internal/engine/durablestate/durable_state_actor.go:169` | Not done. Writes are unchanged. Blocked by the #359 cutover gate (see "Replacement contract"). |

#350's objective is to REPLACE `ActorSystem().Partition(id)` with `hash(scope, entity) mod N`. This PR does not do that: both call sites above still call `Partition(entity.persistenceID)`. An earlier comment on #350 wrongly said N=1024 was ratified before the maintainer had decided; it was corrected. The decision was taken afterwards and is recorded in #350 and #351.

#350's migration criterion asks for a PLAN that feeds #359, not for executing the migration inside #350. The plan below is that input. It is not code.

## Ratified algorithm (what the tests pin)

Key bytes, built from `(scope, entityID)`:

```text
unscoped: 0x00 || id
tenant:   0x01 || uvarint(len(tenant)) || tenant || 0x00 || id
invalid:  0xFF || id            (the zero Scope; callers are expected to reject it earlier)
hash  = FNV-1a 64 (offset 14695981039346656037, prime 1099511628211) over the key
slice = hash mod N
```

`len(tenant)` counts bytes, not runes. `NewTenantID` allows 1 to 128 bytes of valid UTF-8 with no control runes, so a valid Scope yields a one-byte prefix (up to 127 bytes) or a two-byte prefix (128 bytes). A three-byte prefix is not reachable with a valid Scope.

`persistence/slice_test.go` pins this algorithm. Changing any vector is a hash or encoding change, that is, a data migration (see the cutover proposal).

- Slice vectors at N=1024 (the earlier golden vectors, unchanged).
- Full 64-bit hash vectors, each compared with the standard library `hash/fnv` over a key built independently from the layout above (literal markers, `encoding/binary`), covering 127- and 128-byte tenants (both uvarint widths), Unicode tenants and ids, a 128-byte tenant whose rune count differs from its byte length, an id with a NUL byte, and the invalid scope. Tenants follow the real `NewTenantScope` validation.
- Unambiguity: every corpus key decodes to exactly one `(kind, tenant, id)`, including ids that imitate the layout. This is a property of the bytes.
- Collisions are expected: the tests show that distinct keys in distinct scopes can share a slice, and that equal `hash mod 1024` does not imply equal hash.

What these tests do not say: that distinct scopes never share a hash or a slice. They can. Isolation depends on keeping the Scope next to the persistence id wherever the pair is stored or compared, never on the slice.

The 1, 3 and 5 node test proves that the FUNCTION is independent of topology by construction (it has no topology input). It does not run on a real cluster and says nothing about runtime assignment.

### Contract for the opaque Scope (#349)

When `Scope` becomes opaque, `sliceHash` must keep consuming the same bytes: for Unscoped the marker alone, for a tenant the marker, uvarint byte length, tenant bytes, separator, then the id. The check is the FULL 64-bit hash against the reference vectors. A result that is identical modulo 1024 proves nothing, because many different hashes share a slice. If a full-hash vector must change, that is a hash or encoding change (P1) and a data migration, not a refactor.

## Evidence behind the decision: measured, calculated, hypothesized

#355 and #362 contain no figures yet: #355's criteria are unchecked and say initial values are hypotheses, and the PRD states that no throughput or latency target is demonstrated. So there is no workload scenario to test N against. What exists is below, each item labeled by what it is.

### Measured (local, synthetic, not a production result)

Environment: go1.27.0, darwin/arm64, GOMAXPROCS=16, a developer laptop. Scripts were run outside the repository and are not part of this PR.

- Hash cost per key (microbenchmark): FNV-1a 64: 4.9 ns on a 14-byte key, 113.7 ns on a 168-byte key (a 128-byte tenant and a 36-byte id). SHA-256 with the first 8 bytes taken: 42.3 ns and 59.4 ns. FNV-1a is a byte-serial loop, so it loses on long keys here; SHA-256 profits from hardware support on this CPU. The slice is computed once per actor start, not per event, so both costs are negligible against a database write. Other CPUs may rank them differently.
- Distribution over 1,000,000 synthetic ids under one tenant (chi-square, expected about N-1 with standard deviation sqrt(2(N-1))): with ids `entity-<n>` and decimal ids, FNV-1a is more even than random (N=256: 53.5 and 34.8 against about 255 +/- 23), which means sequential ids are spread regularly rather than randomly; with UUID-like ids it is random-like (249.9 and 1003.0). SHA-256 is random-like in all cases. No bucket skew was found for either hash. Real ids may differ from these three synthetic patterns.

### Calculated (arithmetic on stated, hypothetical inputs)

Checkpoint rows follow the #362 identity (mode, scope or cell, processor, version, slice). Upper bound if every slice has a row: `rows = U x P x V x N`, where U is the number of tenants (PerScope) or cells (SharedCell), P the processors, V the live versions (at least 1; more while a rebuild keeps the old version), N the slice count. With P=5 and V=1, and U values chosen only as examples:

| U (tenants or cells) | N=256 | N=1024 |
| --- | --- | --- |
| 10 | 12,800 | 51,200 |
| 100 | 128,000 | 512,000 |
| 1,000 | 1,280,000 | 5,120,000 |
| 10,000 | 12,800,000 | 51,200,000 |

The ratio is always 4. It is smaller when rows are created only for slices that have events. For E independent uniform ids in a scope, the expected number of distinct slices is `N (1 - (1 - 1/N)^E)`:

| E (entities in the scope) | N=256 | N=1024 | ratio |
| --- | --- | --- | --- |
| 10 | 9.8 | 10.0 | 1.01 |
| 100 | 82.9 | 95.3 | 1.15 |
| 1,000 | 250.9 | 638.5 | 2.55 |
| 10,000 | 256.0 | 1023.9 | 4.00 |

So N=1024 costs about 4x rows only for scopes with roughly N or more entities; a scope with 100 entities costs about 15% more. The same formula bounds the checkpoint writes of one batch of E events: distinct slices touched is the number above, if a checkpoint is written per slice touched. SharedCell mode removes the tenant factor from U. At 32 nodes, assignment is 8 slices per node at 256 and 32 at 1024. For 1,000,000 uniform events, the expected load per slice has a relative standard deviation of 1.6% at 256 and 3.2% at 1024 (`1/sqrt(mean)`).

### Hypotheses (unproven)

- Checkpoint granularity can be coarser than the slice (adapters group slices into partitions, as #351 lists among its portability rules and #352 mentions 64 counters as a fallback). If so, the row cost of a large N can be bounded without changing N. This depends on #351/#362 and is not decided.
- A grouping into P partitions with `P` dividing N is consistent with `hash mod N`: `(hash mod N) mod P = hash mod P`. True for powers of two by arithmetic; its use is a #351 decision.
- Typical tenants have fewer entities than N, which would make rows nearly independent of N. Unknown until #355 states entity counts per tenant.
- A hot-slice effect from skewed entities (#355 mentions hot tenants and entities) could make finer slices matter more. Not measured.

### Missing data (owners: #355, #362, #351/#352)

- Tenants, projections per tenant, entities per tenant, versions kept during rebuild.
- Checkpoint row size and write cost on the destination backend, and whether a checkpoint is per slice per batch.
- Per-slice read cost under the stable-prefix mechanism chosen in Gate A (#352/#387).
- Node counts to support and the acceptable slices per node.
- Who assigns persistence ids (threat model for concentration).

## Decision (P1, ratified)

The maintainer ratified P1 after the evidence above, and the decision is recorded in #350 and #351:

- N = 1024. Not because the numbers favor it: the evidence is neutral to slightly against it on cost (up to 4x rows where scopes are large). The deciding asymmetry is reversibility: going from 1024 to 256 (or any power-of-two divisor) is derivable from the stored `shard_number` (`new = old mod newN`), while going from 256 to 1024 requires every persistence id to be rehashed. If row cost proves too high, the grouping hypothesis above bounds it without a migration. Condition kept open: if #351/#362 require one checkpoint row per slice and the measured tenants x projections exceeds the table budget, revisit N before the first write with the new calculation.
- Hash = FNV-1a 64. Both candidates are standard library, unseeded and stable. The measured costs are negligible for either and the measured distribution shows no defect for FNV-1a. SHA-256 (`crypto/sha256`, no secret needed) was a valid alternative.
- Concentration of ids is not solved by any unkeyed hash, FNV or SHA-256: with N slices, an adversary who picks ids finds one that lands in a chosen slice in about N attempts. Only a keyed hash prevents it, and that needs a persisted secret shared by every writer and adapter. Because the key includes the tenant, concentration by one tenant's ids affects that tenant's own distribution; the cross-tenant effect exists only for slices shared between tenants (the feed and projection of a shared cell). Whether this matters depends on who assigns ids; it stays a threat-model input, not decided.
- Encoding = the layout above. It is unambiguous, keeps Unscoped apart from a tenant named `unscoped`, and matches the persisted key unchanged under #349 (`''` for Unscoped, the tenant id otherwise).

Scope of the ratification: it fixes the calculation. It does not approve the migration strategy (P2) and does not enable the new write calculation. Changing N, the hash or the encoding after the first write with the new calculation is a full #359-style migration.

## Replacement contract: what integrating the slice needs

What the code does today (verified in this revision):

- Both actors compute `shardNumber = ActorSystem().Partition(entity.persistenceID)`. Event-sourced does it in the `PostStart` branch of `Receive`; durable state does it in `PreStart`, deliberately, to avoid a race with `PostStop` (see its comment).
- `entity.persistenceID` is first set to `ctx.ActorName()` and then replaced by `behavior.ID()` in `bindIdentity`, before `Partition` runs in both actors. In a multi-tenant engine the actor name is qualified with the tenant, so it is not the entity id; the persisted records keep the id the behavior declares. If no behavior is found the id stays the actor name (legacy and single-tenant engines), and durable state then fails startup ("behavior is required").
- `entity.scope` is already resolved before `Partition` runs in both actors (`resolveScope` in `PreStart`).

Input contract for the replacement: the slice must be computed from `(entity.scope, entity.persistenceID)` taken after `bindIdentity`, which is exactly the pair the stores persist. It must NOT use the actor name, the actor namespace or the GoAkt partition, so the result is the same across namespaces and across the transition to an opaque Scope (#349), where the persisted key stays unchanged. The computation point must keep the existing happens-before properties (durable state in `PreStart`).

Correction: an earlier revision of this document said the scope was missing at the call sites and depended on #349/#430. The scope is already there. What #430/#438 still need to fix is which identity feeds `Partition` TODAY, as a test, and that is not a prerequisite of the replacement.

What blocks the replacement, exactly:

1. Exporting. The function is ratified but unexported on purpose: exporting `SliceOf`/`SliceCount` is public API (checked by apidiff) and waits for a real consumer and for #351's slice-range contract. This is a sequencing choice, not a missing decision on the calculation.
2. #359's cutover. Writing the new slice into `shard_number` while existing rows hold the GoAkt value leaves one column with two meanings, and projection offsets keyed by the old shard become wrong. The writes must be enabled by the same gated step that migrates data and offsets, not before. Enabling them earlier would break compatibility with existing data and offsets, which is the condition this work must not violate. #350's validation text (no schema changes before gates) also applies to any layout signal the cutover needs.
3. #351. The reader that consumes slice ranges and the offset identity are specified there; #350 lists it as its dependency. The writer replacement itself does not need the reader, but the exported API and the cursor format must agree with it before they are public.

What is ready: the pure function, its vectors and its tests; the input contract above; the plan below.

Shape of the change once unblocked (not done here): export the function; replace the two `Partition` calls with it using the input above; gate the write on the layout version recorded by the #359 migration so an old binary and a new binary can never both write; verify by reading persisted records, not by observing the publisher or the actor. This change needs a schema or configuration signal for the layout, which is a #359 decision and is not introduced here.

## B2 propagation (#438) versus #350

#438 covers whether a non-nil partition reaches `Event.Shard` and `DurableState.Shard` and is verified from the persisted row or envelope, and which identity feeds `Partition` after #430. #350's slice stability test does not replace that. This PR does not touch the write path, so it resolves neither.

## Closure matrix for #350

Nothing here adds a closing requirement beyond #350's own criteria and objective.

| #350 criterion or objective | Status | Evidence today | Pending |
| --- | --- | --- | --- |
| Objective: replace `ActorSystem().Partition` with `hash(scope, entity) mod N`, a pure function separate from physical cell assignment | Partly met | The pure function exists, unexported, with vectors and unambiguity tests; the replacement contract is specified. | The replacement in event-sourced and durable state is not done; blocked by the #359 cutover gate. |
| N decided (256 or 1024) and documented | Met | Maintainer decision N = 1024 recorded in #350 and #351; this document holds the comparison and the measured/calculated/hypothesized evidence. | Nothing for the criterion. |
| Hash and key encoding (P1) | Decided | FNV-1a 64 and the layout above ratified and recorded in #350 and #351; pinned by full 64-bit vectors against an independent FNV-1a. | Nothing for the calculation. |
| Slice does not change with 1, 3 and 5 nodes | Met for the function, by construction | Unit test; the function has no topology input. | Nothing required by the criterion as worded; real-cluster evidence is the end-to-end flow listed below. |
| Plan for `shard_number` and existing offsets, feeding I-09b | Met as a plan | The plan section below: resolved items, hypotheses and pending items kept apart, covering concurrent writes, interruption, verification and rollback. | Its hypotheses and pending items are resolved and executed in #359 with #351/#362. The plan feeds #359; #350 does not execute it. |

#350 is not complete: the objective is open on the replacement of `Partition`, which waits for the #359 cutover.

Not part of closing #350: #438 (coverage follow-up, kept open on its own track), executing the migration (#359), the reader contract (#351).

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

Concurrent writes during the migration:

- Rows written while the migrator runs carry whatever layout the writing binary uses. With writers running, a scan can miss a row inserted behind its cursor, and a row can be rewritten by the migrator and then by a writer.
- Offline option: writers are stopped and confirmed stopped (H2's conditions) before the first batch. Simple to verify, with downtime.
- Online option: needs a mechanism that makes every concurrent insert land in the new layout or be caught by a final pass under a writer block, with a captured high-water mark per scope. Not designed and not proven here; it must not be assumed.
- Either way the final verification runs after the last write is blocked, and an old-layout writer must be impossible after the switch (see below).

Keeping the old layout and allowing rollback:

- Overwriting `shard_number` in place destroys the old value, so it is not a rollback plan. The old per-row value and the old offset rows must be preserved by an explicit mechanism, to be chosen in #359: for example an additive column or a shadow copy kept until verification, or backup and restore as the only rollback.
- Rows written after the switch carry only the new slice. The old GoAkt value for them is not reproducible without the original partition count and an actor system, so after the first real new-layout write a rollback cannot be assumed unless the new writers also record the old value, which has to be decided. The single definition of the rollback limit, including the synthetic smoke write that does not end it, is section 3.1 of `slice-cutover-359.md`; this document does not restate it.

Preconditions, cutover, writers, interruption, verification (shape to decide, not decided):

- Preconditions: P1 and P2 recorded; #351 fixes offset identity and cursor format and whether incompatible cursors are rejected; online or offline policy decided; a backup exists; the available-event position per scope is recorded; and the deployment is confirmed to have the state the plan assumes (partition count, no prepared transactions).
- Cutover: the exact switch and what makes it atomic, relating to the pointer-switch transaction in the ADR. The switch is gated on verification having passed.
- Blocking incompatible writers: old-layout writers must be unable to write after the switch, for example by a schema version check older binaries refuse (migration 006 in `scoped-offsets-retention.md` is a precedent), plus an explicit operational step to stop old processes. Mixed writers are not supported. The mechanism is a #359 decision.
- Interruption and resumption: a resumable, idempotent, batched migrator with durable progress, keyset pagination in a stable order, writing only the target value. Where an interrupted run leaves the system, and what is safe to retry, must be stated per phase. A restart must not repeat a batch with a different result, and a crash between the data step and the offset step must leave a state the verifier can classify.
- Independent verification, not relying on the migrator's own counters: per scope, the set of `(persistence id, sequence number)` before equals the set after, with per-slice counts and a checksum; every row's slice equals the function applied to its `(scope, persistence id)` by a separate scan; and for every projection the new starting position is shown to be at or before the first event not yet handled by the old one. For H1 the last point is the part that is not proven.

### What this plan covers for #350 and what #359 owns

#350's criterion asks for a plan for `shard_number` and existing offsets that feeds I-09b. This section is that plan: it covers `shard_number`, offsets, concurrent writes, interruption and resumption, independent verification and rollback, and keeps what is proven apart from what is hypothesis. #359 owns choosing the offset option, the online or offline policy, the retention treatment, the rollback mechanism and the layout signal; implementing the idempotent migration in `persistence/postgres/schema`; the upgrade test from the current schema with data; and the cutover.

## End-to-end evidence still pending (not part of this PR)

#436 adds unit tests only: the pure function and its encoding, with no database, network or cluster. Reading real persisted records is an end-to-end flow for when the persistence core is implemented. It will live in the explicit integration lane (the `inttest` module), reuse its existing harness, and keep to a few flows, each proving a different guarantee, none duplicating unit or conformance coverage:

1. Write, persist, read: the stored record's scope, slice and revision match `SliceOf(scope, persistenceID)` and the expected revision, for both event-sourced and durable state, independent of the actor name and namespace (this is the #438 evidence).
2. Restart and recovery, then continue: after a restart the same entity maps to the same slice, keeps its scope, and continues from the right revision. Topologies of 1, 3 and 5 nodes belong here, where real nodes exist.
3. Migration, once #359 is implemented: rows from the old layout end up with the verified slice and no omission, with an interrupted and resumed run.

This evidence is recorded as pending and does not block #436.

## Decisions still open

P1 is decided. What remains is for #359 and #351, with the proposal in `slice-cutover-359.md`:

1. When to export `SliceOf` and `SliceCount` (with #351).
2. The offset migration option and the online or offline policy (#359).
3. How the migration treats events already removed by retention (#359).
4. The rollback guarantee, the preserved old layout and the layout signal that blocks incompatible writers (#359).
