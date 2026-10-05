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

package eventstream

import (
	"errors"

	"github.com/getsyntegrity/urd/tenancy"
)

// ErrInvalidScope is returned when a zero-value Scope, or a tenant id that
// fails tenancy.NewTenantID, is used to publish or subscribe.
var ErrInvalidScope = errors.New("eventstream: invalid scope")

type scopeKind uint8

const (
	scopeInvalid scopeKind = iota // the zero value
	scopeUnscoped
	scopeTenant
)

// Scope says which tenant boundary a publication or subscription belongs to.
// It mirrors persistence.Scope on purpose but depends only on package tenancy:
// eventstream is reachable from the consumer-side closure that
// internal/runtimeconsumer pins (#147), which must not grow persistence.
//
// The zero value is invalid, so an unset Scope can never fall through to a
// meaningful one. Unscoped is the single-tenant scope; it is never equal to a
// tenant scope, even one named "unscoped". Scope is comparable.
type Scope struct {
	kind   scopeKind
	tenant tenancy.TenantID
}

// Unscoped returns the scope of an engine without tenancy, and of the legacy
// Publish and Subscribe.
func Unscoped() Scope { return Scope{kind: scopeUnscoped} }

// TenantScope returns the scope of tenant id, revalidated through
// tenancy.NewTenantID. It returns an error matching ErrInvalidScope when id is
// not valid.
func TenantScope(id tenancy.TenantID) (Scope, error) {
	validated, err := tenancy.NewTenantID(string(id))
	if err != nil {
		return Scope{}, errors.Join(ErrInvalidScope, err)
	}
	return Scope{kind: scopeTenant, tenant: validated}, nil
}

// Valid reports whether s was built by Unscoped or TenantScope.
func (s Scope) Valid() bool { return s.kind == scopeUnscoped || s.kind == scopeTenant }

// IsUnscoped reports whether s is Unscoped().
func (s Scope) IsUnscoped() bool { return s.kind == scopeUnscoped }

// TenantID returns the tenant of a tenant scope, and "" for any other scope.
func (s Scope) TenantID() tenancy.TenantID {
	if s.kind == scopeTenant {
		return s.tenant
	}
	return ""
}

// Equal reports whether s and other are the same scope.
func (s Scope) Equal(other Scope) bool { return s == other }

// String describes s for logs. It is not a routing key.
func (s Scope) String() string {
	switch s.kind {
	case scopeUnscoped:
		return "scope(unscoped)"
	case scopeTenant:
		return "scope(tenant=" + string(s.tenant) + ")"
	default:
		return "scope(invalid)"
	}
}
