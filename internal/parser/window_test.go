package parser

import "testing"

// TestParseWindowFuncBareOver — the simplest window-function
// shape: `f() OVER ()`. Pins (1) the FuncCall.Over field is
// non-nil, (2) PartitionBy and OrderBy are both empty (which
// the executor will treat as "the entire input is one
// partition" — upstream's default frame for ROW_NUMBER and
// RANK).
func TestParseWindowFuncBareOver(t *testing.T) {
	stmts, err := Parse("SELECT row_number() OVER () FROM pgbench_accounts")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	fc, ok := s.Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("target = %T, want *FuncCall", s.Targets[0].Expr)
	}
	if fc.Over == nil {
		t.Fatal("Over is nil; expected non-nil for OVER () shape")
	}
	if len(fc.Over.PartitionBy) != 0 {
		t.Errorf("PartitionBy = %+v, want empty", fc.Over.PartitionBy)
	}
	if len(fc.Over.OrderBy) != 0 {
		t.Errorf("OrderBy = %+v, want empty", fc.Over.OrderBy)
	}
}

// TestParseWindowFuncPartitionBy — `OVER (PARTITION BY ...)`.
// Pins the partition-key list parses as a normal expression
// list (the executor will hash on these to bucket rows into
// partitions).
func TestParseWindowFuncPartitionBy(t *testing.T) {
	stmts, err := Parse("SELECT rank() OVER (PARTITION BY bid) FROM pgbench_accounts")
	if err != nil {
		t.Fatal(err)
	}
	fc := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if fc.Over == nil || len(fc.Over.PartitionBy) != 1 {
		t.Fatalf("PartitionBy = %+v", fc.Over)
	}
}

// TestParseWindowFuncOrderBy — `OVER (ORDER BY ... [ASC|DESC])`.
// Pins the sort list reuses the existing SortBy AST shape so
// the executor's ordering logic doesn't have to learn new
// sort-key plumbing for window functions.
func TestParseWindowFuncOrderBy(t *testing.T) {
	stmts, err := Parse("SELECT row_number() OVER (ORDER BY aid DESC) FROM pgbench_accounts")
	if err != nil {
		t.Fatal(err)
	}
	fc := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if fc.Over == nil || len(fc.Over.OrderBy) != 1 {
		t.Fatalf("OrderBy = %+v", fc.Over)
	}
	if !fc.Over.OrderBy[0].Desc {
		t.Errorf("OrderBy[0].Desc = false, want true")
	}
}

// TestParseWindowFuncPartitionAndOrder — both PARTITION BY and
// ORDER BY together — the canonical analytical-query shape.
func TestParseWindowFuncPartitionAndOrder(t *testing.T) {
	stmts, err := Parse("SELECT rank() OVER (PARTITION BY bid ORDER BY abalance DESC) FROM pgbench_accounts")
	if err != nil {
		t.Fatal(err)
	}
	fc := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if fc.Over == nil {
		t.Fatal("Over nil")
	}
	if len(fc.Over.PartitionBy) != 1 {
		t.Errorf("PartitionBy len = %d, want 1", len(fc.Over.PartitionBy))
	}
	if len(fc.Over.OrderBy) != 1 || !fc.Over.OrderBy[0].Desc {
		t.Errorf("OrderBy = %+v, want one DESC entry", fc.Over.OrderBy)
	}
}

// TestParseWindowFuncCountStarOver — the `count(*) OVER ()`
// shape pgbench analytic queries lean on. The Star=true case
// must also flow through the OVER-tail path; without the
// `maybeWindowTail` wiring covering it, the window clause
// would silently drop.
func TestParseWindowFuncCountStarOver(t *testing.T) {
	stmts, err := Parse("SELECT count(*) OVER () FROM pgbench_accounts")
	if err != nil {
		t.Fatal(err)
	}
	fc := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if !fc.Star {
		t.Errorf("Star = false, want true")
	}
	if fc.Over == nil {
		t.Fatal("Over nil for count(*) OVER ()")
	}
}

// TestParseWindowFuncRejectsFrameClause — frame clauses (ROWS /
// RANGE / GROUPS) parse but are explicitly rejected so users
// see a clear "deferred" diagnostic rather than a vague syntax
// error. Pins the deferred-frame contract so a future ROWS
// implementation can flip the reject without changing the
// parser AST shape.
func TestParseWindowFuncRejectsFrameClause(t *testing.T) {
	cases := []string{
		"SELECT row_number() OVER (ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) FROM t",
		"SELECT row_number() OVER (ORDER BY a RANGE UNBOUNDED PRECEDING) FROM t",
	}
	for _, sql := range cases {
		if _, err := Parse(sql); err == nil {
			t.Errorf("Parse(%q) succeeded; want error on frame clause", sql)
		}
	}
}

// TestParseWindowClauseNamedWindow — the M0020 named-window slice:
// a trailing `WINDOW name AS (...)` clause plus a bare `OVER name`
// reference. Pins that the clause is captured on SelectStmt.WindowClause
// and the referencing WindowDef carries RefName (not yet resolved —
// that's the analyzer's job) rather than an empty PartitionBy/OrderBy.
func TestParseWindowClauseNamedWindow(t *testing.T) {
	stmts, err := Parse("SELECT rank() OVER w FROM pgbench_accounts WINDOW w AS (PARTITION BY bid ORDER BY aid)")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.WindowClause) != 1 {
		t.Fatalf("WindowClause = %+v, want 1 item", s.WindowClause)
	}
	if s.WindowClause[0].Name != "w" {
		t.Errorf("WindowClause[0].Name = %q, want %q", s.WindowClause[0].Name, "w")
	}
	if len(s.WindowClause[0].Def.PartitionBy) != 1 || len(s.WindowClause[0].Def.OrderBy) != 1 {
		t.Errorf("WindowClause[0].Def = %+v, want 1 PartitionBy + 1 OrderBy", s.WindowClause[0].Def)
	}
	fc := s.Targets[0].Expr.(*FuncCall)
	if fc.Over == nil || fc.Over.RefName != "w" {
		t.Fatalf("Over = %+v, want RefName %q", fc.Over, "w")
	}
	if len(fc.Over.PartitionBy) != 0 || len(fc.Over.OrderBy) != 0 {
		t.Errorf("Over.PartitionBy/OrderBy = %+v/%+v, want both empty pre-resolution", fc.Over.PartitionBy, fc.Over.OrderBy)
	}
}

// TestParseWindowClauseMultipleNamedWindows — a comma-separated
// WINDOW clause defining more than one named window, each referenced
// by a different function.
func TestParseWindowClauseMultipleNamedWindows(t *testing.T) {
	stmts, err := Parse("SELECT row_number() OVER w1, rank() OVER w2 FROM t WINDOW w1 AS (PARTITION BY a), w2 AS (ORDER BY b)")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.WindowClause) != 2 {
		t.Fatalf("WindowClause len = %d, want 2", len(s.WindowClause))
	}
	if s.WindowClause[0].Name != "w1" || s.WindowClause[1].Name != "w2" {
		t.Errorf("WindowClause names = %q, %q", s.WindowClause[0].Name, s.WindowClause[1].Name)
	}
}

// TestParseWindowFuncWithoutOverUnchanged — rollout guardrail:
// pre-M0020 FuncCalls (no OVER tail) produce nil Over, so
// every existing parser/analyzer/planner test continues to
// observe the historical AST.
func TestParseWindowFuncWithoutOverUnchanged(t *testing.T) {
	stmts, err := Parse("SELECT count(*) FROM pgbench_accounts")
	if err != nil {
		t.Fatal(err)
	}
	fc := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if fc.Over != nil {
		t.Errorf("Over = %+v, want nil for non-windowed call", fc.Over)
	}
}
