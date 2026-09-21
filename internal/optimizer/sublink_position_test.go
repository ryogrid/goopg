package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestSublinkConjunctPosition pins the M0145-0015 census classifier.
//
// The position is what decides whether a decline is a MISS or a correct one:
// `pull_up_sublinks_qual_recurse` recurses through AND and through a NOT
// wrapper (postgres/src/backend/optimizer/prep/prepjointree.c:789-845) and
// stops at every other clause type, OR args included (":877 — Stop if not an
// AND"). So `ExistsExpr@or` matches upstream and `ExistsExpr@top` would be a
// real gap. A census that reported only the sublink kind could not tell those
// apart, and the 2026-09-21 corpus run turns entirely on the distinction: all
// 35 residual declines are `@or` or `@scalar`, and NONE is `@top` or `@not`.
func TestSublinkConjunctPosition(t *testing.T) {
	ex := func() Expr { return &ExistsExpr{Plan: &SeqScan{}} }
	col := func() Expr { return &ColumnRef{Index: 0, Name: "c"} }

	cases := []struct {
		name string
		expr Expr
		want string
	}{
		{"bare sublink is top", ex(), "top"},
		{"NOT-wrapped is not", &UnaryOp{Op: parser.OpNot, Operand: ex()}, "not"},
		{"under OR is or", &BinaryOp{Op: parser.OpOr, Left: col(), Right: ex()}, "or"},
		{
			// An AND below the conjunct root is still conjunct position —
			// splitAnd would have separated it had the caller split deeper,
			// so it must NOT be demoted to "scalar".
			"under AND stays top",
			&BinaryOp{Op: parser.OpAnd, Left: col(), Right: ex()},
			"top",
		},
		{
			"inside a comparison is scalar",
			&BinaryOp{Op: parser.OpGt, Left: col(), Right: &SubqueryExpr{Plan: &SeqScan{}}},
			"scalar",
		},
		{
			// An OR ABOVE outranks a NOT below it: the outermost non-AND
			// wrapper is what stops upstream's recursion, so the sublink is
			// unreachable regardless of what sits between.
			"OR above NOT reports or",
			&BinaryOp{Op: parser.OpOr, Left: col(), Right: &UnaryOp{Op: parser.OpNot, Operand: ex()}},
			"or",
		},
		{"no sublink at all", col(), "unknown"},
	}
	for _, tc := range cases {
		if got := sublinkConjunctPosition(tc.expr); got != tc.want {
			t.Errorf("%s: position = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestSublinkConjunctSiteJoinsKindAndPosition pins the census line format.
// The census counts these strings; a silent rename would report the residual
// as having moved when nothing did.
func TestSublinkConjunctSiteJoinsKindAndPosition(t *testing.T) {
	c := &BinaryOp{Op: parser.OpOr, Left: &ColumnRef{Index: 0, Name: "c"}, Right: &ExistsExpr{Plan: &SeqScan{}}}
	if got, want := sublinkConjunctSite(c), "ExistsExpr@or"; got != want {
		t.Fatalf("site = %q, want %q", got, want)
	}
	if got := sublinkConjunctSite(&ColumnRef{Index: 0, Name: "c"}); got != "" {
		t.Fatalf("a sublink-free conjunct must report nothing, got %q — an ordinary `a = 1` is not a missed pull-up", got)
	}
}
