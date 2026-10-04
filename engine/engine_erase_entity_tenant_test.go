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

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
)

// TestEngineEraseEntityCannotEraseAnotherTenantsRecord covers TENANT-003
// T4's EraseEntity fix (engine.go's EraseEntity, ~line 1284): before this
// fix, EraseEntity called the stores with persistence.Unscoped()
// unconditionally regardless of who called it, so any caller who knew a
// persistenceID could erase ANY tenant's events — the exact isolation hole
// this ticket closes. EraseEntity now resolves the caller's own tenant
// scope from ctx (via the same tenancy.TenantResolver as every other
// engine method) and confines its read/delete to that scope alone.
//
// The two records below share the SAME persistenceID but live under two
// different tenant scopes at the store layer — the composite (scope,
// persistenceID) key testkit.EventStore uses internally — simulating
// two tenants that use the same logical id: since EGO-TENANT-009 each gets
// its own live actor, and the records stay apart at the storage layer.
func TestEngineEraseEntityCannotEraseAnotherTenantsRecord(t *testing.T) {
	specs.Describe(t, "EraseEntity confines its delete to the caller's own tenant scope", func(s *specs.Spec) {
		s.It("erases the caller's record and leaves another tenant's record at the same persistenceID", func(ctx *specs.Context) {
			bg := context.Background()
			persistenceID := uuid.NewString()

			store := connectedEventsStore(ctx)

			scopeA, err := persistence.NewTenantScope("acme")
			ctx.Expect(err).To(specs.BeNil())
			scopeB, err := persistence.NewTenantScope("globex")
			ctx.Expect(err).To(specs.BeNil())

			newEvent := func() *egopb.Event {
				eventAny, err := anypb.New(&testpb.AccountCreated{AccountId: persistenceID, AccountBalance: 100})
				ctx.Expect(err).To(specs.BeNil())
				return &egopb.Event{
					PersistenceId:  persistenceID,
					SequenceNumber: 1,
					Event:          eventAny,
					Timestamp:      time.Now().UnixNano(),
				}
			}

			ctx.Expect(store.WriteEvents(bg, scopeA, []*egopb.Event{newEvent()}, persistence.Unconditional())).To(specs.BeNil())
			ctx.Expect(store.WriteEvents(bg, scopeB, []*egopb.Event{newEvent()}, persistence.Unconditional())).To(specs.BeNil())

			// perCallerTenantResolver (option_test.go) resolves whichever tenant id
			// the caller placed on ctx under perCallerTenantKey.
			engine := newSpecsEngine(ctx, "Sample", store, WithTenantResolver(perCallerTenantResolver{}))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())

			ctxA := context.WithValue(bg, perCallerTenantKey{}, "acme")
			ctx.Expect(engine.EraseEntity(ctxA, persistenceID, true)).To(specs.BeNil())

			// tenant A's own record must be erased by its own EraseEntity call
			latestA, err := store.GetLatestEvent(bg, scopeA, persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latestA).To(specs.BeNil())

			// tenant B's record at the same persistenceID must survive tenant A's erasure call
			latestB, err := store.GetLatestEvent(bg, scopeB, persistenceID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(latestB).To(specs.Not(specs.BeNil()))

			ctx.Expect(engine.Stop(bg)).To(specs.BeNil())
		})
	})
}
