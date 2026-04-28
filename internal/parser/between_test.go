package parser

import (
	"testing"
)

// TestParseBetweenDesugar pins the BETWEEN parser-side rewrite:
// `expr BETWEEN low AND high` becomes `(expr >= low) AND
// (expr <= high)` — a BinaryOp{AND} whose children are two
// comparison BinaryOps. NOT BETWEEN wraps the same shape in
// UnaryOp{NOT}. We desugar at parse time so downstream passes
// (analyzer, planner, executor) don't need a dedicated BETWEEN
// node. Required for TPC-H Q6 (`l_discount BETWEEN :1 - 0.01
// AND :1 + 0.01`).
func TestParseBetweenDesugar(t *testing.T) {
	stmts, err := Parse("SELECT * FROM t WHERE x BETWEEN 1 AND 10")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	andOp, ok := sel.Where.(*BinaryOp)
	if !ok || andOp.Op != "AND" {
		t.Fatalf("WHERE root=%T(%v), want BinaryOp{AND}", sel.Where, sel.Where)
	}
	ge, ok := andOp.Left.(*BinaryOp)
	if !ok || ge.Op != ">=" {
		t.Errorf("AND.Left=%T(%v), want BinaryOp{>=}", andOp.Left, andOp.Left)
	}
	le, ok := andOp.Right.(*BinaryOp)
	if !ok || le.Op != "<=" {
		t.Errorf("AND.Right=%T(%v), want BinaryOp{<=}", andOp.Right, andOp.Right)
	}
}

func TestParseNotBetweenDesugar(t *testing.T) {
	stmts, err := Parse("SELECT * FROM t WHERE x NOT BETWEEN 1 AND 10")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	notOp, ok := sel.Where.(*UnaryOp)
	if !ok || notOp.Op != "NOT" {
		t.Fatalf("WHERE root=%T(%v), want UnaryOp{NOT}", sel.Where, sel.Where)
	}
	andOp, ok := notOp.Operand.(*BinaryOp)
	if !ok || andOp.Op != "AND" {
		t.Errorf("NOT.Operand=%T(%v), want BinaryOp{AND}", notOp.Operand, notOp.Operand)
	}
}

// TestParseBetweenWithTrailingAnd guards against the rhs of `low`
// gobbling the BETWEEN's `AND high` separator. The construct
// `BETWEEN a AND b AND c` must parse as `(a BETWEEN ... AND b) AND c`,
// i.e. `c` lands as a top-level conjunct.
func TestParseBetweenWithTrailingAnd(t *testing.T) {
	stmts, err := Parse("SELECT * FROM t WHERE x BETWEEN 1 AND 10 AND y > 5")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	// Top-level should be `((BETWEEN-tree) AND (y > 5))`.
	root, ok := sel.Where.(*BinaryOp)
	if !ok || root.Op != "AND" {
		t.Fatalf("WHERE root=%T(%v), want top-level AND", sel.Where, sel.Where)
	}
	// Left side is the BETWEEN desugar — itself an AND of two
	// comparisons.
	if _, ok := root.Left.(*BinaryOp); !ok {
		t.Errorf("expected nested AND on left, got %T", root.Left)
	}
	// Right side is the y > 5 comparison.
	gt, ok := root.Right.(*BinaryOp)
	if !ok || gt.Op != ">" {
		t.Errorf("AND.Right=%T(%v), want BinaryOp{>}", root.Right, root.Right)
	}
}
