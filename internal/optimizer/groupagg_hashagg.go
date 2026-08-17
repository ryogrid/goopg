package optimizer

import "sync/atomic"

// enable_hashagg bridge — S8 Slice 2b of M0134-0001. In PostgreSQL,
// enable_hashagg is not read during path creation; setting it off merely
// disables the AGG_HASHED arm at cost time (costsize.c:2755-2756 cost_agg), so
// the sorted path wins. goopg has no cost model, so this rule reproduces the
// *outcome* directly: when the GUC is off, a plain grouped aggregate is forced
// to AggStrategySorted (EXPLAIN "GroupAggregate") over a Sort on the group
// keys. Mirrors the enable_presorted_aggregate bridge (S8 Slice 2a) one-to-one.

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
func SetHashAggEnabled(on bool) {
	hashAggEnabled.Store(on)
}

// applyEnableHashAggRule reproduces the cost-model outcome of
// `SET enable_hashagg = off` (postgres/src/backend/optimizer/path/costsize.c:
// 2755-2756 cost_agg: without the AGG_HASHED arm, the sorted path wins). It
// mutates aggNode in place: on success the child is wrapped in an ascending
// Sort on the group keys and the aggregate switches to AggStrategySorted so
// EXPLAIN shows GroupAggregate over Sort over Seq Scan (aggregates.out:3457).
//
// It returns without touching aggNode unless ALL of the following hold:
//   - the GUC is off (hashAggEnabled.Load() == false), AND
//   - the node is still AggStrategyHashed (the presorted-aggregate rule did
//     NOT already claim it — never double-wrap a query that already has an
//     internal ORDER BY / DISTINCT), AND
//   - no grouping sets (those always hash; PG's cost_agg has no SORTED arm
//     for grouping sets), AND
//   - there is a GROUP BY (len(GroupExprs) > 0 — a non-grouped aggregate keeps
//     its plain "Aggregate" label), AND
//   - Mode is AggModeSimple (parallel Partial/Final nodes are not shaped by
//     the GUC).
//
// The executor routing gate that must agree is
// internal/executor/operators_join_agg.go:1979-1980 (openSorted): Strategy==
// AggStrategySorted && GroupingSets==nil && len(GroupExprs)>0 && Mode==
// AggModeSimple — exactly the conditions enforced above, so the EXPLAIN label
// never lies about the executor path.
func applyEnableHashAggRule(aggNode *Aggregate) {
	if !(!hashAggEnabled.Load() &&
		aggNode.Strategy == AggStrategyHashed &&
		aggNode.GroupingSets == nil &&
		len(aggNode.GroupExprs) > 0 &&
		aggNode.Mode == AggModeSimple) {
		return
	}

	keys := make([]SortKey, 0, len(aggNode.GroupExprs))
	for _, g := range aggNode.GroupExprs {
		keys = append(keys, SortKey{Expr: g, Desc: false, NullsFirst: false})
	}

	aggNode.Child = &Sort{pos: aggNode.Pos(), Child: aggNode.Child, Keys: keys}
	aggNode.Strategy = AggStrategySorted
}
