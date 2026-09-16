package optimizer

import (
	"testing"

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

func TestSemiAntiOnQualsOK_AcceptsWellFormedLink(t *testing.T) {
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

	// Mirror the probe's leaf/width reconstruction: t1 (leaf 0), RHS *Project
	// as one opaque leaf (leaf 1).
	scans, widths, _, _, ok := extractSearchLeavesAdmitSemiAnti(j)
	if !ok || len(scans) != 2 {
		t.Fatalf("extractSearchLeavesAdmitSemiAnti(j) = (%v leaves, ok=%v), want 2 leaves ok=true", len(scans), ok)
	}
	cum := make([]int, len(widths)+1)
	for i, w := range widths {
		cum[i+1] = cum[i] + w
	}
	cumOffsets := spansFromCumulative(cum)

	pred := j.Predicate
	if pred == nil && j.LeftKey != nil && j.RightKey != nil {
		pred = &BinaryOp{Op: parser.OpEq, Left: j.LeftKey, Right: j.RightKey}
	}
	if pred == nil {
		t.Fatalf("no correlation predicate on the Semi join: %#v", j)
	}

	link := semiAntiChainLink{
		jointype: parser.JoinSemi,
		lhs:      leafRangeRelSet(0, 1),
		rhs:      leafRangeRelSet(1, 2),
		pred:     pred,
	}

	if !semiAntiOnQualsOK([]semiAntiChainLink{link}, cumOffsets) {
		t.Errorf("semiAntiOnQualsOK(well-formed Semi link) = false, want true — " +
			"this is exactly the shape outerOnQualsOK incorrectly declined (§15)")
	}
}

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

// TestProblemPairsOuterWithDerivedSemiOverDerived mirrors
// TestProblemPairsOuterWithDerivedCTEOuter (outer_over_derived_test.go) for
// the new Semi/Anti arm added to problemPairsOuterWithDerived's switch
// (design doc §15's "new safety finding," landed this loop as the SAME
// change that adds semiAntiChainLink, per §15's own instruction not to defer
// this arm past the change that first makes Semi/Anti admission possible).
func TestProblemPairsOuterWithDerivedSemiOverDerived(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{baseLeaf(), cteLeaf("sub")},
		relInfos: []baseRelInfo{{table: baseTable()}, {table: cteTable("sub")}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinSemi, SynLefthand: 1 << 0, SynRighthand: 1 << 1},
	}
	if !problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("a Semi join whose RHS hand touches a derived (CTE) input must decline, same as LEFT/RIGHT/FULL")
	}
}

func TestProblemPairsOuterWithDerivedAntiOverDerived(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{baseLeaf(), cteLeaf("sub")},
		relInfos: []baseRelInfo{{table: baseTable()}, {table: cteTable("sub")}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinAnti, SynLefthand: 1 << 0, SynRighthand: 1 << 1},
	}
	if !problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("an Anti join whose RHS hand touches a derived (CTE) input must decline, same as LEFT/RIGHT/FULL")
	}
}

// TestProblemPairsOuterWithDerivedSemiOverMultiRelationRHSDoesNotDecline is
// the c8 fix (design doc §42.4): a Semi/Anti link's own opaque RHS leaf has
// no single base table (`table == nil`, same as a genuine CTE leaf above),
// but that is NOT the same "no statistics" signal — joinsearchseam.go's
// synthetic-leaf construction site prices it with `EstimateRows` over an
// already-cost-estimated join subtree and marks it
// `isSemiAntiSyntheticLeaf`, so `problemPairsOuterWithDerived` must not
// decline solely because of it (unlike the two tests above, which must keep
// declining on a REAL derived leaf).
func TestProblemPairsOuterWithDerivedSemiOverMultiRelationRHSDoesNotDecline(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{baseLeaf(), &Join{Type: JoinTypeInner, Left: baseLeaf(), Right: baseLeaf()}},
		relInfos: []baseRelInfo{{table: baseTable()}, {isSemiAntiSyntheticLeaf: true}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinSemi, SynLefthand: 1 << 0, SynRighthand: 1 << 1},
	}
	if problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("a Semi join's own multi-relation RHS leaf (real per-child stats, no single base table) must NOT decline via the derived-input guard")
	}
}

// M0142-0008a-3i-plumbing-b1 (design doc §22.4): unit tests for the REAL
// `extractSearchLeaves`'s Semi/Anti-admission arm, gated behind the new
// `admitSemiAnti` parameter. These exercise the production function itself
// (not the throwaway `extractSearchLeavesAdmitSemiAnti` probe copy), against
// the same Q69-witness-class fixture the probe and the two tests above
// already established produces `j.Right = *Project{Child: *Join{Inner}}`.

// TestExtractSearchLeaves_AdmitSemiAnti_BuildsLinkAndRebuildsSJInfo proves
// items 2-4 of §22.4's scaffold together: the walk extension flattens the
// Semi join's LHS while keeping its RHS one opaque leaf, builds a
// `semiAntiChainLink` real consumers (`semiAntiOnQualsOK`,
// `semiAntiLinksHaveSJInfos`) accept, and rebuilds the join's own attached
// `SJInfo` (originally `existsUnnestSJInfo`'s throwaway synL=1/synR=2) with
// the real leaf-index bits.
func TestExtractSearchLeaves_AdmitSemiAnti_BuildsLinkAndRebuildsSJInfo(t *testing.T) {
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
	if j.SJInfo == nil {
		t.Fatalf("j.SJInfo = nil, want the inert SpecialJoinInfo unnestExistsExpr attaches")
	}
	// The placeholder this loop's change replaces (unnest.go's
	// existsUnnestSJInfo, §22.4 item 4's stated target).
	if j.SJInfo.SynLefthand != 1 || j.SJInfo.SynRighthand != 2 {
		t.Fatalf("j.SJInfo.{SynLefthand,SynRighthand} = {%#x,%#x} before admission, want the throwaway {1,2} placeholder — fixture or existsUnnestSJInfo changed out from under this test", j.SJInfo.SynLefthand, j.SJInfo.SynRighthand)
	}
	// This fixture is the FINAL planned tree (post join-method selection):
	// a hash-keyed Semi/Anti carries its correlation as (LeftKey, RightKey),
	// not j.Predicate (m0142_0008a_3i_plumbing_probe_test.go's own finding).
	// extractSearchLeaves's real production call site (predp.go's pre-search
	// origChain) runs BEFORE method selection, so Predicate is always
	// populated there — reconstruct it here purely to exercise that real,
	// predicate-bearing case against this post-selection fixture.
	if j.Predicate == nil && j.LeftKey != nil && j.RightKey != nil {
		j.Predicate = &BinaryOp{Op: parser.OpEq, Left: j.LeftKey, Right: j.RightKey}
	}
	if j.Predicate == nil {
		t.Fatalf("no correlation predicate on the Semi join: %#v", j)
	}

	scans, widths, onQuals, outer, semiAnti, ok := extractSearchLeaves(j, true)
	if !ok {
		t.Fatalf("extractSearchLeaves(j, true) ok=false, want true: %s", planString(node))
	}
	if len(scans) != 2 {
		t.Fatalf("scans = %d leaves (%v), want 2 (t1, and the RHS *Project as one opaque leaf) — widths=%v", len(scans), scans, widths)
	}
	if _, isProj := scans[1].(*Project); !isProj {
		t.Errorf("scans[1] = %T, want *Project (RHS stays opaque, no recursion into inner t2/t3)", scans[1])
	}
	if len(onQuals) != 0 {
		t.Errorf("onQuals = %v, want none — the Semi join's predicate must land in `semiAnti`, not `onQuals`", onQuals)
	}
	if len(outer) != 0 {
		t.Errorf("outer = %v, want none — a Semi/Anti link is a semiAntiChainLink, not an outerChainLink", outer)
	}
	if len(semiAnti) != 1 {
		t.Fatalf("semiAnti = %d links, want exactly 1: %+v", len(semiAnti), semiAnti)
	}
	lk := semiAnti[0]
	wantLHS, wantRHS := leafRangeRelSet(0, 1), leafRangeRelSet(1, 2)
	if lk.jointype != parser.JoinSemi {
		t.Errorf("semiAnti[0].jointype = %v, want parser.JoinSemi", lk.jointype)
	}
	if lk.lhs != wantLHS || lk.rhs != wantRHS {
		t.Errorf("semiAnti[0] = {lhs:%#x, rhs:%#x}, want {lhs:%#x, rhs:%#x}", lk.lhs, lk.rhs, wantLHS, wantRHS)
	}
	if lk.pred == nil {
		t.Fatalf("semiAnti[0].pred = nil, want the correlation predicate")
	}

	cumOffsets := buildLeafSpans(widths, semiAnti)
	if !semiAntiOnQualsOK(semiAnti, cumOffsets) {
		t.Errorf("semiAntiOnQualsOK(semiAnti, cumOffsets) = false, want true — the walk's own link must satisfy the consumer it was built to feed")
	}

	// Item 4: the placeholder synL=1/synR=2 must be REPLACED by the real
	// leaf-index bits, in place, on the same *SpecialJoinInfo the Join node
	// already carries.
	if j.SJInfo.SynLefthand != wantLHS || j.SJInfo.SynRighthand != wantRHS {
		t.Errorf("after admission, j.SJInfo.{SynLefthand,SynRighthand} = {%#x,%#x}, want {%#x,%#x} (rebuilt from real leaf-index bits, replacing the {1,2} placeholder)", j.SJInfo.SynLefthand, j.SJInfo.SynRighthand, wantLHS, wantRHS)
	}
	if j.SJInfo.MinLefthand != wantLHS || j.SJInfo.MinRighthand != wantRHS {
		t.Errorf("after admission, j.SJInfo.{MinLefthand,MinRighthand} = {%#x,%#x}, want {%#x,%#x}", j.SJInfo.MinLefthand, j.SJInfo.MinRighthand, wantLHS, wantRHS)
	}
	if !semiAntiLinksHaveSJInfos(semiAnti, []*SpecialJoinInfo{j.SJInfo}) {
		t.Errorf("semiAntiLinksHaveSJInfos(semiAnti, [j.SJInfo]) = false, want true — the rebuilt SJInfo must match the link the same walk just built")
	}
}

// TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation
// (M0142-0008a-3i-plumbing-c10, design doc §44.3-44.4, filed by c9) proves the
// positive case none of the existing SJInfo-rebuild tests can: with only ONE
// outer relation, `lhs` is already a single bit and "narrowed" is
// indistinguishable from "un-narrowed" (see the BuildsLinkAndRebuildsSJInfo
// test above, whose own assertion — `MinLefthand == wantLHS` — passes either
// way). This fixture gives EXISTS a TWO-relation outer (t1, t3) but
// correlates it to only t1, so a correct fix must produce
// `MinLefthand != lhs` while `SynLefthand` stays the whole composite.
func TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation(t *testing.T) {
	cat := analyzedThreeTablesCatalog(t)
	// Every outer column is selected (t1.x, t3.a, t3.b) so the narrowing
	// pass has nothing to prune and leaves t1 JOIN t3 as a raw *Join chain
	// instead of wrapping it in a *Project — extractSearchLeaves only
	// flattens *Join nodes, so a Project in between would opaque the whole
	// composite into one leaf and defeat this test's purpose.
	sql := "SELECT t1.x, t3.a, t3.b FROM t1, t3 WHERE t1.x = t3.a AND EXISTS (" +
		"SELECT 1 FROM t2 WHERE t2.z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found: %s", planString(node))
	}
	if j.SJInfo == nil {
		t.Fatalf("j.SJInfo = nil, want the inert SpecialJoinInfo unnestExistsExpr attaches")
	}
	// Same reconstruction TestExtractSearchLeaves_AdmitSemiAnti_BuildsLinkAndRebuildsSJInfo
	// uses: this is the post-method-selection fixture, so the correlation
	// lives in (LeftKey, RightKey), not Predicate.
	if j.Predicate == nil && j.LeftKey != nil && j.RightKey != nil {
		j.Predicate = &BinaryOp{Op: parser.OpEq, Left: j.LeftKey, Right: j.RightKey}
	}
	if j.Predicate == nil {
		t.Fatalf("no correlation predicate on the Semi join: %#v", j)
	}

	scans, widths, _, _, semiAnti, ok := extractSearchLeaves(j, true)
	if !ok {
		t.Fatalf("extractSearchLeaves(j, true) ok=false, want true: %s", planString(node))
	}
	if len(scans) != 3 {
		t.Fatalf("scans = %d leaves, want 3 (t1+t3 flattened, plus the RHS opaque leaf): %v widths=%v", len(scans), scans, widths)
	}
	// t1's leaf position depends on join-order costing, not source-text
	// order, so find it rather than assuming index 0.
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

// TestExtractSearchLeaves_AdmitSemiAnti_FoldsKeyEquijoinIntoPred pins design
// doc §28.4's finding (M0142-0008a-3i-plumbing-b2 step 3 scoping pass): a
// bare correlated EXISTS with no OTHER residual leaves `j.Predicate` nil —
// its whole correlation lives in (LeftKey, RightKey), which unnestExistsExpr
// deliberately excludes from Predicate since the hash match enforces it at
// execution time (unnest.go's `join := &Join{Predicate: joinPredicate}`
// followed by a SEPARATE `join.LeftKey = outerKey` assignment). Unlike
// TestExtractSearchLeaves_AdmitSemiAnti_BuildsLinkAndRebuildsSJInfo (which
// hand-patches j.Predicate before calling extractSearchLeaves, working around
// exactly this gap), this fixture calls extractSearchLeaves against the
// UNPATCHED, naturally-nil Predicate: before the §28.4 fix the resulting
// semiAntiChainLink.pred would be nil (semiAntiOnQualsOK declines a nil pred,
// per TestSemiAntiOnQualsOK_DeclinesNilPredicate) — a future plan-build arm
// consuming `pred` alone would silently build an unconditional (Cartesian-
// like) Semi/Anti the moment admitSemiAnti is ever wired live.
func TestExtractSearchLeaves_AdmitSemiAnti_FoldsKeyEquijoinIntoPred(t *testing.T) {
	cat := analyzedThreeTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found: %s", planString(node))
	}
	if j.Predicate != nil {
		t.Fatalf("j.Predicate = %#v, want nil — this fixture is only a witness for the gap if the equijoin is ENTIRELY absent from Predicate (fixture drifted, or unnestExistsExpr's own convention changed)", j.Predicate)
	}
	if j.LeftKey == nil || j.RightKey == nil {
		t.Fatalf("j.{LeftKey,RightKey} = {%v,%v}, want both set — this fixture must produce a hash-keyed Semi join", j.LeftKey, j.RightKey)
	}

	_, widths, _, _, semiAnti, ok := extractSearchLeaves(j, true)
	if !ok {
		t.Fatalf("extractSearchLeaves(j, true) ok=false, want true: %s", planString(node))
	}
	if len(semiAnti) != 1 {
		t.Fatalf("semiAnti = %d links, want exactly 1: %+v", len(semiAnti), semiAnti)
	}
	lk := semiAnti[0]
	if lk.pred == nil {
		t.Fatalf("semiAnti[0].pred = nil, want the (LeftKey = RightKey) equijoin folded in — the equality is otherwise dropped entirely (§28.4)")
	}
	conjuncts := splitAnd(lk.pred)
	if len(conjuncts) != 1 {
		t.Fatalf("splitAnd(semiAnti[0].pred) = %d conjuncts, want exactly 1 (Predicate was nil, so only the folded-in key equality should be present): %+v", len(conjuncts), conjuncts)
	}
	eq, ok := conjuncts[0].(*BinaryOp)
	if !ok || eq.Op != parser.OpEq || eq.Left != Expr(j.LeftKey) || eq.Right != Expr(j.RightKey) {
		t.Errorf("semiAnti[0].pred's conjunct = %#v, want (LeftKey = RightKey) built from j.LeftKey/j.RightKey by pointer", conjuncts[0])
	}

	cumOffsets := buildLeafSpans(widths, semiAnti)
	if !semiAntiOnQualsOK(semiAnti, cumOffsets) {
		t.Errorf("semiAntiOnQualsOK(semiAnti, cumOffsets) = false, want true — the walk's own link must satisfy the consumer it was built to feed")
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

	scans, widths, _, _, semiAnti, ok := extractSearchLeaves(j, true)
	if !ok {
		t.Fatalf("extractSearchLeaves(j, true) ok=false, want true: %s", planString(node))
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

// TestExtractSearchLeaves_AdmitSemiAntiFalse_UnchangedFromProduction proves
// the scaffold is inert exactly as design doc §22.4 requires: with
// `admitSemiAnti=false` (the literal value the ONE production call site
// passes, joinsearchseam.go's tryPGShapedJoinSearch), the Semi join is
// treated as an ordinary opaque leaf — byte-identical to
// pre-M0142-0008a-3i-plumbing-b1 behavior — and its SJInfo is left untouched.
func TestExtractSearchLeaves_AdmitSemiAntiFalse_UnchangedFromProduction(t *testing.T) {
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

	scans, _, onQuals, outer, semiAnti, ok := extractSearchLeaves(j, false)
	if !ok {
		t.Fatalf("extractSearchLeaves(j, false) ok=false, want true (a Semi *Join must still be a valid opaque leaf)")
	}
	if len(scans) != 1 || scans[0] != Node(j) {
		t.Errorf("scans = %v, want exactly [j] (admitSemiAnti=false: the Semi join itself is one opaque leaf, no descent)", scans)
	}
	if len(onQuals) != 0 || len(outer) != 0 || len(semiAnti) != 0 {
		t.Errorf("onQuals=%v outer=%v semiAnti=%v, want all empty with admitSemiAnti=false", onQuals, outer, semiAnti)
	}
	if j.SJInfo != nil && (j.SJInfo.SynLefthand != 1 || j.SJInfo.SynRighthand != 2) {
		t.Errorf("j.SJInfo.{SynLefthand,SynRighthand} = {%#x,%#x}, want the untouched {1,2} placeholder — admitSemiAnti=false must not rebuild it", j.SJInfo.SynLefthand, j.SJInfo.SynRighthand)
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

// TestPgShapedOffsetChecksOK_ReducesToPlainChecksWhenNoSemiAnti pins design
// doc §31.3's inertness claim: with `semiAnti` empty (today's only
// production shape, since `tryPGShapedJoinSearch`'s one call site always
// passes `admitSemiAnti=false` to `extractSearchLeaves`),
// `pgShapedOffsetChecksOK` must behave exactly like the plain per-index
// comparison it replaced — both accepting a well-formed two-leaf-plus-spine
// shape and declining a mismatched one.
func TestPgShapedOffsetChecksOK_ReducesToPlainChecksWhenNoSemiAnti(t *testing.T) {
	widths := []int{2, 3} // two real leaves, no synthetic ones
	cumOffsets := buildLeafSpans(widths, nil)
	bindingOffsets := []int{0, 2} // matches cumOffsets exactly

	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, nil, widths, bindingOffsets, true, 5); !ok {
		t.Errorf("pgShapedOffsetChecksOK(...) declined (%q), want accepted — matches the old plain checks on a well-formed shape", reason)
	}
	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, nil, widths, []int{0, 99}, true, 5); ok {
		t.Errorf("pgShapedOffsetChecksOK(...) = accepted, want declined (offset-disagreement) — bindingOffsets[1] deliberately mismatches cumOffsets[1].lo")
	} else if reason != "offset-disagreement" {
		t.Errorf("declineReason = %q, want %q", reason, "offset-disagreement")
	}
	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, nil, widths, bindingOffsets, true, 99); ok {
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

	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, semiAnti, widths, bindingOffsets, false, 0); !ok {
		t.Errorf("pgShapedOffsetChecksOK(...) declined (%q), want accepted — A and C both agree with their real ctx.bindings offsets once B is skipped", reason)
	}
	// A deliberate mismatch on C's binding offset must still be caught.
	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, semiAnti, widths, []int{0, wA + 1}, false, 0); ok {
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
	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, semiAnti, widths, bindingOffsets, true, wA); !ok {
		t.Errorf("pgShapedOffsetChecksOK(...) declined (%q), want accepted — spine offset %d correctly matches the REAL total width (%d), not cumOffsets' raw last entry (%d)", reason, wA, wA, cumOffsets[len(cumOffsets)-1].hi)
	}
	// The old (buggy) comparison target, `cumOffsets[len-1].hi` = wA+wB, must
	// NOT be what this function accepts — passing it as spineOffset proves
	// the fix is live, not accidentally still comparing against the old value.
	if reason, ok := pgShapedOffsetChecksOK(cumOffsets, semiAnti, widths, bindingOffsets, true, wA+wB); ok {
		t.Errorf("pgShapedOffsetChecksOK(...) = accepted for spineOffset=%d (the OLD buggy target, cumOffsets' raw last .hi), want declined — this would mean the fix regressed back to comparing against the synthetic leaf's out-of-band span", wA+wB)
	} else if reason != "spine-offset-disagreement" {
		t.Errorf("declineReason = %q, want %q", reason, "spine-offset-disagreement")
	}
}
