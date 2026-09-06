package optimizer

// M0127-P5.3 / P5.3a tests. The subject is ENUMERATION — which pairs the search offers
// and in which order — so every test asserts on the pair sequence a recording
// builder captures, never on a cost. That separation is the point of the
// `joinRelBuilder` seam: P5.4's costing and P5.6's sizing can change freely
// without moving a single expectation here, and conversely a change to the
// enumeration cannot hide behind a cost that happened to come out the same.
//
// The four properties pinned, each a distinct branch of joinrels.c:
//
//  1. the level-2 `first_rel` offset (joinrels.c:112-116) — without it every
//     level-2 pair is considered twice;
//  2. the clauseless else-branch (joinrels.c:127-137) — a disconnected rel is
//     crossed in EAGERLY at level 2, not deferred to the last-ditch pass, and
//     without the level-2 offset, which PG applies to the clause branch only;
//  3. the last-ditch pass (joinrels.c:200-256) on PG's own documented shape —
//     every rel in the sub-problem has join clauses, but only to rels outside
//     it, so phase 1 produces nothing;
//  4. find-or-create (`build_join_rel`): one RelOptInfo per relset, sized
//     once, with paths offered in both outer/inner orders by every pair that
//     spans it;
//  5. phase 2 (joinrels.c:141-198, added at P5.3a) — the bushy pair a chain
//     only reaches at k == lev−k, the clauseless-composite skip (:170-172),
//     and, on a complete clause graph, the whole enumeration counted against
//     03 §7's closed form.

import (
	"fmt"
	"testing"
)

// recordedPair is one addPaths call: the joinrel's relset and the outer/inner
// order it was offered in.
type recordedPair struct {
	join, outer, inner RelSet
}

func (p recordedPair) String() string {
	return fmt.Sprintf("%#b={%#b}x{%#b}", p.join, p.outer, p.inner)
}

// recordingBuilder is the test's joinRelBuilder: it records every sizing and
// path-adding call and hands back a trivially costed path so that
// `setCheapest` has something to choose. Rows are the plain product, which is
// deliberately NOT a cardinality model — P5.6 owns that, and this test must
// not encode an expectation about it.
type recordingBuilder struct {
	sized []RelSet
	pairs []recordedPair
	// sjinfos[i] is the SpecialJoinInfo makeJoinRel matched for pairs[i] —
	// nil for a plain inner join (C-03b). Kept alongside rather than inside
	// recordedPair so the positional literals the enumeration tests compare
	// against stay readable: those tests assert the PAIR SEQUENCE, and the
	// sjinfo is a separate contract (the same one reaches both directions,
	// and the callee decides which orientation may perform the join).
	sjinfos []*SpecialJoinInfo
	fail    RelSet // when non-zero, addPaths errors on this joinrel
}

func (b *recordingBuilder) sizeJoinRel(outer, inner *RelOptInfo, clauses []*restrictInfo) (float64, int) {
	b.sized = append(b.sized, outer.Relids|inner.Relids)
	return outer.Rows * inner.Rows, outer.Width + inner.Width
}

func (b *recordingBuilder) addPaths(joinrel, outer, inner *RelOptInfo, clauses []*restrictInfo, sjinfo *SpecialJoinInfo) error {
	b.pairs = append(b.pairs, recordedPair{join: joinrel.Relids, outer: outer.Relids, inner: inner.Relids})
	b.sjinfos = append(b.sjinfos, sjinfo)
	if b.fail != 0 && joinrel.Relids == b.fail {
		return fmt.Errorf("test builder refuses %#b", joinrel.Relids)
	}
	addPath(joinrel, &Path{
		Kind: PathPrebuilt,
		Rel:  joinrel,
		Rows: joinrel.Rows,
		// Distinct per direction so add_path's dominance test has something
		// to work with; the outer relset is an arbitrary tie-break.
		Cost: Cost{Startup: float64(outer.Relids), Total: joinrel.Rows + float64(outer.Relids)},
	}, "test")
	return nil
}

// jslCtx builds an n-relation search context whose initial rels each hold one
// path, bypassing buildInitialRels (which needs the pre-search pipeline's
// three parallel slices). Rel i owns bit i and has 10*(i+1) rows.
func jslCtx(t *testing.T, n int) *searchCtx {
	t.Helper()
	s, err := newSearchCtx(n, defaultCostParams(), nil)
	if err != nil {
		t.Fatalf("newSearchCtx: %v", err)
	}
	for i := 0; i < n; i++ {
		rel := newRelOptInfo(RelSet(1)<<uint(i), float64(10*(i+1)), 8)
		if err := s.addRel(rel); err != nil {
			t.Fatalf("addRel %d: %v", i, err)
		}
		addPath(rel, &Path{Kind: PathPrebuilt, Rel: rel, Rows: rel.Rows, Cost: Cost{Total: rel.Rows}}, "test")
		setCheapest(rel)
	}
	return s
}

// jslClauses hand-builds a clause list from bare relsets. The enumerator reads
// nothing else off a restrictInfo, and going through buildRestrictInfos would
// make it impossible to express property (3)'s shape — a clause whose other
// operand lives OUTSIDE the sub-problem, which in the search's coordinate
// space is a clause with a single bit set.
func jslClauses(relids ...RelSet) *restrictInfoList {
	l := &restrictInfoList{}
	for _, r := range relids {
		l.all = append(l.all, &restrictInfo{relids: r, ecID: noEquivClass})
	}
	return l
}

// levelSets is the relsets at a level, in list order.
func levelSets(s *searchCtx, lev int) []RelSet {
	var out []RelSet
	for _, rel := range s.levelRels(lev) {
		out = append(out, rel.Relids)
	}
	return out
}

func wantSets(t *testing.T, got []RelSet, want []RelSet, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %#b (%d rels), want %#b (%d)", what, got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: got %#b, want %#b", what, got, want)
		}
	}
}

const (
	rA RelSet = 1 << 0
	rB RelSet = 1 << 1
	rC RelSet = 1 << 2
	rD RelSet = 1 << 3
)

// TestJoinSearchChainEnumeration runs the whole search over the 3-rel chain
// a-b-c and pins both the level contents and the exact pair sequence — which
// makes it property (1)'s test as well: without joinrels.c:112-116's level-2
// offset, old=b would re-pair with a and old=c with b, and the pair sequence
// below would carry four extra entries.
func TestJoinSearchChainEnumeration(t *testing.T) {
	s := jslCtx(t, 3)
	b := &recordingBuilder{}
	// a-b and b-c; a-c absent.
	final, err := s.joinSearch(jslClauses(rA|rB, rB|rC), b)
	if err != nil {
		t.Fatalf("joinSearch: %v", err)
	}
	if final.Relids != rA|rB|rC {
		t.Fatalf("final rel %#b, want %#b", final.Relids, rA|rB|rC)
	}

	wantSets(t, levelSets(s, 2), []RelSet{rA | rB, rB | rC}, "level 2")
	wantSets(t, levelSets(s, 3), []RelSet{rA | rB | rC}, "level 3")

	// Level 2: old=a offers (a,b) — old=b's first_rel skips a and offers
	// (b,c) — old=c has nothing left. Level 3: {a,b} pairs with c, then
	// {b,c} pairs with a, both landing on the SAME relset.
	want := []recordedPair{
		{rA | rB, rA, rB}, {rA | rB, rB, rA},
		{rB | rC, rB, rC}, {rB | rC, rC, rB},
		{rA | rB | rC, rA | rB, rC}, {rA | rB | rC, rC, rA | rB},
		{rA | rB | rC, rB | rC, rA}, {rA | rB | rC, rA, rB | rC},
	}
	wantPairs(t, b.pairs, want)

	// Find-or-create, property (4): three relsets, each sized exactly once,
	// even though {a,b,c} was reached from two different pairs.
	if len(b.sized) != 3 {
		t.Fatalf("sizeJoinRel called %d times (%#b), want 3", len(b.sized), b.sized)
	}
	for _, rel := range s.levelRels(3) {
		if rel.CheapestTotal == nil {
			t.Fatalf("rel %#b has no cheapest path", rel.Relids)
		}
		if len(rel.Pathlist) == 0 {
			t.Fatalf("rel %#b has an empty pathlist", rel.Relids)
		}
	}
}

// TestJoinSearchClauselessEagerCartesian pins property (2). c participates in
// no join clause; PG's else-branch (joinrels.c:127-137) crosses it in at level
// 2 against EVERY initial rel, so a 1-row disconnected dimension does not have
// to wait for the top of the search. Note the asymmetry the test also pins:
// the clause branch used the level-2 offset, the clauseless branch does not.
func TestJoinSearchClauselessEagerCartesian(t *testing.T) {
	s := jslCtx(t, 3)
	b := &recordingBuilder{}
	if _, err := s.joinSearch(jslClauses(rA|rB), b); err != nil {
		t.Fatalf("joinSearch: %v", err)
	}

	// old=a (clause branch, first=1): b is relevant, c is not.
	// old=b (clause branch, first=2): nothing left.
	// old=c (clauseless branch, NO offset): both a and b.
	wantSets(t, levelSets(s, 2), []RelSet{rA | rB, rA | rC, rB | rC}, "level 2")

	// Level 3 shows the other side of the same rule, and it is not obvious:
	// {a,b} ⋈ c is NOT offered, because `have_relevant_joinclause({a,b},{c})`
	// is false — c participates in no clause at all — and {a,b} is not itself
	// clauseless, so it never reaches the else-branch. PG enumerates exactly
	// this way; {a,b,c} is instead reached from {a,c} ⋈ b and {b,c} ⋈ a, both
	// of which the a=b clause makes relevant.
	want := []recordedPair{
		{rA | rB, rA, rB}, {rA | rB, rB, rA},
		{rA | rC, rC, rA}, {rA | rC, rA, rC},
		{rB | rC, rC, rB}, {rB | rC, rB, rC},
		{rA | rB | rC, rA | rC, rB}, {rA | rB | rC, rB, rA | rC},
		{rA | rB | rC, rB | rC, rA}, {rA | rB | rC, rA, rB | rC},
	}
	wantPairs(t, b.pairs, want)
}

// TestJoinSearchLastDitch pins property (3) on PG's own documented example
// (joinrels.c:206-215): `a INNER JOIN b ON TRUE, c, d WHERE a.w = c.x AND
// b.y = d.z`, with {a,b} as the sub-problem. Inside it both rels have a join
// clause — so neither takes the clauseless branch — but no clause connects
// them, so phase 1 emits nothing and only the last-ditch pass can build the
// level.
func TestJoinSearchLastDitch(t *testing.T) {
	s := jslCtx(t, 2)
	b := &recordingBuilder{}
	// Each clause has one bit inside the sub-problem; its other operand is a
	// relation the sub-problem does not contain.
	final, err := s.joinSearch(jslClauses(rA, rB), b)
	if err != nil {
		t.Fatalf("joinSearch: %v", err)
	}
	if final.Relids != rA|rB {
		t.Fatalf("final rel %#b, want %#b", final.Relids, rA|rB)
	}
	// Phase 1 offered nothing at all; every pair below came from phase 3,
	// which is the clauseless loop and therefore has no level-2 offset (a is
	// crossed with b, then b with a, onto one rel).
	want := []recordedPair{
		{rA | rB, rA, rB}, {rA | rB, rB, rA},
		{rA | rB, rB, rA}, {rA | rB, rA, rB},
	}
	wantPairs(t, b.pairs, want)
	if len(b.sized) != 1 {
		t.Fatalf("sizeJoinRel called %d times, want 1", len(b.sized))
	}
}

// TestJoinSearchNoClausesAtAll: a FROM list with no join qual anywhere. Every
// rel takes the clauseless branch at every level, and the search still
// terminates with one final rel — the nil-clause-list path the production
// caller hits for `SELECT * FROM a, b`.
func TestJoinSearchNoClausesAtAll(t *testing.T) {
	s := jslCtx(t, 3)
	b := &recordingBuilder{}
	final, err := s.joinSearch(nil, b)
	if err != nil {
		t.Fatalf("joinSearch with a nil clause list: %v", err)
	}
	if final.Relids != rA|rB|rC {
		t.Fatalf("final rel %#b, want %#b", final.Relids, rA|rB|rC)
	}
	wantSets(t, levelSets(s, 2), []RelSet{rA | rB, rA | rC, rB | rC}, "level 2")
	wantSets(t, levelSets(s, 3), []RelSet{rA | rB | rC}, "level 3")
}

// TestJoinSearchFourRelChainOffersBushyPair is the arrival test for phase 2
// (M0127-P5.3a) — it was `…IsLeftDeepOnly` at P5.3, asserting the bushy pair
// was ABSENT, and flipping it is what proves phase 2 actually runs. Over the
// chain a-b-c-d the level-4 rel is reachable three ways: {a,b,c}⋈d and
// {b,c,d}⋈a from phase 1, and ({a,b},{c,d}) from phase 2 — the last one at
// k == lev−k, so the mirror-image offset (joinrels.c:174-177) is exercised
// too: without it the same pair would be offered again from old={c,d}.
//
// The pair sequence is pinned whole, because phase 2's position in the
// function is itself load-bearing: its pairs must land after phase 1's and
// before phase 3 can test the level for emptiness.
func TestJoinSearchFourRelChainOffersBushyPair(t *testing.T) {
	s := jslCtx(t, 4)
	b := &recordingBuilder{}
	if _, err := s.joinSearch(jslClauses(rA|rB, rB|rC, rC|rD), b); err != nil {
		t.Fatalf("joinSearch: %v", err)
	}
	wantSets(t, levelSets(s, 2), []RelSet{rA | rB, rB | rC, rC | rD}, "level 2")
	wantSets(t, levelSets(s, 3), []RelSet{rA | rB | rC, rB | rC | rD}, "level 3")
	wantSets(t, levelSets(s, 4), []RelSet{rA | rB | rC | rD}, "level 4")

	ab, bc, cd := rA|rB, rB|rC, rC|rD
	abc, bcd, abcd := rA|rB|rC, rB|rC|rD, rA|rB|rC|rD
	want := []recordedPair{
		// Level 2, phase 1: the chain's three adjacent pairs.
		{ab, rA, rB}, {ab, rB, rA},
		{bc, rB, rC}, {bc, rC, rB},
		{cd, rC, rD}, {cd, rD, rC},
		// Level 3, phase 1. {a,b}⋈c and {b,c}⋈a reach the same relset; note
		// {c,d}⋈a is skipped — no clause spans {c,d} and a.
		{abc, ab, rC}, {abc, rC, ab},
		{abc, bc, rA}, {abc, rA, bc},
		{bcd, bc, rD}, {bcd, rD, bc},
		{bcd, cd, rB}, {bcd, rB, cd},
		// Level 4, phase 1 first…
		{abcd, abc, rD}, {abcd, rD, abc},
		{abcd, bcd, rA}, {abcd, rA, bcd},
		// …then phase 2's single surviving bushy pair. old={b,c} and
		// old={c,d} both overlap their only candidate, so this is the whole
		// phase.
		{abcd, ab, cd}, {abcd, cd, ab},
	}
	wantPairs(t, b.pairs, want)

	// Find-or-create still holds across the phase boundary: {a,b,c,d} was
	// reached from three different pairs, two of them in a different phase,
	// and is sized once.
	if len(b.sized) != 6 {
		t.Fatalf("sizeJoinRel called %d times (%#b), want 6", len(b.sized), b.sized)
	}
}

// TestJoinSearchBushyIsClauseOnly pins the one asymmetry between phase 1 and
// phase 2: phase 1 has a clauseless else-branch that cross-joins eagerly,
// phase 2 has NONE (joinrels.c:190-191 is the whole gate). PG's reason is
// planning time — the unfiltered bushy space is what 03 §7's closed form
// counts — and without the gate a disconnected level-2 rel would be crossed
// into every other level-2 rel at every level above.
//
// The setup is a-b connected and c, d connected to nothing, so level 2 is
// mostly cartesian: [{a,b},{a,c},{b,c},{c,d},{a,d},{b,d}]. Exactly two bushy
// pairs are clause-connected — a is in {a,c} and b is in {b,d}, so the a=b
// clause spans them, and likewise for {b,c}/{a,d}. Everything else at that
// level either overlaps or is unconnected, so the expectation is exact.
//
// Note what this test does NOT claim to cover: the clauseless-composite skip
// at :170-172 is unobservable in v1 — with `hasJoinRestriction` constant
// false, a rel with no join clause at all cannot satisfy the pair gate for any
// partner, so removing the skip changes performance and nothing else. It is
// kept verbatim because it becomes load-bearing the moment restrictions enter
// the search; see the comment at its site.
func TestJoinSearchBushyIsClauseOnly(t *testing.T) {
	s := jslCtx(t, 4)
	b := &recordingBuilder{}
	if _, err := s.joinSearch(jslClauses(rA|rB), b); err != nil {
		t.Fatalf("joinSearch: %v", err)
	}
	var bushy []recordedPair
	for _, p := range b.pairs {
		// A pair whose two sides are both composites can only be phase 2's.
		if relLevel(p.outer) >= 2 && relLevel(p.inner) >= 2 {
			bushy = append(bushy, p)
		}
	}
	want := []recordedPair{
		{rA | rB | rC | rD, rA | rC, rB | rD}, {rA | rB | rC | rD, rB | rD, rA | rC},
		{rA | rB | rC | rD, rB | rC, rA | rD}, {rA | rB | rC | rD, rA | rD, rB | rC},
	}
	wantPairs(t, bushy, want)
	for _, p := range bushy {
		if !s.clauses.hasRelevantJoinClauseRelids(p.outer, p.inner) {
			t.Fatalf("phase 2 offered an unconnected pair: %v", p)
		}
	}
}

// TestJoinSearchPairCountMatchesClosedForm is P5.3a's arithmetic check against
// 03 §7. On a COMPLETE clause graph — one clause spanning every rel, so every
// disjoint pair is relevant and no connectivity filter fires — phases 1 and 2
// together must enumerate every unordered split of every subset into two
// non-empty parts, and that count has a closed form: (3ⁿ − 2ⁿ⁺¹ + 1)/2. (Each
// element is in the left part, the right part, or neither: 3ⁿ ordered
// assignments; subtract the 2ⁿ with an empty left and the 2ⁿ with an empty
// right, add back the once-doubly-subtracted empty/empty; halve for order.)
//
// This is the test that would catch a phase-2 k-loop that stopped short, an
// off-by-one in the mirror-image offset (which would double-count the halfway
// level), or a phase-1 regression that lost the (lev−1, 1) splits — none of
// which the fixed chain sequences above can see, because a chain's
// connectivity filter masks most of the space.
//
// makeJoinRel calls, not addPaths calls, are the unit: each pair is offered in
// both directions (03 §4.4), so the recorded sequence is exactly twice as long.
func TestJoinSearchPairCountMatchesClosedForm(t *testing.T) {
	for n := 2; n <= 7; n++ {
		var all RelSet
		for i := 0; i < n; i++ {
			all |= RelSet(1) << uint(i)
		}
		s := jslCtx(t, n)
		b := &recordingBuilder{}
		// One clause referencing every rel: hasRelevantJoinClause is then
		// true for any pair, so nothing is filtered out.
		if _, err := s.joinSearch(jslClauses(all), b); err != nil {
			t.Fatalf("n=%d: joinSearch: %v", n, err)
		}
		want := (pow(3, n) - 2*pow(2, n) + 1) / 2
		if got := len(b.pairs) / 2; got != want {
			t.Errorf("n=%d: %d makeJoinRel calls, want (3^%d - 2^%d + 1)/2 = %d",
				n, got, n, n+1, want)
		}
		if len(b.pairs)%2 != 0 {
			t.Errorf("n=%d: %d addPaths calls is odd; every pair is offered in both directions",
				n, len(b.pairs))
		}
		// Every subset of size ≥ 2 is built exactly once (find-or-create).
		if got, want := len(b.sized), pow(2, n)-n-1; got != want {
			t.Errorf("n=%d: %d joinrels sized, want 2^%d - %d - 1 = %d", n, got, n, n, want)
		}
	}
}

func pow(base, exp int) int {
	out := 1
	for i := 0; i < exp; i++ {
		out *= base
	}
	return out
}

// TestMakeJoinRelRejectsOverlap: PG asserts non-overlap (joinrels.c:706);
// goopg returns it, because the search's failure mode is "fall back to the
// syntactic shape", not "abort".
// TestMakeJoinRelAddsColumnCounts pins the join arm of RelOptInfo.NCols
// (M0127-P5.7-a). A join row is its two inputs concatenated, so the count adds
// — and it must, because `hashJoinCost` solves the hash geometry for a build
// over a JOIN rel just as often as over a base rel, and a zero there would
// price a multi-gigabyte intermediate build as never spilling.
func TestMakeJoinRelAddsColumnCounts(t *testing.T) {
	s := jslCtx(t, 3)
	s.clauses = jslClauses()
	s.builder = &recordingBuilder{}
	for i, rel := range []*RelOptInfo{s.findRel(rA), s.findRel(rB), s.findRel(rC)} {
		rel.NCols = i + 2 // 2, 3, 4
	}
	ab, err := s.makeJoinRel(s.findRel(rA), s.findRel(rB))
	if err != nil {
		t.Fatalf("makeJoinRel(a,b): %v", err)
	}
	if ab.NCols != 5 {
		t.Fatalf("NCols(a|b) = %d, want 5", ab.NCols)
	}
	abc, err := s.makeJoinRel(ab, s.findRel(rC))
	if err != nil {
		t.Fatalf("makeJoinRel(ab,c): %v", err)
	}
	if abc.NCols != 9 {
		t.Fatalf("NCols(a|b|c) = %d, want 9 — the count must compose, not reset", abc.NCols)
	}
}

func TestMakeJoinRelRejectsOverlap(t *testing.T) {
	s := jslCtx(t, 2)
	s.clauses = jslClauses()
	s.builder = &recordingBuilder{}
	a := s.findRel(rA)
	if _, err := s.makeJoinRel(a, a); err == nil {
		t.Fatal("makeJoinRel accepted overlapping relsets")
	}
	if _, err := s.makeJoinRel(nil, a); err == nil {
		t.Fatal("makeJoinRel accepted a nil rel")
	}
}

// TestJoinSearchOneLevelGuards covers the level preconditions: PG's
// `Assert(joinrels[level] == NIL)` (joinrels.c:79) and its
// "failed to build any %d-way joins" sanity check (:252-255). The latter is
// unreachable while v1's pin holds — nothing can starve a level when no
// join-order restriction exists — so it is exercised by starving the previous
// level directly.
func TestJoinSearchOneLevelGuards(t *testing.T) {
	s := jslCtx(t, 3)
	s.clauses = jslClauses(rA | rB)
	s.builder = &recordingBuilder{}

	if err := s.joinSearchOneLevel(1); err == nil {
		t.Fatal("joinSearchOneLevel(1) accepted; levels start at 2")
	}
	if err := s.joinSearchOneLevel(4); err == nil {
		t.Fatal("joinSearchOneLevel(4) accepted on a 3-relation problem")
	}
	// Level 2 is empty on entry, so level 3 with an unpopulated level 2 has
	// nothing to extend: phase 1 and phase 3 both iterate an empty list.
	if err := s.joinSearchOneLevel(3); err == nil {
		t.Fatal("joinSearchOneLevel(3) accepted with an empty level 2")
	}

	if err := s.joinSearchOneLevel(2); err != nil {
		t.Fatalf("joinSearchOneLevel(2): %v", err)
	}
	if err := s.joinSearchOneLevel(2); err == nil {
		t.Fatal("joinSearchOneLevel re-ran a populated level")
	}
}

// TestJoinSearchRequiresBuilder: the seam is mandatory, not optional. A search
// with no builder must refuse rather than build path-less rels.
func TestJoinSearchRequiresBuilder(t *testing.T) {
	s := jslCtx(t, 2)
	if _, err := s.joinSearch(jslClauses(rA|rB), nil); err == nil {
		t.Fatal("joinSearch ran without a joinrel builder")
	}
}

// TestJoinSearchPropagatesBuilderError: a builder failure aborts the search
// rather than leaving a path-less rel behind for setCheapest to mis-answer.
func TestJoinSearchPropagatesBuilderError(t *testing.T) {
	s := jslCtx(t, 3)
	b := &recordingBuilder{fail: rA | rB}
	if _, err := s.joinSearch(jslClauses(rA|rB, rB|rC), b); err == nil {
		t.Fatal("joinSearch swallowed a builder error")
	}
}

func wantPairs(t *testing.T, got, want []recordedPair) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("pair sequence length %d, want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pair %d: got %v, want %v\n got: %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}
