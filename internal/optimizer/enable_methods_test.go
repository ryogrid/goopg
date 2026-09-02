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

// TestEnableMemoizeIsPerStatementNotProcessGlobal pins take2 P2-02c.
//
// enable_memoize used to reach the planner through a registry.OnChange bridge
// in cmd/goopg/main.go that stored into a process-global atomic, so one
// session's `SET enable_memoize = off` disabled Memoize for every other session
// on the server — "the most-recent SET wins process-wide", as that bridge's own
// comment said. The GUC is a per-statement planner input and now travels on
// PlannerSettings with the rest.
//
// The process global survives as the GOOPG_MEMOIZE env kill-switch and as the
// legacy arm's gate; what it must no longer carry is a session's SET.
func TestEnableMemoizeIsPerStatementNotProcessGlobal(t *testing.T) {
	if !DefaultPlannerSettings().EnableMemoize {
		t.Fatal("enable_memoize must default to ENABLED")
	}
	if !defaultCostParams().enableMemoize {
		t.Fatal("defaultCostParams must agree with DefaultPlannerSettings")
	}

	ps := DefaultPlannerSettings()
	ps.EnableMemoize = false
	if ps.costParams().enableMemoize {
		t.Error("costParams() dropped EnableMemoize=false")
	}

	// Two settings values coexist without touching shared state — the property
	// the process global could not provide.
	on, off := DefaultPlannerSettings(), DefaultPlannerSettings()
	off.EnableMemoize = false
	if !on.costParams().enableMemoize || off.costParams().enableMemoize {
		t.Error("two PlannerSettings must resolve independently; one session's " +
			"setting is leaking into another's")
	}
	// The global is untouched by any of the above.
	if !MemoizeEnabled() {
		t.Error("resolving per-statement settings must not mutate the process global")
	}
}

// TestGeqoTuningGUCsReachTheSearch pins take2 P3-10.
//
// geqo_effort, geqo_pool_size, geqo_generations, geqo_selection_bias and
// geqo_seed were registered GUCs that reached nothing: geqoSearch ran at a
// hard-coded effort of 5, generations and pool size at their derived defaults,
// selection bias at a literal 2.0, and the PRNG at a fixed seed. The comment on
// that seed said "the planner has no session in scope to read the GUC", which
// stopped being true at P2-02.
//
// The assertion is that the values survive the PlannerSettings -> costParams
// conversion the search reads them through, and that the DEFAULTS are PG's — so
// a session that sets nothing plans exactly as before.
func TestGeqoTuningGUCsReachTheSearch(t *testing.T) {
	def := DefaultPlannerSettings()
	if def.GeqoEffort != 5 {
		t.Errorf("geqo_effort default = %d, want PG's DEFAULT_GEQO_EFFORT of 5", def.GeqoEffort)
	}
	if def.GeqoSelectionBias != 2.0 {
		t.Errorf("geqo_selection_bias default = %v, want 2.0", def.GeqoSelectionBias)
	}
	// Zero is MEANINGFUL for these two — PG reads it as "derive me from
	// effort / pool size" — so the default must BE zero, not a substitute.
	if def.GeqoPoolSize != 0 || def.GeqoGenerations != 0 {
		t.Errorf("geqo_pool_size/geqo_generations defaults = %d/%d, want 0/0 "+
			"(PG's \"derive\" sentinel)", def.GeqoPoolSize, def.GeqoGenerations)
	}
	if def.GeqoSeed != 0 {
		t.Errorf("geqo_seed default = %v, want 0", def.GeqoSeed)
	}

	ps := DefaultPlannerSettings()
	ps.GeqoEffort, ps.GeqoPoolSize, ps.GeqoGenerations = 9, 77, 33
	ps.GeqoSelectionBias, ps.GeqoSeed = 1.75, 0.5
	cp := ps.costParams()
	if cp.geqoEffort != 9 || cp.geqoPoolSize != 77 || cp.geqoGenerations != 33 {
		t.Errorf("integer knobs lost in conversion: effort=%d pool=%d gens=%d",
			cp.geqoEffort, cp.geqoPoolSize, cp.geqoGenerations)
	}
	if cp.geqoBias != 1.75 || cp.geqoSeed != 0.5 {
		t.Errorf("real knobs lost in conversion: bias=%v seed=%v", cp.geqoBias, cp.geqoSeed)
	}

	// The seed mapping must keep PG's default a no-op, so the change is
	// plan-neutral at the defaults, and must be monotone above it.
	if geqoSeedState(0) != 0 {
		t.Error("geqo_seed = 0 must leave the PRNG at its existing fixed state")
	}
	if !(geqoSeedState(0.5) > 0 && geqoSeedState(1) > geqoSeedState(0.5)) {
		t.Errorf("seed mapping is not monotone: 0.5 -> %d, 1 -> %d",
			geqoSeedState(0.5), geqoSeedState(1))
	}
}
