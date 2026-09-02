package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// ── Unit tests for FoldConstants ────────────────────────────────────────────

// TestFoldIntegerArithmetic verifies that all-integer-literal arithmetic is
// reduced to a single IntegerConst at plan time.
func TestFoldIntegerArithmetic(t *testing.T) {
	cases := []struct {
		expr string
		want int64
	}{
		{"1 + 2", 3},
		{"10 - 3", 7},
		{"3 * 4", 12},
		{"10 / 3", 3}, // integer division
		{"11 % 3", 2},
		{"(2 + 3) * 4", 20},
		{"100 - (10 + 5)", 85},
	}
	for _, tc := range cases {
		e, err := parseSingleExpr(t, tc.expr)
		if err != nil {
			t.Errorf("parseSingleExpr(%q): %v", tc.expr, err)
			continue
		}
		folded := FoldConstants(e)
		ic, ok := folded.(*IntegerConst)
		if !ok {
			t.Errorf("FoldConstants(%q): want *IntegerConst, got %T", tc.expr, folded)
			continue
		}
		if ic.Value != tc.want {
			t.Errorf("FoldConstants(%q) = %d, want %d", tc.expr, ic.Value, tc.want)
		}
	}
}

// TestFoldStringConcat verifies string literal concatenation.
func TestFoldStringConcat(t *testing.T) {
	e, err := parseSingleExpr(t, "'hello' || ' ' || 'world'")
	if err != nil {
		t.Fatal(err)
	}
	folded := FoldConstants(e)
	sc, ok := folded.(*StringConst)
	if !ok {
		t.Fatalf("want *StringConst, got %T", folded)
	}
	if sc.Value != "hello world" {
		t.Errorf("got %q, want %q", sc.Value, "hello world")
	}
}

// TestFoldComparisonLiterals verifies that a comparison of two literals is
// reduced to a BooleanConst.
func TestFoldComparisonLiterals(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"1 < 2", true},
		{"2 < 1", false},
		{"3 > 3", false},
		{"3 >= 3", true},
		{"1 <> 2", true},
		{"2 = 2", true},
		{"'a' < 'b'", true},
		{"'b' < 'a'", false},
	}
	for _, tc := range cases {
		e, err := parseSingleExpr(t, tc.expr)
		if err != nil {
			t.Errorf("parseSingleExpr(%q): %v", tc.expr, err)
			continue
		}
		folded := FoldConstants(e)
		bc, ok := folded.(*BooleanConst)
		if !ok {
			t.Errorf("FoldConstants(%q): want *BooleanConst, got %T", tc.expr, folded)
			continue
		}
		if bc.Value != tc.want {
			t.Errorf("FoldConstants(%q) = %v, want %v", tc.expr, bc.Value, tc.want)
		}
	}
}

// TestFoldBooleanShortCircuit verifies AND/OR short-circuit folding.
func TestFoldBooleanShortCircuit(t *testing.T) {
	// helper to build a ColumnRef placeholder (non-foldable)
	col := &ColumnRef{pos: 0, Index: 0, Name: "x", Type: catalog.Type{Name: "bool"}}

	cases := []struct {
		desc string
		expr Expr
		want Expr
	}{
		{
			"TRUE AND col",
			&BinaryOp{Op: parser.OpAnd, Left: &BooleanConst{Value: true}, Right: col},
			col,
		},
		{
			"FALSE AND col",
			&BinaryOp{Op: parser.OpAnd, Left: &BooleanConst{Value: false}, Right: col},
			&BooleanConst{Value: false},
		},
		{
			"col AND TRUE",
			&BinaryOp{Op: parser.OpAnd, Left: col, Right: &BooleanConst{Value: true}},
			col,
		},
		{
			"col AND FALSE",
			&BinaryOp{Op: parser.OpAnd, Left: col, Right: &BooleanConst{Value: false}},
			&BooleanConst{Value: false},
		},
		{
			"TRUE OR col",
			&BinaryOp{Op: parser.OpOr, Left: &BooleanConst{Value: true}, Right: col},
			&BooleanConst{Value: true},
		},
		{
			"FALSE OR col",
			&BinaryOp{Op: parser.OpOr, Left: &BooleanConst{Value: false}, Right: col},
			col,
		},
		{
			"col OR TRUE",
			&BinaryOp{Op: parser.OpOr, Left: col, Right: &BooleanConst{Value: true}},
			&BooleanConst{Value: true},
		},
		{
			"col OR FALSE",
			&BinaryOp{Op: parser.OpOr, Left: col, Right: &BooleanConst{Value: false}},
			col,
		},
	}
	for _, tc := range cases {
		folded := FoldConstants(tc.expr)
		switch want := tc.want.(type) {
		case *BooleanConst:
			got, ok := folded.(*BooleanConst)
			if !ok {
				t.Errorf("%s: want *BooleanConst(%v), got %T", tc.desc, want.Value, folded)
				continue
			}
			if got.Value != want.Value {
				t.Errorf("%s: got BooleanConst(%v), want BooleanConst(%v)", tc.desc, got.Value, want.Value)
			}
		case *ColumnRef:
			if _, ok := folded.(*ColumnRef); !ok {
				t.Errorf("%s: want ColumnRef pass-through, got %T", tc.desc, folded)
			}
		}
	}
}

// TestFoldNullPropagation verifies that NULL propagates through arithmetic and
// comparison operators, and that AND/OR follow Kleene logic.
func TestFoldNullPropagation(t *testing.T) {
	null := &NullConst{}
	one := &IntegerConst{Value: 1}
	trueV := &BooleanConst{Value: true}
	falseV := &BooleanConst{Value: false}

	cases := []struct {
		desc string
		expr Expr
		want string // "*NullConst" or "*BooleanConst(true/false)"
	}{
		{"NULL + 1", &BinaryOp{Op: parser.OpAdd, Left: null, Right: one}, "*NullConst"},
		{"1 + NULL", &BinaryOp{Op: parser.OpAdd, Left: one, Right: null}, "*NullConst"},
		{"NULL = 1", &BinaryOp{Op: parser.OpEq, Left: null, Right: one}, "*NullConst"},
		{"NULL AND FALSE", &BinaryOp{Op: parser.OpAnd, Left: null, Right: falseV}, "*BooleanConst(false)"},
		{"NULL AND TRUE", &BinaryOp{Op: parser.OpAnd, Left: null, Right: trueV}, "*NullConst"},
		{"NULL OR TRUE", &BinaryOp{Op: parser.OpOr, Left: null, Right: trueV}, "*BooleanConst(true)"},
		{"NULL OR FALSE", &BinaryOp{Op: parser.OpOr, Left: null, Right: falseV}, "*NullConst"},
	}
	for _, tc := range cases {
		folded := FoldConstants(tc.expr)
		switch tc.want {
		case "*NullConst":
			if _, ok := folded.(*NullConst); !ok {
				t.Errorf("%s: want *NullConst, got %T", tc.desc, folded)
			}
		case "*BooleanConst(false)":
			if bc, ok := folded.(*BooleanConst); !ok || bc.Value {
				t.Errorf("%s: want BooleanConst(false), got %T(%v)", tc.desc, folded, folded)
			}
		case "*BooleanConst(true)":
			if bc, ok := folded.(*BooleanConst); !ok || !bc.Value {
				t.Errorf("%s: want BooleanConst(true), got %T(%v)", tc.desc, folded, folded)
			}
		}
	}
}

// TestFoldUnaryOps verifies unary operator folding.
func TestFoldUnaryOps(t *testing.T) {
	cases := []struct {
		op    parser.OpCode
		in    Expr
		wantT string
		wantV interface{}
	}{
		{parser.OpUnaryNeg, &IntegerConst{Value: 5}, "*IntegerConst", int64(-5)},
		{parser.OpUnaryNeg, &IntegerConst{Value: -3}, "*IntegerConst", int64(3)},
		{parser.OpUnaryPos, &IntegerConst{Value: 7}, "*IntegerConst", int64(7)},
		{parser.OpNot, &BooleanConst{Value: true}, "*BooleanConst", false},
		{parser.OpNot, &BooleanConst{Value: false}, "*BooleanConst", true},
	}
	for _, tc := range cases {
		folded := FoldConstants(&UnaryOp{Op: tc.op, Operand: tc.in})
		switch tc.wantT {
		case "*IntegerConst":
			ic, ok := folded.(*IntegerConst)
			if !ok {
				t.Errorf("-%v: want IntegerConst, got %T", tc.in, folded)
				continue
			}
			if ic.Value != tc.wantV.(int64) {
				t.Errorf("-%v: got %d, want %d", tc.in, ic.Value, tc.wantV.(int64))
			}
		case "*BooleanConst":
			bc, ok := folded.(*BooleanConst)
			if !ok {
				t.Errorf("NOT: want BooleanConst, got %T", folded)
				continue
			}
			if bc.Value != tc.wantV.(bool) {
				t.Errorf("NOT: got %v, want %v", bc.Value, tc.wantV.(bool))
			}
		}
	}
}

// TestFoldNonLiteralPassThrough verifies that expressions containing
// ColumnRefs are not folded (they're returned unchanged, or with folded
// sub-trees).
func TestFoldNonLiteralPassThrough(t *testing.T) {
	col := &ColumnRef{Index: 0, Name: "x", Type: catalog.Type{Name: "int4"}}
	three := &BinaryOp{Op: parser.OpAdd, Left: &IntegerConst{Value: 1}, Right: &IntegerConst{Value: 2}}
	// x > 1+2  →  x > 3
	expr := &BinaryOp{Op: parser.OpGt, Left: col, Right: three}
	folded := FoldConstants(expr)
	bin, ok := folded.(*BinaryOp)
	if !ok {
		t.Fatalf("want *BinaryOp, got %T", folded)
	}
	if _, ok := bin.Left.(*ColumnRef); !ok {
		t.Errorf("Left: want ColumnRef, got %T", bin.Left)
	}
	ic, ok := bin.Right.(*IntegerConst)
	if !ok {
		t.Errorf("Right: want *IntegerConst, got %T", bin.Right)
	} else if ic.Value != 3 {
		t.Errorf("Right: got %d, want 3", ic.Value)
	}
}

// ── Integration (DoD) test ───────────────────────────────────────────────────

// TestFoldConstantsDoD is the Definition-of-Done test for M0051-0002:
// EXPLAIN SELECT * FROM t WHERE x > 1+2 must produce a Filter whose
// predicate is BinaryOp{">", ColumnRef{x}, IntegerConst{3}} — not
// BinaryOp{">", ColumnRef{x}, BinaryOp{"+",...}}.
func TestFoldConstantsDoD(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(
		parser.ObjectName{Name: "t"},
		[]catalog.Column{{Name: "x", Type: catalog.Type{Name: "int4"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = tbl

	stmts, err := parser.Parse("SELECT * FROM t WHERE x > 1 + 2")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// The plan is Project(Filter(SeqScan(t), x > 3)); unwrap Project.
	if proj, ok := node.(*Project); ok {
		node = proj.Child
	}
	filt, ok := node.(*Filter)
	if !ok {
		t.Fatalf("root node (below Project): want *Filter, got %T", node)
	}
	bin, ok := filt.Predicate.(*BinaryOp)
	if !ok {
		t.Fatalf("predicate: want *BinaryOp, got %T", filt.Predicate)
	}
	if bin.Op != parser.OpGt {
		t.Fatalf("predicate op: want >, got %s", bin.Op)
	}
	if _, ok := bin.Left.(*ColumnRef); !ok {
		t.Errorf("predicate left: want *ColumnRef, got %T", bin.Left)
	}
	ic, ok := bin.Right.(*IntegerConst)
	if !ok {
		t.Errorf("predicate right: want *IntegerConst (folded), got %T — constant folding did not fire", bin.Right)
	} else if ic.Value != 3 {
		t.Errorf("predicate right value: got %d, want 3", ic.Value)
	}
}

// TestFoldTrueAndDoD verifies that TRUE AND x = 1 is simplified to x = 1.
func TestFoldTrueAndDoD(t *testing.T) {
	cat := catalog.NewInMemory()
	_, err := cat.CreateTable(
		parser.ObjectName{Name: "t"},
		[]catalog.Column{{Name: "x", Type: catalog.Type{Name: "int4"}}},
	)
	if err != nil {
		t.Fatal(err)
	}

	stmts, err := parser.Parse("SELECT * FROM t WHERE TRUE AND x = 1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// Unwrap Project if present, then check Filter.
	if proj, ok := node.(*Project); ok {
		node = proj.Child
	}
	filt, ok := node.(*Filter)
	if !ok {
		t.Fatalf("root (below Project): want *Filter, got %T", node)
	}
	// The predicate must not be a BinaryOp with Op="AND"
	if bin, isBin := filt.Predicate.(*BinaryOp); isBin && bin.Op == parser.OpAnd {
		t.Errorf("TRUE AND x=1: AND was not eliminated; predicate is still %T{Op:%s}", bin, bin.Op)
	}
}

// ── CASE constant-folding tests (M0097-0047) ─────────────────────────────────

// TestFoldCaseDeadBranchSuppressesDivisionByZero verifies that a WHEN branch
// whose condition is a known-false constant does NOT fold its THEN body.
// This mirrors PostgreSQL behaviour: unreachable branches are not evaluated at
// plan time, so `CASE WHEN 1=0 THEN 1/0 WHEN 1=1 THEN 1 ELSE 2/0 END` must
// return 1 without throwing a division-by-zero error.
func TestFoldCaseDeadBranchSuppressesDivisionByZero(t *testing.T) {
	// Searched CASE: WHEN 1=0 THEN 1/0 — dead; WHEN 1=1 THEN 1 — live
	e, err := parseSingleExpr(t, "CASE WHEN 1=0 THEN 1/0 WHEN 1=1 THEN 1 ELSE 2/0 END")
	if err != nil {
		t.Fatalf("parseSingleExpr: %v", err)
	}
	folded := FoldConstants(e) // must NOT panic
	ic, ok := folded.(*IntegerConst)
	if !ok {
		t.Fatalf("want *IntegerConst(1), got %T", folded)
	}
	if ic.Value != 1 {
		t.Errorf("got %d, want 1", ic.Value)
	}
}

// TestFoldSimpleCaseDeadBranchSuppressesDivisionByZero verifies the same
// behaviour for a simple CASE (with operand):
// `CASE 1 WHEN 0 THEN 1/0 WHEN 1 THEN 1 ELSE 2/0 END` → 1
func TestFoldSimpleCaseDeadBranchSuppressesDivisionByZero(t *testing.T) {
	e, err := parseSingleExpr(t, "CASE 1 WHEN 0 THEN 1/0 WHEN 1 THEN 1 ELSE 2/0 END")
	if err != nil {
		t.Fatalf("parseSingleExpr: %v", err)
	}
	folded := FoldConstants(e)
	ic, ok := folded.(*IntegerConst)
	if !ok {
		t.Fatalf("want *IntegerConst(1), got %T", folded)
	}
	if ic.Value != 1 {
		t.Errorf("got %d, want 1", ic.Value)
	}
}

// TestFoldPlanConstantsDivisionByZeroPropagates verifies that
// foldPlanConstants returns an error (not a panic) when a potentially-reachable
// THEN body contains a constant division-by-zero.
// Mirrors: SELECT CASE WHEN i > 100 THEN 1/0 ELSE 0 END FROM case_tbl
// which PostgreSQL evaluates at plan time and raises ERROR: division by zero.
func TestFoldPlanConstantsDivisionByZeroPropagates(t *testing.T) {
	cat := catalog.NewInMemory()
	_, err := cat.CreateTable(
		parser.ObjectName{Name: "case_tbl"},
		[]catalog.Column{{Name: "i", Type: catalog.Type{Name: "int4"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	// The THEN `1/0` is under a non-constant condition `i > 100` (ColumnRef),
	// so it is "potentially reachable" and must be folded at plan time → error.
	_, planErr := Plan(
		mustParse(t, "SELECT CASE WHEN i > 100 THEN 1/0 ELSE 0 END FROM case_tbl"),
		cat,
	)
	if planErr == nil {
		t.Fatal("Plan: expected division-by-zero error, got nil")
	}
	pe, ok := planErr.(*PlanError)
	if !ok {
		t.Fatalf("expected *PlanError, got %T: %v", planErr, planErr)
	}
	if pe.Code != "22012" {
		t.Errorf("PlanError.Code = %q, want \"22012\"", pe.Code)
	}
}

// mustParse is a test helper that parses a single SQL statement or fatals.
func mustParse(t *testing.T, sql string) parser.Stmt {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(stmts) == 0 {
		t.Fatalf("Parse(%q): no statements", sql)
	}
	return stmts[0]
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// parseSingleExpr parses a bare SQL expression and converts it to the
// simplest planner-expression equivalent. Uses the parser's expression
// path via a dummy SELECT.
func parseSingleExpr(t *testing.T, expr string) (Expr, error) {
	t.Helper()
	// Wrap in a SELECT so the parser accepts it.
	stmts, err := parser.Parse("SELECT " + expr)
	if err != nil {
		return nil, err
	}
	sel := stmts[0].(*parser.SelectStmt)
	if len(sel.Targets) != 1 {
		return nil, nil
	}
	// Convert the parser expression to a planner expression via resolveExpr.
	resolved, err := resolveExpr(sel.Targets[0].Expr, newResolveContext(nil, nil, DefaultPlannerSettings()))
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

// TestFoldSimpleCaseNullOperandSuppressesDivisionByZero pins the NULL-operand
// case: `CASE NULL::int WHEN 1 THEN 1/0 WHEN 2 THEN 2/0 ELSE 42 END` tests
// `NULL = 1` / `NULL = 2`, which are NULL, so every THEN is dead and PG 18.3
// returns 42 (verified against the oracle). Before review/260831-2 OP2-1 the
// simple-CASE shortcut was skipped for a NULL operand — toLiteralValue does not
// cover *NullConst — and both dead THEN bodies were folded, raising a plan-time
// division by zero.
func TestFoldSimpleCaseNullOperandSuppressesDivisionByZero(t *testing.T) {
	for _, sql := range []string{
		"CASE NULL::int WHEN 1 THEN 1/0 WHEN 2 THEN 2/0 ELSE 42 END",
		"CASE NULL WHEN 1 THEN 1/0 ELSE 42 END",
	} {
		e, err := parseSingleExpr(t, sql)
		if err != nil {
			t.Fatalf("parseSingleExpr(%q): %v", sql, err)
		}
		folded := FoldConstants(e) // must NOT panic
		ic, ok := folded.(*IntegerConst)
		if !ok {
			t.Fatalf("%s: want *IntegerConst(42), got %T", sql, folded)
		}
		if ic.Value != 42 {
			t.Errorf("%s: got %d, want 42", sql, ic.Value)
		}
	}
}
