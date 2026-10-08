package executor

import (
	"strings"
	"testing"
)

// M0146-0105: PG numbers a query level's sublinks in preprocess_expression
// order — targetlist (with its resjunk ORDER BY items), then the quals in
// source order, then HAVING — not in the order goopg's plan places them
// (TPC-DS Q6). Each arm of a set operation is a query level of its own, so
// its InitPlan prints on the arm, not on the Append.
func TestExplainSublinkNumberingFollowsQueryOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE m105a (k int, m int, v int)",
		"CREATE TABLE m105b (k int, w int)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	explain := func(q string) string {
		return strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+q), "\n")
	}
	for _, tc := range []struct {
		q    string
		want []string
	}{
		// Two quals on different relations: source order, whichever
		// scan the join puts first.
		{"SELECT * FROM m105a a JOIN m105a b ON a.k = b.k WHERE a.v = (SELECT 1) AND b.m = (SELECT 2)",
			[]string{"(v = (InitPlan 1).col1)", "(m = (InitPlan 2).col1)"}},
		// An ORDER BY sublink is a targetlist entry: numbered before WHERE.
		{"SELECT k FROM m105a WHERE m = (SELECT max(m) FROM m105a) ORDER BY (SELECT min(v) FROM m105a j WHERE j.k = m105a.k)",
			[]string{"(m = (InitPlan 2).col1)", "SubPlan 1"}},
		// targetlist, WHERE, HAVING.
		{"SELECT m, (SELECT 9), count(*) FROM m105a WHERE v > (SELECT 1) GROUP BY m HAVING count(*) > (SELECT 2)",
			[]string{"(v > (InitPlan 2).col1)", "(count(*) > (InitPlan 3).col1)"}},
	} {
		got := explain(tc.q)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s\nwant %q; got:\n%s", tc.q, w, got)
			}
		}
	}
	union := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT k FROM m105a WHERE v = (SELECT 1) UNION ALL SELECT k FROM m105b WHERE w = (SELECT 2)")
	for _, l := range union {
		if l == "  InitPlan 1" || l == "  InitPlan 2" {
			t.Errorf("a set-operation arm's InitPlan printed on the Append:\n%s", strings.Join(union, "\n"))
		}
	}
	if j := strings.Join(union, "\n"); !strings.Contains(j, "InitPlan 1") || !strings.Contains(j, "InitPlan 2") {
		t.Errorf("missing InitPlans:\n%s", j)
	}
}
