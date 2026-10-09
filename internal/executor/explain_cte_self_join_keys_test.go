package executor

import (
	"strings"
	"testing"
)

// M0146-0103: two references to one CTE (TPC-DS Q39's `inv inv1, inv inv2`)
// each publish their own relation id, so a key over the second reference
// deparses with its alias, and an ORDER BY item a WHERE operator over
// literals pins (`inv2.d_moy = 1+1`) sorts nothing, as PG folds it first.
func TestExplainCTESelfJoinSortKeys(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE m103c (k int, m int, v int)"); err != nil {
		t.Fatal(err)
	}
	const with = "EXPLAIN (COSTS OFF) WITH c AS MATERIALIZED (SELECT k, m, v FROM m103c) SELECT a.k, a.v, b.v FROM c a, c b WHERE a.k = b.k"
	for _, tc := range []struct{ tail, want string }{
		{" ORDER BY b.v, a.v", "Sort Key: b.v, a.v"},
		{" AND a.m = 1 AND b.m = 1+1 ORDER BY a.v, b.m, b.v", "Sort Key: a.v, b.v"},
	} {
		joined := strings.Join(runExplainRows(t, ctx, with+tc.tail), "\n")
		if !strings.Contains(joined, tc.want+"\n") {
			t.Errorf("%s: want %q; got:\n%s", tc.tail, tc.want, joined)
		}
	}
}
