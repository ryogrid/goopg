package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Mapping is one package relocation: a directory and package rename.
type Mapping struct {
	OldDir string // repo-relative source directory, e.g. "internal/planner"
	OldPkg string // current package name, e.g. "planner"
	NewDir string // repo-relative destination directory
	NewPkg string // new package name, e.g. "optimizer"
}

// LoadMapping reads a TSV file of columns old_dir, old_pkg, new_dir, new_pkg.
// Blank lines and lines starting with '#' are skipped.
func LoadMapping(path string) ([]Mapping, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var ms []Mapping
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 4 {
			return nil, fmt.Errorf("%s:%d: row %q: want 4 tab-separated columns, got %d", path, lineNo, line, len(cols))
		}
		ms = append(ms, Mapping{
			OldDir: cols[0],
			OldPkg: cols[1],
			NewDir: cols[2],
			NewPkg: cols[3],
		})
	}
	return ms, sc.Err()
}
