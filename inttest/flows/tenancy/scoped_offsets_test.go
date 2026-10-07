package tenancy_test

import (
	"context"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/persistence/postgres"
	"github.com/jackc/pgx/v5"
)

func TestScopedOffsetsPreserveLegacyAndIsolateResetOnPostgres(t *testing.T) {
	t.Parallel()
	specs.Describe(t, "scoped offsets on PostgreSQL", func(s *specs.Spec) {
		s.It("preserves legacy cursors and isolates tenant resets", func(sc *specs.Context) {
			ctx := context.Background()
			dsn := shared.NewDatabase(t)
			conn, err := pgx.Connect(ctx, dsn)
			sc.Expect(err).To(specs.BeNil())
			sc.Cleanup(func() { _ = conn.Close(ctx) })
			_, err = conn.Exec(ctx, `CREATE TABLE offsets_store (projection_name VARCHAR(255) NOT NULL, shard_number BIGINT NOT NULL, current_offset BIGINT NOT NULL, timestamp BIGINT NOT NULL, PRIMARY KEY (projection_name, shard_number)); INSERT INTO offsets_store VALUES ('same', 1, 17, 0)`)
			sc.Expect(err).To(specs.BeNil())
			store := postgres.NewOffsetStore(dsn)
			if err := store.Connect(ctx); err != nil {
				sc.Expect(err).To(specs.BeNil())
			}
			sc.Cleanup(func() { _ = store.Disconnect(ctx) })
			if err := store.Migrate(ctx); err != nil {
				sc.Expect(err).To(specs.BeNil())
			}
			a, _ := persistence.NewTenantScope("acme")
			b, _ := persistence.NewTenantScope("unscoped")
			id := &egopb.ProjectionId{ProjectionName: "same", ShardNumber: 1}
			check := func(scope persistence.Scope, want int64) {
				sc.Helper()
				got, err := store.GetScopedOffset(ctx, scope, id)
				if err != nil {
					sc.Expect(err).To(specs.BeNil())
				}
				if got.GetValue() != want {
					sc.Expect(got.GetValue()).To(specs.Equal(want))
				}
			}
			check(persistence.Unscoped(), 17)
			check(a, 0)
			for i, scope := range []persistence.Scope{a, b} {
				for _, shard := range []uint64{1, 2} {
					if err := store.WriteScopedOffset(ctx, scope, &egopb.Offset{ProjectionName: "same", ShardNumber: shard, Value: int64(30 + i)}); err != nil {
						sc.Expect(err).To(specs.BeNil())
					}
				}
			}
			if err := store.ResetScopedOffset(ctx, a, "same", 99); err != nil {
				sc.Expect(err).To(specs.BeNil())
			}
			check(a, 99)
			check(b, 31)
			check(persistence.Unscoped(), 17)
			if err := store.ResetOffset(ctx, "same", 77); err != nil {
				sc.Expect(err).To(specs.BeNil())
			}
			check(persistence.Unscoped(), 77)
			check(a, 99)
			check(b, 31)
			id.ShardNumber = 2
			check(a, 99)
			check(b, 31)
		})
	})
}
