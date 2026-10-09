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

// TestRestrictionQualECOrder pins M0146-0042 against PG 18.3: a relation's
// EC-derived equalities come out in EquivalenceClass creation order
// (generate_base_implied_equalities walks root->eq_classes), not in the
// order written for that relation. `o2.a = 1999` joins the EC opened by
// `o1.a = 1999` before `o2.b = 2`'s, and a constant matches an equal
// constant, so `o1.b = 2` joins the EC of an earlier `o2.b = 2` (TPC-DS Q31's
// `(d_year = 1999) AND (d_qoy = 2)`). Each want plan is PG's output.
func TestRestrictionQualECOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ec_o1 (a int, b int, c text, d int)")
	runSQL(t, ctx, "CREATE TABLE ec_o2 (a int, b int, c text, d int)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ where, want string }{
		{"o1.b = 1 AND o1.a = 1999 AND o1.c = o2.c AND o2.b = 2 AND o2.a = 1999",
			"Nested Loop\nJoin Filter: (o1.c = o2.c)\n->  Seq Scan on ec_o1 o1\nFilter: ((b = 1) AND (a = 1999))\n" +
				"->  Seq Scan on ec_o2 o2\nFilter: ((a = 1999) AND (b = 2))"},
		{"o1.c = o2.c AND o2.b = 2 AND o1.a = 7 AND o2.a = 7 AND o1.b = 2",
			"Nested Loop\nJoin Filter: (o1.c = o2.c)\n->  Seq Scan on ec_o1 o1\nFilter: ((b = 2) AND (a = 7))\n" +
				"->  Seq Scan on ec_o2 o2\nFilter: ((b = 2) AND (a = 7))"},
		// regress join.sql's "Don't remove SJ" shape: `3 = o2.d` joins the EC
		// of `o1.d = 3`, which is regenerated as `member = const`; the
		// two-member EC of `2 = o1.a` hands back the written clause.
		{"o1.c = o2.c AND 2 = o1.a AND o1.d = 3 AND o2.a = 1 AND 3 = o2.d",
			"Nested Loop\nJoin Filter: (o1.c = o2.c)\n->  Seq Scan on ec_o1 o1\nFilter: ((2 = a) AND (d = 3))\n" +
				"->  Seq Scan on ec_o2 o2\nFilter: ((d = 3) AND (a = 1))"},
	} {
		q := "SELECT * FROM ec_o1 o1, ec_o2 o2 WHERE " + c.where
		var lines []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
			if len(r) > 0 && r[0].Kind == KindString {
				lines = append(lines, strings.TrimSpace(r[0].StringValue()))
			}
		}
		if got := strings.Join(lines, "\n"); got != c.want {
			t.Errorf("%s\ngot:\n%s\nwant PG's:\n%s", q, got, c.want)
		}
	}
}
