package optimizer

// M0145-0003 — sublink pull-up into the jointree (jointreepullup.go,
// docs/design/0100-0149/m0145-0003-*.md). Two pins:
//
//  1. under GOOPG_JOINTREE_PIPELINE=1, a flat correlated EXISTS /
//     NOT EXISTS enters the join-order problem — the plan carries a
//     searched Semi/Anti join whose RHS is the body's own leaf
//     scan(s), and no ExistsExpr survives anywhere in the plan
//     (the conjunct was consumed, not duplicated into a residual);
//  2. every decline gate keeps the statement exactly where the legacy
//     pipeline finds it — knob-on and knob-off plans are identical
//     for bodies the pull-up refuses.

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func jtpCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "jtp_o"}, []catalog.Column{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
		{Name: "tag", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateTable(parser.ObjectName{Name: "jtp_i"}, []catalog.Column{
		{Name: "j", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateTable(parser.ObjectName{Name: "jtp_i2"}, []catalog.Column{
		{Name: "j2", Type: catalog.Type{Name: "int4"}},
		{Name: "w", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return cat
}

// planOnPipeline plans sql with the package pipeline knob forced to on,
// restoring it afterwards — same pattern the dispatch test uses (the
// knob is process-global and this package's tests do not run parallel).
func planOnPipeline(t *testing.T, sql string, cat catalog.Catalog, on bool) Node {
	t.Helper()
	defer func(v bool) { jointreePipeline = v }(jointreePipeline)
	jointreePipeline = on
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatalf("Plan(%q, jointree=%v): %v", sql, on, err)
	}
	return node
}

// planHasExistsExpr reports whether any expr anywhere in the plan —
// filter predicates, join quals, keys, project targets — still carries
// an *ExistsExpr. walkPlanExprs is the package's own deep walk, so a
// residual the pull-up forgot to exclude is caught wherever it hides.
func planHasExistsExpr(n Node) bool {
	found := false
	walkPlanExprs(n, func(e Expr) {
		if _, ok := e.(*ExistsExpr); ok {
			found = true
		}
	})
	return found
}

// planSeqScans collects the table names of every *SeqScan in the plan.
func planSeqScans(n Node) []string {
	var names []string
	var walk func(Node)
	walk = func(cur Node) {
		if cur == nil {
			return
		}
		switch x := cur.(type) {
		case *SeqScan:
			names = append(names, x.Table.Name)
		case *Join:
			walk(x.Left)
			walk(x.Right)
		case *NestedLoopIndexJoin:
			walk(x.Outer)
			walk(x.Inner)
		case *Filter:
			walk(x.Child)
		case *Project:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *IndexScan:
			names = append(names, x.Table.Name)
		case *IndexOnlyScan:
			names = append(names, x.Table.Name)
		}
	}
	walk(n)
	return names
}

// TestJointreePullupExistsSplicesSemiJoin is the core pin: a flat
// correlated EXISTS becomes a searched SEMI join, not a subplan.
func TestJointreePullupExistsSplicesSemiJoin(t *testing.T) {
	cat := jtpCatalog(t)
	node := planOnPipeline(t,
		`select tag from jtp_o where exists (select 1 from jtp_i where j = k)`, cat, true)
	j := findSemiOrAntiJoin(node)
	if j == nil {
		t.Fatalf("knob-on EXISTS did not decorrelate to a semi/anti join; tree: %s", describePlanTree(node))
	}
	if j.Type != JoinTypeSemi {
		t.Fatalf("EXISTS produced %v, want JoinTypeSemi; tree: %s", j.Type, describePlanTree(node))
	}
	if planHasExistsExpr(node) {
		t.Fatalf("pulled EXISTS still present in the plan — the consumed conjunct leaked into a residual; tree: %s", describePlanTree(node))
	}
	// The body leaf must be a real search leaf: a SeqScan (or index
	// path) on jtp_i below the semi join's RHS, not an opaque subplan.
	found := false
	for _, name := range planSeqScans(j.Right) {
		if name == "jtp_i" {
			found = true
		}
	}
	if !found {
		t.Fatalf("semi-join RHS contains no jtp_i scan — pulled leaf missing; tree: %s", describePlanTree(node))
	}
}

// TestJointreePullupStarBodySplices pins the dominant benchmark form —
// `EXISTS (SELECT * FROM ...)` — whose star target list carries no
// function call and so passes sublinkBodyIsSimple.
func TestJointreePullupStarBodySplices(t *testing.T) {
	cat := jtpCatalog(t)
	node := planOnPipeline(t,
		`select tag from jtp_o where exists (select * from jtp_i where j = k)`, cat, true)
	j := findSemiOrAntiJoin(node)
	if j == nil {
		t.Fatalf("SELECT * EXISTS produced no semi join; tree: %s", describePlanTree(node))
	}
	if j.Type != JoinTypeSemi {
		t.Fatalf("SELECT * EXISTS produced %v, want JoinTypeSemi", j.Type)
	}
	if planHasExistsExpr(node) {
		t.Fatalf("pulled EXISTS still present in the plan; tree: %s", describePlanTree(node))
	}
}

// TestJointreePullupNotExistsSplicesAntiJoin pins the NOT EXISTS arm.
func TestJointreePullupNotExistsSplicesAntiJoin(t *testing.T) {
	cat := jtpCatalog(t)
	node := planOnPipeline(t,
		`select tag from jtp_o where not exists (select 1 from jtp_i where j = k)`, cat, true)
	j := findSemiOrAntiJoin(node)
	if j == nil {
		t.Fatalf("knob-on NOT EXISTS produced no anti join; tree: %s", describePlanTree(node))
	}
	if j.Type != JoinTypeAnti {
		t.Fatalf("NOT EXISTS produced %v, want JoinTypeAnti; tree: %s", j.Type, describePlanTree(node))
	}
	if planHasExistsExpr(node) {
		t.Fatalf("pulled NOT EXISTS still present in the plan; tree: %s", describePlanTree(node))
	}
}

// TestJointreePullupMultiRelBody pins a two-leaf body: both body rels
// enter the problem under ONE semijoin link, and the intra-body qual
// (a.v = b.j2) survives inside it.
func TestJointreePullupMultiRelBody(t *testing.T) {
	cat := jtpCatalog(t)
	node := planOnPipeline(t,
		`select tag from jtp_o where exists (select 1 from jtp_i a, jtp_i2 b where a.j = k and a.v = b.j2)`, cat, true)
	j := findSemiOrAntiJoin(node)
	if j == nil {
		t.Fatalf("multi-rel EXISTS produced no semi join; tree: %s", describePlanTree(node))
	}
	if planHasExistsExpr(node) {
		t.Fatalf("pulled EXISTS still present in the plan; tree: %s", describePlanTree(node))
	}
	// Both body rels must appear as leaves — a two-leaf pulled body,
	// not one opaque rel.
	var sawI, sawI2 bool
	for _, name := range planSeqScans(node) {
		sawI = sawI || name == "jtp_i"
		sawI2 = sawI2 || name == "jtp_i2"
	}
	if !sawI || !sawI2 {
		t.Fatalf("expected both body rels as leaves (jtp_i=%v jtp_i2=%v); tree: %s", sawI, sawI2, describePlanTree(node))
	}
	// The intra-body qual must not be dropped: some expr in the plan
	// still names column w or j2... v = j2 is the join clause; at
	// minimum a ColumnRef for both v and j2 must survive.
	var sawV, sawJ2 bool
	walkPlanExprs(node, func(e Expr) {
		if cr, ok := e.(*ColumnRef); ok {
			sawV = sawV || cr.Name == "v"
			sawJ2 = sawJ2 || cr.Name == "j2"
		}
	})
	if !sawV || !sawJ2 {
		t.Fatalf("intra-body qual a.v = b.j2 dropped (v=%v j2=%v); tree: %s", sawV, sawJ2, describePlanTree(node))
	}
}

// TestJointreePullupIntegratesLink is the white-box pin the plan-shape
// tests cannot provide: the semijoin they find could equally be the
// legacy post-search unnest's output, since both routes produce
// Semi(outer, inner) trees. Driving pullUpSublinksIntoJointree +
// integratePulledSublinks directly proves the pulled body became a
// leaf + link inside the search problem — the property the milestone
// exists for.
func TestJointreePullupIntegratesLink(t *testing.T) {
	cat := jtpCatalog(t)
	sel, ok := parseOne(t, `select tag from jtp_o where exists (select 1 from jtp_i where j = k)`).(*parser.SelectStmt)
	if !ok {
		t.Fatalf("stmt is %T, want *parser.SelectStmt", parseOne(t, `select 1`))
	}
	ps := DefaultPlannerSettings()
	node, ctx, err := planFromClause(sel, cat, ps, nil)
	if err != nil || node == nil || ctx == nil {
		t.Fatalf("planFromClause: node=%v ctx=%v err=%v", node, ctx, err)
	}
	ctx.cat = cat
	ctx.settings = ps
	pred, err := resolveExpr(sel.Where, ctx)
	if err != nil {
		t.Fatalf("resolveExpr: %v", err)
	}
	pu := pullUpSublinksIntoJointree(pred, ctx, cat, ps)
	if pu == nil {
		t.Fatalf("pullUpSublinksIntoJointree declined a flat correlated EXISTS")
	}
	if len(pu.bodies) != 1 {
		t.Fatalf("bodies = %d, want 1", len(pu.bodies))
	}
	if pu.bodies[0].jointype != parser.JoinSemi {
		t.Fatalf("body jointype = %v, want JoinSemi", pu.bodies[0].jointype)
	}
	if len(pu.pulled) != 1 {
		t.Fatalf("pulled marks = %d, want 1", len(pu.pulled))
	}
	// Feed the seam the same leaf tables extractSearchLeaves would
	// have produced for `FROM jtp_o`: one emitting leaf.
	scans := []Node{node}
	widths := []int{len(node.Output())}
	var semiAnti []semiAntiChainLink
	if !integratePulledSublinks(pu, 1, ctx, &scans, &widths, &semiAnti) {
		t.Fatalf("integratePulledSublinks declined the pulled body")
	}
	if len(scans) != 2 || len(widths) != 2 {
		t.Fatalf("leaf table = %d scans / %d widths, want 2/2", len(scans), len(widths))
	}
	if len(semiAnti) != 1 {
		t.Fatalf("links = %d, want 1", len(semiAnti))
	}
	lk := semiAnti[0]
	if lk.jointype != parser.JoinSemi {
		t.Fatalf("link jointype = %v, want JoinSemi", lk.jointype)
	}
	if lk.lhs != leafRangeRelSet(0, 1) || lk.rhs != leafRangeRelSet(1, 2) {
		t.Fatalf("link hands = lhs %08b rhs %08b, want lhs 01 rhs 10", lk.lhs, lk.rhs)
	}
	if lk.pred == nil {
		t.Fatalf("link pred is nil — the correlation conjunct did not become the link predicate")
	}
	if lk.sjinfo == nil {
		t.Fatalf("link carries no SpecialJoinInfo — joinIsLegal would treat the pulled leaf as INNER-joinable")
	}
	// The rebased predicate must span emitting leaf 0 and pulled leaf
	// 1 — attribute it against the problem's own span layout.
	spans := buildLeafSpans(widths, semiAnti)
	for _, c := range splitAnd(lk.pred) {
		rs, attributable := relidsOfExpr(c, spans)
		if !attributable || rs != leafRangeRelSet(0, 2) {
			t.Fatalf("link pred conjunct attributed to %08b (ok=%v), want relids 11", rs, attributable)
		}
	}
}

// TestJointreePullupIntegratesAntiLink pins the NOT EXISTS arm at the
// same white-box level: JoinAnti link, same hand layout.
func TestJointreePullupIntegratesAntiLink(t *testing.T) {
	cat := jtpCatalog(t)
	sel, ok := parseOne(t, `select tag from jtp_o where not exists (select 1 from jtp_i where j = k)`).(*parser.SelectStmt)
	if !ok {
		t.Fatalf("stmt is %T, want *parser.SelectStmt", sel)
	}
	ps := DefaultPlannerSettings()
	node, ctx, err := planFromClause(sel, cat, ps, nil)
	if err != nil || node == nil || ctx == nil {
		t.Fatalf("planFromClause: %v", err)
	}
	ctx.cat = cat
	ctx.settings = ps
	pred, err := resolveExpr(sel.Where, ctx)
	if err != nil {
		t.Fatalf("resolveExpr: %v", err)
	}
	pu := pullUpSublinksIntoJointree(pred, ctx, cat, ps)
	if pu == nil {
		t.Fatalf("pullUpSublinksIntoJointree declined a flat correlated NOT EXISTS")
	}
	scans := []Node{node}
	widths := []int{len(node.Output())}
	var semiAnti []semiAntiChainLink
	if !integratePulledSublinks(pu, 1, ctx, &scans, &widths, &semiAnti) {
		t.Fatalf("integratePulledSublinks declined the pulled body")
	}
	if len(semiAnti) != 1 || semiAnti[0].jointype != parser.JoinAnti {
		t.Fatalf("expected one JoinAnti link, got %+v", semiAnti)
	}
}

// TestJointreePullupDeclineParity is the fail-closed pin: every body
// shape the pull-up refuses must leave the knob-on plan byte-identical
// to the legacy one — the pull-up's marks are inert outside the seam.
func TestJointreePullupDeclineParity(t *testing.T) {
	cases := map[string]string{
		// No correlation at all — contain_vars_of_level(whereClause,1)
		// fails; upstream leaves an uncorrelated EXISTS a subplan.
		"uncorrelated": `select tag from jtp_o where exists (select 1 from jtp_i)`,
		// Correlation outside the WHERE only — here the body's own
		// WHERE is absent; same upstream gate.
		"uncorrelated-where": `select tag from jtp_o where exists (select 1 from jtp_i where j = 3)`,
		// GROUP BY body — sublinkBodyIsSimple (simplify_EXISTS_query
		// equivalent) rejects.
		"group-by": `select tag from jtp_o where exists (select j from jtp_i where j = k group by j)`,
		// ORDER BY body — same gate.
		"order-by": `select tag from jtp_o where exists (select j from jtp_i where j = k order by j)`,
		// LIMIT body — same gate.
		"limit": `select tag from jtp_o where exists (select j from jtp_i where j = k limit 1)`,
		// Derived-table body — sublinkBodyFromIsFlat rejects non-bare
		// FROM items.
		"derived": `select tag from jtp_o where exists (select 1 from (select j from jtp_i) d where d.j = k)`,
		// Nested sublink in the body WHERE — exprHasSublinkPlan: a
		// nested sublink's Level-1 refs only bind while the body
		// evaluates as one unit.
		"nested-exists": `select tag from jtp_o where exists (select 1 from jtp_i a where a.j = k and exists (select 1 from jtp_i2 b where b.j2 = a.v))`,
		// Volatile body WHERE — contain_volatile_functions: splicing
		// changes the qual's evaluation count.
		"volatile": `select tag from jtp_o where exists (select 1 from jtp_i where j = k and random() < 0.5)`,
		// Outer-local-only correlation under ANTI — the qual reads no
		// body column, so nothing can become the link predicate.
		"anti-outer-local": `select tag from jtp_o where not exists (select 1 from jtp_i where k = 5)`,
		// Same shape under SEMI — outer-local-only, no spanning
		// conjunct exists.
		"semi-outer-local": `select tag from jtp_o where exists (select 1 from jtp_i where k = 5)`,
		// USING join in the body — sublinkBodyFromIsFlat rejects it.
		"using-join": `select tag from jtp_o where exists (select 1 from jtp_i a join jtp_i b using (j) where a.j = k)`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			cat := jtpCatalog(t)
			off := planOnPipeline(t, sql, cat, false)
			on := planOnPipeline(t, sql, cat, true)
			if !reflect.DeepEqual(off, on) {
				t.Errorf("declined pull-up changed the plan\nknob-off: %s\nknob-on:  %s", describePlanTree(off), describePlanTree(on))
			}
		})
	}
}

// TestJointreePullupSiblingConjunctPreserved pins that a WHERE conjunct
// beside the pulled sublink survives the search unchanged — the
// conjunct pool loses only the pulled sublink.
func TestJointreePullupSiblingConjunctPreserved(t *testing.T) {
	cat := jtpCatalog(t)
	node := planOnPipeline(t,
		`select tag from jtp_o where tag = 'x' and exists (select 1 from jtp_i where j = k)`, cat, true)
	if findSemiOrAntiJoin(node) == nil {
		t.Fatalf("no semi join; tree: %s", describePlanTree(node))
	}
	if planHasExistsExpr(node) {
		t.Fatalf("pulled EXISTS still present in the plan; tree: %s", describePlanTree(node))
	}
	// The sibling restriction must survive somewhere — as a leaf-local
	// filter, a join clause, or the residual. If it were dropped the
	// plan would have no expr naming `tag` outside the projection.
	var sawTagQual bool
	walkPlanExprs(node, func(e Expr) {
		b, ok := e.(*BinaryOp)
		if !ok || b.Op != parser.OpEq {
			return
		}
		if cr, ok := b.Left.(*ColumnRef); ok && cr.Name == "tag" {
			sawTagQual = true
		}
		if cr, ok := b.Right.(*ColumnRef); ok && cr.Name == "tag" {
			sawTagQual = true
		}
	})
	if !sawTagQual {
		t.Fatalf("sibling conjunct tag = 'x' dropped; tree: %s", describePlanTree(node))
	}
}

// TestJointreePullupNoExistsKeepsLegacyIdentical pins the trivial case:
// a WHERE clause with no sublink at all plans identically on both arms.
func TestJointreePullupNoExistsKeepsLegacyIdentical(t *testing.T) {
	cat := jtpCatalog(t)
	sql := `select tag from jtp_o where k > 3`
	off := planOnPipeline(t, sql, cat, false)
	on := planOnPipeline(t, sql, cat, true)
	if !reflect.DeepEqual(off, on) {
		t.Errorf("sublink-free statement diverged\nknob-off: %s\nknob-on:  %s", describePlanTree(off), describePlanTree(on))
	}
}
