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
	nethttp "net/http"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/tochemey/goakt/v4/remote"
	"github.com/travisjeffery/go-dynaport"

	"github.com/getsyntegrity/urd/engine"
	"github.com/getsyntegrity/urd/tenancy"
)

// callerPropagator stands for a propagator the caller passes to WithRemoting.
type callerPropagator struct{}

func (callerPropagator) Inject(_ context.Context, h nethttp.Header) error {
	h.Set("X-Caller", "1")
	return nil
}

func (callerPropagator) Extract(ctx context.Context, _ nethttp.Header) (context.Context, error) {
	return ctx, nil
}

// injected returns the headers the remoting config's propagator writes for a
// context that carries the tenant acme.
func injected(ctx *specs.Context, cfg *remote.Config) nethttp.Header {
	ctx.Helper()
	tc, err := tenancy.NewTenantContext("acme")
	ctx.Expect(err).To(specs.BeNil())
	attached, err := tenancy.Attach(context.Background(), tc)
	ctx.Expect(err).To(specs.BeNil())
	headers := nethttp.Header{}
	ctx.Expect(cfg.ContextPropagator().Inject(attached, headers)).To(specs.BeNil())
	return headers
}

// TestRemotingConfig_InstallsTheTenantPropagator: the remoting config the App
// builds for WithRemoting carries the engine's tenant propagator, and it wins
// over a propagator the caller supplied; a legacy engine leaves the caller's.
func TestRemotingConfig_InstallsTheTenantPropagator(t *testing.T) {
	specs.Describe(t, "remotingConfig", func(s *specs.Spec) {
		s.It("installs the tenant propagator of a tenant-aware engine over the caller's", func(ctx *specs.Context) {
			resolver, err := tenancy.WithSingleTenant("acme")
			ctx.Expect(err).To(specs.BeNil())
			store := newFixture(ctx, "remoting-config").spec.EventsStore
			config := engine.NewConfig(store, engine.WithTenantResolver(resolver))

			spec := &remotingSpec{host: "127.0.0.1", port: 1, opts: []remote.Option{remote.WithContextPropagator(callerPropagator{})}}
			headers := injected(ctx, remotingConfig(config, spec))
			ctx.Expect(len(headers.Values("Urd-Tenant"))).To(specs.Equal(1))
			ctx.Expect(headers.Get("X-Caller")).To(specs.Equal(""))
		})

		s.It("keeps the caller's propagator when the engine is not tenant-aware", func(ctx *specs.Context) {
			store := newFixture(ctx, "remoting-config-legacy").spec.EventsStore
			spec := &remotingSpec{host: "127.0.0.1", port: 1, opts: []remote.Option{remote.WithContextPropagator(callerPropagator{})}}
			headers := injected(ctx, remotingConfig(engine.NewConfig(store), spec))
			ctx.Expect(headers.Get("X-Caller")).To(specs.Equal("1"))
			ctx.Expect(len(headers.Values("Urd-Tenant"))).To(specs.Equal(0))
		})
	})
}

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
