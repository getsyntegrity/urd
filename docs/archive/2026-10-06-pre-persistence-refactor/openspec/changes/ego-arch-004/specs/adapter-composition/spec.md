# Spec 3 of 3 — Composition uses the SPI, and the extension guide (EGO-ARCH-004)

| Field | Value |
|---|---|
| Change | `ego-arch-004` (umbrella: [`proposal.md`](../../proposal.md), [`design.md`](../../design.md)) |
| Chain position | **Spec 3 of 3.** Previous: [Spec 2 — conformance suites and two adopters](../adapter-conformance/spec.md). Next: none in this chain; the follow-ups are listed in design §5 |
| Slices (pull requests) | SPI-5 |
| Tracker | [`#106`](https://github.com/getsyntegrity/ego/issues/106) |
| Baseline | `main` at `f2b5130148086d95a4d6362971b85e800b0ce155` |
| Decisions applied | design §D3, §D4, §D5, §D6, §4; maintainer decision O4 (2026-09-27, design §9) |

## Purpose

After specs 1 and 2 the SPI exists and is tested, but core code does not use it yet. This spec makes the composition root and the engine use it:

- `compose.Spec.Validate` gains rule V8, which rejects a declared adapter whose declaration and methods disagree, before anything starts.
- `compose/goakt` step 4 starts and probes owned publishers through `port/adapter`, using a helper that `compose/inmem` (#148) can reuse.
- The engine's one scattered type assertion (`engine.go:883`) moves behind a `tenancy` accessor.
- The extension guide, `docs/adapters.md`, is written.

## Requirements

### Requirement: V8 validates declared adapters statically

`compose.Spec.Validate` MUST apply the following rules to every slot value that declares a descriptor. They run after V5 and V6 and skip values those rules already rejected.

- **V8a** — the slot's port is one of the descriptor's `Ports`.
- **V8b** — declaration and method set agree in both directions for `CapStart`, `CapReady` and `CapFixedTenant`, checked through `adapter.StarterOf`/`PingerOf` and `tenancy.AsFixedTenantResolver`. Capabilities implied by the port are skipped.
- **V8c** — a requirement table that starts empty.

Undeclared adapters MUST validate exactly as today (design §D6). Two limits on V8b also apply:

- **Unknown capabilities.** A declared capability that `compose` does not know (one added later by another issue) MUST be accepted by V8; the adapter's own conformance tests check it (AT-1 through `Target.Capabilities`).
- **The runtime port.** When a runtime port is added (F-E), V8b MUST be one-directional for it (declared ⇒ implemented only), because the composite `port/runtime` interface makes every runtime implement every capability interface (`openspec/changes/ego-runtime-001/design.md` §8). Runtime capabilities are declaration-only.

#### Scenario: undeclared-but-implemented fixed tenant

- GIVEN a declared tenant resolver that implements `FixedTenantResolver` but does not declare `CapFixedTenant`
- WHEN `Spec.Validate` runs
- THEN it returns a `*ValidationError` with `Rule: "V8"` naming the slot and the capability

#### Scenario: typed nil is reported once

- GIVEN a typed-nil publisher in `Spec.EventPublishers`
- WHEN `Spec.Validate` runs
- THEN V5 reports it and V8 does not call `Describe` on it

### Requirement: step 4 starts and probes owned publishers

`compose/goakt` step 4 MUST, for each publisher in `Spec` order, call `Start` when `adapter.StarterOf` finds one, then `Ping` when `adapter.PingerOf` finds one, before attaching publishers. It MUST do so through a runtime-free helper in `compose/internal/adapters`. A failure MUST roll back as ego-arch-003 §D6 defines, and every publisher MUST end up closed.

#### Scenario: failure at publisher k

- GIVEN three publishers whose second `Start` fails
- WHEN `App.Start` runs
- THEN it returns a `*compose.StartError` with step "attach publishers" naming the second publisher, and all three publishers are closed

### Requirement: one assertion per optional interface

`tenancy.AsFixedTenantResolver` MUST be the only type assertion on `FixedTenantResolver`. `tenancy.FixedTenantOf` and `engine.go:883` MUST call it, with no change in behavior.

#### Scenario: a multi-tenant resolver that implements the interface

- GIVEN a resolver that implements `FixedTenantResolver` and returns `(zero TenantID, false)`
- WHEN a spawn without `WithTenant` runs
- THEN `FixedTenantOf` reports no fixed tenant and the spawn fails with `ErrSpawnTenantUndetermined`, exactly as before this spec

#### Scenario: no other assertion site

- GIVEN the production Go files after this spec
- WHEN they are searched for a type assertion to `FixedTenantResolver`
- THEN the only match is inside `tenancy.AsFixedTenantResolver`

## Tasks (5)

1. **V8** — V8a/V8b/V8c in `compose/spec.go`, written RED first (including the undeclared-but-implemented `FixedTenantResolver` case and the typed-nil ordering case).
2. **Start-and-probe helper** — `compose/internal/adapters`; `compose/goakt` step 4 uses it; injected-failure test at publisher *k*.
3. **Store probe** — `probeStores` uses `adapter.PingerOf` instead of its private `pinger` interface (`compose/goakt/app.go:241`).
4. **Tenancy accessor** — `tenancy.CapFixedTenant`, `AsFixedTenantResolver`, `FixedTenantOf`; `engine.go:883` calls `FixedTenantOf`.
5. **Guide** — `docs/adapters.md` from design §4, with the websocket adopter from spec 2 as its example.

## Checks

- `go test ./compose/... ./tenancy/...`
- the root lane for the `engine.go` change (the selector runs the full root package suite for any root-package file)
- `go run ./internal/cmd/archcheck`: `composition-no-runtime` covers `compose/internal/adapters`
- apidiff on touched packages: additions only, no exported change in `compose`, `compose/goakt` or `ego`

## File ownership

| Slice | Files |
|---|---|
| SPI-5 | `compose/spec.go`, `compose/spec_test.go`, `compose/internal/adapters/**` (new), `compose/goakt/app.go`, `compose/goakt/app_test.go`, `tenancy/resolver.go`, `engine.go` (the line at `:883` only), `docs/adapters.md` (new) |

## Dependencies

- **Spec 1** (`port/adapter`) and **spec 2** (the adopter the guide uses) must be merged.
- **Shared hot spots** (design §6): `compose/goakt/app.go` and `app_test.go` with #147 S4-4 (`App.Runtime()`) and #146. The recommended order is to let S4-4 land first and rebase this slice onto it. #147 S4-3 does not touch `engine.go`: its compile-time assertion goes in `engine_runtime.go`. #147 S4-2 edits only the error `var` block of `engine.go`, a different region from line 883.
- **#148** (`compose/inmem`) reuses `compose/internal/adapters`. If #148 lands first, this slice extracts the helper from both composition roots instead.

## Next in the chain

None: this spec completes #106's acceptance criteria (design §8). The named follow-ups outside the chain, including the approved O3 publisher IDs and the remaining publishers' adoption, are listed in design §5.
