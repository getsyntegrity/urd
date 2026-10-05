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
// Unscoped(), and a tenant scope and Unscoped() never meet.
type ScopedStream interface {
	Stream
	// PublishScoped publishes msg on topic for scope. A zero-value scope fails
	// closed with an error matching ErrInvalidScope and nothing is
	// delivered.
	PublishScoped(scope Scope, topic string, msg any) error
	// SubscribeScoped subscribes sub to topic for scope only. A zero-value
	// scope fails closed with an error matching ErrInvalidScope and
	// nothing is registered.
	SubscribeScoped(sub Subscriber, scope Scope, topic string) error
	// UnsubscribeScoped removes a SubscribeScoped registration.
	UnsubscribeScoped(sub Subscriber, scope Scope, topic string) error
}

// route is the routing key: a (scope, topic) pair, compared as a struct.
type route struct {
	scope Scope
	topic string
}

func unscopedRoute(topic string) route {
	return route{scope: legacyScope(), topic: topic}
}

func legacyScope() Scope { return Unscoped() }

func invalidScope() error { return ErrInvalidScope }

// PublishScoped implements ScopedStream.
func (b *EventsStream) PublishScoped(scope Scope, topic string, msg any) error {
	if !scope.Valid() {
		return invalidScope()
	}
	b.publishToRoute(scope, topic, msg)
	return nil
}

// SubscribeScoped implements ScopedStream.
func (b *EventsStream) SubscribeScoped(sub Subscriber, scope Scope, topic string) error {
	if !scope.Valid() {
		return invalidScope()
	}
	b.subscribeRoute(sub, route{scope: scope, topic: topic})
	return nil
}

// UnsubscribeScoped implements ScopedStream.
func (b *EventsStream) UnsubscribeScoped(sub Subscriber, scope Scope, topic string) error {
	if !scope.Valid() {
		return invalidScope()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unsubscribeRoute(sub, route{scope: scope, topic: topic})
	return nil
}

// VerifyScope reports whether tenantMetadata (the tenant_metadata map of an
// event or durable state) agrees with scope. It fails closed:
//
//   - a zero-value scope returns an error matching ErrInvalidScope;
//   - Unscoped() requires no tenant metadata at all;
//   - a tenant scope requires tenant metadata of that same, valid tenant;
//     absent, invalid, administrative or different identity returns an error
//     matching ErrScopeMismatch. There is no administrative bypass.
func VerifyScope(scope Scope, tenantMetadata map[string]string) error {
	if !scope.Valid() {
		return ErrInvalidScope
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
