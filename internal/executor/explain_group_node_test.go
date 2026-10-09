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

// TestExplainHavingWithoutAggregateMovesToWhere pins M0146-0024 against PG
// 18.3: subquery_planner moves a HAVING conjunct with no aggregate,
// volatile function or SubPlan into WHERE, so it filters the scan; an
// aggregate or volatile conjunct stays as the grouping node's Filter.
func TestExplainHavingWithoutAggregateMovesToWhere(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE gd (g int, h int, x int)"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "plain conjunct moves, aggregate stays",
			sql:  "SELECT g, count(*) FROM gd GROUP BY g HAVING g > 1 AND count(*) > 2",
			want: []string{"HashAggregate", "Group Key: g", "Filter: (count(*) > 2)", "->  Seq Scan on gd", "Filter: (g > 1)"},
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
	// A volatile conjunct stays on the grouping node (PG prints the
	// constant as '0.5'::double precision — a separate rendering gap, so
	// only the placement is asserted here).
	vol := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT g FROM gd GROUP BY g HAVING random() > 0.5")
	if len(vol) != 4 || !strings.HasPrefix(strings.TrimSpace(vol[2]), "Filter: (random() >") ||
		strings.TrimSpace(vol[3]) != "->  Seq Scan on gd" {
		t.Errorf("volatile HAVING must stay on the grouping node; got:\n%s", strings.Join(vol, "\n"))
	}
	// An ungrouped column must still raise PG's grouping error rather than
	// silently becoming a WHERE filter.
	if _, err := runQueryWithErr(ctx, "SELECT g FROM gd GROUP BY g HAVING x > 1"); err == nil ||
		!strings.Contains(err.Error(), "must appear in the GROUP BY clause") {
		t.Errorf("ungrouped HAVING column: got err %v, want the GROUP BY error", err)
	}
	// Rows: the moved clause filters the same groups.
	for _, q := range []string{"INSERT INTO gd VALUES (1,1,1),(2,2,2),(2,3,3),(3,1,1)"} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := runQueryWithErr(ctx, "SELECT g, count(*) FROM gd GROUP BY g HAVING g > 1 ORDER BY g")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(renderRows(rows), ";"); got != "2|2;3|1" {
		t.Errorf("rows: got %q want %q", got, "2|2;3|1")
	}
}
