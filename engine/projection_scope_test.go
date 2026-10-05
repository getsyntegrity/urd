/*
 * MIT License
 *
 * Copyright (c) 2022-2026 Arsene Tochemey Gandote
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy of
 * this software and associated documentation files (the "Software"), to deal in
 * the Software without restriction, including without limitation the rights to
 * use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
 * the Software, and to permit persons to whom the Software is furnished to do so,
 * subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
 * FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
 * COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
 * IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
 * CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 */

package engine

import (
	"context"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/projection"
	"github.com/getsyntegrity/urd/tenancy"
)

type scopeProbeHandler struct{}

func (scopeProbeHandler) Handle(_ context.Context, _ string, _ *anypb.Any, _ uint64) error {
	return nil
}

func scopeTenant(ctx *specs.Context, id string) persistence.Scope {
	tid, err := tenancy.NewTenantID(id)
	ctx.Expect(err).To(specs.BeNil())
	scope, err := persistence.NewTenantScope(tid)
	ctx.Expect(err).To(specs.BeNil())
	return scope
}

// unscopedPtr is the resolved scope the engine would hand a legacy projection;
// tests that build the projection registry by hand set it explicitly.
func unscopedPtr() *persistence.Scope {
	scope := persistence.Unscoped()
	return &scope
}

func scopeOpts(scope *persistence.Scope) *projection.Options {
	return &projection.Options{Handler: scopeProbeHandler{}, Scope: scope}
}

func fixedResolver(ctx *specs.Context, id string) tenancy.TenantResolver {
	tid, err := tenancy.NewTenantID(id)
	ctx.Expect(err).To(specs.BeNil())
	resolver, err := tenancy.WithSingleTenant(tid)
	ctx.Expect(err).To(specs.BeNil())
	return resolver
}

func resolve(opts ...Option) (map[string]*projection.Options, error) {
	return NewConfig(nil, opts...).resolveProjections()
}

// TestProjectionScopeMatrix is the #71 contract matrix: omitted, explicit
// Unscoped(), explicit tenant and explicit invalid scope on an engine without
// tenancy, with a fixed single-tenant resolver and with a multi-tenant one
// (AC-R3-1 to AC-R3-7, AC-R5-1, AC-R5-2).
func TestProjectionScopeMatrix(t *testing.T) {
	specs.Describe(t, "projection scope against the engine tenancy mode", func(s *specs.Spec) {
		s.It("legacy engine: omitted binds Unscoped and explicit Unscoped is admitted (AC-R5-1)", func(ctx *specs.Context) {
			reg, err := resolve(WithProjection("p", scopeOpts(nil)))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(reg["p"].Scope.IsUnscoped()).To(specs.BeTrue())

			unscoped := persistence.Unscoped()
			reg, err = resolve(WithProjection("p", scopeOpts(&unscoped)))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(reg["p"].Scope.IsUnscoped()).To(specs.BeTrue())
		})

		s.It("legacy engine: an explicit tenant scope is rejected and tenancy is not enabled (AC-R3-7)", func(ctx *specs.Context) {
			acme := scopeTenant(ctx, "acme")
			_, err := resolve(WithProjection("p", scopeOpts(&acme)))
			ctx.Expect(err).To(specs.MatchError(ErrProjectionScopeRejected))
		})

		s.It("an explicit invalid scope is rejected in every mode (AC-R3-1, AC-R3-3)", func(ctx *specs.Context) {
			invalid := persistence.Scope{}
			_, err := resolve(WithProjection("p", scopeOpts(&invalid)))
			ctx.Expect(err).To(specs.MatchError(ErrProjectionScopeRejected))

			_, err = resolve(WithTenantResolver(fixedResolver(ctx, "acme")), WithProjection("p", scopeOpts(&invalid)))
			ctx.Expect(err).To(specs.MatchError(ErrProjectionScopeRejected))

			_, err = resolve(WithTenantResolver(&stubTenantResolver{id: "acme"}), WithProjection("p", scopeOpts(&invalid)))
			ctx.Expect(err).To(specs.MatchError(ErrProjectionScopeRejected))
		})

		s.It("fixed single-tenant: omitted binds the fixed tenant (AC-R5-2)", func(ctx *specs.Context) {
			reg, err := resolve(WithTenantResolver(fixedResolver(ctx, "acme")), WithProjection("p", scopeOpts(nil)))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(reg["p"].Scope.Equal(scopeTenant(ctx, "acme"))).To(specs.BeTrue())
		})

		s.It("fixed single-tenant: the same tenant is admitted, another tenant and Unscoped are rejected (AC-R3-4, AC-R3-5)", func(ctx *specs.Context) {
			acme, other, unscoped := scopeTenant(ctx, "acme"), scopeTenant(ctx, "other"), persistence.Unscoped()
			resolver := WithTenantResolver(fixedResolver(ctx, "acme"))

			reg, err := resolve(resolver, WithProjection("p", scopeOpts(&acme)))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(reg["p"].Scope.Equal(acme)).To(specs.BeTrue())

			_, err = resolve(resolver, WithProjection("p", scopeOpts(&other)))
			ctx.Expect(err).To(specs.MatchError(ErrProjectionScopeRejected))

			_, err = resolve(resolver, WithProjection("p", scopeOpts(&unscoped)))
			ctx.Expect(err).To(specs.MatchError(ErrProjectionScopeRejected))
		})

		s.It("multi-tenant: omitted fails closed with no Unscoped fallback (AC-R3-2)", func(ctx *specs.Context) {
			_, err := resolve(WithTenantResolver(&stubTenantResolver{id: "acme"}), WithProjection("p", scopeOpts(nil)))
			ctx.Expect(err).To(specs.MatchError(ErrProjectionScopeUndetermined))
		})

		s.It("multi-tenant: an explicit tenant is admitted and explicit Unscoped is rejected (AC-R3-5)", func(ctx *specs.Context) {
			acme, unscoped := scopeTenant(ctx, "acme"), persistence.Unscoped()
			resolver := WithTenantResolver(&stubTenantResolver{id: "x"})

			reg, err := resolve(resolver, WithProjection("p", scopeOpts(&acme)))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(reg["p"].Scope.Equal(acme)).To(specs.BeTrue())

			_, err = resolve(resolver, WithProjection("p", scopeOpts(&unscoped)))
			ctx.Expect(err).To(specs.MatchError(ErrProjectionScopeRejected))
		})

		s.It("the result does not depend on the order of the options", func(ctx *specs.Context) {
			acme := scopeTenant(ctx, "other")
			_, first := resolve(WithProjection("p", scopeOpts(&acme)), WithTenantResolver(fixedResolver(ctx, "acme")))
			_, second := resolve(WithTenantResolver(fixedResolver(ctx, "acme")), WithProjection("p", scopeOpts(&acme)))
			ctx.Expect(first).To(specs.MatchError(ErrProjectionScopeRejected))
			ctx.Expect(second).To(specs.MatchError(ErrProjectionScopeRejected))
		})
	})
}

// TestProjectionRegistryIdentity covers (scope, name) identity: exact
// duplicates are rejected, the same name under two scopes is two registrations
// that the engine refuses until scoped addressing exists, and a registration
// is a copy (AC-R2-1, AC-R3-6).
func TestProjectionRegistryIdentity(t *testing.T) {
	specs.Describe(t, "projection registry identity", func(s *specs.Spec) {
		s.It("rejects an exact (scope, name) duplicate instead of last-wins", func(ctx *specs.Context) {
			acme := scopeTenant(ctx, "acme")
			_, err := resolve(WithTenantResolver(&stubTenantResolver{id: "x"}),
				WithProjection("p", scopeOpts(&acme)), WithProjection("p", scopeOpts(&acme)))
			ctx.Expect(err).To(specs.MatchError(ErrProjectionDuplicate))

			_, err = resolve(WithProjection("p", scopeOpts(nil)), WithProjection("p", scopeOpts(nil)))
			ctx.Expect(err).To(specs.MatchError(ErrProjectionDuplicate))
		})

		s.It("an omitted scope duplicates the explicit scope it resolves to", func(ctx *specs.Context) {
			acme := scopeTenant(ctx, "acme")
			_, err := resolve(WithTenantResolver(fixedResolver(ctx, "acme")),
				WithProjection("p", scopeOpts(nil)), WithProjection("p", scopeOpts(&acme)))
			ctx.Expect(err).To(specs.MatchError(ErrProjectionDuplicate))
		})

		s.It("keeps the same name under two scopes as distinct registrations and refuses to run it by name", func(ctx *specs.Context) {
			a, b := scopeTenant(ctx, "a"), scopeTenant(ctx, "b")
			cfg := NewConfig(nil, WithTenantResolver(&stubTenantResolver{id: "x"}),
				WithProjection("p", scopeOpts(&a)), WithProjection("p", scopeOpts(&b)))
			ctx.Expect(cfg.projections).To(specs.HaveLen(2))
			_, err := cfg.resolveProjections()
			ctx.Expect(err).To(specs.MatchError(ErrProjectionNameAmbiguous))
		})

		s.It("a later change to the pointed-to variable does not change the registered scope (AC-R3-6)", func(ctx *specs.Context) {
			a, b := scopeTenant(ctx, "a"), scopeTenant(ctx, "b")
			target := a
			cfg := NewConfig(nil, WithTenantResolver(&stubTenantResolver{id: "x"}), WithProjection("p", scopeOpts(&target)))

			target = b

			reg, err := cfg.resolveProjections()
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(reg["p"].Scope.Equal(a)).To(specs.BeTrue())
			// the extension options never alias the caller's pointer
			ctx.Expect(reg["p"].Scope == &target).To(specs.BeFalse())
		})

		s.It("does not mutate the caller's Options", func(ctx *specs.Context) {
			opts := scopeOpts(nil)
			_, err := resolve(WithProjection("p", opts))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(opts.Scope == nil).To(specs.BeTrue())
			ctx.Expect(opts.Recovery == nil).To(specs.BeTrue())
		})
	})
}
