package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0146-0028 slice 1 — pull_up_simple_subquery for FROM-clause subqueries
// with a bare-column target list (derivedpullup.go).

func pullupPlanFrom(t *testing.T, q string) (*parser.SelectStmt, Node, *resolveContext) {
	t.Helper()
	stmts, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := stmts[0].(*parser.SelectStmt)
	node, rctx, err := planFromClause(s, scopeTestCatalog(t), DefaultPlannerSettings(), newRtableScope())
	if err != nil {
		t.Fatalf("planFromClause: %v", err)
	}
	return s, node, rctx
}

func pullupResolve(t *testing.T, ctx *resolveContext, table, col string) Expr {
	t.Helper()
	e, err := resolveColumnRef(&parser.ColumnRef{Table: table, Column: col}, ctx)
	if err != nil {
		t.Fatalf("resolve %s.%s: %v", table, col, err)
	}
	return e
}

// The body's relation becomes a leaf of the parent's FROM walk, hidden from
// parent-level names; the derived alias resolves to the body column it
// renames, and the body WHERE is carried up as a qual.
func TestDerivedPullupSplicesBodyLeaves(t *testing.T) {
	_, node, rctx := pullupPlanFrom(t, "SELECT * FROM (SELECT ax AS k, ay FROM a WHERE ay > 1) y, b WHERE y.k = b.bx")
	if len(rctx.pulledDerived) != 1 || len(rctx.pulledQuals) != 1 {
		t.Fatalf("pulledDerived=%d pulledQuals=%d, want 1 and 1", len(rctx.pulledDerived), len(rctx.pulledQuals))
	}
	if len(rctx.bindings) != 2 || !rctx.bindings[0].pulledHidden || rctx.bindings[1].pulledHidden {
		t.Fatalf("bindings: want [a(hidden), b], got %d", len(rctx.bindings))
	}
	if len(node.Output()) != 4 {
		t.Fatalf("FROM tree width %d, want a's 2 + b's 2 columns", len(node.Output()))
	}
	k, ok := pullupResolve(t, rctx, "y", "k").(*ColumnRef)
	if !ok || k.Index != rctx.bindings[0].offset || k.Name != "ax" {
		t.Fatalf("y.k resolved to %#v, want a.ax at slot %d", k, rctx.bindings[0].offset)
	}
	if u, ok := pullupResolve(t, rctx, "", "ay").(*ColumnRef); !ok || u.Index != rctx.bindings[0].offset+1 {
		t.Fatalf("unqualified ay resolved to %#v", u)
	}
	// The body's relation is not nameable from the parent (PG: missing
	// FROM-clause entry for table "a").
	if _, err := resolveColumnRef(&parser.ColumnRef{Table: "a", Column: "ax"}, rctx); err == nil {
		t.Fatal("a.ax resolved at the parent level; a pulled-up RTE is not nameable there")
	}
	// A column the derived table does not output is not visible either.
	if _, err := resolveColumnRef(&parser.ColumnRef{Table: "y", Column: "ax"}, rctx); err == nil {
		t.Fatal("y.ax resolved, but y outputs only k and ay")
	}
}

// `SELECT *` emits the derived table's outputs, under their derived names,
// where the derived item stood; ORDER BY <n> counts those outputs.
func TestDerivedPullupStarAndOrdinal(t *testing.T) {
	s, _, rctx := pullupPlanFrom(t, "SELECT * FROM b, (SELECT ay AS v FROM a) y ORDER BY 3")
	exprs, schema, err := expandStarTarget(s.Targets[0].Expr.(*parser.StarExpr), rctx)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(schema) != 3 || schema[0].Name != "bx" || schema[1].Name != "by" || schema[2].Name != "v" {
		t.Fatalf("star schema %v, want [bx by v]", schema)
	}
	ord, ok := ordinalThroughStarTargets(3, s.Targets, rctx)
	if !ok {
		t.Fatal("ordinal 3 not resolved through the star")
	}
	cr, _ := ord.(*ColumnRef)
	want := exprs[2].(*ColumnRef)
	if cr == nil || cr.Index != want.Index || cr.Name != "ay" {
		t.Fatalf("ORDER BY 3 -> %#v, want the body column a.ay at slot %d", ord, want.Index)
	}
	// The input schema's third column is a.ax — the slot the old
	// input-positional rule would have sorted by.
	if cr.Index == 2 && rctx.bindings[1].offset != 2 {
		t.Fatalf("ordinal landed on the input-schema position")
	}
}

// Shapes the pull-up leaves as ordinary derived leaves.
func TestDerivedPullupDeclines(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM (SELECT ax, count(*) FROM a GROUP BY ax) y, b",
		"SELECT x, y FROM (SELECT ax AS x, ax AS y FROM a) t GROUP BY GROUPING SETS (x, y)", // parent grouping sets (PHV wrap)
		"SELECT * FROM (SELECT random() AS k FROM a) y, b",                                  // volatile call target
		"SELECT * FROM (SELECT generate_series(1, ax) AS k FROM a) y, b",                    // set-returning call target
		"SELECT * FROM (SELECT no_such_fn(ax) AS k FROM a) y, b",                            // unknown call target
		"SELECT * FROM (SELECT count(ax) AS k FROM a) y, b",                                 // aggregate without GROUP BY
		"SELECT * FROM (SELECT (SELECT 1) AS k FROM a) y, b",                                // sublink target
		"SELECT * FROM (SELECT ax AS k, ay AS k FROM a) y, b",                               // duplicate output name          // grouping
		"SELECT * FROM (SELECT DISTINCT ax FROM a) y, b",                                    // DISTINCT
		"SELECT * FROM (SELECT ax FROM a LIMIT 1) y, b",                                     // LIMIT
		"SELECT * FROM (SELECT ax FROM a JOIN c USING (ax)) y, b",                           // USING in body
		"SELECT * FROM (SELECT ax FROM a NATURAL JOIN c) y, b",                              // NATURAL in body
		"SELECT * FROM (SELECT ax FROM a JOIN (SELECT cx FROM c) z ON ax = cx) y, b",        // derived join leg
		"SELECT * FROM (SELECT ax, ax FROM a) y, b",                                         // duplicate output name
	} {
		t.Run(q, func(t *testing.T) {
			stmts, err := parser.Parse(q)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, rctx, err := planFromClause(stmts[0].(*parser.SelectStmt), scopeTestCatalog(t), DefaultPlannerSettings(), newRtableScope())
			if err != nil {
				return // a shape the FROM walk itself rejects is also not pulled up
			}
			if len(rctx.pulledDerived) != 0 {
				t.Fatalf("pulled up a declined shape")
			}
		})
	}
}

// Two pulled bodies over the same relation (TPC-DS Q59's shape): the
// unqualified name they share is ambiguous exactly as the two derived leaves
// were, and each alias reaches its own body.
func TestDerivedPullupTwoBodiesSameRelation(t *testing.T) {
	_, _, rctx := pullupPlanFrom(t, "SELECT 1 FROM (SELECT ax FROM a WHERE ay < 5) y, (SELECT ax FROM a WHERE ay >= 5) x WHERE y.ax = x.ax")
	if len(rctx.pulledDerived) != 2 || len(rctx.pulledQuals) != 2 {
		t.Fatalf("pulledDerived=%d pulledQuals=%d, want 2 and 2", len(rctx.pulledDerived), len(rctx.pulledQuals))
	}
	y := pullupResolve(t, rctx, "y", "ax").(*ColumnRef)
	x := pullupResolve(t, rctx, "x", "ax").(*ColumnRef)
	if y.Index == x.Index || y.SourceTableIdx == x.SourceTableIdx {
		t.Fatalf("y.ax and x.ax resolved to the same leaf: %#v %#v", y, x)
	}
	if _, err := resolveColumnRef(&parser.ColumnRef{Column: "ax"}, rctx); err == nil {
		t.Fatal("unqualified ax resolved; it is ambiguous between y and x")
	}
}

// Slice 2 (M0146-0028b): an expression output is substituted wherever the
// derived column is referenced — a fresh copy per reference, rebased to the
// referencing level — and the lone FROM item is pulled up too.
func TestDerivedPullupExpressionTargets(t *testing.T) {
	_, _, rctx := pullupPlanFrom(t, "SELECT v, w FROM (SELECT ax * 2 AS v, ay FROM a WHERE ax > 0) y, b WHERE y.ay = b.bx")
	if len(rctx.pulledDerived) != 1 {
		t.Fatalf("pulledDerived=%d, want 1", len(rctx.pulledDerived))
	}
	v1 := pullupResolve(t, rctx, "y", "v")
	v2 := pullupResolve(t, rctx, "", "v")
	if _, isCol := v1.(*ColumnRef); isCol {
		t.Fatalf("y.v resolved to a bare column %#v; want the body expression", v1)
	}
	if v1 == v2 {
		t.Fatal("two references share one expression tree; each must get its own copy")
	}
	// A sublink one level down reads the body columns as level-1 outer refs.
	outer := pulledDerivedRef(rctx.pulledDerived[0].cols[0], 0, 1)
	sawOuter := false
	walkExprTree(outer, func(x Expr) {
		if _, ok := x.(*ColumnRef); ok {
			t.Fatalf("level-1 reference still holds a current-level ColumnRef")
		}
		if o, ok := x.(*OuterColumnRef); ok && o.Level == 1 {
			sawOuter = true
		}
	})
	if !sawOuter {
		t.Fatal("level-1 reference carries no OuterColumnRef")
	}
	// targetMeta names the output as written, not by the expression.
	name, _ := targetMeta(v2, parser.ResTarget{Expr: &parser.ColumnRef{Column: "v"}})
	if name != "v" {
		t.Fatalf("output name %q, want v", name)
	}
	// Unnamed expression outputs take the name the body's own projection
	// would have given them.
	_, _, rctx2 := pullupPlanFrom(t, "SELECT 1 FROM (SELECT ax + 1, ay FROM a) y, b")
	if len(rctx2.pulledDerived) != 1 || rctx2.pulledDerived[0].names[0] != "?column?" {
		t.Fatalf("unnamed expression output: %+v", rctx2.pulledDerived)
	}
}

func TestDerivedPullupLoneFromItem(t *testing.T) {
	_, node, rctx := pullupPlanFrom(t, "SELECT k FROM (SELECT ax AS k FROM a, b WHERE ay = bx) y")
	if len(rctx.pulledDerived) != 1 || len(rctx.bindings) != 2 || len(rctx.pulledQuals) != 1 {
		t.Fatalf("lone item not pulled up: derived=%d bindings=%d quals=%d", len(rctx.pulledDerived), len(rctx.bindings), len(rctx.pulledQuals))
	}
	if len(node.Output()) != 4 {
		t.Fatalf("FROM tree width %d, want a's 2 + b's 2", len(node.Output()))
	}
}

// Slice 3 (M0146-0028c): an INNER join inside the body is pulled up with it
// — every relation of the join becomes a hidden leaf of the parent search.
func TestDerivedPullupBodyInnerJoin(t *testing.T) {
	_, node, rctx := pullupPlanFrom(t, "SELECT k, z FROM (SELECT ax AS k, cy AS z FROM a JOIN c ON a.ay = c.cx WHERE ax > 1) y, b WHERE y.k = b.bx")
	if len(rctx.pulledDerived) != 1 || len(rctx.bindings) != 3 {
		t.Fatalf("body join not pulled up: derived=%d bindings=%d", len(rctx.pulledDerived), len(rctx.bindings))
	}
	if !rctx.bindings[0].pulledHidden || !rctx.bindings[1].pulledHidden || rctx.bindings[2].pulledHidden {
		t.Fatal("both join relations must be hidden leaves; b must stay visible")
	}
	if len(node.Output()) != 6 {
		t.Fatalf("FROM tree width %d, want a(2)+c(2)+b(2)", len(node.Output()))
	}
	z, ok := pullupResolve(t, rctx, "y", "z").(*ColumnRef)
	if !ok || z.Index != rctx.bindings[1].offset+1 {
		t.Fatalf("y.z resolved to %#v, want c.cy at slot %d", z, rctx.bindings[1].offset+1)
	}
}

// Slice 4 (M0146-0028d): an outer join inside the body is pulled up with it,
// demoted against the BODY's WHERE: a strict body qual on the nullable side
// reduces LEFT to INNER exactly as it did while the body was its own scope,
// and without one the join stays LEFT.
func TestDerivedPullupBodyOuterJoin(t *testing.T) {
	for _, q := range []string{
		"SELECT k, z FROM (SELECT ax AS k, cy AS z FROM a LEFT JOIN c ON a.ay = c.cx) y, b WHERE y.k = b.bx",
		"SELECT k, z FROM (SELECT ax AS k, cy AS z FROM a LEFT JOIN c ON a.ay = c.cx WHERE c.cy > 0) y, b WHERE y.k = b.bx",
		"SELECT k FROM (SELECT ax AS k FROM a FULL JOIN c ON a.ay = c.cx) y",
	} {
		_, _, rctx := pullupPlanFrom(t, q)
		if len(rctx.pulledDerived) != 1 {
			t.Fatalf("%s: outer-join body not pulled up", q)
		}
	}
}

// Slice 5 (M0146-0028e): a pullable derived operand of an all-INNER join
// chain is pulled up — the chain is split into comma items and its ON
// clauses become statement-level quals, exactly as an inner join's ON clause
// is a WHERE qual in all but name.
func TestDerivedPullupInnerJoinOperand(t *testing.T) {
	_, _, rctx := pullupPlanFrom(t, "SELECT b.bx, s.k FROM b JOIN (SELECT ax AS k FROM a WHERE ay > 0) s ON b.bx = s.k")
	if len(rctx.pulledDerived) != 1 {
		t.Fatalf("inner-join derived operand not pulled up")
	}
	if len(rctx.pulledQuals) != 2 {
		t.Fatalf("want the body WHERE and the ON clause as statement quals, got %d", len(rctx.pulledQuals))
	}
	for _, q := range []string{
		"SELECT * FROM b LEFT JOIN (SELECT ax AS k FROM a) s ON b.bx = s.k", // outer link
		"SELECT * FROM b JOIN (SELECT ax AS k FROM a) s ON bx = s.k",        // unqualified ON ref
		"SELECT * FROM b JOIN (SELECT ax AS bx FROM a) s USING (bx)",        // USING
		"SELECT * FROM b JOIN c ON b.bx = c.cx",                             // no derived operand
	} {
		_, _, rctx := pullupPlanFrom(t, q)
		if len(rctx.pulledDerived) != 0 {
			t.Fatalf("%s: pulled up a declined chain", q)
		}
	}
}

// TestDerivedPullupAdmitsSafeCallsAndWhereSublinks pins M0146-0028f: a known,
// non-volatile, non-set-returning function call in a body target and a
// sublink in the body WHERE no longer decline (PG's is_simple_subquery
// refuses only hasTargetSRFs / volatile targets, and pull_up_simple_subquery
// runs pull_up_sublinks on the body first). Each WHERE conjunct keeps the
// body context it was resolved in for the jointree sublink pull-up.
func TestDerivedPullupAdmitsSafeCallsAndWhereSublinks(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM (SELECT abs(ax) AS k FROM a) y, b",
		"SELECT * FROM (SELECT ax FROM a WHERE ay IN (SELECT dx FROM d)) y, b",
		"SELECT * FROM (SELECT ax FROM a WHERE NOT EXISTS (SELECT 1 FROM d WHERE dx = ay)) y, b",
	} {
		t.Run(q, func(t *testing.T) {
			stmts, err := parser.Parse(q)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, rctx, err := planFromClause(stmts[0].(*parser.SelectStmt), scopeTestCatalog(t), DefaultPlannerSettings(), newRtableScope())
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if len(rctx.pulledDerived) != 1 {
				t.Fatalf("pulled %d bodies, want 1", len(rctx.pulledDerived))
			}
			for _, q := range rctx.pulledQuals {
				for _, c := range splitAnd(q) {
					if rctx.pulledQualCtx[c] == nil {
						t.Errorf("pulled conjunct %T has no body context", c)
					}
				}
			}
		})
	}
}

// M0146-0028g: a column-alias list does not stop the pull-up; it renames the
// leading outputs, and the rest keep their own names (a list shorter than the
// output is legal). The original output name is no longer visible.
func TestDerivedPullupAliasList(t *testing.T) {
	s, _, rctx := pullupPlanFrom(t, "SELECT * FROM (SELECT ax, ay FROM a WHERE ay > 1) y(c), b WHERE y.c = b.bx")
	if len(rctx.pulledDerived) != 1 {
		t.Fatalf("pulledDerived=%d, want the alias-listed body pulled up", len(rctx.pulledDerived))
	}
	if c, ok := pullupResolve(t, rctx, "y", "c").(*ColumnRef); !ok || c.Name != "ax" {
		t.Fatalf("y.c resolved to %#v, want a.ax", c)
	}
	if _, ok := pullupResolve(t, rctx, "y", "ay").(*ColumnRef); !ok {
		t.Fatal("y.ay must keep its own name past the alias list")
	}
	if _, err := resolveColumnRef(&parser.ColumnRef{Table: "y", Column: "ax"}, rctx); err == nil {
		t.Fatal("y.ax resolved, but the alias list renamed it to c")
	}
	_, schema, err := expandStarTarget(s.Targets[0].Expr.(*parser.StarExpr), rctx)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(schema) != 4 || schema[0].Name != "c" || schema[1].Name != "ay" {
		t.Fatalf("star schema %v, want [c ay bx by]", schema)
	}
	// A full-length list is pulled up too.
	_, _, rctx = pullupPlanFrom(t, "SELECT 1 FROM b, (SELECT ax FROM a) y(c)")
	if len(rctx.pulledDerived) != 1 {
		t.Fatalf("one alias for one output: pulledDerived=%d, want 1", len(rctx.pulledDerived))
	}
}

// M0146-0028h: a LATERAL simple subquery of the statement's FROM list is
// pulled up when no outer join sits above it (is_simple_subquery's lateral
// arm with lowest_outer_join NULL). Its references to the items on its left
// become plain columns of the parent, so its WHERE becomes a join clause
// over both relations; its own column wins over a left item's of the same
// name (PG's namespace order).
func TestDerivedPullupLateral(t *testing.T) {
	_, _, rctx := pullupPlanFrom(t, "SELECT * FROM b, LATERAL (SELECT ax, bx FROM a WHERE ay = b.by) y")
	if len(rctx.pulledDerived) != 1 || len(rctx.pulledQuals) != 1 {
		t.Fatalf("pulledDerived=%d pulledQuals=%d, want the LATERAL body pulled up", len(rctx.pulledDerived), len(rctx.pulledQuals))
	}
	walkExprRefs(rctx.pulledQuals[0], scopeIgnore, exprVisitor{Visit: func(n Expr) bool {
		if _, outer := n.(*OuterColumnRef); outer {
			t.Fatalf("pulled LATERAL qual keeps an outer reference: %#v", rctx.pulledQuals[0])
		}
		return true
	}})
	// y.bx is the left item b's column, now a plain column of the parent.
	if c, ok := pullupResolve(t, rctx, "y", "bx").(*ColumnRef); !ok || c.Name != "bx" {
		t.Fatalf("y.bx resolved to %#v, want b.bx", c)
	}
	// A body with a sublink is not pulled up, and with it the whole
	// statement stays unpulled.
	_, _, rctx = pullupPlanFrom(t, "SELECT * FROM b, LATERAL (SELECT ax FROM a WHERE ay IN (SELECT cx FROM c WHERE cy = b.by)) y")
	if len(rctx.pulledDerived) != 0 {
		t.Fatalf("pulledDerived=%d, want the sublink-bearing LATERAL body left unpulled", len(rctx.pulledDerived))
	}
}
