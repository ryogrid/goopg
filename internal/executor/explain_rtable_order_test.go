package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestRelationSuffixesFollowFlatRtableOrder pins M0146-0005df against PG
// 18.3: set_rtable_names hands out `_N` suffixes in glob->finalrtable order,
// where the top query's own range table comes first and a CTE body — a
// subplan, flattened only after the top plan — comes after it. So the main
// query's `zrt` keeps the bare name and the CTE body's scan is `zrt_1`,
// although the CTE body is planned (and its relation allocated) first.
// Every want line is PG's output.
func TestRelationSuffixesFollowFlatRtableOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE zrt (a int, b int)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	q := "WITH c AS (SELECT a, count(*) n FROM zrt GROUP BY a) SELECT * FROM c, c c2, zrt WHERE c.a = c2.a AND zrt.a = c.a"
	var lines []string
	for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, strings.TrimSpace(r[0].StringValue()))
		}
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"Group Key: zrt_1.a",
		"->  Seq Scan on zrt zrt_1",
		"->  Seq Scan on zrt",
		"Hash Cond: (zrt.a = c.a)",
	} {
		found := false
		for _, l := range lines {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("want line %q in:\n%s", want, got)
		}
	}
}
