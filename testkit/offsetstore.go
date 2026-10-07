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

package testkit

import (
	"context"
	"sync"
	"time"

	"go.uber.org/atomic"
	"google.golang.org/protobuf/proto"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/offsetstore"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/port/adapter"
)

type OffsetKey struct {
	TenantID       string
	ProjectionName string
	ShardNumber    uint64
}
type OffsetStore struct {
	db        *sync.Map
	connected *atomic.Bool
}

var _ offsetstore.OffsetStore = (*OffsetStore)(nil)

// Describe implements adapter.Describer. It declares no capability:
// CapReady is implied by the store port, whose interface already has Ping.
func (x *OffsetStore) Describe() adapter.Descriptor {
	return adapter.Descriptor{Ports: []adapter.Port{offsetstore.PortOffsetStore}, Name: "testkit-memory"}
}

func NewOffsetStore() *OffsetStore {
	return &OffsetStore{
		db:        &sync.Map{},
		connected: atomic.NewBool(false),
	}
}

func (x *OffsetStore) Connect(context.Context) error {
	if x.connected.Load() {
		return nil
	}
	x.connected.Store(true)
	return nil
}

func (x *OffsetStore) Disconnect(context.Context) error {
	if !x.connected.Load() {
		return nil
	}
	x.db.Range(func(key interface{}, _ interface{}) bool {
		x.db.Delete(key)
		return true
	})
	x.connected.Store(false)
	return nil
}

func (x *OffsetStore) Ping(ctx context.Context) error {
	if !x.connected.Load() {
		return x.Connect(ctx)
	}
	return nil
}

func (x *OffsetStore) WriteOffset(ctx context.Context, offset *egopb.Offset) error {
	return x.WriteScopedOffset(ctx, persistence.Unscoped(), offset)
}

func (x *OffsetStore) WriteScopedOffset(_ context.Context, scope persistence.Scope, offset *egopb.Offset) error {
	if !scope.Valid() {
		return persistence.ErrInvalidScope
	}
	key := OffsetKey{
		TenantID:       string(scope.TenantID()),
		ProjectionName: offset.GetProjectionName(),
		ShardNumber:    offset.GetShardNumber(),
	}
	x.db.Store(key, proto.Clone(offset).(*egopb.Offset))
	return nil
}

func (x *OffsetStore) GetCurrentOffset(ctx context.Context, projectionID *egopb.ProjectionId) (*egopb.Offset, error) {
	return x.GetScopedOffset(ctx, persistence.Unscoped(), projectionID)
}

func (x *OffsetStore) GetScopedOffset(_ context.Context, scope persistence.Scope, projectionID *egopb.ProjectionId) (*egopb.Offset, error) {
	if !scope.Valid() {
		return nil, persistence.ErrInvalidScope
	}
	key := OffsetKey{
		TenantID:       string(scope.TenantID()),
		ProjectionName: projectionID.GetProjectionName(),
		ShardNumber:    projectionID.GetShardNumber(),
	}
	value, ok := x.db.Load(key)
	if !ok {
		return nil, nil
	}
	return proto.Clone(value.(*egopb.Offset)).(*egopb.Offset), nil
}

func (x *OffsetStore) ResetOffset(ctx context.Context, projectionName string, value int64) error {
	return x.ResetScopedOffset(ctx, persistence.Unscoped(), projectionName, value)
}

func (x *OffsetStore) ResetScopedOffset(_ context.Context, scope persistence.Scope, projectionName string, value int64) error {
	if !scope.Valid() {
		return persistence.ErrInvalidScope
	}
	ts := time.Now().UnixMilli()
	x.db.Range(func(k interface{}, v interface{}) bool {
		key := k.(OffsetKey)
		if key.ProjectionName != projectionName || key.TenantID != string(scope.TenantID()) {
			return true
		}
		// Store a copy: the stored offset may be the caller's own pointer, which
		// a reset must not modify.
		reset := proto.Clone(v.(*egopb.Offset)).(*egopb.Offset)
		reset.Value = value
		reset.Timestamp = ts
		x.db.Store(key, reset)
		return true
	})
	return nil
}
