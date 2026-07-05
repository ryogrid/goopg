package executor

// operators_alter_table_owner_test.go pins a deferral-ledger fix: `ALTER
// TABLE name OWNER TO role` previously recorded tbl.Owner unconditionally,
// unlike every sibling OWNER TO site (schema/statistics/collation/aggregate),
// which all reject an unknown role with 42704. Real PostgreSQL's
// AlterTableOwner (postgres/src/backend/commands/tablecmds.c) resolves the
// target role via get_role_oid(false) and raises `role "..." does not exist`
// for an unknown role before assigning ownership. execAlterTable now makes
// the same im.RoleOID existence check. ALTER SEQUENCE/VIEW OWNER TO share
// this exact code path (see operators_alter_sequence_relation_ops_test.go /
// operators_alter_view_relation_ops_test.go), so this fix closes the gap for
// all three relation kinds at once; their existing "OWNER TO alice" tests
// were updated to register the role first since they now exercise the new
// enforcement path too.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func TestAlterTableOwnerTo(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	cat.(*catalog.InMemory).RegisterRole("alice")
	if err := runDDL(t, ctx, "CREATE TABLE t_own (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE t_own OWNER TO alice"); err != nil {
		t.Fatalf("ALTER TABLE OWNER TO: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t_own"})
	if !ok {
		t.Fatalf("catalog lost the t_own relation after OWNER TO")
	}
	if tbl.Owner != "alice" {
		t.Errorf("Owner = %q, want %q", tbl.Owner, "alice")
	}
}

func TestAlterTableOwnerToCurrentUser(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t_own_cu (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE t_own_cu OWNER TO CURRENT_USER"); err != nil {
		t.Fatalf("ALTER TABLE OWNER TO CURRENT_USER: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t_own_cu"})
	if !ok {
		t.Fatalf("catalog lost the t_own_cu relation after OWNER TO CURRENT_USER")
	}
	if tbl.Owner != "" {
		t.Errorf("Owner = %q, want %q (bootstrap superuser sentinel)", tbl.Owner, "")
	}
}

// TestAlterTableOwnerToUnknownRoleErrors pins the actual bug fix: previously
// `tbl.Owner = s.OwnerTo` was unconditional, so this silently "succeeded" and
// left the table owned by a role that does not exist in the catalog — a
// divergence from real PostgreSQL's 42704 error (get_role_oid raises
// ROLE_NOT_FOUND) that would corrupt any pg_dump-restore round-trip depending
// on OWNER TO failing fast for a typo'd/dropped role name.
func TestAlterTableOwnerToUnknownRoleErrors(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t_own_bad (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	err := runDDL(t, ctx, "ALTER TABLE t_own_bad OWNER TO nonexistent_role")
	if err == nil {
		t.Fatal("ALTER TABLE OWNER TO nonexistent_role: want 42704 error, got nil")
	}
	if got := err.Error(); got != `42704: role "nonexistent_role" does not exist (byte 0)` {
		t.Errorf("error = %q, want role-does-not-exist 42704", got)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "t_own_bad"})
	if !ok {
		t.Fatalf("catalog lost the t_own_bad relation after failed OWNER TO")
	}
	if tbl.Owner != "" {
		t.Errorf("Owner = %q, want unchanged empty owner after rejected OWNER TO", tbl.Owner)
	}
}
