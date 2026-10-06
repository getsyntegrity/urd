# Exploration — Physical Go modules and impact-aware CI (EGO-ARCH-006)

| Field | Value |
|---|---|
| Change | `ego-arch-006` |
| Date | 2026-09-27 |
| Phase | `sdd-explore` |
| Tracker | [`#102`](https://github.com/getsyntegrity/ego/issues/102), parent [`#10`](https://github.com/getsyntegrity/ego/issues/10), related [`#38`](https://github.com/getsyntegrity/ego/issues/38) |
| Baseline | `main` at `77beda6b91646b0e031ce78401ee997fd919bd65` |
| Next | [`proposal.md`](./proposal.md), [`design.md`](./design.md) |

## 1. Question

Issue #102 asks Ego to turn its architectural boundaries into real Go modules, so that a change builds and tests only the modules it touches plus every module that depends on them, with the full suite kept as the integration and release gate. Before deciding which boundaries become modules, this exploration measures four things on `main`:

1. which modules exist, and what each one actually pulls in;
2. how the CI selector (`internal/cmd/ciselect`) finds modules and decides which ones to run;
3. what CI costs today for each kind of change, from real GitHub Actions runs;
4. whether a nested module in this repository can be consumed from the Go module proxy at all.

The fourth question was not in the original brief. It turned out to be the constraint that shapes every extraction option in the design, so it is reported here in full (section 5).

Terms used below: a **nested module** is a directory with its own `go.mod` below the repository root. The **root module** is `github.com/pablogore/ego/v4`, declared in `go.mod:1`. A **requirement** is a `require` line in a `go.mod`; the **module graph** is what `go mod graph` prints, and it is what a consumer downloads and resolves, independently of which packages it imports. The **package closure** is what `go list -deps` prints: the packages that are actually compiled.

## 2. Method

All commands ran on a clean worktree of the baseline, local Go 1.27.1 on linux/amd64, `GOWORK=off`, `GOFLAGS=-mod=mod`, no `-race`. CI uses Go 1.27.0. Proxy probes used `GOPROXY=https://proxy.golang.org,direct` (the default) and, where stated, `GOPROXY=direct`, with a scratch module cache. CI timings come from `gh run view <id> --json jobs`; a run's wall-clock time is its `createdAt` to `updatedAt`. Throwaway scripts were kept outside the repository and are not part of this change.

## 3. Modules today

There are seven modules and no `go.work` file. `go.work` is not listed in `.gitignore` either; it is simply absent.

| Directory | Module path | Declared `go` | Requires root at | Local `replace` of root | Released? |
|---|---|---|---|---|---|
| `.` | `github.com/pablogore/ego/v4` | 1.26.0 | — | — | no tags exist |
| `publisher/kafka` | `github.com/pablogore/ego/v4/publisher/kafka` | 1.26.0 | `v4.4.3` (line 7) | `../../` (line 104) | intended, `release.yml` |
| `publisher/nats` | `.../publisher/nats` | 1.26.0 | `v4.4.3` (line 7) | `../../` (line 97) | intended |
| `publisher/pulsar` | `.../publisher/pulsar` | 1.26.2 | `v4.4.3` (line 7) | `../../` (line 139) | intended |
| `publisher/websocket` | `.../publisher/websocket` | 1.26.0 | `v4.4.3` (line 7) | `../../` (line 91) | intended |
| `benchmark` | `.../benchmark` | 1.26.0 | `v4.4.3` (line 7) | `../` (line 74) | never |
| `example/cluster` | `.../example/cluster` | 1.26.0 | `v4.4.3` (line 10) | `../../` (line 6) | never |

`git ls-remote --tags origin` returns zero tags, so `v4.4.3` has never been published (ego-arch-001 design §8 already recorded this). No nested module requires another nested module: every edge between modules points at the root.

## 4. The real graphs

### 4.1 Root module packages

The root module has **35 packages** (`go list ./...`) and **55 production first-party import edges**. It had 32 packages at `a4edded` (ego-arch-001); the three new ones are `port/publishing`, `internal/cmd/archcheck` and `internal/cmd/archcheck/rules`.

Every contract package compiles without GoAkt, Olric or OpenTelemetry. The table below is `go list -deps <pkg>`, counting packages:

| Package | Layer (ego-arch-001 §4) | First-party | GoAkt | Olric | OTel | Total |
|---|---|---|---|---|---|---|
| `ego` (root package) | runtime adapter | 16 | 45 | 34 | 20 | 540 |
| `internal/extensions` | runtime adapter | 11 | 1 | 0 | 10 | 185 |
| `migration` | application | 17 | 45 | 34 | 20 | 541 |
| `persistence` | contract | 3 | 0 | 0 | 0 | 115 |
| `offsetstore` | contract | 2 | 0 | 0 | 0 | 114 |
| `port/publishing` | contract | 2 | 0 | 0 | 0 | 114 |
| `eventstream` | contract | 3 | 0 | 0 | 0 | 128 |
| `tenancy`, `command`, `encryption`, `eventadapter`, `projection` | contract | 1–2 | 0 | 0 | 0 | 62–117 |
| `egopb` | schema | 1 | 0 | 0 | 0 | 112 |
| `testkit` | test support | 6 | 0 | 0 | 0 | 257 |
| `persistence/conformance` | test support | 5 | 0 | 0 | 0 | 251 |
| `internal/cmd/ciselect`, `internal/cmd/archcheck` | tooling | 2 | 0 | 0 | 0 | 89 |

Three edges matter for any split:

- `egopb` is imported in production by `ego`, `migration`, `offsetstore`, `persistence`, `persistence/conformance`, `port/publishing`, `testkit` and three `mocks/*` packages. Any module that holds a contract importing `egopb` needs `egopb` in the same module or in a module below it; otherwise the root and the contract module would require each other.
- `eventstream`, a contract, imports the root's `internal/queue` and `internal/syncmap`. If `eventstream` ever leaves the root module, those two utilities must move with it, because ego-arch-001 §3 forbids importing another module's `internal/` packages.
- Root tests import `testkit` (from `ego`, `internal/extensions` and `migration`), and `testkit` imports contracts and `egopb`. `testkit` cannot become a module before the contracts do, or the two modules would require each other.

The tooling packages under `internal/cmd` import only the standard library and their own subpackages.

### 4.2 Nested modules: compiled packages versus the module graph

This is the measurement ego-arch-001's §6 criterion 2 depends on. It separates what a module **compiles** from what it **requires**:

| Module | Production closure (GoAkt pkgs) | Test closure (GoAkt pkgs) | `go mod graph` edges | …of which mention GoAkt / Olric | Broker/driver in closure |
|---|---|---|---|---|---|
| `publisher/kafka` | 299 (0) | 611 (45) | 890 | 173 / 35 | `IBM/sarama` |
| `publisher/nats` | 256 (0) | 565 (45) | 922 | 173 / 35 | `nats-io/*` |
| `publisher/pulsar` | 577 (0) | 822 (45) | 1679 | 173 / 35 | `apache/pulsar-client-go`, 63 OTel pkgs |
| `publisher/websocket` | 239 (0) | 551 (45) | 905 | 173 / 35 | `gorilla/websocket` |
| `benchmark` | 1 (0) | 561 (45) | 795 | 173 / 35 | — |
| `example/cluster` | 1039 (46) | 1056 (46) | 1361 | 173 / 35 | `jackc/pgx/v5` |

Two conclusions:

1. **Package extraction already removed GoAkt from what the publishers compile in production** (S1b, #121): their production closure imports exactly two root packages, `egopb` and `port/publishing`. Their tests still compile GoAkt, because each `compat_test.go` imports the root package `ego`; open PR [#130](https://github.com/getsyntegrity/ego/pull/130) moves that file behind a `compat` build tag.
2. **Package extraction cannot remove GoAkt from what the publishers require.** Every publisher requires the root module, and the root module requires GoAkt, so all 173 GoAkt edges and 35 Olric edges stay in each publisher's module graph whatever it imports. Only a module boundary below the root (a module the publishers can require *instead of* the root) removes them. This is exactly the benefit ego-arch-001 §6 criterion 2 names ("removing a heavy module … from consumers' requirement lists").

## 5. Finding: nested modules under `/v4/` cannot be consumed

`release.yml` tags publishers as `publisher/<name>/vX.Y.Z` (`.github/workflows/release.yml:116`, `:204`), and ego-arch-001 §8 proposes the same scheme. The probe below shows that a consumer cannot resolve a publisher under either spelling of its path, at the baseline commit:

| Probe (scratch module cache) | Result |
|---|---|
| `go mod download -json github.com/pablogore/ego/v4@77beda6…` | resolves to `v4.0.0-20260927004543-77beda6b9164` (the redirect to `getsyntegrity/ego` works) |
| `go mod download -json github.com/pablogore/ego/v4/publisher/kafka@77beda6…` (proxy, and again with `GOPROXY=direct`) | `invalid version: missing github.com/pablogore/ego/v4/publisher/kafka/go.mod at revision 77beda6b9164` |
| `go get github.com/pablogore/ego/v4/publisher/kafka@77beda6…` from a scratch consumer | `module github.com/pablogore/ego/v4@… found …, but does not contain package github.com/pablogore/ego/v4/publisher/kafka` |
| `go get github.com/pablogore/ego/publisher/kafka@77beda6…` | `module declares its path as: github.com/pablogore/ego/v4/publisher/kafka but was required as: github.com/pablogore/ego/publisher/kafka` |

**Why.** The go command maps a module path to a directory by stripping the repository root (`github.com/pablogore/ego`) from the path. For `github.com/pablogore/ego/v4/publisher/kafka` that leaves `v4/publisher/kafka`, so the go command looks for `v4/publisher/kafka/go.mod` and for tags prefixed `v4/publisher/kafka/`. The `/vN` special case, where the major-version suffix may be left out of the directory, applies only when `/vN` is the **last** element of the path (the root module itself). The module lives in `publisher/kafka`, so it is never found.

**Consequences.**

- At the baseline commit, none of the four publishers can be consumed from the proxy, and the tags `release.yml` would create cannot fix that. Earlier commits were not probed; their `go.mod` files declare the same paths in the same directories. This is a defect in the release scheme, not in #102, but #102 inherits it: **any new nested module that keeps the `github.com/pablogore/ego/v4/<dir>` path in directory `<dir>` has the same problem.**
- It matters most for a module that the **root** requires. If the root required, say, a contracts module that consumers cannot resolve, the root module itself would stop being consumable.
- It does not matter for a module that is never consumed (`benchmark`, `example/cluster`, or a new integration-test module), because nothing outside the repository ever resolves it.

The design (§3) turns this into an explicit human decision about nested-module paths and layout, alongside the two decisions ego-arch-001 §10 already left open (module path, first published version).

## 6. How `ciselect` finds and selects modules today

There is no checked-in list of modules. `modules.json` is an **output** that `ciselect` writes on every run (`internal/cmd/ciselect/main.go:323-360`), not a configuration file. Discovery and selection work like this:

- **Discovery.** `findSatelliteDirs` (`main.go:290-319`) walks the repository for any directory holding a `go.mod`, skipping `.git`, `vendor`, `node_modules`, `.codegraph`, `.atl` and `odd` (`main.go:52-59`). A new module is therefore found the moment its `go.mod` exists.
- **Module imports.** `discoverModuleImports` (`main.go:381-425`) parses every `.go` file of each nested module, tests included, with `go/parser` in imports-only mode (`main.go:400`). It keeps only root-module import paths. It does not evaluate build constraints, so a file behind `//go:build compat` still counts; #130 relies on that.
- **Module selection.** `selectModules` (`selector/select.go:265-296`) selects a nested module when a changed file is under its directory, when one of its imports is in the root lane's selected package set, or when a full-gate path changed: root `go.mod`, `go.sum`, `.golangci.yml`, or anything under `.github`, `scripts/ci` or `internal/cmd/ciselect` (`select.go:232-256`).
- **Only nested-to-root edges exist in the model.** A `Module` carries root import paths and nothing else (`select.go:72-82`). A nested module that required another nested module would never be selected by a change to that other module.

Running the baseline selector locally on representative changes gives:

| Change | Root lane mode | Root packages selected (of 23) | Nested modules selected |
|---|---|---|---|
| `publisher/kafka/kafka.go` | `none` | 0 | `publisher/kafka` |
| `docs/ci.md` | `none` | 0 | none |
| `internal/cmd/archcheck/main.go` (tooling) | `affected` | 1 | none |
| `internal/ticker/ticker.go` (root leaf) | `affected` | 3, including root package `ego` | all 6 |
| `internal/pause/pause.go` (test utility) | `affected` | 2, including `ego` | all 6 |
| `migration/migration.go` (application) | `affected` | 1 | none |
| `mocks/ego/event_publisher.go` (generated mock) | `affected` | 1 (`ego`, whose tests import it) | all 6 |
| `encryption/encryptor.go` | `affected` | 5, including `ego` | all 6 |
| `persistence/events_store.go` (contract) | `affected` | 6, including `ego` | all 6 |
| `port/publishing/publishing.go` (contract) | `affected` | 3, including `ego` | all 6 |
| `egopb/ego.pb.go` | `full` | 23 | all 6 |
| `engine.go` (root package) | `full` | 23 | all 6 |
| `go.work` (a new root file) | `affected` | 2 (`ego`, `migration`) | all 6 |
| `newmod/go.mod` + `newmod/x.go` (not on disk) | `full` (unknown path) | 23 | all 6 |

Three gaps follow from this table:

1. **Every root Go change outside `internal/cmd` and `migration` selects all six nested modules.** Every other root package reaches the root package `ego`, through production or test imports (a change under `example/*`, which nothing imports, falls back to the full suite), and every nested module imports `ego` somewhere (the publishers only in `compat_test.go`, which the parser still sees after #130).
2. **A `go.work` change is not treated as global.** A non-Go file at the root maps to the root package directory (`classify.go:174-178`, via `packageDirFor` at `classify.go:236-240`), so it selects the root package instead of forcing the full gate. #102 requires `go.work*` to force the full gate.
3. **Adding a `go.mod` inside an existing root directory is not detected as a boundary change.** Once the `go.mod` exists on disk, its files classify as satellite and the root lane can report `none`, although root packages that imported the carved-out directory may no longer build.

## 7. CI cost today, from real runs

All runs are `pull_request.yml` unless marked `build`, after #111 merged (the `modules` matrix job exists). The critical path is the `build` job followed by the slowest `modules` job.

| Change class | Run | Wall clock | Root lane | `build` job (Run tests) | Module jobs |
|---|---|---|---|---|---|
| Docs only | [36288797672](https://github.com/getsyntegrity/ego/actions/runs/36288797672) | 43 s | `none`, 0 of 23 | 40 s (0 s) | skipped |
| Leaf modules: the four publishers, plus an archcheck baseline file | [36283180992](https://github.com/getsyntegrity/ego/actions/runs/36283180992) | 177 s | `affected`, 1 of 23 | 42 s (10 s) | 4 jobs, 66–130 s (Pulsar slowest) |
| Same PR, earlier push | [36282737062](https://github.com/getsyntegrity/ego/actions/runs/36282737062) | 171 s | `affected`, 1 of 23 | 42 s (13 s) | 4 jobs, 75–124 s |
| Root package change (`fix(actors)`) | [36286599103](https://github.com/getsyntegrity/ego/actions/runs/36286599103) | 658 s | `full`, 23 of 23 | 529 s (497 s) | 6 jobs, 32–124 s |
| Root tests only (`test/112`) | [36288015030](https://github.com/getsyntegrity/ego/actions/runs/36288015030) | 578 s | `full`, 23 of 23 | 439 s (406 s) | 6 jobs, 21–134 s |
| New contract package plus root (`port/behavior`, #123 S3-1) | [36289361947](https://github.com/getsyntegrity/ego/actions/runs/36289361947) | 656 s | `full`, 24 of 24 | 528 s (498 s) | 6 jobs, 29–123 s |
| `scripts/ci` plus publishers (#130) | [36288686950](https://github.com/getsyntegrity/ego/actions/runs/36288686950) | 672 s | `full`, 23 of 23 | 539 s (499 s) | 6 jobs, 28–112 s |
| `build` on `main` (full gate) | [36283476574](https://github.com/getsyntegrity/ego/actions/runs/36283476574) | 654 s | `full` | 529 s (499 s) | 6 jobs, 24–98 s |

**A contract-only change has no post-#111 run to cite.** The local selector (section 6) shows that one selects the root package `ego` and all six modules. The root package's own tests took 97% of root test time in the docs/ci.md baseline (run 35868911889, which ran with `-race -p 1` before #108 removed `-p 1`), so such a change should cost about the same as a root change. That is an expectation, not a measurement; slice S0 in the design records a real one.

What the numbers say:

- **A root change costs about 11 minutes, and roughly 8.5 minutes of that is the root package's own tests.** No module topology changes that, because every contract change also reaches the root package, which consumes the contracts. Root test latency is tracked in #112.
- **Module jobs add about two minutes to the critical path** of a root change: the slowest module job (Pulsar, ~100 s of `Verify module`) runs after `build`. These jobs run only because every publisher requires and imports the root.
- **A leaf-module change is already fast**: about 3 minutes, because the root lane runs almost nothing.

## 8. `go.work` today and how it would interact with CI

No `go.work` is committed and nothing depends on one. `scripts/ci/verify-module.sh:41` and `scripts/ci/verify-published.sh:46` force `GOWORK=off`. `ciselect` and the root lane do not, and inherit whatever the environment says (`main.go:169`, `main.go:197`).

A probe on a copy of the baseline, with a `go.work` that uses all seven modules (`go work init . ./publisher/* ./benchmark ./example/cluster`):

- The root lane's `go mod vendor` step (`pull_request.yml:35`) fails: `'go mod vendor' cannot be run in workspace mode. Run 'go work vendor' … or set 'GOWORK=off'`.
- `GOFLAGS=-mod=vendor go list ./...`, which `ciselect` runs, fails and asks for `go work vendor`.
- `go work init` wrote `go 1.27.1`, the local toolchain, into the file. A committed `go.work` would therefore pin a toolchain line that has to be kept in step with CI's 1.27.0.

So a committed `go.work` needs `GOWORK=off` in every CI step that is not explicitly a workspace check, including the root lane. With `GOWORK=off`, a `go.work` also cannot mask a missing `require` in a nested module.

## 9. Related work in flight

- **#130** (open): `compat_test.go` behind `//go:build compat`; `verify-module.sh` runs a second `-tags compat` pass. `ciselect` untouched.
- **#128** (open, ego-arch-002-s3): `port/behavior` contracts. It records two decisions in ego-arch-001 §10: `egopb` stays a public contract in v4, and the alias window ends at the #124 major. S3-1 is in review as PR #131 and adds `port/behavior` to the root module.
- **#125** (open, ego-arch-003): `compose` and `compose/goakt` packages inside the root module.
- **#124**: the last task of #10. No Go files at the repository root, and the aliases in package `ego` are removed in a major release.
- **#39**: module identity (`pablogore` versus `getsyntegrity`), the release train and publishing. #102 lists publishing each module independently as out of scope for its first story.

## 10. Options considered for the topology

1. **Keep one module and improve selection only.** This is the cheapest option and it keeps the root package lane. But it cannot remove GoAkt from the publishers' requirement lists (section 4.2), and it leaves #102's "boundaries are real modules" criterion unmet.
2. **Many modules now, one per boundary, at the current paths.** Rejected. Each would inherit the section 5 defect, and a contracts module required by the root would make the root itself unconsumable.
3. **Selector first, then a small, ordered set of modules that each pass ego-arch-001 §6, with the path and layout questions decided by the maintainers before the first module the root requires.** Recommended; see the proposal and design.

In option 3, the contracts module can contain only packages whose imports stay inside it. `port/publishing` imports only `egopb`, so the two can move together. `port/behavior` (PR #131) imports `command`, which imports `tenancy`, so it cannot move without them (design D7).

Option 3 is the only one that satisfies #102's acceptance criteria and ego-arch-001 §6 at the same time. The one tension between them, #102's "architectural boundaries must also be compilation boundaries" against §6's "faster compilation alone does not qualify", is resolved by the measurement: the boundary that pays (the contracts module, below the root) pays through the module graph, not through compile time. The design records this reading as a decision for the maintainers to confirm.
