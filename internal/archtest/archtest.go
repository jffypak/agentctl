// Package archtest enforces ARCHITECTURE.md §Code conventions 6 (import
// rules, the exec/kill/paste vocabulary, and non-destructive gitx) by parsing
// the module's Go sources. A rule for a package that does not exist yet
// passes vacuously. This package's own sources (which spell out the
// forbidden vocabulary) and testdata/vendor/dot directories are not scanned.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// selfDir is excluded from scanning.
const selfDir = "internal/archtest"

// File is one parsed Go source file.
type File struct {
	Rel    string // slash path relative to the module root
	Dir    string // slash directory relative to the module root ("" at the root)
	Pkg    string // import path of Dir
	IsTest bool
	Syntax *ast.File
	Fset   *token.FileSet
}

// Module is a parsed module tree.
type Module struct {
	Path  string // module path
	Files []File
}

// Violation is one rule failure.
type Violation struct {
	Rule string
	Pos  string // file:line
	Msg  string
}

func (v Violation) String() string { return v.Rule + ": " + v.Pos + ": " + v.Msg }

// Rule is one architecture rule.
type Rule struct {
	ID    string
	Doc   string
	Check func(m *Module) []Violation
}

// Load parses every .go file under root.
func Load(root, modulePath string) (*Module, error) {
	m := &Module{Path: modulePath}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if rel != "." && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" || rel == selfDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := parseFile(fset, modulePath, rel, src)
		if err != nil {
			return err
		}
		m.Files = append(m.Files, f)
		return nil
	})
	return m, err
}

// LoadSources builds a module from in-memory sources (rule self-tests).
func LoadSources(modulePath string, files map[string]string) (*Module, error) {
	m := &Module{Path: modulePath}
	fset := token.NewFileSet()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, rel := range names {
		f, err := parseFile(fset, modulePath, rel, []byte(files[rel]))
		if err != nil {
			return nil, err
		}
		m.Files = append(m.Files, f)
	}
	return m, nil
}

func parseFile(fset *token.FileSet, modulePath, rel string, src []byte) (File, error) {
	syn, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		return File{}, fmt.Errorf("archtest: parse %s: %w", rel, err)
	}
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	pkg := modulePath
	if dir != "" {
		pkg = modulePath + "/" + dir
	}
	return File{Rel: rel, Dir: dir, Pkg: pkg, IsTest: strings.HasSuffix(rel, "_test.go"), Syntax: syn, Fset: fset}, nil
}

// Check runs rules over m.
func Check(m *Module, rules []Rule) []Violation {
	var out []Violation
	for _, r := range rules {
		out = append(out, r.Check(m)...)
	}
	return out
}

// --- helpers -------------------------------------------------------------------

// in reports whether the file lives in dir or one of its subdirectories.
func (f File) in(dir string) bool { return f.Dir == dir || strings.HasPrefix(f.Dir, dir+"/") }

func (f File) pos(p token.Pos) string {
	return fmt.Sprintf("%s:%d", f.Rel, f.Fset.Position(p).Line)
}

// imports returns (import path, position) pairs.
func (f File) imports() []struct {
	path string
	pos  token.Pos
} {
	var out []struct {
		path string
		pos  token.Pos
	}
	for _, imp := range f.Syntax.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		out = append(out, struct {
			path string
			pos  token.Pos
		}{p, imp.Pos()})
	}
	return out
}

// stringLits calls fn for every string literal (unquoted) in the file.
func (f File) stringLits(fn func(s string, pos token.Pos)) {
	ast.Inspect(f.Syntax, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				fn(s, lit.Pos())
			}
		}
		return true
	})
}

// importName is the local name under which the file imports importPath ("" if not imported).
func (f File) importName(importPath string) string {
	for _, imp := range f.Syntax.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != importPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return path.Base(importPath)
	}
	return ""
}

func hasPathPrefix(p, prefix string) bool { return p == prefix || strings.HasPrefix(p, prefix+"/") }

func tokens(s string) []string { return strings.Fields(s) }
