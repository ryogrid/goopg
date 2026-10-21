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
// itself) has a member whose relids are not inside the joinrel.
//
// The grouping and distinct arms (pathkeys_useful_for_grouping /
// _for_distinct, M0146-0223) keep the leading keys that are members of
// group_pathkeys / distinct_pathkeys, in any order. Without them a join
// ordered on a group key that is not the leading one was truncated to no
// ordering, and its partial path lost to an unordered one PG rejects (TPC-DS
// Q39's inv ⋈ item merge join, ordered on i_item_sk under GROUP BY
// w_warehouse_sk, i_item_sk, d_moy). The set-op arm has nothing to read:
// goopg derives no setop_pathkeys (querypathkeys.go).

// pathkeyUsefulness is what a joinrel needs to truncate the pathkeys of its
// paths, computed once when the joinrel is built.
type pathkeyUsefulness struct {
	// mergeExprs are the expressions a later merge join can use.
	mergeExprs []Expr
	// queryPathkeys is root->query_pathkeys.
	queryPathkeys []PathKey
	// groupPathkeys / distinctPathkeys are root->group_pathkeys and
	// root->distinct_pathkeys.
	groupPathkeys    []PathKey
	distinctPathkeys []PathKey
	// relids and classMembers answer the question PG's canonical pathkeys
	// answer by construction: two expressions of this rel that are members of
	// one equivalence class order the rel's rows identically, because the
	// class's equality is enforced inside the rel (M0146-0005dv).
	relids       RelSet
	classMembers []classMember
	// ecMembers are the members of every equivalence class in the search,
	// enforced inside this rel or not. PG's canonical pathkeys name the
	// class itself, so list_member_ptr matches an ordering on any member
	// (classOf).
	ecMembers []classMember
	// keys memoises exprIdentityKey for the expressions the methods below
	// compare (pathkey expressions, merge and query keys), keyed by node
	// identity, for this rel's lifetime (M0122-0012 slice 32).
	keys map[Expr]cachedKey
}

type cachedKey struct {
	key string
	ok  bool
}

// keyOf is the identity key exprEqual would compute for e, memoised.
func (u *pathkeyUsefulness) keyOf(e Expr) (string, bool) {
	if e == nil {
		return "", false
	}
	if c, hit := u.keys[e]; hit {
		return c.key, c.ok
	}
	k, ok := exprIdentityKey(e, scopeVeto)
	if u.keys == nil {
		u.keys = make(map[Expr]cachedKey)
	}
	u.keys[e] = cachedKey{k, ok}
	return k, ok
}

// equalKeyed is exprEqual(a, b) through the memo.
func (u *pathkeyUsefulness) equalKeyed(a, b Expr) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a == b {
		return true
	}
	ka, oka := u.keyOf(a)
	kb, okb := u.keyOf(b)
	return oka && okb && ka == kb
}

// classMember is one expression of an equivalence class, with the relations
// it reads.
type classMember struct {
	expr   Expr
	relids RelSet
	id     int
	// key / keyOK are exprIdentityKey(expr, scopeVeto), computed once when
	// the member is collected (newClassMember). sameClass compares every
	// member against two expressions per call, and exprEqual formats both
	// keys on every comparison: TPC-DS Q64 spent ~36 s planning in it
	// (M0122-0012 slice 32).
	key   string
	keyOK bool
	keyed bool // key/keyOK were computed; a literal member falls back to the memo
}

func newClassMember(e Expr, relids RelSet, id int) classMember {
	m := classMember{expr: e, relids: relids, id: id, keyed: true}
	if e != nil {
		m.key, m.keyOK = exprIdentityKey(e, scopeVeto)
	}
	return m
}

// memberKey is m's identity key, precomputed or through the memo.
func (u *pathkeyUsefulness) memberKey(m *classMember) (string, bool) {
	if m.keyed {
		return m.key, m.keyOK
	}
	return u.keyOf(m.expr)
}

// exprEqualKeyed is exprEqual(a, b) given each side's precomputed identity key
// (keyOf): nil only equals nil, the same expression equals itself, and
// otherwise both keys must exist and match.
func exprEqualKeyed(a Expr, ka string, oka bool, b Expr, kb string, okb bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a == b {
		return true
	}
	return oka && okb && ka == kb
}

// pathkeyUsefulnessFor collects the merge-useful expressions for joinrelids
// from the search's clause list.
func (s *searchCtx) pathkeyUsefulnessFor(joinrelids RelSet) *pathkeyUsefulness {
	if s == nil {
		return nil
	}
	u := &pathkeyUsefulness{queryPathkeys: s.queryPathkeys, groupPathkeys: s.groupPathkeys,
		distinctPathkeys: s.distinctPathkeys, relids: joinrelids}
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
		// M0146-0207: a SubPlan key is never a mergeclause.
		if ri == nil || !ri.isEquijoin || ri.subplanKey || ri.leftKey == nil || ri.rightKey == nil {
			continue
		}
		id := ri.ecID
		if id == noEquivClass {
			id = next
			next++
		} else {
			if relsSubset(ri.leftRelids, joinrelids) && relsSubset(ri.rightRelids, joinrelids) {
				u.classMembers = append(u.classMembers,
					newClassMember(ri.leftKey, ri.leftRelids, id), newClassMember(ri.rightKey, ri.rightRelids, id))
			}
		}
		classes[id] = append(classes[id], member{ri.leftKey, ri.leftRelids}, member{ri.rightKey, ri.rightRelids})
		if ri.ecID != noEquivClass {
			u.ecMembers = append(u.ecMembers,
				newClassMember(ri.leftKey, ri.leftRelids, id), newClassMember(ri.rightKey, ri.rightRelids, id))
		}
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

// truncateUselessPathkeys is truncate_useless_pathkeys over the merging,
// ordering, grouping and distinct arms. A nil usefulness (a rel built outside
// the search) keeps the keys whole.
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
	if nGrp := u.leadingMembersOf(keys, u.groupPathkeys); nGrp > n {
		n = nGrp
	}
	if nDist := u.leadingMembersOf(keys, u.distinctPathkeys); nDist > n {
		n = nDist
	}
	switch {
	case n == 0:
		return nil
	case n == len(keys):
		return keys
	}
	return keys[:n:n]
}

// leadingMembersOf is pathkeys_useful_for_grouping and
// pathkeys_useful_for_distinct: the number of leading keys that are members
// of list, in any order. Membership is list_member_ptr on canonical pathkeys:
// the same direction, nulls order and equivalence class (classOf).
func (u *pathkeyUsefulness) leadingMembersOf(keys, list []PathKey) int {
	if len(list) == 0 {
		return 0
	}
	n := 0
	for _, k := range keys {
		member := false
		for _, l := range list {
			if pathKeyEqual(k, l) || (k.SortAsc == l.SortAsc && k.NullsFirst == l.NullsFirst &&
				k.GroupingNulled == l.GroupingNulled && u.sameClass(k.Expr, l.Expr)) {
				member = true
				break
			}
		}
		if !member {
			break
		}
		n++
	}
	return n
}

// sameClass reports whether a and b are members of one equivalence class of
// the search, wherever its equality is enforced. This is the identity of
// PG's canonical pathkeys; equivalentWithin is the narrower question of
// whether this rel's rows are ordered alike by both.
func (u *pathkeyUsefulness) sameClass(a, b Expr) bool {
	ka, oka := u.keyOf(a)
	kb, okb := u.keyOf(b)
	ida, idb := -1, -1
	for i := range u.ecMembers {
		m := &u.ecMembers[i]
		mk, mok := u.memberKey(m)
		if ida < 0 && exprEqualKeyed(m.expr, mk, mok, a, ka, oka) {
			ida = m.id
		}
		if idb < 0 && exprEqualKeyed(m.expr, mk, mok, b, kb, okb) {
			idb = m.id
		}
	}
	return ida >= 0 && ida == idb
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
			if u.equalKeyed(e, pk.Expr) {
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
		if u.equalKeyed(q.Expr, pk.Expr) {
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
	ka, oka := u.keyOf(a)
	kb, okb := u.keyOf(b)
	ida, idb := -1, -1
	for i := range u.classMembers {
		m := &u.classMembers[i]
		mk, mok := u.memberKey(m)
		if ida < 0 && exprEqualKeyed(m.expr, mk, mok, a, ka, oka) {
			ida = m.id
		}
		if idb < 0 && exprEqualKeyed(m.expr, mk, mok, b, kb, okb) {
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
