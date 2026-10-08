package executor

import (
	"strings"
	"testing"
)

// M0146-0112: PG drops a dummy UNION ALL member (a constant-false WHERE) from
// the Append, elides an Append left with one member, and plans an all-dummy
// UNION ALL as one childless `Result  One-Time Filter: false`. The surviving
// member still answers to the first member's column names.
func TestExplainDummyUnionAllMemberDropped(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE m112a (a int, b int)",
		"CREATE TABLE m112b (a int, c int)",
		"CREATE TABLE m112c (a int)",
		"INSERT INTO m112b VALUES (1, 1), (2, 2)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"SELECT a FROM m112a WHERE false UNION ALL SELECT a FROM m112b", []string{"Seq Scan on m112b"}},
		{"SELECT a FROM m112a UNION ALL SELECT a FROM m112b WHERE false", []string{"Seq Scan on m112a"}},
		{"SELECT a FROM m112a WHERE false UNION ALL SELECT a FROM m112b WHERE false",
			[]string{"Result", "  One-Time Filter: false"}},
		{"SELECT a FROM m112a UNION ALL SELECT a FROM m112b WHERE false UNION ALL SELECT a FROM m112c",
			[]string{"Append", "  ->  Seq Scan on m112a", "  ->  Seq Scan on m112c"}},
	} {
		rows := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+tc.q)
		if strings.Join(rows, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s:\nwant:\n%s\ngot:\n%s", tc.q, strings.Join(tc.want, "\n"), strings.Join(rows, "\n"))
		}
	}
	const q = "SELECT a AS x FROM m112a WHERE false UNION ALL SELECT a FROM m112b ORDER BY 1"
	out := planOne(t, q, ctx.Catalog).Output()
	if len(out) != 1 || out[0].Name != "x" {
		t.Errorf("the surviving member must keep the first member's column name x, got %v", out)
	}
	if rows := renderRows(runSQL(t, ctx, q)); len(rows) != 2 {
		t.Errorf("want 2 rows, got %v", rows)
	}
}
