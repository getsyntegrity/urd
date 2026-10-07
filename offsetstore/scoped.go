package offsetstore

import (
	"context"
	"errors"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
)

// ErrScopeUnsupported rejects tenant projections on an offset adapter that
// cannot isolate their progress. Falling back to a shared cursor is unsafe.
var ErrScopeUnsupported = errors.New("offsetstore: tenant scope is not supported")

// ScopedOffsetStore keys progress and resets structurally by persistence scope,
// projection and shard. Existing OffsetStore methods address Unscoped only.
// Never derive scope from a projection or actor name.
type ScopedOffsetStore interface {
	OffsetStore
	WriteScopedOffset(context.Context, persistence.Scope, *egopb.Offset) error
	GetScopedOffset(context.Context, persistence.Scope, *egopb.ProjectionId) (*egopb.Offset, error)
	ResetScopedOffset(context.Context, persistence.Scope, string, int64) error
}

type boundStore struct {
	ScopedOffsetStore
	scope persistence.Scope
}

func (b *boundStore) WriteOffset(ctx context.Context, value *egopb.Offset) error {
	return b.WriteScopedOffset(ctx, b.scope, value)
}
func (b *boundStore) GetCurrentOffset(ctx context.Context, id *egopb.ProjectionId) (*egopb.Offset, error) {
	return b.GetScopedOffset(ctx, b.scope, id)
}
func (b *boundStore) ResetOffset(ctx context.Context, name string, value int64) error {
	return b.ResetScopedOffset(ctx, b.scope, name, value)
}

// ForScope binds every offset operation to one scope. The legacy API remains
// compatible for Unscoped; a tenant needs explicit support from the adapter.
func ForScope(store OffsetStore, scope persistence.Scope) (OffsetStore, error) {
	if !scope.Valid() {
		return nil, persistence.ErrInvalidScope
	}
	if store == nil {
		return nil, ErrScopeUnsupported
	}
	if bound, ok := store.(*boundStore); ok {
		if bound.scope.Equal(scope) {
			return bound, nil
		}
		store = bound.ScopedOffsetStore
	}
	if scope.IsUnscoped() {
		return store, nil
	}
	scoped, ok := store.(ScopedOffsetStore)
	if !ok {
		return nil, ErrScopeUnsupported
	}
	return &boundStore{ScopedOffsetStore: scoped, scope: scope}, nil
}
