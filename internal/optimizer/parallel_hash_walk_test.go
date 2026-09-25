package optimizer

import "testing"

// TestParallelHashJoinsInDescendsLikeTheClaimWalks: the Gather registers a
// Parallel Hash join's shared build state only if this walk finds it, and a
// join it misses fails loudly at execution. TPC-H Q12 found the first gap: a
// Parallel Hash below a Partial Aggregate under the Gather was never
// registered. The walk must descend every node the claim walks run.
func TestParallelHashJoinsInDescendsLikeTheClaimWalks(t *testing.T) {
	scan := func() *SeqScan { return &SeqScan{} }
	ph := &Join{Type: JoinTypeInner, Algo: JoinAlgoHash, ParallelHash: true, Left: scan(), Right: scan()}
	for name, root := range map[string]Node{
		"bare":            ph,
		"under aggregate": &Aggregate{Child: ph, Mode: AggModePartial},
		"under sort":      &Sort{Child: ph},
		"under filter":    &Filter{Child: ph},
	} {
		got := ParallelHashJoinsIn(root)
		if len(got) != 1 || got[0] != ph {
			t.Errorf("%s: found %d Parallel Hash joins, want the one", name, len(got))
		}
	}
	plain := &Join{Type: JoinTypeInner, Algo: JoinAlgoHash, Left: scan(), Right: scan()}
	if got := ParallelHashJoinsIn(&Aggregate{Child: plain}); len(got) != 0 {
		t.Errorf("an ordinary hash join was reported as Parallel Hash")
	}
}
