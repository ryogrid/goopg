package executor

import (
	"strings"
	"testing"
)

// TestAggEmitFoundedMatchesKeyedEmit pins M0122-0012 C29: a Simple-mode hash
// aggregate emits from its founding-order group list — without re-keying the
// int64 lane into the string map or looking every group up again — and its
// rows, order included, are the ones the keyed emit gave. The queries cover
// the pure int64 lane, a lane demoted mid-drain by a fractional key and by a
// NULL, string and multi-column keys, grouping sets (whose empty set is
// founded before the drain), an ungrouped aggregate and empty input.
func TestAggEmitFoundedMatchesKeyedEmit(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE aef (k numeric, s text, i int)")
	runSQL(t, ctx, "INSERT INTO aef SELECT x % 37, 's' || (x % 11), x FROM generate_series(1, 600) x")
	runSQL(t, ctx, "CREATE TABLE aef2 (k numeric, i int)")
	runSQL(t, ctx, "INSERT INTO aef2 SELECT x % 29, x FROM generate_series(1, 300) x")
	runSQL(t, ctx, "INSERT INTO aef2 VALUES (2.5, 1), (NULL, 2), (NULL, 3)")
	runSQL(t, ctx, "INSERT INTO aef2 SELECT x % 31, x FROM generate_series(1, 200) x")
	for _, tb := range []string{"aef", "aef2"} {
		runSQL(t, ctx, "ANALYZE "+tb)
	}
	for _, q := range []string{
		"SELECT k, count(*), sum(i) FROM aef GROUP BY k",
		"SELECT k, count(*), sum(i) FROM aef2 GROUP BY k",
		"SELECT s, count(*), max(i) FROM aef GROUP BY s",
		"SELECT k, s, count(*) FROM aef GROUP BY k, s",
		"SELECT k, s, sum(i) FROM aef GROUP BY GROUPING SETS ((k), (s), ())",
		"SELECT count(*), sum(i) FROM aef",
		"SELECT k, count(*) FROM aef WHERE i < 0 GROUP BY k",
	} {
		got := renderRows(runSQL(t, ctx, q))
		aggEmitFoundedDisabled = true
		want := renderRows(runSQL(t, ctx, q))
		aggEmitFoundedDisabled = false
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: founded emit differs from keyed emit (rows %d vs %d)\nfounded %v\nkeyed   %v", q, len(got), len(want), got, want)
		}
	}
}
