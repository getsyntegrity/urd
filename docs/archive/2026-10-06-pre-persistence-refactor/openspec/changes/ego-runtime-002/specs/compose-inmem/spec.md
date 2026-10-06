# Spec 5 of 7 — `compose/inmem` composition root (#105 IMPL-6)

| Field | Value |
|---|---|
| Change | `ego-runtime-002` (umbrella: [`proposal.md`](../../proposal.md), [`design.md`](../../design.md)) |
| Chain position | **Spec 5.** Previous: [Spec 2](../inmem-runtime-state/spec.md) (specs 3 and 4 can run in parallel). Next: [Spec 6 — neutrality proof](../runtime-neutrality/spec.md) |
| Tracker | [`#148`](https://github.com/getsyntegrity/ego/issues/148), [`#105`](https://github.com/getsyntegrity/ego/issues/105) IMPL-6 |
| Baseline | `main` at `57c4b11` |
| Decisions applied | design §D8; ego-arch-003 §D4–§D7 (with the §5.2 departure of design §D8); ego-arch-004 §D4, §D6; maintainer decisions on Q8 and Q9 (2026-09-27) |

## Purpose

This spec adds the only new public package of the chain. It is a composition root that takes the same `compose.Spec` as `compose/goakt` and validates it with the same rules, plus one of its own (M1: no projections). It starts, rolls back and stops the in-memory runtime with the same shared sequencer. Consumer code gets the runtime through `App.Runtime()`, typed as `port/runtime.Runtime`, exactly as it does from `compose/goakt`. There is no clock option (Q9).

## Requirements

### Requirement: validation at `New`

`New` MUST run `spec.Validate()` (V1–V8) and rule M1 (`Spec.Projections` empty), return every problem joined, each a `*compose.ValidationError`, and start nothing. It MUST apply no naming rule to `Spec.Name`.

### Requirement: start order and rollback

`Start` MUST run the steps `probe stores`, `start runtime` and `attach publishers` through `compose/internal/lifecycle`, and use `compose/internal/adapters.StartAndProbe` for publishers. On failure it MUST return a `*compose.StartError` naming the step, after undoing the earlier steps and closing every publisher never attached. The `App` MUST be single-use.

### Requirement: stop and accessor

`Stop` MUST undo the steps in reverse under the cleanup context, attempt every undo, be idempotent, and close the publishers of an `App` that never started. `Runtime()` MUST return an untyped nil before a successful `Start`, and for good after a failed one; after `Stop` it returns the stopped runtime.

### Requirement: package documentation

The package documentation MUST state:

- that the runtime is for tests and local development;
- which guarantees are in-memory-only (design §D7);
- that the order in which sagas receive events is **unspecified**, without naming any order (Q8);
- that `EraseEntity` returns `ErrUnsupported` until crypto-shredding lands ([#166](https://github.com/getsyntegrity/ego/issues/166)), and that this **does not meet** the `port/runtime` erasure contract. The public text refers to #166, not to an internal spec number;
- what differs from `compose/goakt`: three start steps, M1, and no `WithCluster`, `WithActorSystemOptions`, `WithTelemetry` or clock option.

### Requirement: GoAkt-free closure

`compose/inmem`'s production and test closures MUST contain neither the root package nor GoAkt nor `compose/goakt`.

## Tasks (5)

1. **`New`, options, M1** (RED first). *Check:* tests mirroring `compose/goakt/app_test.go`'s `TestNew_*` tests, plus M1 and a several-problems-at-once case.
2. **Start steps and rollback.** *Check:* a failure injected at each step (via the `afterStep` hook); a probe failure names the store; a cancelled context starts nothing; a publisher failure at *k*.
3. **Stop.** *Check:* a never-started `App` closes its publishers; `Stop` after `Stop` is a no-op; the stop order is recorded; the durable-state flush happens after the publishers close.
4. **`Runtime()` and the closure test.** *Check:* nil before `Start`; nil after a failed `Start`; the same runtime after `Start` and after `Stop`; `closure_test.go` over `go list -deps` and `-deps -test`.
5. **Docs and changelog.** The package documentation above, and a `CHANGELOG.md` Features entry that names the `EraseEntity` limitation without presenting it as meeting the contract. *Check:* review of both texts against the requirement (no order named; the erasure limitation stated as a limitation); apidiff on `compose/inmem` reports additions only; `go vet`, `golangci-lint`.

## Checks

- `go test ./compose/...` (the shared `compose` and `compose/internal/...` tests pass unchanged)
- `go run ./internal/cmd/archcheck`
- apidiff: additions only for `compose/inmem`; no report for `compose`, `compose/goakt` or `ego`
- the pull request uses "Refs #148", never a closing keyword, and states that `EraseEntity` does not yet meet the `port/runtime` contract (design §8)

## File ownership

`compose/inmem/**` (new); `CHANGELOG.md`. It MUST NOT edit `compose/spec.go`, `compose/errors.go`, `compose/internal/**` or `compose/goakt/**`.

## Dependencies

Specs 1 and 2 merged. It does not depend on spec 4 (there is no clock option) or on #166.

## Next in the chain

[Spec 6](../runtime-neutrality/spec.md).
