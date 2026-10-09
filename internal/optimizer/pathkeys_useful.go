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
	// relids and classMembers answer the question PG's canonical pathkeys
	// answer by construction: two expressions of this rel that are members of
	// one equivalence class order the rel's rows identically, because the
	// class's equality is enforced inside the rel (M0146-0005dv).
	relids       RelSet
	classMembers []classMember
}

// classMember is one expression of an equivalence class, with the relations
// it reads.
type classMember struct {
	expr   Expr
	relids RelSet
	id     int
}

// pathkeyUsefulnessFor collects the merge-useful expressions for joinrelids
// from the search's clause list.
func (s *searchCtx) pathkeyUsefulnessFor(joinrelids RelSet) *pathkeyUsefulness {
	if s == nil {
		return nil
	}
	u := &pathkeyUsefulness{queryPathkeys: s.queryPathkeys, relids: joinrelids}
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
		} else {
			if relsSubset(ri.leftRelids, joinrelids) && relsSubset(ri.rightRelids, joinrelids) {
				u.classMembers = append(u.classMembers,
					classMember{ri.leftKey, ri.leftRelids, id}, classMember{ri.rightKey, ri.rightRelids, id})
			}
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
	// pathkeys_useful_for_ordering compares canonical (equivalence-class)
	// pathkeys: an ordering on cs_item_sk serves a query ordered on
	// i_item_sk once the rel enforces cs_item_sk = i_item_sk (TPC-DS Q64's
	// cross_sales, M0146-0134).
	if _, nOrd := u.countContainedIn(keys, u.queryPathkeys); nOrd > n {
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

// equivalentWithin reports whether a and b belong to one equivalence class
// whose equality this rel enforces — a clause of the class joins a and b's
// relations inside the rel — so an ordering by a is an ordering by b. PG's
// pathkeys point at the class itself (make_canonical_pathkey), so
// pathkeys_contained_in sees this without asking; goopg's pathkeys carry an
// expression and must ask. A nil usefulness (a rel built outside the search)
// knows no classes.
func (u *pathkeyUsefulness) equivalentWithin(a, b Expr) bool {
	if u == nil || len(u.classMembers) == 0 {
		return false
	}
	ida, idb := -1, -1
	for _, m := range u.classMembers {
		if ida < 0 && exprEqual(m.expr, a) {
			ida = m.id
		}
		if idb < 0 && exprEqual(m.expr, b) {
			idb = m.id
		}
	}
	return ida >= 0 && ida == idb
}

// countContainedIn is pathkeys_count_contained_in over this rel's classes:
// the leading keys of required that keys satisfy, each by an equal key or by
// one on another member of an equivalence class the rel enforces
// (equivalentWithin). A nil usefulness compares syntactically.
func (u *pathkeyUsefulness) countContainedIn(keys, required []PathKey) (contained bool, nCommon int) {
	n := 0
	for n < len(required) && n < len(keys) {
		k, r := keys[n], required[n]
		if !pathKeyEqual(k, r) && (k.SortAsc != r.SortAsc || k.NullsFirst != r.NullsFirst ||
			k.GroupingNulled != r.GroupingNulled || !u.equivalentWithin(k.Expr, r.Expr)) {
			break
		}
		n++
	}
	return n == len(required), n
}

// pathkeysContainedInRel is pathkeys_contained_in for a path of rel: a key is
// satisfied by an equal key or by one on another member of the same
// equivalence class the rel enforces (M0146-0005dv). Without the class arm a
// merge join's inner that is a join ordered by the class's other member —
// TPC-DS Q47's `vl ⋈ v0` ordered by `vl.k` where the merge wants `v0.k` — was
// priced with an explicit Sort instead of PG's Material.
func pathkeysContainedInRel(rel *RelOptInfo, keys, required []PathKey) bool {
	if len(required) > len(keys) {
		return false
	}
	var u *pathkeyUsefulness
	if rel != nil {
		u = rel.usefulKeys
	}
	for i := range required {
		k, r := keys[i], required[i]
		if pathKeyEqual(k, r) {
			continue
		}
		if k.SortAsc != r.SortAsc || k.NullsFirst != r.NullsFirst || !u.equivalentWithin(k.Expr, r.Expr) {
			return false
		}
	}
	return true
}
