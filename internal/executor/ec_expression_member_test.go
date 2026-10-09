package executor

import (
	"strings"
	"testing"
)

// TestECExpressionMemberValues pins the values of M0146-0005do's derived
// clauses: with `a = b - 52 AND a = c` the planner may join b's and c's
// relations on the derived `(b - 52) = c`, and a constant on the class
// filters b's relation on `(b - 52) = 7`. Values as PG 18.3 returns them.
func TestECExpressionMemberValues(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE e1 (a int, p int)")
	runSQL(t, ctx, "CREATE TABLE e2 (b int, q int)")
	runSQL(t, ctx, "CREATE TABLE e3 (c int, r int)")
	runSQL(t, ctx, "INSERT INTO e1 SELECT g, g FROM generate_series(1,200) g")
	runSQL(t, ctx, "INSERT INTO e2 SELECT g, g FROM generate_series(1,200) g")
	runSQL(t, ctx, "INSERT INTO e3 SELECT g, g FROM generate_series(1,200) g")
	for _, c := range []struct{ sql, want string }{
		{"SELECT count(*) FROM e1, e2, e3 WHERE e1.a = e2.b - 52 AND e1.a = e3.c", "148"},
		{"SELECT count(*) FROM e1, e2, e3 WHERE e1.a = e2.b - 52 AND e1.a = e3.c AND e3.r < 100", "99"},
		{"SELECT e1.a, e2.b, e3.c FROM e1, e2, e3 WHERE e1.a = e2.b - 52 AND e1.a = e3.c AND e1.a = 7", "7|59|7"},
	} {
		got := strings.Join(renderRows(runSQL(t, ctx, c.sql)), ";")
		if got != c.want {
			t.Errorf("%s\n got %q, want %q", c.sql, got, c.want)
		}
	}
}
