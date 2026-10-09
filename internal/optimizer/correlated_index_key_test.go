package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestNormalizeColumnIndexKeyAdmitsOuterRefs pins the operand half of PG's
// match_clause_to_indexcol that M0146-0015a ported: a correlated outer
// reference (OuterColumnRef / ExecParamRef) is a pseudo-constant index key in
// either operand order, while the shared normalizeColumnConst keeps its
// literal-only contract for the selectivity code.
func TestNormalizeColumnIndexKeyAdmitsOuterRefs(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	col := &ColumnRef{Index: 0, Name: "k", Type: int4}
	outer := &OuterColumnRef{Level: 1, Index: 2, Name: "o", Type: int4}
	param := &ExecParamRef{ID: 0, Type: int4}
	for name, tc := range map[string]struct {
		l, r       Expr
		colOnRight bool
	}{
		"col = outer":  {col, outer, false},
		"outer = col":  {outer, col, true},
		"col = $param": {col, param, false},
		"col = 5":      {col, &IntegerConst{Value: 5}, false},
	} {
		cr, key, onRight, ok := normalizeColumnIndexKey(tc.l, tc.r)
		if !ok || cr != col || onRight != tc.colOnRight || (key != tc.l && key != tc.r) {
			t.Errorf("%s: got (%v, %T, %v, %v)", name, cr, key, onRight, ok)
		}
	}
	if _, _, ok := normalizeColumnConst(col, outer); ok {
		t.Error("normalizeColumnConst must stay literal-only")
	}
	if _, _, _, ok := normalizeColumnIndexKey(col, &ColumnRef{Index: 1, Name: "x"}); ok {
		t.Error("a same-scope column is not a pseudo-constant key")
	}

	// The probe encodes the key uncast, so an outer key must carry the
	// column's own type; an int8 outer value against an int4 column stays a
	// filter.
	c := catalog.Column{Name: "k", Type: int4}
	if !restrictionKeyUsable(nil, c, outer) {
		t.Error("a same-typed outer key must be usable")
	}
	if restrictionKeyUsable(nil, c, &OuterColumnRef{Level: 1, Type: catalog.Type{Name: "int8"}}) {
		t.Error("a cross-typed outer key must decline")
	}
}

// TestCorrelationOnUnliftableJoinSide pins the unnest collectors' guard: a
// correlated reference under a semi/anti join's RHS or an outer join's
// nullable side cannot be hoisted to the unnested join, so both collectors
// decline and the sublink stays a SubPlan.
func TestCorrelationOnUnliftableJoinSide(t *testing.T) {
	corr := func() Node {
		return &Filter{Child: &SeqScan{}, Predicate: &BinaryOp{Op: parser.OpEq,
			Left: &ColumnRef{Index: 0, Name: "c"}, Right: &OuterColumnRef{Level: 1, Index: 0, Name: "o"}}}
	}
	for name, tc := range map[string]struct {
		n    Node
		want bool
	}{
		"inner join":            {&Join{Type: JoinTypeInner, Left: &SeqScan{}, Right: corr()}, false},
		"semi join RHS":         {&Join{Type: JoinTypeSemi, Left: &SeqScan{}, Right: corr()}, true},
		"semi join LHS":         {&Join{Type: JoinTypeSemi, Left: corr(), Right: &SeqScan{}}, false},
		"anti join RHS":         {&Join{Type: JoinTypeAnti, Left: &SeqScan{}, Right: corr()}, true},
		"left join nullable":    {&Join{Type: JoinTypeLeft, Left: &SeqScan{}, Right: corr()}, true},
		"right join nullable":   {&Join{Type: JoinTypeRight, Left: corr(), Right: &SeqScan{}}, true},
		"full join":             {&Join{Type: JoinTypeFull, Left: corr(), Right: &SeqScan{}}, true},
		"under aggregate":       {&Aggregate{Child: &Join{Type: JoinTypeSemi, Left: &SeqScan{}, Right: corr()}}, true},
		"no join":               {corr(), false},
		"left join ON has corr": {&Join{Type: JoinTypeLeft, Left: &SeqScan{}, Right: &SeqScan{}, Predicate: &OuterColumnRef{Level: 1}}, true},
	} {
		if got := correlationOnUnliftableJoinSide(tc.n); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
	if collectUnnestParamsAndResiduals(&Join{Type: JoinTypeSemi, Left: &SeqScan{}, Right: corr()}) != nil {
		t.Error("collectUnnestParamsAndResiduals must decline a correlated semi RHS")
	}
	if collectUnnestParams(&Join{Type: JoinTypeSemi, Left: &SeqScan{}, Right: corr()}) != nil {
		t.Error("collectUnnestParams must decline a correlated semi RHS")
	}
}

// TestPartitionAdmitsOuterRefsInOneRelationScope pins the bound on
// M0146-0015a's admission: in a one-relation scope a correlated conjunct is a
// base restriction of that relation (PG's distribute_qual_to_rels), so the
// index producers can bind it; in a multi-relation scope it stays in the join
// residual, where the post-planning EXISTS→ANY / unnest passes read it
// (ledgered). conjunctIsLocalEligible's own contract is unchanged.
func TestPartitionAdmitsOuterRefsInOneRelationScope(t *testing.T) {
	corr := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0, Name: "k"},
		Right: &OuterColumnRef{Level: 1, Index: 0, Name: "o"}}
	join, locals := partitionConjunctsForJoinPlanning([]Expr{corr}, []leafSpan{{lo: 0, hi: 2}})
	if len(join) != 0 || len(locals.byBinding[0]) != 1 {
		t.Errorf("one-relation scope: join=%d locals=%d, want the conjunct local", len(join), len(locals.byBinding[0]))
	}
	join, locals = partitionConjunctsForJoinPlanning([]Expr{corr}, []leafSpan{{lo: 0, hi: 2}, {lo: 2, hi: 4}})
	if len(join) != 1 || len(locals.byBinding) != 0 {
		t.Errorf("multi-relation scope: join=%d locals=%d, want the conjunct in the residual", len(join), len(locals.byBinding))
	}
	if conjunctIsLocalEligible(corr) {
		t.Error("conjunctIsLocalEligible must keep declining an OuterColumnRef")
	}
}
