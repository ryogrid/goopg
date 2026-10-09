package executor

import (
	"strings"
	"testing"
)

// TestCorrelatedOuterRefIndexKey pins M0146-0015a. A correlated SubPlan must
// use its outer reference as an index key, as PG's match_clause_to_indexcol
// does with the PARAM_EXEC Param an outer Var becomes. After the jointree
// cutover every correlated qual stayed above the search, so the regress
// `subselect` nested EXISTS / NOT EXISTS query scanned whole relations per
// outer row for over an hour. The nested-EXISTS rows guard the two latent
// defects that making correlated quals base restrictions exposed:
//   - the pull-up rebase left a grandparent reference one level too high;
//   - the unnest collectors hoisted a correlated qual out of a semi join's
//     RHS, which read the wrong column.
//
// Every expected value is PG 18.3's for the same data.
func TestCorrelatedOuterRefIndexKey(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, d := range []string{
		"CREATE TABLE cq (u int4, th int4, h int4, u2 int4)",
		"INSERT INTO cq SELECT g, g % 100, g % 10, (g * 7) % 1000 FROM generate_series(0, 999) g",
		"CREATE INDEX cq_th ON cq (th)",
		"CREATE INDEX cq_u2 ON cq (u2)",
		"ANALYZE cq",
	} {
		if err := runDDL(t, ctx, d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	const oneLevel = "SELECT sum((SELECT count(*) FROM cq d WHERE d.th = a.th)) FROM cq a"
	// EXPLAIN on the non-aggregated form: a SubPlan inside an aggregate's
	// argument is not rendered.
	plan := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT (SELECT count(*) FROM cq d WHERE d.th = a.th) FROM cq a"), "\n")
	if !strings.Contains(plan, "Index Cond: (th = a.th)") {
		t.Errorf("the correlated qual must be the SubPlan's index key; got:\n%s", plan)
	}
	cases := []struct {
		q    string
		want string
	}{
		{oneLevel, "10000"},
		{"SELECT sum((SELECT count(*) FROM cq d WHERE d.th > a.th)) FROM cq a WHERE a.u < 50", "37250"},
		{"SELECT count(*) FROM cq a WHERE a.u < 300 AND EXISTS (SELECT 1 FROM cq b WHERE b.th = a.th AND EXISTS (SELECT 1 FROM cq c WHERE c.u2 = a.u2 + 70 AND c.h = b.h))", "280"},
		{"SELECT count(*) FROM cq a WHERE a.u < 300 AND NOT EXISTS (SELECT 1 FROM cq b WHERE b.th = a.th AND b.u <> a.u AND EXISTS (SELECT 1 FROM cq c WHERE c.u2 = a.u2 + 70 AND c.h = b.h))", "20"},
		{"SELECT count(*) FROM cq a WHERE a.u < 300 AND NOT EXISTS (SELECT 1 FROM cq b WHERE b.th = a.th AND b.u <> a.u AND EXISTS (SELECT 1 FROM cq c WHERE c.u2 = a.u2 + 3 AND c.h = b.h))", "300"},
		{"SELECT count(*) FROM cq a WHERE EXISTS (SELECT 1 FROM cq b WHERE b.th = a.th AND NOT EXISTS (SELECT 1 FROM cq d WHERE a.th = d.th))", "0"},
	}
	for _, tc := range cases {
		rows, err := runQueryWithErr(ctx, tc.q)
		if err != nil {
			t.Errorf("%s: %v", tc.q, err)
			continue
		}
		if len(rows) != 1 || rows[0][0].Format() != tc.want {
			var got []string
			for _, r := range rows {
				got = append(got, r[0].Format())
			}
			t.Errorf("%s\n got %v, want PG 18.3's %s", tc.q, got, tc.want)
		}
	}
}
