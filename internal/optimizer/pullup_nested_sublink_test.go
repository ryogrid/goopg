package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// nestedSublinkQual builds `col <op> <sublink>` where the sublink is whichever
// expression node the caller wants. The sublink needs a non-nil plan for
// `ExprSubplans` to see it at all — an unplanned sublink node is invisible to
// every census and gate in this file.
func nestedSublinkQual(t *testing.T, sub Expr) Expr {
	t.Helper()
	return &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0, Name: "c"}, Right: sub}
}

// TestBodyQualsAdmitSublinks pins M0145-0014's split of the old blanket
// `nested-sublink` refusal.
//
// The refusal was over-broad against the oracle: PG converts the OUTER sublink
// first (`convert_ANY_sublink_to_join` gates only on correlation and
// volatility) and only then recurses on the pulled-up quals, so a nested
// sublink PG would not convert never blocks the outer conversion — it just
// stays a SubPlan. The two halves need different machinery, so they get
// different reasons and the census can count them apart.
func TestBodyQualsAdmitSublinks(t *testing.T) {
	plain := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0, Name: "c"}, Right: &IntegerConst{Value: 1}}

	t.Run("no sublink is admitted", func(t *testing.T) {
		if why, ok := bodyQualsAdmitSublinks(plain, nil); !ok {
			t.Fatalf("a plain qual must be admitted, got %q", why)
		}
	})

	t.Run("scalar sublink is admitted", func(t *testing.T) {
		scalar := &SubqueryExpr{Plan: &SeqScan{}}
		q := nestedSublinkQual(t, scalar)
		if !exprHasSublinkPlan(q) {
			t.Fatalf("fixture is wrong: the qual must carry a subplan or the gate is untested")
		}
		if why, ok := bodyQualsAdmitSublinks(q, nil); !ok {
			t.Fatalf("a NON-convertible nested sublink must ride along as a SubPlan, "+
				"exactly as it does in PG; got %q", why)
		}
	})

	t.Run("convertible nested sublink is declined and named", func(t *testing.T) {
		nested := &InExpr{Plan: &SeqScan{}}
		q := nestedSublinkQual(t, nested)
		why, ok := bodyQualsAdmitSublinks(q, nil)
		if ok {
			t.Fatalf("a nested ANY must stay declined — converting it is the recursion " +
				"M0145-0014 is named for, and that machinery is not built")
		}
		if why != "nested-sublink-convertible" {
			t.Fatalf("reason = %q, want nested-sublink-convertible — the census counts this string", why)
		}
	})

	t.Run("onQuals are checked too", func(t *testing.T) {
		nested := &ExistsExpr{Plan: &SeqScan{}}
		if why, ok := bodyQualsAdmitSublinks(nil, []Expr{nestedSublinkQual(t, nested)}); ok {
			t.Fatalf("an ON qual carrying a convertible sublink must decline, got ok (why=%q)", why)
		}
	})
}

// TestExprHasConvertibleSublink pins WHICH sublink kinds count as convertible.
// The list is not "every sublink": `pull_up_sublinks_qual_recurse` converts
// ANY and EXISTS and nothing else, so a scalar subquery must answer false or
// the admission above collapses back into the blanket refusal.
func TestExprHasConvertibleSublink(t *testing.T) {
	cases := []struct {
		name string
		expr Expr
		want bool
	}{
		{"ANY", &InExpr{Plan: &SeqScan{}}, true},
		{"EXISTS", &ExistsExpr{Plan: &SeqScan{}}, true},
		{"scalar", &SubqueryExpr{Plan: &SeqScan{}}, false},
		{"no sublink", &IntegerConst{Value: 1}, false},
	}
	for _, tc := range cases {
		if got := exprHasConvertibleSublink(tc.expr); got != tc.want {
			t.Errorf("%s: exprHasConvertibleSublink = %v, want %v", tc.name, got, tc.want)
		}
	}
}
