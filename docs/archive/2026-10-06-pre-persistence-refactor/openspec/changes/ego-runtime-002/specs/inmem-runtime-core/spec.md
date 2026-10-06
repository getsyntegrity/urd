# Spec 1 of 7 — In-memory runtime core: package, rule, clock, event-sourced entities and commands (EGO-RUNTIME-005)

| Field | Value |
|---|---|
| Change | `ego-runtime-002` (umbrella: [`proposal.md`](../../proposal.md), [`design.md`](../../design.md)) |
| Chain position | **Spec 1.** Previous: [Spec 0 — GoAkt characterization](../goakt-characterization/spec.md). Next: [Spec 2 — stream, durable state, publishers, tenancy](../inmem-runtime-state/spec.md) |
| Tracker | [`#148`](https://github.com/getsyntegrity/ego/issues/148) |
| Baseline | `main` at `57c4b11` |
| Decisions applied | design §D1, §D2, §D3, §D4, §D7, §D11 (the internal clock, Q9). It applies the maintainer's 2026-09-27 decisions on Q1, Q3, Q4 and Q2 (Q2 conditional on spec 0). The rules spec 0 measured are taken as recorded in the design |

## Purpose

This spec creates the runtime package, the rule that keeps it free of GoAkt, and the internal clock every later timer uses. It then makes event-sourced entities work: they recover from the stores and handle commands one at a time, with the failure, conflict and panic rules spec 0 measured on GoAkt. The package implements the whole `port/runtime.Runtime` interface from its first pull request. What is not built yet answers with a typed error, as the method table below says.

## Requirements

### Requirement: the package implements `port/runtime.Runtime` without GoAkt

`internal/inmemruntime` MUST declare `var _ runtimeport.Runtime = (*Runtime)(nil)`. Its production and test dependency closures (`go list -deps` and `go list -deps -test`) MUST contain none of these: the root package, any `github.com/tochemey/goakt/v4` package, or `compose/goakt`.

### Requirement: archcheck rule `inmem-no-runtime`

`internal/cmd/archcheck/rules` MUST gain `InMemoryRuntimeLayer`, matching `internal/inmemruntime/...` and `compose/inmem/...`, and the denylist rule `inmem-no-runtime` of design §D2. No baseline entry and no exception MAY be added, and no existing rule MAY change.

#### Scenario: a forbidden edge

- GIVEN a test graph in which `internal/inmemruntime` imports the root package (and, in separate cases, `internal/extensions`, a GoAkt package, and `compose/goakt`)
- WHEN `rules.Evaluate` runs
- THEN it reports one `inmem-no-runtime` violation per edge, with the matching reason

### Requirement: the internal clock

`internal/inmemruntime/clock.go` MUST hold the unexported `clock` interface, `wallClock` (used by `New`) and `manualClock`. `manualClock` is reachable only by the package's tests, through `newWithClock` (design §D11). `manualClock.advance(d)` MUST run every callback due at or before the new time, synchronously, in the caller's goroutine. It MUST order them by deadline, then by registration order, and repeat until none is due. Nothing about the clock is exported.

### Requirement: the method table

The runtime MUST answer all 14 methods of `port/runtime.Runtime` from this spec on:

| Methods | After spec 1 | Changed by |
|---|---|---|
| `SpawnEventSourced`, `EntityExists`, `SendCommand`, `Dispatch` | implemented | — |
| `Subscribe`, `SpawnDurableState` | `*UnsupportedError{Runtime: "inmem", Operation: <method>}`, as a placeholder | spec 2 |
| `SpawnSaga`, `SagaStatus` | `*UnsupportedError`, as a placeholder | spec 3 |
| `EraseEntity` | `*UnsupportedError`. This does **not** satisfy the contract (`port/runtime/runtime.go:115-119`) | spec 7, after #166 |
| the five `Projections` methods | `*UnsupportedError`, in every lifecycle state, before any side effect | nobody in this chain (Q3) |

Otherwise, before `Start` and after `Stop` every method MUST return `ErrEngineNotStarted`. An unsupported method returns its error in every lifecycle state ("ErrUnsupported comes first"). The table test is keyed by method, and each later spec updates its own rows.

### Requirement: event-sourced entities

- **Spawn.** `SpawnEventSourced` MUST check, in order: started, family, events store, tenancy. It MUST be idempotent for a live ID, and it MUST recover synchronously from the snapshot store (when set) and the events store.
- **Commands.** `SendCommand` and `Dispatch` MUST run inside the entity's mailbox, one command at a time. They MUST apply the deadline checks, handler preference, precondition mapping, conflict result and no-event reply (current state and revision) of design §D4.
- **Failures.** They MUST also apply the rules for failed writes, conflicts, queued commands and panics as spec 0 recorded them in design §D4.
- **Writes.** Events MUST be written with one `WriteEvents` per command. Publishing them on the stream is spec 2.
- **Spawn options.** Placement and relocation are ignored (Q2, decided, provisional pending RUNTIME-003). Passivation is spec 4.

#### Scenario: family before store

- GIVEN a runtime that declares only `DurableState` and has no events store
- WHEN `SpawnEventSourced` is called
- THEN the error wraps `ErrEntityFamilyNotDeclared`, not `ErrEventsStoreRequired`

#### Scenario: no event

- GIVEN a live entity at revision 2
- WHEN a command emits no events
- THEN `SendCommand` returns the current state (not nil) and revision 2

#### Scenario: serialized mailbox

- GIVEN ten goroutines each sending one command, each emitting one event, to the same entity
- WHEN all return
- THEN the revisions are exactly 1 through 10, each once

## Tasks (5)

1. **Package and rule.** `internal/inmemruntime` with `Config`, `New`, `Runtime`, `Start`, `Stop`, the compile-time assertion and `closure_test.go`. The `inmem-no-runtime` layer and rule, with `evaluate_test.go` cases. The `docs/ci.md` rule-table row. *Check:* `go test ./internal/inmemruntime/ ./internal/cmd/archcheck/...`; `go run ./internal/cmd/archcheck` reports 0 violations and the unchanged baseline count.
2. **Lifecycle and the method table** (RED first). *Check:* the method-keyed table test before `Start`, after `Start` and after `Stop`.
3. **Clock.** `clock.go` with the three pieces. *Check:* `manualClock` unit tests (callbacks run inside `advance`, ordered by deadline then registration; a callback registered during `advance` that is already due also runs; a stopped callback does not run); an exported-API check that nothing clock-related is exported.
4. **Spawn and recovery** for event-sourced entities, `EntityExists`, the family guard in GoAkt's order. *Check:* recovery, idempotent re-spawn, missing events store, family-before-store and undeclared-family tests.
5. **Commands.** Mailbox, `SendCommand`, `Dispatch` with deadlines and preconditions, the no-event reply, and the failure, conflict, queued-command and panic rules as recorded by spec 0. *Check:* the scenarios above, a deadline-already-passed test and a canceled-context test, plus one test per rule spec 0 recorded. No sleeps.

## Checks

- `go test ./internal/inmemruntime/ ./internal/cmd/archcheck/...` (no `-race` locally)
- `go run ./internal/cmd/archcheck`; `golangci-lint run ./internal/inmemruntime/... ./internal/cmd/archcheck/...`
- apidiff: no report for any public package
- the pull request uses "Refs #148", never a closing keyword, and states that `EraseEntity` does not yet meet the `port/runtime` contract (design §8)

## File ownership

`internal/inmemruntime/**` (new); `internal/cmd/archcheck/rules/layers.go`, `rules.go`, `evaluate_test.go`; `docs/ci.md` (one row in the rule table). This is the only spec that touches archcheck.

## Dependencies

Spec 0 merged, with its results recorded in the design.

## Next in the chain

[Spec 2](../inmem-runtime-state/spec.md).
