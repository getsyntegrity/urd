// Command impact selects what a pull request has to run. It reads the files a change touched and the import graph
// of every module in the repository, and decides which modules and which packages of the root module are affected,
// directly or through a consumer, and which CI lanes must run (issue #209). The plan it prints is also what the
// ci-ok gate compares the job results against, so a required job that did not run fails the gate.
//
// This file builds the graph. It does not call `go list`: it parses the imports of every Go file, so it needs no
// network and no module download, and a test can feed it an in-memory file system. Build constraints are ignored,
// which makes the graph a superset of what one platform compiles. A superset can select a package that did not
// need to run; it can never miss one that did.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Module is one go.mod of the repository.
type Module struct {
	Path string // module path, from the module line of go.mod
	Dir  string // slash-separated, relative to the repository root; "." for the root module
}

// Package is one directory with Go files inside a module. The three import sets are kept apart because they
// matter differently: a package is rebuilt when anything it imports changes, but the tests of one package never
// compile the test files of another.
type Package struct {
	ImportPath   string
	Dir          string // relative to the repository root
	Module       string // Module.Path
	Imports      []string
	TestImports  []string // imports of the _test.go files that are in the package itself
	XTestImports []string // imports of the _test.go files of the external test package (package x_test)
}

// Graph is every module and package of the repository, and the lookups the selector needs.
type Graph struct {
	Modules  []Module
	Packages map[string]*Package // by import path
	byDir    map[string]*Package
}

// skippedDir reports directories the go tool never treats as part of a package tree.
func skippedDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" || name == "node_modules"
}

// LoadGraph discovers every module from its go.mod and reads the imports of every Go file under it.
// It fails, instead of returning a partial graph, when a go.mod or a Go file cannot be read.
func LoadGraph(fsys fs.FS) (*Graph, error) {
	mods, err := discoverModules(fsys)
	if err != nil {
		return nil, err
	}
	g := &Graph{Modules: mods, Packages: map[string]*Package{}, byDir: map[string]*Package{}}
	moduleDirs := map[string]bool{}
	for _, m := range mods {
		moduleDirs[m.Dir] = true
	}
	for _, m := range mods {
		if err := g.loadModule(fsys, m, moduleDirs); err != nil {
			return nil, err
		}
	}
	for _, p := range g.Packages {
		sort.Strings(p.Imports)
		sort.Strings(p.TestImports)
		sort.Strings(p.XTestImports)
	}
	return g, nil
}

// discoverModules finds every go.mod, skipping the trees the go tool skips, and reads its module path.
func discoverModules(fsys fs.FS) ([]Module, error) {
	var mods []Module
	seen := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && skippedDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "go.mod" {
			return nil
		}
		modPath, err := readModulePath(fsys, p)
		if err != nil {
			return err
		}
		dir := path.Dir(p)
		if other, dup := seen[modPath]; dup {
			return fmt.Errorf("module %q is declared by both %s and %s", modPath, other, p)
		}
		seen[modPath] = p
		mods = append(mods, Module{Path: modPath, Dir: dir})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover modules: %w", err)
	}
	// The root module first, then the rest by directory, whatever order the walk found them in.
	sort.Slice(mods, func(i, j int) bool {
		if (mods[i].Dir == ".") != (mods[j].Dir == ".") {
			return mods[i].Dir == "."
		}
		return mods[i].Dir < mods[j].Dir
	})
	if len(mods) == 0 || mods[0].Dir != "." {
		return nil, errors.New("discover modules: there is no go.mod at the repository root")
	}
	return mods, nil
}

func readModulePath(fsys fs.FS, name string) (string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"`), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s has no module line", name)
}

// loadModule walks one module tree, leaving out the nested modules, which are loaded on their own.
func (g *Graph) loadModule(fsys fs.FS, m Module, moduleDirs map[string]bool) error {
	return fs.WalkDir(fsys, m.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != m.Dir && (skippedDir(d.Name()) || moduleDirs[p]) {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			return nil
		}
		return g.addFile(fsys, m, p)
	})
}

func (g *Graph) addFile(fsys fs.FS, m Module, file string) error {
	src, err := fs.ReadFile(fsys, file)
	if err != nil {
		return err
	}
	f, err := parser.ParseFile(token.NewFileSet(), file, src, parser.ImportsOnly)
	if err != nil {
		return fmt.Errorf("parse %s: %w", file, err)
	}
	dir := path.Dir(file)
	importPath := m.Path
	if dir != m.Dir {
		rel := dir
		if m.Dir != "." {
			rel = strings.TrimPrefix(dir, m.Dir+"/")
		}
		importPath = m.Path + "/" + rel
	}
	pkg := g.Packages[importPath]
	if pkg == nil {
		pkg = &Package{ImportPath: importPath, Dir: dir, Module: m.Path}
		g.Packages[importPath] = pkg
		g.byDir[dir] = pkg
	}
	isTest := strings.HasSuffix(file, "_test.go")
	for _, spec := range f.Imports {
		imp := strings.Trim(spec.Path.Value, "\"`")
		switch {
		case !isTest:
			pkg.Imports = appendUnique(pkg.Imports, imp)
		case strings.HasSuffix(f.Name.Name, "_test"):
			pkg.XTestImports = appendUnique(pkg.XTestImports, imp)
		default:
			pkg.TestImports = appendUnique(pkg.TestImports, imp)
		}
	}
	return nil
}

func appendUnique(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// ModuleOf returns the module that owns a repository path: the one with the longest directory prefix.
func (g *Graph) ModuleOf(file string) Module {
	best := g.Modules[0]
	for _, m := range g.Modules {
		if m.Dir == "." {
			continue
		}
		if (file == m.Dir || strings.HasPrefix(file, m.Dir+"/")) && len(m.Dir) > len(best.Dir) {
			best = m
		}
	}
	return best
}

// PackageOf returns the package a file belongs to: the package of its directory or, for a file in a directory
// that is not a package (testdata, an embedded schema folder), of the nearest ancestor that is one, without
// leaving the owning module. It returns nil when no package owns the file.
func (g *Graph) PackageOf(file string) *Package {
	mod := g.ModuleOf(file)
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if p, ok := g.byDir[dir]; ok && p.Module == mod.Path {
			return p
		}
		if dir == mod.Dir || dir == "." || dir == "/" {
			return nil
		}
	}
}

// PackagesOf lists the packages of a module, sorted by import path.
func (g *Graph) PackagesOf(modulePath string) []*Package {
	var out []*Package
	for _, p := range g.Packages {
		if p.Module == modulePath {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ImportPath < out[j].ImportPath })
	return out
}
