# CI/CD pipeline

This document explains how Urd is built, tested and released. The branching rules that surround the pipeline are in [`docs/main-branch-policy.md`](main-branch-policy.md), and the everyday contributor steps are in [`contributing.md`](../contributing.md).

Urd is a Go library, so a release is a git tag: `go get` resolves the tag, and nothing else has to be built or uploaded. The pipeline therefore does two jobs. On every pull request it proves the change is safe. On every merge to `main` it turns the merge into a semantic version tag plus a GitHub Release, without a human running any release command.

## The branch model

There are two long-lived branches.

- `develop` is the integration branch. Feature pull requests target it.
- `main` holds released code. Only two kinds of pull request may target it: `develop` to `main` (a normal release) and `hotfix/*` to `main` (an urgent fix). The `flow` job of the CI fails any other source branch.

The day-to-day path is: open a branch from `develop`, open a pull request back to `develop`, merge it once `ci-ok` is green. When enough work has accumulated, open a pull request from `develop` to `main`. Merging that pull request publishes the release. For an urgent fix, branch `hotfix/<name>` from `main`, open the pull request to `main`, and after the release the pipeline opens a `main` to `develop` pull request so the fix is not lost.

## What runs on a pull request

Everything is in `.github/workflows/ci.yml` and reports into one required check, `ci-ok`. Branch protection only has to require `ci-ok` (and `pr-meta`); adding a new job to the pipeline means listing it in the `needs` of `ci-ok`, and branch protection does not change.

| Job | What it does |
|---|---|
| `flow` | Rejects pull requests to `main` that do not come from `develop` or `hotfix/*`. On pull requests to `main` it also computes the version that will be published and prints it in the run summary. It fails early if the bump cannot be published, for example a major bump without `/vN` in `go.mod`. |
| `lint` | `golangci-lint` with `.golangci.yml`. On pull requests it only blocks issues introduced by the diff (`only-new-issues`), so existing problems do not stop new work. |
| `plan`, `test (shard N)`, `test-report` | The root module tests, split into shards by real timings from the previous run (see "Slow packages" below). Every shard skips the `TestCluster*` tests (`-skip '^TestCluster'`); they run in the `cluster` job. `test-report` merges coverage, lists the slowest tests and stores the timings for next time. Pull requests that only touch Markdown, `CHANGELOG/`, `OWNERS` or issue templates skip the tests; `ci-ok` still reports. |
| `test (min)` | Builds and vets with the minimum Go version declared in `go.mod`. On the `develop` to `main` release pull request it also runs every test with that version, the `TestCluster*` tests included; a hotfix pull request to `main` runs them with `-skip '^TestCluster'` (see "Test lanes"). |
| `modules (dir)` | Urd has nested Go modules (`benchmark`, `example` (every example, `example/cluster` included), `inttest`, `persistence/postgres`, `publisher/kafka`, `publisher/nats`, `publisher/pulsar`, `publisher/websocket`, `test/compat`). `./...` at the root does not reach them, so this job builds and vets each one on every pull request with Go changes, and tests all of them except `inttest` and `benchmark`. Those two are only built and vetted here (`go vet` compiles their test files). They are heavy, because `inttest` needs Docker and the benchmarks start a real goakt actor system, so they run in their own jobs after the merge. The `example` module is compiled here too (see "Examples"). |
| `benchmark` | Runs every benchmark of the `benchmark` module once (`go test -run '^$' -bench . -benchtime=1x`, about two minutes). The module has no `Test` functions, so a plain `go test` would compile it and run nothing. The job fails if no benchmark reports a result, and writes the results to the run summary and the `benchmark-results` artifact. A manual run (`workflow_dispatch`) takes a `benchtime` input, for example `2s`, for a real measurement. The triggers are the same as `inttest`: every push to `develop`, the `develop` to `main` release pull request and manual runs. Feature and hotfix pull requests only compile it, in `modules`. A red run on `develop` follows the same rule as `inttest`: the author of the merged pull request fixes it, or reverts the merge, before the next merge. |
| `inttest` | Runs the integration tests of the `inttest` module on real containers (see "Integration tests" below). It runs on every push to `develop` (each merge), on the `develop` to `main` release pull request and on manual runs. Feature and hotfix pull requests never run it; `ci-ok` accepts the skip. Because the release pull request requires `ci-ok`, `main` never receives a release with a red integration run. A hotfix reaches `main` without it and is covered when its `main` to `develop` sync pull request is merged and `develop` is pushed. |
| `cluster` | Runs every multi-node `TestCluster*` test of the root module on its own (`go test -run '^TestCluster' -json ./...`). It counts the top-level `TestCluster*` tests that passed and fails if there are none, because `go test` reports "no tests to run" as a pass. The counts go to the run summary and the JSON to the `cluster-results` artifact. It runs on every push to `develop` (each merge), on the `develop` to `main` release pull request and on manual runs, like `inttest`. Feature and hotfix pull requests never run it; `ci-ok` accepts the skip, and a failure blocks `ci-ok` on the runs that do execute it (see "Test lanes"). |
| `race` | Runs the whole root module under the race detector (`go test -race -skip '^TestCluster' ./...`). Cluster tests under `-race` are out of scope. It runs on every pull request with Go changes, like the unit shards, plus pushes to `develop`. |
| `unit-gate` | The unit-test rules in `docs/testing/go-specs.md` (no testify, no generated mocks, go-specs, no real resources in unit tests), plus the rule that nothing under `inttest/` skips, pends or focuses a test. |
| `tidy` | Runs `go mod tidy` in the root module and in every nested module and fails if `go.mod` or `go.sum` change. |
| `api` | Compares the public API with `apidiff`. Against `develop` it only warns. Against the latest tag (pull requests to `main`) it fails when the API breaks and the release is not labelled `release:major`. |
| `vuln` | `govulncheck`. It fails only when the code calls a vulnerable function. |
| `ci-ok` | Passes when every job above succeeded or was legitimately skipped. This is the single required status check. |

The race detector only runs in the `race` job, on the whole root module except the `TestCluster*` tests. The Go version comes from `.go-version` in every job, through the `go-setup` composite action, so nothing pins it by hand. `TEST_SHARDS`, `COVERAGE_MIN` and `TESTFLAGS` are set at the top of `ci.yml`; coverage is only reported for now (`COVERAGE_MIN` is `0`).

### Slow packages

`gotestsum tool ci-matrix` only moves whole packages between shards, so one package that takes minutes would set the wall time of the whole run. The `plan` job therefore runs `.github/scripts/test-matrix.sh`. From the timings of the previous run it finds every package slower than `SPLIT_THRESHOLD` seconds and splits it by top-level test into about `time / SPLIT_TARGET` shards, balancing them with longest-test-first. Each of those shards runs `go test -run '^(TestA|TestB)$'` for its share; the script checks that every test listed by `go test -list` lands in exactly one shard. It leaves the `TestCluster*` tests out of that list (the variable `CLUSTER_TESTS`, default `^TestCluster`), because the shards run with `-skip` of the same regex: `-run` selects the share and `-skip` removes the cluster tests from it. The remaining packages still go whole into `TEST_SHARDS` shards. Nothing is hard-coded, so a renamed or reorganized package is picked up from its timings. Coverage profiles of the shards of one package overlap; `go tool cover` sums duplicated blocks, so `test-report` just concatenates them.

The two knobs live at the top of `ci.yml`: `SPLIT_THRESHOLD` (default `90`, seconds a package may take before it is split) and `SPLIT_TARGET` (default `90`, aimed seconds per shard of a split package). Without timings (first run, empty cache) nothing is split.

The `pr-meta` workflow is separate because it also runs when the pull request description is edited, and that should not re-run the tests. It requires a `release-note` block and checks that `OWNERS` and `.github/CODEOWNERS` list the same people.

A push to `develop` runs the same CI (without `lint` and the pull-request-only jobs) so the merged result is validated and the test timings used by later pull requests stay fresh.

## Test lanes

The tests are not all run the same way. A lane is a job that runs one kind of test, and the kinds are told apart by name, not by build tags or by separate Go modules: every test is compiled and vetted in the normal build, and only the choice of which tests execute changes. `CLUSTER_TESTS: "^TestCluster"` at the top of `ci.yml` holds the one regex that splits the lanes.

| Lane (job) | What runs | Feature PRs to `develop` | Push to `develop` | Release PR `develop` to `main` | Hotfix PR to `main` | `workflow_dispatch` |
|---|---|---|---|---|---|---|
| Unit and component shards (`test (shard N)`) | The root module, `-skip '^TestCluster'`, with coverage | yes | yes | yes | yes | yes |
| `test (min)` | Build and vet with the minimum Go; on the release PR also every test, `TestCluster*` included (a hotfix PR skips them) | build and vet only | build and vet only | everything | everything except `TestCluster*` | build and vet only |
| `cluster` | `-run '^TestCluster' ./...` on the root module, at least one must pass | no | yes | yes | no | yes |
| `race` | `-race`, `-skip '^TestCluster'`, `./...` on the root module | yes | yes | yes | yes | yes |
| `inttest` | The `inttest` module on real containers | no | yes | yes | no | yes |
| `benchmark` | Every benchmark of the `benchmark` module once | no | yes | yes | no | yes |
| `modules (dir)` | Build, vet and test of each nested module (`inttest` and `benchmark`: build and vet only) | yes | yes | yes | yes | yes |

The `workflow_dispatch` column reads "yes" once `ci.yml` exists on the default branch, because GitHub only offers a manual run for workflows that are there.

### The `TestCluster` naming rule

A test that starts a clustered actor system (a real GoAkt cluster with gossip, peer and remoting ports on loopback, alone or with several nodes) is a top-level test whose name starts with `TestCluster`, in the same package as the code it tests. Nothing else is called `TestCluster*`: a single-node test of a cluster helper, such as `TestEngineClusterKindsExposesUrdActors`, uses another name, or it would be moved to the cluster lane by accident. The name is the only selector, so the lanes need no tag and no configuration, and every test runs in exactly one lane.

The `unit-gate` job enforces the rule in one direction. Its `cluster-name` rule fails when a test file calls `WithCluster` or a `dynaport` function from a top-level test that is not called `TestCluster*`, directly or through a helper of the same file. It cannot see a helper in another file, and it does not flag a `TestCluster*` test that starts no cluster; such a test only costs time in the cluster lane. How to write one is in [`testing/go-specs.md`](testing/go-specs.md#writing-a-cluster-test).

### Why the cluster tests have their own lane

The cluster tests are the slowest and the most sensitive to the machine (ports, timing), and a failure in them says little about the code in the pull request next to them. They do not need Docker: they are in-process, and every pull request ran them before the lanes existed (inside the unit shards). So they follow the `inttest` rule: they run after the merge (push to `develop`), on the release pull request and on manual runs, and never on feature or hotfix pull requests, so they do not lengthen the pull request path. Because `ci-ok` requires the job, a red cluster run blocks the release pull request and `main` never receives one; a hotfix is covered when its `main` to `develop` sync pull request is merged and `develop` is pushed. On the release pull request `test (min)` deliberately does not skip `TestCluster*`: it is the only job that runs them with the minimum Go version.

The `cluster` job runs `./...` and not a list of packages. With a hardcoded list, a new `TestCluster*` in another package would be skipped by the shards and never run in the lane, and the zero-pass guard would not notice because the listed packages still pass.

Neither job retries. A flaky cluster or race test is fixed in its own pull request, not re-run until green. A race that `race` finds in a package is fixed in a separate pull request that is merged first, or the package is excluded through an explicit list in `ci.yml` with a linked issue and an exit condition; the job is never silenced. If `cluster` or `race` is red on a push to `develop`, the author of the merged pull request fixes it, or reverts the merge, before the next merge to `develop`.

### What `race` does not cover

The race detector runs on the whole root module (`./...`), and it skips the `TestCluster*` tests. Those are not run under `-race` yet, which is a known gap, not a decision that they are race-free. The race detector is never run locally; it only runs in this job.

## Integration tests

Unit tests never leave the process. Tests that need a real system, today a Postgres database, live in the `inttest/` module and run against containers they start themselves.

### Where they live

`inttest/` is a nested Go module (`github.com/getsyntegrity/urd/inttest`) with `replace` directives to the root and to `persistence/postgres`, so it always tests the code in the working tree. pgx and Testcontainers appear only in its `go.mod` and in `persistence/postgres/go.mod`, never in the root one.

The module has two kinds of packages and no others, and no Go file sits directly in `inttest/`, `inttest/infra/` or `inttest/flows/`:

- `inttest/infra/<backend>` starts the infrastructure. Today that is `inttest/infra/postgres` (package `postgres`, imported as `pginfra` because `persistence/postgres` has the same name): `StartPostgres` returns a handle, and `NewDatabase(t)` creates an empty database with a unique name on it and drops it when the test ends. `infra/kafka`, `infra/nats` and `infra/pulsar` will sit beside it.
- `inttest/flows/<area>` checks a behavior against that infrastructure. Each flow package starts its own container from `TestMain`.
  - `inttest/flows/eventstore` holds the Postgres event store tests and the store and schema conformance suites.
  - `inttest/flows/restart` runs an engine on a Postgres events store, stops the whole actor system, starts a new one on the same database and checks that the entity comes back with its balance and revision, and keeps going from there.

Integration tests have one technique: Go tests in `inttest/` that start their containers with Testcontainers. There is no `docker-compose` file, no `services:` section in a workflow and no curl script that checks a running cluster. The curl-based `make test` of `example/cluster` was removed for that reason; `make load-test` stays because it is a load generator, not a pass/fail check.

### Why they never skip

The old Postgres tests called `t.Skip` when `URD_EXAMPLE_POSTGRES_DSN` was not set, and `go test` reports a skip as a pass, so they looked green in every CI run without running. The `inttest` module removes the cause instead of auditing it afterwards:

- Each test package starts its container from `TestMain`. If Docker or the container is not available, `TestMain` exits non-zero and the run fails with the reason. There is no variable to forget.
- The `unit-gate` job fails on a call to `Skip`, `Skipf` or `SkipNow` on any receiver, on the go-specs `SkipIt`, `PendingIt` and `FIt` (on a `Spec` or a `Builder`; `FIt` focuses one case, so every other case would be skipped), and on `testing.Short`, in any Go file under `inttest/`. No allowlist can excuse it.
- A nested module is opt-in by directory, not by build tag. The root `go test ./...` never reaches it, and `cd inttest && go test ./...` always runs everything in it.

### Run them locally

You need Docker and nothing else. No database, no environment variable:

```sh
cd inttest && go test -count=1 ./...
```

The first run pulls the images. Without Docker the run fails; it does not skip.

### Add an infra helper

For a new system (Kafka, NATS or Pulsar are the planned ones), add a package `inttest/infra/<backend>`:

1. Write `StartX(ctx) (*X, error)` on top of the Testcontainers module for that system. It takes no `testing.TB`, because `TestMain` has none, and returns an error that says Docker may be the problem.
2. Pin the image to an exact tag, never `latest`, and use the module's readiness wait strategy so the function returns only when the system accepts connections.
3. Give the handle a `Terminate(ctx)` and a per-test isolation method in the style of `NewDatabase` (a database, a topic or a subject with a unique name, removed in `t.Cleanup`).
4. In the flow package under `inttest/flows/<area>`, start it once in `TestMain`, terminate it after `m.Run`, and have every test call `t.Parallel()` and take its own isolated resource. One container per package, never one per test.
5. Use go-specs and `Eventually` for anything asynchronous. No `time.Sleep`.

### When CI runs them

The `inttest` job runs in these cases:

- on every push to `develop`, so the result of each merge is tested;
- on the `develop` to `main` release pull request. It is the integration gate of `main`: the release pull request requires `ci-ok`, so a red integration run blocks the release;
- on `workflow_dispatch`.

Feature pull requests to `develop` and `hotfix/*` pull requests to `main` never run it, so the everyday pull request stays fast. A breaking change from a feature branch shows up on the push to `develop` that follows its merge, and is caught at the latest by the next release pull request. A hotfix reaches `main` without the job. The `main` to `develop` sync pull request that the pipeline opens after the release brings it back to `develop`, and the push that merges that pull request runs the job. `ci-ok` accepts the skipped job. The job has no `services:` section: `ubuntu-latest` already has Docker and the tests start what they need.

Leaving the job out of feature pull requests is a deliberate trade-off. The changes most likely to break integration, such as `persistence/postgres` or `internal/engine/eventsource`, arrive through feature pull requests. Their authors do not see the failure before merging, so `develop` can go red after a merge. The rule for that case: when `inttest` fails on a push to `develop`, the author of the merged pull request fixes it, or reverts the merge, before the next merge to `develop`. To get the signal earlier, run the suite locally before merging (`cd inttest && go test -count=1 ./...`, only Docker needed) or start the `ci` workflow by hand with `workflow_dispatch`.

## Examples

The programs under `example/` are written to be copied, so they live in their own Go module and use only the public API. That one module is `example` (`github.com/getsyntegrity/urd/example`). Its `replace` directives point at `../` and `../persistence/postgres`, so it always builds against the working tree. It holds `durablestate`, `eventssourced`, `saga` and the Kubernetes `cluster` example. The cluster example brings `pgx`, OpenTelemetry and the Kubernetes client into the module's `go.mod`. Go only compiles what each `main` package imports, so the simple examples do not link them. The examples import the protobuf messages from `example/examplepb`, not from `internal/`, because a user who copies an example cannot import an internal package. `example/examplepb` is generated from `protos/sample/sample.proto` with `buf.gen.example.yaml`, which only overrides `go_package`; the root tests keep their own copy in `internal/samplepb`, and `make proto` regenerates both. No binary links both copies, because the same proto file registered twice would conflict.

The examples are compiled, never executed. The `modules` job builds, vets and tests the `example` module (it has no tests of its own) on every pull request with Go changes, feature pull requests included. This is the opposite of `inttest`, which stays out of feature pull requests. The reason is cost: compiling the examples takes seconds and needs no Docker, and a feature pull request that breaks an example should fail before the merge, not after it. Dependabot and the `tidy` job cover the module.

To check them locally:

```sh
cd example && go build ./... && go vet ./... && go test ./...
```

To run one, use `make run-eventsourced`, `make run-durablestate` or `make run-saga` from the root; they run `go run` inside `example/`.

## The release note block

Every pull request body contains a fenced block:

````
```release-note
Adds the `WithTimeout` option to the client.
```
````

Write the note as a consumer of the library would want to read it. Write `NONE` when there is no user-visible change. If consumers must act when upgrading, include the words `action required`: the note is then also listed under "Urgent Upgrade Notes". `pr-meta` fails when the block is empty. Pull requests labelled `skip-changelog` or `kind/deps`, and the `develop` to `main` release pull request itself, are exempt.

These notes are the release notes. `.github/scripts/changelog.sh` collects them from every pull request merged since the previous tag, groups them by the pull request's `kind/*` label, adds a dependency section from the `go.mod` diff, and produces the text of the GitHub Release.

## Versions and labels

The next version is computed by `.github/scripts/next-version.sh` from the previous stable tag reachable from `main`:

1. A `release:major`, `release:minor` or `release:patch` label on the pull request wins.
2. Otherwise a `hotfix/*` branch is a patch and a `develop` branch is a minor.
3. Any other source is a patch and prints a warning.

With no tag yet, the first release from `develop` is `v0.1.0`. The module path is `github.com/getsyntegrity/urd` without a `/vN` suffix, so it can only publish `v0.x` and `v1.x`. A `release:major` bump to `v2.0.0` or higher fails in `flow` until `go.mod` declares `/v2` (Go's semantic import versioning). The `kind/*` labels (`kind/feature`, `kind/bug`, `kind/breaking`, `kind/deprecation`, `kind/deps`, `kind/chore`, `kind/docs`, `kind/flake`) decide the CHANGELOG section. `.github/scripts/labels.sh` creates all labels.

## What happens on merge to main

`.github/workflows/release.yml` runs on every push to `main`:

1. It finds the pull request that produced the commit and reads its source branch and labels.
2. It computes the version with `next-version.sh` (or reuses the tag if the commit already has one, which makes re-runs safe).
3. It creates the annotated tag and pushes it.
4. It generates the release notes with `changelog.sh` and publishes the GitHub Release with GoReleaser. The library builds no binaries (`.goreleaser.yaml` has `builds: skip`).
5. The `changelog` job opens, or updates, a `docs/changelog` pull request to `develop` that writes `CHANGELOG/CHANGELOG-X.Y.md`. It cannot push to `develop` directly because of branch protection.
6. For a hotfix, the `sync_develop` job opens a `main` to `develop` pull request.
7. The `notify` job posts to Slack if it is configured.

Releases never run in parallel (`concurrency: release`, without cancellation).

## Other workflows

- `security.yml` runs CodeQL and a strict `govulncheck` on pushes to `develop`, on a nightly schedule and on demand. It warns; it does not block pull requests.
- `go-sdk-update.yml` is manual. Run it from the Actions tab with a Go version (or `latest`). It runs `.github/scripts/go-sdk-update.sh`, which rewrites `.go-version` and the `toolchain` line of every `go.mod`, and opens a pull request to `develop`. Tick `raise_min` only when the minimum Go version for consumers should also move.
- `.github/dependabot.yml` opens weekly Go dependency pull requests (root and every nested module) and monthly GitHub Actions updates, all against `develop` and labelled `kind/deps`.

## Optional secrets and variables

Nothing below is required for the pipeline to work.

- `ORG_CHECKOUT_TOKEN` is a personal access token. Workflows use `secrets.ORG_CHECKOUT_TOKEN || github.token`. Without it, the pipeline still works, but pull requests it creates (changelog, hotfix sync, Go SDK update) are made with the default token, and GitHub does not start workflows for events caused by that token. Those pull requests then need a manual re-run or a push to trigger `ci-ok`. The repository setting "Allow GitHub Actions to create and approve pull requests" must be enabled for them to be opened at all.
- `SLACK_BOT_TOKEN` and the repository variable `SLACK_CHANNEL` (for example `#urd-releases`) enable Slack notifications for releases and the nightly security scan. If either is missing, the notify step is skipped silently.

## Manual setup that the repository still needs

These steps live in GitHub settings, so no commit can do them:

1. Create the `develop` branch from `main` and make it the default branch if you want pull requests to target it by default.
2. Protect `main` and `develop` and require the status checks `ci-ok` and `pr-meta`. Because the release tag is pushed with the workflow token, keep "Restrict who can push" compatible with GitHub Actions.
3. Run `.github/scripts/labels.sh` once (needs an authenticated `gh`) to create the `kind/*`, `release:*`, `skip-changelog` and `needs-triage` labels.
4. Optionally add the secrets and variable described above.

## Local equivalents

The Makefile targets `docker-lint`, `docker-test`, `docker-mock` and `docker-protogen` run inside `Dockerfile.ci` and are meant for contributors who do not have the toolchain installed. To check a change like the CI does, run `go build ./... && go vet ./... && go test ./...` in the root and in each nested module, `go mod tidy` in each, and `golangci-lint run`. The exception is `inttest`, whose tests need Docker (run `cd inttest && go test -count=1 ./...` there). To preview the next version: `.github/scripts/next-version.sh develop release:minor` (it needs the tags of the repository).
