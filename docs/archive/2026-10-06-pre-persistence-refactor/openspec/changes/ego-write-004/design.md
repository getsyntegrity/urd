# Design — Expected revision and optimistic concurrency (EGO-WRITE-004)

Resolves what `proposal.md` (C1–C9) and the four delta specs deferred to design. Spec requirements are cited by name, never restated. The #65 mandate is closed; nothing here reopens it.

## Technical Approach

One precondition value travels down — `command.Metadata → Carrier → context → actor → persist request → store compare-and-commit` — and one typed conflict error travels back up — `store → actor → egopb.ErrorReply → resultFromReply → command.Result`. Persistence owns the atomic compare-and-commit (C4); every layer above only transports it. Three seams carry the change: a value type in `persistence` (D1), an optional metadata field in `command` (D5), a generalized reply classifier in `ego` (D8).

## Architecture Decisions

### D1 — `persistence.WritePrecondition`: one value type, zero value invalid

| Option | Tradeoff | Decision |
|---|---|---|
| `*uint64` / sentinel `uint64` | Collapses genesis into "unspecified"; violates C1/C2 | Rejected |
| Interface + three implementations | Heap + type switch in every adapter, no gain | Rejected |
| Comparable value struct, unexported fields, named constructors | Copies free, exhaustive, no sentinel reachable | **Chosen** |

Four modes, not three: `preconditionUnspecified` is the zero value and is **invalid** — a store MUST reject it with `ErrInvalidPrecondition` rather than read it as unconditional. An unset field in a caller's struct literal must never silently become a write; that is C2's "absence never becomes a default" rule one layer down. Mirrors `command.Outcome`/`tenancy.Reason` zero-invalid enums. Satisfies *Explicit Write Precondition Type*.

### D2 — Extend the two existing methods in place; precondition is the trailing parameter

No `ConditionalWriteEvents`, no optional SPI (C5). Trailing position keeps adapters and the five in-repo production call sites to a one-token edit, and gives external implementers a compile error rather than a silent no-op (*External implementer fails at compile time*).

Batch-scope invariant: one precondition governs one `WriteEvents` call (*Precondition Shape Does Not Block Future Atomic Multi-Event Append*). When it is not `Unconditional()`, all events in the batch MUST share one `PersistenceId` — the store reads the target from `events[0]` and returns `ErrPreconditionScope` otherwise, including for an empty slice (no target to evaluate). Already true of every current caller.

### D3 — `*persistence.ConflictError` + `persistence.ErrConcurrencyConflict`

Identifiable by both `errors.As(err, &ce)` and `errors.Is(err, ErrConcurrencyConflict)` (`ConflictError.Is` matches the sentinel) — never string matching (*Stale exact-revision precondition is rejected atomically*). It always carries the declared precondition, and the actual `StorageRevision` under an explicit-presence pair `ActualRevision() (uint64, bool)` set **only** from the value the store read at the failed compare. An adapter that cannot learn it cheaply (SQL `UPDATE ... WHERE version = N` affecting 0 rows) omits it, and nothing upstream may supply it from a local counter (*Conflict is never invented from a stale local counter*).

### D4 — Revision model mapping, fixed now (C8)

| Concept | EventSourced | DurableState |
|---|---|---|
| `ExpectedRevision` | `Metadata.ExpectedRevision()`; absent → `Unconditional()`, `0` → `ExpectGenesis()`, `N>0` → `ExpectRevision(N)` | identical translation |
| `CurrentRevision` | `eventsCounter` (`batchCounter` while a batch is open); advanced only by `applyConfirmedState` after a confirmed write | `currentVersion`; advanced only inside `commitState` after `WriteState` returns nil |
| `StorageRevision` | highest committed `egopb.Event.SequenceNumber` per `persistenceID` | committed `egopb.DurableState.VersionNumber` per `persistenceID` |

`ExpectRevision(0)` is unreachable from command metadata (C2 maps `0` to genesis) but is a distinct, meaningful SPI value: a `DurableState` record committed at `VersionNumber == 0` exists — the legacy genesis record `recoverFromStore` already handles (`durable_state_actor.go:214-221`) — so "record at version 0" and "no record" are different storage facts. Consequence, stated explicitly: a caller declaring `ExpectedRevision=0` against an existing version-0 record gets a conflict, which is correct per the mandate's genesis rule. For `EventsStore`, sequence numbers start at 1, so `ExpectGenesis()` and `ExpectRevision(0)` hold under the same storage state while staying distinct values the conflict error reports faithfully.

### D5 — `ExpectedRevision` metadata: `hasX/value`, carrier key `ego.cmd.expected_revision`

Field pair `expectedRevision uint64` / `hasExpectedRevision bool`, set only by `WithExpectedRevision`, read by `ExpectedRevision() (uint64, bool)` — the exact shape of `causationID/hasCausation` and `deadline/hasDeadline`. `"expected_revision"` joins `reservedCustomKeys` so no custom key can shadow it (WRITE-003 D6). Carrier key literal: **`ego.cmd.expected_revision`**, emitted only when present, value `strconv.FormatUint(v, 10)`; `UnmarshalMetadata` rejects a malformed value with `ErrInvalidMetadata` (same trust-boundary rule as the deadline parse) and never defaults it, so *Absent expected revision never becomes genesis* holds structurally.

`Metadata.Derive` does **not** inherit it — the fail-explicit rule WRITE-003 D7 applied to custom metadata. A precondition binds one aggregate at one revision; carrying it into a child command targeting another aggregate would be a correctness bug.

### D6 — `command.CodeConcurrencyConflict = "concurrency_conflict"`

Exported constant in `command`, carried by the existing `WithFailureCode` + `NewRejected` — no new `Outcome`, no new `Failure` field (*No seventh outcome kind is introduced*). It is the first framework-emitted failure code; `WithFailureCode` stays caller-defined for everything else.

### D7 — Conflict detail crosses the actor boundary in `ErrorReply.Message`

`protos/`/`egopb` are frozen (proposal Out-of-scope) and `egopb.ErrorReply` carries a single `string Message` (`egopb/ego.pb.go:310-316`). So `ConflictError.Error()` defines a canonical, parseable one-line grammar and `persistence.ParseConflictError` is its exact inverse, with a round-trip property test as the contract. This generalizes the precedent already shipped for the deadline gate (`deadline_gate.go:38-42`), which recovers classification from the same string.

Invariant (test-enforced): a `*ConflictError` MUST reach `sendErrorReply` **unwrapped** — no `fmt.Errorf("...: %w", err)` prefix — or classification degrades to `OutcomeFailed`. That already holds on both paths (`replyDirect` and `commitState` forward the store error verbatim); the test keeps it holding. A store whose error does not conform simply does not classify: the caller sees today's `OutcomeFailed`, never a wrong conflict.

### D8 — `resultFromReply`: table-driven classifier registry

`engine.go:1365-1370`'s hardcoded two-case `switch` becomes an ordered registry in a new `reply_classification.go`:

| Sentinel | Constructor | Failure code |
|---|---|---|
| `errActorContextCanceled` | `NewCanceled` | — |
| `errActorDeadlineExceeded` | `NewTimedOut` | — |
| `persistence.ErrConcurrencyConflict` | `NewRejected` | `concurrency_conflict` |
| *(no match)* | `NewFailed` | — |

Each entry matches `strings.HasPrefix(message, sentinel.Error())`; first match wins; declaration order is priority. Registry invariant, test-enforced: no sentinel's text is a prefix of another's, so order can never silently change a classification. The conflict entry additionally runs `ParseConflictError` and attaches the rematerialized `*ConflictError` via `WithFailureCause`, so `errors.As(result.Err(), &ce)` recovers expected/actual through `Error.Unwrap` (`command/errors.go:93`). That is the full reply-path reconciliation, and it needs no new field on `command.Failure`; `command` gains no `persistence` import, since `ego` is the only package joining the two.

### D9 — Batched flush: base-anchored precondition + admission gate

A flush is **one** physical `WriteEvents`, so it carries exactly one precondition. Policy:

1. `batchBase` = the founding command's own declared `ExpectedRevision` when it declares one, else `eventsCounter`, fixed the moment the batch opens — the store revision the flush appends onto. Intra-batch sequence numbers already chain from it (`buildEnvelopes`). `batchBase` never moves for the rest of that batch cycle: a later admitted command's own declared revision was already checked against the running logical counter by step 2's admission gate, which is exactly equivalent to re-checking it against `batchBase` at flush time (step 3's equivalence argument) — overwriting `batchBase` with a later command's logical, mid-batch revision would anchor the physical CAS to a value the store was never actually at, and every such flush would fail.
2. Admission: a command declaring `ExpectedRevision=E` joins the open batch only if `E == batchCounter` (the revision the batch reaches immediately before that command's events). A command declaring nothing always joins. Admission, and therefore whether the batch now "has a precondition" (step 3), depends only on whether the command declared a revision and was admitted with it — never on how many events that command goes on to produce, including zero: a same-cycle command that is admitted with a matching declared revision but turns out to be a no-op still owes the batch a real CAS precondition, exactly as if it had produced events.
3. Resolution: no admitted command declared one, batch-wide across the whole cycle (not merely the founder) → `Unconditional()`; otherwise `ExpectGenesis()` when `batchBase == 0` and the first declaring command declared `0`, else `ExpectRevision(batchBase)`. Not a weakening: requiring storage `== batchBase` and then atomically appending the preceding staged events is exactly equivalent to each admitted command's own `E`. Nothing is dropped or averaged. In particular, a batch founded by a command that declares nothing still owes a real precondition to a later admitted command that does declare one — tracking "declared at all" as the founder's own `hasRevision` rather than as an OR across every admitted command silently downgrades that later command's explicit guarantee to `Unconditional()`.
4. Non-admissible command → **forced flush** of the open batch first; the command then anchors a fresh batch against the newly confirmed `eventsCounter`, so every precondition is evaluated against the storage state it actually declared against.

Steps 2/4 use local state for a *batching boundary* decision only, never a commit decision — the store stays the sole authority and a stale local counter costs at most an extra flush. Attribution limit, accepted and documented: one atomic write has one outcome, so a batch conflict rejects every command in that flush with the same `concurrency_conflict` (today's `handleBatchPersistResponse` error path already assigns one reply to all entries). The actor MUST NOT name a "culprit". Exact per-command attribution is what the non-batched path gives; batching is opt-in via `batchThreshold`.

### D10 — Post-conflict actor lifecycle: stay alive only when provably in sync

A conflict is routine, not corruption — but it may mean the actor's `CurrentRevision` is behind storage. Rule, using only reliably-known data: if the `ConflictError` reports an actual revision **and** it equals the actor's own `eventsCounter`/`currentVersion`, the actor is provably in sync (the caller's expectation was stale) → reply `concurrency_conflict`, discard the pending write, keep running. Otherwise (actual unknown, or actual differs) the local counter is untrustworthy → reply first, then take the existing failure path: `EventSourcedActor` sets `directShutdown`/`shutdownOnDrain` so the supervisor restarts it into `recover()`; `DurableStateActor`, which has no shutdown path, re-runs `recoverFromStore` before the next command. Rejected alternative: adopting the store's revision in place, which needs a new resync mechanism and risks serving state the store never confirmed. No in-memory mutation happens on either branch (*No partial commit on conflict*).

### D11 — testkit CAS: immutable per-persistenceID cells

`testkit/eventstore.go` re-keys `db` from `EventKey` to `persistenceID → *eventLog`, where `eventLog{revision uint64, events []*egopb.Event}` is immutable once published. A conditional write builds the successor log and publishes it with `db.CompareAndSwap(pid, old, new)` (pointer identity); genesis uses `db.LoadOrStore(pid, new)` and treats `loaded == true` as the conflict. Losers publish nothing, so no partial batch is observable and no orphan record can shadow the winner's — which is why the immutable-cell restructure is required rather than mutating the existing per-event map. `testkit/durablestore.go` needs only the same two primitives over its existing `pid → *egopb.DurableState` map. No mutex, no queue, no caller-side ordering (*Unserialized goroutines compete directly against the store*). `EventKey` stays declared, marked `// Deprecated:` and unused, to avoid an incidental second break in the same PR.

Retention trap: `DeleteEvents` must truncate `eventLog.events` while leaving `eventLog.revision` unchanged, or retention resets `StorageRevision` and lets a stale writer win.

### D12 — Saga write sites stay unconditional (correction to `proposal.md`'s affected-areas table)

`saga_actor.go:574` and `:611` call `WriteEvents` directly — two production call sites the proposal listed as unchanged. They are mechanically updated to pass `persistence.Unconditional()`. Saga journaling gains no precondition semantics here: a compile-driven edit, not a behavior change.

## Data Flow

    Caller ── WithExpectedRevision(N) ──→ Metadata ──→ Carrier[ego.cmd.expected_revision]
                                                              │ (ctx, local hop)
      EventSourcedActor / DurableStateActor ── dispatchToBehavior (handler: no precondition, C7)
                          preconditionFromMetadata(md) ───────┤
                                   │                          │
         persistEventsRequest{envelopes, precondition}   commitState(..., precondition)
                                   ▼                          ▼
              eventsWriterActor ─→ WriteEvents(ctx, ev, p)   WriteState(ctx, st, p)
                                   │        ATOMIC COMPARE-AND-COMMIT
                                   ▼
                    *ConflictError ─→ ErrorReply.Message ─→ resultFromReply (D8)
                                   ─→ Result{OutcomeRejected, Code=concurrency_conflict}

## Interfaces / Contracts

```go
package persistence // CONTRACT-CONDITIONAL-WRITE-v1

type WritePrecondition struct{ /* mode, revision — unexported, comparable */ }

func Unconditional() WritePrecondition                // legacy, no check
func ExpectGenesis() WritePrecondition                // no prior commit may exist
func ExpectRevision(revision uint64) WritePrecondition
func (p WritePrecondition) IsUnconditional() bool
func (p WritePrecondition) IsGenesis() bool
func (p WritePrecondition) Revision() (uint64, bool)  // hasX/value; false unless exact-revision
func (p WritePrecondition) Valid() bool               // false for the zero value

type EventsStore interface { // only this method changes
    WriteEvents(ctx context.Context, events []*egopb.Event, precondition WritePrecondition) error
}
type StateStore interface { // only this method changes
    WriteState(ctx context.Context, state *egopb.DurableState, precondition WritePrecondition) error
}

var ErrConcurrencyConflict = errors.New("ego: concurrency conflict")
var ErrInvalidPrecondition = errors.New("persistence: write precondition is not valid")
var ErrPreconditionScope   = errors.New("persistence: conditional write spans multiple persistence ids")

type ConflictError struct{ /* persistenceID, expected, actual, hasActual — unexported */ }

func NewConflictError(persistenceID string, expected WritePrecondition, opts ...ConflictOption) *ConflictError
func WithActualRevision(revision uint64) ConflictOption // ONLY when read at the failed compare
func (e *ConflictError) PersistenceID() string
func (e *ConflictError) Expected() WritePrecondition
func (e *ConflictError) ActualRevision() (uint64, bool)
func (e *ConflictError) Is(target error) bool          // matches ErrConcurrencyConflict
func (e *ConflictError) Error() string                 // canonical grammar below
func ParseConflictError(message string) (*ConflictError, bool) // exact inverse of Error()

package command // CONTRACT-EXPECTED-REVISION-v1 (additive only)

const CodeConcurrencyConflict = "concurrency_conflict"
const carrierKeyExpectedRevision = "ego.cmd.expected_revision"

func WithExpectedRevision(revision uint64) MetadataOption
func (m Metadata) ExpectedRevision() (uint64, bool)
```

Canonical grammar (D7), one line, no wrapping prefix:
`ego: concurrency conflict: persistence_id=<id>, expected=<unconditional|genesis|N>, actual=<M|unknown>`

## File Changes

| File | Action | Description |
|---|---|---|
| `persistence/precondition.go` | Create | D1 type, constructors, accessors |
| `persistence/conflict.go` | Create | D3 error + sentinels, `Error()`/`ParseConflictError` (D7) |
| `persistence/events_store.go`, `state_store.go` | Modify | D2 signature break + documented atomicity contract |
| `command/metadata.go`, `carrier.go` | Modify | D5 field, option, accessor, carrier key, reserved key, no `Derive` inheritance |
| `command/result.go` | Modify | D6 constant |
| `reply_classification.go` | Create | D8 registry; `resultFromReply` delegates to it |
| `engine.go` | Modify | D8 — replace the hardcoded prefix `switch` |
| `testkit/eventstore.go`, `durablestore.go` | Modify | D11 CAS cells; head-preserving `DeleteEvents` |
| `mocks/persistence/{events_store,state_store}.go` | Regenerate | mockery, new signatures |
| `events_writer_actor.go` | Modify | precondition on `persistEventsRequest`; conditional call site (`:97`) |
| `event_sourced_actor.go` | Modify | extract after `dispatchToBehavior`; `persistAsync`/`askEventsWriter`; D9 fields + admission; D10 |
| `durable_state_actor.go` | Modify | extract in `processCommand`; `commitState`/`persistStateAndPublish`; D10 |
| `saga_actor.go` | Modify | D12 — two `Unconditional()` call sites |
| `protos/`, `egopb/`, behaviors, `Engine.SendCommand` | Unchanged | C7 / out of scope |

## Testing Strategy

| Layer | What | Mandate tests |
|---|---|---|
| Unit — `command` | presence, genesis≠absence, carrier round-trip, no `Derive` inheritance, code constant, six outcomes intact | T5 (contract half) |
| Unit — `persistence` | precondition states, zero-value rejection, scope error, `Error()`/`Parse` round trip | — |
| Unit — `ego` | classifier registry: conflict→Rejected, non-prefix-overlap invariant, unwrapped-error invariant | — |
| Concurrency — testkit | unserialized goroutines under `-race`; success==1 ∧ conflict==1; T11: same unserialized-writer construction is the proof the guarantee is persistence-owned, not mailbox-owned | T1–T4, T8, T9, T10, T11 |
| Integration — ES | real actor + real store; stale revision leaves storage unchanged; batch merge + forced flush | T6 |
| Integration — DS | real actor + real store; `checkPreconditions` passes yet conflict still rejects | T7 |
| E2E / compat | legacy commands unchanged end to end | T5 |

## Threat Matrix

N/A — no routing, shell, subprocess, VCS/PR automation, executable-file classification, or process-integration boundary. The change is confined to in-process Go contracts and a store SPI.

## Migration / Rollout (AC12 — breaking change for external SPI implementers)

### M-1 Inventory — affected methods and sites

| Surface | Old → new | Sites |
|---|---|---|
| `EventsStore.WriteEvents` (`persistence/events_store.go:40`) | `(ctx, events []*egopb.Event) error` → `(ctx, events []*egopb.Event, precondition WritePrecondition) error` | 1 definition |
| `StateStore.WriteState` (`persistence/state_store.go:40`) | `(ctx, state *egopb.DurableState) error` → `(ctx, state *egopb.DurableState, precondition WritePrecondition) error` | 1 definition |
| Production callers | — | `events_writer_actor.go:97`, `saga_actor.go:574`, `saga_actor.go:611`, `durable_state_actor.go:534`, `durable_state_actor.go:573` |
| In-repo implementations | — | `testkit/eventstore.go:75`, `testkit/durablestore.go:86`, 2 mockery mocks |
| Test/benchmark call sites | — | 72 occurrences across 12 files |
| External implementations | — | not enumerable — `github.com/pablogore/ego/v4` is a published module |

### M-2 Breaking-change classification

| Class | What | Who breaks |
|---|---|---|
| **Source-breaking** | Both SPI methods gain a required parameter | Every external `EventsStore`/`StateStore` implementation |
| **Source-breaking (narrow)** | Every direct caller of either method | M-1's in-repo sites + external callers |
| **Additive** | `command` metadata field, carrier key, failure code | Nobody |
| **Behavioral** | Conflict replies reclassify `OutcomeFailed → OutcomeRejected` | Callers matching on outcome; only reachable once a precondition is declared |
| **Wire / persisted data** | None — `protos/`, `egopb`, stored records unchanged | Nobody |

### M-3 Upgrade recipe for an external implementer

1. Add the parameter; return `ErrInvalidPrecondition` when `!precondition.Valid()`.
2. `IsUnconditional()` → today's write, byte for byte. This alone restores compilation with pre-WRITE-004 behavior for every caller that declares nothing.
3. `IsGenesis()` → insert-if-absent in one atomic statement (`INSERT ... ON CONFLICT DO NOTHING`, `LoadOrStore`, conditional put); 0 rows / `loaded` → `NewConflictError(id, precondition, ...)`.
4. `Revision()` → single-statement compare-and-commit (`WHERE version = $expected`, CAS); 0 rows affected → conflict. Never `SELECT` then `INSERT`: read-compare-write is exactly what C4 forbids.
5. Attach `WithActualRevision` only if the compare itself yielded the stored value; otherwise omit.
6. Never wrap the returned `*ConflictError` in a message-prefixing error (D7).

### M-4 Adoption sequence (matching PR1–PR5)

| Stage | Work | Breaking? |
|---|---|---|
| **PR1** | `command` field/carrier/code + contract tests. Zero persistence, zero actors | No |
| **PR2** | Both SPI signatures, precondition + conflict types, testkit CAS, mock regen, mechanical call-site updates incl. D12 | **Yes — the break lands here, alone** |
| **PR3** | EventSourced propagation, D8 classifier, D9 batch policy, D10 | No |
| **PR4** | DurableState propagation, D10 | No |
| **PR5** | E2E, legacy-compat proof, this migration doc referenced from release notes | No |

PR2 is deliberately the only breaking PR, so an external implementer has one commit to port against. Release-note requirement: name both methods, both signatures, and M-3 verbatim. Module is `/v4`; the break ships in a minor release with no removal of any other symbol.

## Open Questions

None blocking. Two items are recorded as accepted, not open: the batch-granularity attribution limit (D9) and the string-encoded conflict detail forced by the frozen wire format (D7). The human gate named in `proposal.md` (public API + architecture, for the SPI break) is still required before implementation begins.
