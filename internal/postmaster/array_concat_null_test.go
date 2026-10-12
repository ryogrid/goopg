package postmaster

import (
	"strings"
	"testing"
)

// TestArrayConcatNonStrict pins M0146-0125: `||` with an array operand is
// array_cat / array_append / array_prepend (array_userfuncs.c), none of which
// is strict — a NULL array acts as empty, a NULL element is appended as NULL,
// and array_cat is NULL only for two NULLs. Arrays one dimension apart nest.
// Expectations are PostgreSQL 18.3's answers; the WHERE-clause forms reach the
// compiled evaluator twin.
func TestArrayConcatNonStrict(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, sql := range []string{
		"CREATE TABLE at(id int, a int[], n int, b int[])",
		"INSERT INTO at VALUES (1, '{1,2}', NULL, NULL), (2, NULL, 5, '{9}'), (3, NULL, NULL, NULL)",
	} {
		if _, e := setClauseQuery(t, conn, sql); e != "" {
			t.Fatalf("%s: %q", sql, e)
		}
	}
	for _, c := range []struct {
		sql  string
		want []string
	}{
		{"SELECT array[1] || null, array[1] || null::int, null || array[2]", []string{"{1}", "{1,NULL}", "{2}"}},
		{"SELECT null::int[] || array[1], null::int || array[1], (null::int[] || null::int[]) IS NULL", []string{"{1}", "{NULL,1}", "t"}},
		{"SELECT array[1,2] || array[[3,4]], array[[1,2]] || array[3,4]", []string{"{{1,2},{3,4}}", "{{1,2},{3,4}}"}},
		{"SELECT array_cat(null::int[], null::int[]) IS NULL, array_cat(null, array[1])", []string{"t", "{1}"}},
		{"SELECT '{x}'::text[] || 'a b'::text, array[1] || '{2,3}'", []string{`{x,"a b"}`, "{1,2,3}"}},
		{"SELECT 'a'::text || null IS NULL", []string{"t"}},
		{"SELECT a || n, n || a, a || b FROM at WHERE id = 1", []string{"{1,2,NULL}", "{NULL,1,2}", "{1,2}"}},
		{"SELECT a || n, a || b FROM at WHERE id = 3", []string{"{NULL}", ""}},
		{"SELECT id FROM at WHERE (a || n) = '{5}' ORDER BY id", []string{"2"}},
	} {
		got, e := setClauseQuery(t, conn, c.sql)
		if e != "" || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s = %q (err %q), want %q", c.sql, got, e, c.want)
		}
	}
	for _, c := range []struct{ sql, want string }{
		{"SELECT array[[1,2]] || array[[[[3]]]]", "cannot concatenate incompatible arrays"},
		{"SELECT array[1] || '3'", `malformed array literal: "3"`},
	} {
		if _, e := setClauseQuery(t, conn, c.sql); !strings.Contains(e, c.want) {
			t.Errorf("%s: error = %q, want %q", c.sql, e, c.want)
		}
	}
}
