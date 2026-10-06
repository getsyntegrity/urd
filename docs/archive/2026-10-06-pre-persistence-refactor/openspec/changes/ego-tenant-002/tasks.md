# Tasks: Tenant-aware write path, remaining slices (EGO-TENANT-002)

Tracker [#54](https://github.com/getsyntegrity/ego/issues/54), epic #23 · `specs/tenancy-write-path/spec.md` (read-only) · `design.md` (read-only) · `proposal.md` (read-only). PR1 (`#73`), PR2 (`#77`), and **PR3 (`#78`) are all merged** — proto, `event_sourced_actor.go`, and `durable_state_actor.go` are consumed unchanged by PR3, not edited by any task below. **This entire task list is now a historical record of already-completed work; do not re-run it.** No PR of EGO-TENANT-002 remains open — see `design.md`'s intro for the same status.

## Review Workload Forecast

| Field | Value |
|---|---|
| Estimated changed lines | PR2 ~250-350 (1 file + 1 new test file); PR3 ~450-600 (1 file + 2 new test files) |
| 400-line budget risk | PR2: Low-Med; PR3: Medium |
| Chained PRs recommended | Yes |
| Suggested split | PR2 (durable-state) → PR3 (saga + e2e) |
| Delivery strategy | ask-on-risk |
| Chain strategy | stacked-to-main |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: Medium

Rationale: `proposal.md`'s delivery plan is already a stacked chain (PR1 merged); `design.md`'s PR Slicing table confirms disjoint file sets, PR3 targeting PR2's merged base, and explicitly names PR4 as contingency only — no PR4 task group is created here.

### Suggested Work Units

| Unit | Goal | Likely PR | Focused test command | Runtime harness | Rollback boundary |
|---|---|---|---|---|---|
| 1 | Durable-state tenant persist/recover/identity/read-gate | PR 2 | `go test -mod=vendor -race -run 'TestDurableState.*Tenant' .` | N/A — actor unit tests via `mocks.StateStore` | Revert `durable_state_actor.go` diff + delete `durable_state_actor_tenant_persist_test.go` |
| 2 | Saga tenant binding + real-dispatch e2e | PR 3 | `go test -mod=vendor -race -run 'TestSaga.*Tenant|TestTenantWritePathE2E' .` | `require.Eventually` over `testkit.NewEventsStore()` (real dispatch) | Revert `saga_actor.go` diff + delete `saga_actor_tenant_test.go`, `tenant_write_path_e2e_test.go` |

---

## PR2 — Durable-State Tenant Enforcement — **DONE, merged as [`#77`](https://github.com/getsyntegrity/ego/pull/77)**

Targeted PR1's merged base (`main` @ `ddf9337`). No proto change. New test file: `durable_state_actor_tenant_persist_test.go`. All tasks below are checked off as a historical record; the implementation went through its own review round with two refinements beyond what's listed here — a `commitState` single-commit-point consolidation and a `currentVersion > 0` discriminant for DS3's unseeded-`PostStop` skip (semantically equivalent to `actorTenant == noTenantContext`, more robust). See `design.md`'s PR2 header note.

### Phase 1: Foundation — seed at `recoverFromStore` (DS2)

- [x] 1.1 RED `durable_state_actor_tenant_persist_test.go` — `recoverFromStore` seeds `actorTenant` from a record with valid tenant metadata
- [x] 1.2 RED `durable_state_actor_tenant_persist_test.go` — genesis (`durableState == nil || proto.Equal(...)`) leaves the actor unseeded (`noTenantContext`), no `ErrInvalid`
- [x] 1.3 RED `durable_state_actor_tenant_persist_test.go` — absent tenant metadata on a non-genesis record (tenant-aware mode) fails `PreStart` with `ErrInvalid`
- [x] 1.4 RED `durable_state_actor_tenant_persist_test.go` — malformed/undecodable tenant metadata fails `PreStart` with `ErrInvalid`
- [x] 1.5 GREEN `durable_state_actor.go` — add `actorTenant tenancy.TenantContext` field; seed in `recoverFromStore` after the genesis branch (~line 178), before `resultingState.UnmarshalTo` (~line 185), per DS2

### Phase 2: T4-A gate extension (DS1)

- [x] 2.1 RED `durable_state_actor_tenant_persist_test.go` — command from a different tenant than the established `actorTenant` rejected with `ErrDenied` before `HandleCommand` runs and before `entity.currentState` mutates (behavior spy, zero invocations)
- [x] 2.2 RED `durable_state_actor_tenant_persist_test.go` — missing ctx tenant in tenant-aware mode rejected with `ErrMissing`
- [x] 2.3 RED `durable_state_actor_tenant_persist_test.go` — matching-tenant command proceeds unchanged (positive path)
- [x] 2.4 GREEN `durable_state_actor.go` — extend the existing T4-A gate in `processCommand` (~lines 222-227): capture `tc`, `tenancy.Require`, `VerifyUnchanged` against `actorTenant`, `establishActorTenant` on first command (DS1). `verifyTenantForPersist` (T4-B, ~line 254) stays unchanged, per DS1's rejected-alternatives table

### Phase 3: Persist writes `actorTenant` + unseeded `PostStop` skip (DS3, 4 mandatory tests)

- [x] 3.1 RED `durable_state_actor_tenant_persist_test.go` — `persistStateAndPublish` via the `processCommand` path writes exact `ego.tenant.*` keys via `MarshalMetadata(actorTenant)`; legacy mode writes none
- [x] 3.2 RED `durable_state_actor_tenant_persist_test.go` — (DS3 mandatory test a) tenant-aware + never seeded: `PostStop` does not call `persistStateAndPublish` (store call-count assertion, zero calls)
- [x] 3.3 RED `durable_state_actor_tenant_persist_test.go` — (DS3 mandatory test b) tenant-aware + seeded: `PostStop` writes with `actorTenant`'s metadata (store capture, assert `tenant_metadata` matches)
- [x] 3.4 RED `durable_state_actor_tenant_persist_test.go` — (DS3 mandatory test c) legacy mode: `PostStop` keeps today's unconditional flush unchanged (regression assertion against pre-PR2 behavior)
- [x] 3.5 RED `durable_state_actor_tenant_persist_test.go` — (DS3 mandatory test d) recovery with invalid/malformed tenant metadata fails at `PreStart` via DS2; `PostStop` is never reached (assert `PreStart` returns wrapped `ErrInvalid`, actor never becomes ready)
- [x] 3.6 GREEN `durable_state_actor.go` — `persistStateAndPublish` writes `entity.actorTenant` metadata at both call sites (`processCommand:259`, `PostStop:166`); `PostStop` skips the call when `actorTenant == noTenantContext` in tenant-aware mode; legacy mode keeps the unconditional flush (DS3)

### Phase 4: `GetStateCommand` read gate (DS4)

- [x] 4.1 RED `durable_state_actor_tenant_persist_test.go` — `GetStateCommand` from a foreign tenant rejected with `ErrDenied`
- [x] 4.2 RED `durable_state_actor_tenant_persist_test.go` — `GetStateCommand` with missing ctx tenant (tenant-aware mode) rejected with `ErrMissing`
- [x] 4.3 RED `durable_state_actor_tenant_persist_test.go` — `GetStateCommand` from the matching tenant succeeds (positive path)
- [x] 4.4 GREEN `durable_state_actor.go` — add `tenancy.Require` + `VerifyUnchanged` gate before `sendStateReply` in the `*egopb.GetStateCommand` case (~line 141), mirroring `event_sourced_actor.go:613-628` (read-only reference) per DS4

### Phase 5: Regression + verification (PR2)

- [x] 5.1 Run the existing suite in legacy mode; confirm no behavior change (`TestEngineDurableState` unmodified)
- [x] 5.2 `go mod vendor && go test -mod=vendor -p 1 -timeout 0 -race ./...` green
- [x] 5.3 `go build ./...` and `go vet ./...` clean; confirm `command/` (read-only), `tenancy/` (read-only), `event_sourced_actor.go` (read-only), `protos/` (read-only), `egopb/` (read-only) diffs are empty against `main`

---

## PR3 — Saga Tenant Binding + End-to-End Integrity — **DONE, merged as [`#78`](https://github.com/getsyntegrity/ego/pull/78)**

Targeted PR2's merged base per `design.md`'s PR Slicing table. No proto change. New test files: `saga_actor_tenant_test.go` (8 test functions), `tenant_write_path_e2e_test.go` (2 test functions). Also touched, beyond this task list's original file scope: `saga.go` (`SagaAction.isNoop()`), `saga_test.go` (`TestSagaFailsClosed` updated), `engine.go` (`SagaStatus` tenant resolution) — see `design.md`'s File Changes table. The implementation went through two review-round corrections beyond what Phase 2/4 describe below: SG4's bind timing was corrected from bind-on-decode to bind-deferred-until-`HandleEvent`-proves-relevance, and SG-DUR1 (durable binding before commit + a `GetStateCommand` read gate, OPT.1 below) was added. All tasks below are checked off as a historical record.

### Phase 1: Foundation — `eventContext` helper (SG2)

- [x] 1.1 RED `saga_actor_tenant_test.go` — `eventContext` reconstructs `TenantContext` (tenant + administrative scope) from valid metadata (table test) — `TestSagaActorEventContext`
- [x] 1.2 RED `saga_actor_tenant_test.go` — `eventContext` returns wrapped `ErrInvalid` on absent/malformed tenant metadata in tenant-aware mode; parent ctx unchanged — `TestSagaActorEventContext`
- [x] 1.3 RED `saga_actor_tenant_test.go` — `eventContext` legacy-mode passthrough returns `(parent, nil)` — `TestSagaActorEventContext`
- [x] 1.4 GREEN `saga_actor.go` — add `tenantAware bool` and `boundTenant tenancy.TenantContext` fields (`noTenantContext` until seeded); implement `eventContext(parent context.Context, event *egopb.Event) (context.Context, error)` (SG2). **Ordering requirement satisfied**: `PreStart` sets `s.tenantAware` from the extension's presence *before* calling `s.recover()` (`saga_actor.go:103-105`, explicit comment: "set before recover() so replay validation (SG5) gates on the same tenantAware value the live path uses (SG4)") — this is what makes Phase 4's replay validation and the live-path bind/verify consistent with each other; there is no window where `recover()` runs against a stale/zero `tenantAware`.

### Phase 2: Bind deferred until `HandleEvent` proves relevance + `VerifyUnchanged` (SG4, corrected from the original bind-on-decode plan)

- [x] 2.1 RED `saga_actor_tenant_test.go` — a freshly started saga instance binds to the first valid tenant it processes (behavior spy asserting `boundTenant` set after event 1) — `TestSagaActorBindOnFirstEvent`
- [x] 2.2 RED `saga_actor_tenant_test.go` — a second event from a different tenant, once bound, rejected with `ErrDenied` before `HandleEvent` runs; no mutation, no dispatch, no persisted event (two-tenant fixture, zero-invocation assertion) — `TestSagaActorBindOnFirstEvent`
- [x] 2.3 RED `saga_actor_tenant_test.go` — tenant-less/malformed event at a reset site: `HandleEvent` never invoked, no dispatch, `status` unchanged, saga still consumes the next valid event — `TestSagaActorEventContext` / `TestSagaFailsClosed` (`saga_test.go`)
- [x] 2.4 RED `saga_actor_tenant_test.go` — `compensate` dispatches under `boundTenant` (timeout path uses `boundTenant` directly) — `TestSagaActorCompensateUsesBoundTenant`
- [x] 2.5 RED `saga_actor_tenant_test.go` — unbound tenant-aware timeout (no event ever processed) fails closed: `status = SagaFailed`, zero dispatches — `TestSagaActorCompensateUsesBoundTenant`
- [x] 2.6 GREEN `saga_actor.go` — **implemented, then corrected by review**: `handleStreamEvent` (`saga_actor.go:372-460`) runs `bindOrVerify` (`VerifyUnchanged`) *before* `HandleEvent` only when already bound; when unbound, `HandleEvent` runs first and only a non-noop `SagaAction` (`SagaAction.isNoop()`, `saga.go`) binds `boundTenant` — deferred so an unrelated tenant's noise event on the shared topic can never poison the binding. Reject + log (saga ID, persistence ID, sequence number only) + return on `ErrInvalid`/`ErrDenied`; `compensate` reads `boundTenant` directly via `compensationContext` and fails closed if unbound (SG4). See `design.md`'s SG4 section and sequence diagram for the exact accepted/rejected flow.

### Phase 3: Thread ctx through reset sites; saga events carry metadata (SG1, SG3)

- [x] 3.1 RED `saga_actor_tenant_test.go` — `persistAndApplyEvents` writes `tenant_metadata` on saga-emitted events in tenant-aware mode; legacy mode writes none (SG3) — `TestSagaActorPersistAndApplyEventsWritesTenantMetadata`
- [x] 3.2 RED `saga_actor_tenant_test.go` — `sendCommand` dispatches on a ctx where `tenancy.Require` succeeds with the bound tenant (entity-side probe) — `TestSagaActorSendCommandThreadsTenantContext`
- [x] 3.3 GREEN `saga_actor.go` — ctx threaded from `eventContext` through `handleStreamEvent` → `processAction`/`dispatchActionEffects` → `persistAndApplyEvents` and `sendCommand`; each gains a `context.Context` parameter. Site 242 (`consumeEvents` → `Tell(context.Background(), ...)`) stays unchanged by design (SG1). `persistAndApplyEvents` sets `TenantMetadata: tenancy.MarshalMetadata(tc)` guarded by `tenantAware` (SG3)

### Phase 4: Replay validation against first-replayed-event `boundTenant` (SG5)

- [x] 4.1 RED `saga_actor_tenant_test.go` — `recover()` establishes `boundTenant` from the first successfully-decoded replayed event — `TestSagaActorRecoverReplayTenantValidation`
- [x] 4.2 RED `saga_actor_tenant_test.go` — a later replayed event from a different tenant fails recovery closed at `PreStart` (`require.ErrorIs(..., tenancy.ErrDenied)`), mirroring `applyPersistedEvent`'s replay-path gate (read-only reference, PR1) — `TestSagaActorRecoverReplayTenantValidation`
- [x] 4.3 RED `saga_actor_tenant_test.go` — a replayed event with absent/undecodable tenant metadata fails `PreStart` with `ErrInvalid` (TA3, no backfill) — `TestSagaActorRecoverReplayTenantValidation`
- [x] 4.4 GREEN `saga_actor.go` — `recover()`'s per-event replay (`saga_actor.go:196-255`) wired through `eventContext` + the Phase 2 bind-or-verify logic (SG5); relies on 1.4's ordering (`tenantAware` set before `recover()` runs)

### Phase 5: Real-dispatch end-to-end integrity

- [x] 5.1 RED `tenant_write_path_e2e_test.go` — `Engine.SendCommand` (stub resolver → one tenant) → entity A persists → saga consumes → `SagaCommand` → entity B's `HandleCommand` observes the same tenant via `tenancy.From`; `require.Eventually` + `testkit.NewEventsStore()`. Not `TestSendCommandTenantResolution` — `TestTenantWritePathE2E`
- [x] 5.2 GREEN — no new production mechanism required beyond Phases 1-4 + SG-DUR1's wiring; `TestTenantWritePathE2E` and `TestEngineSagaStatusTenantIsolation` both pass against the shipped code

### Phase 6: Regression + verification (PR3)

- [x] 6.1 Legacy-mode suite unchanged in behavior; `saga_test.go`'s `TestSagaFailsClosed` was updated (not left unmodified) to assert the SG4 correction's stronger guarantee — see `design.md`'s File Changes table for why the "read-only" assumption on that file didn't hold
- [x] 6.2 `go mod vendor && go test -mod=vendor -p 1 -timeout 0 -race ./...` reported green in PR78's merge description; re-verified from this branch after rebasing onto `main` (see PR #79 CI run)
- [x] 6.3 `go build ./...` and `go vet ./...` clean; `command/`, `tenancy/`, `protos/`, `egopb/`, `durable_state_actor.go`, `option.go`, `persistence/` diffs verified empty against PR2's merged base (`git diff --stat f0bc1f8..main`); `engine.go` and `saga.go`/`saga_test.go` were **not** empty — corrected in `design.md`'s File Changes table, this was a scoping miss in the original task list, not an undisclosed change

### Optional / Stretch

- [x] OPT.1 `replyWithState` (`*egopb.GetStateCommand` on `SagaActor`) read gate. This task list originally recorded it as a PR3 follow-up *recommendation only*, requiring explicit human sign-off before implementation. **It shipped anyway**, inside PR3's own second review round, as SG-DUR1: `checkStateReadTenant` gates `getStateAndReply` mirroring DS4's shape (`TestSagaActorCheckStateReadTenant`), and `Engine.SagaStatus` now attaches the caller's tenant at the trust boundary (`TestEngineSagaStatusTenantIsolation`). The shipped behavior is correct, tested, and closes a real cross-tenant read gap — but it landed without the sign-off this task explicitly required. Recorded here as a governance note, not swept under a silent checkmark: the fix was worth having; the process that let it in without the gate is worth tightening for future PRs.
