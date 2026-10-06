# Spec 6 of 7 — Neutrality proof (#105, #148)

| Field | Value |
|---|---|
| Change | `ego-runtime-002` (umbrella: [`proposal.md`](../../proposal.md), [`design.md`](../../design.md)) |
| Chain position | **Spec 6.** Previous: [Spec 3](../inmem-runtime-sagas/spec.md), [Spec 4](../inmem-runtime-passivation/spec.md), [Spec 5](../compose-inmem/spec.md). Next: none ([spec 7](../inmem-runtime-erasure/spec.md) is independent) |
| Tracker | [`#148`](https://github.com/getsyntegrity/ego/issues/148), [`#105`](https://github.com/getsyntegrity/ego/issues/105) |
| Baseline | `main` at `57c4b11` |
| Decisions applied | design §D9, §D10, §D12; maintainer decision on Q8 (2026-09-27) |

## Purpose

This spec delivers the evidence #105 has been waiting for. One behavior value and one consumer function, both written only against `port/runtime` and `port/behavior`, run on `compose/goakt` and on `compose/inmem` with the same `Spec`, and they must produce the same observable state, journal and events. It reuses the harness spec 0 built. Every scenario spec 0 measured on GoAkt now also runs on the in-memory root, so the two runtimes are compared rather than guessed.

## Requirements

### Requirement: `Observe` and the duplicated-rule scenarios

`internal/runtimeconsumer` MUST gain `Observe` (the account behavior, one durable-state behavior and one saga) and the duplicated-rule scenarios of design §D10, in production files. The closure allowlist stays as spec 0 left it.

### Requirement: the same observations on both roots

`internal/runtimeconsumer/neutrality_test.go` MUST do the following for every entry of `Scenarios`: start `compose/goakt` and `compose/inmem` from one `compose.Spec` shape with fresh `testkit` stores for each root, run the scenario on each `App.Runtime()`, and require equal normalized traces (design §D9). Stream messages are compared as a **multiset**. Collection stops at the count implied by the journal and the latest states (passivation writes included), or at the context deadline, which is a failure.

#### Scenario: drift is caught

- GIVEN a deliberate change to the in-memory no-event reply, made only in a local experiment
- WHEN the neutrality test runs
- THEN it fails, naming the scenario and the differing field (recorded in the pull request, not committed)

### Requirement: order-independent fixtures (Q8)

The sagas `Observe` uses as comparison fixtures MUST produce the same trace whatever order they receive events in; this is a constraint on the fixtures, not on consumer behaviors. No test MAY assert a delivery order, and no hook to change it MAY exist.

### Requirement: no sleeps

Every wait goes through `awaitStream` or `awaitCondition` under a context deadline.

## Tasks (4)

1. **`Observe` and the duplicated-rule scenarios** (RED first: the neutrality test fails to build). *Check:* each scenario runs on `compose/inmem` alone; review check: the saga fixtures' results depend only on counts and final states.
2. **`neutrality_test.go`** over every entry of `Scenarios`, including the ones spec 0 wrote. *Check:* `go test ./internal/runtimeconsumer/` green on both roots.
3. **Drift experiment.** *Check:* the experiment's failure output is recorded in the pull request.
4. **Records.**
   - `CHANGELOG.md`: one line saying that the in-memory composition runs the same behavior and consumer as the GoAkt one.
   - ego-arch-003 §6: the IMPL-6 row marked done, with the §5.2 departure of design §D8.
   - ego-arch-001 §4: the map rows.
   
   *Check:* the documentation is read back. The pull request maps each #148 criterion to its evidence, and states that `EraseEntity` is not covered until spec 7, that projections meet the typed-error criterion only for their own capability (Q3), and that #148 remains open pending spec 7.

## Checks

- `go test ./internal/runtimeconsumer/ ./compose/...`
- the root lane `ciselect` picks (no selector change)
- `go run ./internal/cmd/archcheck`; apidiff: no report for any public package
- the pull request uses "Refs #148", never a closing keyword, and states that `EraseEntity` does not yet meet the `port/runtime` contract (design §8)

## File ownership

`internal/runtimeconsumer/**`; `CHANGELOG.md`; `openspec/changes/ego-arch-003/design.md` (§6 IMPL-6 row, §7 last row); `openspec/changes/ego-arch-001/design.md` (§4 map rows).

## Dependencies

Specs 3, 4 and 5 merged. After it lands, #148's neutrality criterion and #105's in-memory criterion have their evidence. The `EraseEntity` contract is **not** met until spec 7. #148's typed-error criterion is covered only for projections (Q3), and #148's wording is amended for spawn settings (Q2). **#148 stays open:** this spec's criteria mapping records #148 as open pending spec 7, and its pull request uses "Refs #148", never a closing keyword. Only spec 7's pull request closes #148 (design §8). Closing #105 remains the maintainer's call.

## Next in the chain

None. The follow-ups are FU-A (projection runner), FU-B (extract shared rules), FU-C (ordered event stream), FU-D (neutral entity-not-found error) and FU-E (the no-event `SendCommand` contract text), listed in design §8.
