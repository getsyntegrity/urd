# I-00 — Baseline of `develop` and gaps (#346)

Evidence collected on 2026-10-06 (historical baseline) and updated on
2026-10-07 (current `develop`). Epic: #345. This change adds this report and
characterization tests; it changes no production code.

**Status: baseline validated on both SHAs; two findings recorded (#427, #428);
#346 stays open** (see "Status of the #346 criteria").

## Update on current `develop` (2026-10-07)

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
| Adoption of legacy data on PostgreSQL | `TestAdoptionOfLegacyDataFailsOnPostgres` (`inttest/flows/tenancy`) | the adopter fails: see #428 |

Without `DOCKER_HOST`, the `inttest` packages fail at start ("rootless Docker
not found"): that is a local Docker socket issue, not a test result. These
tests run in the `inttest` lane of CI, not in the feature/hotfix PR lane; this
PR adds no job to it.

Finding #428: `migration.TenantAdopter` cannot read legacy events from
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

## B2 — `Partition` outside a cluster: not reproduced as a bug

Scope of the evidence: standalone mode with shard 0 only. It does not show how
a non-zero partition propagates.

Call sites in Urd (the only two):

- `internal/engine/eventsource/event_sourced_actor.go:353`, on `PostStart`;
- `internal/engine/durablestate/durable_state_actor.go:167`, during start.

Both store the result in `shardNumber`, which is written into the stored event
envelopes and durable-state `Shard` fields.

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
would read the same if the actors never wrote it. Proving the propagation
would need a test that injects a non-zero partition; none exists.

Open observation (not a bug claim): outside a cluster the stored shard is 0
for every entity, so any consumer that groups by shard sees one shard. Whether that
matters for `GetShardEvents` consumers is not verified here.

B2 is independent of tenancy: single-tenant with cluster and multi-tenant
without cluster are both possible. This test covers the no-resolver
standalone case only; the other combinations are not tested here.

## B4 — actor name collisions: reproduced

Actor names come from `engine.actorName(tenantID, id)`. Without a tenant
resolver, or with a fixed single-tenant resolver, the name is the bare entity
ID; only multi-tenant engines qualify it. Event-sourced, durable-state and saga
actors are spawned into the same GoAkt actor system.

Reproduction: `TestBaseline346`, case "B4". One engine,
one ID:

- `SpawnEventSourced(id)` returns nil;
- `SpawnDurableState(id)` returns nil (no error);
- `SpawnSaga(id)` returns nil (no error);
- `SendCommand(id, CreateAccount)` succeeds with revision 1, the event store
  holds an event for `id`, and the durable store holds no state for `id`.

What the test proves:

- neither later spawn returns an error;
- the name keeps its first holder: `ActorOf(id)` resolves to the
  event-sourced actor (by its type) after the first spawn, and resolves to the
  same actor (same ID, still running, same type) after `SpawnDurableState`
  and `SpawnSaga`. Neither spawn replaced it or registered an actor of its own
  under that name. The check is by name, not by a count of the actors in the
  system, so it does not depend on what else starts or stops there. (An
  earlier version counted actors and needed a wait for the entity's child
  actor; it was replaced because the count is only a proxy);
- a command to the shared ID is handled as an event-sourced entity, and the
  durable store stays empty.

`SagaStatus` is not asserted. An exploratory probe showed that it answers
without error and reports `running` for the shared ID, but a reply with no
saga status also maps to `running` (`saga.StatusFromProto`), so the
event-sourced actor that holds the name gives the same answer. It cannot
tell the two apart, and pinning it would only fix incidental behaviour.

So the collision is silent for both later kinds: no error, and the name stays
with the first actor. `resolveExistingSpawn` only verifies the binding of an
existing actor in the tenant-aware path.

The test asserts today's behaviour. The fix is tracked in #427 and will flip
its assertions.

### Target solution (decided by the owner, tracked in #427, not implemented here)

- The actor name must distinguish scope, actor family and logical definition.
  When several engine instances share one `ActorSystem`, a stable, explicit
  namespace is required. Adding only event-sourced/durable-state/saga kinds
  does not cover all collisions.
- Reusing an existing actor must verify its identity and return a typed error
  on incompatibility.
- Renaming actors must not silently change journal keys, snapshots or any
  persisted identity. Today `persistenceID` is taken from `ctx.ActorName()`
  (`event_sourced_actor.go:313`, `durable_state_actor.go:138`,
  `saga_actor.go:183`), so a name change would change it unless the persistence
  ID is decoupled first. That coupling needs its own decision in the fix.

## Go version

Decision of the owner: the common minimum is **Go 1.27.x** (first proposed as
1.26.2, then changed). All ten modules build on 1.26.2, and on 1.27.0 with the
root module's vet and tests passing. The alignment of module `go` directives,
the readme and CI documentation is a separate PR (#426, draft), which assumes
`1.27.0` as the floor. This PR does not change any `go` directive.

## Tracker review (#345 and its tasks)

All 79 issues from #345 to #424 are open (2026-10-06); none is closed or
partially closed in the tracker. Checked against `develop` by searching
non-test Go code for the signature of each area:

| Area (issues) | Found in code | Reading |
| --- | --- | --- |
| xid8 horizon (#352), `ScopeRouter` and cells (#368, #369), `DedicatedCell`/pool policies (#390–#394) | no symbol | pending |
| Per-tenant quotas (#380), tenant delete (#382) | no symbol (entity erase exists, see below) | pending |
| `ReadSideProcessor` (#366), parked entities (#356, #375) | no symbol | pending |
| Outbox and relay (#399–#403) | no symbol | pending |
| Offsets keyed by full identity (#362) | `offsets_store` has no tenant column (see single tenant) | pending |
| Rebuild per tenant (#381) | `Engine.RebuildProjection` exists, global per projection (`engine/projections.go:185`) | baseline exists; per-tenant part pending |
| Wake after commit (#371) | `internal/engine/projection/wake_stream.go` exists | partial; not verified against #371's scope |
| Tenant context (#379) | `tenancy/` package and propagation exist | baseline exists; the audit is the task |
| Sagas (#409–#413) | public API and persisted saga events exist | partial, see below |

This table is a code-signature check. A missing symbol is not proof that a
capability is absent, and a present one does not prove a criterion. For four
tasks the acceptance criteria were read against the code (2026-10-07):

| Task | Criterion | Evidence | State |
| --- | --- | --- | --- |
| #381 rebuild per tenant | rebuild keeps other tenants' offsets | `Engine.RebuildProjection(ctx, name, from)` stops the projection by name and calls `offsetStore.ResetOffset(ctx, name, ...)` (`engine/projections.go:185`): no tenant parameter | not met |
| #362 offsets keyed by full identity | `ResetOffset` receives the full identity | same call: only `name` | not met |
| #379 tenant context | three modes validated | single-tenant, legacy and multi-tenant paths covered by `TestConformance_W7_*` over PostgreSQL (passing); remote propagation and the #305 comparison not checked | partially verified |
| #371 wake after commit | conformance with notifications off; p50 before/after | `wake_stream.go` exists; no measurement or conformance run | not verified |

The other 76 linked tasks are audited criterion by criterion, in two passes,
in `i-00-epic-345-audit.md` (develop `4c66286`, 2026-10-07): 431 criteria
(one count, reproducible with the command in that file): 23 cumplido (17
negative criteria met because nothing was built, 6 demonstrated), 88 parcial,
240 no implementado, 6 no verificado, 74 bloqueado, each blocked row naming
its dependency. By kind: 14 current defects, 394 new capabilities, 17
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
`remote.Peer` fields and passivation-strategy visibility for remote PIDs. No
API was found marked unsupported or deprecated.

## Single tenant (#424)

Read from code and tests; the tests named below were run on 2026-10-07. Three modes
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
- Shown not to work: adoption over PostgreSQL (#428). Recovery after adoption
  on PostgreSQL cannot be demonstrated yet; the characterization test records
  the current failure.
- Not verified: snapshot and durable-state adoption in a database;
  `MIGRATION.md` guidance.
- Gap: `offsets_store` has no tenant column (keyed by projection name and
  shard), and no offset migration was found for a switch of mode.

## Status of the #346 criteria

| Criterion | State |
| --- | --- |
| Record the SHA, build with Go 1.26, get the `go list` graph | done on `4ebdc3d`/Go 1.26 (historical); modules re-built and tested on current `develop`/Go 1.27.0 |
| Confirm or discard B2 and B4 | B2 not reproduced as a bug (non-zero partition not tested); B4 reproduced, fix tracked in #427 |
| Review each task against code and tracker | done: 81 linked tasks, all open; 5 here and 76 in `i-00-epic-345-audit.md`, 431 criteria, two passes; static reading, targeted tests only where a doubt could be settled |
| Audit publishers, sagas, testkit and controls for #395–#398 | done from code; not exercised by tests |
| Fix the GoAkt version and fork for #419 | done |
| Single-tenant inventory (#424) | done; the PostgreSQL adoption path fails (#428) |
| Module tests and PostgreSQL integration | done on current `develop` |
| Recovery after adopting legacy data to single-tenant | not demonstrated: blocked by #428 |

#346 stays open. Pending:

- recovery after adoption on PostgreSQL, blocked by #428;
- a test that a non-zero partition reaches the stored `Shard` of events and
  durable state. No acceptance criterion in #350 or elsewhere asks for it
  (#350 asks for slice stability across 1, 3 and 5 nodes, a different
  guarantee); it is a gap found by B2, and a stable-slice test would not cover
  it;
- the 14 current defects and the 74 blocked criteria of the epic audit, which
  belong to their own issues, not to this PR;
- limits of the audit: static reading plus targeted tests, so an equivalent
  implementation under an unsearched name may exist.

`no implementado` on criteria of new guarantees is the expected baseline, not
a defect.
