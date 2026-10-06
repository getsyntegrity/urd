# Proposal — Canonical `TenantContext` and `TenantResolver` SPI (EGO-TENANT-001)

| Field | Value |
|---|---|
| Change | `ego-tenant-context` |
| Date | 2026-09-13 |
| Phase | `sdd-propose` |
| Status | proposed |
| Tracker | [`getsyntegrity/ego#45`](https://github.com/getsyntegrity/ego/issues/45) (EGO-TENANT-001) |
| Epic | [`getsyntegrity/ego#23`](https://github.com/getsyntegrity/ego/issues/23) (native multi-tenancy) |
| Dependency | [`getsyntegrity/ego#10`](https://github.com/getsyntegrity/ego/issues/10) (hexagonalization) |
| Evidence base | `exploration.md` (this change folder) |
| Prior decisions | `state.yaml` → `phases.decision` (closed by user, 2026-09-13) |

Documentation only. No Go code in this artifact.

## Problem

Ego has no tenancy concept (explore §3: zero real matches for `tenant`; every adjacent candidate is a false positive). Epic #23's stories (envelopes, store isolation, projections, topic isolation, single-tenant mode, conformance tests, admin semantics) all need one shared vocabulary for "which tenant is this execution acting for," or each will invent its own. `Engine.EraseEntity` and `migration.Migrator.Run` already iterate persistence unconditionally (explore §8/§10) — harmless today, a silent leak path once tenant data lands in shared stores.

**Success**: one canonical, transport-neutral, GoAkt-independent contract that TENANT-002…008 consume, plus an enforceable guarantee it stays free of transport/auth/actor-runtime dependencies.

## Decisions (closed — not reopened here)

Closed by the user against explore §13(b); inputs to this proposal, not choices under evaluation.

| # | Decision | Rationale (explore ref) |
|---|---|---|
| S1 | `TenantID` is `type TenantID string` — a defined type, not an alias — opaque, non-empty, no UUID mandate | Every existing ID (`persistenceID`, `entityID`, `sagaID`) is a bare string (§4.1); a defined type buys compile-time isolation an alias cannot, without a struct's ergonomic cost (§4.2) |
| S2 | `TenantResolver` core contract MUST NOT depend on GoAkt. The GoAkt runtime MAY wrap/transport a resolver via `internal/extensions`, but that's a runtime-adapter concern (TENANT-006), not this decision | Existing SPI idiom (`encryption.Encryptor`) takes only `context.Context` + framework types (§6.1); `behavior.go`/`saga.go` are already infra-clean (§10) |
| S3 | Resolution ≠ propagation. `TenantResolver` resolves ONCE at the trust boundary into a `TenantContext`. Direct/local propagation via `context.Context` applies only on paths that preserve that context end-to-end (proven same-node, §5.1) — it does **not** cover the saga boundary (see note below). Remote/cross-node propagation uses a tenant-aware envelope owned by TENANT-002 | §5.2: `context.Context` cannot cross a GoAkt cluster relocation — envelopes are load-bearing, not optional, for TENANT-002 |
| S4 | Tenant + aggregate identity together form the aggregate's effective identity. Rejected: a global actor whose tenant varies per command | §12: that shape is a confused-deputy vulnerability, not a deferrable open question. Physical namespace format deferred to TENANT-002/003 |
| S5 | Single-tenant and multi-tenant share one machinery. `WithSingleTenant(TenantID)` is a built-in resolver/policy, not a second execution path | §7: `ResolveLogger`'s built-in-default-through-the-same-seam pattern is direct precedent |

**Saga boundary is not a `context.Context`-preserving path**: `saga_actor.go` resets to `context.Background()` at several points in the pipeline (explore §10), so a `TenantContext` attached upstream does not survive it via S3's direct/local mechanism. Crossing a saga MUST instead reconstruct `TenantContext` explicitly from tenant-aware metadata carried with the saga event/command — never inferred, never silently dropped. EGO-TENANT-001 defines the contract/invariant this reconstruction depends on (see the normative invariant below, which already names "saga" as a boundary identity must survive); the durable envelope/serialization format that carries that metadata remains TENANT-002's, consistent with S3's remote-propagation ownership.

**Contract named (shape fixed in `sdd-design`)**: `TenantID`, `TenantContext` (tenant identity + an explicit `Tenant`/`Administrative` scope discriminator — never nil, empty, or a magic tenant name, per §8), `TenantResolver`, `WithSingleTenant`. Exact signatures, constructors, and helper methods are deferred to design.

**Normative invariant** (carried to design, to be specified against): *Once an execution has a tenant identity, Ego MUST NOT permit that identity to change implicitly while crossing command, saga, event, persistence, or projection boundaries.*

**Architecture conformance**: upgrade the existing substring-scan technique (`logger_architecture_test.go`, explore §2/§11) to an import-graph check for the tenancy core boundary — additive, not a replacement. Exact tooling (`go/packages` vs `go list -deps`) and the banned-import list are a design-phase decision; this is also a candidate foundational piece for epic #10.

## Scope

**In**: `TenantID` + constructor/validator; `TenantContext` (tenant/administrative scope); `TenantResolver` SPI; `WithSingleTenant`; local context attach/read/require helpers; the tenant error model; the import-graph conformance test.

**Out**:

| Deferred | Owner |
|---|---|
| Remote/cross-node envelope propagation | TENANT-002 |
| Physical persistence namespace/key format | TENANT-002/003 |
| Tenant parameter on `Behavior`/`Saga`/other public signatures | never (protected contract) |
| Engine-side `Option` wiring (`WithTenantResolver`, extension registration) | TENANT-006 |
| `testkit/scenario.go` hardcoded `context.Background()` seam | TENANT-007 |
| Tenant scoping of `EraseEntity`/`Migrator.Run` | TENANT-008 |
| Per-tenant broker topic strategy | TENANT-005 |

## Capabilities

*Contract with `sdd-spec`; `openspec/specs/` is currently empty.*

- **New**: `tenancy-core` — `TenantID`, `TenantContext`, `TenantResolver`, `WithSingleTenant`, propagation helpers, error model, conformance guarantee.
- **Modified**: None.

## Consequences / Risks

| Risk | Mitigation |
|---|---|
| Contract implies cluster-wide propagation it can't yet deliver | S3 states the same-node limit explicitly; design must document it on the helpers themselves |
| Saga boundary silently loses/changes tenant identity because `saga_actor.go` resets to `context.Background()` | S3 now scopes direct `context.Context` propagation to paths that preserve it and excludes the saga boundary explicitly; the saga note above requires reconstruction from tenant-aware metadata, and a required design-phase acceptance scenario (Success Criteria) proves it |
| S4's effective-identity rule isn't enforceable until TENANT-002/003 land | Accepted deliberately — the constraint ships now so those stories inherit it rather than negotiate it |
| Recommendations R1–R5 (below) get treated as settled downstream | Kept structurally separate from Decisions above; `sdd-design` must re-surface any it depends on |
| Import-graph conformance is a technique unused elsewhere in this repo | Additive; the substring-scan fallback still satisfies #45 if the harness proves costly |

**Rollback**: purely additive, zero consumers, no signature/schema/wire change. Revert = delete the `tenancy` package, its conformance test, and its mocks; `go mod tidy && go mod vendor`.

## Recommendations on still-open items (NOT settled — open to override)

| # | Item | Recommendation |
|---|---|---|
| R1 | Normalization policy | Validate, don't normalize: reject empty/whitespace/control/non-UTF-8, cap length, no case-folding |
| R2 | Engine-invokes vs caller-side resolver | Invocation ownership is an explicit contract obligation of #001, not left implicit: `WithSingleTenant(...)` (or any other `TenantResolver`) only configures which resolver a trust boundary uses — it does not invoke itself. The transport/runtime entrypoint at each trust boundary MUST invoke the configured resolver and attach the resulting `TenantContext` before domain code runs; `Behavior`/`Saga` implementations MUST NOT know about JWT/HTTP/Ory or any other external identity mechanism — they only read an already-attached `TenantContext` (Ego entrypoints enforce this by rejecting an absent one). The mechanical wiring that lets the GoAkt engine auto-invoke a resolver (`WithTenantResolver` `Option`, extension registration) stays TENANT-006's scope — #001 defines the obligation, TENANT-006 automates it. Until TENANT-006 lands, invoking the resolver and attaching `TenantContext` is the caller's/transport-adapter's manual responsibility; `WithSingleTenant` alone does not deliver "zero-plumbing" |
| R3 | Administrative payload | Required actor identity + required reason, optional correlation ID, no `TenantID` field |
| R4 | Error taxonomy | Three reasons: `Missing`/`Invalid`/`Denied` (subsumes unknown/forbidden via `Unwrap()`); no `Ambiguous` (see R5) |
| R5 | Resolver composition | Exactly one configured resolver; no chaining in #001 — an app needing composition can wrap the interface itself |

## Success Criteria

- [ ] `TenantID`/`TenantContext`/`TenantResolver` live in one GoAkt-free package; import-graph test proves it, not inspection.
- [ ] `TenantContext` cannot be constructed invalid: no empty `TenantID`, no zero-value at a boundary, no unattributed administrative context.
- [ ] Administrative scope is distinguished by type, never by sentinel/empty/nil.
- [ ] `WithSingleTenant` produces a `TenantContext` indistinguishable in kind from a multi-tenant one.
- [ ] The normative invariant is carried into `design.md` verbatim.
- [ ] `sdd-design` specifies an acceptance scenario that fails if a saga loses or changes its tenant identity when crossing a `context.Background()` reset — proving the saga-boundary correction above, not just documenting it.
- [ ] `sdd-design` specifies an acceptance scenario that demonstrates, concretely, who invokes the resolver and attaches `TenantContext` in single-tenant mode — proving R2's invocation-ownership correction above, not just documenting it.
- [ ] `Behavior`/`Saga`/`Engine`/`Config` public signatures are byte-identical before and after.
- [ ] Every R1–R5 is ratified or explicitly overridden before `sdd-design` completes.
