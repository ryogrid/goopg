package executor

import (
	"strings"
	"testing"
)

// TestExplainRendersEveryStackedFilter pins M0145-0008j: when the plan stacks a
// residual *Filter over a searched leaf's own *Filter (an uncorrelated EXISTS
// conjunct the pull-up declines, above the scan-local `a > 1`), EXPLAIN must
// print BOTH predicates. The collapse into one scan line used to keep only the
// outermost predicate, so `a > 1` executed but never appeared — EXPLAIN, the
// acceptance instrument for plan parity, under-reported the scan's quals.
//
// PG prints one qual list per node (`ExplainNode` → `show_upper_qual` over
// `plan->qual`, explain.c), so the collapsed line carries the conjunction in
// evaluation order: the inner (scan-local) predicate first.
func TestExplainRendersEveryStackedFilter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE sf1 (a int)",
		"CREATE TABLE sf3 (s text)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	joined := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT a FROM sf1 WHERE a > 1 AND EXISTS (SELECT 1 FROM sf3)"), "\n")
	if !strings.Contains(joined, "a > 1") {
		t.Errorf("the scan-local predicate `a > 1` is missing from EXPLAIN:\n%s", joined)
	}
	if !strings.Contains(joined, "EXISTS") {
		t.Errorf("the EXISTS predicate is missing from EXPLAIN:\n%s", joined)
	}
}
