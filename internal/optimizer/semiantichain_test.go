package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0142-0008a-3i-plumbing-a (design doc §15 item 2, settled; §21): unit tests
// for `semiAntiChainLink` and its two consumers, `semiAntiLinksHaveSJInfos`
// and `semiAntiOnQualsOK`, landed as inert infrastructure ahead of the
// coupled `extractSearchLeaves`/predp.go wiring change (§21 found item 1's
// walk extension is dead code without it, so it is not done in this loop).
//
// These tests reuse the m0142_0008a_3i_plumbing_probe_test.go fixture and
// prove the POSITIVE case the probe could only show the OLD outerChainLink
// consumers declining: a well-formed Semi link, encoded as a
// semiAntiChainLink (lhs/rhs, no nullable=0 workaround), is ACCEPTED by
// semiAntiOnQualsOK and by semiAntiLinksHaveSJInfos against a real
// existsUnnestSJInfo-built SpecialJoinInfo.

func TestSemiAntiOnQualsOK_DeclinesSingleSideConjunct(t *testing.T) {
	// A conjunct confined to one side has no place in a Semi/Anti link's
	// correlation predicate (any such filter should already have been pushed
	// into the opaque RHS leaf by the unnest rewrite) — decline it rather
	// than silently accept a shape nothing produces today.
	col0 := &ColumnRef{Index: 0}
	link := semiAntiChainLink{
		jointype: parser.JoinSemi,
		lhs:      leafRangeRelSet(0, 1),
		rhs:      leafRangeRelSet(1, 2),
		pred:     &BinaryOp{Op: parser.OpEq, Left: col0, Right: &IntegerConst{Value: int64(5)}},
	}
	cumOffsets := spansFromCumulative([]int{0, 1, 2})
	if semiAntiOnQualsOK([]semiAntiChainLink{link}, cumOffsets) {
		t.Errorf("semiAntiOnQualsOK(single-side conjunct) = true, want false")
	}
}

func TestSemiAntiOnQualsOK_DeclinesNilPredicate(t *testing.T) {
	link := semiAntiChainLink{jointype: parser.JoinSemi, lhs: 1, rhs: 2, pred: nil}
	if semiAntiOnQualsOK([]semiAntiChainLink{link}, spansFromCumulative([]int{0, 1, 2})) {
		t.Errorf("semiAntiOnQualsOK(nil pred) = true, want false — unlike a cartesian LEFT join, a Semi/Anti link always carries a correlation predicate by construction (unnestExistsExpr's own belt check)")
	}
}

func TestSemiAntiLinksHaveSJInfos_MatchesRealSJInfo(t *testing.T) {
	cat := analyzedThreeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (" +
		"SELECT 1 FROM t2, t3 WHERE t2.z = t1.x AND t2.y = t3.a)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found: %s", planString(node))
	}
	pred := j.Predicate
	if pred == nil && j.LeftKey != nil && j.RightKey != nil {
		pred = &BinaryOp{Op: parser.OpEq, Left: j.LeftKey, Right: j.RightKey}
	}

	sj := existsUnnestSJInfo(JoinTypeSemi, nil, []Expr{pred}, 0)
	link := semiAntiChainLink{jointype: parser.JoinSemi, lhs: sj.SynLefthand, rhs: sj.SynRighthand, pred: pred}

	if !semiAntiLinksHaveSJInfos([]semiAntiChainLink{link}, []*SpecialJoinInfo{sj}) {
		t.Errorf("semiAntiLinksHaveSJInfos([link], [sj]) = false, want true — link's sides were built directly from sj's own SynLefthand/SynRighthand")
	}
}

func TestSemiAntiLinksHaveSJInfos_DeclinesWrongJointype(t *testing.T) {
	link := semiAntiChainLink{jointype: parser.JoinAnti, lhs: 1, rhs: 2}
	sj := &SpecialJoinInfo{Jointype: parser.JoinSemi, SynLefthand: 1, SynRighthand: 2}
	if semiAntiLinksHaveSJInfos([]semiAntiChainLink{link}, []*SpecialJoinInfo{sj}) {
		t.Errorf("semiAntiLinksHaveSJInfos(Anti link, [Semi sj]) = true, want false")
	}
}

func TestSemiAntiLinksHaveSJInfos_DeclinesMismatchedSides(t *testing.T) {
	link := semiAntiChainLink{jointype: parser.JoinSemi, lhs: 1, rhs: 4}
	sj := &SpecialJoinInfo{Jointype: parser.JoinSemi, SynLefthand: 1, SynRighthand: 2}
	if semiAntiLinksHaveSJInfos([]semiAntiChainLink{link}, []*SpecialJoinInfo{sj}) {
		t.Errorf("semiAntiLinksHaveSJInfos(mismatched rhs) = true, want false")
	}
}

// M0142-0008a-3i-plumbing-b1 (design doc §22.4): unit tests for the REAL
// `extractSearchLeaves`'s Semi/Anti-admission arm, gated behind the new
// `admitSemiAnti` parameter. These exercise the production function itself
// (not the throwaway `extractSearchLeavesAdmitSemiAnti` probe copy), against
// the same Q69-witness-class fixture the probe and the two tests above
// already established produces `j.Right = *Project{Child: *Join{Inner}}`.

// TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation
// (M0142-0008a-3i-plumbing-c10, design doc §44.3-44.4, filed by c9) proves the
// positive case none of the existing SJInfo-rebuild tests can: with only ONE
// outer relation, `lhs` is already a single bit and "narrowed" is
// indistinguishable from "un-narrowed" (see the BuildsLinkAndRebuildsSJInfo
// test above, whose own assertion — `MinLefthand == wantLHS` — passes either
// way). This fixture gives EXISTS a TWO-relation outer (t1, t3) but
// correlates it to only t1, so a correct fix must produce
// `MinLefthand != lhs` while `SynLefthand` stays the whole composite.
//
// M0142-0008a-3i-plumbing-c16 (design doc §51.1) rewrote this from a
// `Plan()`-driven fixture to a hand-built one. Once c16's SJInfo Path→Join
// carrier landed, the real DP search legally reorders this query as
// `InnerJoin(SemiJoin(t1, t2), t3)` — t1 SEMI t2 evaluated before t3 joins
// in, since the EXISTS correlates only to t1 (exactly the narrowing this
// test exists to prove) — instead of the `SemiJoin(InnerJoin(t1, t3), t2)`
// shape this test's assertions assume. Depending on `Plan()`'s DP-search
// tie-breaking to produce one specific legal shape is exactly the hidden
// coupling this project's history warns against, so the fixture is now
// built directly from `*SeqScan`/`*Join` literals — a true white-box unit
// test of `extractSearchLeaves` alone, independent of join-order costing.
func TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation(t *testing.T) {
	t1Tbl := &catalog.Table{Name: "t1", Columns: []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
	}}
	t3Tbl := &catalog.Table{Name: "t3", Columns: []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}}
	t2Tbl := &catalog.Table{Name: "t2", Columns: []catalog.Column{
		{Name: "y", Type: catalog.Type{Name: "int4"}},
		{Name: "z", Type: catalog.Type{Name: "int4"}},
	}}
	t1 := &SeqScan{Table: t1Tbl, schema: tableSchema(t1Tbl)}
	t3 := &SeqScan{Table: t3Tbl, schema: tableSchema(t3Tbl)}
	t2 := &SeqScan{Table: t2Tbl, schema: tableSchema(t2Tbl)}

	// t1 JOIN t3 ON t1.x = t3.a. t1 is this join's Left, so within its own
	// composite (Left++Right) schema t1.x is index 0 and t3.a is index 1
	// (t1 contributes 1 column ahead of it).
	outerJoin := &Join{
		Type:  JoinTypeInner,
		Left:  t1,
		Right: t3,
		Predicate: &BinaryOp{
			Op:    parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "x"},
			Right: &ColumnRef{Index: 1, Name: "a"},
		},
		schema: append(append(Schema{}, t1.Output()...), t3.Output()...),
	}
	outerWidth := len(outerJoin.Output()) // 3: t1.x, t3.a, t3.b

	// SELECT ... FROM t1, t3 WHERE t1.x = t3.a AND EXISTS (SELECT 1 FROM t2
	// WHERE t2.z = t1.x), in unnestExistsExpr's post-method-selection shape
	// (unnest.go:4738-4762): LeftKey reads the outer's own local index
	// (t1.x, index 0 in outerJoin's composite schema); RightKey is
	// `outerWidth + innerColIndex` (t2.z, local index 1 in t2's own
	// 2-column schema).
	j := &Join{
		Type:     JoinTypeSemi,
		Left:     outerJoin,
		Right:    t2,
		LeftKey:  &ColumnRef{Index: 0, Name: "x"},
		RightKey: &ColumnRef{Index: outerWidth + 1, Name: "z"},
		SJInfo:   &SpecialJoinInfo{Jointype: parser.JoinSemi},
	}

	scans, widths, _, _, semiAnti, ok := extractSearchLeaves(j)
	if !ok {
		t.Fatalf("extractSearchLeaves(j) ok=false, want true")
	}
	if len(scans) != 3 {
		t.Fatalf("scans = %d leaves, want 3 (t1+t3 flattened, plus the RHS opaque leaf): %v widths=%v", len(scans), scans, widths)
	}
	// t1 is deterministically scans[0] in this hand-built tree (no search
	// reordering), but look it up rather than assume it, matching how a
	// real search-produced fixture would still need to.
	t1Idx := -1
	for i, s := range scans[:2] {
		if seq, isSeq := s.(*SeqScan); isSeq && seq.Table != nil && seq.Table.Name == "t1" {
			t1Idx = i
		}
	}
	if t1Idx == -1 {
		t.Fatalf("could not find t1's leaf among the outer scans: %#v", scans[:2])
	}
	if len(semiAnti) != 1 {
		t.Fatalf("semiAnti = %d links, want exactly 1: %+v", len(semiAnti), semiAnti)
	}
	wantLHS := leafRangeRelSet(0, 2)
	if lk := semiAnti[0]; lk.lhs != wantLHS {
		t.Fatalf("semiAnti[0].lhs = %#x, want %#x (the whole 2-relation outer)", lk.lhs, wantLHS)
	}
	if j.SJInfo.SynLefthand != wantLHS {
		t.Errorf("j.SJInfo.SynLefthand = %#x, want %#x — SynLefthand must stay the WHOLE atomic outer side (existsUnnestSJInfo's deliberate convention), narrowing applies to MinLefthand only", j.SJInfo.SynLefthand, wantLHS)
	}
	wantMinL := leafRangeRelSet(t1Idx, t1Idx+1)
	if j.SJInfo.MinLefthand != wantMinL {
		t.Errorf("j.SJInfo.MinLefthand = %#x, want %#x (narrowed to just t1, the correlation's real referenced relation) — got the un-narrowed %#x instead", j.SJInfo.MinLefthand, wantMinL, j.SJInfo.MinLefthand)
	}
	// MinRighthand cannot narrow below the single-bit synthetic RHS leaf —
	// still pinned as a sanity check that the symmetric computation didn't
	// zero it out or otherwise corrupt it.
	wantRHS := leafRangeRelSet(2, 3)
	if j.SJInfo.MinRighthand != wantRHS {
		t.Errorf("j.SJInfo.MinRighthand = %#x, want %#x (the single synthetic RHS leaf, unchanged)", j.SJInfo.MinRighthand, wantRHS)
	}
}

// TestExtractSearchLeaves_AdmitSemiAnti_ChainedLinksRebaseInnerKeyCorrectly
// pins the c7 fix (design doc §41.3): TWO semiAnti links chained over the
// same outer (Q69's pattern — three EXISTS/NOT EXISTS conjuncts over one
// outer, here reduced to the minimal two-link case) must each resolve their
// own equijoin's INNER operand to their OWN opaque leaf, not to an earlier
// sibling link's leaf. `rebaseChainQual`'s single additive `base` gets the
// FIRST (unchained) link right by construction but silently mis-shifts the
// SECOND link's inner operand into the first link's leaf range, since
// unnestExistsExpr's RightKey.Index (`outerWidth + subcol`, unnest.go:4694)
// is encoded relative to the SEMANTIC outer width
// (`len(j.Left.Output())`), not the walk's own flat leaf count — the two
// diverge exactly when `j.Left` already has an earlier chained link's
// opaque leaf spliced into it, which is the second link's situation here.
// Before the fix, `semiAntiOnQualsOK` declines (the inner operand doesn't
// overlap `rhs`) — the exact `overlapR=false` symptom the design doc's
// throwaway Q69 instrumentation measured.
func TestExtractSearchLeaves_AdmitSemiAnti_ChainedLinksRebaseInnerKeyCorrectly(t *testing.T) {
	cat := analyzedThreeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.z = t1.x) " +
		"AND EXISTS (SELECT 1 FROM t3 WHERE t3.a = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found: %s", planString(node))
	}
	inner, isJoin := j.Left.(*Join)
	if !isJoin || inner.Type != JoinTypeSemi {
		t.Fatalf("j.Left = %T, want a chained *Join{Type:Semi} (the FIRST EXISTS's link) — fixture shape drifted from the expected chained tree: %s", j.Left, planString(node))
	}

	scans, widths, _, _, semiAnti, ok := extractSearchLeaves(j)
	if !ok {
		t.Fatalf("extractSearchLeaves(j) ok=false, want true: %s", planString(node))
	}
	if len(scans) != 3 {
		t.Fatalf("scans = %d leaves, want 3 (t1, RHS1, RHS2): widths=%v", len(scans), widths)
	}
	if len(semiAnti) != 2 {
		t.Fatalf("semiAnti = %d links, want exactly 2 (chained): %+v", len(semiAnti), semiAnti)
	}

	link1, link2 := semiAnti[0], semiAnti[1]
	wantLHS1, wantRHS1 := leafRangeRelSet(0, 1), leafRangeRelSet(1, 2)
	if link1.lhs != wantLHS1 || link1.rhs != wantRHS1 {
		t.Errorf("semiAnti[0] = {lhs:%#x, rhs:%#x}, want {lhs:%#x, rhs:%#x}", link1.lhs, link1.rhs, wantLHS1, wantRHS1)
	}
	wantLHS2, wantRHS2 := leafRangeRelSet(0, 2), leafRangeRelSet(2, 3)
	if link2.lhs != wantLHS2 || link2.rhs != wantRHS2 {
		t.Errorf("semiAnti[1] = {lhs:%#x, rhs:%#x}, want {lhs:%#x, rhs:%#x}", link2.lhs, link2.rhs, wantLHS2, wantRHS2)
	}

	cumOffsets := buildLeafSpans(widths, semiAnti)
	// The load-bearing assertion: before c7, link2's folded equality
	// resolves its inner operand into leaf 1 (link1's own opaque leaf)
	// instead of leaf 2 (link2's own), so `relidsOfExpr` lands OUTSIDE
	// `link2.rhs` and this declines.
	for i, lk := range []semiAntiChainLink{link1, link2} {
		for _, c := range splitAnd(lk.pred) {
			rs, ok := relidsOfExpr(c, cumOffsets)
			if !ok || !relsOverlap(rs, lk.rhs) {
				t.Errorf("semiAnti[%d] conjunct %#v: relidsOfExpr = %#x (ok=%v), want overlap with rhs=%#x", i, c, rs, ok, lk.rhs)
			}
		}
	}
	if !semiAntiOnQualsOK(semiAnti, cumOffsets) {
		t.Errorf("semiAntiOnQualsOK(semiAnti, cumOffsets) = false, want true — both chained links' quals must resolve within their own {lhs,rhs}")
	}
}

// TestExtractSearchLeaves_SemiJoinIsAdmitted proves the only remaining
// arm's contract: with `admitSemiAnti` retired (M0145-0005 slice 2 —
// the one production call site had passed `true` since b2, so the off
// arm was dead flexibility) a Semi join is ALWAYS decomposed — its
// left side walks to real leaves, its right side becomes one opaque
// synthetic leaf (no FlattenedRHS marker here), and the walk renumbers
// its placeholder SJInfo to the real leaf-index bits.
func TestExtractSearchLeaves_SemiJoinIsAdmitted(t *testing.T) {
	cat := analyzedThreeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (" +
		"SELECT 1 FROM t2, t3 WHERE t2.z = t1.x AND t2.y = t3.a)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found: %s", planString(node))
	}

	scans, _, onQuals, outer, semiAnti, ok := extractSearchLeaves(j)
	if !ok {
		t.Fatalf("extractSearchLeaves(j) ok=false, want true")
	}
	if len(scans) != 2 || scans[0] != j.Left || scans[1] != j.Right {
		t.Errorf("scans = %v, want [j.Left, j.Right] — the left side descends to its leaf, the right side is one opaque leaf", scans)
	}
	if len(onQuals) != 0 || len(outer) != 0 || len(semiAnti) != 1 {
		t.Errorf("onQuals=%v outer=%v semiAnti=%v, want [0,0,1] — one admitted Semi link", onQuals, outer, semiAnti)
	}
	if len(semiAnti) == 1 && (semiAnti[0].lhs != leafRangeRelSet(0, 1) || semiAnti[0].rhs != leafRangeRelSet(1, 2)) {
		t.Errorf("semiAnti[0].{lhs,rhs} = {%#x,%#x}, want {01,10}", semiAnti[0].lhs, semiAnti[0].rhs)
	}
	if j.SJInfo != nil && (j.SJInfo.SynLefthand != 1 || j.SJInfo.SynRighthand != 2) {
		t.Errorf("j.SJInfo.{SynLefthand,SynRighthand} = {%#x,%#x}, want {01,10} — renumbered to the real leaf-index bits", j.SJInfo.SynLefthand, j.SJInfo.SynRighthand)
	}
}

// TestBuildLeafSpansAttributesRealLeafAfterSyntheticCorrectly pins §25.1's
// traced bug and §25.3's fix directly, at the `(A SEMI JOIN B) JOIN C ON
// qualAC`-shaped level design doc §25.4 named as the one case no existing
// `-b1` test covers: a REAL leaf (C) positioned AFTER a Semi/Anti node's
// synthetic RHS leaf (B) in walk order, with B's real width NONZERO (the
// M14/R3-4 shapes §25.1 named, not the dormant `wB=0` bare-EXISTS case that
// happens to survive under the old cumulative scheme too).
//
// Old `cumOffsets` (plain cumulative sum over walk-order widths) put B's
// range at [wA, wA+wB) and C's at [wA+wB, wA+wB+wC) — so a qual referencing
// C at its REAL, `ctx.bindings`-resolved absolute index `wA+k` (for any
// `k < wB`) fell inside B's range and was misattributed to the synthetic
// leaf instead of C. `buildLeafSpans` (§25.3) fixes this by keeping every
// real leaf's range exactly as `ctx.bindings` assigned it (skipping
// synthetic widths when accumulating) and appending the synthetic leaf's
// range out of band, after the total real width.
func TestBuildLeafSpansAttributesRealLeafAfterSyntheticCorrectly(t *testing.T) {
	const wA, wB, wC = 2, 2, 2
	widths := []int{wA, wB, wC} // walk order: A (real), B (synthetic RHS), C (real)
	semiAnti := []semiAntiChainLink{{
		jointype: parser.JoinSemi,
		lhs:      leafRangeRelSet(0, 1), // A
		rhs:      leafRangeRelSet(1, 2), // B, synthetic
	}}

	spans := buildLeafSpans(widths, semiAnti)
	if len(spans) != 3 {
		t.Fatalf("len(spans) = %d, want 3", len(spans))
	}
	// A (real leaf 0): untouched, matches what ctx.bindings would assign —
	// [0, wA).
	if spans[0] != (leafSpan{lo: 0, hi: wA}) {
		t.Errorf("spans[0] (A) = %+v, want {lo:0, hi:%d}", spans[0], wA)
	}
	// C (real leaf 2): appended right after A's real width, NOT shifted by
	// B's synthetic width — [wA, wA+wC), exactly what ctx.bindings resolved
	// C's columns against pre-unnest, since B never existed in that
	// coordinate space.
	if spans[2] != (leafSpan{lo: wA, hi: wA + wC}) {
		t.Errorf("spans[2] (C) = %+v, want {lo:%d, hi:%d} — a real leaf after a synthetic one must not inherit the synthetic leaf's width", spans[2], wA, wA+wC)
	}
	// B (synthetic leaf 1): out-of-band, after the total real width (wA+wC).
	if spans[1] != (leafSpan{lo: wA + wC, hi: wA + wC + wB}) {
		t.Errorf("spans[1] (B, synthetic) = %+v, want {lo:%d, hi:%d} (appended after total real width)", spans[1], wA+wC, wA+wC+wB)
	}

	// qualAC: C's first column, at its REAL absolute index wA+0 = wA — the
	// exact index §25.1 traced falling inside B's OLD (buggy) range whenever
	// k < wB, which holds here (k=0 < wB=2).
	qualAC := &ColumnRef{Index: wA}
	rs, ok := relidsOfExpr(qualAC, spans)
	if !ok {
		t.Fatalf("relidsOfExpr(qualAC, spans) ok=false, want true")
	}
	wantLeaf2 := leafRangeRelSet(2, 3)
	if rs != wantLeaf2 {
		t.Errorf("relidsOfExpr(qualAC, spans) = %#x, want %#x (leaf 2, C) — got attributed to a different leaf, which is exactly §25.1's misattribution if it reproduces leaf 1 (B, %#x)", rs, wantLeaf2, leafRangeRelSet(1, 2))
	}
}

// TestRemapWalkOrderFlatToSpans_RealLeafAfterSyntheticRHS pins
// M0142-0008a-3i-plumbing-c20 (design doc §56) directly, at unit-test size:
// TPC-DS Q78's live shape is `(A ANTI B) JOIN C`, walk order A(real,leaf0)
// B(synthetic RHS,leaf1) C(real,leaf2) — the exact shape
// `TestBuildLeafSpansAttributesRealLeafAfterSyntheticCorrectly` above already
// pins at the `buildLeafSpans` level. What THAT test did not cover is a
// semiAnti link's own folded `LeftKey=RightKey` predicate, which
// `extractSearchLeaves` rebases into WALK-ORDER flat space (leaf B's columns
// start right after leaf A's, at index wA) — a space `buildLeafSpans`'s
// `cumOffsets` does NOT share, since it relocates B (synthetic) out-of-band
// after C (real). A predicate ColumnRef pointing at B's first column
// (walk-order-flat index wA) must resolve, after remapping, to leaf 1 (B),
// never to leaf 2 (C) — the exact misattribution Q78 hit live.
func TestRemapWalkOrderFlatToSpans_RealLeafAfterSyntheticRHS(t *testing.T) {
	const wA, wB, wC = 2, 2, 2
	widths := []int{wA, wB, wC} // walk order: A (real), B (synthetic RHS), C (real)
	semiAnti := []semiAntiChainLink{{
		jointype: parser.JoinAnti,
		lhs:      leafRangeRelSet(0, 1), // A
		rhs:      leafRangeRelSet(1, 2), // B, synthetic
	}}
	cumOffsets := buildLeafSpans(widths, semiAnti)

	// A folded equality conjunct, in `extractSearchLeaves`'s own walk-order
	// flat space: A's second column (index 1, well within A's [0,wA)) equals
	// B's first column (index wA, walk-order-flat — the ANTI join's synthetic
	// RHS leaf starts right after A in WALK order, base=0/rightBase=outerWidth
	// exactly like Q78's live trace).
	pred := &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: 1},
		Right: &ColumnRef{Index: wA},
	}

	remapped, ok := remapWalkOrderFlatToSpans(pred, widths, cumOffsets, 0, 0, identityLeafPerm(len(widths)))
	if !ok {
		t.Fatalf("remapWalkOrderFlatToSpans(...) ok=false, want true")
	}
	bo, isBinOp := remapped.(*BinaryOp)
	if !isBinOp {
		t.Fatalf("remapped = %T, want *BinaryOp", remapped)
	}
	left, isCol := bo.Left.(*ColumnRef)
	if !isCol {
		t.Fatalf("remapped.Left = %T, want *ColumnRef", bo.Left)
	}
	right, isCol := bo.Right.(*ColumnRef)
	if !isCol {
		t.Fatalf("remapped.Right = %T, want *ColumnRef", bo.Right)
	}

	if left.Index != cumOffsets[0].lo+1 {
		t.Errorf("remapped Left.Index = %d, want %d (leaf 0/A, local offset 1)", left.Index, cumOffsets[0].lo+1)
	}
	// This is the exact bug: without the fix, Right.Index stays at the
	// walk-order-flat value `wA`, which lands inside leaf 2 (C)'s cumOffsets
	// range, not leaf 1 (B)'s.
	if right.Index != cumOffsets[1].lo {
		t.Errorf("remapped Right.Index = %d, want %d (leaf 1/B's cumOffsets.lo, NOT leaf 2/C's range %v)", right.Index, cumOffsets[1].lo, cumOffsets[2])
	}

	rs, okRelids := relidsOfExpr(bo.Right, cumOffsets)
	if !okRelids {
		t.Fatalf("relidsOfExpr(remapped.Right, cumOffsets) ok=false, want true")
	}
	wantLeaf1 := leafRangeRelSet(1, 2)
	if rs != wantLeaf1 {
		t.Errorf("relidsOfExpr(remapped.Right, cumOffsets) = %#x, want %#x (leaf 1, B) — got attributed to a different leaf (leaf 2/C is %#x), which is exactly the Q78 misattribution this fix corrects", rs, wantLeaf1, leafRangeRelSet(2, 3))
	}
}

// TestPgShapedOffsetChecksOK_ReducesToPlainChecksWhenNoSemiAnti pins design
// doc §31.3's inertness claim: with `semiAnti` empty — still the only
// production shape today, because every corpus chain carrying a link
// declines at the leaf-count gate before reaching this check —
// `pgShapedOffsetChecksOK` must behave exactly like the plain per-index
// comparison it replaced — both accepting a well-formed two-leaf-plus-spine
// shape and declining a mismatched one.
func TestPgShapedOffsetChecksOK_ReducesToPlainChecksWhenNoSemiAnti(t *testing.T) {
	widths := []int{2, 3} // two real leaves, no synthetic ones
	cumOffsets := buildLeafSpans(widths, nil)
	bindingOffsets := []int{0, 2} // matches cumOffsets exactly

	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, 0, widths, bindingOffsets, true, 5); !ok {
		t.Errorf("pgShapedOffsetChecksOK(...) declined (%q), want accepted — matches the old plain checks on a well-formed shape", reason)
	}
	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, 0, widths, []int{0, 99}, true, 5); ok {
		t.Errorf("pgShapedOffsetChecksOK(...) = accepted, want declined (offset-disagreement) — bindingOffsets[1] deliberately mismatches cumOffsets[1].lo")
	} else if reason != "offset-disagreement" {
		t.Errorf("declineReason = %q, want %q", reason, "offset-disagreement")
	}
	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, 0, widths, bindingOffsets, true, 99); ok {
		t.Errorf("pgShapedOffsetChecksOK(...) = accepted, want declined (spine-offset-disagreement) — spineOffset deliberately mismatches the real total width")
	} else if reason != "spine-offset-disagreement" {
		t.Errorf("declineReason = %q, want %q", reason, "spine-offset-disagreement")
	}
}

// TestPgShapedOffsetChecksOK_RealLeafAfterSynthetic pins design doc §31.3
// item 2's fix directly: a REAL leaf (C) positioned AFTER a Semi/Anti
// synthetic RHS leaf (B) in walk order must be compared against its own
// ctx.bindings entry, not shifted by B's presence — the exact shape the old
// plain `ctx.bindings[i].offset != cumOffsets[i].lo` loop got wrong (and,
// since `len(scans) > len(ctx.bindings)` whenever a synthetic leaf is
// present, would have PANICKED on an index out of range rather than merely
// mis-comparing).
func TestPgShapedOffsetChecksOK_RealLeafAfterSynthetic(t *testing.T) {
	const wA, wB, wC = 2, 2, 2
	widths := []int{wA, wB, wC} // walk order: A (real), B (synthetic RHS), C (real)
	semiAnti := []semiAntiChainLink{{
		jointype: parser.JoinSemi,
		lhs:      leafRangeRelSet(0, 1), // A
		rhs:      leafRangeRelSet(1, 2), // B, synthetic
	}}
	cumOffsets := buildLeafSpans(widths, semiAnti)
	// ctx.bindings only ever holds REAL leaves: A at offset 0, C at offset
	// wA (B was never a FROM item and has no entry).
	bindingOffsets := []int{0, wA}

	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, semiAnti[0].rhs, widths, bindingOffsets, false, 0); !ok {
		t.Errorf("pgShapedOffsetChecksOK(...) declined (%q), want accepted — A and C both agree with their real ctx.bindings offsets once B is skipped", reason)
	}
	// A deliberate mismatch on C's binding offset must still be caught.
	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, semiAnti[0].rhs, widths, []int{0, wA + 1}, false, 0); ok {
		t.Errorf("pgShapedOffsetChecksOK(...) = accepted, want declined (offset-disagreement) — C's binding offset deliberately mismatches")
	} else if reason != "offset-disagreement" {
		t.Errorf("declineReason = %q, want %q", reason, "offset-disagreement")
	}
}

// TestPgShapedOffsetChecksOK_SyntheticLastInWalkOrder pins design doc
// §31.3 item 3's fix: when the Semi/Anti synthetic leaf is LAST in walk
// order (a bare trailing EXISTS with a spine above it — plausibly the
// common case, not a corner one), the spine's own offset must be compared
// against the REAL total width, not `cumOffsets`'s raw last entry, which
// `buildLeafSpans` places out-of-band past the real total and therefore
// overshoots by the synthetic leaf's own width.
func TestPgShapedOffsetChecksOK_SyntheticLastInWalkOrder(t *testing.T) {
	const wA, wB = 3, 2
	widths := []int{wA, wB} // walk order: A (real), B (synthetic RHS, LAST)
	semiAnti := []semiAntiChainLink{{
		jointype: parser.JoinSemi,
		lhs:      leafRangeRelSet(0, 1), // A
		rhs:      leafRangeRelSet(1, 2), // B, synthetic, last
	}}
	cumOffsets := buildLeafSpans(widths, semiAnti)
	bindingOffsets := []int{0} // A alone

	// The spine begins right after A's real width (wA) — NOT after
	// cumOffsets's raw last entry, which would be wA+wB (B's out-of-band
	// span's `hi`) and would false-decline this well-formed shape.
	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, semiAnti[0].rhs, widths, bindingOffsets, true, wA); !ok {
		t.Errorf("pgShapedOffsetChecksOK(...) declined (%q), want accepted — spine offset %d correctly matches the REAL total width (%d), not cumOffsets' raw last entry (%d)", reason, wA, wA, cumOffsets[len(cumOffsets)-1].hi)
	}
	// The old (buggy) comparison target, `cumOffsets[len-1].hi` = wA+wB, must
	// NOT be what this function accepts — passing it as spineOffset proves
	// the fix is live, not accidentally still comparing against the old value.
	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, semiAnti[0].rhs, widths, bindingOffsets, true, wA+wB); ok {
		t.Errorf("pgShapedOffsetChecksOK(...) = accepted for spineOffset=%d (the OLD buggy target, cumOffsets' raw last .hi), want declined — this would mean the fix regressed back to comparing against the synthetic leaf's out-of-band span", wA+wB)
	} else if reason != "spine-offset-disagreement" {
		t.Errorf("declineReason = %q, want %q", reason, "spine-offset-disagreement")
	}
}

// TestSemiAntiPredHasKeyEq covers the helper's orientation and fail-closed
// cases.
func TestSemiAntiPredHasKeyEq(t *testing.T) {
	a := &ColumnRef{Index: 0, Name: "a", SourceTableIdx: 1}
	b := &ColumnRef{Index: 3, Name: "b", SourceTableIdx: 2}
	c := &ColumnRef{Index: 4, Name: "c", SourceTableIdx: 2}
	eq := func(l, r Expr) Expr { return &BinaryOp{Op: parser.OpEq, Left: l, Right: r} }
	cases := []struct {
		name string
		pred Expr
		want bool
	}{
		{"nil predicate", nil, false},
		{"same orientation", eq(a, b), true},
		{"commuted", eq(b, a), true},
		{"inside a conjunction", combineAnd([]Expr{eq(a, c), eq(b, a)}), true},
		{"different column", eq(a, c), false},
		{"not an equality", &BinaryOp{Op: parser.OpLt, Left: a, Right: b}, false},
	}
	for _, tc := range cases {
		if got := semiAntiPredHasKeyEq(tc.pred, a, b); got != tc.want {
			t.Errorf("%s: semiAntiPredHasKeyEq = %v, want %v", tc.name, got, tc.want)
		}
	}
}
