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
	"fmt"

	"github.com/getsyntegrity/urd/internal/actoridentity"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/tenancy"
)

// qualifiesActorNames reports whether an engine built with resolver names its
// actors after (tenant, entity ID) instead of the bare entity ID.
//
// A multi-tenant engine does, so two tenants that use the same ID get two
// independent actors (EGO-TENANT-009). The two modes that have exactly one
// tenant do not, and keep the names they always had:
//
//   - legacy mode (no resolver): there is no tenant to qualify with.
//   - single-tenant mode (a tenancy.FixedTenantResolver that reports a fixed
//     tenant, such as tenancy.WithSingleTenant): every caller is the same
//     tenant, so the bare ID is already unambiguous, and zero-plumbing means
//     existing names, ActorOf lookups and cluster placement stay as they are.
func qualifiesActorNames(resolver tenancy.TenantResolver) bool {
	if resolver == nil {
		return false
	}
	_, fixed := tenancy.FixedTenantOf(resolver)
	return !fixed
}

// actorName returns the GoAkt actor name of entity id of tenantID: the
// qualified name in a multi-tenant engine, the bare id otherwise. It is the
// only way the engine builds an actor name, so spawn, dispatch, lookup and
// respawn address the same actor. tenantID is ignored when the engine does not
// qualify names.
func (engine *Engine) actorName(tenantID, id string) (string, error) {
	if !qualifiesActorNames(engine.tenantResolver) {
		return actoridentity.InNamespace(engine.actorNamespace, id)
	}
	name, err := actoridentity.Qualify(tenantID, id)
	if err != nil {
		return "", fmt.Errorf("cannot address entity %q of tenant %q: %w", id, tenantID, err)
	}
	return actoridentity.InNamespace(engine.actorNamespace, name)
}

// actorNameFor resolves the actor name of entity id for the already-resolved
// caller identity tc. A caller with no tenant (administrative or zero scope)
// cannot name an actor of any tenant, so in a multi-tenant engine it is denied
// here, before anything is sent; no administrative bypass is supported (openspec/changes/ego-tenant-008).
func (engine *Engine) actorNameFor(tc tenancy.TenantContext, id string) (string, error) {
	if !qualifiesActorNames(engine.tenantResolver) {
		return actoridentity.InNamespace(engine.actorNamespace, id)
	}
	tenantID, ok := tc.Tenant()
	if !ok {
		return "", fmt.Errorf("cannot address entity %q: the caller carries no tenant, got %s: %w", id, tc.Scope(), tenancy.ErrDenied)
	}
	return engine.actorName(string(tenantID), id)
}

// spawnActorName returns the actor name for a spawn of entity id under the
// scope spawnTenantScope chose. scope is nil in legacy mode, where the name is
// the bare id.
func (engine *Engine) spawnActorName(scope *extensions.EntityTenantScope, id string) (string, error) {
	if scope == nil {
		return actoridentity.InNamespace(engine.actorNamespace, id)
	}
	return engine.actorName(scope.TenantID, id)
}

// lookupActorName resolves the actor name of entity id for a lookup that has no
// command to carry the caller's identity (EntityExists). In a multi-tenant
// engine it resolves the caller's tenant exactly as Dispatch does, so a caller
// only ever sees the entities of its own tenant.
func (engine *Engine) lookupActorName(ctx context.Context, id string) (string, error) {
	if !qualifiesActorNames(engine.tenantResolver) {
		return actoridentity.InNamespace(engine.actorNamespace, id)
	}
	tenantContext, err := engine.tenantResolver.Resolve(ctx)
	if err != nil {
		return "", err
	}
	return engine.actorNameFor(tenantContext, id)
}
