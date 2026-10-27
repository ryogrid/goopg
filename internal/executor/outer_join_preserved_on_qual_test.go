package executor

import (
	"strings"
	"testing"
)

// TestOuterJoinPreservedSideOnQual pins M0147-0053: an outer join's ON
// conjunct that tests only the PRESERVED side is a join clause of that outer
// join (PG's distribute_qual_to_rels: not is_pushed_down, required_relids =
// the join's scope). It decides which preserved rows MATCH; it must never
// filter preserved rows out (a scan restriction would) nor drop null-extended
// rows (a Filter above the join would).
//
// Fixture: 100 outer rows (id = k = 0..99); inner keys 0..49 match, plus
// 100,000 non-matching filler. With `o.id > 10` in the ON clause, ids 11..49
// match (39 rows) and every one of the 100 outer rows survives.
func TestOuterJoinPreservedSideOnQual(t *testing.T) {
	ctx, cleanup := nliJointypeFixture(t)
	defer cleanup()

	for _, tc := range []struct {
		name, sql, countCol string
	}{
		{"left", "SELECT count(*), count(i.v) FROM nlij_outer o LEFT JOIN nlij_inner i ON i.k = o.k AND o.id > 10", "i.v"},
		{"right", "SELECT count(*), count(i.v) FROM nlij_inner i RIGHT JOIN nlij_outer o ON i.k = o.k AND o.id > 10", "i.v"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := runSQLRows(t, ctx, tc.sql)
			if got := strings.Join(rows, ";"); got != "100|39" {
				t.Fatalf("%s: got %q, want 100|39 (every preserved row kept, 39 matched)", tc.name, got)
			}
			plan := explainText(t, ctx, tc.sql)
			if !strings.Contains(plan, "Join Filter: (o.id > 10)") {
				t.Fatalf("%s: the preserved-side ON conjunct is not the outer join's Join Filter:\n%s", tc.name, plan)
			}
		})
	}
}

// runSQLRows renders a query's rows as "c1|c2" strings.
func runSQLRows(t *testing.T, ctx *Context, sql string) []string {
	t.Helper()
	rows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return renderRows(rows)
}
