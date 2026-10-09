package executor

import (
	"strings"
	"testing"
)

// TestLateralSublinkResultsFollowTheLeftRow pins M0146-0079 against PG 18.3.
// A sublink inside a LATERAL item can depend only on the LATERAL's left row,
// as `… WHERE unique2 = v.x` does. Its cache key then encodes either the
// scanned row (the same rows for every left row) or nothing at all
// (IsNonCorrelated). The scoped cache used to be cleared only when the
// OuterRows depth changed. The lateral driver pops one left row and pushes
// the next, keeping the depth, so every left row after the first reused the
// first one's result: an extra `1|1000|0` row for the ANY form, and wrong
// NOT IN and scalar results. The cache is now also cleared when the
// enclosing rows' values change. Every want is PG's result.
func TestLateralSublinkResultsFollowTheLeftRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, q := range []string{
		"CREATE TABLE m79i (f1 int)",
		"INSERT INTO m79i VALUES (0), (5), (7), (123456)",
		"CREATE TABLE m79t (unique1 int, unique2 int)",
		"INSERT INTO m79t VALUES (0, 9998), (5, 1000), (7, 7)",
	} {
		runSQL(t, ctx, q)
	}
	const v4 = "(VALUES (0,9998), (1,1000), (2,9998), (3,7)) v(id,x)"
	const v3 = "(VALUES (0,9998), (1,1000), (2,9998)) v(id,x)"
	for _, c := range []struct{ sql, want string }{
		{"SELECT v.id, ss.f1 FROM " + v4 + ", LATERAL (SELECT f1 FROM m79i WHERE f1 = ANY " +
			"(SELECT unique1 FROM m79t WHERE unique2 = v.x OFFSET 0)) ss ORDER BY 1, 2", "0|0;1|5;2|0;3|7"},
		{"SELECT v.id, ss.f1 FROM " + v4 + ", LATERAL (SELECT f1 FROM m79i WHERE EXISTS " +
			"(SELECT 1 FROM m79t WHERE unique2 = v.x AND unique1 = m79i.f1 OFFSET 0)) ss ORDER BY 1, 2", "0|0;1|5;2|0;3|7"},
		{"SELECT v.id, ss.f1 FROM " + v3 + ", LATERAL (SELECT f1 FROM m79i WHERE f1 NOT IN " +
			"(SELECT unique1 FROM m79t WHERE unique2 = v.x OFFSET 0)) ss ORDER BY 1, 2",
			"0|5;0|7;0|123456;1|0;1|7;1|123456;2|5;2|7;2|123456"},
		{"SELECT v.id, ss.c FROM " + v4 + ", LATERAL (SELECT f1, (SELECT max(unique1) FROM m79t WHERE unique2 = v.x) AS c " +
			"FROM m79i) ss ORDER BY 1, 2 LIMIT 8", "0|0;0|0;0|0;0|0;1|5;1|5;1|5;1|5"},
		{"SELECT v.id, ss.f1 FROM " + v3 + ", LATERAL (SELECT f1 FROM m79i WHERE f1 IN " +
			"(SELECT unique1 FROM m79t WHERE unique2 = v.x)) ss ORDER BY 1, 2", "0|0;1|5;2|0"},
		{"SELECT a.id, w.n FROM (VALUES (1), (2)) a(id), LATERAL (SELECT v.x, (SELECT count(*) FROM m79i WHERE f1 = ANY " +
			"(SELECT unique1 FROM m79t WHERE unique2 = v.x OFFSET 0)) AS n FROM (VALUES (9998 * a.id), (1000)) v(x)) w ORDER BY 1, 2",
			"1|1;1|1;2|0;2|1"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, c.sql)), ";"); got != c.want {
			t.Errorf("%s\ngot  %s\nwant PG's %s", c.sql, got, c.want)
		}
	}
}
