# Spec 4 of 7 — Passivation (EGO-RUNTIME-005)

| Field | Value |
|---|---|
| Change | `ego-runtime-002` (umbrella: [`proposal.md`](../../proposal.md), [`design.md`](../../design.md)) |
| Chain position | **Spec 4.** Previous: [Spec 2](../inmem-runtime-state/spec.md). Next: [Spec 6 — neutrality proof](../runtime-neutrality/spec.md) |
| Tracker | [`#148`](https://github.com/getsyntegrity/ego/issues/148) |
| Baseline | `main` at `57c4b11` |
| Decisions applied | design §D11; maintainer decision on passivation (2026-09-27): implement it, with a typed error only as a fallback; spec 0's recorded activity cases |

## Purpose

On a single node, GoAkt honors `WithPassivateAfter`. An entity idle for that long is stopped, `EntityExists` reports false, and `Dispatch` does not re-spawn it (`engine.go:1910-1911`). The maintainer decided that the in-memory runtime must not ignore the setting silently. This spec implements it with the same observable semantics, driven by the internal clock of spec 1, so its tests never wait on wall time.

## Requirements

### Requirement: activity

Every item an entity's mailbox processes MUST reset its idle timer, except the passivate item itself. That includes commands, refused commands (for example a tenant mismatch) and no-event commands. Idle time counts from the start of the turn (design §D11, following GoAkt's `markActivity`, `actor/pid.go:2192-2196`).

### Requirement: passivation

An event-sourced or durable-state entity spawned with `WithPassivateAfter(d)`, `d > 0`, MUST be passivated after being idle for `d`, in these steps:

1. the timer enqueues a passivate item;
2. when the item runs, it re-checks idleness against the clock;
3. a durable-state entity writes **and publishes** its state;
4. the entity is removed.

After that, `EntityExists` MUST report false, and `SendCommand`/`Dispatch` MUST fail as for an unknown ID, without re-spawning. A new spawn MUST recover the state from the stores. Sagas MUST ignore the setting. `Stop` MUST stop every idle timer.

#### Scenario: a refused command is activity

- GIVEN an entity with `WithPassivateAfter(10s)` under the manual clock
- WHEN the clock advances 6 s, a tenant-mismatched command is refused, and the clock advances 6 s more
- THEN the entity is still alive; after 4 s more it passivates

#### Scenario: the item races a command

- GIVEN a passivate item queued behind a command
- WHEN the command runs first
- THEN the item finds the entity no longer idle and does nothing

## Tasks (3)

1. **Idle timer and activity** (RED first). *Check:* the refused-command scenario; a no-event command is activity; the item-races-a-command scenario.
2. **Passivation and removal.** *Check:* a durable-state entity writes and publishes (checked with `awaitStream`) before removal; `EntityExists` is false; `Dispatch` does not re-spawn; a re-spawn recovers the state; sagas ignore the option.
3. **Timers at `Stop`.** *Check:* after `Stop` with pending idle timers, the manual clock reports no pending callbacks and the goroutine count returns to its value before `Start` (`awaitCondition`).

## Checks

- `go test ./internal/inmemruntime/`; review check: no `time.Sleep`, no `pause.For` and no wall-clock ticker in this spec's tests
- closure test and `go run ./internal/cmd/archcheck`
- apidiff: no report for any public package
- the pull request uses "Refs #148", never a closing keyword, and states that `EraseEntity` does not yet meet the `port/runtime` contract (design §8)

## File ownership

`internal/inmemruntime/**`: new passivation files, plus one call in the mailbox's turn path.

## Dependencies

Specs 1 (the clock) and 2 (the durable-state write and publish) merged.

## Next in the chain

[Spec 6](../runtime-neutrality/spec.md).
