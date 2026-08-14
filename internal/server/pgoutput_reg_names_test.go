package server

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// TestPgoutputSnapshotRegOutRendererWired pins acceptance criterion #3: the
// publisher walsender binds a REAL executor.RegOutRenderer into the catalog
// snapshot, so TEXT-mode pgoutput emits a reg* column's NAME (regclassout via
// OidOutputFunctionCall, postgres/src/backend/replication/logical/proto.c:848)
// rather than the numeric OID that belongs to BINARY mode's typsend. regclass
// 1259 is pg_class itself, resolved against the in-memory catalog's system
// tables; a dangling OID keeps the numeric fallback. M0119-0006 (deferral row
// 1353).
func TestPgoutputSnapshotRegOutRendererWired(t *testing.T) {
	im := catalog.NewInMemory()
	snap := wal.BuildCatalogSnapshot(im, executor.RegOutRenderer(im, false))
	if snap.RegOut == nil {
		t.Fatal("snap.RegOut is nil — the walsender wiring did not bind the renderer")
	}
	if got := snap.RegOut("regclass", 1259); got != "pg_class" {
		t.Errorf("regclass 1259 rendered %q, want %q", got, "pg_class")
	}
	// A relation in pg_catalog never schema-qualifies (the search path always
	// searches it implicitly), so qualify=false yields the bare name.
	if got := snap.RegOut("regclass", 125999999); got != "125999999" {
		t.Errorf("dangling regclass OID rendered %q, want numeric fallback %q", got, "125999999")
	}
}

// TestPgoutputSnapshotRegOutRendererVisibleOffPathQualifies pins acceptance
// criterion #3's schema-qualification half (M0119-0006, deferral row 1354 claim
// 1): the publisher walsender binds the NEW RegOutRendererVisible with the
// synthesized default-search_path predicate {pg_catalog, public}, so an off-path
// regclass renders schema-qualified (`other_schema.tbl`) where the pre-84th
// fixed qualify=false renderer emitted the bare `tbl` — matching regclassout's
// RelationIsVisible arm (regproc.c:973-981). pg_class (1259) stays bare and a
// dangling OID keeps the numeric fallback, the assertions already pinned by the
// sibling test above.
func TestPgoutputSnapshotRegOutRendererVisibleOffPathQualifies(t *testing.T) {
	im := catalog.NewInMemory()
	im.RegisterSchema("other_schema")
	if err := createSiblingTable(t, im, `CREATE TABLE other_schema.tbl (id int)`); err != nil {
		t.Fatalf("CREATE TABLE other_schema.tbl: %v", err)
	}
	other, ok := im.LookupTable(parser.ObjectName{Schema: "other_schema", Name: "tbl"})
	if !ok {
		t.Fatal("other_schema.tbl not found")
	}
	// The walsender's binding (logicalwalsender.go: the publisher side never
	// SETs search_path, so the visible set is effectively {pg_catalog, public}).
	visible := func(s string) bool { return s == "" || s == "pg_catalog" || s == "public" }
	snap := wal.BuildCatalogSnapshot(im, executor.RegOutRendererVisible(im, visible))
	if snap.RegOut == nil {
		t.Fatal("snap.RegOut is nil — the walsender wiring did not bind the renderer")
	}
	if got := snap.RegOut("regclass", other.OID); got != "other_schema.tbl" {
		t.Errorf("regclass(other_schema.tbl) rendered %q, want %q", got, "other_schema.tbl")
	}
	// A relation in pg_catalog never schema-qualifies (implicitly visible).
	if got := snap.RegOut("regclass", 1259); got != "pg_class" {
		t.Errorf("regclass 1259 rendered %q, want %q", got, "pg_class")
	}
	// An unresolvable OID keeps the numeric fallback.
	if got := snap.RegOut("regclass", 125999999); got != "125999999" {
		t.Errorf("dangling regclass OID rendered %q, want numeric fallback %q", got, "125999999")
	}
}
