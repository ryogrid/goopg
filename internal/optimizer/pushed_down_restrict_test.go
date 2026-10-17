package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestTopDelayedConjunctsClosesOverOuterJoins pins M0146-0224 slice 3's
// placement: a held WHERE conjunct is a pushed-down restriction of the
// search's top joinrel when closing its relids over every outer join whose
// nullable side it reads (check_outerjoin_delay) covers every leaf. TPC-DS
// Q78's `coalesce(ws_qty,0) > 0 or coalesce(cs_qty,0) > 0` reads both legs of
// `ss LEFT JOIN ws LEFT JOIN cs`; a conjunct on ws alone completes only the
// first outer join, so it is not placed at the top.
func TestTopDelayedConjunctsClosesOverOuterJoins(t *testing.T) {
	spans := []leafSpan{{0, 2}, {2, 4}, {4, 6}}
	links := []outerChainLink{
		{jointype: parser.JoinLeft, preserved: relsetOf(0), nullable: relsetOf(1)},
		{jointype: parser.JoinLeft, preserved: relsetOf(0, 1), nullable: relsetOf(2)},
	}
	gt := func(idx int) Expr {
		return &BinaryOp{Op: parser.OpGt, Left: &ColumnRef{Index: idx, Name: "q"}, Right: &IntegerConst{Value: 0}}
	}
	both := &BinaryOp{Op: parser.OpOr, Left: gt(3), Right: gt(5)}
	wsOnly := gt(3)
	got := topDelayedConjuncts([]Expr{both, wsOnly}, links, spans, relsetOf(0, 1, 2))
	if len(got) != 1 || got[0] != Expr(both) {
		t.Fatalf("only the conjunct reading both nullable legs is top-delayed, got %d", len(got))
	}
	if got := topDelayedConjuncts([]Expr{both}, nil, spans, relsetOf(0, 1, 2)); got != nil {
		t.Fatalf("no outer links: nothing is delayed, got %d", len(got))
	}
}

// TestPushedDownRestrictionIsNeverAJoinKey pins the clause split: a
// pushed-down equality stays in the residual (PG's hash and merge clause
// selection skip RINFO_IS_PUSHED_DOWN at an outer join), and joinQualsOnly
// hands the unique-inner and semi/anti tests the join quals alone.
func TestPushedDownRestrictionIsNeverAJoinKey(t *testing.T) {
	a := &ColumnRef{Index: 0, Name: "a"}
	b := &ColumnRef{Index: 1, Name: "b"}
	eq := func(pushed bool) *restrictInfo {
		return &restrictInfo{clause: &BinaryOp{Op: parser.OpEq, Left: a, Right: b}, relids: relsetOf(0, 1),
			isEquijoin: true, leftKey: a, rightKey: b, leftRelids: relsetOf(0), rightRelids: relsetOf(1),
			ecID: noEquivClass, pushedDown: pushed}
	}
	join, pushed := eq(false), eq(true)
	keys, residual := splitJoinClauses(relsetOf(0), relsetOf(1), []*restrictInfo{join, pushed})
	if len(keys) != 1 || keys[0] != join || len(residual) != 1 || residual[0] != pushed {
		t.Fatalf("keys=%d residual=%d: the pushed-down equality must stay in the residual", len(keys), len(residual))
	}
	if q := joinQualsOnly([]*restrictInfo{join, pushed}); len(q) != 1 || q[0] != join {
		t.Fatalf("joinQualsOnly kept %d clauses, want the join qual alone", len(q))
	}
}

// TestOrRangeArmDefaultsToIneqSel pins the OR-arm fallback: an inequality
// with no measurement is DEFAULT_INEQ_SEL (scalarineqsel / scalarltjoinsel),
// not the unhandled-clause 0.5 — Q78's `coalesce(ws_qty, 0) > 0` arms.
func TestOrRangeArmDefaultsToIneqSel(t *testing.T) {
	s := &searchCtx{}
	arm := &BinaryOp{Op: parser.OpGt,
		Left:  &FuncCall{Name: "coalesce", Args: []Expr{&ColumnRef{Index: 0, Name: "q"}, &IntegerConst{Value: 0}}},
		Right: &IntegerConst{Value: 0}}
	if sel, def := s.orRangeSelectivity(arm); sel != defaultIneqJoinSel || !def {
		t.Fatalf("unmeasured inequality arm: got %v (default=%v), want %v", sel, def, defaultIneqJoinSel)
	}
}
