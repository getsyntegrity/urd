# Proposal — Runtime tenant wiring and zero-plumbing single-tenant mode (EGO-TENANT-006)

| Field | Value |
|---|---|
| Change | `ego-tenant-runtime-wiring` |
| Date | 2026-09-14 |
| Phase | `sdd-propose` |
| Status | proposed |
| Tracker | [`getsyntegrity/ego#55`](https://github.com/getsyntegrity/ego/issues/55) (EGO-TENANT-006) |
| Epic | [`getsyntegrity/ego#23`](https://github.com/getsyntegrity/ego/issues/23) |
| Depends on | [`#45`](https://github.com/getsyntegrity/ego/issues/45) (EGO-TENANT-001, archived — `tenancy/` shipped) |
| Evidence base | Engram `sdd/ego-tenant-runtime-wiring/explore` |

Documentation only. No Go code in this artifact. Issue acceptance criteria were taken from the exploration (this executor has no shell to run `gh issue view 55`); re-check against the issue before `sdd-spec` closes.

## Problem

`tenancy/` shipped in #45, but nothing in ego ever calls `Resolve` (explore §Current State). The resolver's own docstring assigns resolution to "the runtime entrypoint" — that entrypoint does not exist, so every application must attach `TenantContext` by hand, and nothing stops a command from persisting with no tenant at all.

**Success**: an application opts in with one `Option`, and tenant-less writes become impossible on the wired command paths.

## Decisions (closed here)

| # | Decision | Rationale (explore ref) |
|---|---|---|
| T1 | New `Option` `WithTenantResolver(tenancy.TenantResolver)`; a non-nil resolver conditionally registers a tenancy marker extension | Exactly the existing 8-extension conditional pattern in `GoaktOptions()` — zero new architecture |
| T2 | Resolve + Attach happen exactly once, in `Engine.SendCommand` — the only public external command entrypoint | Recommendation B.1; actors never see a `TenantResolver`, only `Require` |
| T3 (was OD1, **ratified DP1/DP2**) | Registering a non-nil `TenantResolver` activates tenant-aware mode; never registering one keeps legacy behavior. `WithTenantResolver(nil)` is inert and never resets a prior resolver. In tenant-aware mode: exactly one effective resolver — zero is a startup error, two or more is a startup error (no last-call-wins). Outside tenant-aware mode, zero resolvers is valid legacy behavior, not an error | Backward compatibility for every existing `Engine` caller; `TenantResolver` is a security boundary and must not depend on `Option` ordering the way `WithLogger`/`WithTelemetry` do |
| T4-A (was OD2, **ratified**) | The trust-boundary gate blocks the **entire command**, including `behavior.HandleCommand`: in tenant-aware mode a resolution failure or a missing `TenantContext` rejects the command before any domain logic runs, with zero side effects | A tenant-less command must not execute domain logic at all; "persistence-only" would let handlers run untenanted and emit side effects |
| T4-B (**ratified**) | Persistence additionally re-checks that a valid tenant identity is present before a tenant-aware write, **reusing the identity already resolved and propagated from acceptance**. It never re-invokes `Resolve` and is never the sole enforcement point | Defense in depth behind T4-A; keeps a second, independently reachable write path from persisting untenanted |
| T5 | Both gates are evaluated synchronously at command-acceptance time against that command's own context, never at the physical write call site | `flushBatch` merges many commands into one `context.Background()` Ask (`event_sourced_actor.go:859`), so the write site cannot attribute a single command |
| T6 | Single-tenant is `tenancy.WithSingleTenant(id)` over the same execution path as multi-tenant — one path, only the resolver differs | Ratified as S5 in #45; no second code path exists in `tenancy/` |

**Backward compatibility**: no resolver ⇒ no marker extension ⇒ no `Resolve`/`Attach`/`Require`, and the forwarded `ctx` is identical to today's. Tenancy is opt-in at the moment `WithTenantResolver` receives a non-nil value.

**Issue hygiene**: #55's AC6 wording ("sin resolver configurado, el engine falla en startup") is stronger than the ratified intent and must be reconciled on GitHub before this cycle closes — see the spec's "Action Required Outside This Spec".

## Scope

**In**: `WithTenantResolver`; duplicate-registration rejection at `NewEngine`; tenancy marker extension plus its `validateActorSystemExtensions` check; resolve+attach in `Engine.SendCommand`; the T4-A pre-handler gate and the T4-B pre-write invariant in `EventSourcedActor` (batched and non-batched) and `DurableStateActor`; a zero-plumbing single-tenant demonstration.

**Out**:

| Deferred | Owner |
|---|---|
| Saga command-dispatch bypass and `saga_actor.go`'s five `context.Background()` resets | #54 (EGO-TENANT-002) |
| Canonical command/event/query envelopes | WRITE-003 (no issue yet) |
| Tenant + aggregate effective identity | TENANT-002/003 |
| EventStore namespacing, ReadSide/projection and Topic isolation | TENANT-003/004/005 |
| `EraseEntity` / `Migrator.Run` tenancy semantics | TENANT-008 |

## Capabilities

- **New**: `tenancy-runtime` — engine-side resolver configuration, resolve-once-at-entrypoint, fail-closed persistence gate, opt-in back-compat.
- **Modified**: None. `tenancy-core` is consumed unchanged; `tenancy/` is not touched by this change.

## Affected Areas

| Area | Impact | Change |
|---|---|---|
| `option.go` | Modified | `WithTenantResolver`, `Config` field, conditional extension |
| `engine.go` | Modified | `tenantResolver` field, resolve+attach in `SendCommand`, extension validation |
| `internal/extensions/extensions.go` | New | Tenancy marker extension |
| `event_sourced_actor.go`, `durable_state_actor.go` | Modified | Read marker; T4-A gate before `HandleCommand`; T4-B invariant before the write |
| `saga_actor.go`, `tenancy/` | Unchanged | Out of scope (see above) |

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Saga-issued commands never pass `SendCommand`, so a tenant-aware app with sagas fails closed (`ErrMissing`) until #54 lands | High | Accepted known limitation, documented on the option; inert unless a resolver is configured |
| Typed-nil resolver silently yields legacy fail-open (T3) | Medium | Option doc comment must state it; typed-nil is detected the way `isNilLogger` already does and treated as an inert nil, so it never activates tenant-aware mode |
| Batched path regresses silently (`flushBatch` context loss) | Medium | T5, plus a dedicated batched-path missing-tenant test asserting zero store writes |
| T4-A rejects commands that were previously accepted untenanted | Low | Only reachable in tenant-aware mode, which is opt-in; legacy engines never evaluate either gate |

**Rollback**: purely additive and opt-in. Revert = remove the option, the extension, and the gate call sites; applications that never configured a resolver observed no change at any point.

## Human gate

Per `spec-governance` §10 this change is both public API and security-relevant. **Ratified by the repository owner on 2026-09-14**: T3 (DP1 explicit activation + DP2 duplicate-registration error) and the T4-A/T4-B split above. Remaining tracker action, owned by the repository owner and not by any SDD phase: reconcile #55's AC6 wording on GitHub before this cycle closes.

## Success Criteria

- [ ] The resolver is invoked exactly once per `Engine.SendCommand` call, and never again for that command's persistence step or retries within the same acceptance.
- [ ] The resolved `TenantContext` is observable inside `HandleCommand` via `tenancy.From`, with no behavior-interface change.
- [ ] T4-A: tenant-aware mode with no attached `TenantContext` ⇒ `tenancy.ErrMissing`, `HandleCommand` never invoked, and zero store writes — proven separately for the batched and non-batched paths.
- [ ] T4-B: the write path confirms the propagated identity without calling `Resolve` again, proven by a resolver mock asserting exactly one call across acceptance and persistence.
- [ ] Two or more non-nil `WithTenantResolver` registrations fail `NewEngine` with a configuration error; `WithTenantResolver(nil)` after a valid one leaves the valid one effective.
- [ ] The existing engine test suite passes unmodified when no resolver is configured (legacy path has no new checks).
- [ ] The single-tenant demonstration wires nothing beyond one `Option`; no behavior implementation imports `tenancy`.
- [ ] `design.md` records the saga bypass as a known limitation referencing #54, and defers all exact signatures/extension shapes it names.
