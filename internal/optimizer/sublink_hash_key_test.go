package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestParamSubPlanEqualityIsAHashKey pins M0146-0207. In TPC-H Q2,
// `ps_supplycost = (SELECT min(ps_supplycost) ... WHERE p_partkey =
// ps_partkey ...)` reaches the search as a SubPlan in PARAM_EXEC form whose
// only Arg is part.p_partkey. Through pull_varnos, the SubPlan's relids are
// {part}, so check_hashjoinable makes the equality a hash clause between
// {part} and {partsupp}. PG prints it in the Hash Cond:
// `(SubPlan 1) = partsupp.ps_supplycost`. goopg used to keep every
// sublink-bearing equality as a residual.
func TestParamSubPlanEqualityIsAHashKey(t *testing.T) {
	// Binding 0 is part (columns 0-1), binding 1 is partsupp (columns 2-4).
	spans := []leafSpan{{0, 2}, {2, 5}}
	supplycost := &ColumnRef{Name: "ps_supplycost", Index: 4}
	body := &Aggregate{}
	body.PlanCost = PlanCost{StartupCost: 46.85, TotalCost: 46.87, PlanRows: 1, CostSet: true}
	sub := &SubqueryExpr{Plan: body, Args: []Expr{&ColumnRef{Name: "p_partkey", Index: 0}}, ParParam: []int{0}}
	eq := &BinaryOp{Op: parser.OpEq, Left: supplycost, Right: sub}

	l := buildRestrictInfos([]Expr{eq}, 0, spans)
	if len(l.all) != 1 {
		t.Fatalf("restrict infos: %d, want 1", len(l.all))
	}
	ri := l.all[0]
	if !ri.isEquijoin || !ri.subplanKey {
		t.Fatalf("PARAM_EXEC SubPlan equality: isEquijoin=%v subplanKey=%v, want both", ri.isEquijoin, ri.subplanKey)
	}
	if ri.leftRelids != relsetOf(1) || ri.rightRelids != relsetOf(0) {
		t.Fatalf("operand relids %#x / %#x, want partsupp / part", ri.leftRelids, ri.rightRelids)
	}
	keys, residual := splitJoinClauses(relsetOf(0), relsetOf(1), l.all)
	if len(keys) != 1 || len(residual) != 0 {
		t.Fatalf("split at part ⋈ partsupp: %d keys, %d residual; want the clause as the key", len(keys), len(residual))
	}

	// The hash qual cost carries the SubPlan's per-call cost once, as
	// cost_qual_eval_walker does: the comparison plus run + startup.
	cp := defaultCostParams()
	if got, want := hashClausesPerTuple(cp, keys), cp.cpuOperatorCost+46.87; math.Abs(got-want) > 1e-6 {
		t.Errorf("hash_qual_cost.per_tuple %v, want %v", got, want)
	}

	// The merge arms see it as a residual: they cannot sort on a SubPlan.
	mkeys, mres := mergeableKeys(keys, residual)
	if len(mkeys) != 0 || len(mres) != 1 || mres[0] != ri {
		t.Errorf("mergeableKeys: %d keys, %d residual; want the SubPlan key demoted", len(mkeys), len(mres))
	}
	// ...as do the parameterised index probes.
	if got := indexableJoinClausesFor(relsetOf(1), l.all); len(got) != 0 {
		t.Errorf("indexableJoinClausesFor: %d clauses, want none for a SubPlan key", len(got))
	}

	// A SubPlan that still reads its outer row through OuterColumnRef (not
	// lowered) stays a residual, as before.
	raw := &SubqueryExpr{Plan: &Filter{Child: &SeqScan{}, Predicate: &BinaryOp{Op: parser.OpEq,
		Left: &ColumnRef{Name: "ps_partkey", Index: 0}, Right: &OuterColumnRef{Name: "p_partkey", Index: 0, Level: 1}}}}
	cross := &BinaryOp{Op: parser.OpEq, Left: supplycost, Right: &BinaryOp{Op: parser.OpAdd, Left: raw, Right: &ColumnRef{Name: "p_partkey", Index: 0}}}
	if l2 := buildRestrictInfos([]Expr{cross}, 0, spans); len(l2.all) != 1 || l2.all[0].isEquijoin {
		t.Errorf("unlowered sublink equality became an equijoin")
	}
}

// TestHashKeySideReadsSubPlanArgs pins the executor-facing half: after
// planning, the published key list is rebuilt from the join predicate
// (fillOneJoinHashKeys → forEachEqualityForHash), and residualExcluding drops
// the same conjunct (conjunctIsOneOfKeys). A PARAM_EXEC SubPlan lies on the
// side of its Args. Any other inner plan still makes the operand sideMixed.
func TestHashKeySideReadsSubPlanArgs(t *testing.T) {
	const leftWidth = 2 // part's columns 0-1 are the left (probe) input
	sub := &SubqueryExpr{Plan: &SeqScan{}, Args: []Expr{&ColumnRef{Name: "p_partkey", Index: 0}}, ParParam: []int{0}}
	supplycost := &ColumnRef{Name: "ps_supplycost", Index: 4}
	if s := hashKeyExprSide(sub, leftWidth); s != sideLeft {
		t.Fatalf("SubPlan with a left Arg: side %v, want left", s)
	}
	if s := exprSide(sub, leftWidth); s != sideMixed {
		t.Fatalf("exprSide is unchanged for other callers: %v, want mixed", s)
	}
	pred := combineAnd([]Expr{
		&BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Name: "p_partkey", Index: 0}, Right: &ColumnRef{Name: "ps_partkey", Index: 2}},
		&BinaryOp{Op: parser.OpEq, Left: supplycost, Right: sub},
	})
	pairs := splitAllEqualitiesForHash(pred, leftWidth)
	if len(pairs) != 2 || pairs[1].Left != sub || pairs[1].Right != supplycost {
		t.Fatalf("key pairs %+v, want the SubPlan pair oriented SubPlan (left) = ps_supplycost (right)", pairs)
	}
	j := &Join{Predicate: pred, Left: &SeqScan{schema: Schema{{Name: "p_partkey"}, {Name: "p_mfgr"}}}, HashKeys: pairs}
	if res := j.residualExcluding(pairs); res != nil {
		t.Fatalf("residual %v, want none once both pairs are keys", res)
	}

	// A SubPlan with no Args (an InitPlan) has no side.
	initPlan := &SubqueryExpr{Plan: &SeqScan{}}
	if s := hashKeyExprSide(initPlan, leftWidth); s == sideLeft || s == sideRight {
		t.Fatalf("InitPlan: side %v, want neither", s)
	}
}
