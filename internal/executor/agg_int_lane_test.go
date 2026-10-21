package executor

import (
	"strings"
	"testing"
)

// TestAggregateIntGroupLaneMatchesStringPath pins the hashed aggregate's
// int64 group lane (slice 24) against the string path: same groups, same
// aggregate values, and the same output ORDER, since the lane must not change
// which row founds a group or when. The key column mixes spellings of one
// value (2, 2.000, 1.0000000000000000000000 next to 1), negatives and zero.
// One query sees a NULL key mid-stream and one a fraction: both demote the
// lane after many int groups exist, and every group must keep its place.
func TestAggregateIntGroupLaneMatchesStringPath(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, "CREATE TABLE ag (k numeric, i int, s text)")
	runSQL(t, ctx, "INSERT INTO ag SELECT (x * 7919) % 300 - 20, x, 's' || (x % 13) FROM generate_series(1, 3000) x")
	runSQL(t, ctx, "INSERT INTO ag VALUES (2.000, 1, 'a'), (1.0000000000000000000000, 2, 'b'), (-3.00, 3, 'c'), (0.0, 4, 'd')")
	runSQL(t, ctx, "CREATE TABLE agn (k numeric, i int)")
	runSQL(t, ctx, "INSERT INTO agn SELECT x % 50, x FROM generate_series(1, 500) x")
	runSQL(t, ctx, "INSERT INTO agn VALUES (NULL, 7), (NULL, 8)")
	runSQL(t, ctx, "INSERT INTO agn SELECT x % 60, x FROM generate_series(1, 300) x")
	runSQL(t, ctx, "CREATE TABLE agf (k numeric, i int)")
	runSQL(t, ctx, "INSERT INTO agf SELECT x % 40, x FROM generate_series(1, 400) x")
	runSQL(t, ctx, "INSERT INTO agf VALUES (2.5, 1), (2.5, 2)")
	runSQL(t, ctx, "INSERT INTO agf SELECT x % 45, x FROM generate_series(1, 200) x")
	for _, tb := range []string{"ag", "agn", "agf"} {
		runSQL(t, ctx, "ANALYZE "+tb)
	}
	for _, q := range []string{
		"SELECT k, count(*), sum(i), max(s) FROM ag GROUP BY k",
		"SELECT i % 97, count(*), min(k) FROM ag GROUP BY i % 97",
		"SELECT k, count(*), sum(i) FROM agn GROUP BY k",
		"SELECT k, count(*), sum(i) FROM agf GROUP BY k",
		"SELECT s, count(*) FROM ag GROUP BY s",
	} {
		got := renderRows(runSQL(t, ctx, q))
		aggIntLaneDisabled = true
		want := renderRows(runSQL(t, ctx, q))
		aggIntLaneDisabled = false
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: int lane output differs from the string path (rows %d vs %d)\nlane   %v\nstring %v", q, len(got), len(want), got, want)
		}
		if len(got) == 0 {
			t.Fatalf("%s: no rows", q)
		}
	}
}
