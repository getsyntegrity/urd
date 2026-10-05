# Spec: publication-tenant-isolation

## Requirement: Scoped routing is isolated by default
A subscription made for scope S on a topic SHALL receive only messages published
for scope S on that topic. Routing keys are structured (scope, topic).

#### Scenario: no cross-tenant delivery
- GIVEN subscribers for tenant A and tenant B on `topic.events`
- WHEN a message is published for tenant A
- THEN only A's subscriber receives it

#### Scenario: unscoped and tenant scopes never meet
- WHEN a message is published for a tenant scope THEN an `Unscoped()` subscriber does not receive it, and the reverse
- AND a legacy subscriber cannot reach a tenant route by choosing a crafted topic string

## Requirement: Invalid scope fails closed
`PublishScoped`/`SubscribeScoped` with a zero-value `eventstream.Scope` SHALL
return an error wrapping `eventstream.ErrInvalidScope` and deliver or register nothing.

## Requirement: Legacy API is single-tenant
`Publish(topic, msg)`/`Subscribe(sub, topic)` behave exactly as `Unscoped()`.

## Requirement: Publish sites use the scope they hold
The events writer and durable-state actor SHALL publish with their bound scope
only when the envelope's `tenant_metadata` agrees with it (tenant scope: tenant
metadata for the same id; `Unscoped()`: no tenant metadata). Otherwise nothing is
published, an error is logged and a counter is incremented.

## Requirement: Publishers receive explicit scope
For a tenant scope the engine SHALL call `Publish` with a context for which
`tenancy.From` returns that tenant's context. Absent, invalid, administrative or
mismatched metadata SHALL be dropped, logged and counted without stopping the loop.
Signatures of `EventPublisher.Publish`/`StatePublisher.Publish` are unchanged.

## Requirement: Registration is scoped or rejected
In a tenant-aware engine, `AddEventPublishers`, `AddStatePublishers` and
`Engine.Subscribe` SHALL subscribe for the engine's fixed tenant. Without one they
SHALL return `ErrPublicationTenantUndetermined` and register nothing. No bypass.

## Requirement: Per-tenant registration
`Engine.AddEventPublishersForTenant(id tenancy.TenantID, publishers ...EventPublisher)`,
`Engine.AddStatePublishersForTenant(id tenancy.TenantID, publishers ...StatePublisher)`
and `Engine.SubscribeForTenant(id tenancy.TenantID)` SHALL register for exactly
tenant `id`, and the existing methods keep their signatures. They SHALL:
- validate `id` with `tenancy.NewTenantID`; an empty or invalid id returns an error
  matching `ErrInvalidPublicationTenant` (and the tenancy error) and registers nothing;
- work in a tenant-aware engine WITHOUT a fixed tenant (the per-caller resolver case);
- in an engine whose resolver fixes a tenant, accept only that tenant, and fail closed with
  `ErrPublicationTenantMismatch` for any other; in an engine without a tenant resolver
  (single-tenant/legacy), fail closed with `ErrPublicationTenantMismatch` for every id,
  because its traffic is `Unscoped()` and a tenant registration could never receive any;
- never deliver another tenant's traffic, an `Unscoped()` message, or a message whose
  metadata is administrative; there is no administrative scope, wildcard, empty-id-means-all
  or `Unscoped()` fallback;
- return `ErrEngineNotStarted` before `Start`, and register nothing on any error.

#### Scenario: two tenants, one engine
- GIVEN a tenant-aware engine whose resolver has no fixed tenant, and publishers registered for tenant A and tenant B
- THEN A's publisher receives only A's events and B's only B's, with the tenant in `tenancy.From(ctx)`

#### Scenario: re-registration and cleanup
- Publisher IDs are unique per kind across tenants: registering an ID already registered, for the same or another tenant, returns `ErrDuplicatePublisherID` and registers nothing from the call.
- Several publishers may register for one tenant under distinct IDs, and one `ForTenant` call is atomic.
- `Engine.Stop` closes every registered publisher and the stream, whichever tenant it was registered for; a subscriber from `SubscribeForTenant` ends with the stream. There is no per-tenant unregister.

## Deferred: platform publisher for all tenants
A publisher that sees several tenants' events with attribution and isolation may be a
legitimate capability, not necessarily an administrative bypass (#96). It is DEFERRED and
MUST be defined separately. It SHALL NOT be enabled implicitly by anything in this spec:
no wildcard, no empty tenant id meaning "all", no `Unscoped()` fallback.

## Requirement: Single-tenant mode is zero-plumbing
An engine without a tenant resolver publishes and delivers `Unscoped()` with no
tenant context and no new configuration.

## Requirement: Conformance
`publishingtest` SHALL offer a broker-neutral check that a publisher receives the
tenant scope in the context it is given.

## Non-goals
New bus/SPI, broker adapters, the platform publisher for all tenants (deferred), admin bypass (#96), read-side (#93), cluster/distributed publication.
