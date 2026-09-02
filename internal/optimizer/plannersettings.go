package optimizer

import "github.com/goopg/goopg/internal/executor/hashsize"

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

	// EnableHashJoin / EnableMergeJoin / EnableNestLoop are PG's enable_*
	// planner-method GUCs. take2 P2-05.
	//
	// PG does NOT implement these by skipping a producer — it still generates
	// the path and increments `path->disabled_nodes` (PG 18's replacement for
	// the old disable_cost), so a query whose ONLY legal plan uses a disabled
	// method still gets that plan rather than failing. `Path.DisabledNodes` and
	// the dominance ordering that reads it already existed here; nothing set
	// them, so all three GUCs were accepted, shown in pg_settings, and had no
	// effect whatsoever.
	//
	// Zero value is false, which would mean "disabled". DefaultPlannerSettings
	// sets all three true, and every path that builds a PlannerSettings by hand
	// must start from it rather than from the zero value — the same rule the
	// cost fields already carry.
	EnableHashJoin  bool
	EnableMergeJoin bool
	EnableNestLoop  bool

	// EnableHashAgg / EnablePresortedAggregate / Geqo / GeqoThreshold are the
	// remaining P2-02c bridges. Same defect as EnableMemoize below: each
	// reached the planner through a registry.OnChange bridge writing a
	// process-global atomic, so one session's SET steered every session.
	EnableHashAgg            bool
	EnablePresortedAggregate bool
	Geqo                     bool
	GeqoThreshold            int

	// EnableNestLoopIndex is goopg's `enable_nestloop_index`, the last of the
	// six P2-02c bridges.
	EnableNestLoopIndex bool

	// EnableMemoize is `enable_memoize`. take2 P2-02c.
	//
	// It used to reach the planner through a registry.OnChange bridge in
	// cmd/goopg/main.go that wrote a process-global atomic, so `SET
	// enable_memoize = off` in ONE session silently disabled Memoize for every
	// other session on the server. It is a per-statement planner input like any
	// other and now travels with the rest.
	EnableMemoize bool

	// HashMemMultiplier is `hash_mem_multiplier`. A hash build's budget is
	// `work_mem * hash_mem_multiplier` (get_hash_memory_limit,
	// nodeHash.c:3622), NOT work_mem alone — take2 P2-03. Zero means "use the
	// default", so a zero-valued PlannerSettings still prices hashes sanely.
	HashMemMultiplier float64
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
		EnableHashJoin:  true,
		EnableMergeJoin: true,
		EnableNestLoop:  true,
		EnableMemoize:   true,

		EnableHashAgg:            HashAggEnabled(),
		EnablePresortedAggregate: PresortedAggEnabled(),
		EnableNestLoopIndex:      NLIEnabled(),
		Geqo:                     GeqoEnabled(),
		GeqoThreshold:            GeqoThreshold(),
		SeqPageCost:        cp.seqPageCost,
		RandomPageCost:     cp.randomPageCost,
		CPUTupleCost:       cp.cpuTupleCost,
		CPUIndexTupleCost:  cp.cpuIndexTupleCost,
		CPUOperatorCost:    cp.cpuOperatorCost,
		ParallelSetupCost:  cp.parallelSetupCost,
		ParallelTupleCost:  cp.parallelTupleCost,
		EffectiveCacheSize: cp.effectiveCacheSize,
		// The RAW work_mem, not cp.workMem: costParams() applies the
		// multiplier itself, and cp.workMem has already had it applied. Taking
		// it from there would square the multiplier — caught by
		// TestDefaultPlannerSettingsMatchTheHardWiredParams, which is what that
		// round-trip invariant is for.
		WorkMem:           hashsize.DefaultMemLimitBytes,
		HashMemMultiplier: hashsize.DefaultHashMemMultiplier,
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
		// The cost model's hash budget is work_mem * hash_mem_multiplier, the
		// same figure the executor's buildGeometry solves for. Computing it
		// here rather than at each cost site keeps the two on one expression.
		workMem: hashsize.HashMemLimit(ps.WorkMem, ps.HashMemMultiplier),
		// P2-05: the producers take costParams, not PlannerSettings, so the
		// method toggles ride along with the cost inputs they are weighed
		// against.
		enableHashJoin:  ps.EnableHashJoin,
		enableMergeJoin: ps.EnableMergeJoin,
		enableNestLoop:  ps.EnableNestLoop,
		enableMemoize:   ps.EnableMemoize,
		geqo:            ps.Geqo,
		geqoThreshold:   ps.GeqoThreshold,
	}
}
