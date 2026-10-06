# Tasks — Persistence store contract, canonical form (EGO-STORE-001)

Tracker `#70`, epic `#13`. Ratification/spec work only — no production Go code in this change. `sdd-apply` for this change writes/adjusts `spec.md`, `design.md`, and (T3) at most one narrowly-scoped conformance test if the existing evidence turns out not to cover the store layer directly.

**Apply outcome (2026-09-17, `main`@`3b9e678`)**: pure ratification, as expected — every requirement was already satisfied by WRITE-004 and pre-existing code. No production Go code changed. No new test added (T3.3's condition did not trigger — see 3.2/3.3 below). `spec.md`/`design.md`/`proposal.md` needed no corrections; only this file's checkboxes and evidence trail are new.

## T1 — Formalize canonical persistence contracts

- [x] 1.1 `spec.md` R1: `EventsStore`, `StateStore`, `SnapshotStore` documented as canonical, runtime-neutral, adapter-neutral persistence ports, with file:line citations to the current interfaces. Verified: `persistence/events_store.go`, `persistence/state_store.go`, `persistence/snapshot_store.go` import only `context` and `egopb` (no `goakt`/actor dependency); `EventSourcedActor` depends only on `EventsStore`, `DurableStateActor` only on `StateStore`, no shared supertype.
- [x] 1.2 `spec.md` R2: `WritePrecondition`/`*ConflictError` documented as shared correctness primitives, cited by reference to WRITE-004 (#65) design.md, not restated as new requirements. Verified: `persistence/events_store.go:67` `WriteEvents(ctx, events, precondition WritePrecondition)` and `persistence/state_store.go:65` `WriteState(ctx, state, precondition WritePrecondition)` take the identical type; `persistence/precondition.go` and `persistence/conflict.go` declare the sole `WritePrecondition`/`ConflictError` in the package — no duplicate or store-specific variant found.

## T2 — Formalize existing read/lifecycle capabilities

- [x] 2.1 `spec.md` R3: forward-read contract (`ReplayEvents`/`GetLatestEvent`/`PersistenceIDs`/`GetShardEvents`/`ShardOffsets`) ratified with citations. Verified: `migration/migration.go:110` (`PersistenceIDs`), `:137` (`ReplayEvents`); `projection_runner.go:475` (`ShardOffsets`), `:577` (`GetShardEvents`) — every actual consumer call is one of the five forward-read methods.
- [x] 2.2 `spec.md` R3 scenario: explicit non-goal statement that no backward-read method exists or is required, citing the absence of any consumer found during exploration. Verified: no backward/descending-read call site found across the consumer set re-checked in this apply pass.
- [x] 2.3 `spec.md` R4: lifecycle (`Connect`/`Disconnect`/`Ping`) ratified as already part of both store interfaces — no new capability interface introduced. Verified: `events_store.go:48,50,69`, `state_store.go:48,50,52`, `snapshot_store.go:36,38,40` declare all three directly on the store interface, not on a wrapping type.

## T3 — Conformance evidence for single-mutation atomicity

- [x] 3.1 `spec.md` R5: atomicity boundary stated precisely per design.md D4 (single-call/single-`persistence_id` only; explicitly not multi-event batch atomicity, which is WRITE-006's). Text already correct as ratified; no change needed.
- [x] 3.2 Re-ran fresh on `main`@`3b9e6780b1634471eeb368029423227fc1555bc9` under `-race -count=1` (not cited from memory): `TestEventSourcedIntegrationConcurrentGenesisYieldsExactlyOneCommit` — PASS; `TestDurableStateConcurrentGenesisWritersYieldExactlyOneCommit` — PASS. Both still exercise two independent `Engine` instances sharing one `testkit` store.
- [x] 3.3 Condition did not trigger. While re-running 3.2, found that `testkit/concurrency_test.go` already contains `TestEventStore_T10_ConcurrentGenesisHasExactlyOneWinner` and `TestDurableStore_T10_ConcurrentGenesisHasExactlyOneWinner`, which call `store.WriteEvents`/`store.WriteState` directly on `testkit.NewEventsStore()`/`NewDurableStore()` with no actor involved at all (the file's own comment at line 281 confirms: "no actor of any kind is involved"). Re-ran both under `-race -count=1` on the same HEAD — both PASS. This means direct-against-store evidence for R5 already exists independent of the actor-level tests spec.md cites; the guarantee is not "only through an actor." No new test was added — the existing T10 tests already are what 3.3 would have produced.

## T4 — Document opaque persistence identity / tenant boundary

- [x] 4.1 `spec.md` R6: `persistence_id` documented as an opaque, caller-assigned `string` with no structural tenant awareness, citing `event_sourced_actor.go`/`durable_state_actor.go` call sites that pass it through unchanged. Verified reads: `event_sourced_actor.go:451,543`; `durable_state_actor.go:210`. Verified writes: `durable_state_actor.go:604,643` (`entity.stateStore.WriteState(ctx, durableState, ...)`, `durableState` carrying `entity.persistenceID` unchanged).
- [x] 4.2 `spec.md` R7: `tenant_metadata` documented as transported but never consulted by any `WritePrecondition`/CAS path — cite `tenancy.VerifyUnchanged` as the only (after-the-fact, non-preventive) existing safeguard. Verified by reading `tenancy/context.go`'s `VerifyUnchanged` body: it compares two already-constructed `TenantContext` values at a boundary (e.g. post-saga-context-reset) and returns `ErrDenied` on mismatch — it never gates a store write and only detects drift on an already-established stream, exactly as R7's scenario claims.
- [x] 4.3 `spec.md` R8: explicit statement that resolving this gap (composite key, namespace, or other mechanism) is TENANT-003's design decision, not pre-decided here. Verified: `grep -rn "TenantPersistenceID"` across the full tree (excluding `vendor/`) returns zero hits — nothing was introduced.

## T5 — Validate no scope leakage into WRITE-005/006/TENANT-003

- [x] 5.1 Re-read `spec.md` end-to-end — no requirement redefines `WritePrecondition`/`ConflictError`/CAS ownership; R2 explicitly cites WRITE-004 as sole owner.
- [x] 5.2 No requirement mentions `operation_id`, idempotency, deduplication, or retry identity.
- [x] 5.3 No requirement claims or implies multi-event/batch all-or-nothing atomicity on a real adapter; R5's third scenario explicitly disclaims it.
- [x] 5.4 No requirement invents a tenant-scoped ID scheme, namespace, or prefixing convention; R8 explicitly disclaims it and the codebase grep (4.3) confirms nothing was introduced.
- [x] 5.5 No requirement proposes merging `EventsStore`+`StateStore`; R1 explicitly states they remain separate, D1 in design.md records the rejected alternative.
- [x] 5.6 `spec.md` is 207 lines — within the 300-line hard ceiling and the ≤250 target.
- [x] 5.7 Every requirement traces to existing code or an existing test (see 1.1–4.3 evidence above); no new test was needed (3.3).

## T6 — Verify and reconcile #70

- [x] 6.1 Confirmed issue #70's reconciled body (updated 2026-09-17) still matches this proposal's MUST/MUST NOT — no discrepancy found during T5, no edit needed.
- [x] 6.2 PR description notes this formalizes #70 without closing it — see PR body ("Refs #70"); closure/archival remains a separate, later, explicitly-authorized step.
