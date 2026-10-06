# Spec 7 of 7 — `EraseEntity` with crypto-shredding (EGO-RUNTIME-005, #166)

| Field | Value |
|---|---|
| Change | `ego-runtime-002` (umbrella: [`proposal.md`](../../proposal.md), [`design.md`](../../design.md)) |
| Chain position | **Spec 7.** Previous: [Spec 2](../inmem-runtime-state/spec.md). Blocked by [#166](https://github.com/getsyntegrity/ego/issues/166). It is independent of specs 3–6 |
| Tracker | [`#148`](https://github.com/getsyntegrity/ego/issues/148), [`#166`](https://github.com/getsyntegrity/ego/issues/166) |
| Baseline | `main` at `57c4b11` |
| Decisions applied | design §D3, §D6; maintainer decision on `EraseEntity` (Q7, 2026-09-27) |

## Purpose

Until this spec lands, the in-memory `EraseEntity` returns `*UnsupportedError`. That limitation does **not** satisfy the `port/runtime` contract, which promises crypto-shredding (`port/runtime/runtime.go:115-119`).

The maintainer decided in #166 that the promise is kept, through an additive extension, with a key that belongs exclusively to the affected entity and tenant. This spec implements #166's outcome in the in-memory runtime. It is kept apart so that nothing else in the chain waits on #166.

## Requirements

### Requirement: #166's outcome

`EraseEntity` MUST do the following:

- in tenant-aware mode, resolve the caller's tenant, failing closed with `tenancy.ErrDenied` even when `full == false`;
- delete the entity's key through the extension #166 defines, with the entity-and-tenant key granularity #166 settles;
- when `full` is set, also delete events and snapshots up to the latest sequence number.

Where #166's final text differs from this summary, #166 wins, and this spec is revised before implementation.

#### Scenario: another tenant's key survives

- GIVEN the same persistence ID under tenants `t1` and `t2`, each with encrypted events
- WHEN a `t1` caller erases it
- THEN `t1`'s events can no longer be decrypted, and `t2`'s can

## Tasks (3)

1. **RED**: erasure tests per #166 (full and non-full; tenant-scoped and legacy; fail-closed with `full == false`; the scenario above). *Check:* they fail against the placeholder.
2. **Implementation**, and the spec 1 method table row changes. *Check:* the tests pass; closure test and `go run ./internal/cmd/archcheck`.
3. **Shared table, documentation and changelog.** Add an erasure scenario to the shared table in `internal/runtimeconsumer`; #166 also fixes GoAkt, so it runs on both roots. If spec 5 has merged, remove the `EraseEntity` limitation sentence from `compose/inmem`'s package documentation. Add a `CHANGELOG.md` line, because `EraseEntity` on `compose/inmem` goes from `ErrUnsupported` to working, an observable change although apidiff has no report. *Check:* the scenario passes on both roots; review of the documentation and changelog; apidiff shows no report.

## Checks

- `go test ./internal/inmemruntime/ ./internal/runtimeconsumer/ ./compose/...`
- apidiff: no report for any public package

## File ownership

`internal/inmemruntime/**` (erasure files); `internal/runtimeconsumer/**` (the erasure scenario); one sentence of `compose/inmem`'s package documentation; `CHANGELOG.md`.

## Dependencies

- Spec 2 merged (tenancy).
- **[#166](https://github.com/getsyntegrity/ego/issues/166)'s GoAkt implementation merged**: the extension and the key granularity (blocking), since task 3 runs the erasure scenario on both roots. This spec does not decide the key granularity; it depends on #166 for it.

## Closing #148

This spec's pull request is the only one in the chain that carries the closing keyword, "Closes #148" (design §8, maintainer decision 2026-09-27). Specs 0–6 use "Refs #148".

## Next in the chain

None.
