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

package protocol_test

import (
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

// plainStream hides ScopedStream: it is a Stream that cannot route by scope,
// like an external implementation written before ScopedStream existed.
type plainStream struct{ eventstream.Stream }

func scopeOf(ctx *specs.Context, id string) (persistence.Scope, map[string]string) {
	tid, err := tenancy.NewTenantID(id)
	ctx.Expect(err).To(specs.BeNil())
	scope, err := persistence.NewTenantScope(tid)
	ctx.Expect(err).To(specs.BeNil())
	tc, err := tenancy.NewTenantContext(tid)
	ctx.Expect(err).To(specs.BeNil())
	return scope, tenancy.MarshalMetadata(tc)
}

func drainPayloads(sub eventstream.Subscriber) []any {
	var out []any
	for m := range sub.Iterator() {
		out = append(out, m.Payload())
	}
	return out
}

func TestPublishScoped(t *testing.T) {
	specs.Describe(t, "the engine publish site", func(s *specs.Spec) {
		var stream eventstream.Stream
		s.BeforeEach(func(ctx *specs.Context) {
			stream = eventstream.New()
			ctx.Cleanup(stream.Close)
		})

		s.It("publishes for the scope it holds when the metadata agrees", func(ctx *specs.Context) {
			a, md := scopeOf(ctx, "tenant-a")
			b, _ := scopeOf(ctx, "tenant-b")
			subA, subB := stream.AddSubscriber(), stream.AddSubscriber()
			ctx.Expect(protocol.SubscribeScoped(stream, subA, a, "t")).To(specs.BeNil())
			ctx.Expect(protocol.SubscribeScoped(stream, subB, b, "t")).To(specs.BeNil())

			ctx.Expect(protocol.PublishScoped(stream, a, "t", "evt", md)).To(specs.BeNil())

			ctx.Expect(drainPayloads(subA)).To(specs.Equal([]any{"evt"}))
			ctx.Expect(drainPayloads(subB)).To(specs.BeEmpty())
		})

		s.It("refuses, delivering nothing, when the metadata is absent or names another tenant", func(ctx *specs.Context) {
			a, _ := scopeOf(ctx, "tenant-a")
			_, mdB := scopeOf(ctx, "tenant-b")
			sub := stream.AddSubscriber()
			ctx.Expect(protocol.SubscribeScoped(stream, sub, a, "t")).To(specs.BeNil())

			ctx.Expect(protocol.PublishScoped(stream, a, "t", "evt", nil)).To(specs.MatchError(eventstream.ErrScopeMismatch))
			ctx.Expect(protocol.PublishScoped(stream, a, "t", "evt", mdB)).To(specs.MatchError(eventstream.ErrScopeMismatch))
			ctx.Expect(drainPayloads(sub)).To(specs.BeEmpty())
		})

		s.It("refuses a zero-value scope", func(ctx *specs.Context) {
			var zero persistence.Scope
			ctx.Expect(protocol.PublishScoped(stream, zero, "t", "evt", nil)).To(specs.MatchError(persistence.ErrInvalidScope))
		})

		s.It("keeps single-tenant mode zero-plumbing: Unscoped() publishes with no metadata", func(ctx *specs.Context) {
			sub := stream.AddSubscriber()
			ctx.Expect(protocol.SubscribeScoped(stream, sub, persistence.Unscoped(), "t")).To(specs.BeNil())
			ctx.Expect(protocol.PublishScoped(stream, persistence.Unscoped(), "t", "evt", nil)).To(specs.BeNil())
			ctx.Expect(drainPayloads(sub)).To(specs.Equal([]any{"evt"}))
		})

		s.It("falls back to the legacy methods for Unscoped() on a stream without ScopedStream, and refuses a tenant scope there", func(ctx *specs.Context) {
			plain := plainStream{stream}
			sub := stream.AddSubscriber()
			ctx.Expect(protocol.SubscribeScoped(plain, sub, persistence.Unscoped(), "t")).To(specs.BeNil())
			ctx.Expect(protocol.PublishScoped(plain, persistence.Unscoped(), "t", "evt", nil)).To(specs.BeNil())
			ctx.Expect(drainPayloads(sub)).To(specs.Equal([]any{"evt"}))

			a, md := scopeOf(ctx, "tenant-a")
			ctx.Expect(protocol.PublishScoped(plain, a, "t", "evt", md)).To(specs.MatchError(eventstream.ErrInvalidScope))
			ctx.Expect(protocol.SubscribeScoped(plain, sub, a, "t")).To(specs.MatchError(eventstream.ErrInvalidScope))
		})
	})
}
