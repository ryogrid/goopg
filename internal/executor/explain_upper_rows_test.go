package executor

import (
	"regexp"
	"strings"
	"testing"
)

// TestUpperNodeReadsSearchedJoinRows pins M0146-0009k against PG 18.3: a
// node above a join the path search produced is sized from the join's own
// row count — PG's create_sort_path costs the sort over subpath->rows — not
// re-estimated by the pre-search join estimator. goopg's Sort read
// estimateJoin while the Hash Join line printed the searched size (TPC-DS
// Q59: Sort 4811 rows over a 15-row join).
func TestUpperNodeReadsSearchedJoinRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE u1 (a int, p int)")
	runSQL(t, ctx, "CREATE TABLE u2 (b int, q int)")
	runSQL(t, ctx, "CREATE TABLE u3 (c int, r int)")
	runSQL(t, ctx, "INSERT INTO u1 SELECT g, g % 7 FROM generate_series(1,3000) g")
	runSQL(t, ctx, "INSERT INTO u2 SELECT g, g % 11 FROM generate_series(1,2000) g")
	runSQL(t, ctx, "INSERT INTO u3 SELECT g, g % 13 FROM generate_series(1,1000) g")
	runSQL(t, ctx, "ANALYZE u1")
	runSQL(t, ctx, "ANALYZE u2")
	runSQL(t, ctx, "ANALYZE u3")

	rowsRe := regexp.MustCompile(`^\s*(?:->\s+)?(\S.*?)\s+\(cost=\S+ rows=(\d+) `)
	lines := renderRows(runSQL(t, ctx,
		"EXPLAIN SELECT * FROM u1, u2, u3 WHERE u1.a = u2.b AND u2.q = u3.r AND u1.p = 3 ORDER BY u1.a"))
	var sortRows, joinRows string
	for i, l := range lines {
		m := rowsRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if strings.HasPrefix(m[1], "Sort") && sortRows == "" {
			sortRows = m[2]
			// The node directly under the Sort.
			for _, l2 := range lines[i+1:] {
				if m2 := rowsRe.FindStringSubmatch(l2); m2 != nil {
					if strings.Contains(m2[1], "Join") || strings.Contains(m2[1], "Nested Loop") {
						joinRows = m2[2]
					}
					break
				}
			}
		}
	}
	if sortRows == "" || joinRows == "" {
		t.Fatalf("expected a Sort directly over a join:\n%s", strings.Join(lines, "\n"))
	}
	if sortRows != joinRows {
		t.Errorf("Sort rows=%s, join rows=%s; the Sort must read the searched join's size:\n%s",
			sortRows, joinRows, strings.Join(lines, "\n"))
	}
}
