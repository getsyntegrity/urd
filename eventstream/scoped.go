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

package eventstream

import (
	"errors"

	"github.com/getsyntegrity/urd/internal/legacyfanin"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

// ErrScopeMismatch is returned by VerifyScope, and reported by the publish and
// delivery boundaries, when the tenant identity a message carries does not
// agree with the scope it is published or delivered for.
var ErrScopeMismatch = errors.New("eventstream: message tenant identity does not match its scope")

// ScopedStream is the OPTIONAL tenant-aware extension of Stream (EGO-TENANT-005).
// Stream itself is unchanged, so existing implementers keep compiling; the
// engine uses ScopedStream when the stream implements it. EventsStream does.
//
// Isolation is by construction: a route is the pair (scope, topic), compared as
// a struct, never as a concatenated string. A subscription made for scope S on
// topic T receives only messages published for S on T. The legacy Publish and
// Subscribe of Stream are exactly PublishScoped and SubscribeScoped with
// persistence.Unscoped(), and a tenant scope and Unscoped() never meet.
type ScopedStream interface {
	Stream
	// PublishScoped publishes msg on topic for scope. A zero-value scope fails
	// closed with an error matching persistence.ErrInvalidScope and nothing is
	// delivered.
	PublishScoped(scope persistence.Scope, topic string, msg any) error
	// SubscribeScoped subscribes sub to topic for scope only. A zero-value
	// scope fails closed with an error matching persistence.ErrInvalidScope and
	// nothing is registered.
	SubscribeScoped(sub Subscriber, scope persistence.Scope, topic string) error
	// UnsubscribeScoped removes a SubscribeScoped registration.
	UnsubscribeScoped(sub Subscriber, scope persistence.Scope, topic string) error
}

// route is the routing key: a (scope, topic) pair, or, when fanIn is set, the
// internal legacy fan-in of topic that receives every scope.
type route struct {
	scope persistence.Scope
	topic string
	fanIn bool
}

func unscopedRoute(topic string) route {
	return route{scope: legacyScope(), topic: topic}
}

func legacyScope() persistence.Scope { return persistence.Unscoped() }

func invalidScope() error {
	return persistence.ErrInvalidScope
}

// PublishScoped implements ScopedStream.
func (b *EventsStream) PublishScoped(scope persistence.Scope, topic string, msg any) error {
	if !scope.Valid() {
		return invalidScope()
	}
	b.publishToRoute(scope, topic, msg)
	return nil
}

// SubscribeScoped implements ScopedStream.
func (b *EventsStream) SubscribeScoped(sub Subscriber, scope persistence.Scope, topic string) error {
	if !scope.Valid() {
		return invalidScope()
	}
	b.subscribeRoute(sub, route{scope: scope, topic: topic})
	return nil
}

// UnsubscribeScoped implements ScopedStream.
func (b *EventsStream) UnsubscribeScoped(sub Subscriber, scope persistence.Scope, topic string) error {
	if !scope.Valid() {
		return invalidScope()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unsubscribeRoute(sub, route{scope: scope, topic: topic})
	return nil
}

// SubscribeFanIn subscribes sub to topic for every scope.
//
// TEMPORARY, INTERNAL: this exists only for the projection actor until #93
// scopes the projection runner. It requires a legacyfanin.Grant, a type of an
// internal package, so no code outside this module can call it, and the public
// Subscribe, SubscribeScoped, AddEventPublishers and AddStatePublishers paths
// never reach it.
func (b *EventsStream) SubscribeFanIn(sub Subscriber, topic string, _ legacyfanin.Grant) {
	b.subscribeRoute(sub, route{fanIn: true, topic: topic})
}

// VerifyScope reports whether tenantMetadata (the tenant_metadata map of an
// event or durable state) agrees with scope. It fails closed:
//
//   - a zero-value scope returns an error matching persistence.ErrInvalidScope;
//   - Unscoped() requires no tenant metadata at all;
//   - a tenant scope requires tenant metadata of that same, valid tenant;
//     absent, invalid, administrative or different identity returns an error
//     matching ErrScopeMismatch. There is no administrative bypass.
func VerifyScope(scope persistence.Scope, tenantMetadata map[string]string) error {
	if !scope.Valid() {
		return persistence.ErrInvalidScope
	}
	if scope.IsUnscoped() {
		if len(tenantMetadata) != 0 {
			return errors.Join(ErrScopeMismatch, errors.New("tenant metadata present on an unscoped message"))
		}
		return nil
	}
	tc, err := tenancy.UnmarshalMetadata(tenancy.Metadata(tenantMetadata))
	if err != nil {
		return errors.Join(ErrScopeMismatch, err)
	}
	id, ok := tc.Tenant()
	if !ok || id != scope.TenantID() {
		return errors.Join(ErrScopeMismatch, errors.New("tenant metadata names a different tenant"))
	}
	return nil
}
