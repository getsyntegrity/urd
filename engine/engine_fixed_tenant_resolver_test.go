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
	"sync/atomic"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"

	"github.com/getsyntegrity/urd/tenancy"
)

// multiTenantFixedResolver is the multi-tenant resolver the
// tenancy.FixedTenantResolver contract allows: it implements the interface
// but reports no fixed tenant. It counts FixedTenant and Resolve calls.
type multiTenantFixedResolver struct {
	stubTenantResolver
	asked    atomic.Int32
	resolved atomic.Int32
}

var _ tenancy.FixedTenantResolver = (*multiTenantFixedResolver)(nil)

func (r *multiTenantFixedResolver) Resolve(ctx context.Context) (tenancy.TenantContext, error) {
	r.resolved.Add(1)
	return r.stubTenantResolver.Resolve(ctx)
}

func (r *multiTenantFixedResolver) FixedTenant() (tenancy.TenantID, bool) {
	r.asked.Add(1)
	return "", false
}

// TestEngineSpawnWithMultiTenantFixedTenantResolverNeedsWithTenant is the
// ego-arch-004 spec 3 scenario "a multi-tenant resolver that implements the
// interface": the engine asks the resolver for its fixed tenant through
// tenancy.FixedTenantOf, gets none, and fails the spawn with
// ErrSpawnTenantUndetermined, exactly as before the call site moved behind
// the accessor. It never calls Resolve at spawn.
func TestEngineSpawnWithMultiTenantFixedTenantResolverNeedsWithTenant(t *testing.T) {
	specs.Describe(t, "a multi-tenant resolver that implements FixedTenantResolver", func(s *specs.Spec) {
		s.It("needs WithTenant to spawn and never resolves at spawn", func(ctx *specs.Context) {
			bg := context.Background()
			store := connectedEventsStore(ctx)

			resolver := &multiTenantFixedResolver{stubTenantResolver: stubTenantResolver{id: "acme"}}
			engine := newSpecsEngine(ctx, "Sample", store, WithTenantResolver(resolver))
			ctx.Expect(engine.Start(bg)).To(specs.BeNil())

			entityID := uuid.NewString()
			ctx.Expect(engine.Entity(bg, newTenancyProbeEventSourcedBehavior(entityID))).To(specs.MatchError(ErrSpawnTenantUndetermined))

			// the engine must ask the resolver for its fixed tenant
			ctx.Expect(resolver.asked.Load()).To(specs.BeGreaterThan(int32(0)))
			// the engine must never call Resolve at spawn
			ctx.Expect(resolver.resolved.Load()).To(specs.BeZero())

			// no actor may be spawned when the tenant cannot be determined.
			// EntityExists is a lookup, not a spawn: it resolves the caller's
			// tenant once to name the actor it looks for, so it comes after
			// the spawn-time assertion above.
			exists, err := engine.EntityExists(bg, entityID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(exists).To(specs.BeFalse())

			// With WithTenant the same resolver spawns: the fixed tenant is only
			// the fallback.
			ctx.Expect(engine.Entity(bg, newTenancyProbeEventSourcedBehavior(uuid.NewString()), WithTenant("acme"))).To(specs.BeNil())
		})
	})
}
