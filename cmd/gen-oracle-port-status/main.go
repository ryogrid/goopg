package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goopg/goopg/internal/testport/framework"
)

func main() {
	var (
		repoRoot = flag.String("repo-root", ".", "path to repository root")
		inCSV    = flag.String("in-csv", "docs/test-port/postgres-oracle-port-status.csv", "input status csv")
		outMD    = flag.String("out-md", "docs/test-port/postgres-oracle-port-status.md", "output markdown path")
	)
	flag.Parse()

	root, err := filepath.Abs(*repoRoot)
	if err != nil {
		fail("resolve repo root", err)
	}
	csvPath := filepath.Join(root, filepath.FromSlash(*inCSV))
	rows, err := framework.LoadStatusCSV(csvPath)
	if err != nil {
		fail("load status csv", err)
	}
	if err := framework.ValidateStatusRows(rows); err != nil {
		fail("validate status rows", err)
	}

	outPath := filepath.Join(root, filepath.FromSlash(*outMD))
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fail("mkdir output dir", err)
	}
	f, err := os.Create(outPath)
	if err != nil {
		fail("create output file", err)
	}
	defer f.Close()

	if err := framework.WriteStatusMarkdown(f, rows); err != nil {
		fail("write markdown", err)
	}
}

func fail(where string, err error) {
	fmt.Fprintf(os.Stderr, "gen-oracle-port-status: %s: %v\n", where, err)
	os.Exit(1)
}
