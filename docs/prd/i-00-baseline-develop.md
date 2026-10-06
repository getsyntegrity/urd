# I-00 — Baseline of `develop` and gaps (#346)

Evidence collected on 2026-10-06. Epic: #345. This change adds this report and
two characterization tests; it changes no production code.

**Status: root-module baseline validated; integration and publisher tests
pending.** This is not a claim that the whole repository is green.

## Baseline

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

**Not verified (pending):**

- the PostgreSQL / container integration lane (`inttest`, `persistence/postgres` tests);
- tests of the four publisher modules;
- tests of `benchmark`, `example`, `test/compat` (builds only).

Submodule builds and tests that use no real resources can be run independently
of the container lane; that is follow-up work.

## GoAkt dependency

`go.mod` pins `github.com/tochemey/goakt/v4 v4.5.7-0.20261001174708-cf9c8659f741`
and replaces it with `github.com/pablogore/goakt/v4 v4.5.7-actorof.1` (the fix
of Tochemey/goakt#1447, marked TEMPORARY).

Read-only inspection APIs on `goakt.ActorSystem` in that fork, as candidates
for #419 (public API presence only; local/remote access not yet checked):
`Metric`, `Actors`, `NumActors`, `ActorOf`, `Peers`, `Running`.

## B2 — `Partition` outside a cluster: not reproduced as a bug

Call sites in Urd (the only two):

- `internal/engine/eventsource/event_sourced_actor.go:353`, on `PostStart`;
- `internal/engine/durablestate/durable_state_actor.go:167`, during start.

Both store the result in `shardNumber`, which is written into the stored event
envelopes and durable-state `Shard` fields.

In the effective fork, `actorSystem.Partition` returns
`cluster.GetPartition(name)` when `InCluster()` and `uint64(0)` otherwise. It
does not panic or error in standalone mode.

Reproduction: `TestBaseline346PartitionStandalone` (`engine`). A standalone
engine with in-memory event and state stores: `InCluster()` is false,
`Partition("any-name")` is 0, and an event-sourced and a durable-state entity
each spawn and answer `CreateAccount` with revision 1 and the expected state.
No errors.

Open observation (not a bug claim): outside a cluster every entity records
shard 0, so any consumer that groups by shard sees one shard. Whether that
matters for `GetShardEvents` consumers is not verified here.

B2 is independent of tenancy: single-tenant with cluster and multi-tenant
without cluster are both possible. This test covers the no-resolver
standalone case only; the other combinations are not tested here.

## B4 — actor name collisions: reproduced

Actor names come from `engine.actorName(tenantID, id)`. Without a tenant
resolver, or with a fixed single-tenant resolver, the name is the bare entity
ID; only multi-tenant engines qualify it. Event-sourced, durable-state and saga
actors are spawned into the same GoAkt actor system.

Reproduction: `TestBaseline346CrossKindSpawnSharesOneActorName`. One engine,
one ID:

- `SpawnEventSourced(id)` returns nil;
- `SpawnDurableState(id)` returns nil (no error, no durable-state actor);
- `SpawnSaga(id)` returns nil;
- `SendCommand(id, CreateAccount)` succeeds with revision 1, and the durable
  store holds no state for `id`: the event-sourced actor answered.

So a cross-kind collision is silent, and the second and third spawns do not
create what the caller asked for. `resolveExistingSpawn` only verifies the
binding of an existing actor in the tenant-aware path.

The test asserts today's behaviour. The fix will flip its assertions.

### Target solution (decided by the owner, implemented separately)

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

Decision proposed by the owner: adopt **1.26.2** as the common minimum, after
checking every module's effective requirement (above). Two modules already
require it and all ten build on it. The alignment of module `go` directives,
CI and documentation goes in a separate PR, not this one.

## Inventory (Phase 0, not yet complete)

- `port/`: `adapter`, `behavior`, `publishing`, `runtime`; `port/publishing`
  has `port.go`, `publishing.go`, `publishingtest`, and an architecture test.
- `publisher/`: `kafka`, `nats`, `pulsar`, `websocket`, each its own module
  with closure, id and contract/conformance tests.
- `testkit/`: in-memory event, durable-state, snapshot, offset and key stores,
  `scenario.go`, conformance suites.
- Tracker: #345 epic; #364, #371, #395–#398, #419 open; #341 merged.

Not yet done from #346: the per-task review against code and tracker, the
keep/migrate/needs-contract record for #395–#398, the #419 local/remote access
check, and the single-tenant inventory (#424: unresolved defaults, fixed
resolver, dynamic mode, Unscoped data). #346 stays open.
