# Spec 2 of 3 — Conformance suites and two adopters (EGO-ARCH-004)

| Field | Value |
|---|---|
| Change | `ego-arch-004` (umbrella: [`proposal.md`](../../proposal.md), [`design.md`](../../design.md)) |
| Chain position | **Spec 2 of 3.** Previous: [Spec 1 — SPI package and boundary](../adapter-spi-boundary/spec.md). Next: [Spec 3 — composition uses the SPI](../adapter-composition/spec.md) |
| Slices (pull requests) | SPI-3 (suites), SPI-4 (adopters) |
| Tracker | [`#106`](https://github.com/getsyntegrity/ego/issues/106) |
| Baseline | `main` at `f2b5130148086d95a4d6362971b85e800b0ce155` |
| Decisions applied | design §D4, §D8; maintainer decisions O2 and O5 (2026-09-27, design §9) |

## Purpose

#106 asks for a minimal, reusable conformance suite and for at least two adapter types to use the model. This spec builds two standard-library-only test packages and adopts them in one publisher and one store:

- `port/adapter/adaptertest` checks that a descriptor is honest and that the adapter follows the lifecycle rules L1–L4;
- `port/publishing/publishingtest` checks publisher behavior.

It also fixes the one lifecycle rule the chosen publisher breaks today: its `Close` is not idempotent (exploration §2).

## Requirements

### Requirement: the suites are standard-library-only

`adaptertest` MUST import only the standard library and `port/adapter`. `publishingtest` MUST import only the standard library, `port/publishing` and `egopb`, and MUST NOT import `port/adapter`, so it can move with `port/publishing` into the ego-arch-006 contracts module. A publisher that runs the suites MUST keep GoAkt and the root package out of `go list -deps -test ./...` (design §D8).

#### Scenario: the architecture tests hold the line

- GIVEN `adaptertest` and `publishingtest`
- WHEN their architecture tests run `go list -deps` on them
- THEN every non-standard-library dependency is on the package's allowlist (`port/adapter` for `adaptertest`; `port/publishing`, `egopb` and the protobuf runtime for `publishingtest`)

#### Scenario: an adopting publisher keeps its clean test closure

- GIVEN `publisher/websocket` after it runs both suites
- WHEN its closure test runs `go list -deps -test ./...`
- THEN no `github.com/tochemey/goakt/v4` package and not the root package appear

### Requirement: a skip means "unreachable", nothing else

A check MUST be skipped only when the target's factory returns an error matching `adaptertest.ErrUnreachable`. Any other factory error fails. A check that has no hook, or that cannot apply because the adapter connects in its constructor, MUST be reported as "not exercised", never as passed (design §D8 "Hooks").

#### Scenario: a lying descriptor fails

- GIVEN a fake adapter that declares `CapStart` but has no `Start` method
- WHEN `adaptertest` runs AT-1 against it
- THEN AT-1 fails and names `CapStart`

#### Scenario: a capability without a check fails

- GIVEN a fake adapter that declares a capability other than `CapStart` or `CapReady`, with no entry in `Target.Capabilities`
- WHEN AT-1 runs
- THEN AT-1 fails with "no check supplied"

### Requirement: the two adopters run unskipped in CI

`publisher/websocket` (against an `httptest` server) and the `testkit` in-memory stores MUST run the suites in CI with no conformance subtest skipped. The websocket publisher's `Close` MUST return nil when called a second time.

#### Scenario: double close

- GIVEN a websocket events publisher connected to an `httptest` server
- WHEN `Close` is called twice
- THEN both calls return nil

## Tasks (5)

1. **`adaptertest`** — AT-1…AT-5 with `Target` (`Port`, `Ownership`, `New`, `FailStart`, `Stall`, `Capabilities`), `ErrUnreachable` as the only skip, "not exercised" reporting; self-checks against deliberately broken fakes (a `Close` that is not idempotent, a `Close` that ignores the deadline, a lying descriptor); an architecture test that keeps the package standard-library-only. *(SPI-3)*
2. **`publishingtest`** — PT-1…PT-3; self-check against a publisher that keeps publishing after `Close`; architecture test (standard library, `port/publishing` and `egopb` only; no `port/adapter`). *(SPI-3)*
3. **Idempotent websocket `Close`** — RED double-close test against `httptest`, then guard `websocket.go:79-82` and `:144-147` so a second call returns nil. *(SPI-4)*
4. **Websocket adopts the model** — `Describe` on both publishers; their tests run `adaptertest` with `Stall` (AT-2 and the failed-acquire case of AT-3 are reported "not exercised": websocket dials in its constructor, which O5 keeps) and `publishingtest`; `publisher_contract_test.go` folds into PT-1; record that `go list -deps -test ./...` still has no GoAkt and no root package. *(SPI-4)*
5. **`testkit` stores adopt the model** — `Describe` on `EventStore`, `DurableStore` and `OffsetStore`, with no declared capabilities (`CapReady` is implied by the store ports); run `adaptertest` as `Borrowed` next to the existing `persistence/conformance`; AT-4 is reported "not exercised" because there is no backend to stall. *(SPI-4)*

## Checks

- `go test ./port/...` (suites and their self-checks)
- `scripts/ci/verify-module.sh publisher/websocket`, including its closure test
- the root lane for `testkit`
- `go test -v -run Conformance` for both adopters: no `SKIP` line for a conformance subtest
- `go run ./internal/cmd/archcheck`; apidiff on touched packages: additions only

## File ownership

| Slice | Files |
|---|---|
| SPI-3 | `port/adapter/adaptertest/**`, `port/publishing/publishingtest/**` (new) |
| SPI-4 | `publisher/websocket/**` **except** `closure_test.go` (spec 1 owns it); `testkit/eventstore.go`, `testkit/durablestore.go`, `testkit/offsetstore.go` and their tests |

## Dependencies

- **Spec 1** (SPI-1) must be merged: both suites and every `Describe` use `port/adapter`.
- **ego-arch-006 S2/S3 (#102).** At the baseline, publishers still require the root module, so SPI-4 works as written. Under maintainer decision O2, `port/adapter` and `adaptertest` go into the ego-arch-006 contracts module in S2 (design §9 records that this amends ego-arch-006's approved D7 (i)). If S3 lands before S2 carries them, the websocket half of SPI-4 waits for it; the `testkit` half does not.

## Next in the chain

[Spec 3 — composition uses the SPI](../adapter-composition/spec.md). Its guide uses the websocket adopter from this spec as the worked example.
