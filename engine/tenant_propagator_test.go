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

package engine

import (
	"context"
	"errors"
	nethttp "net/http"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/command"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/tenancy"
)

func tenantCtx(ctx *specs.Context, id string) (context.Context, tenancy.TenantContext) {
	tid, err := tenancy.NewTenantID(id)
	ctx.Expect(err).To(specs.BeNil())
	tc, err := tenancy.NewTenantContext(tid)
	ctx.Expect(err).To(specs.BeNil())
	attached, err := tenancy.Attach(context.Background(), tc)
	ctx.Expect(err).To(specs.BeNil())
	return attached, tc
}

func TestTenantPropagatorRoundTrip(t *testing.T) {
	specs.Describe(t, "the tenant propagator", func(s *specs.Spec) {
		s.It("carries the tenant identity and the command carrier to the other side", func(ctx *specs.Context) {
			sender, tc := tenantCtx(ctx, "acme")
			op, err := command.GenerateOperationID()
			ctx.Expect(err).To(specs.BeNil())
			md, err := command.NewMetadata(op, command.WithCustom("k", "v"))
			ctx.Expect(err).To(specs.BeNil())
			sender = protocol.AttachCarrier(sender, command.MarshalMetadata(md))

			headers := nethttp.Header{}
			ctx.Expect(tenantPropagator{}.Inject(sender, headers)).To(specs.BeNil())

			got, err := tenantPropagator{}.Extract(context.Background(), headers)
			ctx.Expect(err).To(specs.BeNil())
			gotTenant, ok := tenancy.From(got)
			ctx.Expect(ok).To(specs.BeTrue())
			ctx.Expect(gotTenant == tc).To(specs.BeTrue())
			gotMD, ok := protocol.MetadataFromContext(got)
			ctx.Expect(ok).To(specs.BeTrue())
			ctx.Expect(gotMD.OperationID() == op).To(specs.BeTrue())
			v, _ := gotMD.CustomValue("k")
			ctx.Expect(v == "v").To(specs.BeTrue())
		})

		s.It("sends nothing when the context has no identity and leaves an empty header untouched", func(ctx *specs.Context) {
			headers := nethttp.Header{}
			ctx.Expect(tenantPropagator{}.Inject(context.Background(), headers)).To(specs.BeNil())
			ctx.Expect(len(headers) == 0).To(specs.BeTrue())

			bg := context.Background()
			got, err := tenantPropagator{}.Extract(bg, headers)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got == bg).To(specs.BeTrue())
			_, ok := tenancy.From(got)
			ctx.Expect(ok).To(specs.BeFalse())
		})
	})
}

func TestTenantPropagatorRejectsHostileInput(t *testing.T) {
	specs.Describe(t, "the tenant propagator on inbound data", func(s *specs.Spec) {
		valid := func(ctx *specs.Context) nethttp.Header {
			sender, _ := tenantCtx(ctx, "acme")
			h := nethttp.Header{}
			ctx.Expect(tenantPropagator{}.Inject(sender, h)).To(specs.BeNil())
			return h
		}
		rejected := func(ctx *specs.Context, p tenantPropagator, h nethttp.Header) {
			_, err := p.Extract(context.Background(), h)
			ctx.Expect(errors.Is(err, errRemoteIdentityRejected)).To(specs.BeTrue())
		}

		s.It("rejects a missing or unknown wire version", func(ctx *specs.Context) {
			h := valid(ctx)
			h.Del(wireHeaderVersion)
			rejected(ctx, tenantPropagator{}, h)
			h.Set(wireHeaderVersion, "999")
			rejected(ctx, tenantPropagator{}, h)
		})
		s.It("rejects a malformed tenant header", func(ctx *specs.Context) {
			h := valid(ctx)
			h.Set(wireHeaderTenant, "not json")
			rejected(ctx, tenantPropagator{}, h)
			h.Set(wireHeaderTenant, `{"ego.tenant.scope":"tenant","ego.tenant.id":""}`)
			rejected(ctx, tenantPropagator{}, h)
		})
		s.It("rejects an oversized header", func(ctx *specs.Context) {
			h := valid(ctx)
			h.Set(wireHeaderTenant, strings.Repeat("a", maxWireValueBytes+1))
			rejected(ctx, tenantPropagator{}, h)
		})
		s.It("rejects a duplicated header", func(ctx *specs.Context) {
			h := valid(ctx)
			h.Add(wireHeaderTenant, h.Get(wireHeaderTenant))
			rejected(ctx, tenantPropagator{}, h)
		})
		s.It("always rejects an administrative scope on the wire, and never sends it", func(ctx *specs.Context) {
			admin, err := tenancy.NewAdministrative("ops", "audit")
			ctx.Expect(err).To(specs.BeNil())
			tc, err := tenancy.NewAdministrativeContext(admin)
			ctx.Expect(err).To(specs.BeNil())
			sender, err := tenancy.Attach(context.Background(), tc)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(errors.Is(tenantPropagator{}.Inject(sender, nethttp.Header{}), errRemoteIdentityRejected)).To(specs.BeTrue())

			h := valid(ctx)
			h.Set(wireHeaderTenant, `{"ego.tenant.scope":"administrative","ego.tenant.admin_actor":"ops","ego.tenant.admin_reason":"audit"}`)
			rejected(ctx, tenantPropagator{}, h)
		})
		s.It("rejects a command header that names a tenant", func(ctx *specs.Context) {
			h := valid(ctx)
			h.Set(wireHeaderCommand, `{"ego.tenant.id":"globex"}`)
			rejected(ctx, tenantPropagator{}, h)
		})
		s.It("rejects an invalid command header", func(ctx *specs.Context) {
			h := valid(ctx)
			h.Set(wireHeaderCommand, `{"ego.cmd.operation_id":"x"}`)
			rejected(ctx, tenantPropagator{}, h)
		})
		s.It("rejects a context that already carries another tenant", func(ctx *specs.Context) {
			h := valid(ctx)
			other, _ := tenantCtx(ctx, "globex")
			_, err := tenantPropagator{}.Extract(other, h)
			ctx.Expect(err).To(specs.Not(specs.BeNil()))
		})
		s.It("accepts only the fixed tenant of a single-tenant node", func(ctx *specs.Context) {
			h := valid(ctx)
			_, err := tenantPropagator{fixed: "acme"}.Extract(context.Background(), h)
			ctx.Expect(err).To(specs.BeNil())
			rejected(ctx, tenantPropagator{fixed: "globex"}, h)
		})
	})
}

func TestConfigRemoteOptions(t *testing.T) {
	specs.Describe(t, "Config.RemoteOptions", func(s *specs.Spec) {
		s.It("is empty for a legacy engine and wires the propagator for a tenant-aware one", func(ctx *specs.Context) {
			store := connectedEventsStore(ctx)
			ctx.Expect(len(NewConfig(store, WithLogger(DiscardLogger)).RemoteOptions()) == 0).To(specs.BeTrue())
			cfg := NewConfig(store, WithLogger(DiscardLogger), WithTenantResolver(perCallerTenantResolver{}))
			ctx.Expect(len(cfg.RemoteOptions()) == 1).To(specs.BeTrue())
			var nilCfg *Config
			ctx.Expect(len(nilCfg.RemoteOptions()) == 0).To(specs.BeTrue())
		})
	})
}
