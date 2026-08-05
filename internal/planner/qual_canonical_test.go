package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0127-P5.6-g-iv. These tests pin `canonicalizeQual` — goopg's
// `find_duplicate_ors`/`process_duplicate_ors` (prepqual.c) — against the four
// behaviours upstream's function is specified by, plus the one goopg-specific
// hazard the strict expression key exists to prevent.
//
// The estimator consequence, which is why the pass was written, is covered
// separately by TestCanonicalizeQualHoistsQ19JoinClause below: after the
// rewrite the join clause is a TOP-LEVEL conjunct, which is the precondition
// for `joinResidualSelectivity` excluding it from the residual instead of
// charging DEFAULT_EQ_SEL for it a second time inside every OR arm.

func qcCol(table, name string) parser.Expr {
	return &parser.ColumnRef{Table: table, Column: name}
}

func qcEq(l, r parser.Expr) parser.Expr {
	return &parser.BinaryOp{Op: parser.OpEq, Left: l, Right: r}
}

func qcInt(v int64) parser.Expr {
	return &parser.IntegerConst{Value: v}
}

func qcStr(v string) parser.Expr {
	return &parser.StringConst{Value: v}
}

func qcAnd(parts ...parser.Expr) parser.Expr { return andChain(parts) }
func qcOr(parts ...parser.Expr) parser.Expr  { return orChain(parts) }

// qcShape renders an expression as a comparable structural string. It uses the
// STRICT key deliberately: a shape assertion that normalised table qualifiers
// away could not tell the qualifier-safety test's two operands apart.
func qcShape(e parser.Expr) string { return strictParserExprKey(e) }

func qcSame(t *testing.T, got, want parser.Expr) {
	t.Helper()
	if qcShape(got) != qcShape(want) {
		t.Fatalf("canonicalizeQual mismatch\n got: %s\nwant: %s", qcShape(got), qcShape(want))
	}
}

// The headline transform: (A ∧ B) ∨ (A ∧ C) → A ∧ (B ∨ C).
func TestCanonicalizeQualHoistsCommonConjunct(t *testing.T) {
	a := qcEq(qcCol("t", "x"), qcCol("u", "y"))
	b := qcEq(qcCol("t", "b"), qcInt(1))
	c := qcEq(qcCol("t", "b"), qcInt(2))

	got := canonicalizeQual(qcOr(qcAnd(a, b), qcAnd(a, c)))
	qcSame(t, got, qcAnd(a, qcOr(b, c)))
}

// Three arms, three winners of different node types — the TPC-H Q19 shape.
// The join clause, the IN list and the string equality are all common; only
// the brand/container/quantity conditions differ.
func TestCanonicalizeQualHoistsQ19JoinClause(t *testing.T) {
	join := qcEq(qcCol("", "p_partkey"), qcCol("", "l_partkey"))
	mode := &parser.InExpr{
		Operand: qcCol("", "l_shipmode"),
		List:    []parser.Expr{qcStr("AIR"), qcStr("AIR REG")},
	}
	instruct := qcEq(qcCol("", "l_shipinstruct"), qcStr("DELIVER IN PERSON"))
	brand := func(n string) parser.Expr { return qcEq(qcCol("", "p_brand"), qcStr(n)) }

	where := qcOr(
		qcAnd(join, brand("Brand#12"), mode, instruct),
		qcAnd(join, brand("Brand#23"), mode, instruct),
		qcAnd(join, brand("Brand#34"), mode, instruct),
	)
	got := canonicalizeQual(where)

	// Every winner must now be a TOP-LEVEL conjunct — that is the property the
	// estimator and the qual-pushdown passes both read.
	top := map[string]bool{}
	walkConjuncts(got, func(c parser.Expr) { top[qcShape(c)] = true })
	for _, want := range []parser.Expr{join, mode, instruct} {
		if !top[qcShape(want)] {
			t.Fatalf("conjunct not hoisted to top level: %s\ngot: %s", qcShape(want), qcShape(got))
		}
	}
	// ...and it must no longer appear inside the reduced OR, or the
	// double-count this pass exists to remove would survive the rewrite.
	var or parser.Expr
	walkConjuncts(got, func(c parser.Expr) {
		if bin, ok := c.(*parser.BinaryOp); ok && bin.Op == parser.OpOr {
			or = c
		}
	})
	if or == nil {
		t.Fatal("reduced OR disappeared; the three brand arms are not interchangeable")
	}
	for _, br := range flattenOrBranches(or) {
		for _, c := range flattenAndBranches(br) {
			if qcShape(c) == qcShape(join) {
				t.Fatalf("join clause still inside the OR: %s", qcShape(or))
			}
		}
	}
}

// `(A ∧ B) ∨ A` reduces to A: the extra conditions in the other arms cannot
// exclude anything A already admits. Upstream's "degenerate case".
func TestCanonicalizeQualDegenerateArmCollapsesToWinners(t *testing.T) {
	a := qcEq(qcCol("t", "x"), qcInt(1))
	b := qcEq(qcCol("t", "y"), qcInt(2))

	qcSame(t, canonicalizeQual(qcOr(qcAnd(a, b), a)), a)
	// Two winners, one degenerate arm: the AND of both winners survives.
	c := qcEq(qcCol("t", "z"), qcInt(3))
	qcSame(t, canonicalizeQual(qcOr(qcAnd(a, b, c), qcAnd(a, c))), qcAnd(a, c))
}

// No conjunct is in every arm, so the OR is returned unchanged. This is the
// common case for real queries and must not be disturbed.
func TestCanonicalizeQualNoWinnersLeavesOrIntact(t *testing.T) {
	a := qcEq(qcCol("t", "x"), qcInt(1))
	b := qcEq(qcCol("t", "y"), qcInt(2))
	c := qcEq(qcCol("t", "z"), qcInt(3))

	in := qcOr(qcAnd(a, b), qcAnd(b, c), a)
	qcSame(t, canonicalizeQual(in), in)
}

// The hazard `strictParserExprKey` exists for: parserExprKey drops a
// ColumnRef's table qualifier (M0097-0003), under which `a.x = 1` and
// `b.x = 1` would compare equal. Hoisting one of them would rewrite a qual
// that admits rows from either table into one that demands both — silently
// losing rows on any query with an OR over two aliases of similar columns.
func TestCanonicalizeQualDoesNotHoistAcrossTableQualifiers(t *testing.T) {
	ax := qcEq(qcCol("a", "x"), qcInt(1))
	bx := qcEq(qcCol("b", "x"), qcInt(1))
	p := qcEq(qcCol("a", "p"), qcInt(7))
	q := qcEq(qcCol("b", "q"), qcInt(8))

	in := qcOr(qcAnd(ax, p), qcAnd(bx, q))
	qcSame(t, canonicalizeQual(in), in)
}

// Idempotence: the pass runs on the output of an earlier pass in several code
// paths (a canonicalised qual can be re-planned), and a second application
// must be a no-op rather than re-associating the tree differently.
func TestCanonicalizeQualIsIdempotent(t *testing.T) {
	a := qcEq(qcCol("t", "x"), qcCol("u", "y"))
	b := qcEq(qcCol("t", "b"), qcInt(1))
	c := qcEq(qcCol("t", "b"), qcInt(2))

	once := canonicalizeQual(qcOr(qcAnd(a, b), qcAnd(a, c)))
	qcSame(t, canonicalizeQual(once), once)
}

// An OR nested under an AND is reached, and an inner OR is reduced before the
// outer level intersects arms — upstream recurses first for the same reason.
func TestCanonicalizeQualRecursesUnderAnd(t *testing.T) {
	guard := qcEq(qcCol("t", "g"), qcInt(0))
	a := qcEq(qcCol("t", "x"), qcCol("u", "y"))
	b := qcEq(qcCol("t", "b"), qcInt(1))
	c := qcEq(qcCol("t", "b"), qcInt(2))

	got := canonicalizeQual(qcAnd(guard, qcOr(qcAnd(a, b), qcAnd(a, c))))
	qcSame(t, got, qcAnd(guard, qcAnd(a, qcOr(b, c))))
}

// A nil qual is the "no WHERE clause" case; callers apply the pass
// unconditionally and must get nil back rather than a panic.
func TestCanonicalizeQualNilAndNonBoolean(t *testing.T) {
	if got := canonicalizeQual(nil); got != nil {
		t.Fatalf("canonicalizeQual(nil) = %v, want nil", got)
	}
	leaf := qcEq(qcCol("t", "x"), qcInt(1))
	if got := canonicalizeQual(leaf); got != leaf {
		t.Fatalf("canonicalizeQual returned a copy of a leaf qual; want the original node")
	}
}
