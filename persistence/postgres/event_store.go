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

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
)

// scopeKey validates scope and returns the tenant_id column value it maps
// to: "" for persistence.Unscoped(), or the tenant's id for a tenant scope.
// It returns persistence.ErrInvalidScope for the invalid zero-value Scope,
// before any caller touches the database. Every record-addressing method
// keys structurally on scope.IsUnscoped()/scope.TenantID(), NEVER on
// scope.String() — see persistence.Scope's doc comment on why String() must
// never be used to build a storage key.
func scopeKey(scope persistence.Scope) (string, error) {
	if !scope.Valid() {
		return "", persistence.ErrInvalidScope
	}
	if scope.IsUnscoped() {
		return "", nil
	}
	return string(scope.TenantID()), nil
}

// EventStore implements persistence.EventsStore using PostgreSQL.
type EventStore struct {
	pool *pgxpool.Pool
	dsn  string
}

var (
	_ persistence.EventsStore    = (*EventStore)(nil)
	_ persistence.SchemaMigrator = (*EventStore)(nil)
)

// NewEventStore creates a new PostgreSQL-backed event store.
func NewEventStore(dsn string) *EventStore {
	return &EventStore{dsn: dsn}
}

func (s *EventStore) Connect(ctx context.Context) error {
	cfg, err := pgxpool.ParseConfig(s.dsn)
	if err != nil {
		return fmt.Errorf("event store config: %w", err)
	}
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("event store connect: %w", err)
	}
	s.pool = pool
	return nil
}

// Migrate brings the database schema, every table of this module, to the
// latest version. It implements persistence.SchemaMigrator and needs a
// connected store; see SchemaMigrator for what it does.
func (s *EventStore) Migrate(ctx context.Context) error {
	return NewSchemaMigrator(s.pool).Migrate(ctx)
}

// SchemaVersion returns the schema version the database is at. It implements
// persistence.SchemaMigrator.
func (s *EventStore) SchemaVersion(ctx context.Context) (uint, error) {
	return NewSchemaMigrator(s.pool).SchemaVersion(ctx)
}

func (s *EventStore) Disconnect(_ context.Context) error {
	if s.pool != nil {
		s.pool.Close()
	}
	return nil
}

func (s *EventStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// WriteEvents implements persistence.EventsStore. See that interface's doc
// comment for the full contract. scope is validated first, then precondition
// (persistence.ErrInvalidScope / persistence.ErrInvalidPrecondition), before
// anything is read or written. For a conditional write (anything but
// Unconditional()), every event in the batch MUST share one PersistenceId,
// including that the batch must be non-empty, or persistence.ErrPreconditionScope
// is returned — mirroring testkit's in-memory EventStore.
func (s *EventStore) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return err
	}

	if !precondition.Valid() {
		return persistence.ErrInvalidPrecondition
	}

	if precondition.IsUnconditional() {
		return s.writeUnconditional(ctx, tenantID, events)
	}

	if len(events) == 0 {
		return persistence.ErrPreconditionScope
	}
	persistenceID := events[0].GetPersistenceId()
	for _, event := range events[1:] {
		if event.GetPersistenceId() != persistenceID {
			return persistence.ErrPreconditionScope
		}
	}
	return s.writeConditional(ctx, scope, tenantID, persistenceID, events, precondition)
}

// insertEventSQL inserts one event row. A conditional write uses it as is, so
// a primary-key clash fails the transaction; an unconditional write appends
// insertEventIgnoreDuplicateSQL to keep its legacy "duplicates are ignored"
// behavior.
const (
	insertEventSQL = `
		INSERT INTO events_store
			(tenant_id, persistence_id, sequence_number, is_deleted, event_payload, event_manifest,
			 timestamp, shard_number, encryption_key_id, is_encrypted, tenant_metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`
	insertEventIgnoreDuplicateSQL = insertEventSQL + `
		ON CONFLICT (tenant_id, persistence_id, sequence_number) DO NOTHING`
)

// insertEvent writes event under tenantID inside tx using query, which is
// insertEventSQL or insertEventIgnoreDuplicateSQL.
//
// event.GetTenantMetadata() is marshaled to JSON for tenant_metadata. A nil
// or empty map marshals to a nil []byte, which pgx sends as SQL NULL rather
// than the JSON object "{}" — proto3 cannot tell a nil map from an empty one
// apart, so both write NULL and both read back as a nil map from scanEvents,
// matching egopb.Event.GetTenantMetadata()'s own zero value (#115 Codex P2;
// see eventsStoreSchemaDDL's doc comment above for why the column is
// nullable).
func insertEvent(ctx context.Context, tx pgx.Tx, query, tenantID string, event *egopb.Event) error {
	payload, err := proto.Marshal(event.GetEvent())
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	var tenantMetadata []byte
	if metadata := event.GetTenantMetadata(); len(metadata) > 0 {
		tenantMetadata, err = json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("marshal tenant metadata: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, query,
		tenantID,
		event.GetPersistenceId(),
		event.GetSequenceNumber(),
		event.GetIsDeleted(),
		payload,
		event.GetEvent().GetTypeUrl(),
		event.GetTimestamp(),
		event.GetShard(),
		event.GetEncryptionKeyId(),
		event.GetIsEncrypted(),
		tenantMetadata,
	); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

// writeUnconditional preserves legacy, precondition-free write semantics:
// every event commits, keyed by (tenantID, its own PersistenceId,
// SequenceNumber), and a primary-key clash is silently ignored.
//
// It still takes part in the per-record lock protocol shared with
// writeConditional. Before inserting any event, it upserts the
// events_store_revisions row of every persistence id in the batch, raising
// the revision to the batch's highest sequence number for that id (never
// lowering it). The upsert row-locks each revision row until commit, so an
// unconditional write can never commit between a conditional writer's
// revision check and its insert. The ids are locked in sorted order, so two
// batches that share ids always acquire their locks in the same order and
// cannot deadlock each other.
func (s *EventStore) writeUnconditional(ctx context.Context, tenantID string, events []*egopb.Event) error {
	if len(events) == 0 {
		return nil
	}

	highest := make(map[string]uint64)
	for _, event := range events {
		id := event.GetPersistenceId()
		if seq, seen := highest[id]; !seen || event.GetSequenceNumber() > seq {
			highest[id] = event.GetSequenceNumber()
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit has succeeded

	for _, persistenceID := range slices.Sorted(maps.Keys(highest)) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO events_store_revisions (tenant_id, persistence_id, revision)
			VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, persistence_id)
			DO UPDATE SET revision = GREATEST(events_store_revisions.revision, EXCLUDED.revision)`,
			tenantID, persistenceID, highest[persistenceID],
		); err != nil {
			return fmt.Errorf("advance revision: %w", err)
		}
	}

	for _, event := range events {
		if err := insertEvent(ctx, tx, insertEventIgnoreDuplicateSQL, tenantID, event); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// writeConditional evaluates precondition for (scope, persistenceID) and
// commits events as one atomic operation. It first locks the record's
// events_store_revisions row (lockRevision), which every writer of that
// record takes before touching events_store, so the revision check, the
// inserts and the revision update happen with no other writer's commit in
// between. DeleteEvents locks the same row (lockRevisionIfExists) before its
// DELETE, so it also serializes against writeConditional and
// writeUnconditional for an established record; see DeleteEvents's doc
// comment for why it cannot reuse lockRevision as is. The insert does NOT use
// ON CONFLICT DO NOTHING: a primary-key
// clash under the held lock means two callers disagree about the target
// sequence numbers, and that must fail the transaction rather than silently
// drop the write.
//
// The revision compared against is the stored StorageRevision, not
// MAX(sequence_number): DeleteEvents removes events for retention without
// touching the revision, so deleted events never reopen ExpectGenesis() or
// let a stale ExpectRevision win.
func (s *EventStore) writeConditional(ctx context.Context, scope persistence.Scope, tenantID, persistenceID string, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit has succeeded

	revision, exists, err := lockRevision(ctx, tx, tenantID, persistenceID)
	if err != nil {
		return err
	}

	if precondition.IsGenesis() {
		if exists {
			return persistence.NewConflictError(scope, persistenceID, precondition, persistence.WithActualRevision(revision))
		}
	} else {
		expectedRevision, _ := precondition.Revision()
		if !exists {
			return persistence.NewConflictError(scope, persistenceID, precondition)
		}
		if revision != expectedRevision {
			return persistence.NewConflictError(scope, persistenceID, precondition, persistence.WithActualRevision(revision))
		}
	}

	highest := revision
	for _, event := range events {
		if err := insertEvent(ctx, tx, insertEventSQL, tenantID, event); err != nil {
			return err
		}
		highest = max(highest, event.GetSequenceNumber())
	}

	if _, err := tx.Exec(ctx,
		`UPDATE events_store_revisions SET revision = $3 WHERE tenant_id = $1 AND persistence_id = $2`,
		tenantID, persistenceID, highest,
	); err != nil {
		return fmt.Errorf("advance revision: %w", err)
	}
	return tx.Commit(ctx)
}

// lockRevision row-locks (tenantID, persistenceID)'s events_store_revisions
// row for the rest of tx and returns its revision. exists is false when the
// record has never been committed; in that case lockRevision inserts a
// placeholder row, which holds the same lock (a concurrent writer's insert or
// upsert of that key waits on it) and disappears if tx rolls back. When the
// placeholder insert finds a row that another transaction committed in the
// meantime, the row is locked and read again.
//
// Only a writer (writeConditional) calls lockRevision: claiming the record
// with a revision-0 placeholder is correct there because the caller is about
// to commit at least one event for it. DeleteEvents must NOT do that — see
// lockRevisionIfExists, which it uses instead.
func lockRevision(ctx context.Context, tx pgx.Tx, tenantID, persistenceID string) (revision uint64, exists bool, err error) {
	for {
		err := tx.QueryRow(ctx,
			`SELECT revision FROM events_store_revisions WHERE tenant_id = $1 AND persistence_id = $2 FOR UPDATE`,
			tenantID, persistenceID,
		).Scan(&revision)
		switch {
		case err == nil:
			return revision, true, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return 0, false, fmt.Errorf("lock revision: %w", err)
		}

		tag, err := tx.Exec(ctx, `
			INSERT INTO events_store_revisions (tenant_id, persistence_id, revision)
			VALUES ($1, $2, 0)
			ON CONFLICT (tenant_id, persistence_id) DO NOTHING`,
			tenantID, persistenceID,
		)
		if err != nil {
			return 0, false, fmt.Errorf("claim revision: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return 0, false, nil
		}
	}
}

// lockRevisionIfExists row-locks (tenantID, persistenceID)'s
// events_store_revisions row for the rest of tx, if one exists, and reports
// whether it did. Unlike lockRevision, it never creates a placeholder row
// when none exists: DeleteEvents is its only caller, and inserting a
// revision-0 row for a persistence id that has never been written would make
// that id look established, so a later ExpectGenesis() would wrongly
// conflict against a record that in fact does not exist.
func lockRevisionIfExists(ctx context.Context, tx pgx.Tx, tenantID, persistenceID string) (exists bool, err error) {
	var revision uint64
	err = tx.QueryRow(ctx,
		`SELECT revision FROM events_store_revisions WHERE tenant_id = $1 AND persistence_id = $2 FOR UPDATE`,
		tenantID, persistenceID,
	).Scan(&revision)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("lock revision: %w", err)
	}
}

// DeleteEvents implements persistence.EventsStore. scope is validated before
// anything is touched, and this never affects a record in another scope.
// Only replayable events are removed: the record's events_store_revisions
// row, and the StorageRevision it holds, are never modified, so retention
// can never reopen ExpectGenesis() or let a stale ExpectRevision win.
//
// DeleteEvents takes part in the same per-record lock protocol as
// WriteEvents (see events_store_revisions's and writeConditional's doc
// comments): inside a transaction, it row-locks the record's
// events_store_revisions row with lockRevisionIfExists BEFORE running the
// DELETE, so a concurrent conditional or unconditional write of the same
// record can never commit between the lock and the delete, and vice versa.
// It deliberately uses lockRevisionIfExists rather than lockRevision — see
// that function's doc comment for why. When no revision row exists yet, the
// DELETE still runs without creating one: that case only ever matches
// legacy rows written before events_store_revisions existed, and there is
// no revision to protect for a persistence id that was never written.
func (s *EventStore) DeleteEvents(ctx context.Context, scope persistence.Scope, persistenceID string, toSequenceNumber uint64) error {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit has succeeded

	if _, err := lockRevisionIfExists(ctx, tx, tenantID, persistenceID); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM events_store WHERE tenant_id=$1 AND persistence_id=$2 AND sequence_number<=$3`,
		tenantID, persistenceID, toSequenceNumber,
	); err != nil {
		return fmt.Errorf("delete events: %w", err)
	}
	return tx.Commit(ctx)
}

// ReplayEvents implements persistence.EventsStore. scope is validated before
// anything is read, and this never returns a record that belongs to another
// scope.
func (s *EventStore) ReplayEvents(ctx context.Context, scope persistence.Scope, persistenceID string, fromSequenceNumber, toSequenceNumber, limit uint64) ([]*egopb.Event, error) {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT persistence_id, sequence_number, is_deleted, event_payload, event_manifest,
		       timestamp, shard_number, encryption_key_id, is_encrypted, tenant_metadata
		FROM events_store
		WHERE tenant_id=$1 AND persistence_id=$2 AND sequence_number>=$3 AND sequence_number<=$4
		ORDER BY sequence_number ASC
		LIMIT $5`,
		tenantID, persistenceID, fromSequenceNumber, toSequenceNumber, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// GetLatestEvent implements persistence.EventsStore. scope is validated
// before anything is read, and this never returns a record that belongs to
// another scope.
func (s *EventStore) GetLatestEvent(ctx context.Context, scope persistence.Scope, persistenceID string) (*egopb.Event, error) {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return nil, err
	}
	events, err := s.pool.Query(ctx, `
		SELECT persistence_id, sequence_number, is_deleted, event_payload, event_manifest,
		       timestamp, shard_number, encryption_key_id, is_encrypted, tenant_metadata
		FROM events_store
		WHERE tenant_id=$1 AND persistence_id=$2
		ORDER BY sequence_number DESC
		LIMIT 1`, tenantID, persistenceID)
	if err != nil {
		return nil, err
	}
	defer events.Close()

	result, err := scanEvents(events)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result[0], nil
}

// PersistenceIDs implements persistence.EventsStore. scope is validated
// before anything is read. nextPageToken is the last persistence id actually
// RETURNED on this page (a cursor over what the caller has consumed), and
// the next call resumes strictly after it (persistence_id > pageToken):
// those two facts agree, so iterating from "" until an empty token is
// returned yields every persistence id in scope exactly once, matching
// persistence.EventsStore.PersistenceIDs's pagination contract.
//
// A zero pageSize is a degenerate page: it returns no ids and an empty
// nextPageToken without querying, like testkit's in-memory EventStore. An
// empty token there means "iteration complete", which stops a caller that
// passes 0 instead of handing it a token that repeats the same empty page.
func (s *EventStore) PersistenceIDs(ctx context.Context, scope persistence.Scope, pageSize uint64, pageToken string) ([]string, string, error) {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return nil, "", err
	}
	if pageSize == 0 {
		return []string{}, "", nil
	}

	var rows pgx.Rows
	if pageToken == "" {
		rows, err = s.pool.Query(ctx,
			`SELECT DISTINCT persistence_id FROM events_store WHERE tenant_id=$1 ORDER BY persistence_id LIMIT $2`,
			tenantID, pageSize)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT DISTINCT persistence_id FROM events_store WHERE tenant_id=$1 AND persistence_id > $2 ORDER BY persistence_id LIMIT $3`,
			tenantID, pageToken, pageSize)
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, "", err
		}
		ids = append(ids, id)
	}

	var next string
	if len(ids) == int(pageSize) {
		next = ids[len(ids)-1]
	}
	return ids, next, rows.Err()
}

// GetShardEvents implements persistence.EventsStore. It reads only the events
// of scope: an invalid scope returns persistence.ErrInvalidScope before any
// query, and Unscoped() reads the rows whose tenant_id is the empty string.
func (s *EventStore) GetShardEvents(ctx context.Context, scope persistence.Scope, shardNumber uint64, offset int64, limit uint64) ([]*egopb.Event, int64, error) {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT persistence_id, sequence_number, is_deleted, event_payload, event_manifest,
		       timestamp, shard_number, encryption_key_id, is_encrypted, tenant_metadata
		FROM events_store
		WHERE tenant_id=$1 AND shard_number=$2 AND timestamp > $3
		ORDER BY timestamp ASC
		LIMIT $4`,
		tenantID, shardNumber, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	events, err := scanEvents(rows)
	if err != nil {
		return nil, 0, err
	}
	if len(events) == 0 {
		return nil, 0, nil
	}
	nextOffset := events[len(events)-1].GetTimestamp()
	return events, nextOffset, nil
}

// ShardOffsets implements persistence.EventsStore for the shards that hold
// events of scope. An invalid scope returns persistence.ErrInvalidScope before
// any query.
func (s *EventStore) ShardOffsets(ctx context.Context, scope persistence.Scope) (map[uint64]int64, error) {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT shard_number, MAX(timestamp) FROM events_store WHERE tenant_id=$1 GROUP BY shard_number`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	offsets := make(map[uint64]int64)
	for rows.Next() {
		var (
			shard  uint64
			offset int64
		)
		if err := rows.Scan(&shard, &offset); err != nil {
			return nil, err
		}
		offsets[shard] = offset
	}
	return offsets, rows.Err()
}

// scanEvents reads rows into egopb.Event slices. Every caller's SELECT (see
// ReplayEvents, GetLatestEvent, GetShardEvents) must list tenant_metadata
// last, matching the Scan order below.
func scanEvents(rows pgx.Rows) ([]*egopb.Event, error) {
	var events []*egopb.Event
	for rows.Next() {
		var (
			persistenceID   string
			sequenceNumber  uint64
			isDeleted       bool
			payload         []byte
			manifest        string
			timestamp       int64
			shardNumber     uint64
			encryptionKeyID string
			isEncrypted     bool
			tenantMetadata  []byte
		)
		if err := rows.Scan(&persistenceID, &sequenceNumber, &isDeleted, &payload, &manifest,
			&timestamp, &shardNumber, &encryptionKeyID, &isEncrypted, &tenantMetadata); err != nil {
			return nil, err
		}

		eventAny := &anypb.Any{TypeUrl: manifest}
		if err := proto.Unmarshal(payload, eventAny); err != nil {
			return nil, fmt.Errorf("unmarshal event payload: %w", err)
		}

		// A NULL tenant_metadata column (legacy rows, and any event written
		// with no tenant metadata — see insertEvent) leaves metadata nil,
		// matching egopb.Event.GetTenantMetadata()'s zero value exactly.
		var metadata map[string]string
		if len(tenantMetadata) > 0 {
			if err := json.Unmarshal(tenantMetadata, &metadata); err != nil {
				return nil, fmt.Errorf("unmarshal tenant metadata: %w", err)
			}
		}

		events = append(events, &egopb.Event{
			PersistenceId:   persistenceID,
			SequenceNumber:  sequenceNumber,
			IsDeleted:       isDeleted,
			Event:           eventAny,
			Timestamp:       timestamp,
			Shard:           shardNumber,
			EncryptionKeyId: encryptionKeyID,
			IsEncrypted:     isEncrypted,
			TenantMetadata:  metadata,
		})
	}
	return events, rows.Err()
}
