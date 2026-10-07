package testkit_test

import (
	"context"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/offsetstore"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/testkit"
)

type legacyOffsets struct{ offsetstore.OffsetStore }

func TestScopedOffsets(t *testing.T) {
	specs.Describe(t, "progress and resets are isolated by scope", func(s *specs.Spec) {
		s.It("isolates equal projection names and shards, including Unscoped and a tenant named unscoped", func(sc *specs.Context) {
			bg := context.Background()
			store := testkit.NewOffsetStore()
			a, err := persistence.NewTenantScope("acme")
			sc.Expect(err).To(specs.BeNil())
			b, err := persistence.NewTenantScope("unscoped")
			sc.Expect(err).To(specs.BeNil())
			scopes := []persistence.Scope{persistence.Unscoped(), a, b}
			for i, scope := range scopes {
				for _, shard := range []uint64{1, 2} {
					sc.Expect(store.WriteScopedOffset(bg, scope, &egopb.Offset{ProjectionName: "same", ShardNumber: shard, Value: int64(i + 10)})).To(specs.BeNil())
				}
			}
			bound, err := offsetstore.ForScope(store, a)
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(bound.ResetOffset(bg, "same", 99)).To(specs.BeNil())
			for i, scope := range scopes {
				want := int64(i + 10)
				if scope.Equal(a) {
					want = 99
				}
				for _, shard := range []uint64{1, 2} {
					value, err := store.GetScopedOffset(bg, scope, &egopb.ProjectionId{ProjectionName: "same", ShardNumber: shard})
					sc.Expect(err).To(specs.BeNil())
					sc.Expect(value.GetValue()).ToEqual(want)
				}
			}
			sc.Expect(store.ResetOffset(bg, "same", 77)).To(specs.BeNil())
			value, err := bound.GetCurrentOffset(bg, &egopb.ProjectionId{ProjectionName: "same", ShardNumber: 1})
			sc.Expect(err).To(specs.BeNil())
			sc.Expect(value.GetValue()).ToEqual(int64(99))
		})
		s.It("rejects a tenant on an adapter without scoped support", func(sc *specs.Context) {
			scope, err := persistence.NewTenantScope("acme")
			sc.Expect(err).To(specs.BeNil())
			_, err = offsetstore.ForScope(legacyOffsets{testkit.NewOffsetStore()}, scope)
			sc.Expect(err).To(specs.MatchError(offsetstore.ErrScopeUnsupported))
		})
		s.It("validates zero scopes before touching storage", func(sc *specs.Context) {
			store := testkit.NewOffsetStore()
			sc.Expect(store.WriteScopedOffset(context.Background(), persistence.Scope{}, &egopb.Offset{})).To(specs.MatchError(persistence.ErrInvalidScope))
			sc.Expect(store.ResetScopedOffset(context.Background(), persistence.Scope{}, "same", 0)).To(specs.MatchError(persistence.ErrInvalidScope))
		})
	})
}
