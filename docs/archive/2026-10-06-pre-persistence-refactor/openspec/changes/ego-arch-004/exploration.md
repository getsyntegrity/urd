# Exploration — Adapter SPI, capabilities and lifecycle (EGO-ARCH-004)

| Field | Value |
|---|---|
| Change | `ego-arch-004` |
| Date | 2026-09-27 |
| Phase | `sdd-explore` |
| Tracker | [`#106`](https://github.com/getsyntegrity/ego/issues/106), parent [`#10`](https://github.com/getsyntegrity/ego/issues/10); related [`#105`](https://github.com/getsyntegrity/ego/issues/105), [`#11`](https://github.com/getsyntegrity/ego/issues/11), [`#24`](https://github.com/getsyntegrity/ego/issues/24), [`#37`](https://github.com/getsyntegrity/ego/issues/37), [`#38`](https://github.com/getsyntegrity/ego/issues/38), [`#102`](https://github.com/getsyntegrity/ego/issues/102) |
| Baseline | `main` at `f2b5130148086d95a4d6362971b85e800b0ce155` |
| Next | [`proposal.md`](./proposal.md), [`design.md`](./design.md) |

## 1. Question

Issue #106 asks for one coherent **service provider interface (SPI)** for adapters. An SPI is the set of contracts a third party implements so the framework can use its code: here, the contracts a Kafka publisher, a Postgres store or a telemetry provider implements so Ego can plug it in without changing core code. #106 wants four things from it: a minimal identity for every adapter, capabilities that are explicit and inspectable instead of discovered by scattered type assertions, a documented Start/Ready/Stop lifecycle with cleanup after a partial startup, and a reusable conformance suite with a guide for adding a new adapter.

A follow-up comment on #106 adds a fifth question. archcheck's `composition-leaf` rule only looks at packages of the root module (ego-arch-003 §D8), so a nested adapter module such as `publisher/kafka` importing `compose` is not flagged. Should adapters be allowed to depend on the composition root at all?

This exploration measures what exists on `main` before any of that is designed:

1. how each adapter family declares identity, lifecycle and optional behavior today;
2. where core code type-asserts or special-cases an adapter;
3. what #105 (the composition root, `compose` and `compose/goakt`) already owns, so #106 does not design it twice;
4. what conformance testing exists, and what the module-closure rules from #122 and #142 allow a shared suite to import;
5. what happens today when an adapter module imports `compose`.

Every file and line below was read on the baseline commit.

## 2. Adapter families today

"Adapter" here means code that binds one of Ego's contracts to a technology (ego-arch-001 §1). Six families exist:

| Family | Contract (file) | Implementations in the repository | Identity | Lifecycle methods | Owner under ego-arch-003 §D5 |
|---|---|---|---|---|---|
| Events, state and snapshot stores | `persistence.EventsStore`, `StateStore`, `SnapshotStore` (`persistence/events_store.go:80`, `state_store.go:73`, `snapshot_store.go:63`) | `testkit` in-memory stores; external stores live outside the repository | none | `Connect`, `Disconnect`, `Ping` (`events_store.go:82-84`, `:112`) | consumer |
| Offset store | `offsetstore.OffsetStore` (`offsetstore/offset_store.go:32-47`) | `testkit.OffsetStore` | none | `Connect`, `Disconnect`, `Ping` (`offset_store.go:34-38`) | consumer |
| Event and state publishers | `publishing.EventPublisher`, `StatePublisher` (`port/publishing/publishing.go:47-78`, `:82-112`) | four nested modules: `publisher/kafka`, `nats`, `pulsar`, `websocket` | `ID() string` | `Close(ctx)` only; no `Start`, no `Ping` | composition root, from `New` onward |
| Encryptor | `encryption.Encryptor` (`encryption/encryptor.go:29-36`) | `encryption/aes_encryptor.go` | none | none | consumer |
| Tenant resolver | `tenancy.TenantResolver` (`tenancy/resolver.go:39`), optional `FixedTenantResolver` (`tenancy/resolver.go:104`) | `tenancy` helpers | none | none | consumer |
| Telemetry and logger | no contract: `ego.Telemetry` is a struct of OpenTelemetry `Tracer` and `Meter` (`telemetry.go:32-37`); the logger is `kitlog.Logger` from a separate module | — | none | none | consumer |

Three facts stand out.

**Identity exists only for publishers, and it names the type, not the instance.** Each publisher's `ID()` returns a constant: `"ego-kafka"` (`publisher/kafka/kafka.go:84-86`, `:164-166`), `"ego-nats"` (`publisher/nats/nats.go:129-130`, `:245-246`), `"ego-pulsar"` (`publisher/pulsar/pulsar.go:102-103`, `:205-206`), `"ego-websocket"` (`publisher/websocket/websocket.go:74-75`, `:139-140`). The engine keys publishers by that ID and rejects a duplicate (`engine.go:1400-1409`, `duplicatePublisherIDs` at `engine.go:1358`), and `compose.Spec.Validate` rejects it too (rule V6, `compose/spec.go:216-236`). So two Kafka events publishers writing to two different topics cannot be attached to the same engine today. No other adapter has any identity at all; a store failure is named by the field it sits in (`compose/goakt/app.go:246-257`: `"ping EventsStore"`).

**Publishers start in their constructor.** `kafka.NewEventsPublisher` opens a producer and sets `started` to `true` before returning (`kafka.go:58-70`); the other three do the same (`nats.go:106`, `pulsar.go:97`, `websocket.go:60-69`, which dials the URL). There is no separate Start, so "constructed" and "running" are the same state, and `ErrPublisherNotStarted` (`port/publishing/publishing.go:42`) in practice means "closed". Close also has one inconsistency: Kafka derives a 3-second context in `Close` and throws it away (`kafka.go:76`: `_, cancel := context.WithTimeout(ctx, 3*time.Second)`), so the producer's `Close` is not bounded by the caller's deadline.

**Closing twice is not safe everywhere.** `compose/goakt` can reach a publisher's `Close` from more than one path (§4 below), so a second close must be harmless. Two publishers are not:

- The websocket publisher's `Close` sets `started` to false and closes the connection again on every call (`publisher/websocket/websocket.go:79-82`, `:144-147`). A second call closes an already closed connection and returns whatever error the connection reports.
- The NATS publisher calls `Drain` on every `Close`, then `Close` on the connection (`publisher/nats/nats.go:119-125`, `:235-241`). A second call drains an already closed connection, and `Drain` returns an error for that case, which `Close` passes on.

Kafka (`kafka.go:73-81`) and Pulsar (`pulsar.go:135-140`) also close their clients unconditionally. Whether their client libraries tolerate a second close was not verified here. The conformance suite proposed in the design (AT-3) is what would establish it.

**Stores are probed with a method that may connect them.** The store contracts document `Ping` as "verifies a connection ... is still alive, establishing a connection if necessary" (`persistence/events_store.go:109-112`). `testkit.EventStore.Ping` does exactly that: it calls `Connect` when disconnected (`testkit/eventstore.go:273-276`). ego-arch-003 §D5 says the composition root "only pings" stores and never connects them; in practice a ping may open a connection that the consumer still owns and must close. That is consistent with D5, but nothing states it.

## 3. How core code detects optional behavior today

#106's criterion is "capabilities are explicit and inspectable without scattered type assertions". This section lists every place production code (tests, mocks and generated code excluded) decides what an adapter can do.

### 3.1 Type assertions on adapter values

| Site | What it asserts | What it decides |
|---|---|---|
| `engine.go:883` | `engine.tenantResolver.(tenancy.FixedTenantResolver)` | whether a spawn without `WithTenant` can take the resolver's fixed tenant; otherwise the spawn fails with `ErrSpawnTenantUndetermined` (`engine.go:874-890`) |
| `logger.go:125` | `backend.(kitlog.CallerSkipper)` | whether the logger can skip one caller frame |
| `logger.go:334` | `a.backend.(kitlog.ManagedLogger)` | whether GoAkt's `Flush` drains a buffered logger or falls back to `Sync` |
| `extension_lookup.go:59`, `:93` | `ext.(T)` on GoAkt extensions | whether the registered extension has the expected type; internal to the GoAkt adapter, hardened to return an error instead of panicking (#100) |

Two related assertions exist on behaviors rather than adapters: `event_sourced_actor.go:773` and `durable_state_actor.go:502` check `behaviorport.EventSourcedEnvelope` / `DurableStateEnvelope`, the optional envelope interfaces `port/behavior` declares (ego-arch-002-s3 §5.1). They follow the same pattern: the optional interface is declared in the contract package, and the runtime asserts it where it needs it.

One kind of site is deliberately left out of this inventory: `errors.As(err, &conflictErr)` on `*persistence.ConflictError` (`event_sourced_actor.go:832`, `durable_state_actor.go:488`). That classifies an *error* a store returned, following the store contract's documented error type (`persistence/events_store.go:105-107`). It does not ask what the store *can do*, so it is error classification, not capability detection, and the SPI leaves it alone.

The pattern is consistent: an optional interface lives in the contract package, and whoever needs it asserts it at the point of use. The problem is not that assertions exist but that the answer to "what can this adapter do?" is spread across call sites, is invisible before the first call, and cannot be checked when the dependency graph is built.

### 3.2 Nil checks standing in for capabilities

Optional *adapters* (as opposed to optional *capabilities* of one adapter) are detected by comparing a field to `nil`:

- `option.go:143-184` registers a GoAkt extension only for the adapters that are set (`stateStore`, `offsetStore`, `snapshotStore`, `telemetry`, `encryptor`, `tenantResolver`), and `validateActorSystemExtensions` mirrors the same list (`engine.go:379-408`).
- `event_sourced_actor.go:514`, `:544`, `:563` guard snapshotting with `entity.snapshotStore != nil`; `:732` and `:1202` guard encryption with `entity.encryptor`; `snapshots_writer_actor.go:137`, `:143`, `events_janitor_actor.go:134` and `projection_runner.go:683` do the same.
- `engine.go:429`, `:1150` check `engine.telemetry`; `engine.go:875`, `:1232`, `:1610`, `:1670` check `engine.tenantResolver`.

These are presence checks for optional slots, not capabilities, and `compose.Spec` already validates them statically (V1–V7, `compose/spec.go:149-210`), including typed nils (V5, `spec.go:241-249`). They are not a problem #106 has to solve.

### 3.3 Special-casing in the composition root

`compose/goakt` probes stores through a local structural interface, `type pinger interface{ Ping(context.Context) error }` (`compose/goakt/app.go:241`), and attaches publishers by kind (`app.go:355-369`). It names each store by its `Spec` field, not by what the adapter is. Nothing in `compose/goakt` asserts a concrete adapter type; the special cases are by slot, which is correct, and by a private interface that should become a public one once an SPI exists.

## 4. What #105 already owns

#105 (ADR ego-arch-003) is closed as far as the GoAkt composition goes, and its code is on `main` (`compose/`, `compose/goakt/`, `compose/internal/lifecycle/`). #106 must build on it, not beside it.

| Concern | Owned by #105 | Where |
|---|---|---|
| Which adapters a deployment uses, by slot | `compose.Spec` fields | `compose/spec.go:83-125` |
| Static validation of the graph | V1–V7, joined errors, `*ValidationError{Rule, Field, Problem}` | `compose/spec.go:149-210`, `compose/errors.go:30-50` |
| Who owns, starts and closes each dependency | D5 table: stores, encryptor, resolver, telemetry and logger stay the consumer's; publishers transfer to the `App` when `New` succeeds | ego-arch-003 §D5; `compose/goakt/app.go:48-54` |
| Start order and rollback | five steps (probe stores, actor system, engine, attach publishers, projections), reverse undo, release of unattached publishers, `*StartError{Step, Err, Rollback}` | ego-arch-003 §D6; `compose/goakt/app.go:170-176`, `:406-425`; `compose/errors.go:57-87` |
| Stop order | projections, engine (closes publishers and event stream), actor system; every step attempted | ego-arch-003 §D7; `compose/goakt/app.go:226-228` |
| Cleanup context | `context.WithoutCancel` bounded by `ShutdownTimeout` (default 30 s, still open) | `compose/internal/lifecycle/lifecycle.go:46-49`, `:272-293` |
| The step contract a component must honor | "When it fails, it must release whatever it acquired itself: the sequence never calls Stop on the step that failed" | `compose/internal/lifecycle/lifecycle.go:60-63` |

ego-arch-003 left two questions for #106: whether `compose/internal/lifecycle` becomes public once an adapter lifecycle is defined (ego-arch-003 §9, last row), and the adapter SPI itself (ego-arch-003 proposal, out of scope). It also left one question to #24: the drain and flush policy for work emitted during shutdown (§D7, §9), which this change must not decide.

## 5. Conformance testing today

Two kinds exist, both per family.

- **Stores.** `persistence/conformance` is a store-agnostic isolation suite (EGO-TENANT-003): `RunEventsStoreConformance`, `RunStateStoreConformance`, `RunSnapshotStoreConformance` take a factory that returns a fresh store (`persistence/conformance/doc.go:31-50`). It manages `Connect`/`Disconnect` around each check through a small structural `Lifecycle` interface (`persistence/conformance/check.go:39-42`), and **skips** a check when `Connect` fails, so a CI without a database is never counted as a pass (`check.go:79-81`). `testkit` runs it (`testkit/conformance_test.go:47-59`). It imports `testify`, which is why archcheck carves it out of the contract layer as test support (`internal/cmd/archcheck/rules/layers.go:62-65`).
- **Publishers.** Each module has a hand-written `publisher_contract_test.go` with two compile-time assertions and one test that `Publish` after close returns `publishing.ErrPublisherNotStarted` (`publisher/kafka/publisher_contract_test.go:43-68`). The four copies are identical in shape.

Nothing tests lifecycle behavior shared by all adapters: that `Close` is idempotent, that it is safe before `Start` or after a failed `Start`, or that it respects the caller's deadline.

**The closure rule any shared suite must respect.** #122 and #142 made each publisher's *test* closure free of the runtime: `TestUnitTestClosureExcludesRuntimeAndRoot` runs `go list -deps -test ./...` and fails if any `github.com/tochemey/goakt/v4` package or the root package `github.com/pablogore/ego/v4` appears (`publisher/kafka/closure_test.go:59-75`). The alias checks that needed package `ego` moved to the unreleased `test/compat` module (#142). A suite a publisher imports in its tests may therefore depend only on the standard library and contract packages. It also cannot use `testify` if it lives under `port/`, because `contract-allowlist` applies to everything under `port/` (`internal/cmd/archcheck/rules/layers.go:45-55`, `rules.go:139-153`).

**The module rule that comes next.** ego-arch-006 slice S3 will make the publishers require only a new contracts module holding `egopb` and `port/publishing` (decision D7 (i), approved 2026-09-27), and no longer the root module. From then on a publisher can import only what that module contains. A shared SPI or suite that publishers import would have to live there too, which D7 (i) as approved does not include.

## 6. What happens when an adapter imports the composition root

The follow-up comment on #106 is correct, and the gap is larger than a missing rule. A throwaway spike on an exported copy of the baseline (never committed; Go 1.27.1 linux/amd64, `GOWORK=off`) added one file to `publisher/kafka`:

| Change to `publisher/kafka` | `go run ./internal/cmd/archcheck` | Production closure (`go list -deps ./...`) |
|---|---|---|
| none (baseline) | 0 violations | 299 packages, 0 GoAkt |
| imports `compose` and `compose/goakt` (then `go mod tidy`) | **0 violations** (8 modules, 45 packages, 194 edges checked) | 608 packages, **45 GoAkt** |
| imports `compose` only (then `go mod tidy`) | **0 violations** | 306 packages, 0 GoAkt; adds `persistence`, `offsetstore`, `tenancy`, `projection`, `encryption`, `eventadapter` and `compose` from the root module |

Why archcheck passes: `composition-leaf` matches only root-module packages (`layers.go:193`: `pkg.Kind != RootModule`), and `external-adapter-no-runtime` denies only the root package `ego` and GoAkt as *direct* imports (`rules.go:167-185`); the publisher imports neither directly. The build only asked for `go mod tidy`, because the publisher already requires the root module (`publisher/kafka/go.mod:7`, `:50`).

Three consequences follow:

1. Importing `compose/goakt` puts the whole GoAkt runtime back into an adapter's production build, undoing S1b (#121) and the goal of #122.
2. Importing the neutral `compose` alone keeps GoAkt out today, but it pulls six root-level contracts that ego-arch-006 keeps in the root module until F1. After ego-arch-006 S3 the publisher would have to require the root module again, and with it GoAkt and Olric in its module graph.
3. The dependency direction is backwards: ego-arch-001 §3 says an external adapter "MAY import only contract packages and `egopb`", but notes that archcheck enforces only the runtime half and leaves the rest to review. `compose` is the composition root, not a contract.

`no-cross-module-internal` already catches one corner: an adapter importing `compose/internal/lifecycle` is flagged, and Go's own `internal` rule rejects it too, because `publisher/...` is not under `compose/`.

## 7. Findings to carry into the proposal

1. The SPI has to be additive in v4. Adding a method to an existing interface such as `publishing.EventPublisher` would break every implementation outside the repository, and ego-arch-001 §10 decided "no break inside v4". Capabilities must therefore be *optional* interfaces plus an inspectable declaration, not new required methods.
2. The existing pattern (optional interface in the contract package, asserted by the user) is sound; what is missing is a single place to inspect it, a declaration that composition can validate before anything starts, and conformance tests that keep the declaration honest.
3. Identity has to separate "what type of adapter is this" (for errors, inspection and capability checks) from "which instance is this" (today's publisher `ID()`), because today's IDs collide by type.
4. Lifecycle already has a composition-level contract (#105). What is missing is the adapter-level half: what `Start`, `Close` and `Ping` must guarantee so that rollback actually releases resources.
5. A shared conformance suite must be standard-library-only and live beside the contracts it tests, so publishers can run it without the root package, GoAkt or `testify`.
6. Adapters should not depend on the composition root; enforcing that needs its own archcheck rule on the adapter layer, because `composition-leaf` is scoped to the root module by design.
7. Placing new SPI packages where publishers can import them after ego-arch-006 S3 touches an approved decision (D7 (i)). That was a decision for the maintainers, not something this change could settle; they made it on 2026-09-27 (O2, design §9).
