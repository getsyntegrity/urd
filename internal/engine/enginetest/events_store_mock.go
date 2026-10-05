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

package enginetest

import (
	"context"

	"github.com/getsyntegrity/go-specs/mock"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
)

// EventsStoreMock is a persistence.EventsStore backed by a go-specs
// mock.Controller. Every method forwards its call to the controller under the
// method's own name, and the case declares the answer with
// c.Method("Name").Expect(...).Return(...). Build the controller with
// mock.NewController(ctx) so its expectations are verified when the case ends,
// and an unexpected call is reported at once.
type EventsStoreMock struct{ c *mock.Controller }

var _ persistence.EventsStore = (*EventsStoreMock)(nil)

// NewEventsStoreMock returns an EventsStoreMock that forwards to c.
func NewEventsStoreMock(c *mock.Controller) *EventsStoreMock { return &EventsStoreMock{c: c} }

// Connect forwards to the controller.
func (m *EventsStoreMock) Connect(ctx context.Context) error {
	return m.c.Method("Connect").Call(ctx).Err(0)
}

// Disconnect forwards to the controller.
func (m *EventsStoreMock) Disconnect(ctx context.Context) error {
	return m.c.Method("Disconnect").Call(ctx).Err(0)
}

// WriteEvents forwards to the controller.
func (m *EventsStoreMock) WriteEvents(ctx context.Context, scope persistence.Scope, events []*egopb.Event, precondition persistence.WritePrecondition) error {
	return m.c.Method("WriteEvents").Call(ctx, scope, events, precondition).Err(0)
}

// Ping forwards to the controller.
func (m *EventsStoreMock) Ping(ctx context.Context) error {
	return m.c.Method("Ping").Call(ctx).Err(0)
}

// DeleteEvents forwards to the controller.
func (m *EventsStoreMock) DeleteEvents(ctx context.Context, scope persistence.Scope, persistenceID string, toSequenceNumber uint64) error {
	return m.c.Method("DeleteEvents").Call(ctx, scope, persistenceID, toSequenceNumber).Err(0)
}

// ReplayEvents forwards to the controller.
func (m *EventsStoreMock) ReplayEvents(ctx context.Context, scope persistence.Scope, persistenceID string, fromSequenceNumber, toSequenceNumber uint64, limit uint64) ([]*egopb.Event, error) {
	r := m.c.Method("ReplayEvents").Call(ctx, scope, persistenceID, fromSequenceNumber, toSequenceNumber, limit)
	return mock.Value[[]*egopb.Event](r, 0), r.Err(1)
}

// GetLatestEvent forwards to the controller.
func (m *EventsStoreMock) GetLatestEvent(ctx context.Context, scope persistence.Scope, persistenceID string) (*egopb.Event, error) {
	r := m.c.Method("GetLatestEvent").Call(ctx, scope, persistenceID)
	return mock.Value[*egopb.Event](r, 0), r.Err(1)
}

// PersistenceIDs forwards to the controller.
func (m *EventsStoreMock) PersistenceIDs(ctx context.Context, scope persistence.Scope, pageSize uint64, pageToken string) ([]string, string, error) {
	r := m.c.Method("PersistenceIDs").Call(ctx, scope, pageSize, pageToken)
	return mock.Value[[]string](r, 0), mock.Value[string](r, 1), r.Err(2)
}

// GetShardEvents forwards to the controller.
func (m *EventsStoreMock) GetShardEvents(ctx context.Context, scope persistence.Scope, shardNumber uint64, offset int64, limit uint64) ([]*egopb.Event, int64, error) {
	r := m.c.Method("GetShardEvents").Call(ctx, scope, shardNumber, offset, limit)
	return mock.Value[[]*egopb.Event](r, 0), mock.Value[int64](r, 1), r.Err(2)
}

// ShardOffsets forwards to the controller.
func (m *EventsStoreMock) ShardOffsets(ctx context.Context, scope persistence.Scope) (map[uint64]int64, error) {
	r := m.c.Method("ShardOffsets").Call(ctx, scope)
	return mock.Value[map[uint64]int64](r, 0), r.Err(1)
}
