# Design — Deterministic in-memory runtime and `compose/inmem` (EGO-RUNTIME-005, #105 IMPL-6)

| Field | Value |
|---|---|
| Change | `ego-runtime-002` |
| Date | 2026-09-27 |
| Phase | `sdd-design` |
| Tracker | [`#148`](https://github.com/getsyntegrity/ego/issues/148), parent [`#11`](https://github.com/getsyntegrity/ego/issues/11) |
| Inputs | [`proposal.md`](./proposal.md); ego-runtime-001 [`design.md`](../ego-runtime-001/design.md) §2, D1–D10; ego-arch-003 [`design.md`](../ego-arch-003/design.md) §D1–§D8, §5.2, §6; ego-arch-004 [`design.md`](../ego-arch-004/design.md) §D4, §D6, F-E; ego-arch-002-s3 [`design.md`](../ego-arch-002-s3/design.md); ego-arch-006 [`design.md`](../ego-arch-006/design.md) D1–D8, F1; ego-arch-001 [`design.md`](../ego-arch-001/design.md) §3, §10 |
| Baseline | `main` at `57c4b11` (every `file:line` below was read there) |

## 1. Summary and vocabulary

Ego can run a behavior only on GoAkt today. This design adds a second runtime that keeps everything in the process, with no actor system: a map of entities, each with its own serialized mailbox. It implements the same interface consumer code already uses (`port/runtime.Runtime`), persists through the same stores, and emits the same event-stream messages. A second composition root, `compose/inmem`, builds it from the same `compose.Spec` that `compose/goakt` takes, validates it with the same rules, and starts and stops it in the same order. A test then runs one behavior, through one consumer function, on both roots and checks that what can be observed is the same. That test is the criterion #105 has been waiting for.

The runtime lives in an internal package, so the only new public API is `compose/inmem`. It changes nothing in package `ego`.

Terms used below:

- **Runtime**: the component that hosts entities and sagas and delivers commands to them. The GoAkt runtime is `*ego.Engine`; the new one is `inmemruntime.Runtime`.
- **Composition root**: the package that validates a `compose.Spec`, builds a runtime from it, and owns its `Start`/`Stop` (`compose/goakt` today, `compose/inmem` here).
- **Mailbox**: a first-in, first-out queue of work for one entity, processed by at most one goroutine at a time, so an entity never runs two commands at once.
- **Observable**: what a consumer can see through public contracts: `port/runtime` return values and errors, the stores' contents (`persistence`), the event stream (`Subscribe`), and what publishers receive. Timing, goroutines, log lines and telemetry are not observable in this sense.
- **Observable-equivalent**: two runtimes are observable-equivalent for a scenario when the observations listed in §D9 are equal after normalization (timestamps and shard numbers removed).
- **Condition-based wait**: waiting for a fact (a channel receive, a status value) bounded by a context deadline, as opposed to a fixed sleep. The repository removed fixed waits in #112; this design adds none.
- **archcheck**: `internal/cmd/archcheck`, the import-rule checker CI runs (`docs/ci.md`, "Architecture boundary check").
- **Validation rule IDs**: `compose.Spec.Validate` reports rules V1–V8 (ego-arch-003 §D4a; V8 from ego-arch-004 §D6, whose part **V8b** checks that a declared capability and the implemented method agree). `compose/goakt` adds GoAkt-only rules **G1** (a cluster needs entity kinds) and **G2** (a valid actor-system name). This design adds `compose/inmem`-only rules with the prefix **M** ("memory"): only **M1** so far (§D8).
- **FU-A … FU-E**: follow-ups this design names but does not deliver, listed in §8: FU-A projection runner, FU-B extract shared rules, FU-C ordered event stream, FU-D neutral entity-not-found error, FU-E the no-event `SendCommand` contract text.
- **Characterization (spec 0)**: tests that run a scenario on the GoAkt runtime only and record what it does, so a behavior this design could not settle by reading becomes a measured fact before the in-memory runtime copies it (§D12).
- **LIFE-004**: the flush and drain policy item of #24 (the lifecycle epic), which decides what happens to work in flight at shutdown.
- **Closure test**: a test that runs `go list -deps` on a package and fails when a forbidden package appears anywhere in the dependency tree, direct or not (`internal/runtimeconsumer/closure_test.go` is the model).

## 2. What the GoAkt runtime does today

The in-memory runtime must match this behavior, so it is recorded first. Every item was read on `57c4b11`.

### 2.1 Event-sourced entities

- **Spawn.** `Engine.spawnEventSourced` (`engine.go:758-775`) checks, in this order, that the engine is started, the declared family (`requireFamily`, `engine.go:831`, error `ErrEntityFamilyNotDeclared`), and only then that an events store exists (`ErrEventsStoreRequired`); `spawnSaga` uses the same order (`engine.go:1517-1533`). It then resolves the spawn tenant (`spawnTenantScope`, `engine.go:875`: `WithTenant`, else the resolver's fixed tenant, else `ErrSpawnTenantUndetermined`), and spawns the actor. Spawning an ID that is alive is idempotent under the same tenant and fails with `ErrSpawnTenantMismatch` under another.
- **Recovery.** On start the actor pings its stores, loads the latest snapshot if a snapshot store exists, then replays events after it through `HandleEvent`, decrypting and running the event adapters first (`EventSourcedActor.recover`, `event_sourced_actor.go:559`).
- **Commands.** The actor prefers `HandleEnvelope` when the behavior implements `behavior.EventSourcedEnvelope` and metadata is present, else `HandleCommand` (`dispatchToBehavior`, `event_sourced_actor.go:772`). It checks the deadline before the handler and again before persisting (`deadline_gate.go:60`). Each event is applied with `HandleEvent` and wrapped in an `egopb.Event` with persistence ID, sequence number, timestamp, shard, encryption fields and, in tenant-aware mode, tenant metadata (`marshalEvent`, `event_sourced_actor.go:1196`).
- **No events.** When the handler returns no events, the actor replies with the **current state** (`event_sourced_actor.go:991-993`), and `resultFromReply` turns that into `OutcomeSuccess` with the current revision (`engine.go:1866-1873`). So `SendCommand` returns the unchanged state, not nil. See §2.6.
- **Revision** is the sequence number of the last persisted event: 1 after the first event.
- **Expected revision.** A command's metadata may carry an expected revision, mapped to `persistence.ExpectGenesis`, `ExpectRevision(n)` or `Unconditional`. A conflict becomes `OutcomeRejected` with `command.CodeConcurrencyConflict`, with the `*persistence.ConflictError` recoverable through `errors.As` (`reply_classification.go:80-90`). 
- **Failed writes stop the entity.** When a write fails, the actor stays alive only for a conflict whose reported actual revision equals its own counter (`shouldStayAliveAfterConflict`, `event_sourced_actor.go:830-837`). Any other failure sets `directShutdown` (`event_sourced_actor.go:1094`), and so does an out-of-sync conflict. `replyDirect` then sends the error reply and calls `ctx.Shutdown()` (`event_sourced_actor.go:1137-1143`). That is GoAkt's graceful stop (`actor/receive_context.go:510-515` in goakt v4.5.4), not a failure the supervisor restarts. So the entity is gone afterwards: `EntityExists` reports false, and a later `Dispatch` does not re-spawn it.
- **Write-side settings.** `WithSnapshotInterval`, `WithRetentionPolicy`, `WithBatchThreshold` and `WithBatchFlushWindow` are GoAkt adapter settings (ego-runtime-001 D3): only `*ego.Engine` reads them.

### 2.2 Durable-state entities

`checkPreconditions` (`durable_state_actor.go:575-590`) applies two rules to what the command handler returns. The new state must have the same protobuf message type as the current state (else "mismatch state types"). The new version must differ from the prior version by exactly one. `commitState` (`durable_state_actor.go:689`) writes the `egopb.DurableState`, then updates memory, then publishes. Durable state is never encrypted. When the actor stops, `PostStop` (`durable_state_actor.go:240`) writes the committed state once more, unconditionally, and publishes it, unless it is a tenant-aware entity that never committed.

### 2.3 Event stream and publishers

- Events go to topic `topic.events` (`event_sourced_actor.go:58`) as `*egopb.Event`, states to `topic.states` (`durable_state_actor.go:53`) as `*egopb.DurableState`, each only after its store write succeeded.
- `Engine.Subscribe` (`engine.go:706`) returns a subscriber already subscribed to both topics.
- Each publisher gets its own subscriber and its own goroutine (`AddEventPublishers`, `engine.go:1391`; `sendEvent`, `engine.go:1966`).
- **Ordering is not guaranteed on the stream, even for one entity.** `EventsStream.publishToTopic` delivers each message to each subscriber in a new goroutine (`go sub.signal(message)`, `eventstream/stream.go:155`), so two messages published in order can be enqueued in either order. This affects every `Subscribe` caller and every publisher on both runtimes, since both use package `eventstream`.

### 2.4 Sagas

- A saga subscribes to `topic.events` (`saga_actor.go:199`) and reacts to each event with `HandleEvent`. It persists its own events to the events store (not to the stream) and applies them with `ApplyEvent`.
- It sends commands with the actor system's `SendSync` (`sendCommand`, `saga_actor.go:758`; default timeout 5 s), not through `Engine.Dispatch`, and calls `HandleResult` or `HandleError` with the answer.
- `Compensate` runs every compensation command in one go; the status becomes `SagaCompleted` when all succeed and `SagaFailed` otherwise (`saga_actor.go:806-829`). A non-zero timeout schedules a message that starts compensation (`saga_actor.go:222-237`).
- A saga stops reacting once it is no longer running: `handleStreamEvent` returns immediately unless the status is `SagaRunning` (`saga_actor.go:482-484`).
- **Tenant rules of a tenant-aware saga** (names from the code comments):
  - **SG4** (live events, `handleStreamEvent` through `bindOrVerify`, `saga_actor.go:442-460`, `:497`): an event whose tenant metadata does not match the saga's bound tenant is rejected fail-closed. A bound saga checks this before `HandleEvent`, so it never sees a foreign tenant's payload. An event with invalid tenant metadata is dropped.
  - **SG5** (replay, `recover`, `saga_actor.go:326`): every replayed saga event must carry the tenant the saga was bound to at spawn.
  - **SG-DUR1** (`persistTenantBinding`, `saga_actor.go:686-706`): the durable tenant-binding marker is an `egopb.Event` whose payload is `emptypb.Empty` and which carries the tenant metadata. Replay recognizes the marker and skips it rather than applying it (`saga_actor.go:346`). Since spawn now binds the tenant, a tenant-aware saga no longer writes new markers (`saga_actor.go:86-94`), but markers already in a journal must still be skipped.
- Since #163, `SagaStatus` reports the lifecycle status (`engine.go:1650`). `SagaCompensating` is never seen by a caller, because compensation runs inside one message (odd/tasks/saga-status-153.md, "Behavior notes"). The status is not persisted.

### 2.5 Other operations

- `Dispatch` (`engine.go:1123`) rejects an empty ID (`ErrUndefinedEntityID`), an internal control payload (`ErrNotACommand`), a nil payload (`command.ErrInvalidEnvelope`) and metadata that does not round-trip; returns a timed-out or canceled `Result` when the context is already done; and bounds the command by the earliest of the context deadline, the metadata deadline and now plus `timeout`. It never spawns: an unknown ID returns GoAkt's "actor not found" error.
- `SendCommand` (`engine.go:1313`) derives metadata from the context, calls `Dispatch`, and maps the `Result` back (`resultToLegacy`, `engine.go:1893`).
- `EntityExists` (`engine.go:988-1000`) is a liveness probe: it never spawns or recovers. It asks `ActorExists` for the ID, so it also reports true for a live saga's ID.
- Saga spawns ignore every option except `WithTenant` (`engine.go:1497-1501`, doc; `engine.go:1563-1567` spawns with fixed options).
- `WithPassivateAfter` is honored on a single node (`engine.go:1910-1911`). An entity idle for that long is stopped: `EntityExists` then reports false, and `Dispatch` does not re-spawn it. Two further details were read in goakt v4.5.4:
  - **What counts as activity.** Every message the actor handles marks activity (`handleReceived` → `markActivity`, `actor/pid.go:2192-2196`), and the passivation manager measures idleness from that mark (`actor/passivation_manager.go:413-422`). That includes user commands, refused commands (for example a tenant mismatch) and internal messages such as persist responses and stash replays. Time counts from when the turn starts, not when it ends.
  - **Coalescing.** The mark reaches the passivation manager at most once per 100 ms (`passivationTouchInterval`, `actor/pid.go:75`; `markActivity`, `:2440-2453`), so GoAkt's passivation time is approximate to about that interval.
  - **No re-check at the deadline.** When the deadline fires, the manager passivates the actor without asking whether it was active since the last renewal it received (`trigger`, `actor/passivation_manager.go:349-372`; `passivate`, `:541-549`; `tryPassivation`, `actor/pid.go:2803` onward). Activity inside the last coalescing window therefore may not prevent passivation.
  - A passivated durable-state entity runs `PostStop`, which writes **and publishes** its state (§2.2).
- `WithPlacement` and `WithRelocation` have no effect on a single node. `SpawnOn` falls through to a local `Spawn` outside a cluster, before placement is read (`actor/spawn.go:295-297`, `:321` in goakt v4.5.4). Relocation applies only when a cluster node departs (`actor/spawn.go:1015-1054`).
- Behavior panics and handler errors follow the supervisor directive (`WithSupervisorDirective`, default restart). The observable result on GoAkt (reply, `EntityExists`, recovered state) cannot be settled by reading. **Spec 0 measures it and records it here before spec 1 starts** (maintainer decision 2026-09-27, §D12).
- `EraseEntity` (`engine.go:1659`) scopes erasure to the caller's tenant in tenant-aware mode and, with `full`, deletes events and snapshots up to the latest sequence number. It touches no actor.
- Projections run in `projection_runner.go` and `projection_actor.go`, both in package `ego` and importing GoAkt. The runner's pull loop uses GoAkt at two points only (a `*goakt.PID` field and one `goakt.Tell`, `projection_runner.go:168`, `:413`), but it lives in package `ego`, so another package cannot import it.

### 2.6 Two mismatches between documentation and code

Found while reading, not changed by this design (Q7):

1. **No-event `SendCommand`.** The port contract says "When the command causes no state change, resultingState is nil" (`port/runtime/runtime.go:95-96`). The GoAkt runtime returns the current state and revision instead (§2.1). An existing test already pins that behavior at the actor level: `event_sourced_actor_test.go:569-590` sends `TestNoEvent` and asserts the reply carries sequence number 2 and the full account state.
2. **`EraseEntity` and crypto-shredding.** The port contract promises crypto-shredding: "When the runtime's encryptor is backed by a key store, it deletes the entity's encryption key" (`port/runtime/runtime.go:115-119`). So does the method's own comment (`engine.go:1656-1658`). The operation exists as `encryption.KeyStore.DeleteKey` (`encryption/key_store.go:45-47`), but `EraseEntity` never calls it (`engine.go:1659-1716`). With `full == false` it deletes nothing, although it still resolves the caller's tenant and can fail closed (`tenancy.ErrDenied`). No test covers key deletion. A second fact matters for the fix: keys are selected by persistence ID alone (`encryption/aes_encryptor.go:48` calls `GetOrCreateKey(ctx, persistenceID)`; `testkit/keystore.go:39` maps persistence ID to key ID), so two tenants that use the same persistence ID share one key, and deleting it would shred the other tenant's data too. Both facts are now tracked in [#166](https://github.com/getsyntegrity/ego/issues/166).

The third fact found while reading is not a documentation mismatch but a code property: the event stream does not order delivery (§2.3). `EventsStream.publishToTopic` starts one goroutine per subscriber per message (`eventstream/stream.go:153-156`), and each goroutine enqueues independently (`subscriber.signal`, `eventstream/subscriber.go:151-162`). The code is the evidence here: a test that reproduces the reordering would fail only some of the time, so it would prove nothing when it passes.

### 2.7 What is GoAkt-free and importable

The contracts a new package needs are all GoAkt-free and outside package `ego`: `command`, `egopb`, `encryption`, `eventadapter`, `eventstream`, `persistence`, `offsetstore`, `projection` (types only), `tenancy`, `port/behavior`, `port/publishing`, `port/runtime`, and `internal/syncmap`, `internal/queue`. The small pure rules the actors apply (`deadline_gate.go`, `reply_classification.go`, `command_context.go`, `tenant_binding.go`, the topic names) import no GoAkt, but they are in package `ego`, so a GoAkt-free package cannot import them.

## 3. Scope boundary

| Concern | Owner | This design |
|---|---|---|
| The in-memory runtime and `compose/inmem` | #148 (here) | Designs them |
| The interface they implement | #147, ego-runtime-001 | Uses it unchanged |
| Placement, supervision, passivation contracts | RUNTIME-003 | Decides in-memory behavior (Q2), leaves the contract to RUNTIME-003 |
| Capability negotiation, a runtime `Descriptor` | RUNTIME-006, ego-arch-004 F-E | Declares nothing; call-time `ErrUnsupported` only |
| Public conformance suite | RUNTIME-007 | Builds an internal table RUNTIME-007 can lift (§D10) |
| Drain and shutdown policy | #24 | Mirrors today's GoAkt order, decides no policy |
| Moving the engine, removing aliases | #124 | Nothing |
| Write-side options | #12 | In-memory ignores them, as every non-GoAkt runtime does (ego-runtime-001 D3) |

## 4. Decisions (D1–D12)

### D1 — Package placement and names

| Package | Module | Visibility | Role |
|---|---|---|---|
| `internal/inmemruntime` (package `inmemruntime`) | root | internal | The runtime: `Config`, `New`, `*Runtime` with `Start`, `Stop`, `AddEventPublishers`, `AddStatePublishers` and the `port/runtime` methods |
| `compose/inmem` (package `inmem`) | root | public | The composition root: `New`, `App`, `Option`, step names |
| `internal/runtimeconsumer` (exists) | root | internal | Gains the characterization harness of spec 0 (§D12), then the neutrality scenarios and their test (§D9, §D10) |

```go
// internal/inmemruntime
type Config struct {
	Families       Family // its own bit set; compose.Family is mapped by compose/inmem
	EventsStore    persistence.EventsStore
	StateStore     persistence.StateStore
	SnapshotStore  persistence.SnapshotStore
	EventAdapters  []eventadapter.EventAdapter
	Encryptor      encryption.Encryptor
	TenantResolver tenancy.TenantResolver
	Stream         eventstream.Stream // allocated by the composition root
	Logger         kitlog.Logger      // github.com/pablogore/kit-logger; nil means the global logger
}

func New(cfg Config) (*Runtime, error) // uses the wall clock (§D11)

var _ runtimeport.Runtime = (*Runtime)(nil) // the #148 compile-time assertion
```

**Why internal.** Consumers only need the runtime through `compose/inmem.App.Runtime()`, which returns `runtimeport.Runtime`. A public `inmemruntime.New` would be a second, unmanaged construction path, the same position `ego.NewEngine` is in next to `compose/goakt` (ego-arch-003 §D1), and it would be public v4 API that cannot be removed before #124. An internal package can be promoted later with an additive change. Rejected alternative: a public `runtime/inmem`. It would also add a top-level `runtime/` directory next to `port/runtime`, two packages named after the same idea. This is decided (Q1, maintainer decision 2026-09-27).

**Why `inmemruntime` and not `inmem`.** `compose/inmem` imports it, and two packages named `inmem` in one file would need an alias every time.

**Why the root module.** The runtime needs `command`, `persistence`, `tenancy` and the other root-level contracts. A nested module would have to require the root module to get them, so its module graph would still contain GoAkt, and ego-arch-006 D6 accepts a module only for module-graph pruning, a toolchain difference or release cadence, none of which applies. A module would also trigger ego-arch-006 F1 (moving the root-level contracts out early). Rejected alternative: a nested `inmem` module.

**Why the runtime does not take a `compose.Spec`.** `internal/inmemruntime` sits outside `compose/`, so `composition-leaf` forbids it to import `compose`. `compose/inmem` translates the `Spec` into a `Config`, exactly as `compose/goakt` translates it into `ego.Option`s (`compose/goakt/option.go`, `egoOptions`).

### D2 — archcheck classification

**Which existing rules apply, unchanged.**

- `composition-leaf` applies to `internal/inmemruntime` (a root-module production package outside `compose/`): it must not import `compose/...`. It does not, by D1.
- `compose/inmem` is under `compose/`, so `composition-leaf` exempts it: it may import `compose`, `compose/internal/lifecycle` and `compose/internal/adapters`, as `compose/goakt` does.
- `composition-no-runtime` does **not** cover `compose/inmem`: its layer is `compose` plus `compose/internal/...` only (`internal/cmd/archcheck/rules/layers.go`, `CompositionLayer`), which deliberately leaves out runtime-specific roots such as `compose/goakt`.
- `no-cross-module-internal`: both packages are in the root module and import only root-module internals.

**One rule is added, `inmem-no-runtime`.** Without it nothing in archcheck stops either package from importing package `ego` or GoAkt directly.

```go
// internal/cmd/archcheck/rules/layers.go
// InMemoryRuntimeLayer: internal/inmemruntime (and subpackages) and compose/inmem (and subpackages).
func InMemoryRuntimeLayer(rootModulePath string) Layer

// internal/cmd/archcheck/rules/rules.go, appended to DefaultRules
{
	ID:          "inmem-no-runtime",
	Description: "the in-memory runtime (internal/inmemruntime) and its composition root (compose/inmem) must not import the root package ego, internal/extensions, the GoAkt runtime or compose/goakt",
	Source:      "ego-runtime-002/design.md §D2",
	Layer:       InMemoryRuntimeLayer(rootModulePath),
	Semantics:   Denylist,
	Forbids:     func(p string) bool { return forbidsRuntime(rootModulePath, p) || hasPathOrSubpath(p, rootModulePath+"/compose/goakt") },
	Reason:      /* runtimeReason, plus a compose/goakt case */,
}
```

It reuses the denylist `application-no-runtime` and `composition-no-runtime` share (`forbidsRuntime`, `rules.go`), plus `compose/goakt`, which would bring GoAkt in through the back door. Its layer matches `internal/inmemruntime` from spec 1, so the rule never matches zero packages (archcheck fails a rule that matches nothing, `docs/ci.md`).

**Why a new rule and not a wider `CompositionLayer`.** Adding `compose/inmem` to `CompositionLayer` would cover the root but not `internal/inmemruntime`, and a violation would be reported as "runtime-neutral composition packages", a layer that by its own definition excludes runtime-specific roots. Rejected alternative: widen `CompositionLayer`.

**Transitive check.** archcheck checks direct edges only. Both packages also get a closure test, modeled on `internal/runtimeconsumer/closure_test.go`, over `go list -deps` **and** `go list -deps -test`: neither GoAkt nor the root package may appear. `compose/inmem`'s test closure must stay GoAkt-free too, which is why the neutrality test does not live there (§D9).

**What changes in the rule table.** One layer and one rule are added, with tests in `internal/cmd/archcheck/rules/evaluate_test.go` (one graph that breaks it for each of the four forbidden targets, one that satisfies it) and one row in `docs/ci.md`'s rule table. No rule is relaxed, no baseline entry is added, and the baseline stays as it is.

### D3 — Lifecycle, unsupported operations, errors

- `New` validates nothing a `compose.Spec` already validated and starts nothing. `Start` marks the runtime started. `Stop` is described in §D8.
- Before `Start` and after `Stop`, every method returns `runtimeport.ErrEngineNotStarted` — except the unsupported ones, which return `*runtimeport.UnsupportedError` in every state, as `port/runtime/runtime.go` requires ("ErrUnsupported comes first").
- Unsupported in this change: the five `Projections` methods. Each returns `&runtimeport.UnsupportedError{Runtime: "inmem", Operation: "StartProjection"}` (and `StopProjection`, `IsProjectionRunning`, `RebuildProjection`, `ProjectionLag`), before any side effect. This is decided (Q3, maintainer decision 2026-09-27).
- **`EraseEntity` is unsupported until spec 7 lands** (maintainer decision 2026-09-27, Q7). Until then it returns `&runtimeport.UnsupportedError{Runtime: "inmem", Operation: "EraseEntity"}` in every lifecycle state, before any side effect. It appears as such in spec 1's method table and in the `compose/inmem` package documentation (§D8). **This limitation does not satisfy the `EraseEntity` contract** (`port/runtime/runtime.go:115-119`). Nothing in this change presents it as doing so: not the proposal, the specs, the #148 criteria mapping or the pull request. Spec 7 implements #166's outcome.
- The runtime's name in errors is the constant `"inmem"`.
- Every sentinel it returns is the `port/runtime` value (`ErrUndefinedEntityID`, `ErrNotACommand`, `ErrEventsStoreRequired`, `ErrDurableStateStoreRequired`, `ErrEntityFamilyNotDeclared`, the three tenant spawn errors), so `errors.Is` holds under either name.
- **Unknown entity.** `SendCommand`/`Dispatch` to an ID that is not alive never spawns, like GoAkt, and returns an error from the runtime. There is no neutral sentinel for it in `port/runtime` today; GoAkt returns its own `ErrActorNotFound`. This design does not add one (Q5).

**Capabilities are declaration-only.** The runtime implements every method, including the unsupported ones, so its method set says nothing about what it supports. It declares no `adapter.Descriptor` in this change: a runtime port and runtime capability constants are RUNTIME-006's (ego-arch-004 F-E), and when they arrive V8b must treat them as declaration-only (`compose/spec.go`, `capabilityCheck.declarationOnly`). No runtime is a `Spec` slot today, so V8 is not involved.

### D4 — Entities

**Structure.** The runtime holds a map from entity ID to an entity record: its behavior, family, tenant binding, current state, revision and mailbox. The map is guarded by one mutex; each record's mailbox is a FIFO queue drained by at most one goroutine at a time (a goroutine is started when work arrives and exits when the queue is empty, so an idle runtime has no goroutines of its own).

**Spawn.** `SpawnEventSourced` and `SpawnDurableState`, in this order, return:

1. `ErrEngineNotStarted` when not started;
2. an error wrapping `ErrEntityFamilyNotDeclared` when `Config.Families` does not contain the family (same message shape as `engine.go:835`);
3. `ErrEventsStoreRequired` / `ErrDurableStateStoreRequired` when the store is missing;
4. the tenant spawn errors when a resolver is configured (§D6);
5. `nil` without change when the ID is alive under the same tenant (idempotent), after resolving `ResolveSpawnOptions(opts...)`.

This is GoAkt's order (§2.1): family before store. So a runtime that declares only `DurableState` and has no events store answers `SpawnEventSourced` with `ErrEntityFamilyNotDeclared`, not `ErrEventsStoreRequired`. The shared table checks that case on both runtimes (§D10).

Otherwise the runtime recovers the entity synchronously before `Spawn` returns: event-sourced entities from the latest snapshot (when a snapshot store is set) plus the events after it, decrypted and adapted, through `HandleEvent`; durable-state entities from `GetLatestState`. A recovery error fails the spawn and registers nothing. Pinging the stores at spawn, as the GoAkt actor does, is kept.

**Commands on an event-sourced entity** run inside the entity's mailbox, in this order: deadline check; `HandleEnvelope` or `HandleCommand` (same preference as §2.1); if no events, the result is `OutcomeSuccess` with the current state and revision (§2.1, not nil; §2.6); `HandleEvent` for each event; deadline check; one `WriteEvents` call with the precondition from the expected revision; on success, update memory, publish each `egopb.Event` to `topic.events` in sequence order, enqueue it to every live saga (§D5), and reply. On a conflict, the result is `OutcomeRejected` with `CodeConcurrencyConflict` and the `*persistence.ConflictError` as cause. **After any failed write, the entity is removed** from the map, exactly when GoAkt stops its actor (§2.1). The only exception is a conflict whose actual revision equals the entity's own revision, which leaves the entity alive and unchanged. Removal happens after the error reply. What happens to commands already queued behind the failed one is not settled by reading. GoAkt's `Shutdown` documents that the actor "completes processing of already enqueued messages" (`actor/receive_context.go:510-513`), and `replyDirect` unstashes before shutting down. So this design provisionally processes them before removal. **Spec 0 measures a command queued behind a failed write on GoAkt** (its reply, and whether it runs), and the result replaces this provisional rule before spec 1 starts. After that, `EntityExists` is false and a later command fails as for an unknown ID (§D3) until the caller spawns again, which recovers from the store. Spec 0 measures liveness after a failed write, an out-of-sync conflict and an in-sync conflict on GoAkt. Spec 6 compares both runtimes (§D10).

**Handler panics and errors** follow the resolved `SupervisorDirective`: `RestartDirective` (the default) re-hydrates the entity from the store and keeps it alive; `StopDirective` removes it. The reply is `OutcomeFailed`. This is provisional: **spec 0 measures a panic under each directive on GoAkt** (the reply, `EntityExists`, the recovered state) and records the result here before spec 1 starts.

**Commands on a durable-state entity** follow §2.2: the new version must be the prior version plus one, else `OutcomeFailed`; one `WriteState`; then memory; then publish the `egopb.DurableState` to `topic.states`.

**Envelope fields.** Persistence ID, sequence or version number, the payload packed in `anypb.Any` (encrypted when an encryptor is set, events only), `Timestamp` from the runtime clock (§D11), encryption key ID and flag, tenant metadata in tenant-aware mode. `Shard` is computed by a documented deterministic function of the persistence ID. It cannot equal GoAkt's, which comes from the actor system's partitioner, so the neutrality proof excludes it (§D9). Shard numbers matter to projections, which the in-memory runtime does not run.

**Results.** The runtime builds `command.Result` values directly, with the same outcome and failure code the GoAkt runtime produces for the same cause: canceled, timed out, concurrency conflict, failed. It does not reproduce GoAkt's wire round-trip or its message strings; failure messages are not part of the observable contract (§D9).

**`EntityExists`** reports whether the ID is in the map of entities **or sagas**, since GoAkt's `ActorExists` also answers true for a live saga (§2.5). An entity stays alive until `Stop`, a failed write, a `StopDirective` or passivation (§D11).

**Write-side settings.** Adapter settings of package `ego` (snapshot interval, retention, batching) are invisible to other runtimes by construction (unexported keys, ego-runtime-001 D3), so the in-memory runtime never writes snapshots, applies no retention, and writes one batch per command. Neutral consumer code cannot request those settings without importing package `ego`, so this does not break neutrality.

### D5 — Sagas

- `SpawnSaga` checks lifecycle, the events store, the saga family and tenancy like an entity spawn, recovers the saga's own events through `ApplyEvent`, and registers the saga as an event recipient. `WithTenant` is the only spawn option it reads, as in GoAkt (`engine.go`, `spawnSaga` doc).
- **Event delivery.** When an entity persists events, the runtime enqueues each one to every live saga's mailbox before the entity replies to the command. Sagas do not read the event stream. **The order in which a saga receives events is unspecified** (maintainer decision on Q8, 2026-09-27). The implementation may happen to deliver one entity's events in persist order, but nothing promises it. No documentation names an order, no exported symbol, option or hook selects or reports one, and no test asserts one.
- In its mailbox, the saga runs `HandleEvent`, persists the action's events to the events store (not to the stream, as `saga_actor.go` does), applies them with `ApplyEvent`, then sends each command through the runtime's internal dispatch with the saga's tenant and metadata derived from its root metadata (the rule of `saga_actor.go:740-749`), default timeout 5 s, and calls `HandleResult` or `HandleError`.
- A saga command to an entity waits for that entity's reply while the saga's own mailbox is busy. That cannot deadlock: an entity never waits on a saga, and delivery to a saga's mailbox never blocks (the queue is unbounded).
- `Complete` sets `SagaCompleted`. `Compensate` runs `behavior.Compensate` and every compensation command in the same mailbox turn, then sets `SagaCompleted` or `SagaFailed` exactly as `saga_actor.go:806-829`. So, as in GoAkt, `SagaCompensating` is never returned by `SagaStatus`.
- **Timeout.** A non-zero timeout starts a timer that enqueues a timeout item to the saga's mailbox; the timeout path compensates as in GoAkt. The timer comes from the runtime clock (§D11): wall time in production, the manual clock in tests, so a timeout test advances the clock instead of waiting.
- **`SagaStatus`** is answered from inside the saga's mailbox, so it sees every item queued before it: `SagaInfo{ID, Status, State}` with the status of #153. The status is not persisted, as in GoAkt.
- **Not running, not reacting.** Once the status is anything but `SagaRunning`, the saga ignores every further event, as `saga_actor.go:482-484` does. The event is dequeued and dropped, without `HandleEvent` and without a write.
- **Tenant rules**, each one enforced in the saga's mailbox and tested on its own in spec 3:
  - **SG4**: a live event's tenant metadata is decoded, and invalid metadata drops the event. A bound saga verifies the event's tenant against its bound tenant before `HandleEvent` and drops a mismatch fail-closed, so a foreign payload never reaches the behavior. In memory a tenant-aware saga is always bound at spawn (as in GoAkt since TENANT-003 T4), so the unbound "bind after an actionable `HandleEvent`" branch never applies.
  - **SG5**: every replayed saga event must carry the spawn-bound tenant; a mismatch fails the spawn and registers nothing.
  - **SG-DUR1**: replay recognizes the `emptypb.Empty` binding marker and skips it instead of calling `ApplyEvent`. The in-memory runtime never writes new markers, as a tenant-aware GoAkt saga no longer does, but it must read journals that contain them.

### D6 — Tenancy, erasure, the event stream and publishers

- **Tenancy** follows GoAkt: the spawn tenant is `WithTenant`, else the resolver's fixed tenant (`tenancy.FixedTenantOf`), else `ErrSpawnTenantUndetermined`; the resolver's `Resolve` is called at `Dispatch`, never at spawn; a command whose resolved tenant differs from the entity's binding is refused; persistence uses `persistence.NewTenantScope`. In memory there is no need to query a binding over the wire, so `ErrSpawnTenantUnverified` is never returned. Without a resolver the runtime uses `persistence.Unscoped()`.
- **`EraseEntity`** is spec 7, blocked only by [#166](https://github.com/getsyntegrity/ego/issues/166) (maintainer decision 2026-09-27). Until spec 7 lands it returns `*UnsupportedError`, which does **not** satisfy the contract (§D3). Spec 7 implements #166's outcome. The port contract's crypto-shredding promise is kept, through an additive extension that deletes a key belonging exclusively to the affected entity and tenant. In tenant-aware mode the caller's tenant is resolved first, failing closed with `tenancy.ErrDenied` even when `full == false`. With `full`, events and snapshots up to the latest sequence number are also deleted. The key granularity is a dependency on #166 (keys are selected by persistence ID alone today, §2.6), not something ego-runtime-002 resolves.
- **`Subscribe`** adds a subscriber to `Config.Stream` subscribed to `topic.events` and `topic.states`. The two topic names are repeated as constants in `internal/inmemruntime`; the shared table (§D10) checks they match what GoAkt publishes.
- **Publishers.** `AddEventPublishers`/`AddStatePublishers` are methods of `*inmemruntime.Runtime`, not of `port/runtime` (they are wiring, ego-runtime-001 D1). Like `engine.go:1391`, each publisher gets its own stream subscriber and goroutine and receives only messages published after it was attached; a duplicate ID is rejected. Ordering is what §2.3 describes for GoAkt, because both use package `eventstream`. Making the stream ordered would be a change to `eventstream` for both runtimes, follow-up FU-C.

### D7 — Concurrency model and guarantees

| Guarantee | In-memory runtime | GoAkt today |
|---|---|---|
| One entity runs one command at a time, in arrival order | yes (mailbox) | yes (actor mailbox) |
| A command's events are persisted before its reply | yes | yes |
| After `SendCommand` returns, the command's events are in the store and queued to every live saga | yes | store yes; saga delivery asynchronous |
| Order in which a saga receives one entity's events | unspecified (the implementation may be ordered) | unspecified (§2.3) |
| `Subscribe` and publishers see one entity's events in order | no (package `eventstream`, §2.3) | no |
| Order across entities | none beyond the caller's own sequencing | none |
| Entity passivates when idle | yes, with `WithPassivateAfter` (§D11, maintainer decision) | yes, with `WithPassivateAfter` (§2.5) |
| An entity survives a failed write | no, except an in-sync conflict | no, except an in-sync conflict (§2.1) |
| Background goroutines when idle | none (publishers' goroutines excepted) | the actor system's |

**Determinism.** For one caller that sends commands one after another, the in-memory runtime's final states, revisions, journals and saga statuses are a pure function of the behaviors, the commands and the store contents. Concurrent callers get a serialization per entity, but which caller goes first is the Go scheduler's choice, as in any runtime.

**Rejected alternative: a single-threaded scheduler** that runs every cascade (an event, the saga it wakes, the commands the saga sends) to completion before `SendCommand` returns. It is fully deterministic but blocks callers on work they did not ask for, differs from GoAkt enough to hide real concurrency bugs, and makes a command sent by a saga re-enter a call that is still on the stack.

**Rejected alternative: one long-lived goroutine per entity.** It gives the same ordering but leaves goroutines behind for every entity ever spawned, which `Stop` must then join. The start-on-demand drain loop has the same guarantee with no idle goroutines.

### D8 — `compose/inmem`

```go
package inmem

const (
	StepProbeStores      = "probe stores"      // same text as compose/goakt
	StepStartRuntime     = "start runtime"
	StepAttachPublishers = "attach publishers" // same text as compose/goakt
)

var ErrNotStartable = errors.New("compose/inmem: app cannot be started: an App is single-use")

var ErrProjectionsUnsupported // see M1 below; wrapped in a *compose.ValidationError

type App struct{ /* spec, options, lifecycle sequence, runtime, stream */ }
type Option func(*options)

func New(spec compose.Spec, opts ...Option) (*App, error)
func (a *App) Start(ctx context.Context) error
func (a *App) Stop(ctx context.Context) error
func (a *App) Runtime() runtimeport.Runtime // nil before a successful Start and for good after a failed one

func WithLogger(logger kitlog.Logger) Option
// No clock option in this chain (maintainer decision on Q9); adding one later is additive.
```

**Validation.** `New` runs `spec.Validate()` (V1–V8, unchanged and shared), then its own rule, and joins every problem as `compose/goakt.New` does:

- **M1** — `Spec.Projections` must be empty, reported as `&compose.ValidationError{Rule: "M1", Field: "Projections", ...}`, because the runtime returns `ErrUnsupported` for projections (D3). Failing at `New` follows #105's "an invalid graph fails at construction, not at first command". Rejected alternative: accept the `Spec` and fail at `Start`. If Q3 is answered by supporting projections, M1 is dropped and a "start projections" step is added.
- `Spec.Name` has no rule in `compose/inmem`: GoAkt's G2 is GoAkt's naming rule, and copying it would make the in-memory root GoAkt-shaped. A `Spec` that passes `compose/goakt` also passes `compose/inmem`, so the neutrality proof can share it. The reverse does not hold for an empty or non-GoAkt name, which is stated in the package documentation.
- `Spec.OffsetStore` is accepted and pinged but unused, since V4 only requires it with projections, which M1 forbids; a `Spec` that sets it without projections is valid on both roots. Neither `Spec.Name` nor an unused `OffsetStore` is observable through `port/runtime`, the stores' entity data or the stream, so these differences cannot affect the neutrality proof.

**compose/goakt options with no in-memory equivalent.** `WithCluster` (and rule G1), `WithActorSystemOptions` and `WithTelemetry` have none. Options are typed per package, so a consumer cannot pass them to `compose/inmem`; there is nothing to validate. `WithTelemetry` in particular takes an `*ego.Telemetry`, which `compose/inmem` cannot name without importing package `ego`; telemetry for any runtime waits for #31. `WithLogger` exists in both, with the same kit-logger type.

**Start.** Three steps through `compose/internal/lifecycle`, with `Spec.ShutdownTimeout` as the cleanup bound:

| Step | Action | Undo on later failure |
|---|---|---|
| 1 `probe stores` | Ping every configured store through `adapter.PingerOf`, same code shape as `compose/goakt/app.go` `probeStores` | nothing |
| 2 `start runtime` | Allocate the event stream (`eventstream.New`), `inmemruntime.New`, `Start` | `runtime.Stop`, which closes the stream |
| 3 `attach publishers` | `adapters.StartAndProbe` over the owned publishers, then `AddEventPublishers`/`AddStatePublishers` | attached publishers are closed by step 2's undo |
| — | any failure | release every publisher never attached, then return `*compose.StartError` naming the step |

GoAkt's steps 2 and 3 ("start actor system", "start engine") build two objects; the in-memory runtime is one object, so they become one step. GoAkt's step 5 ("start projections") has nothing to do after M1. The order of what remains, the rollback, publisher ownership (transferred at `New`, ego-arch-003 §D5), the single-use state machine and `Stop` idempotence are the same, because the same `lifecycle.Sequence` and the same `adapters.StartAndProbe` run them. Rejected alternative: keep five step names with two no-op steps, which would give `StartError.Step` names that describe nothing.

**This departs from ego-arch-003 §5.2.** That section expected `compose/inmem` to differ from `compose/goakt` "only in step 2 of D6 (build an in-memory runtime instead of a GoAkt actor system) and step 4 of D7 (stop that runtime instead of `sys.Stop`)". This design differs in three more ways: it merges start steps 2 and 3 into one, it has no projections step, and it adds validation rule M1. ego-arch-003 §5.2 was written before `port/runtime` existed, when an in-memory "engine" and "runtime" were expected to be two objects. Spec 6 records the departure in ego-arch-003 §6 next to the IMPL-6 row.

**Stop.** `lifecycle.Sequence.Stop` undoes the steps in reverse: publishers attached in step 3 have no undo of their own, and step 2's undo is `Runtime.Stop`, which (1) marks the runtime stopped, so new calls get `ErrEngineNotStarted`; (2) closes the publishers and the stream, like `Engine.Stop`; (3) stops every entity and saga, bounded by the cleanup context `Stop` receives. For each one it waits until the mailbox turn in progress, if any, has finished, so no command is cut off halfway. Only then does a durable-state entity write its state once more, unconditionally, as `PostStop` does in GoAkt. The wait is also what joins the drain goroutines. If the context expires first, `Stop` returns an error naming the entities still busy and skips their final write rather than racing the turn in progress. **Skipping the final write on timeout is provisional**: it is part of the drain policy #24 (LIFE-004) owns, and it follows #24's decision on both runtimes. The final state write therefore happens after publishers are closed, which is the order `compose/goakt`'s `TestStop_D7OpenQuestion_StateFlushedDuringActorShutdown` records for GoAkt.

**Queued items at `Stop` (provisional).** Items still queued in a mailbox behind the current turn are answered with `ErrEngineNotStarted` rather than run. This is a provisional choice: what happens to work in flight at shutdown is #24's drain policy (LIFE-004), and when #24 decides, this rule follows it on both runtimes.

**`Runtime()`** returns the runtime as `runtimeport.Runtime` with the same nil rule as `compose/goakt` (ego-runtime-001 D6): an untyped nil before `Start` succeeds and after a failed `Start`, the stopped runtime after `Stop`.

**Package documentation** states:

- the runtime is for tests and local development;
- which guarantees are in-memory-only (§D7);
- that the order in which sagas receive events is **unspecified**, naming no order (maintainer decision on Q8);
- that `EraseEntity` returns `ErrUnsupported` until crypto-shredding lands ([#166](https://github.com/getsyntegrity/ego/issues/166)), and that this does not meet the `port/runtime` erasure contract;
- what differs from `compose/goakt`.

**Tests carry over in shape.** The IMPL-2/IMPL-3 test shapes apply unchanged: `compose/spec_test.go` and `compose/internal/lifecycle` are shared code and are not touched. `compose/inmem/app_test.go` mirrors `compose/goakt/app_test.go` one for one where the step exists: valid `Spec` runs a runtime, missing dependency fails at `New` with nothing started, every problem reported at once, failure injected at each step releases everything, cancelled context starts nothing, `Stop` before `Start` closes publishers, `Stop` twice is a no-op, stop order, undeclared family returns the typed error, publisher failure at *k*, V8 rejects a lying publisher, plus M1. Failure injection uses the same `hooks.afterStep` pattern as `compose/goakt/app.go`.

### D9 — The neutrality proof

**What runs.** `internal/runtimeconsumer` (production code, closure already free of `ego` and GoAkt) gains one exported scenario function per family, beside the existing `Run`:

```go
// Observe drives r through the scenario and returns what a consumer can see.
func Observe(ctx context.Context, r runtimeport.Runtime, stores Stores) (Trace, error)
```

Its production closure then needs two more first-party packages, `persistence` (to read the journal and states back through `Stores`) and `egopb` (the envelopes it normalizes). So `allowedFirstParty` in `internal/runtimeconsumer/closure_test.go:59-68` grows by exactly those two entries, and still admits neither the root package nor GoAkt. Both are contracts under ego-arch-001 §3.

It uses the existing account behavior plus one durable-state behavior and one saga, all on `test/data/testpb` messages. It subscribes before the first command, spawns, sends a fixed command sequence (including one command that emits no event, one rejected by an expected revision, and one that triggers the saga), waits for the saga's final status with `SagaStatus` under a context deadline, and reads the journal and latest states back from the stores.

**What is compared.** `Trace` holds, after normalization:

- each `SendCommand`/`Dispatch` result: outcome, failure code, revision, and the state (compared with `proto.Equal`);
- the journal from `EventsStore.ReplayEvents` per persistence ID: sequence number, payload, `IsDeleted`, tenant metadata;
- the latest durable state from `StateStore.GetLatestState`: version and payload;
- the stream messages received through `Subscribe`: topic, persistence ID and sequence or version number, compared as a **multiset**, since §2.3 gives no order. A multiset is a sorted slice, so a duplicated message is caught where a set would hide it. Collection stops when the count reaches what the journal and the latest states imply: one message per persisted event, plus one per durable-state write, including the write a durable-state entity makes and publishes when it passivates (§D11). It also stops when the context deadline passes, and that counts as a failure, never as a shorter trace;
- the saga's final `SagaInfo.Status` and state;
- `EntityExists` for each ID.

Removed before comparison: timestamps, `Shard`, encryption key IDs, failure message text.

**Where the test lives.** `internal/runtimeconsumer/neutrality_test.go`, external test package `runtimeconsumer_test`. It builds one `compose.Spec` value per run (fresh `testkit` stores for each root, so the second run cannot see the first run's journal), starts `compose/goakt` and `compose/inmem` from it, calls `Observe` on each `App.Runtime()`, and requires equal traces. Why here:

- Test files may import both roots: `composition-leaf` and every other archcheck rule look at production imports only.
- The package's production closure test (`go list -deps .`) is unaffected by test imports, so `Observe` itself stays provably free of `ego` and GoAkt.
- Rejected: `compose/inmem/neutrality_test.go`. It would put GoAkt in `compose/inmem`'s test closure, which its own closure test forbids (D2).
- Rejected: `compose/neutrality_test.go`. It would put both runtimes in the test closure of the runtime-neutral `compose` package, and every GoAkt change would then select the `compose` tests.
- Rejected: the `test/compat` module. It checks compatibility against released versions (ego-arch-006 S1), a different question.

`internal/cmd/ciselect` needs no change: a test's direct test imports are part of its "affected" expansion (`docs/ci.md`, "Reverse-dependency expansion"), so a change in `compose/goakt`, `compose/inmem`, `internal/inmemruntime` or the root package selects this test.

### D10 — Testing strategy and the shared table

- **Deterministic.** No `time.Sleep` and no `pause.For` anywhere. The in-memory runtime's guarantees (D7) let unit tests assert right after `SendCommand` returns. Asynchronous facts are awaited with two named helpers that spec 0 adds to `internal/runtimeconsumer`:
  - `awaitStream(ctx, sub, want int) ([]Message, error)` receives from the subscriber until it holds `want` messages. It fails when `ctx` expires first.
  - `awaitCondition(ctx, check func(context.Context) (bool, error)) error` re-evaluates `check` until it reports true or `ctx` expires. It follows the pattern of the repository's `waitFor` (`publisher_test.go:138`). It is used only where no channel exists: `SagaStatus` on GoAkt, and `EntityExists` after passivation.

  Inside `internal/inmemruntime`, unit tests drive passivation and saga timeouts with the internal manual clock (§D11), never with wall time.
- **Saga delivery order in tests** (maintainer decision on Q8). No test asserts a delivery order. The shared table's own comparison fixtures (the sagas `Observe` uses) must produce the same trace under any delivery order. That is a constraint on those fixtures only, not on consumer behaviors, which may depend on order.
- **No `-race` locally, no workbench.** CI remains the race gate; `internal/inmemruntime` must be race-clean there.
- **Unit tests** in `internal/inmemruntime` (package-internal, to reach the mailbox) cover each rule of D3–D7 with `testkit` stores and the `mocks/` stores for failure injection.
- **The shared table.** `internal/runtimeconsumer` holds `Scenarios`, a slice of `{Name string; Run func(ctx, runtimeport.Runtime, Stores) (Trace, error)}`. Spec 0 creates the table and its GoAkt-only runner. Spec 6 adds `Observe` and runs every entry on both roots. The entries cover the rules that were duplicated from package `ego` instead of imported, plus the behaviors this design could not settle by reading, which spec 0 measures on GoAkt first:
  - D4 "Results", the deadline gates, the no-event reply, the topic names and the conflict mapping;
  - **spawn order**: with only `DurableState` declared and no events store, `SpawnEventSourced` returns `ErrEntityFamilyNotDeclared`;
  - **liveness after failure**: `EntityExists` after a failed write, an out-of-sync conflict and an in-sync conflict, plus a command queued behind a failed write (its reply and whether it runs);
  - **panics**: a handler panic under `RestartDirective` and under `StopDirective`, recording the reply, `EntityExists` and the recovered state;
  - **ignored settings**: spawning with `WithPlacement(Random)`, `WithPlacement(LeastLoad)`, `WithPlacement(Local)` and `WithRelocation(true)` behaves as a spawn without them, and a saga spawned with options other than `WithTenant` behaves as one spawned without them;
  - **passivation** (§D11): an idle entity stops, `EntityExists` becomes false, `Dispatch` does not re-spawn it, and a new spawn recovers its state. A refused command and a no-event command both count as activity, and a passivating durable-state entity writes and publishes its state;
  - **`EntityExists` on a saga ID** (true while the saga lives).
  
  The neutrality test runs every scenario on both roots. This is how drift in duplicated logic is caught.
- **Not RUNTIME-007.** The table is internal, compares two runtimes with each other rather than against written expectations, and has no public API. RUNTIME-007 can lift it into a public package such as `port/runtime/runtimetest` later; nothing here fixes that package's name or shape.

### D11 — Passivation (maintainer decision 2026-09-27)

**Decision recorded.** On 2026-09-27 the maintainer decided that passivation must be observable behavior, never silently ignored. The preferred option is to implement `WithPassivateAfter` in the in-memory runtime with the observable semantics of single-node GoAkt (§2.5). The fallback, used only if implementing it is not a reasonable scope, is an `*UnsupportedError` returned at spawn when `WithPassivateAfter` is set.

**This design takes the preferred option: implement it.** The scope is small and fits one spec, spec 4 (`inmem-runtime-passivation`), with three tasks, since the clock is built in spec 1. The runtime already has the pieces it needs. Every entity has a mailbox that serializes work, and there is a removal path, since failed writes and `StopDirective` remove entities (§D4). A durable-state final write already exists for `Stop` (§D8). Passivation adds one timer per entity that has a non-zero `PassivateAfter()`, and one mailbox item kind. The fallback would save about one spec of work, but every consumer that sets `WithPassivateAfter` would then fail on the in-memory runtime while succeeding on GoAkt, which is the kind of divergence #105 forbids.

**Semantics** (the GoAkt details of §2.5; spec 0 measures the activity cases before spec 1 starts):

- **Activity.** Every item the entity's mailbox processes resets the idle timer, except the passivate item itself. That includes commands, refused commands (for example a tenant mismatch or a failed precondition) and no-event commands. Idle time counts from the start of the turn, as GoAkt's `markActivity` does. The in-memory runtime has no persist-response or stash messages; its single turn covers what those messages mark in GoAkt.
- **Firing.** When the timer fires, it enqueues a *passivate* item. The item runs in the mailbox like any command, so it never interleaves with one. When it runs, it checks against the clock that the entity has really been idle for the whole period, since a command may have arrived between the timer firing and the item running.
- **Removal.** If the entity has been idle, it is removed. A durable-state entity first writes **and publishes** its state, as GoAkt's `PostStop` does.
- **After removal.** `EntityExists` reports false, `SendCommand`/`Dispatch` fail as for an unknown ID and do not re-spawn, and a new spawn recovers the state from the stores.
- **Sagas** ignore the setting, as in GoAkt (§2.5).
- **`Stop`** stops every idle timer, and the goroutine-leak checks cover them.
- **Timing.** GoAkt's timing is approximate to about 100 ms (coalescing, §2.5), so the neutrality scenario compares outcomes, never exact times.
- **Re-check, stricter than GoAkt.** GoAkt does not re-check activity when the deadline fires (§2.5); the in-memory passivate item does. The in-memory runtime is therefore stricter only for activity inside the last 100 ms before the deadline, which is within this section's 100 ms tolerance. It never passivates an entity that GoAkt would keep.

**Determinism: an internal clock** (maintainer decision on Q9, 2026-09-27: no public clock in this chain). Spec 1 adds `internal/inmemruntime/clock.go` with three unexported pieces:

- `clock`, an interface with `now() time.Time` and `afterFunc(d time.Duration, f func()) (stop func() bool)`;
- `wallClock`, which `New` uses;
- `manualClock`, reachable only by the package's own tests, through the unexported constructor `newWithClock(cfg Config, c clock)`.

`manualClock.advance(d)` moves time forward and runs every callback whose deadline is at or before the new time, synchronously, in the caller's goroutine. It runs them ordered by deadline, then by registration order. It repeats until no callback is due, so a callback registered during `advance` with a due deadline also runs before `advance` returns. A unit test therefore makes an entity idle by calling `advance`, not by waiting. Because the callback only enqueues, the test then waits with an internal helper until the mailbox has run the item. No wall-clock time is involved. The same clock drives event timestamps and saga timeouts (§D5).

`compose/inmem` uses the wall clock and exports no clock option. Adding `WithClock` later is additive.

**The neutrality scenario.** The shared table's passivation scenario spawns with `WithPassivateAfter(d)`, `d ≥ 1 s`, on both roots and uses wall time on both, since neither composition root exposes a clock. Activity messages are spaced at least `d/2` apart, counting the spawn as the first message. That gap is much larger than the 100 ms coalescing window, so a wrong activity rule shows up as passivation about `d/2` early. The gap is kept with a bounded timed wait (a timer in a `select` with the context), the only wall-clock wait allowed. The scenario waits with `awaitCondition` until `EntityExists` is false, bounded by the context deadline, and asserts that the time elapsed since the last message is at least `d − 100 ms`. It then checks that `Dispatch` does not re-spawn the entity. It never asserts that an entity is still alive at a given moment, since that is where coalescing makes GoAkt approximate. This is a condition wait, not a sleep. The deterministic, clock-driven cases live in `internal/inmemruntime`'s unit tests.

**Rejected alternative: the fallback typed error.** Explained above: it is the smaller change but a visible divergence for any consumer that uses the option.

**Rejected alternative: real timers in the in-memory unit tests.** They would inherit wall-clock flakiness for no benefit.

**Rejected alternative: a public `WithClock` now.** Decided against by the maintainer on 2026-09-27 (Q9). It would be public API for a test concern, and it can be added later without a break.

### D12 — Spec 0: characterizing GoAkt first (maintainer decision 2026-09-27)

Several rules in §D4 and §D11 copy GoAkt behaviors that reading could not settle: a panic under each supervisor directive, a command queued behind a failed write, and which messages count as passivation activity. The maintainer decided on 2026-09-27 to measure them before any in-memory code is written, rather than correct the design afterwards.

- **Where.** Spec 0 lives in `internal/runtimeconsumer`. The scenarios are neutral production code written against `port/runtime`, and they are the same entries spec 6 later runs on both roots. The GoAkt-only runner is in a `_test.go` file that uses `compose/goakt`. Spec 0 touches no hot spot and no root-package file.
- **What it measures:**
  - liveness after a failed write, an out-of-sync conflict and an in-sync conflict;
  - a command queued behind a failed write: its reply, and whether it runs;
  - a panic under `RestartDirective` and under `StopDirective`: the reply, `EntityExists` and the recovered state;
  - passivation activity: a refused command, a no-event command, and a durable-state entity's write and publish on passivation;
  - the ignored options: placement, relocation, and saga options other than `WithTenant`.
- **Output.** Each measured result is written into §2, §D4, §D11 and the Q2 table in the spec 0 pull request, replacing the word "provisional". Specs 1–3 start only after that. From then on no rule in this design depends on a later measurement.
- **Contradictions.** **When a measurement contradicts a maintainer decision** (§5, "Maintainer decisions recorded") or a recommendation the maintainer relied on, spec 0 does not amend the design. It records the measurement in the pull request, marks the affected rule "blocked on maintainer", and stops. Spec 1 waits for the maintainer's answer.


## 5. Questions and maintainer decisions

Every question below was decided by the maintainer on 2026-09-27. The options and the reasoning are kept for the record.

**Maintainer decisions recorded (2026-09-27).**

1. Passivation is implemented with single-node GoAkt semantics (part of Q2, §D11).
2. Spec 0 characterizes GoAkt before spec 1 starts (§D12).
3. Saga delivery order is unspecified on every runtime (Q8).
4. There is no public clock in this chain (Q9).
5. `EraseEntity` gets its own spec, spec 7, blocked only by #166. It implements #166's crypto-shredding with entity-and-tenant key granularity (part of Q7).
6. **Q1, Q3, Q4, Q5, Q6 and the no-event `SendCommand` part of Q7 are accepted as recommended:**
   - Q1: the runtime is internal, in `internal/inmemruntime`.
   - Q3: `ErrUnsupported` plus rule M1, with runner extraction as FU-A. This covers #148's typed-error criterion only for projections.
   - Q4: the pure rules are duplicated now, with the shared table as the drift check; FU-B extracts them later, with #124.
   - Q5: no neutral "entity not found" error in this chain (FU-D).
   - Q6: no public quiescence helper.
   - Q7: follow the code and fix the contract text (FU-E).
7. **Q2 is accepted as recommended, conditional on spec 0's characterization.**
   - Placement and relocation are a no-op, provisional pending RUNTIME-003, and #148's wording is amended.
   - The supervisor directive is decided from spec 0's measurement.
   - If spec 0's measurement contradicts these, spec 0 stops and returns to the maintainer (§D12, "Contradictions").
8. **#148 closure rule (maintainer decision 2026-09-27).** #148 is closed only after spec 7, because until then `compose/inmem`'s `EraseEntity` returns `ErrUnsupported` and does not meet the `port/runtime` contract. Specs 0–6 may advance and merge separately. Each of their pull requests states that limitation, uses "Refs #148" and never a closing keyword, and does not mark #148 complete. Spec 6's criteria mapping records #148 as still open pending spec 7. Only spec 7's pull request carries the closing keyword ("Closes #148").


**Q1 — Public or internal runtime package.**
(a) `internal/inmemruntime`, reached only through `compose/inmem`. (b) Public `runtime/inmem` with its own `New`.
*Decided (2026-09-27): (a), as recommended.* It keeps the new public API to `compose/inmem`, avoids a second construction path, and can be promoted additively later; (b) cannot be withdrawn inside v4.

**Q2 — Spawn settings: what the in-memory runtime does with each one.** Any choice here is **provisional pending RUNTIME-003**. ego-runtime-001 D4 rule 3 leaves unhonorable spawn settings to RUNTIME-003, so whatever the maintainer picks now is revisited when RUNTIME-003 writes the contract. The choice is kept inside one function of the spawn path so it can change.

| Setting | GoAkt on a single node (evidence) | In-memory, as designed | Status |
|---|---|---|---|
| `WithPlacement` | Ignored: `SpawnOn` falls through to a local `Spawn` outside a cluster, before placement is read (`actor/spawn.go:295-297`, `:321`, goakt v4.5.4) | Ignored | **Decided (2026-09-27): no-op**, conditional on spec 0 and provisional pending RUNTIME-003 |
| `WithRelocation` | Ignored: relocation happens only when a cluster node departs (`actor/spawn.go:1015-1054`) | Ignored | **Decided (2026-09-27): no-op**, same conditions |
| `WithPassivateAfter` | **Honored** (`engine.go:1910-1911`): an idle entity stops, `EntityExists` becomes false, `Dispatch` does not re-spawn | **Implemented with the same semantics**, including the activity rule and the write-and-publish on passivation (§D11, spec 4); spec 0 measures the activity cases | **Decided by the maintainer, 2026-09-27**: implement; fallback typed error only if the scope is unreasonable, and the design finds it reasonable |
| `WithSupervisorDirective` | Applied to panics and handler errors; the observable result is not yet measured (§2.5) | Restart re-hydrates, stop removes (§D4) | **Decided (2026-09-27): follows spec 0's measurement**, which is recorded in this row and §D4 before spec 1 starts; a contradiction with §D4's provisional rule returns to the maintainer (§D12) |
| `WithTenant` | Honored | Honored (§D6) | not a question |
| Write-side adapter settings (`WithSnapshotInterval`, `WithRetentionPolicy`, `WithBatchThreshold`, `WithBatchFlushWindow`) | Honored, GoAkt only | Ignored **by contract**: other runtimes cannot read another adapter's settings (ego-runtime-001 D3) | not a question |
| Any option but `WithTenant` on `SpawnSaga` | Ignored (`engine.go:1497-1501`, `:1563-1567`); spec 0 measures it | Ignored | not a question |
| `Spec.Name`, an unused `Spec.OffsetStore` | Not observable through `port/runtime` or the stores' entity data | Accepted | not a question |

For placement and relocation:
(a) Treat them as a no-op, exactly as single-node GoAkt does. The table's ignored-settings scenario spawns with `Random`, `LeastLoad`, `Local` and `WithRelocation(true)` on both roots to prove it.
(b) Fail the spawn with `&runtimeport.UnsupportedError{Runtime: "inmem", Operation: "SpawnEventSourced: WithRelocation(true)"}` (and similarly for a non-default placement) before anything is spawned.
*Decided (2026-09-27): (a), as recommended, conditional on spec 0.* The default placement (`RoundRobin`) cannot be told apart from an explicit one in `SpawnSettings`, so (b) could only reject non-default values. The same consumer code would then succeed on single-node GoAkt and fail in memory, which breaks the neutrality #105 asks for.

**#148's wording is amended** (maintainer decision 2026-09-27). The criterion "capabilities the in-memory runtime does not support (for example cluster placement) return an explicit typed error" no longer uses cluster placement as its example, since under (a) cluster placement is not an error on either runtime. ego-runtime-001 D4 rule 3 had flagged this amendment as the maintainer's.

**Q3 — Projections.** Note on #148: returning `ErrUnsupported` for the five projection methods satisfies #148's typed-error criterion **only for the projection capability**. It says nothing about spawn settings, which Q2 covers.
(a) Return `ErrUnsupported` now; `compose/inmem` rejects a `Spec` with projections (M1); follow-up FU-A extracts the projection runner. (b) Include a projection runner in this chain.
*Decided (2026-09-27): (a), as recommended.* The runner is in package `ego` (§2.5), so (b) either duplicates about 870 lines of pull loop, backoff and offset logic, or moves them out of package `ego`, which touches the hot root package this chain otherwise leaves alone. The runner also polls the store on tickers, a background activity the in-memory runtime otherwise does not have (D7). FU-A can replace the runner's one `goakt.Tell` with a callback, move it to an internal package, and let both runtimes use it; M1 is then dropped.

**Q4 — Duplicate or extract the small pure rules of package `ego`.**
(a) Re-implement them in `internal/inmemruntime` (deadline gate, result building, metadata derivation, event envelope, topic names), with the shared table (D10) as the drift check. (b) First move them from package `ego` into internal GoAkt-free packages that both runtimes import.
*Decided (2026-09-27), as recommended: (a) for this chain, (b) as a follow-up (FU-B) aligned with #124.* (b) is the better end state, but it edits `engine.go`, `event_sourced_actor.go`, `durable_state_actor.go`, `saga_actor.go` and `reply_classification.go`, which have several concurrent writers, and it changes the GoAkt adapter while its behavior is the reference this chain measures against.

**Q5 — A neutral "entity not found" error.**
(a) Nothing in this chain: the in-memory runtime returns a descriptive error that wraps no sentinel, and the neutrality scenarios do not cover the case. (b) Add `runtimeport.ErrEntityNotFound` and make both runtimes wrap it; GoAkt would wrap `ErrActorNotFound` so both `errors.Is` checks hold.
*Decided (2026-09-27), as recommended: (a) now, (b) as a small follow-up (FU-D) under RUNTIME-002 or #29,* because (b) edits `engine.go` and changes the error GoAkt callers see (wrapping keeps `errors.Is`, but not `==`).

**Q6 — A public quiescence helper.** Should `compose/inmem.App` export something like `WaitIdle(ctx) error` (all mailboxes empty) for consumer tests?
*Decided (2026-09-27), as recommended: no, not in this chain.* D7 already makes most facts true when `SendCommand` returns, and a public helper would be in-memory-only API that consumer tests then depend on, so the same test could not run on GoAkt. The runtime keeps an internal version for its own tests.

**Q7 — The two mismatches of §2.6.**
- *`EraseEntity` (decided by the maintainer, 2026-09-27):* keep the contract's crypto-shredding promise, as [#166](https://github.com/getsyntegrity/ego/issues/166) specifies: an additive extension, and a key that belongs exclusively to the entity and tenant. The in-memory runtime gets it in its **own small spec, spec 7, blocked only by #166**. Specs 0–6 do not wait on #166. Until spec 7 lands, the in-memory `EraseEntity` returns `*UnsupportedError`. That limitation **does not satisfy the contract** (`port/runtime/runtime.go:115-119`), and no document in this change says it does (§D3).
- *No-event `SendCommand` (decided by the maintainer, 2026-09-27):* should the in-memory runtime follow the code (the current state and revision) or the contract text (`port/runtime/runtime.go:95-96`, nil)?
  *Decided, as recommended: follow the code, and correct the contract text (FU-E).* Following the text would make the two runtimes differ, which is exactly what the neutrality proof must not show.

**Q8 — Saga delivery order (decided by the maintainer, 2026-09-27).** Saga delivery order is **unspecified on every runtime**, and the in-memory runtime promises no order. Consumer sagas may depend on order; the design does not forbid that, it only promises nothing. The check is that nothing promises an order:

- (a) the `port/runtime` and `compose/inmem` package documentation say the order is unspecified and name no order (spec 3 adds the sentence to `port/runtime`'s `Sagas` documentation; spec 5 adds it to `compose/inmem`);
- (b) no exported symbol, option or hook selects or reports an order;
- (c) no test asserts a delivery order.

Only the shared table's comparison fixtures must produce the same trace under any order. That constrains the fixtures, not consumer behaviors.

**Q9 — A public clock (decided by the maintainer, 2026-09-27).** A public `inmem.Clock` / `WithClock` is out of this chain. The manual clock stays internal to `internal/inmemruntime` (§D11). Adding a public clock later is additive.

## 6. Compatibility and apidiff

| Package | Spec 0 | Spec 1 | Spec 2 | Spec 3 | Spec 4 | Spec 5 | Spec 6 | Spec 7 |
|---|---|---|---|---|---|---|---|---|
| `internal/runtimeconsumer` (internal) | extended | — | — | — | — | — | extended | — |
| `internal/inmemruntime` (new, internal) | — | new | extended | extended | extended | — | — | extended |
| `compose/inmem` (new, public) | — | — | — | — | — | new: additions only | — | one doc sentence |
| `internal/cmd/archcheck/rules` (internal) | — | one layer, one rule | — | — | — | — | — | — |
| `port/runtime` | — | — | — | doc comment only (Q8) | — | — | — | — |
| `ego`, `port/behavior`, `compose`, `compose/goakt`, `compose/internal/...`, `eventstream` | unchanged | unchanged | unchanged | unchanged | unchanged | unchanged | unchanged | unchanged |

apidiff: only spec 5 produces a report, and it must list additions only (a new package). Every other spec must show no report for any public package; spec 3's `port/runtime` change is a doc comment. SemVer: a minor release. `CHANGELOG.md` is edited by specs 5, 6 and 7: one entry for the new package in spec 5, one line for the neutrality proof in spec 6, and one line in spec 7, because `compose/inmem.App.Runtime().EraseEntity` changes from `ErrUnsupported` to working (an observable change of a public package, although apidiff reports nothing). No line may present the `EraseEntity` limitation as meeting the contract. No `Deprecated:` marker is added anywhere (ego-arch-001 §10 as corrected by ego-runtime-001 Q1).

## 7. Risks

- **Drift between two implementations of the same rules.** Mitigated by the shared table (D10) and by FU-B. Residual risk: a rule no scenario exercises.
- **The reference moves.** A GoAkt change (for example #24's drain policy or a RUNTIME-003 decision) makes the neutrality test fail on the in-memory side. That is the test working; the cost is that such changes now need a matching in-memory change.
- **Ordering expectations.** A consumer who tests on the in-memory runtime may come to rely on the order in which its sagas happen to receive events there. That order is unspecified on every runtime (Q8). The mitigation is the "unspecified" sentence in the `port/runtime` and `compose/inmem` documentation, which names no order.
- **Test-only use in production.** The in-memory runtime has no clustering, and it keeps every entity in memory until `Stop`, a failed write, a `StopDirective` or passivation removes it. Its documentation says it is for tests and local development (#11's own wording).
- **Goroutine leaks on `Stop`.** Publisher goroutines and saga timers must be joined or stopped; specs 2, 3 and 4 each carry a leak check (`runtime.NumGoroutine` comparison around `Stop`, condition-waited).

## 8. Chain of specs and file ownership

| # | Spec | Tasks | Depends on | Owns |
|---|---|---|---|---|
| 0 | `goakt-characterization` | 4 | this design | `internal/runtimeconsumer/**`; this design's §2, §D4, §D11 and Q2 table (recording the measurements) |
| 1 | `inmem-runtime-core` | 5 | spec 0 | `internal/inmemruntime/**` (new, including `clock.go`); `internal/cmd/archcheck/rules/layers.go`, `rules.go`, `evaluate_test.go`; `docs/ci.md` (rule table row) |
| 2 | `inmem-runtime-state` | 5 | spec 1 | `internal/inmemruntime/**` (stream, durable state, encryption, publishers and `Stop`, tenancy files) |
| 3 | `inmem-runtime-sagas` | 5 | specs 1, 2 | `internal/inmemruntime/**` (saga files); `port/runtime/runtime.go` (one doc sentence, Q8) |
| 4 | `inmem-runtime-passivation` | 3 | specs 1, 2 | `internal/inmemruntime/**` (passivation files) |
| 5 | `compose-inmem` | 5 | specs 1, 2 | `compose/inmem/**` (new); `CHANGELOG.md` |
| 6 | `runtime-neutrality` | 4 | specs 3, 4, 5 | `internal/runtimeconsumer/**`; `CHANGELOG.md`; `openspec/changes/ego-arch-003/design.md` §6/§7 (IMPL-6 row and the §5.2 departure), `openspec/changes/ego-arch-001/design.md` §4 (map row) |
| 7 | `inmem-runtime-erasure` | 3 | spec 2 and [#166](https://github.com/getsyntegrity/ego/issues/166) (blocking) | `internal/inmemruntime/**` (erasure files); `internal/runtimeconsumer/**` (an erasure scenario for the shared table); one sentence of `compose/inmem`'s package documentation; `CHANGELOG.md` |

Specs 2, 3, 4 and 7 all write `internal/inmemruntime`, in different files. Run them one after another, or in parallel only on files agreed in advance. Spec 5 can run in parallel with specs 3 and 4. Spec 7 can land at any point after spec 2, whenever #166 is settled. If spec 5 has merged by then, spec 7 also removes the `EraseEntity` limitation sentence from `compose/inmem`'s documentation.

**Hot spots.** No spec touches `engine.go`, `option.go`, any other root-package file, `.github/workflows/*` or `internal/cmd/ciselect/**`. The only shared tool file is archcheck's rule table, edited by spec 1 alone; rebase it onto any open archcheck change. `CHANGELOG.md` is edited by specs 5, 6 and 7. `port/runtime/runtime.go` gets one doc sentence in spec 3 (Q8), and FU-E and #166 also touch that file, so whichever lands second rebases.

**#148 closure rule (maintainer decision 2026-09-27).** #148 is closed only after spec 7, because until then `compose/inmem`'s `EraseEntity` returns `ErrUnsupported` and does not meet the `port/runtime` contract. Specs 0–6 may advance and merge separately. Each of their pull requests states that limitation, uses "Refs #148" and never a closing keyword, and does not mark #148 complete. Spec 6's criteria mapping records #148 as still open pending spec 7. Only spec 7's pull request carries the closing keyword ("Closes #148").

**Follow-ups named here (outside the chain):** FU-A projection runner for both runtimes (Q3); FU-B extract shared pure rules from package `ego` (Q4); FU-C ordered delivery in `eventstream` (§2.3, D6); FU-D neutral entity-not-found error (Q5); FU-E the no-event `SendCommand` contract text (Q7). The `EraseEntity` mismatch is [#166](https://github.com/getsyntegrity/ego/issues/166), which blocks spec 7 only.

## 9. Evidence and reproduction

| Claim | How it was obtained |
|---|---|
| Every `file:line` in §2 | Read on `57c4b11` with `rg -n` and the files themselves |
| Stream delivery is unordered | `eventstream/stream.go:153-156` (one goroutine per subscriber per message) and `eventstream/subscriber.go:151-162`. A reproducing test would fail only some of the time, so the code is the evidence |
| No-event command returns the current state | `event_sourced_actor.go:991-993`; `engine.go:1866-1873`; `command.NewSuccessNoState` has no production caller (`rg -n NewSuccessNoState` outside `command/`) |
| `EraseEntity` never deletes a key | `engine.go:1659-1716`: no `KeyStore` or encryptor call |
| A failed write or an out-of-sync conflict stops the entity | `event_sourced_actor.go:830-837`, `:1094`, `:1137-1143`; goakt v4.5.4 `actor/receive_context.go:510-515` |
| Spawn checks family before store | `engine.go:758-775`, `:1517-1533` |
| Durable-state type and version rules | `durable_state_actor.go:575-590` |
| Saga tenant rules SG4, SG5, SG-DUR1 and the not-running guard | `saga_actor.go:86-94`, `:326`, `:346`, `:442-460`, `:482-484`, `:497`, `:686-706` |
| Passivation activity and coalescing | goakt v4.5.4 `actor/pid.go:75`, `:2192-2196`, `:2440-2453`; `actor/passivation_manager.go:413-422` |
| Passivation honored on one node; placement and relocation ignored | `engine.go:1910-1911`; goakt v4.5.4 `actor/spawn.go:295-297`, `:321`, `:1015-1054` |
| Saga spawns ignore other options; `EntityExists` covers sagas | `engine.go:1497-1501`, `:1563-1567`, `:988-1000` |
| No-event contract text and the test pinning the actual behavior | `port/runtime/runtime.go:95-96`; `event_sourced_actor_test.go:569-590` |
| Crypto-shredding promised, never called; keys per persistence ID only | `port/runtime/runtime.go:115-119`; `encryption/key_store.go:45-47`; `engine.go:1659-1716`; `encryption/aes_encryptor.go:48`; `testkit/keystore.go:39` |
| `runtimeconsumer` closure allowlist | `internal/runtimeconsumer/closure_test.go:59-68` |
| Contracts the runtime needs are GoAkt-free | `go list -f '{{.Imports}}'` on `testkit`, `projection`, `eventadapter`, `encryption`; `rg -l tochemey/goakt` finds no hit in `command`, `egopb`, `encryption`, `eventadapter`, `eventstream`, `persistence`, `offsetstore`, `projection`, `tenancy`, `port/*` |
| Projection runner's GoAkt use | `projection_runner.go:168`, `:413` |
| `CompositionLayer` excludes runtime-specific roots | `internal/cmd/archcheck/rules/layers.go`, `CompositionLayer` doc and `Match` |
| ciselect follows test imports | `docs/ci.md`, "Reverse-dependency expansion" |

No spike was run for this design. The behaviors that reading could not settle are measured on GoAkt by spec 0 before spec 1 starts (§D12): panics under each supervisor directive, commands queued behind a failed write, and passivation activity. The chain's specs each start with a RED test (strict TDD).

## 10. Alternatives rejected (summary)

- Running GoAkt with `testkit` stores as "the in-memory composition": still the GoAkt runtime (ego-arch-003 §5.2 rules it out).
- A public runtime package (D1, Q1); a nested module (D1).
- Widening `CompositionLayer` instead of a new rule (D2).
- A single-threaded run-to-completion scheduler; one permanent goroutine per entity (D7).
- Failing at `Start` instead of `New` for projections (D8, M1).
- Five step names with no-op steps (D8).
- Copying GoAkt's naming rule G2 (D8).
- The neutrality test in `compose/inmem`, `compose` or `test/compat` (D9).
- Extracting the shared rules from package `ego` inside this chain (Q4).
