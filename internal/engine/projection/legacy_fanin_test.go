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
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

// plain is a Stream that cannot fan in.
type plain struct{ eventstream.Stream }

func TestLegacyFanInStream(t *testing.T) {
	specs.Describe(t, "the temporary legacy fan-in the projection runner is given (removed by #93)", func(s *specs.Spec) {
		s.It("wakes the runner for every tenant's events and for unscoped ones", func(ctx *specs.Context) {
			stream := eventstream.New()
			ctx.Cleanup(stream.Close)
			a, err := persistence.NewTenantScope(tenancy.TenantID("tenant-a"))
			ctx.Expect(err).To(specs.BeNil())

			runner := stream.AddSubscriber()
			withLegacyFanIn(stream).Subscribe(runner, "topic.events")

			scoped := stream.(eventstream.ScopedStream)
			ctx.Expect(scoped.PublishScoped(a, "topic.events", "a")).To(specs.BeNil())
			stream.Publish("topic.events", "u")

			var got int
			for range runner.Iterator() {
				got++
			}
			ctx.Expect(got).To(specs.Equal(2))
		})

		s.It("is not what the public paths use: a plain legacy subscriber still sees no tenant events", func(ctx *specs.Context) {
			stream := eventstream.New()
			ctx.Cleanup(stream.Close)
			a, err := persistence.NewTenantScope(tenancy.TenantID("tenant-a"))
			ctx.Expect(err).To(specs.BeNil())

			legacy := stream.AddSubscriber()
			stream.Subscribe(legacy, "topic.events")
			ctx.Expect(stream.(eventstream.ScopedStream).PublishScoped(a, "topic.events", "a")).To(specs.BeNil())

			ctx.Expect(legacy.Iterator()).To(specs.BeEmpty())
		})

		s.It("leaves a stream that cannot fan in unchanged", func(ctx *specs.Context) {
			stream := plain{eventstream.New()}
			ctx.Expect(withLegacyFanIn(stream)).To(specs.Equal(eventstream.Stream(stream)))
		})
	})
}
