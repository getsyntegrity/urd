# Tasks: Runtime tenant wiring and zero-plumbing single-tenant mode (EGO-TENANT-006)

Tracker `getsyntegrity/ego#55` · spec `specs/tenancy-runtime/spec.md` · `design.md` (7 decisions closed).

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~850–950 (5 modified files, 2 new marker files, tests across engine/actor layers) |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR1 config/marker/validation → PR2 T4-A trust boundary → PR3 T4-B invariant + demo + regression |
| Delivery strategy | ask-on-risk (no cached value received this session) |
| Chain strategy | stacked-to-main (ratified by repository owner; PR1 `getsyntegrity/ego#55`, mirrors EGO-TENANT-001 #49/#50/#51) |

Decision needed before apply: No — resolved (stacked-to-main)
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: High

Rationale: five production files across two subsystems (Config/Engine vs. two actor types), each requiring RED-before-GREEN pairs for two independent gates (T4-A, T4-B) plus a dedicated leak regression — mirrors EGO-TENANT-001's 3-PR precedent in this same repo.

### Suggested Work Units

| Unit | Goal | Likely PR | Focused test command | Runtime harness | Rollback boundary |
|------|------|-----------|----------------------|-----------------|-------------------|
| 1 | Config/Option + marker extension + `NewEngine` validation (Phases 1–2) | PR 1 | `go test . -run 'TenantResolver\|Ambiguous\|Extension' -v` | N/A — pure config/constructor, no actor runtime | Revert `option.go`, `internal/extensions/extensions.go`, `engine.go` `NewEngine`/`validateActorSystemExtensions` additions |
| 2 | T4-A trust boundary: `SendCommand` + 3 actor gates + saga fail-closed proof (Phase 3) | PR 2 | `go test . -run 'SendCommand\|Require\|FailClosed\|Saga' -v` | Existing `engine_test.go` in-process goakt harness (real actors) | Revert `SendCommand` resolve block and the 3 `tenancy.Require` gate call sites |
| 3 | T4-B invariant, `resetBatch` leak fix, single-tenant demo, backward-compat regression (Phases 4–6) | PR 3 | `go test . -run 'VerifyUnchanged\|ResetBatch\|SingleTenant\|Legacy' -v` | `go test -mod=vendor -p 1 -timeout 0 -race ./...` (real exec, mirrors EGO-TENANT-001 task 4.1) | Revert `batchTenant` field, T4-B gate call sites, doc comment |

## Phase 1: Config/Option Foundation (T1, T3, DP1, DP2)

- [x] 1.1 RED `option_test.go`: nil / typed-nil `WithTenantResolver` register nothing, no error (mirrors `isNilLogger`, logger.go:72).
- [x] 1.2 RED `option_test.go`: non-nil registers as effective; nil-after-non-nil does not reset it.
- [x] 1.3 GREEN `option.go`: `Config.tenantResolver`, `tenantResolverCount`; `WithTenantResolver`; `isNilResolver`.
- [x] 1.4 GREEN `option.go` `GoaktOptions()` (:105): register tenancy marker extension only when `tenantResolver != nil`.
- [x] 1.5 GREEN `internal/extensions/extensions.go`: `TenancyExtensionID` const + marker type, no resolver field.
- [x] 1.6 RED `internal/extensions/extensions_test.go`: marker `ID()` returns `TenancyExtensionID`.

## Phase 2: `NewEngine` Validation (DP2, D2)

- [x] 2.1 RED `engine_test.go`: `NewEngine` fails on 2 distinct resolvers.
- [x] 2.2 RED `engine_test.go`: same error when one resolver value is registered twice (count, not identity).
- [x] 2.3 RED `engine_test.go`: exactly one resolver succeeds; zero resolvers succeeds (legacy, no error).
- [x] 2.4 GREEN `engine.go`: `ErrAmbiguousTenantResolver` var beside `ErrActorSystemRequired` (:70); `Engine.tenantResolver`; `NewEngine` (:161) rejects `count > 1`; defensive `count > 0 && tenantResolver == nil` guard.
- [x] 2.5 GREEN `engine.go` `validateActorSystemExtensions` (:217): add `{TenancyExtensionID, cfg.tenantResolver != nil}`.

## Phase 3: T4-A Trust Boundary (AC2, AC3, T2, T4-A, T5)

- [x] 3.1 RED `engine_test.go`: `SendCommand` resolves once, attaches, handler observes via `tenancy.From` (resolver call-count spy).
- [x] 3.2 RED `engine_test.go`: resolver error rejects the command before the actor system; zero writes.
- [x] 3.3 GREEN `engine.go` `SendCommand` (:724): resolve + `tenancy.Attach` before `ref.noSender.SendSync` (:757), guarded by `tenantResolver != nil`.
- [x] 3.4 GREEN `event_sourced_actor.go`: `tenantAware` field, populated from the marker at actor start.
- [x] 3.5 RED `event_sourced_actor_test.go`: non-batched — missing `TenantContext` ⇒ `HandleCommand` never invoked, zero writes.
- [x] 3.6 GREEN `event_sourced_actor.go` `processCommandAndReply` (:505): `tenancy.Require` gate before `HandleCommand` (:527).
- [x] 3.7 RED `event_sourced_actor_test.go`: batched — same fail-closed; `flushBatch`'s `context.Background()` never reached.
- [x] 3.8 GREEN `event_sourced_actor.go` `processAndBatch` (:752): same gate before `HandleCommand` (:772).
- [x] 3.9 GREEN `durable_state_actor.go`: `tenantAware` field, populated at actor start.
- [x] 3.10 RED `durable_state_actor_test.go`: missing `TenantContext` ⇒ `HandleCommand` never invoked, zero writes.
- [x] 3.11 GREEN `durable_state_actor.go` `processCommand` (:172): same gate before `HandleCommand` (:194).
- [x] 3.12 RED `saga_test.go` (reads `saga_actor.go` (read-only)): saga-dispatched command in tenant-aware mode fails closed at the actor gate — documents the known #54 limitation, not ignored.

## Phase 4: T4-B Defensive Invariant (T4-B, D4)

- [x] 4.1 RED `event_sourced_actor_test.go`: non-batched write path confirms tenant without re-invoking `Resolve` (call-count == 1 across accept+persist).
- [x] 4.2 GREEN `event_sourced_actor.go` `processCommandAndReply`: gate after `buildEnvelopes` (:538), before `persistEvents` (:544).
- [x] 4.3 RED `event_sourced_actor_test.go`: mixed-tenant batch rejected at append; `Resolve` never re-called.
- [x] 4.4 GREEN `event_sourced_actor.go`: `batchTenant` field; record on first buffered entry, `tenancy.VerifyUnchanged` on later ones, before `batchBuffer` append (:814).
- [x] 4.5 RED `event_sourced_actor_test.go`: explicit leak case — two sequential flush cycles; second flush's tenant wrongly compared to first flush's stale `batchTenant` — must fail before the fix.
- [x] 4.6 GREEN `event_sourced_actor.go` `resetBatch` (:1031): clear `batchTenant`; 4.5 passes.
- [x] 4.7 RED `durable_state_actor_test.go`: write path confirms tenant without re-invoking `Resolve`.
- [x] 4.8 GREEN `durable_state_actor.go`: gate before `persistStateAndPublish` (:212).
- [x] 4.9 GREEN `durable_state_actor.go` `PostStop` (:142): comment documenting the explicit T4-B exclusion (lifecycle flush, already passed T4-A); no gate added.

## Phase 5: Single-Tenant Demo & Backward Compat (AC4, AC5, T6, D6, D7)

- [x] 5.1 Test (new or `engine_test.go`): `WithSingleTenant(id)` as sole resolver — commands succeed with zero manual `tenancy.Attach`/`Require` in application code.
- [x] 5.2 Test: swap `WithSingleTenant` for a multi-tenant resolver — both traverse the identical resolve-attach-gate sequence.
- [x] 5.3 Regression: run `engine_test.go`, `event_sourced_actor_test.go`, `durable_state_actor_test.go` (read-only, unmodified) with no resolver configured — zero behavior change.
- [x] 5.4 GREEN `option.go`: doc comment on `WithTenantResolver` — nil semantics, ordering-independent security boundary, saga limitation until #54.

## Phase 6: Verification

- [x] 6.1 Run `go test -mod=vendor -p 1 -timeout 0 -race ./...`; record result per EGO-TENANT-001 task-4.1 evidence convention.
- [x] 6.2 Run `go vet ./...`; confirm `saga_actor.go`, `tenancy/` (read-only) byte-identical.

### Verification note — task 6.1

The repository-wide `go test -mod=vendor -p 1 -timeout 0 -race ./...` run failed with exactly
one failure:

`TestEventPublisherClusterHighPartitionCount` (root package)

Failure: `"0" is not greater than "0"` —
`workload only produced events in shards [0..270] (max seen: 0); the test does not exercise
the pre-fix bug range`

Investigation, following the EGO-TENANT-001 task-4.1 evidence convention:

- `git diff --stat -- publisher.go publisher_test.go` is empty and `git log -- publisher_test.go`
  shows no EGO-TENANT-006-related commits: this branch does not touch either file.
- The identical failure was reproduced running the test in isolation on this branch
  (`go test -mod=vendor -race . -run TestEventPublisherClusterHighPartitionCount -v`).
- The identical failure was also reproduced on a disposable worktree checked out at `main`'s
  tip (`fcb11bb`, i.e. with no EGO-TENANT-006 code present at all), confirming it is
  pre-existing and unrelated to this change.
- Re-running the same isolated command twice more afterward (still on this branch) both times
  **passed** (`PASS`, 11.40s) with no code changes in between.

This is the same test EGO-TENANT-001 previously reconciled (see `2026-09-13-ego-tenant-context/tasks.md`
task 4.1) from "pre-existing deterministic failure" to "pre-existing flaky test" after a similar
fail-then-pass pattern was observed there. The same conclusion applies here: `TestEventPublisherClusterHighPartitionCount`
is a pre-existing, environment-dependent flaky test, not attributable to EGO-TENANT-006. All
other packages (`encryption`, `eventadapter`, `eventstream`, `internal/extensions`, `internal/queue`,
`internal/runner`, `internal/syncmap`, `internal/ticker`, `migration`, `projection`, `tenancy`,
`testkit`) passed on every run, and all tenancy-related tests in the root package passed on
every run. Task 6.1 is executed and reconciled.

### Verification note — task 6.2

`go vet ./...` completed with no output (clean). `git diff --stat -- saga_actor.go tenancy/`
is empty, confirming both are byte-identical to their pre-PR3 state (read-only for this
change, as designed).

**Superseded by Phase 7 for `tenancy/`.** The repository owner's adversarial review of PR2/PR3
found a real defect reachable only through this change's own T4-A/T4-B call sites (Blocker 1,
below), requiring a small, deliberate change to `tenancy/context.go` and
`tenancy/tenant_context.go` — see Phase 7 and design.md Decision D8 for the paper trail.
`saga_actor.go` remains untouched (still deferred to #54).

## Phase 7: Review-Fix — TenantContext content validation (post-verify, adversarial review)

`sdd-verify` passed, but the repository owner's own adversarial review of the 3 stacked PRs
found 3 real P1 blockers `sdd-verify` missed, all rooted in the same gap: `tenancy.Attach`/
`tenancy.Require` validated only presence/change of a `TenantContext`, never its content, so a
`TenantResolver.Resolve` returning `tenancy.TenantContext{}, nil` (zero value, no error) flowed
through unchallenged. Fixed here, tracked as design.md Decision D8 / spec.md DP4.

- [x] 7.1 (Blocker 1) GREEN `tenancy/tenant_context.go`: `TenantContext.valid()` — `Scope()` is
      `ScopeTenant` or `ScopeAdministrative`, the zero value is the sole invalid case.
- [x] 7.2 (Blocker 1) GREEN `tenancy/context.go`: `Attach` rejects an invalid `TenantContext`
      before binding (`ErrInvalid`); `Require` rejects one even if somehow already bound,
      independently (defense in depth, both call `valid()`).
- [x] 7.3 (Blocker 1) RED/GREEN `tenancy/context_test.go`, `tenancy/context_internal_test.go`
      (new, white-box): `Attach` rejects `TenantContext{}`; `Require` rejects a directly-bound
      invalid value; valid tenant-scoped/administrative contexts still flow through unchanged.
- [x] 7.4 (Blocker 1) RED/GREEN `engine_test.go` `TestSendCommandTenantResolution`: a resolver
      returning `TenantContext{}, nil` is rejected before dispatch — handler invocation count 0,
      no event persisted. Confirmed genuinely RED by temporarily reverting the tenancy/ fix and
      re-running this subtest in isolation.
- [x] 7.5 docs `design.md`: Decision D8 — option analysis (Attach-only vs Require-only vs both)
      and explicit note on why this is recorded here, not by reopening the archived
      `ego-tenant-context` (EGO-TENANT-001) change.
- [x] 7.6 docs `specs/tenancy-runtime/spec.md`: new Requirement "Fail-Closed Gates Validate
      TenantContext Content, Not Just Presence" (AC3, T4-A, T4-B, DP4), 3 scenarios, Ratified
      Decision DP4, updated AC3 traceability row.
- [x] 7.7 (Blocker 2) GREEN/confirm `event_sourced_actor_test.go`, `durable_state_actor_test.go`:
      new subtests on `TestEventSourcedActorVerifyTenantForPersist` /
      `TestDurableStateActorVerifyTenantForPersist` proving both actors' `verifyTenantForPersist`
      inherit 7.1-7.2's protection with **zero** production-code changes to either function —
      confirmed by test, not re-patched.
- [x] 7.8 (Blocker 3) GREEN `event_sourced_actor.go` `processAndBatch`: moved the
      `entity.batchTenant` vs incoming-tenant homogeneity check (`tenancy.VerifyUnchanged`) into
      the pre-handler gate, capturing `tc` from the existing `tenancy.Require(goCtx)` call there,
      so it runs BEFORE `entity.behavior.HandleCommand` — not only after `buildEnvelopes`.
      Deleted the now-redundant second `tenancy.Require` call that followed `buildEnvelopes`.
- [x] 7.9 (Blocker 3) RED/GREEN `event_sourced_actor_test.go`: new
      `TestEventSourcedActorBatchTenantHomogeneity_ZeroEventCrossTenant` — tenant A opens a batch
      (produces an event); tenant B's zero-event command into the same open batch is rejected
      before `HandleCommand` ever runs (invocation count proven unchanged), with tenant A's
      in-flight batch left uncontaminated. Confirmed genuinely RED by temporarily reverting the
      `event_sourced_actor.go` fix and re-running this test in isolation (observed the exact
      leak: the zero-event command was wrongly accepted). Regression companion
      `TestEventSourcedActorBatchTenantHomogeneity_ZeroEventSameTenant` confirms a same-tenant
      zero-event command still succeeds via the `len(events)==0` cached-state-reply path.
      `helper_test.go`'s `tenancyProbeEventSourcedBehavior.HandleCommand` extended to handle
      `*testpb.TestNoEvent` as a genuine zero-event, no-error command for this test.
- [x] 7.10 Resolved the 3 open P1 GitHub review threads (PR #57 `PRRT_kwDOSegGrc6iNWB7`; PR #58
      `PRRT_kwDOSegGrc6iOul3`, `PRRT_kwDOSegGrc6iOul-`) via `resolveReviewThread`, each with a
      reply quoting the fix commit and the exact test proving it.
- [x] 7.11 Confirmed PR1 (`feat/ego-tenant-006-config-foundation`, tip `3ac8e1d`) still builds,
      `go vet`s, and passes its full test suite standalone/independently (verified in a disposable
      worktree) — untouched by this review-fix phase.

### Verification note — task 7 (full suite)

`go build -mod=vendor ./...` and `go vet -mod=vendor ./...` both clean on PR2 and PR3 branches
after the fixes. `go test -mod=vendor -race . ./tenancy/... -run 'Tenant|Tenancy|Ambiguous|Resolver|Saga|Batch' -v`
passed on PR3 (post-rebase): `ok github.com/pablogore/ego/v4 190.390s`,
`ok github.com/pablogore/ego/v4/tenancy` — no `FAIL`, no `DATA RACE`, no panic anywhere in the
702-line log. `saga_actor.go` remains untouched (still deferred to #54); only `saga_test.go`
was read, not modified, for this phase.
