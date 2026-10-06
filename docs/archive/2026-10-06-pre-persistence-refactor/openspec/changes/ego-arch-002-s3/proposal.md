# Proposal — Neutral behavior contracts (EGO-ARCH-002, slice S3)

| Field | Value |
|---|---|
| Change | `ego-arch-002-s3` |
| Date | 2026-09-26 |
| Phase | `sdd-propose` — proposed |
| Tracker | [`#123`](https://github.com/getsyntegrity/ego/issues/123), parent [`#10`](https://github.com/getsyntegrity/ego/issues/10); origin: criteria 1–3 of [`#103`](https://github.com/getsyntegrity/ego/issues/103) |
| Inputs | [`ego-arch-001/design.md`](../ego-arch-001/design.md) §2, §3, §5 (slice S3), §10; `ego-arch-003` design in PR [`#125`](https://github.com/getsyntegrity/ego/pull/125) |
| Baseline commit SHA | `main` at `77beda6b91646b0e031ce78401ee997fd919bd65` |
| Evidence | [`design.md`](./design.md) (GoAkt serialization path, options, API table, test plan, slices) |
| Blocks | The in-memory composition of [`#105`](https://github.com/getsyntegrity/ego/issues/105) (PR #125, IMPL-6) |
| Ordering constraint | Slices S3-2 (spawn sites in `engine.go`) and S3-4 (kind registration) land before #105 IMPL-4, which rebases onto them (human decision, 2026-09-27) |

## Why now

Every behavior a user writes for Ego today has to implement a GoAkt interface. `EventSourcedBehavior` (`behavior.go:47-48`), `DurableStateBehavior` (`behavior.go:99-100`) and `SagaBehavior` (`saga.go:41-42`) embed `extension.Dependency` from GoAkt, and `EntityKind` is an alias of it (`option.go:338`). That interface requires `MarshalBinary`/`UnmarshalBinary`, which GoAkt uses to copy a behavior to another node in a cluster. So a domain author writes serialization code even for a single-node program. `example/eventssourced/main.go:189-209` exists only to satisfy the interface.

This blocks #105. A second, in-memory composition cannot show that "the same domain runs on two runtimes" while the domain's own types are GoAkt types. It also leaves #103's first three criteria unmet, which #123 took over.

## Intent

Give behaviors runtime-neutral contracts, and keep GoAkt's serialization requirement inside the GoAkt adapter, where it belongs. Do this without breaking any v4 consumer and without changing what travels on the wire between cluster nodes.

## Decisions recorded by this change

The human made two decisions for this change. Both are now written in `ego-arch-001/design.md` §10:

1. **Protobuf policy.** `egopb` and `proto.Message` remain a supported public contract for v4, and are revisited at the next major. The neutral contracts keep `proto.Message` as their command, event and state type.
2. **Compatibility.** Deprecate first; nothing breaks inside v4. Every temporary alias and deprecated API (`EntityKind`, the S1 publisher aliases, and everything S3 deprecates) is removed at the major release introduced by #124, not earlier.

## Approach

- **New contracts under `port/behavior`**: `EventSourced`, `DurableState` and `Saga` (plus the envelope variants, `SagaAction` and `SagaCommand`). They have the same domain methods and `ID()`, but no GoAkt type. The package imports only the standard library, protobuf and `command`, so archcheck's `contract-allowlist` covers it with no exception.
- **Old names stay, unchanged.** `ego.EventSourcedBehavior` becomes `interface{ behavior.EventSourced; extension.Dependency }`, with the same method set as today, so apidiff reports no change. The same applies to the other two. They are marked `Deprecated:` in the last slice.
- **Cluster mode: serialization becomes an optional capability.** At spawn time, the engine checks whether a behavior also implements `MarshalBinary`/`UnmarshalBinary`. If it does, GoAkt receives the behavior itself, exactly as today, so rolling upgrades keep working. If it does not, the behavior runs locally inside an internal wrapper. In cluster mode, where GoAkt serializes every spawn, the engine returns the typed error `*ego.BehaviorPlacementError` (matching `ego.ErrBehaviorNotSerializable`) before anything is spawned.
- **New entry points**: `Engine.SpawnEventSourced`, `SpawnDurableState` and `SpawnSaga` take the neutral contracts. `Entity`, `DurableStateEntity` and `Saga` keep their signatures and delegate.
- **Kind registration**: the new `ego.BehaviorKind` (standard-library method set, identical to `extension.Dependency`) and `ego.WithBehaviorKinds`. `EntityKind`/`WithEntityKinds` keep working and are deprecated.
- **A latent panic goes away.** GoAkt's type registry panics on non-pointer behaviors, while holding the actor-system lock (observed in a spike, `design.md` §2.3). The engine no longer hands such values to the registry.

### Alternatives rejected (details in `design.md` §4)

- **Removing the embed in place, or widening `Engine.Entity`'s parameter.** apidiff reports both as incompatible, and both are real source breaks for callers. This violates decision 2.
- **A kind registry with factories, or an engine-built wrapper that serializes kind + ID + payload.** Both change the wire format, which breaks mixed-version clusters during a rolling upgrade. Both also need a behavior lookup by name inside the actor. A kind registry also needs a new public factory API.
- **A process-global registry.** It is hidden global state, and it cannot hold the per-node registrations of the existing two-node test, which runs both nodes in one process.

## In scope

- The `port/behavior` package and the compatibility shape of package `ego`.
- The adapter bridge at the three spawn sites and `NewEngine`, the typed errors, and the new entry points and kind option.
- Deprecation markers, a `CHANGELOG.md` migration note, and migrating the examples (removing serialization code where it is no longer needed).
- Tests: a two-node remote-spawn test, single-node tests for domain-only behaviors, and apidiff plus a consumer-program comparison.

## Out of scope

- The in-memory runtime or test double (#103 criteria 4–5; #11 `RUNTIME-005`).
- The runtime SPI and separating the GoAkt adapter (#11, slice S4).
- The composition root (#105).
- Placing non-serializable behaviors in a cluster by factory (#11 `RUNTIME-003`, if ever needed).
- Typed wrapping of GoAkt's remote "dependency type not registered" error (named follow-up, `design.md` §12).
- Removing deprecated symbols (#124).

## Affected public surfaces

All changes are additive or deprecations. The full old/new table is in `design.md` §6. apidiff is expected to report only the additions, plus the known cross-package-alias false positive for `SagaAction`/`SagaCommand` that S1 already documented. A consumer program built against `77beda6` decides compatibility, as it did for S1.

## Implementation slices

Five pull requests, each about 400 changed lines or fewer, with file ownership in `design.md` §9:

| Slice | Content | Order |
|---|---|---|
| S3-1 | `port/behavior` contracts; `ego` names re-expressed on top of them | First; independent of #105 |
| S3-2 | Spawn-site bridge in `engine.go`, internal wrapper, typed errors, actors on neutral types | Before #105 IMPL-4 |
| S3-3 | `Spawn*` entry points (new file) and the two-node remote-spawn test | After S3-2 |
| S3-4 | `BehaviorKind`/`WithBehaviorKinds`; `NewEngine` check; the mixed-registration two-node subtest | After S3-2 (and S3-3); before #105 IMPL-4 |
| S3-5 | Deprecations, `CHANGELOG.md`, examples | Last |

## Archcheck

No new baseline entry and no exception. The `migration -> ego` entry (owned by S4/#11) does not change. `migration` uses package `ego` only for `ego.ResolveLogger` (`migration/migration.go:149` and `migration/tenant_adoption.go:516`), which is unrelated to behavior contracts.

## Rollback

This change is documentation only. For the slices: S3-1, S3-3 and S3-4 are additive and can be reverted until a release contains them. After that, `port/behavior` stays for the rest of v4, as `port/publishing` does. S3-2 is internal wiring. S3-5 changes comments, examples and the changelog.

## Risks

- **Two ways to spawn during v4.** Consumers see both `Entity` and `SpawnEventSourced`. The mitigation is `Deprecated:` markers, a changelog table, and migrated examples.
- **Conflicts with #105 in `engine.go`.** The mitigation is the ordering constraint above, an in-place rename in S3-2 so the method bodies do not move, and new entry points in a new file.
- **Rolling upgrade.** The claim that the wire format is unchanged rests on the pass-through identity test and the mixed-registration two-node test. A cross-binary upgrade is not tested.

## Decisions for the human

1. **Reading of #123's first criterion. Decided 2026-09-27.** In v4 it is met by `port/behavior`. The old `ego` names keep the embed, deprecated, until #124, because removing it now is an apidiff-incompatible break (decision 2).
2. **New names. Decided 2026-09-27.** Confirmed as proposed: `port/behavior`, `Spawn*`, `BehaviorKind`/`WithBehaviorKinds`, `BehaviorPlacementError`. The alternative names were rejected.

## Success criteria for this docs change

- [ ] `design.md` explains the cluster-mode serialization path from GoAkt's source, evaluates the options, and chooses one with reasons.
- [ ] Every public API change is listed with old/new signatures, deprecation markers and an apidiff expectation.
- [ ] The remote-spawn test and the typed error are specified.
- [ ] The slices have file ownership and state the ordering with #105 IMPL-4.
- [ ] The two human decisions are recorded in `ego-arch-001/design.md` §10.
- [ ] No production or test code changes.
