package optimizer

// M0145-0008aa — an ANY sublink whose body is not simple is pulled up as ONE
// derived semi-side leaf, PG's un-flattened `ANY_subquery` RTE
// (convert_ANY_sublink_to_join, postgres/src/backend/optimizer/plan/
// subselect.c:1333). Before this arm the pull-up declined the body with
// `any-body-not-simple` and the statement took the post-hoc route.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPullUpAnyDerivedBody pins the derived arm at the pull-up itself: the
// body becomes a single leaf holding the body's own plan, marked derived, with
// the testexpr synthesised as its one link qual.
//
// Since M0145-0008ae the join it lands in depends on the body: a body that is
// `query_is_distinct_for` its output has its semijoin reduced to an inner join
// (PG's `reduce_unique_semijoins`), and any other body keeps the semi join.
func TestPullUpAnyDerivedBody(t *testing.T) {
	cases := map[string]struct {
		sql  string
		semi bool
	}{
		"group-having": {`select tag from jtp_o where k in (select j from jtp_i group by j having sum(v) > 3)`, false},
		"distinct":     {`select tag from jtp_o where k in (select distinct j from jtp_i)`, false},
		"limit":        {`select tag from jtp_o where k in (select j from jtp_i order by j limit 5)`, true},
		"union":        {`select tag from jtp_o where k in (select j from jtp_i union select j2 from jtp_i2)`, false},
	}
	for name, tc := range cases {
		sql := tc.sql
		t.Run(name, func(t *testing.T) {
			cat := jtpCatalog(t)
			delete(sublinkRouteCounts, spineRoutePosthoc)
			delete(sublinkRouteCounts, spineRouteJointree)
			node := planOnPipeline(t, sql, cat)
			if n := sublinkRouteCounts[spineRouteJointree]; n != 1 {
				t.Fatalf("jointree pull-up engaged %d times, want 1; tree: %s", n, describePlanTree(node))
			}
			j := findSemiOrAntiJoin(node)
			if tc.semi && (j == nil || j.Type != JoinTypeSemi) {
				t.Fatalf("no searched semi join over the derived leaf; tree: %s", describePlanTree(node))
			}
			if !tc.semi && j != nil {
				t.Fatalf("a distinct derived body kept its %v join; reduce_unique_semijoins should make it an inner join; tree: %s", j.Type, describePlanTree(node))
			}
		})
	}
}

// TestPullUpAnyDerivedBodyDeclines pins the one PG case the arm refuses: a
// correlated body, which PG pulls up as a LATERAL subquery RTE and goopg's
// search cannot parameterise. It must keep the post-hoc route (and so its
// values), not enter the search with an unbound outer reference.
func TestPullUpAnyDerivedBodyDeclines(t *testing.T) {
	cat := jtpCatalog(t)
	delete(sublinkRouteCounts, spineRoutePosthoc)
	delete(sublinkRouteCounts, spineRouteJointree)
	sql := `select tag from jtp_o where k in (select max(j) from jtp_i where v = k group by v)`
	if planOnPipeline(t, sql, cat) == nil {
		t.Fatal("declined pull-up returned a nil plan")
	}
	if n := sublinkRouteCounts[spineRouteJointree]; n != 0 {
		t.Errorf("pull-up engaged %d times on a correlated derived body", n)
	}
}

// TestPullUpAnyDerivedBodyRecord checks the record the pull-up hands the seam:
// one leaf, flagged derived, whose one qual is `outer = leaf.col0`.
func TestPullUpAnyDerivedBodyRecord(t *testing.T) {
	cat := jtpCatalog(t)
	stmt := parseOne(t, `select tag from jtp_o where k in (select j from jtp_i group by j having count(*) > 1)`)
	sel, ok := stmt.(*parser.SelectStmt)
	if !ok {
		t.Fatalf("parsed %T, want *parser.SelectStmt", stmt)
	}
	outer, ctx, err := planFromClause(sel, cat, PlannerSettings{}, nil)
	if err != nil || outer == nil {
		t.Fatalf("planFromClause: %v", err)
	}
	ctx.cat = cat
	pred, err := resolveExpr(sel.Where, ctx)
	if err != nil {
		t.Fatalf("resolve WHERE: %v", err)
	}
	in, okIn := anyPullupConjunct(pred)
	if !okIn {
		t.Fatalf("WHERE is not an ANY pull-up conjunct: %T", pred)
	}
	pb, why, ok := pullUpAnyBody(in, ctx, cat, PlannerSettings{}, 0)
	if !ok {
		t.Fatalf("derived body declined: %s", why)
	}
	if !pb.derived || len(pb.leafScans) != 1 || len(pb.bodyBindings) != 1 {
		t.Fatalf("derived=%v leaves=%d bindings=%d, want true/1/1", pb.derived, len(pb.leafScans), len(pb.bodyBindings))
	}
	if pb.bodyBindings[0].alias != anyDerivedAlias {
		t.Errorf("leaf alias = %q, want %q", pb.bodyBindings[0].alias, anyDerivedAlias)
	}
	if len(pb.quals) != 1 {
		t.Fatalf("quals = %d, want the single synthesised link", len(pb.quals))
	}
	link, ok := pb.quals[0].(*BinaryOp)
	if !ok {
		t.Fatalf("link qual is %T, want *BinaryOp", pb.quals[0])
	}
	if _, isOuter := link.Left.(*OuterColumnRef); !isOuter {
		t.Errorf("link LHS is %T, want the operand as *OuterColumnRef", link.Left)
	}
	if cr, isCol := link.Right.(*ColumnRef); !isCol || cr.Index != 0 {
		t.Errorf("link RHS = %#v, want the derived leaf's column 0", link.Right)
	}
}
