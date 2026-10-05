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

package goakt

import (
	"context"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/travisjeffery/go-dynaport"
)

// TestClusterWithRemotingStartsRemoting: an App built with
// WithRemoting starts an actor system that serves remoting on the given port.
func TestClusterWithRemotingStartsRemoting(t *testing.T) {
	specs.Describe(t, "WithRemoting", func(s *specs.Spec) {
		s.It("starts the actor system with remoting enabled", func(ctx *specs.Context) {
			bg := context.Background()
			port := dynaport.Get(1)[0]
			app := mustNew(ctx, newFixture(ctx, "with-remoting").spec, WithRemoting("127.0.0.1", port))
			ctx.Expect(app.Start(bg)).To(specs.BeNil())
			ctx.Cleanup(func() { _ = app.Stop(bg) })

			ctx.Expect(app.sys.Host()).To(specs.Equal("127.0.0.1"))
			ctx.Expect(app.sys.Port()).To(specs.Equal(port))
		})
	})
}
