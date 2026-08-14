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

// TestExplainGroupingSetsSuffixUntouched pins that the grouping-sets branch is
// byte-identical to before: the `(N keys, N grouping sets)` suffix remains and
// no `Group Key:` detail line appears (the per-set key lines are a separate
// M0125-0048 shape, out of S5 scope).
func TestExplainGroupingSetsSuffixUntouched(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (a int, b int)"); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT a, count(*) FROM t GROUP BY GROUPING SETS ((a), ())"), "\n")
	if !strings.Contains(joined, "HashAggregate (1 keys, 2 grouping sets)") {
		t.Errorf("grouping-sets suffix must be preserved; got:\n%s", joined)
	}
	if strings.Contains(joined, "Group Key:") {
		t.Errorf("grouping-sets aggregate must not emit a Group Key line; got:\n%s", joined)
	}
}
