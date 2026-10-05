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
// does NOT show end-to-end tenant isolation: the read-side, publication and remote-addressing boundaries have
// no tenant mechanism yet, so they are recorded below and have no test. A row of the pending table becomes a
// test in the PR that delivers its mechanism, and the row moves to the executed table in that same PR.
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
//	R1  A remote command from a non-hosting node keeps the tenant and persists     #305
//	    under the right scope
//	R2  An absent, altered, mismatched or unauthorized remote identity is          #305 (administrative part: #96)
//	    rejected fail-closed
//	R3  SagaStatus and saga to entity commands keep the identity across nodes      #305
//	R4  Relocation keeps the tenant when the same ID lives in two tenants          #305 (remote hop)
//	R5  Legacy and single-tenant remote commands work unchanged                    #305
//	D1  Checkpoints and offsets of one projection name in two tenants do not       #93 (contract also needs #71)
//	    collide
//	D2  A tenant-aware read-side never consumes another tenant's events            #93
//	D3  Restart or resume keeps the read-side's tenant scope and offset            #93
//	D4  A single-tenant read-side needs no extra plumbing                          #93
//	P1  The publication boundary receives an explicit tenant scope                 #94 (needs PUB-001)
//	P2  A subscriber of one tenant never receives another tenant's events          #94
//	P3  Isolation does not rely on a topic naming convention                       #94
//	P4  Single-tenant publication needs no plumbing                                #94
//
// R1 to R5 are not tests of this package: they will be TestCluster* tests of the root module, because the
// cluster lane runs only those. A Postgres-backed cluster composition, if it is still wanted, comes after them.
//
// # Rules every test here follows
//
// No sleep, retry or skip. Concurrency is released by a latch (a closed channel) and asserted on outcomes that
// hold for every interleaving, so no result depends on timing or on the actor mailbox order.
package tenancy
