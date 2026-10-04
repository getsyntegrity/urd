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

package adapter_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

// optionalInterfaces are the optional SPI interfaces that may be
// type-asserted only inside their single accessor (ego-arch-004 design
// §D3, "one assertion per optional interface"). The check matches them by
// name, qualified or not.
var optionalInterfaces = []string{"Describer", "Starter", "Pinger", "FixedTenantResolver"}

// optionalMethods are the methods of those interfaces. An inline or local
// interface whose own methods are all among them is the same assertion in
// disguise (compose/goakt's former private pinger interface), and is
// reported too.
var optionalMethods = []string{"Describe", "Start", "Ping", "FixedTenant"}

// allowedAssertionSites are the only functions in production code that may
// assert an optional interface, as "<file relative to the repository
// root>:<function>".
var allowedAssertionSites = []string{
	"port/adapter/adapter.go:Describe",
	"port/adapter/adapter.go:PingerOf",
	"port/adapter/adapter.go:StarterOf",
	"tenancy/resolver.go:AsFixedTenantResolver",
}

// TestArchitectureOptionalInterfacesAreAssertedOnlyInTheirAccessors scans every
// production Go file of the repository, nested modules included, and
// requires that Describer, Starter, Pinger and FixedTenantResolver are
// type-asserted (in a type assertion or a type switch) only inside
// adapter.Describe, adapter.StarterOf, adapter.PingerOf and
// tenancy.AsFixedTenantResolver. The set of sites found must equal the
// allowed set exactly, so the scan also proves it sees the four accessors.
func TestArchitectureOptionalInterfacesAreAssertedOnlyInTheirAccessors(t *testing.T) {
	specs.Describe(t, "type assertions of the optional adapter interfaces in production code", func(s *specs.Spec) {
		var sites []string
		s.BeforeEach(func(ctx *specs.Context) {
			repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
			ctx.Expect(err).To(specs.BeNil())
			sites = nil
			var scanned int
			walkErr := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					if path != repoRoot && skipDir(entry.Name()) {
						return filepath.SkipDir
					}
					return nil
				}
				if !isProductionGoFile(path) {
					return nil
				}
				src, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(repoRoot, path)
				if err != nil {
					return err
				}
				found, err := assertionSites(filepath.ToSlash(rel), src)
				if err != nil {
					return err
				}
				scanned++
				sites = append(sites, found...)
				return nil
			})
			ctx.Expect(walkErr).To(specs.BeNil())
			// A scan that found no Go source proves nothing.
			ctx.Expect(scanned).To(specs.BeGreaterThan(0))

			slices.Sort(sites)
			sites = slices.Compact(sites)
		})

		s.It("appear only inside the four accessors", func(ctx *specs.Context) {
			// A site outside the allowed set is named in the failure. Fix it by calling
			// adapter.Describe, StarterOf, PingerOf or tenancy.AsFixedTenantResolver
			// instead (openspec/changes/ego-arch-004/design.md section D3).
			var unexpected []string
			for _, site := range sites {
				if !slices.Contains(allowedAssertionSites, site) {
					unexpected = append(unexpected, site)
				}
			}
			ctx.Expect(unexpected).To(specs.BeEmpty())
		})

		s.It("are all found by the scan, so the scan itself is proven to see them", func(ctx *specs.Context) {
			// An accessor the scan did not find either moved or the scan is broken.
			var missing []string
			for _, site := range allowedAssertionSites {
				if !slices.Contains(sites, site) {
					missing = append(missing, site)
				}
			}
			ctx.Expect(missing).To(specs.BeEmpty())
		})
	})
}

// TestArchitectureNoPrivateCopiesOfOptionalInterfaces requires that no production file
// outside port/adapter declares its own interface made only of the
// optional methods (for example a private `pinger`): a value is then used
// through it without any accessor, which is the scattered-assertion
// problem in another form. Only port/adapter/adapter.go declares such
// interfaces (Describer, Starter, Pinger).
func TestArchitectureNoPrivateCopiesOfOptionalInterfaces(t *testing.T) {
	specs.Describe(t, "interfaces made only of the optional methods", func(s *specs.Spec) {
		s.It("are declared only by the adapter file, never privately elsewhere", func(ctx *specs.Context) {
			repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
			ctx.Expect(err).To(specs.BeNil())
			var owners []string
			walkErr := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					if path != repoRoot && skipDir(entry.Name()) {
						return filepath.SkipDir
					}
					return nil
				}
				if !isProductionGoFile(path) {
					return nil
				}
				file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
				if err != nil {
					return err
				}
				if len(localOptionalInterfaces(file)) == 0 {
					return nil
				}
				rel, err := filepath.Rel(repoRoot, path)
				if err != nil {
					return err
				}
				owners = append(owners, filepath.ToSlash(rel))
				return nil
			})
			ctx.Expect(walkErr).To(specs.BeNil())
			// Any other owner is a private copy: use adapter.Describer, Starter or
			// Pinger through their accessors instead.
			ctx.Expect(owners).To(specs.Equal([]string{"port/adapter/adapter.go"}))
		})
	})
}

// TestAssertionSitesNegativeControl proves the scan reports every form of
// the forbidden assertion and nothing else, on a synthetic source.
func TestAssertionSitesNegativeControl(t *testing.T) {
	src := `package sample

import (
	"context"
	"fmt"
)

type pinger interface{ Ping(context.Context) error }

type alias = adapter.Pinger

type bareAlias = Starter

type wrapper interface{ adapter.Describer }

func qualified(v any)  { _, _ = v.(adapter.Starter) }
func embedded(v any)   { _, _ = v.(interface{ adapter.Pinger }) }
func aliased(v any)    { _, _ = v.(alias) }
func bareAliased(v any) { _, _ = v.(bareAlias) }
func wrapped(v any)    { _, _ = v.(wrapper) }
func bare(v any)       { _, _ = v.(Describer) }
func inline(v any)     { _, _ = v.(interface{ Ping(context.Context) error }) }
func local(v any)      { _, _ = v.(pinger) }
func switched(v any) {
	switch v.(type) {
	case fmt.Stringer:
	case tenancy.FixedTenantResolver:
	}
}
func unrelated(v any) {
	_, _ = v.(fmt.Stringer)
	_, _ = v.(interface{ Close() error })
	_, _ = v.(interface {
		Ping(context.Context) error
		Close() error
	})
	switch x := v.(type) {
	case error:
		_ = x
	}
}
`
	specs.Describe(t, "assertionSites finds every optional-interface assertion in a source file", func(s *specs.Spec) {
		s.It("reports each assertion site and ignores unrelated assertions", func(ctx *specs.Context) {
			got, err := assertionSites("sample/sample.go", []byte(src))
			ctx.Expect(err).To(specs.BeNil())
			want := []string{
				"sample/sample.go:aliased",
				"sample/sample.go:bare",
				"sample/sample.go:bareAliased",
				"sample/sample.go:embedded",
				"sample/sample.go:inline",
				"sample/sample.go:local",
				"sample/sample.go:qualified",
				"sample/sample.go:switched",
				"sample/sample.go:wrapped",
			}
			ctx.Expect(got).To(specs.ContainTheSameElementsAs(want))
		})
	})
}

// skipDir reports whether a directory holds no first-party production Go
// code: hidden tooling directories (.git, .codegraph, .claude, …), vendored
// code, test data and the openspec documents.
func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "openspec" || name == "node_modules"
}

// isProductionGoFile reports whether path is hand-written production Go:
// not a test file and not generated protobuf code.
func isProductionGoFile(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && !strings.HasSuffix(path, ".pb.go")
}

// assertionSites parses one file and returns "<rel>:<function>" for every
// type assertion or type-switch case on an optional interface.
func assertionSites(rel string, src []byte) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	local := localOptionalInterfaces(file)
	var sites []string
	for _, decl := range file.Decls {
		scope := "<package scope>"
		if fn, ok := decl.(*ast.FuncDecl); ok {
			scope = fn.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			var types []ast.Expr
			switch node := n.(type) {
			case *ast.TypeAssertExpr:
				if node.Type != nil { // nil in a type switch guard; its cases are handled below
					types = append(types, node.Type)
				}
			case *ast.TypeSwitchStmt:
				for _, stmt := range node.Body.List {
					types = append(types, stmt.(*ast.CaseClause).List...)
				}
			}
			for _, typ := range types {
				if isOptionalInterface(typ, local) {
					sites = append(sites, rel+":"+scope)
				}
			}
			return true
		})
	}
	return sites, nil
}

// isOptionalInterface reports whether typ names an optional interface
// (qualified or not), names a local type that is one, or is an inline
// interface made only of optional methods and embedded optional
// interfaces.
func isOptionalInterface(typ ast.Expr, local map[string]bool) bool {
	switch t := typ.(type) {
	case *ast.Ident:
		return slices.Contains(optionalInterfaces, t.Name) || local[t.Name]
	case *ast.SelectorExpr:
		return slices.Contains(optionalInterfaces, t.Sel.Name)
	case *ast.InterfaceType:
		return onlyOptionalMethods(t, local)
	case *ast.ParenExpr:
		return isOptionalInterface(t.X, local)
	default:
		return false
	}
}

// localOptionalInterfaces returns the file's type declarations that are an
// optional interface under another name: an interface made only of
// optional methods and embedded optional interfaces, or a type or alias
// whose type names one (`type P = adapter.Pinger`). It repeats until
// nothing changes, so a local type built on another local one is found
// too.
func localOptionalInterfaces(file *ast.File) map[string]bool {
	var specs []*ast.TypeSpec
	ast.Inspect(file, func(n ast.Node) bool {
		if spec, ok := n.(*ast.TypeSpec); ok {
			specs = append(specs, spec)
		}
		return true
	})
	out := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, spec := range specs {
			if !out[spec.Name.Name] && isOptionalInterface(spec.Type, out) {
				out[spec.Name.Name] = true
				changed = true
			}
		}
	}
	return out
}

// onlyOptionalMethods reports whether iface has at least one element and
// every element is an optional method or an embedded optional interface.
func onlyOptionalMethods(iface *ast.InterfaceType, local map[string]bool) bool {
	if iface.Methods == nil || len(iface.Methods.List) == 0 {
		return false
	}
	for _, field := range iface.Methods.List {
		if len(field.Names) == 0 {
			// An embedded interface (or a type constraint): optional only
			// when it names an optional interface.
			if !isOptionalInterface(field.Type, local) {
				return false
			}
			continue
		}
		for _, name := range field.Names {
			if !slices.Contains(optionalMethods, name.Name) {
				return false
			}
		}
	}
	return true
}
