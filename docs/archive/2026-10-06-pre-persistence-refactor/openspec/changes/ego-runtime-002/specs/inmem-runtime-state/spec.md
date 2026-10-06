# Spec 2 of 7 — Event stream, durable state, publishers and tenancy (EGO-RUNTIME-005)

| Field | Value |
|---|---|
| Change | `ego-runtime-002` (umbrella: [`proposal.md`](../../proposal.md), [`design.md`](../../design.md)) |
| Chain position | **Spec 2.** Previous: [Spec 1 — runtime core](../inmem-runtime-core/spec.md). Next: [Spec 3 — sagas](../inmem-runtime-sagas/spec.md), [Spec 4 — passivation](../inmem-runtime-passivation/spec.md), [Spec 5 — `compose/inmem`](../compose-inmem/spec.md), [Spec 7 — erasure](../inmem-runtime-erasure/spec.md) |
| Tracker | [`#148`](https://github.com/getsyntegrity/ego/issues/148) |
| Baseline | `main` at `57c4b11` |
| Decisions applied | design §D4, §D6, §D7, §D8 (the `Stop` order) |

## Purpose

After spec 1 the runtime handles event-sourced commands but publishes nothing. This spec adds the rest of what a `compose.Spec` can configure for entities:

- the event stream and `Subscribe`;
- durable-state entities;
- encryption and event adapters;
- event and state publishers;
- tenant-aware mode.

It also gives `Runtime.Stop` the order `compose/inmem` relies on. It does not do erasure, which is spec 7 (blocked by #166). This spec does not wait on #166.

## Requirements

### Requirement: event stream

Events MUST be published to `topic.events`, and durable states to `topic.states`, only after their write succeeds. `Subscribe` MUST return a subscriber on both topics. The spec 1 table rows for `Subscribe` and `SpawnDurableState` change to the real behavior.

#### Scenario: publish after write

- GIVEN a subscriber and a command sequence
- WHEN the commands persist events, and one write fails
- THEN the subscriber receives, as a multiset, exactly the events that were written (checked with `awaitStream`)

### Requirement: durable-state entities

`SpawnDurableState` MUST check started, family, state store, then tenancy, in that order, and recover from `GetLatestState`. A command's new state MUST have the current state's protobuf type, and its version MUST differ from the prior version by exactly one (`durable_state_actor.go:575-590`); otherwise the result is `OutcomeFailed` with nothing written. A valid command MUST write once, update memory, then publish.

### Requirement: encryption and event adapters

With `Config.Encryptor` set, event payloads MUST be stored encrypted and decrypted on recovery. Durable state MUST NOT be encrypted. Event adapters MUST run on recovered events before `HandleEvent`.

### Requirement: publishers and `Stop`

- **Attaching.** Each publisher MUST get its own subscriber and goroutine. A duplicate ID MUST be rejected, and a publisher receives only messages published after it was attached.
- **Stopping.** `Stop`, bounded by its context, MUST do the following in this order (design §D8):
  1. refuse new calls;
  2. close every publisher and the stream, joining the errors;
  3. for each entity, wait for the mailbox turn in progress to finish, then let a durable-state entity write its state once more;
  4. answer items still queued behind the current turn with `ErrEngineNotStarted`. This is provisional under #24 (LIFE-004).
- **Timeout.** When the context expires during step 3, `Stop` MUST return an error naming the busy entities, and MUST skip their final write. This is provisional under #24.

#### Scenario: no leak

- GIVEN a runtime with two publishers and entities that have processed commands
- WHEN `Stop` returns
- THEN both publishers are closed, and the goroutine count, including the mailbox drain goroutines and the publisher goroutines, returns to its value before `Start` (checked with `awaitCondition`)

### Requirement: tenancy

With `Config.TenantResolver` set:

- spawns MUST bind a tenant: `WithTenant`, else `tenancy.FixedTenantOf`, else `ErrSpawnTenantUndetermined`;
- re-spawning under another tenant MUST fail with `ErrSpawnTenantMismatch`;
- `Dispatch` MUST resolve the caller's tenant and refuse a mismatch;
- persistence MUST use `persistence.NewTenantScope`;
- the resolver's `Resolve` MUST NOT be called at spawn.

## Tasks (5)

1. **Event stream and `Subscribe`.** *Check:* the publish-after-write scenario; clock timestamps (pinned with the internal manual clock).
2. **Durable state.** *Check:* version-rule, type-rule, recovery and publish tests.
3. **Encryption and event adapters.** *Check:* a round trip across a restart on the same stores with `testkit`'s key store; the stored payloads are not the plaintext; an adapter test.
4. **Publishers and `Stop`.** *Check:* the duplicate ID is rejected; a publisher attached after a publish sees only later messages; the no-leak scenario; a turn in progress finishes before the final write; a `Stop` whose context expires names the busy entity; one publisher's failing `Close` does not stop the others.
5. **Tenancy.** *Check:* one test per error of the requirement, plus a counting resolver that proves `Resolve` is not called at spawn.

## Checks

- `go test ./internal/inmemruntime/`
- `go run ./internal/cmd/archcheck`; spec 1's closure test still passes
- apidiff: no report for any public package
- the pull request uses "Refs #148", never a closing keyword, and states that `EraseEntity` does not yet meet the `port/runtime` contract (design §8)

## File ownership

`internal/inmemruntime/**`: new files for the stream, durable state, encryption, publishers and tenancy, plus the `Stop` function.

## Dependencies

Spec 1 merged. It does not wait on #166.

## Next in the chain

Specs [3](../inmem-runtime-sagas/spec.md), [4](../inmem-runtime-passivation/spec.md), [5](../compose-inmem/spec.md) and [7](../inmem-runtime-erasure/spec.md).
