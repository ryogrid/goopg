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
