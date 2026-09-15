package optimizer

import "testing"

// M0137-0016 (O15, ledger row
// m0137-0012-o15-gather-crossing-excluded-pushconjuncttraced): pushConjunctTraced
// had descent cases for *Filter, *Project and *Join only. A *Gather in the
// descent path fell through to the terminal-target check and declined,
// silently keeping a PG-placeable restriction as a post-parallel residual —
// the same defect class as R56's Q78 loss, invisible to a values-only sweep.

// TestPushdownCrossesGatherToTheCorrectLeaf pins the fix: Join{ Gather{a}, b }
// — a conjunct naming a must cross the Gather and land on the `a` scan
// beneath it, exactly as it already crosses a Project.
func TestPushdownCrossesGatherToTheCorrectLeaf(t *testing.T) {
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeInner, NewGather(0, a, 2), b)

	repl, ok := pushConjunctIntoSubtree(j, srcEq(0, "x", 1, 7))
	if !ok {
		t.Fatal("a conjunct naming the gathered side must descend through the Gather")
	}
	gather, isGather := repl.(*Join).Left.(*Gather)
	if !isGather {
		t.Fatalf("Gather must be preserved above the pushed filter, got %T", repl.(*Join).Left)
	}
	if _, hasFilter := gather.Child.(*Filter); !hasFilter {
		t.Errorf("filter must land BELOW the Gather, on the scan; got %T", gather.Child)
	}
}

// TestPushdownThroughGatherIsCopyOnlyNotAMove mirrors the Project pin: a
// Gather changes no coordinate space (Output() is exactly Child.Output(),
// per NewGather), but it is still not a proof carrier here — st.proven is
// left at whatever the deeper descent decided, so this only pins that the
// descent SUCCEEDS and does not itself corrupt the proof.
func TestPushdownThroughGatherIsCopyOnlyNotAMove(t *testing.T) {
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeInner, NewGather(0, a, 2), b)

	_, ok, tr := pushConjunctIntoSubtreeTraced(j, srcEq(0, "x", 1, 7))
	if !ok {
		t.Fatal("descent must succeed")
	}
	// srcEq's SourceTableIdx=1 matches `a`, the side being descended into,
	// and the INNER link is fully attributed — so the full-path proof
	// still holds crossing the Gather (unlike the Project arm, which
	// deliberately clears it because its remap has an unnamed-ref
	// fail-open seam that a Gather pass-through does not share).
	if !tr.proven {
		t.Error("crossing a Gather must not itself clear `proven`: it changes no coordinate space")
	}
}

// TestPushdownDescendsGatherThenJoinSpine pins that the Gather arm composes
// with the existing multi-level descent (TestInnerJoinQualPushDescendsJoinSpine's
// sibling): a conjunct must reach a leaf two levels below a Gather sitting
// partway down the spine, not just a Gather directly under the residual
// Filter's Join.
func TestPushdownDescendsGatherThenJoinSpine(t *testing.T) {
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	c := srcScan("c", srcCol("z", 3))
	inner := srcJoin(JoinTypeInner, NewGather(0, a, 2), b)
	f := &Filter{Child: srcJoin(JoinTypeInner, inner, c), Predicate: srcEq(0, "x", 1, 7)}

	pushInnerJoinInputQuals(f)

	upper, ok := f.Child.(*Join)
	if !ok {
		t.Fatalf("f.Child is %T, want the upper *Join", f.Child)
	}
	lower, ok := upper.Left.(*Join)
	if !ok {
		t.Fatalf("upper.Left is %T, want the lower *Join", upper.Left)
	}
	gather, ok := lower.Left.(*Gather)
	if !ok {
		t.Fatalf("lower.Left is %T, want *Gather (untouched above the pushed filter)", lower.Left)
	}
	if _, hasFilter := gather.Child.(*Filter); !hasFilter {
		t.Errorf("filter must land below the Gather on the `a` scan; got %T", gather.Child)
	}
}
