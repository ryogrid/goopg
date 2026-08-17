package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// apply performs the relocation: write all file edits, record the move log, then
// move directories on the filesystem (dependency-ordered so a nested target's parent
// exists before the nested package is moved).
func apply(a *analysis, root string, ms []Mapping) error {
	for abs, edits := range a.edits {
		if len(edits) == 0 {
			continue
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			return fmt.Errorf("read %s: %w", abs, err)
		}
		if err := os.WriteFile(abs, applyEdits(src, edits), 0644); err != nil {
			return fmt.Errorf("write %s: %w", abs, err)
		}
	}
	fmt.Printf("rewrote %d files (%d import paths, %d selectors, %d package clauses)\n",
		len(a.edits), a.importEdits, a.selectorEdits, a.packageEdits)

	if err := writeMoveLog(root, ms); err != nil {
		return err
	}

	order, err := moveDirs(root, ms)
	if err != nil {
		return err
	}
	fmt.Printf("moved %d directories:\n", len(order))
	for _, o := range order {
		fmt.Printf("  %s\n", o)
	}
	return nil
}

// printPlan reports what apply would do, without changing anything.
func printPlan(a *analysis, ms []Mapping) {
	fmt.Printf("plan: %d package relocations\n", len(ms))
	for _, m := range ms {
		fmt.Printf("  %-32s -> %s  (pkg %s -> %s)\n", m.OldDir, m.NewDir, m.OldPkg, m.NewPkg)
	}
	fmt.Printf("\nfile edits: %d files, %d import paths, %d selectors, %d package clauses\n",
		len(a.edits), a.importEdits, a.selectorEdits, a.packageEdits)
	if len(a.unresolved) > 0 {
		fmt.Printf("\nWARNING: %d files needed selector renames but had no type info (syntactic fallback used):\n", len(a.unresolved))
		for f := range a.unresolved {
			fmt.Printf("  %s\n", f)
		}
	}
}

// writeMoveLog writes moves.tsv: a per-file TSV of every old path -> new path.
func writeMoveLog(root string, ms []Mapping) error {
	var b strings.Builder
	b.WriteString("# old_path\tnew_path\n")
	for _, m := range ms {
		oldAbs := filepath.Join(root, filepath.FromSlash(m.OldDir))
		err := filepath.WalkDir(oldAbs, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			newRel := path.Join(m.NewDir, filepath.ToSlash(rel[len(filepath.ToSlash(m.OldDir)):]))
			b.WriteString(filepath.ToSlash(rel))
			b.WriteString("\t")
			b.WriteString(newRel)
			b.WriteString("\n")
			return nil
		})
		if err != nil {
			return fmt.Errorf("walk %s: %w", m.OldDir, err)
		}
	}
	return os.WriteFile(filepath.Join(root, "tools", "backend-layout", "moves.tsv"), []byte(b.String()), 0644)
}

// moveDirs relocates directories with os.Rename (filesystem-only; git staging and
// committing is done separately with explicit pathspecs). Moves are ordered so that a
// nested package's parent directory exists first.
func moveDirs(root string, ms []Mapping) ([]string, error) {
	destSet := map[string]bool{}
	for _, m := range ms {
		destSet[m.NewDir] = true
	}

	// Pre-create parent dirs that are not themselves move destinations.
	for _, m := range ms {
		parent := path.Dir(m.NewDir)
		if !destSet[parent] {
			if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(parent)), 0755); err != nil {
				return nil, err
			}
		}
	}

	remaining := append([]Mapping(nil), ms...)
	var order []string
	for len(remaining) > 0 {
		var next []Mapping
		progressed := false
		for _, m := range remaining {
			parent := path.Dir(m.NewDir)
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(parent))); err == nil {
				oldP := filepath.Join(root, filepath.FromSlash(m.OldDir))
				newP := filepath.Join(root, filepath.FromSlash(m.NewDir))
				if err := os.Rename(oldP, newP); err != nil {
					return nil, fmt.Errorf("move %s -> %s: %w", m.OldDir, m.NewDir, err)
				}
				order = append(order, m.OldDir+" -> "+m.NewDir)
				progressed = true
			} else {
				next = append(next, m)
			}
		}
		if !progressed {
			return nil, fmt.Errorf("unresolvable move ordering; stuck on %v", next)
		}
		remaining = next
	}
	return order, nil
}
