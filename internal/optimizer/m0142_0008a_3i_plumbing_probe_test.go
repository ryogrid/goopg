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

func mustRelids(t *testing.T, e Expr, cumOffsets []leafSpan) RelSet {
	t.Helper()
	rs, ok := relidsOfExpr(e, cumOffsets)
	if !ok {
		t.Fatalf("relidsOfExpr(%v, %v) ok=false", e, cumOffsets)
	}
	return rs
}
