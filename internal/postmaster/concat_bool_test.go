package postmaster

import (
	"strings"
	"testing"
)

// TestConcatBoolUsesTextCast pins M0146-0086: `text || bool` is anytextcat /
// textanycat, SQL functions `$1::text || $2`, so the bool goes through booltext
// ("true" / "false") — not boolout ("t" / "f"), which concat() keeps using.
// The WHERE-clause form reaches the compiled evaluator twin. Expectations are
// PostgreSQL 18.3's.
func TestConcatBoolUsesTextCast(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, c := range []struct {
		sql  string
		want []string
	}{
		{"CREATE TABLE tb(id int, b bool)", nil},
		{"INSERT INTO tb VALUES (1, true), (2, false), (3, NULL)", nil},
		{"SELECT 'x' || true, false || 'y', ('x' || NULL::bool) IS NULL", []string{"xtrue", "falsey", "t"}},
		{"SELECT 'v=' || b, b || '!' FROM tb WHERE id = 2", []string{"v=false", "false!"}},
		{"SELECT id FROM tb WHERE 'v=' || b = 'v=true'", []string{"1"}},
		{"SELECT concat('x', true), 'x' || (1 < 2)", []string{"xt", "xtrue"}},
	} {
		got, e := setClauseQuery(t, conn, c.sql)
		if e != "" || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s = %q (err %q), want %q", c.sql, got, e, c.want)
		}
	}
}
