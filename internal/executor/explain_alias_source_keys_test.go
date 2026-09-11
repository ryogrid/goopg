package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// R66 (alias → source keys): Slice 1 rendered group-section Sort keys
// from GroupExprs plus Star/DISTINCT call expansion; Slice 2 chases
// both Sort and Group keys past republishing layers (resolveKeySource)
// with the boundary rule (sublink-Filter and CTE-qualifier declines).

func TestExplainSortKeyGroupSectionRendersSource(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE r66g (brand text, typ text, sz int)",
		"CREATE TABLE r66h (id int, gbrand text)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	// Q16 shape: group keys from one table, DISTINCT aggregate over
	// another, ORDER BY all four.
	joined := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT g.brand, g.typ, g.sz, count(DISTINCT h.id) AS c FROM r66g g, r66h h WHERE h.gbrand = g.brand GROUP BY g.brand, g.typ, g.sz ORDER BY c DESC, g.brand, g.typ, g.sz"), "\n")
	if !strings.Contains(joined, "Sort Key: (count(DISTINCT h.id)) DESC, g.brand, g.typ, g.sz") {
		t.Errorf("expected sourced+expanded Sort Key; got:\n%s", joined)
	}
	if strings.Contains(joined, "Sort Key: count DESC") || strings.Contains(joined, "Sort Key: (count(DISTINCT h.id)) DESC, brand,") {
		t.Errorf("sort key still renders an output alias; got:\n%s", joined)
	}
	assertNoOpaqueExpr(t, joined)
}

func TestExplainSortKeyCountStarExpands(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE r66s (g int)"); err != nil {
		t.Fatal(err)
	}

	// Q13-key1 shape: Sort key naming a count(*) Aggs output.
	joined := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT g, count(*) AS c FROM r66s GROUP BY g ORDER BY c DESC"), "\n")
	if !strings.Contains(joined, "Sort Key: (count(*)) DESC") {
		t.Errorf("expected expanded `Sort Key: (count(*)) DESC`; got:\n%s", joined)
	}
	assertNoOpaqueExpr(t, joined)
}

func TestExplainHavingCountStarExpands(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE r66v (a int)"); err != nil {
		t.Fatal(err)
	}

	// The shared expansion helper also serves the Aggregate HAVING arm:
	// a Star HAVING qual renders PG's call text (P3-advertised move).
	joined := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT a, count(*) FROM r66v GROUP BY a HAVING count(*) > 1"), "\n")
	if !strings.Contains(joined, "Filter: (count(*) > 1)") {
		t.Errorf("expected expanded HAVING `Filter: (count(*) > 1)`; got:\n%s", joined)
	}
	assertNoOpaqueExpr(t, joined)
}

// SUPERSEDED by the Slice-2 chase per Gate-0-A (PG prints base text
// for flattened subqueries): the alias no-op below became sourced
// text. The N4 qualifier remainder is ledgered — PG qualifies even
// single-RTE (`r66a.n_name`), goopg renders bare there; qualifier gaps
// are N4-forgiven, not this round.
func TestExplainSortKeySubqueryGroupRendersSource(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE r66u (n_name text, v int)"); err != nil {
		t.Fatal(err)
	}

	rows := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT s.supp AS supp_nation, sum(s.v) FROM (SELECT n_name AS supp, v FROM r66u) s GROUP BY s.supp ORDER BY supp_nation")
	var sortLine, groupLine string
	for _, r := range rows {
		t2 := strings.TrimSpace(r)
		if sortLine == "" && strings.Contains(t2, "Sort Key:") {
			sortLine = t2
		}
		if strings.Contains(t2, "Group Key:") {
			groupLine = t2
		}
	}
	if sortLine != "Sort Key: n_name" {
		t.Errorf("expected sourced `Sort Key: n_name`; got:\n%s", strings.Join(rows, "\n"))
	}
	if groupLine != "Group Key: n_name" {
		t.Errorf("expected sourced `Group Key: n_name`; got:\n%s", strings.Join(rows, "\n"))
	}
	assertNoOpaqueExpr(t, strings.Join(rows, "\n"))
}

// Gate-0b note (deliberately NOT a unit pin): the two-table join shape
// routes around the boundary Project at unit scale (identity
// boundaryMap → Agg directly over the join), so no chase fires and the
// alias stands — while live Q7/Q9 grow boundary Projects and chase.
// PG :65432 prints `r66j1.n_name` on both key lines for the mirrored
// shape (transcript /tmp/pp2/r66/gate0/pg-gate0b.txt); that prediction
// is verified at the LIVE explain gate on Q7/Q9 themselves, not here.
// Pinning today's alias text would lock a divergence, so no pin.


func TestExplainTransitiveGroupKeyRendersInnerCall(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE r66t (g int, x int)"); err != nil {
		t.Fatal(err)
	}

	// Q13-key2 miniature: the outer group key names the inner agg's
	// output positionally (`c` vs inner `count`) and renders the inner
	// call — bare in Group Key (HashAggregate over Project, S18), wrapped
	// in Sort Key.
	rows := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT c, count(*) FROM (SELECT g, count(x) AS c FROM r66t GROUP BY g) GROUP BY c ORDER BY c DESC")
	var sortLine, groupLine string
	for _, r := range rows {
		t2 := strings.TrimSpace(r)
		if sortLine == "" && strings.Contains(t2, "Sort Key:") {
			sortLine = t2
		}
		if groupLine == "" && strings.Contains(t2, "Group Key:") {
			groupLine = t2 // the top agg; deeper aggs are not this pin
		}
	}
	if sortLine != "Sort Key: (count(x)) DESC" {
		t.Errorf("expected transitive `Sort Key: (count(x)) DESC`; got:\n%s", strings.Join(rows, "\n"))
	}
	if groupLine != "Group Key: count(x)" {
		t.Errorf("expected transitive `Group Key: count(x)`; got:\n%s", strings.Join(rows, "\n"))
	}
	assertNoOpaqueExpr(t, strings.Join(rows, "\n"))
}

func TestExplainSortKeySubqueryGroupByUnchanged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE r66q1 (x int)",
		"CREATE TABLE r66q2 (x int)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	// End-to-end sublink guard: GROUP BY a scalar subquery. The Sort key
	// names the SELECT alias over the sublink and must NOT expand through
	// it (subplan numbering belongs to the main plan walk) — while the
	// Group Key keeps R65's InitPlan value form with numbering intact.
	rows := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT (SELECT max(x) FROM r66q2) AS s, sum(x) FROM r66q1 GROUP BY (SELECT max(x) FROM r66q2) ORDER BY s")
	var sortLine string
	for _, r := range rows {
		if strings.Contains(r, "Sort Key:") {
			sortLine = strings.TrimSpace(r)
			break // the top Sort; deeper subplan Sorts are not this pin
		}
	}
	if sortLine != "Sort Key: max" {
		t.Errorf("expected byte-identical `Sort Key: max`; got:\n%s", strings.Join(rows, "\n"))
	}
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "Group Key: (InitPlan 1).col1") {
		t.Errorf("expected Group Key InitPlan numbering intact; got:\n%s", joined)
	}
	assertNoOpaqueExpr(t, joined)
}

func TestSynthAggCallDeclinesTableZeroArg(t *testing.T) {
	// DS Q49 guard: a real call over derived (table-0) inputs must not
	// synthesise — PG prints the boundary alias (`in_web.return_ratio`),
	// never the half-chased `((sum / sum))`.
	derived := &optimizer.AggregateCall{
		Name: "sum",
		Arg: &optimizer.BinaryOp{
			Left:  &optimizer.ColumnRef{Name: "sum", SourceTableIdx: 0},
			Right: &optimizer.ColumnRef{Name: "sum", SourceTableIdx: 0},
		},
	}
	if _, hit := synthAggCall(derived); hit {
		t.Errorf("table-0-operand call must decline")
	}
	sourced := &optimizer.AggregateCall{
		Name: "sum",
		Arg:  &optimizer.ColumnRef{Name: "x", SourceTableIdx: 1},
	}
	if _, hit := synthAggCall(sourced); !hit {
		t.Errorf("real-table-operand call must synthesise")
	}
	if exprHasTableZeroRef(derived.Arg) != true {
		t.Errorf("table-0 detector must fire on derived operands")
	}
	if exprHasTableZeroRef(sourced.Arg) {
		t.Errorf("table-0 detector must not fire on base operands")
	}
}

func TestFilterBlocksChase(t *testing.T) {
	// Q44 guard: a Filter carrying a sublink predicate (HAVING-shaped,
	// InitPlan inside) walls off the levels below it — the chase must
	// not synthesise an Aggs call out from under it. Plain predicates
	// step through.
	if !filterBlocksChase(&optimizer.Filter{Predicate: &optimizer.SubqueryExpr{}}) {
		t.Errorf("sublink-predicate Filter must block the chase")
	}
	if filterBlocksChase(&optimizer.Filter{Predicate: &optimizer.ColumnRef{}}) {
		t.Errorf("plain-predicate Filter must not block the chase")
	}
	if filterBlocksChase(nil) {
		t.Errorf("nil Filter must not block the chase")
	}
	// The live Q44 revert (rank_col, not avg-synth) is verified at the
	// explain gate; no unit fixture routes a Filter node (as opposed to
	// an attached HAVING) into a chase path.
}

func TestQualifierNamesCTE(t *testing.T) {
	// Q54 guard: a qualifier naming a statement CTE declines the
	// stop-and-render (PG may have flattened the CTE away per K31, so
	// printing it asserts a boundary PG lacks). Consumer aliases fall
	// through (PG prints `m.col` too).
	reg := &subPlanReg{
		rel: &explainNames{
			bySource: map[int32]string{7: "my_customers", 3: "customer"},
			bySrc:    map[int16]int32{9: 7, 2: 3},
			cols: map[int32]map[string]bool{
				7: {"c_customer_sk": true},
				3: {"c_customer_sk": true},
			},
		},
		cte: &cteHoist{order: []*cteSection{{name: "my_customers"}}},
	}
	set := cteNameSet(reg)
	if !qualifierNamesCTE(reg, &optimizer.ColumnRef{Name: "c_customer_sk", SourceTableIdx: 9}, set) {
		t.Errorf("CTE qualifier must be recognised")
	}
	if qualifierNamesCTE(reg, &optimizer.ColumnRef{Name: "c_customer_sk", SourceTableIdx: 2}, set) {
		t.Errorf("base qualifier must fall through")
	}
	if qualifierNamesCTE(nil, &optimizer.ColumnRef{Name: "c_customer_sk", SourceTableIdx: 9}, set) {
		t.Errorf("nil registry must fall through")
	}
	if qualifierNamesCTE(reg, &optimizer.ColumnRef{Name: "c_customer_sk", SourceTableIdx: 9}, cteNameSet(nil)) {
		t.Errorf("empty CTE set must fall through")
	}
	// The live Q54 revert (bare `c_customer_sk`) is verified at the
	// explain gate.
}

func TestExprHasSubplanOrOuterRef(t *testing.T) {
	// The Arm-S fail-closed guard: any sublink or outer reference in a
	// grouping expression declines key-line expansion (numbering belongs
	// to the main plan walk).
	blocked := []optimizer.Expr{
		&optimizer.SubqueryExpr{},
		&optimizer.ExistsExpr{},
		&optimizer.InExpr{},
		&optimizer.ArraySubqueryExpr{},
		&optimizer.OuterColumnRef{},
		&optimizer.ExecParamRef{},
	}
	for i, e := range blocked {
		if !exprHasSubplanOrOuterRef(e) {
			t.Errorf("blocked[%d] %T: want true", i, e)
		}
	}
	plain := []optimizer.Expr{
		&optimizer.ColumnRef{},
		&optimizer.FuncCall{Name: "count", Star: true},
		&optimizer.FuncCall{Name: "count", Args: []optimizer.Expr{&optimizer.ColumnRef{}}, Distinct: true},
	}
	for i, e := range plain {
		if exprHasSubplanOrOuterRef(e) {
			t.Errorf("plain[%d] %T: want false", i, e)
		}
	}
}
