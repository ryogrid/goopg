package executor

// view_owner_privilege_test.go pins the M0122-0008 view-owner privilege
// fix end to end: CREATE VIEW now stamps the creating role as owner
// (previously every view was silently "owned" by the bootstrap
// superuser), and the planner tags an inlined view's underlying scans
// with that owner so the executor's SELECT-privilege check
// (M0097-0040) runs as the view owner, not the querying session —
// mirroring PostgreSQL's default (non-security_invoker) view semantics.
// Without this fix, `GRANT SELECT ON view TO role` alone (no base-table
// grant) was wrongly denied with 42501 once SELECT enforcement landed.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// openSelect parses, plans, and opens sql against ctx, returning the
// Open() error (nil on success) without failing the test — the caller
// decides what to assert. Closes the operator on success.
func openSelect(t *testing.T, ctx *Context, sql string) error {
	t.Helper()
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
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
	defer op.Close()
	return nil
}

func TestCreateViewStampsCreatingRoleAsOwner(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	im.RegisterRole("alice")

	if err := runDDL(t, ctx, "CREATE TABLE ownersrc (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	ctx.NonSuperuserRole = "alice"
	if err := runDDL(t, ctx, "CREATE VIEW vowner AS SELECT a FROM ownersrc"); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "vowner"})
	if !ok {
		t.Fatalf("catalog lost the vowner relation after CREATE VIEW")
	}
	if tbl.Owner != "alice" {
		t.Errorf("Owner = %q after CREATE VIEW by alice, want %q", tbl.Owner, "alice")
	}

	// CREATE OR REPLACE VIEW run by a different (bootstrap) session must
	// NOT re-stamp the owner — only ALTER VIEW ... OWNER TO changes it in
	// real PostgreSQL.
	ctx.NonSuperuserRole = ""
	if err := runDDL(t, ctx, "CREATE OR REPLACE VIEW vowner AS SELECT a FROM ownersrc"); err != nil {
		t.Fatalf("CREATE OR REPLACE VIEW: %v", err)
	}
	tbl, ok = cat.LookupTable(parser.ObjectName{Name: "vowner"})
	if !ok {
		t.Fatalf("catalog lost the vowner relation after CREATE OR REPLACE VIEW")
	}
	if tbl.Owner != "alice" {
		t.Errorf("Owner = %q after CREATE OR REPLACE VIEW by bootstrap superuser, want unchanged %q", tbl.Owner, "alice")
	}
}

// TestViewOwnerPrivilegeGrantsThroughView is the reported-bug regression:
// a role with SELECT on the view alone (no base-table grant) can query
// through it because the view owner's own base-table grant is what gets
// checked, not the querying role's.
func TestViewOwnerPrivilegeGrantsThroughView(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	im.RegisterRole("alice")
	im.RegisterRole("bob")

	if err := runDDL(t, ctx, "CREATE TABLE base_t (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO base_t VALUES (1), (2)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	// alice owns the view and holds her own SELECT grant on the base table;
	// bob (the querying role below) is granted SELECT on the view only.
	ctx.NonSuperuserRole = "alice"
	if err := runDDL(t, ctx, "CREATE VIEW v_base AS SELECT a FROM base_t"); err != nil {
		t.Fatalf("CREATE VIEW: %v", err)
	}
	ctx.NonSuperuserRole = ""
	if err := runDDL(t, ctx, "GRANT SELECT ON v_base TO bob"); err != nil {
		t.Fatalf("GRANT SELECT ON view: %v", err)
	}

	ctx.NonSuperuserRole = "bob"
	if err := openSelect(t, ctx, "SELECT a FROM v_base"); err == nil {
		t.Fatal("expected 42501: alice (view owner) has no grant on base_t yet")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42501" {
		t.Fatalf("expected 42501, got %#v", err)
	}

	baseTbl, ok := cat.LookupTable(parser.ObjectName{Name: "base_t"})
	if !ok {
		t.Fatalf("catalog lost the base_t relation")
	}
	im.GrantTablePrivilege(baseTbl.OID, "alice", "SELECT")
	if err := openSelect(t, ctx, "SELECT a FROM v_base"); err != nil {
		t.Fatalf("expected success once the view owner (alice) has SELECT on base_t, got %v", err)
	}
}
