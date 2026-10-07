package persistence

import (
	"context"
	"errors"
)

// ErrUnsafeEventRetention rejects automatic journal deletion without a
// retention-aware store. A snapshot alone says nothing about read-side progress.
var ErrUnsafeEventRetention = errors.New("persistence: automatic event deletion requires a retention-aware store")

// RetainedEventsDeleter atomically checks the retention requirements of every
// required consumer, rebuild and recovery reader and deletes only the safe
// prefix through toSequenceNumber. The consumer registry must survive crashes.
// If safety cannot be established it must delete nothing and return an error.
// Checking a horizon then calling DeleteEvents in a separate transaction does
// not satisfy this contract. Ordinary DeleteEvents remains explicit erasure.
type RetainedEventsDeleter interface {
	DeleteRetainedEvents(context.Context, Scope, string, uint64) error
}
