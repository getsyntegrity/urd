# Proposal — Expected revision and optimistic concurrency (EGO-WRITE-004)

| Field | Value |
|---|---|
| Change | `ego-write-004` |
| Date | 2026-09-17 |
| Phase | `sdd-propose` — proposed |
| Tracker | [`#65`](https://github.com/getsyntegrity/ego/issues/65), epic [`#12`](https://github.com/getsyntegrity/ego/issues/12), depends merged [`#59`](https://github.com/getsyntegrity/ego/issues/59) |
| Evidence base | `sdd-explore` pass on #65 (this session) — cited, not restated |

Documentation only. No Go code in this artifact.

## Problem

Ego cannot express "commit only if this aggregate is still at revision N": no write precondition exists in `command.Metadata`, in the persistence SPI, or in the result envelope (per explore). Concurrent writers to one aggregate overwrite each other silently.

**Success**: two independent writers declaring the same `ExpectedRevision=N` produce exactly one commit and exactly one `concurrency_conflict`, enforced inside persistence, across actors, processes and nodes.

## Decisions (closed — not reopened here)

| # | Decision | Rationale |
|---|---|---|
| C1 | `ExpectedRevision` is optional command metadata with explicit presence (`command`'s existing `hasX/value` pattern). | A sentinel collapses "unspecified" into genesis. |
| C2 | Absent → unconditional; `=0` → genesis; `=N>0` → exactly N. | Legacy callers must never become `=0`. |
| C3 | Propagated losslessly `Metadata → Carrier → Envelope → runtime → persistence`; new key in the reserved `ego.cmd.*` namespace. | Single source of truth; follows #59 carrier governance. |
| C4 | Compare-and-commit is ONE atomic conditional operation inside persistence; in-actor checks and mailbox serialization are insufficient. | Must hold across processes and nodes. |
| C5 | Both store SPIs take an explicit precondition type — a real breaking change, no parallel optional SPI. | An opt-in shadow API re-hides the guarantee. |
| C6 | Conflict = `OutcomeRejected` + typed `Failure`, stable code `concurrency_conflict`. No seventh outcome. | Preserves the six-outcome invariant of `openspec/specs/command-envelope/spec.md`. |
| C7 | No behavior/handler signature change; the precondition never reaches the domain handler. | It is a write precondition, not domain data. |
| C8 | `ExpectedRevision` / `CurrentRevision` / `StorageRevision` stay distinct; adapter mapping fixed in design. | Prevents inventing "actual" from a stale counter. |
| C9 | testkit stores implement real CAS — writers compete unserialized. | Otherwise tests prove mailbox ordering. |

**Atomicity**: `spec-governance` returned `SPLIT_REQUIRED` (explicit independent deliverables); `spec-splitting` was already applied. This proposal routes the resulting four capabilities.

## Scope

**In**: the metadata contract and presence semantics; atomic conditional writes for both stores; testkit CAS; EventSourced and DurableState propagation plus conflict mapping; the `concurrency_conflict` contract; legacy/absent compatibility; external-implementer migration docs. Delivered as PR1–PR5; PR5 (e2e/compat/docs) is not a fifth capability — it distributes as integration ACs of the consuming capabilities.

**Out**: WRITE-005 idempotency; WRITE-006 multi-event append (the SPI must not block it later); any `HandleCommand` unification or behavior refactor; repurposing `priorVersion`/`checkPreconditions`, which keeps its current different responsibility; wire/proto changes.

## AC placement against #65

AC1–AC12 are the **intended** AC set for #65, replacing its current single generic AC. Editing the issue is a later step, not this phase.

| #65 AC | Owner |
|---|---|
| AC1 presence, AC2 absence/genesis, AC5 `concurrency_conflict`, AC8 legacy compatibility | `command-envelope` |
| AC3/AC4 atomic conditional writes, AC9/AC10 two-writer tests, AC11 concurrent genesis | `persistence-concurrency` |
| AC6 EventSourced propagation affects the real commit | `event-sourced-concurrency` |
| AC7 DurableState propagation affects the real commit | `durable-state-concurrency` |
| AC12 breaking-change + external-implementer migration docs | `design.md` Migration/Rollout |

## Capabilities

### New Capabilities
- `persistence-concurrency`: EventsStore/StateStore conditional-write SPI, precondition type, testkit CAS.
- `event-sourced-concurrency`: EventSourcedActor/EventsWriterActor integration consuming the two contracts below.
- `durable-state-concurrency`: DurableStateActor integration consuming the same two contracts.

### Modified Capabilities
- `command-envelope`: adds `ExpectedRevision` metadata + the `concurrency_conflict` Failure contract — delta spec against promoted `openspec/specs/command-envelope/spec.md`.

Contracts: `command-envelope` produces `CONTRACT-EXPECTED-REVISION-v1`, `persistence-concurrency` produces `CONTRACT-CONDITIONAL-WRITE-v1`; capabilities 3 and 4 consume both and are independent of each other.

## Affected areas

| Area | Impact | Description |
|---|---|---|
| `command/metadata.go`, `carrier.go`, `envelope.go` | Modified | Additive field, carrier key, propagation. |
| `command/errors.go`, `command/result.go` | Modified | Additive `concurrency_conflict` code. |
| `persistence/events_store.go`, `state_store.go` | Modified | **Breaking**: precondition parameter. |
| `testkit/eventstore.go`, `durablestore.go` | Modified | Real CAS. |
| `event_sourced_actor.go`, `events_writer_actor.go`, `durable_state_actor.go` | Modified | Propagation, conflict mapping. |
| `saga_actor.go` | Modified | Mechanical only: two direct `EventsStore.WriteEvents` call sites (compensate/commit paths) pass `persistence.Unconditional()` to compile against the new signature — no behavior change (design.md D12). |
| `protos/`, `egopb/`, behavior interfaces | Unchanged | Out of scope. |

**Public consumer surfaces**: `persistence.EventsStore`/`StateStore` break (external implementers migrate); `command.Metadata`/`Carrier`/`Envelope`/`Failure` are additive; `Engine.SendCommand`, `EventSourcedBehavior`, `DurableStateBehavior`, `SagaBehavior` and `egopb` stay byte-identical.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Breaking SPI strands external store implementers | High | Gated; per-SPI migration path in design. |
| CAS lands above persistence — passes tests, fails across nodes | Med | Architectural-gate test must prove store-level enforcement. |
| Absent metadata degrades to `=0` somewhere | Med | Explicit presence (C1) + dedicated compatibility AC. |
| Precondition shape blocks WRITE-006 append | Med | Named design constraint before the SPI freezes. |

## Rollback

Phased by PR. PR1 is additive — drop the field, key and code. PR2–PR4 carry the break — restore the prior `WriteEvents`/`WriteState` signatures and testkit stores, then `go mod tidy && go mod vendor`. No stored event, state or reply format changes, so rollback never needs a data migration.

## Human gate

`spec-governance` §10 — **public API** and **architecture**. The core guarantee requires a breaking persistence SPI change (C5); owner approval is required before `sdd-spec` freezes the SPI shape. Every other decision above is already ratified.

## Success criteria

- [ ] Two independent writers at `ExpectedRevision=N` yield success 1 / conflict 1, for both stores.
- [ ] Two concurrent `=0` writers against a nonexistent aggregate yield exactly one commit.
- [ ] The guarantee is shown to come from persistence, not mailbox serialization.
- [ ] Conflicts surface as `OutcomeRejected` with `Failure.Code == concurrency_conflict`.
- [ ] Commands without `ExpectedRevision` behave exactly as before.
- [ ] No seventh `Outcome`; no domain handler signature changed.
- [ ] Both SPIs carry a documented external-implementer migration path.
