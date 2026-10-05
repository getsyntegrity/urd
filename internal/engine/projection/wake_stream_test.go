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

package projection

import (
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

func TestProjectionWakeStream(t *testing.T) {
	specs.Describe(t, "the temporary wake-up the projection runner is given in a tenant-aware engine (removed by #93)", func(s *specs.Spec) {
		tenantFixture := func(ctx *specs.Context) (persistence.Scope, map[string]string) {
			tid, err := tenancy.NewTenantID("tenant-a")
			ctx.Expect(err).To(specs.BeNil())
			scope, err := persistence.NewTenantScope(tid)
			ctx.Expect(err).To(specs.BeNil())
			tc, err := tenancy.NewTenantContext(tid)
			ctx.Expect(err).To(specs.BeNil())
			return scope, tenancy.MarshalMetadata(tc)
		}

		s.It("wakes the runner for a tenant's publication, delivering no payload", func(ctx *specs.Context) {
			stream := eventstream.New()
			ctx.Cleanup(stream.Close)
			scope, md := tenantFixture(ctx)

			runner := stream.AddSubscriber()
			withProjectionWake(stream).Subscribe(runner, protocol.EventsTopic)
			ctx.Expect(protocol.PublishScoped(stream, scope, protocol.EventsTopic, "secret-event", md)).To(specs.BeNil())

			var payloads []any
			for m := range runner.Iterator() {
				payloads = append(payloads, m.Payload())
			}
			ctx.Expect(payloads).To(specs.Equal([]any{nil}))
		})

		s.It("keeps nudging the runner for unscoped publications", func(ctx *specs.Context) {
			stream := eventstream.New()
			ctx.Cleanup(stream.Close)
			runner := stream.AddSubscriber()
			withProjectionWake(stream).Subscribe(runner, protocol.EventsTopic)

			ctx.Expect(protocol.PublishScoped(stream, persistence.Unscoped(), protocol.EventsTopic, "legacy", nil)).To(specs.BeNil())

			ctx.Expect(runner.Iterator()).To(specs.HaveLen(1))
		})

		s.It("is not what the public paths use: a plain legacy subscriber of the events topic sees no tenant event and no wake-up", func(ctx *specs.Context) {
			stream := eventstream.New()
			ctx.Cleanup(stream.Close)
			scope, md := tenantFixture(ctx)
			legacy := stream.AddSubscriber()
			stream.Subscribe(legacy, protocol.EventsTopic)
			stream.Subscribe(legacy, protocol.StatesTopic)

			ctx.Expect(protocol.PublishScoped(stream, scope, protocol.EventsTopic, "secret-event", md)).To(specs.BeNil())

			ctx.Expect(legacy.Iterator()).To(specs.BeEmpty())
		})
	})
}
