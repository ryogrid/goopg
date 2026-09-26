package executor

import (
	"strings"
	"testing"
)

// TestExplainGroupWithoutAggregatesIsGroup pins M0146-0023 against PG 18.3
// (enable_hashagg = off): a sorted GROUP BY with no aggregate is PG's Group
// node (create_group_path), labelled "Group"; a sorted grouping that
// aggregates stays GroupAggregate.
func TestExplainGroupWithoutAggregatesIsGroup(t *testing.T) {
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
			name: "no aggregate",
			sql:  "SELECT g FROM gd GROUP BY g ORDER BY g DESC",
			want: []string{"Group", "Group Key: g", "->  Sort", "Sort Key: g DESC", "->  Seq Scan on gd"},
		},
		{
			name: "no aggregate, two keys",
			sql:  "SELECT g, h FROM gd GROUP BY h, g",
			want: []string{"Group", "Group Key: h, g", "->  Sort", "Sort Key: h, g", "->  Seq Scan on gd"},
		},
		{
			name: "aggregate",
			sql:  "SELECT g, count(*) FROM gd GROUP BY g",
			want: []string{"GroupAggregate", "Group Key: g", "->  Sort", "Sort Key: g", "->  Seq Scan on gd"},
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
}
