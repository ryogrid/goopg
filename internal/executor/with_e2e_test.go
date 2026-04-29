package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// TestExecuteWithSimpleCTE pins the end-to-end pipeline:
// parser → analyzer → planner (with CTE inlining) → executor
// produces the expected row for `WITH a AS (SELECT 1) SELECT * FROM a`.
//
// This is the ONE meaningful executor test for M0016-0002. The
// rest of the contract is pinned at the planner / analyzer
// layers — the executor doesn't need its own CTE infrastructure
// because Stage A inlining produces a vanilla plan tree the
// existing operators can run unchanged.
func TestExecuteWithSimpleCTE(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	stmts, err := parser.Parse("WITH a AS (SELECT 1) SELECT * FROM a")
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

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if len(rows[0]) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != 1 {
		t.Errorf("row = %+v, want one int=1 column", rows[0])
	}
}

// TestExecuteWithCTEMultipleConsumers exercises the inlining
// model: the same CTE appears twice in the FROM list (cross-
// product). With Stage A inlining each consumer plans its own
// copy; the executor sees a plain CrossJoin / Project and
// returns the right number of rows.
func TestExecuteWithCTEMultipleConsumers(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	stmts, err := parser.Parse("WITH a AS (SELECT 1) SELECT * FROM a, a b")
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

	// 1-row CTE × 1-row CTE = 1 row, 2 columns.
	if len(rows) != 1 || len(rows[0]) != 2 {
		t.Errorf("rows=%d cols=%d, want 1×2", len(rows), len(rows[0]))
	}
}
