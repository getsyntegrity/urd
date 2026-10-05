# Tasks: ego-tenant-005

PR (a): stream and publish-site scoping
- [ ] T1 `eventstream.ScopedStream`, struct-keyed routing, scoped Message, legacy mapping, scope/metadata verifier
- [ ] T2 publish sites (`events_writer_actor`, `durable_state_actor`) publish scoped with metadata check, log + counter
- [ ] T3 saga subscribe passes its bound scope; internal legacy fan-in for the projection actor + unreachability test

PR (b): engine and publisher boundary
- [ ] T4 `sendEvent`/`sendState` attach tenant context, verify identity, drop + log + count
- [ ] T5 fixed-tenant scoping of `AddEventPublishers`/`AddStatePublishers`/`Subscribe`; `ErrPublicationTenantUndetermined`
- [ ] T6 `publishingtest` scope-delivery check

Open: per-tenant registration API (owner decision).
