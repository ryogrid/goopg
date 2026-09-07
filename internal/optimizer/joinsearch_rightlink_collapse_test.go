package optimizer

import "testing"

// C-04b mirror of TestLeftLinkSurvivesCollapseSplit (the C-04a Q72 pin): a
// RIGHT link must survive the join_collapse_limit sub-problem split — as the
// LEFT join it reduces to, with the RIGHT input alone on the preserved side —
// and its nullable-side WHERE must stay delayed across the split. Both go
// through the per-problem SJI remap (`sjInfosInItemSpace`): with the first
// eight leaves folded into ONE item, the reduced SJI's MinRighthand names leaf
// bits no item-space joinrel contains, and without the remap the link comes
// back INNER.
func TestRightLinkSurvivesCollapseSplit(t *testing.T) {
	withPGShapedDP(t)
	for _, n := range []int{4, 8, 9, 10, 11} {
		names := make([]string, n)
		rows := make([]int64, n)
		for i := 0; i < n; i++ {
			names[i] = string(rune('a' + i))
			rows[i] = int64(1000 * (i + 1))
		}
		from := names[0]
		for i := 1; i < n; i++ {
			kw := "JOIN"
			if i == n-1 {
				kw = "RIGHT JOIN"
			}
			from += " " + kw + " " + names[i] + " ON " + names[i-1] + ".x = " + names[i] + ".x"
		}
		node, ctx := seamChainFromSQL(t, names, rows, from)
		pred := seamLocal(names, 0) // on the nullable prefix
		out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
		if !used {
			t.Errorf("n=%d: seam declined the RIGHT-topped chain", n)
			continue
		}
		var outer *Join
		nother := 0
		for _, j := range rfjJoins(out) {
			switch j.Type {
			// C-06s: the winner may now come back spelled RIGHT (the commuted
			// direction PG calls JOIN_RIGHT). Both spellings preserve the
			// unmatched rows; INNER is what would drop them.
			case JoinTypeLeft, JoinTypeRight:
				if outer != nil {
					t.Errorf("n=%d: two outer joins for one RIGHT link", n)
				}
				outer = j
			case JoinTypeInner, JoinTypeCross:
				nother++
			default:
				t.Errorf("n=%d: searched tree contains a %v join", n, j.Type)
			}
		}
		t.Logf("n=%d: used=%v residual=%v joins: outer=%v other=%d", n, used, residual != nil, outer != nil, nother)
		if outer == nil {
			t.Errorf("n=%d: RIGHT link lost through the collapse split (planned as INNER)", n)
			continue
		}
		// The preserved hand is Left for a LEFT spelling and Right for a RIGHT
		// one, so read the sides by jointype rather than by position — a
		// commuted winner has its children swapped and would otherwise look
		// like a leaf-count regression.
		preserved, nullExtended := outer.Left, outer.Right
		if outer.Type == JoinTypeRight {
			preserved, nullExtended = outer.Right, outer.Left
		}
		if nl, nr := rfjLeafCount(preserved), rfjLeafCount(nullExtended); nl != 1 || nr != n-1 {
			t.Errorf("n=%d: %v join preserves %d leaves and null-extends %d, want 1 and %d",
				n, outer.Type, nl, nr, n-1)
		}
		if residual != pred {
			t.Errorf("n=%d: nullable-side WHERE was not delayed above the tree (residual=%v)", n, residual)
		}
		if k := len(seamLeafLocalFilters(out)); k != 0 {
			t.Errorf("n=%d: %d leaf-local filters under a RIGHT link's nullable prefix, want 0", n, k)
		}
		rfjAssertBindingOrder(t, out, names)
	}
}
