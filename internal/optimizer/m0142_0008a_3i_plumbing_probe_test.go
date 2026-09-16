package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0142-0008a-3i-plumbing, item 1 of design doc §14.3 (filed by
// -3i-plumbing-recon3, which found §12.4's (a)/(b)/(c) splice-based
// decomposition unbuildable and proposed admitting Semi/Anti into
// extractSearchLeaves's chain-flattening walk instead).
//
// This is a THROWAWAY probe: it copies extractSearchLeaves's walk into a
// local function extended to also admit JoinTypeSemi/JoinTypeAnti, and runs
// it against the same Q69-witness-class fixture -3i-verify already proved
// produces `j.Right = *Project{Child: *Join{Inner}}` (a *Project fails the
// type test either way, so RHS descent was already opaque — the only new
// question is what admitting the TOP Semi/Anti node itself does to the leaf
// list and to the outer-link bookkeeping). Production code
// (joinsearchseam.go) is NOT touched this loop.
func extractSearchLeavesAdmitSemiAnti(node Node) (scans []Node, widths []int, onQuals []chainOnQual, outer []outerChainLink, ok bool) {
	width := 0
	var walk func(n Node, preserved bool) (RelSet, bool)
	walk = func(n Node, preserved bool) (RelSet, bool) {
		if n == nil {
			return 0, false
		}
		j, isJoin := n.(*Join)
		if !isJoin || (j.Type != JoinTypeCross && j.Type != JoinTypeInner &&
			j.Type != JoinTypeLeft && j.Type != JoinTypeRight &&
			j.Type != JoinTypeSemi && j.Type != JoinTypeAnti) {
			scans = append(scans, n)
			widths = append(widths, len(n.Output()))
			width += len(n.Output())
			return 0, true
		}
		outerish := j.Type == JoinTypeLeft || j.Type == JoinTypeRight ||
			j.Type == JoinTypeSemi || j.Type == JoinTypeAnti
		if outerish && !preserved {
			return 0, false
		}
		base := width
		loLeft := len(scans)
		nullLeft, ok := walk(j.Left, preserved && j.Type != JoinTypeRight)
		if !ok {
			return 0, false
		}
		loRight := len(scans)
		nullRight, ok := walk(j.Right, preserved && j.Type != JoinTypeLeft)
		if !ok {
			return 0, false
		}
		hiRight := len(scans)
		below := nullLeft | nullRight
		if j.Type == JoinTypeLeft || j.Type == JoinTypeRight {
			pred := j.Predicate
			if pred != nil && base != 0 {
				shifted, okShift := rebaseChainQual(pred, base)
				if !okShift {
					return 0, false
				}
				pred = shifted
			}
			lk := outerChainLink{
				jointype:  parser.JoinLeft,
				preserved: leafRangeRelSet(loLeft, loRight),
				nullable:  leafRangeRelSet(loRight, hiRight),
				pred:      pred,
			}
			if j.Type == JoinTypeRight {
				lk.jointype, lk.preserved, lk.nullable = reduceRightLink(lk.preserved, lk.nullable)
			}
			outer = append(outer, lk)
			return below | lk.nullable, true
		}
		if j.Type == JoinTypeSemi || j.Type == JoinTypeAnti {
			// §14.3 item 2's literal reading: Semi/Anti never null-extends
			// (reresolveJoinByName:630-646 — "emit Outer (=Left) only at
			// runtime"), so `nullable` is left empty rather than set to the
			// RHS leaf range the Left/Right branch above would use.
			//
			// FINDING (this probe): on the FINAL planned tree (post join-method
			// selection), a hash-keyed Semi/Anti carries its correlation as
			// (LeftKey, RightKey), not j.Predicate (Predicate came back nil in
			// this fixture) — extractSearchLeaves's real production call site
			// (predp.go's pre-search origChain) runs BEFORE method selection, so
			// Predicate is always populated there; this reconstruction exists
			// only so the probe can exercise outerChainLink's `pred`-shaped
			// consumers against the FINAL tree §13's fixture already produces.
			pred := j.Predicate
			if pred == nil && j.LeftKey != nil && j.RightKey != nil {
				pred = &BinaryOp{Op: parser.OpEq, Left: j.LeftKey, Right: j.RightKey}
			}
			if pred != nil && base != 0 {
				shifted, okShift := rebaseChainQual(pred, base)
				if !okShift {
					return 0, false
				}
				pred = shifted
			}
			pjt := parser.JoinSemi
			if j.Type == JoinTypeAnti {
				pjt = parser.JoinAnti
			}
			lk := outerChainLink{
				jointype:  pjt,
				preserved: leafRangeRelSet(loLeft, loRight),
				nullable:  0,
				pred:      pred,
			}
			outer = append(outer, lk)
			// Semi/Anti contributes nothing to the null-extension union an
			// INNER link above it would need to see, matching the "invisible
			// RHS" contract.
			return below, true
		}
		if j.Predicate == nil {
			return below, true
		}
		if j.Type != JoinTypeInner {
			return 0, false
		}
		pred := j.Predicate
		if base != 0 {
			shifted, okShift := rebaseChainQual(pred, base)
			if !okShift {
				return 0, false
			}
			pred = shifted
		}
		onQuals = append(onQuals, chainOnQual{pred: pred, belowNullable: below})
		return below, true
	}
	if _, okWalk := walk(node, true); !okWalk {
		return nil, nil, nil, nil, false
	}
	return scans, widths, onQuals, outer, true
}

// TestM0142_0008a_3iPlumbing_AdmitSemiAnti runs the extended walk over the
// Q69-witness-class fixture and inspects (i) the flattened leaf list and
// (ii) whether the existing outerChainLink consumers choke on a link whose
// `nullable` field is empty.
func TestM0142_0008a_3iPlumbing_AdmitSemiAnti(t *testing.T) {
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
	t.Logf("j.Predicate = %#v, j.Algo = %v, j.LeftKey = %#v, j.RightKey = %#v", j.Predicate, j.Algo, j.LeftKey, j.RightKey)

	scans, widths, onQuals, outer, ok := extractSearchLeavesAdmitSemiAnti(j)
	if !ok {
		t.Fatalf("extractSearchLeavesAdmitSemiAnti(j) ok=false, want true — j.Right's own *Project top node should already stop recursion as an opaque leaf: %s", planString(node))
	}

	// Prediction (§14.3 item 1): admitting the Semi node flattens its LHS
	// (t1, one leaf) and treats its *Project-topped RHS as ONE opaque leaf,
	// same as any other non-*Join node — i.e. exactly two leaves total, not
	// a recursive descent into the RHS's own t2/t3 join.
	if len(scans) != 2 {
		t.Fatalf("scans = %d leaves (%v), want 2 (t1, and the RHS *Project as one opaque leaf) — widths=%v", len(scans), scans, widths)
	}
	if _, ok := scans[1].(*Project); !ok {
		t.Errorf("scans[1] = %T, want *Project (RHS stays opaque, no recursion into inner t2/t3)", scans[1])
	}
	if len(onQuals) != 0 {
		t.Errorf("onQuals = %v, want none — the Semi join's predicate must land in `outer`, not `onQuals` (it is not a plain INNER on-qual)", onQuals)
	}
	if len(outer) != 1 {
		t.Fatalf("outer = %d links, want exactly 1 (the Semi join itself): %+v", len(outer), outer)
	}
	lk := outer[0]
	if lk.jointype != parser.JoinSemi {
		t.Errorf("outer[0].jointype = %v, want parser.JoinSemi", lk.jointype)
	}
	if lk.nullable != 0 {
		t.Errorf("outer[0].nullable = %#x, want 0 (Semi never null-extends) — this loop's literal reading of item 2", lk.nullable)
	}
	if lk.preserved != leafRangeRelSet(0, 1) {
		t.Errorf("outer[0].preserved = %#x, want leafRangeRelSet(0,1) (t1 alone)", lk.preserved)
	}
	if lk.pred == nil {
		t.Fatalf("outer[0].pred = nil, want the correlation predicate")
	}

	// cumOffsets in the same coordinate space extractSearchLeaves's own
	// callers build it in (joinsearchseam.go:317-324): cumulative leaf
	// widths, one entry per leaf plus a trailing total.
	cumOffsets := make([]int, len(widths)+1)
	for i, w := range widths {
		cumOffsets[i+1] = cumOffsets[i] + w
	}
	t.Logf("cumOffsets = %v, pred relids = %v", cumOffsets, mustRelids(t, lk.pred, cumOffsets))

	// Consumer #1: outerOnQualsOK. It requires `relsSubset(rs, lk.preserved|lk.nullable)`
	// for every conjunct's relids. With nullable=0, preserved|nullable is
	// JUST the LHS leaf — but the correlation predicate spans LHS *and* the
	// RHS opaque leaf, so its relids are NOT a subset. Prediction: this
	// existing consumer INCORRECTLY declines a Semi link with an
	// empty-nullable encoding, even though the link and predicate are both
	// well-formed.
	if got := outerOnQualsOK(outer, cumOffsets); got {
		t.Logf("outerOnQualsOK(outer, cumOffsets) = true — reconsider: expected it to choke on the RHS-referencing predicate given nullable=0")
	} else {
		t.Logf("CONFIRMED: outerOnQualsOK(outer, cumOffsets) = false — an empty `nullable` RelSet on a Semi/Anti link makes the existing consumer decline a well-formed link because its own predicate's relids exceed preserved|nullable. A Semi/Anti-aware arm (or a non-empty nullable carrying the RHS leaf range for bookkeeping purposes only, never for actual NULL-extension) is needed before item 2 can reuse outerChainLink as-is.")
	}

	// Consumer #2: deriveOuterLinkConstants. With nullable=0, no column-ref
	// pairing can ever satisfy `relsSubset(rb, lk.nullable)` (true only for
	// rb==0), so it silently contributes nothing for a Semi/Anti link. This
	// is a no-op, not a crash — record it as a follow-on capability gap
	// instead of a correctness break.
	if got := deriveOuterLinkConstants(outer, splitAnd(lk.pred), cumOffsets); len(got) != 0 {
		t.Logf("deriveOuterLinkConstants unexpectedly derived %v from a Semi link with nullable=0 — recheck the no-op prediction", got)
	} else {
		t.Logf("CONFIRMED: deriveOuterLinkConstants(outer, ...) = nil — a Semi/Anti link with nullable=0 is a silent no-op here (not a crash), a capability gap rather than a correctness break")
	}

	// Consumer #3: problemPairsOuterWithDerived operates on []*SpecialJoinInfo
	// (not []outerChainLink), switching on sj.Jointype with an explicit
	// `case parser.JoinLeft, parser.JoinRight, parser.JoinFull: default:
	// continue` — recon2 already found this skips Semi/Anti entirely.
	// Confirm directly with a real existsUnnestSJInfo-shaped SpecialJoinInfo
	// rather than trusting the prior static read.
	sj := existsUnnestSJInfo(JoinTypeSemi, nil, []Expr{lk.pred})
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}}
	relInfos := []baseRelInfo{{}, {}}
	scansArg := []Node{scans[0], scans[1]}
	prob := &joinlistProblem{scans: scansArg, relInfos: relInfos}
	if got := problemPairsOuterWithDerived([]*SpecialJoinInfo{sj}, items, prob); got {
		t.Errorf("problemPairsOuterWithDerived([]*SpecialJoinInfo{semiSJI}, ...) = true — recon2's finding that Semi/Anti is skipped no longer holds, re-check design doc §12.2")
	} else {
		t.Logf("CONFIRMED (re-verifies recon2's static read): problemPairsOuterWithDerived declines to flag a Semi SpecialJoinInfo at all (default: continue on sj.Jointype) — the Q78 catastrophic-mis-costing firewall does not see Semi/Anti today, live-probe-confirmed rather than merely read")
	}
}

func mustRelids(t *testing.T, e Expr, cumOffsets []int) RelSet {
	t.Helper()
	rs, ok := relidsOfExpr(e, cumOffsets)
	if !ok {
		t.Fatalf("relidsOfExpr(%v, %v) ok=false", e, cumOffsets)
	}
	return rs
}
