# Design — Neutral behavior contracts (EGO-ARCH-002, slice S3)

| Field | Value |
|---|---|
| Change | `ego-arch-002-s3` |
| Date | 2026-09-26 |
| Phase | `sdd-design` |
| Tracker | [`#123`](https://github.com/getsyntegrity/ego/issues/123), parent [`#10`](https://github.com/getsyntegrity/ego/issues/10); origin: criteria 1–3 of [`#103`](https://github.com/getsyntegrity/ego/issues/103) |
| Inputs | [`proposal.md`](./proposal.md); [`ego-arch-001/design.md`](../ego-arch-001/design.md) §2 (canonical location), §3 (layer rules), §5 (slice S3), §10 (decisions); `ego-arch-003` design in PR [`#125`](https://github.com/getsyntegrity/ego/pull/125) (composition root) |
| Baseline commit SHA | `main` at `77beda6b91646b0e031ce78401ee997fd919bd65` |
| GoAkt version read | `github.com/tochemey/goakt/v4` `v4.5.4` (module cache) |

## 1. Summary and vocabulary

The three public behavior contracts in package `ego` embed a GoAkt interface. That interface exists so GoAkt can copy a behavior to another cluster node. This design adds runtime-neutral behavior contracts in a new package, `port/behavior`, and moves the serialization requirement into the GoAkt adapter. Behaviors that already implement `MarshalBinary`/`UnmarshalBinary` travel between nodes exactly as they do today, byte for byte. Behaviors that do not implement them run on a single node. In cluster mode, where GoAkt must serialize every spawn, they are rejected with a typed error instead of failing inside GoAkt. Nothing in v4 breaks. The old names stay, marked `Deprecated:`, and are removed at the major release that #124 introduces.

Terms used below:

- **Behavior**: the domain code a user writes, meaning the command and event handlers for one entity (`EventSourcedBehavior`, `DurableStateBehavior`) or one saga (`SagaBehavior`).
- **`extension.Dependency`**: the GoAkt interface (`extension/dependency.go:55`) requiring `ID() string` plus `encoding.BinaryMarshaler` and `encoding.BinaryUnmarshaler`. GoAkt serializes every value passed to `goakt.WithDependencies` when a spawn is placed on, or relocated to, another node.
- **Spawn dependency**: a value the engine hands to GoAkt with `goakt.WithDependencies` so that the spawned actor can read it in `PreStart` through `ctx.Dependencies()`. Today the behavior itself is one of them.
- **Cluster mode**: `goakt.ActorSystem.InCluster()` is true, meaning the actor system was built with `goakt.WithCluster` and has started. In GoAkt v4.5.4 this is exactly the condition under which spawn dependencies are serialized (§2.2).
- **Kind registration**: telling a node's GoAkt type registry about a behavior's Go type ahead of time, so the node can rebuild that behavior from bytes when a peer places a spawn on it. Today this is `WithEntityKinds` (`option.go:359`).
- **apidiff**: `golang.org/x/exp/cmd/apidiff`, the tool that reports incompatible changes between two versions of a Go package's exported API.

## 2. The problem in code

### 2.1 Where GoAkt appears in the public contracts

| Location (at `77beda6`) | What it declares |
|---|---|
| `behavior.go:47-48` | `EventSourcedBehavior` embeds `extension.Dependency` |
| `behavior.go:99-100` | `DurableStateBehavior` embeds `extension.Dependency` |
| `saga.go:41-42` | `SagaBehavior` embeds `extension.Dependency` |
| `option.go:338` | `type EntityKind = extension.Dependency` |
| `option.go:359` | `func WithEntityKinds(kinds ...EntityKind) Option` |

The envelope variants `EventSourcedEnvelopeBehavior` (`behavior.go:78`) and `DurableStateEnvelopeBehavior` (`behavior.go:120`) embed the two base interfaces, so they inherit the embed.

As a result, every domain author implements two GoAkt serialization methods even when no cluster exists. `example/eventssourced/main.go:189-209` implements `MarshalBinary`/`UnmarshalBinary` only to satisfy the interface. Meanwhile `testkit/scenario.go:40-54` declares structural copies of the contracts without `extension.Dependency` and runs `HandleCommand`/`HandleEvent` with no actor system at all. That shows the domain logic never needs the serialization methods.

### 2.2 Why GoAkt needs serialization, and exactly when

Each behavior is passed to GoAkt as a spawn dependency at three sites:

- `Engine.Entity`: `engine.go:662` (`_ = actorSystem.Inject(behavior)`), `engine.go:692` (`deps := []extension.Dependency{behavior, entityConfig}`) and `engine.go:699` (`SpawnOn`).
- `Engine.DurableStateEntity`: `engine.go:911`, `engine.go:925` and `engine.go:932` (`SpawnOn`).
- `Engine.Saga`: `engine.go:1310`, `engine.go:1326` and `engine.go:1332` (`Spawn`).

All numbers were verified on `origin/main` at `77beda6`. The GoAkt spawn calls are at `engine.go:699`, `932` and `1332`. #123's text cites `692`, `925` and `1326`, which are the lines that build the dependency slice, not the lines that spawn. (`Config.GoaktOptions` starts at `option.go:122`; S3 does not change it.)

`NewEngine` also registers the configured kinds with `actorSys.Inject(dependencies...)` (`engine.go:253-259`).

In GoAkt v4.5.4 the dependencies are serialized in these places:

1. **Remote placement.** `SpawnOn` (`actor/spawn.go:272`) spawns locally when `!x.InCluster()` (`spawn.go:296`). Otherwise it may choose a peer and send a `remote.SpawnRequest` carrying the dependencies. The client encodes them with `codec.EncodeDependencies` (`internal/remoteclient/client.go:2271`), which calls `MarshalBinary` and records `types.Name(dependency)` as the type name (`internal/codec/codec.go:49-64`).
2. **Local spawns in cluster mode.** Every spawn in cluster mode writes the actor's registry record: `putActorOnCluster` (`actor/actor_system.go:2843-2854`) calls `pid.toSerialize()`, which encodes all dependencies (`actor/pid.go:3480-3484`). So in cluster mode even a spawn that stays on the calling node serializes the behavior.
3. **Relocation.** When a node leaves, its relocatable actors are serialized with `toSerialize` (`actor_system.go:5316`) and rebuilt on a surviving node.

On the receiving side, `decodeDependencyFromBytes` (`codec.go:98-117`) looks up the type name in the node's registry, creates a new value with `reflect.New`, and calls `UnmarshalBinary`. The registry is filled by `Inject` (`actor_system.go:2254-2267` → `internal/types/registry.go:86-90`).

Outside cluster mode none of this runs: `putActorOnCluster` returns early when the cluster is disabled (`actor_system.go:2844`), and `SpawnOn` falls back to a local `Spawn`. So **serialization is a cluster-mode requirement only**, and the engine can detect cluster mode with the public `ActorSystem.InCluster()` (`actor_system.go:1844-1848`).

### 2.3 A latent panic with value-type behaviors

GoAkt names a registered type with `reflect.TypeOf(v).Elem()` (`internal/types/registry.go:103-117`). `Elem` panics for a non-pointer struct type. A behavior with value receivers (for example `type Account struct{...}` with `func (a Account) ID() string`) therefore makes `actorSystem.Inject(behavior)` at `engine.go:662` panic, even on a single node. Today this is rare, because the serialization methods are usually written with pointer receivers. Once behaviors need only domain methods, value types become natural, so the adapter must not hand a non-pointer behavior to GoAkt's registry. §5.3 handles this.

This was observed in a throwaway spike against GoAkt v4.5.4 (Go 1.26.6, linux/amd64, not committed). `sys.Inject(valueDep{...})` on a started single-node actor system panicked with `reflect: Elem of invalid type spike.valueDep`. A first version of the spike recovered the panic and then called `sys.Stop`, and it hung until the 600 s test timeout. `Inject` takes the actor-system lock and releases it without `defer` (`actor_system.go:2255-2265`), so a panic inside it leaves the lock held. Even a caller that recovers the panic is left with an actor system it cannot stop. Slice S3-2's first RED test (§8) repeats this observation through `Engine.Entity`, without recovering the panic, before the fix.

## 3. Constraints

1. **Protobuf stays public (decided 2026-09-26).** `egopb` and `proto.Message` remain part of the public contract for v4 (`ego-arch-001/design.md` §10). The neutral contracts keep `Command`, `Event` and `State` as `proto.Message`.
2. **No break inside v4 (decided 2026-09-26).** Deprecate first; every deprecated API is removed at the major release introduced by #124. This is measured with apidiff and with a consumer program compiled against both versions (the method S1 used, `odd/tasks/port-publishing.md` T2).
3. **Contracts are born under `port/`** (`ego-arch-001/design.md` §2) and must pass archcheck's `contract-allowlist` (standard library, other contracts, `egopb`, the protobuf runtime).
4. **Layer rule for the adapter.** Package `ego` and `internal/extensions` are the GoAkt adapter and may import GoAkt (`ego-arch-001/design.md` §3).

Constraint 2 rules out the literal reading of #123's first acceptance criterion ("`EventSourcedBehavior` … no incrustan …") inside v4. A spike with apidiff on a reduced copy of the API confirmed both ways that reading could be implemented:

| Change | apidiff verdict |
|---|---|
| Remove the embed from the existing interface (`Behavior` loses `MarshalBinary`/`UnmarshalBinary`) | Incompatible: "`Behavior`: no longer implements `Dep`" |
| Widen `Engine.Entity`'s parameter from the old interface to a smaller one | Incompatible: "`(*Engine).Entity`: changed from `func(Behavior) error` to `func(Neutral) error`" |
| Add a new neutral interface and a new method; re-express the old interface as `interface{ Neutral; Dep }` | Compatible: two additions, the old interface is unchanged |

Both breaks are real for callers, not tool noise. For example, `func f(b ego.EventSourcedBehavior) { b.MarshalBinary() }` stops compiling, and so does `actorSys.Inject(b)` where `b` has static type `ego.EventSourcedBehavior`. So the neutral contracts get **new names in a new package**, and the old names become deprecated specializations of them. #123's first criterion is met by the new contracts in v4 and by the old names at #124 (reading confirmed 2026-09-27, §11).

## 4. The cluster-mode problem: options

If behaviors stop being `extension.Dependency`, the engine still needs something GoAkt can serialize for cluster placement. Five options were evaluated.

| Option | How it works | Verdict |
|---|---|---|
| **A. Kind registry with factories** | The adapter keeps a map from a stable kind name to `func(id string) Behavior`. The spawn dependency carries only kind + ID; the receiving node rebuilds the behavior with the factory. | Rejected (see below) |
| **B. Engine-built wrapper that serializes kind + ID + payload** | An internal `extension.Dependency` wraps every behavior; `MarshalBinary` writes the kind, the ID and the behavior's own bytes; the receiving node resolves the kind from a registry, then unmarshals the payload into it. | Rejected (see below) |
| **C. Serialization as an optional capability, detected at the adapter boundary** | The engine checks at spawn time whether the behavior value also implements `extension.Dependency` (that is, it has `MarshalBinary`/`UnmarshalBinary`). If so, it passes the behavior to GoAkt unchanged, exactly as today. If not, it wraps it in a local-only internal dependency when not in cluster mode, and returns a typed error in cluster mode. | **Chosen** |
| **D. Remove the embed in place** | Shrink the three existing interfaces. | Rejected: apidiff-incompatible (§3), violates decision 2 |
| **E. Process-global kind registry** | A package-level map filled at init time. | Rejected: hidden global state; the existing two-node test runs two engines in one process (`engine_test.go:984`), and a global map cannot hold per-node registrations |

**Why C.** It is the only option that keeps the GoAkt wire format unchanged. A serializable behavior reaches GoAkt as the same Go value with the same type name and the same `MarshalBinary` bytes as at `77beda6`, so a cluster running old and new nodes side by side during a rolling upgrade keeps decoding each other's spawns and relocations. It needs no new public registration concept. `WithEntityKinds` keeps its meaning, and its neutral replacement (§5.5) has the same method set. It matches #123's scope sentence ("verified when registering kinds or when spawning in cluster mode") and its acceptance criterion for a typed error, and it keeps all GoAkt knowledge inside package `ego`, which is already the adapter.

**Why not A.** A changes the wire format. Old nodes cannot decode a kind-name dependency, so rolling upgrades would break, and that is a behavioral break inside v4. It also drops any behavior state that `MarshalBinary` carries today beyond the ID. It needs a new public factory API. And GoAkt rebuilds a dependency with `reflect.New` and no context (`codec.go:109`), so the factory lookup would have to move into the actor's `PreStart`, reached through a GoAkt extension keyed by name. That is a lookup-by-name at the point of use, the service-locator shape that PR #125's D2 forbids in the composition root. Placement semantics are also #11's `RUNTIME-003`, not S3's.

**Why not B.** B has the same wire-format break and the same deferred-resolution problem as A. It gains nothing over C for behaviors that already serialize themselves, and behaviors that do not serialize themselves still cannot carry state beyond their ID.

**What C gives up.** A behavior without `MarshalBinary`/`UnmarshalBinary` cannot run in cluster mode. This is the outcome #123 asks for ("fails with a typed, descriptive error … not a panic"). Letting such behaviors run in a cluster through factories belongs with #11's runtime SPI, if anyone ever needs it (§12).

## 5. Design

### 5.1 New contract package `port/behavior`

A new package, `github.com/pablogore/ego/v4/port/behavior`, holds the neutral contracts. It imports only `context`, `time`, `google.golang.org/protobuf/proto` and `github.com/pablogore/ego/v4/command`, all admitted by `contract-allowlist`.

```go
package behavior

type (
	Command = proto.Message
	Event   = proto.Message
	State   = proto.Message
)

// EventSourced is the domain contract of an event-sourced entity.
type EventSourced interface {
	ID() string
	InitialState() State
	HandleCommand(ctx context.Context, command Command, priorState State) (events []Event, err error)
	HandleEvent(ctx context.Context, event Event, priorState State) (state State, err error)
}

// EventSourcedEnvelope is the optional extension that receives command.Envelope.
type EventSourcedEnvelope interface {
	EventSourced
	HandleEnvelope(ctx context.Context, env command.Envelope, priorState State) (events []Event, err error)
}

type DurableState interface {
	ID() string
	InitialState() State
	HandleCommand(ctx context.Context, command Command, priorVersion uint64, priorState State) (newState State, newVersion uint64, err error)
}

type DurableStateEnvelope interface {
	DurableState
	HandleEnvelope(ctx context.Context, env command.Envelope, priorVersion uint64, priorState State) (newState State, newVersion uint64, err error)
}

type Saga interface {
	ID() string
	InitialState() State
	HandleEvent(ctx context.Context, event Event, state State) (*SagaAction, error)
	HandleResult(ctx context.Context, entityID string, result State, sagaState State) (*SagaAction, error)
	HandleError(ctx context.Context, entityID string, err error, sagaState State) (*SagaAction, error)
	ApplyEvent(ctx context.Context, event Event, state State) (State, error)
	Compensate(ctx context.Context, state State) ([]SagaCommand, error)
}

type SagaAction struct { /* fields moved unchanged from saga.go:67-76 */ }
type SagaCommand struct { /* fields moved unchanged from saga.go:88-103 */ }
```

The doc comments move with the declarations. `SagaAction` and `SagaCommand` must move too, because `Saga`'s methods return them and a contract cannot import package `ego`. Package `ego` keeps them as aliases (§5.2). `SagaStatus` and `SagaInfo` stay in `ego`, since they are results of the engine's `SagaStatus` query, not part of the behavior contract.

Package name: `behavior`. Engine code in package `ego` uses `behavior` as a parameter name in many places (for example `engine.go:645`), so package `ego` imports it as `behaviorport "github.com/pablogore/ego/v4/port/behavior"` to avoid shadowing. User code can import it without an alias.

### 5.2 Compatibility shape of package `ego`

The old names keep identical method sets, so apidiff sees no change to them:

```go
// Deprecated: implement behavior.EventSourced from port/behavior and spawn with
// Engine.SpawnEventSourced. Removed in the next major release (#124).
type EventSourcedBehavior interface {
	behaviorport.EventSourced
	extension.Dependency
}
```

The snippet shows the final shape. S3-1 adds only the re-expressed declaration; the `Deprecated:` comment is added later, in S3-5 (§9). The same shape applies to `DurableStateBehavior` and `SagaBehavior`. `EventSourcedEnvelopeBehavior` and `DurableStateEnvelopeBehavior` keep their declarations (the old base interface plus `HandleEnvelope`). `ID()` appears in both embedded interfaces with an identical signature, which Go has allowed since 1.14.

`SagaAction` and `SagaCommand` become aliases: `type SagaAction = behaviorport.SagaAction`. The unexported method `(*SagaAction).isNoop` (`saga.go:83`) cannot stay a method on a type declared in another package, so it becomes the unexported function `sagaActionIsNoop(a *SagaAction) bool`, used at `saga_actor.go:524`.

### 5.3 The adapter bridge (option C)

One unexported function in package `ego` decides what the engine hands to GoAkt for a behavior:

```go
// spawnDependency returns the GoAkt spawn dependency that carries b.
func spawnDependency(sys goakt.ActorSystem, b interface{ ID() string }) (extension.Dependency, error)
```

| Behavior value `b` | Not in cluster mode | Cluster mode (`sys.InCluster()`) |
|---|---|---|
| Non-nil pointer that implements `extension.Dependency` | Return `b` itself and register its type with `sys.Inject(b)`, as today | Same. The wire bytes are unchanged |
| Implements `extension.Dependency` but is not a pointer | Return `extensions.NewLocalBehavior(b)` (never serialized) | `*BehaviorPlacementError{Err: ErrBehaviorNotPointer}` |
| Does not implement `extension.Dependency` | Return `extensions.NewLocalBehavior(b)` | `*BehaviorPlacementError{Err: ErrBehaviorNotSerializable}` |

The error is returned **before** `SpawnOn`/`Spawn` is called, so nothing is spawned and nothing is written to the cluster registry. Checking before the spawn matters: if a non-serializable wrapper reached GoAkt in cluster mode, the failure would come from `putActorOnCluster` after the actor had already started locally.

`extensions.LocalBehavior` is a new internal type in `internal/extensions/extensions.go`, next to `EntityConfig`:

```go
// LocalBehavior carries a behavior that cannot be serialized to a spawn on
// the local node. It is never registered with Inject and never serialized.
type LocalBehavior struct{ behavior interface{ ID() string } }

func (l *LocalBehavior) ID() string                    { return l.behavior.ID() }
func (l *LocalBehavior) Behavior() any                 { return l.behavior }
func (l *LocalBehavior) MarshalBinary() ([]byte, error) { return nil, errLocalOnly }
func (l *LocalBehavior) UnmarshalBinary([]byte) error  { return errLocalOnly }
```

Its `ID()` returns the behavior's ID, so the dependency map key that GoAkt uses is the same as today. `MarshalBinary` fails by design. The table above keeps it out of cluster mode, and §2.2 shows that spawning and relocation never serialize outside cluster mode. There is one exception outside cluster mode. With remoting enabled and no cluster, a peer can query an actor's dependencies remotely, and GoAkt then encodes them (`actor/remote_server.go:1167`). For an actor carrying a `LocalBehavior`, that query gets an error back: `EncodeDependencies` returns the `errLocalOnly` error, and the remote server logs it and answers `CODE_INTERNAL_ERROR` (`remote_server.go:1167-1171`). Nothing panics, and the local actor is unaffected.

**Actor side.** Each actor reads its behavior from `ctx.Dependencies()`. One unexported generic helper replaces the three direct type assertions:

```go
func behaviorFrom[T any](dep extension.Dependency) (T, bool) {
	if lb, ok := dep.(*extensions.LocalBehavior); ok {
		b, ok := lb.Behavior().(T)
		return b, ok
	}
	b, ok := dep.(T)
	return b, ok
}
```

The actors' `behavior` fields change to the neutral types. These fields are unexported, so the change is not part of the API. A dependency that was passed through unchanged still matches, because a value with extra methods satisfies a smaller interface. A throwaway spike (not committed) confirmed that a type implementing the old contract, held as the neutral interface, still type-asserts to `extension.Dependency` and back.

### 5.4 Engine entry points

Three new methods accept the neutral contracts. The three existing methods keep their signatures and delegate.

| Old (kept, deprecated in S3-5) | New (added) |
|---|---|
| `func (engine *Engine) Entity(ctx context.Context, behavior EventSourcedBehavior, opts ...SpawnOption) error` | `func (engine *Engine) SpawnEventSourced(ctx context.Context, b behavior.EventSourced, opts ...SpawnOption) error` |
| `func (engine *Engine) DurableStateEntity(ctx context.Context, behavior DurableStateBehavior, opts ...SpawnOption) error` | `func (engine *Engine) SpawnDurableState(ctx context.Context, b behavior.DurableState, opts ...SpawnOption) error` |
| `func (engine *Engine) Saga(ctx context.Context, behavior SagaBehavior, timeout time.Duration, opts ...SpawnOption) error` | `func (engine *Engine) SpawnSaga(ctx context.Context, b behavior.Saga, timeout time.Duration, opts ...SpawnOption) error` |

Both the old and the new methods delegate to three unexported functions, `spawnEventSourced`, `spawnDurableState` and `spawnSaga`. S3-2 creates these by renaming the existing method bodies in place, so the body text does not move. Every `EventSourcedBehavior` value is a `behavior.EventSourced`, so `Entity(ctx, b)` can call `spawnEventSourced(ctx, b, opts...)` directly.

**Where #105's family guard goes.** #105 IMPL-4 adds a declared-entity-family guard to these spawn paths (PR #125 D3). This design recommends placing it in the three unexported functions, so the old and new entry points both enforce it with one copy.

### 5.5 Kind registration

| Old (kept, deprecated in S3-5) | New (added) |
|---|---|
| `type EntityKind = extension.Dependency` | `type BehaviorKind interface { ID() string; encoding.BinaryMarshaler; encoding.BinaryUnmarshaler }` |
| `func WithEntityKinds(kinds ...EntityKind) Option` | `func WithBehaviorKinds(kinds ...BehaviorKind) Option` |

`BehaviorKind` has the same method set as `extension.Dependency` but is spelled with the standard library only. Any `EntityKind` value is assignable to `BehaviorKind`, and the reverse also holds. Migrating is a rename at the call site. `WithEntityKinds` appends its arguments to the same unexported `Config` field as `WithBehaviorKinds`, so the two options can be mixed.

`BehaviorKind` lives in package `ego`, not in `port/behavior`. Cluster placement is a runtime capability of the GoAkt adapter; a contract package should not describe how a runtime ships values between nodes. After #124 it moves with the adapter (for example into PR #125's `compose/goakt`).

`NewEngine` (`engine.go:247-259`) registers each kind with `actorSys.Inject`. A kind whose value is not a non-nil pointer makes `NewEngine` return a `*BehaviorPlacementError` with `Err: ErrBehaviorNotPointer`, instead of the GoAkt panic described in §2.3.

### 5.6 Typed error

Declared in package `ego`, in the existing error `var` block (`engine.go:58-137`) and a new type next to it:

```go
var (
	// ErrBehaviorNotSerializable: in cluster mode a behavior must implement
	// encoding.BinaryMarshaler and encoding.BinaryUnmarshaler so GoAkt can
	// place or relocate it on another node.
	ErrBehaviorNotSerializable = errors.New("eGo: behavior must implement encoding.BinaryMarshaler and encoding.BinaryUnmarshaler to be spawned in cluster mode")
	// ErrBehaviorNotPointer: GoAkt's type registry names types through a pointer.
	// (message amended 2026-09-27, #143)
	ErrBehaviorNotPointer = errors.New("eGo: a behavior must be non-nil to be spawned, and a pointer to be spawned in cluster mode; a behavior kind registered with WithBehaviorKinds or WithEntityKinds must be a pointer type (a typed nil is allowed)")
)

// BehaviorPlacementError reports why a behavior kind cannot be registered
// for, or placed by, the GoAkt runtime.
type BehaviorPlacementError struct {
	Kind     string // Go type of the behavior, e.g. "*main.AccountBehavior"
	EntityID string // the spawn's entity or saga ID; empty when raised by NewEngine
	Err      error  // ErrBehaviorNotSerializable or ErrBehaviorNotPointer
}

func (e *BehaviorPlacementError) Error() string // names Kind, EntityID and the cause
func (e *BehaviorPlacementError) Unwrap() error { return e.Err }
```

Callers test with `errors.Is(err, ego.ErrBehaviorNotSerializable)` or read the details with `errors.As`. No path panics.

`ErrBehaviorNotPointer` has two sources. `NewEngine` returns it for a value-type kind in **any** mode, single-node included, because kind registration always goes through GoAkt's registry. A spawn returns it only in cluster mode, because outside cluster mode a value-type behavior is carried by `LocalBehavior`.

One type, `BehaviorPlacementError`, covers both registration (`NewEngine`, empty `EntityID`) and spawning. The name says "placement" even when the error comes from registration. The name `BehaviorPlacementError` was confirmed 2026-09-27 (§11, item 2), and the alternative names were rejected. Its doc comment states that it also covers registration.

### 5.7 Every place that type-asserts or passes a behavior as `extension.Dependency`

This table covers the whole repository at `77beda6`, including nested modules, examples and tests (`rg -n "extension\.Dependency|EntityKind|WithEntityKinds"` plus the actor type assertions).

| Place | Today | After S3 |
|---|---|---|
| `engine.go:662`, `911`, `1310` | `actorSystem.Inject(behavior)` | Done inside `spawnDependency`, only for the pass-through case |
| `engine.go:692`, `925`, `1326` | `[]extension.Dependency{behavior, …}` | `[]extension.Dependency{dep, …}` with `dep` from `spawnDependency` |
| `engine.go:253-257` (`NewEngine`) | Injects `config.entityKinds` | Injects `config.behaviorKinds` after the pointer check |
| `event_sourced_actor.go:125`, `408` | Field and assertion typed `EventSourcedBehavior` | `behaviorport.EventSourced`, via `behaviorFrom` |
| `event_sourced_actor.go:749` | Asserts `EventSourcedEnvelopeBehavior` | Asserts `behaviorport.EventSourcedEnvelope` |
| `durable_state_actor.go:56`, `162`, `497` | `DurableStateBehavior` / `DurableStateEnvelopeBehavior` | `behaviorport.DurableState` / `DurableStateEnvelope` |
| `saga_actor.go:51`, `173` | `SagaBehavior` | `behaviorport.Saga` |
| `resolveScope` in the three actors (`event_sourced_actor.go:448`, `durable_state_actor.go:613`, `saga_actor.go:274`) | Take `[]extension.Dependency`; look only for `*extensions.EntityTenantScope` | Unchanged; they never touch the behavior |
| `testkit_compat_test.go:34-35` | Asserts ego contracts satisfy the testkit copies | Unchanged; add the same assertion for `behaviorport.EventSourced`/`DurableState` |
| `engine_test.go:1001`, `engine_tenant_cluster_test.go:68` | `WithEntityKinds(new(AccountEventSourcedBehavior))` | Unchanged; they keep covering the deprecated path |
| `example/cluster/behavior.go:118-133` | Implements the serialization methods | Kept, since the cluster example needs them; switched to the new names in S3-5 |
| `example/eventssourced/main.go:189-209` (and the `durablestate`, `saga` examples) | Implements the serialization methods only for the interface | Removed in S3-5 (#123 criterion 4) |
| Nested modules (`publisher/*`, `benchmark`) | No use of these symbols as `extension.Dependency` (`benchmark` implements `MarshalBinary` on its behaviors only) | Unchanged |

Callers outside this repository that use a behavior **as** an `extension.Dependency` (calling `MarshalBinary` through an `ego.EventSourcedBehavior`, passing one to `goakt.WithDependencies`, or spreading a `[]ego.EntityKind` into `actorSys.Inject`) keep compiling. The old names keep their method sets, and `EntityKind` remains an alias of `extension.Dependency`.

## 6. Public API changes and apidiff expectation

| Symbol (package `ego` unless stated) | Change | Marker |
|---|---|---|
| package `port/behavior`: `EventSourced`, `EventSourcedEnvelope`, `DurableState`, `DurableStateEnvelope`, `Saga`, `SagaAction`, `SagaCommand`, `Command`, `Event`, `State` | Added | — |
| `EventSourcedBehavior`, `EventSourcedEnvelopeBehavior`, `DurableStateBehavior`, `DurableStateEnvelopeBehavior`, `SagaBehavior` | Re-expressed on top of `port/behavior`; method sets unchanged | `Deprecated:` (S3-5) |
| `SagaAction`, `SagaCommand` | Become aliases of `port/behavior` types | None: aliases stay valid names until #124 |
| `(*Engine).SpawnEventSourced`, `SpawnDurableState`, `SpawnSaga` | Added | — |
| `(*Engine).Entity`, `DurableStateEntity`, `Saga` | Signatures unchanged; delegate | `Deprecated:` (S3-5) |
| `BehaviorKind`, `WithBehaviorKinds` | Added | — |
| `EntityKind`, `WithEntityKinds` | Unchanged | `Deprecated:` (S3-5) |
| `ErrBehaviorNotSerializable`, `ErrBehaviorNotPointer`, `BehaviorPlacementError` | Added | — |

Every deprecated symbol carries the same sentence: "Removed in the next major release (#124)." This follows decision 2.

**apidiff expectation** for package `ego`, `77beda6` against each slice head and against the final head:

- *Compatible*: the additions above.
- *Reported as incompatible, known false positive*: `SagaAction` and `SagaCommand` "changed from X to X", and the four `SagaBehavior` methods that mention them (`HandleEvent`, `HandleResult`, `HandleError`, `Compensate`). This is the same limitation S1 hit with cross-package aliases (`odd/tasks/port-publishing.md`, T2: `apidiff.go` line 1 has a TODO for aliases to another package). The same spike reproduced it for a moved struct and an interface that returns it.
- *Anything else reported as incompatible blocks the slice.*

As in S1, a **consumer program** written against the `77beda6` API decides compatibility. It must build, vet and print identical results against base and head. It implements all three old contracts; embeds them; calls `MarshalBinary` through an `ego.EventSourcedBehavior`; passes one to `goakt.WithDependencies`; spreads a `[]ego.EntityKind` into `actorSys.Inject`; binds `(*ego.Engine).Entity` to a `func` variable of its old type; builds `ego.SagaAction`/`ego.SagaCommand` literals; and type-asserts between the old and new interfaces. The only expected difference is `reflect.TypeOf` naming for the two moved structs, which the `CHANGELOG.md` entry records.

## 7. Architecture check (archcheck) impact

- `port/behavior` is a contract under `port/`, so `contract-allowlist` applies automatically (`internal/cmd/archcheck/rules/layers.go:42-52`). Its imports are all admitted. It gets an architecture test in the style of `port/publishing/publishing_architecture_test.go` that lists its allowed non-standard dependencies.
- Package `ego` and `internal/extensions` keep importing GoAkt, as the adapter layer permits.
- **No new baseline entry and no archcheck exception.**
- **The `migration -> ego` entry** (`internal/cmd/archcheck/baseline.go:33-40`, owned by S4/#11) **does not change.** `migration`'s only production use of package `ego` is `ego.ResolveLogger`, called at `migration/migration.go:149` and `migration/tenant_adoption.go:516`. It is a logger helper that has nothing to do with behavior contracts. S3 neither satisfies the entry's removal criterion nor adds to it. This answers #123's last acceptance criterion: the entry stays, because it is about logging, not behaviors. The entry's own `Justification` and `RemovalCriterion` text in `internal/cmd/archcheck/baseline.go` is stale: it names "runtime types" and S3. Rewording it is a code change, so it is a follow-up (§12) and must not add entries.

## 8. Test plan

Strict TDD applies: each slice records the failing (RED) run before the implementation. For tests that name a symbol S3 adds, RED is a build failure of the test file ("undefined: …"), recorded in the pull request. Where a behavioral RED exists at `77beda6`, it is recorded too.

**Remote-spawn test (multi-node).** The repository already has a two-node cluster test in the root package: `TestEngineMultiNodeRemoteEntitySpawn` (`engine_test.go:974-1075`). It starts two actor systems in one process with `mockClusterProvider`, `dynaport` ports, `ClusterKinds()`, `RoundRobin` placement through `SpawnOn`, and eight spawns from node 1 so that some land on node 2. `example/cluster` needs Kubernetes (`example/cluster/k8s`), so it is not a CI test. S3-3 extracts the node setup of that test into a helper, `newTestCluster(t, nodeOpts...)`, without changing its assertions, and adds `TestEngineMultiNodeNeutralBehaviors` in a new file `engine_neutral_cluster_test.go`, reusing one two-node cluster across subtests:

| Subtest | Slice | Setup | Assertion | Before (`77beda6`) | After |
|---|---|---|---|---|---|
| `serializable behavior placed remotely` | S3-3 | Both nodes `WithEntityKinds(new(AccountEventSourcedBehavior))` (the existing option, so S3-3 does not depend on S3-4); node 1 calls `SpawnEventSourced` eight times | Every spawn and `SendCommand` succeed; at least one entity is hosted by node 2 (checked by a local-actor lookup on node 2) | Build failure (`SpawnEventSourced` undefined) | Pass |
| `domain-only behavior is rejected in cluster mode` | S3-3 | A test behavior with only domain methods and `ID()`, for each of the three families | `errors.Is(err, ErrBehaviorNotSerializable)`; `errors.As` yields `Kind` and `EntityID`; no actor with that name exists on either node; no panic | Build failure (the behavior cannot even be passed to `Entity`) | Pass |
| `value-type behavior in cluster mode` | S3-3 | A behavior implementing the **old** contract with value receivers, spawned through the **old** API: `engine1.Entity(ctx, valueTypeBehavior)` | `errors.Is(err, ErrBehaviorNotPointer)`; no actor spawned; no panic | **Behavioral RED, runnable on `main`**: `Inject` at `engine.go:662` panics in GoAkt's registry (`internal/types/registry.go:109`, `Elem()` on a non-pointer) | Pass |
| `old and new registration interoperate` | S3-4 | Node 1 `WithEntityKinds(...)`, node 2 `WithBehaviorKinds(...)`; spawns issued from both nodes | All spawns and commands succeed in both directions, which proves the registry key and bytes are unchanged | Build failure (`WithBehaviorKinds` undefined) | Pass |

**How the `main` run of the value-type subtest is guarded.** The `ErrBehaviorNotPointer` assertion does not compile on `main`, so the RED run uses a throwaway copy of the subtest that only makes the old-API call. That copy is not committed; its output goes in the S3-3 PR. The call panics while GoAkt holds the actor-system lock, because `Inject` releases the lock without `defer` (`actor/actor_system.go:2255-2265`). A recovered panic would leave that lock held, and the cluster cleanup would hang. So the copy does not recover the panic, and it runs with a short timeout: `go test -run 'TestEngineMultiNodeNeutralBehaviors/value-type' -timeout 90s .`. Either the unrecovered panic ends the binary or the timeout does, and neither can hang CI.

**Single-node tests (S3-2, S3-3).**

- *Value-type behavior on a single node* (behavioral RED at `77beda6`): `engine.Entity(ctx, valueTypeBehavior)`, where the behavior implements the old contract with value receivers, panics inside GoAkt's registry (§2.3). The RED run records that panic. The test does not recover it, because a recovered panic leaves the actor-system lock held. It runs under a short timeout, `go test -run <single-node value-type test> -timeout 90s .`, so either the unrecovered panic ends the test binary or the timeout does, the same guard as the cluster subtest. After S3-2 it spawns through `LocalBehavior` and answers a command. The cluster-mode case is the `value-type behavior in cluster mode` subtest above.
- *Domain-only behavior on a single node* (#123 criterion 2): `SpawnEventSourced`, `SpawnDurableState` and `SpawnSaga` with behaviors that implement only the `port/behavior` methods; one command round trip each; an envelope-capable behavior still receives `HandleEnvelope`.
- *Pass-through identity*: `spawnDependency` returns the very same pointer for a serializable pointer behavior (the wire-format guarantee), and a `*extensions.LocalBehavior` for the other cases outside cluster mode.
- *`NewEngine` with a value-type kind* returns `*BehaviorPlacementError` wrapping `ErrBehaviorNotPointer` instead of panicking.

**Unchanged regression coverage.** `TestEngineMultiNodeRemoteEntitySpawn` and the tenant cluster test (`engine_tenant_cluster_test.go`) keep using `Entity`/`WithEntityKinds` and must pass unmodified, apart from the setup-helper extraction. The whole root suite, `go vet`, archcheck and golangci-lint run per slice. The nested-consumer check from `ego-arch-001/design.md` §5 runs on the slices that change exported API (S3-1, S3-3, S3-4, S3-5). No `-race` and no workbench, per the repository owner's test rules.

## 9. Implementation slices

Each slice is one pull request of about 400 changed lines or fewer (advisory), with at most five tasks, and leaves `main` releasable. "Root hot files" means `engine.go`/`option.go`, which #105's implementation also edits (#126 IMPL-1 and IMPL-4 in PR #125). Those slices are serialized with #105.

| Slice | Content | Files owned | Root hot files? | Ordering | Check |
|---|---|---|---|---|---|
| **S3-1** Contracts | `port/behavior` package with docs and its architecture test; `behavior.go` and `saga.go` re-expressed on top of it (method sets unchanged); `SagaAction`/`SagaCommand` aliases; `sagaActionIsNoop`; compatibility assertions; apidiff and consumer-program evidence | `port/behavior/*` (new), `behavior.go`, `saga.go`, `saga_actor.go` (one call site), `testkit_compat_test.go` | No | First. Independent of #105 | `go test ./port/behavior/` (dependency allowlist test) passes; archcheck passes with an unchanged baseline; apidiff reports only the §6 alias false positives; the base-API consumer program builds and prints identical output on base and head |
| **S3-2** Spawn-site bridge | `spawnDependency`, `extensions.LocalBehavior`, `behaviorFrom`; the two error sentinels and `BehaviorPlacementError`; actors switched to neutral field types; `Entity`/`DurableStateEntity`/`Saga` bodies renamed in place to `spawnEventSourced`/`spawnDurableState`/`spawnSaga` (neutral parameters), with the public methods delegating; single-node value-type RED/GREEN tests | new `behavior_dependency.go` (+ test), `internal/extensions/extensions.go` (+ test), `engine.go` (the three spawn methods at `645-704`, `886-937`, `1295-1344` and the error `var` block only), `event_sourced_actor.go`, `durable_state_actor.go`, `saga_actor.go` | **Yes, `engine.go`** | **Must land before #105 IMPL-4** (orchestrator constraint, critical path #125 → #123 → S4). IMPL-4 rebases onto it and puts its family guard in the three unexported functions (§5.4). If #126 IMPL-1 lands first, S3-2 rebases onto it; the in-place rename keeps that conflict to a few lines. Kept small on purpose | The single-node value-type test is RED on `main` (panic) and GREEN on the slice; the `spawnDependency` pass-through identity and local-wrapper unit tests pass; the existing root suite, including `TestEngineMultiNodeRemoteEntitySpawn`, passes unchanged |
| **S3-3** Public entry points and remote-spawn test | `SpawnEventSourced`/`SpawnDurableState`/`SpawnSaga` as one-line delegations in a new file; two-node test helper extraction; `TestEngineMultiNodeNeutralBehaviors` with its three S3-3 subtests (serializable behavior placed remotely, registered with `WithEntityKinds`; domain-only behavior rejected; value-type behavior in cluster mode); single-node domain-only tests | new `engine_spawn.go`, new `engine_neutral_cluster_test.go`, `engine_test.go` (setup-helper extraction only) | No (new file; `engine.go` untouched) | After S3-2. Independent of S3-4 | The value-type cluster subtest's guarded run on `main` panics (output recorded in the PR); all three S3-3 subtests and the single-node domain-only tests pass on the slice; `TestEngineMultiNodeRemoteEntitySpawn` passes after the helper extraction with unchanged assertions |
| **S3-4** Kind registration | `BehaviorKind`, `WithBehaviorKinds`; `WithEntityKinds` forwards to the shared field; `NewEngine` pointer check and typed error; the `old and new registration interoperate` subtest added to `TestEngineMultiNodeNeutralBehaviors`; `NewEngine` value-type test | `option.go` (`329-362` and the `Config` field at `59`), `engine.go` (`NewEngine`, `247-259`), `engine_neutral_cluster_test.go` (one subtest), tests | **Yes, both** | After S3-2 (and S3-3, which owns the test file). **Must land before #105 IMPL-4** (human decision, 2026-09-27), so `ego.BehaviorKind` exists when IMPL-4 adds `compose/goakt.WithCluster`. IMPL-4 rebases onto it; its additive `ego` options in `option.go` touch different regions | The interop subtest passes in both directions; `NewEngine` with a value-type kind returns `ErrBehaviorNotPointer` in single-node mode and does not panic; the S3-3 subtests still pass |
| **S3-5** Deprecation and migration | `Deprecated:` markers on every symbol in §6; `CHANGELOG.md` migration note with the old-to-new table and final apidiff; examples `eventssourced`, `durablestate`, `saga` switched to `port/behavior` + `Spawn*` with their serialization methods removed; `example/cluster` switched to `WithBehaviorKinds`/`SpawnEventSourced`, keeping serialization; `testkit` doc points to `port/behavior` | `behavior.go`, `saga.go`, `option.go`, `engine.go` (doc comments only), `CHANGELOG.md`, `example/*` | Yes, comments only | Last. Deprecation lands after in-repo callers have migrated, so golangci-lint's `staticcheck` (enabled in `.golangci.yml`) does not flag new lines; tests that deliberately cover the deprecated path carry `//nolint:staticcheck` with a reason | golangci-lint passes; every example builds and the three non-cluster examples run with no serialization methods left; the final apidiff and consumer-program results are recorded in `CHANGELOG.md` and the PR; the nested-consumer check passes |

Five slices fit the five-task cap. Work beyond it goes to the named follow-up in §12.

## 10. Rollback

S3-1, S3-3 and S3-4 are additive and can be reverted up to the first release that contains them. After that, `port/behavior` is public v4 API, and a rollback may revert in-repository callers but must not delete the package, the same rule as `port/publishing`. S3-2 changes internal wiring only. Reverting it restores the direct `Inject`/`WithDependencies` calls and the value-type panic. S3-5 changes comments, examples and the changelog.

## 11. Decisions for the human

1. **Reading of #123's first acceptance criterion.** Decision 2 makes it impossible to remove the embed from `ego.EventSourcedBehavior`, `ego.DurableStateBehavior` and `ego.SagaBehavior` inside v4 (§3, apidiff-incompatible). **Decided 2026-09-27:** in v4 the criterion is met by `port/behavior.EventSourced`/`DurableState`/`Saga`. The old names keep the embed, deprecated, until they are removed at #124.
2. **New names.** **Decided 2026-09-27:** confirmed as proposed: `port/behavior`, `Engine.SpawnEventSourced`/`SpawnDurableState`/`SpawnSaga`, `BehaviorKind`/`WithBehaviorKinds`, and `BehaviorPlacementError` (which also covers registration errors). The alternative error names were rejected.

Recorded, not open: protobuf policy and the deprecation window were decided on 2026-09-26 and are now written in `ego-arch-001/design.md` §10.

## 12. Follow-ups (outside S3)

- **`ego-arch-002-s3-followup`**: wrap GoAkt's remote "dependency type %q not registered" error (`codec.go:101`) in a typed ego error, so a kind missing from a peer's registration is as clear as the errors in §5.6. It needs a remote error round trip, which S3 does not change.
- **archcheck baseline wording (code change, later slice)**: the `migration -> ego` entry's `Justification` and `RemovalCriterion` in `internal/cmd/archcheck/baseline.go` name "runtime types" and S3, but `migration` only calls `ego.ResolveLogger` (§7). Rewording them must not add entries.
- **#124**: remove every symbol marked deprecated in §6 and make the neutral names the only ones.
- **PR #125 / #105 IMPL-4 (alignment, not a gap)**: S3-2 and S3-4 land before IMPL-4 (human decision, 2026-09-27). So `ego.BehaviorKind` already exists when IMPL-4 adds `compose/goakt.WithCluster(cfg, kinds ...ego.BehaviorKind)`, and IMPL-4 never has to adopt the deprecated `EntityKind`. Its walkthrough calls `SpawnEventSourced`, and its family guard goes in the unexported spawn functions from S3-2 (§5.4). #125 is being aligned in parallel. Names confirmed 2026-09-27 (§11, item 2).
- **#11 (`RUNTIME-003`)**: if a runtime ever has to place behaviors that cannot serialize themselves (for example by factory), that is a placement capability of the runtime SPI, not a behavior contract (§4, option A).
