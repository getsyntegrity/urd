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

	"github.com/getsyntegrity/urd/tenancy"
)

func tenantScope(t testing.TB, id string) Scope {
	t.Helper()
	s, err := NewTenantScope(tenancy.TenantID(id))
	if err != nil {
		t.Fatalf("NewTenantScope(%q): %v", id, err)
	}
	return s
}

func TestSliceOfGoldenVectors(t *testing.T) {
	// These vectors freeze FNV-1a 64 plus the key layout. If one changes,
	// every persisted shard_number changes: that is a data migration, not a
	// test update.
	cases := []struct {
		name   string
		scope  Scope
		id     string
		golden uint64
	}{
		{"unscoped empty", Unscoped(), "", 991},
		{"unscoped order-1", Unscoped(), "order-1", 159},
		{"tenant acme order-1", tenantScope(t, "acme"), "order-1", 580},
		{"tenant a/bc", tenantScope(t, "a"), "bc", 381},
		{"tenant unscoped order-1", tenantScope(t, "unscoped"), "order-1", 65},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SliceOf(tc.scope, tc.id); got != tc.golden {
				t.Fatalf("SliceOf = %d, want %d", got, tc.golden)
			}
		})
	}
}

func TestSliceOfDeterministicAndInRange(t *testing.T) {
	scope := tenantScope(t, "acme")
	first := SliceOf(scope, "order-1")
	for i := 0; i < 100; i++ {
		if got := SliceOf(scope, "order-1"); got != first {
			t.Fatalf("not deterministic: %d != %d", got, first)
		}
	}
	for i := 0; i < 10000; i++ {
		if s := SliceOf(scope, fmt.Sprintf("id-%d", i)); s >= SliceCount {
			t.Fatalf("slice %d out of range [0,%d)", s, SliceCount)
		}
	}
	if s := sliceOf(scope, "x", 7); s >= 7 {
		t.Fatalf("slice %d out of range [0,7)", s)
	}
}

// The scope-separation checks compare the full 64-bit hash so they do not
// depend on the 1-in-1024 chance of two distinct keys sharing a slice.
func TestSliceOfScopeSeparation(t *testing.T) {
	t.Run("unscoped differs from tenant named unscoped", func(t *testing.T) {
		if sliceHash(Unscoped(), "e") == sliceHash(tenantScope(t, "unscoped"), "e") {
			t.Fatal("Unscoped collides with tenant \"unscoped\"")
		}
	})
	t.Run("same id in two tenants", func(t *testing.T) {
		if sliceHash(tenantScope(t, "acme"), "e") == sliceHash(tenantScope(t, "globex"), "e") {
			t.Fatal("same id in two tenants hashed identically")
		}
	})
	t.Run("key ambiguity", func(t *testing.T) {
		if sliceHash(tenantScope(t, "a"), "bc") == sliceHash(tenantScope(t, "ab"), "c") {
			t.Fatal(`("a","bc") collides with ("ab","c")`)
		}
	})
	t.Run("invalid scope is distinct and does not panic", func(t *testing.T) {
		if sliceHash(Scope{}, "e") == sliceHash(Unscoped(), "e") {
			t.Fatal("invalid scope collides with Unscoped")
		}
	})
}

func TestSliceOfDistribution(t *testing.T) {
	const keys = 100 * SliceCount
	counts := make([]int, SliceCount)
	scope := tenantScope(t, "acme")
	for i := 0; i < keys; i++ {
		counts[SliceOf(scope, fmt.Sprintf("entity-%d", i))]++
	}
	// The mean is 100 per slice; the bounds are loose on purpose. This guards
	// against a degenerate hash, not against statistical noise.
	for s, c := range counts {
		if c < 40 || c > 180 {
			t.Fatalf("slice %d holds %d keys, expected about 100", s, c)
		}
	}
}

// TestSliceOfIndependentOfTopology computes the slices of a fixed corpus under
// topology 1, 3 and 5 and asserts they are identical. Honest caveat: SliceOf
// takes no topology input, so this holds by construction. The test documents
// and guards that contract (a future signature that adds topology or cluster
// state would have to change this test) rather than exercising a real cluster.
func TestSliceOfIndependentOfTopology(t *testing.T) {
	type entry struct {
		scope Scope
		id    string
	}
	var corpus []entry
	for _, sc := range []Scope{Unscoped(), tenantScope(t, "acme"), tenantScope(t, "globex")} {
		for i := 0; i < 200; i++ {
			corpus = append(corpus, entry{sc, fmt.Sprintf("entity-%d", i)})
		}
	}
	compute := func(nodes int) []uint64 {
		_ = nodes // topology is deliberately not an input of SliceOf
		out := make([]uint64, len(corpus))
		for i, c := range corpus {
			out[i] = SliceOf(c.scope, c.id)
		}
		return out
	}
	base := compute(1)
	for _, nodes := range []int{3, 5} {
		t.Run(fmt.Sprintf("nodes=%d", nodes), func(t *testing.T) {
			got := compute(nodes)
			for i := range base {
				if got[i] != base[i] {
					t.Fatalf("entry %d: slice %d with %d nodes, %d with 1", i, got[i], nodes, base[i])
				}
			}
		})
	}
}
