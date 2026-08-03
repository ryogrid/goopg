package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// Stage 9 (S2c, design bundle D4.2): the subPlanHandle engine. These
// tests grade the two properties the stage exists for — sublink inner
// plans are BUILT once and rescanned per outer row, and the
// cacheability gate keeps volatile / LockRows inners re-executing per
// row (ch.07 M13) — plus the kill switch's promise that the legacy
// lifecycle is still there underneath.
//
// Probe shapes follow subplan_stats_test.go's convention: sublinks are
// parked under an always-false OR arm (or use non-equijoin correlation)
// so the planner leaves them as SubPlans on these fixtures.

// handleStat fetches the recorded stats for the single instrumented
// sublink, like onlyStat, but tolerates the presence of extra sublinks
// by picking the one with the most Calls (probe queries occasionally
// register a second, never-executed sublink).
func handleStat(t *testing.T, ctx *Context) *SubPlanSiteStats {
	t.Helper()
	if len(ctx.SubPlanStats) == 0 {
		t.Fatalf("no instrumented sublink — did the planner unnest the probe?")
	}
	var best *SubPlanSiteStats
	for _, s := range ctx.SubPlanStats {
		if best == nil || s.Calls > best.Calls {
			best = s
		}
	}
	return best
}

// TestHandleRescanFilterIndexScanRooted: the Filter{IndexScan} chain —
// the shape S6's probe-cheap policy keeps on the SubPlan path — must
// build once and rescan per distinct outer key.
func TestHandleRescanFilterIndexScanRooted(t *testing.T) {
	ctx, cleanup := statsFixture(t, "CREATE INDEX t2_a_idx ON t2 (a)")
	defer cleanup()

	// Correlated scalar over the indexed column with an extra local
	// conjunct, so the inner plan is Aggregate{Filter{IndexScan}}.
	runQuery(t, ctx,
		"SELECT * FROM t1 WHERE t1.a = -999 OR t1.b < (SELECT sum(t2.b) FROM t2 WHERE t2.a = t1.a AND t2.b >= 0)")

	s := handleStat(t, ctx)
	if s.Calls != 3 {
		t.Fatalf("Calls = %d, want 3", s.Calls)
	}
	if s.Rebuilds != 1 {
		t.Errorf("Rebuilds = %d, want 1 (index-probe chain builds once)", s.Rebuilds)
	}
	if s.Rescans != s.Calls-1 {
		t.Errorf("Rescans = %d, want %d", s.Rescans, s.Calls-1)
	}
}

// TestHandleRescanLimitRooted: a LIMIT inside the sublink body relies
// on the Stage-7 limitOp Open reset; across ≥3 outer rows the handle
// re-Opens the same tree and each row sees a fresh LIMIT window.
func TestHandleRescanLimitRooted(t *testing.T) {
	ctx, cleanup := statsFixture(t)
	defer cleanup()

	// Scalar sublink with LIMIT 1: t2 has rows for a=1 (100,101) and
	// a=3 (300). Non-equi correlation keeps it un-unnested; ORDER BY
	// keeps the picked row deterministic.
	rows := runQuery(t, ctx,
		"SELECT a FROM t1 WHERE t1.a = -999 OR t1.b <= (SELECT t2.b FROM t2 WHERE t2.a <= t1.a ORDER BY t2.b DESC LIMIT 1) ORDER BY a")

	// Expected: outer rows a=1 (b=10): max t2.b with t2.a<=1 is 101 →
	// 10<=101 true; a=2 (b=20): still 101 → true; a=3 (b=30): 300 → true.
	if len(rows) != 3 {
		t.Fatalf("rows = %v, want 3 rows (each outer row satisfied)", rows)
	}
	s := handleStat(t, ctx)
	if s.Calls != 3 {
		t.Fatalf("Calls = %d, want 3", s.Calls)
	}
	// Sort under the Limit forces the Close+Open rescan kind; still no
	// fresh Build after the first.
	if s.Rebuilds != 1 {
		t.Errorf("Rebuilds = %d, want 1 (Close+Open counts as a rescan)", s.Rebuilds)
	}
	if s.Rescans != s.Calls-1 {
		t.Errorf("Rescans = %d, want %d", s.Rescans, s.Calls-1)
	}
}

// TestHandleRescanSortRootedPartialDrain: EXISTS reads at most one row,
// leaving the sort-rooted subplan mid-drain; the Close+Open rescan must
// not leak rows into the next outer row's result (sortOp.Open appends —
// the reason Sort forces the Close+Open kind).
func TestHandleRescanSortRootedPartialDrain(t *testing.T) {
	ctx, cleanup := statsFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx,
		"SELECT a FROM t1 WHERE t1.a = -999 OR EXISTS (SELECT t2.b FROM t2 WHERE t2.a <= t1.a ORDER BY t2.b) ORDER BY a")

	// Every outer row has at least one qualifying t2 row (a<=1: yes; …).
	if len(rows) != 3 {
		t.Fatalf("rows = %v, want 3", rows)
	}
	s := handleStat(t, ctx)
	if s.Rebuilds != 1 || s.Rescans != s.Calls-1 {
		t.Errorf("Rebuilds/Rescans = %d/%d, want 1/%d", s.Rebuilds, s.Rescans, s.Calls-1)
	}
}

// TestHandleVolatileInnerNeverCached (ch.07 M13): a volatile function
// in the inner plan disables result caching, so every call re-executes
// even when the correlation params repeat. Two outer rows share a=1,
// so a cacheable sublink would serve the second from the cache.
func TestHandleVolatileInnerNeverCached(t *testing.T) {
	ctx, cleanup := statsFixture(t,
		"INSERT INTO t1 VALUES (1, 11)") // duplicate correlation key a=1
	defer cleanup()

	runQuery(t, ctx,
		"SELECT * FROM t1 WHERE t1.a = -999 OR t1.b < (SELECT sum(t2.b) FROM t2 WHERE t2.a <= t1.a AND random() < 2)")

	s := handleStat(t, ctx)
	if s.Calls != 4 {
		t.Fatalf("Calls = %d, want 4", s.Calls)
	}
	if s.CacheHits != 0 {
		t.Errorf("CacheHits = %d, want 0: volatile inner must re-execute per row", s.CacheHits)
	}
	if s.Rebuilds+s.Rescans != s.Calls {
		t.Errorf("Rebuilds+Rescans = %d, want == Calls (%d): every call ran the plan", s.Rebuilds+s.Rescans, s.Calls)
	}
}

// TestHandleStableInnerCached: the same shape WITHOUT the volatile call
// serves repeated correlation values from the projected-key cache.
func TestHandleStableInnerCached(t *testing.T) {
	ctx, cleanup := statsFixture(t,
		"INSERT INTO t1 VALUES (1, 11)")
	defer cleanup()

	runQuery(t, ctx,
		"SELECT * FROM t1 WHERE t1.a = -999 OR t1.b < (SELECT sum(t2.b) FROM t2 WHERE t2.a <= t1.a)")

	s := handleStat(t, ctx)
	if s.Calls != 4 {
		t.Fatalf("Calls = %d, want 4", s.Calls)
	}
	if s.CacheHits == 0 {
		t.Errorf("CacheHits = 0, want > 0: repeated correlation value must hit the projected-key cache")
	}
}

// TestHandleKillSwitchLegacyLifecycle: with the engine off, the
// pre-Stage-9 lifecycle is byte-for-byte back — a correlated EXISTS
// rebuilds on every call and never rescans.
func TestHandleKillSwitchLegacyLifecycle(t *testing.T) {
	// M0125-0036: this probe's `= -999 OR EXISTS (…)` shape is what the
	// EXISTS→ANY conversion consumes; the correlated-SubPlan path it
	// grades is still live for every EXISTS the conversion declines.
	pinCorrelatedSubPlanPath(t)
	SetSubPlanRescanEnabled(false)
	t.Cleanup(func() { SetSubPlanRescanEnabled(true) }) // restore the ON default

	ctx, cleanup := statsFixture(t)
	defer cleanup()

	runQuery(t, ctx,
		"SELECT * FROM t1 WHERE t1.a = -999 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a)")

	s := handleStat(t, ctx)
	if s.Calls != 3 {
		t.Fatalf("Calls = %d, want 3", s.Calls)
	}
	if s.Rebuilds != s.Calls {
		t.Errorf("Rebuilds = %d, want == Calls (%d) on the legacy path", s.Rebuilds, s.Calls)
	}
	if s.Rescans != 0 {
		t.Errorf("Rescans = %d, want 0 on the legacy path", s.Rescans)
	}
	if len(ctx.SubPlanHandles) != 0 {
		t.Errorf("SubPlanHandles allocated on the legacy path: %d", len(ctx.SubPlanHandles))
	}
}

// TestCloseSubPlansIdempotent: teardown twice, and after handles were
// already closed, is a no-op; a fresh evaluation after teardown builds
// a new handle.
func TestCloseSubPlansIdempotent(t *testing.T) {
	ctx, cleanup := statsFixture(t)
	defer cleanup()

	runQuery(t, ctx,
		"SELECT * FROM t1 WHERE t1.a = -999 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a)")
	if len(ctx.SubPlanHandles) == 0 {
		t.Fatalf("no handle built")
	}
	ctx.CloseSubPlans()
	if ctx.SubPlanHandles != nil {
		t.Fatalf("SubPlanHandles not cleared")
	}
	ctx.CloseSubPlans() // second teardown: no-op, no panic
	var nilCtx *Context
	nilCtx.CloseSubPlans() // nil-safe

	// Executing again after teardown rebuilds cleanly.
	rows := runQuery(t, ctx,
		"SELECT a FROM t1 WHERE t1.a = -999 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a) ORDER BY a")
	if len(rows) != 2 {
		t.Fatalf("post-teardown rows = %v, want 2 (a=1 and a=3 have t2 matches)", rows)
	}
}

// TestClassifySubPlanKinds pins the classification table directly.
func TestClassifySubPlanKinds(t *testing.T) {
	idx := &planner.IndexScan{}
	cases := []struct {
		name string
		plan planner.Node
		kind int
	}{
		{"bare index scan", idx, rescanReOpen},
		{"filter over index", &planner.Filter{Child: idx}, rescanReOpen},
		{"sort forces close+open", &planner.Sort{Child: idx}, rescanCloseOpen},
		{"lock rows forces close+open", &planner.LockRows{Child: idx}, rescanCloseOpen},
		{"join forces close+open", &planner.Join{Left: idx, Right: &planner.SeqScan{}}, rescanCloseOpen},
	}
	for _, c := range cases {
		kind, _ := classifySubPlan(c.plan, nil)
		if kind != c.kind {
			t.Errorf("%s: kind = %d, want %d", c.name, kind, c.kind)
		}
	}
	// LockRows is additionally uncacheable.
	if _, cacheable := classifySubPlan(&planner.LockRows{Child: idx}, nil); cacheable {
		t.Errorf("LockRows-rooted plan classified cacheable")
	}
}
