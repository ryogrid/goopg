package postmaster

import (
	"strings"
	"testing"
)

// TestWithMerge pins M0146-0085: gram.y's MergeStmt starts with
// opt_with_clause, so a WITH clause may lead MERGE and its CTEs are visible to
// the USING source and the ON / WHEN clauses; a data-modifying CTE runs first.
// It used to be a syntax error at MERGE. Expectations are PostgreSQL 18.3's.
func TestWithMerge(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, c := range []struct {
		sql  string
		want []string
	}{
		{"CREATE TABLE t(a int, b text)", nil},
		{"INSERT INTO t VALUES (8, 'old'), (9, 'keep')", nil},
		{"WITH s AS (SELECT 8 AS a) MERGE INTO t USING s ON t.a = s.a WHEN MATCHED THEN UPDATE SET a = 80", nil},
		{"SELECT string_agg(a || ':' || b, ',' ORDER BY a) FROM t", []string{"9:keep,80:old"}},
		{"WITH lim AS (SELECT 50 AS v) MERGE INTO t USING (SELECT a FROM t) src ON t.a = src.a AND t.a > (SELECT v FROM lim) WHEN MATCHED THEN DELETE", nil},
		{"SELECT string_agg(a::text, ',' ORDER BY a) FROM t", []string{"9"}},
		{"WITH ins AS (INSERT INTO t VALUES (100, 'cte') RETURNING a) MERGE INTO t USING ins ON t.a = ins.a WHEN MATCHED THEN UPDATE SET b = 'merged' WHEN NOT MATCHED THEN INSERT VALUES (ins.a, 'inserted')", nil},
		// The CTE's row is invisible to MERGE's scan of t (one snapshot), so
		// the source row does not match and is inserted too — PG answers the same.
		{"SELECT string_agg(a || ':' || b, ',' ORDER BY a, b) FROM t", []string{"9:keep,100:cte,100:inserted"}},
	} {
		got, e := setClauseQuery(t, conn, c.sql)
		if e != "" || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s = %q (err %q), want %q", c.sql, got, e, c.want)
		}
	}
}
