# Tasks: Canonical TenantContext / TenantResolver SPI (EGO-TENANT-001)

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~900 (12 tenancy files, root test, Makefile edit) |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR1 foundation -> PR2 resolver/context -> PR3 conformance+mocks |
| Delivery strategy | ask-on-risk |
| Chain strategy | stacked-to-main |

Decision needed before apply: No
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: High

Rationale: one leaf package, no engine wiring, but 12 files plus two acceptance scenarios and a conformance test exceed 400 lines; splits cleanly by file. Stacked-to-main (user choice): each PR merges to `main` in order, not stacked on the `docs/propose-ego-tenant-context` proposal branch.

### Suggested Work Units

| Unit | Goal | Likely PR | Focused test command | Runtime harness | Rollback boundary |
|------|------|-----------|----------------------|-----------------|-------------------|
| 1 | Errors, TenantID, TenantContext | PR 1 | `go test ./tenancy/... -run 'Error\|TenantID\|TenantContext'` | N/A — pure value types | Delete `errors*.go`, `tenant_id*.go`, `tenant_context*.go` |
| 2 | Resolver, context propagation, metadata | PR 2 | `go test ./tenancy/... -run 'Resolver\|Context\|Metadata'` | In-process saga/entrypoint simulation | Delete `resolver*.go`, `context*.go`, `metadata*.go` |
| 3 | Conformance test, mocks, Makefile | PR 3 | `go test -run TestTenancyArchitecture ./...` | `go list -deps ./tenancy/...` (real exec) | Delete `tenancy_architecture_test.go`, `mocks/tenancy/tenant_resolver.go`, revert `Makefile` |

## Phase 1: Foundation

- [x] 1.1 RED: `tenancy/errors_test.go` — `Reason`, `Error.Unwrap/Is`, sentinels `ErrMissing/ErrInvalid/ErrDenied`
- [x] 1.2 GREEN: `tenancy/errors.go` implementing 1.1
- [x] 1.3 RED: `tenancy/tenant_id_test.go` — empty/whitespace/control-rune/invalid-UTF-8/>128-byte rejected; arbitrary non-UUID accepted
- [x] 1.4 GREEN: `tenancy/tenant_id.go` — `TenantID`, `NewTenantID` (R1)
- [x] 1.5 RED: `tenancy/tenant_context_test.go` — empty `TenantID` fails; admin scope type-distinct, attributed
- [x] 1.6 GREEN: `tenancy/tenant_context.go` — `Scope`, `Administrative`, `TenantContext`, constructors, accessors

## Phase 2: Core Mechanics

- [x] 2.1 RED: `tenancy/resolver_test.go` — `WithSingleTenant` produces a `TenantContext` indistinguishable from any resolver's
- [x] 2.2 GREEN: `tenancy/resolver.go` — `TenantResolver` interface, `WithSingleTenant`
- [x] 2.3 RED: `tenancy/context_test.go` — `Attach` idempotent/`ErrDenied` on change; `From`/`Require` `ErrMissing`; `VerifyUnchanged` -> `ErrDenied`
- [x] 2.4 RED: `tenancy/context_test.go` — saga: `context.Background()` reset, reconstruct via metadata+`Attach`; skip -> `ErrMissing`
- [x] 2.5 RED: `tenancy/context_test.go` — invocation: entrypoint `Resolve`+`Attach`, behavior only `Require`; skip -> fails
- [x] 2.6 GREEN: `tenancy/context.go` — `Attach`, `From`, `Require`, `VerifyUnchanged` satisfying 2.3-2.5
- [x] 2.7 RED: `tenancy/metadata_test.go` — `MarshalMetadata`/`UnmarshalMetadata` round-trip, tenant and administrative
- [x] 2.8 GREEN: `tenancy/metadata.go` — `Metadata`, marshal/unmarshal (`ego.tenant.*` keys)

## Phase 3: Conformance & Tooling

- [x] 3.1 RED: `tenancy_architecture_test.go` (root, pkg `ego`) — `go list -deps ./tenancy/...` fails on non-stdlib import
- [x] 3.2 GREEN: confirm `tenancy/` has zero non-stdlib imports; 3.1 passes unmodified
- [x] 3.3 Generate `mocks/tenancy/tenant_resolver.go` mock for `TenantResolver`
- [x] 3.4 Add tenancy mock generation to `Makefile` `docker-mock` target

## Phase 4: Verification

- [x] 4.1 Run `go mod tidy && go mod vendor`, then `go test -mod=vendor -p 1 -timeout 0 -race ./...`.
      Exact command executed at PR3 apply time. Result at that time: FAIL only in
      `TestEventPublisherClusterHighPartitionCount` (root package), reproduced 3/3 on a
      disposable worktree of plain `main` with no EGO-TENANT-001 code present. Re-run
      independently during `sdd-verify` (both locally and via GitHub Actions CI on the
      merged commit `b61eeea`) and found passing both times — see the reclassification
      below. All EGO-TENANT-001 packages and tenancy spec scenarios passed on every run.
- [x] 4.2 Run `go vet ./tenancy/...`; confirm `behavior.go`/`saga.go`/`engine.go`/`option.go`
      are byte-identical (proposal Success Criteria).

### Verification note — task 4.1

At PR3 apply time, the repository-wide race suite was not fully green because of one failure:

`TestEventPublisherClusterHighPartitionCount`

Observed from the exact required command:

`go test -mod=vendor -p 1 -timeout 0 -race ./...`

Failure:

`workload only produced events in shards [0..270] (max seen: 0); the test does not exercise the pre-fix bug range`

That failure was reproduced 3/3 on a disposable worktree of plain `main`, with no
EGO-TENANT-001 code present — establishing at the time that it was unrelated to this change.

**Reclassification (during `sdd-verify`, after merge):** the same command, re-run against
the merged `main` at `b61eeea`, passed with zero failures — `TestEventPublisherClusterHighPartitionCount`
included (11.51s). The same result was independently confirmed by the real GitHub Actions
CI run on that commit (run `34798920900`, `push` event, conclusion `success`). This means
the test is **pre-existing and flaky**, not a deterministic pre-existing failure as first
recorded — the original 3/3 reproduction is preserved above as accurate historical evidence
of what was observed at that time, but the conclusion is corrected: it is not a standing
blocker, and no further action against it is required by EGO-TENANT-001.

All other packages passed on every run, including `tenancy`, and no failure — historical or
current — is attributable to this change. Task 4.1 is executed and reconciled.
