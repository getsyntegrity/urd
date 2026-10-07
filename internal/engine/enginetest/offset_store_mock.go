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
	"github.com/getsyntegrity/urd/offsetstore"
	"github.com/getsyntegrity/urd/persistence"
)

// OffsetStoreMock is an offsetstore.OffsetStore backed by a go-specs
// mock.Controller. Every method forwards its call to the controller under the
// method's own name, and the case declares the answer with
// c.Method("Name").Expect(...).Return(...). Build the controller with
// mock.NewController(ctx) so its expectations are verified when the case ends.
type OffsetStoreMock struct{ c *mock.Controller }

var _ offsetstore.OffsetStore = (*OffsetStoreMock)(nil)

// NewOffsetStoreMock returns an OffsetStoreMock that forwards to c.
func NewOffsetStoreMock(c *mock.Controller) *OffsetStoreMock { return &OffsetStoreMock{c: c} }

// Connect forwards to the controller.
func (m *OffsetStoreMock) Connect(ctx context.Context) error {
	return m.c.Method("Connect").Call(ctx).Err(0)
}

// Disconnect forwards to the controller.
func (m *OffsetStoreMock) Disconnect(ctx context.Context) error {
	return m.c.Method("Disconnect").Call(ctx).Err(0)
}

// Ping forwards to the controller.
func (m *OffsetStoreMock) Ping(ctx context.Context) error {
	return m.c.Method("Ping").Call(ctx).Err(0)
}

// WriteOffset forwards to the controller.
func (m *OffsetStoreMock) WriteOffset(ctx context.Context, offset *egopb.Offset) error {
	return m.c.Method("WriteOffset").Call(ctx, offset).Err(0)
}

// GetCurrentOffset forwards to the controller.
func (m *OffsetStoreMock) GetCurrentOffset(ctx context.Context, projectionID *egopb.ProjectionId) (*egopb.Offset, error) {
	r := m.c.Method("GetCurrentOffset").Call(ctx, projectionID)
	return mock.Value[*egopb.Offset](r, 0), r.Err(1)
}

// ResetOffset forwards to the controller.
func (m *OffsetStoreMock) ResetOffset(ctx context.Context, projectionName string, value int64) error {
	return m.c.Method("ResetOffset").Call(ctx, projectionName, value).Err(0)
}

// WriteScopedOffset forwards the explicit scope for tenant-aware runners.
func (m *OffsetStoreMock) WriteScopedOffset(ctx context.Context, scope persistence.Scope, offset *egopb.Offset) error {
	return m.c.Method("WriteScopedOffset").Call(ctx, scope, offset).Err(0)
}

// GetScopedOffset forwards the explicit scope.
func (m *OffsetStoreMock) GetScopedOffset(ctx context.Context, scope persistence.Scope, id *egopb.ProjectionId) (*egopb.Offset, error) {
	r := m.c.Method("GetScopedOffset").Call(ctx, scope, id)
	return mock.Value[*egopb.Offset](r, 0), r.Err(1)
}

// ResetScopedOffset forwards the explicit reset scope.
func (m *OffsetStoreMock) ResetScopedOffset(ctx context.Context, scope persistence.Scope, name string, value int64) error {
	return m.c.Method("ResetScopedOffset").Call(ctx, scope, name, value).Err(0)
}
