# Spec 1 of 3 — Adapter SPI package and composition-root boundary (EGO-ARCH-004)

| Field | Value |
|---|---|
| Change | `ego-arch-004` (umbrella: [`proposal.md`](../../proposal.md), [`design.md`](../../design.md)) |
| Chain position | **Spec 1 of 3.** Previous: none. Next: [Spec 2 — conformance suites and two adopters](../adapter-conformance/spec.md) |
| Slices (pull requests) | SPI-1 (`port/adapter`), SPI-2 (archcheck rule) |
| Tracker | [`#106`](https://github.com/getsyntegrity/ego/issues/106) |
| Baseline | `main` at `f2b5130148086d95a4d6362971b85e800b0ce155` |
| Decisions applied | design §D1, §D2, §D3, §D7; maintainer decisions O1 and O2 (2026-09-27, design §9) |

## Purpose

This spec creates the vocabulary every other part of the chain uses, and closes the gap the #106 follow-up comment found. It adds a small contract package, `port/adapter`, where an adapter can say which ports it serves and which optional capabilities it has, plus the only functions allowed to type-assert those optional interfaces. It also adds an archcheck rule, so a nested adapter module that imports the composition root (`compose` or anything under it) fails CI. The maintainers decided on 2026-09-27 (O1) that adapters must not do that.

Nothing in core changes behavior in this spec. Core starts using the new package in spec 3.

## Requirements

### Requirement: `port/adapter` is a standard-library-only contract

`port/adapter` MUST import only the standard library. It MUST define `Port`, `Capability`, `Descriptor{Ports, Name, Capabilities}` (with `Declares` and `Serves`), `Describer`, `Starter`, `Pinger`, `CapStart`, `CapReady`, and the accessors `Describe`, `StarterOf` and `PingerOf`. Those three accessors MUST be the only type assertions on `Describer`, `Starter` and `Pinger` in the code base (design §D1, §D3).

#### Scenario: an undeclared value

- GIVEN a value that implements none of the optional interfaces
- WHEN `Describe`, `StarterOf` and `PingerOf` are called on it
- THEN each returns its zero value and `false`, and nothing panics

### Requirement: contract packages keep no dependency on `port/adapter`

`port/publishing`, `persistence`, `offsetstore`, `tenancy` and `encryption` MUST declare their port names as **untyped** string constants, so none of them imports `port/adapter`. If `port/publishing` imported `port/adapter` while the two sit in different modules, the ego-arch-006 S2 contracts module would form a module cycle (design §D3).

#### Scenario: moving `port/publishing` stays cycle-free

- GIVEN the five contract packages after this spec
- WHEN their imports are listed
- THEN none of them imports `port/adapter`

### Requirement: adapter modules must not import the composition root

archcheck MUST report the rule `external-adapter-no-composition` when a package in `ExternalAdapterLayer` (nested modules under `publisher/`) imports `<root>/compose` or any path under it. There is no exemption for `main` packages or examples inside an adapter module. The rule fails closed through the existing zero-match check (design §D7). Each publisher's closure test MUST also reject `<root>/compose` and its subpackages in `go list -deps -test ./...`.

#### Scenario: the exploration spike becomes a failing graph

- GIVEN a graph in which `publisher/kafka` imports `compose/goakt`
- WHEN archcheck evaluates it
- THEN it reports `external-adapter-no-composition` for that edge

#### Scenario: legitimate importers stay allowed

- GIVEN a root-module `main` package that imports `compose`
- WHEN archcheck evaluates it
- THEN no rule from this spec fires

## Tasks (5)

1. **`port/adapter`** — types, accessors, package doc naming the one-assertion rule; unit tests written RED first (accessors on implementing and non-implementing values, `Declares`/`Serves`, typed-nil input). *(SPI-1)*
2. **Port-name constants and architecture tests** — untyped constants in `port/publishing/port.go`, `persistence/port.go`, `offsetstore/port.go`, `tenancy/port.go`, `encryption/port.go` (the resolver and encryptor slots need them for V8a in spec 3); a `go list -deps` architecture test for `port/adapter` with an empty non-stdlib allowlist; a test that none of the five contract packages imports `port/adapter`. *(SPI-1)*
3. **archcheck rule** — RED graph tests (adapter imports `compose`, `compose/goakt`, `compose/internal/lifecycle`, where `no-cross-module-internal` also fires; plus a root `main` importing `compose` that stays allowed), then `external-adapter-no-composition` in `DefaultRules`. *(SPI-2)*
4. **Closure tests** — the four `publisher/*/closure_test.go` reject `<root>/compose` and its subpackages. *(SPI-2)*
5. **Documentation** — `docs/ci.md` rule table and the adapter-roots note under "Adding a layer"; amend `openspec/changes/ego-arch-001/design.md:118`: the `compose` part of "adapters import only contracts and `egopb`" is now enforced by archcheck, and the rest stays enforced in review. *(SPI-2)*

## Checks

- `go test ./port/... ./persistence/... ./offsetstore/... ./tenancy/... ./encryption/...`
- `go test ./internal/cmd/archcheck/...`
- `go run ./internal/cmd/archcheck` on the slice head: 0 violations and no new baseline entry
- `scripts/ci/verify-module.sh` for each of the four publishers (closure tests)
- apidiff on every touched package: additions only

## File ownership

| Slice | Files |
|---|---|
| SPI-1 | `port/adapter/**` (new); `port/publishing/port.go`, `persistence/port.go`, `offsetstore/port.go`, `tenancy/port.go`, `encryption/port.go` (new) |
| SPI-2 | `internal/cmd/archcheck/rules/rules.go`, `internal/cmd/archcheck/rules/*_test.go`, `docs/ci.md`, `publisher/*/closure_test.go` (all four; no later spec edits them), `openspec/changes/ego-arch-001/design.md` (one sentence in §3) |

## Dependencies

- None. SPI-1 and SPI-2 are independent of each other and of every open issue; SPI-2 can land first.
- The ego-arch-006 D1 module-path migration renames the new paths mechanically; do not run it in parallel on the same files.

## Next in the chain

[Spec 2 — conformance suites and two adopters](../adapter-conformance/spec.md). It needs `port/adapter` from this spec.
