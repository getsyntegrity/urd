package main

import (
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

type changesCase struct {
	name string
	raw  string
	want []Change
}

func TestParseChanges(t *testing.T) {
	specs.Describe(t, "impact parsing of git diff --name-status -M -z", func(s *specs.Spec) {
		specs.Table(s, []changesCase{
			{name: "an empty diff", raw: "", want: nil},
			{name: "added, modified and deleted", raw: "A\x00a.go\x00M\x00b.go\x00D\x00c.go\x00",
				want: []Change{{"A", "a.go"}, {"M", "b.go"}, {"D", "c.go"}}},
			{name: "a rename is a delete of the old path and an add of the new one", raw: "R087\x00old.go\x00new.go\x00",
				want: []Change{{"D", "old.go"}, {"A", "new.go"}}},
			{name: "a copy only adds the new path", raw: "C100\x00src.go\x00dst.go\x00",
				want: []Change{{"A", "dst.go"}}},
			{name: "a type change is a modification", raw: "T\x00link\x00", want: []Change{{"M", "link"}}},
			{name: "a path with spaces and non-ASCII characters is kept as is", raw: "M\x00dir with space/ñ.go\x00",
				want: []Change{{"M", "dir with space/ñ.go"}}},
		}, func(c changesCase) string { return c.name }, func(ctx *specs.Context, c changesCase) {
			got, err := ParseChanges(c.raw)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).ToEqual(c.want)
		})

		specs.Table(s, []changesCase{
			{name: "an unmerged path", raw: "U\x00a.go\x00"},
			{name: "an unknown status", raw: "X\x00a.go\x00"},
			{name: "a status without a path", raw: "M\x00"},
			{name: "a rename with one path", raw: "R100\x00a.go\x00"},
			{name: "an empty status", raw: "\x00a.go\x00"},
		}, func(c changesCase) string { return "rejects " + c.name }, func(ctx *specs.Context, c changesCase) {
			_, err := ParseChanges(c.raw)
			ctx.Expect(err == nil).To(specs.BeFalse())
		})
	})
}
