# I-00 — Baseline of `develop` and confirmed gaps (#346)

Evidence collected on 2026-10-06. Epic: #345. Nothing here changes code.

## Baseline

| Item | Value |
| --- | --- |
| `develop` SHA | `4ebdc3d249254a411572f90db6372e585dd88ac5` (`(#423)`) |
| Go | `go1.26.0` (root `go.mod` declares `go 1.26.0`) |
| `go build ./...` (root) | passes |
| `go vet ./...` (root) | passes |
| `go test -count=1 ./...` (root) | exit 0, 40 packages `ok` |
| `go list -deps` graph | 512 package lines (`go list -deps`), 43 packages in the root module |
| Submodule builds | `persistence/postgres`, `publisher/{kafka,nats,websocket}` build on 1.26.0 |
| `publisher/pulsar` | **does not build on 1.26.0**: its `go.mod` requires `go >= 1.26.2` |

Modules in the repo: root, `benchmark`, `example`, `inttest`, `test/compat`,
`persistence/postgres`, `publisher/{kafka,nats,pulsar,websocket}`. There is no
`go.work`.

Not run here: the PostgreSQL / container integration lane (`inttest`,
`persistence/postgres` tests) and the other publishers' tests; they need
containers. The unit-level baseline is the root module only.

## GoAkt dependency

`go.mod` pins `github.com/tochemey/goakt/v4 v4.5.7-0.20261001174708-cf9c8659f741`
and **replaces** it with `github.com/pablogore/goakt/v4 v4.5.7-actorof.1`
(the fix of Tochemey/goakt#1447: `ActorOf`, `ActorExists` and `Kill` panic when
an actor leaves the tree). The replace is marked TEMPORARY; remove it once that
PR is released.

Read-only inspection APIs available on `goakt.ActorSystem` in the fork
(candidates for #419, local and remote):

- `Metric(ctx) *Metric`
- `Actors(ctx, timeout) ([]*PID, error)`
- `NumActors() uint64`
- `ActorOf(ctx, name) (*PID, error)`
- `Peers(ctx, timeout) ([]*remote.Peer, error)`
- `Running() bool`

## Confirmed gap: B4 (actor name collisions across engines)

Actor names are `engine.actorName(tenantID, id)`. In single-tenant mode
(`tenancy.WithSingleTenant`, or no tenant resolver) the name is the **raw
entity ID**; only multi-tenant engines qualify it with the tenant
(`internal/actoridentity`). Event-sourced entities, durable-state entities and
sagas are spawned into the **same** GoAkt actor system, so they share one
namespace, and nothing in the name says which engine owns it.

Probe (throwaway test in `engine`, not committed): one engine, event store and
state store connected, one ID.

```
SpawnEventSourced(id)          -> nil
SpawnDurableState(same id)     -> nil
SpawnSaga(same id)             -> nil
```

All three return `nil`. Only one actor can exist under that name, so the second
and third calls do not create what the caller asked for. I did not follow the
probe through to a command on the "durable-state" ID, so what the caller
observes afterwards (a wrongly typed actor answering) is inferred, not
demonstrated. The silent `nil` is what was observed.

`resolveExistingSpawn` only handles the tenant-aware path
(`requested != nil`); in single-tenant mode there is no kind check.

Consequence for the Epic: any change that adds engines or inspection by name
must first give actor names a kind component, or reject a cross-kind
re-spawn with a typed error.

## B2 (cluster)

The issue text I received is garbled ("B2 cluster)") and #345 does not define
B2 in the parts I could read, so I did **not** confirm or refute it. Needs the
owner to restate B2 before it is closed.

## Inventory (Phase 0)

- `port/`: `adapter`, `behavior`, `publishing`, `runtime`.
  `port/publishing` has `port.go`, `publishing.go` and an architecture test.
- `publisher/`: `kafka`, `nats`, `pulsar`, `websocket`, each its own module with
  `closure_test.go`, `id_test.go` and a publisher contract/conformance test.
- `testkit/`: in-memory `EventStore`, `DurableStore`, `SnapshotStore`,
  `OffsetStore`, `KeyStore`, `scenario.go`, and conformance suites.
- Open tracker items referenced by the issue: #345 (Epic), #364, #371,
  #395 (publication), #396 (testkit), #397 (workflow), #398 (management),
  #419 (GoAkt read-only inspection feasibility). #341 is merged.

## Open items for the owner

1. Restate B2.
2. Decide whether `publisher/pulsar` must build on the 1.26.0 floor or the
   floor moves to 1.26.2.
3. Decide whether B4 is fixed by a kind prefix in names or by a typed
   cross-kind spawn error.
