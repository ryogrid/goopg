package executor

// operators_alter_view_relation_ops_test.go pins the DU-002 slice 440 fix:
// `ALTER VIEW name RENAME TO / OWNER TO / SET SCHEMA` previously had no
// dedicated parser case at all — ALTER VIEW fell into the blanket
// "schema/view/collation/..." compat-stub loop, which consumed the entire
// statement and returned a bare no-op AlterTableStmt. That silently
// discarded the RENAME/OWNER/SET SCHEMA change entirely (not just a
// mistagging bug — the view was never actually renamed/re-owned/moved).
// Real PostgreSQL treats a view as an ordinary relation for these three
// forms (RenameRelation / AlterTableOwner / AlterTableNamespace,
// postgres/src/backend/commands/tablecmds.c), so — mirroring the ALTER
// SEQUENCE fix from DU-002 slice 439 — they now reuse the exact executor
// path ALTER TABLE already uses (AlterTableStmt / execAlterTable).

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestAlterViewRenameTo(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE viewsrc (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO viewsrc VALUES (1), (2)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE VIEW vx AS SELECT a FROM viewsrc"); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER VIEW vx RENAME TO vy"); err != nil {
		t.Fatalf("ALTER VIEW RENAME TO: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "vy"})
	if !ok {
		t.Fatalf("catalog has no relation named vy after rename")
	}
	if tbl.View == nil {
		t.Fatalf("renamed relation vy lost its View definition")
	}
	if _, ok := cat.LookupTable(parser.ObjectName{Name: "vx"}); ok {
		t.Errorf("catalog still has the old relation name vx after rename")
	}
	rows := runQueryRows(t, ctx, "SELECT a FROM vy")
	if len(rows) != 2 {
		t.Fatalf("SELECT a FROM vy after rename: got %d rows, want 2", len(rows))
	}
}

func TestAlterViewOwnerTo(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE viewsrc2 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE VIEW vown AS SELECT a FROM viewsrc2"); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER VIEW vown OWNER TO alice"); err != nil {
		t.Fatalf("ALTER VIEW OWNER TO: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "vown"})
	if !ok {
		t.Fatalf("catalog lost the vown relation after OWNER TO")
	}
	if tbl.Owner != "alice" {
		t.Errorf("Owner = %q, want %q", tbl.Owner, "alice")
	}
}

func TestAlterViewOwnerToCurrentUser(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE viewsrc3 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE VIEW vown2 AS SELECT a FROM viewsrc3"); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER VIEW vown2 OWNER TO CURRENT_USER"); err != nil {
		t.Fatalf("ALTER VIEW OWNER TO CURRENT_USER: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "vown2"})
	if !ok {
		t.Fatalf("catalog lost the vown2 relation after OWNER TO CURRENT_USER")
	}
	if tbl.Owner != "" {
		t.Errorf("Owner = %q, want %q (bootstrap superuser sentinel)", tbl.Owner, "")
	}
}

func TestAlterViewSetSchema(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE SCHEMA vsch1"); err != nil {
		t.Fatalf("CREATE SCHEMA: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE viewsrc4 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO viewsrc4 VALUES (1), (2), (3)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE VIEW vsch AS SELECT a FROM viewsrc4"); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER VIEW vsch SET SCHEMA vsch1"); err != nil {
		t.Fatalf("ALTER VIEW SET SCHEMA: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "vsch1", Name: "vsch"})
	if !ok {
		t.Fatalf("catalog lost the vsch relation after SET SCHEMA")
	}
	if tbl.Schema != "vsch1" {
		t.Errorf("Schema = %q, want %q", tbl.Schema, "vsch1")
	}
	if tbl.View == nil {
		t.Fatalf("moved relation vsch1.vsch lost its View definition")
	}
	rows := runQueryRows(t, ctx, "SELECT a FROM vsch1.vsch")
	if len(rows) != 3 {
		t.Fatalf("SELECT a FROM vsch1.vsch after SET SCHEMA: got %d rows, want 3", len(rows))
	}
}

// TestAlterViewRenameOwnerSchemaCommandTag confirms the CommandComplete tag
// is the PG-accurate "ALTER VIEW", not the generic "ALTER TABLE" the shared
// AlterTableStmt would otherwise report (dispatch.go's ddlTag reads
// AlterTableStmt.TagOverride).
func TestAlterViewRenameOwnerSchemaCommandTag(t *testing.T) {
	stmts := map[string]string{
		"ALTER VIEW vtag1 RENAME TO vtag2": "vtag1",
		"ALTER VIEW vtag3 OWNER TO alice":  "vtag3",
	}
	for sql, want := range stmts {
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", sql, err)
		}
		if len(stmts) != 1 {
			t.Fatalf("Parse(%q) returned %d statements, want 1", sql, len(stmts))
		}
		at, ok := stmts[0].(*parser.AlterTableStmt)
		if !ok {
			t.Fatalf("Parse(%q) = %T, want *parser.AlterTableStmt", sql, stmts[0])
		}
		if at.TagOverride != "ALTER VIEW" {
			t.Errorf("Parse(%q).TagOverride = %q, want %q", sql, at.TagOverride, "ALTER VIEW")
		}
		if at.Name.Name != want {
			t.Errorf("Parse(%q).Name.Name = %q, want %q", sql, at.Name.Name, want)
		}
	}
}
