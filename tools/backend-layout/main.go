// Command backend-layout relocates goopg packages so the internal/ directory
// tree mirrors PostgreSQL's src/backend layout, driven by mapping.tsv.
//
// It performs pure package relocations: move a directory, rename its package
// declaration, rewrite import paths, and rename selector idents that use a moved
// package's default name. No package is split or merged and no symbol is moved
// between packages, so the Go import graph is only relabelled — it stays acyclic
// and no import cycle can be introduced.
//
// Subcommands:
//
//	plan   dry-run: report every directory move and file edit, write moves.tsv.
//	apply  perform the moves and edits, write moves.tsv.
//
// Run from the module root, e.g.:
//
//	cd tools/backend-layout && go run . -root ../.. plan
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", ".", "goopg module root (contains go.mod)")
	module := flag.String("module", "github.com/goopg/goopg", "module path")
	mappingPath := flag.String("mapping", "mapping.tsv", "path to the mapping TSV")
	flag.Parse()

	cmd := "plan"
	if flag.NArg() > 0 {
		cmd = flag.Arg(0)
	}

	// Resolve root to an absolute path: the filesystem walk, the go/packages Dir,
	// and file:line matching all expect consistent absolute paths.
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fatalf("resolve root: %v", err)
	}

	ms, err := LoadMapping(*mappingPath)
	if err != nil {
		fatalf("load mapping: %v", err)
	}
	if len(ms) == 0 {
		fatalf("mapping %s is empty", *mappingPath)
	}

	a, err := compute(absRoot, *module, ms)
	if err != nil {
		fatalf("compute edits: %v", err)
	}

	switch cmd {
	case "plan":
		printPlan(a, ms)
	case "apply":
		if err := apply(a, absRoot, ms); err != nil {
			fatalf("apply: %v", err)
		}
	default:
		fatalf("unknown subcommand %q (want plan or apply)", cmd)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "backend-layout: "+format+"\n", args...)
	os.Exit(1)
}
