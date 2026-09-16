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
	cumOffsets := make([]int, len(widths)+1)
	for i, w := range widths {
		cumOffsets[i+1] = cumOffsets[i] + w
	}

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
	cumOffsets := []int{0, 1, 2}
	if semiAntiOnQualsOK([]semiAntiChainLink{link}, cumOffsets) {
		t.Errorf("semiAntiOnQualsOK(single-side conjunct) = true, want false")
	}
}

func TestSemiAntiOnQualsOK_DeclinesNilPredicate(t *testing.T) {
	link := semiAntiChainLink{jointype: parser.JoinSemi, lhs: 1, rhs: 2, pred: nil}
	if semiAntiOnQualsOK([]semiAntiChainLink{link}, []int{0, 1, 2}) {
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

	sj := existsUnnestSJInfo(JoinTypeSemi, nil, []Expr{pred})
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
