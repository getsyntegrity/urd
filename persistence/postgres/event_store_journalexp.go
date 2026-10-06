//go:build journalexp

package postgres

// EXPERIMENT for #332, behind the journalexp build tag: it is not compiled in a normal build and it is not on the
// production path. It exists to measure, and to check for omissions, the per-(scope, shard) serialization scheme
// on the real write path of this adapter. It does not decide the scheme, the SPI or the migration.
//
// ExperimentalShardSerializedStore implements the CURRENT persistence.EventsStore, with its public signatures
// unchanged, but gives the int64 "offset" of GetShardEvents/ShardOffsets a different meaning: a journal position,
// not a timestamp. Existing consumers of the real conformance suite can therefore run against it unmodified, and
// the checks that silently assume a timestamp offset show up as failures.
//
// Scheme. Every event row gets journal_pos, taken from a persistent counter row per (tenant_id, shard_number)
// (journal_shard_positions). A writer takes the counter rows of the shards of its batch with one
// INSERT .. ON CONFLICT DO UPDATE .. RETURNING each, which is lock and position assignment at once; the row lock
// is held to commit or rollback, so for one (scope, shard) position order is commit order, and a rollback or a
// cancellation hands its position to the next writer. The counter lives in the database: no process-local state.
//
// Global lock acquisition order (the one that makes the scheme deadlock-free together with the existing
// protocol):
//
//  1. events_store_revisions rows, ordered by persistence_id inside a scope (existing: lockRevision for a
//     conditional write, the sorted upserts of an unconditional one, lockRevisionIfExists for DeleteEvents);
//  2. journal_shard_positions rows, ordered by shard_number inside a scope;
//  3. the event inserts, then commit.
//
// No statement of any writer takes a revision row after a counter row, and each class is taken in a total order,
// so no cycle can form. A conditional write whose precondition fails returns BEFORE step 2 and never touches a
// counter. DeleteEvents takes only step 1 and needs no position: it creates no row.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
)

// ErrExperimentalRollback is returned when BeforeCommit asked to roll the write back.
var ErrExperimentalRollback = errors.New("postgres: experimental write rolled back on request")

// ExperimentalShardSerializedStore is the experimental variant. See the file comment.
type ExperimentalShardSerializedStore struct {
	*EventStore

	// BeforeCommit, when not nil, runs after every lock is held, the positions are taken and the rows are
	// inserted, immediately before the commit. It blocks the writer until the caller lets it go; false rolls
	// the write back (ErrExperimentalRollback). Tests and measurements only.
	BeforeCommit func() bool
	// OnCounterWait, when not nil, receives how long taking the counter rows took. Measurements only.
	OnCounterWait func(time.Duration)
}

var _ persistence.EventsStore = (*ExperimentalShardSerializedStore)(nil)

// NewExperimentalShardSerializedStore creates the experimental store over the same DSN as a normal one.
func NewExperimentalShardSerializedStore(dsn string) *ExperimentalShardSerializedStore {
	return &ExperimentalShardSerializedStore{EventStore: NewEventStore(dsn)}
}

// ExperimentalMigrate adds the TEST schema of the experiment on top of the normal one: the journal_pos column,
// its read index and the counter table. It is not part of the schema migrator and not a migration proposal.
func (s *ExperimentalShardSerializedStore) ExperimentalMigrate(ctx context.Context) error {
	for _, ddl := range []string{
		`ALTER TABLE events_store ADD COLUMN IF NOT EXISTS journal_pos BIGINT`,
		`CREATE INDEX IF NOT EXISTS idx_events_store_journal ON events_store (tenant_id, shard_number, journal_pos)`,
		`CREATE TABLE IF NOT EXISTS journal_shard_positions (
			tenant_id    VARCHAR(255) NOT NULL,
			shard_number BIGINT       NOT NULL,
			last         BIGINT       NOT NULL,
			PRIMARY KEY (tenant_id, shard_number))`,
	} {
		if _, err := s.pool.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("experimental schema: %w", err)
		}
	}
	return nil
}

// WriteEvents implements persistence.EventsStore with positions. Validation is identical to EventStore.WriteEvents.
func (s *ExperimentalShardSerializedStore) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return err
	}
	if !precondition.Valid() {
		return persistence.ErrInvalidPrecondition
	}
	if precondition.IsUnconditional() {
		return s.writeUnconditionalPositioned(ctx, tenantID, events)
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
	return s.writeConditionalPositioned(ctx, scope, tenantID, persistenceID, events, precondition)
}

const (
	insertPositionedSQL = `
		INSERT INTO events_store
			(tenant_id, persistence_id, sequence_number, is_deleted, event_payload, event_manifest,
			 timestamp, shard_number, encryption_key_id, is_encrypted, tenant_metadata, journal_pos)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`
	insertPositionedIgnoreDuplicateSQL = insertPositionedSQL + `
		ON CONFLICT (tenant_id, persistence_id, sequence_number) DO NOTHING`
)

func insertPositioned(ctx context.Context, tx pgx.Tx, query, tenantID string, event *egopb.Event, pos int64) error {
	payload, err := proto.Marshal(event.GetEvent())
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	var tenantMetadata []byte
	if metadata := event.GetTenantMetadata(); len(metadata) > 0 {
		if tenantMetadata, err = json.Marshal(metadata); err != nil {
			return fmt.Errorf("marshal tenant metadata: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, query,
		tenantID, event.GetPersistenceId(), event.GetSequenceNumber(), event.GetIsDeleted(), payload,
		event.GetEvent().GetTypeUrl(), event.GetTimestamp(), event.GetShard(), event.GetEncryptionKeyId(),
		event.GetIsEncrypted(), tenantMetadata, pos,
	); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

// takePositions is step 2 of the lock order: the counter row of every shard of the batch, ascending, each one
// lock and position at once.
func (s *ExperimentalShardSerializedStore) takePositions(ctx context.Context, tx pgx.Tx, tenantID string, events []*egopb.Event) (map[uint64]int64, error) {
	shards := make(map[uint64]struct{}, len(events))
	for _, event := range events {
		shards[event.GetShard()] = struct{}{}
	}
	start := time.Now()
	positions := make(map[uint64]int64, len(shards))
	for _, shard := range slices.Sorted(maps.Keys(shards)) {
		var pos int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO journal_shard_positions (tenant_id, shard_number, last) VALUES ($1, $2, 1)
			ON CONFLICT (tenant_id, shard_number) DO UPDATE SET last = journal_shard_positions.last + 1
			RETURNING last`, tenantID, shard).Scan(&pos); err != nil {
			return nil, fmt.Errorf("take journal position: %w", err)
		}
		positions[shard] = pos
	}
	if s.OnCounterWait != nil {
		s.OnCounterWait(time.Since(start))
	}
	return positions, nil
}

func (s *ExperimentalShardSerializedStore) commitOrRollback(ctx context.Context, tx pgx.Tx) error {
	if s.BeforeCommit != nil && !s.BeforeCommit() {
		return ErrExperimentalRollback
	}
	return tx.Commit(ctx)
}

// writeUnconditionalPositioned mirrors writeUnconditional (revision upserts, sorted by id, are step 1) and adds
// step 2 before the inserts.
func (s *ExperimentalShardSerializedStore) writeUnconditionalPositioned(ctx context.Context, tenantID string, events []*egopb.Event) error {
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

	positions, err := s.takePositions(ctx, tx, tenantID, events)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := insertPositioned(ctx, tx, insertPositionedIgnoreDuplicateSQL, tenantID, event, positions[event.GetShard()]); err != nil {
			return err
		}
	}
	return s.commitOrRollback(ctx, tx)
}

// writeConditionalPositioned mirrors writeConditional: the revision lock and the precondition come first (step 1,
// failing fast without touching a counter), then step 2, then the inserts.
func (s *ExperimentalShardSerializedStore) writeConditionalPositioned(ctx context.Context, scope persistence.Scope, tenantID, persistenceID string, events []*egopb.Event, precondition persistence.WritePrecondition) error {
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

	positions, err := s.takePositions(ctx, tx, tenantID, events)
	if err != nil {
		return err
	}
	highest := revision
	for _, event := range events {
		if err := insertPositioned(ctx, tx, insertPositionedSQL, tenantID, event, positions[event.GetShard()]); err != nil {
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
	return s.commitOrRollback(ctx, tx)
}

// GetShardEvents implements persistence.EventsStore BY POSITION: offset is a journal position, strictly after it
// are returned, in (position, persistence_id, sequence_number) order, the batch is extended to the end of the
// group sharing the position of the limit-th row, and the second result is the position of the last event (0
// when there is none). Rows without a position (written by a non-experimental store) are not returned.
func (s *ExperimentalShardSerializedStore) GetShardEvents(ctx context.Context, scope persistence.Scope, shardNumber uint64, offset int64, limit uint64) ([]*egopb.Event, int64, error) {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return nil, 0, err
	}
	if limit == 0 {
		return nil, 0, nil
	}
	if limit > math.MaxInt64 {
		limit = math.MaxInt64
	}
	rows, err := s.pool.Query(ctx, `
		SELECT persistence_id, sequence_number, is_deleted, event_payload, event_manifest,
		       timestamp, shard_number, encryption_key_id, is_encrypted, tenant_metadata, journal_pos
		FROM events_store
		WHERE tenant_id=$1 AND shard_number=$2 AND journal_pos > $3
		  AND journal_pos <= COALESCE((
		      SELECT journal_pos FROM events_store
		      WHERE tenant_id=$1 AND shard_number=$2 AND journal_pos > $3
		      ORDER BY journal_pos ASC, persistence_id ASC, sequence_number ASC
		      OFFSET $4::bigint - 1 LIMIT 1), 9223372036854775807)
		ORDER BY journal_pos ASC, persistence_id ASC, sequence_number ASC`,
		tenantID, shardNumber, offset, int64(limit))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	events, positions, err := scanPositionedEvents(rows)
	if err != nil {
		return nil, 0, err
	}
	if len(events) == 0 {
		return nil, 0, nil
	}
	return events, positions[len(positions)-1], nil
}

// ShardOffsets implements persistence.EventsStore with the highest position of each shard of scope.
func (s *ExperimentalShardSerializedStore) ShardOffsets(ctx context.Context, scope persistence.Scope) (map[uint64]int64, error) {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT shard_number, MAX(journal_pos) FROM events_store WHERE tenant_id=$1 AND journal_pos IS NOT NULL GROUP BY shard_number`, tenantID)
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

// scanPositionedEvents is scanEvents with the journal position as an eleventh column.
func scanPositionedEvents(rows pgx.Rows) ([]*egopb.Event, []int64, error) {
	var (
		events    []*egopb.Event
		positions []int64
	)
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
			pos             int64
		)
		if err := rows.Scan(&persistenceID, &sequenceNumber, &isDeleted, &payload, &manifest,
			&timestamp, &shardNumber, &encryptionKeyID, &isEncrypted, &tenantMetadata, &pos); err != nil {
			return nil, nil, err
		}
		eventAny := &anypb.Any{TypeUrl: manifest}
		if err := proto.Unmarshal(payload, eventAny); err != nil {
			return nil, nil, fmt.Errorf("unmarshal event payload: %w", err)
		}
		var metadata map[string]string
		if len(tenantMetadata) > 0 {
			if err := json.Unmarshal(tenantMetadata, &metadata); err != nil {
				return nil, nil, fmt.Errorf("unmarshal tenant metadata: %w", err)
			}
		}
		events = append(events, &egopb.Event{
			PersistenceId: persistenceID, SequenceNumber: sequenceNumber, IsDeleted: isDeleted, Event: eventAny,
			Timestamp: timestamp, Shard: shardNumber, EncryptionKeyId: encryptionKeyID, IsEncrypted: isEncrypted,
			TenantMetadata: metadata,
		})
		positions = append(positions, pos)
	}
	return events, positions, rows.Err()
}
