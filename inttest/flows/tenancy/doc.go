// MIT License
//
// Copyright (c) 2022-2026 Arsene Tochemey Gandote
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// Package tenancy is the cross-tenant conformance flow of EGO-TENANT-007 (#95), on a real Postgres database.
//
// # Scope of this package today
//
// It executes only invariants whose mechanisms already exist on develop: the tenant-qualified actor identity
// (#97) and the tenant-scoped events store (#92), driven through engine.Engine over persistence/postgres. It
// does NOT show end-to-end tenant isolation. The read-side boundary has no tenant mechanism yet. The
// publication boundary (#316, #317) and the remote-addressing boundary (#314) each have a partial one, proven
// elsewhere by tests this package does not run (see the notes under the pending table). All of them are
// recorded below and have no test here. A row of the pending table becomes a test in the PR that delivers what
// it needs, and the row moves to the executed table in that same PR.
//
// # Executed invariants
//
//	ID  Invariant                                                                  Test
//	W2  One ID under two tenants is two streams: own rows, own revisions, no bleed TestConformance_W2_SameIDInTwoTenantsIsTwoStreams
//	W4  A rejected command (other tenant, no tenant) persists nothing and never    TestConformance_W4_RejectedCommandsPersistNothing
//	    reaches the handler
//	A1  An administrative context never grants a write to a tenant's entity        TestConformance_W4_RejectedCommandsPersistNothing
//	W5  Concurrent commands on one ID under two tenants never cross: replies,      TestConformance_W5_ConcurrentSameIDCommandsDoNotCross
//	    revisions and rows stay with their tenant, whatever the interleaving
//	W6  A restart recovers each tenant's own state and revision from Postgres      TestConformance_W6_RestartRecoversEachTenantsOwnState
//	W7  Single-tenant and legacy modes keep their behavior and do not collide      TestConformance_W7_SingleTenantAndLegacyModes,
//	    with tenant-scoped data                                                    TestConformance_W7_LegacyDataIsNotSeenByATenant
//
// A1 is covered only for the Postgres write path (an administrative context is rejected before it persists).
// The administrative semantics of the other boundaries belong to #96.
//
// # Pending invariants (not yet implemented, no test, nothing skipped)
//
//	ID  Invariant                                                                  Blocked by
//	R1  A remote command from a non-hosting node keeps the tenant and persists     #305/#314 merged; pending here (see below)
//	    under the right scope
//	R2  An absent, altered, mismatched or unauthorized remote identity is          #305/#314 merged; pending here (see below)
//	    rejected fail-closed                                                       (administrative part: #96)
//	R3  SagaStatus and saga to entity commands keep the identity across nodes      #305/#314 merged; pending here (see below)
//	R4  Relocation keeps the tenant when the same ID lives in two tenants          #305/#314 merged; pending here (see below)
//	R5  Legacy and single-tenant remote commands work unchanged                    #305/#314 merged; pending here (see below)
//	D1  Checkpoints and offsets of one projection name in two tenants do not       #93 (contract also needs #71)
//	    collide
//	D2  A tenant-aware read-side never consumes another tenant's events            #93
//	D3  Restart or resume keeps the read-side's tenant scope and offset            #93
//	D4  A single-tenant read-side needs no extra plumbing                          #93
//	P1  The publication boundary receives an explicit tenant scope                 #94 (partly pending, see below)
//	P2  A subscriber of one tenant never receives another tenant's events          #94 (partly pending, see below)
//	P3  Isolation does not rely on a topic naming convention                       #94 (partly pending, see below)
//	P4  Single-tenant publication needs no plumbing                                #94 (partly pending, see below)
//
// P1 to P4 are still pending: #94 is only partly delivered, and merging #316 and #317 does not satisfy them.
//
// What #316 and #317 provide today, at package and engine level, with fake publishers and in-process
// subscribers (none of it is run by this package, and none of it is counted as conformance of P1 to P4):
//
//   - a tenant-scoped event stream (eventstream: TestScopedStream, TestScope, TestVerifyScope);
//   - per-tenant registration and subscription on the engine, AddEventPublishersForTenant,
//     AddStatePublishersForTenant and SubscribeForTenant, with typed errors ErrPublicationTenantUndetermined,
//     ErrInvalidPublicationTenant and ErrPublicationTenantMismatch (engine: TestPerTenantRegistrationIsolatesTwoTenants,
//     TestPerTenantRegistrationValidation, TestPublicationTenantIsolation);
//   - fixed-tenant and single-tenant engines delivering only their tenant's messages, with that tenant in the
//     context, and the unscoped case staying zero-plumbing (the same engine tests);
//   - the publisher contract check PT-4 in port/publishing/publishingtest (TestCapture_PT4), which runs only
//     for a target that sets WithScope and ScopeOf.
//
// What is still not satisfied for the cross-boundary invariants:
//
//   - PT-4 is exercised only by fakes. No real publisher (publisher/kafka, nats, pulsar, websocket) implements
//     WithScope or ScopeOf, so how a scope reaches a backend, and broker neutrality, are not demonstrated;
//   - publication is not shown across processes: there is no distributed publication test, and the per-tenant
//     API is used in no cluster test;
//   - the tenant journal and read-side side of the same flow (what a subscriber reads back, offsets) is #93, and
//     D1 to D4 also wait on #71.
//
// R1 to R5 are still pending here: #305 is delivered by #314, and merging it does not make them conformance of
// this package.
//
// What #314 provides today (engine/tenant_propagator.go), proven in the root module cluster lane and not run
// by this package (none of it is counted as conformance of R1 to R5 here):
//
//   - a command from the node that does not host the actor reaches the actor of the caller's own tenant, with
//     that tenant's scope on what it persists, for two tenants sharing an entity ID, and an absent or altered
//     identity is rejected (engine: TestClusterTenantRemoteCommands);
//   - SagaStatus from the non-host node and a saga commanding, across nodes, the entity of its own tenant
//     (TestClusterTenantRemoteSaga);
//   - relocation with the remote hop (TestClusterTenantRemoteRelocation);
//   - single-tenant and legacy engines unchanged across nodes (TestClusterTenantRemoteSingleTenantAndLegacy);
//   - the wire format and the receiving side's handling of hostile input, by unit tests
//     (TestTenantPropagatorRoundTrip, TestTenantPropagatorRejectsHostileInput) and the remoting options a
//     tenant-aware engine hands to the transport (TestConfigRemoteOptions).
//
// What is still not satisfied for the cross-boundary invariants:
//
//   - the cluster tests use two in-process nodes that share ONE in-process event stream and store, so the nodes
//     are not independent processes and no Postgres journal is shared between them;
//   - there is no mutual TLS: the identity on the wire is a claim of the sending node, and a cluster without
//     mTLS trusts every peer that can reach its remoting port;
//   - an administrative scope is rejected at the sender in a cluster test, and at the receiver only by a unit
//     test (TestTenantPropagatorRejectsHostileInput), not by a cluster test; its semantics are #96;
//   - R1 to R5 against the Postgres-backed engine in a cluster, with nodes as independent processes, do not
//     exist. They stay recorded here as pending, and would be TestCluster* tests of the root module (the
//     cluster lane runs only those) or a flow of this module that starts real nodes.
//
// # Rules every test here follows
//
// No sleep, retry or skip. Concurrency is released by a latch (a closed channel) and asserted on outcomes that
// hold for every interleaving, so no result depends on timing or on the actor mailbox order.
package tenancy
