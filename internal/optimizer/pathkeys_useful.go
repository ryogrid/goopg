package optimizer

// truncate_useless_pathkeys for join rels (M0146-0005n).
//
// PG's build_join_pathkeys returns truncate_useless_pathkeys(root, joinrel,
// outer_pathkeys) (pathkeys.c): a join path keeps its outer's ordering only as
// far as something above can use it — a later merge join
// (pathkeys_useful_for_merging: the pathkey's equivalence class still has a
// member outside this joinrel) or the query's ordering
// (pathkeys_useful_for_ordering: the common prefix with query_pathkeys).
// Without it every ordered outer becomes a separately retained join path, and
// add_path keeps orderings PG discards (the M0146-0005m regressions).
//
// goopg's pathkeys are syntactic, so the merge test is by expression: a
// pathkey is merge-useful when it equals an operand of an equijoin clause
// whose equivalence class (or, for a clause outside any class, the clause
// itself) has a member whose relids are not inside the joinrel. The grouping,
// distinct and set-op arms of truncate_useless_pathkeys are not ported: the
// search keeps only the chosen query_pathkeys.

// pathkeyUsefulness is what a joinrel needs to truncate the pathkeys of its
// paths, computed once when the joinrel is built.
type pathkeyUsefulness struct {
	// mergeExprs are the expressions a later merge join can use.
	mergeExprs []Expr
	// queryPathkeys is root->query_pathkeys.
	queryPathkeys []PathKey
}

// pathkeyUsefulnessFor collects the merge-useful expressions for joinrelids
// from the search's clause list.
func (s *searchCtx) pathkeyUsefulnessFor(joinrelids RelSet) *pathkeyUsefulness {
	if s == nil {
		return nil
	}
	u := &pathkeyUsefulness{queryPathkeys: s.queryPathkeys}
	if s.clauses == nil {
		return u
	}
	type member struct {
		expr   Expr
		relids RelSet
	}
	// Members per equivalence class; a clause outside any class is its own
	// two-member class, keyed after the dense class ids.
	classes := make(map[int][]member)
	next := s.clauses.nclasses
	for _, ri := range s.clauses.all {
		if ri == nil || !ri.isEquijoin || ri.leftKey == nil || ri.rightKey == nil {
			continue
		}
		id := ri.ecID
		if id == noEquivClass {
			id = next
			next++
		}
		classes[id] = append(classes[id], member{ri.leftKey, ri.leftRelids}, member{ri.rightKey, ri.rightRelids})
	}
	for _, ms := range classes {
		outside := false
		for _, m := range ms {
			if !relsSubset(m.relids, joinrelids) {
				outside = true
				break
			}
		}
		if !outside {
			continue
		}
		for _, m := range ms {
			if m.relids != 0 && relsSubset(m.relids, joinrelids) {
				u.mergeExprs = append(u.mergeExprs, m.expr)
			}
		}
	}
	return u
}

// truncateUselessPathkeys is truncate_useless_pathkeys over the merging and
// ordering arms. A nil usefulness (a rel built outside the search) keeps the
// keys whole.
func truncateUselessPathkeys(u *pathkeyUsefulness, keys []PathKey) []PathKey {
	if u == nil || len(keys) == 0 {
		return keys
	}
	n := u.usefulForMerging(keys)
	if _, nOrd := pathkeysCountContainedIn(keys, u.queryPathkeys); nOrd > n {
		n = nOrd
	}
	switch {
	case n == 0:
		return nil
	case n == len(keys):
		return keys
	}
	return keys[:n:n]
}

// usefulForMerging is pathkeys_useful_for_merging: the leading pathkeys, in
// order, that a later merge join can use, stopping at the first that cannot
// or that runs against the query's direction (right_merge_direction).
func (u *pathkeyUsefulness) usefulForMerging(keys []PathKey) int {
	useful := 0
	for _, pk := range keys {
		if !u.rightMergeDirection(pk) {
			break
		}
		matched := false
		for _, e := range u.mergeExprs {
			if exprEqual(e, pk.Expr) {
				matched = true
				break
			}
		}
		if !matched {
			break
		}
		useful++
	}
	return useful
}

// rightMergeDirection is right_merge_direction: a pathkey the query orders by
// is useful only in the query's direction; any other is useful ascending.
func (u *pathkeyUsefulness) rightMergeDirection(pk PathKey) bool {
	for _, q := range u.queryPathkeys {
		if exprEqual(q.Expr, pk.Expr) {
			return q.SortAsc == pk.SortAsc
		}
	}
	return pk.SortAsc
}
