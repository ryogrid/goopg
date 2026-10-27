package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestCompiledCondMatchesInterpreter pins M0122-0012 C43: the index scan,
// index-only scan and bitmap heap scan evaluate their per-row Cond through
// the compiled evaluator, and must keep exactly the rows the interpreted
// evalExpr kept — order included. The filters mix AND/OR, NULL-yielding
// comparisons, char(n) and numeric columns, an uncorrelated sublink (an
// InitPlan, as TPC-DS Q14's `d_week_seq = (SELECT …)`), a function call, an
// outer reference from a parameterised nested loop, and an expression that
// raises for some rows only (the error must surface the same way).
func TestCompiledCondMatchesInterpreter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	t.Cleanup(func() { compiledCondDisabled = false })
	runSQL(t, ctx, "CREATE TABLE cc_dim (id int PRIMARY KEY, y int, m int, wk int, nm char(9), amt numeric(9,2), tag text)")
	runSQL(t, ctx, "INSERT INTO cc_dim SELECT x, 1998 + x % 5, 1 + x % 12, x / 7, CASE x % 7 WHEN 0 THEN 'Sunday' WHEN 1 THEN 'Monday' ELSE 'Other' END, x * 0.75, CASE WHEN x % 11 = 0 THEN NULL ELSE 't' || x END FROM generate_series(1, 4000) x")
	runSQL(t, ctx, "CREATE INDEX cc_dim_y ON cc_dim (y, m)")
	runSQL(t, ctx, "CREATE TABLE cc_drv (did int, lim int)")
	runSQL(t, ctx, "INSERT INTO cc_drv SELECT (x * 53) % 4000 + 1, x % 13 FROM generate_series(1, 500) x")
	runSQL(t, ctx, "ANALYZE cc_dim")
	runSQL(t, ctx, "ANALYZE cc_drv")
	nl := optimizer.DefaultPlannerSettings()
	nl.EnableHashJoin, nl.EnableMergeJoin, nl.EnableSeqScan, nl.EnableBitmapScan = false, false, false, false
	nl.MaxParallelWorkersPerGather = 0
	bm := nl
	bm.EnableBitmapScan, bm.EnableIndexScan = true, false
	queries := []struct {
		sql string
		ps  optimizer.PlannerSettings
	}{
		{"SELECT v.did, d.nm, d.amt FROM cc_drv v JOIN cc_dim d ON d.id = v.did AND d.y = 2001 AND d.m = 11 ORDER BY v.did", nl},
		{"SELECT v.did, d.wk FROM cc_drv v JOIN cc_dim d ON d.id = v.did AND (d.wk = (SELECT max(wk) FROM cc_dim WHERE y = 1999 AND m = 3) - 1 OR d.wk = (SELECT min(wk) FROM cc_dim WHERE y = 2000)) ORDER BY v.did", nl},
		{"SELECT v.did, d.tag FROM cc_drv v JOIN cc_dim d ON d.id = v.did AND (d.nm = 'Sunday' OR d.tag IS NULL) ORDER BY v.did", nl},
		{"SELECT v.did, d.amt FROM cc_drv v JOIN cc_dim d ON d.id = v.did AND d.m > v.lim AND upper(d.nm) <> 'OTHER' ORDER BY v.did", nl},
		{"SELECT count(*), sum(d.amt) FROM cc_drv v JOIN cc_dim d ON d.id = v.did AND d.amt * 2 > d.wk", nl},
		{"SELECT id, nm FROM cc_dim WHERE y = 2000 AND m = 4 AND amt > 100 ORDER BY id", nl},
		{"SELECT id, y, m FROM cc_dim WHERE y = 2002 AND m BETWEEN 2 AND 5 AND m <> 3 ORDER BY id", nl},
		{"SELECT id, tag FROM cc_dim WHERE y = 1999 AND (tag LIKE 't1%' OR m = 7) ORDER BY id", bm},
		{"SELECT count(*), sum(amt) FROM cc_dim WHERE y = 2001 AND nm = 'Monday'", bm},
	}
	for _, q := range queries {
		got := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q.sql, q.ps)))
		compiledCondDisabled = true
		want := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q.sql, q.ps)))
		compiledCondDisabled = false
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: compiled Cond differs from interpreted\ncompiled    %v\ninterpreted %v", q.sql, got, want)
		}
		t.Logf("rows=%d %s", len(got), q.sql)
	}
	// An error raised by the filter must surface the same way.
	errQ := "SELECT v.did FROM cc_drv v JOIN cc_dim d ON d.id = v.did AND 100 / (d.m - d.m) > 1"
	_, gotErr := tryDrainPlan(ctx, planWithSettings(t, ctx, errQ, nl))
	compiledCondDisabled = true
	_, wantErr := tryDrainPlan(ctx, planWithSettings(t, ctx, errQ, nl))
	compiledCondDisabled = false
	if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
		t.Fatalf("filter error differs: compiled %v, interpreted %v", gotErr, wantErr)
	}
	if gotErr == nil {
		t.Fatal("division by zero in the filter raised no error")
	}
}

// tryDrainPlan runs plan and returns its rows or the first error.
func tryDrainPlan(ctx *Context, plan optimizer.Node) ([]Row, error) {
	op, err := Build(plan)
	if err != nil {
		return nil, err
	}
	if err := op.Open(ctx); err != nil {
		return nil, err
	}
	defer op.Close()
	return drainScan(op)
}
