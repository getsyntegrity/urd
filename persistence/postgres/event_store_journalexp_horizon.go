//go:build journalexp

package postgres

// EXPERIMENT for #332, behind the journalexp build tag, like event_store_journalexp.go: the xid8 + xmin horizon
// scheme on the real write path of this adapter, to compare like for like with the shard-serialization store.
// Not on the production path; it decides nothing.
//
// Scheme. Every row gets journal_pos = 2^62 + the top-level xid8 of the writing transaction (same column and
// read index as the serialization experiment; the counter table is not used). A reader returns only rows whose
// position is below 2^62 + pg_snapshot_xmin of the very snapshot of its statement, so no row that may still
// become visible can sit below what it returned.
//
// Order of acquisition. The advisory entity locks come FIRST, for every entity of the batch, ordered by
// (tenant, persistence id), before any statement that can assign a transaction id: the revision row locks of
// this adapter (SELECT .. FOR UPDATE, the upserts) do assign one. Only after them come the existing revision
// locks, the inserts and the commit. For one entity, lock order is then xid order. DeleteEvents is unchanged: it
// takes the revision row only and creates no row. The stable order reduces cycles between these paths; it does
// not by itself prove the absence of every possible deadlock.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
)

// horizonBase is 2^62: a new row's position is horizonBase + xid8, which fits a signed bigint while xid8 < 2^62.
const horizonBase = int64(1) << 62

const (
	insertHorizonSQL = `
		INSERT INTO events_store
			(tenant_id, persistence_id, sequence_number, is_deleted, event_payload, event_manifest,
			 timestamp, shard_number, encryption_key_id, is_encrypted, tenant_metadata, journal_pos)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, $12::bigint + pg_current_xact_id()::text::bigint)`
	insertHorizonIgnoreDuplicateSQL = insertHorizonSQL + `
		ON CONFLICT (tenant_id, persistence_id, sequence_number) DO NOTHING`
)

// ErrXidAssignedBeforeLocks is returned by a write when AssertNoXidBeforeLocks is set and the transaction already
// holds a transaction id after taking its advisory locks: the lock-before-xid rule was broken.
var ErrXidAssignedBeforeLocks = errors.New("postgres: a transaction id was assigned before the entity locks were taken")

// ExperimentalHorizonStore is the xid8 + xmin horizon variant over the current EventsStore.
type ExperimentalHorizonStore struct {
	*EventStore

	// BeforeCommit, as in ExperimentalShardSerializedStore.
	BeforeCommit func() bool
	// OnLockWait receives how long taking the advisory entity locks took. Measurements only.
	OnLockWait func(time.Duration)
	// AssertNoXidBeforeLocks makes every write check, right after its advisory locks, that no transaction id
	// was assigned yet. Tests only.
	AssertNoXidBeforeLocks bool
}

var _ persistence.EventsStore = (*ExperimentalHorizonStore)(nil)

// NewExperimentalHorizonStore creates the store over the same DSN as a normal one.
func NewExperimentalHorizonStore(dsn string) *ExperimentalHorizonStore {
	return &ExperimentalHorizonStore{EventStore: NewEventStore(dsn)}
}

// ExperimentalMigrate adds the TEST schema of the experiment (shared with the serialization experiment).
func (s *ExperimentalHorizonStore) ExperimentalMigrate(ctx context.Context) error {
	return s.EventStore.experimentalMigrate(ctx)
}

// lockEntities takes the advisory lock of every distinct (tenant, persistence id), ascending. The key is
// length-prefixed so two different pairs never share it.
func (s *ExperimentalHorizonStore) lockEntities(ctx context.Context, tx pgx.Tx, tenantID string, ids []string) error {
	start := time.Now()
	for _, id := range slices.Compact(slices.Sorted(slices.Values(ids))) {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("%d:%s%s", len(tenantID), tenantID, id)); err != nil {
			return fmt.Errorf("lock entity: %w", err)
		}
	}
	if s.OnLockWait != nil {
		s.OnLockWait(time.Since(start))
	}
	if s.AssertNoXidBeforeLocks {
		var assigned bool
		if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id_if_assigned() IS NOT NULL`).Scan(&assigned); err != nil {
			return err
		}
		if assigned {
			return ErrXidAssignedBeforeLocks
		}
	}
	return nil
}

func (s *ExperimentalHorizonStore) commitOrRollback(ctx context.Context, tx pgx.Tx) error {
	if s.BeforeCommit != nil && !s.BeforeCommit() {
		return ErrExperimentalRollback
	}
	return tx.Commit(ctx)
}

// WriteEvents implements persistence.EventsStore with horizon positions. Validation is identical to
// EventStore.WriteEvents.
func (s *ExperimentalHorizonStore) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return err
	}
	if !precondition.Valid() {
		return persistence.ErrInvalidPrecondition
	}
	if precondition.IsUnconditional() {
		return s.writeUnconditionalHorizon(ctx, tenantID, events)
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
	return s.writeConditionalHorizon(ctx, scope, tenantID, persistenceID, events, precondition)
}

func (s *ExperimentalHorizonStore) writeUnconditionalHorizon(ctx context.Context, tenantID string, events []*egopb.Event) error {
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

	if err := s.lockEntities(ctx, tx, tenantID, slices.Collect(maps.Keys(highest))); err != nil {
		return err
	}
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
		if err := insertPositioned(ctx, tx, insertHorizonIgnoreDuplicateSQL, tenantID, event, horizonBase); err != nil {
			return err
		}
	}
	return s.commitOrRollback(ctx, tx)
}

func (s *ExperimentalHorizonStore) writeConditionalHorizon(ctx context.Context, scope persistence.Scope, tenantID, persistenceID string, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit has succeeded

	if err := s.lockEntities(ctx, tx, tenantID, []string{persistenceID}); err != nil {
		return err
	}
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
		if err := insertPositioned(ctx, tx, insertHorizonSQL, tenantID, event, horizonBase); err != nil {
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

// GetShardEvents implements persistence.EventsStore BY HORIZON POSITION: rows strictly after offset whose
// position is below 2^62 + the xmin of this statement's own snapshot, in (position, persistence_id,
// sequence_number) order, the batch extended to the end of the group sharing the position of the limit-th row.
// The second result is the position of the last event (0 when there is none).
func (s *ExperimentalHorizonStore) GetShardEvents(ctx context.Context, scope persistence.Scope, shardNumber uint64, offset int64, limit uint64) ([]*egopb.Event, int64, error) {
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
		  AND journal_pos < $5::bigint + pg_snapshot_xmin(pg_current_snapshot())::text::bigint
		  AND journal_pos <= COALESCE((
		      SELECT journal_pos FROM events_store
		      WHERE tenant_id=$1 AND shard_number=$2 AND journal_pos > $3
		        AND journal_pos < $5::bigint + pg_snapshot_xmin(pg_current_snapshot())::text::bigint
		      ORDER BY journal_pos ASC, persistence_id ASC, sequence_number ASC
		      OFFSET $4::bigint - 1 LIMIT 1), 9223372036854775807)
		ORDER BY journal_pos ASC, persistence_id ASC, sequence_number ASC`,
		tenantID, shardNumber, offset, int64(limit), horizonBase)
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

// ShardOffsets implements persistence.EventsStore with the highest position of each shard of scope that is
// already below the horizon.
func (s *ExperimentalHorizonStore) ShardOffsets(ctx context.Context, scope persistence.Scope) (map[uint64]int64, error) {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT shard_number, MAX(journal_pos) FROM events_store
		WHERE tenant_id=$1 AND journal_pos IS NOT NULL
		  AND journal_pos < $2::bigint + pg_snapshot_xmin(pg_current_snapshot())::text::bigint
		GROUP BY shard_number`, tenantID, horizonBase)
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
