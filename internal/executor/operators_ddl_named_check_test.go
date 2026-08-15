package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestNamedCheckPropagatesThroughLikeConstraints verifies that an explicitly
// named CHECK constraint added via ALTER TABLE is tracked in NamedChecks
// (parallel to CheckConstraints) and that LIKE INCLUDING CONSTRAINTS preserves
// the source constraint name. This is the catalog-side prerequisite for
// reporting the constraint name in violation messages. M0097-0023.
func TestNamedCheckPropagatesThroughLikeConstraints(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE src (xx text)`); err != nil {
		t.Fatalf("CREATE TABLE src: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE src ADD CONSTRAINT foo CHECK (xx = 'text')`); err != nil {
		t.Fatalf("ALTER TABLE src ADD CONSTRAINT: %v", err)
	}

	srcTbl, ok := cat.LookupTable(parser.ObjectName{Name: "src"})
	if !ok {
		t.Fatal("src table not found")
	}
	if len(srcTbl.NamedChecks) != len(srcTbl.CheckConstraints) {
		t.Fatalf("NamedChecks (%d) must stay parallel to CheckConstraints (%d)",
			len(srcTbl.NamedChecks), len(srcTbl.CheckConstraints))
	}
	if len(srcTbl.NamedChecks) != 1 || srcTbl.NamedChecks[0].Name != "foo" {
		t.Fatalf("expected NamedChecks[0].Name=foo, got %+v", srcTbl.NamedChecks)
	}

	// LIKE INCLUDING CONSTRAINTS must copy the constraint AND its name.
	if err := runDDL(t, ctx, `CREATE TABLE dst (a text, LIKE src INCLUDING CONSTRAINTS, b text)`); err != nil {
		t.Fatalf("CREATE TABLE dst: %v", err)
	}
	dstTbl, ok := cat.LookupTable(parser.ObjectName{Name: "dst"})
	if !ok {
		t.Fatal("dst table not found")
	}
	if len(dstTbl.NamedChecks) != len(dstTbl.CheckConstraints) {
		t.Fatalf("dst NamedChecks (%d) must stay parallel to CheckConstraints (%d)",
			len(dstTbl.NamedChecks), len(dstTbl.CheckConstraints))
	}
	foundFoo := false
	for _, nc := range dstTbl.NamedChecks {
		if nc.Name == "foo" {
			foundFoo = true
		}
	}
	if !foundFoo {
		t.Fatalf("LIKE INCLUDING CONSTRAINTS should preserve name 'foo', got %+v", dstTbl.NamedChecks)
	}
}

// TestAlterTableSetDropNotNull verifies that `ALTER TABLE ... ALTER COLUMN c
// SET NOT NULL` marks the column NOT NULL AND records a contype='n' constraint
// (catalog.Table.AddNotNull, conislocal=true), and that `DROP NOT NULL` clears
// both. This is the catalog-side prerequisite for pg_dump re-emitting the NOT
// NULL on an inherited column as a standalone `NOT NULL <col>` body item.
// DU-002 slice 270.
func TestAlterTableSetDropNotNull(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nnt (a integer, b integer)`); err != nil {
		t.Fatalf("CREATE TABLE nnt: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE nnt ALTER COLUMN a SET NOT NULL`); err != nil {
		t.Fatalf("SET NOT NULL: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "nnt"})
	if !ok {
		t.Fatal("nnt table not found")
	}
	if !tbl.Columns[0].NotNull {
		t.Errorf("column a should be NotNull after SET NOT NULL")
	}
	nnFor := func(col string) (catalog.NamedNotNullConstraint, bool) {
		for _, nc := range tbl.NotNullConstraints {
			if strings.EqualFold(nc.ColName, col) {
				return nc, true
			}
		}
		return catalog.NamedNotNullConstraint{}, false
	}
	nc, ok := nnFor("a")
	if !ok {
		t.Fatalf("SET NOT NULL did not record a contype='n' constraint for a; got %+v", tbl.NotNullConstraints)
	}
	if nc.Name != "nnt_a_not_null" {
		t.Errorf("NOT NULL constraint name=%q want nnt_a_not_null", nc.Name)
	}
	if !nc.IsLocal {
		t.Errorf("NOT NULL constraint for a should be conislocal=true")
	}

	// SET NOT NULL is idempotent — a second SET must not add a duplicate row.
	if err := runDDL(t, ctx, `ALTER TABLE nnt ALTER COLUMN a SET NOT NULL`); err != nil {
		t.Fatalf("second SET NOT NULL: %v", err)
	}
	count := 0
	for _, c := range tbl.NotNullConstraints {
		if strings.EqualFold(c.ColName, "a") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("idempotent SET NOT NULL produced %d constraints for a, want 1", count)
	}

	// DROP NOT NULL clears both the flag and the constraint.
	if err := runDDL(t, ctx, `ALTER TABLE nnt ALTER COLUMN a DROP NOT NULL`); err != nil {
		t.Fatalf("DROP NOT NULL: %v", err)
	}
	if tbl.Columns[0].NotNull {
		t.Errorf("column a should not be NotNull after DROP NOT NULL")
	}
	if _, ok := nnFor("a"); ok {
		t.Errorf("DROP NOT NULL left a contype='n' constraint for a; got %+v", tbl.NotNullConstraints)
	}
}

// TestCreateTableInheritsNotNullConstraintIsNonLocal verifies that a column
// pulled into a child purely via a plain `INHERITS (parent)` clause (not
// redeclared in the child's own column list) whose NOT NULL comes from the
// parent gets conislocal='f'/coninhcount=1 and reuses the PARENT's
// constraint name — matching real PostgreSQL's MergeAttributes (confirmed
// against a live PostgreSQL 18.3 instance: `parent_t_id_not_null` appears on
// BOTH tables, conislocal=t/coninhcount=0 on the parent, conislocal=f/
// coninhcount=1 on the child). Before this fix goopg always recorded
// isLocal=true/inhCount=0 for every NOT NULL column regardless of origin,
// which made pg_dump's shouldPrintColumn/print_notnull (pg_dump.c) treat the
// purely-inherited column as locally NOT NULL and emit a malformed
// standalone `NOT NULL id` table element (PG's non-conforming syntax for a
// LOCAL not-null override on a non-printed inherited column) even though the
// column is never locally declared at all — real PG emits no such element
// and lets INHERITS carry the column in unchanged. M0119-0004-ACLHEAP
// follow-up (M0110-0001 pg_dump catalog-view parity thread).
func TestCreateTableInheritsNotNullConstraintIsNonLocal(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE parent_t (id integer PRIMARY KEY, name text)`); err != nil {
		t.Fatalf("CREATE TABLE parent_t: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE child_t (extra text) INHERITS (parent_t)`); err != nil {
		t.Fatalf("CREATE TABLE child_t: %v", err)
	}

	parentTbl, ok := cat.LookupTable(parser.ObjectName{Name: "parent_t"})
	if !ok {
		t.Fatal("parent_t table not found")
	}
	childTbl, ok := cat.LookupTable(parser.ObjectName{Name: "child_t"})
	if !ok {
		t.Fatal("child_t table not found")
	}

	nnFor := func(tbl *catalog.Table, col string) (catalog.NamedNotNullConstraint, bool) {
		for _, nc := range tbl.NotNullConstraints {
			if strings.EqualFold(nc.ColName, col) {
				return nc, true
			}
		}
		return catalog.NamedNotNullConstraint{}, false
	}

	parentNC, ok := nnFor(parentTbl, "id")
	if !ok {
		t.Fatalf("parent_t.id should carry a contype='n' constraint (PK implies NOT NULL); got %+v", parentTbl.NotNullConstraints)
	}
	if !parentNC.IsLocal || parentNC.InhCount != 0 {
		t.Errorf("parent_t.id NOT NULL should be conislocal=t/coninhcount=0, got IsLocal=%v InhCount=%d", parentNC.IsLocal, parentNC.InhCount)
	}

	childNC, ok := nnFor(childTbl, "id")
	if !ok {
		t.Fatalf("child_t.id should carry a contype='n' constraint inherited from parent_t; got %+v", childTbl.NotNullConstraints)
	}
	if childNC.IsLocal {
		t.Errorf("child_t.id NOT NULL should be conislocal=f (purely inherited via INHERITS), got IsLocal=true")
	}
	if childNC.InhCount != 1 {
		t.Errorf("child_t.id NOT NULL should be coninhcount=1, got %d", childNC.InhCount)
	}
	if childNC.Name != parentNC.Name {
		t.Errorf("child_t.id NOT NULL constraint name should match the parent's (%q), got %q", parentNC.Name, childNC.Name)
	}

	// A column the child ALSO declares itself (even one implied NOT NULL by a
	// child-local PRIMARY KEY) is locally defined (conislocal=t, own name),
	// but since the INHERITS parent ALSO enforces NOT NULL on it, real PG's
	// MergeAttributes ("merging column ... with inherited definition") still
	// counts that parent enforcement: coninhcount=1, not 0 (verified live
	// against PG 18.3: `child2_t_id_not_null`/conislocal=t/coninhcount=1).
	if err := runDDL(t, ctx, `CREATE TABLE child2_t (id integer NOT NULL, extra text) INHERITS (parent_t)`); err != nil {
		t.Fatalf("CREATE TABLE child2_t: %v", err)
	}
	child2Tbl, ok := cat.LookupTable(parser.ObjectName{Name: "child2_t"})
	if !ok {
		t.Fatal("child2_t table not found")
	}
	child2NC, ok := nnFor(child2Tbl, "id")
	if !ok {
		t.Fatalf("child2_t.id should carry a contype='n' constraint; got %+v", child2Tbl.NotNullConstraints)
	}
	if !child2NC.IsLocal || child2NC.InhCount != 1 {
		t.Errorf("child2_t.id (locally redeclared, parent also NOT NULL) NOT NULL should be conislocal=t/coninhcount=1, got IsLocal=%v InhCount=%d", child2NC.IsLocal, child2NC.InhCount)
	}
}

// TestCreatePartitionChildNotNullConstraintLocality verifies that CREATE TABLE
// ... PARTITION OF registers a contype='n' pg_constraint row for EVERY NOT
// NULL column, not just ones explicitly listed in the partition's own
// column-constraint clause. Verified against live PostgreSQL 18.3:
//   - A column NOT NULL purely because the parent enforces it (not
//     re-declared on the partition) reuses the PARENT's constraint name with
//     conislocal='f'/coninhcount=1.
//   - A column explicitly re-declared NOT NULL on the partition gets its own
//     <child>_<col>_not_null name with conislocal='t'; coninhcount is 1 if
//     the parent also enforces it there, 0 if the parent column is nullable.
//
// M0110-0001 (DU-002 partition-child NOT NULL locality).
func TestCreatePartitionChildNotNullConstraintLocality(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE p (id int NOT NULL, v text) PARTITION BY RANGE (id)`); err != nil {
		t.Fatalf("CREATE TABLE p: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE p1 PARTITION OF p FOR VALUES FROM (0) TO (100)`); err != nil {
		t.Fatalf("CREATE TABLE p1: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE p2 (id int, v text) PARTITION BY RANGE (id)`); err != nil {
		t.Fatalf("CREATE TABLE p2: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE p2a PARTITION OF p2 (v NOT NULL) FOR VALUES FROM (0) TO (100)`); err != nil {
		t.Fatalf("CREATE TABLE p2a: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE p3 (id int NOT NULL, v text) PARTITION BY RANGE (id)`); err != nil {
		t.Fatalf("CREATE TABLE p3: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE p3a PARTITION OF p3 (id NOT NULL) FOR VALUES FROM (0) TO (100)`); err != nil {
		t.Fatalf("CREATE TABLE p3a: %v", err)
	}

	nnFor := func(tbl *catalog.Table, col string) (catalog.NamedNotNullConstraint, bool) {
		for _, nc := range tbl.NotNullConstraints {
			if strings.EqualFold(nc.ColName, col) {
				return nc, true
			}
		}
		return catalog.NamedNotNullConstraint{}, false
	}
	lookup := func(name string) *catalog.Table {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("%s table not found", name)
		}
		return tbl
	}

	pTbl, p1Tbl := lookup("p"), lookup("p1")
	pNC, ok := nnFor(pTbl, "id")
	if !ok {
		t.Fatalf("p.id should carry a contype='n' constraint; got %+v", pTbl.NotNullConstraints)
	}
	// Purely inherited (not re-declared on p1): reuse parent's name, non-local.
	p1NC, ok := nnFor(p1Tbl, "id")
	if !ok {
		t.Fatalf("p1.id should carry a contype='n' constraint inherited from p; got %+v", p1Tbl.NotNullConstraints)
	}
	if p1NC.IsLocal {
		t.Errorf("p1.id NOT NULL should be conislocal=f (purely inherited from partition parent), got IsLocal=true")
	}
	if p1NC.InhCount != 1 {
		t.Errorf("p1.id NOT NULL should be coninhcount=1, got %d", p1NC.InhCount)
	}
	if p1NC.Name != pNC.Name {
		t.Errorf("p1.id NOT NULL constraint name should match the parent's (%q), got %q", pNC.Name, p1NC.Name)
	}

	// Explicitly re-declared on the partition; parent column is nullable:
	// local, own name, coninhcount=0.
	p2aTbl := lookup("p2a")
	p2aNC, ok := nnFor(p2aTbl, "v")
	if !ok {
		t.Fatalf("p2a.v should carry a contype='n' constraint; got %+v", p2aTbl.NotNullConstraints)
	}
	if !p2aNC.IsLocal || p2aNC.InhCount != 0 {
		t.Errorf("p2a.v (locally declared, parent nullable) NOT NULL should be conislocal=t/coninhcount=0, got IsLocal=%v InhCount=%d", p2aNC.IsLocal, p2aNC.InhCount)
	}
	if p2aNC.Name != "p2a_v_not_null" {
		t.Errorf("p2a.v NOT NULL constraint should get its own name, got %q", p2aNC.Name)
	}

	// Explicitly re-declared on the partition; parent ALSO enforces NOT NULL:
	// local, own name (not the parent's), coninhcount=1.
	p3aTbl := lookup("p3a")
	p3aNC, ok := nnFor(p3aTbl, "id")
	if !ok {
		t.Fatalf("p3a.id should carry a contype='n' constraint; got %+v", p3aTbl.NotNullConstraints)
	}
	if !p3aNC.IsLocal || p3aNC.InhCount != 1 {
		t.Errorf("p3a.id (locally redeclared, parent also NOT NULL) NOT NULL should be conislocal=t/coninhcount=1, got IsLocal=%v InhCount=%d", p3aNC.IsLocal, p3aNC.InhCount)
	}
	if p3aNC.Name != "p3a_id_not_null" {
		t.Errorf("p3a.id NOT NULL constraint should get its own name (not the parent's), got %q", p3aNC.Name)
	}
}

// TestAlterTableAddNotNullNamed verifies that `ALTER TABLE ... ADD CONSTRAINT
// <name> NOT NULL <col>` marks the column NOT NULL AND records a contype='n'
// constraint carrying the EXPLICIT name (not the auto-name), with
// conislocal=true. When the name differs from PG's default
// `<table>_<col>_not_null`, pg_dump prints the `CONSTRAINT <name> NOT NULL
// <col>` form. A bare `ADD NOT NULL <col>` (no name) falls back to the
// auto-name. DU-002 slice 271.
func TestAlterTableAddNotNullNamed(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE annt (a integer, b integer)`); err != nil {
		t.Fatalf("CREATE TABLE annt: %v", err)
	}
	// Named form: the constraint keeps the explicit name `my_nn`.
	if err := runDDL(t, ctx, `ALTER TABLE annt ADD CONSTRAINT my_nn NOT NULL a`); err != nil {
		t.Fatalf("ADD CONSTRAINT my_nn NOT NULL a: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "annt"})
	if !ok {
		t.Fatal("annt table not found")
	}
	if !tbl.Columns[0].NotNull {
		t.Errorf("column a should be NotNull after ADD CONSTRAINT NOT NULL")
	}
	nnFor := func(col string) (catalog.NamedNotNullConstraint, bool) {
		for _, nc := range tbl.NotNullConstraints {
			if strings.EqualFold(nc.ColName, col) {
				return nc, true
			}
		}
		return catalog.NamedNotNullConstraint{}, false
	}
	nc, ok := nnFor("a")
	if !ok {
		t.Fatalf("ADD CONSTRAINT NOT NULL did not record a contype='n' constraint for a; got %+v", tbl.NotNullConstraints)
	}
	if nc.Name != "my_nn" {
		t.Errorf("NOT NULL constraint name=%q want my_nn (explicit name)", nc.Name)
	}
	if !nc.IsLocal {
		t.Errorf("NOT NULL constraint for a should be conislocal=true")
	}

	// Idempotent: a second ADD on the same column must not add a duplicate row.
	if err := runDDL(t, ctx, `ALTER TABLE annt ADD CONSTRAINT other_nn NOT NULL a`); err != nil {
		t.Fatalf("second ADD NOT NULL a: %v", err)
	}
	count := 0
	for _, c := range tbl.NotNullConstraints {
		if strings.EqualFold(c.ColName, "a") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("idempotent ADD NOT NULL produced %d constraints for a, want 1", count)
	}

	// Bare `ADD NOT NULL b` (no CONSTRAINT name) falls back to the auto-name.
	if err := runDDL(t, ctx, `ALTER TABLE annt ADD NOT NULL b`); err != nil {
		t.Fatalf("ADD NOT NULL b: %v", err)
	}
	if !tbl.Columns[1].NotNull {
		t.Errorf("column b should be NotNull after ADD NOT NULL")
	}
	ncb, ok := nnFor("b")
	if !ok {
		t.Fatalf("ADD NOT NULL b did not record a constraint; got %+v", tbl.NotNullConstraints)
	}
	if ncb.Name != "annt_b_not_null" {
		t.Errorf("unnamed ADD NOT NULL name=%q want annt_b_not_null (auto-name)", ncb.Name)
	}

	// A missing column raises 42703.
	err := runDDL(t, ctx, `ALTER TABLE annt ADD CONSTRAINT z_nn NOT NULL nope`)
	if err == nil {
		t.Fatal("ADD NOT NULL on a missing column should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42703" {
		t.Errorf("missing column: err=%v want SQLSTATE 42703", err)
	}
}

// TestAlterTableAddNotNullNotValid verifies the PG18 `ADD CONSTRAINT name NOT
// NULL col NOT VALID` trailer (alter_table.sql:915 `ALTER TABLE atnnparted ADD
// CONSTRAINT dummy_constr NOT NULL id NOT VALID`): the constraint is recorded
// with convalidated='f' in pg_constraint (psql renders the trailing ` NOT
// VALID`), and `VALIDATE CONSTRAINT name` flips it back to 't' (re-renders
// without ` NOT VALID`). PG excludes CONSTR_NOTNULL from the Phase-3 pre-scan
// (ATExecValidateConstraint, tablecmds.c:9956), so the flip is the whole
// behavior — no row scan. M0134-0002 C2 slice 10.
func TestAlterTableAddNotNullNotValid(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE atnnparted (id int, col1 int)`); err != nil {
		t.Fatalf("CREATE TABLE atnnparted: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE atnnparted ADD CONSTRAINT dummy_constr NOT NULL id NOT VALID`); err != nil {
		t.Fatalf("ADD CONSTRAINT dummy_constr NOT NULL id NOT VALID: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "atnnparted"})
	if !ok {
		t.Fatal("atnnparted table not found")
	}
	if len(tbl.NotNullConstraints) != 1 || tbl.NotNullConstraints[0].Name != "dummy_constr" {
		t.Fatalf("expected 1 NOT NULL constraint dummy_constr, got %+v", tbl.NotNullConstraints)
	}
	if !tbl.NotNullConstraints[0].NotValid {
		t.Fatalf("expected NotNullConstraints[0].NotValid=true, got %+v", tbl.NotNullConstraints[0])
	}

	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	// PGConstraintRowsForDBOid re-reads the live in-memory catalog on every
	// call, so re-invoking VirtualRows after VALIDATE observes the flip.
	convalidated := func() string {
		for _, r := range pgcon.VirtualRows() {
			if r[3] == "n" && r[1] == "dummy_constr" {
				return r[6]
			}
		}
		return ""
	}
	if got := convalidated(); got != "f" {
		t.Errorf("convalidated after ADD ... NOT VALID = %q, want f", got)
	}

	// VALIDATE CONSTRAINT flips convalidated back to 't'; psql's \d+ then
	// re-renders the constraint without the ` NOT VALID` suffix (alter_table.sql:922).
	if err := runDDL(t, ctx, `ALTER TABLE atnnparted VALIDATE CONSTRAINT dummy_constr`); err != nil {
		t.Fatalf("ALTER TABLE atnnparted VALIDATE CONSTRAINT dummy_constr: %v", err)
	}
	if tbl.NotNullConstraints[0].NotValid {
		t.Errorf("NotValid still true after VALIDATE CONSTRAINT")
	}
	if got := convalidated(); got != "t" {
		t.Errorf("convalidated after VALIDATE = %q, want t", got)
	}

	// An unknown name still raises 42704.
	err := runDDL(t, ctx, `ALTER TABLE atnnparted VALIDATE CONSTRAINT no_such_constraint`)
	if err == nil {
		t.Fatal("VALIDATE CONSTRAINT on a missing name should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("missing constraint: err=%v want SQLSTATE 42704", err)
	}
}

// TestCheckViolationReportsNameAndDetail verifies that a CHECK constraint
// violation reports the constraint name and a "Failing row contains (…)"
// DETAIL line, matching PostgreSQL (SQLSTATE 23514). M0097-0023.
func TestCheckViolationReportsNameAndDetail(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE inhg (x text, xx text, y text)`); err != nil {
		t.Fatalf("CREATE TABLE inhg: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE inhg ADD CONSTRAINT foo CHECK (xx = 'text')`); err != nil {
		t.Fatalf("ALTER TABLE inhg ADD CONSTRAINT: %v", err)
	}
	inhg, ok := cat.LookupTable(parser.ObjectName{Name: "inhg"})
	if !ok {
		t.Fatal("inhg table not found")
	}

	// Violating row: xx = 'foo' != 'text'.
	row := Row{NewStringDatum("x"), NewStringDatum("foo"), NewStringDatum("y")}
	err := checkConstraints(ctx, inhg, row)
	if err == nil {
		t.Fatal("expected check constraint violation, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23514" {
		t.Errorf("expected SQLSTATE 23514, got %q", ee.Code)
	}
	wantMsg := `new row for relation "inhg" violates check constraint "foo"`
	if ee.Message != wantMsg {
		t.Errorf("message = %q, want %q", ee.Message, wantMsg)
	}
	wantDetail := "Failing row contains (x, foo, y)."
	if !strings.Contains(ee.Detail, wantDetail) {
		t.Errorf("detail = %q, want it to contain %q", ee.Detail, wantDetail)
	}

	// A satisfying row must pass.
	okRow := Row{NewStringDatum("x"), NewStringDatum("text"), NewStringDatum("y")}
	if err := checkConstraints(ctx, inhg, okRow); err != nil {
		t.Errorf("satisfying row should pass, got %v", err)
	}

	// The named CHECK must now carry a real OID (the latent virtual-table join
	// crash that previously forced OID 0 was fixed in M0097-0023-loop34) and
	// surface as a row in pg_constraint. M0097-0023.
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not InMemory")
	}
	var fooOID uint32
	for _, nc := range inhg.NamedChecks {
		if nc.Name == "foo" {
			fooOID = nc.OID
		}
	}
	if fooOID == 0 {
		t.Fatalf("named check %q was not assigned an OID", "foo")
	}

	// pg_constraint's VirtualRows must emit exactly one row for the constraint,
	// with contype='c', conrelid=inhg.OID, conname='foo', and conbin=the expr.
	pgcon, ok := im.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	var found bool
	for _, r := range pgcon.VirtualRows() {
		if len(r) < 25 || r[1] != "foo" {
			continue
		}
		found = true
		if r[0] != fmt.Sprintf("%d", fooOID) {
			t.Errorf("pg_constraint oid = %q, want %d", r[0], fooOID)
		}
		if r[3] != "c" {
			t.Errorf("pg_constraint contype = %q, want \"c\"", r[3])
		}
		if r[7] != fmt.Sprintf("%d", inhg.OID) {
			t.Errorf("pg_constraint conrelid = %q, want %d", r[7], inhg.OID)
		}
		if r[24] != "xx = 'text'" {
			t.Errorf("pg_constraint conbin = %q, want %q", r[24], "xx = 'text'")
		}
	}
	if !found {
		t.Errorf("pg_constraint did not surface named check %q", "foo")
	}
}
