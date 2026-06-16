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

// TestAlterTableAddForeignKeyCapturesActions verifies that the ALTER TABLE ADD
// FOREIGN KEY path captures the ON DELETE / ON UPDATE referential actions
// (previously dropped at the parser, AST, and executor), so they reach
// pg_constraint and pg_get_constraintdef renders the ` ON UPDATE …`/` ON DELETE …`
// clauses (ON UPDATE before ON DELETE, mirroring ruleutils.c). DU-002 slice 52.
func TestAlterTableAddForeignKeyCapturesActions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE parent (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE child (cid integer PRIMARY KEY, pid integer)`); err != nil {
		t.Fatalf("CREATE TABLE child: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE child ADD CONSTRAINT child_pid_fkey `+
		`FOREIGN KEY (pid) REFERENCES parent (id) ON UPDATE CASCADE ON DELETE SET NULL`); err != nil {
		t.Fatalf("ALTER TABLE child ADD FK: %v", err)
	}

	childTbl, ok := cat.LookupTable(parser.ObjectName{Name: "child"})
	if !ok {
		t.Fatal("child table not found")
	}
	if len(childTbl.ForeignKeys) != 1 {
		t.Fatalf("expected 1 FK, got %d", len(childTbl.ForeignKeys))
	}
	fk := childTbl.ForeignKeys[0]
	if fk.OnUpdate != parser.FKActionCascade {
		t.Errorf("OnUpdate = %v, want CASCADE", fk.OnUpdate)
	}
	if fk.OnDelete != parser.FKActionSetNull {
		t.Errorf("OnDelete = %v, want SET NULL", fk.OnDelete)
	}

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	got := buildForeignKeyDefString(im, fk)
	want := "FOREIGN KEY (pid) REFERENCES public.parent(id) ON UPDATE CASCADE ON DELETE SET NULL"
	if got != want {
		t.Errorf("buildForeignKeyDefString = %q, want %q", got, want)
	}
}

// TestCreateTableTableLevelCompositeForeignKey verifies that a table-level
// (composite) FOREIGN KEY declared in the CREATE TABLE body — `FOREIGN KEY
// (a, b) REFERENCES t (x, y)` — is captured rather than silently dropped. The
// parser previously treated table-level FKs as a no-op, so a multi-column FK
// never reached the catalog, pg_constraint, or pg_dump. Both the anonymous and
// CONSTRAINT-named forms are exercised. DU-002 slice 53.
func TestCreateTableTableLevelCompositeForeignKey(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE bar (a integer, b integer, PRIMARY KEY (a, b))`); err != nil {
		t.Fatalf("CREATE TABLE bar: %v", err)
	}
	// Anonymous table-level composite FK (auto-named <table>_<firstcol>_fkey).
	if err := runDDL(t, ctx, `CREATE TABLE baz (x integer, y integer, `+
		`FOREIGN KEY (x, y) REFERENCES bar (a, b) ON DELETE CASCADE)`); err != nil {
		t.Fatalf("CREATE TABLE baz: %v", err)
	}
	// CONSTRAINT-named table-level composite FK.
	if err := runDDL(t, ctx, `CREATE TABLE qux (x integer, y integer, `+
		`CONSTRAINT qux_ref FOREIGN KEY (x, y) REFERENCES bar (a, b))`); err != nil {
		t.Fatalf("CREATE TABLE qux: %v", err)
	}

	barTbl, ok := cat.LookupTable(parser.ObjectName{Name: "bar"})
	if !ok {
		t.Fatal("bar table not found")
	}
	bazTbl, ok := cat.LookupTable(parser.ObjectName{Name: "baz"})
	if !ok {
		t.Fatal("baz table not found")
	}
	if len(bazTbl.ForeignKeys) != 1 {
		t.Fatalf("expected 1 FK on baz, got %d", len(bazTbl.ForeignKeys))
	}
	bazFK := bazTbl.ForeignKeys[0]
	if bazFK.Name != "baz_x_fkey" {
		t.Errorf("anonymous composite FK name = %q, want %q", bazFK.Name, "baz_x_fkey")
	}
	if len(bazFK.Columns) != 2 || bazFK.Columns[0] != "x" || bazFK.Columns[1] != "y" {
		t.Errorf("FK Columns = %v, want [x y]", bazFK.Columns)
	}
	if len(bazFK.RefColumns) != 2 || bazFK.RefColumns[0] != "a" || bazFK.RefColumns[1] != "b" {
		t.Errorf("FK RefColumns = %v, want [a b]", bazFK.RefColumns)
	}
	if bazFK.OnDelete != parser.FKActionCascade {
		t.Errorf("OnDelete = %v, want CASCADE", bazFK.OnDelete)
	}

	// CONSTRAINT-named form keeps the explicit name.
	quxTbl, ok := cat.LookupTable(parser.ObjectName{Name: "qux"})
	if !ok {
		t.Fatal("qux table not found")
	}
	if len(quxTbl.ForeignKeys) != 1 || quxTbl.ForeignKeys[0].Name != "qux_ref" {
		t.Fatalf("expected qux FK named qux_ref, got %+v", quxTbl.ForeignKeys)
	}

	// pg_constraint must emit a contype='f' row with multi-column conkey/confkey.
	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	var r []string
	for _, row := range pgcon.VirtualRows() {
		if row[3] == "f" && row[1] == "baz_x_fkey" {
			r = row
			break
		}
	}
	if r == nil {
		t.Fatal("no contype='f' row for baz_x_fkey")
	}
	if r[11] != fmt.Sprintf("%d", barTbl.OID) {
		t.Errorf("confrelid = %q, want %d (bar)", r[11], barTbl.OID)
	}
	if r[19] != "{1,2}" {
		t.Errorf("conkey = %q, want {1,2}", r[19])
	}
	if r[20] != "{1,2}" {
		t.Errorf("confkey = %q, want {1,2}", r[20])
	}

	// pg_get_constraintdef's FK branch must join multiple columns.
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	got := buildForeignKeyDefString(im, bazFK)
	want := "FOREIGN KEY (x, y) REFERENCES public.bar(a, b) ON DELETE CASCADE"
	if got != want {
		t.Errorf("buildForeignKeyDefString = %q, want %q", got, want)
	}
}
