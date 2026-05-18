package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgOpfamily pins the M0106-0010 step 3bm
// catalog seed for pg_opfamily (OID 2753). PG's standby boot opens
// `RelationBuildDesc(2753)` once Step 3bl's pg_operator_oprname_l_r_n_index
// seed cleared the previous FATAL; without this entry it FATALs with
// `could not open relation with OID 2753`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_opfamily.h
//   - postgres/src/include/catalog/pg_opfamily_d.h
//     (OperatorFamilyRelationId = 2753, 5 columns all fixed-width
//     NOT NULL: oid, opfmethod, opfname, opfnamespace, opfowner).
func TestNailedLocalRelsContainsPgOpfamily(t *testing.T) {
	const opfOID = 2753

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == opfOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_opfamily) — step 3bm regression", opfOID)
	}
	if got.RelName != "pg_opfamily" {
		t.Fatalf("OID %d: RelName=%q want %q", opfOID, got.RelName, "pg_opfamily")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", opfOID, got.RelKind)
	}
	if got.RelNatts != 5 {
		t.Fatalf("OID %d: RelNatts=%d want 5 (PG18 Natts_pg_opfamily)", opfOID, got.RelNatts)
	}
	if len(got.Attrs) != 5 {
		t.Fatalf("OID %d: len(Attrs)=%d want 5", opfOID, len(got.Attrs))
	}

	// Per-attribute pins against PG18 pg_opfamily.h. No CATALOG_VARLEN
	// columns — every column is fixed-width NOT NULL.
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"oid", 26, 1, 4, true},
		{"opfmethod", 26, 2, 4, true},
		{"opfname", 19, 3, 64, true},
		{"opfnamespace", 26, 4, 4, true},
		{"opfowner", 26, 5, 4, true},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgOpfamily pins that the
// step 3bm seed wires pg_opfamily (OID 2753) into the empty-heap
// placeholder list so PG's mdopen finds a valid 8-KiB heap file at
// base/{1,5}/2753 once the pg_class row resolves the relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgOpfamily(t *testing.T) {
	dir := t.TempDir()
	for _, db := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, db), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := bootstrapMappedLocalCatalogHeaps(dir); err != nil {
		t.Fatalf("bootstrapMappedLocalCatalogHeaps: %v", err)
	}
	for _, db := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, db, "2753")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3bm regression)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}
