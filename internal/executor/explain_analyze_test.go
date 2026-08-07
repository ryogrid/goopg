package executor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestExplainAnalyzeRunsInnerAndReportsActualRows: `EXPLAIN
// ANALYZE SELECT * FROM t` over a 5-row table reports
// `actual ... rows=5` somewhere in the output. Pins that the
// instrumentation table is populated and that the renderer
// surfaces the count.
func TestExplainAnalyzeRunsInnerAndReportsActualRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	for _, n := range []int64{1, 2, 3, 4, 5} {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: n}}); err != nil {
			t.Fatal(err)
		}
	}

	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE SELECT * FROM t")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "rows=5") || !strings.Contains(joined, "loops=1") {
		t.Errorf("ANALYZE output missing actual-rows / loops:\n%s", joined)
	}
	if !strings.Contains(joined, "actual time=") {
		t.Errorf("ANALYZE output missing 'actual time=' bracket:\n%s", joined)
	}
}

// TestExplainAnalyzeIncludesPlanningExecutionTime: the TEXT
// output gains a Planning Time / Execution Time footer when
// SUMMARY is on (default under ANALYZE).
func TestExplainAnalyzeIncludesPlanningExecutionTime(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE SELECT 1")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Planning Time:") {
		t.Errorf("missing 'Planning Time:' footer:\n%s", joined)
	}
	if !strings.Contains(joined, "Execution Time:") {
		t.Errorf("missing 'Execution Time:' footer:\n%s", joined)
	}
}

// TestExplainAnalyzeJSONIncludesActualFields: the JSON shape
// gains Actual Rows / Actual Loops / Actual Total Time / Actual
// Startup Time keys per node, plus Planning Time / Execution
// Time on the root.
func TestExplainAnalyzeJSONIncludesActualFields(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, FORMAT JSON) SELECT 1")
	if len(lines) != 1 {
		t.Fatalf("got %d rows, want 1", len(lines))
	}
	var top []map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &top); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, lines[0])
	}
	// 0118-0102: the plan tree nests under a top-level "Plan" key
	// (PG-faithful), with Planning/Execution Time as siblings.
	root := top[0]
	plan, ok := root["Plan"].(map[string]any)
	if !ok {
		t.Fatalf("top[0] missing \"Plan\" object: %+v", root)
	}
	for _, key := range []string{"Actual Rows", "Actual Loops", "Actual Total Time", "Actual Startup Time"} {
		if _, ok := plan[key]; !ok {
			t.Errorf("Plan missing %q: %+v", key, plan)
		}
	}
	for _, key := range []string{"Planning Time", "Execution Time"} {
		if _, ok := root[key]; !ok {
			t.Errorf("root missing %q: %+v", key, root)
		}
	}
	// `SELECT 1` produces 1 row; verify the count actually
	// flowed through the instrumentation.
	if rows, ok := plan["Actual Rows"].(float64); ok {
		if rows != 1 {
			t.Errorf("Actual Rows = %v, want 1", rows)
		}
	}
}

// TestExplainAnalyzeOnSelectOneRowsAccurate pins the simplest
// case: `EXPLAIN ANALYZE SELECT 1` must report exactly 1 row.
func TestExplainAnalyzeOnSelectOneRowsAccurate(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE SELECT 1")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "rows=1") {
			t.Errorf("rows=1 not found:\n%s", joined)
		}
	}

// TestExplainAnalyzeRowsRemovedByFilter verifies that EXPLAIN ANALYZE
// emits "Rows Removed by Filter: N" for a scan whose Filter wrapper
// rejects some tuples — the M0128-P5.2 scan-filter counter.
func TestExplainAnalyzeRowsRemovedByFilter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	for _, n := range []int64{1, 2, 3, 4, 5} {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: n}}); err != nil {
			t.Fatal(err)
		}
	}

	// WHERE id > 3: rows 1,2,3 are rejected → expect "Rows Removed by Filter"
	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE SELECT * FROM t WHERE id > 3")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Rows Removed by Filter:") {
		t.Errorf("missing 'Rows Removed by Filter' line:\n%s", joined)
	}
}

// TestExplainAnalyzeRowsRemovedByJoinFilter verifies that EXPLAIN
// ANALYZE emits "Rows Removed by Join Filter: N" for a hash join
// whose residual qual rejects some candidate matches — the
// M0128-P5.2 join-filter counter.
func TestExplainAnalyzeRowsRemovedByJoinFilter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE a (id int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE b (id int, val text)"); err != nil {
		t.Fatal(err)
	}
	ta, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "a"})
	tb, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "b"})
	ra := ctx.Catalog.RelFileNode(ta)
	rb := ctx.Catalog.RelFileNode(tb)
	for _, n := range []int64{1, 2, 3} {
		if err := writeHeapRow(ctx, ra, ta.Columns, Row{{Kind: KindInt, Int: n}}); err != nil {
			t.Fatal(err)
		}
	}
	for i, v := range []string{"x", "y", "z"} {
		if err := writeHeapRow(ctx, rb, tb.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			NewStringDatum(v),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Hash join on a.id=b.id with extra residual b.val <> 'y'.
	// The hash key matches but the residual rejects on id=2 (val='y').
	lines := runExplainRows(t, ctx,
		"EXPLAIN ANALYZE SELECT * FROM a JOIN b ON a.id = b.id AND b.val <> 'y'")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Rows Removed by Join Filter:") {
		t.Errorf("missing 'Rows Removed by Join Filter' line:\n%s", joined)
	}
}
