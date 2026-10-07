# I-00 — Baseline of `develop` and gaps (#346)

Evidence collected on 2026-10-06 (historical baseline), updated on
2026-10-07 (first update, `4c66286`) and refreshed on 2026-10-07 against
`develop` `01da644` (this update). Epic: #345. This change edits documents
only; it changes no production code, test or `go.mod`.

**Status: baseline validated on three SHAs. B4 is fixed (#430, #427 closed).
The adoption finding (#428) is fixed (#429; snapshot and durable-state
adoption moved to #435). B2 stays open as a coverage gap. #346 can close as an
evidence gate once the open items under "Status of the #346 criteria" are
accepted or moved to their own issues.**

## Refresh on `develop` `01da644` (2026-10-07)

| Item | Value |
| --- | --- |
| `develop` SHA | `01da644a9515355ed8c21de90d88566407ac2fcc` (#433), on top of #426, #429, #430, #431, #432 |
| Toolchain used | `go1.27.0 darwin/arm64`. The "Go 1.26" in the text of #346 was superseded by #426 (all ten modules declare `go 1.27.0`; the root `go.mod` was read on this SHA). |
| Dependency graph (root module) | `go list -deps ./... \| wc -l` gives 523 lines. `go list -deps ./... \| grep -c '^github.com/getsyntegrity/urd/'` gives 43 packages of the root module (module path `github.com/getsyntegrity/urd`). Recomputed on this SHA with go1.27.0. For comparison, `4ebdc3d` under Go 1.26 recorded 512 lines and 43 packages. |
| Module and PostgreSQL results | CI run 37635241381 (workflow `ci`, `push` to `develop`, head SHA `01da644a9515355ed8c21de90d88566407ac2fcc`, conclusion success, read with `gh run view`). Green jobs: `test (shard 0)`, `test (shard 1)`, `race`, `cluster`, `architecture`, `tidy`, `inttest`, `test (min)`, and `modules` for `benchmark`, `example`, `inttest`, `persistence/postgres`, `publisher/{kafka,nats,pulsar,websocket}` and `test/compat`; `lint` and `api` show as skipped (no result). In the `inttest` job log, `inttest/flows/{eventstore,restart,tenancy}` and `inttest/infra/postgres` report `ok`. |

What was not re-run locally for this refresh: the module test suites and the
`inttest` lane (evidence is the CI run above, not a local run); the earlier
local PostgreSQL results below are from `4c66286` and are kept as history. The
run's log was read for the `ok` lines of the `inttest` packages only; per-test
names for the `cluster` job were not visible in the log that was read.

### Findings of the previous update, now resolved

| Finding | State on `01da644` | Evidence read |
| --- | --- | --- |
| B4, actor name collisions (#427, closed) | fixed by #430 | `engine/actor_binding.go` defines `ErrSpawnIdentityMismatch` and `ActorIdentityError` (`Unwrap` returns the sentinel); `engine.WithActorNamespace` exists (`engine/option.go:590`). `TestBaseline346` case B4 now asserts `SpawnDurableState` and `SpawnSaga` on an event-sourced ID return `ErrSpawnIdentityMismatch` and leave the first actor unchanged. |
| TenantAdopter on PostgreSQL (#428, closed) | fixed by #429 | `TestAdoptionOfLegacyDataFailsOnPostgres` no longer exists; `inttest/flows/tenancy/adoption_test.go:50` is `TestAdoptionOfLegacyDataRecoversOnPostgres` (adopt 1 aggregate, 4 rows, recover balance 15 at revision 2 in single-tenant mode, continue to revision 3). Its result is `ok` in the `inttest` job of CI run 37635241381 (package `flows/tenancy`). |

The table "Update on current `develop` (2026-10-07)" below and the sections
that describe the failure of #428 and the B4 collision are the **history** of
`4c66286`. Their statements about those two findings are superseded by the
table above.

## Previous update on `4c66286` (2026-10-07, history)

#426 is merged: the ten modules declare `go 1.27.0`. The sections below the
"Baseline" heading are the **historical** results, obtained on `4ebdc3d` with
Go 1.26.0 / 1.26.2; they were not re-run on 1.27 and are not rewritten. The
results of this update were obtained on the branch rebased onto `develop`
`4c66286` (#426), with `go1.27.0 darwin/arm64` and PostgreSQL 17.6 in a real
container (Testcontainers over Colima).

| Check | Command | Result |
| --- | --- | --- |
| Build, vet, tests per module (10 modules) | `go build ./... && go vet ./... && go test -count=1 ./...` in each module | pass; root 40 packages `ok`; `example` has no test files |
| PostgreSQL integration | `cd inttest && go test -count=1 ./...` with `DOCKER_HOST` set to the Colima socket | `flows/eventstore`, `flows/restart`, `flows/tenancy`, `infra/postgres` `ok` |
| B2/B4 characterization | `go test -count=1 -race -run TestBaseline346 ./engine` | pass |
| Adoption of legacy data on PostgreSQL | `TestAdoptionOfLegacyDataFailsOnPostgres` (`inttest/flows/tenancy`) | the adopter failed (#428); replaced by `TestAdoptionOfLegacyDataRecoversOnPostgres` after #429 |

Without `DOCKER_HOST`, the `inttest` packages fail at start ("rootless Docker
not found"): that is a local Docker socket issue, not a test result. These
tests run in the `inttest` lane of CI, not in the feature/hotfix PR lane; this
PR adds no job to it.

Finding #428 (fixed by #429; kept as history): `migration.TenantAdopter` cannot read legacy events from
`postgres.EventStore`. It replays with `maxReplaySequence = math.MaxUint64`
(`migration/tenant_adoption.go:58`) and the sequence column is `int8`, so the
query fails to encode its argument. The in-memory adoption test passes, which
is why it went unseen. Not fixed here. The characterization test asserts
today's behaviour (nothing copied, legacy rows untouched and still
recoverable) and becomes the single-tenant recovery check once #428 is fixed.

## Baseline (historical: `4ebdc3d`, Go 1.26)

| Item | Value |
| --- | --- |
| `develop` SHA | `4ebdc3d249254a411572f90db6372e585dd88ac5` (#423) |
| Go (root module) | `go1.26.0` (root `go.mod` declares `go 1.26.0`) |
| `go build ./...`, `go vet ./...` (root) | pass |
| `go test -count=1 ./...` (root) | exit 0, 40 packages `ok` |
| `go list -deps -f ... ./...` (root) | 512 lines; 43 packages in the root module |
| Module builds on 1.26.0 | root, `persistence/postgres`, `publisher/{kafka,nats,websocket}` pass; `publisher/pulsar` fails (`go.mod requires go >= 1.26.2`) |
| Module builds on 1.26.2 | all ten modules build with no error output |

Modules and their `go` directive: root, `benchmark`, `example`, `inttest`,
`persistence/postgres`, `publisher/{kafka,nats,websocket}` declare `1.26.0`;
`publisher/pulsar` and `test/compat` declare `1.26.2`. There is no `go.work`.
`Dockerfile.ci` uses `golang:1.27.0-alpine`; CI reads `.go-version`.

Not verified at that time: the PostgreSQL integration lane and the tests of
the submodules. Both were completed on current `develop`; see the update above.

## GoAkt dependency

`go.mod` pins `github.com/tochemey/goakt/v4 v4.5.7-0.20261001174708-cf9c8659f741`
and replaces it with `github.com/pablogore/goakt/v4 v4.5.7-actorof.1` (the fix
of Tochemey/goakt#1447, marked TEMPORARY).

Read-only inspection APIs on `goakt.ActorSystem` in that fork, as candidates
for #419 (public API presence only; local/remote access not yet checked):
`Metric`, `Actors`, `NumActors`, `ActorOf`, `Peers`, `Running`.

## B2 — `Partition` outside a cluster: not reproduced as a bug; propagation gap open

Scope of the evidence in `TestBaseline346` case "B2": standalone mode with
shard 0 only. That case alone does not show how a non-zero partition
propagates; the cluster test below does for events only.

Call sites in Urd (the only two, checked with `grep -rn "Partition(" internal engine`
excluding tests, on `01da644`):

- `internal/engine/eventsource/event_sourced_actor.go:355`, on `PostStart`
  (`entity.shardNumber = ctx.ActorSystem().Partition(entity.persistenceID)`);
- `internal/engine/durablestate/durable_state_actor.go:169`, during start.

Both store the result in `shardNumber`, which is written into the stored event
envelopes (`event_sourced_actor.go:1323`, `Shard: shard`) and durable-state
`Shard` fields.

In the effective fork, `actorSystem.Partition` returns
`cluster.GetPartition(name)` when `InCluster()` and `uint64(0)` otherwise. It
does not panic or error in standalone mode.

Reproduction: `TestBaseline346`, case "B2" (`engine`). A standalone
engine with in-memory event and state stores: `InCluster()` is false,
`Partition("any-name")` is 0, and an event-sourced and a durable-state entity
each spawn and answer `CreateAccount` with revision 1 and the expected state.
No errors. The test also reads back what was persisted: the stored event and
the stored durable state both carry `Shard == 0`. That does not prove the
shard came from `Partition`: 0 is also the zero value of the field, so it
would read the same if the actors never wrote it.

Correction to the previous revision of this report, which said no test injects
a non-zero partition: `TestClusterEventPublisherHighPartitionCount`
(`engine/publisher_test.go:409`) starts a cluster with `WithPartitionCount(1009)`
and 40 entities, and asserts (lines 526-535) that at least one event delivered
to the cluster publisher has `GetShard() >= 271`. So for **events in a
cluster**, a non-zero value from `Partition` reaching the published event
envelope is covered. Limits of that evidence: it is a `TestCluster*` test (CI
`cluster` job, which was green in run 37635241381; the log read does not show
whether this test ran or took its documented `t.Skip` at `publisher_test.go:483`
when no shard >= 271 appeared within 60 s); it observes the published event,
not the persisted row.

Still not covered by any test found (searched `engine/publisher_test.go`,
`engine/baseline_346_test.go`, `testkit`, `inttest`):

- a non-zero `Shard` on **durable state** (published or stored);
- a non-zero `Shard` on the **persisted** event row (read back from an events
  store), as opposed to the published event;
- a non-zero `Shard` on persisted durable state.

Possible issue, from code reading only, not reproduced or tested: after #430
`bindIdentity` rebinds `entity.persistenceID = entity.behavior.ID()`
(`event_sourced_actor.go:481`, `durable_state_actor.go:209`). In the
durable-state actor, `Partition(entity.persistenceID)` runs right after
`bindIdentity` in `PreStart` (`durable_state_actor.go:166-169`), so it hashes
the behavior ID. In the event-sourced actor `bindIdentity` is called from
`PreStart` (`event_sourced_actor.go:296`, call at line 332) and
`Partition(entity.persistenceID)` runs later, on `PostStart` (line 355), so by
reading it also uses the rebound behavior ID, not `ctx.ActorName()`. That is
consistent for both actors, but it differs from what #346 expected (name-based)
and from the pre-#430 behaviour in multi-tenant engines, where the name is
tenant-qualified: the shard of an existing entity may have changed across the
upgrade. This is from code reading only; no test pins which identity feeds
`Partition`, and the order of the GoAkt `PreStart` and `PostStart` calls was
not observed by running anything. It belongs with the propagation gap, not
with B4.

This propagation gap (does a non-zero partition reach the stored `Shard`, and
from which identity is it computed) is **not** the same as the criterion of
#350, which asks that the slice be stable across 1, 3 and 5 nodes. A
stable-slice test does not cover propagation, and a propagation test does not
show stability. See "#350" in `i-00-epic-345-audit.md`.

Open observation (not a bug claim): outside a cluster the stored shard is 0
for every entity, so any consumer that groups by shard sees one shard. Whether that
matters for `GetShardEvents` consumers is not verified here.

B2 is independent of tenancy: single-tenant with cluster and multi-tenant
without cluster are both possible. This test covers the no-resolver
standalone case only; the other combinations are not tested here.

## B4 — actor name collisions: fixed by #430 (#427 closed)

Status on `01da644`: **fixed.** The reproduction below is the behaviour on
`4ebdc3d` and `4c66286` and is kept as history.

What #430 added (read in code):

- `ErrSpawnIdentityMismatch` and the typed `ActorIdentityError`
  (`engine/actor_binding.go`); `errors.Is` matches the sentinel through `Unwrap`.
- `engine.WithActorNamespace(namespace)` (`engine/option.go:590`), so engines
  that share one `ActorSystem` can use distinct, explicit address namespaces.
- `bindIdentity` makes `behavior.ID()` the persistence ID instead of the actor
  name (`event_sourced_actor.go:481`, `durable_state_actor.go:209`), so a
  qualified actor name no longer changes journal keys; #433 adds a test that
  persisted keys survive namespace addressing.

Tests that exist now:

- `TestBaseline346` case B4 (`engine/baseline_346_test.go`): one engine, one ID;
  `SpawnDurableState` and `SpawnSaga` return `ErrSpawnIdentityMismatch`, the
  name still resolves to the same running event-sourced actor, a command is
  handled as event-sourced and the durable store stays empty.
- `TestSpawnVerifiesDefinition` (`engine/actor_binding_test.go:22`): the same
  family with a different definition is rejected with `*ActorIdentityError`
  and the original actor is untouched.
- `TestActorNamespacesOnSharedSystem` (`engine/actor_binding_test.go:49`):
  **a two-engines-one-ActorSystem case exists, for distinct namespaces.**
  Engine `a` (`WithActorNamespace("accounts")`) is started, engine `b`
  (`"payments"`) is created over `a`'s actor system with `NewEngine`; the same
  ID `shared-id` is spawned as event-sourced on `a` and as durable-state on
  `b`; both handle `CreateAccount` independently at revision 1, the persisted
  IDs stay `shared-id`, and the two actor names differ.
- Not found: a test of two engines on one `ActorSystem` with the **same**
  namespace (or with none) and the same ID, asserting
  `ErrSpawnIdentityMismatch`. `engine_tenant_actor_identity_test.go` has five
  tests (two tenants under one ID, cross-tenant access rejected, single-tenant
  names, restart per tenant, stopping one tenant's actor); none creates a
  second engine on a shared system.

### Target solution (history; implemented by #430 except as noted)

- The actor name must distinguish scope, actor family and logical definition.
  When several engine instances share one `ActorSystem`, a stable, explicit
  namespace is required.
- Reusing an existing actor must verify its identity and return a typed error
  on incompatibility.
- Renaming actors must not silently change journal keys, snapshots or any
  persisted identity: `persistenceID` is now decoupled from the actor name for
  event-sourced and durable-state actors, and `sagaID` is rebound to
  `behavior.ID()` in `saga_actor.go:218` (read, not run).

### Reproduction on `4ebdc3d` / `4c66286` (history)

Actor names came from `engine.actorName(tenantID, id)`. Without a tenant
resolver, or with a fixed single-tenant resolver, the name is the bare entity
ID. With one engine and one ID, `SpawnDurableState(id)` and `SpawnSaga(id)`
returned nil, the name kept its first holder (the event-sourced actor), a
command was handled as event-sourced and the durable store stayed empty. The
characterization test asserted that behaviour and #427 was to flip it; it did.

## Go version

Decision of the owner: the common minimum is **Go 1.27.x** (first proposed as
1.26.2, then changed). #426 is merged: the ten modules declare `go 1.27.0`,
and this refresh used `go1.27.0`. This change does not touch any `go`
directive.

## Tracker review (#345 and its tasks)

All 79 issues from #345 to #424 were open on 2026-10-06; none was closed or
partially closed in the tracker. The tracker was not re-queried for this
refresh (only #427 and #428 are known to be closed, from the task statement
and from the merged PRs #429 and #430). Checked against `develop` by searching
non-test Go code for the signature of each area:

| Area (issues) | Found in code | Reading |
| --- | --- | --- |
| xid8 horizon (#352), `ScopeRouter` and cells (#368, #369), `DedicatedCell`/pool policies (#390–#394) | no symbol | pending |
| Per-tenant quotas (#380), tenant delete (#382) | no symbol (entity erase exists, see below) | pending |
| `ReadSideProcessor` (#366), parked entities (#356, #375) | no symbol | pending |
| Outbox and relay (#399–#403) | no symbol | pending |
| Offsets keyed by full identity (#362) | after #431 `offsets_store` has `tenant_id` (migration `006_scoped_offsets.sql`, primary key `(tenant_id, projection_name, shard_number)`); no processor/version identity | partial; see below |
| Rebuild per tenant (#381) | `Engine.RebuildProjection` exists; after #431 it resets through `offsetstore.ForScope(offsetStore, scope)` using the scope registered for that projection (`engine/projections.go:199-220`) | partial; see below |
| Wake after commit (#371) | `internal/engine/projection/wake_stream.go` exists | partial; not verified against #371's scope |
| Tenant context (#379) | `tenancy/` package and propagation exist | baseline exists; the audit is the task |
| Sagas (#409–#413) | public API and persisted saga events exist | partial, see below |

This table is a code-signature check. A missing symbol is not proof that a
capability is absent, and a present one does not prove a criterion. For four
tasks the acceptance criteria were read against the code (2026-10-07, rows for #381 and #362 re-read on `01da644` after #431):

| Task | Criterion | Evidence | State |
| --- | --- | --- | --- |
| #381 rebuild per tenant | rebuild keeps other tenants' offsets | `RebuildProjection(ctx, name, from)` keeps its signature but looks up the scope registered for `name` and resets through `offsetstore.ForScope` (`ResetScopedOffset`); the reset is limited to that scope. `TestScopedOffsetsPreserveLegacyAndIsolateResetOnPostgres` (`inttest/flows/tenancy/scoped_offsets_test.go`, store level, `ok` in CI run 37635241381) shows a reset of tenant `acme` leaving another tenant and the Unscoped cursor intact. No engine-level test of a rebuild with two scopes was found (the only `RebuildProjection` test in `engine/engine_test.go` covers error guards). The caller still cannot choose a scope per call | partially met (store level); engine level not shown |
| #362 offsets keyed by full identity | `ResetOffset` receives the full identity | the key now includes the tenant (`tenant_id`, `projection_name`, `shard_number`); no processor, version or full scope identity beyond the tenant | partially met |
| #379 tenant context | three modes validated | single-tenant, legacy and multi-tenant paths covered by `TestConformance_W7_*` over PostgreSQL (passing); remote propagation and the #305 comparison not checked | partially verified |
| #371 wake after commit | conformance with notifications off; p50 before/after | `wake_stream.go` exists; no measurement or conformance run | not verified |

The other 76 linked tasks are audited criterion by criterion, in two passes,
in `i-00-epic-345-audit.md` (audited on `4c66286`, 2026-10-07; rows touched by #429-#433 re-read on `01da644`, state counts not recomputed): 431 criteria
(one count, reproducible with the command in that file): 23 cumplido (17
negative criteria met because nothing was built, 6 demonstrated), 88 parcial,
240 no implementado, 6 no verificado, 74 bloqueado, each blocked row naming
its dependency. By kind (re-derived on `01da644`): 13 current defects (the #416 raw-reset
row was reclassified after #431; 14 on `4c66286`), 395 new capabilities, 17
negative, 6 met.

## Inventory for #395–#398

Read from code only; no test was run for this section. Classification:
**keep** (reuse as is), **migrate** (exists but must move or change shape),
**needs contract** (missing or undefined).

### #395 Durable publication and outbox

| Capability | State | Evidence | Class |
| --- | --- | --- | --- |
| `EventPublisher` / `StatePublisher` contract (ID, Publish, Close) | exists | `port/publishing/publishing.go:36-112` | keep |
| Delivery semantics stated on the port | no; only "responsible for ensuring delivered" | `port/publishing/publishing.go` | needs contract |
| Engine publish loop | partial: in-memory fan-out | `engine/streams.go:456-501` | migrate |
| Retry or backoff on publish failure | no: failure is logged and the event is skipped for that publisher | `engine/streams.go:486-493` | needs contract |
| Outbox, relay, dedupe | no | not found | needs contract |
| Drop on tenant failure, logged and counted | exists | `engine/streams.go:105-115` | keep |
| Kafka, NATS, Pulsar, WebSocket adapters | exist; Kafka producer is idempotent | `publisher/*` | keep |
| Publisher conformance (PT-1 to PT-4) | partial: no failure, retry or ordering checks | `port/publishing/publishingtest/publishingtest.go` | keep, extend |
| Durable, retried feed with committed offset | exists in the projection runner, not in the publisher path | `persistence/events_store.go:169-176`, `internal/projectionrunner/runner.go` | keep (basis for outbox over projection) |
| Projection retry and dead-letter handler | exist | `internal/projectionrunner/retry.go`, `projection/deadletter.go` | keep |

Caveat: `persistence/events_store.go:169-172` says a timestamp cursor does not
close a commit-order gap, so a late commit can be missed. That bears on any
outbox built on `GetShardEvents` and is the subject of #352.

### #396 Testkit

| Capability | State | Evidence | Class |
| --- | --- | --- | --- |
| In-memory event, snapshot, durable-state, offset and key stores | exist | `testkit/*.go` | keep |
| Given/when/then scenarios (event-sourced, durable-state) | exist | `testkit/scenario.go` | keep |
| Store conformance (events, state, snapshot, schema) | exist | `persistence/conformance/` | keep |
| Offset-store conformance | not found | `persistence/conformance/` | needs contract |
| Publisher conformance | exists (see above) | `publishingtest` | keep, extend |
| Saga, projection or publisher scenarios; read-side driver | no | `testkit/` | needs contract |
| Fault injection (failing store, publisher, clock) | no in the public testkit | not found | needs contract |
| Internal mocks, failing behaviour, fake clock | exist but internal | `internal/engine/enginetest/`, `internal/projectionrunner/clock.go` | migrate |

### #397 Sagas

| Capability | State | Evidence | Class |
| --- | --- | --- | --- |
| Public spawn and status (`SpawnSaga`, `SagaStatus`) | exists | `engine/sagas.go`, `port/runtime/runtime.go` | keep |
| Saga persists its events and rebuilds on start | exists | `internal/engine/saga/saga_actor.go` | keep |
| Status after restart | not persisted: reports running again (documented in `engine/sagas.go`) | `engine/sagas.go:140-150` | needs contract |
| Timeout rescheduled on every start | observed in code; effect on restart not verified | `saga_actor.go:274-296` | needs contract |
| Event intake | live stream only; no catch-up from the store seen | `saga_actor.go:250-263` | needs contract |
| List or cancel sagas | no | `engine/sagas.go` | needs contract |
| Retry, command acknowledgement, dedupe | not verified | | needs contract |

### #398 Operational control

| Capability | State | Evidence | Class |
| --- | --- | --- | --- |
| Start, stop, is-running, rebuild, lag of a projection | exist | `engine/projections.go` | keep |
| Pause and resume of a projection | no (the `Resume` hits are GoAkt supervision and store tests) | | needs contract |
| Erase entity, tenant-scoped, fails closed | exists | `engine/entities.go:308-345` | keep |
| Metrics through an OpenTelemetry `Meter` | exists; no query API | `engine/telemetry.go` | keep |
| Controls for a failed publisher; authorization and audit; admin endpoint | not found | | needs contract |

Package boundaries for the new areas are proposals, not implemented work.

## #419 GoAkt inspector feasibility

Version audited: `github.com/pablogore/goakt/v4 v4.5.7-actorof.1`, which
replaces `tochemey/goakt/v4 v4.5.7-0.20261001174708-cf9c8659f741`. Read from
the fork source; nothing was run.

Main finding: the remote introspection client (`RemoteLookup`, `RemoteMetric`,
`RemoteChildren`, `RemoteState`, `RemoteStashSize`) lives in `internal/remoteclient`
of GoAkt, which an external module cannot import. The public `client.Client`
exposes `Kinds`, `Spawn`, `SpawnBalanced`, `ReSpawn`, `Tell`, `Ask`,
`AskGrain`, `TellGrain`, `Stop`, `Exists`, `Reinstate` and no list, metric,
children or state call. So every public introspection API needs a live
`actor.ActorSystem` in the calling process.

| Capability | Public | Scope |
| --- | --- | --- |
| `Actors`, `ActorOf`, `ActorExists` | yes | local; remote lightweight PIDs when clustered |
| `NumActors`, `Metric` (dead letters, actors, uptime, memory) | yes | local node only |
| `Peers`, `InCluster`, `IsLeader` | yes | cluster membership |
| Per-PID `Metric`, children, running/suspended, stash size | yes | local; remote through the internal client |
| Lifecycle and cluster events via `Subscribe` | yes | local stream only |
| Mailbox size | no accessor found | |
| Dead-letter list | counts only; the dead-letter actor is unexported | |
| `RemoteLookup`, `RemoteStop`, `RemoteReSpawn` on a PID | yes, needs an existing PID | remote; `ErrRemotingDisabled` without remoting |

What a read-only attach can use today without private access: a collector
embedded in the target process (with the `ActorSystem` handle), and
`Kinds`/`Exists` through `client.Client`. A standalone console process can
only observe by joining the cluster as a node, which is not read-only.

Would need a GoAkt change: an exported read-only remote introspection client,
a remote "list actors" and "node metric" call, a mailbox size accessor,
dead-letter inspection, and a remote event-stream subscription. Not verified:
`remote.Peer` fields and passivation-strategy visibility for remote PIDs. On the claim
that no API was found marked unsupported or deprecated: a probe was run on
2026-10-07 against the fork source in the module cache
(`go list -m -f '{{.Dir}}' github.com/tochemey/goakt/v4` resolves to
`pablogore/goakt/v4@v4.5.7-actorof.1`), using
`grep -rniE "^\s*//\s*Deprecated" . --include='*.go'` excluding tests. It
found `Deprecated:` notices only on grain-factory functions (`GrainOf`
replacements), the options `WithPartitionHasher` and `WithTLS`
(`actor/option.go:152` and `:210`), the error `ErrSingletonAlreadyExists`
(`errors/errors.go:208`), and the Kubernetes discovery config. None is on the
introspection APIs listed above (`Actors`, `ActorOf`, `NumActors`, `Metric`,
`Peers`, `InCluster`, `IsLeader`, `Subscribe`). The probe covers doc-comment
deprecation only; a grep for "unsupported" found no API marked that way, only
error text and a memory stub. It does not show the APIs are stable or
supported beyond that, and I did not read each API's godoc individually, so
"no API marked unsupported or deprecated" holds for these greps and is
otherwise unverified.

## Single tenant (#424)

Read from code and tests. The tests named below were run locally on 2026-10-07 on `4c66286`; the PostgreSQL ones were also green in CI run 37635241381 on `01da644`. Three modes
(`engine.WithTenantResolver`, first registration wins; a second non-nil one is
`ErrAmbiguousTenantResolver`):

| | No resolver (legacy) | Fixed (`tenancy.WithSingleTenant`) | Dynamic resolver |
| --- | --- | --- | --- |
| Actor name | bare ID | bare ID | qualified (`internal/actoridentity`) |
| Store scope | `Unscoped()`, `tenant_id = ''` | tenant scope, e.g. `tenant_id = 'solo'` | tenant scope per spawn |
| Spawn without `WithTenant` | works, no tenant injected | works, falls back to the fixed tenant | `ErrSpawnTenantUndetermined` |
| Projection scope | `Unscoped()` | the fixed tenant; another scope is rejected | must be declared; `Unscoped()` rejected |
| Publication scope | `Unscoped()` | the fixed tenant; mismatch is an error | undetermined unless registered per tenant |
| `EraseEntity` | `Unscoped()` | tenant scope | tenant scope |

(`engine/actor_identity.go`, `engine/spawn_tenancy.go`, `engine/streams.go`,
`engine/projection_scope.go`, `engine/entities.go`.)

Compatibility with existing Unscoped data: **not automatic.** Unscoped and
tenant scopes are separate keyspaces (`tenant_id` is part of the primary key
of `events_store` and `events_store_revisions`). A `WithSingleTenant` engine
does not see legacy Unscoped rows; an existing aggregate restarts empty unless
`migration.TenantAdopter` ran first.

- Proven (passing): `TestConformance_W7_LegacyDataIsNotSeenByATenant`
  (`inttest/flows/tenancy/conformance_test.go`, PostgreSQL), and
  `TestTenantAdopterEndToEndRecoveryThroughRealActor`
  (`migration/tenant_adoption_test.go`, `testkit.EventsStore` only).
- Proven (passing, `01da644`): adoption over PostgreSQL recovers.
  `TestAdoptionOfLegacyDataRecoversOnPostgres` (replacing
  `TestAdoptionOfLegacyDataFailsOnPostgres`, #429) adopts a legacy aggregate
  into tenant `acme`, finds 4 rows (2 Unscoped, 2 `acme`), recovers balance 15
  at revision 2 in single-tenant mode and continues to revision 3 without
  touching the Unscoped rows.
- Not covered: snapshot and durable-state adoption. Moved to #435 (#428 is
  closed); the adopter's test above covers the event journal only. Whether
  `MIGRATION.md` guidance exists or is correct was not verified.
- Offsets: the earlier gap "`offsets_store` has no tenant column" is
  **closed** by #431. `persistence/postgres/schema/006_scoped_offsets.sql` adds
  `tenant_id TEXT NOT NULL DEFAULT ''`, keeps old rows under `''` (Unscoped)
  and makes the primary key `(tenant_id, projection_name, shard_number)`;
  `OffsetStore` implements `offsetstore.ScopedOffsetStore`; the projection
  runner binds `offsetstore.ForScope(x.offsetsStore, x.scope)`. The migration
  upgrade is exercised by `TestScopedOffsetsPreserveLegacyAndIsolateResetOnPostgres`.
  Remaining: no offset migration was found for a *switch of mode*
  (Unscoped cursor to a tenant cursor): by design old offsets stay Unscoped
  and a tenant starts at 0, which is a documented choice in the SQL comment,
  not a copy. Whether that is acceptable for a single-tenant adopter is a
  question for #424/#435, not verified here.

## Status of the #346 criteria

State on `develop` `01da644` (go1.27.0):

| Criterion | State |
| --- | --- |
| Record the SHA, build with the common Go version, get the `go list` graph | done on `01da644` with go1.27.0: 523 lines, 43 root-module packages (`go list -deps ./...`, command above). The Go 1.26 wording of the issue was superseded by #426. Earlier SHAs kept as history |
| Confirm or discard B2 and B4 | B4 reproduced, then fixed by #430 (#427 closed); two-engines-one-system exists for distinct namespaces only. B2 not reproduced as a bug; events in a cluster reach a non-zero `Shard` (`TestClusterEventPublisherHighPartitionCount`); durable-state and persisted-row propagation untested |
| Review each task against code and tracker | done on `4c66286`; rows for #362, #381, actor identity and adoption re-read on `01da644`; counts not recomputed |
| Audit publishers, sagas, testkit and controls for #395-#398 | done from code on `4c66286`; not exercised by tests; not re-read in this refresh |
| Fix the GoAkt version and fork for #419 | done; the "no unsupported API" statement is backed by a grep (see #419) with its limits |
| Single-tenant inventory (#424) | done; PostgreSQL adoption recovers (#429); the offsets tenant-column gap is closed (#431) |
| Module tests and PostgreSQL integration | done: CI run 37635241381 on `01da644`, all listed jobs green |
| Recovery after adopting legacy data to single-tenant | demonstrated for the event journal on PostgreSQL (`TestAdoptionOfLegacyDataRecoversOnPostgres`); snapshots and durable state are #435 |

Open items after this refresh (none is a regression introduced by it). The maintainer accepted closure of #346 as an evidence gate with these gaps tracked separately: B2/B4 coverage in #438, PostgreSQL snapshot/state adoption in #435, and per-scope rebuild evidence in #381/#362. This does not mark those capabilities or tests as delivered:

- B2 propagation gap: a test that a non-zero partition reaches the stored
  `Shard` of durable state and of persisted events, and a decision on which
  identity feeds `Partition` after #430. No acceptance criterion in #350 or
  elsewhere asks for it (#350 asks for slice stability across 1, 3 and 5
  nodes, a different guarantee); a stable-slice test would not cover it;
- no test of two engines on one `ActorSystem` with the same namespace and the
  same ID asserting `ErrSpawnIdentityMismatch`;
- snapshot and durable-state adoption (#435);
- an engine-level test that a per-scope rebuild leaves another scope's offsets
  alone (the store-level test exists);
- the 13 current defects and the 74 blocked criteria of the epic audit, which
  belong to their own issues, not to this PR (counts from `4c66286`);
- limits of the audit: static reading plus targeted tests, so an equivalent
  implementation under an unsearched name may exist. This refresh did not
  re-run the test suites locally; CI run 37635241381 is the evidence.

`no implementado` on criteria of new guarantees is the expected baseline, not
a defect.
