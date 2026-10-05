# Design - ReadSideProcessor canonical contract (#71)

Everything marked "exists" was checked against `develop`.

## D1. A read side is a named, durable, resumable processor

Identity is a name. Today `engine.WithProjection(name, ...)` keys the
registry, `StartProjection(name)` spawns an actor of that name (a cluster
singleton in cluster mode), and the runner commits offsets under that name.
Proposal: the contract calls this name the processor identity, and
"ReadSideProcessor" is the contract name for what code calls a projection.
No rename of existing identifiers; no new public type is required by this
proposal. Whether to add an alias type is an open question (Q1).

## D2. Consumption

Exists: the runner reads the journal in shards. Per pull it asks
`EventsStore.ShardOffsets` which shards are ahead of the committed offsets,
then `GetShardEvents(shard, offset, limit)`, decrypts, adapts, and calls
`Handler.Handle(ctx, persistenceID, event, revision)`. Events of one shard,
and so of one persistence ID, arrive in order. Delivery is at-least-once: the
offset is committed once per batch, so a crash re-delivers the batch.
Recovery (`Fail`, `RetryAndFail`, `RetryAndSkip` style policies in
`projection/recovery.go`) decides the error path; the dead-letter handler
receives skipped events.

Proposal: the contract states these as guarantees (ordering per persistence
ID, at-least-once, idempotent handler) and does not add any.

## D3. Lifecycle

Exists: register on the engine config (every node in cluster mode), start,
stop, running check, rebuild from a timestamp. A failed store round trip is
retried in place; an unprocessable event stops the processor through
supervision. The contract adopts these.

## D4. Tenant scope is an explicit element of the registration (PROPOSAL)

A processor is bound to exactly one scope for its run: either
`persistence.Unscoped()` or one tenant (`persistence.NewTenantScope`). The
scope is part of the processor's effective identity, the pair
`(scope, name)`, in the same way `(scope, persistence_id)` identifies an
aggregate after TENANT-003. The scope is declared at registration or start,
never read from the payload, never derived from the name by prefixing, and
never inferred from event contents. Reuse `persistence.Scope`; no second
scope type.

The contract only says that the scope is explicit and part of identity. How
it is spelled in the registration API (option on `WithProjection`, field on
`projection.Options`, argument to `StartProjection`) is left to the owner
(Q2), because `engine/option.go` is outside this change.

Unscoped deployments declare nothing and behave as today.

## D5. What this change does not decide

How offsets are stored (the offset-store signature change is #93's, after
approval), how several processors are claimed across nodes, and any adapter.

## Alternatives rejected

- Per-tenant processor names composed by the application (`orders:acme`):
  that is the convention-based isolation the epic forbids; the store would
  have nothing to verify.
- One processor fanning out over all tenants with per-tenant offsets: a
  runner redesign; deferred (Q3).
