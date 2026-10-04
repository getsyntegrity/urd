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

package actoridentity

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

// goaktName is GoAkt's rule for an actor name (internal/address Validate): a
// letter or digit first, then letters, digits, '-', '_' and '.'.
var goaktName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-_.]*$`)

// pairs holds the (tenant, id) values the encoding must keep apart. Several
// are chosen to defeat a naive "tenant + separator + id": values that hold the
// separator, that move the boundary between tenant and id, and tenants that
// are not valid in a GoAkt name at all.
var pairs = [][2]string{
	{"acme", "order-1"},
	{"globex", "order-1"},
	{"acme", "order-2"},
	{"a", "b.c"},
	{"a.b", "c"},
	{"a.b.c", "d"},
	{"a", "b.c.d"},
	{"t", "t.t.t"},
	{"t.t", "t"},
	{"acme", "1"},
	{"acme1", ""},
	{"Acme", "order-1"},
	{"acme corp", "order-1"},
	{"acme/eu", "order-1"},
	{"acme:eu", "order-1"},
	{"tenant-ü-日本", "order-1"},
	{"-", "x"},
	{"_", "x"},
	{"..", "x"},
	{"GoAkt", "x"},
	{"a", "GoAkt"},
}

func TestQualifyKeepsDifferentPairsApart(t *testing.T) {
	specs.Describe(t, "Qualify", func(s *specs.Spec) {
		s.It("never gives two different (tenant, id) pairs the same name", func(ctx *specs.Context) {
			seen := map[string][2]string{}
			for _, pair := range pairs {
				if pair[1] == "" {
					continue
				}
				name, err := Qualify(pair[0], pair[1])
				ctx.Expect(err).To(specs.BeNil())
				_, dup := seen[name]
				ctx.Expect(dup).To(specs.BeFalse())
				seen[name] = pair
			}
			ctx.Expect(len(seen)).To(specs.Equal(len(pairs) - 1))
		})

		s.It("keeps the boundary a plain separator join would lose", func(ctx *specs.Context) {
			// "a.b" + "." + "c" and "a" + "." + "b.c" are the same string.
			left, err := Qualify("a.b", "c")
			ctx.Expect(err).To(specs.BeNil())
			right, err := Qualify("a", "b.c")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(left).To(specs.Not(specs.Equal(right)))
		})

		s.It("produces names GoAkt accepts, for tenants GoAkt would reject", func(ctx *specs.Context) {
			for _, pair := range pairs {
				if pair[1] == "" {
					continue
				}
				name, err := Qualify(pair[0], pair[1])
				ctx.Expect(err).To(specs.BeNil())
				ctx.Expect(goaktName.MatchString(name)).To(specs.BeTrue())
				ctx.Expect(strings.HasPrefix(name, "GoAkt")).To(specs.BeFalse())
			}
		})

		s.It("is deterministic", func(ctx *specs.Context) {
			first, err := Qualify("acme", "order-1")
			ctx.Expect(err).To(specs.BeNil())
			second, err := Qualify("acme", "order-1")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(first).To(specs.Equal(second))
		})

		s.It("rejects an empty tenant, an empty id and a name over GoAkt's limit", func(ctx *specs.Context) {
			_, err := Qualify("", "order-1")
			ctx.Expect(err).To(specs.MatchError(ErrEmptyTenant))

			_, err = Qualify("acme", "")
			ctx.Expect(err).To(specs.MatchError(ErrEmptyID))

			_, err = Qualify("acme", strings.Repeat("x", maxNameLen))
			ctx.Expect(err).To(specs.MatchError(ErrNameTooLong))

			// the longest tenant (128 bytes) still leaves room for a usable id
			longest := strings.Repeat("é", 64)
			name, err := Qualify(longest, strings.Repeat("x", 60))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(len(name) <= maxNameLen).To(specs.BeTrue())
		})
	})
}

func TestParseReversesQualify(t *testing.T) {
	specs.Describe(t, "Parse", func(s *specs.Spec) {
		s.It("recovers the exact tenant and id of every pair", func(ctx *specs.Context) {
			for _, pair := range pairs {
				if pair[1] == "" {
					continue
				}
				name, err := Qualify(pair[0], pair[1])
				ctx.Expect(err).To(specs.BeNil())

				tenantID, id, ok := Parse(name)
				ctx.Expect(ok).To(specs.BeTrue())
				ctx.Expect(tenantID).To(specs.Equal(pair[0]))
				ctx.Expect(id).To(specs.Equal(pair[1]))
			}
		})

		s.It("does not read an unqualified name as a qualified one", func(ctx *specs.Context) {
			for _, name := range []string{"order-1", "t", "t.", "t.x", "t..x", "t.x.", "t.!!.x", "T.YWNtZQ.x"} {
				_, _, ok := Parse(name)
				ctx.Expect(ok).To(specs.BeFalse())
			}
		})

		s.It("is injective over a generated grid of awkward values", func(ctx *specs.Context) {
			values := []string{"a", "b", "a.b", "b.a", "t", "t.t", ".", "-", "_", "a-b", "a_b", "ü", "A"}
			seen := map[string]string{}
			for _, tenantID := range values {
				for _, id := range values {
					if id == "." || id == "-" || id == "_" {
						// not valid as the first character of an id, and never a
						// distinguishing case: the id is not rewritten
						continue
					}
					name, err := Qualify(tenantID, id)
					ctx.Expect(err).To(specs.BeNil())
					key := fmt.Sprintf("%q|%q", tenantID, id)
					_, dup := seen[name]
					ctx.Expect(dup).To(specs.BeFalse())
					seen[name] = key

					gotTenant, gotID, ok := Parse(name)
					ctx.Expect(ok).To(specs.BeTrue())
					ctx.Expect(gotTenant).To(specs.Equal(tenantID))
					ctx.Expect(gotID).To(specs.Equal(id))
				}
			}
		})
	})
}

func TestVerifyBindsANameToItsTenantAndID(t *testing.T) {
	specs.Describe(t, "Verify", func(s *specs.Spec) {
		s.It("accepts the name its tenant and id derive", func(ctx *specs.Context) {
			name, err := Qualify("acme", "order-1")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(Verify(name, "acme", "order-1")).To(specs.BeNil())
		})

		s.It("refuses another tenant's name, another id's name and an unqualified name", func(ctx *specs.Context) {
			acme, err := Qualify("acme", "order-1")
			ctx.Expect(err).To(specs.BeNil())

			ctx.Expect(Verify(acme, "globex", "order-1")).To(specs.MatchError(ErrMismatch))
			ctx.Expect(Verify(acme, "acme", "order-2")).To(specs.MatchError(ErrMismatch))
			ctx.Expect(Verify("order-1", "acme", "order-1")).To(specs.MatchError(ErrMismatch))
			ctx.Expect(Verify(acme, "", "order-1")).To(specs.MatchError(ErrMismatch))
		})
	})
}
