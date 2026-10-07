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

package reader_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/persistence/conformance/readertck"
	"github.com/getsyntegrity/urd/persistence/postgres"
)

// insertEventSQL writes one row the way postgres.EventStore does (its own
// statement is unexported). The harness has to hold a writer transaction OPEN
// across other writers' commits, which EventStore.WriteEvents cannot do because
// it commits before it returns; that is the whole point of the late-commit case.
const insertEventSQL = `
	INSERT INTO events_store
		(tenant_id, persistence_id, sequence_number, is_deleted, event_payload, event_manifest,
		 timestamp, shard_number, encryption_key_id, is_encrypted, tenant_metadata)
	VALUES ($1,$2,$3,FALSE,$4,$5,$6,$7,'',FALSE,NULL)`

// pgBackend is a readertck.Backend over a real PostgreSQL database. A writer
// transaction is a real pgx transaction on its own connection, so a transaction
// that appended an event earlier can commit after another writer's later event
// is already visible, exactly as concurrent writers behave in production.
// The harness interfaces return no errors, so the first failure is kept in err
// and every later call becomes a no-op; the test asserts err after the run.
type pgBackend struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	store  *postgres.EventStore
	scopes []persistence.Scope
	slices []uint32
	txs    map[string]pgx.Tx
	mk     func(*pgBackend) readertck.Subject
	err    error
}

// pgFactory builds a backend on a fresh, migrated database. The schema comes
// from the store's own Migrate, the path an engine started with
// WithSchemaMigration takes. made receives every backend the factory builds so
// the test can read its error after the run.
func pgFactory(t *testing.T, mk func(*pgBackend) readertck.Subject, made *[]*pgBackend) readertck.Factory {
	return func(sc readertck.Scenario) readertck.Backend {
		ctx := context.Background()
		dsn := shared.NewDatabase(t)
		b := &pgBackend{ctx: ctx, scopes: sc.Scopes(), slices: sc.Slices(), txs: map[string]pgx.Tx{}, mk: mk}
		*made = append(*made, b)

		migrating := postgres.NewEventStore(dsn)
		if err := migrating.Connect(ctx); err != nil {
			b.err = fmt.Errorf("connect to migrate: %w", err)
			return b
		}
		migrateErr := migrating.Migrate(ctx)
		_ = migrating.Disconnect(ctx)
		if migrateErr != nil {
			b.err = fmt.Errorf("migrate: %w", migrateErr)
			return b
		}

		b.store = postgres.NewEventStore(dsn)
		if err := b.store.Connect(ctx); err != nil {
			b.err = fmt.Errorf("connect the store: %w", err)
			return b
		}
		t.Cleanup(func() { _ = b.store.Disconnect(context.Background()) })

		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			b.err = fmt.Errorf("open the writer pool: %w", err)
			return b
		}
		t.Cleanup(pool.Close)
		b.pool = pool
		return b
	}
}

func (b *pgBackend) fail(op string, err error) {
	if b.err == nil {
		b.err = fmt.Errorf("%s: %w", op, err)
	}
}

func (b *pgBackend) Begin(tx string) {
	if b.err != nil {
		return
	}
	t, err := b.pool.Begin(b.ctx)
	if err != nil {
		b.fail("begin "+tx, err)
		return
	}
	b.txs[tx] = t
}

func (b *pgBackend) Append(tx string, e readertck.Event) {
	t, ok := b.txs[tx]
	if b.err != nil || !ok {
		return
	}
	payload, err := anypb.New(&testpb.AccountCreated{AccountId: e.PersistenceID})
	if err != nil {
		b.fail("pack the payload of "+e.Label, err)
		return
	}
	raw, err := proto.Marshal(payload)
	if err != nil {
		b.fail("marshal the payload of "+e.Label, err)
		return
	}
	// '' is the persisted key of Unscoped and the tenant id of a tenant scope.
	if _, err := t.Exec(b.ctx, insertEventSQL, string(e.Scope.TenantID()), e.PersistenceID, e.Seq, raw, payload.GetTypeUrl(), e.Timestamp, int64(e.Slice)); err != nil {
		b.fail("append "+e.Label, err)
	}
}

func (b *pgBackend) Commit(tx string) {
	t, ok := b.txs[tx]
	if b.err != nil || !ok {
		return
	}
	delete(b.txs, tx)
	if err := t.Commit(b.ctx); err != nil {
		b.fail("commit "+tx, err)
	}
}

func (b *pgBackend) Abort(tx string) {
	t, ok := b.txs[tx]
	if b.err != nil || !ok {
		return
	}
	delete(b.txs, tx)
	if err := t.Rollback(b.ctx); err != nil {
		b.fail("abort "+tx, err)
	}
}

// Tick is a no-op: time is the harness's logical tick counter, and PostgreSQL's
// clock is never consulted.
func (b *pgBackend) Tick(int64) {}

func (b *pgBackend) NewReader() readertck.Subject { return b.mk(b) }

// newLegacy is the CURRENT read API over the real adapter.
func newLegacy(b *pgBackend) readertck.Subject {
	return readertck.LegacyReader{Store: b.store, Scopes: b.scopes, Slices: b.slices}
}

// newStoreSet is the correct reference reader over the same adapter.
func newStoreSet(b *pgBackend) readertck.Subject {
	return readertck.StoreSetReader{Store: b.store, Scopes: b.scopes}
}
