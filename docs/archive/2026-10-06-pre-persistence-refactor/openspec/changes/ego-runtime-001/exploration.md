# Exploration — Runtime SPI, application side (EGO-RUNTIME-001/002, slice S4)

| Field | Value |
|---|---|
| Change | `ego-runtime-001` |
| Date | 2026-09-27 |
| Phase | `sdd-explore` |
| Tracker | [`#147`](https://github.com/getsyntegrity/ego/issues/147) (slice S4-D), parent [`#11`](https://github.com/getsyntegrity/ego/issues/11), foundation [`#10`](https://github.com/getsyntegrity/ego/issues/10); related [`#148`](https://github.com/getsyntegrity/ego/issues/148), [`#105`](https://github.com/getsyntegrity/ego/issues/105), [`#106`](https://github.com/getsyntegrity/ego/issues/106) / PR [`#149`](https://github.com/getsyntegrity/ego/pull/149), [`#124`](https://github.com/getsyntegrity/ego/issues/124), [`#12`](https://github.com/getsyntegrity/ego/issues/12), [`#24`](https://github.com/getsyntegrity/ego/issues/24), [`#29`](https://github.com/getsyntegrity/ego/issues/29) |
| Baseline | `main` at `f2b5130148086d95a4d6362971b85e800b0ce155` |
| Next | [`proposal.md`](./proposal.md), [`design.md`](./design.md) |

## 1. Question

Consumer code drives Ego through `*ego.Engine`: it spawns entities and sagas, sends commands, starts projections and subscribes to the event stream. `*ego.Engine` is the GoAkt runtime adapter, so that code is GoAkt code whichever runtime executes it. ego-arch-003 §5.2 (blocker 3) records the consequence: code written as `app.Engine().SpawnEventSourced(...)` against `compose/goakt` cannot compile against a future `compose/inmem`, because `compose/inmem` cannot return an `*ego.Engine`.

#147 asks for the application-facing half of #11's runtime **SPI** (service provider interface: the contract a runtime implements so Ego can use it). This exploration measures, on the baseline:

1. every exported `Engine` method, grouped by what it does, and whether the types in its signature are runtime-neutral or bound to GoAkt;
2. the types those signatures need that are declared in package `ego`, and why a contract package cannot import them today;
3. every exported symbol of package `ego` whose signature names a GoAkt type, as the input for #124.

"Neutral" below means the type is declared in the standard library, `google.golang.org/protobuf`, or a contract package that archcheck's `contract-allowlist` rule admits (`internal/cmd/archcheck/rules/rules.go:140-153`). "GoAkt-bound" means the type is declared in `github.com/tochemey/goakt/v4`, or embeds a GoAkt type. "Declared in `ego`" means the type itself is neutral in shape but lives in package `ego`, which a contract cannot import (ego-arch-001 §3).

## 2. Exported `Engine` methods by capability

All 23 exported methods, from `rg -n '^func \(\w+ \*Engine\) [A-Z]'` over the root package's non-test files.

### 2.1 Entities (spawn, invoke, erase)

| Method | Location | Signature types | Verdict |
|---|---|---|---|
| `SpawnEventSourced(ctx, b behaviorport.EventSourced, opts ...SpawnOption) error` | `engine_spawn.go:48` | `port/behavior.EventSourced`; `ego.SpawnOption` | Neutral except `SpawnOption`, declared in `ego` |
| `SpawnDurableState(ctx, b behaviorport.DurableState, opts ...SpawnOption) error` | `engine_spawn.go:63` | same | same |
| `EntityExists(ctx, entityID string) (bool, error)` | `engine.go:989` | stdlib | Neutral |
| `SendCommand(ctx, entityID string, cmd Command, timeout time.Duration) (State, uint64, error)` | `engine.go:1314` | `ego.Command`/`ego.State`, both aliases of `proto.Message` (`behavior.go:37`, `:45`) | Neutral (the aliases resolve to protobuf) |
| `Dispatch(ctx, entityID string, env command.Envelope, timeout time.Duration) (command.Result, error)` | `engine.go:1124` | `command` contract | Neutral |
| `EraseEntity(ctx, persistenceID string, full bool) error` | `engine.go:1648` | stdlib | Neutral |
| `Entity(ctx, behavior EventSourcedBehavior, opts ...SpawnOption) error` | `engine.go:750` | `EventSourcedBehavior` embeds GoAkt's `extension.Dependency` (`behavior.go:58`) | GoAkt-bound; deprecated since S3-5 |
| `DurableStateEntity(ctx, behavior DurableStateBehavior, opts ...SpawnOption) error` | `engine.go:1031` | `DurableStateBehavior` (`behavior.go:107`) | GoAkt-bound; deprecated |

### 2.2 Sagas

| Method | Location | Signature types | Verdict |
|---|---|---|---|
| `SpawnSaga(ctx, b behaviorport.Saga, timeout time.Duration, opts ...SpawnOption) error` | `engine_spawn.go:79` | `port/behavior.Saga`; `ego.SpawnOption` | Neutral except `SpawnOption` |
| `SagaStatus(ctx, sagaID string, timeout time.Duration) (*SagaInfo, error)` | `engine.go:1591` | `ego.SagaInfo` (`saga.go:104`), whose `Status` field is `ego.SagaStatus` (`saga.go:74`) and `State` field `proto.Message` | Neutral shape, declared in `ego` |
| `Saga(ctx, behavior SagaBehavior, timeout time.Duration, opts ...SpawnOption) error` | `engine.go:1512` | `SagaBehavior` embeds `extension.Dependency` (`saga.go:47-50`) | GoAkt-bound; deprecated |

### 2.3 Projections

| Method | Location | Signature types | Verdict |
|---|---|---|---|
| `StartProjection(ctx, name string) error` | `engine.go:546` | stdlib | Neutral |
| `StopProjection(ctx, name string) error` | `engine.go:606` | stdlib | Neutral |
| `IsProjectionRunning(ctx, name string) (bool, error)` | `engine.go:631` | stdlib | Neutral |
| `RebuildProjection(ctx, name string, from time.Time) error` | `engine.go:666` | stdlib | Neutral |
| `ProjectionLag(ctx, projectionName string) (map[uint64]time.Duration, error)` | `engine.go:1732` | stdlib | Neutral |

### 2.4 Events

| Method | Location | Signature types | Verdict |
|---|---|---|---|
| `Subscribe() (eventstream.Subscriber, error)` | `engine.go:705` | `eventstream` contract | Neutral |

### 2.5 Not part of the consumer's invocation surface

| Method | Location | What it is | Verdict |
|---|---|---|---|
| `Start(ctx) error` | `engine.go:424` | lifecycle, owned by the composition root (ego-arch-003 §D6) | Neutral types; kept out of the interface by #147 |
| `Stop(ctx) error` | `engine.go:464` | lifecycle (§D7) | same |
| `Started() bool` | `engine.go:504` | lifecycle probe | same (not named by #147; see design §D1) |
| `AddEventPublishers(...EventPublisher) error` | `engine.go:1392` | wiring; `compose/goakt` step 4 calls it | Neutral types (`port/publishing` aliases, `publisher.go:32-35`); kept out by #147 |
| `AddStatePublishers(...StatePublisher) error` | `engine.go:1447` | wiring | same |
| `ActorSystem() goakt.ActorSystem` | `engine.go:513` | GoAkt escape hatch | GoAkt-bound; kept out by #147 |

**Result.** Every method #147 puts in the interface is neutral except for five types declared in package `ego`: `SpawnOption` and `SagaInfo` in the signatures, `SagaStatus` inside `SagaInfo`, and `EntitiesPlacement` (`spawn_config.go:36`) and `SupervisorDirective` (`supervisor.go:33`), which the options that build a `SpawnOption` take. No method in the interface needs a GoAkt type.

## 3. Types declared in `ego` that the interface needs

### 3.1 `SpawnOption` is sealed to package `ego`

```go
// spawn_config.go:111-114
type SpawnOption interface {
	// Apply sets the Option value of a config.
	Apply(config *spawnConfig)
}
```

`spawnConfig` (`spawn_config.go:65-96`) is unexported, so no other package can write a method with that parameter type. Only package `ego` can create options, and only package `ego` can read them: `newSpawnConfig` (`spawn_config.go:99-108`) applies them over the defaults `RestartDirective` and `RoundRobin`. A second runtime could accept an `ego.SpawnOption` value but could never learn what it says.

`spawnConfig` mixes two kinds of setting:

| Setting | Option | Location | Kind |
|---|---|---|---|
| passivation timeout | `WithPassivateAfter` | `spawn_config.go:132` | runtime (lifecycle) |
| relocation on node loss | `WithRelocation` | `:155` | runtime (placement) |
| supervisor directive | `WithSupervisorDirective` | `:165` | runtime (supervision) |
| placement strategy | `WithPlacement` | `:204` | runtime (placement) |
| tenant binding | `WithTenant` | `:265` | runtime (tenancy; takes `tenancy.TenantID`, a contract type) |
| snapshot interval | `WithSnapshotInterval` | `:178` | write side |
| retention policy | `WithRetentionPolicy` (takes `ego.RetentionPolicy`, `retention.go:28`) | `:187` | write side |
| batch threshold | `WithBatchThreshold` | `:223` | write side |
| batch flush window | `WithBatchFlushWindow` | `:237` | write side |

The write-side settings belong to the command-handling pipeline #12 (EGO-WRITE) owns. Maintainer decision 3 leaves to #12 whether they belong in a runtime contract.

### 3.2 The other types and constants

| Symbol | Location | Shape |
|---|---|---|
| `EntitiesPlacement` and `RoundRobin`, `Random`, `Local`, `LeastLoad` | `spawn_config.go:36-62` | `int` enum |
| `SupervisorDirective` and `StopDirective`, `RestartDirective` | `supervisor.go:33-45` | `int` enum |
| `SagaStatus` and `SagaRunning`, `SagaCompleted`, `SagaCompensating`, `SagaFailed`, method `String` | `saga.go:74-101` | `int` enum |
| `SagaInfo{ID string; Status SagaStatus; State proto.Message}` | `saga.go:104-111` | struct |

### 3.3 Sentinel errors

All exported sentinels of the root package live in one `var` block (`engine.go:61-174`), plus the S1 alias `ErrPublisherNotStarted` (`publisher.go:39`). Classified by who returns them and whether their meaning depends on GoAkt:

| Sentinel | Location | Returned by | Classification |
|---|---|---|---|
| `ErrEngineNotStarted` | `engine.go:63` | every interface method | Neutral: moves |
| `ErrUndefinedEntityID` | `:65` | `Dispatch`, `SagaStatus` | Neutral: moves |
| `ErrDurableStateStoreRequired` | `:69` | `SpawnDurableState` (`engine.go:1058`) | Neutral: moves |
| `ErrEventsStoreRequired` | `:74` | `SpawnEventSourced`, `SpawnSaga` | Neutral: moves |
| `ErrProjectionNotRegistered` | `:83` | `StartProjection` (`engine.go:564`) | Neutral: moves |
| `ErrSpawnTenantUndetermined` | `:118` | the three spawns | Neutral (tenancy semantics): moves |
| `ErrSpawnTenantMismatch` | `:127` | the three spawns | Neutral: moves |
| `ErrSpawnTenantUnverified` | `:135` | the three spawns | Neutral (its doc mentions a remote PID, but the meaning is "binding could not be verified"): moves |
| `ErrNotACommand` | `:140` | `Dispatch`, `SendCommand` | Neutral: moves |
| `ErrEntityFamilyNotDeclared` | `:171` | the three spawns | Neutral: moves |
| `ErrCommandReplyUnmarshalling` | `:67` | `Dispatch` (`engine.go:1280`), when the actor's reply is not an `egopb.CommandReply` | Adapter-internal: stays |
| `ErrDuplicatePublisherID` | `:80` | `Add*Publishers` (outside the interface) | Wiring: stays |
| `ErrActorSystemRequired`, `ErrActorSystemNotStarted`, `ErrMissingRequiredExtensions` | `:87`, `:97`, `:104` | `NewEngine` | GoAkt: stay |
| `ErrAmbiguousTenantResolver` | `:94` | `NewEngine` | Construction: stays |
| `ErrEntityTenantScopeMissing` | `:148` | an actor's `PreStart` | GoAkt actor internals: stays |
| `ErrBehaviorNotSerializable`, `ErrBehaviorNotPointer`, `BehaviorPlacementError` | `:155`, `:164`, `:185` | cluster placement (ego-arch-002-s3 §5.6) | GoAkt: stay |

`ZeroTime` (`engine.go:173`) is a variable, not an error; `RebuildProjection` accepts `time.Time{}` directly, so it stays.

## 4. GoAkt-typed symbols in package `ego`

Every exported symbol whose signature names a GoAkt type, found with `rg -n` over exported declarations in the root package's non-test files and read one by one. `goakt` is `github.com/tochemey/goakt/v4/actor`; `extension` is `github.com/tochemey/goakt/v4/extension`.

| Symbol | Location | GoAkt type in the signature |
|---|---|---|
| `NewEngine(actorSys goakt.ActorSystem, config *Config)` | `engine.go:299` | `goakt.ActorSystem` |
| `(*Engine).ActorSystem() goakt.ActorSystem` | `engine.go:513` | `goakt.ActorSystem` |
| `(*Config).GoaktOptions() []goakt.Option` | `option.go:129` | `goakt.Option` |
| `ClusterKinds() []goakt.Actor` | `option.go:199` | `goakt.Actor` |
| `EntityKind = extension.Dependency` | `option.go:347` | alias of a GoAkt interface (deprecated, S3-5) |
| `WithEntityKinds(kinds ...EntityKind) Option` | `option.go:375` | through `EntityKind` (deprecated) |
| `EventSourcedBehavior`, `EventSourcedEnvelopeBehavior`, `DurableStateBehavior`, `DurableStateEnvelopeBehavior` | `behavior.go:58`, `:76`, `:107`, `:122` | embed `extension.Dependency` (deprecated) |
| `SagaBehavior` | `saga.go:47` | embeds `extension.Dependency` (deprecated) |
| `(*Engine).Entity`, `DurableStateEntity`, `Saga` | `engine.go:750`, `:1031`, `:1512` | through the deprecated behavior interfaces |
| `EventSourcedActor` and its `PreStart`, `Receive`, `PostStop` | `event_sourced_actor.go:125`, `:286`, `:332`, `:372` | `*goakt.Context`, `*goakt.ReceiveContext` |
| `DurableStateActor` and its three methods | `durable_state_actor.go:56`, `:131`, `:202`, `:240` | same |
| `SagaActor` and its three methods | `saga_actor.go:51`, `:140`, `:210`, `:252` | same |
| `ProjectionActor`, `NewProjectionActor`, and its three methods | `projection_actor.go:61`, `:70`, `:75`, `:166`, `:183` | same |

Two neighbouring groups are not GoAkt-typed but belong to the adapter: `BehaviorKind`/`WithBehaviorKinds` (`option.go:393`, `:414`; standard library only, but they exist for GoAkt's cluster registry, ego-arch-002-s3 §5.5) and the unexported `loggerAdapter` (`logger.go`, implements GoAkt's `log.Logger`). `Telemetry` (`telemetry.go:32`) exposes OpenTelemetry types, not GoAkt; it belongs to #31. Outside package `ego`, `compose/goakt` exposes `WithCluster(cfg *actor.ClusterConfig, kinds ...ego.BehaviorKind)` and `WithActorSystemOptions(opts ...actor.Option)` (`compose/goakt/option.go:87`, `:99`) and `App.Engine() *ego.Engine` (`compose/goakt/app.go:234`). The destination of each group at #124 is in design §D10.

## 5. Findings the move transports unchanged

These are observed on the baseline. S4 moves the types as they are (#147: "the semantics of placement, supervision and passivation are transported as is"); each finding names who owns it.

1. **`SagaStatus` never fills `SagaInfo.Status`.** `Engine.SagaStatus` builds `&SagaInfo{ID: sagaID, State: state}` (`engine.go:1638-1641`), so `Status` is always the zero value `SagaRunning`, even for a completed saga. The saga actor tracks its own status (`saga_actor.go:58`) but the reply does not carry it. Owner: follow-up FU-1, [#153](https://github.com/getsyntegrity/ego/issues/153) (design §7); a runtime-neutral `SagaInfo` makes the gap visible to every runtime, and the in-memory runtime (#148) should not copy it.
2. **Relocation is off by default, although the documentation says it is on.** `WithRelocation`'s comment says "In cluster mode, entities are relocatable by default" (`spawn_config.go:141`), but `spawnConfig.toRelocate` defaults to `false` and `buildSpawnOptionsFromConfig` disables relocation unless `WithRelocation(true)` was passed (`engine.go:1903-1905`). Owner: follow-up FU-2, [#154](https://github.com/getsyntegrity/ego/issues/154) (design §7), with the contract text in RUNTIME-003.
3. **`EraseEntity` and `ProjectionLag` call the events store without checking it exists.** Recorded in #24's comment of 2026-09-27; owner #24. The interface keeps the signatures; the fix does not change them.
4. **Placement only matters in cluster mode** (`spawn_config.go:34`). A single-node runtime that ignores placement matches today's semantics. Owner of the contract text: RUNTIME-003.

## 6. What PR #149 (adapter SPI, #106) already proposes that this must align with

PR #149 is open and undecided. Relevant to S4:

- Capabilities are **declared** in a `port/adapter.Descriptor` and **implemented** through an optional interface in the contract package that owns the port, with one accessor per capability as the only type assertion (#149 design §D3).
- #149 does not design the runtime port; runtime capabilities for RUNTIME-006 reuse its `Descriptor` in follow-up F-E (#149 design §2, §5).
- `engine.go:883` (the `tenancy.FixedTenantResolver` assertion) moves behind `tenancy.FixedTenantOf` in #149's slice SPI-5, a one-line edit in `engine.go`.
- Lifecycle (`Start`, `Ping`, `Close`) stays outside port interfaces and is sequenced by the composition root.

S4 needs none of `port/adapter`, reserves no capability name, and adds no type assertion. The alignment is spelled out in design §D4 and §8.

## 7. Evidence

| Claim | How it was obtained |
|---|---|
| Every `file:line` above | Read on `f2b5130` in a clean worktree |
| Method list (§2) | `rg -n --max-depth 1 -g '!*_test.go' '^func \(\w+ \*Engine\) [A-Z]'` |
| Exported-symbol and GoAkt inventory (§4) | `rg -n --max-depth 1 -g '!*_test.go' '^(func\|type) (\([^)]*\) )?[A-Z]'`, then filtering for `goakt`, `extension.` and reading each hit |
| `SagaInfo.Status` is never set (§5.1) | `rg -n 'Status\s*[:=]\|\.Status\b'` over `engine.go` and `saga_actor.go` finds no assignment into a `SagaInfo` |
| The moved types fit a contract, and `*ego.Engine` satisfies the interface once they move | Spike, design §11 |
