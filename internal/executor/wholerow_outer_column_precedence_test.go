package executor

import (
	"strings"
	"testing"
)

// TestBareNamePrefersOuterColumnOverLocalWholeRow pins M0146-0047c against
// PG 18.3's transformColumnRef: a bare name is first looked up as a COLUMN at
// every visible query level (colNameToVar) and only then as a relation name
// for a whole-row reference (refnameNamespaceItem). goopg tried both per
// level, so a sublink's own relation `b` shadowed the outer column `wd.b`.
func TestBareNamePrefersOuterColumnOverLocalWholeRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE wa (id int, k int)")
	runSQL(t, ctx, "CREATE TABLE wb (k int, x int, y text)")
	runSQL(t, ctx, "CREATE TABLE wd (b int)")
	runSQL(t, ctx, "INSERT INTO wa VALUES (1,1)")
	runSQL(t, ctx, "INSERT INTO wb VALUES (1,10,'a')")
	runSQL(t, ctx, "INSERT INTO wd VALUES (7)")

	for _, c := range []struct{ sql, want string }{
		// The outer column wins over the sublink's relation of that name.
		{"SELECT (SELECT b FROM wb b LIMIT 1) FROM wd", "7"},
		{"SELECT wd.b FROM wd WHERE EXISTS (SELECT 1 FROM wb b WHERE b = 7)", "7"},
		{"SELECT wd.b, x.r FROM wd LEFT JOIN LATERAL (SELECT b AS r FROM wa a JOIN wb b ON b.k = a.k) x ON true", "7|7"},
		// No column of that name anywhere: the local whole row.
		{"SELECT (SELECT b FROM wb b LIMIT 1) FROM wa", "(1,10,a)"},
		// A relation name only: the innermost relation of that name.
		{"SELECT (SELECT wd FROM wb wd LIMIT 1) FROM wd", "(1,10,a)"},
		{"SELECT (SELECT wd FROM wb LIMIT 1) FROM wd", "(7)"},
		// Same level: the column wins too.
		{"SELECT b FROM wd JOIN wb b ON true", "7"},
	} {
		got := strings.Join(renderRows(runSQL(t, ctx, c.sql)), ";")
		if got != c.want {
			t.Errorf("%s\n got %q, want %q", c.sql, got, c.want)
		}
	}
}
