# Proposal — Adapter SPI, capabilities and lifecycle (EGO-ARCH-004)

| Field | Value |
|---|---|
| Change | `ego-arch-004` |
| Date | 2026-09-27 |
| Phase | `sdd-propose`: proposed (architecture decision record; ADR) |
| Tracker | [`#106`](https://github.com/getsyntegrity/ego/issues/106), parent [`#10`](https://github.com/getsyntegrity/ego/issues/10) |
| Baseline | `main` at `f2b5130148086d95a4d6362971b85e800b0ce155` |
| Evidence | [`exploration.md`](./exploration.md) (adapter inventory, assertion sites, what #105 owns, composition-root spike); [`design.md`](./design.md) (decisions D1–D9, conformance suite, guide outline, chain of specs, maintainer decisions); [`specs/`](./specs/) (the three chained specs) |
| Builds on | ego-arch-001 §2, §3, §10; ego-arch-003 §D2, §D5–§D8 (#105); ego-arch-006 §3 D1, D7, D8 (#102) |
| Related | [`#11`](https://github.com/getsyntegrity/ego/issues/11) runtime SPI; [`#24`](https://github.com/getsyntegrity/ego/issues/24) lifecycle; [`#37`](https://github.com/getsyntegrity/ego/issues/37) compatibility; [`#38`](https://github.com/getsyntegrity/ego/issues/38) CI; [`#31`](https://github.com/getsyntegrity/ego/issues/31) observability; [`#147`](https://github.com/getsyntegrity/ego/issues/147) `port/runtime`; [`#148`](https://github.com/getsyntegrity/ego/issues/148) in-memory runtime and `compose/inmem`; [`#146`](https://github.com/getsyntegrity/ego/issues/146) two-node test |
| Maintainer decisions | 2026-09-27: chained specs (A) and O1–O6 approved as recommended, O7 deferred (B); design §9. O1 answers the #106 follow-up comment |

## Problem

Ego's adapters (stores, publishers, the encryptor, the tenant resolver) each implement a contract, and since #105 one composition root wires them. What no adapter can do is say what it is or what optional things it supports, and nothing states what an adapter must guarantee when it starts, fails or stops. Four concrete consequences on `main`:

1. **Optional behavior is found by type assertions at the point of use.** `engine.go:883` asserts `tenancy.FixedTenantResolver` in the middle of a spawn; nobody can ask "can this resolver supply a fixed tenant?" before the first spawn needs it (exploration §3.1).
2. **Identity is missing or collides.** Stores, the encryptor and resolvers have none. Publishers have `ID()`, but it returns a type-wide constant (`"ego-kafka"`, `publisher/kafka/kafka.go:84-86`), so two Kafka events publishers cannot be attached to one engine (exploration §2).
3. **Rollback relies on guarantees nobody wrote down.** #105's sequencer never undoes the step that failed (`compose/internal/lifecycle/lifecycle.go:60-63`) and bounds all cleanup with one deadline, but no contract says an adapter must clean up after a failed start, tolerate a second close, or honor that deadline. The Kafka publisher discards the deadline it derives (`kafka.go:76`).
4. **An adapter can depend on the composition root and nothing catches it.** A spike added `compose/goakt` to `publisher/kafka`: archcheck reported 0 violations, and the publisher's production build went from 0 to 45 GoAkt packages (exploration §6). That is the gap the follow-up comment on #106 points at.

## What changes

This change is documentation only. It proposes:

- **A small SPI package, `port/adapter`.** It holds a `Descriptor` (the ports an adapter implements, since one type may serve several; a name for its implementation; and the optional capabilities it declares), the optional lifecycle interfaces `Starter` and `Pinger`, and the only functions that assert them: `adapter.Describe`, `StarterOf` and `PingerOf`. It imports only the standard library (design §D1).
- **Capabilities that are declared and implemented.** A capability is an optional interface in the contract package that owns the port, plus a string constant, plus one accessor that is the only place the interface is asserted (`tenancy.AsFixedTenantResolver` for the one capability core uses today). An adapter declares the capability in its descriptor and implements the interface. A new static rule, V8 in `compose.Spec.Validate`, rejects a declared adapter whose declaration and method set disagree in either direction, before anything starts. Capabilities the port already makes mandatory, such as `Ping` on stores, are implied and never declared. Undeclared adapters keep working exactly as today (design §D3, §D6).
- **An adapter lifecycle contract, L1–L6.** A failed `Start` releases what it acquired; `Close` is idempotent, safe before `Start` and after a failed `Start`, and bounded by the caller's deadline; `Ping` is the readiness probe. The composition root starts and probes owned publishers inside its existing step 4, so #105's step names, rollback and ownership table stay as they are (design §D4).
- **A rule that adapters must not import the composition root:** `external-adapter-no-composition` in archcheck, on the existing adapter layer, fail-closed, plus the same check in each publisher's closure test (design §D7). The maintainers approved this as decision O1 on 2026-09-27.
- **A reusable conformance suite,** standard-library-only so publishers can run it without the runtime: `port/adapter/adaptertest` for lifecycle and descriptor rules, `port/publishing/publishingtest` for publishers. A check is skipped only when the factory returns `adaptertest.ErrUnreachable`, and caller-supplied hooks drive the failure and stall cases. Stores keep `persistence/conformance` for data semantics (design §D8).
- **An idempotent websocket `Close`.** Today the websocket and NATS publishers already break the "idempotent Close" rule (exploration §2). SPI-4 fixes websocket; NATS goes to follow-up F-A.
- **Two adopters that prove the model without special cases in core:** `publisher/websocket` (testable in CI with an `httptest` server) and the `testkit` in-memory stores (design §D8, slice SPI-4).
- **An extension guide,** `docs/adapters.md`, outlined in design §4.

## Why this shape

- **Additive in v4.** Adding methods to existing port interfaces would break every implementation outside the repository, and ego-arch-001 §10 decided "no break inside v4". Optional interfaces plus a descriptor add capabilities without changing an interface. Every package's apidiff result is additions only (design §D9).
- **It reuses what #105 built.** Ownership (ego-arch-003 §D5), start and stop order (§D6, §D7), the cleanup context and `StartError` are unchanged. This change adds the adapter-level half that makes them hold.
- **A declaration and a method, not either one alone.** A method alone cannot be inspected before it is called; a declaration alone can lie. Keeping both, and checking that they agree in two places (V8 and the conformance suite), is what makes capabilities explicit and trustworthy (design §D3).
- **The composition-root rule follows the dependency direction ego-arch-001 §3 already states.** The composition root depends on adapters, not the reverse, and the measured cost of the reverse is the GoAkt runtime back in an adapter's build (exploration §6).

## In scope

Decisions D1–D9 in `design.md`; the conformance suite design; the guide outline; a chain of three specs under `specs/`, each with at most five tasks, its own checks, file ownership, dependencies and next link (design §5):

1. `specs/adapter-spi-boundary/spec.md`: SPI-1 + SPI-2 (`port/adapter`, the archcheck rule, the `ego-arch-001/design.md:118` amendment);
2. `specs/adapter-conformance/spec.md`: SPI-3 + SPI-4 (conformance suites, the websocket and `testkit` adopters);
3. `specs/adapter-composition/spec.md`: SPI-5 (V8, the step-4 helper, the tenancy accessor, `docs/adapters.md`).

Also in scope: named follow-ups F-A to F-F, and the mapping of every #106 acceptance criterion to a slice.

## Out of scope (MUST NOT in this change)

- Any production code. Implementation happens in slices SPI-1 to SPI-5, each its own pull request.
- The runtime SPI and runtime capability negotiation (#11, RUNTIME-001/006). This change offers `port/adapter` as a shared vocabulary only.
- Drain and flush policy, command admission, timeouts and restart semantics (#24, LIFE-003/004/007/008). No capability name is reserved without semantics (design §7).
- Adapter and SPI versioning or compatibility ranges (#37, COMPAT-005). `Descriptor` is a struct, so #37 can add a field additively.
- An observability contract for telemetry and logging (#31).
- Dynamic loading of plugins, a remote registry, or implementing every adapter on the roadmap (#106 "fuera de alcance").
- Editing ego-arch-006's files, the module path migration (D1), or the release pipeline (F4). The amendment of ego-arch-006 D7 (i) that decision O2 requires is recorded in design §9 and applied by the owner of ego-arch-006 S2.

## Dependencies

- **None for SPI-1, SPI-2 and SPI-3.** They add packages, an archcheck rule and tests.
- **ego-arch-006 S3 (#102).** At the baseline, publishers still require the root module, so the publisher adopter in SPI-4 works as designed. If S3 lands first, the publisher half of SPI-4 waits until ego-arch-006 S2 carries `port/adapter` into the contracts module, as maintainer decision O2 requires (an amendment of ego-arch-006's approved D7 (i), recorded in design §9).
- **ego-arch-006 D1 (path migration).** The new packages are renamed by the same mechanical pull request as everything else; no slice needs special handling.
- **#24.** No slice waits for it; design §7 records every interaction.

## Affected public surfaces

All additive (design §D9):

- New packages `port/adapter`, `port/adapter/adaptertest`, `port/publishing/publishingtest`.
- New untyped constants in `port/publishing`, `persistence`, `offsetstore`, `tenancy` and `encryption`, plus `tenancy.AsFixedTenantResolver` and `tenancy.FixedTenantOf`. No existing contract package imports `port/adapter`.
- New `Describe` methods on `testkit` stores and on `publisher/websocket` types; websocket `Close` becomes idempotent (a second call returns nil instead of the connection error).
- Behavior: `compose.Spec.Validate` rule V8 applies only to adapters that declare a descriptor; `compose/goakt` step 4 starts and probes publishers that implement `Starter`/`Pinger`. No existing signature changes and nothing is deprecated.

## Rollback

This change is documentation; reverting it removes `openspec/changes/ego-arch-004/`. Each slice is additive and can be reverted on its own until a release exposes the new packages. After that they are public v4 API and may not be deleted before the #124 major, the same rule ego-arch-001 §5 applies to `port/publishing`.

## Risks

- **A descriptor that lies.** Mitigation: V8 at composition time and AT-1 in the adapter's own tests both compare the declaration with the method set.
- **Capability sprawl.** Every issue could invent capability names. Mitigation: only capabilities with a caller are defined (three in this change), each lives in the contract that owns it, and a new one needs semantics and a test (design §D3).
- **Placement after ego-arch-006 S3.** Publishers may lose access to `port/adapter`. Mitigation: publishing-side constants and `publishingtest` avoid importing it, and decision O2 places `port/adapter` in the contracts module.
- **The composition-root rule misses test files.** archcheck does not read `_test.go`. Mitigation: SPI-2 extends each publisher's closure test to reject `compose` too.

## Maintainer decisions (2026-09-27; design §9)

- **A — Chained specs.** No spec exceeds 4–5 atomic tasks. This directory stays the umbrella, and the work is a chain of three specs: spec 1 = SPI-1 + SPI-2, spec 2 = SPI-3 + SPI-4, spec 3 = SPI-5 (see In scope).
- **B — O1–O6 approved as recommended; O7 deferred.**
  - **O1:** adapter modules may not import `compose/...`, enforced by `external-adapter-no-composition`.
  - **O2:** `port/adapter` and `adaptertest` go into the ego-arch-006 contracts module in S2. This amends ego-arch-006's approved D7 (i); the amendment is recorded in design §9.
  - **O3:** each publisher `Config` gets an optional `ID`, defaulting to today's constant (follow-up F-C).
  - **O4:** `compose/internal/lifecycle` stays internal.
  - **O5:** existing publishers keep dialing in their constructor; new adapters do their I/O in `Start`.
  - **O6:** the directory convention for non-publisher adapter modules is decided when the first one arrives.
  - **O7:** making `Describe` mandatory at the #124 major is deferred to #124 and #37.

## Success criteria for this docs change

- [ ] The current state of identity, lifecycle and capability detection is inventoried with verified `file:line` evidence (`exploration.md`).
- [ ] The SPI, the capability model, the lifecycle contract and the compatibility plan are stated as proposed decisions with rejected alternatives (`design.md` §3, §10).
- [ ] The composition-root dependency question has a decision (O1) and a fully specified rule (name, layer, fail-closed behavior, interactions).
- [ ] The conformance suite design shows how a publisher and a store use it, and how nested modules run it without the root runtime.
- [ ] Every #106 acceptance criterion maps to a slice and a check (`design.md` §8).
- [ ] The maintainer decisions of 2026-09-27 are recorded with the options that were considered (design §9), and the work is split into chained specs of at most five tasks each (`specs/`).
- [ ] No production code changes.
