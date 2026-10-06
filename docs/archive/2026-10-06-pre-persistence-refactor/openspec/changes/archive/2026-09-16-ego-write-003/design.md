# Design — Canonical command/result envelopes and metadata (EGO-WRITE-003)

Tracker [`#59`](https://github.com/getsyntegrity/ego/issues/59) · scope/W1–W7: `proposal.md` · evidence: Engram `sdd/ego-write-003/explore`.

**Normative invariant**: *A command's logical identity (operation, correlation, causation), its tenant slot, principal and governed custom metadata are carried as a value on the envelope. `context.Context` may mirror them for execution, but is never their canonical home* (W2).

## Technical Approach

New leaf package `command/`, sibling of `tenancy/`. Every type is a closed value — unexported fields, constructor-only, copy-returning derivation — the shape `tenancy.TenantContext`/`Administrative` already use, so an invalid or tampered envelope is *unrepresentable*, not merely discouraged. Import allowlist: stdlib + `google.golang.org/protobuf` + `ego/v4/tenancy`; banned: goakt, otel, `egopb`, root `ego`, transport/auth. Nothing existing changes — `engine.go`, `saga*.go`, `behavior.go`, `protos/`, `egopb/` stay byte-identical (W1).

## Architecture Decisions

### D1 — Package `command/`, not `envelope/`

| Option | Tradeoff | Decision |
|---|---|---|
| `command/` | domain-noun leaf matching `tenancy`/`encryption`/`projection`; `command.Envelope`/`.Result`/`.Metadata` read without stutter | **Chosen** |
| `envelope/` | collides with the *event* envelope already in use (`buildEnvelopes`, `egopb` event envelopes) | Rejected |
| types on root `ego` | root owns goakt, so a `tenancy`-style import-allowlist conformance test becomes impossible and #54/WRITE-005 inherit the whole runtime | Rejected |

Root may later alias (`type CommandEnvelope = command.Envelope`), exactly as `Command = proto.Message` already does (behavior.go:34). `command/` never imports root, so no cycle.

### D2 — Non-generic envelope, generic accessor

| Option | Tradeoff | Decision |
|---|---|---|
| `payload proto.Message` + free `PayloadAs[T proto.Message](Envelope) (T, bool)` | heterogeneous collections work (compensation lists, `[]SagaCommand`, `[]Envelope`); typed extraction stays compile-checked where it matters | **Chosen** |
| `Envelope[T proto.Message]` | cannot be held in one slice — fatal for `SagaBehavior.Compensate`'s `[]SagaCommand`; and `HandleCommand(ctx, Command, State)` type-switches anyway, erasing the parameter at the boundary that consumes it | Rejected |

### D3 — Functional options over a private builder, one `(T, error)`

`WithX`-chaining (tenancy's `Administrative.WithCorrelationID`) cannot return an error, so a reserved-key rejection would have to be silently coerced — which defeats W6. Options carrying errors into a single `NewMetadata` return keeps validation total and centralized, and matches `NewEngine(..., opts ...Option)`. Canonical fields are set once, at construction, and are unreachable afterwards.

### D4 — Naming collision with `tenancy.Administrative.CorrelationID()`

**Keep `CorrelationID` as the command-flow name; disambiguate by type, package and reserved namespace. Do not touch `tenancy`.**

| Option | Tradeoff | Decision |
|---|---|---|
| `command.CorrelationID` (defined type) alongside the existing accessor | industry-standard and #59's own vocabulary; the two are structurally unconfusable — see mechanism below | **Chosen** |
| Rename the new concept (`FlowID`, `ConversationID`) | non-standard vocabulary diverges from #59 and the proposal's own contract naming, taxing every consumer to protect against a confusion that is already a compile error | Rejected |
| Rename `tenancy` → `AdminCorrelationID()` | breaks a shipped public API of an archived change for a naming preference; `config.yaml` requires a rollback plan for any public-API change, and there is nothing to roll back to | Rejected |

Disambiguation is mechanical, not documentary:

1. **Type**: `command.CorrelationID` is a defined type; tenancy's is a `(string, bool)` accessor returning bare `string`. `md.WithCorrelationID(admin.CorrelationID())` does not compile — accidental cross-wiring is a build failure.
2. **Reachability**: tenancy's is reachable only via `TenantContext.Administrative()`, i.e. only in `ScopeAdministrative`; command correlation exists on every envelope.
3. **Namespace**: `ego.cmd.correlation_id` vs. the already-qualified `ego.tenant.admin_correlation_id` (tenancy/metadata.go:36) — the carry format never collided.
4. **Doc**: `command.CorrelationID`'s doc comment states the distinction and that mapping one onto the other, if ever wanted, is the caller's explicit one-way choice (admin correlation = attribution for an administrative action; command correlation = write-flow identity).

### D5 — Six outcomes as a `uint8` with a zero-invalid enum and six constructors

Mirrors `tenancy.Reason` (tenancy/errors.go:30-44) exactly. Kind-specific constructors make the kinds *structurally* distinguishable: `NewSuccess` requires a non-nil state, `NewSuccessNoState` forbids one, the four failure constructors require a `Failure`. The success pair maps 1:1 onto today's `SendCommand` returns — `(state, revision, nil)` and `(nil, 0, nil)` (engine.go:735) — which is what makes the Stage-1 adapter below lossless.

`Result.Err()` returns `*command.Error`, classifiable with `errors.Is` against `ErrRejected`/`ErrFailed`/`ErrTimedOut`/`ErrCanceled` and traversable via `Unwrap`. `NewTimedOut`/`NewCanceled` default a nil cause to `context.DeadlineExceeded`/`context.Canceled`, so stdlib classification works unchanged. **This is the W5 extension point**: the taxonomy is Go-side only. `egopb.ErrorReply{message}` (protos/ego/ego.proto:57-62) stays untouched; the documented lossy boundary is that a reply arriving over the wire carries no discriminator and therefore maps to `OutcomeFailed` until a wire-side discriminator exists (follow-up territory).

### D6 — Custom-metadata governance (W6)

`map[string]string`, never `map[string]any`; accessors return defensive copies. `WithCustom` rejects, with `ErrReservedKey`: any key prefixed `ego.` (blanket framework reservation, covering `ego.tenant.*`, `ego.cmd.*` and WRITE-005's future `ego.idem.*`), any exact canonical key, and any key failing `NewTenantID`'s rules reused verbatim (non-empty, valid UTF-8, ≤128 bytes, no surrounding whitespace, no control runes). Values: valid UTF-8, ≤1024 bytes, no control runes.

### D7 — Derivation composes `tenancy`, never re-implements it

`Derive(op)` yields: `correlation` inherited, `operation` = `op` (must differ from the parent → `ErrSameOperationID`), `causation` = `CausationID(parent.operation)`, fresh `timestamp`. Tenant is inherited and verified through the existing `tenancy.VerifyUnchanged` — an attempt to switch tenants in a derived command fails with `tenancy.ErrDenied`, reusing tenancy's own invariant rather than restating it (W3). Deadline is inherited and may only be **shortened** (`ErrDeadlineExtension`), matching `context.WithDeadline`. Principal is inherited unless overridden. Custom metadata is **not** inherited — it is per-operation and would otherwise grow unbounded across a saga chain.

### D8 — `operation_id` ≠ `op_key` (W4), stated as a mechanism

This contract assigns **no retry semantics** to `OperationID` — it identifies one dispatched command instance. An idempotency key identifies a logical *intent* stable across retries; its stability rules are WRITE-005's to define. The `ego.idem.` prefix is reserved for it now (by D6's blanket `ego.` rule), so WRITE-005 can land either as a governed namespaced key or as a new canonical slot without this change pre-deciding. A contract test asserts `Derive` changes `OperationID`, proving the two cannot be the same value.

### D9 — `Carrier` flat-map carry format — **APPROVED by repository owner (2026-09-14)**

`type Carrier map[string]string` + `MarshalMetadata`/`UnmarshalMetadata`, mirroring `tenancy.Metadata` (tenancy/metadata.go:41-102). This is a Go-side carry format, not a wire format — no proto, no `egopb`, so W5/Invariant 8 hold.

**Owner's ruling**: this is no longer speculative convenience — M-3 is a concrete, demonstrated constraint. `goakt.SendSync` accepts only `proto.Message`; a Go `command.Envelope` cannot cross it as-is. If WRITE-003 claims causal identity can survive sagas/retries/persistence (W2), it must define a portable representation of its own metadata, or W2 is an unproven claim. `Carrier` is that representation.

**Hard boundary the owner set, to keep `Carrier` from becoming a covert proto/transport format:**

- `Carrier` carries **canonical metadata only** — `operation_id`, `correlation_id`, `causation_id`, tenant, principal, custom, temporal. It MUST NOT carry the command payload. (`Envelope.Payload()` has no `Carrier` path; only `Metadata` does.)
- `Carrier` MUST NOT decide *how* metadata crosses goakt, protobuf, persistence, saga, or retry boundaries. That integration choice (M-3's options a/b/c) is explicitly follow-up territory, not decided here.
- WRITE-003's obligation is limited to defining `Marshal`/`Unmarshal` and proving the round-trip — see the expanded Testing Strategy row below.

## Data Flow

    Consumer builds ──→ command.NewEnvelope(payload, md) ──→ (WRITE-003 stops here)
         │                       │
         │                       ├─ md.Derive(newOp) ──→ child: same correlation,
         │                       │                       causation = parent operation
         │                       └─ MarshalMetadata ──→ Carrier ──→ Unmarshal ──→ Metadata
         └──────────────── command.Result ←── NewSuccess | NewSuccessNoState
                                          ←── NewRejected | NewFailed | NewTimedOut | NewCanceled

## Interfaces / Contracts

```go
package command // stdlib + google.golang.org/protobuf + ego/v4/tenancy only

type OperationID string; type CorrelationID string; type CausationID string // defined types:
func NewOperationID(s string) (OperationID, error)   // tenancy.NewTenantID's rules, reused
func GenerateOperationID() (OperationID, error)      // crypto/rand 128-bit hex — stdlib, no uuid dep

type Principal struct{ /* id, kind — no tokens, roles, scopes or credentials (AC14) */ }
func NewPrincipal(id string, opts ...PrincipalOption) (Principal, error)
func WithPrincipalKind(kind string) PrincipalOption
func (p Principal) ID() string
func (p Principal) Kind() (string, bool)

type Metadata struct{ /* all fields unexported */ }
func NewMetadata(op OperationID, opts ...MetadataOption) (Metadata, error)
func WithCorrelationID(id CorrelationID) MetadataOption // default at root: CorrelationID(op)
func WithTenant(tc tenancy.TenantContext) MetadataOption
func WithPrincipal(p Principal) MetadataOption
func WithCustom(key, value string) MetadataOption       // ErrReservedKey per D6
func WithTimestamp(t time.Time) MetadataOption          // default time.Now().UTC()
func WithDeadline(t time.Time) MetadataOption
func (m Metadata) OperationID() OperationID
func (m Metadata) CorrelationID() CorrelationID
func (m Metadata) CausationID() (CausationID, bool)     // false at the root
func (m Metadata) Tenant() (tenancy.TenantContext, bool)
func (m Metadata) Principal() (Principal, bool)
func (m Metadata) Custom() map[string]string            // defensive copy
func (m Metadata) CustomValue(key string) (string, bool)
func (m Metadata) Timestamp() time.Time
func (m Metadata) Deadline() (time.Time, bool)
func (m Metadata) Derive(op OperationID, opts ...MetadataOption) (Metadata, error) // D7

type Envelope struct{ /* payload, metadata */ }
func NewEnvelope(payload proto.Message, md Metadata) (Envelope, error) // payload MUST be non-nil
func (e Envelope) Payload() proto.Message
func (e Envelope) Metadata() Metadata
func (e Envelope) Derive(payload proto.Message, op OperationID, opts ...MetadataOption) (Envelope, error)
func PayloadAs[T proto.Message](e Envelope) (T, bool)

type Outcome uint8 // zero value invalid; String()
const (_ Outcome = iota; OutcomeSuccess; OutcomeSuccessNoState; OutcomeRejected
       OutcomeFailed; OutcomeTimedOut; OutcomeCanceled)

type Failure struct{ /* code, message, cause */ }
func NewFailure(message string, opts ...FailureOption) (Failure, error)
func WithFailureCode(code string) FailureOption; func WithFailureCause(err error) FailureOption

type Result struct{ /* metadata, outcome, state, revision, failure */ }
func NewSuccess(md Metadata, state proto.Message, revision uint64) (Result, error)
func NewSuccessNoState(md Metadata) (Result, error)
func NewRejected(md Metadata, f Failure) (Result, error)
func NewFailed(md Metadata, f Failure) (Result, error)
func NewTimedOut(md Metadata, f Failure) (Result, error) // nil cause ⇒ context.DeadlineExceeded
func NewCanceled(md Metadata, f Failure) (Result, error) // nil cause ⇒ context.Canceled
func (r Result) Outcome() Outcome; func (r Result) Metadata() Metadata
func (r Result) State() (proto.Message, bool); func (r Result) Revision() uint64
func (r Result) Failure() (Failure, bool)
func (r Result) Err() error // nil for both success kinds; *Error otherwise
func StateAs[T proto.Message](r Result) (T, bool)

type Error struct{ /* Outcome(); Failure(); Unwrap(); Is(error) bool */ }
var ErrRejected, ErrFailed, ErrTimedOut, ErrCanceled error            // outcome classification
var ErrInvalidMetadata, ErrInvalidEnvelope, ErrInvalidResult,          // construction/validation
    ErrReservedKey, ErrSameOperationID, ErrDeadlineExtension error

type Carrier map[string]string // ego.cmd.* ∪ tenancy's ego.tenant.* — D9
func MarshalMetadata(m Metadata) Carrier
func UnmarshalMetadata(c Carrier) (Metadata, error)
```

Canonical carrier keys: `ego.cmd.{operation_id,correlation_id,causation_id,timestamp,deadline,principal_id,principal_kind}`. The tenant slot round-trips through `tenancy.MarshalMetadata`/`UnmarshalMetadata` unchanged — no duplicate tenant serialization exists (W3).

## File Changes

| File | Action | Description |
|---|---|---|
| `command/{identity,principal,metadata,envelope,result,errors,carrier}.go` + `_test.go` siblings | Create | The whole contract and its tests |
| `command_architecture_test.go` (root, pkg `ego`) | Create | `go list -deps ./command/...` allowlist, mirroring `tenancy_architecture_test.go` |
| `engine.go`, `saga.go`, `saga_actor.go`, `behavior.go`, `option.go`, `protos/`, `egopb/`, `tenancy/`, `Makefile` | Unchanged | Follow-up territory / consumed as-is / no new SPI to mock |

## Testing Strategy

| Layer | What | How |
|---|---|---|
| Unit | Constructors, D6 key/value rules, defensive-copy of `Custom`, `Outcome.String()`, `errors.Is/As/Unwrap` on `*Error` | Table tests, `tenancy/*_test.go` style |
| Contract (AC13) | Root (no causation) · derived (correlation persists, operation changes, causation = parent) · tenant slot · principal · custom · deadline · all six outcomes mutually distinguishable | Table tests over `Derive` and the six constructors |
| Contract (negative) | Reserved/canonical key rejected · tenant switch in `Derive` ⇒ `tenancy.ErrDenied` · deadline extension rejected · same operation ID rejected · nil payload/state rejected | `require.ErrorIs` |
| Contract (W2/D9) | `Metadata → Carrier → Metadata` round-trip: absence/presence of optional fields (causation, tenant, principal, deadline), reserved-key and invalid-value rejection on `Unmarshal`, unknown-key forward compatibility (ignored, not rejected), and — specifically — exact preservation of `operation_id`, `correlation_id` and `causation_id` | Round-trip test |
| Conformance | `go list -deps ./command/...`: stdlib + `google.golang.org/protobuf/*` + `ego/v4/tenancy` only | Real subprocess, per TENANT-001 precedent |

## Threat Matrix

N/A — no routing, shell, subprocess, VCS/PR automation, executable-file classification, or process-integration boundary. The conformance test execs the Go toolchain with fixed argv and no product input, identically to `tenancy_architecture_test.go` (recorded N/A there for the same reason).

## Migration / Rollout (W7 — written deliverable, not executed)

**This change requires no migration**: purely additive, zero consumers, rollback per `proposal.md`. Everything below inventories and classifies what the *follow-up* will face, so the contract is validated as adoptable before it is frozen.

### M-1 Inventory — affected APIs and dispatch paths

| Surface | Site | Call sites |
|---|---|---|
| `Engine.SendCommand` (definition) | `engine.go:750` | 1 |
| `SendCommand` callers, tests | `engine_test.go` (29), `benchmark/benchmark_test.go` (20), `publisher_test.go` (9) | 58 |
| `SendCommand` callers, examples | `example/{saga(5),cluster(2),eventssourced(2),durablestate(2)}/main.go` | 11 |
| `SendCommand` callers, docs | `readme.md` | 1 |
| Reply parsing | `parseCommandReply` `engine.go:1117-1141` — collapses every failure to `errors.New(message)` | 2 internal |
| Saga bypass (dispatch) | `SagaActor.sendCommand` `saga_actor.go:329-372` — `NoSender().SendSync(context.Background(), …)` | 1 |
| Saga bypass (compensation) | `SagaActor.compensate` `saga_actor.go:375-399` — same, reply discarded | 1 |
| Saga command struct | `SagaCommand{EntityID, Command, Timeout}` `saga.go:77-84` — no identity fields | — |
| Behavior entry | `EventSourcedBehavior.HandleCommand` / `DurableStateBehavior.HandleCommand` `behavior.go:61,91` | every consumer |
| Actor-side dispatch | `event_sourced_actor.go` `processCommandAndReply`/`processAndBatch`; `durable_state_actor.go` `processCommand` | 3 |
| Reply construction | `sendErrorReply`, `SagaActor.replyWithState` `saga_actor.go:402` | 2 |

**Total in-repo `SendCommand` call sites: 69** (58 test, 11 example) plus unbounded external consumers — `github.com/pablogore/ego/v4` is a published module, so the real blast radius is not enumerable from this repo.

### M-2 Breaking-change classification

| Class | What | Who breaks |
|---|---|---|
| **Additive / non-breaking** | Everything in WRITE-003: new package, no existing symbol touched | Nobody |
| **Additive / non-breaking** | A second engine entry point taking an `Envelope`, alongside `SendCommand` | Nobody |
| **Source-breaking** | Changing `SendCommand`'s signature or return to speak envelopes | 69 in-repo + every external caller |
| **Source-breaking** | Widening `EventSourcedBehavior`/`DurableStateBehavior.HandleCommand` — every consumer implements these | Every consumer. **Largest single break; must be avoided via M-4 Stage 4** |
| **Source-breaking (narrow)** | Adding a `Metadata` field to `SagaCommand` — safe for keyed literals, breaks positional ones | Consumers using positional struct literals |
| **Behavioral** | `parseCommandReply` returning a `Result` instead of `(State, uint64, error)` — internal only | Nobody, if the projection in Stage 1 is kept |
| **Wire / persistence** | None now. See M-3 | — |

### M-3 Blocking finding for the follow-up

`goakt`'s `SendSync(ctx, name string, msg proto.Message, timeout)` (engine.go:802, saga_actor.go:337/391) accepts **only** a `proto.Message`. `command.Envelope` is a Go struct, so it **cannot cross the actor boundary as-is**. The follow-up must pick one of: (a) a new proto envelope message wrapping `Any` + a metadata map — a wire change, explicitly out of scope here per W5; (b) goakt-level message headers, if available in v4.5.4; (c) keep the envelope engine-side and re-materialize it actor-side from a `Carrier` — no wire change, and the reason D9's carry format is not optional if (a) is to be avoided. Additionally, `SagaActor` uses `context.Background()` at every dispatch site, so **no** ctx-carried mechanism can reach a saga-issued command — this is the same gap TENANT-006 accepted and #54 owns, and it is the concrete proof of W2 (a ctx-only contract is not a contract).

### M-4 Incremental adoption sequence (no big-bang)

| Stage | Work | Breaking? |
|---|---|---|
| **0 — this change** | Land `command/`, zero consumers | No |
| **1** | Add `Engine.Dispatch(ctx, entityID string, env command.Envelope) (command.Result, error)`. `SendCommand` becomes a thin adapter over it: mints a root `Metadata`, projects `Result → (State, uint64, error)` via the D5 1:1 mapping. One implementation, two faces. `Metadata.Deadline` subsumes the `timeout time.Duration` parameter | No |
| **2** | Actor-side `Carrier` propagation so metadata survives the goakt hop (M-3); `parseCommandReply` yields a `Result` internally | No (internal) |
| **3** | `SagaCommand` gains a `Metadata` field; `sendCommand`/`compensate` route through `Dispatch` and `Derive` child metadata from the saga's triggering operation. Closes the bypass and makes causal propagation real | Narrow (M-2) |
| **4** | Behaviors opt in via a **new optional interface** (e.g. `HandleCommandEnvelope`) type-asserted at dispatch, falling back to `HandleCommand`. Never widen the shipped interfaces | No |
| **5** | Mark `SendCommand` `// Deprecated:` only after 1–4 have shipped and one minor release has passed. Removal requires `/v5` | No until `/v5` |

**Adapters required**: (1) `SendCommand` → envelope, (2) `Result` → `(State, uint64, error)`, (3) `egopb.ErrorReply` → `OutcomeFailed` (lossy, per D5), (4) optional-behavior-interface assertion. **Deprecations required**: `SendCommand` only, deprecated-not-removed; module is `/v4`, so no removal before `/v5`. None of these are built in this change.

## Open Questions

All resolved by the repository owner (2026-09-14). Nothing blocks implementation or `sdd-tasks`:

- [x] **D9** — **Approved.** `Carrier`/`MarshalMetadata`/`UnmarshalMetadata` ship in WRITE-003, bounded as stated in D9 above (metadata only, no payload, no boundary-crossing mechanism decided here).
- [x] **D7** — **Approved.** Root `CorrelationID` defaults to `CorrelationID(operationID)`.
- [x] **D7** — **Approved.** Custom metadata is *not* inherited by `Derive` — owner prefers fail-explicit propagation over silent transitive copying.
- [x] **D4** — **Approved.** `tenancy.Administrative.CorrelationID()` stays untouched; disambiguation by type/scope/namespace, no rename of shipped API for stylistic reasons.
- [x] **M-4 Stage 1** — **Approved as a non-binding proposal.** `Engine.Dispatch` is a name suggestion only; WRITE-003 has no ownership over the future runtime-integration API and does not bind the follow-up to it.
