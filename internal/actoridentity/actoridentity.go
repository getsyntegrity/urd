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

// Package actoridentity derives the GoAkt actor name of an entity, durable
// state entity or saga from the tenant it belongs to and its business ID
// (EGO-TENANT-009).
//
// In a tenant-aware engine the actor's identity is the pair (tenant, ID), not
// the bare ID: two tenants that use the same ID must get two independent
// actors. GoAkt addresses actors by one string, so the pair has to be encoded
// into a name. Qualify is the only function that does it; the engine and the
// actors call it instead of building names themselves, so spawn, dispatch,
// lookup, relocation and the actor's own PreStart check agree by construction.
//
// The name is only an address. The business ID and the persistence ID stay
// the bare ID, and the records an actor persists never carry the name.
package actoridentity

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	// prefix opens every qualified name. It starts with a letter, as GoAkt
	// requires of an actor name, and never with GoAkt's reserved "GoAkt".
	prefix = "t"

	// separator splits the three fields of a qualified name.
	separator = "."

	// maxNameLen is GoAkt's limit on the length of an actor name.
	maxNameLen = 255
)

var (
	// ErrEmptyTenant is returned when the tenant is empty: a qualified name
	// without a tenant would collide with nothing and isolate nothing.
	ErrEmptyTenant = errors.New("urd: actor identity requires a tenant")

	// ErrEmptyID is returned when the business ID is empty.
	ErrEmptyID = errors.New("urd: actor identity requires an entity id")

	// ErrNameTooLong is returned when the qualified name would exceed
	// GoAkt's actor name limit.
	ErrNameTooLong = errors.New("urd: qualified actor name exceeds the maximum length")

	// ErrMismatch is returned by Verify when an actor's name is not the name
	// its tenant and ID produce.
	ErrMismatch = errors.New("urd: actor name does not match its tenant and entity id")
)

// encoding turns the tenant into text that is valid in an actor name and that
// never contains the separator. TenantID is any UTF-8 string up to 128 bytes,
// so it can hold dots, slashes, spaces or any rune a GoAkt name rejects; the
// URL-safe base64 alphabet (A-Z a-z 0-9 - _) is inside GoAkt's name alphabet
// and has no dot.
var encoding = base64.RawURLEncoding

// Qualify returns the actor name for the entity id of tenantID.
//
// The name is prefix + "." + base64url(tenant) + "." + id. Because the encoded
// tenant cannot contain a dot, the first two dots always end the prefix and
// the tenant, and everything after them is the ID, dots included. The
// encoding is therefore injective: two different (tenant, id) pairs never
// produce the same name, whatever characters either value holds. The id is
// not rewritten, so GoAkt still rejects an id outside its name alphabet
// exactly as it does for an unqualified name.
func Qualify(tenantID, id string) (string, error) {
	if tenantID == "" {
		return "", ErrEmptyTenant
	}
	if id == "" {
		return "", ErrEmptyID
	}

	name := prefix + separator + encoding.EncodeToString([]byte(tenantID)) + separator + id
	if len(name) > maxNameLen {
		return "", fmt.Errorf("%w: %d > %d bytes", ErrNameTooLong, len(name), maxNameLen)
	}
	return name, nil
}

// Parse is the inverse of Qualify. It reports false when name is not a
// qualified name. Production code addresses actors with Qualify alone; Parse
// exists so the encoding can be proven reversible.
func Parse(name string) (tenantID, id string, ok bool) {
	rest, found := strings.CutPrefix(name, prefix+separator)
	if !found {
		return "", "", false
	}
	encoded, id, found := strings.Cut(rest, separator)
	if !found || encoded == "" || id == "" {
		return "", "", false
	}
	tenant, err := encoding.DecodeString(encoded)
	if err != nil || len(tenant) == 0 {
		return "", "", false
	}
	return string(tenant), id, true
}

// Verify checks that an actor named actorName is the actor tenantID and id
// identify. An actor calls it in PreStart in a tenant-aware engine, before it
// reads its store: it proves that the tenant the actor carries, which GoAkt
// serializes with the actor when it relocates it to another node, is the one
// its address was derived from.
func Verify(actorName, tenantID, id string) error {
	want, err := Qualify(tenantID, id)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMismatch, err)
	}
	if actorName != want {
		return fmt.Errorf("%w: actor %q is not the actor of that tenant and id", ErrMismatch, actorName)
	}
	return nil
}
