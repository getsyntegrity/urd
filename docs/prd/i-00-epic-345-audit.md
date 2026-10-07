# I-00 - Audit of the #345 tasks against their acceptance criteria

Companion of `i-00-baseline-develop.md`. Audited on 2026-10-07 against code at
`develop` `4c66286` (#426, Go 1.27.0). The historical baseline (`4ebdc3d`,
Go 1.26) is not mixed in. Rows touched by #429-#433 (actor identity, adoption
on PostgreSQL, scoped offsets, schema version six) were re-read against
`develop` `01da644` and are marked `01da644` in their last column; all other
rows still reflect `4c66286`, and their `confirmed` marks were not repeated. This is a baseline with documented problems, not a
claim that the epic is satisfied.

## Method, in two passes

1. First pass: one reviewer per group read each issue and classified each
   criterion by reading code, docs and tests.
2. Second pass: a different reviewer re-checked every row against the sources
   it cites and against the live issue body (no criterion dropped or invented),
   searched by behavior for an equivalent implementation behind each
   `no implementado`, required a concrete dependency for each `bloqueado`, and
   searched existing issues by content terms. It changed 26 states (see
   "Corrections"). No issue was opened: related existing issues are linked in
   the last column of each matrix.

Membership: a task of #345 is an issue whose body says `Epica: #345` (16,
including #346), or `Epica: #N` of one of the sub-epics #342, #343, #344, #395,
#396, #397, #398 (58), plus the seven sub-epics: 81 issues, all open. The body
of #345 lists only part of them, so it was not used as the list. #346, #362,
#371, #379 and #381 are in the main report; the other 76 are here.

Tests were run only where one could settle a doubt (saga actor and engine saga
status, projection runner architecture and lag, the persistence and testkit
suites and `inttest/flows/eventstore`, already green). No suite was repeated.
Static reading is the main evidence and can miss an equivalent implementation
under a name nobody searched; that is the main limit of this audit.

States: `cumplido`, `parcial`, `no implementado`, `no verificado`,
`bloqueado` (a named open issue or finding is the reason the criterion cannot
be met or proven yet). Kinds: `current-defect` (existing behavior that is
wrong or unsafe today), `new-capability` (guarantee the architecture asks for
that does not exist yet; not a defect), `negative-ok` (negative criterion met
because nothing was built), `met` (positively demonstrated).

## Totals (one reproducible count)

One row per criterion, counted from the matrices of this file:

```
awk -F'|' 'NF==9 && $3 !~ /State|---/ {gsub(/^ +| +$/,"",$3); c[$3]++} END {for (k in c) print k, c[k]}' docs/prd/i-00-epic-345-audit.md
```

| Group | Issues | Criteria | cumplido | parcial | no implementado | no verificado | bloqueado |
|---|---|---|---|---|---|---|---|
| A: Direct tasks of the epic | 15 | 104 | 7 | 20 | 68 | 5 | 4 |
| B: Persistence (#342) | 16 | 78 | 2 | 16 | 42 | 0 | 18 |
| C: Projection (#343) and tenancy (#344) | 17 | 82 | 3 | 10 | 59 | 0 | 10 |
| D: Integration (#395) and testkit (#396) | 12 | 72 | 5 | 19 | 33 | 0 | 15 |
| E: Workflow (#397) and management (#398) | 16 | 95 | 6 | 23 | 38 | 1 | 27 |
| **Total** | 76 | 431 | 23 | 88 | 240 | 6 | 74 |

| Kind | Criteria |
|---|---|
| `current-defect` | 13 |
| `new-capability` | 395 |
| `negative-ok` | 17 |
| `met` | 6 |

6 criteria are positively demonstrated and 17 are negative
criteria met because nothing was built. 395 describe new
guarantees: their `no implementado` or `parcial` state is the expected
baseline, not a defect.

## Current defects (13)

Behavior that exists today and is wrong or unsafe, as opposed to a capability
that is not built yet. (The #416 raw-reset row left this list on `01da644`,
after #431; the count of 14 recorded on `4c66286` is now 13. The kind total
was re-derived with the awk command above, adapted to column 4.)

| Task | Criterion | State | Evidence | Related |
|---|---|---|---|---|
| #342 | Imports y capacidades respetan la arquitectura | parcial | `go list -deps ./persistence` includes urd/tenancy (persistence/scope.go:28) and urd/egopb; rest of persistence imports are clean. | #349, #363, #347 |
| #349 | persistence no importa tenancy | no implementado | persistence/scope.go:28 imports urd/tenancy; Scope holds `tenant tenancy.TenantID`; `go list -deps ./persistence` lists tenancy. | #347, #344 |
| #354 | Si hoy no se cumple, el adapter se corrige en este issue | no implementado | Postgres writeConditional (event_store.go:273-314) checks revision only, no events[0].seq == revision+1 or contiguity: a gap is accepted. Overlap on an existing seq is re | #377 (batching) |
| #363 | Cierre de dependencias de persistence no incluye mensajes del engine | no implementado | `go list -deps ./persistence` includes urd/egopb; protos/ego/ego.proto mixes Event/Offset/Snapshot with CommandReply, StateReply, TenantBinding*. | #347 |
| #377 | Stash durante append tiene límite y rechazo inmediato | no implementado | ctx.Stash() at event_sourced_actor.go:387,397,913,1098,1481,1573,1735,1878 with no bound or rejection; grep for limit/max near stash empty. | #378 (similar admission theme) |
| #378 | Saturación no produce espera/memoria ilimitada | no implementado | Unbounded waiters in pgxpool; stash also unbounded (see #377). | #377 |
| #370 | política de retención que impide a DeleteEvents borrar eventos que una versión vigente nec | no implementado | events_janitor_actor.go:125 deletes from snapshot retention count; no offset lookup (grep). Equivalent term searches (retention barrier, min offset) found nothing. | #362 #403 #357 |
| #401 | No perder intent ante timeout/cancelacion ni agotar memoria; polling obligatorio | no implementado | A failed publish is logged and dropped, no retry (`engine/streams.go:486-493`); no intent store to retain. | #400, #402 |
| #409 | Consumo durable usa EventReader/runner projection; pub/sub solo wakeup | bloqueado | `consumeEvents` (:443-477) reads live in-memory subscriber only; events during downtime are lost; no `EventReader` type exists | #360 (I-08a read in adapters), #361 (runner read path), #371 |
| #410 | Replay tras caída/reinicio recupera pendientes y no genera intenciones nuevas para evento  | no implementado | `recover()` (:363) replays only own events; events during downtime lost; repeated event is handled twice (no dedupe, `handleStreamEvent` :543) | #361 #360 #373 |
| #411 | Límites de concurrencia/espera/retry; no bloquear mailbox indiscriminadamente | parcial | Per-command timeout, default 5s (`effectiveCommandTimeout` :826; test 'default timeout when zero' saga_test.go:741) | #378 (related bounds) |
| #412 | Reinicio recupera timers vencidos con política explícita | no implementado | PostStart reschedules the full timeout on each start (:275-277); `recover()` sets status Running (:365) so a terminal saga is reactivated | #410 #301 (closed NOT_PLANNED) |
| #417 | [single tenant] Identidad de operador separada del tenant ID; fail-closed sin tenant ficti | no implementado | Legacy mode (no resolver) erases `Unscoped()` with no authorization (`entities.go:322-326`) | #424 #379 |

## Blocked criteria and their dependencies (74 rows)

Each blocked row names the open issue or finding that stops it; a row can name
several, so the counts add up to more than the number of rows. Rows blocked
without a concrete dependency: 0.

| Blocking dependency | Blocked rows | Tasks |
|---|---|---|
| #374 | 15 | #361, #367, #376, #382, #393, #401, #406, #410, #412, #415, #416, #417 |
| #373 | 10 | #361, #393, #400, #406, #408, #410, #412 |
| #419 | 8 | #389, #420, #421, #422 |
| #390 | 8 | #392, #415 |
| #375 | 8 | #356, #361, #370, #393, #406, #410, #415, #416 |
| #378 | 6 | #392, #393, #406, #415 |
| #421 | 5 | #422 |
| #348 | 4 | #352, #360, #361, #406 |
| #362 | 4 | #359, #361, #365, #370 |
| #347 | 4 | #363, #395, #397, #398 |
| #368 | 4 | #380, #392, #415 |
| #366 | 4 | #400, #408 |
| #414 | 4 | #398, #415, #417 |
| #420 | 4 | #421, #422 |
| #387 | 3 | #358, #385 |
| #351 | 3 | #358, #360 |
| #350 | 3 | #359 |
| #380 | 3 | #392, #403, #417 |
| #360 | 3 | #361, #400, #409 |
| #352 | 2 | #358 |
| #358 | 2 | #359 |
| #379 | 2 | #392, #422 |
| #383 | 2 | #392, #398 |
| #361 | 2 | #400, #409 |
| #406 | 2 | #401, #408 |
| #401 | 2 | #403 |
| #370 | 2 | #403, #416 |
| #382 | 2 | #403, #416 |
| #369 | 2 | #403, #416 |
| #354 | 2 | #406 |
| #393 | 2 | #408 |
| #417 | 2 | #416, #418 |
| #424 | 2 | #421, #422 |
| #389 | 1 | #372 |
| #394 | 1 | #389 |
| #422 | 1 | #389 |
| #395 | 1 | #389 |
| #398 | 1 | #389 |
| #359 | 1 | #360 |
| #391 | 1 | #392 |
| #353 | 1 | #365 |
| #364 | 1 | #365 |
| #392 | 1 | #380 |
| #399 | 1 | #395 |
| #400 | 1 | #401 |
| #402 | 1 | #403 |
| #435 | 1 | #403 |
| #409 | 1 | #397 |
| #371 | 1 | #409 |
| #376 | 1 | #410 |
| #410 | 1 | #411 |
| #357 | 1 | #411 |
| #381 | 1 | #416 |
| #408 | 1 | #418 |
| #416 | 1 | #418 |
| #415 | 1 | #421 |
| #355 | 1 | #422 |
| #407 | 1 | #422 |
| #405 | 1 | #422 |

#427 (B4, fixed by #430) and #428 (TenantAdopter on PostgreSQL, fixed by #429)
are cited only where a row still reads them as history. The blocked row that
named #428 now names #435.

## #350: slice stability and non-zero partition propagation

The text of #350 asks only for the first guarantee below. The second is a gap
in the issue text, found while reviewing B2. They are different guarantees and
one test does not stand in for the other.

Guarantee split (the issue lists only guarantee 1 as a test criterion):
- (1) Stability across 1/3/5 nodes: criteria "N decidido" (prerequisite) and "test 1, 3, 5 nodos". Evidence today: none; the only stored-shard code reads GoAkt Partition, so stability is not provided by design. Test needed: unit test of the pure function independent of any ActorSystem, plus (optional, integration) a cluster of 1/3/5 nodes asserting the same persistenceID yields the same stored Shard.
- (2) Propagation of a non-zero partition value into stored Shard fields: NOT a criterion in the issue body (gap in issue text). Evidence today (`01da644`): `TestBaseline346` B2 only shows shard 0 (zero value, cannot prove propagation); `TestClusterEventPublisherHighPartitionCount` (engine/publisher_test.go:409, assertions at 526-535) does show `GetShard() >= 271` on events published in a cluster, so propagation is covered for published events in a cluster only. Still uncovered: durable-state non-zero Shard, persisted rows, and which identity feeds `Partition` after #430 (it hashes `behavior.ID()` by code reading, unverified by a test). Code writes `entity.shardNumber` at event_sourced_actor.go:355 and durable_state_actor.go:169. Test needed: with a slice function returning a non-zero value (injected stub or real), spawn an event-sourced and a durable-state entity and assert stored Event.Shard and DurableState.Shard equal it; zero-value tests cannot substitute.
- Neither test substitutes for the other: a stable pure function can still never reach storage, and a propagated value can still vary with topology.

## Corrections made by the second pass

**Direct tasks of the epic.** 6 rows changed state
- #366 docs outbox at-least-once: parcial -> no implementado
- #372 versioning policy: no verificado -> bloqueado (by #389)
- #372 tenancy only with second consumer: no verificado -> cumplido (negative-ok)
- #386 no exactly-once promise: no verificado -> cumplido (negative-ok)
- #389 assess #395-#398/#419-#422: no verificado -> bloqueado (by open epics)
- #394 guide CPU/IO/locks: parcial -> no implementado

**Persistence (#342).** 3 rows changed state
- #351 "reglas de portabilidad": no verificado -> parcial
- #377 "aislamiento de snapshots por scope": cumplido -> parcial
- #390 "Configuración por defecto Shared conserva...": no verificado -> no implementado
(Also noted without state change: #354 row 2, audit claim that Postgres accepts overlap is inaccurate; the PK rejects it with an untyped error.)

**Projection (#343) and tenancy (#344).** 5 rows changed state
- #393 "PerScope soporta selección de destino compatible": parcial -> no implementado
- #368 "Cambiar solo pool no exige PerScope": bloqueado -> no implementado
- #369 "prueba de punta a punta": parcial -> no implementado
- #369 "cero omisiones según el oráculo": no verificado -> no implementado
- #344 "Imports y capacidades respetan la arquitectura": no verificado -> parcial

**Integration (#395) and testkit (#396).** 3 rows changed state
- #403 "Metrics de backlog/edad/errores y admision": parcial -> no implementado
- #404 "Relacionar #348/#354 y restantes TCK con oraculos existentes": bloqueado -> no implementado
- #408 "Funciones GoAkt via harness existente; no framework actor paralelo": no verificado -> cumplido (negative-ok)

**Workflow (#397) and management (#398).** 9 rows changed state
- #410 'No incorporar runner propio': no implementado -> cumplido
- #411 'Compensación ID propio estable': no implementado -> parcial
- #414 'Registro por capacidades': bloqueado -> no implementado
- #418 'No exigir terminar management para GateB/C': no verificado -> cumplido
- #420 'Capturar metadata': bloqueado -> no implementado
- #420 'Sampling/ring buffer': bloqueado -> no implementado
- #420 'Instrumentación aislada': bloqueado -> no implementado
- #421 'Solo lectura': no implementado -> cumplido
- #421 'Sin API remota obligatoria': no implementado -> cumplido

# Matrices

Columns: criterion, state, kind, evidence, gap, blocking or related issues,
and the result of the second-pass check.

## Direct tasks of the epic

### #347 [I-01] ADR: amendment to ego-arch-001 for module topology
Dependencies: #346 (done), epic #345; related #349, #353, #383, #384, #424, #372, #395-#398, #419-#422
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| ADR states persistence imports neither tenancy nor projection | no implementado | new-capability | No ADR file (docs/ has only PRD/baseline); persistence/scope.go:25 and conflict.go:30 import tenancy. | ADR missing; code decoupling pending. | Related #349 (code), #342 | confirmed |
| Resolves adapters-under-contract-path rule (B10) | no implementado | new-capability | No ADR; PRD lines 227/262 only say #347/#353 reconcile the rules. | ADR missing. | Related #353 | confirmed |
| Defines where egopb lives | no implementado | new-capability | egopb/ still at root; no decision text anywhere in docs (searched egopb, ADR). | ADR missing. | Related #363 | confirmed |
| No module names a concrete engine outside its adapter | no implementado | new-capability | Rule unwritten. Core module has no pgx import (only persistence/postgres plus nested example/inttest); nothing enforces it. | ADR and enforcement missing. | Related #353, #384 | confirmed |
| Minimum capabilities per role; composition rejects adapter not declaring | no implementado | new-capability | PRD C-/U- text only; compose/spec.go:327 requiredCapabilities is an empty map (spec_test.go:692 asserts empty). | ADR missing; mechanism belongs to #384. | Related #384 | confirmed |
| Amend: #395 Integration boundary | no implementado | new-capability | No ADR text; no integration package. | Pending ADR. | Related #395 | confirmed |
| Amend: #397 Workflow CommandDispatcher SPI | no implementado | new-capability | No ADR; no workflow module or dispatcher SPI. | Pending ADR. | Related #397 | confirmed |
| Amend: #398 Management boundary | no implementado | new-capability | No ADR; no management package. | Pending ADR. | Related #398 | confirmed |
| Amend: #396 Testkit boundary (no prod import of testkit/cmd) | no implementado | new-capability | No ADR; no test forbids it (today only example/ imports testkit). | Pending ADR. | Related #396, #353 | confirmed |
| Amend: generic Inspector #419-#422 DTO boundary | no implementado | new-capability | No ADR; no inspector code. | Pending ADR. | Related #419-#422 | confirmed |
| Amend: no new go.mod; extraction follows #372 | no implementado | new-capability | No ADR; existing nested go.mod only (persistence/postgres, publishers, inttest, example, benchmark, test/compat). | Pending ADR. | Related #372 | confirmed |
| Amend: Workflow is Urd-own consolidation + dependency diagram | no implementado | new-capability | No ADR/diagram; mermaid exists only in the issue body. | Pending ADR. | Related #397 | confirmed |
| Single tenant: record Unscoped/fixed/multitenant modes, adoption/migration on identity change | parcial | new-capability | PRD docs/prd/urd-platform-prd.md:32-42 records the three modes ("names subject to the ADR"); persistence.Unscoped() exists. | PRD is not the ADR; final API names and per-capability service requirements undecided. | Related #424 | confirmed |
Issue verdict: not done; no ADR deliverable exists (PRD is a proposal, not the ADR).

### #353 [I-07] Architecture: tests for the new rules
Dependencies: #347 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Architecture lane fails on a new forbidden import | parcial | new-capability | TestArchitecture* lane exists (26 funcs, e.g. engine/tenancy_architecture_test.go, port/*_architecture_test.go; docs/ci.md:112). None covers the I-01 persistence/projection/tenancy rules. | No test for I-01 rules; rules undefined until #347. | Blocked by #347; related #349 | confirmed |
| Each exception links the issue that removes it (I-03, I-12, I-13) | no implementado | new-capability | No temporary-exception list exists (searched exception, allowlist in tests/docs). | Exception list absent; #349, #364, #365 not linked. | Blocked by #347; related #349, #364, #365 | confirmed |
| Verify #395-#398 boundaries and #347 deps; forbid lower-to-root imports | no implementado | new-capability | No such tests; those packages do not exist. | Rules not defined. | Blocked by #347; related #395-#398 | confirmed |
| Production does not import testkit or cmd | no implementado | new-capability | Currently true in the core module (only example/ imports testkit) but no test asserts it. | Test absent. | Blocked by #347 (rule text); related #396 | confirmed |
| Generic inspector does not import Urd domain/tenancy | no implementado | new-capability | No inspector code or test. | Nothing to test yet. | Blocked by #347, #419-#422 | confirmed |
| No new go.mod; no copy of runner/ownership/Tx/tenant state machines | no implementado | new-capability | No test enforces it. | Test absent. | Blocked by #347 | confirmed |
Issue verdict: not done; only the pre-existing architecture lane exists.

### #355 [I-20] Define load model, limits and measurable SLOs of a cell
Dependencies: #346 (done)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Doc with commands/s, events/s, bytes, fan-out cost, hot tenants/entities | no implementado | new-capability | No such doc; PRD s.5 (prd:203) only says #355 will define them. | Deliverable missing. | Related #387, #385 | confirmed |
| SLOs distinguish availability, in-quota rejections, write ack, eligibility, application | parcial | new-capability | PRD s.5 table separates eligibility/processing lag, admission, recovery; provisional p99 50 ms etc. (prd:215); no SLO definitions. | Not a #355 document; no SLO definitions. | Related #387 | confirmed |
| Limits budget: input, mailbox, stash, pool (in flight/wait), batches, parked | no implementado | new-capability | No doc; only hard-coded cfg.MaxConns = 20 (persistence/postgres/event_store.go:81, offset_store.go:57). | Deliverable missing. | Related #378, #391 | confirmed |
| Initial values marked as hypotheses | parcial | new-capability | PRD prd:215 labels figures "provisional experiment inputs". | No values in a #355 doc. | none | confirmed |
| Do not promise 1M r/s without measurement | cumplido | negative-ok | PRD prd:203 and prd:243 exclude unmeasured million-requests claims; no 1M claim in non-archive docs (grep). | None; re-check when the doc is written. | none | confirmed |
Issue verdict: not done; only PRD placeholders exist.

### #366 [I-14] ReadSideProcessor with transaction and envelope
Dependencies: #361, #365, #373, #376, #388 (all open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Same-backend effect, applied mark and offset in one Tx | no implementado | new-capability | projection/handler.go:57 Handle(ctx, persistenceID, event, revision) has no Tx; offsetstore/offset_store.go:42 WriteOffset is separate; no applied-mark code (grep). | No ReadSideProcessor, no Tx API. | Blocked by #373 (P-TX); related #361 | confirmed |
| Mark per (processor, version, scope, entity); kill -9 and two projections test | no implementado | new-capability | No applied-marks table or API; no such test. | Not built. | Blocked by #373, #376 | confirmed |
| GlobalPrepare/Prepare idempotent under lease/fencing; mid-crash test | no implementado | new-capability | grep GlobalPrepare/Prepare in *.go: no hits. | Not built. | Blocked by #376 (P-PREP), #374 | confirmed |
| Docs state outbox is at-least-once, destination needs idempotency | no implementado | new-capability | handler.go:46 says projection delivery is at-least-once and handlers should be idempotent; that is the runner, not the outbox. No outbox exists or is documented. | Outbox doc absent. | Blocked by #400 (outbox); related #395 | corrected: was parcial; handler.go note is about runner redelivery, not the outbox the criterion names |
| Single tenant: starts without tenancy extension; envelope exposes Unscoped/fixed; PerScope/SharedCell validated | no implementado | new-capability | No envelope/PerScope/SharedCell code (grep). | Not built. | Blocked by #424, #361 | confirmed |
Issue verdict: not started; Handler SPI is still the legacy Tx-less one.

### #372 [I-18] Extract and publish the Go modules
Dependencies: #365, #368, #389 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| No replace to GoAkt forks (#326 resolved) | no implementado | new-capability | go.mod:93 and persistence/postgres/go.mod:27 `replace github.com/tochemey/goakt/v4 => github.com/pablogore/goakt/v4 v4.5.7-actorof.1`; #326 only merged a temporary replace. | Fork replace still present in two go.mod files (upstream release pending). | Related #326 (merged), #339 (closed) | confirmed |
| Each module compiles and passes TCK against a published root version | no implementado | new-capability | persistence/, projection/, tenancy/ have no go.mod; persistence/postgres go.mod:6 `replace => ../../`. | Extraction not done. | Blocked by #389 (gate), #365 | confirmed |
| Versioning policy applied | bloqueado | new-capability | Only a root semver policy exists (docs/main-branch-policy.md:42, ci.md:246); nothing for per-module versioning since no module exists. | Cannot apply a policy to modules not extracted. | Blocked by #389 (extraction decision) | corrected: was no verificado; no concrete policy to verify, depends on extraction decided in #389 |
| Extraction optional, conditioned on deps/cadence/consumers | no verificado | new-capability | Decision gated by #389; no record. PRD prd:234 repeats the condition. | Not decided. | Related #389 | confirmed |
| tenancy only with a second consumer | cumplido | negative-ok | tenancy has no go.mod today (not extracted), so the condition is not violated. | None now; re-evaluate in #389. | Related #389 | corrected: was no verificado; vacuously met since nothing was extracted |
| Keep the nested PostgreSQL adapter | cumplido | met | persistence/postgres/go.mod exists and is intact. | None. | none | confirmed |
Issue verdict: not done; blocked by #365, #368, #389 and the GoAkt replace.

### #383 [U-EXT] Compose GoAkt extensions and resolve deps in PreStart
Dependencies: #347, #349 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Stable extension IDs, deps in PreStart, resources released | parcial | new-capability | Stable ID constants (internal/extensions/extensions.go:43-79); extensions.Require/Optional (lookup.go:60,95) used in actor PreStart (e.g. event_sourced_actor.go:303); engine/option.go:143-185 WithExtensions. | No resource-ownership/close test for shared pools; tenancy extension is only a marker. | Related #378, #394 | confirmed |
| Relocated actor gets extensions of destination node | parcial | new-capability | engine/engine_tenant_relocation_test.go:111 (relocated actor recovers its tenant state) implicitly needs the stores on the new node. | No test asserting extensions specifically at the destination node. | none | confirmed |
| Missing/incompatible extension fails at start | cumplido | met | Require returns ErrMissingRequiredExtensions (lookup.go:35); lookup_test.go:106,128,208; engine/extension_sentinel_test.go. | Covers current extensions only. | none | confirmed |
| Compile-time integration, no dynamic plugins | cumplido | negative-ok | Extensions registered via goakt.WithExtensions in engine/option.go:143-185; no plugin loading anywhere. | None. | none | confirmed |
| Single tenant: tenancy service only if mode requires; absence valid | parcial | new-capability | Tenancy marker registered only when resolver non-nil (engine/option.go:185); absence works today. | No explicit mode model; requirement map absent. | Blocked by #347, #424 | confirmed |
Issue verdict: partially satisfied by existing extension plumbing; new guarantees need #347/#349.

### #384 [U-CAPS] Validate adapter capabilities per role at composition
Dependencies: #347, #351, #390 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Journal requires conditional append, entity read, uniqueness/contiguity, declared idempotency | no implementado | new-capability | compose/spec.go:327 requiredCapabilities empty; no journal capability declared. persistence/conformance tests behavior, not declaration. | No declarations or validation. | Blocked by #347 | confirmed |
| Feed requires stable prefix, declared eligibility, validated cursor | no implementado | new-capability | No feed contract in code. | Not built. | Blocked by #351 | confirmed |
| Destination declares common Tx or idempotent upsert, and fencing | no implementado | new-capability | No destination role (grep). | Not built. | Blocked by #373, #374 | confirmed |
| TCK by capability without skipping minima | parcial | new-capability | persistence/conformance (events.go etc.) runs fixed suites, not capability-parametrised. | Capability-driven selection absent. | Related #348, #374 | confirmed |
| Concrete engines only in adapters | parcial | new-capability | pgx in core module confined to persistence/postgres (also nested example/inttest/benchmark modules); no test enforces it. | Unenforced. | Related #353 | confirmed |
| Validate role, backend/cell, destination identity; separate resources Journal/Feed vs destination | no implementado | new-capability | No role/cell model (grep role, cell in compose). | Not built. | Blocked by #390 | confirmed |
| Shared: finite max per instance, replica max, headroom; sum physical backend | no implementado | new-capability | No budget code; MaxConns=20 hard-coded (event_store.go:81). | Not built. | Blocked by #390; related #391, #378 | confirmed |
| Reject SharedCell with independent destinations under shared cursor | no implementado | new-capability | No SharedCell (grep). | Not built. | Blocked by #373, #393 | confirmed |
| Dedicated enabled only with registry+capability | no implementado | new-capability | No Dedicated code (grep). | Phase 2 work. | Blocked by #392 | confirmed |
| Reject invalid combos in PreStart; pgx mins not availability guarantee | no implementado | new-capability | compose Spec.Validate V5/V6/V8 check ports, publisher IDs, CapStart/CapReady/CapFixedTenant only. | No role/budget rules. | Related #391 | confirmed |
| Single tenant: composition does not require tenancy for unscoped | parcial | new-capability | Resolver slot optional; V8 checks CapFixedTenant only when declared. | No mode model, no mismatch checks. | Blocked by #424 | confirmed |
Issue verdict: not done; only generic adapter-descriptor validation (V8) exists.

### #385 [U-LOAD] Validate overload and integral recovery in a cell
Dependencies: #361, #378, #377, #376 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Measure committed writes, all projections, availability, in-quota rejections, memory, both lags, recovery | no implementado | new-capability | benchmark/ holds micro-benchmarks only (benchmark_test.go); no overload/saturation harness in inttest or benchmark. | No harness. | Blocked by #355, #361 | confirmed |
| Hot tenant/entity, sustained and peak scenarios | no implementado | new-capability | None found. | Not built. | Blocked by #355 | confirmed |
| Per-tenant/cell capacity declared with evidence | no implementado | new-capability | None. | Not built. | Blocked by #355 | confirmed |
| Repeat under Gate A conditions | bloqueado | new-capability | Gate A (#387) has no recorded run or conditions. | Needs Gate A result. | Blocked by #387 (open) | confirmed |
| Single tenant Unscoped hot load scenario | no implementado | new-capability | None. | Not built. | Blocked by #424 | confirmed |
Issue verdict: not started; blocked by #355 and core work.

### #386 [U-GUIDE] Examples and guide for multitenant read-side and operation
Dependencies: #366, #381, #380, #394 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Handlers receive visible tenant and destination Tx | no implementado | new-capability | example/ has cluster, durablestate, eventssourced, saga only; no PerScope/SharedCell example. | Not built. | Blocked by #366 | confirmed |
| Examples of crash/retry, isolated rebuild, privileged selection | no implementado | new-capability | None. | Not built. | Blocked by #366, #381 | confirmed |
| No exactly-once / external-effect idempotency promise | cumplido | negative-ok | No guide exists; no "exactly-once" promise in non-archive docs or readme (grep). | Re-check when the guide is written. | none | corrected: was no verificado; negative criterion vacuously met |
| Document outbox destination key and mandatory polling | no implementado | new-capability | No outbox implementation or doc. | Not built. | Blocked by #395, #400 | confirmed |
| Connection-policy examples from #394 | no implementado | new-capability | None. | Not built. | Blocked by #394 | confirmed |
| Single tenant quickstart without tenancy config | no implementado | new-capability | readme.md:521 explains Unscoped default but there is no quickstart/guide. | Guide missing. | Blocked by #424 | confirmed |
Issue verdict: not started; deliverable guide absent.

### #387 [G-A] Gate A: choose mechanism with single-cell evidence
Dependencies: #352, #351, #355 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Zero omissions, eligibility<=T, progress, >=80% throughput, p99, overload results | no implementado | new-capability | No experiment results or decision record; xid8 appears only in docs (no Go code). | Experiment not run. | Blocked by #352, #355 | confirmed |
| Config/hardware, limits, failure criteria published | no implementado | new-capability | PRD prd:215-217 gives provisional criteria only. | Hardware/config not recorded. | Blocked by #352, #355 | confirmed |
| If it fails, compare alternatives | no verificado | new-capability | Conditional on a result that does not exist. | Nothing to compare yet. | Related #352 | confirmed |
Issue verdict: not done; gate decision absent.

### #388 [G-B] Gate B: core TCK and injected faults green
Dependencies: #361, #353, #365, #376, #384, #385, #424 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Memory/testkit and Postgres pass reader, append, idempotency, isolation, cursor, crash, dedupe, parking/replay, stale executor | parcial | new-capability | persistence/conformance covers events/snapshots/state (append, CAS, scope isolation) and runs on testkit and Postgres. No reader, cursor, parking, stale-executor cases. | Missing TCK cases; B2 non-zero partition untested. | Related #348, #374, #375, #357 | confirmed |
| Boundaries verified | parcial | new-capability | Pre-existing architecture lane only; new rules absent. | New rules absent. | Blocked by #353 | confirmed |
| Overload/recovery documented | no implementado | new-capability | None. | Not done. | Blocked by #385 | confirmed |
| Zero exceptions invalidating guarantees | no verificado | new-capability | No exception list exists; #427 (B4 actor identity collisions, fixed by #430) and #428 (TenantAdopter on Postgres, fixed by #429) no longer qualify; no list exists, so the criterion still cannot be claimed. | Cannot be claimed. | Related #353 | 01da644: #427/#428 fixed |
| Single tenant unscoped/fixed + multitenant accepted in memory and Postgres | parcial | new-capability | Unscoped tests: testkit/scope_test.go, persistence/postgres/event_store_test.go, conformance events.go:59-74; engine_fixed_tenant_resolver_test.go. No unified matrix. | No matrix; adoption on Postgres broken. | Blocked by #424; related #428 | confirmed |
Issue verdict: not met; blocked by many open dependencies. Findings #427 and #428 were fixed (#430, #429).

### #389 [G-C] Gate C: SPI stability in real use before extraction
Dependencies: #366, #367, #379, #380, #381, #386 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Real use recorded, SPI no incompatible change in window, independence criteria, GoAkt ecosystem interest | no implementado | new-capability | No record or evaluation doc; window undefined. | Phase 2; not done. | Blocked by #366, #386 | confirmed |
| Tenancy requires second consumer | no verificado | new-capability | No consumer evidence recorded. | Not evaluated. | Related #372 | confirmed |
| No obligation to contact maintainers | cumplido | negative-ok | Negative statement; nothing in repo requires it. | None. | none | confirmed |
| Shared/Dedicated lifecycle evidence from #394 | bloqueado | new-capability | #394 open; no Dedicated code. | Needs #394 evidence. | Blocked by #394 | confirmed |
| Assess #395-#398/#419-#422 impact without requiring completion | bloqueado | new-capability | Packages do not exist; assessment happens "when implemented" (issue body). | Cannot assess unbuilt boundaries; not a gate-closing dependency. | Blocked by #395-#398, #419-#422 (open) | corrected: was no verificado; concrete open epics are the dependency |
Issue verdict: not done; premature (depends on phase-2 work).

### #391 [U-POOL-BUDGET] Validate connection budget per backend and replicas
Dependencies: #390, #355, #368, #380, #384, #388 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Budget groups by real physical backend | no implementado | new-capability | No budget code; pools sized ad hoc (event_store.go:81, offset_store.go:57 MaxConns=20). | Not started. | Related #390, #378 | confirmed |
| Sums Shared+Dedicated max per instance x max replicas + headroom/admin/external | no implementado | new-capability | None. | Not started. | Related #390, #392 | confirmed |
| Reference formula documented | no implementado | new-capability | None. | Not documented. | none | confirmed |
| Mins vs maxes distinct, no double count | no implementado | new-capability | None. | Not started. | none | confirmed |
| Over-allocated, unbounded max, missing replica max rejected pre-start | no implementado | new-capability | None. | Not started. | Related #384 | confirmed |
| Dedicated provisioning consumes an admitted reservation | no implementado | new-capability | No Dedicated registry. | Needs registry. | Blocked by #392 | confirmed |
| Limit/scale change requires revalidation, rolling-update coexistence | no implementado | new-capability | None. | Not started. | none | confirmed |
| Document external load can exhaust DB; no absolute availability | no implementado | new-capability | PRD prd:133 states pool-isolation limits generically; no operator guide. | Not in a guide. | Related #386, #394 | confirmed |
| ADR leaves global dynamic admission as future extension | no implementado | new-capability | PRD prd:256 says "requires a future ADR"; no ADR exists. | ADR missing. | Blocked by #347 | confirmed |
| Single tenant budget counts pools/roles/all replicas | no implementado | new-capability | None. | Not started. | Blocked by #424 | confirmed |
Issue verdict: not started.

### #394 [U-POOL-E2E] Compose and verify Shared, Dedicated and DedicatedCell
Dependencies: #392, #391, #393, #380, #383, #384, #385 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Per tenant/role Shared or Dedicated; DedicatedCell | no implementado | new-capability | No policy code (grep Dedicated: PRD docs only). | Not built. | Blocked by #392 | confirmed |
| Resolve deps in PreStart; reject invalid policies/over-budget before accepting work | no implementado | new-capability | None. | Not built. | Blocked by #391, #383 | confirmed |
| Examples: tenant A exclusive pool, B another, standard shared with quotas | no implementado | new-capability | None in example/. | Not started. | Blocked by #392, #380 | confirmed |
| Contention: noisy Shared stays under quota; Dedicated does not lend | no implementado | new-capability | None. | Not started. | Blocked by #380, #392 | confirmed |
| Aggregate maxima, cancel, reconnect, drain, shutdown, rolling update | no implementado | new-capability | None. | Not started. | Blocked by #391 | confirmed |
| Journal/Feed/destination roles, SharedCell compatible, reject independent destinations | no implementado | new-capability | None. | Not started. | Blocked by #393, #384 | confirmed |
| Comparable bounded-cardinality metrics per policy | no implementado | new-capability | None. | Not started. | Related #378 | confirmed |
| Guide: CPU/IO/locks shared, no availability guarantee | no implementado | new-capability | PRD prd:133 states it, but the criterion asks for a guide; no guide exists. | Guide absent. | Related #386 | corrected: was parcial; a PRD sentence is not the guide deliverable (consistent with #391 row) |
| Reproducible evidence in integration/load lane | no implementado | new-capability | inttest/ holds flows only; no pool/load evidence. | Not started. | Related #385 | confirmed |
| Single tenant matrix unscoped/fixed/multi | no implementado | new-capability | None. | Not started. | Blocked by #424 | confirmed |
Issue verdict: not started.

### #424 [U-TENANCY-MODES] Explicit single and multitenant support in config and core
Dependencies: #346 (done), #347, #349, #383, #384, #390 (open)
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Default config works with no tenant ID/resolver/catalog/tenancy extension | parcial | new-capability | Engine without resolver registers no tenancy marker (engine/option.go:185) and uses Unscoped (readme.md:521); persistence.Unscoped() exists. | No explicit SingleTenantUnscoped mode/default cell/profile; persistence still imports tenancy. | Related #349, #347 | confirmed |
| Fixed identity validated; rejects other identity; multitenant validations kept | parcial | new-capability | tenancy.WithSingleTenant (tenancy/resolver.go:77) and FixedTenantResolver; engine_fixed_tenant_resolver_test.go; compose CapFixedTenant. | Not a configurable mode with scope binding; names pending ADR. | Blocked by #347 | confirmed |
| Unscoped preserves keys/data; identified scope needs explicit adoption | parcial | new-capability | migration/tenant_adoption.go TenantAdopter exists with tests; `TestAdoptionOfLegacyDataRecoversOnPostgres` (inttest/flows/tenancy/adoption_test.go:50) passes in CI run 37635241381. | Event journal only (snapshots and durable state: #435); no explicit no-silent-reinterpretation test. | Related #435 | 01da644: #428 fixed by #429 |
| Selection, cursor, idempotent commands, snapshots, offsets, markers keep explicit scope in all modes | parcial | new-capability | Journal/snapshot/state stores take Scope; offsetstore.WriteOffset (offset_store.go:42) has no scope; no cursor/command-ID/marker model. | Offsets/cursor/commands not scoped. | Related #357, #361 | confirmed |
| Trivial router/default cell, global bounded resource profile per role | no implementado | new-capability | No ScopeRouter or resource profile code (grep router, profile). | Not built. | Blocked by #368, #390 | confirmed |
| Absence of tenancy not confused with missing required extension | parcial | new-capability | extensions.Require applies only to required extensions; tenancy marker is optional. | requiredCapabilities empty; no capability-driven requirement map. | Blocked by #384 | confirmed |
| Matrix memory/testkit + Postgres: unscoped, fixed, multi, replay/crash, incompatible cursor, compatible persistence | parcial | new-capability | testkit/scope_test.go, persistence/conformance scope tests, postgres event_store_test.go. | No cursor test, no fixed matrix; B2 non-zero partition untested for durable state and persisted rows (events published in a cluster are covered by `TestClusterEventPublisherHighPartitionCount`). | Related #388 | 01da644 |
| Authorization remains: omitting tenant ID gives no global privilege | parcial | new-capability | engine_tenant_administrative_scope_test.go (e.g. TestNoTenantIdentityGrantsAdministrativePrivilege:212). | Not verified against the new modes. | Related #347 | confirmed |
| Product/integration/workflow scenarios incorporate modes later | no verificado | new-capability | Deferred by design to those issues. | Out of core scope. | Related #395-#398 | confirmed |
Issue verdict: partial legacy capability (Unscoped, FixedTenantResolver); explicit mode and new guarantees not implemented.


## Persistence (#342)

### #342 [Epic] Journal y lectura correctos
Dependencies: #346, children #348-#363, #377, #378, #390, #392; coordination #343-#345, #395-#398, #424.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Issues del alcance completados o diferidos mediante decisión explícita | no implementado | new-capability | All children listed in the body are OPEN; no deferral decision recorded. | Children not delivered. | Children #348-#363, #377, #378, #390, #392; #346 | confirmed |
| Garantías demostradas por pruebas y condiciones operativas documentadas | no implementado | new-capability | persistence/conformance/events.go:57-74 has no reader, contiguity or idempotency checks; postgres README has no operational conditions. | No proof, no docs. | #348, #352, #354, #357, #360 | confirmed |
| Imports y capacidades respetan la arquitectura | parcial | current-defect | `go list -deps ./persistence` includes urd/tenancy (persistence/scope.go:28) and urd/egopb; rest of persistence imports are clean. | tenancy and egopb coupling remain. | #349, #363, #347 | confirmed |
| Migraciones/compatibilidad y guía de uso actualizadas donde aplique | no implementado | new-capability | Schema dir has 001-006 (006 = scoped offsets, #431); no migration for the target schema, no guide; TenantAdopter works on Postgres for the event journal (#429). | Nothing delivered for this criterion. | #358, #359; #435 | 01da644 |
Issue verdict: epic correctly open; all four closure criteria unmet (one partial).

### #348 [I-02] TCK del lector: omisiones, elegibilidad y progreso
Dependencies: #346 (I-00).
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Check de omisiones falla de forma determinista con adapter Postgres actual, marcado fallo conocido | no implementado | new-capability | No a@100/b@200/late@150 check in EventsStoreChecks; no known-failure marker (only t.Skipf at check.go:78); gap acknowledged in persistence/events_store.go:169-172. Searched "known fail", "late", "omission". | Check plus known-fail mechanism. | #351, #352, #360; none other | confirmed |
| Checks de elegibilidad y progreso no prometen latencia de aplicación | no implementado | new-capability | No eligibility/progress checks in conformance. | Deliverable absent. | #352, #351 | confirmed |
| Todos corren también contra testkit | no implementado | new-capability | testkit/conformance_test.go runs EventsStoreChecks but no reader checks exist; in-memory store has no commit-order gap, needs different oracle. | Checks absent; oracle for testkit undefined. | #396 (testkit expansion epic) | confirmed |
Issue verdict: not started; only timestamp-tie paging checks (#330) exist and they do not cover late commits.

### #349 [I-03] Scope agnóstico de tenant
Dependencies: #347 (I-01, ADR); #346.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| persistence no importa tenancy | no implementado | current-defect | persistence/scope.go:28 imports urd/tenancy; Scope holds `tenant tenancy.TenantID`; `go list -deps ./persistence` lists tenancy. | Scope must become opaque prefix. | #347, #344 | confirmed |
| Clave persistida sigue igual ('' Unscoped, ID del tenant), sin migración | parcial | new-capability | Current mapping holds (postgres/event_store.go scopeKey -> string(scope.TenantID())); guarantee after the refactor cannot be tested yet. | Regression test of the key through the opaque Scope. | #424 | 01da644: #428 fixed |
| La conformance actual pasa | cumplido | met | testkit and inttest/flows/eventstore pass today (audit run). | Re-run after refactor. | none | confirmed |
| (single tenant) Preservar clave Unscoped sin migración; sin tenant ID ficticio; adopción requiere migración explícita | parcial | new-capability | Unscoped '' preserved (scope.go); adoption not automatic per docs/prd/i-00-baseline-develop.md; adoption path recovers on Postgres for the event journal (#429). | Explicit-migration guide; snapshot and durable-state adoption (#435). | #435, #424 | 01da644 |
Issue verdict: preservation behavior exists, structural goal (no tenancy import) unmet.

### #350 [I-04] Slices fijos independientes de GoAkt
Dependencies: #351 (I-05).
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| N decidido y documentado (256 o 1024) | no implementado | new-capability | No N constant or slice function in persistence; searched "slice", "hash(scope", "mod N"; only B2 notes mention it. Prerequisite for guarantee (1) only. | Decision absent. | #351; #387 | confirmed |
| Test de que el slice no cambia con 1, 3 y 5 nodos (guarantee 1: STABILITY across cluster size) | no implementado | new-capability | Shard still `ActorSystem().Partition(entity.persistenceID)` at internal/engine/eventsource/event_sourced_actor.go:355 and durablestate/durable_state_actor.go:169 (01da644); no pure function exists, so nothing to test. B2 in engine/baseline_346_test.go is standalone only. | Pure slice(scope,id,N) function plus table test over topologies (1, 3, 5) showing identical slice. | #351; (#427 fixed: persistenceID is now `behavior.ID()`, so the hash input is the behavior ID, see baseline B2) | 01da644 |
| Plan de migración de shard_number y offsets existentes, alimenta I-09b | no implementado | new-capability | schema 005 keyed `(projection_name, shard_number)`; 006 (#431) adds `tenant_id` to the key (existing rows stay under `''`); no plan doc for shard_number or the offset cut-over. Belongs to neither guarantee below; it is a migration concern. | Plan document. | #359, #362, #370 | confirmed |
Guarantee split (the issue lists only guarantee 1 as a test criterion):
- (1) Stability across 1/3/5 nodes: criteria "N decidido" (prerequisite) and "test 1, 3, 5 nodos". Evidence today: none; the only stored-shard code reads GoAkt Partition, so stability is not provided by design. Test needed: unit test of the pure function independent of any ActorSystem, plus (optional, integration) a cluster of 1/3/5 nodes asserting the same persistenceID yields the same stored Shard.
- (2) Propagation of a non-zero partition value into stored Shard fields: NOT a criterion in the issue body (gap in issue text). Evidence today (`01da644`): `TestBaseline346` B2 only shows shard 0 (zero value, cannot prove propagation); `TestClusterEventPublisherHighPartitionCount` (engine/publisher_test.go:409, assertions at 526-535) does show `GetShard() >= 271` on events published in a cluster, so propagation is covered for published events in a cluster only. Still uncovered: durable-state non-zero Shard, persisted rows, and which identity feeds `Partition` after #430 (it hashes `behavior.ID()` by code reading, unverified by a test). Code writes `entity.shardNumber` at event_sourced_actor.go:355 and durable_state_actor.go:169. Test needed: with a slice function returning a non-zero value (injected stub or real), spawn an event-sourced and a durable-state entity and assert stored Event.Shard and DurableState.Shard equal it; zero-value tests cannot substitute.
- Neither test substitutes for the other: a stable pure function can still never reach storage, and a propagated value can still vary with topology.
Issue verdict: not started; B2 consequence present (shard depends on GoAkt); propagation guarantee should be added to the issue.

### #351 [I-05] Spec: contrato de lectura con prefijo estable
Dependencies: #348 (I-02); related #352, #387.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Contrato nuevo aprobado en la ubicación vigente de especificaciones | parcial | new-capability | docs/prd/urd-platform-prd.md:114 describes stable prefix, OneScope, AllScopesInCell, ErrCursorMismatch; openspec/ has README and config only. | PRD is not an approved spec. | #348, #352, #387 | confirmed |
| No reactivar specs archivadas automáticamente | cumplido | negative-ok | docs/archive/2026-10-06-pre-persistence-refactor preserved; openspec has no active specs. | Maintain constraint. | #341 (archived roadmap) | confirmed |
| Casos de rechazo del cursor definidos | parcial | new-capability | PRD:114 names ErrCursorMismatch and slice-range validation; no enumerated cases. | Enumerate format, cell, fingerprint, range cases. | #360 | confirmed |
| Condiciones de la cota ≤ T declaradas y qué pasa si se rompen | parcial | new-capability | PRD:219 lists dedicated cluster, transaction_timeout, max_prepared_transactions=0, oldest-XID alert; says bound not claimed if violated. | Not in an approved contract; xid8 undecided. | #352, #387 | confirmed |
| Reglas de portabilidad (offset en backend del destino, Tx del destino, slices en particiones, capacidades por adapter) | parcial | new-capability | PRD:130-135 (C-05, capabilities per role, common destination Tx) cover destination Tx and capabilities; slice-grouping in partitions not found. | Spec text and slice grouping rule. | #343, #362, #390 | corrected: was no verificado; PRD lines 130-135 partially cover it |
| Identidad del offset y de la marca de aplicados | parcial | new-capability | PRD:114 and R-01 (PRD:106) define PerScope/SharedCell identity; code keys offsets by `(tenant_id, projection_name, shard_number)` since #431 (migration 006); no processor, version or marks identity. | Spec not approved; code gap narrowed to processor/version/marks. | #362, #343 | 01da644: partly closed by #431 |
| GetShardEvents deprecado con plan de retiro | no implementado | new-capability | persistence/events_store.go:140-175 has no Deprecated marker; doc says the commit-order gap is "a separate change". Searched "Deprecated", "retire", "GetShardEvents" in PRD (none). | Add deprecation and plan. | #370, #360 | confirmed |
| (single tenant) OneScope(Unscoped()) válida; AllScopesInCell privilegiado y distinto | parcial | new-capability | PRD:36 states reads use OneScope(Unscoped()); no OneScope/AllScopesInCell symbol in code. | Spec text only in PRD. | #424 | confirmed |
Issue verdict: requirements drafted in PRD only; no approved contract or deprecation.

### #352 [I-06] Experimento: horizonte xid8 en PostgreSQL
Dependencies: #348, #355 (I-20).
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| 0 omisiones y progreso según I-02 | bloqueado | new-capability | No experiment; needs the I-02 oracle, which does not exist. | Oracle absent. | #348 (open) | confirmed |
| Throughput ≥ 80 % del control en el shard caliente | no implementado | new-capability | benchmark/ has only actor benchmarks; no xid8/reader benchmark; no load model. Searched "xid", "horizon", "hot shard". | Experiment not run. | #355, #387 | confirmed |
| p99 ≤ transaction_timeout + 1 s con transacción larga en otra base | no implementado | new-capability | No such measurement anywhere. | Experiment not run. | #355, #387 | confirmed |
| Resultados en benchmark/ | no implementado | new-capability | No result files. | Not run. | #387 | confirmed |
Issue verdict: not started; xid8 remains a candidate (PRD:217-219).

### #354 [I-19] TCK: append contiguo sin huecos
Dependencies: #346 (I-00).
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Checks de conformance para lote con hueco, lote solapado y lote correcto | no implementado | new-capability | EventsStoreChecks (events.go:57-74) has CAS/genesis checks only; no gap/overlap/correct-batch check. | Add 3 checks. | #396; none other | confirmed |
| Si hoy no se cumple, el adapter se corrige en este issue | no implementado | current-defect | Postgres writeConditional (event_store.go:273-314) checks revision only, no events[0].seq == revision+1 or contiguity: a gap is accepted. Overlap on an existing seq is rejected only by PK (001 schema:17) as a raw insert error, not a conflict. testkit newEventLog (eventstore.go:248-278) accepts gaps and silently overwrites overlaps. | Audit said both adapters accept overlap; Postgres does not (PK), though with a non-typed error. Fix needs explicit contiguity validation in both. | #377 (batching) | confirmed |
Issue verdict: adapters violate the gap criterion by reading; no TCK and no fix.

### #357 [I-23] Identidad de comandos y reintentos
Dependencies: #346 (I-00).
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Commit exitoso con respuesta perdida y reintento devuelve resultado original | no implementado | new-capability | No command_id store or dedupe; searched "idempot", "command_id", "dedup" in persistence and engine (only archived inventory mentions). | Absent. | #377, #381 | confirmed |
| Mismo ID con otra huella rechazado | no implementado | new-capability | Same. | Absent. | #377 | confirmed |
| Comando exitoso sin eventos también registrado | no implementado | new-capability | Same. | Absent. | #377 | confirmed |
| Rechazo de dominio determinista memorizado | no implementado | new-capability | Same. | Absent. | #377 | confirmed |
| Ventana de retención de IDs declarada | no implementado | new-capability | No doc. | Absent. | #370, #381 | confirmed |
| Verificado si command-envelope ya trae el identificador | parcial | new-capability | command/envelope.go and metadata.go carry operation/correlation/causation IDs; archived spec states operation identity is not an idempotency key (docs/archive/.../command-envelope/spec.md:81). Finding not recorded in the issue. | Record the verification result; no client idempotency ID exists. | #345 | confirmed |
| (single tenant) Dedupe con Unscoped y scope fijo; mismo ID en scopes distintos no comparte | no implementado | new-capability | No dedupe. | Absent. | #424 | confirmed |
Issue verdict: not started; envelope has operation identity but intentionally no idempotency key.

### #358 [I-09a] postgres: definición del esquema
Dependencies: #351, #352, #387 (G-A).
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| DDL revisado | bloqueado | new-capability | schema/ has 001-006; no xid8 column, no applied-marks table; mechanism not chosen. | Mechanism undecided. | #387, #352, #351 (all open) | confirmed |
| Plan de consulta sin escaneo secuencial con 1M filas | bloqueado | new-capability | No 1M-row plan test; 002 indexes are single-column. Needs the new schema. | Needs DDL first. | #387, #352, #351 | confirmed |
Issue verdict: blocked by Gate A; no schema work.

### #359 [I-09b] postgres: migración del esquema
Dependencies: #358, #350.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Migración idempotente en persistence/postgres/schema | bloqueado | new-capability | Migrator exists (schema_migrator.go) but target schema (#358) and slice function (#350) do not. | Target undefined. | #358, #350 (open) | confirmed |
| Prueba de actualización desde esquema actual con datos | bloqueado | new-capability | inttest/flows/eventstore/schema_test.go tracks the latest schema version (6, `postgresLatestSchemaVersion`); no upgrade to a target schema exists. | Needs new schema. | #358, #350 | confirmed |
| Plan de corte para offsets existentes | bloqueado | new-capability | No plan. | Needs new offset identity and slices. | #350, #362 | confirmed |
Issue verdict: infrastructure reusable; deliverables blocked.

### #360 [I-08a] persistence: lectura nueva en los adapters
Dependencies: #359 (I-09b).
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Checks de I-02 pasan en ambos adapters | bloqueado | new-capability | Neither the checks nor the reader exist. | Needs #348 checks and migrated schema. | #348, #359 (open) | confirmed |
| Rechazos de cursor de I-05 tienen tests | bloqueado | new-capability | No cursor type; cases not specified. | Needs approved spec. | #351 (open) | confirmed |
| transaction_timeout y max_prepared_transactions = 0 documentados en README del adapter | no implementado | new-capability | persistence/postgres/README.md has no mention (grep empty); only PRD:219. Documentable independently of the reader. | Add text (conditional on xid8 selection). | #352 | confirmed |
Issue verdict: blocked; no new reader exists.

### #363 [I-11] egopb: separar los tipos del journal
Dependencies: #347 (I-01).
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Cierre de dependencias de persistence no incluye mensajes del engine | no implementado | current-defect | `go list -deps ./persistence` includes urd/egopb; protos/ego/ego.proto mixes Event/Offset/Snapshot with CommandReply, StateReply, TenantBinding*. | Split journal types. | #347 | confirmed |
| Decisión del ADR (I-01) aplicada | bloqueado | new-capability | ADR #347 is open; no ADR in docs. | Decision absent. | #347 (open) | confirmed |
Issue verdict: not started; shared egopb package confirmed.

### #377 [P-HELPER] Helper de actor persistente
Dependencies: #349, #354, #357, #383.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Recuperación de snapshot+journal antes de aceptar comandos | parcial | new-capability | Exists inside Urd's actor (event_sourced_actor.go PreStart, recover, recoverFromSnapshot); restart test in inttest/flows/restart. | Not extracted as generic helper; not via GoAkt extensions. | #383 | confirmed |
| Stash durante append tiene límite y rechazo inmediato | no implementado | current-defect | ctx.Stash() at event_sourced_actor.go:387,397,913,1098,1481,1573,1735,1878 with no bound or rejection; grep for limit/max near stash empty. | Unbounded stash. | #378 (similar admission theme) | confirmed |
| Batching mantiene append contiguo e idempotencia | parcial | new-capability | Batching exists (flushBatch, TestResolveBatchPrecondition); contiguity not enforced by stores; no idempotency. | Depends on store guarantees. | #354, #357 | confirmed |
| Fallas de persist/reinicio probadas | parcial | new-capability | TestPendingRequestsAreAnsweredWhenTheWriteFails, events_writer_actor_test.go, restart test over Postgres. | Not against the extracted helper; no fault matrix. | #388 | confirmed |
| Aislamiento de snapshots por scope | parcial | new-capability | Store-level only: persistence/conformance/snapshot.go isolation checks; event_sourced_actor_scope_test.go. The criterion targets the helper, which does not exist. | Helper-level scope test after extraction. | #349, #383 | corrected: was cumplido; the criterion concerns the helper that is not extracted |
Issue verdict: behavior lives inside Urd handlers; helper not extracted; stash unbounded.

### #378 [P-POOL] Acotar operaciones en vuelo y espera del pool PostgreSQL
Dependencies: #355 (I-20), #390, #384, #380 (compose), #424.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Máximo de operaciones y solicitudes esperando conexión configurables | no implementado | new-capability | Only hard-coded `cfg.MaxConns = 20` (postgres/event_store.go:81, offset_store.go:57); no waiter limit. Searched "MaxConns", "waiter", "semaphore". | No config surface. | #355, #384 | confirmed |
| Al llenar, rechazo inmediato identificable | no implementado | new-capability | pgxpool blocks until ctx ends; no sentinel error. | Absent. | #355 | confirmed |
| Deadline adicional, cancelación libera cupo | no implementado | new-capability | Only caller ctx; no admission permit. | Absent. | none | confirmed |
| Saturación no produce espera/memoria ilimitada | no implementado | current-defect | Unbounded waiters in pgxpool; stash also unbounded (see #377). | Absent. | #377 | confirmed |
| Métricas de ocupación y rechazos | no implementado | new-capability | No pool metrics in postgres module. | Absent. | #395 | confirmed |
| (ext) Cupos por Scope opaco; límites scope y pool Shared sin importar tenancy | no implementado | new-capability | No quota code. | Needs SPI. | #390, #380, #349 | confirmed |
| (ext) Sin tenancy mantener límites del pool; scope no crea pool | parcial | new-capability | Fixed MaxConns=20 applies regardless and there is a single pool per store. | Not configurable. | #424 | confirmed |
| (ext) Release y Commit/Rollback en toda ruta | parcial | new-capability | `defer tx.Rollback` in writeUnconditional/writeConditional; other paths and permit release not audited (no permits exist). | Audit remaining paths. | none | confirmed |
| (ext) Config Shared con máximo finito y presupuesto validable | no implementado | new-capability | Hard-coded 20; no budget validation. | Absent. | #384, #391 | confirmed |
| (ext) Métricas acotadas sin DSN | no implementado | new-capability | None. | Absent. | none | confirmed |
| (single tenant) Cupos genéricos sin tenant ID | no implementado | new-capability | None. | Absent. | #424 | confirmed |
Issue verdict: not started; only a fixed pool size exists.

### #390 [P-POOL-SPI] Contrato de selección de recursos por Scope, celda y rol
Dependencies: #346, #347, #355; complements #349, #378.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Contratos y ADR distinguen política, selección, admisión y lifecycle | no implementado | new-capability | No selector or ADR; only PRD C-02 (PRD:127). | Absent. | #347 | confirmed |
| Configuración por defecto Shared conserva el comportamiento existente | no implementado | new-capability | No SPI or default config exists to compare; a vacuous truth is not credited. | SPI absent. | #378 | corrected: was no verificado; nothing exists to verify |
| Journal/Feed y ProjectionDestination con identidades y capacidades independientes | no implementado | new-capability | No role types; PRD C-02 only. | Absent. | #384, #343 | confirmed |
| Context cancelable, errores de saturación/configuración y propiedad de release especificados | no implementado | new-capability | Not specified. | Absent. | #378 | confirmed |
| Identidad de destino transaccional coherente verificable | no implementado | new-capability | None. | Absent. | #343, #362 | confirmed |
| Ejemplos Shared, Dedicated y DedicatedCell sin importar tenancy | no implementado | new-capability | None. | Absent. | #368 | confirmed |
| Tests de contrato con mocks y registro de garantías/limitaciones | no implementado | new-capability | None. | Absent. | #396 | confirmed |
| (single tenant) Selector acepta Unscoped y perfil default | no implementado | new-capability | None. | Absent. | #424 | confirmed |
Issue verdict: not started.

### #392 [P-POOL-DEDICATED] Registry y lifecycle de pools exclusivos por scope
Dependencies: #390, #391, #368, #379, #380, #378, #388.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Pool exclusivo por identidad de recurso y scope con máximo obligatorio | bloqueado | new-capability | No registry; needs SPI and admission core. | Phase 2. | #390, #378 (open) | confirmed |
| Creación lazy concurrente produce una sola instancia; creación/espera acotadas | bloqueado | new-capability | Absent. | Phase 2. | #390, #378 | confirmed |
| Reserva presupuestaria admitida antes de crear; fallo libera | bloqueado | new-capability | Absent. | Budget validation absent. | #391 (open) | confirmed |
| Conexiones dedicadas no se prestan a otro tenant; sin fallback silencioso al Shared | bloqueado | new-capability | Absent. | Needs registry. | #390, #368 | confirmed |
| Idle eviction/rotación/cierre drenan conexiones y Tx | bloqueado | new-capability | Absent. | Needs registry. | #390, #379 | confirmed |
| Cambios de credenciales o celda con identidad/versionado y drenaje | bloqueado | new-capability | Absent. | Needs registry and cell routing. | #368, #390 | confirmed |
| PostStop de un actor no cierra un pool compartido | bloqueado | new-capability | Absent; today Disconnect closes the store's own pool (postgres/event_store.go ~104-106). | Needs registry owned by extension. | #383, #390 | confirmed |
| Métricas con cardinalidad acotada y sin secretos | bloqueado | new-capability | Absent. | Needs pool registry and metrics base. | #378, #380 | confirmed |
| Mínimo configurado no se presenta como garantía de disponibilidad | bloqueado | new-capability | Absent. | Documentation tied to implementation. | #390 | confirmed |
Issue verdict: phase 2, all rows blocked by open #390/#391/#378 and not started.


## Projection (#343) and tenancy (#344)

### #343 Epic: projection engine
Dependencies: children #356 #361 #362 #364 #365 #367 #370 #371 #373 #374 #375 #376 #393; coordination #342 #344 #345 #346 #395-#398 #423 #424.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Issues del alcance completados o diferidos | no implementado | new-capability | All listed children are OPEN and unchecked in the body; #362/#371 audited elsewhere. | No deferral decision recorded either. | children above | confirmed |
| Garantías demostradas por pruebas y condiciones operativas documentadas | no implementado | new-capability | Only at-least-once tested (runner_test.go, TestRunnerPagesThroughTimestampTies); no atomicity, fencing, parking, crash tests exist. | Guarantees and operating conditions undocumented. | #373 #374 #375 | confirmed |
| Imports y capacidades respetan la arquitectura | parcial | new-capability | TestArchitectureProjectionRunnerStaysRuntimeNeutral (runner_test.go:1736) forbids only GoAkt, engine, internal/extensions; passes. | Runner still imports encryption, eventadapter, eventstream, internal/instrumentation, projection. | #364 #365 #353 | confirmed |
| Migraciones/compatibilidad y guía de uso actualizadas | no implementado | new-capability | Offsets schema gained `tenant_id` in 006 (#431) but nothing for identity/marks; no read-side guide. | Nothing to migrate until identity/marks exist. | #362 #373 #386 #359 | confirmed |

Issue verdict: epic not closable; all four criteria open or partial.

### #356 [I-22] Error policy and parked-entity contract
Dependencies: #348 (I-02), epic #343; implementation in #375.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Contrato y casos de TCK: falla 5 y llega 6, marca permanece en 4 | no implementado | new-capability | projection/recovery.go has only Fail/RetryAndFail/Skip/RetryAndSkip; testkit/offsetstore.go has no parking case; grep park/estacion/quarantine: nothing in code. | No contract text outside issue diagram/PRD, no TCK case. | #375 #348 #396 | confirmed |
| otras entidades progresan | no implementado | new-capability | processEvents (runner.go:672) returns eventError on first failure, halting the shard; Skip/RetryAndSkip dead-letter the event. | Runner halts or skips; never parks one entity. | #375 #373 | confirmed |
| Estados estacionada->recuperando->activa, máximo de estacionadas y auditoría de saltos definidos | no implementado | new-capability | No states, limit or audit in code or docs/prd (only PRD mention). | Nothing defined. | #375 #417 | confirmed |
| Replay incluye evento 7 concurrente | no implementado | new-capability | No replay-of-parked design anywhere. | Not defined. | #375 | confirmed |
| Implementación transaccional en P-ERROR | bloqueado | new-capability | Pointer criterion: deliverable lives in #375. | Cannot start before #375, which needs #373/#374. | #375 (open) | confirmed |
| este ticket no exige tablas antes del esquema | cumplido | negative-ok | No parking tables; schema 005 untouched. | None. | #359 #358 | confirmed |

Issue verdict: design deliverable absent; current policies are halt or per-event skip.

### #361 [I-08b] Runner integrates new read
Dependencies: #360, #362, #375, #373, #374.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| el runner ya no llama a GetShardEvents | no implementado | new-capability | runner.go:596 `x.eventsStore.GetShardEvents(ctx, x.scope, shard, currOffset, maxBufferSize)`. | New read from #360 absent from persistence. | #360 #348 #351 | confirmed |
| métricas de lag de elegibilidad y de procesamiento expuestas | parcial | new-capability | Single gauge urd.projection.lag_ms (instrumentation.go:92), set at runner.go:619 as wall clock minus offset; TestProjectionRunnerLagMetrics exists. | Two lags not separated; eligibility lag has no source. | #360 #415 | confirmed |
| batches acotados por cantidad, bytes y tiempo | parcial | new-capability | WithMaxBufferSize (option.go:75) is event count only; no byte or time option. | No byte or time bound. | #355 | confirmed |
| la conformance pasa de punta a punta | bloqueado | new-capability | No conformance for the new read exists. | Needs the read, identity, parking, Tx, fence. | #360 #362 #373 #374 #375 #348 | confirmed |

Issue verdict: not started.

### #364 [I-12] Decouple the runner
Dependencies: #353 (I-07); brecha B6.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| internal/projectionrunner importa solo persistence, offsetstore y egopb | no implementado | new-capability | runner.go:47-54 imports egopb, encryption, eventadapter, eventstream, internal/instrumentation, offsetstore, persistence, projection (five extras, not four). | Arch test does not check this allowlist. | #353 #365 | confirmed (evidence count corrected) |
| Urd arma el decodificador con cifrado y adapters de eventos | no implementado | new-capability | Runner decrypts and adapts itself (processEnvelope, runner.go:682); engine/projection/projection_actor.go:113,131 passes WithEventAdapters/WithEncryptor. | No injected decoder, no metrics interface. | #353 | confirmed |

Issue verdict: not met.

### #365 [I-13] Single root package
Dependencies: #362, #363, #364.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| projection importa solo persistence (más egopb) | no implementado | new-capability | offsetstore/ and projection/ are separate roots; runner still internal with extra imports. | Nothing moved, no compat aliases. | #364 #362 #363 | confirmed |
| las excepciones de I-07 para projection se eliminan | bloqueado | new-capability | I-07 (#353) exceptions not yet created or removable. | Cannot remove before #353 and #364 land. | #353 #364 #362 | confirmed |

Issue verdict: not started.

### #367 [I-15] Slice-range distribution
Dependencies: #350, #366, #374, #388.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| reasignación al entrar o salir un nodo | no implementado | new-capability | engine/projections.go:95 SpawnSingleton: one actor walks all shards; numWorkers=5 pool is local (runner.go:58). | No range ownership or rebalance. | #350 #374 #366 | confirmed |
| el token se valida al escribir efectos y offsets, y un ejecutor que perdió el rango es rechazado en ambos | bloqueado | new-capability | No token anywhere; WriteOffset takes no token. | Needs fencing primitive first. | #374 | confirmed |
| un procesador cuyo destino no valida el token se declara de un solo ejecutor | parcial | new-capability | Singleton is the only mode (de facto single executor); no capability declaration. | No destination flag. | #374 #384 | confirmed |

Issue verdict: not met; singleton only.

### #370 [I-24] Journal retention and version cut
Dependencies: #362, #366, #388.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| política de retención que impide a DeleteEvents borrar eventos que una versión vigente necesita | no implementado | current-defect | events_janitor_actor.go:125 deletes from snapshot retention count; no offset lookup (grep). Equivalent term searches (retention barrier, min offset) found nothing. | Janitor can delete events a projection has not read today. | #362 #403 #357 | confirmed |
| corte con barrera por slice (pausa, barrera junto al puntero, verificación de offsets en la Tx) | no implementado | new-capability | No projection version, pointer or barrier in code. | Absent. | #362 #366 #381 | confirmed |
| condición de pendientes: la nueva no activa entidades estacionadas que la vieja no tenía | bloqueado | new-capability | No parked-entity concept exists. | Needs parking and version identity. | #375 #362 | confirmed |
| prueba con escrituras concurrentes durante el corte donde los lectores nunca retroceden | no implementado | new-capability | No such test. | Absent. | #370 own scope; #406 #388 | confirmed |

Issue verdict: not met; janitor-versus-projection hazard is a live defect with no dedicated issue other than #370.

### #373 [P-TX] Effect, mark and offset in destination Tx
Dependencies: #362, #359.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Un commit incluye efecto, marca por (procesador, versión, scope, entidad) y offset con identidad completa | no implementado | new-capability | processEvents runs handlers then commitOffset (runner.go:806) via WriteOffset; Handler.Handle has no Tx; offset key name+shard only. | No Tx handle, no marks, no identity. | #362 #359 #366 | confirmed |
| Rollback no deja progreso parcial | no implementado | new-capability | Handler effects persist while offset is simply not written; at-least-once. | Not atomic. | #366 | confirmed |
| Duplicado no repite efecto | no implementado | new-capability | Re-pulled batch re-invokes handler; no applied mark. | No dedup. | #366 | confirmed |
| dos procesadores no comparten marcas | no implementado | new-capability | No marks exist. | n/a. | #362 | confirmed |
| Pruebas de crash antes/después de commit y capacidad declarada para destinos sin Tx común | no implementado | new-capability | No crash tests in runner_test.go; no capability declaration. | Absent. | #406 #384 #408 | confirmed |

Issue verdict: not met; at-least-once with separate offset write.

### #374 [P-FENCE] Ownership and fencing
Dependencies: #373, #350.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Ejecutor obsoleto es rechazado al escribir efecto, marca y offset | no implementado | new-capability | WriteOffset has no token; runner.go:141 comment relies on cluster singleton as sole writer. | Only singleton guards it; no write-time validation. | #373 #350 | confirmed |
| validación junto a la mutación, sin ventana check-then-write | no implementado | new-capability | None. | Absent. | #373 | confirmed |
| Fallas inyectadas en renovación/transferencia | no implementado | new-capability | None. | Absent. | #406 #388 | confirmed |
| Destino sin fencing obliga modo de un ejecutor | parcial | new-capability | Singleton mode exists as only mode; no destination capability to force or declare it. | No declaration or enforcement. | #373 #384 | confirmed |

Issue verdict: not met; migration AdoptionFence (tenant_adoption.go:423 WithAdoptionFence) is an unrelated per-aggregate lock.

### #375 [P-ERROR] Parking and ordered replay
Dependencies: #356, #359, #373, #374.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Estado de estacionamiento, marca y offset se actualizan atómicamente | no implementado | new-capability | No parking store or marks. | Absent. | #373 #356 | confirmed |
| Falla 5/llega 6 deja marca=4 y otras entidades avanzan | no implementado | new-capability | Runner halts on error or dead-letters (handleWithPolicy, runner.go:719). | No per-entity gating. | #356 | confirmed |
| límite activa parada del rango | no implementado | new-capability | None. | Absent. | #356 | confirmed |
| Replay 5/6 con 7 concurrente usa marca condicional y mismo fence | no implementado | new-capability | None. | Absent. | #374 | confirmed |
| salto manual auditado | no implementado | new-capability | Skip policy is automatic; DeadLetterHandler only receives events, no audit trail. | Absent. | #416 #417 #398 | confirmed |

Issue verdict: not started.

### #376 [P-PREP] Idempotent preparation
Dependencies: #374, #359.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Estado pendiente/listo por procesador/versión y preparación por rango | no implementado | new-capability | No prepare hook in projection.Handler, no state table. | Absent. | #359 #374 | confirmed |
| Una caída a mitad se reintenta sin duplicar estado | no implementado | new-capability | None. | Absent. | #374 | confirmed |
| no ejecutar handlers antes de listo | no implementado | new-capability | Runner invokes handler as soon as Start/Run begins. | Absent. | #374 | confirmed |
| propietario obsoleto no completa preparación | bloqueado | new-capability | No ownership concept. | Needs fencing. | #374 | confirmed |

Issue verdict: not started.

### #393 [P-POOL-ROUTING] Destination pool selection
Dependencies: #390 #392 #368 #373 #374 #380 #388.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Resolver pool del destino antes de abrir Tx según scope y política | no implementado | new-capability | No destination/pool concept in runner; handler gets no Tx. | Absent. | #390 #373 #378 | confirmed |
| La misma Tx conserva efecto, marca, offset y validación de fencing | bloqueado | new-capability | No Tx, marks or fence exist. | Needs both primitives. | #373 #374 | confirmed |
| No reutilizar por accidente pool de Journal/Feed como destino | no implementado | new-capability | No backend roles. | Absent. | #390 #384 | confirmed |
| Batches con scopes diferentes definen partición/admisión/selección | no implementado | new-capability | Runner is single-scope (WithScope, ErrScopeRequired; TestRunnerRequiresAScope run: ok). | No multi-scope mode. | #368 #390 | confirmed |
| SharedCell con pools tenant distintos al mismo destino coherente: pruebas explícitas | no implementado | new-capability | No SharedCell symbol anywhere (grep). | Absent. | #390 #392 | confirmed |
| SharedCell con destinos independientes bajo checkpoint compartido se rechaza con error accionable | no implementado | new-capability | None. | Absent. | #390 #394 | confirmed |
| PerScope soporta selección de destino compatible con su propio checkpoint | no implementado | new-capability | One scope per runner exists, but offsets are not scope-keyed and no destination selection exists. | Own-checkpoint identity missing. | #362 #390 #424 | corrected: was parcial; the criterion is destination selection and scope-keyed checkpoint, neither exists |
| Saturación/cancelación de un pool no avanza offsets | bloqueado | new-capability | No pool or Tx path. | Needs Tx and parking ordering. | #373 #375 #378 | confirmed |

Issue verdict: not started.

### #344 Epic: tenancy
Dependencies: children #368 #369 #379 #380 #381 #382; coordination #342 #343 #345 #346 #388-#398 #424.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Issues del alcance completados o diferidos | no implementado | new-capability | All six children open; #379/#381 audited elsewhere. | None deferred. | children above | confirmed |
| Garantías demostradas por pruebas y condiciones operativas documentadas | parcial | new-capability | Isolation conformance exists (persistence/conformance/events.go, inttest/flows/tenancy/conformance_test.go). | Cells, quotas, deletion, rebuild untested. | #368 #380 #382 #381 | confirmed |
| Imports y capacidades respetan la arquitectura | parcial | new-capability | engine/tenancy_architecture_test.go TestArchitectureTenancy (go list -deps ./tenancy/..., stdlib only) exists and passes. | No guard for future router/quota capabilities, which do not exist. | #368 #380 | corrected: was no verificado; an import guard exists and passes |
| Migraciones/compatibilidad y guía de uso actualizadas | parcial | new-capability | migration/tenant_adoption.go exists; inttest/flows/tenancy/adoption_test.go:50 `TestAdoptionOfLegacyDataRecoversOnPostgres` proves recovery after adoption (event journal). | No guide; snapshots and durable state (#435). | #435 #424 #386 | 01da644: #428 fixed by #429 |

Issue verdict: epic partly grounded (isolation, adoption) but phase 2 scope absent.

### #368 [I-16] ScopeRouter and cells
Dependencies: #349, #360, #388.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| test de un tenant asignado a otra celda sin cambios en el engine | no implementado | new-capability | grep ScopeRouter/cell/SharedCell: no match; persistence.Scope has Unscoped/Tenant only. | No router or cells. | #349 #390 | confirmed |
| la configuración por defecto se comporta como hoy | no implementado | new-capability | No router abstraction to default. | Absent. | #349 | confirmed |
| los slices no cambian al cambiar la celda | parcial | new-capability | Slice = ActorSystem().Partition(persistenceID) (event_sourced_actor.go:353), independent of tenant/storage. | Property holds but no cell notion or test. | #350 | confirmed |
| Distinguir roles del backend y selección de Journal/Feed frente al destino | no implementado | new-capability | No roles. | Absent. | #390 #384 | confirmed |
| Incluir identidad/versionado del recurso para migración y drenaje | no implementado | new-capability | None. | Absent. | #390 #369 | confirmed |
| Cambiar solo pool no exige PerScope ni cambia slices | no implementado | new-capability | No pool selection or PerScope exists; verification deferred to #393. | Absent; #393 is a verifier, not a declared dependency. | #390 #393 | corrected: was bloqueado; #393 is not a listed dependency and the pool SPI (#390) is the real prerequisite, so nothing is concretely in progress |
| Router trivial/celda por defecto soporta Unscoped sin catálogo, lookup ni routing dinámico | no implementado | new-capability | No router. | Absent. | #424 #349 | confirmed |

Issue verdict: not started.

### #369 [I-21] Tenant migration between cells
Dependencies: #367, #368.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| prueba de punta a punta: corte de escrituras, copia preservando (scope, entidad, seqNr), transferencia de ownership con fencing nuevo, invalidación de cursores xid8, retoma, reapertura | no implementado | new-capability | No such e2e test. Precursor tenant_adoption.go copies/verifies within one store; its tests pass, including over Postgres since #429. | No write cut-off, ownership transfer, cursor invalidation, cells. | #367 #368 #374 #352 | corrected: was parcial; the criterion is an e2e protocol test none of whose cut-off/ownership/cursor steps exist |
| cero omisiones según el oráculo | no implementado | new-capability | No oracle code in repo (grep oracle/oráculo in *.go: none). | Oracle absent, not merely unverified. | #406 #348 #428 | corrected: was no verificado; there is nothing to verify, no oracle exists |

Issue verdict: only an in-store adoption precursor exists; protocol not implemented.

### #380 [T-QUOTA] Quotas and admission
Dependencies: #355, #379, #368, #390.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Cuotas configurables por tenant | no implementado | new-capability | grep quota/token bucket/rate limit/semaphore: only unrelated "admission gate" ExpectedRevision logic. | Absent. | #355 #378 #417 | confirmed |
| máximo de concurrencia/espera | no implementado | new-capability | None. | Absent. | #378 | confirmed |
| rechazo inmediato medido | no implementado | new-capability | None. | Absent. | #355 | confirmed |
| Prueba de tenant ruidoso junto a tenant dentro de cuota y métricas por clase | no implementado | new-capability | None. | Absent. | #385 #394 | confirmed |
| No construir HTTP/gRPC nuevos | cumplido | negative-ok | No quota transport added. | Vacuous. | none | confirmed |
| Tenancy produce perfiles Shared o Dedicated por tenant y rol; no importa pgx | no implementado | new-capability | No profiles. | Absent. | #390 #392 #394 | confirmed |
| Shared aplica cupos de concurrencia y espera por scope además del límite del pool | no implementado | new-capability | None. | Absent. | #378 | confirmed |
| Dedicated reserva presupuesto y usa pool exclusivo con máximo obligatorio | no implementado | new-capability | None. | Absent. | #392 #391 | confirmed |
| DedicatedCell combina routing hacia celda dedicada y política de pool | bloqueado | new-capability | No router. | Routing half depends on ScopeRouter. | #368 #392 | confirmed |
| Cupos de token bucket y de adquisición de conexión se distinguen | no implementado | new-capability | None. | Absent. | #378 | confirmed |
| Reservas aceptadas entran en el presupuesto de deployment | no implementado | new-capability | None. | Absent. | #391 | confirmed |
| No afirmar aislamiento CPU/IO/locks ni disponibilidad garantizada | cumplido | negative-ok | No such claim found in code or non-PRD docs. | Vacuous. | none | confirmed |
| Aplicar políticas por tenant solo cuando tenancy está configurada; unscoped usa perfil default acotado | no implementado | new-capability | No policies. | Absent. | #424 #368 | confirmed |

Issue verdict: not started.

### #382 [T-DELETE] Coordinated tenant deletion
Dependencies: #379, #368, #374, #370.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Política de retención/auditoría aprobada antes de borrar | no implementado | new-capability | No policy doc or code. | Absent. | #370 #403 | confirmed |
| Revocar acceso y drenar escrituras | no implementado | new-capability | grep delete/purge tenant: none. | Absent. | #379 #368 | confirmed |
| detener ownership | bloqueado | new-capability | No ownership concept. | Needs fencing. | #374 | confirmed |
| eliminar journal, snapshots, offsets, marcas y read models según política | parcial | new-capability | Per-id scoped DeleteEvents/snapshot delete tested (persistence/conformance/events.go:159, snapshot.go:145). | No tenant-wide purge; offsets have no tenant column; no marks. | #362 #373 | confirmed |
| Operación reanudable/idempotente y solicitudes antiguas no recrean tenant | no implementado | new-capability | None. | Absent. | #416 | confirmed |

Issue verdict: only per-entity scoped deletes exist; no coordinated deletion.


## Integration (#395) and testkit (#396)

### #395 [Epic][integration] Publicacion durable y outbox sobre projection
Dependencies: children #399-#403; coordination #342-#345; validation #346, #347. Related: #17 (closed), #424.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Tareas completadas o diferidas por decision explicita | no implementado | new-capability | All five children open; no outbox/relay/envelope in non-test Go. | #399-#403 pending. | #399-#403 | confirmed |
| Compatibilidad con capacidades existentes y evidencia de recuperacion/aislamiento | parcial | new-capability | Existing publish path untouched; tenant delivery check `engine/streams.go:90-115`; PT-4 scope conformance in `publishingtest.go`. | No recovery/isolation evidence for outbox, which does not exist. | #403, #401 | confirmed |
| Imports respetan ADR y lifecycle se integra mediante extensiones GoAkt | bloqueado | new-capability | No integration package exists; ADR not accepted. | Rule cannot be checked until ADR and package exist. | #347 (ADR open), #399 | confirmed |
| Guia y escenarios ejecutables reflejan garantias reales | no implementado | new-capability | No durable-publication guide; `port/publishing/publishing.go` states no delivery semantics (baseline: "responsible for ensuring delivered"). | Guide/scenarios pending. | #403, #386 | confirmed |
| Single tenant: "Productor, envelope, outbox y relay admiten scope Unscoped o fijo" | no implementado | new-capability | None of those components exist. | Via children. | #399, #400, #401, #424 | confirmed |

Issue verdict: not started; today's publisher path is non-durable in-memory fan-out and a failed publish is dropped (current-defect).

### #399 [integration][IN-CONTRACT] Contrato de eventos de integracion y ACK por adapter
Dependencies: #395, #346, #347, #357, #384, #388. Related: #424.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Envelope con Scope opaco, ID estable, version, topic/key, causation/correlation, payload versionado | no implementado | new-capability | `egopb.Event` (`protos/ego/ego.proto:10-33`) has persistence_id, seq, tenant_metadata only; causation/correlation only in `command/carrier.go:41-42`. | Envelope and mapper absent. | #357 | confirmed |
| Transformacion journal->integration-event explicita y reproducible; evolucion documentada | no implementado | new-capability | No mapper or injection point; `Handler.Handle` (`projection/handler.go:57`) receives `anypb.Any` only. | Mapper contract absent. | #366, #361 | confirmed |
| Declarar capacidades ACK/fallo de cada publisher | no implementado | new-capability | `port/adapter/adapter.go:73,78` only `CapStart`, `CapReady`; no ACK capability; port doc silent on ACK. Searched ACK/Ack in port/: none. | ACK capability absent. | #384, #402 | confirmed |
| Reintentos conservan ID y destino logico; destino requiere dedupe | no implementado | new-capability | Publish has no retry nor event ID; failure dropped (`engine/streams.go:486-493`); Kafka key = persistence_id (`kafka.go:100-102`). | No ID, no retry layer. | #401, #400 | confirmed |
| No prometer exactly-once externo ni incorporar consumidores/brokers nuevos | cumplido | negative-ok | grep: no exactly-once claim in port/publishing or publisher/*; only the four existing publishers. | Re-check when the contract text is written. | #403 | confirmed |
| ID estable incluye productor logico, Scope, evento/comando fuente y clave/ordinal de salida | no implementado | new-capability | No such ID anywhere (searched id/envelope/ordinal terms). | Absent. | #357, #400 | confirmed |
| Politica de version/rebuild: reejecutar proyeccion no republica | no implementado | new-capability | `Engine.RebuildProjection` resets offset by name (`engine/projections.go:172-185`); no dedupe or publication-decision policy. | Policy absent. | #381, #362, #370 | confirmed |
| Single tenant: "Envelope/identidad admite Unscoped o fixed, sin campo tenant obligatorio" | no implementado | new-capability | Unscoped scope supported (`engine/streams.go:93-97`) but no envelope exists. | Envelope absent. | #424, #349 | confirmed |

Issue verdict: not implemented; contract and identity are green field.

### #400 [integration][IN-OUTBOX] Persistir intents outbox junto a marca y offset en Tx destino
Dependencies: #395, #399, #360, #361, #373, #374. Related: #352 (commit-order gap in GetShardEvents), baseline B2.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Camino v1 journal->EventReader->runner->Tx destino escribe intent+marca+offset | bloqueado | new-capability | Runner calls `Handle` (`runner.go:775`) then separate `WriteOffset` (`:814`); no Tx, no applied mark. | Needs Tx contract and new read path. | #373 (P-TX), #360, #361 | confirmed |
| Handle puede escribir intent en su misma Tx del destino | bloqueado | new-capability | `Handler.Handle(ctx, id, *anypb.Any, rev)` has no Tx (`projection/handler.go:57`). | Tx-aware handler needed. | #373, #366 | confirmed |
| Clave estable por scope/destino/evento previene intent duplicado en replay | no implementado | new-capability | No intent table or key; searched outbox/intent/idempotency key: no hits. | Needs identity first. | #399 | confirmed |
| Esquema, estados y migracion compatibles; no nuevo store paralelo | no implementado | new-capability | No outbox table in `persistence/postgres/schema`; `schema_migrator.go` exists as base. | Schema pending. | #358, #359 | confirmed |
| Rollback impide intent huerfano y avance de offset; un solo destino coherente | bloqueado | new-capability | Offset store is a separate port (`offsetstore/offset_store.go`); no shared Tx. | Same Tx needed. | #373 | confirmed |
| Pruebas de commit/rollback/crash y aislamiento multitenant en integracion | no implementado | new-capability | `inttest/flows/{eventstore,restart,tenancy}` have no outbox tests; restart flow stops the node gracefully, no kill. | Tests and crash support missing. | #407, #406 | confirmed |
| ID estable incluye productor, Scope, evento fuente, ordinal | no implementado | new-capability | Same as #399. | Same. | #399 | confirmed |
| Politica de version/rebuild sin republicar | no implementado | new-capability | Same as #399 (`RebuildProjection` resets by name). | Same. | #399, #381 | confirmed |

Issue verdict: not implemented; blocked by Tx contract #373 and new read path #360/#361; per-shard reads also affected by B2 and #352.

### #401 [integration][IN-RELAY] Dispatcher outbox fenced con reintentos acotados
Dependencies: #395, #400, #374, #375, #378.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Claim/lease y fence; owner obsoleto no confirma delivery | bloqueado | new-capability | No fence/lease symbols in non-test Go (grep). | Fence contract and outbox absent. | #374 (P-FENCE), #400 | confirmed |
| Enviar->ACK declarado->marcar delivered; caida tras ACK implica duplicado, conserva ID | no implementado | new-capability | Engine sends and forgets (`engine/streams.go:486-500`); no delivered mark, no ID. | Whole path absent. | #399, #400 | confirmed |
| Maximos de operaciones/espera/batch, backoff cancelable, politicas error/parking configurables | no implementado | new-capability | Projection runner has buffer/pull/retry options (`option.go:67,75,114`, `retry.go`) but none for publishing. | No dispatcher; parking and pool bounds absent. | #375, #356, #378 | confirmed |
| No perder intent ante timeout/cancelacion ni agotar memoria; polling obligatorio | no implementado | current-defect | A failed publish is logged and dropped, no retry (`engine/streams.go:486-493`); no intent store to retain. | Event lost for that publisher permanently. | #400, #402 | confirmed |
| Lifecycle shutdown drena trabajo y libera recursos; scope y rol backend | no implementado | new-capability | Only publisher Close via `compose/goakt/app.go` `releasePublishers` (:454); no dispatcher. | Drain logic absent. | #378, #390 | confirmed |
| Oraculos de fallos antes envio/despues envio/despues ACK y takeover | bloqueado | new-capability | No fault drivers or fence in repo. | Needs driver and fence. | #406, #374 | confirmed |

Issue verdict: not implemented; the dropped-publish behavior at `engine/streams.go:486-493` is the live defect this closes.

### #402 [integration][IN-PUBLISH] Adaptar publishers existentes al contrato durable
Dependencies: #395, #399, #384.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Inventario de publishers distingue ACK broker/durable, memoria e incompatibles | parcial | new-capability | Kafka sync producer, WaitForAll, idempotent (`config.go:79-85`, `kafka.go:113`); NATS JetStream ack (`nats.go:148`); Pulsar `Send` receipt (`pulsar.go:120`); WebSocket plain write (`websocket.go:112-120`). | No committed classification document. | #404, #346 | confirmed |
| Adapters conservan Scope/key/ID y causation; retry no regenera IDs | parcial | new-capability | Kafka and Pulsar key = persistence_id; scope only in payload `tenant_metadata`; NATS no key; no event ID or causation. | No stable ID/causation header; no retry layer. | #399, #357 | confirmed |
| Capacidad ACK/fallo declarada y verificada con conformance; sin ACK adecuado no es durable | no implementado | new-capability | `publishingtest` PT-1..PT-4 only (`publishingtest.go:23-45`), no failure/ack check; only `publisher/websocket` imports it (kafka/nats/pulsar do not). | ACK capability, conformance and gating absent. | #384, #399 | confirmed |
| No crear broker deployment ni transportes; simulador/mock en unitarios | cumplido | negative-ok | Only the four existing adapters; websocket tests use httptest; no new transport or broker. | None. | #407 | confirmed |
| Urd compone adapters; integration no importa root | parcial | new-capability | `compose/goakt` composes publishers; publisher modules carry architecture tests. | No integration module to check. | #347, #384 | confirmed |

Issue verdict: partial groundwork; durable-ACK declaration and conformance missing.

### #403 [integration][IN-LIFECYCLE] Recuperacion, retencion y lifecycle multitenant
Dependencies: #395, #401, #402, #370, #369, #382, #380. Related: #424, #435.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Matriz crash/restart/duplicates/owner obsoleto/tenant ruidoso demuestra no perdida, at-least-once | bloqueado | new-capability | No outbox/relay; `inttest/flows/restart` covers entity recovery with graceful stop only. | Nothing to test yet. | #401, #402, #380 | confirmed |
| Retencion del journal no borra eventos antes de checkpoints; outbox conserva pendientes | bloqueado | new-capability | `DeleteEvents` exists in stores (`persistence/events_store.go:121`); no retention/checkpoint coupling. | Retention cut undefined. | #370 | confirmed |
| Migracion/borrado inventarian intents/checkpoints/IDs; delegar #369/#382 | bloqueado | new-capability | Neither procedure exists; only `EraseEntity` (`engine/entities.go:308`). | Procedures to delegate to do not exist. | #369, #382 | confirmed |
| Metrics de backlog/edad/errores y admision con cardinalidad limitada | no implementado | new-capability | Only `urd.projection.lag_ms` (`instrumentation.go:92`) and `PublicationRejected` (tenant-check drops, :148); no outbox backlog/age/error or admission metrics. | Outbox metrics absent; cardinality/secrets unassessable. | #380, #401 | corrected: was parcial; existing metrics are projection lag and tenant-check drops, not outbox backlog/age/errors/admission |
| Ejemplo reproducible enlaza guia; sin exactly-once | no implementado | new-capability | No example or guide for durable publication. | Pending. | #386, #401 | confirmed |
| Single tenant: "recuperacion/retencion/drenaje cubren unscoped/fixed; adopcion explicita" | bloqueado | new-capability | No intents exist; adoption on PostgreSQL recovers for the event journal since #429 (`TestAdoptionOfLegacyDataRecoversOnPostgres`). | Needs the relay (#401); snapshot/durable-state adoption is #435. | #401, #435 | 01da644: #428 fixed |

Issue verdict: not started; blocked by #401/#402, #370, #369/#382.

### #396 [Epic][testkit] Ampliar testkit existente para contratos y fallos durables
Dependencies: children #404-#408; #342-#345; #346, #347. Related: #424.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Tareas completadas o diferidas por decision explicita | no implementado | new-capability | Five children open; existing testkit stores/scenarios predate the epic. | #404-#408 pending. | #404-#408 | confirmed |
| Compatibilidad con capacidades existentes y evidencia de recuperacion/aislamiento | parcial | new-capability | `testkit` and `persistence/conformance` tests pass; `inttest` tenancy/restart flows pass per baseline. | No new fakes/drivers, so no evidence for them. | #405-#407 | confirmed |
| Imports respetan ADR y lifecycle via extensiones GoAkt | parcial | new-capability | Non-test `testkit/*.go` import no `internal/*` nor goakt; production code does not import testkit (only tests and `example/*`); no architecture test enforces it. | Rule unenforced; ADR open. | #347 | confirmed |
| Guia y escenarios ejecutables reflejan garantias reales | no implementado | new-capability | `docs/testing/*` cover go-specs, architecture tests, unit migration; no fault-driver guide. | Needs drivers first. | #404, #408 | confirmed |
| Single tenant: "Fixtures y drivers cubren unscoped, fixed y multitenant" | parcial | new-capability | Unscoped vs tenant covered in `testkit/scope_test.go:234`; `inttest/flows/tenancy` covers three modes on PostgreSQL. | No reusable fixtures/drivers; fixed mode not in testkit. | #405, #406, #424 | confirmed |

Issue verdict: epic open; stores, scenarios and conformance are a base, new capabilities not built.

### #404 [testkit][TK-INVENTORY] Inventariar y ampliar testkit existente
Dependencies: #396, #346, #347.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Registrar SHA e inventario entity tests, store TCK, actor harness y utilidades | parcial | new-capability | `docs/prd/i-00-baseline-develop.md` records SHAs (:43) and a testkit table (:225-236: stores, scenario, conformance, publisher conformance). Actor harness (`internal/engine/enginetest`) not itemized. | No compat/migration notes. | #346 | confirmed |
| Relacionar #348/#354 y restantes TCK con oraculos existentes | no implementado | new-capability | Baseline notes offset-store conformance "not found"; `persistence/conformance` has events/state/snapshot/schema only. Mapping can be written now; dependencies of #404 are only #346/#347. | Mapping not written. | #348, #354 (related, not formal blockers) | corrected: was bloqueado; #348/#354 are not dependencies of #404 and the mapping to existing oracles can be written now |
| Definir APIs fixtures/fault drivers sin importar internals | no implementado | new-capability | Mocks/fakes live in `internal/engine/enginetest` (not importable externally); public testkit has no fault API. | API design pending. | #405, #406 | confirmed |
| Separar core fase1 de drivers read-side/outbox/workflow fase2; GateB no depende de #366 | no implementado | new-capability | No phase-2 drivers exist; no doc defines the split. | Pending. | #388, #366, #408 | confirmed |
| Documentar go-specs y mocks/fakes para unitarios, lanes separados | cumplido | met | `docs/testing/go-specs.md` (mock rules, testkit stores as in-memory fakes, no real resources :258-278) and `docs/ci.md` lanes table. | Extend when new fakes land. | #405 | confirmed |

Issue verdict: partial; current-state documentation exists, API/boundary decisions do not.

### #405 [testkit][TK-FAKES] Fakes, mocks y reloj determinista
Dependencies: #396, #404, #349, #351, #357, #390.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Fakes configurables de stores/EventReader/admision/dispatcher respetan contratos | parcial | new-capability | In-memory `EventStore`, `OffsetStore`, `SnapshotStore`, `DurableStore`, `KeyStore` in `testkit/*.go` pass conformance; no fault config, no EventReader/admission/dispatcher fake. | Configurable faults; new contracts. | #349, #351, #380, #401 | confirmed |
| Clock/timers/backoff deterministas, IDs controlados, sin sleeps | parcial | new-capability | Manual clock exists but package-private (`internal/projectionrunner/clock.go:28-32`, "private on purpose"); go-specs offers a manual clock. | Not public in testkit; no ID control. | #357 | confirmed |
| Fakes permiten cancelacion/errores sin ocultar invariantes | parcial | new-capability | Mocks in `internal/engine/enginetest` (e.g. `events_store_mock.go`) can return errors; internal only; testkit stores have no error injection. | Public invariant-preserving fakes. | #406 | confirmed |
| Fixtures tenant/scope/celda/rol cubren colisiones y aislamiento | parcial | new-capability | Scope isolation checked (`testkit/conformance_test.go:189` catches a non-isolating store); no cell/role fixtures. | Cell/role fixtures. | #390 | confirmed |
| Ningun paquete productivo importa testkit; unitarios nunca acceden a DB/API real | parcial | new-capability | Importers of testkit: `*_test.go`, `example/*`, `benchmark` only (grep); unit gate bans real resources (`go-specs.md:258-278`). No architecture test for the import rule. | Enforce import rule by test. | #347 | confirmed |
| Single tenant: "fixtures sin tenancy, fixed y multitenant; datos legacy; rechazo de cursor/identidad cruzada" | parcial | new-capability | Unscoped/tenant stores covered; legacy-not-seen on PG (`inttest/flows/tenancy` W7 test); no cursor/identity rejection fixture found. | Reusable fixtures; cursor rejection. | #349, #360, #424 | confirmed |

Issue verdict: partial; stores exist, deterministic time/fault/ID fakes are not public.

### #406 [testkit][TK-FAULTS] Drivers de fallos y oraculos de invariantes
Dependencies: #396, #405, #348, #354, #356, #373, #374, #375, #378.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Intercalaciones reproducibles: commit tardio, repeticion, omision/cursor, owner obsoleto, rollback, parking, cancelacion del pool | no implementado | new-capability | Only ad-hoc tests (`TestRunnerPagesThroughTimestampTies`, conformance `PagingResumesFromACommittedOffsetInsideATie` `events.go:71`); no driver; fence/parking/pool absent. | Driver absent. | #373, #374, #375, #378, #348 | confirmed |
| Oraculos: secuencia contigua, no avance sin efecto, fence vigente, release de cupos | bloqueado | new-capability | No contiguous-append TCK, no fence, no pool bounds in the tree. | Needs the contracts to assert on. | #354, #374, #378 | confirmed |
| Triggers en limites publicos o adapters de prueba, sin hooks invasivos | no implementado | new-capability | No fault trigger in public testkit. | Pending. | #405 | confirmed |
| Logs/evidencia permiten reproducir seed/orden | no implementado | new-capability | grep for seed/order tooling in testkit and enginetest: none. | Pending. | #405 | confirmed |
| Reusar #348/#354/#373-375; documentar diferencia fake vs validacion real | bloqueado | new-capability | All reuse targets are open. | Dependencies. | #348, #354, #373, #374, #375 | confirmed |
| Single tenant: "escenarios con scope Unscoped y fijo, ademas de multitenant" | parcial | new-capability | Isolation conformance for Unscoped vs tenant exists; replay/cancel/idempotency scenarios absent. | Scenarios. | #424, #405 | confirmed |

Issue verdict: not implemented; depends on open TCK/fencing/parking work. Row 2 blockers refined (#378 for quota/pool release rather than #380).

### #407 [testkit][TK-HARNESS] Harness PostgreSQL testcontainers para lane de integracion
Dependencies: #396, #404, #405, #358, #359, #360, #378.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Reusar testcontainers/harness existente; fixtures esquema/seed aislados por test | parcial | new-capability | `inttest/infra/postgres/postgres.go:118` `NewDatabase`: unique DB per test, dropped at cleanup; no seed-fixture helper found. | Schema/seed fixtures. | #358, #359 | confirmed |
| Lane de integracion explicito independiente del build/test unitario; no DB desde unitarios | cumplido | met | Separate `inttest` module; CI job only on develop push/release/manual (`docs/ci.md`); unit gate rule 4 forbids `sql.Open` etc. and rule 5 forbids skips in `inttest` (`go-specs.md:258-278`). | None. | none | confirmed |
| Teardown cancela drivers y libera pool/containers ante errores, timeout o fallo | parcial | new-capability | DB drop in `Cleanup`; `Terminate` after `m.Run` (`postgres.go:107`). No drivers exist to cancel; abort/timeout path not verified. | Driver cancellation. | #406 | confirmed |
| Versiones/capacidades PostgreSQL y limites registrados; reproducible local/CI | parcial | new-capability | Image pinned `postgres:17.6-alpine` (`postgres.go:59`), `max_connections=1000`, `fsync=off` (:84). Server version not recorded in test output. | Recording missing. | #378 | confirmed |
| Permitir fault drivers y pruebas de concurrencia/fencing/pools sin suponer shutdown graceful = crash | no implementado | new-capability | No kill/crash facility in `inttest` (grep kill/crash: none); restart flow does graceful `stop`; `fsync=off`. | Crash support absent. | #406, #374, #378 | confirmed |
| Single tenant: "cubre unscoped sin migracion, fixed y multitenant; adopcion explicita vs startup compatible" | parcial | new-capability | `TestConformance_W7_SingleTenantAndLegacyModes` and `..._LegacyDataIsNotSeenByATenant` pass on PG; adoption over PG recovers (`TestAdoptionOfLegacyDataRecoversOnPostgres`, #429). | Event journal only; snapshot and durable-state adoption is #435. | #435, #424 | 01da644: was parcial because of #428; stays parcial because of #435 |

Issue verdict: partial; PostgreSQL harness and lane exist, crash/fault support and version recording do not.

### #408 [testkit][TK-PRODUCT] Driver publico de read-side y escenarios de producto
Dependencies: #396, #406, #407, #366, #393.

| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Driver de handler: prepare, Tx/rollback, duplicados, scopes y tenant visible sin cluster | bloqueado | new-capability | No ReadSideProcessor; `Handler` has only `Handle` (`projection/handler.go:57`), no prepare/Tx. | API to drive does not exist. | #366, #373, #406 | confirmed |
| Integracion PerScope/SharedCell con destinos compatibles y rechazos | bloqueado | new-capability | No PerScope/SharedCell symbols anywhere (grep). | Routing absent. | #366, #393 | confirmed |
| API registra escenarios outbox/workflow como extensiones opt-in | no implementado | new-capability | No extension registry in testkit; outbox absent. | Pending. | #406, #400 | confirmed |
| Funciones GoAkt via harness existente; no framework actor paralelo | cumplido | negative-ok | `testkit/*.go` non-test files import no goakt and define no actor framework; actor harness stays in `enginetest`. Vacuous: no driver built yet. | Re-check when the driver exists. | #406 | corrected: was no verificado; negative criterion met because no parallel actor framework exists |
| GateB no depende de este driver; matriz garantias mocks vs integracion | no implementado | new-capability | No matrix doc; Gate B issue open. Negative half vacuous, documentation half absent. | Documentation. | #388, #404 | confirmed |
| Single tenant: "PerScope usa OneScope(Unscoped()) sin catalogo; fixed/multi; SharedCell explicito" | bloqueado | new-capability | No OneScope/PerScope API (grep). | API absent. | #366, #393 | confirmed |

Issue verdict: not started; phase 2, blocked by #366/#393.


## Workflow (#397) and management (#398)

### #397 [Epic][workflow] Consolidar sagas y procesos durables recuperables
Dependencies: #409-#413 (children), #342-#345, #424, #423.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Tareas completadas o diferidas por decisión explícita | no implementado | new-capability | Children #409-#413 all OPEN; no workflow package; nothing deferred by explicit decision | Epic closure criterion; nothing done or deferred | #409 #410 #411 #412 #413 | confirmed |
| Compatibilidad con capacidades existentes y evidencia de recuperación/aislamiento | parcial | new-capability | Saga recovery/tenant tests exist (`saga_test.go:298`, `saga_actor_tenant_test.go`); `engine/saga.go:147` says status is not persisted | No evidence for downtime, timer restart, terminal-saga restart (recover resets status Running, `saga_actor.go:365`) | #412 #413 #301 (closed) | confirmed |
| Imports respetan ADR y lifecycle se integra mediante extensiones GoAkt | bloqueado | new-capability | Saga uses `extensions.Require` in PreStart (`saga_actor.go:173,177`); no workflow module to check imports | Boundary untestable until module exists | #347 (ADR, defines module rules), #409 (workflow boundary) | confirmed |
| Guía y escenarios ejecutables reflejan garantías reales | no implementado | new-capability | No saga file in `testkit/`; README.md:414 only a short intro; no saga guide in docs/ | Behavior search (saga, guide, scenario) finds nothing equivalent | #408 #413 #386 | confirmed |
Issue verdict: epic not started; only legacy saga actor exists; baseline inventory is accurate.

### #409 [workflow][WF-CONTRACT] Frontera workflow y contrato de sagas
Dependencies: #397, #346, #347, #357, #361, #388.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Inventario de sagas, formato persistido y suscripciones; migración/compatibilidad explícita | parcial | new-capability | Baseline `docs/prd/i-00-baseline-develop.md:241` inventory; saga persists `egopb.Event` + tenant marker (`saga_actor.go:729,766`); in-process subscribe (:251) | Persisted format not stated as contract; no migration/compat rules | #346 #347 #301 (closed) | confirmed |
| Identidad (scope,workflowID,version), state-machine, causation/correlation, eventos de resultado definidos | parcial | new-capability | Scope + `behavior.ID()` (:216); root metadata causation (:223-229, :798-815); status enum | No version, no explicit state machine or result events; actor-name collision across families was #427, fixed by #430 (`ErrSpawnIdentityMismatch`) | #357 | 01da644 |
| CommandDispatcher es interfaz local de workflow implementada/inyectada por Urd; workflow jamás importa root | no implementado | new-capability | No `CommandDispatcher`/`Dispatcher` in any .go; saga calls `SendSync` directly (:862) | SPI and workflow package absent; searched Dispatcher, command port, injected | #347 #414 (related SPI style) | confirmed |
| Consumo durable usa EventReader/runner projection; pub/sub solo wakeup | bloqueado | current-defect | `consumeEvents` (:443-477) reads live in-memory subscriber only; events during downtime are lost; no `EventReader` type exists | Needs new read path from persistence | #360 (I-08a read in adapters), #361 (runner read path), #371 (wakeup after commit) | confirmed (blockers widened to #360 and #371) |
| No presentar workflow como equivalente exacto de módulo Akka/Lagom; distinguir propuesta Urd | cumplido | met | `docs/prd/urd-platform-prd.md:72` states it is a Urd proposal, not an Akka/Lagom equivalent | None; docs only | #423 | confirmed |
| [single tenant] Consolidar contrato conservando ejecución Unscoped y fixed; cambio de identidad es migración | parcial | new-capability | `resolveScope` returns `Unscoped()` when no tenancy (:332-336); tenant paths tested in `saga_actor_tenant_test.go` | No saga test under fixed-tenant resolver (`tenancy/resolver.go`); identity-change migration undefined | #424 #369 | confirmed |
Issue verdict: inventory partial; SPI, versioned identity and durable consumption not started.

### #410 [workflow][WF-STATE] Persistir transición con inbox e intent en Tx
Dependencies: #397, #409, #361, #373, #374, #375.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Transición+inbox dedupe+intent+checkpoint en una Tx destino coherente con #373 | bloqueado | new-capability | `saga_actor.go:729` plain `WriteEvents(..., Unconditional())`; no inbox, intent or checkpoint | New guarantee; Tx destination contract missing | #373 (P-TX), #376 (P-PREP) | confirmed |
| Scope/ID/version aíslan workflow y datos de otros tenants | parcial | new-capability | Scoped store access, `bindOrVerify`, `VerifyActorIdentity` (:218, :511); tenant tests present | No version dimension. #427 (actor-name family collision) is fixed by #430 and was never about data isolation | #344 | 01da644 |
| Replay tras caída/reinicio recupera pendientes y no genera intenciones nuevas para evento repetido | no implementado | current-defect | `recover()` (:363) replays only own events; events during downtime lost; repeated event is handled twice (no dedupe, `handleStreamEvent` :543) | Needs catch-up reader and inbox; behavior search (dedupe, inbox, idempotent) finds none | #361 #360 #373 | confirmed |
| CAS/revisión/fence impiden writers obsoletos; errores/parking delegan #375 | bloqueado | new-capability | Writes use `Unconditional()`; `persistence.ExpectRevision` exists (`persistence/precondition.go`) but saga does not use it; no fence | Fence missing in destination; parking missing | #374 (fence), #375 (parking) | confirmed |
| No incorporar runner propio ni declarar Tx atómica entre bases independientes | cumplido | negative-ok | No workflow code adds a runner or claims cross-DB Tx; legacy saga own loop (`consumeEvents` :443) predates and is #409/#361 scope | Vacuous: nothing built under this issue; legacy loop must still migrate to runner | #361 #409 | corrected: was no implementado; criterion is a prohibition on new work, vacuously met (legacy loop noted in Gap) |
Issue verdict: none of the new transactional guarantees exist; the saga replays only its own journal.

### #411 [workflow][WF-COMMANDS] Despachar comandos con identidad estable y dedupe
Dependencies: #397, #410, #357.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Entrega al menos una vez con command_id estable #357; reintentos no crean nuevos IDs | bloqueado | new-capability | `attachCommandMetadata` (:798-815) uses `GenerateOperationID` (random) unless the behavior supplies `SagaCommand.Metadata`; no retry loop | Needs stable command identity and durable intent | #357 (command IDs/retries), #410 (durable intent) | confirmed |
| Compensación tiene ID propio estable y causation hacia el intento original | parcial | new-capability | `SagaCommand.Metadata` explicit override is used verbatim (`port/behavior/saga.go:82-89`, test `saga_actor_metadata_test.go:76`); default is random child of saga root (:904-928) | Engine does not derive a stable compensation ID nor link causation to the original attempt; relies on the behavior | #357 #410 | corrected: was no implementado; behaviors can already pin explicit stable Metadata, engine just does not enforce it |
| ACK/error/resultados definen transición y recuperación; indeterminado vs fracaso definitivo | parcial | new-capability | Error and timeout both go to `HandleError` (:864-872); `ParseCommandReply` classifies error replies | No indeterminate vs definitive distinction; searched Indeterminate, ambiguous: none | #357 #413 | confirmed |
| Límites de concurrencia/espera/retry; no bloquear mailbox indiscriminadamente | parcial | current-defect | Per-command timeout, default 5s (`effectiveCommandTimeout` :826; test 'default timeout when zero' saga_test.go:741) | `SendSync` runs inside actor Receive via `handleStreamEvent` (:862), blocking the mailbox; no concurrency/retry bound | #378 (related bounds) | confirmed |
| Pruebas respuesta perdida/despacho repetido/caída tras aceptación demuestran dedupe por destino | no implementado | new-capability | No such tests in `internal/engine/saga`; no target-side dedupe exists | Needs retry loop plus target dedupe to test | #357 #410 | confirmed |
Issue verdict: today only synchronous best-effort dispatch; stable IDs are possible only if the behavior supplies them.

### #412 [workflow][WF-TIMERS] Timers durables y recuperables bajo fencing
Dependencies: #397, #410, #374.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| TimerID estable por workflow/scope/version, fecha durable, estado idempotente | no implementado | new-capability | Only `ScheduleOnce(&sagaTimeoutMsg{}, timeout)` in memory (`saga_actor.go:275-277`); no timer ID or stored deadline | Nothing durable; searched timer, deadline, ScheduleOnce | #410 #301 (closed) | confirmed |
| Reinicio recupera timers vencidos con política explícita | no implementado | current-defect | PostStart reschedules the full timeout on each start (:275-277); `recover()` sets status Running (:365) so a terminal saga is reactivated | Deadline restarts and terminal sagas revive after restart | #410 #301 (closed NOT_PLANNED) | confirmed |
| Owner obsoleto no dispara transición confirmada; fence validado en destino | bloqueado | new-capability | No fence; timeout path only checks `status == Running` (:282) | Needs destination fence | #374 | confirmed |
| Scheduler nativo GoAkt como señal; intent durable es fuente de verdad | parcial | new-capability | GoAkt `ScheduleOnce` reused (:277) | It is the only source; no durable intent | #410 | confirmed |
| Carga/recuperación acotadas; sin colas ilimitadas ni IDs nuevos al retry | no implementado | new-capability | No recovery path for timers exists | New guarantee; needs durable timers first | #355 (budgets), #357 | confirmed |
| Cancelación/reprogramación vs disparo usa CAS/revisión y fence en el mismo destino | bloqueado | new-capability | No cancel/reprogram API on the saga | CAS exists in persistence but no fence and no API | #374, #373 | confirmed |
| Pruebas deterministas cancel/fire, reprogram/fire, owner obsoleto, retry con ID estable | no implementado | new-capability | Only 'PostStart with timeout triggers compensation' (`saga_test.go:342`); saga has no clock seam | Needs the cancel/reprogram API and a fake clock | #405 (TK-FAKES clock), #374 | confirmed |
Issue verdict: timeout is volatile and resets on restart; only the GoAkt scheduler is reusable.

### #413 [workflow][WF-RECOVERY] Compensación, recovery y escenarios auditables
Dependencies: #397, #411, #412, #375, #408.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Política configurable de retry/compensación/parking por pasos y errores indeterminados | parcial | new-capability | Behavior-coded `HandleError`/`Compensate` (`port/behavior/saga.go:51-59`) | No engine-level policy, no parking, no indeterminate class | #375 #411 | confirmed |
| Compensación es acción idempotente propia, no revierte efectos externos | parcial | new-capability | Compensation is a separate behavior method returning commands (`saga_actor.go:905`) | No stable ID by default; a failed compensation sets `SagaFailed` without retry (:908-922) | #411 | confirmed |
| Auditoría registra scope/workflowID/version/causation y estado sin secretos | no implementado | new-capability | Only log lines; command metadata carries causation; searched audit: no Go audit record | No audit record, no version | #417 (audit guard), #409 | confirmed |
| Escenarios testkit: reinicio, evento en downtime, timer vencido, duplicado, fallo en compensación | parcial | new-capability | Internal tests cover recovery (`saga_test.go:298`) and compensation failures (:640-687); `testkit/` has no saga scenario | Downtime, expired timer, duplicate uncovered; none in testkit | #408 #412 | confirmed |
| Guía conservación/migración de sagas y límites de garantías; no resetear otros scopes | no implementado | new-capability | No saga guide in docs/ or MIGRATION.md (only README.md:414 intro) | Docs missing | #386 #409 | confirmed |
Issue verdict: behavior-level compensation exists; recovery policy, audit and scenarios do not.

### #398 [Epic][management] Control operativo seguro mediante capacidades públicas
Dependencies: #414-#418, #419-#422, #342-#345, #424.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Tareas completadas o diferidas por decisión explícita | no implementado | new-capability | Children #414-#422 OPEN; no management package, no `cmd/` | Epic closure criterion | #414-#422 | confirmed |
| Compatibilidad con capacidades existentes y evidencia de recuperación/aislamiento | parcial | new-capability | `StartProjection`/`StopProjection`/`RebuildProjection`/`ProjectionLag` (`engine/projections.go:65,125,185,282`), `EraseEntity` (`entities.go:308`) | Isolation evidence only for EraseEntity tenant gate | #381 #382 | confirmed |
| Imports respetan ADR y lifecycle se integra mediante extensiones GoAkt | bloqueado | new-capability | No management module | Boundary untestable until module exists | #347 (ADR), #383 (U-EXT), #414 | confirmed |
| Guía y escenarios ejecutables reflejan garantías reales | no implementado | new-capability | None found in docs/ or testkit/ | Nothing to describe yet | #418 #408 #386 | confirmed |
Issue verdict: only scattered engine operations exist; no pause/resume anywhere (searched pause, suspend, disable, park).

### #414 [management][MG-CONTRACT] Control SPI y operaciones identificadas
Dependencies: #398, #346, #347, #384, #388.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| OperationID y objetivo scope/celda/processor/version/rango; Unscoped no concede acceso a todos | no implementado | new-capability | `command.OperationID` exists for commands only (`command/identity.go:39`); no management operation type | New contract | #357 (related ID semantics), #368 (cell) | confirmed |
| SPI distingue consultas, mutaciones y operaciones largas con resultado/estado/cancelación | no implementado | new-capability | No management SPI in any .go | New contract | none (own deliverable) | confirmed |
| Registro por capacidades: núcleo existe sin exigir todas las operaciones | no implementado | new-capability | No registry; `requireFamily` (`entities.go:154`) is unrelated entity-family gating | #384 validates ADAPTER roles at composition, not management operations; registry is this issue's own deliverable | #384 (related), #418 | corrected: was bloqueado; #384 is not a concrete blocker for a management operation registry |
| management no importa internals ni implementa actores/remoting; Urd inyecta controles públicos | no implementado | new-capability | Package does not exist | Mixed criterion: prohibition vacuous, but the injection of public controls is not built | #347 | confirmed |
| No crear HTTP/gRPC/dashboard ni QueryBus obligatorio | cumplido | negative-ok | No management or dashboard code exists (no `cmd/`, no handlers) | Vacuous: nothing built | none | confirmed |
| [single tenant] SPI y registry aceptan Unscoped; autorización operador y scope target explícitos | no implementado | new-capability | None | New contract | #424 #417 | confirmed |
Issue verdict: contract not defined.

### #415 [management][MG-STATUS] Progreso y estado operativo
Dependencies: #398, #414, #362, #374, #375, #378.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Lag y cursor distinguen observado/confirmado, falta de capacidad y ausencia de datos | parcial | new-capability | `Engine.ProjectionLag` per shard vs committed offset (`projections.go:282-340`); errors if no offset store | No observed vs confirmed split; no capability/no-data states | #361 (two lags), #362 | confirmed |
| Consultar parking, ownership, rebuild y pools solo cuando la capacidad esté registrada | bloqueado | new-capability | No parking, ownership or pool status APIs | Underlying features absent | #375 (parking), #374 (ownership), #378 (pool), #414 (registry) | confirmed |
| Scope/rol/celda explícitos para Journal/Feed y destino; sin confundir presupuestos ni pool dedicado con aislamiento físico | bloqueado | new-capability | No cell/role model in code (`cell` appears only in unrelated publisher/runner internals) | Needs cell and role model | #368 (ScopeRouter/celdas), #390 (resource selection by scope/cell/role), #414 | confirmed (real blockers are #368 and #390, not #355) |
| Paginación/espera/límites de consulta acotados y métricas de cardinalidad bounded | no implementado | new-capability | `ProjectionLag` enumerates all shards unbounded; `Telemetry` is only Tracer+Meter (`telemetry.go:30-37`) | No query limits | #355 | confirmed |
| No revelar DSN, credenciales, payload sensible ni datos de otro tenant | no verificado | new-capability | `ProjectionLag` returns durations only, scope from `projectionScope` (:368); store errors are wrapped with `%w` and unchecked | No status API with redaction tests; leak via wrapped errors unverified | #379 | confirmed |
Issue verdict: only projection lag exists, without the required state model.

### #416 [management][MG-CONTROL] Pausa/reanudación durables y delegación
Dependencies: #398, #414, #374, #375, #417.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Pausa/reanudación durables identifican scope/processor/version/rango y sobreviven restart/takeover | no implementado | new-capability | Only volatile Start/Stop (`projections.go:65,125`); no durable pause state; searched pause, suspend, disable, halt | New primitive in projection | #374 (takeover), #36 (closed EGO-ADMIN) | confirmed |
| Pausa define límite de trabajo en vuelo y ACK operativo; no cerrar pools compartidos ni perder offsets | no implementado | new-capability | None | New guarantee | #378 | confirmed |
| Retry/replay delegan #375; change-version #370; rebuild #381; migración #369; borrado #382 | bloqueado | new-capability | All five delegated issues OPEN; no delegation facade | Targets missing | #375 #370 #381 #369 #382 | confirmed |
| Operación concreta solo se habilita al existir capacidad; no depender de todas para cerrar control básico | no implementado | new-capability | No capability gating | Design only | #414 | confirmed |
| Nunca reset bruto de offsets compartidos ni copiar state machines de tenancy/projection | parcial | new-capability | After #431 `RebuildProjection` resets through `offsetstore.ForScope` for the scope registered for that projection (`projections.go:199-220`); it is no longer a raw reset of the shared name. Store-level isolation: `TestScopedOffsetsPreserveLegacyAndIsolateResetOnPostgres`. | No engine-level two-scope rebuild test; no per-call scope choice | #381 (per-scope rebuild), #362 | 01da644: was current-defect (raw reset); reclassified after #431 |
| Usar fence/guardas de MG-GUARDS cuando habilitado, sin dependencia cíclica | bloqueado | new-capability | No fence, no guards; issue body section 'Guarda obligatoria' (non-checkbox) also demands authz/dedupe/audit/fencing for every mutation | Needs fence and guard layer | #374, #417 | confirmed |
Issue verdict: no control primitive exists; the rebuild is scope-bound since #431 but not yet proven at engine level. Criterion 1 text in audit dropped 'scope/processor/version/rango' detail (restored above).

### #417 [management][MG-GUARDS] Autorizar, deduplicar y auditar
Dependencies: #398, #414, #379, #374, #380.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Autorización por port inyectado; identity extraction reutiliza #379 | parcial | new-capability | Tenant resolver injected into engine; `EraseEntity` fails closed on administrative scope (`entities.go:332-338`, `engine_tenant_administrative_scope_test.go`) | No authorization port for operations; #379 open | #379 | confirmed |
| Scope explícito y operación identificada; mutaciones reintentadas devuelven mismo resultado o conflicto | no implementado | new-capability | No operation dedupe store; searched operation id, idempotent mutation | New guarantee | #414 #357 | confirmed |
| Validar fence/version al modificar y registrar auditoría antes/después | bloqueado | new-capability | No fence, no audit code (searched audit across Go sources) | Fence part blocked; audit part is own deliverable | #374 (fence) | confirmed |
| Rechazar capacidades ausentes, cuotas agotadas y scopes inválidos con errores accionables | bloqueado | new-capability | No quota code; `ErrEntityFamilyNotDeclared` rejects absent families only for spawns | Quotas absent; no management capabilities | #380 (quotas), #414 | confirmed |
| Consultas/operaciones largas tienen límites; ninguna identidad implica privilegio global | parcial | new-capability | Tenant-aware `EraseEntity` denies non-tenant identity (`entities.go:333-338`) | One operation only; no limits | #414 | confirmed |
| [single tenant] Identidad de operador separada del tenant ID; fail-closed sin tenant ficticio | no implementado | current-defect | Legacy mode (no resolver) erases `Unscoped()` with no authorization (`entities.go:322-326`) | Default deployment is not fail-closed for erasure | #424 #379 | confirmed |
Issue verdict: only a tenant gate on EraseEntity; no guard layer; legacy EraseEntity is unauthenticated.

### #418 [management][MG-COMPOSE] Componer management en GoAkt y verificar con testkit
Dependencies: #398, #415, #416, #417, #383, #408.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Registro compile-time/WithExtensions y dependencias PreStart, sin plugins dinámicos ni cambios GoAkt | no implementado | new-capability | Engine extension pattern exists (`extensions.Require`) but no management registration | Own deliverable; relies on extension composition | #383 (U-EXT) | confirmed |
| Capacidades incompletas se reportan; no habilitar endpoint/control inexistente | no implementado | new-capability | None | Needs registry | #414 | confirmed |
| Testkit prueba autorización, idempotencia, takeover, pausa/reinicio y aislamiento multitenant | bloqueado | new-capability | `testkit/` has no management, saga or projection scenario | Nothing to test yet | #408, #416, #417 | confirmed |
| Guía muestra operaciones habilitadas y limitaciones | no implementado | new-capability | None | Docs missing | #386 | confirmed |
| No exigir terminar toda management para GateB/C ni agregar transporte/producto nuevo | cumplido | negative-ok | #388 and #389 bodies state 'no exigir terminar ... management' (#389 line 28); no new transport exists | Vacuous policy criterion; met by issue structure | #388 #389 | corrected: was no verificado; gate bodies confirm the policy and no transport was added |
Issue verdict: not started.

### #419 [management][INSPECT-FEASIBILITY] Auditar GoAkt, collector y attach read-only
Dependencies: #398, #346, #347, #414, #383.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Auditar versión de go.mod y replace; fijar SHA y capacidades observadas | parcial | new-capability | go.mod:9 requires `v4.5.7-0.20261001174708-cf9c8659f741`, go.mod:93 replaces with fork `v4.5.7-actorof.1`; fork origin hash `c51a72dca92f` in module cache; report `i-00-baseline-develop.md:264-300` lists APIs | Report cites upstream base SHA only; fork commit SHA not recorded; capabilities read but not exercised | #346 | confirmed |
| Collector local read-only con identidad/nodo/placement/estado/métricas; snapshots con timestamp, provenance, incomplete/stale | no implementado | new-capability | No collector code (no use of `Actors(`, `NumActors`, `Metric` in Go sources). Fork APIs confirmed: `Actors` actor_system.go:136, `NumActors` :334, `Metric` :126, `PID.Metric` pid.go:499, `Children` :673 | Collector and snapshot DTO absent | #383 | confirmed |
| Attach opt-in explícito (export/replay local o canal autorizado); sin acceso a memoria; sin servidor remoto always-on | no implementado | new-capability | Public `client.Client` has Kinds, Spawn, SpawnBalanced, ReSpawn, Tell, Ask, AskGrain, TellGrain, Stop, Exists, Reinstate: no list/metric (audit's method list was incomplete, conclusion holds) | Report analyses options but no design chosen (export vs channel) | #421 #422 | confirmed |
| Auditar Actors/Metric/Peers y traversal remoto; presupuesto timeout/cancelación | parcial | new-capability | `Actors(ctx, timeout)` actor_system.go:136 (cluster scan, 'may impact performance'), `Peers(ctx, timeout)` :713; `remote.Peer` fields (Host, DiscoveryPort, PeersPort, RemotingPort, Roles, CreatedAt) verified here | Report says `remote.Peer` not verified; no measured remote cost or cancellation budget | #355 | confirmed |
| Core inspector sin dominio Urd; enriquecimiento opcional; no llamar Actor() ni reflejar estado | no implementado | new-capability | No inspector package | Design only | #347 | confirmed |
| Interfaces públicas y formato snapshot versionado | no implementado | new-capability | None | New contract | #389 (SPI stability) | confirmed |
| OSS licencia compatible, sin backend pago; no prometer paridad con consola Akka | parcial | new-capability | Fork LICENSE is MIT; repo LICENSE is MIT (both verified); `urd-platform-prd.md:187` avoids parity claim | No tool or dependencies chosen, so dependency licensing unchecked | #421 | confirmed (evidence fixed: repo license is MIT, so compatibility with GoAkt holds) |
| Documentar unsupported y seguridad/permisos/redaction del canal y tenant boundary | parcial | new-capability | Report lists mailbox size, dead-letter list, remote list/metric as unsupported; PRD risk row :261 mentions leak mitigation | Security, permissions, redaction and tenant boundary of the channel not documented | #379 | confirmed |
| [single tenant] Inspector sin Urd tenancy; attach autorizado por app/nodo | no implementado | new-capability | None | Design only | #424 | confirmed |
Issue verdict: audit is a solid start (fork APIs confirmed); fork SHA, remote budgets, security notes and all design deliverables are missing.

### #420 [management][INSPECT-TELEMETRY] Interacciones observadas con sampling y buffers
Dependencies: #398, #419, #379, #355.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Auditar hooks de Send/Tell/Ask y métricas/event stream en versión fijada | parcial | new-capability | Fork: `ActorSystem.Subscribe` (actor_system.go:2050, lifecycle events), `WithMetrics` (option.go:425); no message interceptor or middleware found in `actor/` | Report covers Subscribe and Metric but not the absence of Send/Tell hooks | #419 | confirmed |
| Capturar metadata origen/destino/nodo/timestamp/tipo sin payload ni inferencia | no implementado | new-capability | No capture code; fork has no per-message hook | Own design (explicit Urd adapter) is part of this issue; no cross-issue blocker | #419 (related snapshot format) | corrected: was bloqueado; ordering inside the issue, #419 is related not a hard blocker |
| Sampling/ring buffer/colas con máximos; pérdidas y cobertura visibles | no implementado | new-capability | None | Generic bounded buffer is buildable independently | #419 (related), #355 | corrected: was bloqueado; no concrete blocker, buffers do not need the #419 attach decision |
| Edges con ventana temporal, origen, conteo y staleness | bloqueado | new-capability | None | Edge fields belong in the versioned snapshot format | #419 (snapshot format) | confirmed |
| State dominio opt-in autorizado; aislar tenants y redactar secretos | no implementado | new-capability | None | Needs authorization and tenant extraction | #379, #419 | confirmed |
| Instrumentación aislada, cancelable y con costo medido | no implementado | new-capability | None | Measurement is done in #422; instrumentation itself unblocked | #355 (workload), #422 | corrected: was bloqueado; no concrete blocker, cost validation is #422 |
Issue verdict: not started; likely needs an explicit Urd adapter because the fork exposes no Send/Tell hook.

### #421 [management][INSPECT-TUI] cmd/urd-inspect con vistas terminal
Dependencies: #398, #419, #420, #415.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| cmd/urd-inspect sin nuevo go.mod; core genérico y adapter Urd opcional | no implementado | new-capability | No `cmd/` directory; `ls cmd` fails | Absent | #419 | confirmed |
| Consumir attach/export; snapshot/replay explícito; live solo con canal real | bloqueado | new-capability | No attach/export format exists | Needs the format and attach decision | #419 | confirmed |
| Vistas lista/ubicación/nodo/estado/health, topología parent-child y grafo; navegación sin GUI | bloqueado | new-capability | None | Needs snapshot data and observed edges | #419, #420, #415 (Urd adapter status) | confirmed |
| Grafos ASCII; refresh/consultas/layout acotados y cancelables; incomplete/stale/drops | bloqueado | new-capability | None | Needs staleness/drop signals | #419, #420 | confirmed |
| Solo lectura: sin kill/restart ni mutaciones de actor/tenant/projection | cumplido | negative-ok | No tool exists, so no mutations exist | Vacuous | none | corrected: was no implementado; pure prohibition, consistent with #414 'No crear HTTP' row |
| Sin API remota obligatoria ni backend de telemetría pago; licencias compatibles | cumplido | negative-ok | No tool or dependencies exist; repo and GoAkt are MIT | Vacuous; dependency licenses must be rechecked when the tool is built | #419 | corrected: was no implementado; prohibition criterion, vacuously met |
| Distinguir actor local/remoto y datos no disponibles; dominio opt-in | bloqueado | new-capability | Fork has `PID.IsLocal/IsRemote`; no snapshot flags exist | Needs snapshot format | #419 | confirmed |
| [single tenant] TUI en single tenant o GoAkt puro; UI distingue scope default/fijo/multi y nodos | bloqueado | new-capability | None | Needs #419 data model | #419, #424 | confirmed |
Issue verdict: not started.

### #422 [management][INSPECT-VALIDATE] Validar aislamiento y costo; propuesta GoAkt
Dependencies: #398, #421, #405, #407, #418.
| Criterion | State | Kind | Evidence | Gap | Blocked by / related issues | Check |
|---|---|---|---|---|---|---|
| Escenarios nodos vivos/remotos, restart, actor desaparecido, snapshot viejo, canal caído, drops | bloqueado | new-capability | Nothing to test | Needs collector and TUI | #421, #419 | confirmed |
| Tests tenant authorization/redaction; dominio no habilitado por defecto | bloqueado | new-capability | None | Needs enrichment and TUI | #420, #421, #379 | confirmed |
| Medir CPU/memoria/cardinalidad/espera con carga fija; saturación reduce cobertura | bloqueado | new-capability | None | Needs instrumentation | #420, #355 | confirmed |
| Unitarios con fakes, harness de integración opt-in; distinguir limitaciones GoAkt | bloqueado | new-capability | #405 and #407 OPEN; no inspector code under test | Primary blocker is the code under test; fakes/harness are related | #421, #405, #407 | confirmed |
| Guía launch/export/attach/snapshot/replay, seguridad y límites; portable terminal sin backend pago | bloqueado | new-capability | None | Needs working tool | #421 | confirmed |
| Propuesta GoAkt con core, licencia, SPI, evidencia; sin contactar ni mensajear owner | no implementado | new-capability | None; negative half (no owner contact) is vacuously met | Document absent; evidence will come from #419-#421 | #419 #421 | confirmed |
| Mermaid docs explican arquitectura/attach sin prometer paridad exacta | parcial | new-capability | PRD mermaid at `urd-platform-prd.md:191-196`; parity disclaimer :187 | Product PRD only; no inspector guide | #421 | confirmed |
| [single tenant] Pruebas GoAkt puro/unscoped/fixed/multi; autorización fallida bloquea attach | bloqueado | new-capability | None | Needs attach channel | #421, #424 | confirmed |
Issue verdict: not started; fully dependent on #421 (and #419/#420 upstream).

Duplicate search (related issues found for real gaps; none is an open duplicate of a gap): saga durable timer/deadline/restart -> #301 (closed NOT_PLANNED), #30 (closed), #18 (closed); saga durable consumption -> #360 #361 #371; saga command identity -> #357; pause/resume -> #36 #14 (closed); inspector/attach/TUI -> only #419-#422 and #398; saga name collision -> #427 (closed, fixed by #430); EraseEntity unauthenticated legacy mode -> no existing issue (candidate gap, relates #417 #379 #424); RebuildProjection raw reset -> #381 #362; saga SendSync mailbox blocking, saga audit/guide, redaction/DSN status, ring buffer, GoAkt upstream proposal: no existing issue beyond the module issues above.
