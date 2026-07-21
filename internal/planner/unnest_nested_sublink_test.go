package planner

// R2-2 (bundle phase S4b / D3.3): nested-sublink tolerance for EXISTS
// pull-up. Before this stage, canUnnestExistsExpr bailed wholesale on
// ANY nested sublink; now a nested sublink whose references stay inside
// the body rides into the semi/anti build side as an ordinary SubPlan,
// while a reference that reaches PAST the body still bails — the deep
// escape check in collectUnnestParamsAndResiduals is what tells the two
// apart (Level > planDepth = escaping).
//
// Why the escape check matters (the F7 hazard these tests pin): after a
// pull-up the outer query's row is no longer on the executor's
// OuterRows stack when the body runs as a join input, so an escaping
// ref would silently resolve against whatever occupies that slot — the
// grandparent-aliasing wrong-results failure mode, with no error.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// nestedRefFixture hand-builds a body plan carrying a nested sublink
// whose inner plan hides an OuterColumnRef at the given Level:
//
//	Filter{ pred: (z = InExpr{Plan: Filter{y = OuterColumnRef{Level:L}}(SeqScan t2)}) }(SeqScan t2)
func nestedRefFixture(level int) (Node, *OuterColumnRef, *InExpr) {
	hidden := &OuterColumnRef{Level: level, Index: 0, Name: "x"}
	nestedInner := &Filter{
		Predicate: &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0, Name: "y"}, Right: hidden},
		Child:     &SeqScan{},
	}
	in := &InExpr{Operand: &ColumnRef{Index: 1, Name: "z"}, Plan: nestedInner}
	body := &Filter{Predicate: in, Child: &SeqScan{}}
	return body, hidden, in
}

// TestWalkPlanExprsDeepFindsNestedLevel2 pins the deep walker's
// contract: an OuterColumnRef hidden inside a nested sublink's plan is
// reported at planDepth 1, while the shallow walker — the negative
// control, and the reason this hazard existed — never sees it at all.
func TestWalkPlanExprsDeepFindsNestedLevel2(t *testing.T) {
	body, hidden, _ := nestedRefFixture(2)

	foundDepth := -1
	walkPlanExprsDeep(body, 0, func(e Expr, depth int) {
		if e == hidden {
			foundDepth = depth
		}
	})
	if foundDepth != 1 {
		t.Fatalf("deep walker reported the nested Level-2 ref at depth %d, want 1", foundDepth)
	}

	// Negative control: the shallow walker treats the sublink as a
	// leaf, so the hidden ref is invisible — exactly why the blanket
	// nested-sublink bail was the only safe pre-D3.3 behaviour.
	seenShallow := false
	walkPlanExprs(body, func(e Expr) {
		if e == hidden {
			seenShallow = true
		}
	})
	if seenShallow {
		t.Fatal("shallow walker unexpectedly descended into the nested sublink plan; the deep walker is redundant")
	}
}

// TestDeepWalkerVisitsInOperand pins the other blind spot: an IN's
// Operand evaluates in the HOST scope but walkExprTree treats the whole
// InExpr as a leaf. The deep walker must report operand refs at the
// sublink's own depth.
func TestDeepWalkerVisitsInOperand(t *testing.T) {
	hidden := &OuterColumnRef{Level: 1, Index: 0, Name: "x"}
	in := &InExpr{Operand: hidden, Plan: &Filter{Predicate: &BooleanConst{Value: true}, Child: &SeqScan{}}}
	body := &Filter{Predicate: in, Child: &SeqScan{}}

	foundDepth := -1
	walkPlanExprsDeep(body, 0, func(e Expr, depth int) {
		if e == hidden {
			foundDepth = depth
		}
	})
	if foundDepth != 0 {
		t.Fatalf("operand-position ref reported at depth %d, want 0 (host scope)", foundDepth)
	}
}

// TestNestedSublinkLevel1Unnests: a nested sublink correlated only to
// the EXISTS body (Level 1 at planDepth 1) no longer blocks the
// pull-up — the EXISTS becomes a semi join and the nested sublink rides
// inside its build side.
func TestNestedSublinkLevel1Unnests(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (" +
		"SELECT 1 FROM t2 WHERE t2.y = t1.x AND t2.z IN (" +
		"SELECT z FROM t2 x2 WHERE x2.y = t2.y))"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if hasSemiOrAntiJoin(node) == nil {
		t.Fatalf("EXISTS with a body-correlated nested sublink did not unnest:\n%s", planString(node))
	}
	if findExistsExpr(node) != nil {
		t.Fatalf("ExistsExpr survived alongside the semi join:\n%s", planString(node))
	}
}

// TestNestedSublinkLevel2Bails: the nested sublink references the TRUE
// outer query (t1, Level 2 seen from inside it) — the pull-up must
// bail, because after unnesting the body would run as a join input with
// t1's row no longer on the OuterRows stack.
func TestNestedSublinkLevel2Bails(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (" +
		"SELECT 1 FROM t2 WHERE t2.y = t1.x AND t2.z IN (" +
		"SELECT z FROM t2 x2 WHERE x2.y = t1.x))"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findExistsExpr(node) == nil {
		t.Fatalf("escaping-ref EXISTS disappeared; it must stay a SubPlan:\n%s", planString(node))
	}
	if j := hasSemiOrAntiJoin(node); j != nil {
		t.Fatalf("escaping-ref EXISTS was pulled up into a %v join:\n%s", j.Type, planString(node))
	}
}

// TestOperandHiddenOuterRefBails: the correlation hides inside a nested
// IN's Operand — a host-scope position the shallow walkers treat as a
// leaf. The deep accounting must flag it unaccounted and keep the
// EXISTS a SubPlan (previously, on the IN/scalar paths, such refs
// slipped through and the pull-up proceeded over a dangling reference).
func TestOperandHiddenOuterRefBails(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (" +
		"SELECT 1 FROM t2 WHERE t2.y = t1.x AND t1.x IN (" +
		"SELECT z FROM t2 x2))"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	if findExistsExpr(node) == nil {
		t.Fatalf("operand-hidden-ref EXISTS disappeared; it must stay a SubPlan:\n%s", planString(node))
	}
}

// TestNestedSublinkCloneNotShared pins the F7 fix at the clone layer:
// cloning a body that carries a nested sublink must deep-copy the
// sublink node AND its inner plan, so a later in-place mutation of the
// clone cannot leak into the original tree (pre-fix, cloneExprLeaf's
// default arm returned the sublink by shared pointer).
func TestNestedSublinkCloneNotShared(t *testing.T) {
	body, _, origIn := nestedRefFixture(1)

	cloned, err := clonePlanReplacingOuter(body, map[*OuterColumnRef]*ColumnRef{})
	if err != nil {
		t.Fatal(err)
	}
	clonedFilter, ok := cloned.(*Filter)
	if !ok {
		t.Fatalf("clone root is %T, want *Filter", cloned)
	}
	clonedIn, ok := clonedFilter.Predicate.(*InExpr)
	if !ok {
		t.Fatalf("clone predicate is %T, want *InExpr", clonedFilter.Predicate)
	}
	if clonedIn == origIn {
		t.Fatal("nested sublink node is SHARED between clone and original")
	}
	if clonedIn.Plan == origIn.Plan {
		t.Fatal("nested sublink inner plan is SHARED between clone and original (the F7 aliasing trap)")
	}

	// Mutate the clone's nested plan; the original must be unaffected.
	clonedNestedFilter := clonedIn.Plan.(*Filter)
	clonedNestedFilter.Predicate = &BooleanConst{Value: false}
	origNestedFilter := origIn.Plan.(*Filter)
	if _, isBool := origNestedFilter.Predicate.(*BooleanConst); isBool {
		t.Fatal("mutating the clone's nested plan leaked into the original tree")
	}
}

// TestLevel2EquijoinParamNotHarvested pins the params-side twin of the
// escape check: a Level-2 ref must never be accepted as a decorrelation
// key (extractEquijoinPair / harvestIndexKeyParams Level guards). The
// driver's recursive descent into an un-unnestable body would otherwise
// pull the nested sublink up INSIDE the body, keyed against the wrong
// schema.
func TestLevel2EquijoinParamNotHarvested(t *testing.T) {
	hidden := &OuterColumnRef{Level: 2, Index: 0, Name: "x"}
	if o, _ := extractEquijoinPair(hidden, &ColumnRef{Index: 0, Name: "y"}); o != nil {
		t.Fatal("extractEquijoinPair accepted a Level-2 ref as a decorrelation key")
	}
	if o, _ := extractEquijoinPair(&ColumnRef{Index: 0, Name: "y"}, hidden); o != nil {
		t.Fatal("extractEquijoinPair (swapped) accepted a Level-2 ref as a decorrelation key")
	}
}
