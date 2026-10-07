# I-00 - Audit of the #345 tasks against their acceptance criteria

Companion of `i-00-baseline-develop.md`. Audited on 2026-10-07 against code at
`develop` `4c66286` (#426, Go 1.27.0), the branch tip being the commit that
adds this file. The historical baseline (`4ebdc3d`, Go 1.26) is not mixed in.

## Method and limits

- Membership: an issue is a task of #345 if its body says `Epica: #345`
  (16 issues, #346 among them), or `Epica: #N` of one of the seven sub-epics
  #342, #343, #344, #395, #396, #397, #398 (58 issues), plus the 7 sub-epics
  themselves: 81 linked issues, all open. The body of #345 lists only part of
  them, so it was not used as the list. #346, #362, #371, #379 and #381 were
  audited in the main report and are not repeated; this file covers the other
  76. #423 (PRD) carries no `Epica:` line and is not a task.
- Each criterion comes from the issue body (checkbox lines). States:
  `cumplido`, `parcial`, `no implementado`, `no verificado`, `bloqueado`
  (a dependency or finding blocks the proof).
- The audit was done by reading code, docs and tests, one reviewer per group.
  Tests were run only where one could settle a doubt (saga actor and engine
  saga status tests, projection runner architecture and lag tests); no whole
  suite was repeated. Anything not run is marked as read-only evidence.
- Several `cumplido` rows are negative criteria (no new HTTP/gRPC, no 1M r/s
  claim, no plugins, no tables yet). They hold because nothing was built; they
  are not proof of a capability.
- "No implementado" means no equivalent was found after searching by behavior,
  not only by name. It describes the state at this SHA, not a defect.
- The audit is a snapshot of issue bodies of 2026-10-07; the criteria were not
  edited.

## Findings that cut across tasks

- #427 (B4): actor name collisions, relevant to sagas, workflow and any task
  that counts on actor identity.
- #428: `TenantAdopter` fails on PostgreSQL, so adoption and recovery to
  single tenant are not demonstrated. Tasks that depend on that path are
  marked `bloqueado`.
- B2, non-zero partition: required by #350 ("the slice does not change with
  1, 3 and 5 nodes"), and relevant to the reader and outbox tasks that rely on
  per-shard reads (#360, #361, #400, #404). No test injects a non-zero
  partition; today `Partition` is GoAkt-dependent
  (`event_sourced_actor.go:353`, `durable_state_actor.go:167`).
- Offsets are keyed by (projection name, shard) with no tenant column, and
  `RebuildProjection`/`ResetOffset` take only a name (#362, #381).
- The runner still reads with `GetShardEvents` and a timestamp cursor that does
  not close a commit-order gap (`persistence/events_store.go:169-172`, #352).


## Totals (table rows counted from the matrices below; the reviewers' own tallies differ by 2 rows)

| Group | Issues | cumplido | parcial | no implementado | no verificado | bloqueado |
|---|---|---|---|---|---|---|
| A | 15 | 5 | 22 | 66 | 9 | 2 |
| B | 16 | 1 | 14 | 41 | 2 | 18 |
| C | 17 | 3 | 11 | 55 | 2 | 11 |
| D | 12 | 4 | 20 | 31 | 1 | 16 |
| E | 16 | 2 | 22 | 38 | 2 | 31 |
| Total | 76 | 15 | 89 | 231 | 16 | 78 |


## Group A: direct tasks of the epic

### #347 [I-01] ADR: amendment to ego-arch-001 for module topology
Dependencies: #346 (done), epic #345; related #424, #372, #395-#398, #419-#422
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| ADR states persistence imports neither tenancy nor projection | no implementado | No ADR file. Code contradicts: persistence/scope.go:25, conflict.go:30 import tenancy (go list -deps ./persistence). | ADR missing; code fix tracked by #349. |
| Resolves adapters-under-contract-path rule (B10) | no implementado | No ADR; only PRD line 227/262 says #347 must reconcile. | ADR missing. |
| Defines where egopb lives | no implementado | egopb/ still in root; PRD mentions #363 (I-11) split. No decision doc. | ADR missing. |
| No module names a concrete engine outside its adapter | no implementado | Rule not written. persistence/postgres is nested adapter; no test enforces it. | ADR and enforcement (#353) missing. |
| Minimum capabilities per role; composition rejects adapter not declaring | no implementado | PRD C-/U- text only (prd:131,135). compose/spec.go requiredCapabilities is empty (spec.go:327). | ADR missing; mechanism is #384. |
| Amend: #395 Integration boundary | no implementado | No ADR text. | Pending ADR. |
| Amend: #397 Workflow CommandDispatcher SPI | no implementado | No ADR; no workflow module. | Pending ADR. |
| Amend: #398 Management boundary | no implementado | No ADR. | Pending ADR. |
| Amend: #396 Testkit boundary (no prod import of testkit/cmd) | no implementado | No ADR; no test forbids it. | Pending ADR; test in #353. |
| Amend: generic Inspector #419-#422 DTO boundary | no implementado | No ADR; no inspector code. | Pending ADR. |
| Amend: no new go.mod; extraction follows #372 | no implementado | No ADR. Existing nested go.mod only (persistence/postgres, publishers, inttest...). | Pending ADR. |
| Amend: Workflow is Urd-own consolidation + mermaid dependency diagram | no implementado | No ADR/diagram (diagram exists only in the issue body). | Pending ADR. |
| Single tenant: record Unscoped/fixed/multitenant modes, adoption/migration on identity change | parcial | PRD docs/prd/urd-platform-prd.md:32-42 records three modes ("final API names subject to ADR"). persistence.Unscoped() exists (scope.go). | PRD is not the ADR; API names and per-capability service requirements undecided. |
Issue verdict: not done; no ADR deliverable exists (PRD is a proposal, not the ADR). Blocks #353, #383, #384, #424.

### #353 [I-07] Architecture: tests for the new rules
Dependencies: #347 (open work)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Architecture lane fails on a new forbidden import | parcial | TestArchitecture* lane exists (23 tests, docs/testing/architecture-tests.md; engine/tenancy_architecture_test.go etc.). None covers persistence/projection/tenancy rules of I-01. | No persistence-not-importing-tenancy/projection test; rules undefined until #347. |
| Each exception links the issue that removes it (I-03, I-12, I-13) | no implementado | No exception list found (grep). | Exception list absent; I-03=#349, I-12=#364, I-13=#365 not linked. |
| Verify #395-#398 boundaries and #347 deps; forbid lower-to-root imports | no implementado | No such tests; those packages do not exist. | Blocked by #347. |
| Production does not import testkit or cmd | no implementado | No test asserting it. | Test absent. Blocked by #347 for rule text. |
| Generic inspector does not import Urd domain/tenancy | no implementado | No inspector code or test. | Blocked by #347 and #419-#422. |
| No new go.mod; no copy of runner/ownership/Tx/tenant state machines | no implementado | No test. | Test absent. |
Issue verdict: not done; only the pre-existing architecture lane exists. Blocked by #347.

### #355 [I-20] Define load model, limits and measurable SLOs of a cell
Dependencies: #346 (done)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Doc with commands/s, events/s, bytes, fan-out cost, hot tenants/entities | no implementado | No such doc. PRD section 5 (prd:203-217) only says #355 will define them. | Deliverable missing. |
| SLOs distinguish availability, in-quota rejections, write ack, eligibility, application | parcial | PRD s.5 table lists measures, but states no SLA defined; provisional values (p99 50 ms etc.) only. | Not a #355 document; no SLO definitions. |
| Limits budget: input, mailbox, stash, pool (in flight/wait), batches, parked | no implementado | No doc. Only hard-coded MaxConns=20 (persistence/postgres/event_store.go:81). | Deliverable missing. |
| Initial values marked as hypotheses | parcial | PRD prd:215 labels figures "provisional experiment inputs". | No values set in a #355 doc. |
| Do not promise 1M r/s without measurement | cumplido | PRD s.5 "No throughput, SLA ... target is demonstrated" (prd:203); no 1M claim found in repo docs. | Negative criterion; re-check when the doc is written. |
Issue verdict: not done; only PRD placeholders exist.

### #366 [I-14] ReadSideProcessor with transaction and envelope
Dependencies: #361, #365, #373, #376, #388 (all open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Same-backend effect, applied mark and offset in one Tx | no implementado | projection.Handler.Handle(ctx, persistenceID, event, revision) has no Tx (projection/handler.go:57); offsetstore.WriteOffset separate (offsetstore/offset_store.go:42). | No ReadSideProcessor, no Tx API; blocked by #373. |
| Mark per (processor, version, scope, entity); kill -9 and two projections test | no implementado | No applied-marks table or API; no such test. | Blocked by #373/#376. |
| GlobalPrepare/Prepare idempotent under lease/fencing; mid-crash test | no implementado | grep GlobalPrepare: no hits. | Blocked by #376/#374. |
| Docs state outbox is at-least-once, destination needs idempotency | parcial | handler.go:46 says handlers should be idempotent (at-least-once on restart). No outbox doc. | Outbox absent (#395). |
| Single tenant: starts without tenancy extension; envelope exposes Unscoped/fixed; PerScope/SharedCell validated | no implementado | No envelope/PerScope/SharedCell code. | Blocked by #424 and #361. |
Issue verdict: not started; Handler SPI is still the legacy Tx-less one.

### #372 [I-18] Extract and publish the Go modules
Dependencies: #365, #368, #389 (open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| No replace to GoAkt forks (#326 resolved) | no implementado | go.mod:93 `replace github.com/tochemey/goakt/v4 => github.com/pablogore/goakt/v4 v4.5.7-actorof.1`; #326 merged only as a temporary replace. | Replace still present (upstream PR #1447 unreleased). |
| Each module compiles and passes TCK against a published root version | no implementado | persistence/, projection/, tenancy/ have no go.mod; persistence/postgres uses `replace => ../../` (go.mod:6). | Extraction not done. |
| Versioning policy applied | no verificado | No policy doc found for these modules. | Missing. |
| Extraction optional, conditioned on deps/cadence/consumers | no verificado | Decision gated by #389; no record. | Not decided. |
| tenancy only with a second consumer | no verificado | No consumer evidence; gated by #389. | Not decided. |
| Keep the nested PostgreSQL adapter | cumplido | persistence/postgres/go.mod exists and is intact. | None. |
Issue verdict: not done; blocked by #365, #368, #389 and the GoAkt replace.

### #383 [U-EXT] Compose GoAkt extensions and resolve deps in PreStart
Dependencies: #347, #349 (open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Stable extension IDs, deps in PreStart, resources released | parcial | Stable ID constants (internal/extensions/extensions.go:38-79); Require/Optional in PreStart (lookup.go); engine/option.go:143-185 WithExtensions. | Persistence/offset/tenancy extensions exist, but no resource-ownership tests for pools (shared-pool close ownership undefined); tenancy is only a marker. |
| Relocated actor gets extensions of destination node | parcial | engine/engine_tenant_relocation_test.go, engine_tenant_cluster_test.go exist (tenant respawn). | Not checked as an extension-specific criterion; not verified at the new-extension level. |
| Missing/incompatible extension fails at start | cumplido | extensions.Require returns ErrMissingRequiredExtensions instead of panic (lookup.go); internal/extensions/lookup_test.go; engine/extension_sentinel_test.go. | Covers current extensions only, not new capability validation. |
| Compile-time integration, no dynamic plugins | cumplido | Extensions registered via goakt.WithExtensions in engine/option.go; no plugin loading. | None. |
| Single tenant: tenancy service only if mode requires; absence valid | parcial | Tenancy extension is a marker registered only when resolver non-nil (engine/option.go:185). | No explicit unscoped/fixed/multi mode; blocked by #347/#424. |
Issue verdict: partially satisfied by the existing extension plumbing; the new guarantees need #347/#349.

### #384 [U-CAPS] Validate adapter capabilities per role at composition
Dependencies: #347, #351, #390 (open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Journal requires conditional append, entity read, uniqueness/contiguity, declared idempotency | no implementado | compose/spec.go:327 requiredCapabilities is empty; no journal capabilities declared. Conformance suite persistence/conformance checks behavior (events.go) but not declaration. | No capability declarations/validation. |
| Feed requires stable prefix, declared eligibility, validated cursor | no implementado | No feed contract (#351 open). | Blocked by #351. |
| Destination declares common Tx or idempotent upsert, and fencing | no implementado | No destination role. | Blocked by #373/#374. |
| TCK by capability without skipping minima | parcial | persistence/conformance (Check/Report) exists for events/snapshots/state; not capability-parametrised. | Capability-driven selection absent. |
| Concrete engines only in adapters | parcial | pgx confined to persistence/postgres; no test enforces it (grep outside). | Unenforced. |
| Validate role, backend/cell, destination identity; separate resources Journal/Feed vs destination | no implementado | No role/cell model. | Blocked by #390. |
| Shared: finite max per instance, replica max, headroom; sum physical backend | no implementado | No budget code (grep). | Pending #391 design. |
| Reject SharedCell with independent destinations under shared cursor | no implementado | No SharedCell. | Blocked by #373/#393. |
| Dedicated enabled only with registry+capability | no implementado | No Dedicated code. | Phase 2 (#392). |
| Reject invalid combos in PreStart; pgx mins not availability guarantee | no implementado | compose/spec.go validates only V5/V6/V8 for ports (publisher IDs, ports, CapStart/CapReady, FixedTenant). | Not role/budget. |
| Single tenant: composition does not require tenancy for unscoped | parcial | compose V8 only checks CapFixedTenant; resolver slot optional. | No mode model; mode mismatch checks absent. |
Issue verdict: not done; only the generic adapter-descriptor validation (V8) exists.

### #385 [U-LOAD] Validate overload and integral recovery in a cell
Dependencies: #361, #378, #377, #376 (open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Measure committed writes, all projections, availability, in-quota rejections, memory, both lags, recovery | no implementado | No load harness for this; benchmark/ is a micro-benchmark module only. | Needs #355 model first. |
| Hot tenant/entity, sustained and peak scenarios | no implementado | none | Blocked by #355. |
| Per-tenant/cell capacity declared with evidence | no implementado | none | Blocked. |
| Repeat under Gate A conditions | bloqueado | Gate A (#387) not run. | Blocked by #387/#352. |
| Single tenant Unscoped hot load scenario | no implementado | none | Blocked by #424. |
Issue verdict: not started; blocked by #355 and the core.

### #386 [U-GUIDE] Examples and guide for multitenant read-side and operation
Dependencies: #366, #381, #380, #394 (open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Handlers receive visible tenant and destination Tx | no implementado | example/ has cluster, durablestate, eventssourced, saga only; no PerScope/SharedCell. | Blocked by #366. |
| Examples of crash/retry, isolated rebuild, privileged selection | no implementado | none | Blocked by #381/#366. |
| No exactly-once / external-effect idempotency promise | no verificado | Not applicable until guide exists; docs/prd states at-least-once style caveats. | Guide missing. |
| Document outbox destination key and mandatory polling | no implementado | No outbox implementation or doc (#395). | Blocked by #395/#400. |
| Connection-policy examples from #394 | no implementado | none | Blocked by #394. |
| Single tenant quickstart without tenancy config | no implementado | readme.md mentions Unscoped in changelog only; no quickstart. | Guide missing; blocked by #424. |
Issue verdict: not started; deliverable guide absent.

### #387 [G-A] Gate A: choose mechanism with single-cell evidence
Dependencies: #352, #351, #355 (open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Zero omissions, eligibility<=T, progress, >=80% throughput, p99, overload results | no implementado | No experiment results or decision record in repo (grep xid8: PRD/baseline mentions only). | #352 experiment not run. |
| Config/hardware, limits, failure criteria published | no implementado | PRD s.5 (prd:215-217) gives provisional criteria only. | Hardware/config not recorded. |
| If it fails, compare alternatives | no verificado | Nothing to compare yet. | Conditional on result. |
Issue verdict: not done; gate decision absent.

### #388 [G-B] Gate B: core TCK and injected faults green
Dependencies: #361, #353, #365, #376, #384, #385, #424 (open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Memory/testkit and Postgres pass reader, append, idempotency, isolation, cursor, crash, dedupe, parking/replay, stale executor | parcial | persistence/conformance covers events/snapshots/state (append, CAS, scope isolation); testkit tests; Postgres runs it. No new reader, cursor, parking, stale-executor cases. | Reader/parking/fencing TCK absent (#348, #375, #374). Known: B2 non-zero partition untested. |
| Boundaries verified | parcial | Pre-existing architecture lane only; new rules absent (#353). | Blocked by #347/#353. |
| Overload/recovery documented | no implementado | none | Blocked by #385. |
| Zero exceptions invalidating guarantees | no verificado | Open defects: #427 (B4 actor name collisions), #428 (TenantAdopter fails on Postgres); exception list does not exist. | Cannot be claimed. |
| Single tenant unscoped/fixed + multitenant accepted in memory and Postgres | parcial | Unscoped scope tests in testkit/scope_test.go and persistence/postgres/event_store_test.go; engine fixed-resolver tests. No unified matrix. | #424 not complete; #428 affects Postgres adoption. |
Issue verdict: not met; blocked by many open dependencies. Known findings #427, #428.

### #389 [G-C] Gate C: SPI stability in real use before extraction
Dependencies: #366, #367, #379, #380, #381, #386 (mostly open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Real use recorded, SPI no incompatible change in window, independence criteria, GoAkt ecosystem interest | no implementado | No record/evaluation doc. | Phase 2; window not defined. |
| Tenancy requires second consumer | no verificado | No consumer evidence. | None recorded. |
| No obligation to contact maintainers | cumplido | Negative statement; nothing in repo requires it. | None. |
| Shared/Dedicated lifecycle evidence from #394 | bloqueado | #394 open; no Dedicated code. | Blocked by #394. |
| Assess #395-#398/#419-#422 impact without requiring completion | no verificado | Packages do not exist. | Deferred by design. |
Issue verdict: not done; premature (depends on phase-2 work).

### #391 [U-POOL-BUDGET] Validate connection budget per backend and replicas
Dependencies: #390, #355, #368, #380, #384, #388 (open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Budget groups by real physical backend | no implementado | grep: no budget code; pools sized ad hoc (persistence/postgres/event_store.go:81 MaxConns=20; offset_store.go parses cfg separately). | Not started. |
| Sums Shared+Dedicated max per instance x max replicas + headroom/admin/external | no implementado | none | Not started. |
| Reference formula documented (instance sum x replicas + headroom <= allocated) | no implementado | none | Not documented. |
| Mins vs maxes distinct, no double count | no implementado | none | Not started. |
| Over-allocated, unbounded max, missing replica max rejected pre-start | no implementado | none | Not started. |
| Dedicated provisioning consumes an admitted reservation | no implementado | No Dedicated registry (#392). | Blocked by #392. |
| Limit/scale change requires revalidation, rolling-update coexistence | no implementado | none | Not started. |
| Document external load can exhaust DB; no absolute availability | no implementado | PRD prd:133 has a generic statement. | Not in a guide. |
| ADR leaves global dynamic admission as future extension | no implementado | PRD risks table prd:256 says "requires a future ADR"; no ADR. | ADR missing (#347). |
| Single tenant budget counts pools/roles/all replicas | no implementado | none | Not started. |
Issue verdict: not started.

### #394 [U-POOL-E2E] Compose and verify Shared, Dedicated and DedicatedCell
Dependencies: #392, #391, #393, #380, #383, #384, #385 (open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Per tenant/role Shared or Dedicated; DedicatedCell | no implementado | No policy code (grep Dedicated: only PRD docs). | Blocked by #392. |
| Resolve deps in PreStart; reject invalid policies/over-budget before accepting work | no implementado | none | Blocked by #391/#383. |
| Examples: tenant A exclusive pool, B another, standard shared with quotas | no implementado | none | Not started. |
| Contention: noisy Shared stays under quota; Dedicated does not lend | no implementado | none | Blocked by #380/#392. |
| Aggregate maxima, cancel, reconnect, drain, shutdown, rolling update | no implementado | none | Not started. |
| Journal/Feed/destination roles, SharedCell compatible, reject independent destinations | no implementado | none | Blocked by #393/#384. |
| Comparable bounded-cardinality metrics per policy | no implementado | none | Not started. |
| Guide: CPU/IO/locks shared, no availability guarantee | parcial | PRD prd:133 states it; no guide. | Guide absent (#386). |
| Reproducible evidence in integration/load lane | no implementado | inttest/ has flows only. | Not started. |
| Single tenant matrix unscoped/fixed/multi | no implementado | none | Blocked by #424. |
Issue verdict: not started.

### #424 [U-TENANCY-MODES] Explicit single and multitenant support in config and core
Dependencies: #346 (done), #347, #349, #383, #384, #390 (open)
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| Default config works with no tenant ID/resolver/catalog/tenancy extension | parcial | Engine without resolver is legacy non-tenant mode (internal/extensions/extensions.go:75-79 marker absent; tests engine/*). persistence.Unscoped() exists. | No explicit SingleTenantUnscoped mode or API; persistence still imports tenancy (#349). |
| Fixed identity validated; rejects other identity; multitenant validations kept | parcial | tenancy.WithSingleTenant (tenancy/resolver.go:77), FixedTenantResolver; engine/engine_fixed_tenant_resolver_test.go; compose V8 CapFixedTenant. | Not a configurable mode with scope binding; names pending ADR (#347). |
| Unscoped preserves keys/data; identified scope needs explicit adoption | parcial | migration.TenantAdopter exists (migration/tenant_adoption.go). | #428 TenantAdopter fails on Postgres (maxReplaySequence exceeds int8); no explicit "no silent reinterpret" test found. |
| Selection, cursor, idempotent commands, snapshots, offsets, markers keep explicit scope in all modes | parcial | Journal/snapshot/state stores take Scope (persistence/events_store.go). offsetstore.WriteOffset has no scope (offsetstore/offset_store.go:42-46); no cursor/command-ID/marker model. | Offsets/cursor/commands not scoped (#357, #361). |
| Trivial router/default cell, global bounded resource profile per role | no implementado | No ScopeRouter (#368) or resource profiles (#390). | Blocked. |
| Absence of tenancy not confused with missing required extension | parcial | extensions.Require is for required ones; tenancy marker optional. | No capability-driven requirement map (compose requiredCapabilities empty). |
| Matrix memory/testkit + Postgres: unscoped, fixed, multi, replay/crash, incompatible cursor, compatible persistence | parcial | testkit/scope_test.go, persistence/conformance (OneScope names, scope isolation), postgres event_store_test.go. | No cursor test; no fixed matrix; B2 non-zero partition untested; #428 on Postgres adoption. |
| Authorization remains: omitting tenant ID gives no global privilege | parcial | engine tests engine_tenant_administrative_scope_test.go; Unscoped distinct from all scopes. | Not verified against new modes. |
| Product/integration/workflow scenarios incorporate modes later | no verificado | Deferred by design. | Out of core scope. |
Issue verdict: partially present as legacy capability (Unscoped, FixedTenantResolver); the explicit mode and new guarantees are not implemented.

## Summary count
Total criteria: 104; cumplido 5, parcial 22, no implementado 66, no verificado 9, bloqueado 2

## Group B: persistence (#342)

### #342 [Epic] Journal y lectura correctos
Dependencies: #346, children #348-#363, #377, #378, #390, #392; coordination #343-#345, #395-#398, #424.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Issues del alcance completados o diferidos" | no implementado | All children open; see per-issue blocks | #346 still open (docs/prd/i-00-baseline-develop.md) |
| "Garantías demostradas por pruebas y condiciones operativas documentadas" | no implementado | No reader TCK, no xid8 experiment, no README operational conditions | Blocked by #348/#352/#360 |
| "Imports y capacidades respetan la arquitectura" | parcial | `go list -deps ./persistence` includes `urd/tenancy` and `egopb` (persistence/scope.go:27) | Violates "no importa tenancy" (#349) and journal types (#363) |
| "Migraciones/compatibilidad y guía de uso actualizadas" | no implementado | No new schema/migration; MIGRATION.md adoption path broken on Postgres (#428) | Blocked by #358/#359; #428 |
Issue verdict: epic correctly open; all four closure criteria unmet.

### #348 [I-02] TCK del lector: omisiones, elegibilidad y progreso
Dependencies: #346 (I-00).
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "check de omisiones falla de forma determinista con el adapter PostgreSQL actual y queda marcado como fallo conocido" | no implementado | No a@100/b@200/late@150 check in persistence/conformance; no known-failure marker mechanism (grep empty); gap acknowledged only in comment persistence/events_store.go:169-172 | Write check with two concurrent Tx and a known-fail annotation in Check/report.go |
| "elegibilidad y progreso no prometen latencia de aplicación" | no implementado | No eligibility/progress checks exist | Deliverable absent |
| "todos corren también contra testkit" | no implementado | testkit/conformance_test.go:54 runs EventsStoreChecks, but no reader checks exist; in-memory store has no commit-order gap, so the omission check would pass or need a different oracle | Absent; testkit semantics differ |
Issue verdict: not started; only timestamp-tie paging checks (#330) exist, which do not cover late commits.

### #349 [I-03] Scope agnóstico de tenant
Dependencies: #347 (I-01, ADR); #346.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "persistence no importa tenancy" | no implementado | persistence/scope.go:27 imports `urd/tenancy`; `NewTenantScope(tenancy.TenantID)` and `TenantID()` return that type; `go list -deps ./persistence` lists tenancy | Scope must become opaque prefix; tenancy builds it |
| "clave persistida sigue igual ('' Unscoped, tenant ID si no), sin migración" | parcial | Current mapping holds: persistence/postgres/event_store.go:43-57 `scopeKey` -> `string(scope.TenantID())`; schema key (tenant_id, persistence_id,...) | True today; guarantee for the refactored opaque Scope untested |
| "la conformance actual pasa" | cumplido (baseline) | testkit and inttest/flows/eventstore pass today | Must be re-run after the refactor; trivially true now |
| (single tenant) "Preservar clave Unscoped sin migración; sin tenant ID ficticio; adopción requiere migración explícita" | parcial | Unscoped key '' preserved (scope.go "Backward compatibility"); docs/prd/i-00-baseline-develop.md "Single tenant" says adoption is not automatic; adoption fails on Postgres, #428 | Adoption path broken (#428); explicit-migration decision not documented as a guide |
Issue verdict: behavior to preserve exists, but the structural goal (no tenancy import) is not met.

### #350 [I-04] Slices fijos independientes de GoAkt
Dependencies: #351 (I-05).
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "N decidido y documentado (256 o 1024)" | no implementado | No N or slice function; docs only reference B2 | Decision absent; blocked by #351 |
| "test de que el slice no cambia con 1, 3 y 5 nodos" | no implementado | Shard still `ActorSystem().Partition(id)` at internal/engine/eventsource/event_sourced_actor.go:353 and durablestate/durable_state_actor.go:167; no test with non-zero partition (I-00 notes) | Needs pure function plus test; related #427 (actor names feed persistenceID) |
| "plan de migración de shard_number y offsets existentes" | no implementado | schema 005_offsets_store.sql keyed (projection_name, shard_number); no plan doc | Feeds #359; blocked by #351 |
Issue verdict: not started; B2 consequence still present (shard depends on GoAkt cluster).

### #351 [I-05] Spec: contrato de lectura con prefijo estable
Dependencies: #348 (I-02); related #352, #387.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "contrato nuevo aprobado en la ubicación vigente" | parcial | Only the PRD docs/prd/urd-platform-prd.md:114 describes stable prefix, OneScope, AllScopesInCell, ErrCursorMismatch; openspec/ has no active spec | PRD is not an approved spec in the spec location |
| "no reactivar specs archivadas automáticamente" | cumplido | docs/archive/... preserved, openspec/README.md says not active | Maintain constraint |
| "casos de rechazo del cursor definidos" | parcial | PRD:114 names ErrCursorMismatch and slice-range incompatibility only; no exhaustive cases | No spec enumerating cases (format, cell, fingerprint, range) |
| "condiciones de la cota ≤ T declaradas ... y qué pasa si se rompen" | parcial | PRD:219 lists dedicated cluster, transaction_timeout, max_prepared_transactions=0, oldest-XID alert and states bound is not claimed if violated | Conditional on xid8; not in an approved contract; blocked by #352 |
| "reglas de portabilidad" | no verificado | PRD mentions capabilities per role; no explicit rules for offset in destination backend / Tx / slice grouping found in the code or spec location | Needs spec text |
| "identidad del offset y de la marca de aplicados" | parcial | PRD:114 describes PerScope/SharedCell checkpoint identity; code keys by (projection_name, shard) only | Spec not approved; code gap tracked in #362 |
| "GetShardEvents deprecado con plan de retiro" | no implementado | persistence/events_store.go:140-170 has no Deprecated marker or retirement plan | Add deprecation and plan (#370 related) |
| (single tenant) "OneScope(Unscoped()) como lectura single tenant válida; AllScopesInCell privilegiado" | parcial | PRD:36 states reads use `OneScope(Unscoped())`; type does not exist in code | Spec only in PRD |
Issue verdict: requirements are drafted in the PRD but no approved contract or deprecation exists; blocked by #348 ordering.

### #352 [I-06] Experimento: horizonte xid8 en PostgreSQL
Dependencies: #348, #355 (I-20).
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "0 omisiones y progreso según I-02" | bloqueado | No experiment; depends on #348 oracle (absent) | #348 |
| "throughput ≥ 80 % del control en el shard caliente" | no implementado | benchmark/ has only actor benchmarks (benchmark_test.go: BenchmarkEventSourcedActor_*); no xid8/reader benchmark | No load model (#355) |
| "p99 ≤ transaction_timeout + 1 s con transacción larga en otra base" | no implementado | No such measurement (grep xid/horizon in benchmark empty) | #355 |
| "resultados en benchmark/" | no implementado | No result files | Experiment not run; xid8 remains candidate per PRD:219 |
Issue verdict: not started; the known gap is documented at persistence/events_store.go:169-172.

### #354 [I-19] TCK: append contiguo sin huecos
Dependencies: #346.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "checks de conformance para lote con hueco, lote solapado y lote correcto" | no implementado | EventsStoreChecks (events.go:57-74) has CAS/genesis checks only (inttest/testkit pass) but none with gap/overlap batch | Add 3 checks |
| "si hoy no se cumple, el adapter se corrige en este issue" | no implementado | Postgres writeConditional (persistence/postgres/event_store.go:~265-310) compares revision only, never validates events[0].seq == revision+1 or contiguity; `highest = max(...)`. testkit newEventLog (eventstore.go:248-275) merges/overwrites by sequence, accepts gaps and overlaps (replaces same seq) | Both adapters accept gap and overlap on a revision match; by reading, not by running a failing test |
Issue verdict: adapters most likely violate the criterion; neither the TCK nor the fix exists.

### #357 [I-23] Identidad de comandos y reintentos
Dependencies: #346.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "commit exitoso con respuesta perdida y reintento devuelve resultado original" | no implementado | No command_id table/store; grep idempot/command_id finds nothing in persistence or engine | Absent |
| "mismo ID con otra huella rechazado" | no implementado | Same | Absent |
| "comando exitoso sin eventos queda registrado" | no implementado | Same | Absent |
| "rechazo de dominio determinista memorizado" | no implementado | Same | Absent |
| "Ventana de retención de IDs declarada" | no implementado | No doc | Absent |
| "verificado si command-envelope ya trae el identificador" | parcial | command/envelope.go + command/metadata.go carry OperationID/CorrelationID/CausationID; archived spec states operation_id MUST NOT double as an idempotency key (docs/archive/.../command-envelope/spec.md:81) | Finding exists only in archived spec; not recorded in this issue; no client idempotency ID |
| (single tenant) "dedupe con Unscoped y scope fijo; mismo ID en scopes distintos no comparte" | no implementado | No dedupe | Absent |
Issue verdict: not started; the envelope has operation identity but intentionally not an idempotency key.

### #358 [I-09a] postgres: definición del esquema
Dependencies: #351, #352, #387 (G-A).
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "DDL revisado" | bloqueado | schema/ has 001-005 only (no xid8 column, no applied-marks table, offsets_store cursor is BIGINT `current_offset`) | Blocked by Gate A (#387), #352, #351 |
| "plan de consulta sin escaneo secuencial con un millón de filas" | bloqueado | No 1M-row plan test; current indexes 002 are single-column (timestamp, shard_number) | Same |
Issue verdict: blocked; no schema work done.

### #359 [I-09b] postgres: migración del esquema
Dependencies: #358, #350.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "migración idempotente en persistence/postgres/schema" | bloqueado | Migrator exists (schema_migrator.go, advisory lock, per-version tx, README) but no migration for the new schema | #358, #350 |
| "prueba de actualización desde el esquema actual con datos" | bloqueado | inttest/flows/eventstore/schema_test.go tests upgrade of legacy->current (001-005), not to the new schema | Same |
| "plan de corte para los offsets existentes" | bloqueado | No plan | Same; also #362 |
Issue verdict: infrastructure reusable, deliverables blocked by #358/#350.

### #360 [I-08a] lectura nueva en los adapters
Dependencies: #359.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "checks de I-02 pasan en ambos adapters" | bloqueado | Checks (#348) and reader absent | #348, #359 |
| "rechazos de cursor de I-05 tienen tests" | bloqueado | No cursor type | #351 |
| "transaction_timeout y max_prepared_transactions = 0 documentados en README del adapter" | no implementado | persistence/postgres/README.md has no mention (grep empty); only in PRD:219 | Add after mechanism chosen; blocked |
Issue verdict: blocked; no new reader exists.

### #363 [I-11] egopb: separar los tipos del journal
Dependencies: #347 (I-01).
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "cierre de dependencias de persistence no incluye mensajes del engine" | no implementado | `go list -deps ./persistence` includes `urd/egopb`; protos/ego/ego.proto holds Event, Offset, Snapshot, DurableState together with CommandReply, StateReply, SagaLifecycleStatus, TenantBinding*; offsetstore uses egopb.Offset/ProjectionId | Split journal types from engine protocol |
| "decisión del ADR (I-01) aplicada" | bloqueado | #347 open; no ADR in docs | #347 |
Issue verdict: not started; B7 confirmed by one shared egopb package.

### #377 [P-HELPER] Helper de actor persistente
Dependencies: #349, #354, #357, #383.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Recuperación de snapshot+journal antes de aceptar comandos" | parcial | Exists inside Urd's actor: event_sourced_actor.go:295 PreStart, recover :614, recoverFromSnapshot :680, run in the PreStart chain :579; restart tested in inttest/flows/restart (TestEngineRestart_RecoversEntityFromPostgres), tenancy W6 | Not extracted as a generic helper; not through GoAkt extensions (#383) |
| "Stash durante append tiene límite y rechazo inmediato" | no implementado | ctx.Stash() at event_sourced_actor.go:387,397,1098,1481,1573 with no bound or rejection (grep for limit empty) | Unbounded stash |
| "Batching mantiene append contiguo e idempotencia" | parcial | Batching exists (flushBatch, expected revision resolve, TestResolveBatchPrecondition); contiguity not enforced by the store (#354); no idempotency (#357) | Blocked by #354/#357 |
| "Fallas de persist/reinicio probadas" | parcial | TestPendingRequestsAreAnsweredWhenTheWriteFails, events_writer_actor_test.go, phaseStopping, restart test over Postgres | Not against the extracted helper; no persist-fault matrix |
| "aislamiento de snapshots por scope" | cumplido (store level) | persistence/conformance/snapshot.go:57-62 isolation checks; event_sourced_actor_scope_test.go | Helper-level scope test after extraction |
Issue verdict: behavior exists inside Urd handlers but helper not extracted; stash bound missing, dependencies open.

### #378 [P-POOL] Acotar operaciones en vuelo y espera del pool PostgreSQL
Dependencies: #355 (I-20), #390, #384, #380 (compose), #424.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Máximo de operaciones y solicitudes esperando conexión configurables" | no implementado | Only hard-coded `cfg.MaxConns = 20` (persistence/postgres/event_store.go:81, offset_store.go:57); no waiter limit | No config surface |
| "al llenar, rechazo inmediato identificable" | no implementado | pgxpool blocks until ctx ends; no sentinel error | Absent |
| "Deadline adicional, cancelación libera cupo" | no implementado | Only caller ctx; no admission permit | Absent |
| "saturación no produce espera/memoria ilimitada" | no implementado | Unbounded waiters in pgxpool | Absent |
| "Métricas de ocupación y rechazos" | no implementado | No pool metrics in postgres module | Absent |
| (ext) cupos por Scope opaco / Shared sin importar tenancy | no implementado | No quota code | Needs #390 SPI |
| (ext) "sin tenancy mantener límites del pool" | parcial | Fixed MaxConns=20 applies regardless | Not configurable |
| (ext) release y Commit/Rollback en toda ruta | parcial | `defer tx.Rollback` in writeUnconditional/writeConditional (event_store.go ~L190, L270) | Other paths and permit release not audited |
| (ext) config Shared con máximo finito y presupuesto validable (#384) | no implementado | Hard-coded 20; no budget validation | #384 |
| (ext) métricas acotadas sin DSN | no implementado | None | Absent |
| (single tenant) cupos genéricos sin tenant ID | no implementado | None | Absent |
Issue verdict: not started; only a fixed pool size exists.

### #390 [P-POOL-SPI] Contrato de selección de recursos por Scope, celda y rol
Dependencies: #346, #347, #355; complements #349, #378.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Contratos y ADR distinguen política, selección, admisión y lifecycle" | no implementado | No selector/ADR; only PRD:127 (C-02) | Absent |
| "Configuración por defecto Shared conserva el comportamiento existente" | no verificado | Default today is a single pool per store; no SPI to compare | Absent |
| "Journal/Feed y ProjectionDestination identidades y capacidades independientes" | no implementado | No role types; PRD C-02 only | Absent |
| "Context cancelable, errores de saturación/configuración y propiedad de release" | no implementado | Not specified | Absent |
| "Identidad de destino transaccional coherente verificable" | no implementado | None | Absent |
| "Ejemplos Shared, Dedicated y DedicatedCell sin importar tenancy" | no implementado | None (I-00 table: no symbol) | Absent |
| "Tests de contrato con mocks y registro de garantías/limitaciones" | no implementado | None | Absent |
| (single tenant) "Selector acepta Unscoped y perfil default" | no implementado | None | Absent |
Issue verdict: not started.

### #392 [P-POOL-DEDICATED] Registry y lifecycle de pools exclusivos por scope
Dependencies: #390, #391, #368, #379, #380, #378, #388.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Pool exclusivo por identidad de recurso y scope con máximo obligatorio" | bloqueado | No registry; blocked by #390/#378 | Phase 2 |
| "Creación lazy concurrente produce una sola instancia; creación/espera acotadas" | bloqueado | Absent | Same |
| "Reserva presupuestaria admitida antes de crear/calentar; fallo libera" | bloqueado | Absent (#391) | Same |
| "Conexiones dedicadas no se prestan a otro tenant; sin fallback silencioso al Shared" | bloqueado | Absent | Same |
| "Idle eviction/rotación/cierre drenan conexiones y Tx" | bloqueado | Absent | Same |
| "Cambios de credenciales o celda con identidad/versionado y drenaje" | bloqueado | Absent | Same |
| "PostStop de un actor no cierra un pool compartido" | bloqueado | Absent; currently Disconnect closes the store's own pool (event_store.go:~109) | Same |
| "Métricas con cardinalidad acotada y sin secretos" | bloqueado | Absent | Same |
| "Mínimo configurado no se presenta como garantía de disponibilidad" | bloqueado | Absent | Same |
Issue verdict: phase 2, entirely blocked and not started.

---
Summary count of criteria per state (78 rows incl. epic, ext and single-tenant lines): cumplido 3 | parcial 14 | no implementado 41 | no verificado 2 | bloqueado 18

## Group C: projection (#343) and tenancy (#344)

### #343 Epic: projection engine
Dependencies: #356 #361 #362 #364 #365 #373 #374 #375 #376 #367 #370 #371 #393 (children), #342 #344 #345 #346 #366 #386 #395-#398 #423 #424.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Issues del alcance completados o diferidos" | no implementado | Children #356 to #376, #393 audited below; none complete. #362 and #371 audited separately. | Epic is open with the whole phase 1 and 2 scope pending. |
| "Garantías demostradas por pruebas y condiciones operativas documentadas" | no implementado | Only at-least-once is demonstrated (`runner_test.go`, `TestRunnerPagesThroughTimestampTies`). | No atomicity, fencing, parking or crash tests; blocked by #373/#374/#375. |
| "Imports y capacidades respetan la arquitectura" | parcial | `TestArchitectureProjectionRunnerStaysRuntimeNeutral` passes (run). It only forbids GoAkt, engine and internal/extensions. | Runner still imports encryption, eventadapter, eventstream, instrumentation; see #364/#365. |
| "Migraciones/compatibilidad y guía de uso actualizadas" | no implementado | Offsets schema unchanged (005); no migration for new identity or marks. | Blocked by #362 and #373. |

Issue verdict: epic not closable; all four closure criteria are open or only partly met.

### #356 [I-22] Error policy and parked-entity contract
Dependencies: #348 (I-02), epic #343, #375.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Contrato y casos de TCK: falla 5 y llega 6, marca permanece en 4" | no implementado | `projection/recovery.go` has only Fail/RetryAndFail/RetryAndSkip/Skip; no applied mark. `testkit/offsetstore.go` has no parking case. | No contract text, no TCK case, no per-entity mark. |
| "otras entidades progresan" | no implementado | `eventError` stops the whole runner (`runner.go` processEvents/`onFailure`); Skip policy drops the event to dead letter. | Runner halts or skips; it never parks one entity. |
| "Estados estacionada→recuperando→activa, máximo de estacionadas y auditoría de saltos definidos" | no implementado | grep for park/estacion: nothing. | No states, limit or audit defined. |
| "Replay incluye evento 7 concurrente" | no implementado | None. | Not defined. |
| "Implementación transaccional en P-ERROR" | bloqueado | Depends on #375, #373, #374. | Not started. |
| "este ticket no exige tablas antes del esquema" | cumplido | No parking tables exist; schema untouched (vacuous). | None. |

Issue verdict: design deliverable absent; current policies are per-event skip or stop, not park.

### #361 [I-08b] Runner integrates new read
Dependencies: #360, #362, #375, #373, #374.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "el runner ya no llama a GetShardEvents" | no implementado | `runner.go:596` `x.eventsStore.GetShardEvents(...)`. | The I-08a read (#360) is absent from `EventsStore`. |
| "métricas de lag de elegibilidad y de procesamiento expuestas" | parcial | `instrumentation.go:92` has a single `urd.projection.lag_ms`; `TestProjectionRunnerLagMetrics` passes (run). | One lag only (wall clock minus offset); two lags not split. |
| "batches acotados por cantidad, bytes y tiempo" | parcial | `WithMaxBufferSize` (count, a target not a cap per option.go doc). | No byte or time bound. |
| "la conformance pasa de punta a punta" | bloqueado | Depends on #360/#362/#373/#374/#375. | No new conformance suite for the read. |

Issue verdict: not started; blocked by five unimplemented dependencies.

### #364 [I-12] Decouple the runner
Dependencies: #353 (I-07).
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "internal/projectionrunner importa solo persistence, offsetstore y egopb" | no implementado | `runner.go` imports encryption, eventadapter, eventstream, `internal/instrumentation`, projection (also `option.go`). | Four extra imports remain. The arch test (`TestArchitecture...`) does not check this allowlist. |
| "Urd arma el decodificador con cifrado y adapters de eventos" | no implementado | `processEnvelope` decrypts and adapts inside the runner; `projection_actor.go:~110` passes `WithEncryptor`/`WithEventAdapters`. | No injected decoder or metrics interface. |

Issue verdict: not met; runner still owns decryption, adapting and OTel instruments.

### #365 [I-13] Single root package
Dependencies: #362, #363, #364.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "projection importa solo persistence (más egopb)" | no implementado | `offsetstore/` and `projection/` are separate root packages; `internal/projectionrunner` stays internal with the extra imports above. | Nothing moved under `projection/`; no compat aliases. |
| "las excepciones de I-07 para projection se eliminan" | bloqueado | Blocked by #364 and #362. | Exceptions still needed. |

Issue verdict: not started.

### #367 [I-15] Slice-range distribution
Dependencies: #350, #366, #374, #388.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "reasignación al entrar o salir un nodo" | no implementado | `engine/projections.go:95` uses `SpawnSingleton`: one actor walks all shards (`numWorkers` pool in `runner.go`). | No range ownership or rebalance. |
| "el token se valida al escribir efectos y offsets…" | bloqueado | No token anywhere; depends on #374. | Not started. |
| "destino que no valida token se declara de un solo ejecutor" | parcial | Singleton is the de facto single executor, but no capability declaration exists. | No destination capability flag. |

Issue verdict: not met; singleton model only.

### #370 [I-24] Journal retention and version cut
Dependencies: #362, #366, #388.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "política de retención que impide a DeleteEvents borrar eventos que una versión vigente necesita" | no implementado | `events_janitor_actor.go:125` calls `DeleteEvents` from the snapshot retention count; no projection offset consulted. | A janitor can delete events a projection has not read. |
| "corte con barrera por slice…" | no implementado | No projection version, pointer or barrier (grep). | Absent. |
| "condición de pendientes: la nueva no activa entidades estacionadas…" | bloqueado | Depends on #375 parked entities. | Absent. |
| "prueba con escrituras concurrentes durante el corte donde los lectores nunca retroceden" | no implementado | No such test. | Absent. |

Issue verdict: not met; the janitor-versus-projection retention hazard is open. No existing issue found to link beyond this one.

### #373 [P-TX] Effect, mark and offset in destination Tx
Dependencies: #362, #359.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Un commit incluye efecto, marca … y offset con identidad completa" | no implementado | `processEvents` runs handlers, then separate `commitOffset` -> `offsetsStore.WriteOffset` (no shared Tx; `Handler.Handle` has no Tx). Offset key is name+shard only (see #362). | No Tx handle, no marks. |
| "Rollback no deja progreso parcial" | no implementado | Handler effects are not rolled back with the offset; only the offset is skipped on failure. | At-least-once, not atomic. |
| "Duplicado no repite efecto" | no implementado | A re-pulled batch re-invokes the handler (documented at-least-once in `processEvents`). | No applied mark. |
| "dos procesadores no comparten marcas" | no implementado | No marks exist. | n/a |
| "Pruebas de crash antes/después de commit y capacidad declarada para destinos sin Tx común" | no implementado | No crash tests; no capability declaration. | Absent. |

Issue verdict: not met; current engine is at-least-once with separate offset write.

### #374 [P-FENCE] Ownership and fencing
Dependencies: #373, #350.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Ejecutor obsoleto es rechazado al escribir efecto, marca y offset" | no implementado | No token on `OffsetStore.WriteOffset`; the runner cache comment relies on "cluster singleton" as sole writer (`runner.go` committedOffsets). | Only the goakt singleton guards it, with no write-time validation. |
| "validación junto a la mutación, sin ventana check-then-write" | no implementado | None. | Absent. |
| "Fallas inyectadas en renovación/transferencia" | no implementado | None. | Absent. |
| "Destino sin fencing obliga modo de un ejecutor" | parcial | Singleton spawn exists; no destination capability to force it. | No declaration or enforcement. |

Issue verdict: not met; the migration `AdoptionFence` (`migration/tenant_adoption.go:423`) is an unrelated per-id lock.

### #375 [P-ERROR] Parking and ordered replay
Dependencies: #356, #359, #373, #374.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Estado de estacionamiento, marca y offset se actualizan atómicamente" | no implementado | No parking store. | Absent. |
| "Falla 5/llega 6 deja marca=4 y otras entidades avanzan" | no implementado | Runner stops on error or skips to dead letter (`handleWithPolicy`). | No per-entity gating. |
| "límite activa parada del rango" | no implementado | None. | Absent. |
| "Replay 5/6 con 7 concurrente usa marca condicional y mismo fence" | no implementado | None. | Absent. |
| "salto manual auditado" | no implementado | Skip policy is automatic, not audited (`DeadLetterHandler` only). | Absent. |

Issue verdict: not started.

### #376 [P-PREP] Idempotent preparation
Dependencies: #374, #359.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Estado pendiente/listo por procesador/versión y preparación por rango" | no implementado | No prepare hook in `projection.Handler`; no state table (grep). | Absent. |
| "Una caída a mitad se reintenta sin duplicar estado" | no implementado | None. | Absent. |
| "no ejecutar handlers antes de listo" | no implementado | The runner invokes the handler immediately on Start. | Absent. |
| "propietario obsoleto no completa preparación" | bloqueado | Depends on #374. | Absent. |

Issue verdict: not started.

### #393 [P-POOL-ROUTING] Destination pool selection
Dependencies: #390 #392 #368 #373 #374 #380 #388.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Resolver pool del destino antes de abrir Tx…" | no implementado | No destination or pool concept in runner; handler gets no Tx. | Absent. |
| "La misma Tx conserva efecto, marca, offset y validación de fencing" | bloqueado | #373/#374. | Absent. |
| "No reutilizar por accidente pool de Journal/Feed como destino…" | no implementado | No roles. | Absent. |
| "Batches con scopes diferentes definen partición/admisión/selección" | no implementado | Runner is single-scope: `WithScope`, `ErrScopeRequired`, `TestRunnerRequiresAScope` (not run). | No multi-scope (SharedCell) mode. |
| "SharedCell con pools tenant distintos… pruebas explícitas" | no implementado | No SharedCell type in `persistence/scope.go` (only Unscoped/tenant). | Absent. |
| "SharedCell con destinos independientes… se rechaza con error accionable" | no implementado | None. | Absent. |
| "PerScope soporta selección de destino compatible con su propio checkpoint" | parcial | Runner is per-scope today (one scope per runner), but the offset key lacks scope (see #362) and there is no destination selection. | Offsets not scope-keyed. |
| "Saturación/cancelación de un pool no avanza offsets" | bloqueado | #373/#375. | Absent. |

Issue verdict: not started; no read-side pool routing.

### #344 Epic: tenancy
Dependencies: #368 #369 #379 #380 #381 #382 (children), #346 #342 #343 #345 #388-#398 #424.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Issues del alcance completados o diferidos" | no implementado | #368/#369/#380/#382 not started; #379/#381 audited separately (#381 not met). | Open. |
| "Garantías demostradas por pruebas y condiciones operativas documentadas" | parcial | Tenant isolation conformance: `persistence/conformance`, `inttest/flows/tenancy/conformance_test.go`. | Cells, quotas, delete, rebuild untested. |
| "Imports y capacidades respetan la arquitectura" | no verificado | `tenancy/` is not scanned by an import test in this audit. | Not checked. |
| "Migraciones/compatibilidad y guía de uso actualizadas" | parcial | `migration/tenant_adoption.go` exists; known #428 (TenantAdopter fails on Postgres). | Guide and Postgres path incomplete. |

Issue verdict: epic partly grounded (isolation, adoption) but phase 2 scope absent.

### #368 [I-16] ScopeRouter and cells
Dependencies: #349, #360, #388.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "test de un tenant asignado a otra celda sin cambios en el engine" | no implementado | grep ScopeRouter/cell: no match. `persistence.Scope` has only Unscoped/Tenant. | No router, no cells. |
| "la configuración por defecto se comporta como hoy" | no implementado | No router to default (current behaviour exists but is not behind an abstraction). | Absent. |
| "los slices no cambian al cambiar la celda" | parcial | Slice = `Partition(persistenceID)` (`event_sourced_actor.go:353`), independent of tenant and storage; no cell notion to test against. | No test. |
| "Distinguir roles del backend y selección de Journal/Feed frente al destino" | no implementado | No roles. | Absent. |
| "Incluir identidad/versionado del recurso para migración y drenaje" | no implementado | None. | Absent. |
| "Cambiar solo pool no exige PerScope ni cambia slices" | bloqueado | #393. | Absent. |
| "Router trivial/celda por defecto soporta Unscoped sin catálogo…" | no implementado | No router. | Absent. |

Issue verdict: not started.

### #369 [I-21] Tenant migration between cells
Dependencies: #367, #368.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "prueba de punta a punta: corte de escrituras, copia preservando (scope, entidad, seqNr), transferencia de ownership…, invalidación de cursores xid8…, reapertura" | parcial | `migration/tenant_adoption.go` copies and verifies (scope,id,seq), has `WithAdoptionFence` per-id lock; tests `TestTenantAdopterRealRunCopiesAndKeepsSource`, `...EndToEndRecoveryThroughRealActor` (not run). Known #428: fails on Postgres. | No write cut-off, no ownership transfer, no cursor invalidation, no cells; Postgres broken (#428). |
| "cero omisiones según el oráculo" | no verificado | Adopter verifies copies; no oracle test across migration. | No oracle test; fails on Postgres (#428). |

Issue verdict: partial precursor (adoption within one store); protocol not implemented.

### #380 [T-QUOTA] Quotas and admission
Dependencies: #355, #379, #368, #390.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Cuotas configurables por tenant" | no implementado | grep quota/token bucket/rate limit: no match in non-test code. | Absent. |
| "máximo de concurrencia/espera" | no implementado | None. | Absent. |
| "rechazo inmediato medido" | no implementado | None. | Absent. |
| "Prueba de tenant ruidoso … métricas por clase" | no implementado | None. | Absent. |
| "No construir HTTP/gRPC nuevos" | cumplido | No new transport added (vacuous). | None. |
| "Tenancy produce perfiles Shared o Dedicated…; no importa pgx" | no implementado | No profiles. | Absent. |
| "Shared aplica cupos de concurrencia y espera por scope…" | no implementado | None. | Absent. |
| "Dedicated reserva presupuesto y utiliza pool exclusivo…" | no implementado | None. | Absent. |
| "DedicatedCell combina routing… y política de pool" | bloqueado | #368. | Absent. |
| "Cupos de token bucket… y cupos de adquisición de conexión se distinguen" | no implementado | None. | Absent. |
| "Reservas aceptadas entran en el presupuesto de deployment" | no implementado | None (#391). | Absent. |
| "No afirmar aislamiento CPU/IO/locks…" | cumplido | No such claim in code (vacuous). | None. |
| "Aplicar políticas por tenant solo cuando tenancy está configurada…" | no implementado | No policies. | Absent. |

Issue verdict: not started.

### #382 [T-DELETE] Coordinated tenant deletion
Dependencies: #379, #368, #374, #370.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Política de retención/auditoría aprobada antes de borrar" | no implementado | No policy doc or code. | Absent. |
| "Revocar acceso y drenar escrituras" | no implementado | grep delete-tenant: none. | Absent. |
| "detener ownership" | bloqueado | #374. | Absent. |
| "eliminar journal, snapshots, offsets, marcas y read models según política" | parcial | Per-id `DeleteEvents`/snapshot delete are scoped and conformance-tested (`persistence/conformance/events.go:159`, `snapshot.go:145`). | No tenant-wide purge; offsets have no tenant column; no marks. |
| "Operación reanudable/idempotente y solicitudes antiguas no recrean tenant" | no implementado | None. | Absent. |

Issue verdict: only per-entity scoped deletes exist; no coordinated deletion.

---
Summary count (criteria): cumplido 3 | parcial 11 | no implementado 55 | no verificado 2 | bloqueado 11 (total 82)

## Group D: integration (#395) and testkit (#396)

### #395 [Epic][integration] Publicacion durable y outbox sobre projection
Dependencies: #399, #400, #401, #402, #403 (children); coordination #342, #343, #344, #345; #346, #347 (validation).

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Tareas completadas o diferidas por decision explicita" | no implementado | All 5 children open; no outbox/relay code. | #399-#403 pending. |
| "Compatibilidad con capacidades existentes y evidencia de recuperacion/aislamiento" | parcial | Existing publish path untouched; tenant delivery check `engine/streams.go:90-115`; PT-4 scope conformance (`publishingtest.go:245`). | No recovery/isolation evidence for outbox (does not exist). |
| "Imports respetan ADR y lifecycle se integra mediante extensiones GoAkt" | bloqueado | ADR #347 open; no integration package exists to check imports. | Blocked by #347. |
| "Guia y escenarios ejecutables reflejan garantias reales" | no implementado | No guide/example for durable publication; `port/publishing/publishing.go:41-50` states no delivery semantics. | Needs #403. |
| Single-tenant paragraph: "Productor, envelope, outbox y relay admiten scope Unscoped o fijo" | no implementado | None of those components exist. | Via #399-#401. |

Issue verdict: not started; current publisher path is non-durable in-memory fan-out with failures dropped.

### #399 [integration][IN-CONTRACT] Contrato de eventos de integracion y ACK por adapter
Dependencies: #395, #346, #347, #357, #384, #388.

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Envelope con Scope opaco, ID estable, version, topic/key, causation/correlation, payload versionado" | no implementado | `egopb.Event` (`protos/ego/ego.proto:10-33`) has persistence_id, seq, tenant_metadata; no integration envelope. Causation/correlation exist only in `command/carrier.go:41-42`. | Envelope and mapper type absent; #357 (command identity) open. |
| "Transformacion journal->integration-event explicita y reproducible; evolucion documentada" | no implementado | No mapper/injection point; `Handler.Handle` receives `anypb.Any` only. | Depends on #361/#366 integration path. |
| "Declarar capacidades ACK/fallo de cada publisher" | no implementado | `port/adapter/adapter.go:73,78` only `CapStart`, `CapReady`; port doc silent on ACK (`publishing.go:41-50`). | No ACK capability; blocked by #384 (caps by role). |
| "Reintentos conservan ID y destino logico; destino requiere dedupe" | no implementado | Publish has no retry nor event ID; Kafka key = persistence_id only (`publisher/kafka/kafka.go:108`). | Needs ID (this issue) and relay (#401). |
| "No prometer exactly-once externo ni incorporar consumidores/brokers nuevos" | cumplido | grep: no exactly-once claim for publishing; only the 4 existing publishers. Vacuous: nothing new promised. | Keep when the contract is written. |
| "ID estable incluye productor logico, Scope, evento/comando fuente y clave/ordinal de salida" | no implementado | No such ID anywhere. | Absent. |
| "Politica de version/rebuild: reejecutar proyeccion no republica" | no implementado | `Engine.RebuildProjection` resets offset globally by name (`engine/projections.go:172-185`), so a rebuild would replay everything; no dedupe policy. | Needs policy; also #362/#381 (offset identity) per baseline. |
| Single tenant: "Envelope/identidad admite Unscoped o fixed, sin tenant obligatorio independiente del scope" | no implementado | `eventstream.Scope`/`persistence.Scope` support Unscoped (`engine/streams.go:93-97`) but no envelope. | Envelope absent. |

Issue verdict: not implemented; contract and identity are a green field, ACK semantics undeclared.

### #400 [integration][IN-OUTBOX] Persistir intents outbox junto a marca y offset en Tx destino
Dependencies: #395, #399, #360, #361, #373, #374.

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Camino v1 journal->EventReader->runner->Tx del destino escribe intent+marca+offset" | bloqueado | Runner: `Handle` then separate `WriteOffset` (`runner.go:775,814`); no Tx, no mark. | Blocked by #373 (P-TX), #360/#361. |
| "Handle puede escribir intent en su misma Tx del destino" | bloqueado | `Handler.Handle` signature has no Tx (`projection/handler.go:55-57`); ReadSideProcessor #366 absent. | #373, #366. |
| "Clave estable por scope/destino/evento previene intent duplicado en replay" | no implementado | No intent table or key. | Needs #399 identity. |
| "Esquema, estados y migracion compatibles; no nuevo store paralelo" | no implementado | No outbox table in `persistence/postgres/schema.go`; schema migrator exists (`persistence/postgres/schema_migrator.go`) as the base (#358/#359 open). | Schema pending. |
| "Rollback impide intent huerfano y avance de offset; un solo destino coherente" | bloqueado | No shared Tx; offset store is separate (`offsetstore/offset_store.go` interface). | #373. |
| "Pruebas de commit/rollback/crash y aislamiento multitenant en integracion" | no implementado | `inttest/flows/{eventstore,restart,tenancy}` have none for outbox; no kill/crash support. | Needs #407. |
| "ID estable incluye productor, Scope, evento fuente, ordinal" | no implementado | See #399. | Same. |
| "Politica de version/rebuild sin republicar" | no implementado | See #399. | Same. |

Issue verdict: not implemented; blocked by the Tx contract (#373) and the new read path (#360/#361). B2 non-zero partition untested affects per-shard reads this will rely on.

### #401 [integration][IN-RELAY] Dispatcher outbox fenced con reintentos acotados
Dependencies: #395, #400, #374, #375, #378.

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Claim/lease y fence; owner obsoleto no confirma delivery" | bloqueado | No fence/lease symbols (grep). | #374 (P-FENCE), #400. |
| "Enviar->ACK declarado->marcar delivered; caida tras ACK implica duplicado, conserva ID" | no implementado | Engine sends and forgets (`engine/streams.go:486-500`); no delivered mark; no ID. | Needs #399/#400. |
| "Maximos de operaciones/espera/batch, backoff cancelable, politicas error/parking configurables" | no implementado | Projection runner has buffer/pull/retry (`option.go:67,75,114`; `retry.go`) but not for publishing; parking #375/#356 open. | Reusable pieces, no dispatcher. |
| "No perder intent ante timeout/cancelacion ni agotar memoria; polling obligatorio" | no implementado | Today a failed publish is dropped (`streams.go:486-493`). | No intent store to retain. |
| "Lifecycle shutdown drena trabajo y libera recursos; scope y rol backend" | no implementado | Only publisher Close via `compose/goakt/app.go` `releasePublishers`; no dispatcher. | #378, #390 pool by role. |
| "Oraculos de fallos antes envio/despues envio/despues ACK y takeover" | bloqueado | No fault drivers (see #406). | #406. |

Issue verdict: not implemented; the publisher failure-is-skipped behavior at `engine/streams.go:486` is exactly the gap this closes.

### #402 [integration][IN-PUBLISH] Adaptar publishers existentes al contrato durable
Dependencies: #395, #399, #384.

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Inventario de publishers distingue ACK broker/durable, memoria e incompatibles" | parcial | Code shows: Kafka sync producer, WaitForAll, idempotent (`publisher/kafka/config.go:79-85`, `kafka.go:113`); NATS JetStream publish ack (`nats.go:148`); Pulsar `Send` receipt (`pulsar.go:120`); WebSocket plain write (`websocket.go:112-120`, no ACK). Baseline report lists adapters but not ACK class (`i-00...md:216`). | No committed inventory or classification; facts derivable only. |
| "Adapters conservan Scope/key/ID y causation; retry no regenera IDs" | parcial | Kafka and Pulsar key = persistence_id; scope only inside payload `tenant_metadata`; NATS no key; no event ID/causation (`ego.proto:10-33`). | No stable ID/causation header; no retry layer. |
| "Capacidad ACK/fallo declarada y verificada con conformance; sin ACK adecuado no es durable" | no implementado | `publishingtest` has PT-1..PT-4 only (`publishingtest.go:23-45`), no failure/ack check; only `publisher/websocket` runs it (`conformance_test.go:133,146`). | ACK capability + conformance + gating absent. |
| "No crear broker deployment ni transportes; simulador/mock en unitarios" | cumplido | Only existing four adapters; websocket tests use httptest; kafka/nats/pulsar unit tests need no broker (tests pass). | None. |
| "Urd compone adapters; integration no importa root" | parcial | `compose/goakt` composes publishers; publisher modules have architecture tests keeping them off `engine`. | No integration module; cannot verify its imports. |

Issue verdict: partial groundwork (adapters, composition, PT-1..4); durable-ACK declaration and conformance missing.

### #403 [integration][IN-LIFECYCLE] Recuperacion, retencion y lifecycle multitenant
Dependencies: #395, #401, #402, #370, #369, #382, #380.

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Matriz crash/restart/duplicates/owner obsoleto/tenant ruidoso demuestra no perdida, at-least-once" | bloqueado | No outbox/relay; `inttest/flows/restart` covers only entity recovery. | #401, #402. |
| "Retencion del journal no borra eventos antes de checkpoints; outbox conserva pendientes" | bloqueado | `DeleteEvents` exists in stores; no retention/checkpoint coupling; #370 open. | #370. |
| "Migracion/borrado inventarian intents/checkpoints/IDs; delegar #369/#382" | bloqueado | Neither procedure exists (baseline: tenant delete pending); `EraseEntity` only (`engine/entities.go:308-345`). | #369, #382. |
| "Metrics de backlog/edad/errores y admision con cardinalidad limitada" | parcial | `urd.projection.lag_ms` (`internal/instrumentation/instrumentation.go:92`) and `PublicationRejected` counter exist; no backlog/age/outbox metrics; cardinality not verified. | Outbox metrics; #380 admission. |
| "Ejemplo reproducible enlaza guia; sin exactly-once" | no implementado | No example/guide. | Pending. |
| Single tenant: "recuperacion/retencion/drenaje cubren unscoped/fixed; adopcion explicita" | bloqueado | No intents; adoption over PostgreSQL broken (#428). | #401, #428. |

Issue verdict: not started; blocked by #401/#402 and retention/tenant lifecycle issues.

### #396 [Epic][testkit] Ampliar testkit existente para contratos y fallos durables
Dependencies: #404, #405, #406, #407, #408 (children); #342-#345; #346, #347.

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Tareas completadas o diferidas por decision explicita" | no implementado | 5 children open; existing testkit stores/scenarios predate the epic. | #404-#408 pending. |
| "Compatibilidad con capacidades existentes y evidencia de recuperacion/aislamiento" | parcial | `testkit` tests, `persistence/conformance` pass (run); `inttest` tenancy/restart flows pass per baseline. | New fakes/drivers absent, so no evidence for them. |
| "Imports respetan ADR y lifecycle via extensiones GoAkt" | parcial | `testkit/*.go` (non-test) import no `internal/*` nor goakt; no engine/production file imports testkit/enginetest (only `example/*` and `enginetest` itself import testkit); no architecture test enforces it. ADR #347 open. | Rule not enforced by a test; #347. |
| "Guia y escenarios ejecutables reflejan garantias reales" | no implementado | `docs/testing/*` cover go-specs/lanes only; no guide for fault drivers. | Needs #404/#408. |
| Single tenant: "Fixtures y drivers cubren unscoped, fixed y multitenant" | parcial | Store tests cover Unscoped vs tenant (`testkit/scope_test.go:234`); `inttest/flows/tenancy` W7 covers three modes on PostgreSQL. | No reusable fixtures/drivers; fixed mode not in testkit. |

Issue verdict: epic open; existing in-memory stores, scenarios and conformance are a base, new capabilities not built.

### #404 [testkit][TK-INVENTORY] Inventariar y ampliar testkit existente
Dependencies: #396, #346, #347.

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Registrar SHA e inventario entity tests, store TCK, actor harness y utilidades" | parcial | `docs/prd/i-00-baseline-develop.md:225-236` has SHA and testkit table (stores, scenario, conformance, enginetest). Actor harness and entity-test helpers not itemized. | Complete inventory with migration/compat notes. |
| "Relacionar #348/#354 y restantes TCK con oraculos existentes" | bloqueado | #348/#354 open; no offset-store TCK (`persistence/conformance` has events/state/snapshot/schema only). | #348, #354. |
| "Definir APIs fixtures/fault drivers sin importar internals" | no implementado | Fakes/mocks live in `internal/engine/enginetest` (not importable externally); public testkit has no fault API. | API design pending. |
| "Separar core fase1 de drivers read-side/outbox/workflow fase2; GateB no depende de #366" | no implementado | No phase-2 drivers exist, no doc defines the split. | Pending; #388. |
| "Documentar go-specs y mocks/fakes para unitarios, lanes separados" | cumplido | `docs/testing/go-specs.md` (mock.Controller, no external resources, testkit stores as fakes) and `docs/ci.md:27-33` lanes. | Extend when new fakes land (current scope covered). |

Issue verdict: partial; documentation of the current state is there, the API/boundary decisions are not.

### #405 [testkit][TK-FAKES] Fakes, mocks y reloj determinista
Dependencies: #396, #404, #349, #351, #357, #390.

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Fakes configurables de stores/EventReader/admision/dispatcher respetan contratos" | parcial | In-memory `EventStore`, `OffsetStore`, `SnapshotStore`, `DurableStore`, `KeyStore` (`testkit/*.go`) pass conformance; no failure config, no EventReader/admission/dispatcher fake. | Configurable faults; new contracts (#349/#351). |
| "Clock/timers/backoff deterministas, IDs controlados, sin sleeps" | parcial | Manual clock exists but package-private in `internal/projectionrunner/clock.go:31`; go-specs manual clock for Eventually. | Not public in testkit; no ID control. |
| "Fakes permiten cancelacion/errores sin ocultar invariantes" | parcial | go-specs mocks in `internal/engine/enginetest` (e.g. `events_store_mock.go`) can return errors; internal only. testkit stores have no error injection. | Public, invariant-preserving fakes. |
| "Fixtures tenant/scope/celda/rol cubren colisiones y aislamiento" | parcial | Scope isolation tests/conformance (`testkit/conformance_test.go:189` catches non-isolating store); no cell/role (#390 open). | Cell/role fixtures. |
| "Ningun paquete productivo importa testkit; unitarios nunca acceden a DB/API real" | parcial | No production code imports testkit (only `example/*` and enginetest); unit gate bans real resources (`docs/testing/go-specs.md:262-275`). No architecture test for the import rule. | Enforce import rule. |
| Single tenant: "fixtures sin tenancy, fixed y multitenant; datos legacy; rechazo de cursor/identidad cruzada" | parcial | Unscoped/tenant stores covered; legacy-not-seen on PG (`inttest/flows/tenancy` `TestConformance_W7_LegacyDataIsNotSeenByATenant`); cursor rejection not found. | Reusable fixtures; cursor rejection. |

Issue verdict: partial; stores exist, deterministic time/fault/ID fakes are not public.

### #406 [testkit][TK-FAULTS] Drivers de fallos y oraculos de invariantes
Dependencies: #396, #405, #348, #354, #356, #373, #374, #375, #378.

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Intercalaciones reproducibles: commit tardio, repeticion, omision/cursor, owner obsoleto, rollback, parking, cancelacion del pool" | no implementado | Only ad-hoc tests (`TestRunnerPagesThroughTimestampTies`, conformance `PagingResumesFromACommittedOffsetInsideATie`, `events.go:71`); no driver. Fence/parking/pool absent. | Driver; #373-#375, #378. |
| "Oraculos: secuencia contigua, no avance sin efecto, fence vigente, release de cupos" | bloqueado | Contiguous-append TCK #354 open; no fence/quota. | #354, #374, #380. |
| "Triggers en limites publicos o adapters de prueba, sin hooks invasivos" | no implementado | None in public testkit. | Pending #405. |
| "Logs/evidencia permiten reproducir seed/orden" | no implementado | No seed/order tooling found. | Pending. |
| "Reusar #348/#354/#373-375; documentar diferencia fake vs validacion real" | bloqueado | All open. | Dependencies. |
| Single tenant: "escenarios con scope Unscoped y fijo, ademas de multitenant" | parcial | Isolation conformance for Unscoped vs tenant exists; replay/cancel/idempotency scenarios absent. | Scenarios. |

Issue verdict: not implemented; depends on TCK/fencing/parking work still open.

### #407 [testkit][TK-HARNESS] Harness PostgreSQL testcontainers para lane de integracion
Dependencies: #396, #404, #405, #358, #359, #360, #378.

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Reusar testcontainers/harness existente; fixtures esquema/seed aislados por test" | parcial | `inttest/infra/postgres/postgres.go:118` `NewDatabase`: unique DB per test, dropped WITH (FORCE) at cleanup; `TestPostgresNewDatabase` passed (run). No seed fixtures helper found. | Schema/seed fixtures. |
| "Lane de integracion explicito independiente del build/test unitario; no DB desde unitarios" | cumplido | Separate `inttest` module; CI job only on develop push/release/manual (`docs/ci.md:29`); unit gate forbids `sql.Open` etc. and `inttest` never skips (`go-specs.md:262-275`). | None. |
| "Teardown cancela drivers y libera pool/containers ante errores, timeout o fallo" | parcial | DB drop in `Cleanup`; `Terminate` after `m.Run` (`postgres.go:107`). No drivers to cancel; `RYUK_DISABLED` in local env means leaked containers on abort (not verified). | Driver cancellation; abort/timeout path unverified. |
| "Versiones/capacidades PostgreSQL y limites registrados; reproducible local/CI" | parcial | Image pinned `postgres:17.6-alpine` (`postgres.go:59`), `max_connections=1000`, `fsync=off` (`:84`). Server version/capabilities not recorded in test output; local needs Colima DOCKER_HOST. | Recording. |
| "Permitir fault drivers y pruebas de concurrencia/fencing/pools sin suponer shutdown graceful = crash" | no implementado | No kill/crash facility in `inttest`; no fault drivers (#406); fsync=off diverges from crash-safety assumptions. | Crash support. |
| Single tenant: "cubre unscoped sin migracion, fixed y multitenant; distingue adopcion explicita de startup compatible" | parcial | `TestConformance_W7_SingleTenantAndLegacyModes`, `..._LegacyDataIsNotSeenByATenant` pass on PG; adoption over PG fails (#428), only characterized. | #428. |

Issue verdict: partial; the PostgreSQL harness and lane exist, crash/fault support and recording do not.

### #408 [testkit][TK-PRODUCT] Driver publico de read-side y escenarios de producto
Dependencies: #396, #406, #407, #366, #393.

| Criterion | State | Evidence | Gap |
| --- | --- | --- | --- |
| "Driver de handler: prepare, Tx/rollback, duplicados, scopes y tenant visible sin cluster" | bloqueado | No ReadSideProcessor (#366); `Handler` has no prepare/Tx. | #366, #406. |
| "Integracion PerScope/SharedCell con destinos compatibles y rechazos" | bloqueado | No PerScope/SharedCell symbols. | #366, #393. |
| "API registra escenarios outbox/workflow como extensiones opt-in" | no implementado | No extension registry in testkit; outbox absent. | Pending. |
| "Funciones GoAkt via harness existente; no framework actor paralelo" | no verificado | `testkit/*.go` imports no goakt (grep); nothing to verify yet. | Re-check when driver exists. |
| "GateB no depende de este driver; matriz garantias mocks vs integracion" | no implementado | No matrix doc; #388 open. | Documentation. |
| Single tenant: "PerScope usa OneScope(Unscoped()) sin catalogo; fixed/multi; SharedCell explicito" | bloqueado | No OneScope/PerScope API. | #366. |

Issue verdict: not started; phase 2 and blocked by #366/#393.

## Group E: workflow (#397) and management (#398)

### #397 [Epic][workflow] Consolidar sagas y procesos durables recuperables
Dependencies: #409-#413 (children), #342-#345, #424, #423.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Tareas completadas o diferidas" | no implementado | Children #409-#413 all open; no workflow package | No task done or explicitly deferred |
| "Compatibilidad con capacidades existentes y evidencia de recuperación/aislamiento" | parcial | Saga recover/tenant tests pass (`TestSagaActor`, `saga_actor_tenant_test.go`); engine/saga.go:138-150 status not persisted | Evidence only for current saga; no downtime or restart-of-timer evidence |
| "Imports respetan ADR y lifecycle se integra mediante extensiones GoAkt" | bloqueado | Saga uses `extensions.Require` in PreStart (saga_actor.go:173) but no workflow module | ADR #347 open; boundary untestable without a workflow package |
| "Guía y escenarios ejecutables reflejan garantías reales" | no implementado | No saga scenarios in `testkit/`; no guide | Needs #408 and #413 |
Issue verdict: epic not started; only the legacy saga actor exists. Baseline #397 inventory is accurate.

### #409 [workflow][WF-CONTRACT] Frontera workflow y contrato de sagas
Dependencies: #397, #346, #347, #357, #361, #388.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Inventario de sagas, formato persistido y suscripciones; migración/compatibilidad" | parcial | Baseline report 'Inventory #397' lists it; saga persists `egopb.Event` + tenant marker (saga_actor.go:694-772); in-process subscription (:250) | Persisted format not documented as contract; no migration/compat rules |
| "Identidad (scope,workflowID,version), state-machine, causation/correlation, eventos de resultado" | parcial | Scope + `behavior.ID()` (saga_actor.go:216); causation/correlation via `command.Metadata` root (:223-229, :798); status enum | No version; no explicit state machine or result events; #427 name collisions affect saga names |
| "CommandDispatcher interfaz local inyectada por Urd; workflow jamás importa root" | no implementado | grep finds no `CommandDispatcher` in code; saga calls `SendSync` directly (saga_actor.go:862) | SPI and workflow package absent |
| "Consumo durable usa EventReader/runner projection; pub/sub solo wakeup" | bloqueado | saga_actor.go:443-477 reads live in-memory stream only, no catch-up | Needs #361 (runner read path); today pub/sub is the only source |
| "No presentar workflow como equivalente exacto de Akka/Lagom" | cumplido | `docs/prd/urd-platform-prd.md:72` states Urd proposal | Docs only; nothing else to check |
| "[single tenant] Consolidar contrato conservando Unscoped y fixed" | parcial | resolveScope Unscoped when no tenancy (saga_actor.go:332-336); fixed/tenant via spawn scope; tests in saga_actor_tenant_test.go | No test of saga under `WithSingleTenant`; identity-change migration not defined |
Issue verdict: inventory partial, SPI and durable consumption not started.

### #410 [workflow][WF-STATE] Persistir transición con inbox e intent en Tx
Dependencies: #397, #409, #361, #373, #374, #375.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Transición+inbox dedupe+intent+checkpoint en una Tx destino" | bloqueado | saga_actor.go:729 plain `WriteEvents(..., Unconditional())`; no inbox/intent/checkpoint | New guarantee; needs #373 |
| "Scope/ID/version aíslan workflow y datos de otros tenants" | parcial | Scoped store access, `bindOrVerify`, `VerifyActorIdentity` (saga_actor.go:218, :511); tenant tests pass | No version; name collisions (#427) |
| "Replay tras caída recupera pendientes y no genera nuevas intenciones para evento repetido" | no implementado | recover() replays only own events (:363); events during downtime are lost (live stream); no dedupe of repeated event | Needs catch-up (#361) and inbox |
| "CAS/revisión/fence impiden writers obsoletos; errores/parking delegan #375" | bloqueado | `persistence.Unconditional()` write; no fence | Needs #374, #375 |
| "No incorporar runner propio ni Tx atómica entre bases" | no implementado | Saga has its own consume loop (`consumeEvents`, saga_actor.go:443) outside the projection runner | Must move to the runner (#361) |
Issue verdict: none of the new transactional guarantees exist.

### #411 [workflow][WF-COMMANDS] Despachar comandos con identidad estable y dedupe
Dependencies: #397, #410, #357.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Entrega al menos una vez con command_id estable #357; reintentos sin nuevos IDs" | bloqueado | `attachCommandMetadata` uses `GenerateOperationID` (random) per dispatch (saga_actor.go:803); no retry loop exists | Needs #357 and durable intent |
| "Compensación con ID propio estable y causation al intento original" | no implementado | compensate (:904-928) derives from saga root, random ID, causation = saga root not original attempt | New guarantee |
| "ACK/error/resultados definen transición; indeterminado vs definitivo" | parcial | Error and timeout both go to `HandleError` (:864-872); `ParseCommandReply` for error replies | No indeterminate/definitive distinction |
| "Límites de concurrencia/espera/retry; no bloquear mailbox" | parcial | Per-command timeout, default 5s (`effectiveCommandTimeout`, :826; test 'sendCommand: default timeout when zero') | `SendSync` runs inside actor Receive (:862) so it blocks the mailbox; no concurrency or retry bound |
| "Pruebas respuesta perdida/despacho repetido/caída tras aceptación" | no implementado | No such tests in internal/engine/saga | Needs #357 target-side dedupe |
Issue verdict: today only synchronous best-effort dispatch with random IDs.

### #412 [workflow][WF-TIMERS] Timers durables y recuperables bajo fencing
Dependencies: #397, #410, #374.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "TimerID estable por workflow/scope/version, fecha durable, estado idempotente" | no implementado | Only `ScheduleOnce(&sagaTimeoutMsg{}, timeout)` in memory (saga_actor.go:275-277) | Nothing durable |
| "Reinicio recupera vencidos con política explícita" | no implementado | PostStart reschedules the full timeout on each start (:275); deadline not persisted | Restarted saga gets a fresh timeout |
| "Owner obsoleto no dispara transición; fence validado en destino" | bloqueado | No fence; timeout path only checks `status == Running` (:282) | Needs #374 |
| "Scheduler GoAkt como señal; intent durable fuente de verdad" | parcial | GoAkt `ScheduleOnce` is reused (:277) | It is the only source; no durable intent |
| "Carga/recuperación acotadas; sin IDs nuevos al retry" | no implementado | No recovery path | New guarantee |
| "Cancelación/reprogramación vs disparo usa CAS/revisión y fence" | bloqueado | No cancel/reprogram API | Needs #374, #373 |
| "Pruebas deterministas cancel/fire, reprogram/fire, owner obsoleto, retry" | no implementado | Existing tests only cover timeout compensation ('PostStart with timeout triggers compensation', saga_test.go:342) | Needs fake clock (#405) |
Issue verdict: timeout is volatile; only the GoAkt scheduler part is reusable.

### #413 [workflow][WF-RECOVERY] Compensación, recovery y escenarios auditables
Dependencies: #397, #411, #412, #375, #408.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Política configurable de retry/compensación/parking por pasos y errores indeterminados" | parcial | Behavior-coded `HandleError`/`Compensate` (port/behavior/saga.go:51-59) | No engine-level policy, no parking (#375), no indeterminate class |
| "Compensación acción idempotente propia, no revierte efectos externos" | parcial | Compensation is a separate behavior method returning commands (saga_actor.go:905) | No stable ID or idempotency; compensation failure sets `SagaFailed` with no retry |
| "Auditoría scope/workflowID/version/causation y estado sin secretos" | no implementado | Only log lines; command metadata carries causation but no audit record; no version | New guarantee |
| "Escenarios testkit: reinicio, evento en downtime, timer vencido, duplicado, fallo en compensación" | parcial | Internal tests: recovery with prior events (saga_test.go:298), compensation failures (:640-687) pass | No testkit saga scenarios (#408); downtime, expired timer and duplicate not covered |
| "Guía conservación/migración de sagas y límites; no resetear otros scopes" | no implementado | None found in docs/ or MIGRATION.md | Docs missing |
Issue verdict: behavior-level compensation exists; recovery policy, audit and scenarios do not.

### #398 [Epic][management] Control operativo seguro mediante capacidades públicas
Dependencies: #414-#418, #419-#422, #342-#345, #424.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Tareas completadas o diferidas" | no implementado | Children #414-#422 open; no management package, no `cmd/` | Nothing started |
| "Compatibilidad con capacidades existentes y evidencia de recuperación/aislamiento" | parcial | Existing ops work: `StartProjection`/`StopProjection`/`RebuildProjection`/`ProjectionLag`/`EraseEntity` (engine/projections.go, entities.go:308) | No isolation evidence for operations beyond EraseEntity tenant gate |
| "Imports respetan ADR y lifecycle por extensiones GoAkt" | bloqueado | No management module | ADR #347, #383 open |
| "Guía y escenarios ejecutables reflejan garantías reales" | no implementado | None | Needs #418, #408 |
Issue verdict: only scattered engine operations; baseline #398 inventory is accurate (no pause/resume found by grep).

### #414 [management][MG-CONTRACT] Control SPI y operaciones identificadas
Dependencies: #398, #346, #347, #384, #388.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "OperationID y objetivo scope/celda/processor/version/rango; Unscoped no concede todo" | no implementado | `command.OperationID` exists for commands only (command/identity.go:39); no management operation type | New contract |
| "SPI distingue consultas, mutaciones y operaciones largas con resultado/cancelación" | no implementado | None | New contract |
| "Registro por capacidades; núcleo sin exigir todas las operaciones" | bloqueado | No registry; `requireFamily` is unrelated (entities.go:154) | Needs #384 |
| "management no importa internals ni implementa actores/remoting" | no implementado | Package does not exist (compliance vacuous) | Cannot verify boundary |
| "No crear HTTP/gRPC/dashboard ni QueryBus" | cumplido | No such code in engine/ or root | Vacuous: nothing exists yet |
| "[single tenant] SPI y registry aceptan Unscoped; autorización operador y scope explícitos" | no implementado | None | New contract |
Issue verdict: contract not defined.

### #415 [management][MG-STATUS] Progreso y estado operativo
Dependencies: #398, #414, #362, #374, #375, #378.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Lag y cursor distinguen observado/confirmado, falta de capacidad y ausencia de datos" | parcial | `Engine.ProjectionLag` per shard vs committed offset (projections.go:282) | No observed/committed split, no 'no capability'/'no data' states; returns error if no offset store |
| "Consultar parking, ownership, rebuild y pools solo si registrada" | bloqueado | No parking/ownership/pool status APIs | Needs #375, #374, #378 |
| "Scope/rol/celda explícitos; no confundir presupuestos ni pool dedicado con aislamiento físico" | bloqueado | No cell/role model in code | Needs #414, #355 |
| "Paginación/espera/límites acotados y métricas de cardinalidad bounded" | no implementado | `ProjectionLag` enumerates all shards unbounded; `Telemetry` is only Tracer+Meter (telemetry.go:30-37) | No query limits |
| "No revelar DSN, credenciales, payload sensible ni otro tenant" | no verificado | `ProjectionLag` uses the projection scope (projections.go:368) | No status API with redaction tests |
Issue verdict: only projection lag exists, without the required state model.

### #416 [management][MG-CONTROL] Pausa/reanudación durables y delegación
Dependencies: #398, #414, #374, #375, #417.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Pausa/reanudación durables sobreviven restart/takeover" | no implementado | No pause/resume in projection or engine (grep); only Start/Stop (projections.go:65,125) | New primitive in projection |
| "Pausa define límite en vuelo y ACK; no cerrar pools ni perder offsets" | no implementado | None | New guarantee |
| "Retry/replay #375; change-version #370; rebuild #381; migración #369; borrado #382" | bloqueado | All delegated issues open | Delegation targets missing |
| "Operación concreta solo con capacidad; control básico sin depender de todas" | no implementado | No capability gating | Needs #414 |
| "Nunca reset bruto de offsets compartidos ni copiar state machines" | parcial | `RebuildProjection` calls `ResetOffset(name, from)` for the whole projection (projections.go:185-230); offsets keyed by name+shard, no tenant | Existing rebuild is a raw reset; per-scope rebuild is #381 |
| "Usar fence/guardas de MG-GUARDS cuando habilitado" | bloqueado | No fence | Needs #374, #417 |
Issue verdict: no control primitive exists; existing rebuild contradicts the target rule.

### #417 [management][MG-GUARDS] Autorizar, deduplicar y auditar
Dependencies: #398, #414, #379, #374, #380.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Autorización por port inyectado; identity extraction reutiliza #379" | parcial | `tenancy` resolver; `EraseEntity` fails closed for administrative scope (entities.go:332-338; `engine_tenant_administrative_scope_test.go`) | No authorization port; #379 open |
| "Scope explícito y operación identificada; reintentos devuelven mismo resultado o conflicto" | no implementado | None | No operation dedupe store |
| "Validar fence/version y auditoría antes/después" | bloqueado | No fence, no audit | Needs #374 |
| "Rechazar capacidades ausentes, cuotas agotadas, scopes inválidos con errores accionables" | bloqueado | No quotas | Needs #380, #414 |
| "Límites en consultas/operaciones largas; identidad no implica privilegio global" | parcial | Tenant-aware EraseEntity denies non-tenant identity (entities.go:333-338) | Only one operation; no limits |
| "[single tenant] Identidad operador separada del tenant ID; fail-closed" | no implementado | Legacy mode (no resolver) erases `Unscoped()` with no authorization (entities.go:322-326) | Opposite of fail-closed default |
Issue verdict: only a tenant gate on EraseEntity; no guard layer.

### #418 [management][MG-COMPOSE] Componer management en GoAkt y verificar con testkit
Dependencies: #398, #415, #416, #417, #383, #408.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Registro compile-time/WithExtensions y dependencias PreStart, sin plugins ni cambios GoAkt" | no implementado | Engine extension pattern exists (`extensions.Require`) but no management registration | Needs #383 |
| "Capacidades incompletas se reportan; no habilitar control inexistente" | no implementado | None | Needs #414 |
| "Testkit prueba autorización, idempotencia, takeover, pausa/reinicio, aislamiento" | bloqueado | `testkit/` has no management or saga/projection scenarios | Needs #408, #416, #417 |
| "Guía de operaciones habilitadas y limitaciones" | no implementado | None | Docs missing |
| "No exigir terminar management para GateB/C ni nuevo transporte" | no verificado | Not checked against gate definitions (#388) | Policy statement; no code evidence |
Issue verdict: not started.

### #419 [management][INSPECT-FEASIBILITY] Auditar GoAkt, collector y attach read-only
Dependencies: #398, #346, #347, #414, #383.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Auditar versión de go.mod y replace; fijar SHA y capacidades" | parcial | go.mod:9,93 (verified: replace to `pablogore/goakt/v4 v4.5.7-actorof.1`); go.sum:182 h1 hash; report lists APIs | Fork commit SHA not recorded (tag + module hash only); capabilities listed but not exercised |
| "Collector local read-only con identidad/nodo/placement/estado/métricas; snapshots con timestamp, provenance, incomplete/stale" | no implementado | No collector code. Fork APIs exist: `ActorSystem.Actors(ctx,timeout)`, `NumActors`, `Metric`, `PID.Metric/Children/IsRunning` | Collector and snapshot DTO absent |
| "Attach opt-in explícito: export/replay o canal autorizado; sin acceso a memoria; sin servidor always-on" | no implementado | Report finding verified: `client.Client` has only Kinds/Spawn/Tell/Ask/Stop/Exists/Reinstate/Reinstate, no list/metric; remote client is internal | Design not chosen (export vs channel) |
| "Auditar Actors/Metric/Peers, traversal remoto, presupuesto timeout/cancelación" | parcial | Report table gives scope per API; `Actors(ctx, timeout)` and `Peers(ctx, timeout)` take timeouts (actor_system.go:136,713) | Remote traversal cost/cancellation not measured; `remote.Peer` fields not verified (report says so) |
| "Core inspector sin dominio Urd; enriquecimiento opcional; no Actor() ni estado arbitrario" | no implementado | No package | Design only |
| "Interfaces públicas y formato snapshot versionado" | no implementado | None | New contract |
| "OSS licencia compatible, sin backend pago; no prometer paridad Akka" | parcial | GoAkt fork is MIT (module LICENSE); PRD:187 avoids parity claim | No tool/deps chosen; repo license compat not checked |
| "Documentar unsupported y seguridad/permisos/redaction/tenant boundary" | parcial | Report lists unsupported (mailbox size, dead-letter list, remote list/metric) | Security/permissions/redaction/tenant boundary not documented |
| "[single tenant] Inspector sin Urd tenancy; attach autorizado por app/nodo" | no implementado | None | Design only |
Issue verdict: audit is a solid start (fork APIs confirmed), but SHA, remote budgets and all design deliverables are missing.

### #420 [management][INSPECT-TELEMETRY] Interacciones observadas con sampling y buffers
Dependencies: #398, #419, #379, #355.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Auditar hooks de Send/Tell/Ask y métricas/event stream en versión fijada" | parcial | In fork: `ActorSystem.Subscribe` (lifecycle events), `WithMetrics` OTel (option.go:425); grep finds no message interceptor/middleware in `actor/` | Not documented in the report; Send/Tell hook absence needs a written audit |
| "Capturar metadata origen/destino/nodo/timestamp/tipo, sin payload ni inferencia" | bloqueado | No capture code; no per-message hook found in fork | Needs #419 decision (adapter) |
| "Sampling/ring buffer/colas con máximos; pérdidas visibles" | bloqueado | None | Needs #419 |
| "Edges con ventana, origen, conteo, staleness" | bloqueado | None | Needs #419 |
| "State dominio opt-in autorizado; aislar tenants; redactar secretos" | no implementado | None | Needs #379 too |
| "Instrumentación aislada, cancelable, costo medido" | bloqueado | None | Needs #419, #355 |
Issue verdict: not started; likely needs an explicit Urd adapter because the fork exposes no Send/Tell hook.

### #421 [management][INSPECT-TUI] cmd/urd-inspect con vistas terminal
Dependencies: #398, #419, #420, #415.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "cmd/urd-inspect sin nuevo go.mod; core genérico y adapter Urd" | no implementado | No `cmd/` directory | Absent |
| "Consumir attach/export; snapshot/replay explícito; live solo con canal real" | bloqueado | No attach/export format | Needs #419 |
| "Vistas lista/ubicación/nodo/estado/health, topología, grafo; navegación sin GUI" | bloqueado | None | Needs #419, #420, #415 |
| "Grafos ASCII; refresh/consultas/layout acotados, cancelables; incomplete/stale/drops" | bloqueado | None | Same |
| "Solo lectura: sin kill/restart" | no implementado | No tool exists | Cannot be verified |
| "Sin API remota obligatoria ni backend pago; licencias compatibles" | no implementado | No tool/deps | Cannot be verified |
| "Distinguir actor local/remoto y datos no disponibles; dominio opt-in" | bloqueado | None | Needs #419 |
| "[single tenant] TUI en single tenant o GoAkt puro; UI distingue scope default/fijo/multi" | bloqueado | None | Needs #419 |
Issue verdict: not started.

### #422 [management][INSPECT-VALIDATE] Validar aislamiento y costo; propuesta GoAkt
Dependencies: #398, #421, #405, #407, #418.
| Criterion | State | Evidence | Gap |
|---|---|---|---|
| "Escenarios nodos vivos/remotos, restart, actor desaparecido, snapshot viejo, canal caído, drops" | bloqueado | Nothing to test | Needs #421 |
| "Tests tenant authorization/redaction; dominio no habilitado por defecto" | bloqueado | None | Needs #420, #421 |
| "Medir CPU/memoria/cardinalidad/espera con carga fija; saturación reduce cobertura" | bloqueado | None | Needs #420 |
| "Unitarios con fakes, harness opt-in; distinguir limitaciones GoAkt" | bloqueado | #405 and #407 open | Fakes/harness absent |
| "Guía launch/export/attach/snapshot/replay, seguridad y límites" | bloqueado | None | Needs #421 |
| "Propuesta GoAkt con core, licencia, SPI, evidencia; sin contactar owner" | no implementado | None | Document absent |
| "Mermaid docs explican arquitectura/attach sin prometer paridad" | parcial | PRD mermaid at urd-platform-prd.md:191-196; parity disclaimer :187 | Product PRD only; no inspector guide |
| "[single tenant] Pruebas GoAkt puro/unscoped/fixed/multi; autorización fallida bloquea attach" | bloqueado | None | Needs #421 |
Issue verdict: not started; fully blocked by #421.

---
Summary count of criteria per state (95 total): cumplido 2 | parcial 22 | no implementado 38 | no verificado 2 | bloqueado 31
