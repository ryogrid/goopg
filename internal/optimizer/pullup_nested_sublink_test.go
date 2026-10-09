package optimizer

import (
	"fmt"
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

// TestJoinClauseKeepsSurvivingOuterRef pins the translateToLayout
// pass-through: a surviving `*OuterColumnRef` inside a multi-leaf join
// clause is a correlation bound through the enclosing scope chain at
// execution — PG leaves the outer Var in a correlated subplan's join qual
// — so createPlan must carry it to `lowerSubPlanParams`, not panic. Both
// shapes below used to die in createPlan ("join clause carries a
// *optimizer.OuterColumnRef"): a correlated scalar subplan whose join
// clause mixes two leaves with an outer ref, and the M0146-0015c
// nested-EXISTS shape whose composite escaping ref made a's own pull-up
// emit `b.j2 = a.v + OuterRef` as the inner semi join's link clause.
func TestJoinClauseKeepsSurvivingOuterRef(t *testing.T) {
	cat := jtpCatalog(t)

	t.Run("correlated join clause inside a scalar subplan plans", func(t *testing.T) {
		node := planOnPipeline(t,
			`select k, (select a.j + b.j2 from jtp_i a, jtp_i2 b where a.j = b.j2 + k) from jtp_o`, cat)
		// The scalar subquery becomes a SubPlan in the projection; inside
		// its plan the outer ref must have been LOWERED to an ExecParamRef
		// — a surviving OuterColumnRef would mean the join clause carried
		// an unbound correlation into the executor.
		var sub *SubqueryExpr
		walkPlanExprs(node, func(e Expr) {
			if s, isS := e.(*SubqueryExpr); isS && s.Plan != nil {
				sub = s
			}
		})
		if sub == nil {
			t.Fatalf("no scalar SubPlan in the projection; tree: %s", describePlanTree(node))
		}
		var sawParam bool
		walkPlanExprs(sub.Plan, func(e Expr) {
			switch x := e.(type) {
			case *OuterColumnRef:
				t.Fatalf("OuterColumnRef %s/level=%d survived lowering inside the "+
					"subplan's join clause", x.Name, x.Level)
			case *ExecParamRef:
				sawParam = true
			}
		})
		if !sawParam {
			t.Fatalf("the surviving outer ref did not lower to an ExecParamRef")
		}
	})

	t.Run("composite escaping ref in a nested EXISTS plans", func(t *testing.T) {
		// `b.j2 = a.v + k`: inside a's subquery the inner EXISTS pulls and
		// its link clause carries the Level-2 escape as a surviving
		// OuterColumnRef — the exact shape translateToLayout used to
		// refuse. At top level the inner EXISTS spans scopes and stays a
		// kept SubPlan; either way the statement must plan, and plan
		// correctly.
		node := planOnPipeline(t,
			`select tag from jtp_o where exists (select 1 from jtp_i a where a.j = k `+
				`and exists (select 1 from jtp_i2 b where b.j2 = a.v + k))`, cat)
		if node == nil {
			t.Fatalf("the composite-escape shape must plan (it panicked in createPlan)")
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

// TestJointreePullupNestedLargArm is M0146-0015d's pin: a nested sublink
// whose correlation names the EMITTING scope — not the parent body's own
// rels — pulls through PG's OTHER insertion point, `&j->larg` under
// `available_rels1` (prepjointree.c:749-754), instead of dying at
// `no-level1-correlation` against the parent-body context. The child
// flattens BEFORE its parent, carries parent=nil (its Level-1 refs are
// emitting-scope refs), and the parent keeps it on largChildren so
// classifyPulledQuals can widen the parent's sjLeft by its leaves — PG's
// `syn_lefthand` of the stacked join.
func TestJointreePullupNestedLargArm(t *testing.T) {
	cat := jtpCatalog(t)

	planPullup := func(t *testing.T, sql string) *jtPullup {
		t.Helper()
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
		return pu
	}

	t.Run("emitting-scope NOT EXISTS pulls as a larg anti join", func(t *testing.T) {
		// `d` correlates on `x` — an EMITTING-scope rel — so it cannot
		// bind inside `b`'s body (Level-2 there). PG converts it under
		// available_rels1={x,a} into `j->larg`: the canonical
		// regress-subselect shape.
		pu := planPullup(t,
			`select tag from jtp_o x, jtp_i a where a.j = x.k and exists `+
				`(select 1 from jtp_i2 b where b.j2 = a.v and not exists `+
				`(select 1 from jtp_o d where d.k = x.k))`)
		if len(pu.bodies) != 2 {
			t.Fatalf("pulled %d bodies, want 2 (the outer EXISTS and the larg anti join)", len(pu.bodies))
		}
		anti, semi := pu.bodies[0], pu.bodies[1]
		if anti.jointype != parser.JoinAnti {
			t.Fatalf("larg child's jointype = %v, want JoinAnti", anti.jointype)
		}
		if semi.jointype != parser.JoinSemi {
			t.Fatalf("outer body's jointype = %v, want JoinSemi", semi.jointype)
		}
		if anti.parent != nil {
			t.Fatalf("larg child's parent = %p, want nil — its Level-1 refs name the EMITTING "+
				"scope (the parent's own parent), not the parent's columns", anti.parent)
		}
		if len(semi.largChildren) != 1 || semi.largChildren[0] != anti {
			t.Fatalf("parent largChildren = %v, want [%p] — flatten must keep the link so "+
				"classifyPulledQuals widens sjLeft (PG's syn_lefthand of the larg subtree)", semi.largChildren, anti)
		}
		if semi.subtreeLeaves != 1 {
			t.Fatalf("parent subtreeLeaves = %d, want 1 — the larg child is NOT in the "+
				"parent's rarg subtree (it sits in the parent's LEFT subtree)", semi.subtreeLeaves)
		}
	})

	t.Run("emitting-scope EXISTS pulls as a larg semi join", func(t *testing.T) {
		pu := planPullup(t,
			`select tag from jtp_o x, jtp_i a where a.j = x.k and exists `+
				`(select 1 from jtp_i2 b where b.j2 = a.v and exists `+
				`(select 1 from jtp_o d where d.k = x.k))`)
		if len(pu.bodies) != 2 || pu.bodies[0].jointype != parser.JoinSemi ||
			pu.bodies[0].parent != nil || len(pu.bodies[1].largChildren) != 1 {
			t.Fatalf("emitting-scope nested EXISTS did not pull as a larg semi join: %s",
				describeBodiesForTest(pu))
		}
	})

	t.Run("parent-scope-only nested body still pulls rarg", func(t *testing.T) {
		// `d` correlates on `b` — the parent body's own rel — so the larg
		// bind fails name resolution (b is not in the enclosing scope)
		// and the j->rarg arm takes it exactly as before.
		pu := planPullup(t,
			`select tag from jtp_o x, jtp_i a where a.j = x.k and exists `+
				`(select 1 from jtp_i2 b where b.j2 = a.v and not exists `+
				`(select 1 from jtp_o d where d.k = b.w))`)
		if len(pu.bodies) != 2 {
			t.Fatalf("pulled %d bodies, want 2", len(pu.bodies))
		}
		semi, child := pu.bodies[0], pu.bodies[1]
		if child.parent != semi {
			t.Fatalf("rarg child's parent = %p, want %p — a parent-scope correlation must "+
				"still splice inside the parent's RIGHT subtree", child.parent, semi)
		}
		if len(semi.largChildren) != 0 {
			t.Fatalf("largChildren = %v, want empty — a parent-scope correlation cannot "+
				"bind against the enclosing scope", semi.largChildren)
		}
		if semi.subtreeLeaves != 2 {
			t.Fatalf("parent subtreeLeaves = %d, want 2 (rarg descendants count)", semi.subtreeLeaves)
		}
	})

	t.Run("mixed-scope nested body stays kept", func(t *testing.T) {
		// `d` correlates on BOTH the emitting scope (x) and the parent
		// body (b): the larg bind cannot resolve `b`, the rarg bind
		// fails nestedBodySpansScopes — PG keeps it as a SubPlan too
		// ({x,b} is a subset of neither {x,a} nor {b}).
		pu := planPullup(t,
			`select tag from jtp_o x, jtp_i a where a.j = x.k and exists `+
				`(select 1 from jtp_i2 b where b.j2 = a.v and not exists `+
				`(select 1 from jtp_o d where d.k = x.k and d.k = b.w))`)
		if len(pu.bodies) != 1 {
			t.Fatalf("pulled %d bodies, want 1 — the mixed-scope inner sublink must stay kept "+
				"inside the outer EXISTS (PG's available-rels subset test fails both arms)", len(pu.bodies))
		}
	})

	t.Run("ANY operand bound in the parent scope never larg-binds", func(t *testing.T) {
		// The TPC-DS Q83 shape: a nested `parentcol IN (...)` inside a
		// pulled ANY body. outerOperandAsLevel1 re-expresses the
		// operand's ColumnRefs by NAME — bound against the enclosing
		// scope a same-named binding steals them (Q83 rebound
		// `d_week_seq` onto the outer query's own date_dim leaf and
		// panicked in translateToLayout). The ANY arm therefore has no
		// larg branch at all: the rarg arm — or nothing — takes it.
		pu := planPullup(t,
			`select tag from jtp_o x where x.k in `+
				`(select j from jtp_i a where a.v in (select w from jtp_i2 b))`)
		if len(pu.bodies) != 2 {
			t.Fatalf("pulled %d bodies, want 2", len(pu.bodies))
		}
		if pu.bodies[1].parent != pu.bodies[0] {
			t.Fatalf("the nested ANY must splice inside the parent's rarg (parent=%p), "+
				"got %p — a larg splice would misresolve the operand by name",
				pu.bodies[0], pu.bodies[1].parent)
		}
		if len(pu.bodies[0].largChildren) != 0 {
			t.Fatalf("largChildren = %v, want empty for a parent-scope ANY operand",
				pu.bodies[0].largChildren)
		}
	})

	t.Run("the canonical shape plans end to end", func(t *testing.T) {
		node := planOnPipeline(t,
			`select tag from jtp_o x, jtp_i a where a.j = x.k and exists `+
				`(select 1 from jtp_i2 b where b.j2 = a.v and not exists `+
				`(select 1 from jtp_o d where d.k = x.k))`, cat)
		if node == nil {
			t.Fatalf("canonical larg-pull shape did not plan")
		}
		if planHasExistsExpr(node) {
			t.Fatalf("an EXISTS survived into the plan — the larg pull or its sjLeft "+
				"widening was refused downstream: %s", describePlanTree(node))
		}
	})
}

// describeBodiesForTest renders a pulled-body list compactly for test
// failures — flat order, jointype, and the parent link, the three facts
// every larg/rarg assertion pins.
func describeBodiesForTest(pu *jtPullup) string {
	if pu == nil {
		return "<nil pullup>"
	}
	out := ""
	for i, pb := range pu.bodies {
		parent := "nil"
		if pb.parent != nil {
			parent = "set"
		}
		out += fmt.Sprintf(" [%d] jointype=%v parent=%s leaves=%d subtree=%d larg=%d", i,
			pb.jointype, parent, len(pb.leafScans), pb.subtreeLeaves, len(pb.largChildren))
	}
	return out
}
