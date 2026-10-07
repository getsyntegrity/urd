# Scope isolation and safe retention

Offset identity now consists of persistence scope, projection name and shard. The optional ScopedOffsetStore capability binds runner, lag and rebuild operations to the registered projection scope. A tenant projection fails closed with an unsupported adapter. Existing OffsetStore methods refer exclusively to Unscoped.

PostgreSQL migration 006 adds tenant_id and replaces the offset primary key. Existing rows retain their values under Unscoped: a name does not prove ownership. Tenant projections start with fresh tenant cursors and may replay events previously handled with a shared cursor; handlers must tolerate replay. Upgrade all writers together: old binaries use the former conflict key and must not write after migration. The newer schema version is rejected by older migrators. No implicit tenant adoption of offsets occurs.

Cursor values and rebuild boundaries use Unix nanoseconds consistently. Offset write metadata timestamps remain Unix milliseconds.

Automatic event deletion after snapshots now requires an adapter implementing RetainedEventsDeleter. This operation must atomically verify durable progress of every required consumer and reader before deleting a safe prefix. Existing adapters do not provide this guarantee, so selecting automatic event retention is rejected with ErrUnsafeEventRetention. Snapshot-only retention remains available. Explicit erasure through DeleteEvents keeps its existing semantics. A separate check followed by DeleteEvents cannot satisfy this contract because a new reader could enter between the operations.

This fixes current cross-scope offset resets and unsafe automatic retention. It does not implement the future projection mode/version/cell model, durable consumer registry, commit-order-safe feed, or guarded deletion adapter; #362, #381 and related architecture tasks remain open.
