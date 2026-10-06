# Tasks — EventStore tenant isolation (EGO-TENANT-003)

Tracker `#92`, epic `#23`. All six slices this change was cut into (T1–T6)
are complete, each committed on `feat/ego-tenant-003-eventstore-isolation`.
The remaining isolation work — read-side/projection isolation, an
administrative bypass path, and tenant-qualified actor identity — does not
fit in this change; it is named below as an explicit follow-up chain of
later, separately-authorized SDD changes. T7 is a later addition: two P1
defects a review of the pull request for this branch (`getsyntegrity/ego#98`)
found in T6's own tenant-adoption tool, fixed on the same branch.

| Task | Summary | Commit |
| --- | --- | --- |
| T1 | Introduce `persistence.Scope` | `0154133` |
| T2 | Extend the tenant-aware SPI (`EventsStore`/`StateStore`/`SnapshotStore` take `Scope`) | `787550a` |
| T3 | Cross-tenant store conformance suite | `5b1d16f` |
| T4 | Engine/actor wiring | `99eccad` |
| T5 | Migration and compatibility documentation | `c3a4ece` |
| T6 | Tenant adoption tool for existing `Unscoped()` data | `40a7d0b` |
| T7 | Review fix: full-record verification before source deletion, `PersistenceIDs` pagination off-by-one | this commit |

## T1 — Introduce `persistence.Scope`

- [x] 1.1 Write `persistence/scope_test.go` first (strict TDD is in force
      for this repository). Covers: zero value invalid; `Unscoped()` and a
      tenant scope both valid; `NewTenantScope` rejects an empty
      `tenancy.TenantID` with `ErrInvalidScope`; `Unscoped()` is never
      `Equal()` to a tenant scope in either direction; two tenant scopes
      compare equal iff their `tenancy.TenantID` is equal; `IsUnscoped()`
      is true only for `Unscoped()`; `TenantID()` round-trips for a tenant
      scope and is the zero value when unscoped; `String()` distinguishes
      the two kinds; a tenant literally named `"unscoped"` still does not
      equal `Unscoped()` (the forging guard — see design.md D3).
      Req: `specs/persistence-tenant-isolation/spec.md` — "Explicit Tenant
      Scope At The Boundary", "Effective Identity Is The Pair (Scope,
      persistence_id)", "Unscoped Backward Compatibility And
      Non-Collision".
- [x] 1.2 Ran the test suite before `scope.go` existed and observed it
      fail to compile (RED): `go test -mod=vendor ./persistence/...` →
      `undefined: persistence.Scope`, `undefined: persistence.Unscoped`,
      `undefined: persistence.NewTenantScope`,
      `undefined: persistence.ErrInvalidScope` (10 errors, capped at "too
      many errors" by the compiler).
- [x] 1.3 Implement `persistence/scope.go`: `Scope` value type (unexported
      `kind`/`tenant` fields, invalid zero value), `Unscoped()`,
      `NewTenantScope(tenancy.TenantID) (Scope, error)`, `ErrInvalidScope`
      sentinel, `Valid()`, `IsUnscoped()`, `TenantID()`, `Equal()`,
      `String()` — mirroring `persistence/precondition.go`'s conventions
      exactly (unexported fields, named constructors, `Valid()`,
      `String()`), per design.md D1–D3.
      Req: same as 1.1, plus "tenant_metadata Stays Non-Authoritative"
      (this task does not touch `tenant_metadata` handling at all, which
      is itself the evidence for that requirement at this slice).
- [x] 1.4 Ran the test suite again and observed it pass (GREEN):
      `go test -mod=vendor ./persistence/... -run TestScope -v` → 10/10
      `PASS`, then the full package `go test -mod=vendor ./persistence/...`
      → `ok`.
- [x] 1.5 Confirmed `tenancy` gained no dependency:
      `go test -mod=vendor -run TestTenancyArchitecture .` → `PASS`
      (import is one-directional, `persistence` → `tenancy`, per
      design.md D5).
- [x] 1.6 `go build -mod=vendor ./...` and
      `go vet -mod=vendor ./persistence/...` both clean.
- [x] 1.7 Wrote this proposal, `design.md`,
      `specs/persistence-tenant-isolation/spec.md`, and this task file.

**Evidence**: `persistence/scope.go`, `persistence/scope_test.go`. All
four verification commands captured in the apply/commit record for this
change (`go build -mod=vendor ./...`, `go test -mod=vendor
./persistence/...`, `go test -mod=vendor -run TestTenancyArchitecture .`,
`go vet -mod=vendor ./persistence/...`) — all clean. No production
interface signature was changed; `Scope` exists as an unused-by-any-store
type at the end of this slice, exactly as scoped.

## T2 — Extend the tenant-aware SPI

- [x] 2.1 Changed every record-addressing method on `EventsStore`
      (`WriteEvents`, `DeleteEvents`, `ReplayEvents`, `GetLatestEvent`,
      `PersistenceIDs`), `StateStore` (`WriteState`, `GetLatestState`), and
      `SnapshotStore` (`WriteSnapshot`, `GetLatestSnapshot`,
      `DeleteSnapshots`) to take a `scope Scope` parameter immediately
      after `ctx`, mirroring `WritePrecondition`'s placement (design.md,
      "What this slice deliberately leaves open for T2-T5"). `Connect`,
      `Disconnect`, `Ping`, `GetShardEvents`, and `ShardOffsets` were left
      unchanged, deliberately: the first three are connection lifecycle,
      not record addressing; the last two are shard-level projection
      reads, whose tenant isolation is EGO-TENANT-004's scope.
      Req: `specs/persistence-tenant-isolation/spec.md` - "Effective
      Identity Is The Pair (Scope, persistence_id)".
- [x] 2.2 Re-keyed the in-repo `testkit` stores (`EventStore`,
      `DurableStore`, `SnapshotStore`; `testkit/eventstore.go`,
      `testkit/durablestore.go`, `testkit/snapshotstore.go`) structurally on
      `(Scope, persistenceID)` - never on `Scope.String()` or a
      concatenated string - with an invalid zero-value `Scope` rejected via
      `ErrInvalidScope` before any state is touched. The WRITE-004
      CAS/precondition compare now happens within a scope, so
      `ExpectGenesis()` succeeds for a second tenant on a `persistenceID`
      the first tenant already owns; `testkit/scope_test.go` (new, 238
      lines) pins this directly.
- [x] 2.3 Gave `persistence.ConflictError` a required `Scope` constructor
      parameter (`NewConflictError(scope, persistenceID, expected, ...)`,
      recovered via `(*ConflictError).Scope()`) and extended its canonical
      wire grammar and `ParseConflictError` with the scope
      (`persistence/conflict.go`, `persistence/conflict_test.go`). Review of
      #98 found the first, unversioned `scope=` rendering ambiguous for valid
      tenant ids containing `", persistence_id="`; it is now `grammar=v1`
      with both identifiers `strconv.Quote`d, an exact inverse pinned by
      `TestParseConflictErrorRoundTripsAdversarialIdentifiers` and
      `FuzzParseConflictErrorRoundTrip`. Older renderings are rejected, not
      reconstructed, and still classify by the sentinel prefix.
- [x] 2.4 Hand-updated the three generated mocks
      (`mocks/persistence/events_store.go`, `snapshot_store.go`,
      `state_store.go`) and the ad-hoc test fakes
      (`preconditionSpyEventsStore`, `slowEventsStore`, `slowStateStore`) to
      the new signatures, and updated every production call site (actors,
      engine, `migration/migration.go`, saga) to pass `persistence.Unscoped()`
      for now, each marked `// TENANT-003 T4: carries the resolved tenant
      scope once entity actors bind one at spawn.` - so this slice is
      observably a no-op for existing non-tenant deployments.
      `example/cluster` is a separate Go module excluded from the main
      build/test; its `PostgresEventStore.WriteEvents` was already stale on
      the pre-WRITE-004 signature before this change and was left as is.
- [x] 2.5 `go build -mod=vendor ./...` and the full `go test -mod=vendor
      ./...` (excluding `example/cluster`) both clean after the call-site
      updates; 45 files touched across production code, tests, and mocks
      (see the commit's own `--stat`).

**Evidence**: commit `787550a`. Production:
`persistence/events_store.go`, `persistence/state_store.go`,
`persistence/snapshot_store.go`, `persistence/conflict.go`,
`testkit/eventstore.go`, `testkit/durablestore.go`,
`testkit/snapshotstore.go`, `migration/migration.go`, plus every actor and
engine call site listed in the commit. Tests: new `testkit/scope_test.go`,
`persistence/conflict_test.go` extended, and every existing test file that
called a store method updated to the new signature (see the commit's
`--stat` for the full 45-file list). The interface change alone is
sufficient evidence of the break: any pre-T2 implementation of
`EventsStore`/`StateStore`/`SnapshotStore` fails to compile against this
package, exactly as the doc comments' manual-verification recipe
describes.

## T3 — Cross-tenant conformance suite

- [x] 3.1 Added `persistence/conformance`, a store-agnostic,
      factory-driven suite (`RunEventsStoreConformance`,
      `RunStateStoreConformance`, `RunSnapshotStoreConformance`, each
      taking a `func(t *testing.T) <Store>` that must return a fresh, empty
      store per subtest) proving EGO-TENANT-003's isolation requirements
      directly against `EventsStore`, `StateStore`, and `SnapshotStore`
      implementations, independent of any actor or mailbox - the issue's
      own acceptance-criteria wording, "tests cross-tenant
      direct-store/conformance independientes del mailbox del actor".
      Req: `specs/persistence-tenant-isolation/spec.md`.
- [x] 3.2 The matrix (`persistence/conformance/events.go`,
      `state.go`, `snapshot.go`) covers, per store: read isolation
      (`ReadIsolation/OtherTenantGetsNothing`,
      `ReadIsolation/UnscopedAndTenantDoNotCrossRead`,
      `ReadIsolation/BothTenantsReadTheirOwnRecord`), write isolation
      including scoped delete (`WriteIsolation/OtherTenantWriteLeavesRecordUntouched`,
      `WriteIsolation/DeleteIsScoped`), WRITE-004 CAS semantics preserved
      per scope (`CAS/ExpectGenesisSucceedsForNewTenantOnEstablishedID`,
      `CAS/ExpectRevisionConflictCarriesItsScope`,
      `CAS/ConflictInOneScopeNotObservableInAnother`), `PersistenceIDs`
      enumeration isolation for `EventsStore`
      (`Enumeration/PersistenceIDsScopedToOwnTenant`), and the
      `Unscoped()`-vs-tenant-named-"unscoped" forging guard
      (`Unscoped/NeverCollidesWithTenantNamedUnscoped`) on all three.
- [x] 3.3 Connected/disconnected around every subtest
      (`persistence/conformance/check.go`'s `runConformance`), skipping a
      subtest (not failing it) when `Connect` reports the store
      unreachable, so an external adapter's CI with no live database is
      never misread as a passing isolation proof.
- [x] 3.4 Wired all three suites into the in-repo stores
      (`testkit/conformance_test.go`: `TestEventStoreConformance`,
      `TestDurableStoreConformance`, `TestSnapshotStoreConformance`),
      proving both that the suite is usable end to end and that T2's
      implementations are correct.
- [x] 3.5 Wrote the suite's own permanent self-check,
      `TestConformanceCatchesNonIsolatingStore`
      (`testkit/conformance_test.go`), which runs the same named checks
      (via `CaptureEventsStoreChecks`/`CaptureStateStoreChecks`/
      `CaptureSnapshotStoreChecks`, `persistence/conformance/check.go`)
      against wrappers that collapse every caller-supplied `Scope` to
      `Unscoped()` before delegating to an otherwise-correct store -
      exactly the shape of a naive tenant_metadata-only adapter. Verified
      RED first, directly: every one of the 10 event checks genuinely
      failed against the non-isolating wrapper before the capturing
      harness existed, then GREEN once the capture harness asserted on the
      expected failures instead of propagating them.

**Evidence**: commit `5b1d16f`. New package:
`persistence/conformance/{doc,check,events,state,snapshot,helpers}.go`
(1,134 lines added). Wiring and self-check: `testkit/conformance_test.go`
(194 lines added).

## T4 — Engine/actor wiring

- [x] 4.1 **Superseded by 4.8 below — kept for history, do not re-implement
      this shape.** The first cut resolved the caller's real
      `tenancy.TenantContext` at spawn time in
      `Engine.Entity`/`Engine.DurableStateEntity`/`Engine.Saga`
      (`engine.go`'s `resolveSpawnTenantScope`), and injected it as a new
      per-spawn dependency, `internal/extensions.EntityTenantScope`, rather
      than resolving inside the actor itself. A resolved
      `tenancy.ScopeAdministrative` context was refused outright with a
      (now-removed) `ErrAdministrativeScopeEntitySpawn` sentinel; legacy
      mode (no resolver configured) injected nothing and was
      byte-identical to before. **This called `TenantResolver.Resolve` at
      spawn, which CI caught as a violation of
      `openspec/specs/tenancy-core/spec.md`'s Resolve-Once,
      Propagate-After requirement — see 4.8.**
- [x] 4.2 Each of `EventSourcedActor`, `DurableStateActor`, and `SagaActor`
      gained a `scope persistence.Scope` field, bound once in `PreStart`
      via a new `resolveScope` method — called before `loadOptionalExtensions`/
      `setConfig` and before any store read, including recovery. Legacy
      mode binds `persistence.Unscoped()`; tenant-aware mode reads the
      injected `EntityTenantScope` dependency and fails closed with the new
      `ErrEntityTenantScopeMissing` sentinel when it is absent or invalid —
      no store read or write ever happens in that case.
- [x] 4.3 Threaded `scope` onto the request structs the parent actor sends
      its child persistence actors, since those are separate actors with no
      access to the parent's own dependencies: `persistEventsRequest.scope`
      (`events_writer_actor.go`), `persistSnapshotRequest.scope`
      (`snapshots_writer_actor.go`), `applyRetentionRequest.scope`
      (`events_janitor_actor.go`). Every store call in all three child
      actors now passes the caller-scoped value instead of a hardcoded
      `persistence.Unscoped()`.
- [x] 4.4 Turned recovered `tenant_metadata` from a first-seed into a
      cross-check (D6 strengthening): since `resolveScope` pre-seeds
      `actorTenant`/`boundTenant` from the spawn-bound tenant before
      recovery runs, a recovered record whose `tenant_metadata` disagrees
      with the spawn-bound tenant now fails closed via
      `tenancy.VerifyUnchanged` instead of silently being adopted as the
      actor's identity.
- [x] 4.5 `Engine.EraseEntity` now resolves the caller's tenant and scopes
      its erasure to it, instead of unconditionally calling the stores
      with `persistence.Unscoped()` regardless of who called it — the
      exact cross-tenant erasure hole this ticket exists to close. A
      resolver error, or a resolved context carrying no tenant identity
      (administrative scope, or invalid), fails the erasure closed; legacy
      mode is unchanged.
- [x] 4.6 Wrote the tests the implementation above did not yet have (strict
      TDD: for each, the corresponding production line was temporarily
      reverted, the test observed to fail (RED) for that specific reason,
      then the line was restored and the test observed to pass (GREEN)):
      - `TestEventSourcedActorSpawnBindsExactTenantScope`
        (`event_sourced_actor_scope_test.go`) — a tenant-scoped entity's
        recovery read and command write both carry its exact tenant
        `Scope`, proven with a `*mocks.EventsStore` whose expectations name
        that scope explicitly (not `mock.Anything`). RED: hardcoding
        `persistence.Unscoped()` in `events_writer_actor.go`'s `WriteEvents`
        call made the mock reject the unexpected-scope call (command timed
        out). GREEN after restoring.
      - `TestEventSourcedActorPreStartFailsClosedWithoutTenantScope`
        (same file) — tenancy active, no `EntityTenantScope` dependency
        injected: `PreStart` fails with `ErrEntityTenantScopeMissing`
        before `Ping`/`GetLatestEvent`/`WriteEvents` are ever called
        (`store.AssertNotCalled`). RED: removing `resolveScope`'s
        fail-closed default (falling back to `Unscoped()` instead of
        returning the sentinel) caused the mock to panic on an
        unexpected `Ping` call. GREEN after restoring.
      - `TestEventSourcedActorLegacyModeAlwaysUsesUnscopedStore` (same
        file) — no resolver configured: every store call still carries
        `persistence.Unscoped()`, unchanged for existing non-tenant users.
        RED: changing the legacy branch to bind an invalid
        `persistence.Scope{}` instead of `Unscoped()` made the mock panic
        on the mismatched-scope call. GREEN after restoring.
      - `TestEventSourcedActorRecoverRejectsMismatchedSpawnBoundTenant`
        (`event_sourced_actor_tenant_persist_test.go`) — a record stored
        under the actor's own spawn-bound scope but whose `tenant_metadata`
        names a different tenant fails recovery with
        `tenancy.ErrDenied`. RED: temporarily skipping the `actorTenant`
        pre-seed in `resolveScope` made the test's expected error vanish
        (recover silently adopted the foreign tenant). GREEN after
        restoring.
      - `TestEngineEntitySpawnRejectsAdministrativeScope`
        (`engine_tenant_spawn_test.go`) — **superseded by 4.8's
        `TestEngineEntitySpawnRequiresExplicitTenantWhenResolverHasNoFixedTenant`,
        which replaced this test file's contents once spawn stopped
        resolving anything.** Originally: an administrative-scope resolver
        blocked both `Engine.Entity` and `Engine.DurableStateEntity` with
        `ErrAdministrativeScopeEntitySpawn`, and no actor was ever spawned
        (`EntityExists` stayed `false`). RED: making the administrative
        branch of `resolveSpawnTenantScope` return `(nil, nil)` (i.e. fall
        through as legacy) surfaced a different failure
        (`ErrEntityTenantScopeMissing` from the actor's own fail-closed
        guard) instead of the expected sentinel — still a clear failure.
        GREEN after restoring.
      - `TestEngineEraseEntityCannotEraseAnotherTenantsRecord`
        (`engine_erase_entity_tenant_test.go`) — two records under the
        same `persistence_id` but different tenant scopes; erasing as
        tenant A removes only tenant A's record and leaves tenant B's
        untouched. RED: disabling `EraseEntity`'s tenant-resolution branch
        (falling back to unconditional `Unscoped()`) made tenant A's own
        erasure silently no-op, since `Unscoped()` no longer matches any
        tenant-scoped record — failing the first assertion. GREEN after
        restoring.
- [x] 4.7 Documented the residual limitation this slice deliberately leaves
      open: a GoAkt actor's name is still the bare `entityID`/`sagaID`, not
      tenant-qualified, so two tenants sharing the same id still contend
      for one actor. Added a "Known limitation" section to `design.md`, a
      matching non-goal/requirement pair to
      `specs/persistence-tenant-isolation/spec.md`, and confirmed (via
      `rg -n 'Known limitation|not tenant-qualified' *.go`) that the
      production doc comments on `EventSourcedActor.scope`,
      `DurableStateActor.scope`, and `SagaActor.scope` already carried this
      note.

- [x] 4.8 **CI correction (EGO-TENANT-003, issue #92 / PR #98 review):** CI
      caught that 4.1's spawn-time design called
      `engine.tenantResolver.Resolve(ctx)` at spawn, violating
      `openspec/specs/tenancy-core/spec.md`'s Resolve-Once,
      Propagate-After requirement — `TenantResolver` MUST be invoked
      exactly once, at the trust boundary, never at spawn. Two tests
      proved it: `TestSendCommandResolverSwapIdenticalSequence`'s
      multi-tenant subtest (`engine_test.go`) asserted the resolver is
      invoked exactly once across one `engine.Entity` call plus one
      `SendCommand` call and observed two; `TestSagaFailsClosed`
      (`saga_test.go`) — spawning its target and saga directly through
      `actorSystem.Spawn`, bypassing the engine — started failing at spawn
      with `ErrEntityTenantScopeMissing` because it had never supplied the
      per-spawn `EntityTenantScope` dependency itself, a gap the earlier
      design's spawn-time `Resolve` call had been masking.

      **This is not a quiet rewrite of 4.1's history — it is a genuine
      design correction, recorded honestly:** `engine.go`'s
      `resolveSpawnTenantScope` is replaced by `spawnTenantScope(config
      *spawnConfig)`, which never calls `Resolve` and instead determines
      the spawn's tenant from `config.tenantID` (set by the new
      `ego.WithTenant(id tenancy.TenantID)` `SpawnOption`,
      `spawn_config.go`) or, absent that, the registered resolver's fixed
      tenant via the new `tenancy.FixedTenantResolver` capability
      interface (`FixedTenant() (TenantID, bool)`, implemented by
      `tenancy.WithSingleTenant`'s resolver so single-tenant mode still
      needs no `WithTenant` — acceptance criterion 6). Neither source
      yielding a tenant fails the spawn closed with the new
      `ErrSpawnTenantUndetermined`, replacing the removed
      `ErrAdministrativeScopeEntitySpawn` (which existed only to name the
      outcome of resolving an administrative context at spawn — moot once
      spawn resolves nothing). `Engine.Saga` gained a trailing `opts
      ...SpawnOption` parameter (additive, non-breaking) purely as
      `WithTenant`'s carrier. `Dispatch`, `SagaStatus`, and `EraseEntity`
      — the actual trust boundaries — and the actor-side `resolveScope`/
      `PreStart`/`EntityTenantScope` machinery are unchanged; see
      `design.md`'s "CI correction" section for the full account.

      Fixed, with reasons: `TestSendCommandResolverSwapIdenticalSequence`
      now spawns its multi-tenant subtest's entity with
      `WithTenant("acme")` (the single-tenant subtest still needs nothing,
      proving criterion 6), and its resolver-count assertion moved from 2
      to 1. `TestSagaFailsClosed` now supplies
      `extensions.NewEntityTenantScope("acme")` on both of its direct
      `actorSystem.Spawn` calls (target and saga), matching what
      `Engine.Entity`/`Engine.Saga` would inject given `WithTenant`; its
      original assertions (no `HandleEvent`, no `HandleCommand`, no
      persisted event) are unchanged — where it fails closed moved
      earlier (spawn) only incidentally, because it had never carried the
      dependency the corrected design also requires. `option_test.go`'s
      `erroringTenantResolver`/`zeroValueTenantResolver` lost their
      `succeedID` escape hatch (spawn no longer calls `Resolve`, so
      nothing needs to be let through); every call site across
      `engine_test.go` and `tenant_write_path_e2e_test.go` that spawned an
      entity/saga under a non-fixed resolver gained an explicit
      `WithTenant(...)`, and every resolver-call-count assertion that
      counted "one spawn-time resolve plus N command resolves" was
      corrected to count N alone. `engine_tenant_spawn_test.go` was
      rewritten from testing administrative-scope rejection at spawn (now
      structurally impossible, since spawn never resolves) to testing the
      corrected contract directly.

      New tests (strict TDD; RED observed by temporarily reintroducing the
      old behavior, then reverting and observing GREEN):
      - `TestEngineEntitySpawnWithExplicitTenantResolvesOnce`
        (`engine_tenant_spawn_test.go`) — the dedicated regression guard:
        a multi-tenant resolver plus `WithTenant`, spawn then one command,
        resolver called exactly once. RED: temporarily reintroducing a
        `tenantResolver.Resolve(ctx)` call inside `Engine.Entity`
        reproduced the exact CI failure (count 1 expected, 2 observed;
        `TestSendCommandResolverSwapIdenticalSequence`'s multi-tenant
        subtest failed identically under the same temporary change).
        GREEN after reverting.
      - `TestEngineEntitySpawnRequiresExplicitTenantWhenResolverHasNoFixedTenant`
        (same file) — tenancy active, no fixed tenant, no `WithTenant`:
        `Engine.Entity`/`DurableStateEntity` fail closed with
        `ErrSpawnTenantUndetermined` before any store method runs (a
        `mocks/persistence` store with zero expectations set, so any call
        at all fails the test). RED: temporarily making
        `spawnTenantScope` return `(nil, nil)` unconditionally (i.e.
        silently fall back to legacy/`Unscoped()`) let the spawn succeed,
        failing the "no actor spawned" assertions. GREEN after reverting.
      - `TestEngineWithSingleTenantSpawnNeedsNoWithTenant` (same file) —
        `tenancy.WithSingleTenant`, no `WithTenant` at spawn, entity still
        spawns and is bound to (and writes under) that resolver's fixed
        tenant scope — criterion 6, checked directly at the spawn
        boundary rather than only through `SendCommand`.
      - `TestEngineEntitySpawnWithoutResolverStaysUnscoped` (same file) —
        no resolver registered at all: spawn needs no `WithTenant`, and
        the store still receives `persistence.Unscoped()`, unchanged from
        legacy behavior.
      - `TestEngineCommandRejectsTenantMismatchWithSpawnDeclaredTenant`
        (same file) — an entity spawned with `WithTenant("acme")` rejects
        a command whose resolver-attached tenant is `"globex"`, via the
        actor's existing `tenancy.VerifyUnchanged` cross-check; proves the
        spawn-declared tenant is enforced, not merely advisory.

**Evidence**: `engine.go`, `spawn_config.go`, `tenancy/resolver.go`,
`event_sourced_actor.go`, `durable_state_actor.go`,
`saga_actor.go`, `events_writer_actor.go`, `snapshots_writer_actor.go`,
`events_janitor_actor.go`, `migration/migration.go`,
`internal/extensions/extensions.go` (production); new test files
`event_sourced_actor_scope_test.go`, `engine_tenant_spawn_test.go`,
`engine_erase_entity_tenant_test.go`, plus a new test added to
`event_sourced_actor_tenant_persist_test.go`; existing tests across
`engine_test.go`, `option_test.go`, `durable_state_actor_test.go`,
`durable_state_actor_tenant_persist_test.go`, `event_sourced_actor_test.go`,
`event_sourced_actor_tenant_persist_test.go`, `saga_actor_tenant_test.go`,
`saga_test.go`, `events_janitor_actor_test.go`, `events_writer_actor_test.go`,
`snapshots_writer_actor_test.go`, and `tenant_write_path_e2e_test.go`
adapted for the corrected (4.8) spawn-time tenant declaration and the
`scope` field. All new tests (6 from 4.6, 5 from 4.8) observed RED (for
the reasons named above) and GREEN. Full verification command results are
recorded in this change's commit(s).

## T5 — Migration and compatibility documentation

- [x] 5.1 Added a Breaking Changes entry to `CHANGELOG.md`'s
      `[Unreleased]` section: the old/new signature for every changed
      method on `EventsStore`, `StateStore`, and `SnapshotStore`; why
      `Connect`/`Disconnect`/`Ping`/`GetShardEvents`/`ShardOffsets` did
      NOT change; that `ConflictError` and its wire grammar gained a
      `scope=` field; the structural-key upgrade recipe (real column,
      never `Scope.String()`); the zero-migration guarantee for a
      deployment that never activates tenancy, and what an operator must
      deliberately decide for a deployment that adopts tenancy on
      existing data; the `persistence/conformance` acceptance-test
      wiring; and the shared-actor-name known limitation.
- [x] 5.2 Added a "Tenant scoping" subsection to `readme.md`'s existing
      `## Persistence` section, pointing at `persistence/conformance` and
      `tenancy.WithSingleTenant`/`ego.WithTenantResolver`, and added it to
      the table of contents.
- [x] 5.3 Reconciled this task file with reality: T2 and T3 were
      committed (`787550a`, `5b1d16f`) but the "Follow-up chain" section
      still listed them as unchecked bullets. Replaced that section with
      completed T2/T3 task blocks carrying their real evidence, and this
      T5 block, plus the acceptance-criteria mapping below.
- [x] 5.4 Verified no production code or test logic changed:
      `go build -mod=vendor ./...` stays clean, and `git diff --stat`
      touches only `CHANGELOG.md`, `readme.md`, and this file.

**Evidence**: `CHANGELOG.md`, `readme.md`, this file
(`openspec/changes/ego-tenant-003/tasks.md`). Commit `c3a4ece`.

## T6 — Tenant adoption tool for existing `Unscoped()` data

T5 documented the compatibility contract and the zero-migration guarantee
for a deployment that never activates tenancy, but shipped no tool for the
harder case T5's own acceptance-criteria note named as unmet: a deployment
that already has data written under `Unscoped()` and now wants to adopt
tenancy. This slice closes exactly that gap.

- [x] 6.1 Read `migration/migration.go`, `migration/option.go`, and
      `migration/migration_test.go` first, to follow the existing
      `Migrator`'s conventions: a `New(stores, opts...)` constructor,
      functional options, a kit-logger `Logger` resolved through
      `ego.ResolveLogger`, a `Ping` preflight on each store, paged
      `PersistenceIDs` enumeration, and a package doc comment with a usage
      example.
- [x] 6.2 Verified the CRITICAL correctness claim in the brief against
      T4's real code before relying on it: `event_sourced_actor.go`'s
      `recover`/`recoverFromSnapshot`/`applyPersistedEvent` each call
      `entity.seedActorTenant`/`tenancy.VerifyUnchanged` against
      `entity.actorTenant`, which `resolveScope` (line 438) pre-seeds from
      the spawn-bound `extensions.EntityTenantScope` dependency *before*
      recovery ever runs (see `resolveScope`'s own doc comment: "turns
      seedActorTenant's later calls ... from a first-seed into a
      cross-check"). The claim held: a copied record with empty or stale
      `tenant_metadata` fails recovery under a tenant-bound actor via
      `tenancy.ErrDenied` (mismatch) or `UnmarshalMetadata`'s `ErrInvalid`
      (missing/malformed) — this is a real, confirmed data-stranding risk
      the tool must avoid, not a hypothetical one.
- [x] 6.3 Wrote `migration/tenant_adoption_test.go` first (strict TDD),
      then ran it and observed RED: `go vet -mod=vendor ./migration/...`
      →` undefined: TenantAssignment` (the type did not exist yet).
- [x] 6.4 Implemented `migration/tenant_adoption.go`: `TenantAdopter`,
      built via `NewTenantAdopter(assign TenantAssignment, opts
      ...AdoptionOption)` where `assign` is a required constructor
      argument (not an option, since the framework cannot decide which
      tenant an existing aggregate belongs to). Options:
      `WithEventsStore`/`WithSnapshotStore`/`WithStateStore` (each
      optional — a nil store skips that record kind, never panics),
      `WithSourceScope` (default `Unscoped()`), `WithScanPageSize`,
      `WithWriteEnabled` (required opt-in; default is dry-run — plans and
      reports, writes nothing), `WithSourceDeletion` (opt-in; deletes a
      source-scope copy only after it was written to the target and read
      back and verified), `WithFailFast`, `WithPersistenceIDs` (explicit
      id list), and `WithAdoptionLogger`. Every write into the target
      scope uses `persistence.ExpectGenesis()` for events and durable
      state (an existing target aggregate can never be silently
      clobbered; the resulting `*persistence.ConflictError` is reported as
      `already_present`, not a crash); `persistence.SnapshotStore.WriteSnapshot`
      has no precondition parameter at all in the SPI, so snapshot
      adoption instead reads the target first and skips the write if
      anything is already there — a documented, unavoidable read-then-write
      window, not an oversight. Every copied record's `TenantMetadata` is
      stamped via `tenancy.MarshalMetadata` of a `tenancy.NewTenantContext`
      built for the target tenant, exactly like the actors stamp it.
      `AdoptionReport` carries per-outcome counts (scanned, assigned,
      skipped-by-assignment, copied, already-present, verified,
      source-deleted, failed) and a `Failures` list, and implements
      `String()` for logging.
- [x] 6.5 Ran `migration/tenant_adoption_test.go` again and observed GREEN:
      `go test -mod=vendor -count=1 -v ./migration/...` → every
      `TestTenantAdopter*` subtest `PASS`, including
      `TestTenantAdopterEndToEndRecoveryThroughRealActor`, which spawns a
      REAL tenant-bound `ego.EventSourcedActor` (via a real `ego.Engine`
      built from `ego.NewConfig`/`goakt.NewActorSystem`/`ego.NewEngine`,
      exactly as production code assembles one) over a legacy event
      adopted into tenant `"acme"`, and proves the recovered balance (100,
      from the migrated event) plus a new credit (50) sums to 150 — the
      actor could only have recovered that starting balance from the
      migrated data, which it could only do if the migrated
      `tenant_metadata` passed T4's cross-check.
- [x] 6.6 Documented the durable-state (and, found while implementing,
      snapshot-store) enumeration gap honestly rather than inventing an
      API: `persistence.StateStore` and `persistence.SnapshotStore` have
      no `PersistenceIDs`-style method, so when no events store is
      configured, `WithPersistenceIDs` is the *only* source of ids —
      stated in both the package doc comment (`migration/migration.go`)
      and `WithPersistenceIDs`'s own doc comment.
      `TestTenantAdopterExplicitPersistenceIDsForDurableStateOnly` proves
      the durable-state-only path works with an explicit id and no events
      store at all.
- [x] 6.7 Also discovered, and documented rather than working around,
      that `persistence.StateStore` has no delete method in the SPI at
      all (unlike `EventsStore.DeleteEvents` and
      `SnapshotStore.DeleteSnapshots`): `WithSourceDeletion` therefore
      never deletes a durable-state source copy, regardless of the
      option, and `adoptState`'s doc comment says so plainly.
- [x] 6.8 Extended `migration/migration.go`'s package doc comment with a
      "Adopting tenancy for existing data" section (usage example, the
      required-`TenantAssignment` rationale, and the enumeration
      limitation) rather than adding a second package doc comment, to
      avoid two competing `// Package migration` blocks in one package.
- [x] 6.9 Ran the full verification suite: `go build -mod=vendor ./...`,
      `go vet -mod=vendor ./...`, `go test -mod=vendor -count=1
      ./migration/... ./persistence/... ./testkit/...` three times, and
      `go test -mod=vendor -count=1 -run
      TestTenantAdopterEndToEndRecoveryThroughRealActor -v ./migration/...`
      twice — all clean, no flake signature encountered. `git diff --stat`
      confirmed only `migration/tenant_adoption.go`,
      `migration/tenant_adoption_test.go`, `migration/migration.go`,
      `CHANGELOG.md`, `readme.md`, and this file were touched.

**Evidence**: `migration/tenant_adoption.go` (new),
`migration/tenant_adoption_test.go` (new, 20 test functions/subtests
covering dry-run-writes-nothing, real-run copies events/snapshot/state and
keeps the source, `tenant_metadata` stamping, the end-to-end real-actor
recovery proof, two-tenant isolation, `ok=false` leaves an aggregate
untouched, idempotent re-run, never-overwrite-an-existing-target,
opt-in source deletion only after verification, per-aggregate failure
does not abort the run, and explicit ids for a durable-state-only
deployment), `migration/migration.go` (package doc extended),
`CHANGELOG.md`, `readme.md`, this file. Commit: `40a7d0b`.

## T7 — Review fix: full-record verification and the `PersistenceIDs` off-by-one

A review of `getsyntegrity/ego#98` (this branch, issue `#92`) found two P1
defects in T6's own tenant-adoption tool. Both were confirmed against the
source before being fixed here; this task records what the review caught
and how each was closed.

- [x] 7.1 **Defect: `PersistenceIDs` pagination silently skipped one id at
      every page boundary.** `testkit/eventstore.go`'s `PersistenceIDs`
      returned `keys[endIndex]` — the first key NOT yet returned — as
      `nextPageToken`, while the following page resumed strictly AFTER
      that same token (`key > pageToken`). The key handed back as the
      token was therefore never itself returned by any page. This is
      PRE-EXISTING on upstream `main` (`e28593c`; the older `Migrator` has
      the same exposure) but became a real data-loss risk here because
      `TenantAdopter.collectPersistenceIDs` drives the adoption scan off
      this exact enumeration: a run could report success while leaving
      some aggregates unadopted.
      Fix: `nextPageToken` is now the LAST key actually RETURNED on the
      page (`persistenceIDs[len(persistenceIDs)-1]`), not the first key
      held back — a cursor over what the caller has consumed, agreeing
      with the unchanged `>` comparison on the next call. Guarded so a
      degenerate `pageSize == 0` call (no items returned) terminates
      instead of looping on an empty page.
      Contract: `persistence.EventsStore.PersistenceIDs`'s doc comment
      (`persistence/events_store.go`) now states the pagination contract
      normatively for every implementation, in this repo or external:
      opaque token, no skip, no duplicate, empty token means done.
      Pinned by: `persistence/conformance/events.go`'s new
      `Enumeration/PersistenceIDsPaginationCoversEveryIDExactlyOnce`
      check, which writes 13 ids at page size 4 (forcing 4 pages) and
      asserts the collected set equals exactly what was written, no
      duplicates — run via `RunEventsStoreConformance`/wired into
      `testkit/conformance_test.go`'s `TestEventStoreConformance` like
      every other check in the suite. RED observed directly: reverting
      the token fix and re-running just this subtest reproduced the exact
      skip (`pagination-id-004` and `pagination-id-009` missing from the
      collected set at page size 4/total 13). GREEN after restoring.
      Direct regressions: `testkit/stores_test.go`'s
      `TestEventStore_PersistenceIDsPaginationExhaustive` (10 ids, page
      size 3) and a strengthened assertion in the existing
      `TestEventStore_PersistenceIDs`. Adopter-level regression:
      `migration/tenant_adoption_test.go`'s
      `TestTenantAdopterAdoptsEveryAggregateAcrossMultiplePages` (13
      aggregates, `WithScanPageSize(4)`) asserts every one is scanned and
      adopted.
- [x] 7.2 **Defect: source deletion happened after a verification that
      could not detect corruption.** `adoptEvents`/`adoptSnapshot`/
      `adoptState` (`migration/tenant_adoption.go`) verified a copy by
      comparing only a proxy field — event count
      (`len(written) != len(sourceEvents)`), snapshot sequence number, or
      durable-state version number — then, for events and snapshots,
      deleted the SOURCE scope on success. A faulty adapter, a truncated
      write, or a write that dropped the payload, `tenant_metadata`, or an
      encryption envelope satisfied all three old checks, so the source
      was destroyed anyway. `adoptState` never deletes (no delete method
      exists on `persistence.StateStore`), but its verification was
      exactly as wrong and fed `AdoptionReport.Verified` just the same.
      Fix: every kind now verifies the FULL record — a `proto.Equal` match
      against the exact record this tool intended to write (the source
      record with `tenant_metadata` replaced by the target's). Events are
      matched by `SequenceNumber` via the new `verifyEventsMatchBySequence`
      helper, not by slice position or count, so a store that reorders or
      corrupts a payload while preserving the count/sequence no longer
      passes.
      Tests (written first per this repo's strict TDD, RED observed, then
      GREEN): `migration/tenant_adoption_test.go` gained
      `corruptingEventsStore`/`corruptingSnapshotStore`/
      `corruptingStateStore` — adversarial store wrappers that corrupt a
      write to the TARGET scope only (dropped payload/`tenant_metadata`
      for events; a different state payload for snapshot and durable
      state) while preserving exactly the field the old check compared
      (count, sequence number, version number). Three new tests
      (`TestTenantAdopterEventsVerificationCatchesCorruptedWrite`,
      `TestTenantAdopterSnapshotVerificationCatchesCorruptedWrite`,
      `TestTenantAdopterStateVerificationCatchesCorruptedWrite`) assert
      the aggregate is reported failed, the failure names the persistence
      id, and — for events and snapshot, with `WithSourceDeletion()`
      requested — the source is NOT deleted. RED observed directly:
      temporarily restoring the old proxy checks made all three tests
      fail exactly as expected (`report.Failed == 0`, `SourceDeleted == 1`
      for events, corruption silently accepted). GREEN after restoring the
      full-record checks.
      Docs corrected to match reality (they previously said "read back and
      verified" without stating the check was a proxy, which was
      technically true but easy to over-read as the current state):
      `migration/tenant_adoption.go`'s `StatusSourceDeleted`,
      `AdoptionReport.Verified`, and `WithSourceDeletion` doc comments;
      `migration/migration.go`'s package doc; `CHANGELOG.md`'s TENANT-003
      entry.
- [x] 7.3 Ran the full verification suite: `go build -mod=vendor ./...`,
      `go vet -mod=vendor ./...` clean; `go test -mod=vendor -count=1
      ./migration/... ./persistence/... ./testkit/...` three times, all
      clean; `go test -mod=vendor -count=1 -v ./testkit/ -run Conformance`
      shows the new pagination subtest passing alongside the existing
      ones; `golangci-lint` reports the same 14 pre-existing `revive`
      findings in `command/errors.go`/`tenancy/errors.go` and zero new
      findings.

**Evidence**: `persistence/events_store.go` (contract doc comment),
`testkit/eventstore.go` (pagination fix), `persistence/conformance/events.go`
(new conformance check), `testkit/stores_test.go` (direct regression),
`migration/tenant_adoption.go` (full-record verification,
`verifyEventsMatchBySequence`, corrected doc comments),
`migration/tenant_adoption_test.go` (adversarial wrappers, 5 new tests),
`migration/migration.go` (package doc corrected), `CHANGELOG.md`. Refs `#92`.
Commit: this commit (see `git log -1` on
`feat/ego-tenant-003-eventstore-isolation` for its SHA).

## T8 — PR #98 review round 2 and the spawn-contract correction

Base rebased onto `main@930b097` (#100, `requireExtension[T]`); every actor
`PreStart` still orders `requireExtension` → `tenantAware` →
`resolveScope` → recovery/store access.

- [x] 8.1 An existing target is `already_present` only when proven to be
      this adoption, otherwise `errTargetNotEquivalent`: exact comparison
      while the source exists (a later snapshot/state proves no lineage and
      fails), and a per-record adoption receipt (`ego.adoption.receipt`,
      SHA-256 of source scope plus deterministic encoding) once
      `WithSourceDeletion` removed it. Tenant ownership alone never counts.
      Read-back with a duplicate sequence row no longer verifies. Evidence:
      `TestTenantAdopterSourceDeletingReRunIsIdempotent`,
      `...SnapshotOnlyReRunAfterDeletionIsIdempotent`,
      `...MissingSourceClassification`,
      `...LaterSameTenantTargetIsNotEquivalent`,
      `...SamePositionTargetClassification`,
      `...ReceiptProvesAdoptionAfterSourceDeletion`,
      `...EventsVerificationRejectsDuplicateSequenceRows`.
- [x] 8.2 `ConflictError` wire grammar is `grammar=v1` with quoted
      identifiers (see 2.3).
- [x] 8.3 Spec/implementation contradiction found and fixed in code, per
      the ratified spec: a local re-spawn of a live id under a different
      tenant returned nil (GoAkt returns the running PID). The engine now
      checks the returned actor's spawn binding and fails with
      `ErrSpawnTenantMismatch`. Evidence:
      `TestEngineRespawnUnderAnotherTenantIsRejected` (entity, durable state,
      saga), `TestEngineConcurrentCrossTenantSpawnHasExactlyOneWinner`
      (also under a targeted `-race`), `TestEngineRespawnInLegacyModeIsUnchanged`.

- [x] 8.4 Codex review of `063b212`: a remote PID's binding is read through
      goakt's `RemoteDependencies`, which reports failure as "no
      dependencies", so it is retried and an unreadable binding is
      `ErrSpawnTenantUnverified` rather than a false mismatch
      (`TestVerifyTenantBindingDistinguishesMismatchFromUnverifiable`);
      `WithSourceDeletion` re-reads the source around each deletion and never
      reports a source that grew as deleted
      (`TestTenantAdopterSourceDeletionRefusesSuccessUnderConcurrentWrites`,
      `TestTenantAdopterSnapshotDeletionRefusesSuccessUnderConcurrentWrites`);
      `WithScanPageSize(0)` is rejected
      (`TestNewTenantAdopterRejectsZeroScanPageSize`).
- [x] 8.5 Codex review of `ef94bbc`: a deleting run also removes the source
      of a target an earlier run already adopted exactly
      (`TestTenantAdopterDeletesSourceOfVerifiedExistingTarget`), and the
      pre-delete re-read compares the full source records, so a record
      rewritten at the same sequence number blocks deletion
      (`TestTenantAdopterRefusesDeletionOfReplacedSameSequenceSnapshot`,
      `TestTenantAdopterRefusesDeletionOfRewrittenSourceEvent`).
- [x] 8.6 Review of `3206f52` (user contract): the spawn check asks the
      owning actor (`egopb.TenantBindingQuery`, one shared handler), resolves
      cluster `ErrActorAlreadyExists` in tenant-aware mode, and `Dispatch`
      rejects the query (`TestEngineRemoteSpawnTenantBinding` on a real
      two-node cluster, mutation-checked). A write-enabled adoption requires
      an `AdoptionFence` held for source and target, in a fixed order, from
      the first read through deletion and released on every path; it closes
      the target-snapshot overwrite race and makes the pre-delete re-read a
      proof (`TestNewTenantAdopterRequiresAFenceToWrite`,
      `...NeverOverwritesAConcurrentlyCreatedTargetSnapshot`,
      `...FencedSourceWriterCannotInterleaveWithDeletion`,
      `...ReleasesItsFencesOnEveryPath`, `...AcquiresFencesInDeterministicOrder`).
      Durable state has no such race: `WriteState(ExpectGenesis)` is atomic
      (`...DurableStateTargetRaceIsStoppedByItsPrecondition`).
- [x] 8.7 Codex review of `aaf0d86`: the legacy `Migrator` hard-coded
      `Unscoped()` for listing, replay, and the snapshot write while claiming
      to walk every tenant. `migration.WithScope` now selects one scope for
      all three (default `Unscoped()`), and `migration.New` returns
      `(*Migrator, error)`, rejecting an invalid scope (breaking). Evidence:
      `TestMigratorScope` (a spy over every store call; homonyms in other
      scopes untouched) and `TestNewRejectsAnInvalidScope`.

## Follow-up chain (not part of this change; each a separate, later,
## explicitly-authorized SDD change)

- **Read-side/projection isolation (EGO-TENANT-004)**: `GetShardEvents`
  and `ShardOffsets` stayed deliberately unscoped in T2 (see
  `persistence/events_store.go`'s doc comment); projection consumers do
  not yet filter by tenant. This is the next slice of the isolation work
  this issue started.
- **Idempotency (#66)**: not addressed by this change.
- **Atomic multi-event append (#67)**: not addressed by this change.
- **Administrative bypass path (TENANT-008)**: an administrative scope
  can never act as an aggregate tenant — it cannot declare a spawn
  (`ErrSpawnTenantUndetermined`, 4.8), command a tenant-bound entity, or
  scope an erasure (`TestAdministrativeScopeIsNeverAnAggregateTenantScope`);
  a deliberate, audited administrative bypass is left to TENANT-008.
- **Tenant-qualified actor identity**: the known limitation documented in
  `design.md` and the CHANGELOG entry above — a GoAkt actor's name is the
  bare `entityID`/`sagaID`, not `(tenant, entityID)`, so two tenants
  sharing an id contend for one actor (fail-closed, not a leak). Deriving
  the actor name from the tenant is its own design pass, since it likely
  touches cluster placement/rebalancing and any tooling that addresses an
  actor by bare entity id.

## Acceptance criteria (issue #92) — evidence mapping

Issue #92 lists eight acceptance criteria. Each is mapped below to the
concrete test, file, or spec requirement that satisfies it, or marked
partial with what remains.

1. **Tenant-aware store mutations and reads receive an explicit tenant
   scope and do not depend only on descriptive metadata.** MET. Every
   record-addressing method on `EventsStore`/`StateStore`/`SnapshotStore`
   takes a `scope Scope` parameter (`persistence/events_store.go`,
   `state_store.go`, `snapshot_store.go`); `tenant_metadata` is
   documented and enforced as non-authoritative — `persistence.Scope`'s
   doc comment's "tenant_metadata stays non-authoritative" section, and
   `resolveScope`'s cross-check via `tenancy.VerifyUnchanged`
   (`TestEventSourcedActorRecoverRejectsMismatchedSpawnBoundTenant`,
   `event_sourced_actor_tenant_persist_test.go`) proves a disagreeing
   `tenant_metadata` is rejected rather than trusted.

2. **Two tenants with the same `persistence_id` do not share an effective
   storage identity.** MET. The `testkit` stores key structurally on
   `(Scope, persistenceID)` (T2, `787550a`); proven directly by
   `ReadIsolation/BothTenantsReadTheirOwnRecord` in
   `persistence/conformance/{events,state,snapshot}.go`.

3. **A tenant cannot read state/events belonging to another tenant through
   a `persistence_id` collision.** MET.
   `ReadIsolation/OtherTenantGetsNothing` and
   `ReadIsolation/UnscopedAndTenantDoNotCrossRead`
   (`persistence/conformance/{events,state,snapshot}.go`) prove this at
   the store level; `TestEventSourcedActorPreStartFailsClosedWithoutTenantScope`
   and `TestEventSourcedActorSpawnBindsExactTenantScope`
   (`event_sourced_actor_scope_test.go`) prove it at the actor level.

4. **A tenant cannot modify another tenant's revision/state/event
   stream.** MET. `WriteIsolation/OtherTenantWriteLeavesRecordUntouched`
   and `WriteIsolation/DeleteIsScoped`
   (`persistence/conformance/events.go`, `snapshot.go`;
   `stateOtherTenantWriteLeavesRecordUntouched` in `state.go`) at the
   store level; `TestEngineEraseEntityCannotEraseAnotherTenantsRecord`
   (`engine_erase_entity_tenant_test.go`) at the engine level.

5. **WRITE-004's optimistic concurrency semantics are preserved within
   each tenant scope.** MET.
   `CAS/ExpectGenesisSucceedsForNewTenantOnEstablishedID`,
   `CAS/ExpectRevisionConflictCarriesItsScope`, and
   `CAS/ConflictInOneScopeNotObservableInAnother`
   (`persistence/conformance/events.go`, `state.go`) prove the persisted
   revision a precondition compares against is scoped; `ConflictError`
   carries its `Scope` (`persistence/conflict.go`).

6. **`single_tenant_mode` keeps working without tenant plumbing invented
   by the application.** MET, with a naming correction: there is no
   literal `single_tenant_mode` flag in the code. The implemented form is
   the built-in resolver `tenancy.WithSingleTenant(id)`
   (`tenancy/resolver.go`), registered once via `ego.WithTenantResolver`.
   `TestSendCommandSingleTenantZeroPlumbing` (`engine_test.go`) proves a
   command succeeds with a plain `context.Background()` and zero
   `tenancy.Attach`/`tenancy.Require` calls at the call site; T4's
   `resolveScope` binds the same `persistence.Scope` machinery for this
   resolver as for any multi-tenant resolver (`TestSendCommandResolverSwapIdenticalSequence`,
   same file) — there is no special-cased single-tenant code path to
   diverge from the isolation guarantees above. At spawn (4.8's
   correction), this resolver additionally implements
   `tenancy.FixedTenantResolver`, so `Engine.Entity`/`DurableStateEntity`/
   `Saga` need no `ego.WithTenant` either —
   `TestEngineWithSingleTenantSpawnNeedsNoWithTenant`
   (`engine_tenant_spawn_test.go`) checks this directly at the spawn
   boundary, and `TestSendCommandResolverSwapIdenticalSequence`'s
   single-tenant subtest confirms it end to end through a real command.

7. **Cross-tenant direct-store/conformance tests exist, independent of
   the actor mailbox.** MET. `persistence/conformance` (T3, `5b1d16f`) is
   exactly this: a factory-driven suite that never constructs an actor or
   mailbox, wired into the in-repo stores by
   `testkit/conformance_test.go`. Its own permanent regression guard,
   `TestConformanceCatchesNonIsolatingStore`, proves the suite is not a
   tautology — it genuinely fails against a store that ignores `Scope`.

8. **Compatibility and migration of existing implementations are
   explicit.** MET. The compatibility contract itself is fully explicit
   (T5): the `CHANGELOG.md` `[Unreleased]` entry and the doc comments on
   `EventsStore`/`StateStore`/`SnapshotStore` state the old/new
   signatures, the structural-key upgrade recipe, and the zero-migration
   guarantee for a deployment that never activates tenancy (every call
   already carries `Unscoped()`, unchanged). T6 closes what T5 left
   unmet: `migration.TenantAdopter`
   (`migration/tenant_adoption.go`) is the migration tool for a
   deployment that adopts tenancy on already-existing data. The business
   decision of which tenant an existing aggregate belongs to remains the
   operator's — the framework cannot and does not infer it — but it is
   now expressed through one required, explicit `TenantAssignment`
   function rather than left as an unstarted, unautomated task. The tool
   defaults to dry-run, never deletes source data without an explicit
   opt-in *and* a verified copy, never clobbers an existing target
   (`persistence.ExpectGenesis()`/pre-read for snapshots), stamps
   `tenant_metadata` the same way the actors do (proven end to end
   through a real tenant-bound `EventSourcedActor` in
   `TestTenantAdopterEndToEndRecoveryThroughRealActor`), and is
   idempotent on re-run. What is honestly still a gap, stated rather than
   hidden: `persistence.SnapshotStore`/`persistence.StateStore` have no
   `PersistenceIDs`-style enumeration method in the SPI, so a
   durable-state-only or snapshot-only deployment cannot be discovered
   automatically — the operator must supply those ids via
   `WithPersistenceIDs` (`migration/tenant_adoption.go`'s and
   `migration/migration.go`'s doc comments say so plainly); and
   `persistence.StateStore` has no delete method at all, so
   `WithSourceDeletion` can never remove a durable-state source copy.
   Both are gaps in today's persistence SPI, not something this tool
   could paper over without inventing an API this change did not
   authorize.
