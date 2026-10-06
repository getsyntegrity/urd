# Proposal — Runtime SPI, application side: `port/runtime` (EGO-RUNTIME-001/002, slice S4)

| Field | Value |
|---|---|
| Change | `ego-runtime-001` |
| Date | 2026-09-27 |
| Phase | `sdd-propose` |
| Tracker | [`#147`](https://github.com/getsyntegrity/ego/issues/147) (this is slice S4-D), parent [`#11`](https://github.com/getsyntegrity/ego/issues/11), foundation [`#10`](https://github.com/getsyntegrity/ego/issues/10) |
| Baseline | `main` at `f2b5130148086d95a4d6362971b85e800b0ce155` |
| Evidence | [`exploration.md`](./exploration.md); [`design.md`](./design.md) §11 (spike) |
| Builds on | ego-arch-001 §2, §3, §10; ego-arch-002-s3 §5, §6; ego-arch-003 §5.2, §6 (IMPL-6) |
| Human gate | Maintainer approval of this design before S4-2 (#147, slice table) |
| Related | [`#148`](https://github.com/getsyntegrity/ego/issues/148) in-memory runtime and `compose/inmem`; [`#106`](https://github.com/getsyntegrity/ego/issues/106) / PR [`#149`](https://github.com/getsyntegrity/ego/pull/149) adapter SPI; [`#124`](https://github.com/getsyntegrity/ego/issues/124) final layout; [`#12`](https://github.com/getsyntegrity/ego/issues/12) write side; [`#29`](https://github.com/getsyntegrity/ego/issues/29) distributed semantics; [`#24`](https://github.com/getsyntegrity/ego/issues/24) lifecycle; [`#146`](https://github.com/getsyntegrity/ego/issues/146) two-node test |

## Why

#105's last criterion, "one GoAkt composition and one in-memory composition without changing the domain", is blocked by three things (ego-arch-003 §5.2). The first, GoAkt-free behavior contracts, is done (`port/behavior`, #123). The second, an in-memory runtime, is #148. The third is this change: there is no interface for what consumer code does with a running Ego. Consumer code calls methods on `*ego.Engine`, a struct that holds a GoAkt actor system, so a consumer written against `compose/goakt` is GoAkt code and cannot run on anything else.

The fix looks small, an interface over the methods consumers already call, but the methods mention five types that live in package `ego`: `SpawnOption`, `SagaInfo` and `SagaStatus` directly, and `EntitiesPlacement` and `SupervisorDirective` through the spawn options. A contract package cannot import package `ego` (ego-arch-001 §3). One of those types, `SpawnOption`, is also sealed: its only method takes an unexported parameter type (`spawn_config.go:111-114`), so no other runtime could read an option even if it could name the type (exploration §3.1).

## What changes

1. **A new contract package, `port/runtime`**, with four small interfaces, one per capability, and one composite:
   - `Entities`: spawn event-sourced and durable-state entities, check existence, send and dispatch commands, erase.
   - `Sagas`: spawn a saga and read its status.
   - `Projections`: start, stop, inspect, rebuild and measure lag.
   - `Events`: subscribe to the event stream.
   - `Runtime`: all four together.

   An entity is referenced by its `string` ID and invoked with `SendCommand` or with `Dispatch` and `command.Envelope`/`command.Result` (maintainer decision 1).
2. **The types those interfaces need move to `port/runtime`**, and package `ego` keeps them as aliases, so `ego.SagaInfo` and `runtime.SagaInfo` are the same type and `errors.Is` matches either name. The neutral sentinel errors move the same way.
3. **`SpawnOption` becomes readable by any runtime** through a read-only `SpawnSettings` value that `runtime.ResolveSpawnOptions` returns. The option stays sealed (only `port/runtime` builds options) but is no longer private to the GoAkt adapter. The write-side options (`WithSnapshotInterval`, `WithRetentionPolicy`, `WithBatchThreshold`, `WithBatchFlushWindow`) stay in package `ego` and travel as adapter-specific settings, so this change does not decide #12's question.
4. **The GoAkt adapter implements the contract.** `*ego.Engine` satisfies `runtime.Runtime` with a compile-time assertion and no behavior change; `compose/goakt.App` gains `Runtime() runtime.Runtime`, next to the existing `Engine()`.
5. **Proof.** A GoAkt-free test double implements the interface, and a small consumer package, whose production dependency closure contains neither GoAkt nor package `ego`, spawns a behavior and sends it commands through `App.Runtime()`.

A capability a runtime does not have is reported with an error that matches `runtime.ErrUnsupported`, never a panic. How a caller learns in advance what a runtime supports is RUNTIME-006, and it will use #149's descriptor model.

## Why this shape

- **Small interfaces per capability** (maintainer decision 2) let consumer code ask for exactly what it uses (`func handle(r runtime.Entities)`) and keep a runtime without projections from being shaped like GoAkt.
- **Aliases instead of new types** are the pattern S1 (`port/publishing`) and S3 (`SagaAction`, `SagaCommand`) already used. Callers see no change, and `errors.Is` keeps matching because each old name is the very same value.
- **A resolved read-only struct** (decision 3) lets another runtime read options without letting it, or a consumer, write to the GoAkt adapter's private state.
- **No move of the engine itself.** The physical extraction belongs to #124 (decision 5). S4 only puts an interface in front of it.

## Compatibility

Additive in v4 (ego-arch-001 §10). A spike on the baseline (design §11) moved every type in this proposal and:

- built the root module, `benchmark`, `compose/...` and `migration`;
- compiled `var _ runtime.Runtime = (*ego.Engine)(nil)` with no change to any `Engine` method;
- ran a consumer program written against the baseline API against both trees. The program embeds `ego.SpawnOption`, takes the method expression `ego.SpawnOption.Apply`, binds `(*ego.Engine).SpawnEventSourced` and `SagaStatus` to function variables of their old types, switches over every moved constant, and checks `errors.Is` for every moved sentinel. It vetted cleanly and printed identical output on both;
- got from apidiff only the "changed from X to X" reports that cross-package aliases always produce (ego-arch-002-s3 §6), and nothing else.

The one internal adjustment is `spawn_config_test.go`, which calls `Apply` with the unexported config and must go through `newSpawnConfig` instead. `reflect`-based type names change (`%T` of a `SagaInfo` prints `runtime.SagaInfo`), as they did for S3; `CHANGELOG.md` records it.

## Slices

| Slice | Content | Gate |
|---|---|---|
| S4-1 | Remove the `migration -> ego` archcheck baseline entry (`internal/logging`) | In progress separately; not designed here |
| **S4-2** | `port/runtime` types, options, `SpawnSettings`, sentinels; aliases in `ego`; `CHANGELOG.md` entry for them | This design approved |
| **S4-3** | `port/runtime` interfaces; `*ego.Engine` assertion; GoAkt-free test double | S4-2; best after #146 |
| **S4-4** | `compose/goakt.App.Runtime()`; end-to-end consumer; `CHANGELOG.md` addition; ego-arch-001 §3–§5 | S4-3; after S4-1 (shared ADR sections); best after #146 |

Each slice has at most four tasks (design §9). No release tag is cut between S4-2 and S4-3, so the types never ship without the interface that justifies them (#147, risks).

## Out of scope

The in-memory runtime and `compose/inmem` (#148); moving the engine, the actors and `internal/extensions` (#124, ego-arch-006 F3); documented placement, supervision and passivation semantics (RUNTIME-003); capability negotiation (RUNTIME-006, #149 F-E); the conformance suite (RUNTIME-007); drain and shutdown policy (#24); the write-side option redesign (#12); an `EntityRef` handle (#29, #12).

## Maintainer decisions (2026-09-27)

The two questions this proposal left open were decided by the maintainer on PR #151 (design §2 and §10):

- **Q1:** no `Deprecated:` markers on the aliases (S1, S3 and the new S4 aliases) or on the `ego.With*` wrappers; they stay unmarked until #124 removes them, accepting that consumers get no staticcheck warning before then. This pull request corrects ego-arch-001 §10 to match.
- **Q2:** `runtime.WithAdapterSetting` is public v4 API, additive and not removable inside v4. Where the write-side options finally live stays #12's call.

## Rollback

S4-2 and S4-3 are additive and can be reverted until the first release that contains them. After that, `port/runtime` is public v4 API, and a rollback may revert callers but must not delete the package (the rule `port/publishing` and `port/behavior` follow).
