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
