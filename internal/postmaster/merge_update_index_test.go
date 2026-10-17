package postmaster

import (
	"strings"
	"testing"
)

// TestMergeUpdateMaintainsIndexes pins M0146-0169 against PG 18.3. A MERGE
// WHEN MATCHED THEN UPDATE writes a new heap version; like UPDATE
// (ExecUpdate → ExecInsertIndexTuples) it must file an index entry for that
// version in every index of the target, and check unique constraints before
// writing. Without the entries an index scan reached only the dead old
// version and the row vanished from every index access path; without the
// check a key change onto an existing key created a duplicate primary key.
func TestMergeUpdateMaintainsIndexes(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, c := range []struct {
		sql  string
		want []string
		err  string
	}{
		{sql: "CREATE TABLE m1 (a int PRIMARY KEY, b text, c int)"},
		{sql: "CREATE INDEX m1_c ON m1(c)"},
		{sql: "INSERT INTO m1 VALUES (1,'x',10),(2,'y',20)"},
		{sql: "MERGE INTO m1 t USING (VALUES (1,'m',30)) v(a,b,c) ON t.a = v.a WHEN MATCHED THEN UPDATE SET b = v.b, c = v.c"},
		{sql: "SET enable_seqscan = off"},
		{sql: "SELECT a, b, c FROM m1 WHERE a = 1", want: []string{"1", "m", "30"}},
		{sql: "SELECT a, b, c FROM m1 WHERE c = 30", want: []string{"1", "m", "30"}},
		{sql: "SELECT a FROM m1 WHERE c = 10"},
		{sql: "SELECT a, b FROM m1 ORDER BY a", want: []string{"1", "m", "2", "y"}},
		{sql: "MERGE INTO m1 t USING (VALUES (1)) v(a) ON t.a = v.a WHEN MATCHED THEN UPDATE SET a = 2",
			err: `duplicate key value violates unique constraint \"m1_pkey\"`},
		{sql: "MERGE INTO m1 t USING (VALUES (1)) v(a) ON t.a = v.a WHEN MATCHED THEN UPDATE SET a = 5"},
		{sql: "SELECT a, b FROM m1 WHERE a = 5", want: []string{"5", "m"}},
		{sql: "SELECT a FROM m1 WHERE a = 1"},
	} {
		got, e := allRowsQuery(t, conn, c.sql)
		if c.err != "" {
			if !strings.Contains(e, strings.ReplaceAll(c.err, `\"`, `"`)) {
				t.Errorf("%s: error %q, want %q", c.sql, e, c.err)
			}
			continue
		}
		if e != "" || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s = %q (err %q), want %q", c.sql, got, e, c.want)
		}
	}
}
