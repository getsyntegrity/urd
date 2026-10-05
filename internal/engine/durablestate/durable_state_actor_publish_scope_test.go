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

package durablestate

import (
	"context"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

func streamScope(ctx *specs.Context, scope persistence.Scope) eventstream.Scope {
	out, err := protocol.StreamScope(scope)
	ctx.Expect(err).To(specs.BeNil())
	return out
}

func TestDurableStatePublishesForItsScope(t *testing.T) {
	specs.Describe(t, "a durable state actor publishes its confirmed state for the scope it is bound to (EGO-TENANT-005)", func(s *specs.Spec) {
		bg := context.Background()

		type rig struct {
			entity *Actor
			stream eventstream.ScopedStream
		}
		newRig := func(ctx *specs.Context, aware bool, actorTenant tenancy.TenantContext, scope persistence.Scope) rig {
			store := testkit.NewDurableStore()
			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = store.Disconnect(bg) })
			stream := eventstream.New()
			ctx.Cleanup(stream.Close)
			state := &testpb.Account{AccountId: "acct-1", AccountBalance: 100}
			cached, err := anypb.New(state)
			ctx.Expect(err).To(specs.BeNil())
			return rig{
				entity: &Actor{
					persistenceID: "acct-1", currentState: state, cachedStateAny: cached, currentVersion: 1,
					lastCommandTime: time.Now(), stateStore: store, eventsStream: stream,
					tenantAware: aware, actorTenant: actorTenant, scope: scope,
				},
				stream: stream.(eventstream.ScopedStream),
			}
		}
		count := func(sub eventstream.Subscriber) int {
			n := 0
			for range sub.Iterator() {
				n++
			}
			return n
		}

		s.It("delivers a tenant's state to that tenant only", func(ctx *specs.Context) {
			scopeA, scopeB := tenantScopeFor(ctx, "tenant-a"), tenantScopeFor(ctx, "tenant-b")
			r := newRig(ctx, true, tenantContextFor(ctx, "tenant-a"), scopeA)
			subA, subB, legacy := r.stream.AddSubscriber(), r.stream.AddSubscriber(), r.stream.AddSubscriber()
			ctx.Expect(r.stream.SubscribeScoped(subA, streamScope(ctx, scopeA), protocol.StatesTopic)).To(specs.BeNil())
			ctx.Expect(r.stream.SubscribeScoped(subB, streamScope(ctx, scopeB), protocol.StatesTopic)).To(specs.BeNil())
			r.stream.Subscribe(legacy, protocol.StatesTopic)

			ctx.Expect(r.entity.persistStateAndPublish(bg)).To(specs.BeNil())

			ctx.Expect(count(subA)).To(specs.Equal(1))
			ctx.Expect(count(subB)).To(specs.Equal(0))
			ctx.Expect(count(legacy)).To(specs.Equal(0))
		})

		s.It("single-tenant mode still publishes to unscoped subscribers", func(ctx *specs.Context) {
			r := newRig(ctx, false, noTenantContext, persistence.Unscoped())
			legacy := r.stream.AddSubscriber()
			r.stream.Subscribe(legacy, protocol.StatesTopic)

			ctx.Expect(r.entity.persistStateAndPublish(bg)).To(specs.BeNil())

			ctx.Expect(count(legacy)).To(specs.Equal(1))
		})

		s.It("drops, without failing the write, a state whose tenant identity does not match its scope", func(ctx *specs.Context) {
			scopeA := tenantScopeFor(ctx, "tenant-a")
			r := newRig(ctx, true, tenantContextFor(ctx, "tenant-b"), scopeA)
			subA := r.stream.AddSubscriber()
			ctx.Expect(r.stream.SubscribeScoped(subA, streamScope(ctx, scopeA), protocol.StatesTopic)).To(specs.BeNil())

			ctx.Expect(r.entity.persistStateAndPublish(bg)).To(specs.BeNil())

			ctx.Expect(count(subA)).To(specs.Equal(0))
		})
	})
}
