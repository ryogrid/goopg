package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// runDMLUnderDBOid is like runDDL, but plans the statement through a
// catalog.SearchPathCatalog seeded with ctx.CurrentDatabaseOid, mirroring how
// the real server always plans (server.sessionPlanCatalog/ctxPlanCatalog) —
// unlike runDDL's raw planner.Plan(stmts[0], ctx.Catalog) call, which never
// resolves table names under any dbOid but DefaultDBOid. Needed for INSERT/
// TRUNCATE statements, whose target-table name IS resolved at planning time
// (unlike CREATE TABLE/CREATE VIEW's outer statement plan, which routing
// tests elsewhere in this package exercise via plain runDDL).
func runDMLUnderDBOid(t *testing.T, ctx *Context, sql string) error {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	planCat := catalog.WithSearchPath(ctx.Catalog, nil)
	planCat.DBOid = ctx.CurrentDatabaseOid
	plan, err := planner.Plan(stmts[0], planCat)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		return err
	}
	if _, err := op.Next(); err != EOF {
		return err
	}
	return op.Close()
}

// runQueryUnderDBOid is runQuery's counterpart to runDMLUnderDBOid — plans
// through the same dbOid-seeded SearchPathCatalog so a SELECT against a
// distinct-dbOid table resolves.
func runQueryUnderDBOid(t *testing.T, ctx *Context, sql string) []Row {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	planCat := catalog.WithSearchPath(ctx.Catalog, nil)
	planCat.DBOid = ctx.CurrentDatabaseOid
	plan, err := planner.Plan(stmts[0], planCat)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open(%q): %v", sql, err)
	}
	rows, err := drainScan(op)
	if err != nil {
		t.Fatalf("drain(%q): %v", sql, err)
	}
	_ = op.Close()
	return rows
}

// TestAssertParentExistsFindsOwnDistinctDBOidParent covers M0122-0007 slice
// 4e's first item, FK target resolution: assertParentExists (the INSERT/
// UPDATE-time FK-parent-exists check) used to call im.LookupTable(fk.RefTable)
// with no dbOid argument, which always resolved DefaultDBOid regardless of
// which database the inserting connection was bound to. On a connection whose
// CurrentDatabaseOid names a genuinely distinct database, the referenced
// table (created under that same distinct dbOid by 4d-i's CreateTable
// routing) was invisible to this lookup — ok=false — so assertParentExists
// took its "referenced table not found (CREATE TABLE out of order) — skip"
// early return and let ANY INSERT through with no FK enforcement at all,
// instead of correctly rejecting rows whose FK value has no matching parent.
func TestAssertParentExistsFindsOwnDistinctDBOidParent(t *testing.T) {
	const otherDBOid = 7001
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE parent (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE parent: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE child (id int4, pid int4 REFERENCES parent(id))"); err != nil {
		t.Fatalf("CREATE TABLE child: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO parent VALUES (1)"); err != nil {
		t.Fatalf("INSERT INTO parent: %v", err)
	}

	// pid=2 has no matching row in parent — must be rejected with 23503.
	err := runDMLUnderDBOid(t, ctx, "INSERT INTO child VALUES (100, 2)")
	if err == nil {
		t.Fatal("INSERT INTO child with a non-existent FK target unexpectedly succeeded — FK enforcement silently skipped under a distinct connection dbOid")
	}
	execErr, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if execErr.Code != "23503" {
		t.Fatalf("expected 23503, got %q: %v", execErr.Code, execErr)
	}

	// pid=1 matches parent's row and must be accepted.
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO child VALUES (101, 1)"); err != nil {
		t.Fatalf("INSERT INTO child with a valid FK target: %v", err)
	}
}

// TestExecTruncateCascadeFindsOwnDistinctDBOidReferencingTable covers M0122-
// 0007 slice 4e's FK target resolution item 3: execTruncate's CASCADE
// expansion built its whole-catalog referencer set via im.AllTables() with no
// dbOid argument, which always scanned DefaultDBOid's namespace. On a
// connection bound to a genuinely distinct dbOid, the real referencing table
// (created under that same dbOid) was invisible to the scan, so TRUNCATE ...
// CASCADE silently failed to cascade to it — leaving its rows behind,
// dangling references to the just-truncated parent.
func TestExecTruncateCascadeFindsOwnDistinctDBOidReferencingTable(t *testing.T) {
	const otherDBOid = 7002
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = otherDBOid

	if err := runDDL(t, ctx, "CREATE TABLE parent (id int4 PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE parent: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE child (id int4, pid int4 REFERENCES parent(id))"); err != nil {
		t.Fatalf("CREATE TABLE child: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO parent VALUES (1)"); err != nil {
		t.Fatalf("INSERT INTO parent: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO child VALUES (1, 1)"); err != nil {
		t.Fatalf("INSERT INTO child: %v", err)
	}

	if err := runDMLUnderDBOid(t, ctx, "TRUNCATE parent CASCADE"); err != nil {
		t.Fatalf("TRUNCATE parent CASCADE: %v", err)
	}

	rows := runQueryUnderDBOid(t, ctx, "SELECT id FROM child")
	if len(rows) != 0 {
		t.Fatalf("child has %d rows after TRUNCATE parent CASCADE, want 0 (CASCADE did not reach the connection's own distinct-dbOid referencing table)", len(rows))
	}
}
