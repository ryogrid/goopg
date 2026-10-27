package optimizer

import (
	"slices"
	"unsafe"
)

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
	// ecByKey / ecByPtr index ecMembers for sameClass: the class id of the
	// FIRST member, in ecMembers order, with each identity key (keyed
	// members) or each expression node (members without a key). Built
	// lazily on first use; ecMembers is complete by then. M0122-0012 slice 42.
	ecIndexed bool
	ecByKey   map[string]int
	ecByPtr   map[Expr]int
	// ecIdx, when set, is the search's shared index (classIndex) in place of
	// the three fields above.
	ecIdx *classIndex
	// groupMeta / distinctMeta are leadingMembersOf's per-entry identity keys
	// and classes of groupPathkeys / distinctPathkeys, formed once per rel.
	groupMeta, distinctMeta []listKeyMeta
	// mergeMeta / queryMeta are the identity keys of mergeExprs and of
	// queryPathkeys' expressions, formed once per rel for usefulForMerging
	// (M0122-0012 slice 78).
	mergeMeta, queryMeta []listKeyMeta
	// truncMemo memoises truncateUselessPathkeys' kept length per key list,
	// bucketed by the list's backing array and length and verified against
	// a copy of the list on every hit (M0122-0012 C16f).
	truncMemo map[truncMemoKey]truncMemoEntry
}

type truncMemoKey struct {
	data *PathKey
	n    int
}

type truncMemoEntry struct {
	keys []PathKey
	kept int
}

// classIndex is ecClassOf's index over ecMembers, which are the members of
// every equivalence class in the search's clause list — the same list for
// every rel of the search, so the index is built once per clause list and
// shared (M0122-0012 slice 78; it was rebuilt per rel). memo caches the
// answer per expression node: an expression's class does not depend on the
// rel asking.
type classIndex struct {
	clauses *restrictInfoList
	built   bool
	byKey   map[string]int
	byPtr   map[Expr]int
	memo    map[Expr]int
}

// listKeyMeta is one entry of a pathkey list with its expression's identity
// key and class, looked up once.
type listKeyMeta struct {
	key   string
	keyOK bool
	class int
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

// pathKeyEqual is the package pathKeyEqual(a, b) with exprEqual through the
// memo. truncateUselessPathkeys asks it for every key of every candidate path
// against the query's grouping and distinct keys, and exprEqual formats both
// identity keys on every call: 70 % of TPC-DS Q64's planning
// (M0122-0012 slice 78). A nil receiver answers with the unmemoised form.
func (u *pathkeyUsefulness) pathKeyEqual(a, b PathKey) bool {
	if u == nil {
		return pathKeyEqual(a, b)
	}
	return a.SortAsc == b.SortAsc && a.NullsFirst == b.NullsFirst &&
		a.GroupingNulled == b.GroupingNulled && u.equalKeyed(a.Expr, b.Expr)
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

// newClassMember is newClassMember with the key taken from u's memo.
func (u *pathkeyUsefulness) newClassMember(e Expr, relids RelSet, id int) classMember {
	m := classMember{expr: e, relids: relids, id: id, keyed: true}
	if e != nil {
		m.key, m.keyOK = u.keyOf(e)
	}
	return m
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
	owner := s.currentUsefulMemoOwner()
	if s.usefulMemo == nil || s.usefulMemoOwner != owner {
		s.usefulMemo = make(map[RelSet]*pathkeyUsefulness)
		s.usefulMemoOwner = owner
	}
	if u, hit := s.usefulMemo[joinrelids]; hit {
		return u
	}
	u := s.buildPathkeyUsefulness(joinrelids)
	s.usefulMemo[joinrelids] = u
	return u
}

// usefulMemoOwner is the identity of every input pathkeyUsefulnessFor reads
// besides the relids: the clause list (and its length) and the backing arrays
// of the query, group and distinct pathkeys.
type usefulMemoOwner struct {
	clauses                 *restrictInfoList
	nclauses                int
	query, group, distinct  *PathKey
	nquery, ngroup, ndistin int
}

func (s *searchCtx) currentUsefulMemoOwner() usefulMemoOwner {
	o := usefulMemoOwner{clauses: s.clauses,
		query: unsafe.SliceData(s.queryPathkeys), nquery: len(s.queryPathkeys),
		group: unsafe.SliceData(s.groupPathkeys), ngroup: len(s.groupPathkeys),
		distinct: unsafe.SliceData(s.distinctPathkeys), ndistin: len(s.distinctPathkeys)}
	if s.clauses != nil {
		o.nclauses = len(s.clauses.all)
	}
	return o
}

// buildPathkeyUsefulness is pathkeyUsefulnessFor's computation. The result is
// shared by every caller asking for the same relids: its lazily formed
// indexes (ecByKey, the *Meta lists) depend on nothing else either.
func (s *searchCtx) buildPathkeyUsefulness(joinrelids RelSet) *pathkeyUsefulness {
	if s.exprKeys == nil {
		s.exprKeys = make(map[Expr]cachedKey)
	}
	// The identity-key memo is the search's, not this rel's: TPC-DS Q64
	// builds hundreds of join rels over the same clause and pathkey
	// expressions, and re-formatting their keys per rel was 26 % of its
	// planning (M0122-0012 slice 42).
	if s.ecIdx == nil || s.ecIdx.clauses != s.clauses {
		s.ecIdx = &classIndex{clauses: s.clauses}
	}
	u := &pathkeyUsefulness{queryPathkeys: s.queryPathkeys, groupPathkeys: s.groupPathkeys,
		distinctPathkeys: s.distinctPathkeys, relids: joinrelids, keys: s.exprKeys, ecIdx: s.ecIdx}
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
					u.newClassMember(ri.leftKey, ri.leftRelids, id), u.newClassMember(ri.rightKey, ri.rightRelids, id))
			}
		}
		classes[id] = append(classes[id], member{ri.leftKey, ri.leftRelids}, member{ri.rightKey, ri.rightRelids})
		if ri.ecID != noEquivClass {
			u.ecMembers = append(u.ecMembers,
				u.newClassMember(ri.leftKey, ri.leftRelids, id), u.newClassMember(ri.rightKey, ri.rightRelids, id))
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
	// The kept length is a function of u and the key list's contents. The
	// same outer ordering is offered to a joinrel with every inner path and
	// join method, and u is shared by every GEQO tour (usefulMemo), so the
	// answer repeats; PG's build_join_pathkeys is cheap because canonical
	// pathkeys compare by pointer. A hit must match the stored copy key by
	// key — PathKey compares Expr by pointer (every Expr is a pointer type) —
	// so a reused backing array can never return another list's length, and
	// the result always slices the caller's own list.
	mk := truncMemoKey{data: unsafe.SliceData(keys), n: len(keys)}
	if e, hit := u.truncMemo[mk]; hit && slices.Equal(e.keys, keys) {
		return keepPathkeys(keys, e.kept)
	}
	n := u.truncateUselessLen(keys)
	if u.truncMemo == nil {
		u.truncMemo = make(map[truncMemoKey]truncMemoEntry)
	}
	u.truncMemo[mk] = truncMemoEntry{keys: slices.Clone(keys), kept: n}
	return keepPathkeys(keys, n)
}

// keepPathkeys is keys' leading n, capacity-capped so an append cannot write
// into the caller's list; nil for none.
func keepPathkeys(keys []PathKey, n int) []PathKey {
	switch {
	case n == 0:
		return nil
	case n == len(keys):
		return keys
	}
	return keys[:n:n]
}

// truncateUselessLen is truncate_useless_pathkeys' kept length.
func (u *pathkeyUsefulness) truncateUselessLen(keys []PathKey) int {
	n := u.usefulForMerging(keys)
	// pathkeys_useful_for_ordering compares canonical (equivalence-class)
	// pathkeys: an ordering on cs_item_sk serves a query ordered on
	// i_item_sk once the rel enforces cs_item_sk = i_item_sk (TPC-DS Q64's
	// cross_sales, M0146-0134).
	if _, nOrd := u.countContainedIn(keys, u.queryPathkeys); nOrd > n {
		n = nOrd
	}
	if nGrp := u.leadingMembersOf(keys, u.groupPathkeys, &u.groupMeta); nGrp > n {
		n = nGrp
	}
	if nDist := u.leadingMembersOf(keys, u.distinctPathkeys, &u.distinctMeta); nDist > n {
		n = nDist
	}
	return n
}

// leadingMembersOf is pathkeys_useful_for_grouping and
// pathkeys_useful_for_distinct: the number of leading keys that are members
// of list, in any order. Membership is list_member_ptr on canonical pathkeys:
// the same direction, nulls order and equivalence class (classOf).
func (u *pathkeyUsefulness) leadingMembersOf(keys, list []PathKey, meta *[]listKeyMeta) int {
	if len(list) == 0 {
		return 0
	}
	// Each list entry's identity key and class are looked up once per rel
	// (*meta), each candidate key's once per key — not once per pair, which
	// was most of TPC-DS Q64's planning after the memo (M0122-0012 slice 78).
	// The test per pair is unchanged: pathKeyEqual (exprEqual by identity
	// key) or, with the same direction and nulls order, sameClass.
	if len(*meta) != len(list) {
		m := make([]listKeyMeta, len(list))
		for j, l := range list {
			m[j].key, m[j].keyOK = u.keyOf(l.Expr)
			m[j].class = u.ecClassOf(l.Expr)
		}
		*meta = m
	}
	lm := *meta
	n := 0
	for _, k := range keys {
		kk, kok := u.keyOf(k.Expr)
		kc, kcDone := 0, false
		member := false
		for j, l := range list {
			if k.SortAsc != l.SortAsc || k.NullsFirst != l.NullsFirst || k.GroupingNulled != l.GroupingNulled {
				continue
			}
			if exprEqualKeyed(k.Expr, kk, kok, l.Expr, lm[j].key, lm[j].keyOK) {
				member = true
				break
			}
			if !kcDone {
				kc, kcDone = u.ecClassOf(k.Expr), true
			}
			if kc >= 0 && kc == lm[j].class {
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
	ida := u.ecClassOf(a)
	return ida >= 0 && ida == u.ecClassOf(b)
}

// ecClassOf is the class id of the first member of ecMembers that e equals
// (exprEqualKeyed), or -1. That first match is found by lookup instead of a
// scan of every member (M0122-0012 slice 42: the scan was 30 % of TPC-DS
// Q64's planning). An expression with an identity key equals exactly the
// members carrying the same key — a member that is the same node carries the
// same key — so the first keyed match is ecByKey[key]. An expression without
// a key equals only itself, so its first match is ecByPtr[e]. A nil e equals
// only a nil member, which ecByPtr also records.
func (u *pathkeyUsefulness) ecClassOf(e Expr) int {
	if x := u.ecIdx; x != nil {
		if id, hit := x.memo[e]; hit {
			return id
		}
		if !x.built {
			x.built = true
			x.byKey, x.byPtr = u.indexMembers()
			x.memo = make(map[Expr]int)
		}
		id := u.classIn(e, x.byKey, x.byPtr)
		x.memo[e] = id
		return id
	}
	if !u.ecIndexed {
		u.ecIndexed = true
		u.ecByKey, u.ecByPtr = u.indexMembers()
	}
	return u.classIn(e, u.ecByKey, u.ecByPtr)
}

// indexMembers builds ecClassOf's lookup maps over ecMembers.
func (u *pathkeyUsefulness) indexMembers() (byKey map[string]int, byPtr map[Expr]int) {
	byKey = make(map[string]int, len(u.ecMembers))
	byPtr = make(map[Expr]int, len(u.ecMembers))
	for i := range u.ecMembers {
		m := &u.ecMembers[i]
		if mk, mok := u.memberKey(m); mok {
			if _, seen := byKey[mk]; !seen {
				byKey[mk] = m.id
			}
		}
		if _, seen := byPtr[m.expr]; !seen {
			byPtr[m.expr] = m.id
		}
	}
	return byKey, byPtr
}

// classIn is ecClassOf's lookup of e in one index.
func (u *pathkeyUsefulness) classIn(e Expr, byKey map[string]int, byPtr map[Expr]int) int {
	if e != nil {
		if k, ok := u.keyOf(e); ok {
			if id, hit := byKey[k]; hit {
				return id
			}
			return -1
		}
	}
	if id, hit := byPtr[e]; hit {
		return id
	}
	return -1
}

// usefulForMerging is pathkeys_useful_for_merging: the leading pathkeys, in
// order, that a later merge join can use, stopping at the first that cannot
// or that runs against the query's direction (right_merge_direction).
func (u *pathkeyUsefulness) usefulForMerging(keys []PathKey) int {
	// The merge and query expressions' keys are looked up once per rel,
	// each pathkey's once per key (M0122-0012 slice 78); every comparison is
	// still exprEqual by identity key.
	u.ensureMergeMeta()
	useful := 0
	for _, pk := range keys {
		pkk, pok := u.keyOf(pk.Expr)
		if !u.rightMergeDirectionKeyed(pk, pkk, pok) {
			break
		}
		matched := false
		for j, e := range u.mergeExprs {
			if exprEqualKeyed(e, u.mergeMeta[j].key, u.mergeMeta[j].keyOK, pk.Expr, pkk, pok) {
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
	u.ensureMergeMeta()
	pkk, pok := u.keyOf(pk.Expr)
	return u.rightMergeDirectionKeyed(pk, pkk, pok)
}

// ensureMergeMeta forms mergeMeta and queryMeta once per rel.
func (u *pathkeyUsefulness) ensureMergeMeta() {
	if len(u.mergeMeta) != len(u.mergeExprs) {
		u.mergeMeta = make([]listKeyMeta, len(u.mergeExprs))
		for j, e := range u.mergeExprs {
			u.mergeMeta[j].key, u.mergeMeta[j].keyOK = u.keyOf(e)
		}
	}
	if len(u.queryMeta) != len(u.queryPathkeys) {
		u.queryMeta = make([]listKeyMeta, len(u.queryPathkeys))
		for j, q := range u.queryPathkeys {
			u.queryMeta[j].key, u.queryMeta[j].keyOK = u.keyOf(q.Expr)
		}
	}
}

// rightMergeDirectionKeyed is rightMergeDirection given pk's identity key;
// queryMeta must be formed (ensureMergeMeta).
func (u *pathkeyUsefulness) rightMergeDirectionKeyed(pk PathKey, pkk string, pok bool) bool {
	for j, q := range u.queryPathkeys {
		if exprEqualKeyed(q.Expr, u.queryMeta[j].key, u.queryMeta[j].keyOK, pk.Expr, pkk, pok) {
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
		if !u.pathKeyEqual(k, r) && (k.SortAsc != r.SortAsc || k.NullsFirst != r.NullsFirst ||
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
		if u.pathKeyEqual(k, r) {
			continue
		}
		if k.SortAsc != r.SortAsc || k.NullsFirst != r.NullsFirst || !u.equivalentWithin(k.Expr, r.Expr) {
			return false
		}
	}
	return true
}
