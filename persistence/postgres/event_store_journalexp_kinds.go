//go:build journalexp

package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/getsyntegrity/urd/persistence"
)

// ExperimentalStore is what the experiments need from any variant: the current EventsStore plus the test-only
// schema and hooks. It lets one test and one benchmark drive every variant through the same code.
type ExperimentalStore interface {
	persistence.EventsStore
	Migrate(ctx context.Context) error
	ExperimentalMigrate(ctx context.Context) error
	SetBeforeCommit(f func() bool)
	// SetOnLockWait receives how long taking the variant's write locks (counter rows, or advisory entity locks)
	// took, per write.
	SetOnLockWait(f func(time.Duration))
}

// Names of the experimental variants.
const (
	KindSerialized     = "serialized"      // counter rows after the revision locks, before the inserts
	KindSerializedLate = "serialized-late" // counter rows after the inserts, positions stamped under the lock
	KindHorizon        = "horizon"         // xid8 position, xmin horizon on reads
)

// NewExperimentalStore creates the variant named kind over dsn.
func NewExperimentalStore(kind, dsn string) (ExperimentalStore, error) {
	switch kind {
	case KindSerialized:
		return NewExperimentalShardSerializedStore(dsn), nil
	case KindSerializedLate:
		s := NewExperimentalShardSerializedStore(dsn)
		s.CounterLast = true
		return s, nil
	case KindHorizon:
		return NewExperimentalHorizonStore(dsn), nil
	}
	return nil, fmt.Errorf("unknown experimental variant %q", kind)
}

func (s *ExperimentalShardSerializedStore) SetBeforeCommit(f func() bool) { s.BeforeCommit = f }
func (s *ExperimentalShardSerializedStore) SetOnLockWait(f func(time.Duration)) {
	s.OnCounterWait = f
}
func (s *ExperimentalHorizonStore) SetBeforeCommit(f func() bool)       { s.BeforeCommit = f }
func (s *ExperimentalHorizonStore) SetOnLockWait(f func(time.Duration)) { s.OnLockWait = f }
