# Tenancy Runtime Wiring Specification

## Purpose

Wires the `tenancy/` contract (#45) into the real engine command path: exactly
one resolver, invoked automatically at the command trust boundary, fail-closed
before any domain or persistence effect. Reconciles proposal.md's T1–T6 with
the live text of #55 (AC1–AC7). New capability; `tenancy-core` is unchanged.

**Ratification status**: DP1 (explicit activation) and DP2 (duplicate
registration) were ratified by the repository owner on 2026-09-14 and are now
normative below. See `## Ratified Decisions`. DP3 (proposal.md staleness) is
closed — proposal.md has been amended to the T4-A/T4-B split.

## Requirements

### Requirement: Tenancy Is Explicitly Activated, Never Global (AC6, DP1, T3)

Tenancy MUST NOT become mandatory for every `Engine`. Registering a non-nil
`TenantResolver` — and only that — activates tenant-aware mode.

- An application that never registers a non-nil `TenantResolver` MUST retain
  legacy behavior and MUST start normally.
- Once tenant-aware mode is active, the engine MUST have exactly one effective
  resolver. Zero effective resolvers in tenant-aware mode MUST be a
  startup/configuration error. Outside tenant-aware mode, zero resolvers is
  valid legacy behavior and MUST NOT be an error.
- There MUST be no implicit default resolver and no inferred single tenant.

#### Scenario: Engine without tenancy starts normally

- GIVEN an application that never registers a non-nil `TenantResolver`
- WHEN the engine is constructed and started
- THEN construction succeeds, no resolution occurs, and the command path is
  byte-for-byte the legacy path

#### Scenario: Tenant-aware mode with no effective resolver fails closed

- GIVEN tenant-aware mode is active but no effective resolver remains
- WHEN the engine is constructed
- THEN construction fails with a configuration error and the engine does not
  start

### Requirement: Single Resolver Registration Option (AC1, T3, DP2)

The engine MUST expose exactly one `Option` to register a `TenantResolver`.
Passing `nil` to that `Option` MUST be inert: it registers no resolver, does
not activate tenant-aware mode, raises no error at Option-application time,
and MUST NOT reset a resolver registered by an earlier call. Nil MUST NOT be
usable as a mechanism to silently disable tenancy once a resolver is
configured.

#### Scenario: Nil option is inert

- GIVEN the tenant-resolver `Option` is called with `nil`
- WHEN `Option`s are applied to `Config`
- THEN no resolver is registered, tenant-aware mode is not activated, and no
  error is raised at that call

#### Scenario: Nil after non-nil does not disable tenancy

- GIVEN the tenant-resolver `Option` was called with a valid resolver and then
  called again with `nil`
- WHEN `Option`s are applied
- THEN the previously registered resolver remains the effective resolver and
  tenant-aware mode stays active

#### Scenario: Non-nil resolver registers as effective

- GIVEN the tenant-resolver `Option` is called with a valid `TenantResolver`
- WHEN `Option`s are applied
- THEN that resolver becomes the engine's effective resolver and tenant-aware
  mode is activated

### Requirement: Duplicate Resolver Registration Rejected (AC1, DP2)

`TenantResolver` registration is a security boundary and MUST NOT depend on
`Option` ordering. Two or more non-nil registrations MUST fail engine
construction with a clear configuration error. The engine MUST NOT apply
last-call-wins and MUST NOT silently pick one. No disambiguation mechanism
exists: the only valid tenant-aware configuration is exactly one non-nil
resolver.

#### Scenario: Two resolvers configured

- GIVEN two distinct non-nil resolvers were registered
- WHEN the engine is constructed
- THEN construction fails with a configuration error naming the ambiguity

#### Scenario: Same resolver registered twice

- GIVEN the same non-nil resolver value was registered twice
- WHEN the engine is constructed
- THEN construction fails with the same configuration error — count, not
  value identity, is what is rejected

#### Scenario: Nil registrations do not count

- GIVEN one non-nil registration and any number of `nil` registrations
- WHEN the engine is constructed
- THEN construction succeeds with the single non-nil resolver as effective

#### Scenario: Exactly one resolver configured

- GIVEN exactly one non-nil resolver was registered
- WHEN the engine is constructed
- THEN construction succeeds

### Requirement: Automatic Resolution at the Command Trust Boundary (AC2, T4-A, T5, T2)

The engine MUST invoke the configured resolver's `Resolve` automatically,
exactly once per accepted command, at command-acceptance time — before the
command reaches the domain handler — and attach the result via
`tenancy.Attach` to the context the handler receives. The application MUST
NOT need to call `tenancy.Attach` manually on this path. `Resolve` MUST NOT be
invoked again for that command's persistence step or any retry within the
same acceptance.

#### Scenario: Resolver runs before the handler

- GIVEN an engine configured with a resolver that succeeds
- WHEN a command is sent
- THEN `Resolve` runs once, `TenantContext` is attached, and the handler
  observes it via `tenancy.From`

#### Scenario: Batched command path resolves once too

- GIVEN a command destined for the batched persistence path
- WHEN it is accepted
- THEN resolution happens once at acceptance, not re-derived at flush time

### Requirement: Fail-Closed Before the Domain Handler Runs (AC3, T4-A)

(proposal.md's initial T4 draft scoped the fail-closed gate to persistence
only; this corrected the draft, and proposal.md has since been amended to the
T4-A/T4-B split. The two documents now agree.)

When resolution fails, or `tenancy.Require` finds no valid `TenantContext` in
multi-tenant mode, the command MUST be rejected before the domain handler
executes. The domain handler MUST NOT run, no persistence MUST occur, and no
other side effect MUST occur.

#### Scenario: Missing tenant rejects before the handler

- GIVEN multi-tenant mode with no tenant resolvable for a command
- WHEN the command is sent
- THEN `tenancy.ErrMissing` (or `Require`'s error) is returned, the domain
  handler is never invoked, and zero store writes occur

#### Scenario: Batched path fails closed identically

- GIVEN the same missing-tenant condition on a command bound for the batched
  writer
- WHEN it is accepted
- THEN the rejection happens at acceptance, before entering the batch —
  `flushBatch`'s `context.Background()` reset is never reached for this command

### Requirement: Defensive Persistence Invariant (T4-B)

Independent of the acceptance-time gate above, the persistence/write path
MUST additionally verify a valid tenant identity is present before a
tenant-aware write, using the identity already resolved and propagated from
acceptance. This check MUST NOT re-invoke `TenantResolver.Resolve` and MUST
NOT be the sole enforcement point for fail-closed behavior.

#### Scenario: Persistence stage double-checks without re-resolving

- GIVEN a command already passed the acceptance-time gate
- WHEN the write path executes
- THEN it confirms the propagated `TenantContext` is present without calling
  `Resolve` again

### Requirement: Fail-Closed Gates Validate TenantContext Content, Not Just Presence (AC3, T4-A, T4-B, DP4)

Both the acceptance-time gate (T4-A) and the persistence-time gate (T4-B) rely
on `tenancy.Require` to decide whether a valid tenant identity is attached.
"Present" MUST mean well-formed, not merely non-absent: a `TenantContext`
whose `Scope()` is neither `ScopeTenant` nor `ScopeAdministrative` — most
notably the zero value `tenancy.TenantContext{}`, which a misbehaving or
buggy `TenantResolver.Resolve` implementation can return alongside a `nil`
error — MUST be rejected exactly as if no `TenantContext` were attached at
all. `tenancy.Attach` MUST also refuse to bind such a value in the first
place. Neither gate MUST treat a resolver's `TenantContext{}, nil` return as
a valid identity.

#### Scenario: A resolver returning the zero-value TenantContext is rejected before dispatch

- GIVEN an engine configured with a `TenantResolver` whose `Resolve` returns
  `tenancy.TenantContext{}, nil` (no error, but an invalid, zero-value
  identity)
- WHEN a command is sent
- THEN the command is rejected before the domain handler runs, the domain
  handler is invoked zero times, and no event or state is persisted

#### Scenario: Attach never binds an invalid TenantContext

- GIVEN code attempts `tenancy.Attach(ctx, tenancy.TenantContext{})`
- WHEN `Attach` is called
- THEN it returns an error and the returned context carries no tenant
  identity — a subsequent `tenancy.From` on it returns `ok == false`

#### Scenario: Require rejects an invalid TenantContext even if already bound

- GIVEN a context that somehow already carries a zero-value `TenantContext`
  (bypassing `Attach`)
- WHEN `tenancy.Require` reads it
- THEN it returns an error rather than the invalid value

### Requirement: Zero-Plumbing Single-Tenant Mode (AC4)

Configuring `tenancy.WithSingleTenant(id)` as the sole resolver MUST activate
single-tenant behavior with no additional application-level tenancy plumbing.

#### Scenario: Single-tenant app needs no manual wiring

- GIVEN an application configured only with `WithSingleTenant(id)`
- WHEN it sends commands
- THEN tenant context is attached automatically; the application never calls
  `tenancy.Attach` or `tenancy.Require` itself

### Requirement: Unified Execution Path for Single and Multi-Tenant (AC5, T6)

Single-tenant and multi-tenant configurations MUST invoke resolution through
the identical code path. The engine MUST NOT branch on "is this
single-tenant"; `WithSingleTenant` is only a `TenantResolver` implementation.

#### Scenario: Swapping resolvers changes only which one runs

- GIVEN one engine using `WithSingleTenant(id)` and another using a
  multi-tenant resolver
- WHEN each sends a command
- THEN both traverse the same resolve-attach-gate sequence

### Requirement: No Implicit Tenant Inference Outside Explicit Single-Tenant (AC7)

Outside of an explicitly configured single-tenant resolver, the engine MUST
NOT infer, assume, or default a tenant identity for any command.

#### Scenario: No resolver, no inference

- GIVEN a command reaches an engine with a configured resolver that returns
  no tenant
- WHEN it is evaluated
- THEN no tenant is assumed; the command is rejected per the fail-closed
  requirement above, never silently defaulted

### Requirement: No Implicit Default at Startup (AC6, DP1 — ratified)

The engine MUST NOT start tenant-aware mode with an implicit tenant default.
Scope is settled by DP1: this applies once tenant-aware mode is active, not to
every `Engine`. The normative form is stated in "Tenancy Is Explicitly
Activated, Never Global" above; this requirement adds the startup-time
consequence only.

#### Scenario: Ambiguous configuration always fails startup

- GIVEN more than one non-nil resolver was registered
- WHEN the engine is constructed
- THEN construction fails with a clear configuration error

#### Scenario: Legacy engine is not a startup failure

- GIVEN tenant-aware mode was never activated
- WHEN the engine is constructed
- THEN construction succeeds and no tenant default is assumed anywhere

## Ratified Decisions

Ratified by the repository owner on 2026-09-14. These close the previously
open decision points; the requirements above are the normative form.

### DP1 — Explicit activation (CLOSED: Option 2)

Tenancy is not globally mandatory. Registering a non-nil `TenantResolver`
activates tenant-aware mode; never registering one keeps legacy behavior.
`WithTenantResolver(nil)` is inert and never resets a prior resolver. Once
active: exactly one effective resolver, no implicit default, no inferred
single tenant, resolver failure blocks the whole command before the handler
(T4-A), and persistence keeps T4-B as a defensive invariant.

**T3 corrected.** The earlier wording — "zero effective resolvers after all
Options = startup error" — was wrong because it read as global. Correct
wording: *in tenant-aware mode, zero effective resolvers is a
startup/configuration error; outside tenant-aware mode (a non-nil resolver was
never registered) it is valid legacy behavior, not an error.*

### DP2 — Duplicate registration (CLOSED: configuration error, not last-call-wins)

Other single-value `Option`s in `option.go` (`WithLogger`, `WithTelemetry`)
are last-call-wins. `TenantResolver` deliberately is not: it defines a
security boundary and MUST NOT silently depend on `Option` ordering.

| Non-nil registrations | Outcome |
|---|---|
| 0, tenancy never activated | Legacy behavior, valid |
| exactly 1 | Valid, tenant-aware |
| 2 or more | Startup/configuration error |

`WithTenantResolver(nil)` is not a registration and does not reset a prior
one. No disambiguation mechanism is introduced — #55 does not need one.

### DP3 — proposal.md staleness (CLOSED)

proposal.md has been amended to the T4-A/T4-B split (trust-boundary gate
before the handler; defensive persistence invariant reusing already-resolved
identity, never re-invoking `Resolve`). Both documents now agree.

### DP4 — TenantContext content validation at Attach/Require (CLOSED: validate in both, review reconciliation)

Closed by the repository owner during adversarial review of PR2 (#57)/PR3
(#58), after the review found T4-A/T4-B's `tenancy.Require` calls accepted a
zero-value `TenantContext` returned by a misbehaving resolver (`Resolve`
returning `tenancy.TenantContext{}, nil`) as if it were a real identity. See
design.md Decision D8 for the full option analysis and the reasoning for
recording this in this change's spec rather than reopening the archived
`ego-tenant-context` (EGO-TENANT-001) change.

Both `tenancy.Attach` (reject before binding) and `tenancy.Require` (reject
on read, in case something else bound an invalid value directly) now
validate `Scope()` is `ScopeTenant` or `ScopeAdministrative`. This closes the
gap at every existing T4-A/T4-B call site with no changes required to
`engine.go`, `event_sourced_actor.go`, or `durable_state_actor.go`'s gate
call sites themselves — see "Fail-Closed Gates Validate TenantContext
Content, Not Just Presence" above.

## Action Required Outside This Spec

**Issue #55 AC6 needs a wording reconciliation on GitHub before the SDD cycle
closes.** Its literal current text — "sin resolver configurado, el engine
falla en startup" — is stronger than the ratified intent and, read literally,
breaks backward compatibility for every existing `Engine` caller. The
ratified, owner-approved intent is:

> "Once tenant-aware mode is enabled, the engine MUST have exactly one valid
> `TenantResolver` and MUST fail closed if that configuration is absent or
> ambiguous. Engines that do not enable tenancy preserve legacy behavior."

This spec is authoritative for implementation. Updating the issue text is a
tracker-hygiene action owned by the repository owner, not by any SDD phase.

## Traceability

| AC (#55) | Requirement |
|---|---|
| 1 | Single Resolver Registration Option; Duplicate Resolver Registration Rejected |
| 2 | Automatic Resolution at Trust Boundary |
| 3 | Fail-Closed Before Domain Handler Runs; Defensive Persistence Invariant; Fail-Closed Gates Validate TenantContext Content, Not Just Presence (DP4) |
| 4 | Zero-Plumbing Single-Tenant Mode |
| 5 | Unified Execution Path |
| 6 | Tenancy Is Explicitly Activated, Never Global; No Implicit Default at Startup (DP1 closed — issue wording reconciliation pending, see "Action Required Outside This Spec") |
| 7 | No Implicit Tenant Inference |
