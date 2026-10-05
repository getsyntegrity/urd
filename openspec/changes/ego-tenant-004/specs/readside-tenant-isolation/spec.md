# Read-Side Tenant Isolation Specification (PROPOSED)

## Purpose

State the isolation guarantees for read-side processors and their offsets,
written against the proposed `ego-read-001` contract (processor identity is
the pair (scope, name); scope is `persistence.Scope`). Not satisfied by any
code yet.

## Requirements

### Requirement: Offset identity includes the scope

An offset MUST be identified by (scope, processor name, shard). Offsets of
different scopes MUST NOT collide, read, overwrite or reset each other.

### Requirement: A scoped processor reads only its scope

A processor bound to a tenant scope MUST be delivered only that tenant's
events, and shard pending/cursor decisions MUST consider only that scope.

### Requirement: Scope survives restart

Restart or resume MUST reuse the scope the processor was bound to and its
own committed positions.

### Requirement: Fail closed

A tenant-aware engine that cannot determine a processor's scope MUST refuse
to start it and MUST NOT fall back to `Unscoped()`.

### Requirement: Single-tenant needs no plumbing

An engine without tenancy, or with a fixed single tenant, MUST run
processors with no scope declared by the application, and existing
unscoped offsets MUST remain readable.

## Acceptance Criteria

AC-1 Same name, same persistence ID, tenants A and B: offsets written for A
are not returned for B and conversely.

AC-2 A processor bound to A, over a shard holding events of A and B, receives
only A's events; B's events do not advance A's cursor, and
the pending-shard decision ignores B's later events.

AC-3 A and B at different progress: after restart each resumes from its own
offset. Resetting A's offset leaves B's unchanged. No event is lost or
duplicated beyond at-least-once for either.

AC-4 `Unscoped()` and a tenant literally named "unscoped" do not collide, for
offsets and for reads.

AC-5 Tenant-aware engine, processor with undeterminable scope: start fails
closed; no read, no offset write happens. A zero-value scope is rejected.

AC-6 Single-tenant (`WithSingleTenant`) and no-tenancy engines: processor
starts with no extra application plumbing, and an offset that was committed
before the change is still read.

AC-7 (storage) The offsets schema change is idempotent, preserves existing
rows as unscoped, and enforces uniqueness on (scope, name, shard).

## Test plan

AC-1 to AC-6: unit lane, go-specs with the offset-store and events-store
mocks or the testkit in-memory stores, deterministic runner clock, no sleeps,
no external resources. AC-7: Postgres lane under `inttest` only.
Conformance-suite additions belong to #95.

## Open questions

See `ego-read-001/tasks.md` and the PR description.
