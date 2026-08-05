package executor

// M0127-P5.6-g-v — the row count on a line that carries a `Filter:` detail
// must be the POST-qual estimate.
//
// goopg renders a qual and the rows it filters from two different plan
// nodes: the predicate lives on a `*Filter` wrapper, which the EXPLAIN
// walker collapses onto the child below it so the output matches PG's
// node set. Before this fix the collapsed line kept printing
// `EstimateRows(child)` — the PRE-qual count — while the predicate it
// printed beside it had already been applied by the estimator. A filtered
// `lineitem` scan therefore rendered rows=5997241 (the whole relation)
// where PG rendered rows=1673754, and Q18's HAVING node rendered its
// group count instead of the post-HAVING count.
//
// Upstream has no equivalent gap because the qual and the rowcount live on
// one struct: `set_baserel_size_estimates` (costsize.c) stores `rel->rows`
// already scaled by `clauselist_selectivity(baserestrictinfo)`, and
// `cost_agg` sets `path->rows` only after scaling `output_tuples` by the
// HAVING quals' selectivity.
//
// This matters beyond cosmetics: `internal/estimateaudit` parses exactly
// this `rows=` field (audit.go's `nodeLineRe`) and the TPC-DS SF0.5 gate's
// `plans` channel captures it, so both acceptance instruments were reading
// the unfiltered number for every filtered scan.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// newCollapsedFilterFixture: fr(k, v) with injected stats — 1000 rows over
// 10 distinct k. The in-process fixture's ANALYZE is a no-op, so stats are
// set directly (same convention as newMemoizeFixture).
func newCollapsedFilterFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	stmts := []string{"CREATE TABLE fr (k int, v int)"}
	for i := 0; i < 20; i++ {
		stmts = append(stmts, "INSERT INTO fr VALUES ("+itoa(i%10)+", "+itoa(i)+")")
	}
	for _, stmt := range stmts {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "fr"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 1000, Columns: []catalog.ColumnStats{
			{NDistinct: 10}, {NDistinct: 1000},
		}}
	}
	return ctx, cleanup
}

// findRowsOnLineWith returns the rows=N value from the first plan line
// whose text contains `needle`, and the line itself for diagnostics.
func findRowsOnLineWith(t *testing.T, lines []string, needle string) (int, string) {
	t.Helper()
	for _, ln := range lines {
		if !strings.Contains(ln, needle) {
			continue
		}
		i := strings.Index(ln, "rows=")
		if i < 0 {
			t.Fatalf("line %q carries no rows= field", ln)
		}
		rest := ln[i+len("rows="):]
		end := strings.IndexAny(rest, " )")
		if end < 0 {
			end = len(rest)
		}
		n := 0
		for _, c := range rest[:end] {
			if c < '0' || c > '9' {
				t.Fatalf("line %q has non-numeric rows=%q", ln, rest[:end])
			}
			n = n*10 + int(c-'0')
		}
		return n, ln
	}
	t.Fatalf("no plan line contains %q; plan was:\n%s", needle, strings.Join(lines, "\n"))
	return 0, ""
}

// TestExplainScanFilterRowsArePostQual: a `WHERE k = 3` against a
// 1000-row table with 10 distinct k must render rows=100 on the scan line
// that carries the `Filter:` detail — not the relation's 1000.
func TestExplainScanFilterRowsArePostQual(t *testing.T) {
	ctx, cleanup := newCollapsedFilterFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN SELECT v FROM fr WHERE k = 3")
	plan := strings.Join(lines, "\n")
	if !strings.Contains(plan, "Filter:") {
		t.Fatalf("expected a Filter: detail line, got:\n%s", plan)
	}
	got, ln := findRowsOnLineWith(t, lines, "Seq Scan")
	// 1000 rows / NDistinct 10 = 100. The pre-fix bug rendered 1000.
	if got != 100 {
		t.Errorf("scan line rows = %d, want 100 (post-qual); line: %s\nplan:\n%s", got, ln, plan)
	}
}

// TestExplainHavingRowsArePostQual: the HAVING predicate collapses onto
// the HashAggregate line, so that line must report the post-HAVING count.
// With 10 groups and no statistics for `sum(v)`, goopg falls back to
// DEFAULT_INEQ_SEL (1/3) exactly as PG's cost_agg does → 10/3 = 3.
func TestExplainHavingRowsArePostQual(t *testing.T) {
	ctx, cleanup := newCollapsedFilterFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN SELECT k FROM fr GROUP BY k HAVING sum(v) > 5")
	plan := strings.Join(lines, "\n")
	if !strings.Contains(plan, "Filter:") {
		t.Fatalf("expected a Filter: detail line for HAVING, got:\n%s", plan)
	}
	got, ln := findRowsOnLineWith(t, lines, "Aggregate")
	// 10 groups × DEFAULT_INEQ_SEL. The pre-fix bug rendered the bare 10.
	if got != 3 {
		t.Errorf("aggregate line rows = %d, want 3 (post-HAVING); line: %s\nplan:\n%s", got, ln, plan)
	}
}

// TestExplainUnfilteredScanRowsUnchanged: the fix must not disturb a scan
// with no qual — no Filter wrapper, so the relation's own estimate stands.
func TestExplainUnfilteredScanRowsUnchanged(t *testing.T) {
	ctx, cleanup := newCollapsedFilterFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN SELECT v FROM fr")
	got, ln := findRowsOnLineWith(t, lines, "Seq Scan")
	if got != 1000 {
		t.Errorf("unfiltered scan rows = %d, want 1000; line: %s", got, ln)
	}
}
