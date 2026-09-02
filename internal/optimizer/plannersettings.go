package optimizer

// PlannerSettings is the per-statement planner context: the session-settable
// values goopg's cost model reads, PG's PlannerGlobal analogue.
//
// WHY IT EXISTS. `defaultCostParams()` returns hard-wired constants and is the
// only source of cost parameters in the planner, so all nine cost GUCs are
// registered, settable, and reach nothing: `SET random_page_cost = 1.1` changes
// no plan. `cost_funcs.go`'s own `workMem` comment records the gap and its
// hazard — "The two must agree or the planner prices a geometry the executor
// will not build" — and that hazard blocks P0-12, because setting `work_mem` in
// a bench conf today would move the EXECUTOR while the planner kept pricing at
// the old value.
//
// This type is the carrier. Filling it from a session is P2-02, and that item
// is blocked on P2-04: the plan cache is cross-session and keyed without any
// GUC fingerprint.
//
// The precedent is `ParallelSettings` (parallel.go) — a plain value struct built
// from the session by the postmaster and handed to the planner. It is
// deliberately NOT a package-level global: the six `enable_*`/`geqo` bridges in
// cmd/goopg/main.go are, and a `SET` in one session changes the planner for
// every session. That is the defect P2-02c removes, not a pattern to copy.
//
// design: docs/design/not_ralph/planner_refactor_take2/impl/P2-A-planner-context.md
type PlannerSettings struct {
	SeqPageCost       float64
	RandomPageCost    float64
	CPUTupleCost      float64
	CPUIndexTupleCost float64
	CPUOperatorCost   float64
	ParallelSetupCost float64
	ParallelTupleCost float64

	// EffectiveCacheSize is in BLOCKS, the unit PG's own variable carries
	// (GUC_UNIT_BLOCKS, cost.h). The GUC is registered `UnitKB` with BootVal
	// "4GB" (internal/utils/misc/defaults.go), so whoever fills this struct
	// must convert — a trap recorded here because the field alone does not say
	// it.
	EffectiveCacheSize float64

	// WorkMem is in BYTES. The GUC is registered `UnitKB` with BootVal
	// "512MB"; same conversion caveat as above.
	WorkMem int64
}

// DefaultPlannerSettings returns the settings a statement plans under when no
// session supplies any.
//
// It is defined AS the values `defaultCostParams()` reads, not as a second copy
// of the constants — two lists of the same numbers is the duplication this
// repository has already been bitten by twice in the flag-label table.
func DefaultPlannerSettings() PlannerSettings {
	cp := defaultCostParams()
	return PlannerSettings{
		SeqPageCost:        cp.seqPageCost,
		RandomPageCost:     cp.randomPageCost,
		CPUTupleCost:       cp.cpuTupleCost,
		CPUIndexTupleCost:  cp.cpuIndexTupleCost,
		CPUOperatorCost:    cp.cpuOperatorCost,
		ParallelSetupCost:  cp.parallelSetupCost,
		ParallelTupleCost:  cp.parallelTupleCost,
		EffectiveCacheSize: cp.effectiveCacheSize,
		WorkMem:            cp.workMem,
	}
}

// costParams converts to the planner's internal currency.
//
// The two types stay distinct on purpose: `costParams` is unexported and is
// mutated freely by roughly two hundred test sites (`cp := defaultCostParams();
// cp.workMem = …`), so it cannot become a public boundary; `PlannerSettings` is
// the boundary the postmaster fills.
func (ps PlannerSettings) costParams() costParams {
	return costParams{
		seqPageCost:        ps.SeqPageCost,
		randomPageCost:     ps.RandomPageCost,
		cpuTupleCost:       ps.CPUTupleCost,
		cpuIndexTupleCost:  ps.CPUIndexTupleCost,
		cpuOperatorCost:    ps.CPUOperatorCost,
		parallelSetupCost:  ps.ParallelSetupCost,
		parallelTupleCost:  ps.ParallelTupleCost,
		effectiveCacheSize: ps.EffectiveCacheSize,
		workMem:            ps.WorkMem,
	}
}
