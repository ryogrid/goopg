package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestBuildWalkerAcceptsOnlyWhatCanBeWired is a DIFFERENTIAL test over the two
// walkers that decide parallel-hash-build eligibility:
//
//   - extractSeqScanFromPlan, at the plan level, decides whether to try;
//   - attachParallelScan, at the operator level, actually wires the shared
//     block allocator into the driving scan.
//
// The invariant is ONE-DIRECTIONAL, and that is the point. Everything the plan
// walker accepts must be wirable, or the build fails at run time. The converse
// is NOT required: attachParallelScan also descends aggregateOp, sortOp and a
// joinOp's probe side, which are safe under a Gather (the planner supplies
// Partial/Finalize and Gather Merge) but NOT under the cooperative hash build,
// which supplies neither. See extractSeqScanFromPlan's comment.
//
// An earlier version of this test asserted a hardcoded expectation table and
// never called attachParallelScan at all — it would have stayed green if the
// projectOp arm were deleted, which is precisely the drift it is named for.
func TestBuildWalkerAcceptsOnlyWhatCanBeWired(t *testing.T) {
	tbl := &catalog.Table{Name: "t"}
	scan := func() *optimizer.SeqScan { return &optimizer.SeqScan{Table: tbl} }

	accepted := []struct {
		name string
		node optimizer.Node
	}{
		{"bare SeqScan", scan()},
		{"Filter -> SeqScan", &optimizer.Filter{Child: scan()}},
		{"Filter -> Filter -> SeqScan", &optimizer.Filter{Child: &optimizer.Filter{Child: scan()}}},
		{"Project -> SeqScan", &optimizer.Project{Child: scan()}},
		{"Project -> Filter -> SeqScan", &optimizer.Project{Child: &optimizer.Filter{Child: scan()}}},
		{"Filter -> Project -> SeqScan", &optimizer.Filter{Child: &optimizer.Project{Child: scan()}}},
	}
	for _, tc := range accepted {
		t.Run("wirable/"+tc.name, func(t *testing.T) {
			if extractSeqScanFromPlan(tc.node) == nil {
				t.Fatalf("plan walker rejected %s; expected it to accept", tc.name)
			}
			// The load-bearing half: what the plan walker accepted, the
			// operator walker must be able to wire.
			tree, err := BuildWorker(tc.node)
			if err != nil {
				t.Fatalf("BuildWorker: %v", err)
			}
			if !attachParallelScan(tree, newParallelScanState(0)) {
				t.Errorf("plan walker ACCEPTED %s but attachParallelScan could not "+
					"wire it — eligibility would say yes and the build would fail "+
					"at run time", tc.name)
			}
		})
	}

	// Shapes the plan walker must refuse. Aggregate is the safety-critical one:
	// attachParallelScan WOULD descend it, and doing so from the cooperative
	// build (which has no Finalize) yields wrong rows for HAVING sum(...).
	refused := []struct {
		name string
		node optimizer.Node
	}{
		{"Join", &optimizer.Join{Left: scan(), Right: scan()}},
		{"Filter -> Join", &optimizer.Filter{Child: &optimizer.Join{Left: scan(), Right: scan()}}},
		{"nil", nil},
	}
	for _, tc := range refused {
		t.Run("refused/"+tc.name, func(t *testing.T) {
			if extractSeqScanFromPlan(tc.node) != nil {
				t.Errorf("plan walker accepted %s; it must refuse shapes whose "+
					"parallel-safety the cooperative build cannot guarantee", tc.name)
			}
		})
	}
}
