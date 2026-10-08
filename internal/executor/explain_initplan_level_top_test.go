package executor

import (
	"strings"
	"testing"
)

// M0146-0104: PG's SS_attach_initplans hangs a query level's initPlans on
// the level's top plan node, so EXPLAIN prints `InitPlan N` there — on the
// join above the scan whose Filter reads it (TPC-DS Q58's GroupAggregate
// over the Gather, not the date_dim scan below it). An initPlan still prints
// once, however many expression copies reference it (a HAVING qual).
func TestExplainInitPlanOnLevelTop(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE m104a (k int, m int, v int)",
		"CREATE TABLE m104b (k int, w int)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	join := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT * FROM m104a JOIN m104b ON m104a.k = m104b.k WHERE m104a.m = (SELECT max(m) FROM m104a)")
	if len(join) < 3 || strings.TrimSpace(join[0]) == "" || strings.HasPrefix(join[0], " ") {
		t.Fatalf("unexpected plan:\n%s", strings.Join(join, "\n"))
	}
	// The InitPlan label is the top node's own section: indented by two,
	// before the join's children.
	top := -1
	for i, l := range join {
		if l == "  InitPlan 1" {
			top = i
		}
		if strings.Contains(l, "InitPlan 1") && strings.HasPrefix(strings.TrimSpace(l), "InitPlan") && l != "  InitPlan 1" {
			t.Errorf("InitPlan 1 printed below the level top:\n%s", strings.Join(join, "\n"))
		}
	}
	if top < 0 {
		t.Errorf("InitPlan 1 is not on the top node:\n%s", strings.Join(join, "\n"))
	}

	having := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT m, count(*) FROM m104a GROUP BY m HAVING count(*) > (SELECT count(*) FROM m104b)"), "\n")
	if c := strings.Count(having, "InitPlan 1\n"); c != 1 {
		t.Errorf("InitPlan 1 section printed %d times:\n%s", c, having)
	}
}
