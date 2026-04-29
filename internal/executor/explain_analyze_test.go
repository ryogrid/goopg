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
	root := top[0]
	for _, key := range []string{"Actual Rows", "Actual Loops", "Actual Total Time", "Actual Startup Time", "Planning Time", "Execution Time"} {
		if _, ok := root[key]; !ok {
			t.Errorf("root missing %q: %+v", key, root)
		}
	}
	// `SELECT 1` produces 1 row; verify the count actually
	// flowed through the instrumentation.
	if rows, ok := root["Actual Rows"].(float64); ok {
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

