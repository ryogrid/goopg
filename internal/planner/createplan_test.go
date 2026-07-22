package planner

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
