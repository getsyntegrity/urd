package engine

import (
	"context"
	"errors"
	"fmt"

	goakt "github.com/tochemey/goakt/v4/actor"
	goakterrors "github.com/tochemey/goakt/v4/errors"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
)

// ErrSpawnIdentityMismatch means a name is already bound to an incompatible
// actor family, behavior definition, namespace or persistence scope.
var ErrSpawnIdentityMismatch = errors.New("urd: incompatible actor identity")

// ActorIdentityError is the typed, fail-closed outcome of an incompatible
// idempotent spawn. It reports only the requested actor name.
type ActorIdentityError struct{ Name string }

func (e *ActorIdentityError) Error() string {
	return fmt.Sprintf("%v: %q", ErrSpawnIdentityMismatch, e.Name)
}
func (e *ActorIdentityError) Unwrap() error { return ErrSpawnIdentityMismatch }

func (engine *Engine) bindingQuery(family string, behavior any, tenant *extensions.EntityTenantScope) *egopb.ActorBindingQuery {
	query := &egopb.ActorBindingQuery{Family: family, Definition: protocol.DefinitionOf(behavior), Namespace: engine.actorNamespace}
	if tenant != nil {
		query.TenantAware = true
		query.TenantId = tenant.TenantID
	}
	return query
}

func verifySpawnedIdentity(ctx context.Context, pid *goakt.PID, tenant *extensions.EntityTenantScope, query *egopb.ActorBindingQuery) error {
	if err := verifySpawnedTenant(ctx, pid, tenant); err != nil {
		return err
	}
	reply, err := goakt.Ask(ctx, pid, query, spawnBindingQueryTimeout)
	if err != nil {
		return fmt.Errorf("%w: actor %q did not answer identity query: %w", ErrSpawnTenantUnverified, pid.Name(), err)
	}
	answer, ok := reply.(*egopb.ActorBindingReply)
	if !ok || !answer.GetMatches() {
		return &ActorIdentityError{Name: pid.Name()}
	}
	return nil
}

func resolveIdentitySpawn(ctx context.Context, sys goakt.ActorSystem, name string, tenant *extensions.EntityTenantScope, query *egopb.ActorBindingQuery, spawnErr error) error {
	if !errors.Is(spawnErr, goakterrors.ErrActorAlreadyExists) {
		return spawnErr
	}
	pid, err := sys.ActorOf(ctx, name)
	if err != nil {
		return errors.Join(spawnErr, err)
	}
	return verifySpawnedIdentity(ctx, pid, tenant, query)
}
