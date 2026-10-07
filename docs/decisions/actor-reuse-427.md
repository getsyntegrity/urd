# Actor reuse and engine namespaces (#427)

The current command API addresses an entity by ID and resolves its tenant. It
cannot choose an actor family or definition. Within one engine namespace, a
second spawn at that address is idempotent only if scope, family and definition
all match. An incompatible spawn returns `*engine.ActorIdentityError`, wrapping
`engine.ErrSpawnIdentityMismatch`, and leaves the original actor intact.

The PID answers an internal protobuf identity query from its own binding, both
locally and remotely. No caller-provided intent is accepted as proof. An older
node that cannot answer the new query fails closed; upgrade the engine nodes
before relying on idempotent spawn across mixed versions.

`WithActorNamespace` supplies a stable explicit address namespace when engines
share an ActorSystem. Configure the same value on every node of one engine.
An empty namespace preserves existing names. A namespace separates actor
addresses, **not persisted data**: scope and stores remain the storage boundary.
A behavior's ID stays the persistence ID, including events, snapshots and
state. Using different namespaces does not create independent journals for the
same ID and scope in the same store; applications must not create competing
writers to that journal.

A definition defaults to its protobuf full name or Go package/type. A behavior
can implement `DefinitionID() string` to distinguish multiple logical
definitions implemented by one Go type; choose a stable non-empty value.

This resolves the contradictory acceptance wording in #427: the same ID can
be used under different scopes or explicit engine namespaces, while an
incompatible family/definition at the same address is rejected. Simultaneous
families at one namespace/scope/ID require a future family-qualified command
API and persistence identity contract; changing actor names alone is unsafe.
