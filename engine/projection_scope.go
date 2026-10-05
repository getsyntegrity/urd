/*
 * MIT License
 *
 * Copyright (c) 2022-2026 Arsene Tochemey Gandote
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy of
 * this software and associated documentation files (the "Software"), to deal in
 * the Software without restriction, including without limitation the rights to
 * use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
 * the Software, and to permit persons to whom the Software is furnished to do so,
 * subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
 * FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
 * COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
 * IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
 * CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 */

package engine

import (
	"fmt"

	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/projection"
	"github.com/getsyntegrity/urd/tenancy"
)

// projectionRegistration is one WithProjection call, captured at registration.
// The options are a copy and the declared scope is a value copy, so neither
// later changes to the caller's Options nor to the variable its Scope pointer
// referred to can alter the registration.
type projectionRegistration struct {
	name     string
	options  projection.Options
	declared bool
	scope    persistence.Scope
}

// newProjectionRegistration copies options and the scope it points to.
func newProjectionRegistration(name string, options *projection.Options) projectionRegistration {
	reg := projectionRegistration{name: name, options: *options}
	if options.Scope != nil {
		reg.declared = true
		reg.scope = *options.Scope
		copied := reg.scope
		reg.options.Scope = &copied
	}
	return reg
}

// projectionKey is the registry identity of a projection: (scope, name).
type projectionKey struct {
	scope persistence.Scope
	name  string
}

// resolveProjectionScope applies the tenancy-mode rules to one registration
// and returns its effective scope. It reads the resolver only through
// tenancy.FixedTenantOf and never calls Resolve.
func resolveProjectionScope(resolver tenancy.TenantResolver, reg projectionRegistration) (persistence.Scope, error) {
	if reg.declared && !reg.scope.Valid() {
		return persistence.Scope{}, fmt.Errorf("%w: projection %q declares an invalid scope", ErrProjectionScopeRejected, reg.name)
	}

	if resolver == nil {
		if !reg.declared {
			return persistence.Unscoped(), nil
		}
		if !reg.scope.IsUnscoped() {
			return persistence.Scope{}, fmt.Errorf("%w: projection %q declares tenant scope %s on an engine without tenancy", ErrProjectionScopeRejected, reg.name, reg.scope)
		}
		return reg.scope, nil
	}

	fixed, hasFixed := tenancy.FixedTenantOf(resolver)
	if hasFixed {
		fixedScope, err := persistence.NewTenantScope(fixed)
		if err != nil {
			return persistence.Scope{}, fmt.Errorf("%w: projection %q: fixed tenant is not a valid scope: %w", ErrProjectionScopeRejected, reg.name, err)
		}
		if !reg.declared {
			return fixedScope, nil
		}
		if !reg.scope.Equal(fixedScope) {
			return persistence.Scope{}, fmt.Errorf("%w: projection %q declares scope %s on an engine fixed to %s", ErrProjectionScopeRejected, reg.name, reg.scope, fixedScope)
		}
		return reg.scope, nil
	}

	if !reg.declared {
		return persistence.Scope{}, fmt.Errorf("%w: projection %q", ErrProjectionScopeUndetermined, reg.name)
	}
	if reg.scope.IsUnscoped() {
		return persistence.Scope{}, fmt.Errorf("%w: projection %q declares Unscoped() on a tenant-aware engine", ErrProjectionScopeRejected, reg.name)
	}
	return reg.scope, nil
}

// resolveProjections validates every registration against the engine's tenancy
// mode, in registration order, and returns the normalized options keyed by
// name for the projection extension. Each returned Options carries its
// effective scope. It does not depend on the order in which WithProjection and
// WithTenantResolver were applied.
func (c *Config) resolveProjections() (map[string]*projection.Options, error) {
	if len(c.projections) == 0 {
		return nil, nil
	}
	seen := make(map[projectionKey]struct{}, len(c.projections))
	scopeOf := make(map[string]persistence.Scope, len(c.projections))
	resolved := make(map[string]*projection.Options, len(c.projections))
	for _, reg := range c.projections {
		scope, err := resolveProjectionScope(c.tenantResolver, reg)
		if err != nil {
			return nil, err
		}
		key := projectionKey{scope: scope, name: reg.name}
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("%w: projection %q under scope %s", ErrProjectionDuplicate, reg.name, scope)
		}
		seen[key] = struct{}{}
		if other, ok := scopeOf[reg.name]; ok && !other.Equal(scope) {
			return nil, fmt.Errorf("%w: projection %q", ErrProjectionNameAmbiguous, reg.name)
		}
		scopeOf[reg.name] = scope

		normalized := reg.options
		if normalized.Recovery == nil {
			normalized.Recovery = projection.NewRecovery()
		}
		effective := scope
		normalized.Scope = &effective
		resolved[reg.name] = &normalized
	}
	return resolved, nil
}
