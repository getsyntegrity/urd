# Proposal — Canonical package and module topology (EGO-ARCH-001)

| Field | Value |
|---|---|
| Change | `ego-arch-001` |
| Date | 2026-09-23 |
| Phase | `sdd-propose` — proposed (architecture decision record; ADR) |
| Tracker | [`#104`](https://github.com/getsyntegrity/ego/issues/104), parent [`#10`](https://github.com/getsyntegrity/ego/issues/10) |
| Baseline | `main` at `a4edded` |
| Evidence | [`exploration.md`](./exploration.md) (compiler graph, CI selection, timings); [`design.md`](./design.md) (rules, diagram, map, slices) |
| Blocked by (for new `go.mod` and publisher migration only) | [`#111`](https://github.com/getsyntegrity/ego/issues/111) CI-001/002, multi-module verification |
| Related owners | [`#103`](https://github.com/getsyntegrity/ego/issues/103) neutral behavior contracts, [`#11`](https://github.com/getsyntegrity/ego/issues/11) runtime SPI and GoAkt adapter, [`#112`](https://github.com/getsyntegrity/ego/issues/112) root test latency, [`#38`](https://github.com/getsyntegrity/ego/issues/38) CI, [`#105`](https://github.com/getsyntegrity/ego/issues/105) assembly, [`#106`](https://github.com/getsyntegrity/ego/issues/106) adapter SPI |

## Why now

Ego is about to move code: #103 wants neutral behavior contracts, #11 wants a runtime service provider interface (SPI) with GoAkt behind an adapter, and #111 wants CI that verifies every Go module. Each of those needs the same answer first: which packages are contracts, which are adapters, which way dependencies may point, and when a boundary deserves its own `go.mod`. Without that answer, each issue would draw its own boundary and the refactor would drift.

The exploration measured the current state with the Go compiler rather than a source scan. Three facts shape this proposal:

1. **The package graph is already clean enough to build on.** The root module has 32 packages, 52 production import edges and no cycles. Most contract packages (`persistence`, `offsetstore`, `command`, `tenancy`, `projection`, `eventstream`, `encryption`, `eventadapter`) already compile without GoAkt. The coupling is concentrated in the root package `ego` (15 of its 25 production files import GoAkt) and in `internal/extensions`.
2. **Adapters pay for coupling they do not use.** The four publisher modules use only two interfaces and one error from root `publisher.go`, which imports nothing but `egopb`. Yet each publisher compiles 15 root packages and 45 GoAkt packages, because those symbols live in the root package.
3. **The existing module boundaries are not verified.** All six nested modules require root `v4.4.3`, a version that was never published; they build only through local `replace` directives. CI does not build, vet or lint them: a change confined to `publisher/kafka` makes the selector return `ModeNone` and the check pass.

## Intent

Adopt a normative topology for Ego: neutral contracts at the center, GoAkt and other technologies as adapters around them, dependency direction enforced by a check derived from `go list`, and new Go modules only when a measured criterion holds. The decision prefers **package boundaries first**: extract neutral packages inside the root module, keep compatibility aliases at the old paths, and promote a boundary to a module only when a package cannot deliver the benefit.

This is an architecture decision. It moves no production code and does not implement the CI, runtime or test-latency work owned by other issues.

## In scope (decisions this proposal closes)

- **Layer classification** of every current package (contract, schema, adapter, application, test support, tooling), recorded in the source-to-destination map in `design.md`.
- **Dependency direction** as MUST / MUST NOT rules: contracts never import GoAkt, the root package or `internal/extensions`; adapters depend on contracts, not on the runtime they do not need.
- **Canonical location for contracts that live in the root package today.** The first one is the publisher contract, proposed as package `port/publishing` inside the root module with aliases left in package `ego`.
- **Criterion for a separate `go.mod`**, stated so that it can be checked, not argued.
- **Ordered first slices** that can land without a big-bang migration.
- **Impact on CI and test selection**, and the dependency on #111 that follows from it.
- **Proposed versioning policy** for the six nested modules, separating integrated verification in the monorepo from verification against a published version.

## Out of scope (MUST NOT in this change)

- Moving production code, including the `port/publishing` extraction itself. This change designs slice S1; it does not implement it.
- Multi-module CI, satellite selection and release verification — owned by #111.
- Neutral behavior contracts (removing `extension.Dependency` from `EventSourcedBehavior`, `DurableStateBehavior`, `SagaBehavior`) — owned by #103.
- Runtime SPI, GoAkt adapter extraction and in-memory conformance — owned by #11.
- Root test latency (`pause.For`, cluster startup, retry timing) — owned by #112. The exploration's profiling is context only.
- Choosing a protobuf policy or a module path. Both stay explicit open decisions (see below).
- Creating empty modules or duplicate APIs to satisfy a theoretical topology.

## Approach

The recommended approach is **extract neutral packages first, then promote proven boundaries to modules** (option 2 in `exploration.md`). Two alternatives were considered and rejected:

- **Create many modules now** was rejected because CI cannot verify the modules that already exist (#111), because a module boundary does not remove GoAkt from the behavior API, and because it would multiply unpublished `replace` chains.
- **Remain one module permanently** was kept as a measured fallback, not the default: a package extraction isolates compilation, but it cannot isolate a consumer's dependency list, toolchain or release cadence. If no boundary ever meets the module criterion, the topology stays a single module and the decision still holds.

A throwaway prototype supports the package-first choice. It moved the publisher contract into `port/publishing`, left aliases in the root package and pointed `publisher/kafka` at the new package. This is **prototype evidence, not an implemented slice**:

| Kafka cold build, fresh `GOCACHE` | Wall time | User CPU | Max RSS | Packages in `go list -deps` |
|---|---|---|---|---|
| Baseline `a4edded` (two rounds) | 15.89 s / 22.24 s | 128 s / 186 s | ~1.03 GB | 592 |
| Prototype (two rounds) | 6.85 s / 9.13 s | 55 s / 76 s | ~320 MB | 290 |

Kafka then compiled only two root packages (`egopb`, `port/publishing`) and no GoAkt package; root `go build ./...` and `go vet ./...` (tests included) passed.

Limits of that evidence: Kafka only; two rounds on a host with variable load (load average 3 to 11); max RSS of one process, not the process tree; local Go 1.26.6 while CI uses 1.27.0. Alias compatibility was checked only by compiling the root module and Kafka, not with an API-compatibility tool, not for `nats`, `pulsar` or `websocket`, and not for external implementers. The nested `go.mod` still requires the root module, so Kafka's **module requirement list** (GoAkt, Olric and the rest) is unchanged: package extraction isolates compilation, not dependency lists.

## Dependencies and sequencing

- **#111 blocks** any new `go.mod` and the migration of the publisher modules to `port/publishing`, because a publisher-only change is not verified by CI today. Designing and extracting `port/publishing` **inside the root module** is not blocked, but the root lane alone does not verify it: the publishers, `benchmark` and `example/cluster` consume the root API from nested modules. Merging that extraction (S1a) requires the manual nested-consumer build and vet check defined in `design.md` section 5 until #111 automates it. The publisher migration lands only after #111 makes a Kafka-only change run Kafka build, vet and lint.
- **Verification against a published tag is a release condition.** It is not a requirement to accept this ADR, nor to extract a package inside the root module. It becomes mandatory when a nested module is released (see `design.md`, versioning policy).
- **#103 and #11** consume this topology; this change does not lock their signatures.
- **#112** is independent; nothing here depends on test latency improvements.

## Affected public consumer surfaces

This change only decides; the surfaces below are affected when slice S1 is implemented, and are listed now because `openspec/config.yaml` requires naming them:

- `ego.EventPublisher`, `ego.StatePublisher`, `ego.ErrPublisherNotStarted` (root `publisher.go`), which become aliases of `port/publishing` symbols.
- `Engine.AddEventPublishers(...EventPublisher)` and `Engine.AddStatePublishers(...StatePublisher)` (`engine.go:1207`, `engine.go:1248`), whose signatures keep the `ego` names.
- Generated mocks `mocks/ego.EventPublisher` and `mocks/ego.StatePublisher`.
- The four publisher modules (`publisher/kafka`, `publisher/nats`, `publisher/pulsar`, `publisher/websocket`), and any external implementation or type assertion against `ego.EventPublisher` / `ego.StatePublisher`.

Later slices (#103, #11) will affect `EventSourcedBehavior`, `DurableStateBehavior`, `SagaBehavior`, `NewEngine(goakt.ActorSystem, ...)`, `Engine.ActorSystem()`, `Config.GoaktOptions()` and `ClusterKinds()`; those changes need their own compatibility plans.

## Rollback

This change is documentation only; rolling it back means reverting the files in `openspec/changes/ego-arch-001/`.

For slice S1 when it is implemented: Go type aliases (`type EventPublisher = publishing.EventPublisher`) and a variable that references the same error value keep type identity and `errors.Is` behavior, so callers that keep importing package `ego` compile unchanged. The aliases protect only that direction. A consumer that starts importing `port/publishing` directly would break if the package were deleted, so the rollback depends on whether S1 has been released:

- **Before a release exposes `port/publishing`**, rollback means reverting S1: move the declarations back into `publisher.go` and delete `port/publishing`. No published consumer can depend on the new path yet.
- **After a release exposes it**, `port/publishing` is public API and MUST be kept for the rest of the v4 line. Rollback is then limited to reverting callers inside this repository (for example, pointing the publishers back at package `ego`). The package itself stays, and removing it would require a new major version.

S1 is not considered implemented until alias compatibility is verified (see `design.md`).

## Risks

- **Aliases may hide an incompatibility the prototype did not exercise**, such as reflection on type names, generated mocks or documentation links. Mitigation: an API-compatibility check and a build of all four publishers and the mocks before S1 is declared done.
- **Package extraction may be mistaken for module isolation.** Mitigation: the module criterion in `design.md` states that dependency-list isolation needs a module, and S1 does not claim it.
- **Local `replace` directives can mask release failures.** Mitigation: the versioning policy separates integrated from published verification; release verification is #111's acceptance item.
- **Protobuf policy may force a later reshuffle** of `persistence`, `offsetstore` and `port/publishing`, which import `egopb`. Mitigation: those contracts are classified with `egopb` as an explicit schema dependency, not as protobuf-free.

## Open decisions (explicitly not closed here)

1. **Protobuf policy.** Whether `egopb` messages and `proto.Message` (`Command`, `Event`, `State` in `behavior.go:34-44`) remain part of the supported public contract. Removing GoAkt does not decide this.
2. **Module path.** Whether to keep `github.com/pablogore/ego/v4` while the repository lives at `getsyntegrity/ego` and resolves by redirect, or migrate the path.
3. **First published root version.** Whether to publish `v4.4.3` (what the nested modules already require) or another version, and when.
4. **Deprecation window** for aliases left at old paths after contracts move.

## Success criteria (acceptance of this ADR, mapped to #104)

- [ ] A normative document with a diagram and MUST / MUST NOT rules exists (`design.md`).
- [ ] The current graph inventory is recorded with reproducible commands (`exploration.md`, `design.md` evidence section).
- [ ] A source-to-destination map covers every affected package (`design.md`).
- [ ] The first slices are identified and ordered, with no big-bang step (`design.md`).
- [ ] Package versus module boundaries are defined with a checkable criterion (`design.md`).
- [ ] The impact on CI and test selection is documented, including the #111 dependency (`design.md`).
- [ ] No production code moves in this change.
