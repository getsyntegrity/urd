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
   is unchanged). Routing is keyed by (`persistence.Scope`, topic) with a struct
   key, never by string concatenation. A subscription sees only its own scope.
   The legacy `Publish`/`Subscribe` map to `persistence.Unscoped()`.
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
through the legacy API. To keep it working in tenant mode, an INTERNAL legacy
fan-in exists, reachable only through a grant type defined in an `internal`
package and used only by the projection actor. It is unreachable from the public
`Subscribe`/`AddEventPublishers` paths, is covered by a test saying so, and is to
be removed by #93.

## Open question (owner decision pending)

Per-tenant public registration (`AddEventPublishersForTenant`,
`AddStatePublishersForTenant`, `SubscribeForTenant`) is a public API decision and
is NOT part of this change. Until it is taken, a multi-tenant engine without a
fixed tenant cannot register publishers or subscribers (it fails closed).

## Governance (spec-governance)

Outcome: "publication and subscription are tenant-isolated by default across the
existing boundaries". Expanded capabilities: scoped stream, publish-site scope
validation, publisher-boundary scope delivery, fail-closed registration,
conformance check. None has an acceptance criterion outside the parent outcome
(fail-closed registration is required for isolation to be correct), so they are
instrumental: `ATOMIC`. The two PRs are a delivery slicing of one spec, not two
specs. Human gate (Public API: new optional interface, typed error): approved by
the coordinator's recorded answers (optional interface; per-tenant API excluded).

## Rollback

Single-tenant and legacy engines publish `Unscoped()` end to end, so reverting
the change restores prior behavior. No data migration.
