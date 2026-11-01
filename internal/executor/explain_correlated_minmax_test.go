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
