# Proposal — Canonical command/result envelopes and metadata (EGO-WRITE-003)

| Field | Value |
|---|---|
| Change | `ego-write-003` |
| Date | 2026-09-14 |
| Phase | `sdd-propose` — proposed |
| Tracker | [`#59`](https://github.com/getsyntegrity/ego/issues/59), epic [`#12`](https://github.com/getsyntegrity/ego/issues/12), depends [`#10`](https://github.com/getsyntegrity/ego/issues/10)/[`#11`](https://github.com/getsyntegrity/ego/issues/11), blocks [`#54`](https://github.com/getsyntegrity/ego/issues/54) |
| Evidence base | Engram `sdd/ego-write-003/explore` |

Documentation only. No Go code in this artifact.

## Problem

Ego has zero envelope/identity infrastructure (explore §Current State): `Command` is a bare `proto.Message` alias, `egopb.ErrorReply` is `{message string}`, and no operation, correlation, causation or principal concept exists. Every downstream WRITE-*/TENANT-* story needs one shared vocabulary for "what operation is this, what caused it, who and which tenant is acting" — or each invents its own, starting with #54.

**Success**: one canonical, runtime-independent Go contract that #54 and WRITE-005 extend instead of duplicating.

## Decisions (closed — not reopened here)

| # | Decision | Rationale |
|---|---|---|
| W1 | Contract-first, standalone: real Go envelope/metadata types + contract tests. `Engine.SendCommand`, its 29 callers, the dispatch path and `SagaActor` are untouched. | Owner-ratified. "What the envelope means" ≠ "how it traverses the runtime"; conflating them turns a contract story into a transversal migration that still would not close the `SagaActor` bypass. TENANT-001 → TENANT-006 precedent. |
| W2 | The envelope is the durable/logical contract; `context.Context` is at most an execution-time carrier, never the canonical home. | Identity and tenant metadata must survive persistence, sagas and retries; ctx-only re-hides the contract. |
| W3 | Tenant metadata is a *slot* composing `tenancy/`. No resolver, default, or parallel tenancy contract. | Reuse, don't duplicate; #54 extends this slot. |
| W4 | `operation_id` ≠ `op_key`; idempotency keys stay WRITE-005's. | WRITE-005 hasn't started; collapsing them is unrecoverable. |
| W5 | Outcome/failure taxonomy is Go-side on the result envelope only; `protos/` and `egopb.ErrorReply` untouched. | Invariant 8; the flat-string wire error is an accepted, documented gap. |
| W6 | Custom metadata is governed: namespaced reserved keys, canonical fields non-overwritable, no ungoverned `map[string]any`. | Mirrors `tenancy.Metadata`'s `ego.tenant.*` precedent. |
| W7 | Compatibility/migration ships as a **documentation/design deliverable of WRITE-003**: inventory of affected APIs and dispatch paths (`SendCommand`, `SagaActor`, any other path found), a breaking-change classification, and a proposed incremental adoption sequence (overloads/adapters/constructors/deprecations as needed) — written, not executed. WRITE-003 still does not modify `SendCommand`, create compatibility shims, or migrate callers. | Owner-ratified. The contract must be validated as *adoptable* before it is frozen — approving a canonical type nobody has checked against the 29 real callers risks freezing something awkward or needlessly breaking once the follow-up tries to wire it in. Execution of the migration stays in the follow-up. |

**Contract named** (shapes fixed in design): a command envelope; command metadata (operation/correlation/causation IDs, tenant slot, principal slot, custom, timestamp, deadline); a result/outcome envelope; a derive-child-operation rule — correlation persists across a flow, operation changes per operation, causation points at the causing operation.

**Atomicity**: explore's `REVIEW_REQUIRED` (wire `SendCommand` now vs. contract-first) is closed by W1; one outcome remains, so `ATOMIC`.

## Scope

**In**: envelope, metadata and result/outcome types; identity semantics and child-operation derivation; tenant slot over `tenancy/`; principal slot; custom-metadata governance; temporal fields; contract tests.

**In, added**: a written migration strategy (API/dispatch-path inventory, breaking-change classification, incremental adoption sequence, adapter/deprecation needs) — document only, W7.

**Out** — the first four rows are ONE follow-up issue, *"WRITE-00x — Canonical command envelope runtime integration"*, not done unless all four are covered:

| Deferred | Owner |
|---|---|
| `Engine.SendCommand` signature/behavior + its 29 callers | follow-up |
| Dispatch path, ctx propagation, end-to-end envelope flow | follow-up |
| `SagaActor.sendCommand`/`.compensate` bypass; `SagaCommand` identity fields | follow-up |
| Migration **execution** (adapters/shims built, callers actually migrated, deprecations landed) | follow-up |
| Idempotency `op_key` | WRITE-005 |
| Tenant-aware propagation; tenant + aggregate identity | #54 |
| Wire/proto changes; trace/span/baggage | #59 non-goals |

## AC placement against #59

Verified directly against the 14 literal ACs in issue #59 (not just by theme):

| #59 AC | Here | Follow-up |
|---|---|---|
| AC1 Canonical command envelope | yes | — |
| AC2 Operation identity (semantics/ownership/propagation rules) | yes | rules *applied* at dispatch/saga |
| AC3 Idempotency boundary (`operation_id` ≠ `op_key`, documented) | yes (W4) | — |
| AC4 Tenant metadata slot reusing `tenancy/` | yes | end-to-end propagation (#54) |
| AC5 Principal/security metadata (abstract slot only) | yes | — |
| AC6 Custom metadata governance | yes | — |
| AC7 Time/deadline semantics | yes (fields + semantics) | deadline *enforcement* at runtime |
| AC8 Canonical result envelope; 6 outcome kinds | yes | outcome *observed* end-to-end |
| AC9 Framework boundary (consumer builds the envelope, not Ego) | yes — stated as an invariant of the contract itself | — |
| AC10 Causal propagation proven by test/example | yes (derivation rule + unit tests) | derivation *exercised* inside real dispatch/saga |
| AC11 #54 dependency (consumes, no parallel envelope) | yes — enforced as a design constraint + risk (R4) | #54's own compliance |
| AC12 Backward compatibility / migration strategy | yes — **written strategy only** (W7) | migration *execution* |
| AC13 Contract coverage (tests for root/causal/tenant/principal/custom/deadline/success/failure) | yes | — |
| AC14 No infrastructure leakage (no protocols/headers/wire/credentials/tracing) | yes — enforced by W1/W5 + success-criteria compile check | — |

All 14 ACs are satisfiable contract-first; none require the runtime round-trip to close. The issue's boundary diagram (`Caller → CommandEnvelope → Ego write side → CommandResultEnvelope → Caller`) is architectural framing, not a literal acceptance criterion — no reconciliation with #59's GitHub text is needed.

**Previously open, now closed by the repository owner (2026-09-14):**

- ~~RQ1~~ → W7: migration strategy ships as a written deliverable of WRITE-003; execution stays in the follow-up.
- ~~RQ2~~ → resolved: no AC demands the literal end-to-end flow; the diagram is framing, not a criterion.
- ~~RQ3~~ → resolved: AC numbering verified against #59's text above; theme-level grouping was already accurate, no AC was misplaced.

## Capabilities

- **New**: `command-envelope` — command/result envelopes, canonical metadata, operation identity semantics, custom-metadata governance.
- **Modified**: None. `tenancy-core` is consumed unchanged; `tenancy-runtime` untouched.

## Affected areas

| Area | Impact |
|---|---|
| new envelope package (name in design) | New — all types + contract tests |
| `tenancy/` | Unchanged — consumed for the tenant slot |
| `engine.go`, `saga.go`, `saga_actor.go`, `behavior.go`, `protos/ego/`, `egopb/` | Unchanged — follow-up territory |

**Public consumer surfaces, byte-identical after this change**: `Engine.SendCommand`, `Command`/`Event`/`State`, `SagaCommand`, `EventSourcedBehavior`/`DurableStateBehavior`/`SagaBehavior`, `Config`/`Option`, `egopb.CommandReply`/`ErrorReply`.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| An inert contract nothing calls until the follow-up lands | High | Accepted (TENANT-001 precedent); follow-up issue filed this cycle |
| `tenancy.Administrative.CorrelationID()` already exists with an unrelated meaning | Med | Design must disambiguate naming |
| A ctx carrier does not survive a remote goakt hop | Med | Documented boundary, identical to tenancy's — not a new gap |
| #54 introduces a parallel envelope | Med | #54 must extend this contract |

## Human gate

`spec-governance` §10 — public API. W1–W7 and the AC-placement table were ratified by the repository owner (2026-09-14). No open decisions remain before `sdd-spec`.

## Rollback

Purely additive: new package, zero consumers, no signature, wire, schema or behavior change. Revert = delete the new package and its tests, then `go mod tidy && go mod vendor`. No caller, stored event or reply payload is affected at any point.

## Success criteria

- [ ] The envelope package compiles with no import of goakt, `engine.go`, or transport/auth libraries.
- [ ] The tenant slot holds `tenancy/`'s own types — no duplicate tenant identity or tenant-metadata definition exists.
- [ ] Derivation proven by test: correlation persists, operation changes, causation equals the parent operation.
- [ ] All six outcomes (success with payload, success without payload, domain rejection, application/runtime failure, deadline/timeout, cancellation) are representable and mutually distinguishable.
- [ ] Custom metadata cannot overwrite a canonical field or use a reserved key.
- [ ] `operation_id` and an idempotency key are separable — nothing forces WRITE-005 to reuse `operation_id`.
- [ ] `Engine.SendCommand`, `SagaCommand`, `egopb` and `protos/` diffs are empty.
- [ ] A written migration strategy exists: affected APIs/dispatch paths inventoried, breaking changes classified, incremental adoption sequence proposed — no code changes to execute it.
- [ ] The follow-up issue exists and names `SendCommand`, dispatch, `SagaActor` and migration execution as one unit.
