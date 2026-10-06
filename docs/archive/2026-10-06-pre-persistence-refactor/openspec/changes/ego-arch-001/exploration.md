# Exploration: EGO-ARCH-001 package and module topology

Date: 2026-09-23
Tracker: getsyntegrity/ego#104; parent #10; downstream runtime epic #11
Baseline: `main` at `a4edded`
Phase: read-only architecture spike; no production changes

## Current state

The root module is `github.com/pablogore/ego/v4` (`go.mod:1`). Six nested
modules already exist: four `publisher/{kafka,nats,pulsar,websocket}` modules,
`benchmark`, and `example/cluster`. All six require root `v4.4.3` through local `replace` directives. That tag has
never been published (neither repository has tags), so none builds without its
replace. Kafka builds after substituting the root pseudo-version
`v4.0.0-20260923154928-a4edded48f01`; release versioning remains unresolved.

The root `ego` package contains 25 production and 41 test Go files. It mixes
behavior definitions (`behavior.go`), `Engine` (`engine.go`), GoAkt actor
implementations, options (`option.go`), projection and saga execution, and
telemetry. A compiler-resolved `go list -e -deps -test -json ./...` run in each of the
seven modules completed without load errors. The root has 32 packages and 52
production edges, plus 14 edges appearing only in tests; there are no cycles.
The six nested modules depend on 15–17 root packages each. The four publishers
have no tests, so an initial CI gate can verify build, vet and lint but cannot
claim publisher test coverage. These measurements used local Go 1.26.6;
CI uses Go 1.27.0. Reproduction details and limits are recorded below.

| Package/group | Current role and imports | Initial destination |
| --- | --- | --- |
| Root `ego` | Public behavior contracts plus GoAkt actors, engine, options and OTel; imports 13 first-party packages | Split neutral definitions from GoAkt adapter; retain a compatibility facade at the old path |
| `command`, `tenancy` | Command model and tenant identity/resolution; `command` imports `tenancy` | Neutral application/domain contracts, subject to protobuf policy |
| `persistence`, `offsetstore` | Public store interfaces and scopes; both import `egopb`, and `persistence` imports `tenancy` | Ports, with explicit wire-format decision |
| `projection`, `eventstream` | Handler and stream interfaces; `eventstream` imports two internal utilities | Application/ports; isolate concrete runners |
| `encryption`, `eventadapter` | Public interfaces; adapter uses protobuf | Ports/codec boundary |
| `egopb` | Generated shared message types, imported by root, stores, migration, testkit and mocks | Explicit shared schema/codec boundary; do not assume runtime-neutral means protobuf-free |
| `internal/extensions` | GoAkt registration and concrete wiring; imports six contract packages | GoAkt adapter implementation |
| `migration` | Imports root `ego`, stores, tenancy and `egopb` | Application service or separately versioned tool after dependency check |
| `testkit`, `persistence/conformance`, `mocks`, `test/data` | Fixtures and conformance support | Test support; keep reverse test-import edges visible to CI |
| `publisher/*` | Already separate modules with broker/client implementations | Existing adapters; add independent CI coverage |
| `example/*`, `benchmark` | Consumer examples and benchmarks; cluster and benchmark have own modules | Consumer verification, outside core contract |
| `internal/{queue,runner,syncmap,ticker,pause}` | Local implementation utilities | Keep private; relocate only when an extracted module needs one |
| `internal/cmd/ciselect` | Root-module package selection | Extend to repository-wide module graph before another split |

The production graph is acyclic at package level, but neutral contracts are
not independent of implementation yet. `EventSourcedBehavior` and
`DurableStateBehavior` embed `extension.Dependency` (`behavior.go:47,99`),
and `SagaBehavior` does so in `saga.go:41`. The root API exposes
`NewEngine(goakt.ActorSystem, ...)` (`engine.go:221`),
`Engine.ActorSystem() goakt.ActorSystem` (`engine.go:415`),
`Config.GoaktOptions() []goakt.Option` (`option.go:122`) and
`ClusterKinds() []goakt.Actor` (`option.go:192`). Protobuf is also a
deliberate public API today: `Command`, `Event`, `State` alias `proto.Message`
(`behavior.go:34-44`), and command envelopes and results use it. The ADR must
separate the GoAkt coupling from the independent protobuf policy; treating all
protobuf as an accidental adapter detail would change the programming model.

## CI and test-selection evidence

`internal/cmd/ciselect/selector/graph.go` computes reverse production-import
closure and test-import consumers for the root module. The PR workflow runs
that selector and `scripts/ci/go-test.sh` under `-race`; the push workflow runs
its full mode. Both workflows execute `go mod tidy && go mod vendor` in the
root and use a root-only linter. This is a fast lane for the root module,
not repository-wide multi-module CI.

`selector/classify.go:164` marks any nested module change as `ClassSatellite`;
`selector/select.go:129,143` permits a change consisting only of such files
to return `ModeNone`. The root full suite (`go test` package list from root
`go list ./...`) also cannot include nested modules. No module-specific test
jobs appear in the two build workflows. A real `ciselect` run with five Kafka files returned `ModeNone`, zero of 20
included root packages and a satellite warning. Consequently a Kafka-only PR
passes the root lint and test job without building, vetting or linting Kafka.
The publishers are **separate in layout, but not verified by these CI jobs**. This
must be addressed before adding more `go.mod` files. In addition, all root
Go files force full fallback (`selector/classify.go`), so moving neutral code
out of root is necessary to realize a smaller affected set.

The current `-coverpkg` denominator is the same 20 included root packages in
both modes. A partial-cache measurement for `persistence` was 0.17 s with
`-coverpkg` versus 0.12 s without; it does not explain CI latency. The root
test binary alone ran for 592.05 s (3.4 s CPU), while its cold compilation
was 24.60 s. The 260 top-level root tests ran serially, with no `t.Parallel`.
The full root suite passed in 619 s locally, with 591 s in that one package.
A hypothesis is that 528 `pause.For` calls and 182 `ActorSystem` constructions
account for much of the waiting; this has not been profiled call by call.

## Approaches

1. **Create many modules now** — put every port and adapter behind its own
   `go.mod` immediately.
   - Pros: explicit compilation boundaries and isolated dependency lists.
   - Cons: breaks unprepared CI, complicates versions and `internal` imports,
     and retains GoAkt in the behavior API unless contracts change first.
   - Effort: high; not recommended.
2. **Extract neutral packages, then promote proven boundaries to modules** —
   move definitions behind stable imports, keep adapters outward, extend CI,
   and split module(s) after independent builds are demonstrated.
   - Pros: each move can preserve behavior and expose an actual boundary;
     reverse-dependency selection already works inside the root module.
   - Cons: temporary facade/compatibility code and two stages of CI work.
   - Effort: medium to high; recommended.
3. **Remain one root module permanently** — enforce package boundaries and
   keep the existing fast lane.
   - Pros: simplest versioning and releases.
   - Cons: cannot isolate GoAkt dependency graph/toolchain per module;
     package selection alone may not meet the compile-time objective.
   - Effort: medium; retain as a measured fallback, not an a priori choice.

## Recommendation and candidate boundaries

Define a neutral behavior/command/tenant/store-port dependency direction in
#104. #103 should remove GoAkt `extension.Dependency` from the behavior
contracts or introduce a separate neutral definition with a documented
compatibility bridge. #11 owns exact Runtime SPI, neutral references,
capabilities, GoAkt adapter and in-memory conformance; #104 should not lock
those signatures. #105 owns assembly, while #106 owns general adapter SPI.

First candidate module: **neutral core/ports**, once it demonstrably builds
without GoAkt and without importing the root `ego` package. The GoAkt runtime
adapter can become a second module after its imports point into that core;
existing publisher modules stay separate. Before another module split, fix
satellite CI selection and define a release version policy; the currently
required `v4.4.3` does not exist. `persistence`/`egopb` placement
depends on whether protobuf is part of the supported public contract. Do not
create a module for each small package or move code into `internal` if public
consumers must import its contracts. Avoid root ↔ nested-module cycles; local
`replace` directives must not be mistaken for release version policy.

For a change to component X, execute X's tests and the tests of **transitive
reverse consumers**, including packages that only import X in tests. Go
compiles forward dependencies automatically; rerunning all their tests is a
separate full-gate choice. Compute a repository-wide module/package graph,
include tests in every existing nested module, fail closed on unknown paths
and graph errors, and print the selection. A dynamic job matrix can supply
one workflow; a separate YAML per module is not required. Require full tests
for module/workspace metadata, generated schemas, selector/CI changes, and
the final main/merge gate. Measured no-race cold/warm compilation was
3.40/0.08 s for `internal/queue`, 6.64/0.12 s for `persistence`,
24.60/0.29 s for root, and 16.87/0.09 s for Kafka build. Profile the
root test waits and measure CI race timing before promising a latency target.

## Risks and open decisions

- Existing users implement `extension.Dependency` on behaviors and construct
  a GoAkt system before `NewEngine`; compatibility cannot be inferred from a
  pure package move. Name affected APIs and rollback in the proposal.
- Moving `egopb` or changing `proto.Message` changes serialization and public
  types. Decide this separately from removing GoAkt.
- Independent release/version policy for nested modules is undefined here;
  all six currently require nonexistent `v4.4.3` and compile only through
  checked-in local `replace` directives. Release verification must build
  without those replacements against a published version.
- `internal/extensions` is rooted under today's module. Go's `internal`
  visibility rules and package imports need checking when a new module owns
  the adapter.
- CI's current satellite `ModeNone` is a safety gap. Correct it before any
  new module is introduced, and test already-existing satellite modules.

## Atomicity

SPLIT_REQUIRED: normative architecture/map (#104), runtime SPI and
conformance (#11), and multi-module CI (#38) are separate verifiable outcomes.
The proposal for #104 can remain atomic if it defines the topology and
compatibility decisions without implementing the other epics.

## Ready for proposal

The compiler-resolved graph and local timing baseline are complete. A final
normative topology still needs the protobuf and public compatibility decisions,
release version policy, and a plan to close satellite CI's `ModeNone` gap.
The local run did not measure race timing, CI timing, cold module downloads,
or the exact source of root test waits.

## Reproduction and measurement limits

Baseline: `a4edded48f01b5430f4555effd99bd5e06c62702`, archived into a
temporary directory without moving the checkout. Run `go list -e -deps -test
-json ./...` separately in each module and inspect `Error` and `DepsErrors`.
Run `go run ./internal/cmd/ciselect -changed kafka.txt -out-dir out-kafka`
with the five Kafka file paths to reproduce `ModeNone`. Compile with
`go test -c`, then time the test binary itself with `-test.count=1`; use a
fresh `GOCACHE` for cold compilation and `go test -count=1 -timeout 30m -json
./...` for the full root suite. Local measurements used Go 1.26.6 on
Linux/amd64 under variable host load. No local `-race` run was performed.
The reported max RSS is the peak of one process, not the whole process tree.
The original raw logs lived in a temporary Claude job directory and are not
part of this PR; retain repeatable commands and observed values here.
