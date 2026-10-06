# Proposal — Persistence store contract, canonical form (EGO-STORE-001)

| Field | Value |
|---|---|
| Change | `ego-store-001` |
| Date | 2026-09-17 |
| Phase | `sdd-propose` — proposed |
| Tracker | [`#70`](https://github.com/getsyntegrity/ego/issues/70), epic [`#13`](https://github.com/getsyntegrity/ego/issues/13) |
| Depends on | [`#65`](https://github.com/getsyntegrity/ego/issues/65) WRITE-004 (**completed**, `main`@`9435db9`) — ratified authority for `WritePrecondition`/`ConflictError`/CAS |
| Blocks | TENANT-003 (EventStore isolation), not yet filed — cited as blocked by #23 |
| Does not block | [`#66`](https://github.com/getsyntegrity/ego/issues/66) WRITE-005, [`#67`](https://github.com/getsyntegrity/ego/issues/67) WRITE-006 — both already have everything they need from WRITE-004 |

## Why now

A read-only architectural exploration (2026-09-17, post-WRITE-004) found #70's original text stale: it asked to "define" an EventStore SPI, runtime-neutral append/read/lifecycle, and health/lifecycle as open design questions — all of which WRITE-004 and prior work already shipped as code:

- `persistence.EventsStore` / `persistence.StateStore` / `persistence.SnapshotStore` — already runtime-neutral, adapter-neutral interfaces, zero coupling to goakt or any concrete adapter.
- `persistence.WritePrecondition` (`Unconditional()`/`ExpectGenesis()`/`ExpectRevision(N)`) and `*persistence.ConflictError` — already the shared concurrency contract for both `EventsStore.WriteEvents` and `StateStore.WriteState`.
- Lifecycle (`Connect`/`Disconnect`/`Ping`) — already part of both store interfaces.
- Forward reads (`ReplayEvents`/`GetLatestEvent`/`PersistenceIDs`/`GetShardEvents`/`ShardOffsets`) — already implemented, pre-dating WRITE-004.
- Tenant metadata (`tenant_metadata` on `Event`/`Snapshot`/`DurableState`) — already shipped by EGO-TENANT-002, transported but not enforced by the store.

Issue #70 was reconciled on GitHub (2026-09-17) to reflect this: it is no longer "design an EventStore from scratch," it is "formalize into OpenSpec artifacts, and freeze the boundary TENANT-003 needs, what the code already does." This proposal is that formalization.

## Intent

Ratify `EventsStore` and `StateStore` (and `SnapshotStore` where it shares the same primitives) as canonical persistence contracts: cite WRITE-004's concurrency primitives by reference, formalize the atomicity guarantee already stated only in doc comments (with conformance evidence against the existing reference implementation), and freeze — without resolving — the exact boundary that blocks TENANT-003: `persistence_id` is caller-provided and opaque, and `tenant_metadata` is transported but not structurally enforced.

This is contract/spec work. No production Go code changes.

## In scope (MUST — see design.md and spec.md for detail)

- Canonical recognition of `EventsStore`/`StateStore`/`SnapshotStore`.
- Reference (not redefinition) of `WritePrecondition`/`ConflictError` as the shared concurrency contract.
- Ratification of the existing forward-read contract; explicit non-goal: backward reads.
- Ratification of existing lifecycle (`Connect`/`Disconnect`/`Ping`).
- Formalization of single-`persistence_id`/single-call atomicity, backed by existing conformance evidence (`TestEventSourcedIntegrationConcurrentGenesisYieldsExactlyOneCommit`, `TestDurableStateConcurrentGenesisWritersYieldExactlyOneCommit` — both already prove the store's own CAS, not caller/mailbox ordering, decides the winner, under `-race`, across two independent `Engine` instances).
- Explicit documentation that `persistence_id` is opaque/caller-assigned and `tenant_metadata` does not constitute isolation — the exact input TENANT-003 needs.

## Out of scope (MUST NOT)

- Redefining `WritePrecondition`, `ConflictError`, or any optimistic-concurrency semantics — WRITE-004 (#65) is ratified authority.
- Backward reads — no consumer, no demonstrated requirement.
- `operation_id` persistence, idempotency, deduplication, retry identity — WRITE-005 (#66).
- Atomic multi-event append / partial-failure semantics on a real (non-testkit) adapter — WRITE-006 (#67).
- Tenant isolation / structural scoping of `persistence_id` — TENANT-003 (not yet filed); this proposal documents the gap, it does not close it.
- Concrete adapters (Postgres/in-memory/Stoolap) — STORE-007/008/009.
- A conformance test suite as a deliverable — STORE-006.
- Merging `EventsStore` and `StateStore` into one interface — evaluated in design.md and rejected.

## Success criteria

`openspec/changes/ego-store-001/specs/persistence-store-contract/spec.md` exists, is self-contained, cites WRITE-004 by reference rather than restating it, and every requirement maps to code, an existing test, or a small new conformance test — no requirement without a real consumer or evidence.
