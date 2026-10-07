<!-- markdownlint-disable MD033 MD041 -->

<p align="center">
  <img src="assets/logo.png" alt="Urd" width="480" />
</p>

<p align="center">
  <a href="https://github.com/getsyntegrity/urd/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/getsyntegrity/urd/ci.yml?branch=develop" alt="Build status"></a>
  <a href="https://go.dev/doc/install"><img src="https://img.shields.io/github/go-mod/go-version/getsyntegrity/urd" alt="Go version"></a>
  <a href="https://human-oss.dev"><img src="https://human-oss.dev/badge.svg" alt="Open Source AI Manifesto"></a>
</p>

**Urd — event sourcing for Go**

Urd is a protobuf-first framework for building event-sourced and durable-state CQRS applications in Go. It runs on [Go-Akt](https://github.com/Tochemey/goakt) and adds persistence, projections, publishers, sagas, encryption, and observability to an actor system that your application owns.

Urd deliberately does not hide the actor runtime. Your application creates and operates the Go-Akt actor system, including clustering, discovery, remoting, TLS, supervision, and non-Urd actors. Urd contributes the extensions and actor kinds needed for its persistence model.

`github.com/getsyntegrity/urd` (formerly `github.com/getsyntegrity/ego`) is a fork of [tochemey/ego](https://github.com/Tochemey/ego), maintained independently since 2026-09. Releases, module path, and CI here are this fork's own; they do not track the upstream project.

## Table of contents

- [Features](#features)
- [Requirements](#requirements)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Modeling entities](#modeling-entities)
    - [Event-sourced vs durable-state](#event-sourced-vs-durable-state)
- [Configuration](#configuration)
- [Snapshots and retention](#snapshots-and-retention)
- [Event batching](#event-batching)
- [Performance tuning](#performance-tuning)
- [Projections](#projections)
- [Publishers](#publishers)
- [Sagas and process managers](#sagas-and-process-managers)
- [Clustering](#clustering)
- [Persistence](#persistence)
    - [Tenant scoping](#tenant-scoping)
- [Encryption and schema evolution](#encryption-and-schema-evolution)
- [Observability](#observability)
- [Logging](#logging)
- [Reliability and operations](#reliability-and-operations)
- [Testing](#testing)
- [Examples](#examples)
- [Upgrading](#upgrading)
- [Contributing](#contributing)

## Features

- Event-sourced entities with deterministic recovery
- Durable-state entities that persist only the latest state
- Named, independently configured CQRS projections
- Snapshots, retention policies, and event batching
- Event adapters for protobuf schema evolution
- Event and state publishers for Kafka, NATS, Pulsar, and WebSocket
- Saga/process-manager support with compensation
- OpenTelemetry traces and metrics
- AES-256-GCM event and snapshot encryption
- Entity passivation, placement, relocation, and supervision controls
- In-memory stores, and behavior scenarios for testing

## Requirements

- Go 1.27 or later
- Basic familiarity with [Go-Akt](https://github.com/Tochemey/goakt#readme)
- Protobuf messages for commands, events, and state

For production use, provide durable implementations of the stores your application needs. The stores in [`testkit`](./testkit) are intended for tests and local examples.

## Installation

Urd is **pre-release**. No version has been published yet, so there is nothing to install with `go get` today; the first merge from `develop` to `main` publishes `v0.1.0` automatically (see [`docs/ci.md`](./docs/ci.md)). The module path is `github.com/getsyntegrity/urd` (no `/vN` suffix, so its versions will be `v0.x` or `v1.x`); the four publishers live at `github.com/getsyntegrity/urd/publisher/<name>`. The version `v0.0.0` that appears in the nested modules' `go.mod` files is only the development placeholder resolved through a local `replace`: it is not a tag and it is not installable.

To try it now, work from a checkout of this repository (`git clone https://github.com/getsyntegrity/urd`), or wait for the first tag and then run `go get github.com/getsyntegrity/urd/engine@<version>`.

The module root holds no Go files: import the runtime engine as `github.com/getsyntegrity/urd/engine` (package `engine`). Earlier snippets that imported the module root and used the `ego.` qualifier now use `engine.`; see the [changelog](./CHANGELOG.md).

### Formerly ego

Urd was published as `github.com/getsyntegrity/ego`. Only the module paths changed; the `engine` package and its API are the same. To migrate an application, rewrite the old path in your Go files and `go.mod`, then tidy:

```sh
grep -rl 'github.com/getsyntegrity/ego' --include='*.go' --include='go.mod' . \
  | xargs sed -i 's#github.com/getsyntegrity/ego#github.com/getsyntegrity/urd#g' && go mod tidy
```

On macOS, use `sed -i ''` instead of `sed -i`. Some persisted and wire-level names intentionally keep their `ego` spelling. [`MIGRATION.md`](./MIGRATION.md) lists them, with the reasons.

## Quick start

Build one `engine.Config`, use it to construct the Go-Akt actor system, start that system, and then plug in the Urd engine:

```go
package main

import (
    "context"
    "log"
    "time"

    accountpb "example.com/myapp/gen/account/v1"
    goakt "github.com/tochemey/goakt/v4/actor"
    "github.com/getsyntegrity/urd/engine"
    "github.com/getsyntegrity/urd/projection"
    "github.com/getsyntegrity/urd/testkit"
)

func main() {
    ctx := context.Background()

    // Use a durable EventsStore in production.
    eventsStore := testkit.NewEventsStore()
    if err := eventsStore.Connect(ctx); err != nil {
        log.Fatal(err)
    }
    defer eventsStore.Disconnect(ctx)

    offsetStore := testkit.NewOffsetStore()
    if err := offsetStore.Connect(ctx); err != nil {
        log.Fatal(err)
    }
    defer offsetStore.Disconnect(ctx)

    cfg := engine.NewConfig(eventsStore,
        engine.WithOffsetStore(offsetStore),
        engine.WithProjection("account-balances", &projection.Options{
            Handler:      NewAccountBalancesProjection(),
            BufferSize:   100,
            PullInterval: 500 * time.Millisecond,
        }),
        engine.WithProjection("account-audit", &projection.Options{
            Handler:      NewAccountAuditProjection(),
            BufferSize:   100,
            PullInterval: time.Second,
        }),
    )

    sys, err := goakt.NewActorSystem("accounts", cfg.GoaktOptions()...)
    if err != nil {
        log.Fatal(err)
    }

    if err := sys.Start(ctx); err != nil {
        log.Fatal(err)
    }
    defer sys.Stop(ctx)

    eng, err := engine.NewEngine(sys, cfg)
    if err != nil {
        log.Fatal(err)
    }

    if err := eng.Start(ctx); err != nil {
        log.Fatal(err)
    }
    defer eng.Stop(ctx)

    if err := eng.StartProjection(ctx, "account-balances"); err != nil {
        log.Fatal(err)
    }
    
    if err := eng.StartProjection(ctx, "account-audit"); err != nil {
        log.Fatal(err)
    }

    account := NewAccountBehavior("account-123")
    if err := eng.Entity(ctx, account); err != nil {
        log.Fatal(err)
    }

    state, revision, err := eng.SendCommand(
        ctx,
        account.ID(),
        &accountpb.OpenAccount{InitialBalance: 1000},
        5*time.Second,
    )
    if err != nil {
        log.Fatal(err)
    }

    log.Printf("state=%v revision=%d", state, revision)
}
```

`NewEngine` requires a running actor system built with `cfg.GoaktOptions()`. The same `Config` must be passed to both calls so the engine and actor-system extensions stay in sync.

`NewAccountBalancesProjection` and `NewAccountAuditProjection` represent application handlers that each implement `projection.Handler`. Each named projection keeps independent offsets and runtime settings.

The engine does not own the actor system. Stop the engine before stopping the actor system, as the deferred calls above do.

## Modeling entities

All commands, events, and states are protobuf messages.

An event-sourced behavior implements `engine.EventSourcedBehavior`:

```go
type EventSourcedBehavior interface {
    InitialState() engine.State
    HandleCommand(context.Context, engine.Command, engine.State) ([]engine.Event, error)
    HandleEvent(context.Context, engine.Event, engine.State) (engine.State, error)
}
```

`HandleCommand` validates a command and returns zero or more events. Urd persists those events before committing the resulting state. `HandleEvent` must be deterministic because it is also used during recovery.

A durable-state behavior implements `engine.DurableStateBehavior`:

```go
type DurableStateBehavior interface {
    InitialState() engine.State
    HandleCommand(context.Context, engine.Command, uint64, engine.State) (newState engine.State, newVersion uint64, err error)
}
```

Configure a state store and spawn the behavior with `DurableStateEntity`:

```go
cfg := engine.NewConfig(nil, engine.WithStateStore(stateStore))

// Build and start the actor system and engine as shown above.
if err := eng.DurableStateEntity(ctx, behavior); err != nil {
    return err
}
```

Behavior values are Go-Akt dependencies. In addition to the methods above, they provide an `ID` and binary marshalling methods so they can travel with cluster spawn requests. See the [event-sourced](./example/eventssourced), [durable-state](./example/durablestate), and [saga](./example/saga) examples for complete implementations.

### Event-sourced vs durable-state

| Aspect            | `EventSourcedBehavior`                    | `DurableStateBehavior`       |
|-------------------|-------------------------------------------|------------------------------|
| Persistence model | Persists domain events                    | Persists the latest state    |
| Recovery          | Replays events, optionally from snapshots | Loads the latest state       |
| History           | Full audit trail                          | No historical log            |
| Complexity        | Higher                                    | Lower                        |
| Best fit          | Traceable, business-critical workflows    | Simpler CRUD-like aggregates |

## Configuration

Engine-wide options are passed to `engine.NewConfig`:

- `WithStateStore` enables durable-state entities.
- `WithSnapshotStore` enables event-sourced snapshots.
- `WithOffsetStore` supplies durable projection offsets.
- `WithProjection` registers a named projection and its handler.
- `WithEventAdapters` applies schema transformations during recovery and projection consumption.
- `WithTelemetry` enables OpenTelemetry instrumentation.
- `WithEncryptor` encrypts persisted event and snapshot payloads.
- `WithEntityKinds` registers behavior types on every cluster node.
- `WithLogger` sets the [kit-logger](https://github.com/pablogore/kit-logger) logger used by Urd and the underlying actor system. See [Logging](#logging).

Entity-specific options are passed when an entity is spawned:

- `WithPassivateAfter`
- `WithRelocation`
- `WithSupervisorDirective`
- `WithPlacement`

Event-sourced entities additionally support `WithSnapshotInterval`, `WithRetentionPolicy`, `WithBatchThreshold`, and `WithBatchFlushWindow`.

API details and defaults are documented in the Go doc comments of the `engine` package (`go doc ./engine`).

## Snapshots and retention

Snapshots reduce recovery work by restoring the most recent state and replaying only later events:

```go
cfg := engine.NewConfig(eventsStore,
    engine.WithSnapshotStore(snapshotStore),
)

err := eng.Entity(ctx, behavior,
    engine.WithSnapshotInterval(100),
)
```

A snapshot interval of `0` disables automatic snapshots. Retention runs only after a snapshot has been successfully written:

```go
err := eng.Entity(ctx, behavior,
    engine.WithSnapshotInterval(100),
    engine.WithRetentionPolicy(engine.RetentionPolicy{
        DeleteEventsOnSnapshot:    true,
        DeleteSnapshotsOnSnapshot: true,
        EventsRetentionCount:      200,
    }),
)
```

Your `EventsStore` and `SnapshotStore` implementations must support the corresponding delete operations.

## Event batching

Batching combines events produced by multiple commands into fewer store writes. It is disabled by default:

```go
err := eng.Entity(ctx, behavior,
    engine.WithBatchThreshold(10),
    engine.WithBatchFlushWindow(5*time.Millisecond),
)
```

The threshold or flush window, whichever is reached first, triggers the write. If batching is enabled without a flush window, Urd uses a 5 ms default. Benchmark the settings with your command pattern and persistence backend; batching trades additional latency for fewer writes and does not have one ideal threshold.

## Performance tuning

Urd can sustain hundreds of thousands of commands per second on a single node with an in-memory store, and tens of thousands with durable backends like Postgres. This section outlines the recommended approach to maximize throughput and minimize memory cost.

### Enable event batching under concurrent load

Batching amortizes the cost of a single store write across multiple commands. It is most effective when:

- The entity receives commands concurrently (multiple goroutines or upstream services)
- The persistence store has non-trivial write latency (e.g. database round-trip > 100us)

Sequential command streams do not benefit from batching because each command waits for the flush window before the batch is written. For purely sequential workloads, leave batching disabled (the default).

### Choose the right batch threshold

| Write latency       | Recommended threshold | Rationale                                        |
|---------------------|-----------------------|--------------------------------------------------|
| < 100us (in-memory) | Disabled (0)          | Batching adds overhead with no I/O to amortize   |
| 100us - 1ms         | 5 - 10                | Small batches reduce flush window wait           |
| 1ms - 10ms          | 10 - 50               | Larger batches amortize the I/O cost well        |
| > 10ms              | 50 - 100              | Maximize events per write to offset high latency |

### Minimize allocations for high throughput

Urd's hot path is optimized for low allocation overhead (~22 heap allocations per command round-trip). The dominant allocation cost comes from Protocol Buffers serialization, which is inherent to the persistence model. To keep allocation pressure low:

- **Keep command and event protos small.** Smaller messages reduce marshal/unmarshal cost.
- **Use snapshots.** They reduce recovery replay length and the number of events held in the store.
- **Avoid large state protos.** The state is serialized on every reply; smaller states mean fewer bytes and less GC pressure.

### Scale horizontally with clustering

For workloads beyond what a single node can handle, build a clustered Go-Akt actor system and plug Urd into it as described in [Clustering](#clustering).

## Projections

Each projection has its own name, handler, offsets, and recovery settings. Register projections on the `Config`, then start them after the engine:

```go
cfg := engine.NewConfig(eventsStore,
    engine.WithOffsetStore(offsetStore),
    engine.WithProjection("account-balances", &projection.Options{
        Handler:      accountBalancesHandler,
        BufferSize:   100,
        PullInterval: 500 * time.Millisecond,
        Recovery:     projection.NewRecovery(
            projection.WithRecoveryPolicy(projection.RetryAndFail),
            projection.WithRetries(5),
            projection.WithRetryDelay(time.Second),
        ),
    }),
    engine.WithProjection("account-audit", &projection.Options{
        Handler:           accountAuditHandler,
        BufferSize:        250,
        PullInterval:      time.Second,
        Recovery:          projection.NewRecovery(
            projection.WithRecoveryPolicy(projection.RetryAndSkip),
            projection.WithRetries(3),
            projection.WithRetryDelay(2*time.Second),
        ),
        DeadLetterHandler: accountAuditDeadLetterHandler,
    }),
)

// Build and start the actor system and engine as shown above.
if err := eng.StartProjection(ctx, "account-balances"); err != nil {
    return err
}
if err := eng.StartProjection(ctx, "account-audit"); err != nil {
    return err
}
```

The two projections consume the same event journal independently. Each uses its own handler, buffer, pull interval, recovery policy, dead-letter handling, and offsets.

Projection handlers receive events with at-least-once delivery. They must be idempotent and safe for concurrent calls across different shards; events within one shard are delivered sequentially.

The engine also supports:

- `StopProjection` to stop a running projection
- `IsProjectionRunning` to inspect its runtime state
- `RebuildProjection` to reset offsets and replay from a timestamp
- `ProjectionLag` to report lag by shard
- Recovery policies and dead-letter handlers for processing failures

`Engine.Stop` does not stop projection actors because they belong to the caller-owned Go-Akt actor system. In the normal shutdown sequence, call `eng.Stop(ctx)` and then `sys.Stop(ctx)`; stopping the actor system terminates all projections. If the actor system must remain running, call `eng.StopProjection(ctx, name)` for each projection before stopping the engine.

In a cluster, a projection runs as a singleton. Every node must register the same named projections because the hosting node resolves each handler from its local `Config`.

## Publishers

Call `AddEventPublishers` or `AddStatePublishers` after the engine starts and before producing changes:

```go
if err := eng.AddEventPublishers(eventPublisher); err != nil {
    return err
}
```

Urd includes connector modules for:

- [Kafka](./publisher/kafka)
- [NATS](./publisher/nats)
- [Pulsar](./publisher/pulsar)
- [WebSocket](./publisher/websocket)

You can also implement `engine.EventPublisher` or `engine.StatePublisher`. Publisher payload timestamps are Unix nanoseconds, and each payload includes its source shard.

## Sagas and process managers

Urd includes first-class saga support for long-running business processes that coordinate multiple entities. You can:

- Start a saga with `Engine.Saga(...)`
- Inspect it with `Engine.SagaStatus(...)`
- Model compensation logic for timeouts and failures
- Persist saga state using the same event-sourced foundations

See the [fund-transfer saga example](./example/saga) for a complete implementation.

## Clustering

Cluster, discovery, remoting, and TLS are configured with Go-Akt. Register Urd's actor kinds in the cluster configuration:

```go
clusterConfig := goakt.NewClusterConfig().
    WithDiscovery(discoveryProvider).
    WithDiscoveryPort(gossipPort).
    WithPeersPort(peersPort).
    WithPartitionCount(partitions).
    WithMinimumPeersQuorum(quorum).
    WithReplicaCount(replicas).
    WithKinds(engine.ClusterKinds()...) // Urd's actor kinds, required for relocation
```

Also register every event-sourced, durable-state, and saga behavior type on every node:

```go
cfg := engine.NewConfig(eventsStore,
    engine.WithEntityKinds(
        new(AccountBehavior),
        new(OrderBehavior),
        new(CheckoutSaga),
    ),
)
```

Then compose `cfg.GoaktOptions()` with Go-Akt's cluster, remote, TLS, and application-specific options when constructing the actor system:

```go
sys, err := goakt.NewActorSystem("accounts",
    append(
        cfg.GoaktOptions(),
        goakt.WithCluster(clusterConfig),
        goakt.WithRemote(remote.NewConfig(host, remotingPort)),
        goakt.WithTLS(&tlsInfo),
    )...,
)
```

You retain full control over discovery, partitioning, quorum, replicas, TLS, remoting, and any additional cluster knobs Go-Akt exposes. Urd derives cluster behavior (e.g. running projections as singletons) directly from `sys.InCluster()` at runtime — no separate cluster flag to keep in sync.

Single-node deployments do not need `ClusterKinds` or `WithEntityKinds`.

### Remoting

Go-Akt speaks a multiplexed remoting protocol — per-peer lane connections, chunked large messages, and credit-based flow control. Urd requires no configuration for it: `remote.NewConfig(host, remotingPort)` negotiates it on its own, and Urd's remote surface is unchanged.

Two things follow from how Urd's traffic maps onto those lanes.

**Entity placement travels on the control lane.** Spawns, singleton placement, and death-watch are carried separately from user commands, so a burst of entity traffic can no longer delay them.

**Entity commands share one ordinary lane by default.** `SendCommand` and saga participant calls are asks, and every ask from one node to a given peer rides a single connection with one writer queue and one credit window. Raising the lane count shards receivers across connections so sends proceed in parallel:

```go
goakt.WithRemote(remote.NewConfig(host, remotingPort,
    remote.WithOrdinaryLanes(4),
)),
```

Any lane count is safe for Urd. Ordering in Urd is per entity — each entity actor serializes its own mailbox — and Go-Akt pins a receiver to a lane by a stable hash of its address, so commands to one entity stay in order however many lanes exist. Urd never relies on ordering between different entities.

Slow entities degrade gracefully rather than stalling a connection: asks are multiplexed by correlation ID and dispatched on a bounded worker pool, so an entity waiting on its events store occupies a worker instead of blocking the socket. When that pool saturates, the affected request comes back as an unavailable error and the connection stays healthy.

Leave the protocol pin at its `auto` default while upgrading a running cluster. Nodes negotiate the multiplexed protocol with peers that support it and fall back to the legacy wire for those that do not, so a cluster rolls node by node without a flag day.

The [Kubernetes cluster example](./example/cluster) demonstrates a three-node deployment with PostgreSQL, Kubernetes discovery, a singleton projection, OpenTelemetry, Prometheus, Jaeger, and Grafana.

## Persistence

Urd defines small interfaces for:

- [`persistence.EventsStore`](./persistence/events_store.go)
- [`persistence.SnapshotStore`](./persistence/snapshot_store.go)
- [`persistence.StateStore`](./persistence/state_store.go)
- [`offsetstore.OffsetStore`](./offsetstore/offset_store.go)

Applications may implement these interfaces directly. The [ego-contrib](https://github.com/Tochemey/ego-contrib) project provides ready-to-use implementations:

- **Postgres** event store, snapshot store, offset store, and durable state store
- **MongoDB** event store, snapshot store, offset store, and durable state store

To use a contrib store, import the relevant module alongside Urd:

```go
import (
    "github.com/tochemey/ego-contrib/eventstore/postgres"
    "github.com/tochemey/ego-contrib/snapshotstore/postgres"
    "github.com/tochemey/ego-contrib/offsetstore/postgres"
)
```

Applications own store connectivity: connect stores before starting the actor system and disconnect them after the engine and actor system have stopped.

### Tenant scoping

Every record-addressing method on `EventsStore`, `StateStore`, and `SnapshotStore` takes a `persistence.Scope`: a persisted record's effective identity is the pair `(Scope, persistence_id)`, never `persistence_id` alone. `persistence.Unscoped()` is the scope every call carries when no [`tenancy.TenantResolver`](./tenancy/resolver.go) is configured, so a deployment that never activates tenancy is unaffected. Registering one with `engine.WithTenantResolver` makes the engine resolve the caller's tenant and attach it to `ctx` at the command trust boundary (`SendCommand`/`Dispatch`, `SagaStatus`, `EraseEntity`) — a `TenantResolver` is invoked exactly once per call, never at spawn. Spawning an entity, durable-state entity, or saga instead declares its tenant with `engine.WithTenant(id)`, so the application states which tenant an aggregate belongs to rather than the engine inferring it; the built-in `tenancy.WithSingleTenant(id)` is the one exception, since a deployment with exactly one tenant can expose it as a fixed identity and needs no `WithTenant` or other per-call plumbing at all.

A custom store adapter must key its records on `(Scope, persistence_id)` structurally, e.g. a real tenant column in a SQL primary key and every `WHERE` clause — never by concatenating `Scope.String()`, which is a diagnostic rendering only. [`persistence/conformance`](./persistence/conformance) is the isolation acceptance suite: wire `conformance.RunEventsStoreConformance` (and its `RunStateStoreConformance`/`RunSnapshotStoreConformance` equivalents) into the adapter's own tests, the way [`testkit/conformance_test.go`](./testkit/conformance_test.go) does for the in-repo stores.

Adopting tenancy on a deployment that already has data written under `Unscoped()`? [`migration.TenantAdopter`](./migration/tenant_adoption.go) copies an aggregate's events, snapshot, and durable state into a per-aggregate target tenant scope, stamping `tenant_metadata` the way the actors do. It defaults to dry-run and never deletes source data without an explicit, verified opt-in. See the `[Unreleased]` entry in [CHANGELOG.md](./CHANGELOG.md) for the full breaking-change and migration details.

## Encryption and schema evolution

`WithEncryptor` transparently encrypts event and snapshot payloads before persistence and decrypts them during entity recovery and projection processing. The built-in `encryption.AESEncryptor` uses AES-256-GCM and a pluggable `encryption.KeyStore`.

`WithEventAdapters` registers transformations for events written with older protobuf schemas. Adapters run in registration order during recovery and projection consumption.

## Observability

`WithTelemetry` accepts an OpenTelemetry tracer and meter:

```go
cfg := engine.NewConfig(eventsStore,
    engine.WithTelemetry(&engine.Telemetry{
        Tracer: tracer,
        Meter:  meter,
    }),
)
```

Instrumentation covers command dispatch and handling, event persistence, active entities and projections, projection processing, offsets, lag, and approximate events behind.

## Logging

Urd logs through [kit-logger](https://github.com/pablogore/kit-logger), a structured logging framework built on `log/slog`. One kit-logger `Logger` covers the whole runtime: the engine, its projection runners, saga actors and publishers, the migrator, and the GoAkt actor system Urd sits on.

```go
import kitlog "github.com/pablogore/kit-logger/pkg/logger"

logger := kitlog.New(kitlog.Config{
    Level:        kitlog.LevelInfo,
    Format:       kitlog.FormatJSON,
    GlobalFields: map[string]string{"service": "accounts"},
})

cfg := engine.NewConfig(eventStore, engine.WithLogger(logger))
```

- When `WithLogger` is not used, Urd logs through `engine.DefaultLogger()`, which is kit-logger's process-wide logger (`logger.L()`). An application that installs its own logger with `logger.SetGlobal` before building the engine therefore needs no extra wiring.
- `engine.DiscardLogger` drops every record and reports every level as disabled. Use it in tests and benchmarks.
- `engine.ResolveLogger` applies Urd's nil-logger rule outside the engine: a nil or typed-nil logger resolves to `engine.DefaultLogger()`.

Urd's own records are structured: a fixed message plus snake_case fields such as `persistence_id`, `sequence_number`, `projection`, `saga_id` and `error`. Records written with a context go through kit-logger's `*Context` methods, so enabling kit-logger's OpenTelemetry decorator stamps `trace_id` and `span_id` on them, which joins a log line to the trace `WithTelemetry` produced:

```go
import kitotel "github.com/pablogore/kit-logger/pkg/logger/otel"

logger := kitlog.New(kitlog.Config{
    Format:         kitlog.FormatJSON,
    ContextHandler: kitotel.Decorator(kitotel.Options{}),
})
```

Everything else kit-logger offers — level changes at runtime with `SetLevel`, redaction and filtering rules, sampling, rate limiting, `AddSource` call-site attribution, buffered output with an explicit `Flush`/`Shutdown` lifecycle — applies to Urd's records unchanged, because Urd never wraps the logger it is given. Records the actor system writes are attributed to GoAkt's own call site, not to Urd's adapter. The application owns the logger's lifecycle; Urd flushes it when the actor system stops but never shuts it down.

## Reliability and operations

Urd includes several production-focused capabilities:

- Faster recovery through [snapshots](#snapshots-and-retention)
- Storage cleanup through [retention policies](#snapshots-and-retention)
- At-rest [encryption](#encryption-and-schema-evolution) for events and snapshots
- GDPR-style erasure with `Engine.EraseEntity(...)`
- Structured, context-aware [logging](#logging) through kit-logger

## Testing

The [`testkit`](./testkit) package provides in-memory event, snapshot, state, offset, and key stores. Its scenario API tests behavior logic without starting an actor system:

```go
testkit.ForEventSourcedBehavior(behavior).
    Given(priorState).
    When(command).
    ThenEvents(t, expectedEvents...).
    ThenState(t, expectedState)
```

`Given` states the entity's prior state — the same state the engine recovers from the journal and hands to `HandleCommand` — so the arrangement never depends on `HandleEvent`. Omit it to start from `InitialState()`.

Where the history reads better than the folded state, `GivenEvents` arranges the entity from the events it has already recorded, replayed in order through `HandleEvent` just as the engine replays a journal:

```go
testkit.ForEventSourcedBehavior(behavior).
    GivenEvents(accountCreated, accountCredited).
    When(command).
    ThenEvents(t, expectedEvents...)
```

The two compose — `Given(snapshotState).GivenEvents(subsequentEvents...)` mirrors an entity recovered from a snapshot and then replayed. An event `HandleEvent` rejects fails the scenario as a broken arrangement, reported as such by every assertion including `ThenError`, so a bad setup can never pass as a failed command.

The durable-state scenario reads the same way, with `Given(priorState, priorVersion)` and `ThenState`/`ThenVersion`.

## Examples

- [Event-sourced entity](./example/eventssourced)
- [Durable-state entity](./example/durablestate)
- [Fund-transfer saga](./example/saga)
- [Three-node Kubernetes cluster](./example/cluster)
- [Benchmarks](./benchmark)

Run the local examples with:

```bash
make run-eventsourced
make run-durablestate
make run-saga
```

The examples are a separate Go module in `example/`, so `go run` has to start from there, for example `cd example && go run ./saga`. The `make` targets do that for you.

## Upgrading

See the [changelog](./CHANGELOG.md) for breaking changes and version-specific migration instructions.

## Contributing

Contributions are welcome. Read the [contribution guide](./contributing.md) before opening a pull request. To find your way around the engine code, see [how the engine is laid out](./docs/engine.md).

---

Maintained by [GetSyntegrity](https://github.com/getsyntegrity) — Pablo Gore ([@pablogore](https://github.com/pablogore)).
Originally created by [Arsene Tochemey Gandote](https://github.com/Tochemey).
