package executor

import (
	"strings"
	"testing"
)

// TestExplainUnionAllOfSortedGroupsMergeAppends pins M0146-0005au against
// PG 18.3 (enable_hashagg = off, serial): grouping a UNION ALL whose members
// each emit rows ordered on the group key merges the members —
// generate_orderedappend_paths' Merge Append, taken as presorted input by
// add_paths_to_grouping_rel — instead of sorting the whole Append again.
// Witnessed byte-identical on the TPC-DS reference over `item`
// (analysis/m0146/m0146-0005/slice47/).
func TestExplainUnionAllOfSortedGroupsMergeAppends(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE oa (g int, x int)"); err != nil {
		t.Fatal(err)
	}
	defer hashAggSeed(false)()

	rows := runExplainRows(t, ctx, `EXPLAIN (COSTS OFF)
		SELECT g, sum(s) FROM (SELECT g, sum(x) AS s FROM oa GROUP BY g
		                       UNION ALL
		                       SELECT g, sum(x) FROM oa GROUP BY g) u
		GROUP BY g`)
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, strings.TrimSpace(r))
	}
	joined := strings.Join(got, "\n")
	if len(got) < 4 || got[0] != "GroupAggregate" || got[2] != "->  Merge Append" {
		t.Fatalf("want GroupAggregate directly over a Merge Append:\n%s", joined)
	}
	// Each member keeps its own sorted GroupAggregate; nothing re-sorts the
	// merged stream.
	if n := countLinesContaining(got, "->  GroupAggregate"); n != 2 {
		t.Errorf("want 2 member GroupAggregates, got %d:\n%s", n, joined)
	}
	if n := countLinesContaining(got, "->  Sort"); n != 2 {
		t.Errorf("want only the members' 2 Sorts, got %d:\n%s", n, joined)
	}
}
