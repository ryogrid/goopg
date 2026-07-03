package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCommentOnForeignTableStoresDescription verifies COMMENT ON FOREIGN TABLE
// resolves the same pg_class (classoid=1259) LookupTable path as COMMENT ON
// TABLE/VIEW/SEQUENCE/MATERIALIZED VIEW. Before slice 435 "COMMENT ON FOREIGN
// TABLE" was a hard parser error, not merely a dropped comment. DU-002 slice 435.
func TestCommentOnForeignTableStoresDescription(t *testing.T) {
	const oidPgClass = 1259

	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SERVER srv FOREIGN DATA WRAPPER goopg_fdw`); err != nil {
		t.Fatalf("CREATE SERVER: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FOREIGN TABLE ft1 (a int, b text) SERVER srv`); err != nil {
		t.Fatalf("CREATE FOREIGN TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `COMMENT ON FOREIGN TABLE ft1 IS 'a foreign table comment'`); err != nil {
		t.Fatalf("COMMENT ON FOREIGN TABLE: %v", err)
	}

	im := cat.(*catalog.InMemory)
	tbl, ok := im.LookupTable(parser.ObjectName{Name: "ft1"})
	if !ok {
		t.Fatalf("LookupTable(ft1) not found")
	}
	desc, ok := im.GetComment(oidPgClass, tbl.OID, 0)
	if !ok {
		t.Fatalf("GetComment(pg_class, %d, 0) not found", tbl.OID)
	}
	if desc != "a foreign table comment" {
		t.Errorf("description=%q want %q", desc, "a foreign table comment")
	}
}
