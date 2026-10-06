# Draft issue (not created): composite read index for GetShardEvents

Status: DRAFT in the repository for review. It has not been opened on GitHub.

## Title

[PERF] Add a composite read index for `GetShardEvents` over `(tenant_id, shard_number, timestamp)`

## Context

`persistence/postgres.EventStore.GetShardEvents` filters by `tenant_id` and `shard_number` and orders by
`timestamp, persistence_id, sequence_number`. The schema has single-column indexes on `timestamp` and on
`shard_number` and one on `(tenant_id, persistence_id)`, but none that serves this query by scope and shard.

## Evidence (from the #332 experiments, `openspec/changes/ego-journal-cursor-332`)

With a composite index `(tenant_id, shard_number, timestamp)` added as a control on the current adapter, the
tight-loop reader's start-to-delivery latency at write saturation improved (W1, p95: about 1128 ms without it,
290 ms with it, on a 2 vCPU VM, 3 x 5 s). Treat the figures as indicative: the run-to-run variation of that VM is
large.

## What this does NOT do

**It does not fix #332 and must not be read as a mitigation of it.** The same control omitted MORE events than
the adapter without the index (10-18 % against 6-7 % in W1/W2 under the experimental saturated load): a faster
reader reaches the head sooner, so it is more often ahead of a transaction that commits later with an older
timestamp. #332 stays open and needs a cursor that follows commit order.

## Proposal

- Add `CREATE INDEX IF NOT EXISTS ... ON events_store (tenant_id, shard_number, timestamp)` as a numbered schema
  migration, built `CONCURRENTLY` for existing deployments.
- Cost to state in the PR: one more index to maintain on every insert (the control carried it and measured its
  write cost with the rest of the load), and the disk it takes.
- Acceptance: a query-plan check that `GetShardEvents` uses the index; a before/after measurement with the
  existing harness; no change to any observable result of the conformance suite.

## Out of scope

Any change to the offset, cursor, SPI or journal position (#332), and any change to sharding.
