package executor

import (
	"strings"
	"testing"
)

// M0146-0111: a constant FALSE or NULL WHERE conjunct makes the scope's
// relation dummy in PG (relation_excluded_by_constraints, propagated through
// inner joins by is_dummy_rel), planned as a childless `Result  One-Time
// Filter: false`. goopg scanned and joined everything under `Filter: (false)`.
func TestExplainConstantFalseWhereIsDummy(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE m111a (a int, b int)",
		"CREATE TABLE m111b (a int, c int)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"SELECT * FROM m111a WHERE false", []string{"Result", "  One-Time Filter: false"}},
		{"SELECT * FROM m111a WHERE a > 1 AND null::boolean", []string{"Result", "  One-Time Filter: false"}},
		{"SELECT * FROM m111a JOIN m111b ON m111a.a = m111b.a WHERE false", []string{"Result", "  One-Time Filter: false"}},
		{"SELECT 1 FROM (SELECT * FROM m111a OFFSET 0) ss WHERE false", []string{"Result", "  One-Time Filter: false"}},
		{"SELECT count(*) FROM m111a WHERE false", []string{"Aggregate", "  ->  Result", "        One-Time Filter: false"}},
	} {
		rows := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+tc.q)
		if len(rows) != len(tc.want) {
			t.Errorf("%s: want %d lines, got:\n%s", tc.q, len(tc.want), strings.Join(rows, "\n"))
			continue
		}
		for i, w := range tc.want {
			if rows[i] != w {
				t.Errorf("%s: line %d = %q, want %q", tc.q, i, rows[i], w)
			}
		}
	}
	if got := renderRows(runSQL(t, ctx, "SELECT count(*) FROM m111a WHERE false")); len(got) != 1 || got[0] != "0" {
		t.Errorf("count(*) over a dummy rel = %v, want [0]", got)
	}
}
