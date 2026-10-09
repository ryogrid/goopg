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
