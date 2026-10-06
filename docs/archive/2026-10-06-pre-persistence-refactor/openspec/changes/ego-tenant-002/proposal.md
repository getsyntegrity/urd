# Proposal — Tenant-aware write path and aggregate identity (EGO-TENANT-002)

| Field | Value |
|---|---|
| Change | `ego-tenant-002` |
| Date | 2026-09-16 |
| Phase | `sdd-propose` — proposed |
| Tracker | [`#54`](https://github.com/getsyntegrity/ego/issues/54), epic [`#23`](https://github.com/getsyntegrity/ego/issues/23) |
| Depends on | [`#45`](https://github.com/getsyntegrity/ego/issues/45) (`tenancy-core`, shipped), [`#59`](https://github.com/getsyntegrity/ego/issues/59) (`command-envelope`, shipped) |
| Sibling | [`#55`](https://github.com/getsyntegrity/ego/issues/55) (`tenancy-runtime`, shipped) — deferred this gap here, not an ancestor |
| Evidence base | Engram `sdd/explore/ego-tenant-002`, reconciled against merged code |
| **Status** | **PR1 and PR2 of this change are already merged.** PR1: [`#73`](https://github.com/getsyntegrity/ego/pull/73), branch `feat/54-tenant-002-pr1-proto-es-actor`, commits `2a4a465`/`f7b99a6`/`39c55fc`, merge `ddf9337`. Covers proto (all three messages) + `event_sourced_actor.go`. PR2: [`#77`](https://github.com/getsyntegrity/ego/pull/77), branch `feat/54-tenant-002-pr2-durable-state`, merge `f0bc1f8`. Implements DS1–DS4 from `design.md` in `durable_state_actor.go`. Only `saga_actor.go` (PR3) remains. |

This proposal was written by an executor with no shell against a stale local checkout (4 commits behind `origin/main`) and, on first pass, mistook the real, already-merged PR1 work for a stale/fabricated Engram cycle. Corrected after fast-forwarding local `main` to `ddf9337` and independently verifying `#73` on GitHub plus reading the merged diff directly: proto field tags (`Event`=10, `Snapshot`=7, `DurableState`=7), `actorTenant` actor-lifetime binding, fail-closed `seedActorTenant`/`applyPersistedEvent` checks, and the two read-path/replay tenant-isolation fixes in `39c55fc` (`getStateAndReply`, `applyPersistedEvent`) all match TA1–TA8 below with no contradiction. #54 and #75 re-checked live and remain consistent with this proposal's scope.

**Second correction, same lesson repeated**: while committing the `spec.md`/`design.md`/`tasks.md` produced for PR2 (durable-state) and PR3 (saga), the local checkout was again found stale — 8 commits behind `origin/main`, including PR2's own merge (`#77`, `f0bc1f8`), landed by a separate concurrent session running `sdd-apply` against an earlier revision of `design.md`. Verified directly against `durable_state_actor.go` on the synced branch: the shipped code carries inline comments citing `DS1`/`DS3`/`DS4` and matches this change's design exactly, so no contradiction — PR2 is simply done, not pending. Re-synced (`git rebase origin/main`) before continuing.

## Problem

`tenancy-core` ratified "Resolve-Once, Propagate-After Discipline" and "Tenant Plus Aggregate Effective Identity" with no consumer. #55 wired tenant through ctx up to command acceptance, then documented its own gap: "a tenant-aware app with sagas fails closed until #54 lands." Tenant dies at every durable/async boundary — `saga_actor.go` resets to `context.Background()` at 5 sites (242, 262, 279, 335, 376), `event_sourced_actor.go`'s `batchTenant` is cleared at `resetBatch` (1184) so it never survives `recover()`, and `durable_state_actor.go` has no tenant-switch check at all.

**Success**: tenant is unforgeable data on the write path, enforced through real dispatch including a saga hop and an actor restart.

**PR1 (`#73`) already closed the event-sourced half of this gap**: `batchTenant` widened to actor-lifetime `actorTenant`, seeded fail-closed at `recover()`/`recoverFromSnapshot()`, enforced in both the batched and non-batched command paths, and — per human review before merge — also in the read path (`getStateAndReply`) and per-event replay (`applyPersistedEvent`), which the original design/tasks breakdown had missed. **PR2 (`#77`) closed the durable-state half**: the same `actorTenant`-style binding, seeded at `recoverFromStore`, enforced pre-mutation in `processCommand` (DS1), with the `PostStop` unseeded-flush guard (DS3) and the `GetStateCommand` read gate (DS4) this proposal's design phase called for. The remaining gap is exactly `saga_actor.go` (5 `context.Background()` reset sites) — PR3.

## Decisions (closed here)

| # | Decision | Rationale |
|---|---|---|
| TA1 | Tenant travels as **data on the persisted record**, never via `context.Context` | Events reach the saga via in-process `Tell(context.Background(), ...)`; the originating ctx is structurally gone (explore) |
| TA2 | Additive tenant metadata on **three** messages: `egopb.Event`, `egopb.Snapshot`, `egopb.DurableState` | Refines explore's two-message read: with `DeleteEventsOnSnapshot`, `recoverFromSnapshot` is the only survivor, so a tenant-less `Snapshot` makes AC3 unprovable |
| TA3 | **Fail closed** on missing or invalid tenant metadata in tenant-aware mode: no backfill, no grandfathering, no implicit default tenant, no pre-existing-record exemption | Owner-ratified. Ego has no historical data and no legacy users, so the cheap-but-permanent compatibility hole buys nothing |
| TA4 | Reuse `tenancy.MarshalMetadata`/`UnmarshalMetadata` under the existing `ego.tenant.*` keys; no second tenant serialization, no metadata map of our own, no resolution mechanism outside `tenancy.TenantResolver` | `command/carrier.go:94,165` already delegates exactly this way; reinventing it on the persisted record is the AC-level duplication failure |
| TA5 | Scope is **write-side only**: commands, persisted events, snapshots, durable states. Query envelopes are excluded | #54's AC1 was revised; queries moved to [`#75`](https://github.com/getsyntegrity/ego/issues/75) (EGO-QUERY-001) because ego has no `Query` type or dispatch path. #75 is **not** a dependency — #54 neither blocks on it nor consumes it |
| TA6 | Per-aggregate tenant binding must be **durable across actor restart**: widen `batchTenant` from batch scope to actor lifetime, seeded at `recover()`; add the equivalent check to `durable_state_actor.go`, which has none | Today's check is homogeneity within one in-flight batch, which a restart erases; `tenancy.VerifyUnchanged` (`event_sourced_actor.go:887`) is reused, not replaced |
| TA7 | `tenancy-core`'s two requirements are **consumed and proven**, not re-decided; `command-envelope`'s field semantics are untouched | #54 owns the proof against real code, not the contract text |
| TA8 | Architecture conformance applies only to a newly introduced pure-value package, if any | The three actor files import goakt by design — #55 precedent |

## Scope

**In**: additive tenant metadata on `egopb.Event`/`Snapshot`/`DurableState` + regeneration; write at persist time and read back at `recover()`/`replayEvents`/`recoverFromSnapshot`, fail-closed per TA3; `TenantContext` reconstruction at the five `saga_actor.go` reset sites; actor-lifetime tenant binding and cross-tenant command rejection in **both** actor kinds; one end-to-end test over the real `Engine.SendCommand` → actor → saga path.

**Out**:

| Deferred | Owner |
|---|---|
| Query envelope, `Query` type, query dispatch | #75 (EGO-QUERY-001) |
| Redefining operation/correlation/causation/principal/custom/deadline semantics | #59 (shipped) |
| Resolver configuration, auto-invoke, single-tenant mode | #55 (shipped) |
| EventStore namespacing, read-side/projection and topic isolation | TENANT-003/004/005 |
| Transport-level propagation | TRANSPORT-003 |
| Cross-tenant conformance suite | TENANT-007 |
| `EraseEntity` / `Migrator.Run` tenancy semantics | TENANT-008 |

## Capabilities

- **New**: `tenancy-write-path` — durable tenant metadata on persisted events/snapshots/state, fail-closed decode, reconstruction at async boundaries, tenant + aggregate effective identity across all three actor kinds (event-sourced, durable-state, and — per the human-confirmed SG4 decision — saga, which now binds to the first tenant it observes rather than remaining tenant-agnostic).
- **Modified**: None. `tenancy-core` and `command-envelope` are consumed unchanged; `tenancy-runtime`'s T4-A/T4-B gates keep their current semantics and the new enforcement is additive.

## Approach

One mechanism satisfies both open ACs: tenant metadata persisted on the record, decoded at every boundary that reads it back. The saga reconstructs `TenantContext` from the event it receives; the actor re-establishes its binding from the snapshot or replayed events after restart.

Field shape, tag numbers, key set, decode helpers and the rejection error model are **already fixed by PR1** (`#73`): `map<string,string> tenant_metadata` on `Event`(10)/`Snapshot`(7)/`DurableState`(7), `tenancy.MarshalMetadata`/`UnmarshalMetadata`, `tenancy.VerifyUnchanged` → `ErrDenied`, absent/malformed → `ErrInvalid`. **PR2 (`#77`) already reused this exactly** for `durable_state_actor.go`, per `design.md`'s DS1–DS4. `design.md` resolved, by explicit human decision, what this proposal originally deferred for the saga: how `saga_actor.go` threads a per-event reconstructed `context.Context` through its five reset sites. The resolution supersedes this proposal's original framing: the saga actor instance now **binds to the tenant of the first event it validly processes** (`boundTenant`) and rejects any later event or replayed record from a different tenant with `ErrDenied` — it does not remain tenant-agnostic across its lifetime. Serving more than one tenant per saga instance is explicitly out of scope for this change, deferred to a future opt-in capability.

## Affected areas

| Area | Impact | Change |
|---|---|---|
| `protos/ego/ego.proto`, `egopb/` | **Shipped (#73)** | Additive tenant metadata on `Event`(10), `Snapshot`(7), `DurableState`(7) + regen — all three messages, so no further proto change is expected for PR2/PR3 |
| `event_sourced_actor.go` | **Shipped (#73)** | Persist/recover tenant; actor-lifetime `actorTenant` replacing batch-scoped `batchTenant`; enforcement in batched, non-batched, read (`getStateAndReply`) and replay (`applyPersistedEvent`) paths |
| `durable_state_actor.go` | **Shipped (#77)** | `actorTenant` binding seeded at `recoverFromStore`; pre-mutation cross-tenant gate in `processCommand` (DS1); persist/`PostStop`-skip (DS3); `GetStateCommand` read gate (DS4) |
| `saga_actor.go` | Pending (PR3) | Reconstruct `TenantContext` at the 5 ctx reset sites |
| e2e test | Pending (PR3) | Real-dispatch saga-hop and restart tenant integrity |
| `tenancy/`, `command/`, `engine.go`, `option.go` | Unchanged | Consumed as-is (TA4, TA7) |

**Public consumer surfaces**: `egopb.Event`, `egopb.Snapshot` and `egopb.DurableState` gain a field — any third-party `persistence.EventsStore`/`SnapshotStore`/`StateStore` implementation sees a wider message. Byte-identical after this change: `Engine.SendCommand`, `Command`/`Event`/`State`, `SagaCommand`, the three behavior interfaces, `Config`/`Option`, the `command` package.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Wire-format change is the first in this chain; a wrong field shape is expensive to undo once persisted | **Resolved** | Shipped in PR1 (`#73`) — `Event`(10)/`Snapshot`(7)/`DurableState`(7), additive on free tags, already reviewed and merged. No wire-format decision remains open for PR2/PR3, which consume the same fields. |
| Fail-closed (TA3) makes any tenant-aware deployment reject records written by a pre-change build | High | Accepted and intended — no historical data, no legacy users; proven for the event-sourced actor in PR1 and the durable-state actor in PR2 (`ErrInvalid`/`ErrDenied`); still to prove for the saga in PR3 |
| Regenerated `egopb/*.pb.go` plus vendoring inflates the diff | **Resolved** | No new proto fields needed by PR2 or PR3 — confirmed for PR2 (`#77`, no `egopb/` diff) and expected to hold for PR3 |
| 400-line budget across proto + 3 actors + e2e | Low (was High) | PR1 and PR2's authored diffs both landed within their own slice's budget; PR3 (saga + e2e) is the one remaining slice — `sdd-tasks` forecasted it Medium |
| Design/tasks breakdowns that enumerate specific code paths can silently miss sibling paths carrying the same invariant | Med | Learned the hard way on PR1: the original Phase-3/4 breakdown covered only the command-write paths and missed `getStateAndReply` (read) and `applyPersistedEvent` (per-event replay), both fixed in `39c55fc` before merge. PR2's design (DS4) applied the lesson pre-emptively by enumerating `DurableStateActor`'s full dispatch surface, including `GetStateCommand`. PR3's design (SG1/SG5) does the same for `SagaActor`'s reset sites. |

## Delivery plan

Stacked chain — incremental, not from-scratch. One more slice than the #55/#59 precedent because of the proto boundary, but that slice is already done.

1. **PR1** — **DONE, merged as [`#73`](https://github.com/getsyntegrity/ego/pull/73).** Proto (3 messages) + regen; event-sourced persist/recover wiring, fail-closed decode, plus the two read-path/replay tenant-isolation fixes found in review (`39c55fc`). Foundation everything else consumes.
2. **PR2** — **DONE, merged as [`#77`](https://github.com/getsyntegrity/ego/pull/77).** Durable-state persist/recover + tenant-switch enforcement (DS1–DS4) on the carrier fields PR1 shipped. No proto change.
3. **PR3** — saga reconstruction at the five `context.Background()` sites (bind-on-first-event, `VerifyUnchanged` per SG4) + the real end-to-end dispatch test. **The only remaining slice.**
4. **PR4** — only if PR3's authored diff (saga wiring + e2e) exceeds the budget on its own; **default is to keep it inside PR3**, reversing PR1's proposal-time assumption now that PR1 and PR2's diffs both showed slices run smaller than forecast.

## Human gate

Required per `spec-governance` §10 — architecture, public API, and **persisted data format**. TA3 (fail-closed, no backfill) is already owner-ratified. **The proto field shape and tag allocation gate is already satisfied** — PR1 shipped it (`#73`) with human review before merge, covering all three messages this change needs. No outstanding proto/wire-format decision remains for PR2 or PR3, since both consume fields already on the wire. If `sdd-design` finds either slice needs an additional field, that reopens this gate; absent that, PR2/PR3 need no pre-apply human gate on the wire format itself — only the standard PR review.

## Rollback

Revert the commits, `make proto`, `go mod vendor`. The field is additive on free tags, so a reverted build ignores tenant metadata already written and no data migration is undone. Under TA3 there is no grandfathering path to unwind, and no caller signature or reply payload changes at any point.

## Success criteria

- [ ] Saga boundary reconstructs `TenantContext` from the carried metadata, proven by a test that forces the `context.Background()` reset. (PR3, pending)
- [x] Event-sourced: a command against an aggregate already bound to another tenant is rejected, batched and non-batched, read and write paths — proven in PR1 (`#73`). — [x] Durable-state: same, proven in PR2 (`#77`, DS1/DS4).
- [x] Event-sourced: tenant binding survives actor restart, proven via snapshot recovery and via event replay — proven in PR1. — [x] Durable-state: proven in PR2 via `recoverFromStore` (DS2).
- [x] Event-sourced: missing or invalid tenant metadata on an event or snapshot fails closed in tenant-aware mode, zero writes, no default-tenant fallback — proven in PR1 (TA3 negative-path evidence). — [x] Durable-state: proven in PR2 (DS2/DS3, including the unseeded-`PostStop` skip test).
- [x] No new envelope, no new metadata map, no tenant serialization outside `tenancy.MarshalMetadata`/`UnmarshalMetadata`; `command/` and `tenancy/` diffs are empty. Holds through PR1 and PR2 (PR2's own diff confirms empty `command/`/`tenancy/`); to re-check for PR3.
- [ ] One end-to-end test proves tenant integrity through `Engine.SendCommand` → actor → saga hop; the existing `TestSendCommandTenantResolution` explicitly does not satisfy this. (PR3, pending)
- [x] Legacy (non-tenant-aware) engines observe no behavior change — held for PR1 and PR2's full-suite regression; re-verify for PR3.
- [x] Field shape, tag numbers, decode helpers and error model are fixed — shipped in PR1 (`map<string,string> tenant_metadata`, `Event`=10/`Snapshot`=7/`DurableState`=7, `tenancy.MarshalMetadata`/`UnmarshalMetadata`, `ErrInvalid`/`ErrDenied`) and reused unchanged by PR2. PR3's `sdd-design` also reuses it, it does not redecide it.
