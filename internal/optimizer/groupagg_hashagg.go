package optimizer

import "sync/atomic"

// enable_hashagg seed — S8 Slice 2b of M0134-0001, retired by C-15. In
// PostgreSQL, enable_hashagg is not read during path creation; setting it
// off merely disables the AGG_HASHED arm at cost time (costsize.c:2755-2756
// cost_agg), so the sorted path wins. The outcome-forcing RULE that used to
// live here (forcing AggStrategySorted over a Sort) is gone: the GROUP_AGG
// producer (groupingpaths.go) marks the hashed path DisabledNodes instead
// and lets cost_agg decide. What remains is the GUC seed the per-statement
// settings are defaulted from (take2 P2-02c) — the bridge, not the rule.

// hashAggEnabled is the package-level kill-switch for the hashed-aggregate
// strategy. Initialised to "on" (1), mirroring PG's enable_hashagg BootVal.
// Tests toggle it via SetHashAggEnabled(false). The GUC is registered in
// internal/config/defaults.go; the runtime SET bridge lives in
// cmd/goopg/main.go (same OnChange pattern as enable_presorted_aggregate).
var hashAggEnabled atomic.Bool

func init() {
	hashAggEnabled.Store(true)
}

// SetHashAggEnabled flips the enable_hashagg kill-switch on or off. Test-only
// API; the production toggle path is the enable_hashagg GUC bridged in
// cmd/goopg/main.go.
// HashAggEnabled reports the process-wide enable_hashagg SEED — the value a
// planner call with no session defaults to (take2 P2-02c). It is not what a
// session's SET reads; that travels on PlannerSettings.
func HashAggEnabled() bool { return hashAggEnabled.Load() }

func SetHashAggEnabled(on bool) {
	hashAggEnabled.Store(on)
}

// (Retired by C-15.) applyEnableHashAggRule used to reproduce the
// cost-model outcome of `SET enable_hashagg = off`
// (postgres/src/backend/optimizer/path/costsize.c:2755-2756 cost_agg:
// without the AGG_HASHED arm, the sorted path wins) by mutating aggNode in
// place: on success the child was wrapped in an ascending Sort on the group
// keys and the aggregate switched to AggStrategySorted. The GROUP_AGG
// producer (groupingpaths.go) now marks the hashed path DisabledNodes
// instead and lets cost_agg decide; the rule is deleted, the seed stays.
