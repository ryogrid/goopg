package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestRestrictionQualOrder pins M0146-0005co against PG 18.3: a scan's
// restriction list puts the equivalence-class equalities
// (generate_base_implied_equalities appends them) after the other quals,
// then order_qual_clauses stable-sorts by evaluation cost. Each want line
// is PG's own output for the statement.
func TestRestrictionQualOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE oo (a int, b int, c text, d numeric)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ where, want string }{
		{"a = 8 AND b >= 30", "Filter: ((b >= 30) AND (a = 8))"},
		{"c = 's' AND a = 1999 AND d > 0", "Filter: ((d > '0'::numeric) AND (c = 's'::text) AND (a = 1999))"},
		{"a = 1 AND (b = 2 OR b = 3)", "Filter: ((a = 1) AND ((b = 2) OR (b = 3)))"},
		{"a IN (1,2,3,4) AND b = 5", "Filter: ((b = 5) AND (a = ANY ('{1,2,3,4}'::integer[])))"},
		{"upper(c) = 'X' AND b > 1", "Filter: ((b > 1) AND (upper(c) = 'X'::text))"},
		{"a = b AND d > 1", "Filter: ((d > '1'::numeric) AND (a = b))"},
	} {
		q := "SELECT * FROM oo WHERE " + c.where
		var lines []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
			if len(r) > 0 && r[0].Kind == KindString {
				lines = append(lines, strings.TrimSpace(r[0].StringValue()))
			}
		}
		found := false
		for _, l := range lines {
			if l == c.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s\nwant line %q in:\n%s", q, c.want, strings.Join(lines, "\n"))
		}
	}
}
