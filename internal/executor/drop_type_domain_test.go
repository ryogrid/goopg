package executor

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestDropTypeOfDomain pins M0148-0001j3 item 6: PG's DROP TYPE resolves the
// name through get_object_address_type with no typtype check, so it drops a
// domain exactly as DROP DOMAIN does (regress domain.sql `drop type vc4;`,
// jsonb.sql's mixed `drop type jsb_ia, …, jsb_i_not_null_rec`): RESTRICT
// refuses a domain that a column still uses (2BP01), CASCADE drops the
// column, and an unused domain simply goes away.
func TestDropTypeOfDomain(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE DOMAIN vc4 AS varchar(4)",
		"CREATE DOMAIN dfree AS int",
		"CREATE TYPE dct AS (a int)",
		"CREATE TABLE vt (k int, v vc4)",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var ee *ExecError
	if err := runDDL(t, ctx, "DROP TYPE vc4"); !errors.As(err, &ee) || ee.Code != "2BP01" {
		t.Fatalf("DROP TYPE vc4 with a dependent column: err = %v, want 2BP01", err)
	}
	if err := runDDL(t, ctx, "DROP TYPE dfree, dct"); err != nil {
		t.Fatalf("DROP TYPE dfree, dct: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	if _, ok := im.LookupDomain("dfree", ctx.CurrentDatabaseOid); ok {
		t.Errorf("domain dfree survived DROP TYPE")
	}
	if im.LookupCompositeType("dct", ctx.CurrentDatabaseOid) != nil {
		t.Errorf("composite dct survived DROP TYPE")
	}
	if err := runDDL(t, ctx, "DROP TYPE vc4 CASCADE"); err != nil {
		t.Fatalf("DROP TYPE vc4 CASCADE: %v", err)
	}
	if _, ok := im.LookupDomain("vc4", ctx.CurrentDatabaseOid); ok {
		t.Errorf("domain vc4 survived DROP TYPE CASCADE")
	}
	tbl, ok := im.LookupTable(parser.ObjectName{Name: "vt"})
	if !ok {
		t.Fatalf("table vt was dropped; CASCADE must drop only the column")
	}
	for _, c := range tbl.Columns {
		if c.Name == "v" && !c.Dropped {
			t.Errorf("column vt.v survived DROP TYPE vc4 CASCADE")
		}
	}
	if err := runDDL(t, ctx, "DROP TYPE IF EXISTS vc4"); err != nil {
		t.Errorf("DROP TYPE IF EXISTS vc4 after the drop: %v", err)
	}
}
