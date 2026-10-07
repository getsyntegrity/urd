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

package persistence

import (
	"fmt"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/tenancy"
)

// sliceTenant builds a tenant scope for id, failing the case on error.
func sliceTenant(ctx *specs.Context, id string) Scope {
	scope, err := NewTenantScope(tenancy.TenantID(id))
	ctx.Expect(err).To(specs.BeNil())
	return scope
}

type goldenCase struct {
	name   string
	scope  func(ctx *specs.Context) Scope
	id     string
	golden uint64
}

// The golden vectors freeze FNV-1a 64 plus the key layout at N=1024. If one
// changes, every persisted shard_number would change: that is a data
// migration, not a test update.
func TestSliceOfProvisionalGoldenVectors(t *testing.T) {
	specs.Describe(t, "sliceOfProvisional golden vectors", func(s *specs.Spec) {
		specs.Table(s, []goldenCase{
			{"unscoped, empty id", func(*specs.Context) Scope { return Unscoped() }, "", 991},
			{"unscoped, order-1", func(*specs.Context) Scope { return Unscoped() }, "order-1", 159},
			{"tenant acme, order-1", func(c *specs.Context) Scope { return sliceTenant(c, "acme") }, "order-1", 580},
			{"tenant a, bc", func(c *specs.Context) Scope { return sliceTenant(c, "a") }, "bc", 381},
			{"tenant unscoped, order-1", func(c *specs.Context) Scope { return sliceTenant(c, "unscoped") }, "order-1", 65},
		}, func(c goldenCase) string { return c.name }, func(ctx *specs.Context, c goldenCase) {
			ctx.Expect(sliceOfProvisional(c.scope(ctx), c.id)).ToEqual(c.golden)
		})
	})
}

func TestSliceOfProvisionalIsDeterministicAndInRange(t *testing.T) {
	specs.Describe(t, "sliceOfProvisional", func(s *specs.Spec) {
		s.It("returns the same slice for the same input", func(ctx *specs.Context) {
			scope := sliceTenant(ctx, "acme")
			first := sliceOfProvisional(scope, "order-1")
			for i := 0; i < 100; i++ {
				ctx.Expect(sliceOfProvisional(scope, "order-1")).ToEqual(first)
			}
		})

		s.It("stays within [0, provisionalSliceCount)", func(ctx *specs.Context) {
			scope := sliceTenant(ctx, "acme")
			for i := 0; i < 10000; i++ {
				ctx.Expect(sliceOfProvisional(scope, fmt.Sprintf("id-%d", i)) < provisionalSliceCount).To(specs.BeTrue())
			}
		})

		s.It("stays within [0, n) for another modulus", func(ctx *specs.Context) {
			scope := sliceTenant(ctx, "acme")
			for i := 0; i < 1000; i++ {
				ctx.Expect(sliceOfN(scope, fmt.Sprintf("id-%d", i), 7) < 7).To(specs.BeTrue())
			}
		})
	})
}

// The separation checks compare the full 64-bit hash, so they do not depend
// on the 1-in-N chance of two distinct keys sharing a slice.
func TestSliceHashSeparatesScopesAndKeys(t *testing.T) {
	specs.Describe(t, "sliceHash key encoding", func(s *specs.Spec) {
		s.It("keeps Unscoped apart from a tenant named unscoped", func(ctx *specs.Context) {
			ctx.Expect(sliceHash(Unscoped(), "e") != sliceHash(sliceTenant(ctx, "unscoped"), "e")).To(specs.BeTrue())
		})

		s.It("keeps the same id apart in two tenants", func(ctx *specs.Context) {
			ctx.Expect(sliceHash(sliceTenant(ctx, "acme"), "e") != sliceHash(sliceTenant(ctx, "globex"), "e")).To(specs.BeTrue())
		})

		s.It("does not confuse (a, bc) with (ab, c)", func(ctx *specs.Context) {
			ctx.Expect(sliceHash(sliceTenant(ctx, "a"), "bc") != sliceHash(sliceTenant(ctx, "ab"), "c")).To(specs.BeTrue())
		})

		s.It("keeps the invalid scope apart from Unscoped without panicking", func(ctx *specs.Context) {
			ctx.Expect(sliceHash(Scope{}, "e") != sliceHash(Unscoped(), "e")).To(specs.BeTrue())
		})
	})
}

func TestSliceOfProvisionalDistribution(t *testing.T) {
	specs.Describe(t, "sliceOfProvisional distribution", func(s *specs.Spec) {
		s.It("spreads keys without a degenerate bucket", func(ctx *specs.Context) {
			const keys = 100 * provisionalSliceCount
			counts := make([]int, provisionalSliceCount)
			scope := sliceTenant(ctx, "acme")
			for i := 0; i < keys; i++ {
				counts[sliceOfProvisional(scope, fmt.Sprintf("entity-%d", i))]++
			}
			// The mean is 100 per slice; the bounds are loose on purpose. This
			// guards against a degenerate hash, not against statistical noise.
			outOfBounds := 0
			for _, c := range counts {
				if c < 40 || c > 180 {
					outOfBounds++
				}
			}
			ctx.Expect(outOfBounds).ToEqual(0)
		})
	})
}

// This test computes the slices of a fixed corpus under topology 1, 3 and 5
// and asserts they are identical. Honest caveat: sliceOfProvisional takes no
// topology input, so this holds by construction. The test documents and
// guards that contract; it does not execute anything on a real cluster.
func TestSliceOfProvisionalIsIndependentOfTopology(t *testing.T) {
	specs.Describe(t, "sliceOfProvisional across topologies (by construction)", func(s *specs.Spec) {
		type entry struct {
			scope Scope
			id    string
		}
		compute := func(corpus []entry, _ int) []uint64 { // topology is deliberately not an input
			out := make([]uint64, len(corpus))
			for i, c := range corpus {
				out[i] = sliceOfProvisional(c.scope, c.id)
			}
			return out
		}

		specs.Table(s, []int{3, 5}, func(nodes int) string { return fmt.Sprintf("%d nodes matches 1 node", nodes) },
			func(ctx *specs.Context, nodes int) {
				var corpus []entry
				for _, sc := range []Scope{Unscoped(), sliceTenant(ctx, "acme"), sliceTenant(ctx, "globex")} {
					for i := 0; i < 200; i++ {
						corpus = append(corpus, entry{sc, fmt.Sprintf("entity-%d", i)})
					}
				}
				ctx.Expect(compute(corpus, nodes)).ToEqual(compute(corpus, 1))
			})
	})
}
