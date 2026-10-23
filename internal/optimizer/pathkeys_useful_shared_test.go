package optimizer

import (
	"reflect"
	"testing"
)

// TestTruncateUselessPathkeysMatchesOracle pins M0122-0012 slice 78:
// truncate_useless_pathkeys' arms look their list entries' identity keys and
// classes up once per rel (groupMeta / distinctMeta / mergeMeta / queryMeta),
// each candidate key's once per key, and the class index is the search's
// (classIndex). The answer must be the one the plain definitions give —
// pathKeyEqual / exprEqual per pair, the first ecMembers member equal to an
// expression naming its class — for every candidate list, with the shared
// index and without it, and again on a second call that reads the caches.
func TestTruncateUselessPathkeysMatchesOracle(t *testing.T) {
	col := func(i int, n string) *ColumnRef { return &ColumnRef{Index: i, Name: n} }
	a1, a2 := col(1, "a"), col(1, "a") // equal, distinct pointers
	b, c, d, z := col(2, "b"), col(3, "c"), col(4, "d"), col(9, "z")
	e := col(5, "e") // a merge key the query orders DESC, in no class
	members := func() []classMember {
		return []classMember{newClassMember(a1, RelSet(1), 1), newClassMember(b, RelSet(2), 1),
			newClassMember(c, RelSet(1), 2), newClassMember(d, RelSet(2), 2)}
	}
	pk := func(e Expr, asc bool) PathKey { return PathKey{Expr: e, SortAsc: asc} }
	newU := func(shared *classIndex) *pathkeyUsefulness {
		return &pathkeyUsefulness{
			ecMembers:        members(),
			classMembers:     members()[:2], // only class 1 is enforced inside the rel
			queryPathkeys:    []PathKey{pk(c, false), pk(e, false)},
			groupPathkeys:    []PathKey{pk(d, true), pk(b, true)},
			distinctPathkeys: []PathKey{pk(a2, true)},
			mergeExprs:       []Expr{a2, c, e},
			ecIdx:            shared,
		}
	}
	candidates := [][]PathKey{
		{pk(a1, true), pk(c, true)},  // merge: a1 ~ a2; c ASC is against the query's DESC
		{pk(c, false), pk(a2, true)}, // merge on c DESC (query direction)
		{pk(b, true), pk(d, true)},   // grouping members via classes of d and b
		{pk(c, true), pk(a1, true)},  // c's class is d's: a group member
		{pk(a2, true), pk(z, true)},  // distinct member, then z in no class
		{pk(z, true)},
		{pk(a1, false)}, // direction kills every arm but merging-in-direction
		{pk(e, true)},   // only the merge arm could keep it, and ASC runs against the query
		{pk(e, false)},  // the merge arm keeps it in the query's direction
	}
	counts := map[int]bool{}
	for _, keys := range candidates {
		counts[oracleUsefulCount(newU(nil), keys)] = true
	}
	if len(counts) < 3 {
		t.Fatalf("the candidates keep only %v keys: the fixture does not exercise truncation", counts)
	}
	shared := &classIndex{}
	for name, u := range map[string]*pathkeyUsefulness{"shared": newU(shared), "per-rel": newU(nil)} {
		oracle := newU(nil)
		for pass := range 2 {
			for i, keys := range candidates {
				got := truncateUselessPathkeys(u, keys)
				want := keys[:oracleUsefulCount(oracle, keys)]
				if len(got) != len(want) {
					t.Fatalf("%s pass %d candidate %d: kept %d keys, the plain definitions keep %d", name, pass, i, len(got), len(want))
				}
			}
		}
	}
	if !shared.built || len(shared.memo) == 0 {
		t.Fatalf("the shared class index was never used: %+v", shared)
	}
}

// oracleUsefulCount is truncate_useless_pathkeys' kept-key count from the
// plain definitions: exprEqual / pathKeyEqual on every pair, and a class
// found by scanning ecMembers in order.
func oracleUsefulCount(u *pathkeyUsefulness, keys []PathKey) int {
	classOf := func(e Expr) int {
		for _, m := range u.ecMembers {
			if exprEqual(m.expr, e) {
				return m.id
			}
		}
		return -1
	}
	sameFlags := func(x, y PathKey) bool {
		return x.SortAsc == y.SortAsc && x.NullsFirst == y.NullsFirst && x.GroupingNulled == y.GroupingNulled
	}
	merging := 0
	for _, k := range keys {
		dirOK := k.SortAsc
		for _, q := range u.queryPathkeys {
			if exprEqual(q.Expr, k.Expr) {
				dirOK = q.SortAsc == k.SortAsc
				break
			}
		}
		found := false
		for _, e := range u.mergeExprs {
			if exprEqual(e, k.Expr) {
				found = true
				break
			}
		}
		if !dirOK || !found {
			break
		}
		merging++
	}
	n := merging
	ordering := 0
	for ordering < len(u.queryPathkeys) && ordering < len(keys) {
		k, r := keys[ordering], u.queryPathkeys[ordering]
		if !pathKeyEqual(k, r) && (!sameFlags(k, r) || !u.equivalentWithin(k.Expr, r.Expr)) {
			break
		}
		ordering++
	}
	n = max(n, ordering)
	for _, list := range [][]PathKey{u.groupPathkeys, u.distinctPathkeys} {
		lead := 0
		for _, k := range keys {
			member := false
			for _, l := range list {
				if pathKeyEqual(k, l) || (sameFlags(k, l) && classOf(k.Expr) >= 0 && classOf(k.Expr) == classOf(l.Expr)) {
					member = true
					break
				}
			}
			if !member {
				break
			}
			lead++
		}
		if len(list) > 0 {
			n = max(n, lead)
		}
	}
	return n
}

// TestGeqoToursShareKeyMemoAndClassIndex pins the other half of slice 78: a
// GEQO tour's evaluation context (freshEvalCtx) shares the search's identity
// key memo and class index — PG's tours share one PlannerInfo — instead of
// starting empty and re-formatting every key once per tour.
func TestGeqoToursShareKeyMemoAndClassIndex(t *testing.T) {
	s := &searchCtx{clauses: &restrictInfoList{}}
	t1, t2 := s.freshEvalCtx(), s.freshEvalCtx()
	if s.exprKeys == nil || s.ecIdx == nil {
		t.Fatal("freshEvalCtx left the search without a key memo or class index")
	}
	for i, tour := range []*searchCtx{t1, t2} {
		if reflect.ValueOf(tour.exprKeys).Pointer() != reflect.ValueOf(s.exprKeys).Pointer() {
			t.Fatalf("tour %d has its own identity-key memo", i)
		}
		if tour.ecIdx != s.ecIdx {
			t.Fatalf("tour %d has its own class index", i)
		}
		if u := tour.pathkeyUsefulnessFor(RelSet(1)); u.ecIdx != s.ecIdx {
			t.Fatalf("tour %d's rel does not read the shared class index", i)
		}
	}
	// A new clause list gets a new index.
	s2 := &searchCtx{clauses: &restrictInfoList{}, ecIdx: s.ecIdx}
	if s2.freshEvalCtx(); s2.ecIdx == s.ecIdx {
		t.Fatal("a search over another clause list reused a class index built for a different one")
	}
}
