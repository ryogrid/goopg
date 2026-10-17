package executor

import (
	"strings"
	"testing"
)

// TestCorrelatedSublinkRestrictsOffsetBinding pins M0146-0130: a correlated
// scalar sublink whose Vars all name ONE relation is that relation's base
// restriction wherever the relation sits in FROM (PG's
// distribute_qual_to_rels over pull_varnos). M0146-0005bu placed it only on
// the first FROM item; on any other the qual stayed above the join — TPC-DS
// Q6's `item i`, the fifth item, ended as an uncosted Filter on a Gather.
// The localized sublink keeps its inner plan's FROM-cumulative references
// and runs against the leaf row padded back to them (OuterRowPad, in both
// the stack path and PARAM_EXEC lowering), so both FROM orders must agree
// on the count.
func TestCorrelatedSublinkRestrictsOffsetBinding(t *testing.T) {
	ctx, ps := correlatedLeafFixture(t)
	const sub = "ca.v > (SELECT avg(c2.v) FROM ca c2 WHERE c2.s = ca.s)"
	first := "SELECT count(*) FROM ca, cb WHERE ca.k = cb.k AND " + sub
	second := "SELECT count(*) FROM cb, ca WHERE ca.k = cb.k AND " + sub
	var counts []string
	for _, q := range []string{first, second} {
		var lines []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
			if len(r) > 0 && r[0].Kind == KindString {
				lines = append(lines, strings.TrimSpace(r[0].StringValue()))
			}
		}
		placed := false
		for i := 0; i+1 < len(lines); i++ {
			if strings.HasSuffix(lines[i], "on ca") && strings.HasPrefix(lines[i+1], "Filter:") &&
				strings.Contains(lines[i+1], "SubPlan") {
				placed = true
			}
		}
		if !placed {
			t.Fatalf("%s\nwant the SubPlan qual on the ca scan:\n%s", q, strings.Join(lines, "\n"))
		}
		rows := formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
		if len(rows) != 1 {
			t.Fatalf("%s: rows = %v", q, rows)
		}
		counts = append(counts, rows[0])
	}
	if counts[0] != counts[1] || counts[0] == "0" {
		t.Fatalf("counts %v: the offset binding must agree with the first-binding placement", counts)
	}
	// A nullable-side relation keeps the qual ABOVE its outer join: the
	// WHERE rejects the NULL-extended rows, so the count is the inner
	// join's. Sunk below the join, the NULL-extended rows would survive.
	inner := "SELECT count(*) FROM cb, ca WHERE ca.k = cb.k AND ca.k < 1000 AND " + sub
	outer := "SELECT count(*) FROM cb LEFT JOIN ca ON ca.k = cb.k AND ca.k < 1000 WHERE " + sub
	want := formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, inner, ps)))
	got := formatRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, outer, ps)))
	if len(want) != 1 || len(got) != 1 || want[0] != got[0] {
		t.Fatalf("LEFT JOIN with a nullable-side sublink qual: count %v, want the inner join's %v", got, want)
	}
}
