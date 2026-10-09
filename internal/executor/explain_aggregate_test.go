package executor

import (
	"strings"
	"testing"
)

// TestExplainGroupedAggregateBareLabelAndGroupKey pins 0134-0001 P2 S5: a
// grouped aggregate renders PG's AGG_HASHED label `HashAggregate` — no goopg
// `(N keys)` suffix — with the grouping expressions on a separate
// `Group Key: a, b` detail line (explain.c show_agg_keys, 2616-2636; PG
// aggregates.out:1357-1358).
func TestExplainGroupedAggregateBareLabelAndGroupKey(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (a int, b int)"); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT a, b, count(*) FROM t GROUP BY a, b"), "\n")
	if !strings.Contains(joined, "HashAggregate") {
		t.Errorf("expected a HashAggregate node; got:\n%s", joined)
	}
	if strings.Contains(joined, "HashAggregate (") {
		t.Errorf("grouped aggregate still carries the `(N keys)` suffix; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Group Key: a, b") {
		t.Errorf("expected `Group Key: a, b` detail line; got:\n%s", joined)
	}
}

// TestExplainUngroupedAggregateNoGroupKey is the regression guard for the
// ungrouped AGG_PLAIN branch: `Aggregate` with NO Group Key line (show_agg_keys
// returns early when numCols == 0, explain.c:2621; PG aggregates.out Q4).
func TestExplainUngroupedAggregateNoGroupKey(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (a int)"); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT count(*) FROM t"), "\n")
	if !strings.Contains(joined, "Aggregate") {
		t.Errorf("expected an Aggregate node; got:\n%s", joined)
	}
	if strings.Contains(joined, "HashAggregate") {
		t.Errorf("ungrouped aggregate must not use the HashAggregate label; got:\n%s", joined)
	}
	if strings.Contains(joined, "Group Key:") {
		t.Errorf("ungrouped aggregate must not emit a Group Key line; got:\n%s", joined)
	}
}

// TestExplainGroupedAggregateHavingOrder pins PG's detail-line order for a
// grouped aggregate with a HAVING qual: Group Key FIRST, Filter (HAVING) SECOND
// (explain.c:2196-2197).
func TestExplainGroupedAggregateHavingOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (a int)"); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT a, count(*) FROM t GROUP BY a HAVING count(*) > 1"), "\n")
	gkIdx := strings.Index(joined, "Group Key: a")
	filterIdx := strings.Index(joined, "Filter:")
	if gkIdx < 0 {
		t.Fatalf("missing Group Key line; got:\n%s", joined)
	}
	if filterIdx < 0 {
		t.Fatalf("missing Filter (HAVING) line; got:\n%s", joined)
	}
	if gkIdx > filterIdx {
		t.Errorf("Group Key must precede Filter; got:\n%s", joined)
	}
}

// TestExplainGroupingSetsRendersPGKeys pins M0146-0020: a grouping-sets
// aggregate prints as PG 18 does (live capture, private PG 18.3). With an
// empty set it is a MixedAggregate — one `Hash Key:` line per non-empty set
// in rollup order, then `Group Key: ()`; without one it is a HashAggregate
// with `Hash Key:` lines only.
func TestExplainGroupingSetsRendersPGKeys(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (dept text, region text, amount int)"); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"SELECT dept, region, sum(amount) FROM t GROUP BY ROLLUP(dept, region)": {
			"MixedAggregate", "  Hash Key: dept, region", "  Hash Key: dept", "  Group Key: ()", "  ->  Seq Scan on t"},
		"SELECT dept, region, sum(amount) FROM t GROUP BY GROUPING SETS ((dept), (region))": {
			"HashAggregate", "  Hash Key: dept", "  Hash Key: region", "  ->  Seq Scan on t"},
		"SELECT dept, sum(amount) FROM t GROUP BY ROLLUP(dept)": {
			"MixedAggregate", "  Hash Key: dept", "  Group Key: ()", "  ->  Seq Scan on t"},
		"SELECT dept, region, sum(amount) FROM t GROUP BY CUBE(dept, region)": {
			"MixedAggregate", "  Hash Key: dept, region", "  Hash Key: dept", "  Hash Key: region", "  Group Key: ()", "  ->  Seq Scan on t"},
	}
	for q, want := range cases {
		got := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+q)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s:\n got:\n%s\nwant:\n%s", q, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}
