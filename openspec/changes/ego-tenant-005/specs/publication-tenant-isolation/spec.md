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

## Requirement: Single-tenant mode is zero-plumbing
An engine without a tenant resolver publishes and delivers `Unscoped()` with no
tenant context and no new configuration.

## Requirement: Conformance
`publishingtest` SHALL offer a broker-neutral check that a publisher receives the
tenant scope in the context it is given.

## Non-goals
New bus/SPI, broker adapters, per-tenant registration API (open question), admin bypass (#96), read-side (#93).
