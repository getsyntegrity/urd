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

// provisionalSliceCount is the number of logical slices (scope, entity id)
// pairs are mapped onto. It is PROVISIONAL: the final N (256 or 1024) is an
// open maintainer decision tracked with #350 and #351, see
// docs/decisions/logical-slices.md. Until that decision is recorded, the
// constant and the functions below are unexported on purpose, so no public API
// commits to a value that may still change. They are not wired to any write
// path yet.
//
// Whatever N is finally chosen, changing it later, changing the hash, or
// changing the key encoding reassigns existing entities to other slices. Each
// of those is a data migration of every persisted shard_number and of every
// projection offset keyed by it (see the decision doc), not a code-only edit.
const provisionalSliceCount = 1024

// FNV-1a 64-bit parameters. The hash is specified (not seeded, not
// runtime-dependent), so a slice is stable across processes, restarts,
// versions and architectures. Do not replace it with hash/maphash.
const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

// Key-layout markers. Each is the first byte of the hashed key so that the
// three Scope states can never produce the same byte stream.
const (
	sliceKeyUnscoped byte = 0x00
	sliceKeyTenant   byte = 0x01
	sliceKeyInvalid  byte = 0xFF
)

// sliceOfProvisional returns the logical slice, in [0, provisionalSliceCount),
// that owns the entity identified by the pair (scope, entityID).
//
// It is a pure function: the result depends only on its arguments and never on
// cluster topology, node count, time or process state. Independence from
// topology therefore holds by construction.
//
// The hashed key is unambiguous. A tenant scope is encoded as a marker byte,
// the tenant length as a uvarint, the tenant bytes, a 0x00 separator and the
// entity id, so ("a", "bc") and ("ab", "c") differ. Unscoped uses its own
// marker and carries no tenant, so it cannot collide with a tenant whose id
// happens to read "unscoped". An invalid (zero) Scope gets a third marker
// rather than panicking; callers are expected to have rejected it already.
func sliceOfProvisional(scope Scope, entityID string) uint64 {
	return sliceOfN(scope, entityID, provisionalSliceCount)
}

// sliceOfN is sliceOfProvisional with an explicit slice count n, which must be
// > 0. It lets tests exercise other moduli and the 256 candidate.
func sliceOfN(scope Scope, entityID string, n uint64) uint64 {
	return sliceHash(scope, entityID) % n
}

// sliceHash returns the full 64-bit FNV-1a hash of the unambiguous key.
func sliceHash(scope Scope, entityID string) uint64 {
	h := fnvOffset64
	mix := func(b byte) {
		h ^= uint64(b)
		h *= fnvPrime64
	}

	switch {
	case scope.IsUnscoped():
		mix(sliceKeyUnscoped)
	case scope.Valid():
		tenant := string(scope.TenantID())
		mix(sliceKeyTenant)
		// uvarint length prefix
		for l := uint64(len(tenant)); ; l >>= 7 {
			if l < 0x80 {
				mix(byte(l))
				break
			}
			mix(byte(l) | 0x80)
		}
		for i := 0; i < len(tenant); i++ {
			mix(tenant[i])
		}
		mix(0x00)
	default:
		mix(sliceKeyInvalid)
	}
	for i := 0; i < len(entityID); i++ {
		mix(entityID[i])
	}
	return h
}
