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

// TestParseWindowFuncAcceptsFrameClause — frame clauses (ROWS /
// RANGE / GROUPS) now parse successfully (M0122-0004 frame-clause
// slice; previously any frame clause was a parse-time reject — see
// TestParseWindowFrame* above for shape coverage). RANGE/GROUPS are
// still rejected, but at the analyzer layer (0A000) since only the
// analyzer raises non-syntax SQLSTATEs in this codebase — see
// internal/analyzer's window frame tests for that rejection.
func TestParseWindowFuncAcceptsFrameClause(t *testing.T) {
	cases := []string{
		"SELECT row_number() OVER (ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) FROM t",
		"SELECT sum(x) OVER (ORDER BY a RANGE UNBOUNDED PRECEDING) FROM t",
	}
	for _, sql := range cases {
		if _, err := Parse(sql); err != nil {
			t.Errorf("Parse(%q) failed: %v", sql, err)
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

// TestParseWindowFrameRowsBetweenOffsets — the general BETWEEN form
// with two numeric offsets on both sides (M0122-0004 frame-clause
// slice).
func TestParseWindowFrameRowsBetweenOffsets(t *testing.T) {
	stmts, err := Parse("SELECT sum(x) OVER (ORDER BY y ROWS BETWEEN 1 PRECEDING AND 2 FOLLOWING) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	fc := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	fr := fc.Over.Frame
	if fr == nil {
		t.Fatal("Frame = nil, want a parsed ROWS clause")
	}
	if fr.Mode != FrameModeRows {
		t.Errorf("Mode = %v, want FrameModeRows", fr.Mode)
	}
	if fr.StartKind != FrameBoundOffsetPreceding || fr.StartOffset == nil {
		t.Errorf("StartKind/StartOffset = %v/%v, want FrameBoundOffsetPreceding/non-nil", fr.StartKind, fr.StartOffset)
	}
	if fr.EndKind != FrameBoundOffsetFollowing || fr.EndOffset == nil {
		t.Errorf("EndKind/EndOffset = %v/%v, want FrameBoundOffsetFollowing/non-nil", fr.EndKind, fr.EndOffset)
	}
	if fr.Exclusion != FrameExcludeNone {
		t.Errorf("Exclusion = %v, want FrameExcludeNone", fr.Exclusion)
	}
}

// TestParseWindowFrameSingleBoundDefaultsEndToCurrentRow — the
// single-frame_bound form (`ROWS <bound>` with no BETWEEN/AND)
// defaults the end bound to CURRENT ROW, per gram.y's frame_extent:
// frame_bound production.
func TestParseWindowFrameSingleBoundDefaultsEndToCurrentRow(t *testing.T) {
	stmts, err := Parse("SELECT sum(x) OVER (ORDER BY y ROWS UNBOUNDED PRECEDING) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	fc := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	fr := fc.Over.Frame
	if fr == nil {
		t.Fatal("Frame = nil, want a parsed ROWS clause")
	}
	if fr.StartKind != FrameBoundUnboundedPreceding {
		t.Errorf("StartKind = %v, want FrameBoundUnboundedPreceding", fr.StartKind)
	}
	if fr.EndKind != FrameBoundCurrentRow {
		t.Errorf("EndKind = %v, want FrameBoundCurrentRow (single-bound default)", fr.EndKind)
	}
}

// TestParseWindowFrameCurrentRowBound pins `CURRENT ROW` frame_bound
// parsing on both ends of a BETWEEN clause.
func TestParseWindowFrameCurrentRowBound(t *testing.T) {
	stmts, err := Parse("SELECT sum(x) OVER (ORDER BY y ROWS BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	fc := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	fr := fc.Over.Frame
	if fr.StartKind != FrameBoundCurrentRow {
		t.Errorf("StartKind = %v, want FrameBoundCurrentRow", fr.StartKind)
	}
	if fr.EndKind != FrameBoundUnboundedFollowing {
		t.Errorf("EndKind = %v, want FrameBoundUnboundedFollowing", fr.EndKind)
	}
}

// TestParseWindowFrameExcludeClauses pins all four EXCLUDE spellings,
// including that EXCLUDE NO OTHERS parses to the same FrameExcludeNone
// as an omitted clause (gram.y's opt_window_exclusion_clause).
func TestParseWindowFrameExcludeClauses(t *testing.T) {
	cases := []struct {
		sql  string
		want FrameExclusion
	}{
		{"SELECT sum(x) OVER (ORDER BY y ROWS BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE CURRENT ROW) FROM t", FrameExcludeCurrentRow},
		{"SELECT sum(x) OVER (ORDER BY y ROWS BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE GROUP) FROM t", FrameExcludeGroup},
		{"SELECT sum(x) OVER (ORDER BY y ROWS BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE TIES) FROM t", FrameExcludeTies},
		{"SELECT sum(x) OVER (ORDER BY y ROWS BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE NO OTHERS) FROM t", FrameExcludeNone},
	}
	for _, c := range cases {
		stmts, err := Parse(c.sql)
		if err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		fc := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
		if fc.Over.Frame.Exclusion != c.want {
			t.Errorf("%s: Exclusion = %v, want %v", c.sql, fc.Over.Frame.Exclusion, c.want)
		}
	}
}

// TestParseWindowFrameRangeAndGroupsModesParse pins that RANGE and
// GROUPS frame clauses parse structurally (Mode set accordingly) even
// though the analyzer rejects them with 0A000 — only ROWS reaches the
// executor in this slice.
func TestParseWindowFrameRangeAndGroupsModesParse(t *testing.T) {
	stmts, err := Parse("SELECT sum(x) OVER (ORDER BY y RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	fc := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if fc.Over.Frame.Mode != FrameModeRange {
		t.Errorf("Mode = %v, want FrameModeRange", fc.Over.Frame.Mode)
	}

	stmts, err = Parse("SELECT sum(x) OVER (ORDER BY y GROUPS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	fc = stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if fc.Over.Frame.Mode != FrameModeGroups {
		t.Errorf("Mode = %v, want FrameModeGroups", fc.Over.Frame.Mode)
	}
}

// TestParseWindowFrameBoundOrderingErrors pins the gram.y
// frame_extent/frame_bound windowing-error validations that a
// resume-point loop must still add at the analyzer layer (this parser
// itself doesn't raise 42P20 — see parseFrameClause's doc comment).
// This test only pins that these currently-invalid shapes still parse
// (no premature rejection at the parser layer); the analyzer test file
// pins the actual 42P20 rejections.
func TestParseWindowFrameNoFrameClauseStaysNil(t *testing.T) {
	stmts, err := Parse("SELECT row_number() OVER (PARTITION BY a ORDER BY b) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	fc := stmts[0].(*SelectStmt).Targets[0].Expr.(*FuncCall)
	if fc.Over.Frame != nil {
		t.Errorf("Frame = %+v, want nil when no frame clause is written", fc.Over.Frame)
	}
}

// TestParseWindowClauseFrameOnNamedWindow pins that a named `WINDOW
// name AS (...)` item can also carry a frame clause — parseWindowSpecBody
// is shared between the anonymous and named forms
// (pattern_sibling_paths_must_agree), so this must stay in sync with
// TestParseWindowFrameRowsBetweenOffsets.
func TestParseWindowClauseFrameOnNamedWindow(t *testing.T) {
	stmts, err := Parse("SELECT sum(x) OVER w FROM t WINDOW w AS (ORDER BY y ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	fr := s.WindowClause[0].Def.Frame
	if fr == nil || fr.StartKind != FrameBoundOffsetPreceding {
		t.Fatalf("WindowClause[0].Def.Frame = %+v, want StartKind FrameBoundOffsetPreceding", fr)
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
