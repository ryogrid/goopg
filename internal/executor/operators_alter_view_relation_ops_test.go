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

	"github.com/goopg/goopg/internal/catalog"
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

	cat.(*catalog.InMemory).RegisterRole("alice")
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

// TestAlterViewRenameColumn pins DU-002 slice 444: `ALTER VIEW name RENAME
// [COLUMN] old TO new` previously fell through the `&& p.acceptKeyword(KwTo)`
// short-circuit (which already consumed "RENAME") straight into the
// catch-all no-op consume loop — a silent no-op, same class of bug as the
// pre-slice-440 RENAME TO/OWNER TO/SET SCHEMA gap.
func TestAlterViewRenameColumn(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE viewsrc5 (a int, b int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO viewsrc5 VALUES (1, 2)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE VIEW vrencol AS SELECT a, b FROM viewsrc5"); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER VIEW vrencol RENAME COLUMN a TO renamed_a"); err != nil {
		t.Fatalf("ALTER VIEW RENAME COLUMN: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "vrencol"})
	if !ok {
		t.Fatalf("catalog lost the vrencol relation after RENAME COLUMN")
	}
	found := false
	for _, col := range tbl.Columns {
		if col.Name == "renamed_a" {
			found = true
		}
		if col.Name == "a" {
			t.Errorf("column %q still present after RENAME COLUMN a TO renamed_a", col.Name)
		}
	}
	if !found {
		t.Fatalf("column renamed_a not present after RENAME COLUMN")
	}
	rows := runQueryRows(t, ctx, "SELECT renamed_a, b FROM vrencol")
	if len(rows) != 1 {
		t.Fatalf("SELECT renamed_a, b FROM vrencol: got %d rows, want 1", len(rows))
	}
}

// TestAlterViewAlterColumnSetDropDefault pins DU-002 slice 444: `ALTER VIEW
// name ALTER [COLUMN] col SET DEFAULT expr` / `DROP DEFAULT` previously fell
// to the catch-all no-op consume loop.
func TestAlterViewAlterColumnSetDropDefault(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE viewsrc6 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE VIEW vdef AS SELECT a FROM viewsrc6"); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER VIEW vdef ALTER COLUMN a SET DEFAULT 42"); err != nil {
		t.Fatalf("ALTER VIEW ALTER COLUMN SET DEFAULT: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "vdef"})
	if !ok {
		t.Fatalf("catalog lost the vdef relation after SET DEFAULT")
	}
	gotDefault := false
	for _, c := range tbl.Columns {
		if c.Name == "a" && c.DefaultExpr != nil {
			gotDefault = true
		}
	}
	if !gotDefault {
		t.Fatalf("column a has no DefaultExpr after SET DEFAULT")
	}
	if err := runDDL(t, ctx, "ALTER VIEW vdef ALTER COLUMN a DROP DEFAULT"); err != nil {
		t.Fatalf("ALTER VIEW ALTER COLUMN DROP DEFAULT: %v", err)
	}
	tbl, ok = cat.LookupTable(parser.ObjectName{Name: "vdef"})
	if !ok {
		t.Fatalf("catalog lost the vdef relation after DROP DEFAULT")
	}
	for _, c := range tbl.Columns {
		if c.Name == "a" && c.DefaultExpr != nil {
			t.Errorf("column a still has a DefaultExpr after DROP DEFAULT")
		}
	}
}

// TestAlterViewSetResetReloptions pins DU-002 slice 444: `ALTER VIEW name SET
// (option = value, ...)` / `RESET (option, ...)` previously fell to the
// catch-all no-op consume loop (checked after SET SCHEMA, which matches on
// the literal "schema" identifier rather than "(", so the two forms never
// collide).
func TestAlterViewSetResetReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE viewsrc7 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE VIEW vopt AS SELECT a FROM viewsrc7"); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER VIEW vopt SET (security_barrier = true)"); err != nil {
		t.Fatalf("ALTER VIEW SET (...): %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "vopt"})
	if !ok {
		t.Fatalf("catalog lost the vopt relation after SET (...)")
	}
	if !tbl.SecurityBarrierSet || !tbl.SecurityBarrier {
		t.Errorf("SecurityBarrier = %v, SecurityBarrierSet = %v; want true, true", tbl.SecurityBarrier, tbl.SecurityBarrierSet)
	}
	if err := runDDL(t, ctx, "ALTER VIEW vopt RESET (security_barrier)"); err != nil {
		t.Fatalf("ALTER VIEW RESET (...): %v", err)
	}
	tbl, ok = cat.LookupTable(parser.ObjectName{Name: "vopt"})
	if !ok {
		t.Fatalf("catalog lost the vopt relation after RESET (...)")
	}
	if tbl.SecurityBarrierSet {
		t.Errorf("SecurityBarrierSet still true after RESET (...)")
	}
}

// TestAlterViewSetResetCheckOption pins DU-002 slice 444's completion of the
// third and last view_option_name (alongside security_barrier/
// security_invoker in TestAlterViewSetResetReloptions above): `check_option`
// is an enum reloption (`local`/`cascaded`, PG compares case-insensitively),
// unlike the two boolean options, and an invalid value must be rejected
// (22023) rather than silently accepted like an unmodeled option name.
func TestAlterViewSetResetCheckOption(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE viewsrc8 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE VIEW vcheck AS SELECT a FROM viewsrc8"); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER VIEW vcheck SET (check_option = CASCADED)"); err != nil {
		t.Fatalf("ALTER VIEW SET (check_option = CASCADED): %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "vcheck"})
	if !ok {
		t.Fatalf("catalog lost the vcheck relation after SET (check_option = ...)")
	}
	if tbl.CheckOption != "cascaded" {
		t.Errorf("CheckOption = %q, want %q (case-folded)", tbl.CheckOption, "cascaded")
	}
	if err := runDDL(t, ctx, "ALTER VIEW vcheck RESET (check_option)"); err != nil {
		t.Fatalf("ALTER VIEW RESET (check_option): %v", err)
	}
	tbl, ok = cat.LookupTable(parser.ObjectName{Name: "vcheck"})
	if !ok {
		t.Fatalf("catalog lost the vcheck relation after RESET (check_option)")
	}
	if tbl.CheckOption != "" {
		t.Errorf("CheckOption = %q after RESET, want empty", tbl.CheckOption)
	}
	err := runDDL(t, ctx, "ALTER VIEW vcheck SET (check_option = bogus)")
	if err == nil {
		t.Fatalf("ALTER VIEW SET (check_option = bogus): want error, got nil")
	}
	if execErr, ok := err.(*ExecError); !ok || execErr.Code != "22023" {
		t.Errorf("ALTER VIEW SET (check_option = bogus) error = %v, want ExecError 22023", err)
	}
}
