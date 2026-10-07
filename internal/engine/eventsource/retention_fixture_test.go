package eventsource

import (
	"context"

	"github.com/getsyntegrity/urd/persistence"
)

// retentionFixture is test-only authority: these isolated writer/janitor rigs
// have no read-side consumers. Production stores must prove safety themselves.
type retentionFixture struct{ persistence.EventsStore }

func (s retentionFixture) DeleteRetainedEvents(ctx context.Context, scope persistence.Scope, id string, through uint64) error {
	return s.DeleteEvents(ctx, scope, id, through)
}
