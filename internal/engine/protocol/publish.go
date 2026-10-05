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

// ProjectionWakeTopic is the TEMPORARY, INTERNAL, payload-free wake-up topic
// the projection runner is nudged on in a tenant-aware engine (EGO-TENANT-005).
//
// The runner subscribes through the legacy Unscoped() API, which by design
// never receives a tenant's events, and its file belongs to #93. It only uses
// the stream as a "something was persisted" nudge before it pulls from the
// scoped events store, so PublishScoped publishes a message with a nil payload
// here for every tenant-scoped publication. The message carries no event, no
// state and no tenant identity, so it leaks nothing across tenants. #93
// removes this topic when it gives the runner a scope. It is not subscribed
// by Engine.Subscribe, AddEventPublishers or AddStatePublishers.
const ProjectionWakeTopic = "topic.projection.wake"

// StreamScope converts the persistence scope an actor is bound to into the
// scope of the event stream. A zero-value or invalid persistence.Scope fails
// closed with an error matching persistence.ErrInvalidScope and
// eventstream.ErrInvalidScope.
func StreamScope(scope persistence.Scope) (eventstream.Scope, error) {
	switch {
	case !scope.Valid():
		return eventstream.Scope{}, errors.Join(persistence.ErrInvalidScope, eventstream.ErrInvalidScope)
	case scope.IsUnscoped():
		return eventstream.Unscoped(), nil
	default:
		out, err := eventstream.TenantScope(scope.TenantID())
		if err != nil {
			return eventstream.Scope{}, errors.Join(persistence.ErrInvalidScope, err)
		}
		return out, nil
	}
}

// PublishScoped publishes msg on topic for scope, the way every publish site
// of the engine does (EGO-TENANT-005). It fails closed and publishes nothing
// when scope is invalid or tenantMetadata (the message's own tenant_metadata)
// does not agree with scope (eventstream.VerifyScope), and when a tenant scope
// meets a stream that cannot route by scope. Under Unscoped() it is exactly
// the legacy Publish. After a tenant-scoped publication it nudges the
// projection runner on ProjectionWakeTopic.
func PublishScoped(stream eventstream.Stream, scope persistence.Scope, topic string, msg any, tenantMetadata map[string]string) error {
	streamScope, err := StreamScope(scope)
	if err != nil {
		return err
	}
	if err := eventstream.VerifyScope(streamScope, tenantMetadata); err != nil {
		return err
	}
	if scoped, ok := stream.(eventstream.ScopedStream); ok {
		if err := scoped.PublishScoped(streamScope, topic, msg); err != nil {
			return err
		}
		if !streamScope.IsUnscoped() {
			stream.Publish(ProjectionWakeTopic, nil)
		}
		return nil
	}
	if !streamScope.IsUnscoped() {
		return errors.Join(eventstream.ErrInvalidScope, errors.New("event stream cannot route by tenant scope"))
	}
	stream.Publish(topic, msg)
	return nil
}

// SubscribeScoped subscribes sub to topic for scope only, the counterpart of
// PublishScoped. It fails closed, registering nothing, when scope is invalid or
// when a tenant scope meets a stream that cannot route by scope. Under
// Unscoped() it is exactly the legacy Subscribe.
func SubscribeScoped(stream eventstream.Stream, sub eventstream.Subscriber, scope persistence.Scope, topic string) error {
	streamScope, err := StreamScope(scope)
	if err != nil {
		return err
	}
	if scoped, ok := stream.(eventstream.ScopedStream); ok {
		return scoped.SubscribeScoped(sub, streamScope, topic)
	}
	if !streamScope.IsUnscoped() {
		return errors.Join(eventstream.ErrInvalidScope, errors.New("event stream cannot route by tenant scope"))
	}
	stream.Subscribe(sub, topic)
	return nil
}
