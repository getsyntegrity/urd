# Proposal — Composition root and dependency injection model (EGO-ARCH-003)

| Field | Value |
|---|---|
| Change | `ego-arch-003` |
| Date | 2026-09-26 |
| Phase | `sdd-propose` — proposed (architecture decision record; ADR) |
| Tracker | [`#105`](https://github.com/getsyntegrity/ego/issues/105), parent [`#10`](https://github.com/getsyntegrity/ego/issues/10) |
| Baseline | `main` at `77beda6` |
| Evidence | [`design.md`](./design.md) (composition-root walkthroughs, decisions D1–D8, diagrams, slices) |
| Blocked by (in-memory composition only) | [`#123`](https://github.com/getsyntegrity/ego/issues/123) neutral behavior contracts (S3); an in-memory runtime; a runtime-neutral engine API from [`#11`](https://github.com/getsyntegrity/ego/issues/11) |
| Independent bugfix | [`#126`](https://github.com/getsyntegrity/ego/issues/126) (IMPL-1): `Engine.Stop` leak, `Entity`/`Saga` panic without events store, duplicate publisher IDs |
| Related | [`#104`](https://github.com/getsyntegrity/ego/issues/104) ADR ego-arch-001 (hands this decision to #105); [`#103`](https://github.com/getsyntegrity/ego/issues/103) neutral behavior contracts (closed, partially delivered); [`#11`](https://github.com/getsyntegrity/ego/issues/11) runtime SPI epic; [`#106`](https://github.com/getsyntegrity/ego/issues/106) adapter SPI/capabilities; [`#24`](https://github.com/getsyntegrity/ego/issues/24) lifecycle epic; [`#35`](https://github.com/getsyntegrity/ego/issues/35) typed config; [`#31`](https://github.com/getsyntegrity/ego/issues/31) observability |

## Why now

Ego has no composition root today. A **composition root** is the one place in a program that constructs concrete adapters (stores, publishers, the actor system) and wires them into the application's contracts; everywhere else should receive only what it needs already built. In Ego, that place does not exist as a package — it is copy-pasted into every consumer's `main()` and into fifteen call sites in `engine_test.go`. `option.go:92` (`NewConfig`), `option.go:118` (`GoaktOptions`), `engine.go:221` (`NewEngine`), `engine.go:333` (`Start`) and `engine.go:368` (`Stop`) are always called in the same order, but nothing enforces that order or checks that the resulting graph is usable before the first command is sent.

`openspec/changes/ego-arch-001/design.md` (ADR ego-arch-001, §4.1) already reached this conclusion while classifying every package's layer, and explicitly deferred the decision to this issue: *"Where the composition root should live, how it validates the graph and who owns Start/Stop ordering are decided by #105."* This proposal closes that decision.

Two concrete defects motivate acting now, both confirmed on `main` at `77beda6`:

1. **Invalid graphs fail late, not at construction.** `NewConfig(nil, ...)` (no events store) followed by `GoaktOptions()` and `NewEngine` succeeds silently — `option.go:130-133` registers the `EventsStore` extension even when it wraps a nil interface. The first `EventSourcedBehavior` spawn then calls `entity.eventsStore.Ping(ctx.Context())` with no nil guard (`event_sourced_actor.go:489`), which panics on a genuinely nil interface value inside `PreStart`, which GoAkt drives through a `singleflight.Group`. As `extension_lookup.go:31-49` documents (the reason for `#100`), singleflight re-panics it on a fresh, unrecoverable goroutine, so it crashes the whole process instead of failing the one spawn. A projection registered without an offset store fails more gracefully, but still only at `StartProjection`'s actor spawn (`projection_actor.go:76`), not at construction.
2. **Shutdown has no rollback and can leak resources.** `Engine.Stop` (`engine.go:368-403`) sets `started` to `false` before doing any cleanup, then returns immediately if the first publisher's `Close` fails (`engine.go:379-381`) — every later publisher, the event stream and the actor-system reference are left in place with no retry path, because the early return skips them. Every root-module example makes this worse: `example/eventssourced/main.go:75` and `example/durablestate/main.go:74` call `_ = engine.Start(ctx)`, discarding the one error `Start` can return, and all four examples call `os.Exit(1)` on a `NewEngine` failure without stopping the already-started actor system (`example/eventssourced/main.go:63-72`).

## Intent

Give Ego one explicit, documented composition root per runtime, built from a runtime-neutral description of what to wire, so that an invalid dependency graph fails at construction with a clear error, startup and shutdown happen in a deterministic order with rollback on partial failure, and the same domain code can be composed against more than one runtime without becoming a covert service locator. This is an architecture decision: it designs the composition root and states the decisions needed to build it; it does not by itself deliver the in-memory runtime that the second composition depends on.

## In scope (decisions this proposal closes)

- **Location and package split**: a runtime-neutral `compose` package (a plain `Spec` struct and its `Validate` method) plus a GoAkt-specific `compose/goakt` package, with the sequencing logic that both compositions share kept in an unexported `compose/internal/lifecycle`.
- **The dependency-injection model**: explicit constructor injection at the root only, with a closed, checkable list of what is forbidden (exported registries, `Resolve`/`Get`-by-type functions, reflection-based wiring, passing `Spec` or the running app into contracts).
- **Static and probe-time validation** of the dependency graph, replacing today's silent acceptance of a nil events store or an unpaired projection.
- **Deterministic Start/Stop ordering with rollback** on partial startup failure, and best-effort, fully-attempted shutdown instead of `Stop`'s current early return.
- **Ownership of every dependency** (who constructs it, who connects/disconnects it, who closes it), so the composition root never silently takes over a resource the consumer owns.
- **Two architecture-check rules (`archcheck`)**: `composition-no-runtime` keeps the runtime out of `compose`, and `composition-leaf` keeps `compose` out of every production package except `main` packages and `compose/...` itself.
- **A vertical-slice implementation plan**, ordered so each slice is independently reviewable, and a table mapping every #105 acceptance criterion to a slice and a concrete check.

## Out of scope (MUST NOT in this change)

- Any production code. This proposal and its design record decisions; implementation happens in the IMPL-1 through IMPL-6 slices in `design.md`, each its own future pull request.
- The runtime service provider interface (SPI) itself, owned by `#11`; this change only states that the composition root is where a runtime gets selected.
- The adapter SPI, capability negotiation and lifecycle hooks for adapters, owned by `#106`, which depends on this change.
- Typed configuration and environment/file binding, owned by `#35`. `compose.Spec` is an in-code shape; binding it from configuration is a separate concern.
- The flush/drain policy for events or state emitted while an actor system shuts down, owned by `#24` (LIFE-004), and an engine-level switch that stops admitting commands before projections stop (`#24`, LIFE-003); this design records both gaps and defers them.
- Adding runtime-neutral contracts in `port/behavior` (and new `Engine.Spawn*` methods) so a behavior no longer has to satisfy `extension.Dependency` to run — that is `#123` (S3); the existing `ego` behavior interfaces keep embedding `extension.Dependency`, deprecated, until the major release `#124` introduces. This is a prerequisite this change is blocked by for its in-memory half, not a task it performs.
- Building the in-memory runtime or test double itself, and the runtime-neutral engine API consumers would call on both runtimes. Both belong to `#11` (see Dependencies).

## Approach

Two composition roots are designed against the same runtime-neutral `compose.Spec`: `compose/goakt`, which can be built today, and `compose/inmem`, which cannot yet. `compose/goakt.New(spec, opts...)` runs `Spec.Validate()` and GoAkt-specific static checks with no I/O and nothing started; `App.Start(ctx)` then probes every configured store with `Ping` and brings up the actor system, the engine, the publishers and the declared projections in a fixed order, undoing each completed step if a later one fails; `App.Stop(ctx)` reverses that order, attempting every step even if an earlier one errors, and is idempotent.

### State of #103 on main

`#105`'s acceptance criteria call for "at least one GoAkt composition and one in-memory composition without changing the domain." The in-memory half depends on work `#103` was supposed to deliver. `#103` is closed, but only its first slice shipped; the table below is the verified state on `main` at `77beda6`, re-derived from source rather than from the issue's checkboxes.

| # | `#103` criterion | Status on `main` | Evidence |
|---|---|---|---|
| 1 | Core CQRS/ES compiles without importing `github.com/tochemey/goakt` | Not implemented | Package `ego` is the CQRS/ES core and imports GoAkt throughout (`option.go`, `engine.go`, `behavior.go`, `saga.go`, the actor files). Deliberate: `design.md` (ego-arch-001) §3 classifies `ego` as the GoAkt runtime adapter, which MAY import GoAkt. |
| 2 | No public core API exposes GoAkt types | Not implemented | `behavior.go:48` and `behavior.go:99-100` embed `extension.Dependency` in `EventSourcedBehavior`/`DurableStateBehavior`; `saga.go:41-42` does the same for `SagaBehavior`; `option.go:338` aliases `EntityKind` directly to `extension.Dependency`. |
| 3 | GoAkt adapter implements the defined neutral contracts | Not applicable | There is no neutral contract to implement against, because the contracts themselves are GoAkt-coupled (row 2). |
| 4 | An in-memory runtime/test double runs minimal conformance | Not implemented | Only in-memory **stores** exist (`testkit/eventstore.go` and siblings). No in-memory actor runtime exists; `testkit/scenario.go:40-54` runs `HandleCommand`/`HandleEvent` directly with no actor system at all, which is not a runtime. |
| 5 | Tests prove the same domain works on both runtimes | Not implemented | Follows from row 4: there is no second runtime to compare against. |
| 6 | Breaking changes carry SemVer/migration notes | Partial | `CHANGELOG.md:147-151` documents this well for the `port/publishing` extraction that did ship; no entry exists for behavior-contract changes, because none have shipped. |
| 7 | No mega-interfaces | Satisfied for what exists | `EventSourcedBehavior`, `DurableStateBehavior` and `SagaBehavior` stay small and single-purpose. |

What actually shipped under `#103`'s number was the `port/publishing` extraction (S1a/S1b, `#116`/`#121`), which answers none of the seven criteria above — it only moved `EventPublisher`, `StatePublisher` and `ErrPublisherNotStarted` out of package `ego`. `odd/tasks/port-publishing.md:63-72` records that the remaining scope was split into two follow-up specs, `neutral-behavior-contracts` and `inmemory-runtime-conformance`, neither of which was ever started; epic `#10` still lists `#103` unchecked for that reason. `#123`, opened alongside this proposal, is the minimal slice of `neutral-behavior-contracts` needed here: it adds runtime-neutral contracts in a new `port/behavior` package (`EventSourced`, `DurableState`, `Saga`) and new `Engine.SpawnEventSourced`/`SpawnDurableState`/`SpawnSaga` methods that accept them, while the existing `ego.EventSourcedBehavior`/`DurableStateBehavior`/`SagaBehavior` names keep embedding `extension.Dependency` — deprecated — until the major release `#124` introduces, per the "no break in v4" decision. It does not by itself supply an in-memory actor runtime — that is a second, currently unowned prerequisite (see Open decisions).

**Consequence for this proposal:** `compose/inmem` is designed at the same level of detail as `compose/goakt` in `design.md`, using the same `compose.Spec`, the same `Spec.Validate`, and the same `compose/internal/lifecycle` sequencer, so that only the runtime step differs. It cannot be implemented until three things exist: `#123`'s neutral behavior contracts, an in-memory runtime, and a runtime-neutral engine API, because consumer code written against `App.Engine()` returns `*ego.Engine` today (`design.md` §5.2). It is recorded as slice IMPL-6, blocked. The design alone does not satisfy `#105`'s in-memory criterion: `#105` stays open until IMPL-6 runs the same behavior on both compositions, unless maintainers split that criterion into its own issue.

## Dependencies and sequencing

- **`#123`** must land before `compose/inmem` can be implemented; it does not block `compose/goakt`, `compose.Spec`, or `compose/internal/lifecycle` architecturally, none of which touch behavior contracts. Two of its slices, however, land before IMPL-4 specifically: S3-2 (the spawn-site bridge) renames `Engine.Entity`/`Engine.DurableStateEntity`/`Engine.Saga`'s bodies in place into unexported spawn functions at the same `engine.go` call sites IMPL-4's declared-entity-family guard touches, and S3-4 (kind registration) adds `ego.BehaviorKind`/`WithBehaviorKinds` in `option.go`, which IMPL-4's `compose/goakt.WithCluster` needs (`design.md` §5.2, §6): `#123`'s S3-2 and S3-4 must both land first, and IMPL-4 rebases onto them. `ego-arch-002-s3`'s own design (§9) orders S3-4 after S3-2 *and* S3-3 — S3-3 owns the shared test file S3-4 adds a subtest to — so IMPL-4 transitively also waits on S3-3.
- **The in-memory runtime prerequisite** (a runtime, not just stores, that can run an `EventSourcedBehavior` without GoAkt) also blocks `compose/inmem`. Recommended owner, per the #125 review: a child issue under `#11` for its `RUNTIME-005` ("deterministic in-memory runtime") rather than a reopened `#103`, which is about contracts. The issue is not created yet; it belongs to the breakdown of `#11`.
- **A runtime-neutral engine API** also blocks `compose/inmem`: consumers call `Entity`/`SendCommand` on `*ego.Engine`, a GoAkt-backed type. The interface both runtimes implement is the application-facing side of `#11`'s runtime SPI (`RUNTIME-001`/`RUNTIME-002`).
- **`#126`** (IMPL-1) is independent of everything else here and can land first.
- **`#11`** owns the runtime SPI. This design's criterion "the composition root selects the runtime explicitly" is satisfied by having two separate `compose/<runtime>` packages rather than one generic constructor; `#11`'s SPI shape does not need to exist first.
- **`#106`** (adapter SPI, capabilities, lifecycle hooks) depends on this change; it is not a prerequisite for it.
- **`#24`** owns the drain/flush policy referenced in D7 (design.md); this proposal records the open question but does not resolve it here.

## Affected public consumer surfaces

This change adds new packages; it does not change any existing signature except one bugfix:

- New: `compose.Spec`, `compose.Family`, `compose.Spec.Validate`, `compose.StartError`; `compose/goakt.New`, `compose/goakt.App` and its options (including `WithCluster(cfg, kinds ...ego.BehaviorKind)`, built on `#123`'s S3-4 `ego.BehaviorKind`; the deprecated `EntityKind`/`WithEntityKinds` shape remains available on the manual path).
- Two additive `ego` options (IMPL-4): one through which `compose/goakt` tells the engine which entity families were declared, and `WithEventStream`, so the composition root allocates the event stream it owns instead of `NewConfig` allocating it internally. No existing signature changes.
- Bugfix (IMPL-1, `#126`, independent of the rest): `Engine.Entity` and `Engine.Saga` return a typed `ErrEventsStoreRequired` instead of panicking when no events store is configured — today `Engine.Entity` (`engine.go:645`) has no such guard, unlike `Engine.DurableStateEntity` (`engine.go:902`), which already returns `ErrDurableStateStoreRequired`. `Engine.Stop` attempts every shutdown step and joins errors instead of returning on the first failure. `AddEventPublishers`/`AddStatePublishers` reject a duplicate publisher ID, which today silently orphans the first publisher's goroutine (`engine.go:1228`, `engine.go:1269`). No signature changes.
- The existing manual composition path (`NewConfig`, `GoaktOptions`, `goakt.NewActorSystem`, `NewEngine`, `AddEventPublishers`) is unchanged and stays supported as the "advanced/manual composition" path for v4. `compose/goakt` is additive.

## Rollback

This change is documentation only; rolling it back means reverting the files in `openspec/changes/ego-arch-003/`. For the implementation slices once they land: `compose` and `compose/goakt` are new, additive packages with no existing consumer, so any slice can be reverted up to the point of a release with no compatibility obligation. The IMPL-1 bugfix changes a panic into a typed error, makes `Stop` attempt more work than before, and turns a silent duplicate-ID leak into an error; all three are backward compatible for callers that never hit those paths, and revertible independently of the rest.

## Risks

- **A second composition root could re-introduce the coupling it removes** if `compose/goakt` reached into `internal/extensions` types directly instead of going through the same contracts consumers already use. Mitigation: D2 in `design.md` keeps the GoAkt extension registry an internal detail of the GoAkt adapter, never passed through `compose.Spec`.
- **Rollback ordering is easy to get wrong** (undoing steps in the wrong order, or not at all, on partial failure). Mitigation: `compose/internal/lifecycle` is designed and tested in isolation (IMPL-3) with fake steps before `compose/goakt` uses it.
- **Events or state emitted while the actor system shuts down could be silently dropped**, because publishers close (engine.Stop, step 3) before the actor system stops (step 4). This is recorded as an open question owned by `#24`, verified by a test in IMPL-4, not resolved by this design.
- **The in-memory composition may never land** if `#11` is never broken down into the in-memory runtime and the neutral engine API. Mitigation: this proposal names both gaps and their recommended owner instead of assuming they will be picked up implicitly, and `#105` stays open until IMPL-6 lands.
- **Publisher ownership is easy to misread.** A consumer might close a publisher it passed into `New`, or forget `Stop` after a failed `Start`. Mitigation: D5 states one rule (after `New` succeeds, always call `Stop`, never close a publisher yourself), and `Stop` is safe in every state.

## Open decisions (see `design.md` §9 for the full table)

- The default shutdown timeout bounding rollback and `Stop` (`design.md` suggests 30s).
- Creating the `#11` child issues for the in-memory runtime (`RUNTIME-005`) and the runtime-neutral engine API (`RUNTIME-001`/`RUNTIME-002`); recommended owner is `#11`, not a reopened `#103`.
- Whether the manual composition path is ever deprecated.
- The flush policy for in-flight events during shutdown (`#24`, LIFE-004).

## Success criteria for this docs change

- [ ] A normative design with two composition-root walkthroughs (GoAkt and in-memory), diagrams, and MUST-level decisions exists (`design.md`).
- [ ] `#103`'s acceptance criteria are re-verified against `main` and recorded as a table, not assumed from the issue's checkboxes.
- [ ] The in-memory composition's blockers are identified: `extension.Dependency` in the public behavior contracts (`#123`), the missing in-memory runtime, and the missing runtime-neutral engine API (`#11`).
- [ ] Decisions on location, DI model, validation, ownership, and Start/Stop ordering are stated as closed (D1–D8 in `design.md`).
- [ ] An implementation slice plan (IMPL-1 through IMPL-6) maps to every `#105` acceptance criterion.
- [ ] Alternatives (DI frameworks, code generation, composing inside package `ego`, options-based `Spec`) are recorded with rejection reasons.
- [ ] No production code moves in this change.
