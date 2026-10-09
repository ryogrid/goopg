package executor

import (
	"strings"
	"testing"
)

// TestWholeRowOfNullExtendedRowIsNull pins M0146-0047 against PG 18.3: the
// whole-row value of a LEFT JOIN's unmatched (null-extended) row is NULL,
// so count(b) skips it and coalesce(b::text, …) replaces it, while a matched
// row whose other columns are NULL stays a non-NULL composite. The witness is
// b's NOT NULL primary-key column.
func TestWholeRowOfNullExtendedRowIsNull(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE wa (id int, k int)")
	runSQL(t, ctx, "CREATE TABLE wb (k int PRIMARY KEY, x int, y text)")
	runSQL(t, ctx, "INSERT INTO wa VALUES (1,1),(2,2),(3,3),(4,NULL)")
	runSQL(t, ctx, "INSERT INTO wb VALUES (1,10,'a'),(2,NULL,NULL)")

	for _, c := range []struct{ sql, want string }{
		// PG: 2 | 4 — rows 3 and 4 are null-extended.
		{"SELECT count(b), count(*) FROM wa a LEFT JOIN wb b ON b.k = a.k", "2|4"},
		// Matched row 2 has NULL x and y but is a real row.
		{"SELECT string_agg(coalesce(b.k::text, '-') || ':' || (b IS NULL)::text || ':' || coalesce(b::text IS NULL, true)::text, ',' ORDER BY a.id) FROM wa a LEFT JOIN wb b ON b.k = a.k",
			"1:false:false,2:false:false,-:true:true,-:true:true"},
		// Outside an outer join nothing changes.
		{"SELECT count(b) FROM wb b", "2"},
	} {
		got := strings.Join(renderRows(runSQL(t, ctx, c.sql)), ";")
		if got != c.want {
			t.Errorf("%s\n got %q, want %q", c.sql, got, c.want)
		}
	}
}
