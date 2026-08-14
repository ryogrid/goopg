package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/goopg/goopg/internal/testport/framework"
)

func main() {
	var (
		repoRoot = flag.String("repo-root", ".", "path to repository root")
		invCSV   = flag.String("inventory-csv", "docs/test-port/postgres-oracle-target-inventory.csv", "inventory csv")
		outPath  = flag.String("out", "analysis/postgres-oracle-compatibility-report.md", "output report path")
	)
	flag.Parse()

	root, err := filepath.Abs(*repoRoot)
	if err != nil {
		fail("resolve repo root", err)
	}
	rows, err := framework.LoadStatusCSV(filepath.Join(root, filepath.FromSlash(*invCSV)))
	if err != nil {
		fail("load inventory", err)
	}
	if err := framework.ValidateStatusRows(rows); err != nil {
		fail("validate inventory", err)
	}

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
	fmt.Fprintln(f, "Single authority: `docs/test-port/postgres-oracle-target-inventory.csv`.")

	// Inventory snapshot: suite_id -> kind -> count.
	type suiteAgg struct {
		kind  string
		count int
	}
	bySuite := map[string]*suiteAgg{}
	var suiteOrder []string
	for _, r := range rows {
		a, ok := bySuite[r.SuiteID]
		if !ok {
			a = &suiteAgg{kind: r.Kind}
			bySuite[r.SuiteID] = a
			suiteOrder = append(suiteOrder, r.SuiteID)
		}
		a.count++
	}
	sort.Strings(suiteOrder)

	fmt.Fprintln(f, "## Inventory Snapshot")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "| suite_id | kind | discovered_cases |")
	fmt.Fprintln(f, "| -------- | ---- | ---------------: |")
	for _, sid := range suiteOrder {
		a := bySuite[sid]
		fmt.Fprintf(f, "| %s | %s | %d |\n", sid, a.kind, a.count)
	}
	fmt.Fprintln(f)

	// Status summary: full per-status breakdown, no "other" collapse.
	type statusAgg struct {
		pass, failed, notTried, excluded, port, defer_ int
	}
	byStatus := map[string]*statusAgg{}
	for _, sid := range suiteOrder {
		byStatus[sid] = &statusAgg{}
	}
	for _, r := range rows {
		a := byStatus[r.SuiteID]
		switch r.Status {
		case "pass":
			a.pass++
		case "failed":
			a.failed++
		case "not-tried":
			a.notTried++
		case "excluded":
			a.excluded++
		case "port":
			a.port++
		case "defer":
			a.defer_++
		}
	}
	fmt.Fprintln(f, "## Status Summary")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "| suite_id | pass | failed | not-tried | excluded | port | defer |")
	fmt.Fprintln(f, "| -------- | ---: | -----: | --------: | -------: | ---: | ----: |")
	for _, sid := range suiteOrder {
		a := byStatus[sid]
		fmt.Fprintf(f, "| %s | %d | %d | %d | %d | %d | %d |\n",
			sid, a.pass, a.failed, a.notTried, a.excluded, a.port, a.defer_)
	}
	fmt.Fprintln(f)

	// Deferred blockers: defer rows carry a milestone reference.
	fmt.Fprintln(f, "## Deferred Blockers")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "| id | item_path | deferred_to | rationale |")
	fmt.Fprintln(f, "|----|-----------|-------------|-----------|")
	for _, r := range rows {
		if r.Status != "defer" {
			continue
		}
		fmt.Fprintf(f, "| %s | `%s` | `%s` | %s |\n", r.ID, r.ItemPath, r.DeferredTo, r.Rationale)
	}
}

func fail(where string, err error) {
	fmt.Fprintf(os.Stderr, "gen-oracle-report: %s: %v\n", where, err)
	os.Exit(1)
}
