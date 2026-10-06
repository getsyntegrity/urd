# Design — Canonical `TenantContext` / `TenantResolver` (EGO-TENANT-001)

Scope/S1–S6/deferrals: `proposal.md`. Evidence: `exploration.md`. Requirements: `specs/tenancy-core/spec.md`.

**Normative invariant**: *Once an execution has a tenant identity, Ego MUST NOT permit that identity to change implicitly while crossing command, saga, event, persistence, or projection boundaries.*

## Technical Approach

New leaf package `tenancy/`, sibling of `encryption/`, **stdlib only**. SPI copies the `encryption.Encryptor` idiom: `context.Context` first, framework types only, synchronous `(T, error)`. `WithSingleTenant` is a built-in resolver, `ResolveLogger`-shaped. Nothing existing changes — `behavior.go`/`saga.go`/`engine.go`/`option.go` stay byte-identical. The invariant is executable in `Attach`, not a convention.

## Architecture Decisions

### Decision: R1 — Validate, do not normalize (ratified)

**Choice**: `NewTenantID` rejects empty, surrounding space, control runes, invalid UTF-8, >128 bytes; no case-folding, no format mandate.
**Alternatives**: trim+case-fold; UUID-only.
**Rationale**: no identifier here is normalized today (explore §4.2); rewriting would silently merge two spellings into one tenant, with headroom for TENANT-003/005.

### Decision: R2 — Entrypoint invokes, domain code reads (ratified)

**Choice**: the transport/runtime entrypoint calls `Resolve` then `Attach`; `Behavior`/`Saga` only call `From`/`Require`. Configuring a resolver never self-invokes it.
**Alternatives**: behavior-side resolution; engine auto-invocation.
**Rationale**: keeps `behavior.go`/`saga.go` free of identity mechanism (explore §10); `Option` wiring stays TENANT-006's.

### Decision: R3 — Administrative attribution (ratified)

**Choice**: `Administrative{actor, reason}` required, `correlationID` optional, no `TenantID` field, reachable only via `Scope`.
**Alternatives**: sentinel tenant; empty-means-admin.
**Rationale**: both ruled out by the epic (explore §8); a distinct type prevents tenant/admin confusion and gives `EraseEntity`/`Migrator.Run` an auditable scope.

### Decision: R4 — Three reasons (ratified)

**Choice**: `ReasonMissing|ReasonInvalid|ReasonDenied` plus sentinels; resolver-specific unknown/forbidden arrive via `Unwrap()`.
**Alternatives**: adding `Unknown`/`Ambiguous`.
**Rationale**: core can't distinguish resolver-store "unknown" without re-deriving its semantics (explore §9); `Ambiguous` is unreachable per R5.

### Decision: R5 — Exactly one resolver (ratified)

**Choice**: no chaining or priority combinator in #001.
**Alternatives**: chain-of-resolvers, first-match-wins.
**Rationale**: no SPI here composes (explore §6.4); an app needing composition can wrap the interface — a wrong built-in precedence is worse than none.

### Decision: Import-graph tooling — `go list -deps`, stdlib allowlist

**Choice**: `tenancy_architecture_test.go` execs `go list -deps ./tenancy/...`; any dependency whose first path segment contains a dot fails it.
**Alternatives**: `go/packages`; extending the substring scan.
**Rationale**: `go/packages` needs `golang.org/x/tools` vendored just to call `go list` anyway. The allowlist needs no upkeep and catches transitive leaks the substring scan can't.

## Interfaces / Contracts

```go
package tenancy // stdlib only

type TenantID string
func NewTenantID(s string) (TenantID, error)

type Scope uint8 // zero value invalid
const (ScopeTenant Scope = iota + 1; ScopeAdministrative)

type Administrative struct{ /* actor, reason, correlationID: unexported */ }
func NewAdministrative(actor, reason string) (Administrative, error)
func (a Administrative) WithCorrelationID(id string) Administrative

type TenantContext struct{ /* scope, tenant, admin: unexported */ }
func NewTenantContext(id TenantID) (TenantContext, error)
func NewAdministrativeContext(a Administrative) (TenantContext, error)
func (c TenantContext) Scope() Scope
func (c TenantContext) Tenant() (TenantID, bool) // false when administrative
func (c TenantContext) Administrative() (Administrative, bool)

type TenantResolver interface{ Resolve(ctx context.Context) (TenantContext, error) }
func WithSingleTenant(id TenantID) (TenantResolver, error)

// Same-node only (S3); remote is TENANT-002.
func Attach(ctx context.Context, tc TenantContext) (context.Context, error) // ErrDenied on change
func From(ctx context.Context) (TenantContext, bool)
func Require(ctx context.Context) (TenantContext, error) // ErrMissing
func VerifyUnchanged(bound, incoming TenantContext) error // ErrDenied

type Metadata map[string]string // ego.tenant.{scope,id,admin_actor,admin_reason,admin_correlation_id}
func MarshalMetadata(tc TenantContext) Metadata
func UnmarshalMetadata(md Metadata) (TenantContext, error)

type Reason uint8 // Missing | Invalid | Denied
type Error struct{ /* Reason(); Tenant() (TenantID, bool); Unwrap(); Is(error) bool */ }
var ErrMissing, ErrInvalid, ErrDenied error
```

`Attach` is idempotent for the same `TenantContext`, `ErrDenied` otherwise.

## Spec Coverage

| Requirement | Satisfied by |
|---|---|
| TenantID Identity Type | `TenantID` + `NewTenantID` (R1) |
| Resolver Core Independence | stdlib-allowlist conformance test |
| Resolve-Once, Propagate-After | `Resolve`/`Attach` split; metadata round-trip over sagas |
| Tenant + Aggregate Identity | `VerifyUnchanged` → `ErrDenied` |
| Unified Resolver Machinery | `WithSingleTenant` returns a `TenantResolver`; one context type |
| Context Validity + Admin Scope | Unexported fields, constructor-only |
| Identity Immutable Across Boundaries | `Attach` rejects change; `Require` fails closed |

## File Changes

| File | Action |
|---|---|
| `tenancy/{tenant_id,tenant_context,resolver,context,metadata,errors}{,_test}.go` | Create |
| `tenancy_architecture_test.go` (root, pkg `ego`) | Create |
| `Makefile` `docker-mock`, `mocks/tenancy/tenant_resolver.go` | Modify/Create |

## Testing Strategy

| Layer | Approach |
|---|---|
| Unit | Table tests: constructors, R1 rules, `errors.Is/As/Unwrap`, metadata round-trip |
| Acceptance (a) saga | Reset `context.Background()`, reconstruct via `UnmarshalMetadata`+`Attach`, assert same `TenantID`; negative: skip it → `ErrMissing` |
| Acceptance (b) invocation | Entrypoint: `Resolve` then `Attach`; behavior: only `Require`; negative: skip `Attach` → `Require` fails |
| Conformance | `go list -deps ./tenancy/...` |

## Threat Matrix

N/A — no routing/shell/subprocess/VCS-automation/executable-classification/process-integration boundary (conformance test execs the Go toolchain with fixed argv; no product input reaches it).

## Migration / Rollout

None required; additive, zero consumers, rollback per `proposal.md`.

## Open Questions

None — R1–R5 ratified above; deferrals belong to TENANT-002…008.
