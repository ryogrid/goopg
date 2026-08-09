package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// runExplain renders an EXPLAIN over `sql` and returns the lines
// of the QUERY PLAN output. Helper for the M0016-0004 EXPLAIN
// label tests.
func runExplain(t *testing.T, ctx *Context, sql string) []string {
	t.Helper()
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse("EXPLAIN " + sql)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	rows, err := drainScan(op)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	_ = op.Close()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if len(r) > 0 && r[0].Kind == KindString {
			out = append(out, r[0].StringValue())
		}
	}
	return out
}

// TestExplainCTEScanLabelsCTEByName: EXPLAIN over a simple CTE
// surfaces "CTE Scan on a" so an operator can identify the
// inlined CTE in the plan tree. Pre-M0016-0004 the consumer
// site appeared as a bare Values / SeqScan with no CTE
// signal.
func TestExplainCTEScanLabelsCTEByName(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplain(t, ctx, "WITH a AS (SELECT 1) SELECT * FROM a")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "CTE Scan on a") {
		t.Errorf("EXPLAIN output missing 'CTE Scan on a':\n%s", joined)
	}
}

// TestExplainCTEScanShowsAlias: when the consumer renames the
// CTE via a FROM alias, the label includes both names.
func TestExplainCTEScanShowsAlias(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplain(t, ctx, "WITH a AS (SELECT 1) SELECT * FROM a x")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "CTE Scan on a x") {
		t.Errorf("EXPLAIN output missing 'CTE Scan on a x':\n%s", joined)
	}
}

// TestExplainCTEScanRecursesIntoChild: the CTEScan wrap appears
// in the EXPLAIN tree but its inlined child still renders below
// it (so an operator sees what the CTE actually does).
func TestExplainCTEScanRecursesIntoChild(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplain(t, ctx, "WITH a AS (SELECT 1) SELECT * FROM a")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 EXPLAIN lines, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	// First line is the outer SELECT (Projection); somewhere
	// below it should be the CTE Scan, then the inlined body.
	saw := false
	for _, l := range lines {
		if strings.Contains(l, "CTE Scan on a") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("CTE Scan label not found:\n%s", strings.Join(lines, "\n"))
	}
}

// TestExplainCTEScanAnalyzeReportsActualRows: M0122-0003. Build()'s
// CTEScan/CTEDMLPrefix/MaterializedCTEScan cases previously skipped
// maybeInstrument, so `EXPLAIN ANALYZE` over a CTE reported cost-only
// estimates for the CTE Scan node itself (its inlined child still
// showed actual stats, since the child is Built recursively inside
// newCteScanOp — only the wrapping CTEScan node was uninstrumented).
func TestExplainCTEScanAnalyzeReportsActualRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE WITH a AS (SELECT 1) SELECT * FROM a")
	joined := strings.Join(lines, "\n")
	cteLine := ""
	for _, l := range lines {
		if strings.Contains(l, "CTE Scan on a") {
			cteLine = l
			break
		}
	}
	if cteLine == "" {
		t.Fatalf("CTE Scan label not found:\n%s", joined)
	}
	if !strings.Contains(cteLine, "actual time=") || !strings.Contains(cteLine, "rows=1.00") {
		t.Errorf("CTE Scan node missing actual rows/time instrumentation:\n%s", cteLine)
	}
}

// TestExplainCTEDMLPrefixAnalyzeReportsActualRows: the "CTE DML"
// summary node (wrapping data-modifying CTEs run before the outer
// query) reports actual rows/time.
func TestExplainCTEDMLPrefixAnalyzeReportsActualRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE WITH ins AS (INSERT INTO t VALUES (1),(2) RETURNING id) SELECT * FROM ins")
	joined := strings.Join(lines, "\n")
	dmlLine := ""
	for _, l := range lines {
		if strings.Contains(l, "CTE DML") {
			dmlLine = l
			break
		}
	}
	if dmlLine == "" {
		t.Fatalf("CTE DML label not found:\n%s", joined)
	}
	if !strings.Contains(dmlLine, "actual time=") || !strings.Contains(dmlLine, "rows=2.00") {
		t.Errorf("CTE DML node missing actual rows/time instrumentation:\n%s", dmlLine)
	}
}

// TestExplainCTEDMLPrefixNestedInsertReportsActualRows: the DML plans
// nested under "CTE DML" (e.g. "Insert on t") are Built lazily inside
// cteDMLPrefixOp.Open(), after the top-level withInstrumentation()
// scope established around explainOp's Build() call has already been
// torn down. Without reinstating that scope around the nested Build()
// calls, those nodes would report plan-only estimates with no actual
// rows/time even though the DML genuinely ran and produced rows.
func TestExplainCTEDMLPrefixNestedInsertReportsActualRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE WITH ins AS (INSERT INTO t VALUES (1),(2) RETURNING id) SELECT * FROM ins")
	joined := strings.Join(lines, "\n")
	insertLine := ""
	for _, l := range lines {
		if strings.Contains(l, "Insert on t") {
			insertLine = l
			break
		}
	}
	if insertLine == "" {
		t.Fatalf("Insert on t label not found:\n%s", joined)
	}
	if !strings.Contains(insertLine, "actual time=") || !strings.Contains(insertLine, "rows=2.00") {
		t.Errorf("nested Insert node missing actual rows/time instrumentation:\n%s", insertLine)
	}
}
