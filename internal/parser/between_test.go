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
	if !ok || andOp.Op != OpAnd {
		t.Fatalf("WHERE root=%T(%v), want BinaryOp{AND}", sel.Where, sel.Where)
	}
	ge, ok := andOp.Left.(*BinaryOp)
	if !ok || ge.Op != OpGe {
		t.Errorf("AND.Left=%T(%v), want BinaryOp{>=}", andOp.Left, andOp.Left)
	}
	le, ok := andOp.Right.(*BinaryOp)
	if !ok || le.Op != OpLe {
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
	if !ok || notOp.Op != OpNot {
		t.Fatalf("WHERE root=%T(%v), want UnaryOp{NOT}", sel.Where, sel.Where)
	}
	andOp, ok := notOp.Operand.(*BinaryOp)
	if !ok || andOp.Op != OpAnd {
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
	if !ok || root.Op != OpAnd {
		t.Fatalf("WHERE root=%T(%v), want top-level AND", sel.Where, sel.Where)
	}
	// Left side is the BETWEEN desugar — itself an AND of two
	// comparisons.
	if _, ok := root.Left.(*BinaryOp); !ok {
		t.Errorf("expected nested AND on left, got %T", root.Left)
	}
	// Right side is the y > 5 comparison.
	gt, ok := root.Right.(*BinaryOp)
	if !ok || gt.Op != OpGt {
		t.Errorf("AND.Right=%T(%v), want BinaryOp{>}", root.Right, root.Right)
	}
}

// TestParseBetweenSymmetricDesugar pins `expr BETWEEN SYMMETRIC low AND
// high`'s rewrite to `(expr>=low AND expr<=high) OR (expr>=high AND
// expr<=low)` — SYMMETRIC drops the requirement that low <= high (upstream
// gram.y desugars identically).
func TestParseBetweenSymmetricDesugar(t *testing.T) {
	stmts, err := Parse("SELECT * FROM t WHERE x BETWEEN SYMMETRIC 10 AND 1")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	orOp, ok := sel.Where.(*BinaryOp)
	if !ok || orOp.Op != OpOr {
		t.Fatalf("WHERE root=%T(%v), want BinaryOp{OR}", sel.Where, sel.Where)
	}
	for _, side := range []Expr{orOp.Left, orOp.Right} {
		andOp, ok := side.(*BinaryOp)
		if !ok || andOp.Op != OpAnd {
			t.Errorf("OR side=%T(%v), want BinaryOp{AND}", side, side)
			continue
		}
		if ge, ok := andOp.Left.(*BinaryOp); !ok || ge.Op != OpGe {
			t.Errorf("AND.Left=%T(%v), want BinaryOp{>=}", andOp.Left, andOp.Left)
		}
		if le, ok := andOp.Right.(*BinaryOp); !ok || le.Op != OpLe {
			t.Errorf("AND.Right=%T(%v), want BinaryOp{<=}", andOp.Right, andOp.Right)
		}
	}
}

// TestParseNotBetweenSymmetricDesugar pins the negated form: the whole
// SYMMETRIC OR-of-two-orderings expansion is wrapped in a single NOT.
func TestParseNotBetweenSymmetricDesugar(t *testing.T) {
	stmts, err := Parse("SELECT * FROM t WHERE x NOT BETWEEN SYMMETRIC 10 AND 1")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	notOp, ok := sel.Where.(*UnaryOp)
	if !ok || notOp.Op != OpNot {
		t.Fatalf("WHERE root=%T(%v), want UnaryOp{NOT}", sel.Where, sel.Where)
	}
	if orOp, ok := notOp.Operand.(*BinaryOp); !ok || orOp.Op != OpOr {
		t.Errorf("NOT.Operand=%T(%v), want BinaryOp{OR}", notOp.Operand, notOp.Operand)
	}
}

// TestParseBetweenAsymmetricDesugar pins ASYMMETRIC as an explicit,
// accepted-but-no-op spelling of the plain (ordered) BETWEEN form.
func TestParseBetweenAsymmetricDesugar(t *testing.T) {
	stmts, err := Parse("SELECT * FROM t WHERE x BETWEEN ASYMMETRIC 1 AND 10")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	andOp, ok := sel.Where.(*BinaryOp)
	if !ok || andOp.Op != OpAnd {
		t.Fatalf("WHERE root=%T(%v), want BinaryOp{AND}", sel.Where, sel.Where)
	}
	if ge, ok := andOp.Left.(*BinaryOp); !ok || ge.Op != OpGe {
		t.Errorf("AND.Left=%T(%v), want BinaryOp{>=}", andOp.Left, andOp.Left)
	}
	if le, ok := andOp.Right.(*BinaryOp); !ok || le.Op != OpLe {
		t.Errorf("AND.Right=%T(%v), want BinaryOp{<=}", andOp.Right, andOp.Right)
	}
}
