package optimizer

// Memoize insertion rule — bundle phase S7 (D5.1/D5.3), design doc
// docs/design/correlated-subquery-planning/05-memoize-operator.md.
//
// After tryBuildNLI succeeds, maybeAttachMemoize decides whether to
// interpose a Memoize cache between the join driver and the inner
// index probe. PG oracle: get_memoize_path
// (postgres/src/backend/optimizer/path/joinpath.c) consulted exactly
// where nested-loop paths are formed.
//
// The gate is deliberately STRICTER than the NLI gate: a wrong NLI is
// only slow, but a wrong Memoize is pure overhead plus a correctness
// risk surface, so statistics are mandatory (no ANALYZE stats → no
// Memoize). On this codebase ANALYZE statistics are in-memory and lost
// on restart, so fresh servers — including the TPC-H bench — get no
// Memoize insertions at all; that matches PostgreSQL, which memoizes
// zero of the 22 TPC-H queries at SF1.

import (
	"os"
	"sync/atomic"
)

// memoizeOn gates Memoize insertion. Default on; killed by the env
// GOOPG_MEMOIZE=off at process start, by SetMemoizeEnabled from tests,
// or by `SET enable_memoize = off` via the OnChange bridge in
// cmd/goopg/main.go (the same bridge pattern as enable_nestloop_index:
// the GUC stays registered in the planner-method no-op loop and the
// most-recent SET wins process-wide).
var memoizeOn atomic.Bool

func init() {
	memoizeOn.Store(memoizeFromEnv(os.Getenv("GOOPG_MEMOIZE")))
}

// memoizeFromEnv is the kill-switch's polarity, factored out of init so the
// provenance table (flaglabels.go) can ask what an UNSET variable resolves to
// without a subprocess — the same shape as pgShapedDPFromEnv.
func memoizeFromEnv(v string) bool { return v != "off" }

// SetMemoizeEnabled flips Memoize insertion on or off (test hook and
// the enable_memoize GUC bridge target).
func SetMemoizeEnabled(on bool) { memoizeOn.Store(on) }

// MemoizeEnabled reports the current switch state.
func MemoizeEnabled() bool { return memoizeOn.Load() }

const (
	// memoizeMinHitFraction: insert only when at least this fraction of
	// probes is expected to repeat a previously seen key
	// (h = 1 − ndistinct/outerRows).
	memoizeMinHitFraction = 0.5
	// memoizeMinOuterRows: below this the cache bookkeeping cannot pay
	// for itself.
	memoizeMinOuterRows int64 = 1000
	// memoizeMaxEstEntries clamps the planner-side entry estimate so a
	// garbage ndistinct cannot demand an absurd initial size.
	memoizeMaxEstEntries int64 = 1 << 20
)

// maybeAttachMemoize attaches an InnerMemo to nli when the insertion
// gate accepts. Mutates nli in place; always returns nli.
//
// Gate conditions (all must hold):
//   - switch on (GUC/env);
//   - join type INNER or LEFT — NEVER Semi/Anti: their early-out probes
//     cannot run the inner to completion, so a cache entry could never
//     be marked complete (PG refuses SEMI/ANTI in get_memoize_path
//     unless inner_unique; goopg's semi/anti NLI has no inner_unique
//     proof, so the refusal is unconditional);
//   - every probe-key expr is a bare *ColumnRef into the outer schema —
//     this both makes the ndistinct lookup well-defined and subsumes
//     the volatility precondition (a column reference is never
//     volatile, and the aliased Child is a bare IndexScan with no
//     filter expressions of its own);
//   - ANALYZE stats yield outerRows and ndistinct(outer key);
//   - outerRows ≥ memoizeMinOuterRows and expected hit fraction
//     ≥ memoizeMinHitFraction.
func maybeAttachMemoize(nli *NestedLoopIndexJoin) *NestedLoopIndexJoin {
	if nli == nil || !memoizeOn.Load() {
		return nli
	}
	if nli.Type != JoinTypeInner && nli.Type != JoinTypeLeft {
		return nli
	}
	inner := nli.Inner
	if inner == nil || inner.Index == nil {
		return nli
	}
	var keyExprs []Expr
	switch {
	case len(inner.Keys) > 0:
		keyExprs = inner.Keys
	case inner.Key != nil:
		keyExprs = []Expr{inner.Key}
	default:
		return nli // range probe (LowKey/HighKey) — parameter set unclear, skip
	}
	outerWidth := len(nli.Outer.Output())
	// All key exprs must be bare outer-schema ColumnRefs (see doc
	// comment). Collect the first key's ndistinct for the hit-fraction
	// estimate; multi-column keys use the max ndistinct of the parts as
	// a conservative (hit-fraction-lowering) combined estimate.
	var nd int64
	for _, ke := range keyExprs {
		cr, ok := ke.(*ColumnRef)
		if !ok || cr.Index < 0 || cr.Index >= outerWidth {
			return nli
		}
		colND := columnNDistinctForChild(cr.Index, nli.Outer)
		if colND <= 0 {
			return nli // stats mandatory
		}
		if colND > nd {
			nd = colND
		}
	}
	outerRows := EstimateRows(nli.Outer)
	if outerRows < memoizeMinOuterRows {
		return nli
	}
	hitFrac := 1.0 - float64(nd)/float64(outerRows)
	if hitFrac < memoizeMinHitFraction {
		return nli
	}
	est := nd
	if est > memoizeMaxEstEntries {
		est = memoizeMaxEstEntries
	}
	// SingleRow: a full-key probe over a unique index yields at most
	// one row per parameter set (PG's `singlerow`,
	// nodeMemoize.c:832/:1058).
	singleRow := inner.Index.Unique &&
		(len(inner.Keys) == len(inner.Index.Columns) ||
			(inner.Key != nil && len(inner.Index.Columns) == 1))
	nli.InnerMemo = &Memoize{
		pos:        inner.Pos(),
		Child:      inner,
		KeyExprs:   keyExprs,
		SingleRow:  singleRow,
		EstEntries: est,
	}
	return nli
}
