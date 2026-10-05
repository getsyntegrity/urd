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

package tenantfx

import (
	"context"
	"encoding/json"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/jackc/pgx/v5"

	"github.com/getsyntegrity/urd/tenancy"
)

// Row is one record of the events_store table exactly as stored: the tenant column the store keyed it by, the
// business persistence ID, the sequence number and the tenant identity the engine attached to the event.
type Row struct {
	TenantColumn  string
	PersistenceID string
	Sequence      uint64
	Metadata      tenancy.Metadata
}

// AttachedTenant is the tenant ID inside the row's tenant metadata. ok is false when the row carries no tenant
// identity (a legacy or unscoped row) or an administrative one.
func (r Row) AttachedTenant() (id string, ok bool) {
	tc, err := tenancy.UnmarshalMetadata(r.Metadata)
	if err != nil {
		return "", false
	}
	tenant, ok := tc.Tenant()
	return string(tenant), ok
}

// Rows reads every row of persistenceID, whatever its tenant column, ordered by tenant column and sequence. It
// reads the table directly, outside the store, so it sees a row the store would hide behind a scope.
func Rows(sc *specs.Context, dsn, persistenceID string) []Row {
	sc.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	sc.Expect(err).To(specs.BeNil())
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, `SELECT tenant_id, persistence_id, sequence_number, tenant_metadata FROM events_store
		WHERE persistence_id=$1 ORDER BY tenant_id, sequence_number`, persistenceID)
	sc.Expect(err).To(specs.BeNil())
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var (
			row Row
			seq int64
			raw []byte
		)
		sc.Expect(rows.Scan(&row.TenantColumn, &row.PersistenceID, &seq, &raw)).To(specs.BeNil())
		row.Sequence = uint64(seq)
		if len(raw) > 0 {
			sc.Expect(json.Unmarshal(raw, &row.Metadata)).To(specs.BeNil())
		}
		out = append(out, row)
	}
	sc.Expect(rows.Err()).To(specs.BeNil())
	return out
}

// RowsOf is Rows narrowed to one tenant column; the empty string is the unscoped column.
func RowsOf(sc *specs.Context, dsn, tenantColumn, persistenceID string) []Row {
	sc.Helper()
	var out []Row
	for _, row := range Rows(sc, dsn, persistenceID) {
		if row.TenantColumn == tenantColumn {
			out = append(out, row)
		}
	}
	return out
}

// CountAll is the number of rows of the whole events_store table.
func CountAll(sc *specs.Context, dsn string) int {
	sc.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	sc.Expect(err).To(specs.BeNil())
	defer func() { _ = conn.Close(ctx) }()
	var count int
	sc.Expect(conn.QueryRow(ctx, `SELECT COUNT(*) FROM events_store`).Scan(&count)).To(specs.BeNil())
	return count
}
