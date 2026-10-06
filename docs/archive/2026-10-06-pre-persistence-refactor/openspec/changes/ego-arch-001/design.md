# Design — Canonical package and module topology (EGO-ARCH-001)

| Field | Value |
|---|---|
| Change | `ego-arch-001` |
| Date | 2026-09-23 |
| Phase | `sdd-design` |
| Tracker | [`#104`](https://github.com/getsyntegrity/ego/issues/104) |
| Inputs | [`proposal.md`](./proposal.md), [`exploration.md`](./exploration.md) |
| Baseline | `main` at `a4edded48f01b5430f4555effd99bd5e06c62702` |

## 1. Summary

Ego's contracts should sit at the center of the dependency graph and know nothing about GoAkt, the actor runtime Ego runs on today. Adapters — the GoAkt runtime, publishers, stores — point inward at those contracts. Most contract packages already satisfy this; the exceptions are contracts that still live in the root package `ego`, which also contains the GoAkt engine and actors. This design names every package's layer, states the dependency rules as MUST / MUST NOT, orders the first extraction slices, and defines when a boundary becomes a separate Go module.

Vocabulary used below:

- **Contract** — an exported interface, error or value type that adapters implement or consume. It must be importable without pulling in a runtime.
- **Schema** — generated protobuf message types (`egopb`). Contracts may depend on it until the protobuf policy is decided.
- **Adapter** — code that binds a contract to a technology (GoAkt, Kafka, NATS, a database).
- **Application** — a service that orchestrates contracts and runtime (for example `migration`).
- **Composition root** — the code that constructs the runtime and the concrete adapters (stores, publishers, telemetry) and wires them into the contracts. Section 4.1 identifies where that happens today.
- **Compatibility alias** — a Go type alias (`type X = other.X`) or variable (`var E = other.E`) left at the old import path so existing callers keep compiling.

## 2. Target topology

The diagram shows compiler-resolved production imports at `a4edded`, updated for slice S1: the `port/publishing` package (S1a) and the publishers' switch to it (S1b). Solid arrows are first-party imports. Dotted arrows are direct imports of the protobuf runtime library (`google.golang.org/protobuf`) by packages that do **not** import `egopb`. Exactly three contracts import `egopb`: `persistence`, `offsetstore` and `port/publishing`.

```mermaid
flowchart TB
  subgraph contracts["Contracts: no GoAkt in their dependency closure"]
    tenancy["tenancy"]
    command["command"]
    persistence["persistence"]
    offsetstore["offsetstore"]
    publishing["port/publishing<br/>(S1a done, #116; S1b done; ego/publisher.go keeps compatibility aliases)"]
    projection["projection"]
    eventstream["eventstream"]
    encryption["encryption"]
    eventadapter["eventadapter"]
  end

  egopb[["egopb<br/>generated schema<br/>protobuf policy: OPEN"]]
  pbrt(["google.golang.org/protobuf"])
  utils["internal/queue, internal/syncmap"]
  logging["internal/logging<br/>(S4-1 done, #147)"]

  command --> tenancy
  persistence --> tenancy
  persistence --> egopb
  offsetstore --> egopb
  publishing --> egopb
  eventstream --> utils
  command -.-> pbrt
  eventadapter -.-> pbrt
  projection -.-> pbrt
  egopb -.-> pbrt

  subgraph runtime["GoAkt runtime adapter: root module package ego today"]
    ego["ego<br/>engine, actors, options, telemetry<br/>+ compatibility aliases"]
    ext["internal/extensions"]
    rutil["internal/runner, internal/syncmap, internal/ticker"]
  end
  goakt(["github.com/tochemey/goakt/v4"])

  ego --> contracts
  ego --> egopb
  ego --> ext
  ego --> rutil
  ego --> logging
  ext --> contracts
  ego --> goakt
  ext --> goakt

  pubs["publisher/kafka, nats, pulsar, websocket<br/>(nested modules)"]
  pubs -- "since S1b" --> publishing
  pubs --> egopb

  migration["migration<br/>(application)"] --> persistence
  migration --> tenancy
  migration --> egopb
  migration -- "since S4-1" --> logging
```

Notes on the diagram:

- `ext --> contracts` summarizes six real edges: `internal/extensions` imports `encryption`, `eventadapter`, `eventstream`, `offsetstore`, `persistence` and `projection`.
- `ego --> contracts` summarizes the root package's imports of `command`, `encryption`, `eventadapter`, `eventstream`, `offsetstore`, `persistence`, `projection` and `tenancy`. Since S1a (#116), it also imports `port/publishing` to declare the compatibility aliases (`publisher.go`).
- Test support (`testkit`, `persistence/conformance`, `mocks/*`, `test/data/testpb`) and examples are omitted for readability; their edges are listed in section 4.
- Since S1b, the publishers import `port/publishing` and `egopb` instead of package `ego`. S1b landed after #111 made CI build and verify nested modules (section 5), and it removed the four publisher-importing-`ego` edges from `internal/cmd/archcheck/baseline.go`, so the `external-adapter-no-runtime` rule now holds with no exception.
- Since S4-1 (#147), `migration` no longer imports package `ego` at all: its only production use of `ego` was `ego.ResolveLogger`/`ego.DefaultLogger`, resolving the kit-logger logger it falls back to when none is configured. That logic now lives in `internal/logging`, a small runtime-free internal package that imports only kit-logger and `reflect`; `ego.DefaultLogger` and `ego.ResolveLogger` keep their exact signature and delegate to it, so the public API and the logger identity (`ego.DefaultLogger()` still returns the same instance) are unchanged. This removed the last entry from `internal/cmd/archcheck/baseline.go` (`migration -> ego`, `application-no-runtime`); the baseline is empty and archcheck reports `0 baselined, 0 violation(s), 0 stale entries`.

**Canonical location for a new contract.** A new top-level contract package goes under `port/`, the way `port/publishing` does for S1; a subpackage of an existing contract (for example something added under `persistence/`) stays under that contract's own root instead. The eight contracts that already exist at the repository root (`tenancy`, `command`, `persistence`, `offsetstore`, `projection`, `eventstream`, `encryption`, `eventadapter`) are not moved under `port/` in v4 — moving them would be a breaking import-path change for every consumer, and section 4's source-to-destination map already gives every one of them a "Stay" destination. `port/` is where a contract is *born* from now on, not a relocation target for the ones that already have a stable path.

## 3. Dependency rules

These rules apply to the root module and, where named, to nested modules. A rule check (slice S2, `internal/cmd/archcheck`, done — #117) enforces them, but not by walking a full transitive dependency closure. It checks direct production import edges: the root module's own graph from `go list -e -json ./...`, and each nested module's imports parsed directly with `go/parser` (imports only, no build and no network — see docs/ci.md, "Architecture boundary check", for the tool's exact mechanics and its rule table). Direct edges are enough for the rules below because contracts use a closed allowlist, and every package that allowlist admits is itself already runtime-free; a transitive path from a contract to GoAkt would therefore need a new *direct* edge first, and that edge is exactly what the check sees. This is also how "MUST NOT import any package whose dependency closure contains GoAkt" is enforced for contracts, without literally computing a closure. One thing it does not cover: S1 criterion 4 below (that a publisher's transitive `go list -deps` output excludes GoAkt after S1b) is a transitive, nested-module check. It was observed on the S1b pull request; archcheck still does not re-run it (it checks direct edges, not closures), but #122 added `TestUnitTestClosureExcludesRuntimeAndRoot` to each publisher module, a normal test that shells out to `go list -deps -test ./...` and fails if GoAkt or the root package reappears, so it now runs on every `go test ./...` in #111's module job (`scripts/ci/verify-module.sh`) — a stricter version of criterion 4 that also covers the test closure, not just production.

**Contracts** (`tenancy`, `command`, `persistence`, `offsetstore`, `projection`, `eventstream`, `encryption`, `eventadapter`, and every package under `port/`, including `port/publishing`, `port/behavior` and, since S4 (#147), the runtime SPI `port/runtime`):

- MUST depend only on the standard library, other contract packages, `egopb`, the protobuf runtime library, root-module `internal/` utilities that carry no runtime (`internal/queue`, `internal/syncmap`), and small runtime-neutral libraries named in this document. Today that list is `github.com/google/uuid` and `go.uber.org/atomic`, both imported by `eventstream`. Adding a third-party dependency to a contract requires updating this list in review.
- MUST NOT import `github.com/tochemey/goakt/v4` or any package whose dependency closure contains it.
- MUST NOT import the root package `ego`, `internal/extensions`, `migration`, or any test-support package (`testkit`, `mocks/*`, `test/*`, `persistence/conformance`) outside `_test.go` files.
- MUST NOT import OpenTelemetry, broker clients or database drivers. Instrumentation belongs in adapters.
- MUST NOT import the standard-library transport or database packages `net/http`, `net/rpc`, `database/sql`, or anything under them (matched by path segment, so `net/http/httptest` is forbidden but a hypothetical `net/httpx` would not be). gRPC and other third-party transports are already excluded by the closed allowlist above; this rule closes the one gap the allowlist leaves open, since the rest of the standard library is allowed. This restriction applies to direct imports only: unlike a third-party dependency, the standard library is not a closed set the allowlist can enumerate, so a transitive path to one of these packages is possible and is not enforced — for example `expvar` imports `net/http` internally (`expvar.Handler()` returns `http.Handler`), so a contract that imported `expvar` would not be caught even though its dependency closure reaches `net/http`. `net` itself stays allowed: it also carries value types such as `net.IP` that a contract may legitimately need, and only its `net/http` and `net/rpc` subpackages are transport.
- An adapter MUST NOT live under a contract package's own path (for example, a Postgres store belongs in its own adapter location, not `persistence/postgres`): every package under a contract's path is itself a contract by `contract-allowlist`'s definition (section "Canonical location for a new contract" above) and must satisfy the same allowlist, which an adapter generally cannot.

**GoAkt runtime adapter** (package `ego` and `internal/extensions` in v4):

- MAY import contracts, `egopb`, GoAkt and OpenTelemetry.
- MUST keep a compatibility alias in package `ego` for every exported symbol that moves out of it during v4, with no rename.
- Implements the runtime SPI: since S4 (#147) `*ego.Engine` satisfies `port/runtime.Runtime` (a compile-time assertion in `engine_runtime.go`), and `compose/goakt.App.Runtime()` hands it out through that interface. Consumer code that needs a runtime SHOULD depend on `port/runtime`, not on `*ego.Engine`.

**Application** (`migration`):

- MUST NOT import the root package `ego`, `internal/extensions` or `github.com/tochemey/goakt/v4` (enforced by archcheck's `application-no-runtime` rule).
- MAY import contract packages, `egopb`, and root-module `internal/` utilities that carry no runtime, such as `internal/logging`.
- As of S4-1 (#147), `migration` satisfies this rule with no exception. It used to import package `ego` directly, and its only production use of that import was `ego.ResolveLogger` (the earlier text above claiming it "replays through `ego`'s runtime types" was stale — see #147's problem statement); that logger-resolution logic now lives in `internal/logging` (section 4), which `migration` imports instead. The `migration -> ego` baseline entry is gone; `internal/cmd/archcheck/baseline.go` is now empty.

**External adapters** (publisher modules, future store adapters):

- From this repository, an external adapter MAY import only contract packages and `egopb`. archcheck enforces the composition-root part of this: its `external-adapter-no-composition` rule (ego-arch-004 design §D7) rejects an import of `compose` or anything under it, in production code, and each publisher's closure test rejects it in tests. The rest is still enforced in review: `external-adapter-no-runtime` denies only the root package `ego` and GoAkt, so an import of, for example, `testkit` or `migration` would pass the check.
- Third-party libraries are allowed, except GoAkt (`github.com/tochemey/goakt/v4`), which `external-adapter-no-runtime` denies. A publisher genuinely needs a broker client (`github.com/segmentio/kafka-go` and similar).
- MUST NOT import package `ego` or GoAkt, unless the adapter genuinely needs the runtime. That exception is exercised only through an archcheck baseline entry with an owner, a justification and a removal criterion (`internal/cmd/archcheck/baseline.go`); the check has no other escape hatch.
- MUST NOT import package `ego` merely to reach a contract that exists in a contract package.

**Across module boundaries:**

- MUST NOT introduce a cycle between the root module and a nested module.
- MUST NOT import another module's `internal/` packages across a module boundary. Go's `internal` visibility is defined by import path rather than by module, so a nested module under `github.com/pablogore/ego/v4/...` may be able to import root `internal/` packages; this rule forbids it regardless, because it couples module versions invisibly. (Whether the toolchain accepts such an import was not tested in this spike.)

## 4. Source-to-destination map

Every current root-module package appears once. "Stay" means the package already satisfies its layer's rules and does not move.

| Package | Layer | Production first-party imports (`a4edded`) | Destination | Owner / when |
|---|---|---|---|---|
| `tenancy` | Contract | — | Stay | — |
| `command` | Contract | `tenancy` (+ protobuf runtime) | Stay | — |
| `persistence` | Contract | `egopb`, `tenancy` | Stay | Protobuf policy may revisit |
| `offsetstore` | Contract | `egopb` | Stay | Protobuf policy may revisit |
| `projection` | Contract | — (+ protobuf runtime) | Stay | Concrete runners stay in `ego` |
| `eventstream` | Contract | `internal/queue`, `internal/syncmap` | Stay | — |
| `encryption` | Contract | — | Stay | — |
| `eventadapter` | Contract | — (+ protobuf runtime) | Stay | — |
| `ego` (`publisher.go`) | Contract inside runtime package | `egopb` | `port/publishing` + aliases in `ego` | S1a done (#116); S1b done (publishers import `port/publishing`) |
| `ego` (`behavior.go`, `saga.go`) | Contract coupled to GoAkt (`extension.Dependency`) | — | `port/behavior`, proposed in `ego-arch-002-s3/design.md` §5.1 (name confirmed 2026-09-27, #123) | S3, #123 |
| `ego` (engine, actors, options, logger, telemetry, projection runner) | GoAkt runtime adapter | 13 first-party packages | Stay in `ego` for v4; implements `port/runtime.Runtime` since S4. Moving it into a GoAkt adapter package is #124's; the per-group inventory and destinations are `ego-runtime-001/design.md` §D10 | S4 done (#147); move: #124 |
| `port/runtime` | Contract (runtime SPI) | `command`, `eventstream`, `port/behavior`, `tenancy` | Stay (root module until ego-arch-006 F1) | S4-2/S4-3 done (#147) |
| `ego` (`option.go`: `Config`, `NewConfig`, `Config.GoaktOptions`; `engine.go`: `NewEngine`, `Start`, `Stop`, `AddEventPublishers`, `AddStatePublishers`) | Composition-root helpers, mixed into the runtime adapter | (same package as above) | Stay in `ego` for v4; destination defined by #105 (section 4.1) | #105 |
| `internal/extensions` | GoAkt runtime adapter | `encryption`, `eventadapter`, `eventstream`, `offsetstore`, `persistence`, `projection` | Stay; moves with the runtime adapter | #124 |
| `egopb` | Schema | — | Stay | Protobuf policy (open) |
| `migration` | Application | `egopb`, `internal/logging`, `persistence`, `tenancy` | Stay | S4-1 done (#147, `ego` import removed); revisit remaining scope after S4 |
| `internal/logging` | Utility (used by `ego` and `migration`) | — | Stay | S4-1, #147 |
| `internal/queue`, `internal/syncmap` | Utility (used by a contract) | — | Stay | Move only with the module that uses them |
| `internal/runner`, `internal/ticker`, `internal/pause` | Utility (runtime and tests) | — | Stay | — |
| `internal/runtimeconsumer` | Test evidence (no archcheck layer; its `go list -deps` closure test keeps package `ego` and GoAkt out) | `port/behavior`, `port/runtime`, `test/data/testpb` | Stay; driven by `compose/goakt`'s end-to-end test | S4-4 done (#147) |
| `internal/cmd/ciselect`, `.../selector` | Tooling | `.../selector` | Stay | Extended by #111 |
| `internal/cmd/archcheck` | Tooling | `.../rules` | Stay | S2 done, #117 |
| `testkit` | Test support (public) | `egopb`, `encryption`, `offsetstore`, `persistence` | Stay | — |
| `persistence/conformance` | Test support | `egopb`, `persistence`, `tenancy`, `test/data/testpb` | Stay | — |
| `mocks/ego`, `mocks/persistence`, `mocks/offsetstore`, `mocks/encryption`, `mocks/eventadapter`, `mocks/tenancy` | Test support (generated) | contract packages, `egopb` | Stay; `mocks/ego` regenerated or aliased in S1 | S1 |
| `test/data/testpb`, `example/examplepb` | Test/example schema | — | Stay | — |
| `example/durablestate`, `example/eventssourced`, `example/saga` | Consumer example; each `main.go` is that program's composition root | `ego`, `example/examplepb`, `testkit` | Stay | — |

Test-only edges (14 in total) that CI selection must keep visible: `ego` tests import `example/examplepb`, `internal/pause`, the five `mocks/*` packages it uses, `test/data/testpb` and `testkit`; `internal/extensions` tests import `testkit`; `migration` tests import `test/data/testpb` and `testkit`; `testkit` tests import `persistence/conformance` and `test/data/testpb`.

Nested modules, for completeness:

| Module | Uses from root | Destination |
|---|---|---|
| `publisher/kafka`, `publisher/nats`, `publisher/pulsar`, `publisher/websocket` | `ego.EventPublisher`, `ego.StatePublisher`, `ego.ErrPublisherNotStarted`, `egopb.Event`, `egopb.DurableState` | Import `port/publishing` after #111 |
| `benchmark` | `ego`, `egopb`, `example/examplepb`, `persistence`, `testkit` | Unreleased consumer; unchanged |
| `example/cluster` | `ego`, `egopb`, `example/examplepb`, `offsetstore`, `persistence`, `projection` | Unreleased consumer; unchanged. Does not build at `b43fad5` (section 9) |

### 4.1 Composition root today

Issue #104 asks for every package to be classified, including the composition root. Ego has no dedicated composition-root package today. Assembly is split between package `ego` and the consumer's program:

1. The consumer calls `ego.NewConfig(eventsStore, opts...)` (`option.go`). The `With*` options collect the concrete stores, event adapters, encryptor, telemetry, tenant resolver and projections. `NewConfig` also allocates the in-process event stream.
2. The consumer passes `cfg.GoaktOptions()` to `goakt.NewActorSystem` and starts the actor system. `GoaktOptions` translates each configured contract into a GoAkt extension from `internal/extensions`. This is adapter wiring, and it lives in package `ego`.
3. The consumer calls `ego.NewEngine(sys, cfg)` (`engine.go`). It validates that the required extensions are registered and injects the spawn-configuration dependency types, then `Engine.Start` configures the OpenTelemetry propagator when telemetry is set.
4. Publishers are attached after construction with `Engine.AddEventPublishers` and `Engine.AddStatePublishers`, which also start them.
5. Shutdown is split: `Engine.Stop` closes the publishers and the event stream but does not stop the actor system, which the consumer stops.

The consumer's `main` is therefore the actual composition root. The three root-module examples and `example/cluster` all follow it: `NewConfig`, then `GoaktOptions`, then `goakt.NewActorSystem`, then `NewEngine`. The helpers it depends on are mixed into the GoAkt runtime adapter package `ego`.

This ADR only records that classification. Where the composition root should live, how it validates the graph and who owns Start/Stop ordering are decided by #105 (EGO-ARCH-003), which depends on this change and on #103. This change does not move or redesign any of these functions.

## 5. Slices

Each slice is reviewable on its own and leaves the tree releasable.

**S1 — `port/publishing` inside the root module.**
Move the declarations of `EventPublisher`, `StatePublisher` and `ErrPublisherNotStarted` from `publisher.go` into package `github.com/pablogore/ego/v4/port/publishing`. Leave in `publisher.go`:

```go
type (
	EventPublisher = publishing.EventPublisher
	StatePublisher = publishing.StatePublisher
)

var ErrPublisherNotStarted = publishing.ErrPublisherNotStarted
```

S1 has two steps with different preconditions:

- *S1a, root only* — changes only root-module files and may land before #111. The root lane compiles the root module only, though, and the moved API has consumers outside it: the four publishers use `EventPublisher`, `StatePublisher` and `ErrPublisherNotStarted`, and `benchmark` and `example/cluster` compile against the root through their `replace` directives. **Merging S1a therefore requires the nested-consumer check below, observed on the S1a head and recorded in the PR, even though #111 does not automate it yet.**
- *S1b, publisher migration* — switching the four publishers from package `ego` to `port/publishing` MUST wait for #111, so that a publisher-only change runs that module's build, vet and lint. **Done** after #111 (#120): the publishers import `port/publishing`, each has a test that checks the `ego` aliases and `errors.Is` against both sentinels, and the four publisher entries are gone from the archcheck baseline.

Nested-consumer check (merge condition for S1a, and repeated for S1b). Run this as a script, not by pasting `set -e` into an interactive shell:

```sh
set -e
for m in publisher/kafka publisher/nats publisher/pulsar publisher/websocket benchmark; do
  (cd "$m" && go build ./... && go vet ./...)
done
go build ./mocks/ego/ && go vet ./mocks/ego/
```

The four publishers, `benchmark` and `mocks/ego` MUST pass; `set -e` makes any failed build or vet stop the gate. Check `example/cluster` separately against the same commands on `main` and the S1a head, recording both outputs in the PR. It already fails to build on `main` at `b43fad5` because its store implements the old interface (section 9; tracked by #115). Until #115 is fixed, S1a MUST introduce no new errors there. After #115, `example/cluster` MUST build and vet too. Once #111 builds nested modules in CI, its job replaces this manual check.

S1 counts as implemented only when all of the following have been observed:

1. An API-compatibility comparison of package `ego` between the baseline and S1 (for example `golang.org/x/exp/cmd/apidiff`) reports no incompatible change.
2. The nested-consumer check above passes against the S1 root: the four publishers, `benchmark` and `mocks/ego` build and vet, and `example/cluster` introduces no new errors (or builds, once its existing failure is fixed).
3. `errors.Is(err, ego.ErrPublisherNotStarted)` holds for errors returned by the publishers, and a publisher value still satisfies `var _ ego.EventPublisher = ...` assertions.
4. After S1b, `go list -deps` for each publisher contains no `github.com/tochemey/goakt/v4` package.

Once a release exposes `port/publishing`, it is public API for the rest of v4. From then on, rolling back S1 may revert in-repository callers but MUST NOT delete the package (see the proposal's Rollback section).

**S2 — dependency-rule check. Done (#117).**
`internal/cmd/archcheck` checks every direct production import edge in section 3's layers — the root module's graph from `go list -e -json ./...`, plus each nested module parsed with `go/parser` — against a baseline of known violations, and fails CI when an edge breaks a rule that no baseline entry covers, printing the importer, the forbidden import and the rule. It needed no module change and landed in parallel with S1a, as planned. It runs in both `pull_request.yml` and `build.yml`, right after dependencies are installed and before the linter (docs/ci.md, "Architecture boundary check").

**S3 — neutral behavior contracts** (#103). Remove `extension.Dependency` from `EventSourcedBehavior`, `DurableStateBehavior` and `SagaBehavior`, or add neutral definitions with a documented bridge. #103 owns the signatures and the compatibility plan.

**S4 — runtime adapter separation** (#11). Shape the runtime SPI, then move GoAkt-specific engine and actor code behind it. #11 owns the SPI; this ADR only fixes the dependency direction.

- **The SPI is done** (#147, `openspec/changes/ego-runtime-001/design.md`). S4-1 removed the last archcheck baseline entry (`migration` imports `internal/logging`, not `ego`). S4-2 added the contract package `port/runtime` with the neutral types, spawn options and errors, and left aliases in `ego`. S4-3 added the capability interfaces (`Entities`, `Sagas`, `Projections`, `Events`) and the composite `Runtime`, which `*ego.Engine` implements unchanged, plus a GoAkt-free test double. S4-4 added `compose/goakt.App.Runtime()` and `internal/runtimeconsumer`, a consumer whose production closure contains neither `ego` nor GoAkt and which drives a real application end to end through that accessor. No v4 API broke; archcheck has no baseline entry.
- **What remains for #124** (the major release that separates the adapter): move the GoAkt-bound groups out of package `ego` into a GoAkt adapter package and later module, as ego-runtime-001 §D10 lists them with their destinations — engine construction (`NewEngine`, `Config`), the escape hatches (`Engine.ActorSystem`, `Config.GoaktOptions`, `ClusterKinds`), the actors, the adapter-only errors, `internal/extensions` and the other internals; remove the S4 aliases so the `port/runtime` names are the only ones; remove the deprecated GoAkt-typed API; and remove or retype `compose/goakt.App.Engine()`. An in-memory runtime (#148) is the next implementation of `port/runtime`.

## 6. When a boundary deserves its own `go.mod`

A package set is promoted to a separate Go module only when **all** of these hold, each checkable:

1. **Rules hold.** The S2 check has passed for the candidate set on every `main` build for at least one release.
2. **A package cannot deliver the benefit.** The module must deliver one of: removing a heavy module (for example GoAkt or Olric) from consumers' requirement lists; a different Go toolchain or `go` directive; or an independent release cadence. Faster compilation alone does not qualify, because the S1 prototype showed it without a new module.
3. **CI verifies it.** #111's acceptance criteria hold for the new module: it is discovered automatically, and build, vet, lint and tests run on PRs that touch it and on `main`.
4. **It can be released.** It has a tag scheme and a published-version verification job (section 8).
5. **No hidden coupling.** There is no cycle with the root module and no import of another module's `internal/` packages.

Existing nested modules are judged by the same criterion. The publishers already have a real benefit (each pulls a broker client the root module does not need), so they stay modules. `benchmark` and `example/cluster` remain modules as unreleased consumers.

## 7. Impact on CI and test selection

- **Today:** `internal/cmd/ciselect` classifies any nested-module file as satellite, and returns `ModeNone` when a change touches only satellite files (reproduced with the five `publisher/kafka` files: `mode=none`, 0 of 20 included packages). The root workflows lint and test only the root module.
- **Consequence for this ADR:** no new `go.mod` and no publisher migration (S1b) before #111. S2 touches only the root module and is covered by the existing root lane. S1a touches only root files, but it changes an API that nested modules consume, so the root lane covers it only together with the manual nested-consumer check in section 5.
- **Selection rule the topology enables:** for a change to package X, run the tests of X and of X's transitive reverse consumers, including packages that import X only in tests. Moving contracts out of package `ego` matters because today every root-package file forces a full-suite fallback.
- **Existing boundaries:** since S1b, a publisher change selects only that publisher module (observed: a change to `publisher/kafka/kafka.go` alone yields `modules.json` `["publisher/kafka"]` and root mode `none`), and that module no longer compiles GoAkt in production. A change to `port/publishing` selects the root reverse consumers plus the four publishers. Since #122, that module's *default* `go test ./...` no longer compiles GoAkt either — the historical `ego`-alias check moved behind a `compat` build tag, verified separately (docs/ci.md, "Compatibility lane (#122)"). A change to a root file (including the S1 aliases in `publisher.go`) still selects every nested module, because `ciselect` parses `_test.go` imports regardless of build tags (observed: `ciselect -changed publisher.go` → root mode `full`, `modules.json` names all six nested modules), so the compatibility lane keeps running exactly where an alias could break.
- **Test latency** in the root package (about 592 s, mostly fixed waits) limits how much any selection improvement shows in wall time. It is tracked in #112 and is not part of this decision.

## 8. Versioning policy (proposed)

Two kinds of verification are distinct and both are needed:

- **Integrated verification** proves the monorepo builds together at `HEAD`. It uses the checked-in `replace` directives, or a `go.work` file, and runs on every PR. It says nothing about what a consumer resolves from the module proxy.
- **Published verification** proves a released module works for consumers. It builds each nested module with `GOWORK=off` and with its `replace` dropped (for example through a temporary `-modfile`), against a real published root tag. This is a **release condition**. It is not required to accept this ADR, nor to extract a package inside the root module.

Proposed policy:

1. The root module is released first, as a semantic-version tag `v4.x.y` on the repository the module path resolves to.
2. Each released nested module then updates its root requirement to that published version (`go get`, `go mod tidy`; `release.yml` already does this) and is tagged `publisher/<name>/vX.Y.Z` with its own semantic version.
3. A nested module's `require` MUST name a root version that exists on the module proxy at release time. Today all six require `v4.4.3`, which does not exist; the first release must fix that.
4. `benchmark` and `example/cluster` are not released. They keep integrated verification only and may keep their `replace` permanently.
5. Checked-in `replace` directives are acceptable for integrated verification because Go ignores them when the module is consumed as a dependency; they must never be the only evidence for a release.

Open inputs: which root version to publish first, and whether the module path stays `github.com/pablogore/ego/v4` (section 10).

## 9. Evidence and reproduction

Unless a row names another commit, measurements used baseline `a4edded` exported with `git archive` into a disposable directory, local Go 1.26.6 on linux/amd64, and no `-race` flag. CI uses Go 1.27.0.

| Claim | Command | Observed |
|---|---|---|
| Graph, per module | `go list -e -deps -test -json ./...` in each of the seven module directories | 0 load errors; root 32 packages, 52 production edges, 14 test-only edges, no cycles |
| Nested modules need the local `replace` | `go mod edit -modfile=go.norepl.mod -dropreplace=github.com/pablogore/ego/v4` then `go build -mod=mod -modfile=go.norepl.mod ./...` | `unknown revision v4.4.3` for all six |
| A published pseudo-version works | `go mod edit -modfile=go.pv.mod -dropreplace=... -require=github.com/pablogore/ego/v4@v4.0.0-20260923154928-a4edded48f01` then build Kafka | exit 0 |
| Satellite gap | `go run ./internal/cmd/ciselect -changed kafka.txt -out-dir out` with the five `publisher/kafka` paths | `mode=none`, 0 of 20 |
| Publisher coupling | `(cd publisher/kafka && go list -deps ./... \| wc -l)` | 592 packages (15 root, 45 GoAkt) |
| S1 prototype | same, after the throwaway extraction; cold build with `GOCACHE=$(mktemp -d) /usr/bin/time go build ./...` | 290 packages (2 root, 0 GoAkt); see proposal table |
| Nested-consumer check at `main` `b43fad5` (before S1) | the section 5 commands, with `example/cluster` run separately; Go 1.26.6 | the four publishers, `benchmark` and `mocks/ego` pass; `example/cluster` fails: `*PostgresEventStore does not implement persistence.EventsStore (wrong type for method DeleteEvents)`. Its `DeleteEvents` lacks the `persistence.Scope` parameter (`stores.go:45`, `main.go:126`) |

Limits: the S1 prototype covered Kafka only, over two rounds on a loaded host. Max RSS is per process. Alias compatibility was checked by compilation only. No CI, race or cold-module-download timings were taken.

## 10. Open decisions

| Decision | Why it is open | Who closes it |
|---|---|---|
| Protobuf policy: are `egopb` and `proto.Message` part of the supported public contract? | **Decided 2026-09-26:** yes. `egopb` and `proto.Message` remain a supported public contract for all of v4 and are revisited at the next major release. Rationale: archcheck's `contract-allowlist` already admits `egopb` and the protobuf runtime in contracts, and dropping them would break every behavior signature for no runtime-decoupling gain. | Maintainers (closed); revisit at the next major |
| Module path `github.com/pablogore/ego/v4` versus the repository `getsyntegrity/ego` | The proxy resolves the path through a GitHub redirect. Migrating the path is a breaking import change for every consumer. | Maintainers, before the first release |
| First published root version | Nested modules require `v4.4.3`, which does not exist; no tags exist in either repository. | Release owner, together with #111's release verification |
| Composition root destination | Assembly is split today between package `ego` (`NewConfig`, `GoaktOptions`, `NewEngine`, publisher registration) and the consumer's `main` (section 4.1). This ADR only classifies it. | #105 |
| Alias deprecation window | **Decided 2026-09-26:** deprecate first, no break inside v4. Every temporary alias and deprecated API (`EntityKind`, the S1 publisher aliases in package `ego`, and whatever S3 adds) is removed at the major release introduced by #124, not earlier. Rationale: #124 already plans the major that empties the root package, so one removal point keeps v4 compatible and gives consumers a single migration. **Corrected 2026-09-27 (maintainer decision on #151, ego-runtime-001 design §10 Q1):** only APIs replaced by a *different* API are marked `Deprecated:` (for example `EntityKind` and `Engine.Entity`). Aliases of the same type or value (the S1 publisher aliases, S3's `SagaAction`/`SagaCommand`, S4's `port/runtime` aliases) and the `ego.With*` wrappers that forward to `port/runtime` stay unmarked until #124 removes them. Accepted cost: consumers get no staticcheck warning about those names before #124; the `CHANGELOG.md` tables and #124's migration guide are their notice. | Maintainers (closed); removal executed by #124 |
