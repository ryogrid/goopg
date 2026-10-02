package executor

import (
	"strings"
	"testing"
)

// TestWindowsDifferingInNullsOrderStayApart pins M0146-0048 against PG 18.3:
// `OVER (ORDER BY x NULLS FIRST)` and `OVER (ORDER BY x)` are two windows,
// so each rank() is computed under its own NULL placement; an explicit
// `ASC NULLS LAST` is the default and shares the window.
func TestWindowsDifferingInNullsOrderStayApart(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, c := range []struct{ sql, want string }{
		{"SELECT string_agg(coalesce(x::text, 'n') || ':' || r1 || '/' || r2, ',' ORDER BY x NULLS LAST) FROM (SELECT x, rank() OVER (ORDER BY x NULLS FIRST) r1, rank() OVER (ORDER BY x) r2 FROM (VALUES (1),(NULL),(2)) v(x)) q",
			"1:2/1,2:3/2,n:1/3"},
		{"SELECT string_agg(coalesce(x::text, 'n') || ':' || r1 || '/' || r2, ',' ORDER BY x NULLS LAST) FROM (SELECT x, rank() OVER (ORDER BY x DESC NULLS LAST) r1, rank() OVER (ORDER BY x DESC) r2 FROM (VALUES (1),(NULL),(2)) v(x)) q",
			"1:2/3,2:1/2,n:3/1"},
		{"SELECT string_agg(coalesce(x::text, 'n') || ':' || r1 || '/' || r2, ',' ORDER BY x NULLS LAST) FROM (SELECT x, rank() OVER (ORDER BY x ASC NULLS LAST) r1, rank() OVER (ORDER BY x) r2 FROM (VALUES (1),(NULL),(2)) v(x)) q",
			"1:1/1,2:2/2,n:3/3"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, c.sql)), ";"); got != c.want {
			t.Errorf("%s\n got %q, want %q", c.sql, got, c.want)
		}
	}
	plan := explainText(t, ctx, "SELECT rank() OVER (ORDER BY x ASC NULLS LAST), rank() OVER (ORDER BY x) FROM (VALUES (1),(NULL),(2)) v(x)")
	if n := strings.Count(plan, "WindowAgg"); n != 1 {
		t.Errorf("ASC NULLS LAST and the default should share one WindowAgg, got %d:\n%s", n, plan)
	}
}
