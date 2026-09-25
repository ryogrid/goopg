package executor

import (
	"strings"
	"testing"
)

// TestExplainGroupClauseFollowsOrderBy pins M0145-0008d against PG 18.3.
// PG's processed_groupClause gives a GROUP BY item that ORDER BY names ORDER
// BY's direction (transformGroupClauseExpr, parse_clause.c:2424-2438) and
// moves the GROUP BY items forming an ORDER BY prefix to the front
// (preprocess_groupclause, planner.c:2828). A GroupAggregate then sorts
// ONCE in that order, and its output already satisfies the ORDER BY.
//
// Each expected plan is PG 18.3's EXPLAIN (COSTS OFF) of the same statement
// under enable_hashagg = off (analysis/m0145/m0145-0008d/probe.txt). Before
// the change goopg sorted ascending in written order under the aggregate and
// then sorted again for the ORDER BY.
func TestExplainGroupClauseFollowsOrderBy(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE gd (g int, h int, x int)"); err != nil {
		t.Fatal(err)
	}
	defer hashAggSeed(false)()

	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "direction copied, one sort",
			sql:  "SELECT g, count(*) FROM gd GROUP BY g ORDER BY g DESC",
			want: []string{"GroupAggregate", "Group Key: g", "->  Sort", "Sort Key: g DESC", "->  Seq Scan on gd"},
		},
		{
			name: "ORDER BY prefix reorders the group keys",
			sql:  "SELECT g, h, count(*) FROM gd GROUP BY h, g ORDER BY g DESC, h",
			want: []string{"GroupAggregate", "Group Key: g, h", "->  Sort", "Sort Key: g DESC, h", "->  Seq Scan on gd"},
		},
		{
			name: "partial prefix still leads",
			sql:  "SELECT g, h, count(*) FROM gd GROUP BY h, g ORDER BY g DESC",
			want: []string{"GroupAggregate", "Group Key: g, h", "->  Sort", "Sort Key: g DESC, h", "->  Seq Scan on gd"},
		},
		{
			name: "explicit NULLS LAST is copied",
			sql:  "SELECT g, count(*) FROM gd GROUP BY g ORDER BY g DESC NULLS LAST",
			want: []string{"GroupAggregate", "Group Key: g", "->  Sort", "Sort Key: g DESC NULLS LAST", "->  Seq Scan on gd"},
		},
		{
			name: "non-prefix item still copies the direction",
			sql:  "SELECT g, count(*) FROM gd GROUP BY g ORDER BY count(*) DESC, g DESC",
			want: []string{"Sort", "Sort Key: (count(*)) DESC, g DESC", "->  GroupAggregate", "Group Key: g", "->  Sort", "Sort Key: g DESC", "->  Seq Scan on gd"},
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

	// A HashAggregate prints the processed group clause too: PG shows
	// `Group Key: g, h` for GROUP BY h, g ORDER BY g DESC, h.
	t.Run("hashed prints the processed order", func(t *testing.T) {
		defer hashAggSeed(true)()
		joined := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT g, h, count(*) FROM gd GROUP BY h, g ORDER BY g DESC, h"), "\n")
		if !strings.Contains(joined, "HashAggregate") || !strings.Contains(joined, "Group Key: g, h") {
			t.Errorf("expected HashAggregate with `Group Key: g, h`; got:\n%s", joined)
		}
	})
}
