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
		if why, ok := bodyQualsAdmitSublinks(plain, nil, 0, nil); !ok {
			t.Fatalf("a plain qual must be admitted, got %q", why)
		}
	})

	t.Run("scalar sublink is admitted", func(t *testing.T) {
		scalar := &SubqueryExpr{Plan: &SeqScan{}}
		q := nestedSublinkQual(t, scalar)
		if !exprHasSublinkPlan(q) {
			t.Fatalf("fixture is wrong: the qual must carry a subplan or the gate is untested")
		}
		if why, ok := bodyQualsAdmitSublinks(q, nil, 0, nil); !ok {
			t.Fatalf("a NON-convertible nested sublink must ride along as a SubPlan, "+
				"exactly as it does in PG; got %q", why)
		}
	})

	// M0146-0015c slice 2: a nested ANY/EXISTS is no longer declined out of
	// hand. extractNestedPullups already tried — and failed — to convert it;
	// what remains rides as a SubPlan in the pulled qual with its escaping
	// correlation re-based into problem space, which is exactly what PG does.
	// Admission is therefore about the kept plan, not the sublink kind.
	t.Run("convertible nested sublink is admitted as a kept subplan", func(t *testing.T) {
		nested := &InExpr{Plan: &SeqScan{}}
		q := nestedSublinkQual(t, nested)
		if why, ok := bodyQualsAdmitSublinks(q, nil, 0, nil); !ok {
			t.Fatalf("an uncorrelated nested ANY rides as a kept SubPlan; got %q", why)
		}
	})

	t.Run("a kept sublink escaping past the emitting scope declines", func(t *testing.T) {
		// Level 3 at body depth 0 lands above the emitting statement —
		// hops = 3 - 0 - 1 = 2 > depth + 1 = 1.
		nested := &ExistsExpr{Plan: &Filter{
			Predicate: &BinaryOp{Op: parser.OpEq,
				Left:  &OuterColumnRef{Level: 3},
				Right: &IntegerConst{Value: 1}},
		}}
		why, ok := bodyQualsAdmitSublinks(nestedSublinkQual(t, nested), nil, 0, nil)
		if ok || why != "nested-sublink-deep-ref" {
			t.Fatalf("a ref escaping above the emitting scope must decline "+
				"nested-sublink-deep-ref, got (%q, %v)", why, ok)
		}
	})

	t.Run("an uncloneable kept plan declines", func(t *testing.T) {
		// *Distinct is outside planCloneSupported's node set — the pulled
		// arm cannot take a plan it would have to share with the fallback.
		nested := &ExistsExpr{Plan: &Distinct{}}
		why, ok := bodyQualsAdmitSublinks(nestedSublinkQual(t, nested), nil, 0, nil)
		if ok || why != "nested-sublink-uncloneable" {
			t.Fatalf("an uncloneable kept plan must decline "+
				"nested-sublink-uncloneable, got (%q, %v)", why, ok)
		}
	})

	t.Run("onQuals are checked too", func(t *testing.T) {
		nested := &ExistsExpr{Plan: &Filter{
			Predicate: &BinaryOp{Op: parser.OpEq,
				Left:  &OuterColumnRef{Level: 3},
				Right: &IntegerConst{Value: 1}},
		}}
		if why, ok := bodyQualsAdmitSublinks(nil, []Expr{nestedSublinkQual(t, nested)}, 0, nil); ok {
			t.Fatalf("an ON qual carrying an escaping kept sublink must decline, got ok (why=%q)", why)
		}
	})
}

// TestJointreePullupKeepsNestedExists is M0146-0015c's core pin: the
// oracle shape `a … EXISTS (b … EXISTS (c WHERE c.x = a.y AND c.z = b.w))`
// must plan the OUTER EXISTS as a semi join (PG: `Hash Semi Join … Join
// Filter: (ANY ((a.y = (hashed SubPlan N).col1) AND (b.w = (hashed SubPlan
// N).col2)))`). Slice 2 carried the inner EXISTS as a pre-lowered SubPlan;
// slice 3's keptExistsToAny then applies PG's convert_EXISTS_to_ANY: every
// escaping ref is consumed by an `innercol = sentinel` conjunct, so the
// kept SubPlan dissolves into an uncorrelated row-ANY — operand the row of
// correlated columns, plan a Project over the inner equality columns, and
// UnknownEqFalse the two-valued licence PG stamps as unknownEqFalse.
func TestJointreePullupKeepsNestedExists(t *testing.T) {
	cat := jtpCatalog(t)
	delete(sublinkRouteCounts, spineRoutePosthoc)
	delete(sublinkRouteCounts, spineRouteJointree)
	// j=jtp_i.j, k=jtp_o.k, v=jtp_i.v; inside c: w=j is parent-scope,
	// j2=k is emitting-scope — the two-scope correlation the whole
	// slice exists to carry.
	node := planOnPipeline(t,
		`select tag from jtp_o where exists (select 1 from jtp_i a where a.j = k `+
			`and exists (select 1 from jtp_i2 b where b.j2 = k and b.w = a.v))`, cat)
	if n := sublinkRouteCounts[spineRouteJointree]; n < 1 {
		t.Fatalf("jointree pull-up never engaged; tree: %s", describePlanTree(node))
	}
	j := findSemiOrAntiJoin(node)
	if j == nil || j.Type != JoinTypeSemi {
		t.Fatalf("no semi join for the outer EXISTS; tree: %s", describePlanTree(node))
	}
	// The kept EXISTS must have converted in the semi join's predicate:
	// an InExpr — not an ExistsExpr — whose correlation is fully
	// dissolved into the row operand.
	var kept *InExpr
	var stray *ExistsExpr
	walkExprTree(j.Predicate, func(e Expr) {
		if in, isIn := e.(*InExpr); isIn && in.Plan != nil {
			kept = in
		}
		if ex, isEx := e.(*ExistsExpr); isEx {
			stray = ex
		}
	})
	if kept == nil {
		t.Fatalf("no converted ANY in the semi join predicate; tree: %s", describePlanTree(node))
	}
	if stray != nil {
		t.Fatalf("an EXISTS survived keptExistsToAny; tree: %s", describePlanTree(node))
	}
	if !kept.IsNonCorrelated || !kept.UnknownEqFalse {
		t.Fatalf("converted ANY flags IsNonCorrelated=%v UnknownEqFalse=%v — "+
			"want both true (decorrelated hashed-ANY licence)",
			kept.IsNonCorrelated, kept.UnknownEqFalse)
	}
	if len(kept.ParParam) != 0 || len(kept.Args) != 0 {
		t.Fatalf("converted ANY ParParam=%v Args=%d — the operand IS the "+
			"testexpr; no param binding may remain", kept.ParParam, len(kept.Args))
	}
	row, isRow := kept.Operand.(*RowExpr)
	if !isRow || len(row.Elems) != 2 {
		t.Fatalf("operand = %T — want a 2-element RowExpr, one column per "+
			"correlation pair (parent-body + emitting)", kept.Operand)
	}
	for i, el := range row.Elems {
		cr, isCR := el.(*ColumnRef)
		if !isCR {
			t.Fatalf("operand element %d = %T, want a positional ColumnRef the "+
				"eval site can bind against the join row", i, el)
		}
		if cr.Index < 0 {
			t.Fatalf("operand element %d Index = %d — unbased coordinate", i, cr.Index)
		}
	}
	// The ANY's plan must be a Project emitting exactly the two inner
	// equality columns the conjuncts named.
	proj, isProj := kept.Plan.(*Project)
	if !isProj || len(proj.Targets) != 2 {
		t.Fatalf("kept.Plan = %T — want a Project over the two inner "+
			"equality columns", kept.Plan)
	}
	for i, tg := range proj.Targets {
		if _, isCR := tg.(*ColumnRef); !isCR {
			t.Fatalf("projected target %d = %T, want ColumnRef", i, tg)
		}
	}
	// Every escaping ref was consumed by a conjunct: no OuterColumnRef
	// and no ExecParamRef may survive anywhere in the converted plan.
	walkPlanExprs(kept.Plan, func(e Expr) {
		if _, isO := e.(*OuterColumnRef); isO {
			t.Fatalf("an OuterColumnRef survived in the converted ANY plan; tree: %s",
				describePlanTree(node))
		}
		if p, isP := e.(*ExecParamRef); isP {
			t.Fatalf("ExecParamRef %d survived — correlation not fully consumed", p.ID)
		}
	})
}

// TestKeptExistsToAnyVariants pins keptExistsToAny's convert/decline
// matrix — the boundary PG's convert_EXISTS_to_ANY draws, expressed on
// goopg's kept-sublink representation:
//
//   - `NOT EXISTS` converts too — goopg resolves it as
//     `UnaryOp{OpNot, EXISTS}`, so the ANY lands under the NOT exactly
//     where PG leaves `NOT (SubPlan)`;
//   - a correlation conjunct that is not an equality, or whose inner
//     side is not a plain column, keeps EXISTS — the sentinel it still
//     carries is the stray PG detects via contain_vars_of_level;
//   - an equality pair plus a sentinel-free residual converts AND keeps
//     the residual in the subplan's own predicate.
//
// Every case carries `b.j2 = k` (parent scope) plus `b.w = a.v`
// (emitting scope) or a deliberate variant of it: a kept EXISTS needs
// refs to BOTH scopes, because a single-scope correlation pulls the
// inner link into a join of its own and nothing is kept.
func TestKeptExistsToAnyVariants(t *testing.T) {
	cat := jtpCatalog(t)
	// kept censes the kept sublink forms anywhere in the planned tree.
	// The ANY may land in the join predicate or in a filter one side
	// feeds — placement is the join planner's business; this pass owns
	// the conversion, wherever the link ends up.
	kept := func(sql string) (any *InExpr, notAny *InExpr, ex *ExistsExpr) {
		node := planOnPipeline(t, sql, cat)
		j := findSemiOrAntiJoin(node)
		if j == nil {
			t.Fatalf("no semi/anti join for %q; tree: %s", sql, describePlanTree(node))
		}
		walkPlanExprs(node, func(e Expr) {
			switch x := e.(type) {
			case *InExpr:
				if x.Plan != nil {
					any = x
				}
			case *UnaryOp:
				if x.Op == parser.OpNot {
					walkExprTree(x.Operand, func(e2 Expr) {
						if in, isIn := e2.(*InExpr); isIn && in.Plan != nil {
							notAny = in
						}
					})
				}
			case *ExistsExpr:
				ex = x
			}
		})
		return any, notAny, ex
	}

	t.Run("NOT EXISTS converts to an ANY under the NOT", func(t *testing.T) {
		any, notAny, ex := kept(
			`select tag from jtp_o where exists (select 1 from jtp_i a where a.j = k `+
				`and not exists (select 1 from jtp_i2 b where b.j2 = k and b.w = a.v))`)
		if ex != nil {
			t.Fatalf("a negated EXISTS must still convert — PG keeps the NOT "+
				"in the qual above the converted SubPlan")
		}
		if notAny == nil {
			t.Fatalf("converted ANY = %v under NOT = %v — want the link under "+
				"a UnaryOp{OpNot}, PG's `NOT (SubPlan)`", any, notAny)
		}
	})

	t.Run("non-equality correlation keeps EXISTS", func(t *testing.T) {
		// `b.w > a.v` leaves its sentinel in a residual conjunct — the
		// stray check must refuse the whole conversion, exactly as
		// contain_vars_of_level keeps PG's EXISTS.
		any, _, ex := kept(
			`select tag from jtp_o where exists (select 1 from jtp_i a where a.j = k `+
				`and exists (select 1 from jtp_i2 b where b.j2 = k and b.w > a.v))`)
		if any != nil {
			t.Fatalf("non-equality correlation converted — a sentinel would be stranded")
		}
		if ex == nil {
			t.Fatalf("the kept EXISTS form must survive on decline")
		}
	})

	t.Run("composite inner side keeps EXISTS", func(t *testing.T) {
		// `b.w + 1 = a.v`: the sentinel sits against a composite inner
		// expr — PG would also allow an inner expr target, but goopg
		// binds the conversion to plain columns (same bound the flat
		// existsToAny pass applies); the EXISTS form must survive.
		any, _, ex := kept(
			`select tag from jtp_o where exists (select 1 from jtp_i a where a.j = k `+
				`and exists (select 1 from jtp_i2 b where b.j2 = k and b.w + 1 = a.v))`)
		if any != nil {
			t.Fatalf("a composite inner side converted — want decline")
		}
		if ex == nil {
			t.Fatalf("the kept EXISTS form must survive on decline")
		}
	})

	t.Run("sentinel-free residual is preserved in the subplan", func(t *testing.T) {
		// `b.j2 > 0` is inner-local — the two pairs convert and the
		// residual stays in the Filter the ANY's Project wraps.
		any, _, ex := kept(
			`select tag from jtp_o where exists (select 1 from jtp_i a where a.j = k `+
				`and exists (select 1 from jtp_i2 b where b.j2 = k and b.w = a.v and b.j2 > 0))`)
		if ex != nil || any == nil {
			t.Fatalf("clean pairs beside a local residual must convert; any=%v ex=%v", any, ex)
		}
		proj, isProj := any.Plan.(*Project)
		if !isProj || len(proj.Targets) != 2 {
			t.Fatalf("any.Plan = %T — want a 2-column Project", any.Plan)
		}
		filt, isFilt := proj.Child.(*Filter)
		if !isFilt {
			t.Fatalf("the residual conjunct must survive as a Filter under the Project; child=%T", proj.Child)
		}
		bin, isBin := filt.Predicate.(*BinaryOp)
		if !isBin || bin.Op != parser.OpGt {
			t.Fatalf("residual predicate = %T — want the `b.j2 > 0` conjunct", filt.Predicate)
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
