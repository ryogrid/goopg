package executor

// create_table_owner_test.go pins the M0134-0004 S2 fix: CREATE TABLE and
// CREATE TABLE ... AS SELECT now stamp the creating non-superuser role as
// catalog.Table.Owner (mirroring view_owner_privilege_test.go's
// TestCreateViewStampsCreatingRoleAsOwner for CREATE VIEW), so the
// owner-shortcut in dmlPrivilegePermittedAs lets the creating role
// immediately INSERT/SELECT its own table with no explicit GRANT. Before
// this fix, Owner stayed zero-valued and the creating role got a false
// "permission denied" (42501) on a table it had just created. PG:
// tablecmds.c:DefineRelation -> heap.c:heap_create_with_catalog(ownerId =
// GetUserId()).

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCreateTableStampsCreatingRoleAsOwner covers acceptance criterion 1:
// a non-superuser role that creates a table via plain CREATE TABLE can then
// INSERT into and SELECT from it with no GRANT.
func TestCreateTableStampsCreatingRoleAsOwner(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	im.RegisterRole("alice")

	ctx.NonSuperuserRole = "alice"
	if err := runDDL(t, ctx, "CREATE TABLE owntbl (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "owntbl"})
	if !ok {
		t.Fatalf("catalog lost the owntbl relation after CREATE TABLE")
	}
	if tbl.Owner != "alice" {
		t.Fatalf("Owner = %q after CREATE TABLE by alice, want %q", tbl.Owner, "alice")
	}

	if err := runDDL(t, ctx, "INSERT INTO owntbl VALUES (1)"); err != nil {
		t.Fatalf("INSERT by owner alice with no GRANT: %v", err)
	}
	if err := openSelect(t, ctx, "SELECT a FROM owntbl"); err != nil {
		t.Fatalf("SELECT by owner alice with no GRANT: %v", err)
	}
}

// TestCreateTableAsStampsCreatingRoleAsOwner covers acceptance criterion 2:
// the same guarantee for CREATE TABLE ... AS SELECT, an independent
// construction path from plain CREATE TABLE.
func TestCreateTableAsStampsCreatingRoleAsOwner(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	im.RegisterRole("alice")

	if err := runDDL(t, ctx, "CREATE TABLE ctas_src (a int)"); err != nil {
		t.Fatalf("CREATE TABLE ctas_src: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO ctas_src VALUES (1), (2)"); err != nil {
		t.Fatalf("INSERT INTO ctas_src: %v", err)
	}
	srcTbl, ok := cat.LookupTable(parser.ObjectName{Name: "ctas_src"})
	if !ok {
		t.Fatalf("catalog lost the ctas_src relation")
	}
	im.GrantTablePrivilege(srcTbl.OID, "alice", "SELECT")

	ctx.NonSuperuserRole = "alice"
	if err := runDDL(t, ctx, "CREATE TABLE ctas_owner AS SELECT a FROM ctas_src"); err != nil {
		t.Fatalf("CREATE TABLE ... AS SELECT: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "ctas_owner"})
	if !ok {
		t.Fatalf("catalog lost the ctas_owner relation after CREATE TABLE AS")
	}
	if tbl.Owner != "alice" {
		t.Fatalf("Owner = %q after CREATE TABLE AS by alice, want %q", tbl.Owner, "alice")
	}

	if err := runDDL(t, ctx, "INSERT INTO ctas_owner VALUES (3)"); err != nil {
		t.Fatalf("INSERT by owner alice with no GRANT: %v", err)
	}
	if err := openSelect(t, ctx, "SELECT a FROM ctas_owner"); err != nil {
		t.Fatalf("SELECT by owner alice with no GRANT: %v", err)
	}
}

// TestCreateTableOwnerNegativeGuardDeniesOtherRole covers acceptance
// criterion 3: a *different* non-superuser role, with no GRANT, is still
// denied SELECT/INSERT on a table it did not create. This proves the owner
// stamp is additive and did not become an allow-all.
func TestCreateTableOwnerNegativeGuardDeniesOtherRole(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	im.RegisterRole("alice")
	im.RegisterRole("bob")

	ctx.NonSuperuserRole = "alice"
	if err := runDDL(t, ctx, "CREATE TABLE alice_tbl (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	ctx.NonSuperuserRole = "bob"
	if err := runDDL(t, ctx, "INSERT INTO alice_tbl VALUES (1)"); err == nil {
		t.Fatal("expected 42501: bob has no GRANT and does not own alice_tbl")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42501" {
		t.Fatalf("expected 42501, got %#v", err)
	}
	if err := openSelect(t, ctx, "SELECT a FROM alice_tbl"); err == nil {
		t.Fatal("expected 42501: bob has no GRANT and does not own alice_tbl")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42501" {
		t.Fatalf("expected 42501, got %#v", err)
	}
}

// TestCreateTableBootstrapSuperuserOwnerEmpty covers acceptance criterion 4:
// a table created by the bootstrap superuser session (ctx.NonSuperuserRole
// == "") still leaves catalog.Table.Owner at its zero-value sentinel
// (catalog.go:611-621), so every existing table's behavior is unchanged.
func TestCreateTableBootstrapSuperuserOwnerEmpty(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE root_tbl (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "root_tbl"})
	if !ok {
		t.Fatalf("catalog lost the root_tbl relation")
	}
	if tbl.Owner != "" {
		t.Errorf("Owner = %q after CREATE TABLE by bootstrap superuser, want empty sentinel", tbl.Owner)
	}

	if err := runDDL(t, ctx, "CREATE TABLE root_ctas AS SELECT a FROM root_tbl"); err != nil {
		t.Fatalf("CREATE TABLE ... AS SELECT: %v", err)
	}
	ctasTbl, ok := cat.LookupTable(parser.ObjectName{Name: "root_ctas"})
	if !ok {
		t.Fatalf("catalog lost the root_ctas relation")
	}
	if ctasTbl.Owner != "" {
		t.Errorf("Owner = %q after CREATE TABLE AS by bootstrap superuser, want empty sentinel", ctasTbl.Owner)
	}
}

// TestCreateTablePartitionOfStampsCreatingRoleAsOwner covers the
// coordinator-widened round-2 scope: execCreatePartitionChild is a third,
// independent construction site (its own Catalog.CreateTable call), not
// covered by the plain-CREATE-TABLE assignment. A non-superuser role that
// creates a partitioned parent and a PARTITION OF child must be able to
// INSERT/SELECT the child with no GRANT, exactly like the plain-table and
// CTAS cases above.
func TestCreateTablePartitionOfStampsCreatingRoleAsOwner(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	im.RegisterRole("alice")

	ctx.NonSuperuserRole = "alice"
	if err := runDDL(t, ctx, "CREATE TABLE part_parent (a int) PARTITION BY RANGE (a)"); err != nil {
		t.Fatalf("CREATE TABLE ... PARTITION BY: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE part_child PARTITION OF part_parent FOR VALUES FROM (0) TO (100)"); err != nil {
		t.Fatalf("CREATE TABLE ... PARTITION OF: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "part_child"})
	if !ok {
		t.Fatalf("catalog lost the part_child relation after CREATE TABLE PARTITION OF")
	}
	if tbl.Owner != "alice" {
		t.Fatalf("Owner = %q after CREATE TABLE PARTITION OF by alice, want %q", tbl.Owner, "alice")
	}

	if err := runDDL(t, ctx, "INSERT INTO part_child VALUES (1)"); err != nil {
		t.Fatalf("INSERT by owner alice with no GRANT: %v", err)
	}
	if err := openSelect(t, ctx, "SELECT a FROM part_child"); err != nil {
		t.Fatalf("SELECT by owner alice with no GRANT: %v", err)
	}
}
