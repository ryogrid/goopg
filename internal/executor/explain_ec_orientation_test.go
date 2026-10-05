package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestJoinEqualityPrintsInECOrder pins M0146-0005de against PG 18.3. PG
// re-derives every inner-join equality from its equivalence class, and the
// first code path to create a pair fixes its operand order: an index key
// column's own rel first (generate_implied_equalities_for_column), the
// parameterising rel first for the other clauses of an indexed rel's
// parameterised path (get_baserel_parampathinfo), else FROM order
// (make_join_rel). The written order never shows. Each case's want is the
// a/b equality as PG prints it, whatever node carries it.
func TestJoinEqualityPrintsInECOrder(t *testing.T) {
	cases := []struct {
		name, index, query string
		want, notWant      []string
	}{
		{
			name:    "no index: FROM order, not the written order",
			query:   "SELECT * FROM j1, j2 WHERE j2.b = j1.a AND j1.x < j2.y",
			want:    []string{"j1.a = j2.b"},
			notWant: []string{"j2.b = j1.a"},
		},
		{
			name:    "no index: FROM j2, j1",
			query:   "SELECT * FROM j2, j1 WHERE j1.a = j2.b AND j1.x < j2.y",
			want:    []string{"j2.b = j1.a"},
			notWant: []string{"j1.a = j2.b"},
		},
		{
			name:    "index on j1.x: j1's other clause comes from its parameterised path",
			index:   "CREATE INDEX j1x ON j1 (x)",
			query:   "SELECT * FROM j1, j2 WHERE j1.a = j2.b AND j1.x = j2.y",
			want:    []string{"j2.b = "},
			notWant: []string{"j1.a = j2.b", "(a = j2.b)"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, _, cleanup := newDDLFixture(t)
			t.Cleanup(cleanup)
			runSQL(t, ctx, "CREATE TABLE j1 (a int, x int)")
			runSQL(t, ctx, "CREATE TABLE j2 (b int, y int)")
			runSQL(t, ctx, "INSERT INTO j1 SELECT g, g FROM generate_series(1, 1000) g")
			runSQL(t, ctx, "INSERT INTO j2 SELECT g, g FROM generate_series(1, 1000) g")
			if c.index != "" {
				runSQL(t, ctx, c.index)
			}
			ps := optimizer.DefaultPlannerSettings()
			ps.MaxParallelWorkersPerGather = 0
			ps.EnableHashJoin = false
			ps.EnableMergeJoin = false
			var lines []string
			for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+c.query, ps)) {
				if len(r) > 0 && r[0].Kind == KindString {
					lines = append(lines, strings.TrimSpace(r[0].StringValue()))
				}
			}
			got := strings.Join(lines, "\n")
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("want %q in:\n%s", w, got)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unwanted %q in:\n%s", w, got)
				}
			}
		})
	}
}

// TestInferredJoinEqualityPrintsInECOrder pins, against PG 18.3, that an
// equality the query never writes — derived from the equivalence class
// (`ej1.a = ej2.b AND ej2.b = ej3.c` puts ej1.a and ej3.c in one class) — is
// created by the same create_join_clause rules as a written one, so it prints
// in FROM order whichever side is outer (M0146-0042a's first hypothesis; the
// seam already orients these). The 0042a defect itself sits in the candidate
// rebuild (searchedBoundaryRebuild), witnessed by the TPC-DS SF1 Q17/Q25/Q29
// capture in analysis/m0146/m0146-0042a/.
func TestInferredJoinEqualityPrintsInECOrder(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		{
			"select * from ej3, ej1, ej2 where ej1.a = ej2.b and ej2.b = ej3.c and ej1.x < 10 and ej2.y < 20",
			[]string{
				"Nested Loop",
				"Join Filter: (ej3.c = ej1.a)",
				"->  Nested Loop",
				"Join Filter: (ej1.a = ej2.b)",
				"->  Seq Scan on ej2",
				"Filter: (y < 20)",
				"->  Materialize",
				"->  Seq Scan on ej1",
				"Filter: (x < 10)",
				"->  Seq Scan on ej3",
			},
		},
		{
			"select * from ej1, ej2, ej3 where ej2.b = ej1.a and ej3.c = ej2.b and ej1.x < 10 and ej3.z < 20",
			[]string{
				"Nested Loop",
				"Join Filter: (ej1.a = ej2.b)",
				"->  Nested Loop",
				"Join Filter: (ej1.a = ej3.c)",
				"->  Seq Scan on ej3",
				"Filter: (z < 20)",
				"->  Materialize",
				"->  Seq Scan on ej1",
				"Filter: (x < 10)",
				"->  Seq Scan on ej2",
			},
		},
	}
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE ej1 (a int, x int)", "CREATE TABLE ej2 (b int, y int)", "CREATE TABLE ej3 (c int, z int)",
		"INSERT INTO ej1 SELECT g, g FROM generate_series(1, 1000) g",
		"INSERT INTO ej2 SELECT g, g FROM generate_series(1, 1000) g",
		"INSERT INTO ej3 SELECT g, g FROM generate_series(1, 1000) g",
		"ANALYZE ej1", "ANALYZE ej2", "ANALYZE ej3",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableHashJoin = false
	ps.EnableMergeJoin = false
	for _, c := range cases {
		var lines []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+c.query, ps)) {
			if len(r) > 0 && r[0].Kind == KindString {
				lines = append(lines, strings.TrimSpace(r[0].StringValue()))
			}
		}
		if strings.Join(lines, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s:\ngot\n%s\nwant PG's\n%s", c.query, strings.Join(lines, "\n"), strings.Join(c.want, "\n"))
		}
	}
}
