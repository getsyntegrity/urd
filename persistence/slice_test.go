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
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
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

// The golden vectors pin the CANDIDATE algorithm (FNV-1a 64, the key layout and
// N=1024). They do not ratify any of the three: N, hash and encoding are still
// pending a maintainer decision (P1 in the ADR, #350). They exist so that the
// provisional function cannot drift silently. If one changes, every shard_number
// computed with it would change: that is a data migration, not a test update.
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

// referenceSliceKey builds the key bytes straight from the encoding in the
// decision doc, with literal markers and the standard library's uvarint, so it
// shares no code with sliceHash. The layout is:
//
//	unscoped: 0x00 || id
//	tenant:   0x01 || uvarint(len(tenant)) || tenant || 0x00 || id
//	invalid:  0xFF || id
func referenceSliceKey(kind string, tenant, id string) []byte {
	switch kind {
	case "unscoped":
		return append([]byte{0x00}, id...)
	case "invalid":
		return append([]byte{0xFF}, id...)
	}
	key := binary.AppendUvarint([]byte{0x01}, uint64(len(tenant)))
	key = append(key, tenant...)
	key = append(key, 0x00)
	return append(key, id...)
}

// referenceFNV1a64 is the standard library FNV-1a 64 over key.
func referenceFNV1a64(key []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(key)
	return h.Sum64()
}

// decodeReferenceKey parses a reference key back into its parts, failing on any
// malformed input. A key that decodes to exactly one (kind, tenant, id) is what
// makes the encoding unambiguous.
func decodeReferenceKey(key []byte) (kind, tenant, id string, ok bool) {
	if len(key) == 0 {
		return "", "", "", false
	}
	switch key[0] {
	case 0x00:
		return "unscoped", "", string(key[1:]), true
	case 0xFF:
		return "invalid", "", string(key[1:]), true
	case 0x01:
		n, read := binary.Uvarint(key[1:])
		if read <= 0 {
			return "", "", "", false
		}
		rest := key[1+read:]
		if uint64(len(rest)) < n+1 || rest[n] != 0x00 {
			return "", "", "", false
		}
		return "tenant", string(rest[:n]), string(rest[n+1:]), true
	}
	return "", "", "", false
}

type fullHashCase struct {
	name   string
	kind   string // "unscoped", "tenant" or "invalid"
	tenant string
	id     string
	hash   uint64 // full 64-bit FNV-1a of the reference key
	slice  uint64 // hash mod 1024
}

func (c fullHashCase) scope(ctx *specs.Context) Scope {
	switch c.kind {
	case "unscoped":
		return Unscoped()
	case "invalid":
		return Scope{}
	}
	return sliceTenant(ctx, c.tenant)
}

// fullHashCases hold the full 64-bit hash, not only hash mod N. Every value was
// computed with an FNV-1a implementation that shares no code with sliceHash.
// The tenants respect the real Scope validation (valid UTF-8, 1 to 128 bytes, no
// control runes), so a three-byte uvarint prefix is out of reach for a valid
// Scope; 127 and 128 bytes cover the one-byte and two-byte prefixes.
var fullHashCases = []fullHashCase{
	{"unscoped, empty id", "unscoped", "", "", 12638153115695167455, 991},
	{"unscoped, order-1", "unscoped", "", "order-1", 6871610578994667679, 159},
	{"tenant acme, order-1", "tenant", "acme", "order-1", 11339623132362836548, 580},
	{"tenant a, bc", "tenant", "a", "bc", 7728568150747082109, 381},
	{"tenant unscoped, order-1", "tenant", "unscoped", "order-1", 397333059734761537, 65},
	{"127-byte tenant (one-byte uvarint prefix 0x7f)", "tenant", strings.Repeat("t", 127), "e", 1121643126153532000, 608},
	{"128-byte tenant (two-byte uvarint prefix 0x80 0x01)", "tenant", strings.Repeat("t", 128), "e", 3349406442526314680, 184},
	{"unicode tenant and id", "tenant", "ñandú-日本", "pedido-№-✓", 1552433202739978905, 665},
	{"128-byte tenant of 44 runes (length is bytes, not runes)", "tenant", strings.Repeat("日", 42) + "ab", "e", 8064835956696314239, 383},
	{"id containing a NUL byte", "tenant", "acme", "a\x00b", 3112584782578643123, 179},
	{"invalid zero scope", "invalid", "", "e", 763715312229376945, 945},
}

// These cases pin the FULL 64-bit hash and compare it with the standard library.
// Equality modulo 1024 alone would not prove that a refactor (for example the
// opaque Scope of #349) kept the hashed bytes: many different hashes share a
// slice. The full value is what must survive such a change.
func TestSliceHashFullVectors(t *testing.T) {
	specs.Describe(t, "sliceHash full 64-bit vectors (candidate algorithm, not ratified)", func(s *specs.Spec) {
		specs.Table(s, fullHashCases, func(c fullHashCase) string { return c.name }, func(ctx *specs.Context, c fullHashCase) {
			key := referenceSliceKey(c.kind, c.tenant, c.id)
			got := sliceHash(c.scope(ctx), c.id)

			ctx.Expect(got).ToEqual(c.hash)
			ctx.Expect(referenceFNV1a64(key)).ToEqual(c.hash)
			ctx.Expect(got % provisionalSliceCount).ToEqual(c.slice)
			ctx.Expect(sliceOfProvisional(c.scope(ctx), c.id)).ToEqual(c.slice)
		})

		s.It("exercises both uvarint widths the Scope validation allows", func(ctx *specs.Context) {
			one := referenceSliceKey("tenant", strings.Repeat("t", 127), "e")
			two := referenceSliceKey("tenant", strings.Repeat("t", 128), "e")
			ctx.Expect(one[1:2]).ToEqual([]byte{0x7f})
			ctx.Expect(two[1:3]).ToEqual([]byte{0x80, 0x01})
		})

		s.It("rejects a tenant that would need a longer uvarint prefix", func(ctx *specs.Context) {
			_, err := NewTenantScope(tenancy.TenantID(strings.Repeat("t", 129)))
			ctx.Expect(errors.Is(err, ErrInvalidScope)).To(specs.BeTrue())
		})
	})
}

// The encoding is unambiguous: a key decodes to exactly one (kind, tenant, id).
// That is a property of the bytes. It says nothing about hash or slice
// collisions, which are possible; see the next test.
func TestSliceKeyEncodingIsUnambiguous(t *testing.T) {
	specs.Describe(t, "slice key encoding", func(s *specs.Spec) {
		s.It("round-trips every corpus entry, including ids that imitate the layout", func(ctx *specs.Context) {
			tenants := []string{"a", "ab", "unscoped", "ñandú-日本", strings.Repeat("t", 128), "é", "a b"}
			ids := []string{"", "c", "bc", "\x00", "a\x00b", "\x01\x01a\x00", "\xff", "日本", "unscoped"}
			for _, tenant := range tenants {
				for _, id := range ids {
					kind, gotTenant, gotID, ok := decodeReferenceKey(referenceSliceKey("tenant", tenant, id))
					ctx.Expect(ok).To(specs.BeTrue())
					ctx.Expect(kind).ToEqual("tenant")
					ctx.Expect(gotTenant).ToEqual(tenant)
					ctx.Expect(gotID).ToEqual(id)
				}
			}
			for _, id := range ids {
				kind, _, gotID, ok := decodeReferenceKey(referenceSliceKey("unscoped", "", id))
				ctx.Expect(ok).To(specs.BeTrue())
				ctx.Expect(kind).ToEqual("unscoped")
				ctx.Expect(gotID).ToEqual(id)
			}
		})

		s.It("separates scope from entity when their concatenation is equal", func(ctx *specs.Context) {
			type pair struct{ tenant, id string }
			// Each group shares tenant+id as one string; a plain concatenation would
			// make them indistinguishable.
			groups := [][]pair{
				{{"a", "bc"}, {"ab", "c"}},
				{{"日", "本"}, {"日本", ""}},
				{{"acme", "\x00x"}, {"acme\x00", "x"}},
			}
			for _, group := range groups {
				seen := map[string]pair{}
				for _, p := range group {
					key := string(referenceSliceKey("tenant", p.tenant, p.id))
					_, dup := seen[key]
					ctx.Expect(dup).To(specs.BeFalse())
					seen[key] = p
				}
			}
		})

		s.It("rejects the control-rune tenants that would otherwise forge a separator", func(ctx *specs.Context) {
			_, err := NewTenantScope(tenancy.TenantID("acme\x00"))
			ctx.Expect(errors.Is(err, ErrInvalidScope)).To(specs.BeTrue())
		})

		s.It("hashes the Unscoped, tenant and invalid markers apart for the same id", func(ctx *specs.Context) {
			hashes := map[uint64]string{}
			for name, scope := range map[string]Scope{"unscoped": Unscoped(), "tenant": sliceTenant(ctx, "unscoped"), "invalid": {}} {
				h := sliceHash(scope, "e")
				_, dup := hashes[h]
				ctx.Expect(dup).To(specs.BeFalse())
				hashes[h] = name
			}
		})
	})
}

// An unambiguous encoding does not make hashes or slices unique. With N=1024, any
// 1025 keys must share a slice by the pigeonhole principle. Isolation between
// scopes therefore cannot rest on the slice; it rests on keeping the Scope next
// to the persistence id wherever the pair is stored or compared.
func TestSliceCollisionsAreExpected(t *testing.T) {
	specs.Describe(t, "slice collisions", func(s *specs.Spec) {
		s.It("finds distinct keys, in distinct scopes, that share a slice", func(ctx *specs.Context) {
			a, b := sliceTenant(ctx, "acme"), sliceTenant(ctx, "globex")
			bySlice := map[uint64]string{}
			var found bool
			for i := 0; i <= provisionalSliceCount && !found; i++ {
				id := fmt.Sprintf("id-%d", i)
				bySlice[sliceOfProvisional(a, id)] = id
				if other, ok := bySlice[sliceOfProvisional(b, id)]; ok {
					found = true
					ctx.Expect(sliceOfProvisional(a, other)).ToEqual(sliceOfProvisional(b, id))
					ctx.Expect(a.Equal(b)).To(specs.BeFalse())
				}
			}
			ctx.Expect(found).To(specs.BeTrue())
		})

		s.It("shows that equality modulo 1024 does not prove the same hashed bytes", func(ctx *specs.Context) {
			scope := sliceTenant(ctx, "acme")
			bySlice := map[uint64]uint64{}
			var sameSliceDifferentHash bool
			for i := 0; i <= provisionalSliceCount && !sameSliceDifferentHash; i++ {
				h := sliceHash(scope, fmt.Sprintf("id-%d", i))
				if prev, ok := bySlice[h%provisionalSliceCount]; ok && prev != h {
					sameSliceDifferentHash = true
				}
				bySlice[h%provisionalSliceCount] = h
			}
			ctx.Expect(sameSliceDifferentHash).To(specs.BeTrue())
		})
	})
}
