# Proposal — Deterministic in-memory runtime and `compose/inmem` (EGO-RUNTIME-005, #105 IMPL-6)

| Field | Value |
|---|---|
| Change | `ego-runtime-002` |
| Date | 2026-09-27 |
| Phase | `sdd-propose`: proposed, design only |
| Tracker | [`#148`](https://github.com/getsyntegrity/ego/issues/148), parent [`#11`](https://github.com/getsyntegrity/ego/issues/11), foundation [`#10`](https://github.com/getsyntegrity/ego/issues/10); closes the last criterion of [`#105`](https://github.com/getsyntegrity/ego/issues/105) when implemented |
| Baseline | `main` at `57c4b11` |
| Evidence | [`design.md`](./design.md) §2 (what the GoAkt runtime does today, with `file:line`), §9 (reproduction) |
| Builds on | ego-runtime-001 D1–D10 and maintainer decisions 1–9; ego-arch-003 §D1–§D8, §5.2, §6 IMPL-6; ego-arch-004 §D4, §D6 (V8), F-E; ego-arch-002-s3; ego-arch-006 D1–D8, F1; ego-arch-001 §3, §10 |
| Human gate | All questions decided by the maintainer on 2026-09-27 (design §5); Q2 is conditional on spec 0's characterization. #148 closes only with spec 7's pull request. Decided on 2026-09-27: passivation is implemented (§D11); spec 0 characterizes GoAkt first (§D12); saga delivery order is unspecified (Q8); no public clock (Q9); `EraseEntity` is its own spec blocked by #166 (Q7) |

## Problem

Issue #105 says Ego must have "at least one GoAkt composition and one in-memory composition without changing the domain". Only the GoAkt half exists. Ego's one runtime is `*ego.Engine`, which is built on the GoAkt actor system, so the only way to run a behavior today is to start GoAkt.

ego-arch-003 §5.2 listed three blockers for the in-memory half. Two are gone: behaviors no longer need GoAkt (`port/behavior`, #123), and consumer code can be written against a runtime-neutral interface (`port/runtime`, #147, reached through `compose/goakt.App.Runtime()`). The third blocker is the one this change designs: there is no second runtime that implements `port/runtime`, and no composition root that builds it.

## What changes

This change is documentation only. It proposes:

0. **A characterization of GoAkt first** (spec 0, design §D12). The behaviors that reading could not settle are measured on `compose/goakt` and written into the design before any in-memory code exists: panics under each supervisor directive, commands queued behind a failed write, and passivation activity.
1. **An in-memory runtime** in a new root-module package, `internal/inmemruntime` (design §D1). It implements `port/runtime.Runtime` without any actor system. Each entity is a small serialized mailbox (a queue processed by one goroutine at a time), so commands to one entity run one after another, in the order they arrive. It persists through the same stores the `compose.Spec` carries, publishes on the same two event-stream topics, and feeds the same publishers.
2. **Its semantics, stated capability by capability** against what `*ego.Engine` does today (design §D3–§D7): event-sourced and durable-state entities, `SendCommand` and `Dispatch`, sagas including the `SagaStatus` lifecycle status fixed by #153, and the event stream. Projections return `runtime.ErrUnsupported` in this change; decided as Q3, and it answers #148's typed-error criterion only for the projection capability. `WithPassivateAfter` is implemented with the semantics of single-node GoAkt (maintainer decision, 2026-09-27; design §D11). The order in which sagas receive events is unspecified on every runtime (maintainer decision on Q8). `EraseEntity` gets its own spec, blocked only by #166. Until that spec lands, the in-memory `EraseEntity` returns `ErrUnsupported`, which **does not** satisfy the `port/runtime` erasure contract.
3. **A composition root, `compose/inmem`**, with the same `compose.Spec`, the same `Spec.Validate` (V1–V8), and the same sequencer (`compose/internal/lifecycle`) and publisher start-and-probe helper (`compose/internal/adapters`) that `compose/goakt` uses. It exposes `App.Runtime() runtime.Runtime`, like `compose/goakt` (design §D8).
4. **An archcheck rule, `inmem-no-runtime`**, that keeps both new packages away from package `ego`, `internal/extensions` and GoAkt, plus closure tests that check the same thing transitively (design §D2). No baseline entry, no exception, no relaxed rule.
5. **The neutrality proof #105 asks for**: one behavior value and one consumer function, written against `port/runtime`, run on both compositions with the same `Spec`, producing the same observable state, journal and stream messages (design §D9).

## Why this shape

- **No change to package `ego`.** Nothing in the chain edits `engine.go`, `option.go` or any other root-package file, so it cannot conflict with the other writers of those hot files and cannot change GoAkt behavior. The price is that the in-memory runtime re-implements about a dozen small pure rules (deadline gates, result classification, event envelopes). A shared test table runs the same scenarios on both runtimes to catch drift (design §D10). Extraction is follow-up FU-B, with #124 (Q4, decided).
- **Internal runtime, public composition root.** Consumers reach the in-memory runtime only through `compose/inmem.App.Runtime()`, which returns the neutral interface. That keeps the new public v4 API to one small package. Promoting the runtime later is additive, and removing a public package inside v4 is not possible. This is decided (Q1).
- **Determinism by construction.** The runtime reads time only through an internal clock, which drives timestamps, passivation timers and saga timeouts. Its unit tests advance a manual clock instead of waiting. The clock is not public in this chain (maintainer decision on Q9). Every guarantee in design §D7 is checkable with condition-based waits instead of sleeps.

## Compatibility

Additive in v4 (ego-arch-001 §10). The new public package is `compose/inmem`, and apidiff must report additions only for it. `internal/inmemruntime` and `internal/runtimeconsumer` are internal. No existing exported symbol changes in `ego`, `port/runtime`, `compose` or `compose/goakt`; `port/runtime` gains one documentation sentence (design §6).

## Chain of specs

| # | Spec | Tasks | Depends on |
|---|---|---|---|
| 0 | [`specs/goakt-characterization`](./specs/goakt-characterization/spec.md): harness, GoAkt measurements recorded in the design | 4 | this design approved |
| 1 | [`specs/inmem-runtime-core`](./specs/inmem-runtime-core/spec.md): package, archcheck rule, method table, internal clock, event-sourced entities and commands | 5 | spec 0 |
| 2 | [`specs/inmem-runtime-state`](./specs/inmem-runtime-state/spec.md): event stream, durable state, encryption, publishers and `Stop`, tenancy | 5 | spec 1 |
| 3 | [`specs/inmem-runtime-sagas`](./specs/inmem-runtime-sagas/spec.md): sagas, tenant rules, compensation, timeout, `SagaStatus` | 5 | specs 1, 2 |
| 4 | [`specs/inmem-runtime-passivation`](./specs/inmem-runtime-passivation/spec.md): `WithPassivateAfter` | 3 | specs 1, 2 |
| 5 | [`specs/compose-inmem`](./specs/compose-inmem/spec.md): `compose/inmem` composition root | 5 | specs 1, 2 |
| 6 | [`specs/runtime-neutrality`](./specs/runtime-neutrality/spec.md): neutrality proof and records | 4 | specs 3, 4, 5 |
| 7 | [`specs/inmem-runtime-erasure`](./specs/inmem-runtime-erasure/spec.md): `EraseEntity` with crypto-shredding | 3 | spec 2; [#166](https://github.com/getsyntegrity/ego/issues/166) (blocking) |

## Out of scope (MUST NOT in this change)

- Any production code; each spec is implemented in its own pull request.
- Placement, supervision and passivation contracts (RUNTIME-003), capability negotiation and a runtime `Descriptor` (RUNTIME-006, ego-arch-004 F-E), the public conformance suite (RUNTIME-007), drain and shutdown policy (#24), the physical move of the engine (#124), the write-side option redesign (#12), an `EntityRef` handle (#29).
- A projection runner for the in-memory runtime (follow-up FU-A, design §5 Q3).
- Fixing the two contract mismatches of design §2.6. `EraseEntity` crypto-shredding and its key granularity are [#166](https://github.com/getsyntegrity/ego/issues/166), which blocks spec 7 only; the no-event `SendCommand` text is FU-E.
- Changing any doc comment or behavior of the GoAkt adapter.

## Pull requests and #148

**#148 closure rule (maintainer decision 2026-09-27).** #148 is closed only after spec 7, because until then `compose/inmem`'s `EraseEntity` returns `ErrUnsupported` and does not meet the `port/runtime` contract. Specs 0–6 may advance and merge separately. Each of their pull requests states that limitation, uses "Refs #148" and never a closing keyword, and does not mark #148 complete. Spec 6's criteria mapping records #148 as still open pending spec 7. Only spec 7's pull request carries the closing keyword ("Closes #148").

## Rollback

Every spec is additive. Until a release contains `compose/inmem`, any spec can be reverted. After a release, `compose/inmem` is public v4 API and must not be deleted inside v4; the internal packages can still change freely.
