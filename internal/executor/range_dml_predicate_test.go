package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// M0134-0001-followup (nightly batch 20260817-011734, brief
// m-nightly-20260817-e2e-pgstart): tryRangeIndexScan
// (internal/optimizer/planner.go, landed aa40caa6) drops the wrapping
// *Filter for a single-conjunct single-column range IndexScan and hands
// UPDATE/DELETE a bare *optimizer.IndexScan with Key == nil and
// LowKey/HighKey (+ LowOp/HighOp) set. Before this fix,
// indexScanPredicate (operators_storage.go) returned nil for that shape,
// and extractScan's bare-*IndexScan branch has no Filter to fall back
// on, so scanMatching ran with a nil predicate — matching (and
// updating/deleting) EVERY row of the table regardless of the WHERE
// clause. These tests drive the exact SQL shapes that produce that
// bare-IndexScan plan through the real parser -> planner -> executor
// stack (matching the `cte_dml_outer_dml_fence_test.go` convention) and
// assert row-exact UPDATE/DELETE results, both for the affected-row
// count and for the untouched rows' original values. They FAIL at the
// pre-fix indexScanPredicate (which returns nil for Key==nil) and PASS
// after — verified live by temporarily reverting operators_storage.go
// (see report.md).

// planDMLStmt parses+plans one statement without executing it, so the
// caller can assert the plan SHAPE (bare *optimizer.IndexScan, no
// wrapping Filter) before running it — this is what pins the test to
// the actual bug-triggering plan rather than merely re-testing generic
// DML correctness.
func planDMLStmt(t *testing.T, ctx *Context, sql string) optimizer.Node {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	return plan
}

// execDMLPlan builds+opens+drains a previously-planned DML node and
// returns the operator (still open — caller must Close) so tests can
// read RowsAffected() off the concrete *updateOp/*deleteOp.
func execDMLPlan(t *testing.T, ctx *Context, sql string, plan optimizer.Node) Operator {
	t.Helper()
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open(%q): %v", sql, err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Next(%q): %v", sql, err)
	}
	return op
}

// rangeDMLSeed creates a 10-row table (id 1..10 PRIMARY KEY, label "lN",
// qty N*10) plus a secondary composite index on (id, qty) covering the
// multi-column equality (Keys) shape.
func rangeDMLSeed(t *testing.T, ctx *Context, table string) {
	t.Helper()
	if err := runDDL(t, ctx, "CREATE TABLE "+table+" (id int4 PRIMARY KEY, label text, qty int4)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX "+table+"_id_qty_idx ON "+table+" (id, qty)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	for i := int64(1); i <= 10; i++ {
		sql := "INSERT INTO " + table + " VALUES (" +
			itoaTest(i) + ", 'l" + itoaTest(i) + "', " + itoaTest(i*10) + ")"
		runDMLRows(t, ctx, sql)
	}
}

func itoaTest(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// rangeDMLRemaining returns id -> (label, qty) for every row currently
// visible via a fresh SELECT.
func rangeDMLRemaining(t *testing.T, ctx *Context, table string) map[int64][2]interface{} {
	t.Helper()
	rows := runDMLRows(t, ctx, "SELECT id, label, qty FROM "+table)
	got := map[int64][2]interface{}{}
	for _, r := range rows {
		got[r[0].Int] = [2]interface{}{r[1].StringValue(), r[2].Int}
	}
	return got
}

type rangeDMLCase struct {
	name            string
	whereClause     string
	wantAffectedIDs []int64 // ids the predicate must match, out of 1..10
	// requireBare: true when tryRangeIndexScan's single-conjunct
	// Filter-drop applies to this WHERE shape, so the planner MUST hand
	// back a bare *optimizer.IndexScan (Key==nil, no wrapping Filter) —
	// this is the exact M0134-0001-followup bug-triggering shape.
	// BETWEEN desugars to two conjuncts (id >= lo AND id <= hi), which
	// tryRangeIndexScan's single-conjunct check does not collapse, so it
	// keeps the wrapping Filter — still a valid IndexScan-with-bounds
	// shape (already correctness-safe pre-fix via the Filter branch) but
	// not the bare shape, hence requireBare=false.
	requireBare bool
}

func rangeDMLCases() []rangeDMLCase {
	return []rangeDMLCase{
		{name: "gt", whereClause: "id > 5", wantAffectedIDs: []int64{6, 7, 8, 9, 10}, requireBare: true},
		{name: "ge", whereClause: "id >= 5", wantAffectedIDs: []int64{5, 6, 7, 8, 9, 10}, requireBare: true},
		{name: "lt", whereClause: "id < 5", wantAffectedIDs: []int64{1, 2, 3, 4}, requireBare: true},
		{name: "le", whereClause: "id <= 5", wantAffectedIDs: []int64{1, 2, 3, 4, 5}, requireBare: true},
		{name: "between", whereClause: "id BETWEEN 3 AND 7", wantAffectedIDs: []int64{3, 4, 5, 6, 7}, requireBare: false},
	}
}

func wantSet(ids []int64) map[int64]bool {
	m := make(map[int64]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// assertIndexScanChild fails loudly if the planner stops handing this
// DML statement an IndexScan carrying LowKey/HighKey bounds — a silent
// shape drift (e.g. falling back to SeqScan) would make the test pass
// for the wrong reason, never exercising indexScanPredicate's range
// branch at all. When requireBare, the child must be the bare
// *optimizer.IndexScan (no wrapping Filter, Key==nil) shape
// tryRangeIndexScan's single-conjunct Filter-drop produces — the exact
// M0134-0001-followup bug-triggering shape; otherwise a
// *optimizer.Filter{Child: *optimizer.IndexScan} wrapper is also
// accepted (BETWEEN's two-conjunct shape).
func assertIndexScanChild(t *testing.T, sql string, child optimizer.Node, requireBare bool) {
	t.Helper()
	target := child
	if f, ok := child.(*optimizer.Filter); ok {
		if requireBare {
			t.Fatalf("Plan(%q): child = *optimizer.Filter, want bare *optimizer.IndexScan (plan shape drifted — this test no longer exercises the M0134-0001-followup bug)", sql)
		}
		target = f.Child
	}
	ix, ok := target.(*optimizer.IndexScan)
	if !ok {
		t.Fatalf("Plan(%q): child = %T, want *optimizer.IndexScan (possibly Filter-wrapped)", sql, child)
	}
	if requireBare && ix.Key != nil {
		t.Fatalf("Plan(%q): IndexScan.Key = %v, want nil (equality shape, not the range shape under test)", sql, ix.Key)
	}
	if ix.LowKey == nil && ix.HighKey == nil {
		t.Fatalf("Plan(%q): IndexScan has neither LowKey nor HighKey set", sql)
	}
}

// TestRangeIndexScanDeletePredicate: DELETE ... WHERE id <op> K, planned
// through the real optimizer, must produce a bare IndexScan (asserted)
// and delete exactly the matching rows, leaving every other row's
// (label, qty) unchanged.
func TestRangeIndexScanDeletePredicate(t *testing.T) {
	for _, c := range rangeDMLCases() {
		t.Run(c.name, func(t *testing.T) {
			ctx, _, cleanup := newDDLFixture(t)
			defer cleanup()
			rangeDMLSeed(t, ctx, "ridx_del")

			sql := "DELETE FROM ridx_del WHERE " + c.whereClause
			plan := planDMLStmt(t, ctx, sql)
			del, ok := plan.(*optimizer.Delete)
			if !ok {
				t.Fatalf("Plan(%q) = %T, want *optimizer.Delete", sql, plan)
			}
			assertIndexScanChild(t, sql, del.Child, c.requireBare)

			op := execDMLPlan(t, ctx, sql, plan)
			want := wantSet(c.wantAffectedIDs)
			if d := op.(*deleteOp); d.RowsAffected() != int64(len(want)) {
				t.Errorf("RowsAffected=%d want %d", d.RowsAffected(), len(want))
			}
			_ = op.Close()

			remaining := rangeDMLRemaining(t, ctx, "ridx_del")
			for id := int64(1); id <= 10; id++ {
				_, stillThere := remaining[id]
				if want[id] {
					if stillThere {
						t.Errorf("id=%d: deleted row still present", id)
					}
					continue
				}
				if !stillThere {
					t.Errorf("id=%d: untouched row was deleted", id)
					continue
				}
				gotLabel := remaining[id][0].(string)
				wantLabel := "l" + itoaTest(id)
				if gotLabel != wantLabel {
					t.Errorf("id=%d: label=%q want %q (untouched row mutated)", id, gotLabel, wantLabel)
				}
			}
		})
	}
}

// TestRangeIndexScanUpdatePredicate: sibling of
// TestRangeIndexScanDeletePredicate — UPDATE ... SET qty = 0 WHERE
// id <op> K over the same bare-IndexScan shapes. Both DML operators
// share extractScan/indexScanPredicate (Hard-won Rule #2); a fix
// verified only against DELETE proves nothing about UPDATE.
func TestRangeIndexScanUpdatePredicate(t *testing.T) {
	for _, c := range rangeDMLCases() {
		t.Run(c.name, func(t *testing.T) {
			ctx, _, cleanup := newDDLFixture(t)
			defer cleanup()
			rangeDMLSeed(t, ctx, "ridx_upd")

			sql := "UPDATE ridx_upd SET qty = 0 WHERE " + c.whereClause
			plan := planDMLStmt(t, ctx, sql)
			upd, ok := plan.(*optimizer.Update)
			if !ok {
				t.Fatalf("Plan(%q) = %T, want *optimizer.Update", sql, plan)
			}
			assertIndexScanChild(t, sql, upd.Child, c.requireBare)

			op := execDMLPlan(t, ctx, sql, plan)
			want := wantSet(c.wantAffectedIDs)
			if u := op.(*updateOp); u.RowsAffected() != int64(len(want)) {
				t.Errorf("RowsAffected=%d want %d", u.RowsAffected(), len(want))
			}
			_ = op.Close()

			remaining := rangeDMLRemaining(t, ctx, "ridx_upd")
			if len(remaining) != 10 {
				t.Fatalf("row count=%d want 10 (UPDATE must not drop rows)", len(remaining))
			}
			for id := int64(1); id <= 10; id++ {
				got := remaining[id]
				gotLabel := got[0].(string)
				wantLabel := "l" + itoaTest(id)
				if gotLabel != wantLabel {
					t.Errorf("id=%d: label=%q want %q (UPDATE touched an untouched column)", id, gotLabel, wantLabel)
				}
				gotQty := got[1].(int64)
				if want[id] {
					if gotQty != 0 {
						t.Errorf("id=%d: qty=%d want 0 (matching row not updated)", id, gotQty)
					}
				} else if gotQty != id*10 {
					t.Errorf("id=%d: qty=%d want %d (untouched row mutated)", id, gotQty, id*10)
				}
			}
		})
	}
}

// TestMultiColumnIndexScanDMLPredicate: DELETE/UPDATE over a bare
// *optimizer.IndexScan carrying a multi-column equality probe (Keys,
// binding Index.Columns[i] in order) — the ix.Keys branch added
// alongside the range fix, same nil-predicate over-match hazard. The
// plan is obtained from a real equality-planned IndexScan (so its
// unexported output schema is populated by the planner exactly as a
// composite-key probe's would be) and then mutated onto the composite
// index: Key -> nil, Keys -> [id, qty] literals, Index -> the
// (id, qty) index. This pins indexScanPredicate's Keys branch without
// depending on the cost model choosing a composite-key scan (which
// this planner generation may or may not do for a 10-row table).
func TestMultiColumnIndexScanDMLPredicate(t *testing.T) {
	newMultiKeyIndexScan := func(t *testing.T, ctx *Context, table string) *optimizer.IndexScan {
		t.Helper()
		sql := "DELETE FROM " + table + " WHERE id = 5"
		plan := planDMLStmt(t, ctx, sql)
		del, ok := plan.(*optimizer.Delete)
		if !ok {
			t.Fatalf("Plan(%q) = %T, want *optimizer.Delete", sql, plan)
		}
		ix, ok := del.Child.(*optimizer.IndexScan)
		if !ok {
			t.Fatalf("Plan(%q): child = %T, want *optimizer.IndexScan", sql, del.Child)
		}
		multiIdx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: table + "_id_qty_idx"})
		if !ok {
			t.Fatalf("index %s_id_qty_idx not found", table)
		}
		ix.Key = nil
		ix.Index = multiIdx
		ix.Keys = []optimizer.Expr{&optimizer.IntegerConst{Value: 5}, &optimizer.IntegerConst{Value: 50}}
		return ix
	}

	t.Run("delete", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()
		rangeDMLSeed(t, ctx, "ridx_multi_del")

		ix := newMultiKeyIndexScan(t, ctx, "ridx_multi_del")
		del := &optimizer.Delete{Table: ix.Table, Child: ix}
		op := execDMLPlan(t, ctx, "DELETE (multi-key)", del)
		if d := op.(*deleteOp); d.RowsAffected() != 1 {
			t.Errorf("RowsAffected=%d want 1", d.RowsAffected())
		}
		_ = op.Close()

		remaining := rangeDMLRemaining(t, ctx, "ridx_multi_del")
		if len(remaining) != 9 {
			t.Fatalf("row count=%d want 9", len(remaining))
		}
		if _, ok := remaining[5]; ok {
			t.Errorf("id=5 should have been deleted")
		}
	})

	t.Run("update", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()
		rangeDMLSeed(t, ctx, "ridx_multi_upd")

		ix := newMultiKeyIndexScan(t, ctx, "ridx_multi_upd")
		upd := &optimizer.Update{
			Table: ix.Table,
			Child: ix,
			Set:   []optimizer.Expr{nil, &optimizer.StringConst{Value: "updated"}, nil},
		}
		op := execDMLPlan(t, ctx, "UPDATE (multi-key)", upd)
		if u := op.(*updateOp); u.RowsAffected() != 1 {
			t.Errorf("RowsAffected=%d want 1", u.RowsAffected())
		}
		_ = op.Close()

		remaining := rangeDMLRemaining(t, ctx, "ridx_multi_upd")
		if len(remaining) != 10 {
			t.Fatalf("row count=%d want 10", len(remaining))
		}
		for id := int64(1); id <= 10; id++ {
			gotLabel := remaining[id][0].(string)
			wantLabel := "l" + itoaTest(id)
			if id == 5 {
				wantLabel = "updated"
			}
			if gotLabel != wantLabel {
				t.Errorf("id=%d: label=%q want %q", id, gotLabel, wantLabel)
			}
		}
	})
}
