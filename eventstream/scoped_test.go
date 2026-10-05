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
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/tenancy"
)

func tenantScope(ctx *specs.Context, id string) Scope {
	scope, err := TenantScope(tenancy.TenantID(id))
	ctx.Expect(err).To(specs.BeNil())
	return scope
}

func tenantMetadata(ctx *specs.Context, id string) map[string]string {
	tid, err := tenancy.NewTenantID(id)
	ctx.Expect(err).To(specs.BeNil())
	tc, err := tenancy.NewTenantContext(tid)
	ctx.Expect(err).To(specs.BeNil())
	return tenancy.MarshalMetadata(tc)
}

func payloads(msgs []*Message) []any {
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Payload())
	}
	return out
}

func TestScopedStream(t *testing.T) {
	specs.Describe(t, "the scoped event stream", func(s *specs.Spec) {
		var (
			stream *EventsStream
			a, b   Scope
		)
		s.BeforeEach(func(ctx *specs.Context) {
			stream = New().(*EventsStream)
			ctx.Cleanup(stream.Close)
			a, b = tenantScope(ctx, "tenant-a"), tenantScope(ctx, "tenant-b")
		})

		s.It("delivers a tenant's message only to that tenant's subscribers", func(ctx *specs.Context) {
			subA, subB := stream.AddSubscriber(), stream.AddSubscriber()
			ctx.Expect(stream.SubscribeScoped(subA, a, "topic")).To(specs.BeNil())
			ctx.Expect(stream.SubscribeScoped(subB, b, "topic")).To(specs.BeNil())

			ctx.Expect(stream.PublishScoped(a, "topic", "for-a")).To(specs.BeNil())

			ctx.Expect(payloads(drain(subA))).To(specs.Equal([]any{"for-a"}))
			ctx.Expect(drain(subB)).To(specs.BeEmpty())
		})

		s.It("stamps the message with the scope it was published for", func(ctx *specs.Context) {
			sub := stream.AddSubscriber()
			ctx.Expect(stream.SubscribeScoped(sub, a, "topic")).To(specs.BeNil())
			ctx.Expect(stream.PublishScoped(a, "topic", "x")).To(specs.BeNil())
			msgs := drain(sub)
			ctx.Expect(msgs).To(specs.HaveLen(1))
			ctx.Expect(msgs[0].Scope().Equal(a)).To(specs.BeTrue())
		})

		s.It("never lets an unscoped subscriber see a tenant message, or the reverse", func(ctx *specs.Context) {
			legacy, tenant := stream.AddSubscriber(), stream.AddSubscriber()
			stream.Subscribe(legacy, "topic")
			ctx.Expect(stream.SubscribeScoped(tenant, a, "topic")).To(specs.BeNil())

			ctx.Expect(stream.PublishScoped(a, "topic", "tenant-msg")).To(specs.BeNil())
			stream.Publish("topic", "legacy-msg")

			ctx.Expect(payloads(drain(legacy))).To(specs.Equal([]any{"legacy-msg"}))
			ctx.Expect(payloads(drain(tenant))).To(specs.Equal([]any{"tenant-msg"}))
		})

		s.It("keeps Unscoped() a distinct scope from any tenant, scoped API included", func(ctx *specs.Context) {
			un, tenant := stream.AddSubscriber(), stream.AddSubscriber()
			ctx.Expect(stream.SubscribeScoped(un, Unscoped(), "topic")).To(specs.BeNil())
			ctx.Expect(stream.SubscribeScoped(tenant, a, "topic")).To(specs.BeNil())

			ctx.Expect(stream.PublishScoped(Unscoped(), "topic", "u")).To(specs.BeNil())

			ctx.Expect(payloads(drain(un))).To(specs.Equal([]any{"u"}))
			ctx.Expect(drain(tenant)).To(specs.BeEmpty())
		})

		s.It("treats the legacy API exactly as Unscoped()", func(ctx *specs.Context) {
			sub := stream.AddSubscriber()
			ctx.Expect(stream.SubscribeScoped(sub, Unscoped(), "topic")).To(specs.BeNil())
			stream.Publish("topic", "p")
			stream.Broadcast("b", []string{"topic"})
			ctx.Expect(payloads(drain(sub))).To(specs.Equal([]any{"p", "b"}))
			ctx.Expect(stream.SubscribersCount("topic")).To(specs.Equal(1))
		})

		s.It("cannot reach a tenant route through a crafted legacy topic name", func(ctx *specs.Context) {
			legacy := stream.AddSubscriber()
			for _, crafted := range []string{a.String() + "topic", a.String() + "\x00topic", "tenant-a/topic", a.String()} {
				stream.Subscribe(legacy, crafted)
			}
			ctx.Expect(stream.PublishScoped(a, "topic", "secret")).To(specs.BeNil())
			ctx.Expect(drain(legacy)).To(specs.BeEmpty())
		})

		s.It("fails closed on a zero-value scope: nothing is delivered or registered", func(ctx *specs.Context) {
			var zero Scope
			sub := stream.AddSubscriber()
			stream.Subscribe(sub, "topic")

			err := stream.PublishScoped(zero, "topic", "x")
			ctx.Expect(err).To(specs.MatchError(ErrInvalidScope))
			ctx.Expect(drain(sub)).To(specs.BeEmpty())

			err = stream.SubscribeScoped(sub, zero, "topic")
			ctx.Expect(err).To(specs.MatchError(ErrInvalidScope))
			err = stream.UnsubscribeScoped(sub, zero, "topic")
			ctx.Expect(err).To(specs.MatchError(ErrInvalidScope))
		})

		s.It("stops delivering after UnsubscribeScoped and after RemoveSubscriber", func(ctx *specs.Context) {
			sub, other := stream.AddSubscriber(), stream.AddSubscriber()
			ctx.Expect(stream.SubscribeScoped(sub, a, "topic")).To(specs.BeNil())
			ctx.Expect(stream.SubscribeScoped(other, a, "topic")).To(specs.BeNil())
			ctx.Expect(stream.SubscribeScoped(other, b, "topic")).To(specs.BeNil())

			ctx.Expect(stream.UnsubscribeScoped(sub, a, "topic")).To(specs.BeNil())
			ctx.Expect(stream.PublishScoped(a, "topic", "1")).To(specs.BeNil())
			ctx.Expect(drain(sub)).To(specs.BeEmpty())
			ctx.Expect(payloads(drain(other))).To(specs.Equal([]any{"1"}))

			ctx.Expect(other.Topics()).To(specs.Equal([]string{"topic"}))
			stream.RemoveSubscriber(other)
			ctx.Expect(stream.PublishScoped(b, "topic", "2")).To(specs.BeNil())
			ctx.Expect(drain(other)).To(specs.BeEmpty())
			ctx.Expect(other.Topics()).To(specs.BeEmpty())
		})

	})
}

func TestScope(t *testing.T) {
	specs.Describe(t, "the stream Scope", func(s *specs.Spec) {
		s.It("is invalid at its zero value, valid for Unscoped and tenant scopes, and never confuses them", func(ctx *specs.Context) {
			var zero Scope
			ctx.Expect(zero.Valid()).To(specs.BeFalse())
			ctx.Expect(Unscoped().Valid()).To(specs.BeTrue())
			a := tenantScope(ctx, "tenant-a")
			ctx.Expect(a.Valid()).To(specs.BeTrue())
			ctx.Expect(a.IsUnscoped()).To(specs.BeFalse())
			ctx.Expect(a.TenantID()).To(specs.Equal(tenancy.TenantID("tenant-a")))
			ctx.Expect(Unscoped().TenantID()).To(specs.Equal(tenancy.TenantID("")))
			ctx.Expect(a.Equal(tenantScope(ctx, "tenant-a"))).To(specs.BeTrue())
			ctx.Expect(a.Equal(tenantScope(ctx, "tenant-b"))).To(specs.BeFalse())
			ctx.Expect(Unscoped().Equal(tenantScope(ctx, "unscoped"))).To(specs.BeFalse())
			ctx.Expect(zero.String()).To(specs.Contain("invalid"))
			ctx.Expect(a.String()).To(specs.Contain("tenant-a"))
		})

		s.It("rejects an invalid tenant id", func(ctx *specs.Context) {
			_, err := TenantScope("")
			ctx.Expect(err).To(specs.MatchError(ErrInvalidScope))
		})
	})
}

func TestVerifyScope(t *testing.T) {
	specs.Describe(t, "VerifyScope", func(s *specs.Spec) {
		s.It("accepts a tenant scope with that tenant's metadata, and Unscoped() with none", func(ctx *specs.Context) {
			ctx.Expect(VerifyScope(tenantScope(ctx, "tenant-a"), tenantMetadata(ctx, "tenant-a"))).To(specs.BeNil())
			ctx.Expect(VerifyScope(Unscoped(), nil)).To(specs.BeNil())
		})

		s.It("rejects a zero-value scope", func(ctx *specs.Context) {
			var zero Scope
			ctx.Expect(VerifyScope(zero, nil)).To(specs.MatchError(ErrInvalidScope))
		})

		s.It("rejects absent, invalid, mismatched and administrative identity under a tenant scope", func(ctx *specs.Context) {
			a := tenantScope(ctx, "tenant-a")
			admin, err := tenancy.NewAdministrative("ops", "audit")
			ctx.Expect(err).To(specs.BeNil())
			adminCtx, err := tenancy.NewAdministrativeContext(admin)
			ctx.Expect(err).To(specs.BeNil())

			cases := map[string]map[string]string{
				"absent":         nil,
				"empty":          {},
				"garbage":        {"ego.tenant.scope": "nope"},
				"other tenant":   tenantMetadata(ctx, "tenant-b"),
				"administrative": tenancy.MarshalMetadata(adminCtx),
			}
			for name, md := range cases {
				err := VerifyScope(a, md)
				ctx.Expect(err).To(specs.MatchError(ErrScopeMismatch))
				_ = name
			}
		})

		s.It("rejects tenant metadata on an unscoped message", func(ctx *specs.Context) {
			err := VerifyScope(Unscoped(), tenantMetadata(ctx, "tenant-a"))
			ctx.Expect(err).To(specs.MatchError(ErrScopeMismatch))
		})
	})
}
