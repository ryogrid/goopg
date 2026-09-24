package executor

import (
	"strings"
	"testing"
)

// TestExplainRendersEveryStackedFilter pins M0145-0008j (every predicate over
// a scan is printed) and M0145-0008o (a pseudoconstant qual gates the plan as
// PG's Result / One-Time Filter / InitPlan). Expected plans and rows are PG
// 18.3's, captured on a scratch cluster 2026-09-25 with the same tables.
//
// Before 0008j the scan line kept only the outermost of two stacked Filters,
// hiding `a > 1`. Before 0008o the uncorrelated EXISTS stayed a per-row
// `EXISTS(SubPlan 1)` in that Filter, where PG evaluates it once as an
// initplan and gates the scan on it (create_gating_plan, make_subplan).
func TestExplainRendersEveryStackedFilter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE sf1 (a int)",
		"CREATE TABLE sf2 (b int)",
		"CREATE TABLE sf3 (s text)",
		"INSERT INTO sf1 VALUES (1),(2),(3)",
		"INSERT INTO sf3 VALUES ('x')",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, sql string
		want      []string
	}{
		{
			name: "EXISTS gates the scan; a > 1 stays on it",
			sql:  "SELECT a FROM sf1 WHERE a > 1 AND EXISTS (SELECT 1 FROM sf3)",
			want: []string{"Result", "One-Time Filter: (InitPlan 1).col1", "InitPlan 1",
				"->  Seq Scan on sf3", "->  Seq Scan on sf1", "Filter: (a > 1)"},
		},
		{
			name: "NOT EXISTS",
			sql:  "SELECT a FROM sf1 WHERE a > 1 AND NOT EXISTS (SELECT 1 FROM sf3)",
			want: []string{"Result", "One-Time Filter: (NOT (InitPlan 1).col1)", "InitPlan 1",
				"->  Seq Scan on sf3", "->  Seq Scan on sf1", "Filter: (a > 1)"},
		},
		{
			// No gate: a target-list sublink is not a qual. It is still an
			// InitPlan, listed under the node that reads it.
			name: "EXISTS in the target list",
			sql:  "SELECT a, EXISTS (SELECT 1 FROM sf3) FROM sf1",
			want: []string{"Seq Scan on sf1", "InitPlan 1", "->  Seq Scan on sf3"},
		},
		{
			name: "scalar InitPlan compared with a constant",
			sql:  "SELECT a FROM sf1 WHERE (SELECT count(*) FROM sf3) > 0",
			want: []string{"Result", "One-Time Filter: ((InitPlan 1).col1 > 0)", "InitPlan 1",
				"->  Aggregate", "->  Seq Scan on sf3", "->  Seq Scan on sf1"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+c.sql)
			got := make([]string, 0, len(rows))
			for _, r := range rows {
				got = append(got, strings.TrimSpace(r))
			}
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("plan mismatch\n got:\n%s\nwant (PG 18.3):\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}

	// Values: the gate passes (sf3 has a row), so a > 1 decides; NOT EXISTS
	// gates everything out.
	for _, v := range []struct {
		sql  string
		want string
	}{
		{"SELECT a FROM sf1 WHERE a > 1 AND EXISTS (SELECT 1 FROM sf3) ORDER BY a", "2,3"},
		{"SELECT a FROM sf1 WHERE NOT EXISTS (SELECT 1 FROM sf3)", ""},
		{"SELECT a FROM sf1 JOIN sf2 ON a = b WHERE EXISTS (SELECT 1 FROM sf3)", ""},
	} {
		rows, err := runQueryWithErr(ctx, v.sql)
		if err != nil {
			t.Fatalf("%s: %v", v.sql, err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, r[0].Format())
		}
		if strings.Join(got, ",") != v.want {
			t.Errorf("%s = %q, want %q", v.sql, strings.Join(got, ","), v.want)
		}
	}
}
