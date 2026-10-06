# Spec 3 of 7 — Sagas, tenant rules and `SagaStatus` (EGO-RUNTIME-005)

| Field | Value |
|---|---|
| Change | `ego-runtime-002` (umbrella: [`proposal.md`](../../proposal.md), [`design.md`](../../design.md)) |
| Chain position | **Spec 3.** Previous: [Spec 2](../inmem-runtime-state/spec.md). Next: [Spec 6 — neutrality proof](../runtime-neutrality/spec.md) (specs 4 and 5 can run in parallel) |
| Tracker | [`#148`](https://github.com/getsyntegrity/ego/issues/148) |
| Baseline | `main` at `57c4b11` |
| Decisions applied | design §D5, §D7; #153 as fixed by #163; maintainer decision on Q8 (2026-09-27); spec 0's recorded result for saga options |

## Purpose

This spec makes the in-memory runtime run sagas the way `saga_actor.go` does, tenant rules included. A saga receives events directly from the runtime instead of through the event stream. The order in which it receives them is **unspecified** (maintainer decision on Q8): nothing documents, exposes or tests an order. This spec also adds that sentence to `port/runtime`'s `Sagas` documentation. `SagaStatus` reports the lifecycle status #153 fixed. The spec 1 table rows for `SpawnSaga` and `SagaStatus` change to the behavior below.

## Requirements

### Requirement: spawn and recovery

`SpawnSaga` MUST check started, family, events store, then tenancy, in that order. It MUST read only `WithTenant` among the spawn options, and recover the saga's own events through `ApplyEvent` before returning.

### Requirement: delivery and reaction

- **Delivery.** Every event an entity persists MUST be enqueued to every live saga's mailbox before the entity's command reply is returned. The order is unspecified.
- **Reaction.** In its mailbox the saga MUST run `HandleEvent`, persist the action's events to the events store only (not the stream), and apply them. It then sends each command through the runtime's internal dispatch, with the saga's tenant and derived metadata and a default timeout of 5 s, and calls `HandleResult` or `HandleError`.
- **Not running.** A saga whose status is not `SagaRunning` MUST ignore further events (`saga_actor.go:482-484`).

#### Scenario: reaction, without asserting an order

- GIVEN a saga that sends `CreditAccount` to `b` for every `AccountCredited` of `a`
- WHEN three commands credit `a`
- THEN `SagaStatus`, queried after the third `SendCommand` returns, sees three reactions, and `b`'s journal holds three credits. The assertion is about counts and final state only

### Requirement: tenant rules (design §D5)

- **SG4**: a live event with invalid tenant metadata is dropped, and an event whose tenant differs from the saga's bound tenant is dropped before `HandleEvent`.
- **SG5**: a replayed saga event with a different tenant fails the spawn and registers nothing.
- **SG-DUR1**: replay skips an `emptypb.Empty` binding marker without calling `ApplyEvent`, and the runtime never writes new markers.

### Requirement: completion, compensation, timeout, status

- **Completion.** `Complete` MUST set `SagaCompleted`.
- **Compensation and timeout.** `Compensate` and the timeout path MUST run every compensation command in one mailbox turn, then set `SagaCompleted` or `SagaFailed` as `saga_actor.go:806-829` does. The timeout timer comes from the internal clock.
- **Status.** `SagaStatus` MUST be answered from inside the saga's mailbox, with `ErrUndefinedEntityID` for an empty ID.
- **Existence.** `EntityExists` MUST report true for a live saga's ID.

### Requirement: no promised order (Q8)

`port/runtime`'s `Sagas` documentation MUST say that the order in which a saga receives events is unspecified, naming no order. No exported symbol, option or hook MAY select or report an order. No test MAY assert one.

## Tasks (5)

1. **Spawn and recovery** (RED first). *Check:* recovery from persisted saga events; family before store; options other than `WithTenant` have no effect, as spec 0 recorded.
2. **Delivery and reaction.** *Check:* the reaction scenario; a saga command to an unknown entity reaches `HandleError`; no deadlock when the target entity's events wake the same saga; a completed saga ignores events; `EntityExists` on the saga ID.
3. **Tenant rules**, one check per rule. *Check:*
   - SG4: a counting behavior shows that a foreign-tenant event never reaches `HandleEvent`, and that invalid tenant metadata is dropped;
   - SG5: a journal with one foreign-tenant saga event fails the spawn;
   - SG-DUR1: a journal holding a marker recovers without `ApplyEvent` being called for it, and no marker is written.
4. **Completion, compensation, timeout.** *Check:* completed, compensation succeeded, compensation failed, and timeout-triggered compensation driven by `manualClock.advance`; the timer is stopped by `Stop`.
5. **`SagaStatus` and the Q8 documentation.** *Check:* a status table test (running, completed, failed, unknown ID, empty ID); the `port/runtime` doc sentence is present; review check: no test in this spec asserts a delivery order, and no exported symbol or option refers to one.

## Checks

- `go test ./internal/inmemruntime/ ./port/runtime/`
- closure test and `go run ./internal/cmd/archcheck`
- apidiff: no report for any public package (the `port/runtime` change is a doc comment)
- the pull request uses "Refs #148", never a closing keyword, and states that `EraseEntity` does not yet meet the `port/runtime` contract (design §8)

## File ownership

`internal/inmemruntime/**`: new saga files, plus one enqueue call in the entity event path. `port/runtime/runtime.go`: one sentence in the `Sagas` documentation. FU-E (the `SendCommand` contract text) and #166 (the `EraseEntity` contract) also edit `port/runtime/runtime.go`; whichever lands second rebases.

## Dependencies

Specs 1 and 2 merged.

## Next in the chain

[Spec 6](../runtime-neutrality/spec.md).
