package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCreateTableInheritsPrimaryKeyLocalNotNullIsLocal pins M0134-0005s:
// `CREATE TABLE child (PRIMARY KEY (a) DEFERRABLE) INHERITS (parent)` where
// `parent` also enforces NOT NULL on `a` must mark the child's NOT NULL
// constraint IsLocal=true (not just inherited) and InhCount=1, and must
// name it after the CHILD (`<child>_a_not_null`), not the parent — mirrors
// `postgres/src/backend/catalog/heap.c:3038-3050`
// (AddRelationNotNullConstraints's islocal=true loop). Before this slice,
// the col.Inherited branch at operators_ddl.go:4012-4026 left isLocal=false
// and the parent's constraint name uncorrected even though `entries`
// (built from the PK-implied source) was non-empty.
func TestCreateTableInheritsPrimaryKeyLocalNotNullIsLocal(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE notnull_tbl4 (a int NOT NULL)`,
		`CREATE TABLE notnull_tbl4_cld2 (PRIMARY KEY (a) DEFERRABLE) INHERITS (notnull_tbl4)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_tbl4_cld2"})
	if !ok {
		t.Fatal("notnull_tbl4_cld2 not found")
	}
	var nc *catalog.NamedNotNullConstraint
	for i := range child.NotNullConstraints {
		if child.NotNullConstraints[i].ColName == "a" {
			nc = &child.NotNullConstraints[i]
		}
	}
	if nc == nil {
		t.Fatalf("no NOT NULL constraint recorded for notnull_tbl4_cld2.a: %+v", child.NotNullConstraints)
	}
	if !nc.IsLocal {
		t.Errorf("notnull_tbl4_cld2.a NOT NULL IsLocal = false, want true (PK-implied local source)")
	}
	if nc.InhCount != 1 {
		t.Errorf("notnull_tbl4_cld2.a NOT NULL InhCount = %d, want 1", nc.InhCount)
	}
	if want := "notnull_tbl4_cld2_a_not_null"; nc.Name != want {
		t.Errorf("notnull_tbl4_cld2.a NOT NULL Name = %q, want %q (child's auto name, not the parent's)", nc.Name, want)
	}
}

// TestCreateTableInheritsExplicitNotNullKeepsExplicitNameAndIsLocal is the
// twin case: `CONSTRAINT a_nn NOT NULL a` in the child's own body must keep
// its explicit name (mergeNotNullEntries's mergedName wins) while STILL
// gaining IsLocal=true and InhCount=1 from the inherited parent constraint.
func TestCreateTableInheritsExplicitNotNullKeepsExplicitNameAndIsLocal(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE notnull_tbl4b (a int NOT NULL)`,
		`CREATE TABLE notnull_tbl4b_cld3 (PRIMARY KEY (a) DEFERRABLE, CONSTRAINT a_nn NOT NULL a) INHERITS (notnull_tbl4b)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_tbl4b_cld3"})
	if !ok {
		t.Fatal("notnull_tbl4b_cld3 not found")
	}
	var nc *catalog.NamedNotNullConstraint
	for i := range child.NotNullConstraints {
		if child.NotNullConstraints[i].ColName == "a" {
			nc = &child.NotNullConstraints[i]
		}
	}
	if nc == nil {
		t.Fatalf("no NOT NULL constraint recorded for notnull_tbl4b_cld3.a: %+v", child.NotNullConstraints)
	}
	if !nc.IsLocal {
		t.Errorf("notnull_tbl4b_cld3.a NOT NULL IsLocal = false, want true (explicit local source)")
	}
	if nc.InhCount != 1 {
		t.Errorf("notnull_tbl4b_cld3.a NOT NULL InhCount = %d, want 1", nc.InhCount)
	}
	if nc.Name != "a_nn" {
		t.Errorf("notnull_tbl4b_cld3.a NOT NULL Name = %q, want %q (explicit name still wins)", nc.Name, "a_nn")
	}
}
