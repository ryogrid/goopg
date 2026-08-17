package executor

import (
	"strings"
	"testing"
)

// M0134-0001-S11 guards: EXPLAIN text-format indentation must follow
// PostgreSQL's cumulative `es->indent` model
// (postgres/src/backend/commands/explain.c:1616-1635, ExplainNode), not a
// flat `2*depth`. The two models only coincide at depth 0/1 — a "->  "
// marker's raw column runs 0, 2, 8, 14, 20 … (deltas 2, 6, 6, 6) as nesting
// deepens, and every detail line (`Sort Key:`, `Group Key:`, `Index Cond:`
// …) sits at the owning node's own post-increment indent, which is the SAME
// column its own children's "->  " would use.
//
// leadingSpaces returns the number of leading ASCII spaces in s.
func leadingSpaces(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}

// findIndentLine returns the (only) line containing sub, failing the test if
// there isn't exactly one.
func findIndentLine(t *testing.T, lines []string, sub string) string {
	t.Helper()
	var found string
	n := 0
	for _, l := range lines {
		if strings.Contains(l, sub) {
			found = l
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly 1 line containing %q, got %d:\n%s", sub, n, strings.Join(lines, "\n"))
	}
	return found
}

// deepSortFixture builds the 4-level `Sort -> GroupAggregate -> Sort ->
// Index Scan` shape PG's own regression suite uses to exercise indentation
// past depth 1 (postgres/src/test/regress/expected/aggregates.out:3158-3165,
// the `agg_sort_order` case: `array_agg(c1 ORDER BY c2), c2 ... GROUP BY c1
// ORDER BY 2` over a table with a unique index on the ORDER-BY column).
// goopg's planner doesn't always choose the identical strategy PG does
// (e.g. it may keep an explicit outer Sort PG elides via the unique-index
// order), so this fixture picks GROUP BY / ORDER BY columns goopg is known
// to render as this exact 4-level shape (verified with a throwaway probe
// against this package) rather than transcribing PG's precise column
// choice — the point under test is goopg's OWN indent arithmetic, which
// the aggregates.out capture proves the raw-column deltas (0, 2, 8, 14, 20)
// for.
func deepSortFixture(t *testing.T) *Context {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	if err := runDDL(t, ctx, "CREATE TABLE t (a int, b int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE UNIQUE INDEX t_b_idx ON t (b)"); err != nil {
		t.Fatal(err)
	}
	return ctx
}

const deepSortSQL = "SELECT array_agg(a ORDER BY b), b FROM t WHERE b < 100 GROUP BY b ORDER BY 2"

// TestExplainIndentDeepNesting pins the plain-EXPLAIN twin: raw "->  "
// columns 0/2/8/14/20 and matching detail-line columns, verified against
// aggregates.out:3158-3165's node shape (Sort / GroupAggregate / Sort /
// Index Scan) — the FIRST depth (2) where goopg's old flat `2*depth` model
// (which would print 4, not 8) diverged from PG.
func TestExplainIndentDeepNesting(t *testing.T) {
	ctx := deepSortFixture(t)
	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+deepSortSQL)

	if got := leadingSpaces(lines[0]); got != 0 || !strings.HasPrefix(lines[0], "Sort") {
		t.Errorf("root %q: want 0 leading spaces, got %d", lines[0], got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "->  GroupAggregate")); got != 2 {
		t.Errorf("depth-1 arrow: want 2 leading spaces, got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "Group Key: b")); got != 8 {
		t.Errorf("GroupAggregate's Group Key detail: want 8 leading spaces, got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "->  Sort")); got != 8 {
		t.Errorf("depth-2 arrow: want 8 leading spaces (PG-faithful, not the flat model's 4), got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "->  Index Scan")); got != 14 {
		t.Errorf("depth-3 arrow: want 14 leading spaces, got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "Index Cond: (b < 100)")); got != 20 {
		t.Errorf("Index Scan's Index Cond detail: want 20 leading spaces, got %d", got)
	}
}

// TestExplainAnalyzeIndentDeepNesting is the ANALYZE-mode twin of
// TestExplainIndentDeepNesting — walkPlanAnalyzeFiltered must thread the
// same cumulative indent as walkPlanFiltered (M0134-0001-S11: "a green
// test on one twin proves nothing about the other"). The raw columns are
// identical to the plain-EXPLAIN case; only the per-node `(actual …)`
// suffix differs.
func TestExplainAnalyzeIndentDeepNesting(t *testing.T) {
	ctx := deepSortFixture(t)
	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF, ANALYZE) "+deepSortSQL)

	if got := leadingSpaces(findIndentLine(t, lines, "->  GroupAggregate")); got != 2 {
		t.Errorf("depth-1 arrow: want 2 leading spaces, got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "Group Key: b")); got != 8 {
		t.Errorf("GroupAggregate's Group Key detail: want 8 leading spaces, got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "->  Sort")); got != 8 {
		t.Errorf("depth-2 arrow: want 8 leading spaces (PG-faithful, not the flat model's 4), got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "->  Index Scan")); got != 14 {
		t.Errorf("depth-3 arrow: want 14 leading spaces, got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "Index Cond: (b < 100)")); got != 20 {
		t.Errorf("Index Scan's Index Cond detail: want 20 leading spaces, got %d", got)
	}
}

// TestExplainIndentInitPlanBranch covers PG step 1 (the `plan_name`
// branch, explain.c:1621-1624) that a pure depth-formula gets wrong: an
// InitPlan/SubPlan label bumps `es->indent` by only 1, not the 3
// (2-for-arrow + 1-for-name) an ordinary child level adds. Shape verified
// against postgres/src/test/regress/expected/aggregates.out:939-947
// (`Result -> InitPlan 1 -> Limit -> ...`); goopg's plan (no index on `a`)
// carries one extra Sort level below the Limit, giving InitPlan / Limit /
// Sort / Seq Scan at depths 1/2/3/4 instead of PG's InitPlan / Limit /
// Index Only Scan at 1/2/3 — still exercises the same plan_name-then-arrow
// transition this test targets.
func TestExplainIndentInitPlanBranch(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (a int, b int)"); err != nil {
		t.Fatal(err)
	}
	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT min(a) FROM t")

	if got := leadingSpaces(findIndentLine(t, lines, "InitPlan 1")); got != 2 {
		t.Errorf("InitPlan label: want 2 leading spaces, got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "->  Limit")); got != 4 {
		t.Errorf("InitPlan body's own arrow: want 4 leading spaces (label childIndent+1, not label+2), got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "->  Sort")); got != 10 {
		t.Errorf("nested Sort under Limit: want 10 leading spaces, got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "Sort Key: a")); got != 16 {
		t.Errorf("Sort's own detail line: want 16 leading spaces, got %d", got)
	}
	if got := leadingSpaces(findIndentLine(t, lines, "Filter: (a IS NOT NULL)")); got != 22 {
		t.Errorf("Seq Scan's Filter detail: want 22 leading spaces, got %d", got)
	}
}
