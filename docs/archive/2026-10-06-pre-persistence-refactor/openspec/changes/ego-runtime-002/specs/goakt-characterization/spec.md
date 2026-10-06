# Spec 0 of 7 — Characterize the GoAkt runtime first (EGO-RUNTIME-005)

| Field | Value |
|---|---|
| Change | `ego-runtime-002` (umbrella: [`proposal.md`](../../proposal.md), [`design.md`](../../design.md)) |
| Chain position | **Spec 0.** Previous: none. Next: [Spec 1 — runtime core](../inmem-runtime-core/spec.md) |
| Tracker | [`#148`](https://github.com/getsyntegrity/ego/issues/148) |
| Baseline | `main` at `57c4b11` |
| Decisions applied | design §D12 (maintainer decision 2026-09-27), §D9, §D10 |

## Purpose

The in-memory runtime copies GoAkt's observable behavior. A few behaviors cannot be settled by reading the code:

- what a panic does under each supervisor directive;
- what happens to a command queued behind a failed write;
- which messages count as passivation activity.

This spec measures them on GoAkt before any in-memory code exists, and writes the results into the design. It also builds the harness that spec 6 later reuses for the neutrality proof. It touches no root-package file and no hot spot.

## Requirements

### Requirement: a neutral harness

`internal/runtimeconsumer` MUST gain, in production files:

- `Scenarios`, a list of `{Name, Run func(ctx, runtimeport.Runtime, Stores) (Trace, error)}`;
- `Trace` and its normalizer;
- `Stores`;
- the wait helpers `awaitStream` and `awaitCondition` (design §D10).

Its production closure test keeps rejecting the root package and GoAkt. Its `allowedFirstParty` list (`internal/runtimeconsumer/closure_test.go:59-68`) grows by **exactly two entries**, `persistence` and `egopb`.

### Requirement: a GoAkt-only runner

`internal/runtimeconsumer/characterization_test.go` (package `runtimeconsumer_test`) MUST start `compose/goakt` from a `compose.Spec` with fresh `testkit` stores for each scenario, run the scenario on `App.Runtime()`, and assert the measured trace. It uses no sleeps; the only waits are the two helpers, under a context deadline.

### Requirement: results recorded before spec 1

Each task's pull-request commit MUST write the measured result into the design (§2, §D4, §D11 or the Q2 table, as named in the task), replacing the word "provisional". Spec 1 MUST NOT start until all four are recorded.

**When a measurement contradicts a maintainer decision** (§5, "Maintainer decisions recorded") or a recommendation the maintainer relied on, spec 0 does not amend the design. It records the measurement in the pull request, marks the affected rule "blocked on maintainer", and stops. Spec 1 waits for the maintainer's answer.

## Tasks (4)

1. **Harness.** `Scenarios`, `Trace`, `Stores`, `awaitCondition`, `awaitStream`, and the allowlist growing by exactly `persistence` and `egopb`. The comment above `TestProductionClosureExcludesRootAndGoAkt` (`closure_test.go:74-75`), which says the GoAkt end-to-end test lives in `compose/goakt`, is updated to name this package's GoAkt test files too. *Check:* normalizer unit tests (timestamps, shard, key IDs and failure text removed; a multiset comparison catches a duplicate); `awaitStream` fails on a deadline instead of returning a short slice; the closure test passes.
2. **Liveness after failures.** `EntityExists` after a failed write (a `mocks/` events store), after an out-of-sync conflict and after an in-sync conflict. Also a command queued behind a failed write: its reply, and whether it runs. *Check:* the scenario passes on `compose/goakt`; the results are recorded in design §2.1 and §D4.
3. **Panics.** A handler panic under `RestartDirective` and under `StopDirective`: the reply, `EntityExists`, and the state recovered by the next command or spawn. *Check:* the scenario passes on `compose/goakt`; the results are recorded in design §2.5, §D4 and the Q2 table.
4. **Passivation activity and ignored options.**
   - Passivation: spawn with `WithPassivateAfter(d)`, `d ≥ 1 s`. Send a refused command (tenant mismatch) and, in a separate run, a no-event command, each at least `d/2` after the previous message. The spawn counts as the previous message for the first one. A gap of `d/2` (at least 500 ms) is much larger than the 100 ms coalescing window, so a wrong outcome ("a refused command does not count as activity") would make the entity passivate about `d/2` early, and the elapsed-time assertion catches it even when GoAkt's trigger fires late. Wait with `awaitCondition` until `EntityExists` is false, and assert that the time elapsed since the last message is at least `d − 100 ms`. Do not assert that the entity is still alive at a given moment: GoAkt coalesces deadline renewal to once per 100 ms and does not re-check activity when the deadline fires (design §2.5). A passivating durable-state entity writes **and** publishes its state.
   - Ignored options: spawning with `WithPlacement(Random|LeastLoad|Local)` and `WithRelocation(true)` behaves as without them; a saga spawned with options other than `WithTenant` behaves as without them.
   
   *Check:* the scenarios pass on `compose/goakt`. The `d/2` gap between messages is kept with a bounded timed wait (a `time.Timer` inside a `select` with the context). This is the only wall-clock wait this chain allows. It is bounded and is not a sleep; the results are recorded in design §2.5, §D11 and the Q2 table.

## Checks

- `go test ./internal/runtimeconsumer/` (no `-race` locally)
- `go run ./internal/cmd/archcheck`; apidiff: no report for any public package
- the pull request uses "Refs #148", never a closing keyword, and states that `EraseEntity` does not yet meet the `port/runtime` contract (design §8)

## File ownership

`internal/runtimeconsumer/**`; `openspec/changes/ego-runtime-002/design.md` (the recorded results only).

## Dependencies

This design approved.

## Next in the chain

[Spec 1](../inmem-runtime-core/spec.md), which starts only after this spec's results are recorded.
