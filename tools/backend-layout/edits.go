package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Edit is one byte-range replacement in a file.
type Edit struct {
	Start int
	End   int
	New   string
}

// fileEdits maps an absolute file path to its accumulated edits.
type fileEdits map[string][]Edit

func (fe fileEdits) add(path string, e Edit) { fe[path] = append(fe[path], e) }

// analysis carries the result of computing all edits for a relocation plan.
type analysis struct {
	edits         fileEdits
	unresolved    map[string]bool // files needing selector renames but with no type info
	importEdits   int
	selectorEdits int
	packageEdits  int
}

// applyEdits applies non-overlapping, ascending edits to src.
func applyEdits(src []byte, edits []Edit) []byte {
	sort.Slice(edits, func(i, j int) bool { return edits[i].Start < edits[j].Start })
	var out []byte
	prev := 0
	for _, e := range edits {
		if e.Start < prev || e.End > len(src) || e.Start > e.End {
			continue // defensive: skip malformed edits rather than panic
		}
		out = append(out, src[prev:e.Start]...)
		out = append(out, e.New...)
		prev = e.End
	}
	out = append(out, src[prev:]...)
	return out
}

// compute builds the full edit set: a filesystem walk rewrites import paths and
// package clauses (syntactic, reliable), and a type-aware go/packages pass renames
// selector idents (so shadowed locals are never touched).
func compute(root, modulePath string, ms []Mapping) (*analysis, error) {
	byOldImport := map[string]Mapping{}
	byOldDir := map[string]Mapping{}
	for _, m := range ms {
		byOldImport[modulePath+"/"+m.OldDir] = m
		byOldDir[m.OldDir] = m
	}

	a := &analysis{
		edits:      fileEdits{},
		unresolved: map[string]bool{},
	}

	goFiles, err := listGoFiles(root)
	if err != nil {
		return nil, err
	}
	for _, abs := range goFiles {
		if err := syntacticEdits(root, modulePath, abs, byOldImport, byOldDir, a); err != nil {
			fmt.Fprintf(os.Stderr, "warn: skip %s: %v\n", abs, err)
		}
	}

	if err := typeAwareEdits(root, byOldImport, a); err != nil {
		return nil, err
	}

	// Best-effort syntactic fallback for files whose package failed to type-check.
	for abs := range a.unresolved {
		if err := fallbackSelectorEdits(abs, byOldImport, a); err != nil {
			fmt.Fprintf(os.Stderr, "warn: fallback %s: %v\n", abs, err)
		} else {
			delete(a.unresolved, abs)
		}
	}

	return a, nil
}

// skipDirs are top-level directories never part of the goopg module's Go sources.
var skipDirs = map[string]bool{
	".git": true, "postgres": true, "tmp": true, "third-party": true,
	"vendor": true, "venv": true, "HammerDB": true, "HammerDB-5.0": true,
	"wp": true, "serena": true,
}

// listGoFiles returns every .go file under root, excluding third-party trees and
// this tool's own directory.
func listGoFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip dot-directories (.git, .claude, .ralph, .github, ...) — they hold
			// infrastructure and, critically, foreign worktrees of this same module
			// under .claude/worktrees that must not be rewritten.
			if strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			if rel, rerr := filepath.Rel(root, p); rerr == nil && (rel == "tools/backend-layout" || strings.HasPrefix(rel, "tools/backend-layout/")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") {
			out = append(out, filepath.Clean(p))
		}
		return nil
	})
	return out, err
}

// syntacticEdits rewrites import paths and the package clause of a single file.
// These are unambiguous and do not require type information.
func syntacticEdits(root, modulePath, abs string, byOldImport, byOldDir map[string]Mapping, a *analysis) error {
	src, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, abs, src, parser.ParseComments)
	if err != nil {
		return err
	}
	tf := fset.File(f.Pos())

	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return err
	}
	dir := filepath.ToSlash(filepath.Dir(rel))

	if m, ok := byOldDir[dir]; ok {
		// A file in a moved directory declares either the package itself or its
		// external-test twin (package <old>_test); rename each to the matching new
		// name so the _test suffix is preserved.
		newName := ""
		switch f.Name.Name {
		case m.OldPkg:
			newName = m.NewPkg
		case m.OldPkg + "_test":
			newName = m.NewPkg + "_test"
		default:
			fmt.Fprintf(os.Stderr, "warn: %s: dir %s maps from package %s but declares %q (left unchanged)\n", abs, dir, m.OldPkg, f.Name.Name)
		}
		if newName != "" && newName != f.Name.Name {
			a.edits.add(abs, Edit{tf.Offset(f.Name.Pos()), tf.Offset(f.Name.End()), newName})
			a.packageEdits++
		}
	}

	for _, imp := range f.Imports {
		ip, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		m, ok := byOldImport[ip]
		if !ok {
			continue
		}
		newPath := modulePath + "/" + m.NewDir
		a.edits.add(abs, Edit{tf.Offset(imp.Path.Pos()), tf.Offset(imp.Path.End()), strconv.Quote(newPath)})
		a.importEdits++
	}
	return nil
}

// typeAwareEdits renames selector idents (oldpkg.X -> newpkg.X) using go/types so
// that a local variable that happens to share the old package name is never touched.
func typeAwareEdits(root string, byOldImport map[string]Mapping, a *analysis) error {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:   root,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return fmt.Errorf("packages.Load: %w", err)
	}

	done := map[string]bool{}
	for _, pkg := range pkgs {
		for _, perr := range pkg.Errors {
			fmt.Fprintf(os.Stderr, "warn: package %s: %v\n", pkg.PkgPath, perr)
		}
		info := pkg.TypesInfo
		for _, f := range pkg.Syntax {
			abs := filepath.Clean(pkg.Fset.Position(f.Pos()).Filename)
			if info == nil {
				if !done[abs] {
					a.unresolved[abs] = true
				}
				continue
			}
			delete(a.unresolved, abs)
			if done[abs] {
				continue
			}
			done[abs] = true
			renameSelectors(pkg.Fset, abs, f, info, byOldImport, a)
		}
	}
	return nil
}

// renameSelectors emits an edit for every selector pkg.Sel whose pkg ident resolves
// to a moved package's default (unaliased) name.
func renameSelectors(fset *token.FileSet, abs string, f *ast.File, info *types.Info, byOldImport map[string]Mapping, a *analysis) {
	tf := fset.File(f.Pos())

	// Record explicitly-aliased import paths: a redundant alias (import mvcc
	// "…/mvcc") has pn.Name() == old name, so the name guard alone would wrongly
	// rename its selectors; skip any aliased import regardless of its alias text.
	aliased := map[string]bool{}
	for _, imp := range f.Imports {
		if imp.Name != nil {
			if ip, err := strconv.Unquote(imp.Path.Value); err == nil {
				aliased[ip] = true
			}
		}
	}

	ast.Inspect(f, func(n ast.Node) bool {
		se, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := se.X.(*ast.Ident)
		if !ok {
			return true
		}
		obj, ok := info.Uses[x]
		if !ok {
			return true
		}
		pn, ok := obj.(*types.PkgName)
		if !ok {
			return true
		}
		importPath := pn.Imported().Path()
		m, ok := byOldImport[importPath]
		if !ok {
			return true
		}
		if aliased[importPath] || pn.Name() != m.OldPkg {
			return true // explicit alias (incl. redundant) — only the path is rewritten
		}
		if m.OldPkg != m.NewPkg {
			a.edits.add(abs, Edit{tf.Offset(x.Pos()), tf.Offset(x.End()), m.NewPkg})
			a.selectorEdits++
		}
		return true
	})
}

// fallbackSelectorEdits is a syntactic, best-effort selector rename used only for
// files whose package failed to type-check. It is less precise than the go/types
// pass (a shadowing local would be wrongly renamed), so those files are reported.
func fallbackSelectorEdits(abs string, byOldImport map[string]Mapping, a *analysis) error {
	src, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, abs, src, parser.ParseComments)
	if err != nil {
		return err
	}
	tf := fset.File(f.Pos())

	renames := map[string]Mapping{} // local name -> mapping, for unaliased imports
	for _, imp := range f.Imports {
		ip, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		m, ok := byOldImport[ip]
		if !ok || m.OldPkg == m.NewPkg {
			continue
		}
		if imp.Name == nil {
			renames[m.OldPkg] = m
		}
	}

	ast.Inspect(f, func(n ast.Node) bool {
		se, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := se.X.(*ast.Ident)
		if !ok {
			return true
		}
		if m, ok := renames[x.Name]; ok {
			a.edits.add(abs, Edit{tf.Offset(x.Pos()), tf.Offset(x.End()), m.NewPkg})
			a.selectorEdits++
		}
		return true
	})
	return nil
}
