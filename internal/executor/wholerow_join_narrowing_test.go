package executor

import (
	"strings"
	"testing"
)

// TestWholeRowReadAcrossJoinKeepsEveryColumn pins M0146-0047a against PG
// 18.3: a whole-row reference reads every column of its relation, so the
// join's output narrowing must keep the columns the query never names. The
// collectors saw the bare name `b`, which matched no column, and the joined
// row came out `(1,,)`.
func TestWholeRowReadAcrossJoinKeepsEveryColumn(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE wa (id int, k int)")
	runSQL(t, ctx, "CREATE TABLE wb (k int, x int, y text)")
	runSQL(t, ctx, "INSERT INTO wa VALUES (1,1),(2,2)")
	runSQL(t, ctx, "INSERT INTO wb VALUES (1,10,'a')")
	runSQL(t, ctx, "CREATE TABLE wc (id int, wb int)")
	runSQL(t, ctx, "INSERT INTO wc VALUES (1,99)")
	runSQL(t, ctx, "CREATE TABLE wd (b int)")
	runSQL(t, ctx, "INSERT INTO wd VALUES (7)")

	for _, c := range []struct{ sql, want string }{
		{"SELECT a.id, b FROM wa a JOIN wb b ON b.k = a.k", "1|(1,10,a)"},
		{"SELECT a.id, b FROM wa a LEFT JOIN wb b ON b.k = a.k WHERE a.id = 1", "1|(1,10,a)"},
		// Unaliased: the table name is the whole-row reference.
		{"SELECT wa.id, wb FROM wa JOIN wb ON wb.k = wa.k", "1|(1,10,a)"},
		// The other side's whole row, next to a narrowed column.
		{"SELECT a, b.x FROM wa a JOIN wb b ON b.k = a.k", "(1,1)|10"},
		// Read only in GROUP BY / inside a scalar subquery.
		{"SELECT b FROM wa a JOIN wb b ON b.k = a.k GROUP BY b", "(1,10,a)"},
		{"SELECT (SELECT b::text) FROM wa a JOIN wb b ON b.k = a.k", "(1,10,a)"},
		// Through a derived table that is pulled up into the parent.
		{"SELECT t.r FROM (SELECT b AS r FROM wa a JOIN wb b ON b.k = a.k) t", "(1,10,a)"},
		// A column of that name wins over the relation (transformColumnRef):
		// `wb` is wc's column here, not wb's whole row.
		{"SELECT wb FROM wc JOIN wb ON wb.k = wc.id", "99"},
		// A non-LATERAL derived body does not see its parent's FROM list:
		// the parent's column `b` must not stop the body's whole row `b`
		// from being kept.
		{"SELECT t.r, wd.b FROM wd, (SELECT b AS r FROM wa a JOIN wb b ON b.k = a.k) t", "(1,10,a)|7"},
	} {
		got := strings.Join(renderRows(runSQL(t, ctx, c.sql)), ";")
		if got != c.want {
			t.Errorf("%s\n got %q, want %q", c.sql, got, c.want)
		}
	}
}
