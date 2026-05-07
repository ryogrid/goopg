package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/goopg/goopg/internal/testport/framework"
)

type invRow struct {
	SuiteID string
	Kind    string
	Count   int
}

func main() {
	var (
		repoRoot = flag.String("repo-root", ".", "path to repository root")
		invCSV   = flag.String("inventory-csv", "docs/test-port/postgres-oracle-target-inventory.csv", "inventory csv")
		status   = flag.String("status-csv", "docs/test-port/postgres-oracle-port-status.csv", "status csv")
		outPath  = flag.String("out", "analysis/postgres-oracle-compatibility-report.md", "output report path")
	)
	flag.Parse()

	root, err := filepath.Abs(*repoRoot)
	if err != nil {
		fail("resolve repo root", err)
	}
	invRows, err := loadInventoryCSV(filepath.Join(root, filepath.FromSlash(*invCSV)))
	if err != nil {
		fail("load inventory", err)
	}
	statusRows, err := framework.LoadStatusCSV(filepath.Join(root, filepath.FromSlash(*status)))
	if err != nil {
		fail("load status", err)
	}
	if err := framework.ValidateStatusRows(statusRows); err != nil {
		fail("validate status", err)
	}
	summary := framework.SummarizeBySuite(statusRows)
	keys := framework.SuiteKeys(summary)

	fullOut := filepath.Join(root, filepath.FromSlash(*outPath))
	if err := os.MkdirAll(filepath.Dir(fullOut), 0o755); err != nil {
		fail("mkdir report dir", err)
	}
	f, err := os.Create(fullOut)
	if err != nil {
		fail("create report", err)
	}
	defer f.Close()

	fmt.Fprintln(f, "# PostgreSQL Oracle Compatibility Report (M0060)")
	fmt.Fprintln(f)
	fmt.Fprintf(f, "Generated at: %s\n\n", time.Now().Format(time.RFC3339))
	fmt.Fprintln(f, "## Inventory Snapshot")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "| suite_id | kind | discovered_cases |")
	fmt.Fprintln(f, "| -------- | ---- | ---------------: |")
	for _, r := range invRows {
		fmt.Fprintf(f, "| %s | %s | %d |\n", r.SuiteID, r.Kind, r.Count)
	}
	fmt.Fprintln(f)

	fmt.Fprintln(f, "## Status Summary")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "| suite_type | port | defer | excluded |")
	fmt.Fprintln(f, "| ---------- | ----:| -----:| --------:|")
	for _, k := range keys {
		c := summary[k]
		fmt.Fprintf(f, "| %s | %d | %d | %d |\n", k, c.Port, c.Defer, c.Excluded)
	}
	fmt.Fprintln(f)

	fmt.Fprintln(f, "## Deferred Blockers")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "| id | upstream_path | suite_type | deferred_to | rationale |")
	fmt.Fprintln(f, "|----|---------------|------------|-------------|-----------|")
	for _, r := range statusRows {
		if r.Status != "defer" {
			continue
		}
		fmt.Fprintf(f, "| %s | `%s` | %s | `%s` | %s |\n", r.ID, r.UpstreamPath, r.SuiteType, r.DeferredTo, r.Rationale)
	}
}

func loadInventoryCSV(path string) ([]invRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(recs) <= 1 {
		return nil, fmt.Errorf("inventory csv %q has no data rows", path)
	}
	idxSuite := -1
	idxKind := -1
	idxCount := -1
	for i, col := range recs[0] {
		switch col {
		case "suite_id":
			idxSuite = i
		case "kind":
			idxKind = i
		case "count":
			idxCount = i
		}
	}
	if idxSuite < 0 || idxKind < 0 || idxCount < 0 {
		return nil, fmt.Errorf("inventory csv %q missing required columns", path)
	}
	out := make([]invRow, 0, len(recs)-1)
	for _, rec := range recs[1:] {
		if len(rec) <= idxCount {
			continue
		}
		n, err := strconv.Atoi(rec[idxCount])
		if err != nil {
			continue
		}
		out = append(out, invRow{SuiteID: rec[idxSuite], Kind: rec[idxKind], Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SuiteID < out[j].SuiteID })
	return out, nil
}

func fail(where string, err error) {
	fmt.Fprintf(os.Stderr, "gen-oracle-report: %s: %v\n", where, err)
	os.Exit(1)
}
