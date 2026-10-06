# Archive Report: Runtime Tenant Wiring (EGO-TENANT-006)

**Change**: `ego-tenant-runtime-wiring`
**Tracker**: getsyntegrity/ego#55 (epic #23)
**Archived**: 2026-09-14
**Archive location**: `openspec/changes/archive/2026-09-14-ego-tenant-runtime-wiring`

## Executive Summary

The `ego-tenant-runtime-wiring` change (EGO-TENANT-006, issue #55) has been successfully archived following completion of all implementation, verification, and review phases. All 49 tasks are complete; all 11 requirements and 22 scenarios have been proven by passing tests; all 3 stacked PRs (#56, #57, #58) are merged to main; and the verify report attests ready-to-archive: YES.

## Archive Status

| Metric | Value |
|--------|-------|
| **Status** | COMPLETE |
| **Verdict** | PASS (from verify-report) |
| **Archived Date** | 2026-09-14 |
| **Tasks Complete** | 49/49 ✓ |
| **Requirements Complete** | 11/11 ✓ |
| **Scenarios Complete** | 22/22 ✓ |
| **CRITICAL Findings** | 0 |
| **WARNING Findings** | 0 |
| **Ready-to-Merge** | YES (all PRs merged as of 2026-09-14) |
| **Ready-to-Archive** | YES |

## Artifacts Promoted

### Canonical Specs Synced

| Domain | Action | Source | Destination | Details |
|--------|--------|--------|---|---------|
| `tenancy-runtime` | **Created (NEW)** | `openspec/changes/ego-tenant-runtime-wiring/specs/tenancy-runtime/spec.md` | `openspec/specs/tenancy-runtime/spec.md` | Full spec promotion — no pre-existing canonical spec to merge. Contains 11 requirements, 22 scenarios. |

**Diff verification**: Empty `diff -r` output confirms byte-identical copy, exit status 0. The new canonical spec at `openspec/specs/tenancy-runtime/spec.md` is an exact replica of the delta spec.

### Ratified Decisions in Canonical Spec

The promoted spec includes the following ratified decisions (all ratified by repository owner on 2026-09-14):

- **DP1** — Explicit activation: Tenancy is opt-in via non-nil `WithTenantResolver`; never global default
- **DP2** — Duplicate registration: Configuration-time rejection (count-based, not last-call-wins)
- **DP3** — Proposal staleness: Closed by amended proposal.md to T4-A/T4-B split
- **DP4** — TenantContext content validation: Both `Attach` and `Require` validate `Scope()` is `ScopeTenant` or `ScopeAdministrative`

## Requirements and Scenarios Promoted

**All 11 requirements and 22 scenarios from the delta spec are now canonical**:

1. **Tenancy Is Explicitly Activated, Never Global** (AC6, DP1, T3) — 2 scenarios
2. **Single Resolver Registration Option** (AC1, T3, DP2) — 3 scenarios
3. **Duplicate Resolver Registration Rejected** (AC1, DP2) — 4 scenarios
4. **Automatic Resolution at the Command Trust Boundary** (AC2, T4-A, T5, T2) — 2 scenarios
5. **Fail-Closed Before the Domain Handler Runs** (AC3, T4-A) — 2 scenarios
6. **Defensive Persistence Invariant** (T4-B) — 1 scenario
7. **Fail-Closed Gates Validate TenantContext Content, Not Just Presence** (AC3, T4-A, T4-B, DP4) — 3 scenarios
8. **Zero-Plumbing Single-Tenant Mode** (AC4) — 1 scenario
9. **Unified Execution Path for Single and Multi-Tenant** (AC5, T6) — 1 scenario
10. **No Implicit Tenant Inference Outside Explicit Single-Tenant** (AC7) — 1 scenario
11. **No Implicit Default at Startup** (AC6, DP1) — 2 scenarios

**Total: 22 scenarios, all PROVEN by passing runtime tests** (per verify-report evidence).

## Governance & Spec Authority

### D8/DP4 Resolution (TenantContext Content Validation)

Per the launch prompt and verify-report Governance Verdict section, Decision D8/DP4 has been resolved as **verdict (A) — Compatible contract-tightening, correctly attributable to #55**.

**Key evidence**:
- No existing valid caller relied on the permissive (zero-value-accepting) behavior
- The archived canonical `tenancy-core` spec (EGO-TENANT-001, lines 89–94) already normatively required "no zero-value at a boundary"
- D8/DP4 implementation closes an enforcement gap against an already-ratified requirement
- No new error type, no incompatible signature change
- All existing valid callers (tenant-scoped and administrative contexts) are regression-tested unchanged

**Conclusion**: No edit to the archived `tenancy-core` spec is required. The archived spec text was already correct; only the implementation needed to catch up. The correction is fully documented in this change's `design.md` (Decision D8) and `spec.md` (Requirement 7, DP4 section).

### Saga Boundary

Per launch prompt and verify-report:
- `saga_actor.go` is byte-identical to main (zero changes this cycle)
- Saga command-dispatch bypass remains explicitly out of scope for #55, tracked under #54 (EGO-TENANT-002)
- The saga fail-closed test (`TestSagaFailsClosed`) confirms saga dispatches are blocked at the existing actor-side `tenancy.Require` gate, no saga-specific mechanism needed

## Implementation Evidence

### Build & Tests (from verify-report)

- **Build**: `go build -mod=vendor ./...` — PASS (exit 0, clean)
- **Vet**: `go vet -mod=vendor ./...` — PASS (exit 0, clean)
- **Targeted tests (root package)**: `timeout 300 go test -mod=vendor . -run 'Tenant|Tenancy|Ambiguous|Resolver|Saga|Batch' -v` — 33 tests, 33 PASS, 0 FAIL (189.472s)
- **Targeted tests (tenancy package)**: `timeout 200 go test -mod=vendor ./tenancy/... -run 'Tenant|Tenancy|Ambiguous|Resolver|Saga|Batch' -v` — all PASS (0.622s)
- **Race mode (root package)**: `timeout 300 go test -mod=vendor -race . -run 'Tenant|Tenancy|Ambiguous|Resolver|Saga|Batch' -v` — 33 tests, 33 PASS, 0 FAIL, 0 DATA RACE (190.392s)

### PR Integration

All 3 stacked PRs merged to main as of 2026-09-14:

| PR | Title | Commits | Merge SHA | Status |
|----|-------|---------|-----------|--------|
| #56 | feat(tenancy): add Config/Option foundation and NewEngine validation | 2 | 8471cebd36 | ✓ MERGED |
| #57 | feat(tenancy): wire the T4-A trust boundary into SendCommand and actor gates | 3 (incl. Blocker-1 fix) | 0a43a360b1 | ✓ MERGED |
| #58 | feat(tenancy): add T4-B defensive persistence invariant and single-tenant demo | 4 (incl. Blocker 2/3 fixes) | 38e1ac3a79 | ✓ MERGED |

All PRs are clean, correctly scoped, and non-cyclic. Stack integrity confirmed by verify-report.

## Task Completion Gate

The persisted `tasks.md` artifact shows **all 49 implementation tasks marked complete** (`- [x]`):

- Phase 1 (Config/Option): 6 tasks — all checked ✓
- Phase 2 (NewEngine validation): 4 tasks — all checked ✓
- Phase 3 (T4-A trust boundary): 12 tasks — all checked ✓
- Phase 4 (T4-B defensive invariant): 9 tasks — all checked ✓
- Phase 5 (Single-tenant demo & backward compat): 4 tasks — all checked ✓
- Phase 6 (Verification): 2 tasks — all checked ✓
- Phase 7 (Review-fix, Blockers 1–3): 11 tasks — all checked ✓

**Task Completion Gate Status**: ✓ PASS — No stale checkboxes; every implementation task is marked complete in the persisted artifact.

### Phase 7 Blocker Resolutions

All 3 P1 blockers found during adversarial review have been resolved and confirmed:

1. **Blocker 1** — Zero-value `TenantContext` accepted by `Attach`/`Require`
   - **Fix**: `tenancy/context.go` (lines 59–62, 100–102) and `tenancy/tenant_context.go` (lines 180–186) `valid()` method
   - **Evidence**: End-to-end test in `engine_test.go:290–326` (`TestSendCommandTenantResolution`); unit tests in `tenancy/context_test.go` and `context_internal_test.go`
   - **Status**: ✓ CONFIRMED

2. **Blocker 2** — `verifyTenantForPersist` inheriting Blocker-1's fix with zero production-code change
   - **Claim**: `event_sourced_actor.go:708–714` and `durable_state_actor.go:335–341` have unchanged bodies; inheritance via `tenancy.Require` is automatic
   - **Evidence**: New subtests in both `*_test.go` files prove inheritance; no new code added to either function
   - **Status**: ✓ CONFIRMED

3. **Blocker 3** — Batch homogeneity check after `HandleCommand`, allowing zero-event cross-tenant leak
   - **Fix**: `event_sourced_actor.go:875–897` homogeneity gate moved into pre-handler check, before `HandleCommand`
   - **Evidence**: Explicit test `TestEventSourcedActorBatchTenantHomogeneity_ZeroEventCrossTenant` exercises the exact leak scenario
   - **Status**: ✓ CONFIRMED

All review threads resolved (PR #57 `PRRT_kwDOSegGrc6iNWB7`; PR #58 `PRRT_kwDOSegGrc6iOul3`, `PRRT_kwDOSegGrc6iOul-`).

## Verification Report Reference

- **Path**: `openspec/changes/archive/2026-09-14-ego-tenant-runtime-wiring/verify-report.md`
- **Verdict**: **PASS**
- **Critical findings**: 0
- **Warnings**: 0
- **Suggestions**: 2 (non-blocking)
- **Scenarios proven**: 22/22
- **Requirements proven**: 11/11
- **Tasks complete**: 49/49

## Archive Contents Checklist

- [x] **proposal.md** — Outcome, scope, decisions T1–T6 ✓
- [x] **specs/** — `tenancy-runtime/spec.md` (11 requirements, 22 scenarios) ✓
- [x] **design.md** — Technical approach, 8 architecture decisions (D1–D8), Blocker analysis ✓
- [x] **tasks.md** — 49 tasks, all complete; Phases 1–7; flaky-test reconciliation ✓
- [x] **verify-report.md** — Final verification verdict (PASS); spec compliance matrix; blocker confirmations ✓

## Canonical Specs Updated

| Spec File | Status | Change |
|-----------|--------|--------|
| `openspec/specs/tenancy-core/spec.md` | Unchanged | Pre-existing canonical spec from EGO-TENANT-001; no edit required per D8/DP4 governance resolution |
| `openspec/specs/tenancy-runtime/spec.md` | **NEW** | Promoted from delta spec; contains all 11 requirements and 22 scenarios |

## Next Recommended Action

**No follow-up SDD phases required.** The change is complete and archived.

**Administrative action** (owner-owned, not part of SDD): Issue #55 AC6 wording needs a reconciliation on GitHub. The current literal text ("sin resolver configurado, el engine falla en startup") is stronger than the ratified intent and must be clarified per spec.md section "Action Required Outside This Spec".

## Archival Verification

**Mechanical copy verification**:
- Source folder (`openspec/changes/ego-tenant-runtime-wiring`) successfully moved to archive (`openspec/changes/archive/2026-09-14-ego-tenant-runtime-wiring`) via `git mv`
- Source folder confirmed absent from active changes directory
- Archive folder confirmed present with all artifacts (proposal, specs, design, tasks, verify-report)
- `diff -r` output: EMPTY (exit status 0) — confirming byte-identical copy

**Spec promotion verification**:
- Delta spec copied to canonical location (`openspec/specs/tenancy-runtime/spec.md`)
- `diff -r` output: EMPTY (exit status 0) — confirming byte-identical copy

## Dates and Metadata

| Field | Value |
|-------|-------|
| Change name | `ego-tenant-runtime-wiring` |
| GitHub issue | getsyntegrity/ego#55 |
| Epic | getsyntegrity/ego#23 |
| Archive date | 2026-09-14 |
| Archive folder | `openspec/changes/archive/2026-09-14-ego-tenant-runtime-wiring` |
| All PRs merged to main | ✓ Yes, as of 2026-09-14 |
| Verify report verdict | PASS |
| Ready-to-archive | YES |

---

**Archived by**: sdd-archive executor
**Archive report version**: 1.0
**SDD cycle complete**: YES
