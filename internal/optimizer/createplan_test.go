package optimizer

import "testing"

// Phase C0.2 — create_plan seam. In C0 create_plan is an identity over the DP's
// chosen subtree (it is carried whole in a PathPrebuilt), so these tests pin that
// the seam is plan-preserving: the Node in equals the Node out, by pointer.

func TestCreatePlan_PrebuiltReturnsWrappedNode(t *testing.T) {
	n := &SeqScan{}
	got := createPlan(newPrebuiltPath(&RelOptInfo{}, n))
	if got != Node(n) {
		t.Fatalf("createPlan(PathPrebuilt) must return the wrapped node unchanged")
	}
}

// M0137-0015: buildInitialRels wraps a base-local-filtered leaf in
// Filter{Child: SeqScan} and prices the WHOLE wrapper (baseSeqScanCostInputs
// / costSeqscan), so createPlanNode's stampPlanCost call must land on the
// *Filter it returns, not be silently dropped by a missing planCostSetter
// arm (M0137-0011 root-caused exactly this: Q12's EXPLAIN rendered
// 60,299.79 — DeriveLegacyDisplayCost's fallback — while the search had
// actually costed 271,421.24).
func TestCreatePlanNode_StampsCostOnFilterWrappedPrebuiltLeaf(t *testing.T) {
	scan := &SeqScan{}
	filter := &Filter{Child: scan, Predicate: &BooleanConst{Value: true}}
	rel := &RelOptInfo{Relids: 1, Rows: 5}
	p := newPrebuiltPath(rel, filter)
	p.Cost = Cost{Startup: 12.5, Total: 271421.24}

	node, _ := createPlanNode(p)
	got, ok := node.(*Filter)
	if !ok {
		t.Fatalf("createPlanNode(PathPrebuilt) = %T, want *Filter", node)
	}
	pc, set := got.PlanCostInfo()
	if !set {
		t.Fatalf("Filter did not carry the path's cost — stampPlanCost's planCostSetter assertion failed silently again")
	}
	if pc.TotalCost != p.Cost.Total || pc.StartupCost != p.Cost.Startup {
		t.Errorf("Filter.PlanCostInfo() = %+v, want startup=%v total=%v", pc, p.Cost.Startup, p.Cost.Total)
	}
	// The child SeqScan is the one PostgreSQL would price on its own
	// (baserestrictinfo lives on the rel, not a wrapper node), so it is
	// legitimately left unstamped — only the Filter (the actually-produced
	// node) must carry the cost.
	if _, childSet := scan.PlanCostInfo(); childSet {
		t.Errorf("unexpected: the wrapped SeqScan child carries a cost too; only the Filter should")
	}
}

func TestCreatePlanFromDPChoice_IsIdentity(t *testing.T) {
	n := &Join{}
	if got := createPlanFromDPChoice(n); got != Node(n) {
		t.Fatalf("createPlanFromDPChoice must be an identity transform in C0")
	}
	if got := createPlanFromDPChoice(nil); got != nil {
		t.Fatalf("createPlanFromDPChoice(nil) must be nil")
	}
}

func TestCreatePlan_UnhandledKindPanics(t *testing.T) {
	// A path kind that path generation does not yet construct must fail loudly if
	// it ever reaches create_plan, so the phase that adds it cannot silently
	// mis-build.
	defer func() {
		if recover() == nil {
			t.Fatalf("createPlan on an unhandled kind should panic")
		}
	}()
	createPlan(&Path{Kind: PathHashJoin})
}
