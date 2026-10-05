package optimizer

import "sort"

// M0146-0020b — PG's rollups: preprocess_grouping_sets (planner.c) splits the
// grouping sets into chains of sets, each contained in the next
// (extract_rollup_sets, a minimum chain cover found by bipartite matching),
// and orders each chain's columns so that every set is a prefix of its
// largest one (reorder_grouping_sets). A chain is one rollup: AGG_SORTED
// computes it in one pass over input sorted on that order, and each further
// rollup re-sorts the input for its own pass (create_groupingsets_plan's
// chain). The hashed strategies burst the rollups back into single sets, in
// this same order.

// GroupingRollup is one RollupData: Order is its groupClause (GroupExprs slots
// in sort order, the largest set's columns), and Sets lists the indexes into
// Aggregate.GroupingSets it computes, largest set first. Set Sets[k] groups by
// Order[:len(GroupingSets[Sets[k]])].
type GroupingRollup struct {
	Order []int
	Sets  []int
}

// ExtractGroupingRollups is preprocess_grouping_sets' rollup construction over
// the sortable sets (goopg tracks no unsortable grouping column type).
// sortOrder is the ORDER BY as GroupExprs slots, cut at the first item that is
// not a grouping column; it steers the column order only when the sets form a
// single rollup, as PG passes parse->sortClause only then.
//
// The sets are taken shortest first, as expand_grouping_sets leaves them
// (cmp_list_len_asc; a stable sort here, where PG's list_sort is stable only
// below seven sets).
func ExtractGroupingRollups(sets [][]int, sortOrder []int) []GroupingRollup {
	if len(sets) == 0 {
		return nil
	}
	byLen := make([]int, len(sets))
	for i := range byLen {
		byLen[i] = i
	}
	sort.SliceStable(byLen, func(a, b int) bool { return len(sets[byLen[a]]) < len(sets[byLen[b]]) })
	chains := extractRollupSets(sets, byLen)
	var sortRef []int
	if len(chains) == 1 {
		sortRef = sortOrder
	}
	out := make([]GroupingRollup, 0, len(chains))
	for _, chain := range chains {
		out = append(out, reorderGroupingSets(sets, chain, sortRef))
	}
	return out
}

// extractRollupSets is extract_rollup_sets: the set indexes (shortest first)
// partitioned into chains, each chain listed shortest first, every empty set
// at the head of the first chain. Duplicate sets stay with their first copy.
func extractRollupSets(sets [][]int, byLen []int) [][]int {
	numEmpty := 0
	for numEmpty < len(byLen) && len(sets[byLen[numEmpty]]) == 0 {
		numEmpty++
	}
	if numEmpty == len(byLen) {
		return [][]int{append([]int(nil), byLen...)}
	}
	// Index from 1, leaving 0 for the matching's NIL node.
	n := len(byLen) - numEmpty
	origSets := make([][]int, n+1)
	masks := make([]map[int]bool, n+1)
	adjacency := make([][]int, n+1)
	jSize, j, i := 0, 0, 1
	for _, si := range byLen[numEmpty:] {
		cand := map[int]bool{}
		for _, c := range sets[si] {
			cand[c] = true
		}
		dupOf := 0
		if jSize == len(sets[si]) {
			for k := j; k < i; k++ {
				if sameSlotSet(masks[k], cand) {
					dupOf = k
					break
				}
			}
		} else if jSize < len(sets[si]) {
			jSize = len(sets[si])
			j = i
		}
		if dupOf > 0 {
			origSets[dupOf] = append(origSets[dupOf], si)
			continue
		}
		origSets[i] = []int{si}
		masks[i] = cand
		// adjacency[i] = [n, v1, ..., vn]: the smaller sets this one
		// contains, scanned from j-1 down (no need to compare equal sizes).
		adj := []int{0}
		for k := j - 1; k > 0; k-- {
			if slotSubset(masks[k], cand) {
				adj = append(adj, k)
			}
		}
		if len(adj) > 1 {
			adj[0] = len(adj) - 1
			adjacency[i] = adj
		}
		i++
	}
	numSets := i - 1
	pairUV, pairVU := bipartiteMatch(numSets, adjacency)

	chainOf := make([]int, numSets+1)
	numChains := 0
	for i := 1; i <= numSets; i++ {
		u, v := pairVU[i], pairUV[i]
		switch {
		case u > 0 && u < i:
			chainOf[i] = chainOf[u]
		case v > 0 && v < i:
			chainOf[i] = chainOf[v]
		default:
			numChains++
			chainOf[i] = numChains
		}
	}
	results := make([][]int, numChains+1)
	for i := 1; i <= numSets; i++ {
		results[chainOf[i]] = append(results[chainOf[i]], origSets[i]...)
	}
	results[1] = append(append([]int(nil), byLen[:numEmpty]...), results[1]...)
	return results[1:]
}

func sameSlotSet(a, b map[int]bool) bool {
	return len(a) == len(b) && slotSubset(a, b)
}

func slotSubset(a, b map[int]bool) bool {
	for c := range a {
		if !b[c] {
			return false
		}
	}
	return true
}

// bipartiteMatch is BipartiteMatch (lib/bipartite_match.c), Hopcroft-Karp over
// U = V = 1..size: it returns pair_uv and pair_vu. The search order — the
// adjacency lists walked from their last entry down — is upstream's, so the
// matching, and with it the chains, are the ones PG finds.
func bipartiteMatch(size int, adjacency [][]int) (pairUV, pairVU []int) {
	const inf = 1 << 30
	pairUV = make([]int, size+1)
	pairVU = make([]int, size+1)
	distance := make([]int, size+1)
	adjOf := func(u int) []int { return adjacency[u] }
	bfs := func() bool {
		queue := make([]int, 0, size+1)
		distance[0] = inf
		for u := 1; u <= size; u++ {
			if pairUV[u] == 0 {
				distance[u] = 0
				queue = append(queue, u)
			} else {
				distance[u] = inf
			}
		}
		for qt := 0; qt < len(queue); qt++ {
			u := queue[qt]
			if distance[u] < distance[0] {
				adj := adjOf(u)
				n := 0
				if adj != nil {
					n = adj[0]
				}
				for i := n; i > 0; i-- {
					next := pairVU[adj[i]]
					if distance[next] == inf {
						distance[next] = 1 + distance[u]
						queue = append(queue, next)
					}
				}
			}
		}
		return distance[0] != inf
	}
	var dfs func(u int) bool
	dfs = func(u int) bool {
		if u == 0 {
			return true
		}
		if distance[u] == inf {
			return false
		}
		next := distance[u] + 1
		adj := adjOf(u)
		n := 0
		if adj != nil {
			n = adj[0]
		}
		for i := n; i > 0; i-- {
			v := adj[i]
			if distance[pairVU[v]] == next && dfs(pairVU[v]) {
				pairVU[v] = u
				pairUV[u] = v
				return true
			}
		}
		distance[u] = inf
		return false
	}
	for bfs() {
		for u := 1; u <= size; u++ {
			if pairUV[u] == 0 {
				dfs(u)
			}
		}
	}
	return pairUV, pairVU
}

// reorderGroupingSets is reorder_grouping_sets over one chain (set indexes,
// shortest first): each set's new columns are appended to the running order,
// following sortRef while it keeps naming them and giving it up at the first
// divergence. The result lists the sets largest first.
func reorderGroupingSets(sets [][]int, chain []int, sortRef []int) GroupingRollup {
	var previous []int
	inPrev := map[int]bool{}
	r := GroupingRollup{Sets: make([]int, 0, len(chain))}
	for _, si := range chain {
		var newElems []int
		for _, c := range sets[si] {
			if !inPrev[c] {
				newElems = append(newElems, c)
			}
		}
		for len(sortRef) > len(previous) && len(newElems) > 0 {
			ref := sortRef[len(previous)]
			at := -1
			for k, c := range newElems {
				if c == ref {
					at = k
					break
				}
			}
			if at < 0 {
				sortRef = nil
				break
			}
			previous = append(previous, ref)
			inPrev[ref] = true
			newElems = append(newElems[:at:at], newElems[at+1:]...)
		}
		for _, c := range newElems {
			previous = append(previous, c)
			inPrev[c] = true
		}
		r.Sets = append([]int{si}, r.Sets...)
	}
	r.Order = previous
	return r
}
