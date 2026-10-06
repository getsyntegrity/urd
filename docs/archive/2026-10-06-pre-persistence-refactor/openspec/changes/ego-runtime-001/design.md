# Design — Runtime SPI, application side: `port/runtime` (EGO-RUNTIME-001/002, slice S4)

| Field | Value |
|---|---|
| Change | `ego-runtime-001` |
| Date | 2026-09-27 |
| Phase | `sdd-design` |
| Tracker | [`#147`](https://github.com/getsyntegrity/ego/issues/147) slice S4-D, parent [`#11`](https://github.com/getsyntegrity/ego/issues/11) |
| Inputs | [`exploration.md`](./exploration.md), [`proposal.md`](./proposal.md); ego-arch-001 [`design.md`](../ego-arch-001/design.md) §2, §3, §4, §5, §10; ego-arch-002-s3 [`design.md`](../ego-arch-002-s3/design.md) §5, §6; ego-arch-003 [`design.md`](../ego-arch-003/design.md) §5.2, §6; ego-arch-006 [`design.md`](../ego-arch-006/design.md) §2.2 (F1, F3), §2.3; ego-arch-004 design in open PR [`#149`](https://github.com/getsyntegrity/ego/pull/149) |
| Baseline | `main` at `f2b5130148086d95a4d6362971b85e800b0ce155` |

## 1. Summary and vocabulary

Consumer code runs Ego through `*ego.Engine`, the GoAkt adapter, so it cannot run on any other runtime. This design adds a contract package, `port/runtime`, with one small interface per capability (entities, sagas, projections, events) and a composite, `Runtime`. It moves the five types those interfaces need out of package `ego` and leaves aliases behind, so nothing breaks in v4. It makes `SpawnOption` readable by other runtimes through a read-only `SpawnSettings` value. It then proves the contract three ways: `*ego.Engine` implements it unchanged, a GoAkt-free test double implements it, and a consumer package that never imports `ego` or GoAkt drives a real `compose/goakt` application through a new accessor, `App.Runtime()`.

Terms used below:

- **Runtime**: the component that hosts entities and sagas, delivers commands to them, and runs projections. Today only the GoAkt adapter (`*ego.Engine`) exists; #148 adds an in-memory one.
- **SPI (service provider interface)**: the contract a runtime implements so Ego and its consumers can use it. #11 splits it into an application side (what consumers call: this design) and a provider side (lifecycle and capability declaration, aligned with #106).
- **Capability**: a group of operations a runtime may or may not support, such as projections. Here each capability is one Go interface.
- **Alias**: a Go type alias `type X = runtime.X` or variable `var ErrX = runtime.ErrX`. The old name and the new name denote the same type or the same error value.
- **Sealed interface**: an interface whose method mentions an unexported type, so only its own package can implement it.
- **Adapter setting**: a spawn setting that only one runtime adapter understands (§D3).
- **apidiff**: `golang.org/x/exp/cmd/apidiff`, which reports incompatible changes to a package's exported API.

## 2. Decisions already taken by the maintainer

These were decided on #147 on 2026-09-27. This design records them and does not reopen them.

1. **Entity reference** is the existing `string` ID. An `EntityRef` handle is a follow-up owned by #29 and #12.
2. **`port/runtime`** holds small interfaces per capability (entities, sagas, projections, events) plus one composite interface.
3. **`SpawnOption` moves to `port/runtime`** with a resolved read-only settings struct and aliases in `ego`. Whether write-side options belong in the contract is #12's call.
4. **The `migration -> ego` baseline entry** is removed through `internal/logging` (slice S4-1, in progress separately; this design only depends on its ordering, §9).
5. **Physical extraction of the engine** stays in #124.
6. **RUNTIME-005 and `compose/inmem`** are tracked in #148.
7. **`NewEngine`, `Config.GoaktOptions`, `Engine.ActorSystem` and `ClusterKinds` are not deprecated in S4.** #124's plan decides.

The maintainer's instruction for S4-D adds two constraints: keep small interfaces per capability, and keep v4 public compatibility (no break inside v4; aliases with preserved identity; deprecations only per ego-arch-001 §10 until #124's major release).

**Maintainer decisions (2026-09-27) on this design (PR #151):**

8. **Q1:** no `Deprecated:` markers on the aliases (S1, S3 and the new S4 aliases) or on the `ego.With*` wrappers; they stay unmarked until #124 removes them, accepting that consumers get no staticcheck warning before then. ego-arch-001 §10 is corrected accordingly (§10).
9. **Q2:** `runtime.WithAdapterSetting` is public v4 API, additive and not removable inside v4; the home of the write-side options stays #12's call (§10).

Everything else in this document is a proposal that becomes accepted when this pull request is approved.

## 3. Scope boundary

| Concern | Owner | This design |
|---|---|---|
| Operations consumer code calls on a running runtime | #147 (here) | Defines them |
| Adapter identity, capability declaration, lifecycle contract (`Start`, `Ping`, `Close`) | #106, PR #149 | Uses none of it; §8 shows the fit |
| Runtime capability negotiation (inspect before calling) | RUNTIME-006, #149 F-E | Only the call-time answer, `ErrUnsupported` (§D4) |
| Placement, supervision, passivation semantics | RUNTIME-003 | Moves the types unchanged; semantics stay as documented today |
| In-memory runtime, `compose/inmem` | #148 | Defines the interface it implements |
| Write-side options (snapshots, retention, batching) | #12 | Keeps them out of the contract (§D3) |
| Drain, admission, shutdown | #24 | No lifecycle method in the interface |
| Physical move of the engine, removal of aliases and deprecated API | #124 | Records the inventory and destinations (§D10) |

## Decisions (D1–D10)

### D1 — The interfaces

`port/runtime` (`github.com/pablogore/ego/v4/port/runtime`) is a contract package: `contract-allowlist` applies to it because it sits under `port/` (`internal/cmd/archcheck/rules/layers.go:62`, `:81`). Its direct imports are the standard library, `command`, `eventstream`, `tenancy` and `port/behavior`, all contracts.

```go
package runtime

import (
	"context"
	"time"

	"github.com/pablogore/ego/v4/command"
	"github.com/pablogore/ego/v4/eventstream"
	"github.com/pablogore/ego/v4/port/behavior"
)

// Entities spawns event-sourced and durable-state entities and invokes them
// by their string ID.
type Entities interface {
	SpawnEventSourced(ctx context.Context, b behavior.EventSourced, opts ...SpawnOption) error
	SpawnDurableState(ctx context.Context, b behavior.DurableState, opts ...SpawnOption) error
	EntityExists(ctx context.Context, entityID string) (bool, error)
	SendCommand(ctx context.Context, entityID string, cmd behavior.Command, timeout time.Duration) (resultingState behavior.State, revision uint64, err error)
	Dispatch(ctx context.Context, entityID string, env command.Envelope, timeout time.Duration) (command.Result, error)
	EraseEntity(ctx context.Context, persistenceID string, full bool) error
}

// Sagas spawns sagas and reports their status.
type Sagas interface {
	SpawnSaga(ctx context.Context, b behavior.Saga, timeout time.Duration, opts ...SpawnOption) error
	// SagaStatus returns the saga's ID and current state. Known gap: the
	// GoAkt adapter never fills SagaInfo.Status, so it always reads
	// SagaRunning (engine.go:1638-1641; follow-up FU-1, #153). Callers must
	// not rely on Status until #153 is fixed.
	// (Fixed by #163: SagaInfo.Status now carries the lifecycle status.)
	SagaStatus(ctx context.Context, sagaID string, timeout time.Duration) (*SagaInfo, error)
}

// Projections controls the projections registered with the runtime.
type Projections interface {
	StartProjection(ctx context.Context, name string) error
	StopProjection(ctx context.Context, name string) error
	IsProjectionRunning(ctx context.Context, name string) (bool, error)
	RebuildProjection(ctx context.Context, name string, from time.Time) error
	ProjectionLag(ctx context.Context, name string) (map[uint64]time.Duration, error)
}

// Events gives access to the runtime's in-process event stream.
type Events interface {
	Subscribe() (eventstream.Subscriber, error)
}

// Runtime is every capability together. It is what a composition root
// returns (compose/goakt.App.Runtime; compose/inmem in #148).
type Runtime interface {
	Entities
	Sagas
	Projections
	Events
}
```

Each method's signature is the `*ego.Engine` method's, type for type (only parameter names may differ); `behavior.Command`/`behavior.State` are aliases of `proto.Message`, identical to `ego.Command`/`ego.State` (`behavior.go:37`, `:45`). Doc comments move from `engine.go` and `engine_spawn.go` in the S4-3 pull request, with one change: runtime-specific sentences (for example "in cluster mode the projection runs as a singleton", `engine.go:531-532`) stay on the `Engine` method, and the interface comment says what every runtime guarantees.

**What is left out, and why.**

- `Start`, `Stop`, `Started`: lifecycle belongs to the composition root (ego-arch-003 §D6, §D7), and #24 owns its policy. `Started` is not named by #147; it is a lifecycle probe, and every interface method already reports "not started" through `ErrEngineNotStarted`.
- `AddEventPublishers`, `AddStatePublishers`: wiring the composition root performs (`compose/goakt/app.go:355`).
- `ActorSystem`: GoAkt-bound (decision 7 keeps it, outside the contract).
- `Entity`, `DurableStateEntity`, `Saga`: deprecated, GoAkt-bound (exploration §2).

**Why EraseEntity is under Entities.** It addresses one entity by ID and is the GDPR operation consumers call on an entity (#147 lists it there). It does not touch actors, so a runtime without persistence of its own still implements it through the stores.

**Package name.** The package clause is `runtime`, the last element of the path, as Go convention asks. It shadows the standard library's `runtime` in a file that imports both; such a file imports this one under another name. Package `ego`, `compose/goakt` and `internal/runtimeconsumer` import it as `runtimeport`, the way package `ego` imports `port/behavior` as `behaviorport` (ego-arch-002-s3 §5.1).

### D2 — Moved types, aliases and error identity

Moved to `port/runtime`, each with its doc comment:

| Symbol | From | Alias left in `ego` |
|---|---|---|
| `EntitiesPlacement`, `RoundRobin`, `Random`, `Local`, `LeastLoad` | `spawn_config.go:36-62` | `type EntitiesPlacement = runtimeport.EntitiesPlacement`; `const RoundRobin = runtimeport.RoundRobin` (and the other three) |
| `SupervisorDirective`, `StopDirective`, `RestartDirective` | `supervisor.go:33-45` | same pattern |
| `SagaStatus`, its four constants and `String` | `saga.go:74-101` | same pattern; `String` moves with the type |
| `SagaInfo` | `saga.go:104-111` | `type SagaInfo = runtimeport.SagaInfo`; its `State` field is declared `behavior.State`, which is `proto.Message` |
| `SpawnOption` | `spawn_config.go:111-114` | `type SpawnOption = runtimeport.SpawnOption` (§D3) |
| Ten neutral sentinels (exploration §3.3) | `engine.go:63`, `:65`, `:69`, `:74`, `:83`, `:118`, `:127`, `:135`, `:140`, `:171` | `ErrEngineNotStarted = runtimeport.ErrEngineNotStarted` inside the existing `var` block, and so on |

New in `port/runtime` (no old name): `SpawnSettings`, `ResolveSpawnOptions`, `WithAdapterSetting` (§D3); `ErrUnsupported`, `UnsupportedError` (§D4); the five neutral option constructors (`WithPassivateAfter`, `WithRelocation`, `WithSupervisorDirective`, `WithPlacement`, `WithTenant`), which package `ego` keeps as one-line functions with unchanged signatures that return the `port/runtime` option.

**Names and messages are kept.** The sentinels keep their names and their exact messages, including the few that mention `ego.` (`ErrProjectionNotRegistered`: "register it with ego.WithProjection"). Tests and log-based alerts compare those strings, and a renamed error would be a second name for the same thing. A neutral wording can come with #124, when the `ego` names disappear anyway.

**Error identity.** `var ErrX = runtimeport.ErrX` makes `ego.ErrX` and `runtimeport.ErrX` the same pointer, so `errors.Is(err, ego.ErrX)` and `errors.Is(err, runtimeport.ErrX)` are equivalent for every error, wrapped or not. This is how `ego.ErrPublisherNotStarted` works since S1 (`publisher.go:39`). S4-2 adds a table test that wraps each of the ten sentinels and checks both directions, the #147 criterion.

**What stays in `ego`:** `RetentionPolicy` (write side, §D3), `EntityFamily` (a `Config` setting), the GoAkt and construction errors (exploration §3.3), `BehaviorPlacementError`, `Done`, `ZeroTime`.

### D3 — `SpawnOption`: sealed, but readable

**Old**, at `spawn_config.go:65-125`:

```go
type spawnConfig struct { /* nine unexported fields, runtime and write side */ }

type SpawnOption interface {
	Apply(config *spawnConfig) // unexported parameter type: sealed to package ego
}
```

**New**, in `port/runtime`:

```go
// spawnConfig is unexported so that SpawnOption stays sealed to this
// package: options are built only by the constructors below.
type spawnConfig struct {
	passivateAfter      time.Duration
	relocation          bool
	supervisorDirective SupervisorDirective
	placement           EntitiesPlacement
	tenantID            tenancy.TenantID
	adapter             map[any]any // adapter settings, by key
}

// SpawnOption configures one spawn. Build it with the With* functions of
// this package, or of a runtime adapter for adapter settings.
type SpawnOption interface {
	Apply(config *spawnConfig)
}

// SpawnSettings is the resolved, read-only result of a list of options.
// Runtimes read it; nothing can change it after ResolveSpawnOptions. It
// holds a map of adapter settings, so it is not comparable with ==; compare
// the getters instead.
type SpawnSettings struct{ c spawnConfig }

// ResolveSpawnOptions applies opts in order over the defaults
// (RestartDirective, RoundRobin) and returns the result. A nil option is
// skipped; a non-nil value that embeds a nil SpawnOption (for example
// struct{ SpawnOption }{}) is not nil, so its Apply still panics, as today.
func ResolveSpawnOptions(opts ...SpawnOption) SpawnSettings

func (s SpawnSettings) PassivateAfter() time.Duration            // 0: no passivation
func (s SpawnSettings) Relocation() bool                         // default false, as the code behaves today (exploration §5.2)
func (s SpawnSettings) SupervisorDirective() SupervisorDirective // default RestartDirective
func (s SpawnSettings) Placement() EntitiesPlacement             // default RoundRobin
func (s SpawnSettings) Tenant() tenancy.TenantID                 // "" when not declared
func (s SpawnSettings) AdapterSetting(key any) (any, bool)

func WithPassivateAfter(after time.Duration) SpawnOption
func WithRelocation(toRelocate bool) SpawnOption
func WithSupervisorDirective(directive SupervisorDirective) SpawnOption
func WithPlacement(placement EntitiesPlacement) SpawnOption
func WithTenant(id tenancy.TenantID) SpawnOption

// WithAdapterSetting carries a value that only one runtime adapter reads,
// under a key only that adapter can name (an unexported type, as with
// context.WithValue). Other runtimes ignore it. Like context.WithValue,
// it panics when it is called, not later at spawn, if key is nil or not
// comparable, so a bad key fails where the option is built.
func WithAdapterSetting(key, value any) SpawnOption
```

**Relocation's documentation.** Today `WithRelocation`'s comment says "In cluster mode, entities are relocatable by default" (`spawn_config.go:141-144`), but the code disables relocation unless `WithRelocation(true)` is passed (`engine.go:1903-1905`). `runtime.WithRelocation` does not inherit that sentence: its comment keeps the parameter description (`spawn_config.go:150-154`) and states the actual default, "relocation is disabled unless `WithRelocation(true)` is passed; RUNTIME-003 owns the contract". The comment on `ego.WithRelocation` becomes a pointer to it. Whether the default should change is follow-up FU-2 (#154), not S4.

**Why this keeps v4 compatible.** The method keeps its name, `Apply`, and keeps an unexported parameter type; only the package that declares that type changes, from `ego` to `port/runtime`. Code outside both packages could never name either type, so every use it can write compiles unchanged: holding and passing options, storing them in a `[]ego.SpawnOption`, embedding `ego.SpawnOption` in a struct (the only way outside code can satisfy it), taking the method expression `ego.SpawnOption.Apply`, even the pathological `opt.Apply(nil)`. The spike's consumer program exercises every one of these against the baseline and the moved tree (§11).

**Why readable, not implementable.** A runtime needs to *read* what the caller asked for; nobody outside `port/runtime` needs to *invent* a new neutral option, and letting them would make the contract's option set open-ended. Adapter-specific settings get the one controlled door, `WithAdapterSetting`.

**Why getters, not exported fields.** A struct with exported fields would be a second way to state spawn settings (a literal), whose zero value does not match the defaults (`RestartDirective` is 1, `supervisor.go:44`) and whose fields #12 could not later reshape without a break. Getters are read-only by construction and let a field be added additively.

**Write-side options.** `WithSnapshotInterval`, `WithRetentionPolicy`, `WithBatchThreshold` and `WithBatchFlushWindow` stay in package `ego` with unchanged signatures. Each returns `runtimeport.WithAdapterSetting(key, value)` under an unexported key type of package `ego`. The GoAkt adapter reads them back when it builds its private `spawnConfig`:

```go
// spawn_config.go after S4-2 (package ego)
type (
	snapshotIntervalKey struct{}
	retentionPolicyKey  struct{}
	batchThresholdKey   struct{}
	batchFlushWindowKey struct{}
)

func WithBatchThreshold(threshold int) SpawnOption {
	return runtimeport.WithAdapterSetting(batchThresholdKey{}, threshold)
}

func newSpawnConfig(opts ...SpawnOption) *spawnConfig {
	s := runtimeport.ResolveSpawnOptions(opts...)
	c := &spawnConfig{ /* the five neutral fields from s's getters */ }
	if v, ok := s.AdapterSetting(batchThresholdKey{}); ok {
		c.batchThreshold = v.(int)
	}
	// ... the other three write-side settings the same way
	return c
}
```

So the contract carries no write-side field, which leaves #12's decision open, and the three call sites of `newSpawnConfig` in `engine.go` (`:783`, `:1068`, `:1546`) do not change. Another runtime ignores these settings, exactly as it would ignore any adapter's setting. If #12 decides a write-side setting belongs in the contract, a getter is added to `SpawnSettings` and the `ego` constructor switches to a neutral one: additive. Consumer code that wants a write-side option must import `ego` until then; that is the price of not deciding #12 here. The mechanism itself was approved as public v4 API (§10, Q2).

**Behavior change: nil options.** `newSpawnConfig` calls `Apply` on every option (`spawn_config.go:104-106`), so a nil `SpawnOption` panics today. `ResolveSpawnOptions` skips it. No caller can rely on that panic; S4-2's `CHANGELOG.md` entry records the change.

**apidiff expectation.** The alias moves make apidiff report an incompatible change for every symbol whose declaration now names a `port/runtime` type. The spike measured 31 such lines for package `ego` (§11), of two shapes. 16 read "changed from X to X": the seven `Engine` methods that take `...SpawnOption` or return `*SagaInfo`, and the nine `With*` spawn options. 15 read "changed from X to github.com/pablogore/ego/v4/port/runtime.X": the five moved types (for example `SpawnOption: changed from SpawnOption to …/port/runtime.SpawnOption`) and their ten constants (for example `RoundRobin: changed from EntitiesPlacement to …/port/runtime.EntitiesPlacement`), because each now reports its type's new qualified name. This is the cross-package-alias limitation S1 and S3 hit (ego-arch-002-s3 §6), not a real change; as there, the consumer program decides compatibility. The sentinels produce no report (their type is `error` before and after).

### D4 — "Not supported"

A runtime that implements `Runtime` implements every method. When it lacks a capability, or one operation of it, the method returns an error that matches `runtimeport.ErrUnsupported`:

```go
// ErrUnsupported reports an operation this runtime does not provide. It
// wraps errors.ErrUnsupported, so both errors.Is checks hold.
var ErrUnsupported = fmt.Errorf("eGo: operation not supported by this runtime: %w", errors.ErrUnsupported)

// UnsupportedError names the runtime and the operation. Unwrap returns
// ErrUnsupported.
type UnsupportedError struct {
	Runtime   string // e.g. "inmem"
	Operation string // e.g. "StartProjection"
}
```

The contract rules, written into the package documentation:

1. An unsupported operation returns such an error **before any side effect**, and never panics (#148's criterion).
2. It is never used for a transient failure. A runtime that supports projections but cannot reach its store returns that store error.
3. Spawn settings are not operations, and **this design states no default** for a setting a runtime cannot honor (for example cluster placement on a single-node runtime). Whether such a setting is a no-op, as placement outside a cluster is for GoAkt today (`spawn_config.go:34`), or fails the spawn with an `*UnsupportedError` before anything is spawned, is RUNTIME-003's decision. `ErrUnsupported` is available for either answer. **Note for the maintainer:** #148's criterion already expects "cluster placement unsupported → typed error". If RUNTIME-003 chooses no-op, #148's wording needs amending; if it chooses the error, #148 stands as written.

**Why an error, not optional interfaces.** #147 and the maintainer's decision 2 put all four capabilities in the composite, and #148 expects "an explicit typed error, without panic". An error at call time needs no type assertion by the caller. **Knowing in advance** whether a runtime supports projections is RUNTIME-006, which #149 plans to model with its `Descriptor` (§8). The two answers are complementary: the descriptor says what a runtime declares; `ErrUnsupported` is what an undeclared operation returns if it is called anyway. Because every runtime implements every method, runtime capabilities cannot be inferred from the method set; §8 states that constraint for #149 and RUNTIME-006.

**What the GoAkt adapter returns.** Nothing new: `*ego.Engine` supports every operation. `ErrUnsupported` is for #148 and the S4-3 test double.

### D5 — The GoAkt adapter behind the SPI

S4-3 adds one file to package `ego`, `engine_runtime.go`:

```go
// Engine is the GoAkt implementation of the runtime SPI's application side.
var _ runtimeport.Runtime = (*Engine)(nil)
```

No `Engine` method changes. The spike compiled this assertion once the S4-2 aliases existed (§11). The assertion lives in its own file so that S4-3 does not touch `engine.go`, which #149 SPI-5, #24 fixes and S4-2 also edit (§9).

Everything else stays in package `ego` for v4, as #147 lists: `Engine` and its implementation, the exported actors, `internal/extensions`, `spawnDependency`/`LocalBehavior`, `NewEngine`, `Engine.ActorSystem`, `Config.GoaktOptions`, `ClusterKinds`, `BehaviorKind`/`EntityKind`, `loggerAdapter` and the telemetry. Their destinations at #124 are in §D10.

### D6 — `compose/goakt` accessor

```go
// Runtime returns the running application through the runtime-neutral
// contract: code written against it runs unchanged on any runtime's
// composition root. It returns nil until Start has succeeded and for good
// after a failed Start; after Stop it returns the stopped runtime, which
// refuses work with runtimeport.ErrEngineNotStarted. It is the same engine
// Engine returns.
func (a *App) Runtime() runtimeport.Runtime {
	if engine := a.published.Load(); engine != nil {
		return engine
	}
	return nil
}
```

- **Name and type.** `Runtime() runtimeport.Runtime`, the composite, because a composition root hands out the whole runtime and the consumer narrows it (`var entities runtimeport.Entities = app.Runtime()`). `compose/inmem` (#148) exposes the same method with the same type, which is what makes consumer code portable (ego-arch-003 §5.2).
- **The nil check matters.** Returning `a.published.Load()` directly would wrap a nil `*ego.Engine` in a non-nil interface, so `app.Runtime() == nil` would be false before `Start`. The first S4-4 test pins this.
- **`Engine()` stays** (`compose/goakt/app.go:234`), unchanged and not deprecated. Its fate is #124's.
- The package documentation example (`compose/goakt/app.go:42`) switches to `app.Runtime().SpawnEventSourced(ctx, behavior)`.

### D7 — The test double

`port/runtime/double_test.go` (package `runtime_test`) declares `double`, a map-backed implementation with no actor system:

- `var _ runtime.Runtime = (*double)(nil)`: the contract is implementable without GoAkt (#147 criterion).
- `SpawnEventSourced` stores the behavior, its initial state and the `SpawnSettings` it resolved; `SendCommand` runs `HandleCommand`/`HandleEvent` synchronously, the way `testkit/scenario.go` does. Everything else returns an `*UnsupportedError`.
- Tests through the double: options resolve with the documented defaults and in order; an adapter setting is visible under its key and invisible under another; a nil option is skipped; an unsupported call matches `runtime.ErrUnsupported` and `errors.ErrUnsupported` and has no side effect (the entity map is unchanged).

It is deliberately not the in-memory runtime: no mailbox, no persistence, no projections. It proves shape, not behavior. The spike built an equivalent double outside package `ego` (§11).

### D8 — End-to-end consumer

The #147 criterion asks for a consumer package that uses only `port/runtime`, `port/behavior` and contracts, and drives a real `compose/goakt.App` through the neutral accessor.

- **`internal/runtimeconsumer`** (new, root module, never released): an account behavior implementing `behavior.EventSourced` with the `test/data/testpb` messages (`Account`, `CreateAccount`, `CreditAccount`, `AccountCreated`, `AccountCredited`), and one function, `Run(ctx context.Context, r runtimeport.Entities) (*testpb.Account, error)`, that spawns the behavior, sends two commands and returns the final state.
- **Closure test**, `internal/runtimeconsumer/closure_test.go`, modeled on the publishers' `TestUnitTestClosureExcludesRuntimeAndRoot`: `go list -deps` of the package's production build contains neither `github.com/pablogore/ego/v4` nor any `github.com/tochemey/goakt/v4` package. The spike measured exactly that for an equivalent package (§11).
- **End-to-end test**, `compose/goakt/runtime_e2e_test.go` (package `goakt_test`): builds an `App` from a `compose.Spec` with `testkit` stores, starts it, calls `runtimeconsumer.Run(ctx, app.Runtime())`, checks the balance and that `EntityExists` is true, then stops the `App`.

Why a separate package and not a test file: a `_test.go` file in `compose/goakt` shares that package's closure, which contains GoAkt, so it cannot prove anything about consumer code. Why under `internal/`: it is evidence, not API. It needs no archcheck layer; its closure test is stricter than a direct-edge rule would be.

### D9 — archcheck

- `port/runtime` is a contract by location; `contract-allowlist` admits all its imports (spike: archcheck on the moved tree reported 0 violations, 0 stale, with only the pre-existing S4-1 entry baselined).
- **No new baseline entry and no exception**, in any slice.
- `port/runtime/runtime_architecture_test.go`, modeled on `port/behavior/behavior_architecture_test.go`, runs `go list -deps` on the production build and allows only `command`, `tenancy`, `eventstream`, `port/behavior`, `internal/queue`, `internal/syncmap`, `github.com/google/uuid`, `go.uber.org/atomic` and `google.golang.org/protobuf/...` (the queue, syncmap, uuid and atomic packages arrive through `eventstream`, protobuf through `command` and `port/behavior`). From S4-3 it also runs `go list -deps -test` and rejects any GoAkt package and the root package in the test closure, so the double is covered.
- `compose/goakt` importing `port/runtime` is a contract import, allowed by `composition-no-runtime`.
- `port/runtime` stays in the root module until ego-arch-006 F1 moves the root-level contracts it imports; it is not part of the approved contracts module (D7 (i)).

### D10 — GoAkt-bound symbols and their destination at #124

The inventory with `file:line` is exploration §4. #124 decides the final package names; the table records where each group goes under the layout ego-arch-006 F3 describes (a GoAkt runtime adapter package, and later module).

| Group | Symbols | Destination at #124 |
|---|---|---|
| Engine construction | `NewEngine`, `Config`, `NewConfig`, the `Config` options | GoAkt adapter (with `compose/goakt`); whether `NewEngine` and the manual path are deprecated first is #124's plan (decision 7) |
| GoAkt escape hatches | `Engine.ActorSystem`, `Config.GoaktOptions`, `ClusterKinds` | GoAkt adapter only; never in a contract (decision 7) |
| Cluster kind registration | `BehaviorKind`, `WithBehaviorKinds` | GoAkt adapter (ego-arch-002-s3 §5.5) |
| Deprecated GoAkt-typed API | `EntityKind`, `WithEntityKinds`, the five `*Behavior` interfaces, `Engine.Entity`/`DurableStateEntity`/`Saga` | Removed (ego-arch-001 §10) |
| Actors | `EventSourcedActor`, `DurableStateActor`, `SagaActor`, `ProjectionActor`, `NewProjectionActor` and their `PreStart`/`Receive`/`PostStop` | GoAkt adapter. They are exported only because `ClusterKinds` hands them to GoAkt; #124 can unexport them once `ClusterKinds` lives next to them |
| Adapter-only errors | `ErrActorSystemRequired`, `ErrActorSystemNotStarted`, `ErrMissingRequiredExtensions`, `ErrAmbiguousTenantResolver`, `ErrEntityTenantScopeMissing`, `ErrCommandReplyUnmarshalling`, `ErrDuplicatePublisherID`, `ErrBehaviorNotSerializable`, `ErrBehaviorNotPointer`, `BehaviorPlacementError` | GoAkt adapter |
| The engine and its neutral methods | `Engine` and the 14 methods of §D1 | GoAkt adapter, reached by consumers only through `runtime.Runtime` (#147's "what #124 does after") |
| Aliases added here | the §D2 table | Removed; `port/runtime` names become the only ones |
| Write-side options | `WithSnapshotInterval`, `WithRetentionPolicy`, `RetentionPolicy`, `WithBatchThreshold`, `WithBatchFlushWindow` | GoAkt adapter, unless #12 moves them into a contract first |
| Internals | `internal/extensions`, `spawnDependency`, `LocalBehavior`, `loggerAdapter` | GoAkt adapter |
| Composition root | `compose/goakt.App.Engine`, `WithCluster`, `WithActorSystemOptions` | Stay in `compose/goakt`; `Engine()` removed or retyped at #124 |
| Not GoAkt | `Telemetry` (OpenTelemetry) | #31 |

## 4. Compatibility summary

| Package | S4-2 | S4-3 | S4-4 |
|---|---|---|---|
| `port/runtime` (new) | types, options, `SpawnSettings`, sentinels, `ErrUnsupported`: additions | interfaces: additions | — |
| `ego` | aliases and wrappers; apidiff: the 31 known alias reports only (§D3) | `engine_runtime.go` (no exported change) | — |
| `compose/goakt` | — | — | `App.Runtime`: addition |
| everything else | unchanged | unchanged | `internal/runtimeconsumer` is internal |

SemVer: a minor release. No `Deprecated:` marker is added in S4 (§10, Q1). Every slice records in its pull request: apidiff for each package it touches, against the baseline; the base-API consumer program (§11) built, vetted and run against base and head with identical output; and the nested-consumer check of ego-arch-001 §5 for the slices that change exported API (S4-2, S4-4).

## 5. Test plan

Strict TDD: each slice records its RED run first. For symbols that do not exist yet, RED is the build failure ("undefined: runtimeport.ResolveSpawnOptions"), as in S3.

| Slice | RED | GREEN |
|---|---|---|
| S4-2 | `port/runtime` option tests; the `errors.Is` table in package `ego` (`runtime_compat_test.go`) referencing `runtimeport.ErrX` | Options resolve with defaults; each option reaches its getter; adapter settings round-trip; the ten sentinels match in both directions; every existing root test passes, with `spawn_config_test.go` rewritten to go through `newSpawnConfig` (it called `Apply` on the unexported config, `spawn_config_test.go:38-80`) |
| S4-3 | `double_test.go` and `engine_runtime.go`'s assertion fail to build (interfaces undefined) | Double tests (§D7); architecture test; the full root suite unchanged |
| S4-4 | `App.Runtime()` undefined; then the nil-before-`Start` test | `Runtime()` nil before `Start` and after a failed `Start`, the same engine as `Engine()` after `Start`; closure test; end-to-end test |

No `-race` locally and no workbench; CI is the race gate. The two-node test of #146 must stay green on S4-3 and S4-4.

## 6. Risks

- **Freezing the interface before a second real runtime exists.** Mitigated by the double (S4-3), by #148 as the next consumer, and by cutting no release between S4-2 and S4-3.
- **A GoAkt-shaped interface.** Placement and supervision appear only as option values, not as methods; no method mentions a node, a PID or an actor system.
- **Adapter settings as an escape hatch.** A runtime could start to depend on another adapter's keys. It cannot: the keys are unexported types of the adapter that owns them.
- **Merge conflicts** in `engine.go`, `spawn_config.go`, `saga.go`, `compose/goakt/app.go` and `CHANGELOG.md` (§9).

## 7. Interactions with #24, #12, #29, RUNTIME-003

- **#24**: no lifecycle method in the interface; `App.Runtime()` follows `App.Engine()`'s documented state (refuses work after `Stop`). The `EraseEntity`/`ProjectionLag` nil-store fix (#24 comment) keeps the signatures.
- **#12**: write-side options stay out of the contract (§D3); `Dispatch` already takes #12's `command.Envelope`.
- **#29**: an `EntityRef` handle, if added, is new methods next to the ID-based ones; the ID stays valid.
- **RUNTIME-003**: receives the placement finding of exploration §5.4 and the choice between no-op and typed error for settings a runtime cannot honor (§D4 rule 3).

**Named follow-ups for pre-existing bugs** (this change does not fix them; the maintainer opened an issue for each):

- **FU-1** ([#153](https://github.com/getsyntegrity/ego/issues/153)) `Engine.SagaStatus` never fills `SagaInfo.Status`, so it always reads `SagaRunning` (`engine.go:1638-1641`; the actor tracks it at `saga_actor.go:58`). Recorded as a known gap in the `Sagas.SagaStatus` interface doc (§D1). #148's in-memory runtime should fill it. *Fixed by [#163](https://github.com/getsyntegrity/ego/pull/163).*
- **FU-2** ([#154](https://github.com/getsyntegrity/ego/issues/154)) Relocation default: `WithRelocation`'s doc says relocatable by default (`spawn_config.go:141`), the code disables it unless `WithRelocation(true)` (`engine.go:1903-1905`). `runtime.WithRelocation` documents the actual behavior (§D3); whether the default changes is for RUNTIME-003.

## 8. Alignment with PR #149 (adapter SPI)

PR #149 is not merged; this design is written so either outcome of its open decisions leaves S4 unchanged.

| #149 element | Relation to this design |
|---|---|
| `port/adapter.Descriptor`, capability constants, one accessor per capability (§D1–§D3) | Not used. S4 declares no capability names and adds no type assertion. RUNTIME-006 (#149 F-E) can add a `runtime` port name and capability constants, and a runtime adapter can implement `adapter.Describer`, without changing any interface here |
| "Unsupported = not declared and not implemented" (§D3 table), checked by V8b in both directions (declared ⇒ implemented **and** implemented ⇒ declared) | **Constraint for #149 and RUNTIME-006.** The composite requires every method, so every runtime implements all four capability interfaces, including one that answers projections with `ErrUnsupported` (#148). Runtime capabilities therefore **cannot be defined by method set**. RUNTIME-006 must use declaration-only capabilities for the runtime port, or V8b must skip the "implemented ⇒ declared" direction for that port; otherwise a method-set check would force a runtime to declare capabilities it does not have |
| Lifecycle `Starter`/`Pinger`/`Close`, L1–L6 (§D4) | The runtime interface has no lifecycle; the provider side of the runtime follows #149's model when #11 designs it (#147, dependencies) |
| `engine.go:883` behind `tenancy.FixedTenantOf` (SPI-5) | Not touched by S4. Both edit `engine.go`; serialize (§9) |
| `compose/goakt` step 4 and `probeStores` (SPI-5) | S4-4 adds a method elsewhere in `app.go`; serialize |
| O1: adapters must not import `compose` | S4 adds no import from an adapter module |

Nothing here blocks S4-2: #149 adds no runtime port today. The constraint in row 2 is already carried into #149 at head `d928228` (its §2 and follow-up F-E).

## 9. Slices

Each slice is one pull request with at most four tasks. About 400 authored lines per slice is a planning heuristic, not a cap. All three wait for the maintainer's approval of this design.

### S4-2 — Neutral types and options

- **Owns:** `port/runtime/{doc.go,spawn.go,saga.go,errors.go}` and their tests, `port/runtime/runtime_architecture_test.go` (new); `spawn_config.go`, `spawn_config_test.go`, `supervisor.go`, `saga.go`, `engine.go` (the error `var` block, `engine.go:61-174`, only), `runtime_compat_test.go` (new), `CHANGELOG.md`.
- **Tasks:** 1. RED tests (option resolution, adapter settings including a non-comparable key panicking at build time, nil option, `ErrUnsupported`, `errors.Is` table). 2. `port/runtime` types, options, `SpawnSettings`, sentinels, doc and architecture test. 3. Aliases and wrappers in `ego`; write-side options through adapter settings; `spawn_config_test.go` rewritten; `CHANGELOG.md` entry for what lands here (the new package, the old-to-new name table, the nil-option change, `%T` names, SemVer minor). 4. Evidence in the pull request: apidiff for `ego` and `port/runtime`, consumer program on base and head, nested-consumer check, archcheck.
- **Checks:** `go test ./port/runtime/ .` (the root lane runs the full root suite for any root file); `go run ./internal/cmd/archcheck`; `golangci-lint run`; apidiff; consumer program.
- **Serialization:** `engine.go` is shared with #149 SPI-5 (line 883) and #24's fixes; `saga.go` and `spawn_config.go` with nobody known. Rebase, do not run in parallel with an open `engine.go` writer.

### S4-3 — Interfaces, adapter assertion, test double

- **Owns:** `port/runtime/runtime.go`, `port/runtime/double_test.go` (new); `engine_runtime.go` (new); `CHANGELOG.md` (one line: the interfaces).
- **Tasks:** 1. RED: double and assertion that do not build. 2. The five interfaces with their contract documentation (§D1, §D4). 3. `engine_runtime.go`. 4. Double tests and the `-test` closure in the architecture test.
- **Checks:** `go test ./port/runtime/`; root suite unchanged; archcheck; apidiff (`port/runtime` additions; `ego` unchanged).
- **Serialization:** touches no hot file. Best after #146, so its two-node test confirms the adapter unchanged.

### S4-4 — Composition accessor and end-to-end consumer

- **Owns:** `compose/goakt/app.go` (the new method and the package doc example only), `compose/goakt/app_test.go` (accessor tests), `compose/goakt/runtime_e2e_test.go` (new), `internal/runtimeconsumer/**` (new), `CHANGELOG.md`, `openspec/changes/ego-arch-001/design.md` §3, §4, §5.
- **Tasks:** 1. RED accessor tests, including nil before `Start`. 2. `App.Runtime()` and the doc example. 3. `internal/runtimeconsumer` with its closure test, and the end-to-end test. 4. `CHANGELOG.md` (extends S4-2's entry with the interfaces of S4-3 and `App.Runtime`) and ego-arch-001 §3 (`port/runtime` in the contract list), §4 (map rows), §5 (S4 done).
- **Checks:** `go test ./compose/... ./internal/runtimeconsumer/`; closure test; #146's test; archcheck; apidiff (`compose/goakt` additions).
- **Serialization:** after S4-1, which edits ego-arch-001 §2–§4. `app.go` is shared with #149 SPI-5 (different functions) and possibly #146's fixtures; `CHANGELOG.md` with IMPL-5 (PR #150) and S4-1. Best after #146.

### Order

S4-1 is independent and may land any time. S4-2 → S4-3 → S4-4, strictly. No release tag between S4-2 and S4-3. #148 starts after S4-4.

## 10. Maintainer decisions (2026-09-27) on the former open questions

Both questions below were open in the first version of this design and were decided by the maintainer on 2026-09-27 on PR #151. The options are kept for the record.

### Q1 — `Deprecated:` markers on the aliases and wrappers added here (decided)

ego-arch-001 §10 says every temporary alias is marked `Deprecated:`. The code does not do that: the S1 aliases (`publisher.go:27-39`) and the S3 aliases `SagaAction`/`SagaCommand` (`saga.go:52-61`) carry no marker, and ego-arch-002-s3 §6 says "none: aliases stay valid names until #124". Only APIs with a *different* replacement (`Engine.Entity` → `SpawnEventSourced`) were deprecated.

| Option | Consequence |
|---|---|
| (a) No marker in S4, as S1 and S3 did; #124's plan marks everything at once before the major | Consistent with the code; no staticcheck noise inside package `ego`, which uses these names everywhere. **Cost:** consumers get no staticcheck warning about the old names before #124 removes them; the `CHANGELOG.md` table and #124's migration guide are their only notice |
| (b) Mark the aliases and the `ego.With*` wrappers `Deprecated:` in S4-4 | Follows §10 literally; every internal use needs a `//nolint:staticcheck` or a switch to `runtimeport` names in `engine.go` and the actors, a large diff in hot files |

**Maintainer decision (2026-09-27, #151): (a).** No `Deprecated:` marker on the aliases of S1, S3 and S4, or on the `ego.With*` wrappers added here; they stay unmarked until #124 removes them. The accepted cost: consumers get no staticcheck warning before #124. ego-arch-001 §10 is corrected in this pull request to say so, citing this decision.

### Q2 — Adapter settings as public API (decided)

`runtime.WithAdapterSetting(key, value any)` and `SpawnSettings.AdapterSetting(key)` exist so the write-side options can stay outside the contract (decision 3) while `SpawnOption` moves.

| Option | Consequence |
|---|---|
| (a) `WithAdapterSetting`, keyed by an unexported type, as `context.WithValue` | Keeps #12 open; the contract gains a small generic door. Promoting a setting to a getter later is additive, but the door itself is public v4 API and cannot be removed inside v4 |
| (b) Put the four write-side settings in `SpawnSettings` now | No generic door, but decides #12's question here, against decision 3 |
| (c) Keep write-side options returning something other than `SpawnOption` | Incompatible: their result type is `SpawnOption` today |

**Maintainer decision (2026-09-27, #151): (a).** `runtime.WithAdapterSetting` and `SpawnSettings.AdapterSetting` are public v4 API: additive, not removable inside v4. Where the write-side options finally live stays #12's call.

## 11. Evidence and reproduction

A throwaway spike, not committed, ran on two `git archive f2b5130` copies in a scratch directory with Go 1.27.1 linux/amd64 and `GOWORK=off`.

| Claim | How it was obtained | Observed |
|---|---|---|
| The moved types and options build, and `*ego.Engine` satisfies `Runtime` unchanged | In one copy: `port/runtime` as in §D1–§D4; `spawn_config.go`, `supervisor.go`, `saga.go` and the error block as in §D2–§D3; `var _ runtimeport.Runtime = (*Engine)(nil)` appended to `engine.go`; `go build ./...` | Builds. `go vet .` fails only in `spawn_config_test.go` (six `option.Apply(config)` calls on the unexported config, lines 41–80), the one internal adjustment S4-2 plans |
| Test compilation elsewhere is unaffected | `go test -run '^$' . ./compose/... ./port/... ./migration/...` | Only the root package fails, on `spawn_config_test.go` alone |
| apidiff, package `ego` | `apidiff -w base.api github.com/pablogore/ego/v4` in the base copy, then `apidiff base.api github.com/pablogore/ego/v4` in the moved copy | 31 incompatible-change lines, all of the form "changed from X to X" for the aliased types, their constants, the `...SpawnOption`/`*SagaInfo` methods and the `With*` options (§D3); nothing else; no report for the sentinels |
| Source compatibility | A consumer module with a `replace` to each copy; `go vet` and `go run` against both | Vet clean on both; output byte-identical. The program embeds `ego.SpawnOption`, takes `ego.SpawnOption.Apply`, binds `(*ego.Engine).SpawnEventSourced`, `Entity` and `SagaStatus` to function variables of their base types, builds all nine spawn options, switches over every moved constant, calls `SagaStatus.String`, and checks `errors.Is` for the ten sentinels |
| Nested consumer | `go vet ./...` in the moved copy's `benchmark` module | Clean |
| A GoAkt-free implementation and consumer | A package in the moved copy with a map-backed `Runtime` double (`var _ runtime.Runtime = (*Double)(nil)`) and a consumer function taking `runtime.Entities`; `go build`; `go list -deps` | Builds; the closure has no `github.com/tochemey/goakt/v4` package and not the root package |
| `port/runtime` closure | `go list -deps ./port/runtime/` | Non-standard dependencies: `command`, `tenancy`, `eventstream`, `port/behavior`, `internal/queue`, `internal/syncmap`, `github.com/google/uuid`, `go.uber.org/atomic`, `google.golang.org/protobuf/...` |
| archcheck | `go run ./internal/cmd/archcheck` in the moved copy | `8 modules checked, 47 packages checked, 203 edges checked, 1 baselined, 0 violation(s), 0 stale entries` (the one baselined entry is S4-1's) |
| Every `file:line` in this document | Read on `f2b5130` | — |

## 12. Alternatives rejected

- **One large `Runtime` interface without per-capability interfaces.** Every consumer would depend on projections and sagas even when it only sends commands, and a partial runtime would look like a failed full one. Decision 2 chose small interfaces.
- **Optional capability interfaces with accessors** (`runtime.ProjectionsOf(r) (Projections, bool)`), #149's pattern. Deciding how capabilities are discovered is RUNTIME-006; doing it here would pre-empt it and #149. The composite plus `ErrUnsupported` is what #147 and #148 asked for, and accessors can be added later without a break.
- **New neutral types instead of aliases.** `ego.SagaInfo` and `runtime.SagaInfo` would be different types, so `*ego.Engine` could not implement the interface without changing its method signatures, an incompatible change.
- **Making `SpawnOption` implementable** (an exported config with setters). Anyone could invent options the runtimes do not know, and the settings would no longer be read-only.
- **Keeping `SpawnOption` in `ego` and passing `...any` in the interface.** Loses type safety for every caller and makes the interface GoAkt-shaped by convention instead of by type.
- **Renaming the sentinels** (for example `ErrNotStarted`). A second name for the same value helps nobody in v4; #124 can rename when the old names go.
- **`App.NeutralEngine()` or `App.Engine()` retyped.** Retyping `Engine()` breaks its callers; "neutral engine" names the implementation's history, not what it returns.
- **Putting the consumer under `example/`.** The examples are IMPL-5's (PR #150) and are consumer documentation; this package is evidence for a test.
- **Moving `RetentionPolicy` and the write-side options to `port/runtime`.** Decides #12's question (decision 3).
