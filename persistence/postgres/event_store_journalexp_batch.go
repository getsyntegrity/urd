//go:build journalexp

package postgres

// EXPERIMENT for #332, behind the journalexp build tag, off the production path: post-commit BATCH PUBLICATION.
//
// A writer persists its events exactly as the current adapter does (same revision-row locks) with journal_pos NULL
// (pending) and pub_seq taken from a sequence. A publisher later takes the stream row lock, selects the N pending
// rows with the smallest pub_seq, gives them consecutive positions above the stream's last one and commits. Readers
// return only published rows, in position order. Positions are assigned only inside a publisher transaction holding
// the stream lock, so a row that commits late is simply published in a later batch with a higher position.
//
// Deliberately small: no idempotent replay, no identity or sequence checks, no cursor binding, no retention floor,
// no progress store. It exists to answer three questions: do late events, per-entity order and restart/resume hold,
// and what does it cost against the current adapter.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
)

// KindBatch names this variant for NewExperimentalStore.
const KindBatch = "batch"

const (
	insertPendingSQL = `
		INSERT INTO events_store
			(tenant_id, persistence_id, sequence_number, is_deleted, event_payload, event_manifest,
			 timestamp, shard_number, encryption_key_id, is_encrypted, tenant_metadata, journal_pos, pub_seq)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, $12::bigint, nextval('journal_pub_seq'))`
	insertPendingIgnoreDuplicateSQL = insertPendingSQL + `
		ON CONFLICT (tenant_id, persistence_id, sequence_number) DO NOTHING`
)

// PublicationControl is what the tests and the benchmark need from a variant that publishes after the commit.
type PublicationControl interface {
	// PublishAll runs publisher rounds until nothing is pending and returns how many rows it published.
	PublishAll(ctx context.Context) (int, error)
	// StartPublisher runs a background publisher: a round every tau and right after a local commit. stop ends it.
	StartPublisher(ctx context.Context, tau time.Duration) (stop func())
	// Pending counts the persisted rows that are not published yet.
	Pending(ctx context.Context) (int64, error)
}

// ExperimentalBatchStore is the batch-publication variant over the current EventsStore.
type ExperimentalBatchStore struct {
	*ExperimentalShardSerializedStore // reads, schema and the pool; writes are overridden below

	// BatchSize is N, the rows one publisher transaction publishes. Default 100.
	BatchSize int
	// BeforeCommit parks or rolls back a WRITE right before its commit (tests and the held-transaction scenarios).
	BeforeCommit func() bool
	// PublishFailpoint, when it returns an error, makes the publisher transaction fail before its commit (tests).
	PublishFailpoint func() error

	wake chan struct{}
	mu   sync.Mutex

	// the streams the publisher serves are rediscovered at most every streamRefresh; a round probes each with an
	// index lookup. (Discovering them with a GROUP BY over every pending row, on EVERY round, made a round slower the
	// larger the backlog: the first measurement of this prototype ran with that defect.)
	streams   []streamKey
	streamsAt time.Time

	// DIAGNOSTIC: when set, the publisher uses its own connection pool instead of the adapter's shared one.
	pubPool *pgxpool.Pool
}

// pp is the pool the publisher uses.
func (s *ExperimentalBatchStore) pp() *pgxpool.Pool {
	if s.pubPool != nil {
		return s.pubPool
	}
	return s.pool
}

// DedicatePublisherPool gives the publisher its own pool of size conns over dsn (a diagnostic: it separates a
// publisher that is slow because it competes for the shared pool from one that is slow for another reason).
func (s *ExperimentalBatchStore) DedicatePublisherPool(ctx context.Context, dsn string, size int32) error {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return err
	}
	cfg.MaxConns = size
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	s.pubPool = pool
	return nil
}

const streamRefresh = 100 * time.Millisecond

var (
	_ persistence.EventsStore = (*ExperimentalBatchStore)(nil)
	_ PublicationControl      = (*ExperimentalBatchStore)(nil)
)

// NewExperimentalBatchStore creates the store over the same DSN as a normal one.
func NewExperimentalBatchStore(dsn string) *ExperimentalBatchStore {
	return &ExperimentalBatchStore{ExperimentalShardSerializedStore: NewExperimentalShardSerializedStore(dsn), BatchSize: 100, wake: make(chan struct{}, 1)}
}

func (s *ExperimentalBatchStore) SetBeforeCommit(f func() bool) { s.BeforeCommit = f }

// SetOnLockWait is a no-op: a batch writer takes no lock beyond the ones the current adapter already takes.
func (s *ExperimentalBatchStore) SetOnLockWait(func(time.Duration)) {}

func (s *ExperimentalBatchStore) commit(ctx context.Context, tx pgx.Tx) error {
	if s.BeforeCommit != nil && !s.BeforeCommit() {
		return ErrExperimentalRollback
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	select { // wake the background publisher, if any
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

// WriteEvents persists events as PENDING rows. Validation and lock protocol are the current adapter's.
func (s *ExperimentalBatchStore) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	tenantID, err := scopeKey(scope)
	if err != nil {
		return err
	}
	if !precondition.Valid() {
		return persistence.ErrInvalidPrecondition
	}
	if precondition.IsUnconditional() {
		return s.writeUnconditionalPending(ctx, tenantID, events)
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
	return s.writeConditionalPending(ctx, scope, tenantID, persistenceID, events, precondition)
}

func (s *ExperimentalBatchStore) writeUnconditionalPending(ctx context.Context, tenantID string, events []*egopb.Event) error {
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
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

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
		if err := insertPositioned(ctx, tx, insertPendingIgnoreDuplicateSQL, tenantID, event, nil); err != nil {
			return err
		}
	}
	return s.commit(ctx, tx)
}

func (s *ExperimentalBatchStore) writeConditionalPending(ctx context.Context, scope persistence.Scope, tenantID, persistenceID string, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

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
		if err := insertPositioned(ctx, tx, insertPendingSQL, tenantID, event, nil); err != nil {
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
	return s.commit(ctx, tx)
}

type streamKey struct {
	tenant string
	shard  uint64
}

// servedStreams returns the streams to visit this round: the cached list, refreshed when it is empty or older than
// streamRefresh. A stream that appears is served within streamRefresh.
func (s *ExperimentalBatchStore) servedStreams(ctx context.Context) ([]streamKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.streams) == 0 || time.Since(s.streamsAt) > streamRefresh {
		found, err := s.pendingStreams(ctx)
		if err != nil {
			return nil, err
		}
		s.streams, s.streamsAt = found, time.Now()
	}
	return slices.Clone(s.streams), nil
}

// hasPending is an index probe: does the stream have a pending row?
func (s *ExperimentalBatchStore) hasPending(ctx context.Context, k streamKey) (bool, error) {
	var ok bool
	err := s.pp().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events_store WHERE tenant_id = $1 AND shard_number = $2 AND journal_pos IS NULL AND pub_seq IS NOT NULL)`, k.tenant, k.shard).Scan(&ok)
	return ok, err
}

// pendingStreams lists the streams that have pending rows, oldest pending row first (it scans the pending rows, so
// it is not called on every round).
func (s *ExperimentalBatchStore) pendingStreams(ctx context.Context) ([]streamKey, error) {
	rows, err := s.pp().Query(ctx, `
		SELECT tenant_id, shard_number FROM events_store
		WHERE journal_pos IS NULL AND pub_seq IS NOT NULL
		GROUP BY tenant_id, shard_number ORDER BY MIN(pub_seq)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []streamKey
	for rows.Next() {
		var k streamKey
		if err := rows.Scan(&k.tenant, &k.shard); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// publishStream is ONE publisher transaction for one stream: take the stream row, publish up to BatchSize pending
// rows in pub_seq order with the next positions, advance the stream's last position, commit.
func (s *ExperimentalBatchStore) publishStream(ctx context.Context, k streamKey) (int, error) {
	tx, err := s.pp().Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	if _, err := tx.Exec(ctx, `INSERT INTO journal_shard_positions (tenant_id, shard_number, last) VALUES ($1, $2, 0) ON CONFLICT DO NOTHING`, k.tenant, k.shard); err != nil {
		return 0, err
	}
	var last int64
	if err := tx.QueryRow(ctx, `SELECT last FROM journal_shard_positions WHERE tenant_id = $1 AND shard_number = $2 FOR UPDATE`, k.tenant, k.shard).Scan(&last); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		WITH batch AS (
			SELECT persistence_id, sequence_number, row_number() OVER (ORDER BY pub_seq) AS rn
			FROM (SELECT persistence_id, sequence_number, pub_seq FROM events_store
			      WHERE tenant_id = $1 AND shard_number = $2 AND journal_pos IS NULL AND pub_seq IS NOT NULL
			      ORDER BY pub_seq LIMIT $3) q)
		UPDATE events_store e SET journal_pos = $4 + b.rn
		FROM batch b
		WHERE e.tenant_id = $1 AND e.persistence_id = b.persistence_id AND e.sequence_number = b.sequence_number
		  AND e.journal_pos IS NULL`, k.tenant, k.shard, s.BatchSize, last)
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	if n == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE journal_shard_positions SET last = $3 WHERE tenant_id = $1 AND shard_number = $2`, k.tenant, k.shard, last+int64(n)); err != nil {
		return 0, err
	}
	if s.PublishFailpoint != nil {
		if err := s.PublishFailpoint(); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit(ctx)
}

// publishRound visits every stream that has pending rows once, in oldest-pending order.
func (s *ExperimentalBatchStore) publishRound(ctx context.Context) (int, error) {
	streams, err := s.servedStreams(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, k := range streams {
		if ok, err := s.hasPending(ctx, k); err != nil {
			return total, err
		} else if !ok {
			continue
		}
		n, err := s.publishStream(ctx, k)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// PublishRound runs ONE publisher round: one transaction of at most BatchSize rows for each stream with pending rows.
func (s *ExperimentalBatchStore) PublishRound(ctx context.Context) (int, error) {
	return s.publishRound(ctx)
}

// PublishAll publishes until nothing is pending.
func (s *ExperimentalBatchStore) PublishAll(ctx context.Context) (int, error) {
	total := 0
	for {
		s.mu.Lock()
		s.streams = nil // an explicit drain must see every stream, not the cached list
		s.mu.Unlock()
		n, err := s.publishRound(ctx)
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
}

// StartPublisher runs a background publisher until stop is called or ctx ends.
func (s *ExperimentalBatchStore) StartPublisher(ctx context.Context, tau time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(tau)
		defer ticker.Stop()
		for {
			for {
				n, err := s.publishRound(ctx)
				if err != nil || n == 0 {
					if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
						time.Sleep(tau) // a failed round (a test failpoint, a lost connection) is retried
					}
					break
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-s.wake:
			}
		}
	}()
	return func() { cancel(); <-done }
}

// Pending counts persisted rows that are not published yet.
func (s *ExperimentalBatchStore) Pending(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM events_store WHERE journal_pos IS NULL AND pub_seq IS NOT NULL`).Scan(&n)
	return n, err
}
