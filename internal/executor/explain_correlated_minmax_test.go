package executor

import (
	"strings"
	"testing"
)

// M0146-0114: PG's min/max rewrite (planagg.c build_minmax_path) also fires
// inside a correlated SubPlan: the InitPlan takes the outer value as a param.
//
//	Seq Scan on mm1
//	  SubPlan 2
//	    ->  Result
//	          InitPlan 1
//	            ->  Limit
//	                  ->  Index Only Scan using mm2_u on mm2
//	                        Index Cond: ((u IS NOT NULL) AND (u > mm1.f1))
//
// goopg declined the rewrite for a correlated WHERE and printed an Aggregate.
// The InitPlan re-runs per outer row, so the results must follow f1.
func TestCorrelatedMinMaxInitPlan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE mm1 (f1 int)",
		"CREATE TABLE mm2 (u int, v int)",
		"INSERT INTO mm1 VALUES (0), (5), (17), (100), (NULL), (-3)",
		"INSERT INTO mm2 SELECT g, g % 7 FROM generate_series(1, 50) g",
		"INSERT INTO mm2 VALUES (NULL, 1)",
		"CREATE INDEX mm2_u ON mm2 (u)",
		"ANALYZE mm1", "ANALYZE mm2",
	} {
		runSQL(t, ctx, q)
	}
	plan := strings.Join(renderRows(runSQL(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT f1, (SELECT min(u) FROM mm2 WHERE u > f1) AS gt FROM mm1")), "\n")
	for _, want := range []string{"SubPlan 2", "InitPlan 1", "->  Limit", "Index Only Scan using mm2_u on mm2",
		"Index Cond: ((u IS NOT NULL) AND (u > mm1.f1))"} {
		if !strings.Contains(plan, want) {
			t.Errorf("want %q, as PG prints:\n%s", want, plan)
		}
	}
	for _, tc := range []struct{ q, want string }{
		{"SELECT f1, (SELECT min(u) FROM mm2 WHERE u > f1) FROM mm1 ORDER BY f1", "-3|1;0|1;5|6;17|18;100|NULL;NULL|NULL"},
		{"SELECT f1, (SELECT max(u) FROM mm2 WHERE u < f1) FROM mm1 ORDER BY f1", "-3|NULL;0|NULL;5|4;17|16;100|50;NULL|NULL"},
	} {
		got := renderRows(runSQL(t, ctx, tc.q))
		if strings.Join(got, ";") != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.q, strings.Join(got, ";"), tc.want)
		}
	}
}

// M0148-0001c slice (a): Sort / Group Key sites above a min/max-rewritten
// Result print PG's initplan param label (the chase lands on the Result's
// SubqueryExpr target, ruleutils get_special_variable prints it), not
// goopg's output alias — aggregates.sql's
//
//	Group Key: (InitPlan 1).col1        (select distinct max(unique2) …)
//	Sort Key: ((InitPlan 1).col1)       (order by 1 / order by max(unique2))
//	Sort Key: (((InitPlan 1).col1 + 1)) (order by max(unique2)+1)
//
// The S18 force-paren wrap fires on the non-Var referent, so the bare label
// carries its own pair and the Sort line carries the site's.
func TestExplainKeyOverMinMaxResultInitPlanRef(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE mmk (u2 int)",
		"INSERT INTO mmk SELECT g FROM generate_series(1, 20) g",
		"CREATE INDEX mmk_u2 ON mmk (u2)",
		"ANALYZE mmk",
	} {
		runSQL(t, ctx, q)
	}
	for _, tc := range []struct{ q, want string }{
		{"EXPLAIN (COSTS OFF) select distinct max(u2) from mmk", "Group Key: (InitPlan 1).col1"},
		{"EXPLAIN (COSTS OFF) select max(u2) from mmk order by 1", "Sort Key: ((InitPlan 1).col1)"},
		{"EXPLAIN (COSTS OFF) select max(u2) from mmk order by max(u2)", "Sort Key: ((InitPlan 1).col1)"},
		{"EXPLAIN (COSTS OFF) select max(u2) from mmk order by max(u2)+1", "Sort Key: (((InitPlan 1).col1 + 1))"},
	} {
		plan := strings.Join(renderRows(runSQL(t, ctx, tc.q)), "\n")
		if !strings.Contains(plan, tc.want) {
			t.Errorf("%s:\nwant %q in\n%s", tc.q, tc.want, plan)
		}
		if strings.Contains(plan, "Group Key: max\n") || strings.Contains(plan, "Sort Key: max\n") {
			t.Errorf("%s:\nkey still renders the output alias in\n%s", tc.q, plan)
		}
	}
}

// TestExplainMinMaxMultiTargetProjectSet pins M0148-0001c slice (b): a
// multi-target tlist holding exactly ONE strict min/max agg plus a Var-free
// generate_series extra takes PG's create_minmaxagg_plan shape — the whole
// surviving tlist sits in the childless Result's targets, the tlist SRF is
// expanded by the ProjectSet above it, and the ORDER BY over the SRF's alias
// renders the funcexpr itself (the block at
// postgres/src/test/regress/expected/aggregates.out:1169-1180):
//
//	Sort
//	  Sort Key: (generate_series(1, 3)) DESC
//	  InitPlan 1
//	    ->  Limit
//	          ->  Index Only Scan Backward using tenk1_unique2 on tenk1
//	                Index Cond: (unique2 IS NOT NULL)
//	  ->  ProjectSet
//	        ->  Result
func TestExplainMinMaxMultiTargetProjectSet(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE mmt (u2 int)",
		"INSERT INTO mmt SELECT g FROM generate_series(1, 20) g",
		"CREATE INDEX mmt_u2 ON mmt (u2)",
		"ANALYZE mmt",
	} {
		runSQL(t, ctx, q)
	}
	plan := strings.Join(renderRows(runSQL(t, ctx,
		"EXPLAIN (COSTS OFF) select max(u2), generate_series(1,3) as g from mmt order by g desc")), "\n")
	for _, want := range []string{
		"Sort Key: (generate_series(1, 3)) DESC",
		"InitPlan 1",
		"->  Limit",
		"->  Index Only Scan Backward using mmt_u2 on mmt",
		"Index Cond: (u2 IS NOT NULL)",
		"->  ProjectSet",
		"->  Result",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("want %q, as PG prints:\n%s", want, plan)
		}
	}
	if strings.Contains(plan, "Aggregate") {
		t.Errorf("multi-target tlist fell back to the Aggregate path:\n%s", plan)
	}
	// PG prints the query level's initPlan list on the top node (the Sort),
	// BEFORE the children; a Result-deep InitPlan line would land after the
	// ProjectSet arrow.
	if iInit, iPS := strings.Index(plan, "InitPlan 1"), strings.Index(plan, "->  ProjectSet"); iInit >= 0 && iPS >= 0 && iInit > iPS {
		t.Errorf("InitPlan 1 must print on the Sort, above ProjectSet:\n%s", plan)
	}
	// The executed rows: the ProjectSet zips the SRF's 3 steps over the
	// childless Result's one row, each carrying the InitPlan's max.
	rows := renderRows(runSQL(t, ctx,
		"select max(u2), generate_series(1,3) as g from mmt order by g desc"))
	want := []string{"20|3", "20|2", "20|1"}
	if len(rows) != len(want) {
		t.Fatalf("want rows %v, got %v", want, rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d: want %q, got %v", i, want[i], rows)
		}
	}

	// Const-only extra: no SRF to expand, so no ProjectSet — the surviving
	// tlist is the childless Result's own targets (createplan.c:2588-2612).
	constPlan := strings.Join(renderRows(runSQL(t, ctx,
		"EXPLAIN (COSTS OFF) select max(u2), 5 from mmt")), "\n")
	if !strings.Contains(constPlan, "Result") || !strings.Contains(constPlan, "InitPlan 1") {
		t.Errorf("want Result + InitPlan 1 for the const-extra rewrite:\n%s", constPlan)
	}
	if strings.Contains(constPlan, "ProjectSet") {
		t.Errorf("no ProjectSet is due without a tlist SRF:\n%s", constPlan)
	}
	if got := renderRows(runSQL(t, ctx, "select max(u2), 5 from mmt")); len(got) != 1 || got[0] != "20|5" {
		t.Errorf("want [20|5], got %v", got)
	}

	// M0148-0001c slice (c): the two-agg min/maxttest shape lands — TWO
	// InitPlans (min forward, max Backward) over one childless Result, each
	// InitPlan a Limit over the presorted IOS (aggregates.out:1069-1092).
	pairPlan := strings.Join(renderRows(runSQL(t, ctx, "EXPLAIN (COSTS OFF) select min(u2), max(u2) from mmt")), "\n")
	for _, want := range []string{"Result", "InitPlan 1", "InitPlan 2", "Limit", "Index Only Scan"} {
		if !strings.Contains(pairPlan, want) {
			t.Errorf("multi-agg EXPLAIN missing %q:\n%s", want, pairPlan)
		}
	}
	if strings.Contains(pairPlan, "Aggregate") {
		t.Errorf("multi-agg should be the InitPlan rewrite, not the Aggregate path:\n%s", pairPlan)
	}
	if got := renderRows(runSQL(t, ctx, "select min(u2), max(u2) from mmt")); len(got) != 1 || got[0] != "1|20" {
		t.Errorf("multi-agg rows = %v, want [1|20]", got)
	}

	// Scope bounds (deferral ledger): an aggregate extra keeps the ordinary
	// Aggregate path.
	for _, q := range []string{
		"EXPLAIN (COSTS OFF) select max(u2), count(*) from mmt",
	} {
		p := strings.Join(renderRows(runSQL(t, ctx, q)), "\n")
		if !strings.Contains(p, "Aggregate") {
			t.Errorf("%s: scope bound — want the Aggregate path:\n%s", q, p)
		}
	}
}
