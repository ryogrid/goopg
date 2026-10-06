package executor

import (
	"strings"
	"testing"
)

// TestIntegerLiteralIsInt4WhenItFits pins M0146-0062 against PG 18.3.
// make_const (parse_node.c) types an integer literal that fits in 32 bits
// as int4, a wider one as int8; goopg typed every literal int8, so `int4 +
// literal` was int8 arithmetic: `2147483647 + 1` and an int4 column's
// overflow returned a value where PG raises `integer out of range`, and
// pg_typeof(9998) said bigint. A constant folded from int8 arithmetic stays
// int8; a negated literal is typed by its value (doNegate). Each want is
// PG's answer.
func TestIntegerLiteralIsInt4WhenItFits(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE t62 (a int, b smallint, c bigint)")
	runSQL(t, ctx, "INSERT INTO t62 VALUES (2147483647, 32767, 9223372036854775807)")
	for _, c := range []struct{ query, want string }{
		{"SELECT pg_typeof(9998)::text, pg_typeof(9998 + 1)::text, pg_typeof(3000000000)::text, pg_typeof(-2147483648)::text, pg_typeof(3000000000 - 2147483647)::text",
			"integer|integer|bigint|integer|bigint"},
		{"SELECT b + 1, pg_typeof(b + 1)::text FROM t62", "32768|integer"},
		{"SELECT pg_typeof(x)::text FROM (VALUES (1)) v(x)", "integer"},
		// A correlated reference keeps its type.
		{"SELECT v.id, (SELECT pg_typeof(v.x)::text) FROM (VALUES (0, 9998)) v(id, x)", "0|integer"},
		{"SELECT 2147483647::bigint + 1, (3000000000 - 2147483647) * 3, 7 / 2, 7 % 2", "2147483648|2557549059|3|1"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, c.query)), ";"); got != c.want {
			t.Errorf("%s: got %q, want PG's %q", c.query, got, c.want)
		}
	}
	for _, q := range []string{
		"SELECT a + 1 FROM t62",
		"SELECT 2147483647 + 1",
		"SELECT 2147483647 * 2",
		"SELECT -2147483648 / -1",
		"SELECT -2147483648 - 1",
	} {
		_, err := runSQLCtxErr(t, ctx, q)
		if err == nil || !strings.Contains(err.Error(), "integer out of range") {
			t.Errorf("%s: got err %v, want PG's `integer out of range`", q, err)
		}
	}
}
