# Proposal: publication and subscription tenant isolation (EGO-TENANT-005)

| Field | Value |
|---|---|
| Change | `ego-tenant-005` |
| Tracker | #94, epic #23 |
| Depends on | `ego-tenant-002` (tenant metadata on envelopes), `ego-tenant-003` (`persistence.Scope`), #97 (tenant-aware actor identity) |
| Governance verdict | `ATOMIC` (see below) |

## Problem

Every event and durable state an engine publishes goes to two global in-process
topics, `topic.events` and `topic.states` (`internal/engine/protocol/doc.go`).
Every registered `EventPublisher`/`StatePublisher`, every `Engine.Subscribe()`
caller and every saga receives the traffic of every tenant. The publisher SPI
(`port/publishing`) receives `context.Background()` and an event, so a publisher
cannot tell which tenant an event belongs to except by parsing
`tenant_metadata`. Isolation today depends on the application, not on the
framework.

## Decision: advance on the existing boundaries

PUB-001 (a `DomainEventBus`/`Topic` SPI) does not exist and is not created here.
No new bus, no new broker adapter, and `EventPublisher.Publish` and
`StatePublisher.Publish` keep their signatures (no break for `publisher/*`).

## What changes

1. `eventstream` gains an OPTIONAL interface `ScopedStream` (`eventstream.Stream`
   is unchanged) and its own `Scope` type. Routing is keyed by (`Scope`, topic) with a struct
   key, never by string concatenation. A subscription sees only its own scope.
   The legacy `Publish`/`Subscribe` map to `eventstream.Unscoped()`.
2. The publish sites (`eventsWriterActor`, durable-state actor) publish with the
   scope they already hold, after checking that the envelope's tenant metadata
   agrees with it.
3. The engine delivers to a publisher with a `tenancy.Attach`ed context carrying
   the message scope and re-checks scope against `tenant_metadata`. Absent,
   invalid or mismatched identity is dropped, logged at error level and counted;
   the publisher loop keeps running.
4. `AddEventPublishers`, `AddStatePublishers` and `Engine.Subscribe` scope their
   subscriptions to the engine's fixed tenant. A tenant-aware engine without a
   fixed tenant fails closed with `ErrPublicationTenantUndetermined` and
   registers nothing. There is no administrative bypass (#96).
5. Sagas subscribe with their bound scope (their subscribe call only).
6. `publishingtest` gains a broker-neutral check that a publisher receives the
   scope in its context.

## Known limit (temporary)

The projection runner (`internal/projectionrunner`, owned by #93) subscribes
through the legacy `Unscoped()` API, which by design receives nothing a
tenant-aware engine publishes for a tenant. It only uses the stream as a nudge
to pull from the (scoped) events store. To keep it working, a tenant-scoped
publication also posts a payload-free message on the INTERNAL topic
`protocol.ProjectionWakeTopic`, and the projection actor hands the runner a
stream wrapper that also subscribes it there. The wake-up carries no event, no
state and no tenant identity, so nothing crosses tenants. It is never
subscribed by `Subscribe`, `AddEventPublishers` or `AddStatePublishers`, and a
test says so. #93 removes it when it gives the runner a scope.

Why not a fan-in subscription: it needs a capability visible to `eventstream`,
and `eventstream` is in the pinned closure of `internal/runtimeconsumer`
(#147), which allows no new first-party package. For the same reason
`eventstream` carries its own `Scope` type (depending only on `tenancy`)
instead of importing `persistence`.

## Decided: per-tenant registration (owner decision)

`AddEventPublishersForTenant`, `AddStatePublishersForTenant` and
`SubscribeForTenant(id)` are APPROVED and specified in
`specs/publication-tenant-isolation/spec.md`. The current signatures of
`AddEventPublishers`, `AddStatePublishers` and `Subscribe` are unchanged. They
are delivered in the second PR; that PR must not merge before the methods exist
and the engine is tested with a per-caller resolver.

## Deferred: a platform publisher for all tenants

A publisher that sees several tenants' events, with attribution and isolation,
may be a legitimate capability, and is not necessarily an administrative bypass
(#96). It is DEFERRED and must be defined separately. It MUST NOT be enabled
implicitly: no wildcard, no empty tenant id meaning "all", no `Unscoped()`
fallback. Nothing in this change implements or hints at it.

## Governance (spec-governance)

Outcome: "publication and subscription are tenant-isolated by default across the
existing boundaries". Expanded capabilities: scoped stream, publish-site scope
validation, publisher-boundary scope delivery, fail-closed registration,
conformance check. None has an acceptance criterion outside the parent outcome
(fail-closed registration is required for isolation to be correct), so they are
instrumental: `ATOMIC`. The two PRs are a delivery slicing of one spec, not two
specs. Human gate (Public API): approved by the owner decision recorded in the
session of 2026-10-05. Scope of that approval: the optional scoped-stream
interface, the typed errors as documented, and the per-tenant registration
methods. It approves the CONTRACT, not every #94 criterion: #94 stays open
(PT-4 is exercised only against fakes and no real publisher implements it; there
is no cluster or distributed publication; the platform publisher is deferred).

## Rollback

Single-tenant and legacy engines publish `Unscoped()` end to end, so reverting
the change restores prior behavior. No data migration.
