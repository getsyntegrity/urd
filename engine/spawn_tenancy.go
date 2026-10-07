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
	"time"

	goakt "github.com/tochemey/goakt/v4/actor"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/tenancy"
)

// spawnTenantScope determines the per-spawn tenant dependency to inject for
// a single entity/durable-state/saga spawn (TENANT-003 T4).
//
// It returns (nil, nil) in legacy mode (engine.tenantResolver == nil):
// callers must inject nothing in that case, keeping spawn dependencies
// byte-identical to pre-TENANT-003 behavior.
//
// This function deliberately never calls TenantResolver.Resolve. An earlier
// design did, at spawn time, which CI caught as a violation of
// Resolve-Once, Propagate-After (openspec/specs/tenancy-core/spec.md's
// tenancy-core requirement): TestSendCommandResolverSwapIdenticalSequence
// observed a resolver invoked twice (once at spawn, once at SendCommand)
// for a single spawn-plus-command sequence. The corrected design resolves
// the tenant exactly once, at the command trust boundary (Dispatch,
// SagaStatus), and determines the spawn-time tenant from two sources that
// require no Resolve call at all, in this order:
//
//  1. config.tenantID, set by the application via engine.WithTenant(id) — the
//     application declares which tenant this entity/durable-state
//     entity/saga belongs to, rather than the engine inferring it.
//  2. the registered resolver's fixed tenant, when it implements
//     tenancy.FixedTenantResolver and reports one. tenancy.WithSingleTenant
//     always does, which is what lets a single-tenant deployment spawn
//     entities without ever passing engine.WithTenant (acceptance criterion 6).
//
// If neither source yields a tenant, the spawn fails closed with
// ErrSpawnTenantUndetermined: falling back to persistence.Unscoped() would
// silently defeat the isolation TENANT-003 exists to enforce.
func (engine *Engine) spawnTenantScope(config *spawnConfig) (*extensions.EntityTenantScope, error) {
	if engine.tenantResolver == nil {
		return nil, nil
	}

	if config.tenantID != "" {
		return extensions.NewEntityTenantScope(string(config.tenantID)), nil
	}

	if tenantID, hasFixed := tenancy.FixedTenantOf(engine.tenantResolver); hasFixed {
		return extensions.NewEntityTenantScope(string(tenantID)), nil
	}

	return nil, ErrSpawnTenantUndetermined
}

// spawnBindingQueryTimeout bounds how long verifySpawnedTenant waits for the
// returned actor to answer its TenantBindingQuery.
const spawnBindingQueryTimeout = 5 * time.Second

// verifySpawnedTenant proves that the actor a tenant-aware spawn returned is
// bound to the tenant that spawn declared (TENANT-003 T4). It is a no-op in
// legacy mode (requested == nil).
//
// GoAkt's Spawn returns an already-running actor's PID with a nil error, and
// concurrent spawns of one name coalesce onto a single execution — on the
// local node, and on the peer that serves a remote placement — so the PID
// may belong to an actor another spawn created, possibly on another node.
// The authority is therefore the returned actor itself: the engine asks it,
// with the engine-internal egopb.TenantBindingQuery, whether the binding
// its PreStart established (resolveScope) is the declared tenant. goakt
// delivers that request/reply to the node that owns the actor, so a local
// and a remote PID are verified the same way, and nothing about the answer
// is taken from the caller's own intent or from PID metadata. Whichever
// spawn created the actor fixed its tenant, so every other caller is
// compared against that fixed binding and there is no window between
// checking and spawning.
//
// This never calls TenantResolver.Resolve: requested was declared by the
// caller via WithTenant or the resolver's fixed tenant (spawnTenantScope).
func verifySpawnedTenant(ctx context.Context, pid *goakt.PID, requested *extensions.EntityTenantScope) error {
	if requested == nil {
		return nil
	}
	reply, err := goakt.Ask(ctx, pid, &egopb.TenantBindingQuery{TenantId: requested.TenantID}, spawnBindingQueryTimeout)
	if err != nil {
		return fmt.Errorf("%w: actor %q did not answer its tenant binding query: %w", ErrSpawnTenantUnverified, pid.Name(), err)
	}
	answer, ok := reply.(*egopb.TenantBindingReply)
	if !ok {
		return fmt.Errorf("%w: actor %q answered its tenant binding query with %T", ErrSpawnTenantUnverified, pid.Name(), reply)
	}
	return classifyTenantBinding(pid.Name(), requested, answer)
}

// classifyTenantBinding maps an actor's TenantBindingReply to the spawn
// outcome: a matching binding is an idempotent success, a different one is
// ErrSpawnTenantMismatch (also tenancy.ErrDenied), and an actor that holds
// no tenant binding at all is ErrSpawnTenantUnverified.
func classifyTenantBinding(name string, requested *extensions.EntityTenantScope, answer *egopb.TenantBindingReply) error {
	requestedTenant, err := tenancy.NewTenantContext(tenancy.TenantID(requested.TenantID))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSpawnTenantMismatch, err)
	}
	if !answer.GetTenantAware() {
		return fmt.Errorf("%w: actor %q holds no tenant binding", ErrSpawnTenantUnverified, name)
	}
	if !answer.GetMatches() {
		// The reply deliberately does not say which tenant the actor is bound
		// to; VerifyUnchanged against the zero TenantContext yields the
		// tenancy package's own typed denial for "a different identity".
		return fmt.Errorf("%w: actor %q is bound to another tenant: %w", ErrSpawnTenantMismatch, name,
			tenancy.VerifyUnchanged(requestedTenant, tenancy.TenantContext{}))
	}
	return nil
}
