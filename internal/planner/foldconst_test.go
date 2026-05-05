package planner

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
			&BinaryOp{Op: "AND", Left: &BooleanConst{Value: true}, Right: col},
			col,
		},
		{
			"FALSE AND col",
			&BinaryOp{Op: "AND", Left: &BooleanConst{Value: false}, Right: col},
			&BooleanConst{Value: false},
		},
		{
			"col AND TRUE",
			&BinaryOp{Op: "AND", Left: col, Right: &BooleanConst{Value: true}},
			col,
		},
		{
			"col AND FALSE",
			&BinaryOp{Op: "AND", Left: col, Right: &BooleanConst{Value: false}},
			&BooleanConst{Value: false},
		},
		{
			"TRUE OR col",
			&BinaryOp{Op: "OR", Left: &BooleanConst{Value: true}, Right: col},
			&BooleanConst{Value: true},
		},
		{
			"FALSE OR col",
			&BinaryOp{Op: "OR", Left: &BooleanConst{Value: false}, Right: col},
			col,
		},
		{
			"col OR TRUE",
			&BinaryOp{Op: "OR", Left: col, Right: &BooleanConst{Value: true}},
			&BooleanConst{Value: true},
		},
		{
			"col OR FALSE",
			&BinaryOp{Op: "OR", Left: col, Right: &BooleanConst{Value: false}},
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
		{"NULL + 1", &BinaryOp{Op: "+", Left: null, Right: one}, "*NullConst"},
		{"1 + NULL", &BinaryOp{Op: "+", Left: one, Right: null}, "*NullConst"},
		{"NULL = 1", &BinaryOp{Op: "=", Left: null, Right: one}, "*NullConst"},
		{"NULL AND FALSE", &BinaryOp{Op: "AND", Left: null, Right: falseV}, "*BooleanConst(false)"},
		{"NULL AND TRUE", &BinaryOp{Op: "AND", Left: null, Right: trueV}, "*NullConst"},
		{"NULL OR TRUE", &BinaryOp{Op: "OR", Left: null, Right: trueV}, "*BooleanConst(true)"},
		{"NULL OR FALSE", &BinaryOp{Op: "OR", Left: null, Right: falseV}, "*NullConst"},
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
		op   string
		in   Expr
		wantT string
		wantV interface{}
	}{
		{"-", &IntegerConst{Value: 5}, "*IntegerConst", int64(-5)},
		{"-", &IntegerConst{Value: -3}, "*IntegerConst", int64(3)},
		{"+", &IntegerConst{Value: 7}, "*IntegerConst", int64(7)},
		{"NOT", &BooleanConst{Value: true}, "*BooleanConst", false},
		{"NOT", &BooleanConst{Value: false}, "*BooleanConst", true},
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
	three := &BinaryOp{Op: "+", Left: &IntegerConst{Value: 1}, Right: &IntegerConst{Value: 2}}
	// x > 1+2  →  x > 3
	expr := &BinaryOp{Op: ">", Left: col, Right: three}
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
	if bin.Op != ">" {
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
	if bin, isBin := filt.Predicate.(*BinaryOp); isBin && bin.Op == "AND" {
		t.Errorf("TRUE AND x=1: AND was not eliminated; predicate is still %T{Op:%s}", bin, bin.Op)
	}
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
	resolved, err := resolveExpr(sel.Targets[0].Expr, newResolveContext(nil, nil))
	if err != nil {
		return nil, err
	}
	return resolved, nil
}
