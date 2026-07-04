package executor

// view_dml_test.go pins the auto-updatable-view DML rewrite + WITH CHECK
// OPTION enforcement fix (M0119-0004 slice-365 follow-up), including
// view-of-view chaining (root-0025 deferred item 2).
//
// Before this fix, INSERT/UPDATE/DELETE against ANY view (planInsert/
// planUpdate/planDelete never checked catalog.Table.View) silently wrote
// through to storage keyed on the view's own OID — a location no SELECT
// (which always substitutes the view's defining query) ever reads back.
// `INSERT INTO v VALUES (...)` reported success (`INSERT 0 1`) while the row
// vanished, and `UPDATE v SET ... WHERE ...` reported `UPDATE 0` even when a
// matching row existed. These tests confirm the base-table rewrite for
// simple single-relation passthrough views, CHECK OPTION enforcement
// (SQLSTATE 44000, matching PostgreSQL's ERRCODE_WITH_CHECK_OPTION_VIOLATION),
// and that anything outside that restricted auto-updatable subset is
// rejected (55000) rather than silently corrupting data.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

func TestUpdatableViewInsertUpdateDeleteRewriteToBase(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE t2 (id int primary key, name text)")
	must("INSERT INTO t2 VALUES (1, 'a'), (2, 'b')")
	must("CREATE VIEW v2 AS SELECT * FROM t2")

	must("INSERT INTO v2 VALUES (3, 'c')")
	rows := runQueryRows(t, ctx, "SELECT id, name FROM t2 ORDER BY id")
	if len(rows) != 3 {
		t.Fatalf("after INSERT INTO v2: got %d base rows, want 3", len(rows))
	}
	if got := rows[2][1].Format(); got != "c" {
		t.Errorf("inserted row name = %q, want %q", got, "c")
	}

	must("UPDATE v2 SET name = 'bb' WHERE id = 2")
	rows = runQueryRows(t, ctx, "SELECT name FROM t2 WHERE id = 2")
	if len(rows) != 1 || rows[0][0].Format() != "bb" {
		t.Fatalf("after UPDATE through v2: rows=%v, want name=bb", rows)
	}

	must("DELETE FROM v2 WHERE id = 1")
	rows = runQueryRows(t, ctx, "SELECT id FROM t2 ORDER BY id")
	if len(rows) != 2 {
		t.Fatalf("after DELETE through v2: got %d base rows, want 2", len(rows))
	}
}

func TestUpdatableViewWhereQualRestrictsUpdateDeleteTargets(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE t3 (id int primary key, val int)")
	must("INSERT INTO t3 VALUES (1, 10), (2, 20), (3, 30)")
	must("CREATE VIEW v3 AS SELECT id, val FROM t3 WHERE val > 15")

	// id=1 (val=10) is not visible through v3 — an UPDATE/DELETE naming it
	// must touch zero rows even though the base row exists.
	if err := runDDL(t, ctx, "UPDATE v3 SET val = 999 WHERE id = 1"); err != nil {
		t.Fatalf("UPDATE v3 id=1: %v", err)
	}
	rows := runQueryRows(t, ctx, "SELECT val FROM t3 WHERE id = 1")
	if len(rows) != 1 || rows[0][0].Format() != "10" {
		t.Fatalf("row id=1 should be unchanged (not visible through v3), got %v", rows)
	}

	if err := runDDL(t, ctx, "DELETE FROM v3 WHERE id = 1"); err != nil {
		t.Fatalf("DELETE v3 id=1: %v", err)
	}
	rows = runQueryRows(t, ctx, "SELECT id FROM t3 WHERE id = 1")
	if len(rows) != 1 {
		t.Fatalf("row id=1 should survive DELETE FROM v3 (not visible through view), got %v", rows)
	}
}

func TestInsertViewCheckOptionViolation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE t4 (id int primary key, val int)")
	must("CREATE VIEW v4 AS SELECT id, val FROM t4 WHERE val > 15 WITH CHECK OPTION")

	err := runDDL(t, ctx, "INSERT INTO v4 VALUES (1, 5)")
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "44000" {
		t.Fatalf("INSERT violating CHECK OPTION: err=%v, want ExecError 44000", err)
	}
	rows := runQueryRows(t, ctx, "SELECT id FROM t4")
	if len(rows) != 0 {
		t.Fatalf("rejected INSERT must not have written a row, got %v", rows)
	}

	must("INSERT INTO v4 VALUES (2, 25)")
	rows = runQueryRows(t, ctx, "SELECT id, val FROM t4")
	if len(rows) != 1 || rows[0][1].Format() != "25" {
		t.Fatalf("valid INSERT through CHECK OPTION view: got %v", rows)
	}
}

func TestUpdateViewCheckOptionViolation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE t5 (id int primary key, val int)")
	must("INSERT INTO t5 VALUES (1, 20)")
	must("CREATE VIEW v5 AS SELECT id, val FROM t5 WHERE val > 15 WITH CHECK OPTION")

	// Setting val=1 would remove the row from the view's own qual —
	// PostgreSQL rejects this exactly like the INSERT case.
	err := runDDL(t, ctx, "UPDATE v5 SET val = 1 WHERE id = 1")
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "44000" {
		t.Fatalf("UPDATE violating CHECK OPTION: err=%v, want ExecError 44000", err)
	}
	rows := runQueryRows(t, ctx, "SELECT val FROM t5 WHERE id = 1")
	if len(rows) != 1 || rows[0][0].Format() != "20" {
		t.Fatalf("rejected UPDATE must leave the row unchanged, got %v", rows)
	}

	must("UPDATE v5 SET val = 21 WHERE id = 1")
	rows = runQueryRows(t, ctx, "SELECT val FROM t5 WHERE id = 1")
	if len(rows) != 1 || rows[0][0].Format() != "21" {
		t.Fatalf("valid UPDATE through CHECK OPTION view: got %v", rows)
	}
}

// TestUpdatableViewWhereQualEnforcedThroughIndexPath pins the root-0025
// follow-up fix: updateOp.updateViaIndex (operators_storage.go) previously
// only evaluated the B-tree index's own equality key on its initial scan
// pass and never consulted the residual predicate (o.pred) — so a view's own
// WHERE qual, ANDed onto an IndexScan-eligible `WHERE <pk> = ...` UPDATE, was
// silently unenforced outside of an EPQ concurrent-update recheck. The
// original fix worked around this by forcing planUpdate to always fall back
// to SeqScan+Filter whenever a view qual was present, giving up the index
// probe. That workaround is gone: planUpdate now takes the index path
// unconditionally and folds the view qual into the same Filter layer
// extractScan already merges with the index's synthesised equality
// predicate, and updateViaIndex evaluates the combined o.pred itself. This
// test confirms both that the planner actually chooses the index path for
// this shape and that the view qual is still enforced through it.
func TestUpdatableViewWhereQualEnforcedThroughIndexPath(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE t7 (id int primary key, val int)")
	must("INSERT INTO t7 VALUES (1, 10), (2, 20)")
	must("CREATE VIEW v7 AS SELECT id, val FROM t7 WHERE val > 15")

	// Confirm the planner picked the index-driven path (Update.Child is a
	// Filter wrapping an IndexScan, not a plain SeqScan) for this
	// `WHERE <pk> = ...` shape — otherwise this test would not actually be
	// exercising updateViaIndex.
	stmts, err := parser.Parse("UPDATE v7 SET val = 999 WHERE id = 1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := planner.Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	upd, ok := plan.(*planner.Update)
	if !ok {
		t.Fatalf("plan type = %T, want *planner.Update", plan)
	}
	filt, ok := upd.Child.(*planner.Filter)
	if !ok {
		t.Fatalf("Update.Child type = %T, want *planner.Filter (view qual)", upd.Child)
	}
	if _, ok := filt.Child.(*planner.IndexScan); !ok {
		t.Fatalf("Filter.Child type = %T, want *planner.IndexScan — index path not chosen", filt.Child)
	}

	// id=1 (val=10) is not visible through v7's own qual — even though the
	// PK equality lookup finds it via the index, the UPDATE must not apply.
	must("UPDATE v7 SET val = 999 WHERE id = 1")
	rows := runQueryRows(t, ctx, "SELECT val FROM t7 WHERE id = 1")
	if len(rows) != 1 || rows[0][0].Format() != "10" {
		t.Fatalf("row id=1 should be unchanged (excluded by view qual via index path), got %v", rows)
	}

	// id=2 (val=20) IS visible through v7 — the same index-path UPDATE must
	// apply normally.
	must("UPDATE v7 SET val = 25 WHERE id = 2")
	rows = runQueryRows(t, ctx, "SELECT val FROM t7 WHERE id = 2")
	if len(rows) != 1 || rows[0][0].Format() != "25" {
		t.Fatalf("row id=2 should be updated (visible through view qual), got %v", rows)
	}
}

// TestChainedViewInsertUpdateDeleteRewriteToBase pins root-0025 deferred item
// 2 (view-of-view chaining): a simple auto-updatable view defined FROM
// another simple auto-updatable view rewrites INSERT/UPDATE/DELETE all the
// way down to the real base table, and the row-visibility qual from EVERY
// level in the chain (not just the outermost) restricts which rows
// UPDATE/DELETE can touch — mirroring PostgreSQL's recursive
// rewriteHandler.c walk.
func TestChainedViewInsertUpdateDeleteRewriteToBase(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE tc (id int primary key, val int)")
	must("INSERT INTO tc VALUES (1, 5), (2, 50), (3, 150)")
	must("CREATE VIEW v_in AS SELECT id, val FROM tc WHERE val > 10")
	must("CREATE VIEW v_out AS SELECT id, val FROM v_in WHERE val < 100")

	// INSERT rewrites through both levels onto tc, regardless of whether the
	// row satisfies either level's qual (no CHECK OPTION anywhere here).
	must("INSERT INTO v_out VALUES (4, 20)")
	rows := runQueryRows(t, ctx, "SELECT val FROM tc WHERE id = 4")
	if len(rows) != 1 || rows[0][0].Format() != "20" {
		t.Fatalf("after INSERT INTO v_out (chained): got %v, want val=20", rows)
	}

	// id=1 (val=5) fails v_in's own qual (val>10) -> excluded at the inner
	// level even though it would pass v_out's own qual (val<100).
	must("UPDATE v_out SET val = 999 WHERE id = 1")
	rows = runQueryRows(t, ctx, "SELECT val FROM tc WHERE id = 1")
	if len(rows) != 1 || rows[0][0].Format() != "5" {
		t.Fatalf("row id=1 should be unchanged (excluded by inner view's qual), got %v", rows)
	}

	// id=3 (val=150) passes v_in's qual (150>10) but fails v_out's own qual
	// (150<100) -> excluded at the outer level.
	must("UPDATE v_out SET val = 999 WHERE id = 3")
	rows = runQueryRows(t, ctx, "SELECT val FROM tc WHERE id = 3")
	if len(rows) != 1 || rows[0][0].Format() != "150" {
		t.Fatalf("row id=3 should be unchanged (excluded by outer view's qual), got %v", rows)
	}

	// id=2 (val=50) passes both levels' quals -> UPDATE/DELETE apply.
	must("UPDATE v_out SET val = 60 WHERE id = 2")
	rows = runQueryRows(t, ctx, "SELECT val FROM tc WHERE id = 2")
	if len(rows) != 1 || rows[0][0].Format() != "60" {
		t.Fatalf("row id=2 should be updated (visible through both levels), got %v", rows)
	}
	must("DELETE FROM v_out WHERE id = 2")
	rows = runQueryRows(t, ctx, "SELECT id FROM tc WHERE id = 2")
	if len(rows) != 0 {
		t.Fatalf("row id=2 should be deleted (visible through both levels), got %v", rows)
	}
}

// TestChainedViewCheckOptionCascadeReachesInnerView pins the CASCADED half of
// root-0025 deferred item 2: an outer view's (default, or explicit CASCADED)
// CHECK OPTION forces the inner view's own qual to be checked too, even
// though the inner view declares no CHECK OPTION of its own — matching
// rewriteHandler.c's "if the parent view has a cascaded check option, treat
// this view as if it also had a cascaded check option" propagation.
func TestChainedViewCheckOptionCascadeReachesInnerView(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE tc2 (id int primary key, val int)")
	must("CREATE VIEW v_in2 AS SELECT id, val FROM tc2 WHERE val > 10")
	// Default (unqualified WITH CHECK OPTION) is CASCADED.
	must("CREATE VIEW v_out2 AS SELECT id, val FROM v_in2 WHERE val < 100 WITH CHECK OPTION")

	// val=5 satisfies v_out2's own qual (5<100) but violates v_in2's qual
	// (5 is not >10). Without cascading this loop's own single-level check
	// would have wrongly accepted it; CASCADED must reject it.
	err := runDDL(t, ctx, "INSERT INTO v_out2 VALUES (1, 5)")
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "44000" {
		t.Fatalf("INSERT violating cascaded inner qual: err=%v, want ExecError 44000", err)
	}
	rows := runQueryRows(t, ctx, "SELECT id FROM tc2")
	if len(rows) != 0 {
		t.Fatalf("rejected INSERT must not have written a row, got %v", rows)
	}

	// val=150 violates v_out2's own qual directly.
	err = runDDL(t, ctx, "INSERT INTO v_out2 VALUES (2, 150)")
	ee, ok = err.(*ExecError)
	if !ok || ee.Code != "44000" {
		t.Fatalf("INSERT violating outer qual: err=%v, want ExecError 44000", err)
	}

	// val=50 satisfies both levels.
	must("INSERT INTO v_out2 VALUES (3, 50)")
	rows = runQueryRows(t, ctx, "SELECT id, val FROM tc2")
	if len(rows) != 1 || rows[0][1].Format() != "50" {
		t.Fatalf("valid chained CHECK OPTION INSERT: got %v", rows)
	}
}

// TestChainedViewCheckOptionLocalDoesNotForceInnerCheck pins the LOCAL half
// of root-0025 deferred item 2: an outer view's WITH LOCAL CHECK OPTION only
// checks its own qual — it must NOT force the inner view's qual to be
// checked when the inner view has no CHECK OPTION of its own.
func TestChainedViewCheckOptionLocalDoesNotForceInnerCheck(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE tc3 (id int primary key, val int)")
	must("CREATE VIEW v_in3 AS SELECT id, val FROM tc3 WHERE val > 10")
	must("CREATE VIEW v_out3 AS SELECT id, val FROM v_in3 WHERE val < 100 WITH LOCAL CHECK OPTION")

	// val=5 violates v_in3's qual (not >10) but satisfies v_out3's own qual
	// (5<100). LOCAL must NOT force the inner check, so this INSERT succeeds
	// even though the row is immediately invisible through both views.
	must("INSERT INTO v_out3 VALUES (1, 5)")
	rows := runQueryRows(t, ctx, "SELECT val FROM tc3 WHERE id = 1")
	if len(rows) != 1 || rows[0][0].Format() != "5" {
		t.Fatalf("LOCAL check option must not enforce inner qual, got %v", rows)
	}

	// val=150 violates v_out3's own qual directly -> still rejected.
	err := runDDL(t, ctx, "INSERT INTO v_out3 VALUES (2, 150)")
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "44000" {
		t.Fatalf("INSERT violating outer's own LOCAL qual: err=%v, want ExecError 44000", err)
	}
}

// TestChainedViewCheckOptionInnerEnforcedRegardlessOfOuter pins the other
// direction: an inner view's OWN CHECK OPTION is enforced even when the
// outer view (the one the DML statement actually names) has no CHECK OPTION
// at all — PostgreSQL checks every view in the chain that itself declares
// CHECK OPTION, independent of the outer view's setting.
func TestChainedViewCheckOptionInnerEnforcedRegardlessOfOuter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE tc4 (id int primary key, val int)")
	must("CREATE VIEW v_in4 AS SELECT id, val FROM tc4 WHERE val > 10 WITH CHECK OPTION")
	must("CREATE VIEW v_out4 AS SELECT id, val FROM v_in4 WHERE val < 100")

	// val=5 violates v_in4's own CHECK OPTION qual — enforced even though
	// v_out4 (the DML target) declares no CHECK OPTION itself.
	err := runDDL(t, ctx, "INSERT INTO v_out4 VALUES (1, 5)")
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "44000" {
		t.Fatalf("INSERT violating inner's own CHECK OPTION: err=%v, want ExecError 44000", err)
	}
	rows := runQueryRows(t, ctx, "SELECT id FROM tc4")
	if len(rows) != 0 {
		t.Fatalf("rejected INSERT must not have written a row, got %v", rows)
	}

	// val=200 satisfies v_in4's qual (200>10) but fails v_out4's own qual
	// (200<100). v_out4 has no CHECK OPTION, so this is not enforced at
	// INSERT time — the row is written even though it's invisible through
	// v_out4 immediately afterward.
	must("INSERT INTO v_out4 VALUES (2, 200)")
	rows = runQueryRows(t, ctx, "SELECT val FROM tc4 WHERE id = 2")
	if len(rows) != 1 || rows[0][0].Format() != "200" {
		t.Fatalf("outer-unchecked qual violation should still write, got %v", rows)
	}
}

// TestChainedViewInnerNotAutoUpdatableRejectsWholeChain confirms that when
// the inner view of a chain falls outside the restricted auto-updatable
// subset (e.g. it aggregates), the whole chain is rejected 55000 rather than
// silently rewriting past it.
func TestChainedViewInnerNotAutoUpdatableRejectsWholeChain(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE tc5 (id int primary key, val int)")
	must("CREATE VIEW v_in5 AS SELECT id, count(*) AS val FROM tc5 GROUP BY id")
	must("CREATE VIEW v_out5 AS SELECT id, val FROM v_in5")

	stmts, err := parser.Parse("INSERT INTO v_out5 VALUES (1, 1)")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = planner.Plan(stmts[0], cat)
	pe, ok := err.(*planner.PlanError)
	if !ok || pe.Code != "55000" {
		t.Fatalf("INSERT through a chain with a non-updatable inner view: err=%v, want *planner.PlanError 55000", err)
	}
}

// TestNonUpdatableViewDMLRejected confirms that views outside the restricted
// auto-updatable subset (aggregates, joins, expression columns) are rejected
// at plan time with 55000 — never silently accepted with lost writes, which
// was the pre-fix behavior for every view regardless of shape. A view that
// merely renames/reorders/subsets a plain column-reference target list is
// NOT in this category — see TestUpdatableViewColumnSubsetReorderRename.
func TestNonUpdatableViewDMLRejected(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE t6 (id int primary key, val int)")
	must("INSERT INTO t6 VALUES (1, 10)")
	must("CREATE VIEW vagg AS SELECT id, count(*) FROM t6 GROUP BY id")
	must("CREATE VIEW vexpr AS SELECT id, val + 1 AS valplus FROM t6")

	planErr := func(sql string) *planner.PlanError {
		t.Helper()
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", sql, err)
		}
		_, err = planner.Plan(stmts[0], cat)
		pe, ok := err.(*planner.PlanError)
		if !ok {
			t.Fatalf("Plan(%q) err=%v, want *planner.PlanError", sql, err)
		}
		return pe
	}

	if pe := planErr("INSERT INTO vagg VALUES (1, 1)"); pe.Code != "55000" {
		t.Errorf("INSERT into aggregate view: code=%q, want 55000", pe.Code)
	}
	if pe := planErr("UPDATE vagg SET id = 2 WHERE id = 1"); pe.Code != "55000" {
		t.Errorf("UPDATE aggregate view: code=%q, want 55000", pe.Code)
	}
	if pe := planErr("DELETE FROM vagg WHERE id = 1"); pe.Code != "55000" {
		t.Errorf("DELETE FROM aggregate view: code=%q, want 55000", pe.Code)
	}
	if pe := planErr("INSERT INTO vexpr VALUES (9, 9)"); pe.Code != "55000" {
		t.Errorf("INSERT into expression-column view: code=%q, want 55000", pe.Code)
	}

	// Base table must be untouched by every rejected attempt.
	rows := runQueryRows(t, ctx, "SELECT id FROM t6")
	if len(rows) != 1 {
		t.Fatalf("rejected DML must not have mutated the base table, got %v", rows)
	}
}

// TestUpdatableViewColumnSubsetReorderRename pins root-0025 deferred item 1:
// a simple auto-updatable view may rename (either via a per-target AS alias
// or an explicit `CREATE VIEW v (a, b)` column list), reorder, and/or expose
// only a subset of its base relation's columns — PostgreSQL's rule only
// requires each view column to be a plain, unqualified reference to a base
// column, not that every base column appear once in order. Before this fix
// all three shapes were rejected 55000 by the stricter `tbl.Columns ==
// base.Columns` positional-identity check.
func TestUpdatableViewColumnSubsetReorderRename(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	// Rename via per-target AS alias.
	must("CREATE TABLE tr1 (id int primary key, val int)")
	must("INSERT INTO tr1 VALUES (1, 10)")
	must("CREATE VIEW vren1 AS SELECT id, val AS renamed_val FROM tr1")
	must("INSERT INTO vren1 (id, renamed_val) VALUES (2, 20)")
	must("UPDATE vren1 SET renamed_val = 99 WHERE id = 1")
	rows := runQueryRows(t, ctx, "SELECT id, val FROM tr1 ORDER BY id")
	if len(rows) != 2 || rows[0][1].Format() != "99" || rows[1][1].Format() != "20" {
		t.Fatalf("rename via AS alias: got %v, want [[1 99] [2 20]]", rows)
	}
	if err := runDDL(t, ctx, "DELETE FROM vren1 WHERE renamed_val = 99"); err != nil {
		t.Fatalf("DELETE FROM vren1 WHERE renamed_val: %v", err)
	}
	rows = runQueryRows(t, ctx, "SELECT id FROM tr1")
	if len(rows) != 1 || rows[0][0].Format() != "2" {
		t.Fatalf("after DELETE via renamed column: got %v, want just id=2", rows)
	}

	// Rename via an explicit CREATE VIEW column-name list.
	must("CREATE TABLE tr2 (id int primary key, val int)")
	must("INSERT INTO tr2 VALUES (1, 10)")
	must("CREATE VIEW vren2 (xid, xval) AS SELECT id, val FROM tr2")
	must("INSERT INTO vren2 VALUES (2, 20)") // no explicit column list: view's own order
	must("UPDATE vren2 SET xval = 999 WHERE xid = 1")
	rows = runQueryRows(t, ctx, "SELECT id, val FROM tr2 ORDER BY id")
	if len(rows) != 2 || rows[0][1].Format() != "999" || rows[1][1].Format() != "20" {
		t.Fatalf("rename via CREATE VIEW column list: got %v, want [[1 999] [2 20]]", rows)
	}

	// Reorder: the view's target list swaps the base relation's column
	// order, so a column-list-free INSERT must map source values through the
	// VIEW's own order, not base's physical order.
	must("CREATE TABLE tr3 (id int primary key, val int)")
	must("CREATE VIEW vswap AS SELECT val, id FROM tr3")
	must("INSERT INTO vswap VALUES (30, 3)") // val=30, id=3 in view order
	rows = runQueryRows(t, ctx, "SELECT id, val FROM tr3")
	if len(rows) != 1 || rows[0][0].Format() != "3" || rows[0][1].Format() != "30" {
		t.Fatalf("reordered view INSERT: got %v, want id=3 val=30", rows)
	}

	// Subset: the view exposes only `id`; `val` is not part of its row type
	// at all, so referencing it through the view is 42703, and an INSERT
	// with no column list only supplies the exposed column (the excluded
	// column falls back to its default/NULL, mirroring a plain INSERT that
	// omits a column).
	must("CREATE TABLE tr4 (id int primary key, val int)")
	must("CREATE VIEW vsub AS SELECT id FROM tr4")
	must("INSERT INTO vsub VALUES (4)")
	rows = runQueryRows(t, ctx, "SELECT id, val FROM tr4")
	if len(rows) != 1 || rows[0][0].Format() != "4" || !rows[0][1].IsNull() {
		t.Fatalf("subset view INSERT: got %v, want id=4 val=NULL", rows)
	}
	stmts, err := parser.Parse("UPDATE vsub SET val = 1 WHERE id = 4")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = planner.Plan(stmts[0], ctx.Catalog)
	pe, ok := err.(*planner.PlanError)
	if !ok || pe.Code != "42703" {
		t.Fatalf("UPDATE vsub SET val: err=%v, want *planner.PlanError 42703 (val is not part of vsub's row type)", err)
	}
}

// TestUpdatableViewUpdateFromDeleteUsing pins root-0025 deferred item 3:
// `UPDATE ... FROM` and `DELETE ... USING` against a simple auto-updatable
// view now rewrite onto the view's base relation exactly like the
// FROM/USING-free forms, instead of being rejected 55000 unconditionally.
// The view's own WHERE qual (and CHECK OPTION, for UPDATE) is still
// enforced against the combined cross-product row, and the view's own
// (possibly renamed) column vocabulary resolves in SET/WHERE/RETURNING via
// the same viewProxyTable machinery the FROM/USING-free path already uses.
func TestUpdatableViewUpdateFromDeleteUsing(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	must := func(sql string) {
		t.Helper()
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must("CREATE TABLE tf1 (id int primary key, amount int)")
	must("CREATE TABLE tf2 (id int primary key, bump int)")
	must("INSERT INTO tf1 VALUES (1, 100), (2, 200)")
	must("INSERT INTO tf2 VALUES (1, 5), (2, 999)")
	// Renamed column + a WHERE qual restricting the view to id=1 only, so
	// row id=2 must never be touched even though tf2 supplies a matching
	// join row for it too.
	must("CREATE VIEW vf1 AS SELECT id, amount AS amt FROM tf1 WHERE id = 1")

	must("UPDATE vf1 SET amt = amt + bump FROM tf2 WHERE vf1.id = tf2.id")
	rows := runQueryRows(t, ctx, "SELECT id, amount FROM tf1 ORDER BY id")
	if len(rows) != 2 || rows[0][1].Format() != "105" || rows[1][1].Format() != "200" {
		t.Fatalf("UPDATE view FROM: got %v, want [[1 105] [2 200]] (id=2 outside the view's own qual must be untouched)", rows)
	}

	must("CREATE TABLE td1 (id int primary key, val int)")
	must("CREATE TABLE td2 (id int primary key)")
	must("INSERT INTO td1 VALUES (1, 1), (2, 2)")
	must("INSERT INTO td2 VALUES (1), (2)")
	must("CREATE VIEW vd1 AS SELECT id AS xid, val FROM td1 WHERE val = 1")

	must("DELETE FROM vd1 USING td2 WHERE vd1.xid = td2.id")
	rows = runQueryRows(t, ctx, "SELECT id FROM td1")
	if len(rows) != 1 || rows[0][0].Format() != "2" {
		t.Fatalf("DELETE view USING: got %v, want just id=2 (id=1 matched WHERE and the view's own qual)", rows)
	}

	// CHECK OPTION must still be enforced through UPDATE ... FROM.
	must("CREATE TABLE tc1 (id int primary key, val int)")
	must("INSERT INTO tc1 VALUES (1, 20)")
	must("CREATE VIEW vc1 AS SELECT id, val FROM tc1 WHERE val > 15 WITH CHECK OPTION")
	must("CREATE TABLE tc2 (id int primary key, newval int)")
	must("INSERT INTO tc2 VALUES (1, 1)")
	err := runDDL(t, ctx, "UPDATE vc1 SET val = tc2.newval FROM tc2 WHERE vc1.id = tc2.id")
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "44000" {
		t.Fatalf("UPDATE view FROM violating CHECK OPTION: err=%v, want ExecError 44000", err)
	}
	rows = runQueryRows(t, ctx, "SELECT val FROM tc1 WHERE id = 1")
	if len(rows) != 1 || rows[0][0].Format() != "20" {
		t.Fatalf("rejected UPDATE...FROM must leave the row unchanged, got %v", rows)
	}

	// A view outside the auto-updatable subset (aggregation) stays rejected
	// 55000 for UPDATE ... FROM / DELETE ... USING too, not just the
	// FROM/USING-free forms.
	must("CREATE TABLE ta1 (id int primary key, val int)")
	must("CREATE TABLE ta2 (id int primary key)")
	must("INSERT INTO ta1 VALUES (1, 10)")
	must("INSERT INTO ta2 VALUES (1)")
	must("CREATE VIEW vagg1 AS SELECT id, count(*) AS c FROM ta1 GROUP BY id")
	stmts, perr := parser.Parse("UPDATE vagg1 SET id = 2 FROM ta2 WHERE vagg1.id = ta2.id")
	if perr != nil {
		t.Fatalf("Parse: %v", perr)
	}
	_, perr = planner.Plan(stmts[0], ctx.Catalog)
	pe, ok := perr.(*planner.PlanError)
	if !ok || pe.Code != "55000" {
		t.Fatalf("UPDATE aggregate view FROM: err=%v, want *planner.PlanError 55000", perr)
	}
	stmts, perr = parser.Parse("DELETE FROM vagg1 USING ta2 WHERE vagg1.id = ta2.id")
	if perr != nil {
		t.Fatalf("Parse: %v", perr)
	}
	_, perr = planner.Plan(stmts[0], ctx.Catalog)
	pe, ok = perr.(*planner.PlanError)
	if !ok || pe.Code != "55000" {
		t.Fatalf("DELETE aggregate view USING: err=%v, want *planner.PlanError 55000", perr)
	}
}
