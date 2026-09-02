package optimizer

import "testing"

// TestEnableJoinMethodGUCsSetDisabledNodes pins take2 P2-05.
//
// enable_hashjoin / enable_mergejoin / enable_nestloop were registered GUCs
// whose ONLY reference outside internal/utils/misc/defaults.go was the
// pg_settings view in internal/catalog: `SET enable_hashjoin = off` was
// accepted, displayed, and changed nothing. Path.DisabledNodes and the
// dominance ordering that reads it (comparePathCosts) already existed; nothing
// assigned them.
//
// The assertion is on DisabledNodes rather than on a winning plan, because PG's
// mechanism is a PREFERENCE, not a prohibition: the producer still runs, so a
// query whose only legal plan uses the disabled method still plans.
func TestEnableJoinMethodGUCsSetDisabledNodes(t *testing.T) {
	if ps := DefaultPlannerSettings(); !ps.EnableHashJoin || !ps.EnableMergeJoin || !ps.EnableNestLoop {
		t.Fatalf("all three methods must default to ENABLED, got hash=%v merge=%v nl=%v",
			ps.EnableHashJoin, ps.EnableMergeJoin, ps.EnableNestLoop)
	}
	if cp := defaultCostParams(); !cp.enableHashJoin || !cp.enableMergeJoin || !cp.enableNestLoop {
		t.Fatal("defaultCostParams must agree with DefaultPlannerSettings")
	}

	// The zero PlannerSettings would read as "everything disabled"; the
	// conversion must carry the real values, not the zero value.
	ps := DefaultPlannerSettings()
	ps.EnableHashJoin = false
	if cp := ps.costParams(); cp.enableHashJoin {
		t.Error("costParams() dropped EnableHashJoin=false")
	}
	if cp := ps.costParams(); !cp.enableMergeJoin || !cp.enableNestLoop {
		t.Error("costParams() must not disable methods the settings left enabled")
	}
}

// TestDisabledNodesAccumulatesFromChildren pins the accumulation rule: PG's
// disabled_nodes is the SUM over the subtree, so two disabled nodes below a
// path must both be counted, or a plan with one disabled node would be
// indistinguishable from a plan with several.
func TestDisabledNodesAccumulatesFromChildren(t *testing.T) {
	c1 := &Path{DisabledNodes: 1}
	c2 := &Path{DisabledNodes: 2}

	if got := disabledNodesFor(false, c1, c2); got != 3 {
		t.Errorf("an enabled node over children with 1 and 2 disabled: got %d, want 3", got)
	}
	if got := disabledNodesFor(true, c1, c2); got != 4 {
		t.Errorf("a DISABLED node over the same children: got %d, want 4", got)
	}
	if got := disabledNodesFor(true); got != 1 {
		t.Errorf("a disabled leaf: got %d, want 1", got)
	}
	if got := disabledNodesFor(false, nil, c1); got != 1 {
		t.Errorf("a nil child must be skipped, not panic: got %d, want 1", got)
	}
}
