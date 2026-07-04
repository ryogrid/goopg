package executor

// view_dml_test.go pins the auto-updatable-view DML rewrite + WITH CHECK
// OPTION enforcement fix (M0119-0004 slice-365 follow-up).
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

// TestNonUpdatableViewDMLRejected confirms that views outside the restricted
// auto-updatable subset (aggregates, joins, renamed columns) are rejected at
// plan time with 55000 — never silently accepted with lost writes, which was
// the pre-fix behavior for every view regardless of shape.
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
	must("CREATE VIEW vren (xid, xval) AS SELECT id, val FROM t6")

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
	if pe := planErr("INSERT INTO vren VALUES (9, 9)"); pe.Code != "55000" {
		t.Errorf("INSERT into renamed-column view: code=%q, want 55000", pe.Code)
	}

	// Base table must be untouched by every rejected attempt.
	rows := runQueryRows(t, ctx, "SELECT id FROM t6")
	if len(rows) != 1 {
		t.Fatalf("rejected DML must not have mutated the base table, got %v", rows)
	}
}
