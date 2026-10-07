# Logical slices (#350, I-04)

Status: proposed. The value of N is owner-pending ratification together with #351.

## Decision

`persistence.SliceOf(scope, entityID)` maps the pair (Scope, entity id) onto one of `persistence.SliceCount = 1024` logical slices with FNV-1a 64 over an unambiguous key (marker byte, uvarint tenant length, tenant bytes, 0x00, entity id; Unscoped has its own marker). The function is pure: it takes no cluster, clock or topology input and uses no seeded hash. It replaces `ActorSystem().Partition(id)` as the source of the slice number, and it is separate from the physical assignment of slices to nodes or cells.

This change only adds the function and its tests. It is not wired into actors, and it changes no schema, no go.mod and no stored value.

## N = 1024

- Power of two: modulus is cheap and a future split or merge by a factor of two is a mask change.
- Finer units than 256: slices are the unit of rebalancing and of offset tracking, so more slices give smoother distribution across cells and smaller moves.
- Fixed forever: changing N or the hash reassigns entities, so it is a full migration of `shard_number` and of every offset keyed by it.
- Risk: offset rows grow as tenants x projections x N. At 1024 this is four times the 256 case. This has to be weighed with #362 (projection model) and #351 (schema) before N is final.

Ratification: the issue asks for N in {256, 1024}. 1024 is the proposal here. The final choice is owner-pending and is to be confirmed with #351. Until then, acceptance criterion 1 is not closed.

## Two different questions: #346 B2 and #350

- #346 (B2 propagation gap) asks whether the Partition or slice value actually reaches `Event.Shard` and `DurableState.Shard` when records are written. That is a wiring question and belongs to the issues that connect the function to the write path (#351 and later).
- #350 asks for a stability criterion: the slice of an entity does not change with the number of nodes. `SliceOf` meets it by construction because it has no topology input. The test over topologies 1, 3 and 5 documents that contract; it does not exercise a real cluster.

Meeting #350's criterion says nothing about B2.

## Migration plan for shard_number and offsets (outline, not implemented)

Feeds #359 (I-09b). Nothing below exists yet.

1. Recompute `shard_number` for existing events and durable state with `SliceOf(scope, persistence_id)`, using a resumable, idempotent, batched migrator (keyset pagination, progress checkpoint, safe to re-run).
2. Old per-shard offsets do not map one to one onto slices: an old shard holds entities that now spread over many slices, and a slice collects entities from many old shards. Options:
   - Reset and replay: start slice offsets from zero and rely on idempotent consumers (#373).
   - Min-offset per slice: seed each slice with the minimum offset among the old shards that contributed to it; at-least-once, so consumers must tolerate replay.
   - Freeze and drain: stop writers, let projections catch up, migrate, then resume from a known-equal position.
3. Keep the old offsets table until the new one is verified.
4. No old writer after cutover: old binaries must not write once the new layout is live (same constraint as migration 006, see `scoped-offsets-retention.md`).

Open items: choose among the offset options, decide batch size and checkpoint format, and define verification (count and checksum per slice).
