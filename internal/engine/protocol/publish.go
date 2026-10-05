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

package protocol

import (
	"errors"

	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/persistence"
)

// PublishScoped publishes msg on topic for scope, the way every publish site
// of the engine does (EGO-TENANT-005). It fails closed and publishes nothing
// when scope is invalid or tenantMetadata (the message's own tenant_metadata)
// does not agree with scope (eventstream.VerifyScope), and when a tenant scope
// meets a stream that cannot route by scope. Under Unscoped() it is exactly
// the legacy Publish.
func PublishScoped(stream eventstream.Stream, scope persistence.Scope, topic string, msg any, tenantMetadata map[string]string) error {
	if err := eventstream.VerifyScope(scope, tenantMetadata); err != nil {
		return err
	}
	if scoped, ok := stream.(eventstream.ScopedStream); ok {
		return scoped.PublishScoped(scope, topic, msg)
	}
	if !scope.IsUnscoped() {
		return errors.Join(persistence.ErrInvalidScope, errors.New("event stream cannot route by tenant scope"))
	}
	stream.Publish(topic, msg)
	return nil
}

// SubscribeScoped subscribes sub to topic for scope only, the counterpart of
// PublishScoped. It fails closed, registering nothing, when scope is invalid or
// when a tenant scope meets a stream that cannot route by scope. Under
// Unscoped() it is exactly the legacy Subscribe.
func SubscribeScoped(stream eventstream.Stream, sub eventstream.Subscriber, scope persistence.Scope, topic string) error {
	if scoped, ok := stream.(eventstream.ScopedStream); ok {
		return scoped.SubscribeScoped(sub, scope, topic)
	}
	if !scope.Valid() {
		return persistence.ErrInvalidScope
	}
	if !scope.IsUnscoped() {
		return errors.Join(persistence.ErrInvalidScope, errors.New("event stream cannot route by tenant scope"))
	}
	stream.Subscribe(sub, topic)
	return nil
}
