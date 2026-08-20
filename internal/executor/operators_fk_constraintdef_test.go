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
	got := buildForeignKeyDefString(ctx, im, fk)
	want := "FOREIGN KEY (pid) REFERENCES parent(id)"
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
	got := buildForeignKeyDefString(ctx, im, fk)
	want := "FOREIGN KEY (pid) REFERENCES parent(id) ON UPDATE CASCADE ON DELETE SET NULL"
	if got != want {
		t.Errorf("buildForeignKeyDefString = %q, want %q", got, want)
	}
}

// TestForeignKeyMatchFullRoundTrip verifies that a FOREIGN KEY declared with
// MATCH FULL captures the match type (previously dropped at the parser — MATCH
// was never part of the FK grammar), surfaces pg_constraint.confmatchtype='f'
// (vs 's' for MATCH SIMPLE), and that pg_get_constraintdef emits ` MATCH FULL`
// between the REFERENCES column list and the ON UPDATE/DELETE clauses
// (ruleutils.c order). A MATCH SIMPLE control confirms the default stays 's'
// with no clause. DU-002 slice 309.
func TestForeignKeyMatchFullRoundTrip(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE mref (a integer, b integer, PRIMARY KEY (a, b))`); err != nil {
		t.Fatalf("CREATE TABLE mref: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE mfull (a integer, b integer)`); err != nil {
		t.Fatalf("CREATE TABLE mfull: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE msimple (a integer, b integer)`); err != nil {
		t.Fatalf("CREATE TABLE msimple: %v", err)
	}
	// MATCH FULL via ALTER TABLE ADD CONSTRAINT (the path pg_dump re-emits).
	if err := runDDL(t, ctx, `ALTER TABLE mfull ADD CONSTRAINT mfull_fk `+
		`FOREIGN KEY (a, b) REFERENCES mref (a, b) MATCH FULL`); err != nil {
		t.Fatalf("ALTER TABLE mfull ADD FK MATCH FULL: %v", err)
	}
	// Explicit MATCH SIMPLE (the default) must NOT set MatchFull / confmatchtype.
	if err := runDDL(t, ctx, `ALTER TABLE msimple ADD CONSTRAINT msimple_fk `+
		`FOREIGN KEY (a, b) REFERENCES mref (a, b) MATCH SIMPLE ON DELETE CASCADE`); err != nil {
		t.Fatalf("ALTER TABLE msimple ADD FK MATCH SIMPLE: %v", err)
	}

	mfullTbl, ok := cat.LookupTable(parser.ObjectName{Name: "mfull"})
	if !ok {
		t.Fatal("mfull table not found")
	}
	if len(mfullTbl.ForeignKeys) != 1 {
		t.Fatalf("expected 1 FK on mfull, got %d", len(mfullTbl.ForeignKeys))
	}
	fkFull := mfullTbl.ForeignKeys[0]
	if !fkFull.MatchFull {
		t.Error("MATCH FULL FK: catalog.ForeignKey.MatchFull = false, want true")
	}

	msimpleTbl, ok := cat.LookupTable(parser.ObjectName{Name: "msimple"})
	if !ok {
		t.Fatal("msimple table not found")
	}
	fkSimple := msimpleTbl.ForeignKeys[0]
	if fkSimple.MatchFull {
		t.Error("MATCH SIMPLE FK: catalog.ForeignKey.MatchFull = true, want false")
	}

	// pg_constraint.confmatchtype (row[14]) must be 'f' for FULL, 's' for SIMPLE.
	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	match := map[string]string{}
	for _, r := range pgcon.VirtualRows() {
		if r[3] == "f" {
			match[r[1]] = r[14]
		}
	}
	if match["mfull_fk"] != "f" {
		t.Errorf("confmatchtype for mfull_fk = %q, want \"f\" (MATCH FULL)", match["mfull_fk"])
	}
	if match["msimple_fk"] != "s" {
		t.Errorf("confmatchtype for msimple_fk = %q, want \"s\" (MATCH SIMPLE)", match["msimple_fk"])
	}

	// pg_get_constraintdef must emit ` MATCH FULL` before ON UPDATE/DELETE, and
	// nothing for MATCH SIMPLE.
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	if got, want := buildForeignKeyDefString(ctx, im, fkFull),
		"FOREIGN KEY (a, b) REFERENCES mref(a, b) MATCH FULL"; got != want {
		t.Errorf("buildForeignKeyDefString(MATCH FULL) = %q, want %q", got, want)
	}
	if got, want := buildForeignKeyDefString(ctx, im, fkSimple),
		"FOREIGN KEY (a, b) REFERENCES mref(a, b) ON DELETE CASCADE"; got != want {
		t.Errorf("buildForeignKeyDefString(MATCH SIMPLE) = %q, want %q", got, want)
	}
}

// TestForeignKeyOnDeleteSetColsRoundTrip verifies that an `ON DELETE SET NULL
// (col)` action restricted to a column subset (PG15 confdelsetcols) survives
// parsing, surfaces pg_constraint.confdelsetcols as the column attnum array, and
// that pg_get_constraintdef re-emits the ` (col)` suffix after the ON DELETE
// clause (ruleutils.c decompile_column_index_array). A plain `ON DELETE SET
// NULL` (no column list) control confirms confdelsetcols stays NULL and no
// suffix is emitted. DU-002 slice 311.
func TestForeignKeyOnDeleteSetColsRoundTrip(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE sref (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE sref: %v", err)
	}
	// Restricted SET NULL: only column b is nulled on parent delete.
	if err := runDDL(t, ctx, `CREATE TABLE sset (a integer, b integer)`); err != nil {
		t.Fatalf("CREATE TABLE sset: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE sset ADD CONSTRAINT sset_fk `+
		`FOREIGN KEY (b) REFERENCES sref (id) ON DELETE SET NULL (b)`); err != nil {
		t.Fatalf("ALTER TABLE sset ADD FK SET NULL (b): %v", err)
	}
	// Plain SET NULL control (whole key) — no column list.
	if err := runDDL(t, ctx, `CREATE TABLE splain (a integer, b integer)`); err != nil {
		t.Fatalf("CREATE TABLE splain: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE splain ADD CONSTRAINT splain_fk `+
		`FOREIGN KEY (b) REFERENCES sref (id) ON DELETE SET NULL`); err != nil {
		t.Fatalf("ALTER TABLE splain ADD FK SET NULL: %v", err)
	}

	ssetTbl, ok := cat.LookupTable(parser.ObjectName{Name: "sset"})
	if !ok || len(ssetTbl.ForeignKeys) != 1 {
		t.Fatalf("sset FK not captured: %+v", ssetTbl)
	}
	fkSet := ssetTbl.ForeignKeys[0]
	if got := fkSet.OnDeleteSetCols; len(got) != 1 || got[0] != "b" {
		t.Errorf("OnDeleteSetCols = %v, want [b]", got)
	}
	splainTbl, _ := cat.LookupTable(parser.ObjectName{Name: "splain"})
	fkPlain := splainTbl.ForeignKeys[0]
	if len(fkPlain.OnDeleteSetCols) != 0 {
		t.Errorf("plain SET NULL OnDeleteSetCols = %v, want empty", fkPlain.OnDeleteSetCols)
	}

	// pg_constraint.confdelsetcols (row[23]) — attnum array for the restricted FK
	// (b is the 2nd column of sset → {2}), NULL for the whole-key FK.
	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	delsetcols := map[string]string{}
	for _, r := range pgcon.VirtualRows() {
		if r[3] == "f" {
			delsetcols[r[1]] = r[23]
		}
	}
	if delsetcols["sset_fk"] != "{2}" {
		t.Errorf("confdelsetcols for sset_fk = %q, want \"{2}\"", delsetcols["sset_fk"])
	}
	if delsetcols["splain_fk"] != "" {
		t.Errorf("confdelsetcols for splain_fk = %q, want NULL/empty", delsetcols["splain_fk"])
	}

	// pg_get_constraintdef must append ` (b)` after ON DELETE SET NULL for the
	// restricted FK, and emit no suffix for the plain one.
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	if got, want := buildForeignKeyDefString(ctx, im, fkSet),
		"FOREIGN KEY (b) REFERENCES sref(id) ON DELETE SET NULL (b)"; got != want {
		t.Errorf("buildForeignKeyDefString(SET NULL (b)) = %q, want %q", got, want)
	}
	if got, want := buildForeignKeyDefString(ctx, im, fkPlain),
		"FOREIGN KEY (b) REFERENCES sref(id) ON DELETE SET NULL"; got != want {
		t.Errorf("buildForeignKeyDefString(plain SET NULL) = %q, want %q", got, want)
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
	got := buildForeignKeyDefString(ctx, im, bazFK)
	want := "FOREIGN KEY (x, y) REFERENCES bar(a, b) ON DELETE CASCADE"
	if got != want {
		t.Errorf("buildForeignKeyDefString = %q, want %q", got, want)
	}
}
