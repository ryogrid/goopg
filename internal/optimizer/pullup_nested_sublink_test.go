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

// TestJointreePullupNestedExistsIsPulled is the positive counterpart to the
// case M0145-0014 removed from TestJointreePullupDeclineParity: an EXISTS
// whose own body WHERE carries another EXISTS is now pulled up, both levels,
// instead of declining.
//
// It asserts the CENSUS outcome rather than a plan shape. The shape is the
// search's business and is covered corpus-wide by the SF0.25 gate; what this
// task owns is that the recursion fires and that nothing declines on the way
// — a body that is pulled and then refused downstream is strictly worse than
// one never pulled, because the `pulled` mark has already suppressed the
// legacy route (the TPC-H Q4 10x shape).
func TestJointreePullupNestedExistsIsPulled(t *testing.T) {
	cat := jtpCatalog(t)
	const sql = `select tag from jtp_o where exists (select 1 from jtp_i a where a.j = k and exists (select 1 from jtp_i2 b where b.j2 = a.v))`
	sel, ok := parseOne(t, sql).(*parser.SelectStmt)
	if !ok {
		t.Fatalf("stmt is not *parser.SelectStmt")
	}
	ps := DefaultPlannerSettings()
	node, ctx, err := planFromClause(sel, cat, ps, nil)
	if err != nil || node == nil || ctx == nil {
		t.Fatalf("planFromClause: node=%v ctx=%v err=%v", node, ctx, err)
	}
	ctx.cat = cat
	ctx.settings = ps
	where, err := resolveExpr(sel.Where, ctx)
	if err != nil {
		t.Fatalf("resolveExpr: %v", err)
	}
	pu := pullUpSublinksIntoJointree(where, ctx, cat, ps)
	if pu == nil {
		t.Fatalf("pull-up declined the statement entirely")
	}
	if len(pu.bodies) != 2 {
		t.Fatalf("pulled %d bodies, want 2 (the outer EXISTS and the nested one)", len(pu.bodies))
	}
	outer, inner := pu.bodies[0], pu.bodies[1]
	if inner.parent != outer {
		t.Fatalf("body[1].parent = %p, want body[0] (%p): flattenPulledBodies must stamp the chain "+
			"or a nested Level-1 reference has no coordinate path", inner.parent, outer)
	}
	if outer.subtreeLeaves != 2 {
		t.Fatalf("outer.subtreeLeaves = %d, want 2. syn_righthand must cover the SUBTREE: PG splices a "+
			"nested conversion into the parent's j->rarg, and without that the search may complete the "+
			"parent semijoin first — discarding the parent body's columns, which a semijoin does not "+
			"project — and then have nowhere to read the nested link qual's parent column from",
			outer.subtreeLeaves)
	}
	if inner.subtreeLeaves != 1 {
		t.Fatalf("inner.subtreeLeaves = %d, want 1", inner.subtreeLeaves)
	}
	if len(pu.bodies[0].children) != 0 || len(pu.bodies[1].children) != 0 {
		t.Fatalf("children must be cleared after flattening — two representations of one tree double-count leaves")
	}
}
