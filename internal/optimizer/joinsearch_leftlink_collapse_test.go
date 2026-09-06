package optimizer

import "testing"

// C-04a Q72 regression pin: a LEFT link must survive (keep its jointype
// through the search) when the inner chain exceeds join_collapse_limit
// (8) so the joinlist nests a sub-problem. Without the per-problem SJI
// remap the link came back INNER (Q72 100 -> 84 rows on the SF0.5 oracle).
func TestLeftLinkSurvivesCollapseSplit(t *testing.T) {
	withPGShapedDP(t)
	for _, n := range []int{4, 8, 9, 10, 11} {
		names := make([]string, n)
		rows := make([]int64, n)
		from := ""
		for i := 0; i < n; i++ {
			names[i] = string(rune('a' + i))
			rows[i] = int64(1000 * (i + 1))
		}
		from = names[0]
		for i := 1; i < n; i++ {
			kw := "JOIN"
			if i == n-1 {
				kw = "LEFT JOIN"
			}
			from += " " + kw + " " + names[i] + " ON " + names[i-1] + ".x = " + names[i] + ".x"
		}
		node, ctx := seamChainFromSQL(t, names, rows, from)
		out, residual, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
		if !used {
			t.Logf("n=%d: seam declined", n)
			continue
		}
		nleft, ninner := 0, 0
		for _, j := range rfjJoins(out) {
			switch j.Type {
			case JoinTypeLeft:
				nleft++
			default:
				ninner++
			}
		}
		t.Logf("n=%d: used=%v residual=%v joins: left=%d other=%d", n, used, residual, nleft, ninner)
		if nleft != 1 {
			t.Errorf("n=%d: LEFT link lost (left=%d)", n, nleft)
		}
	}
}
