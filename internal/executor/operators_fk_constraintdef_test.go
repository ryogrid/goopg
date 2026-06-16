package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestForeignKeySurfacesInPgConstraint verifies that a FOREIGN KEY declared via
// inline REFERENCES gets a name+OID at DDL time, surfaces as a contype='f' row
// in pg_constraint (with the referencing/referenced ordinals and the referenced
// table OID), and that pg_get_constraintdef's FK branch reconstructs the
// schema-qualified definition pg_dump re-emits. DU-002 slice 51.
func TestForeignKeySurfacesInPgConstraint(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE parent (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE child (cid integer PRIMARY KEY, pid integer REFERENCES parent(id))`); err != nil {
		t.Fatalf("CREATE TABLE child: %v", err)
	}

	parentTbl, ok := cat.LookupTable(parser.ObjectName{Name: "parent"})
	if !ok {
		t.Fatal("parent table not found")
	}
	childTbl, ok := cat.LookupTable(parser.ObjectName{Name: "child"})
	if !ok {
		t.Fatal("child table not found")
	}

	// The FK must have a PG-convention name and a non-zero OID.
	if len(childTbl.ForeignKeys) != 1 {
		t.Fatalf("expected 1 FK, got %d", len(childTbl.ForeignKeys))
	}
	fk := childTbl.ForeignKeys[0]
	if fk.Name != "child_pid_fkey" {
		t.Errorf("FK name = %q, want %q", fk.Name, "child_pid_fkey")
	}
	if fk.OID == 0 {
		t.Error("FK OID must be non-zero so pg_constraint can surface it")
	}

	// pg_constraint must emit exactly one contype='f' row for the FK with the
	// correct conrelid/confrelid and conkey/confkey ordinals.
	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	var fkRows [][]string
	for _, r := range pgcon.VirtualRows() {
		if r[3] == "f" && r[1] == "child_pid_fkey" {
			fkRows = append(fkRows, r)
		}
	}
	if len(fkRows) != 1 {
		t.Fatalf("expected 1 contype='f' row for child_pid_fkey, got %d", len(fkRows))
	}
	r := fkRows[0]
	if r[7] != fmt.Sprintf("%d", childTbl.OID) {
		t.Errorf("conrelid = %q, want %d (child)", r[7], childTbl.OID)
	}
	if r[11] != fmt.Sprintf("%d", parentTbl.OID) {
		t.Errorf("confrelid = %q, want %d (parent)", r[11], parentTbl.OID)
	}
	if r[19] != "{2}" { // pid is the 2nd column of child
		t.Errorf("conkey = %q, want {2}", r[19])
	}
	if r[20] != "{1}" { // id is the 1st column of parent
		t.Errorf("confkey = %q, want {1}", r[20])
	}
	if r[12] != "a" || r[13] != "a" { // NO ACTION default
		t.Errorf("confupdtype/confdeltype = %q/%q, want a/a", r[12], r[13])
	}

	// pg_get_constraintdef's FK branch must reconstruct the schema-qualified def.
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	got := buildForeignKeyDefString(im, fk)
	want := "FOREIGN KEY (pid) REFERENCES public.parent(id)"
	if got != want {
		t.Errorf("buildForeignKeyDefString = %q, want %q", got, want)
	}
}
