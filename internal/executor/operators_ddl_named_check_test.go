package executor

import (
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

	// Verify the named CHECK stays out of pg_constraint for now (OID 0 guard),
	// avoiding the latent virtual-table join bug.
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not InMemory")
	}
	for _, nc := range inhg.NamedChecks {
		if nc.OID != 0 {
			t.Errorf("named check %q unexpectedly assigned OID %d (would surface in pg_constraint and hit the latent join bug)", nc.Name, nc.OID)
		}
	}
	_ = im
}
