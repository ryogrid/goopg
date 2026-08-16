package postmaster

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/access/transam/xlog"
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
	snap := xlog.BuildCatalogSnapshot(im, executor.RegOutRenderer(im, false))
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
	snap := xlog.BuildCatalogSnapshot(im, executor.RegOutRendererVisible(im, visible))
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

// TestRegOutRendererCrossDB pins M0119-0006 (deferral row 1354 claim 2)
// acceptance criterion #4 through the walsender's exact binding shape: the
// renderer AND the snapshot are scoped to the connection's dbOid, so a
// regclass in a non-default database resolves to its NAME instead of falling
// to the numeric OID fallback (regproc.c:943-987) that a DB-1-scoped
// resolution produced for the same dangling-in-DB-1 OID.
func TestRegOutRendererCrossDB(t *testing.T) {
	im := catalog.NewInMemory()
	db2Oid, err := im.CreateDatabase("db2", catalog.BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase(db2): %v", err)
	}
	if _, err := im.CreateTable(parser.ObjectName{Name: "tbl"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
	}, db2Oid); err != nil {
		t.Fatalf("CREATE TABLE db2.tbl: %v", err)
	}
	tbl2, ok := im.LookupTable(parser.ObjectName{Name: "tbl"}, db2Oid)
	if !ok {
		t.Fatal("DB-2 tbl not found")
	}

	// The walsender's binding (logicalwalsender.go): visible set {pg_catalog,
	// public}, renderer + snapshot scoped to the connection dbOid.
	visible := func(s string) bool { return s == "" || s == "pg_catalog" || s == "public" }
	snap := xlog.BuildCatalogSnapshot(im, executor.RegOutRendererVisible(im, visible, db2Oid), db2Oid)
	if snap.RegOut == nil {
		t.Fatal("snap.RegOut is nil — the walsender wiring did not bind the renderer")
	}
	if got := snap.RegOut("regclass", tbl2.OID); got != "tbl" {
		t.Errorf("regclass(DB-2 tbl) with dbOid=%d rendered %q, want %q", db2Oid, got, "tbl")
	}

	// Without a dbOid the renderer resolves against DB 1's namespace: the
	// DB-2 relation is a dangling OID there → numeric fallback.
	snapDefault := xlog.BuildCatalogSnapshot(im, executor.RegOutRendererVisible(im, visible))
	wantNumeric := strconv.FormatUint(uint64(tbl2.OID), 10)
	if got := snapDefault.RegOut("regclass", tbl2.OID); got != wantNumeric {
		t.Errorf("regclass(DB-2 tbl) with default dbOid rendered %q, want numeric fallback %q", got, wantNumeric)
	}
}
