# Architecture tests

Issue #208. Tests that enforce a dependency boundary are named `TestArchitecture*`, so a CI job can select them by
name the way it selects `TestCluster*` (see [docs/ci.md](../ci.md)). This page is the inventory behind that
convention. The names came first (#297); the `architecture` job that runs them, and the exclusion of `TestArchitecture*`
from the shards, the race job and the modules job, came next. How the job selects them is in
[docs/ci.md](../ci.md#the-architecture-lane).

## What counts as an architecture test

Classified by behavior, not by file name. A test is an architecture test when it

- runs `go list` (or any `exec.Command`) to read the resolved import graph and compares it with an allowlist or a
  forbidden set, or
- scans or parses the repository's source files to enforce a boundary rule (who may import what, who may assert an
  interface, which constants are untyped).

Both kinds do work that has nothing to do with the code under test: a subprocess that resolves the module graph, or a
walk over every production file. That is why they do not belong under `-race` or coverage, which only slow them down.

Not architecture tests, and not renamed:

| Test | Why it stays |
|---|---|
| `TestClosureGuardRejectsCompositionRoot` (4 publisher modules) | Pure table over the guard function with inline fixtures: no subprocess, no file read. |
| `TestAssertionSitesNegativeControl` (`port/adapter`) | Runs the scanner over an inline source string only. |
| `TestThing`, `TestScanFlagsRealResourcesInTestFiles` (`.github/scripts/unitgate`) | `exec.Command` appears only inside fixture strings; nothing is executed. |
| `.github/scripts/impact` tests | Build fixture modules in a temp dir; they test the selector, not a repository boundary. |
| `TestCluster*`, `TestPostgres*`, `inttest/`, `example/`, `benchmark/` | Other lanes, other triggers; untouched. |

The guard tests above run next to the architecture test they protect, so they still catch a guard that stops guarding.

## Inventory and name mapping

23 tests: 19 in the root module and 4 in nested modules (the old name is the same in all four of those).

### Root module (`.`): 19 tests

| Package | Old name | New name | Mechanism |
|---|---|---|---|
| `engine` | `TestCommandArchitecture` | `TestArchitectureCommand` | `go list -deps ./command/...` |
| `engine` | `TestTenancyArchitecture` | `TestArchitectureTenancy` | `go list -deps ./tenancy/...` |
| `engine` | `TestKitLoggerIsTheOnlyLoggingBackend` | `TestArchitectureKitLoggerIsTheOnlyLoggingBackend` | walks every production file |
| `internal/instrumentation` | `TestInstrumentationStaysRuntimeNeutral` | `TestArchitectureInstrumentationStaysRuntimeNeutral` | `go list -deps .` |
| `internal/logging` | `TestLoggingStaysRuntimeNeutral` | `TestArchitectureLoggingStaysRuntimeNeutral` | `go list -deps .` |
| `internal/projectionrunner` | `TestProjectionRunnerStaysRuntimeNeutral` | `TestArchitectureProjectionRunnerStaysRuntimeNeutral` | `go list -deps .` |
| `internal/runtimeconsumer` | `TestProductionClosureExcludesRootAndGoAkt` | `TestArchitectureRuntimeConsumerProductionClosureExcludesRootAndGoAkt` | `go list -deps .` |
| `migration` | `TestProductionClosureExcludesRootAndGoAkt` | `TestArchitectureMigrationProductionClosureExcludesRootAndGoAkt` | `go list -deps .` |
| `port/adapter` | `TestAdapterDependsOnlyOnStdlib` | `TestArchitectureAdapterDependsOnlyOnStdlib` | `go list -deps` |
| `port/adapter` | `TestContractPackagesDoNotImportAdapter` | `TestArchitectureContractPackagesDoNotImportAdapter` | `go list -deps` per contract package |
| `port/adapter` | `TestPortNameConstantsAreUntyped` | `TestArchitecturePortNameConstantsAreUntyped` | parses each `port.go` |
| `port/adapter` | `TestOptionalInterfacesAreAssertedOnlyInTheirAccessors` | `TestArchitectureOptionalInterfacesAreAssertedOnlyInTheirAccessors` | parses every production file |
| `port/adapter` | `TestNoPrivateCopiesOfOptionalInterfaces` | `TestArchitectureNoPrivateCopiesOfOptionalInterfaces` | parses every production file |
| `port/adapter/adaptertest` | `TestAdaptertestDependsOnlyOnStdlibAndAdapter` | `TestArchitectureAdaptertestDependsOnlyOnStdlibAndAdapter` | `go list -deps` |
| `port/behavior` | `TestBehaviorDependsOnlyOnContracts` | `TestArchitectureBehaviorDependsOnlyOnContracts` | `go list -deps .` |
| `port/publishing` | `TestPublishingDependsOnlyOnContracts` | `TestArchitecturePublishingDependsOnlyOnContracts` | `go list -deps .` |
| `port/publishing/publishingtest` | `TestPublishingtestDependsOnlyOnStdlibPublishingAndEgopb` | `TestArchitecturePublishingtestDependsOnlyOnStdlibPublishingAndEgopb` | `go list -deps` |
| `port/runtime` | `TestRuntimeDependsOnlyOnContracts` | `TestArchitectureRuntimeDependsOnlyOnContracts` | `go list -deps .` |
| `port/runtime` | `TestRuntimeTestClosureExcludesGoAktAndRoot` | `TestArchitectureRuntimeTestClosureExcludesGoAktAndRoot` | `go list -deps -test .` |

The helpers (`goList`, `tenancyArchitectureGoList`, `hermeticGoEnv`, `portNameConstants`, `assertionSites`, ...) are not
tests and keep their names; they are used only by the tests above.

### Nested modules: 4 tests

`publisher/kafka`, `publisher/nats`, `publisher/pulsar`, `publisher/websocket`: `TestUnitTestClosureExcludesRuntimeAndRoot`
becomes `TestArchitectureUnitTestClosureExcludesRuntimeAndRoot` (`go list -deps -test ./...` in each module).

These modules have their own `go.mod`, so a `-run`/`-skip` on the root module never reaches them. The `architecture` job
runs them from their own directory, and the `modules` job skips `TestArchitecture*`, so they run in exactly one lane.

## Which lane runs what

| Job | `TestArchitecture*` |
|---|---|
| `test (shard N)`, `race`, `modules (dir)` | skipped (`-skip`) |
| `architecture` | runs them: the selected packages and the source-sensitive ones, all of them on a full run; every test the plan expected must pass |
| `test (min)` | still runs them on the release PR and on a hotfix PR to `main`, with the minimum Go (to review) |

Two packages are **source-sensitive**: `engine` and `port/adapter`. Their tests look at files the package does not
import (`./command/...`, `./tenancy/...`, the contract packages that must not import `port/adapter`, every production
file), so the import graph cannot tell when they have to run; a changed production Go file anywhere selects them.

## Parity

Before and after the rename, `go test -list . ./...` on the root module returns 848 test names; the only difference is
the 19 renames above (38 diff lines, one `<` and one `>` per rename). `go test -list '^TestArchitecture' ./...` returns
exactly the 19 new names, plus the 4 publisher tests in their modules. No test was added, removed or changed beyond its
name, and the `specs.Describe` descriptions are unchanged. All of them pass with `-count=1` and no `-race`.

Parity of what runs. Before the lane, a package the plan selected ran its architecture tests inside its shard (with
`-race` and coverage). Now every package with architecture tests that the graph selects runs them in the `architecture`
job instead, with the same reason, and the two source-sensitive packages run them on any production Go change besides.
No selected test lost its lane: `go test -list` of the shards' `-skip '^TestCluster|^TestArchitecture'` plus the
`architecture` plan covers the 848 names, and a full run lists the 23 tests (19 in `.`, 4 in the publishers) and checks
that each one passed.

Historical notes under `odd/` and `openspec/` still quote the old names on purpose: they record what was true then.
