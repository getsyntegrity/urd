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
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

const (
	rootModule  = "github.com/getsyntegrity/urd"
	adapterPath = rootModule + "/port/adapter"
)

// contractPorts lists, for each contract package that owns a composition
// slot, the port-name constants its port.go must declare (ego-arch-004
// design §D3): constant name -> the interface it names. The directory is
// relative to the repository root.
var contractPorts = map[string]map[string]string{
	"port/publishing": {"PortEventPublisher": "EventPublisher", "PortStatePublisher": "StatePublisher"},
	"persistence":     {"PortEventsStore": "EventsStore", "PortStateStore": "StateStore", "PortSnapshotStore": "SnapshotStore"},
	"offsetstore":     {"PortOffsetStore": "OffsetStore"},
	"tenancy":         {"PortTenantResolver": "TenantResolver"},
	"encryption":      {"PortEncryptor": "Encryptor"},
}

// TestArchitectureAdapterDependsOnlyOnStdlib walks the resolved import graph of
// port/adapter with a real `go list -deps` subprocess, so transitive
// dependencies are checked too. The allowlist of non-standard-library
// packages is empty (ego-arch-004 design §D1).
func TestArchitectureAdapterDependsOnlyOnStdlib(t *testing.T) {
	specs.Describe(t, "the import graph of port/adapter", func(s *specs.Spec) {
		s.It("holds only the standard library besides the package itself", func(ctx *specs.Context) {
			deps := goList(ctx, "list", "-deps", ".")
			// The guard first: an empty or truncated graph would prove nothing.
			ctx.Expect(deps).To(specs.ContainAllOf(adapterPath, "context"))
			ctx.Expect(deps).To(specs.EveryElement(specs.Satisfy(
				"port/adapter itself or a standard library package: it may import only the standard library "+
					"(openspec/changes/ego-arch-004/design.md §D1)",
				func(dep any) bool { return dep == adapterPath || isStdlib(dep.(string)) })))
		})
	})
}

// Spec scenario "moving port/publishing stays cycle-free": none of the five
// contract packages imports port/adapter, directly or transitively.
func TestArchitectureContractPackagesDoNotImportAdapter(t *testing.T) {
	pkgs := make([]string, 0, len(contractPorts))
	for dir := range contractPorts {
		pkgs = append(pkgs, rootModule+"/"+dir)
	}
	slices.Sort(pkgs)

	specs.Describe(t, "the contract packages that own a composition slot", func(s *specs.Spec) {
		specs.Table(s, pkgs, func(pkg string) string { return strings.TrimPrefix(pkg, rootModule+"/") }, func(ctx *specs.Context, pkg string) {
			deps := goList(ctx, "list", "-deps", pkg)
			ctx.Expect(deps).To(specs.Contain(pkg)) // the package itself: otherwise the next check proves nothing
			// Contract packages declare port names as untyped constants so they never need port/adapter
			// (openspec/changes/ego-arch-004/design.md §D3).
			ctx.Expect(deps).To(specs.NoElement(specs.Equal(adapterPath)))
		})
	})
}

// portConstants is what a contract package's port.go declares: its package
// name, the constants with a value that is a string literal (name -> value),
// the constants that carry a type, and those without a string-literal value.
type portConstants struct {
	pkgName    string
	values     map[string]string
	typed      []string
	notLiteral []string
}

// TestArchitecturePortNameConstantsAreUntyped reads each contract package's port.go and
// checks that it declares exactly the expected port-name constants, that
// each is an untyped string constant (a typed adapter.Port constant would
// import port/adapter), that its value is "<package>.<Interface>", and
// that the named interface exists in the package.
func TestArchitecturePortNameConstantsAreUntyped(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	dirs := make([]string, 0, len(contractPorts))
	for dir := range contractPorts {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)

	specs.Describe(t, "the port-name constants of each contract package", func(s *specs.Spec) {
		specs.Table(s, dirs, func(dir string) string { return dir }, func(ctx *specs.Context, dir string) {
			want := contractPorts[dir]
			pkgDir := filepath.Join(repoRoot, filepath.FromSlash(dir))
			got := portNameConstants(ctx, filepath.Join(pkgDir, "port.go"))

			ctx.Expect(got.typed).To(specs.BeEmpty())      // a typed constant would import port/adapter
			ctx.Expect(got.notLiteral).To(specs.BeEmpty()) // every constant needs an explicit string literal value

			wantValues := map[string]string{}
			ifaces := make([]any, 0, len(want))
			for constName, iface := range want {
				wantValues[constName] = got.pkgName + "." + iface
				ifaces = append(ifaces, iface)
			}
			ctx.Expect(got.values).To(specs.Equal(wantValues))
			ctx.Expect(interfacesIn(ctx, pkgDir)).To(specs.ContainAllOf(ifaces...))
		})
	})
}

// portNameConstants parses the port.go at path and collects its constants.
func portNameConstants(ctx *specs.Context, path string) portConstants {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	ctx.Expect(wrapErr("parsing "+path, err)).To(specs.BeNil())

	out := portConstants{pkgName: file.Name.Name, values: map[string]string{}}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if vs.Type != nil {
				out.typed = append(out.typed, vs.Names[0].Name)
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					out.notLiteral = append(out.notLiteral, name.Name)
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					out.notLiteral = append(out.notLiteral, name.Name)
					continue
				}
				value, _ := strconv.Unquote(lit.Value)
				out.values[name.Name] = value
			}
		}
	}
	return out
}

// interfacesIn returns the sorted names of the interface types declared in
// the non-test files of dir.
func interfacesIn(ctx *specs.Context, dir string) []string {
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	ctx.Expect(err).To(specs.BeNil())
	var out []string
	fset := token.NewFileSet()
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		ctx.Expect(wrapErr("parsing "+path, err)).To(specs.BeNil())
		ast.Inspect(file, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				if _, ok := ts.Type.(*ast.InterfaceType); ok {
					out = append(out, ts.Name.Name)
				}
			}
			return true
		})
	}
	slices.Sort(out)
	return out
}

// isStdlib reports whether an import path belongs to the standard library:
// its first path element has no dot.
func isStdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

// wrapErr adds context to err and keeps a nil error nil, so the result can go
// straight into a BeNil expectation.
func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}

// goList runs the go command with args and returns the fields of its output.
func goList(ctx *specs.Context, args ...string) []string {
	goBin, err := exec.LookPath("go")
	ctx.Expect(wrapErr("go toolchain not found on PATH", err)).To(specs.BeNil())
	cmd := exec.Command(goBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	var runErr error
	if err := cmd.Run(); err != nil {
		runErr = fmt.Errorf("go %s failed: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	ctx.Expect(runErr).To(specs.BeNil())
	return strings.Fields(stdout.String())
}
