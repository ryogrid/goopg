package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgForeignDataWrapper pins the M0106-0010 step
// 3bb catalog seed for pg_foreign_data_wrapper (OID 2328). PG's standby
// boot opens `RelationBuildDesc(2328)`; without this entry it FATALs with
// `could not open relation with OID 2328`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_foreign_data_wrapper.h
//   - postgres/src/include/catalog/pg_foreign_data_wrapper_d.h
//     (ForeignDataWrapperRelationId = 2328,
//      Natts_pg_foreign_data_wrapper = 7).
func TestNailedLocalRelsContainsPgForeignDataWrapper(t *testing.T) {
	const fdwOID = 2328

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == fdwOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_foreign_data_wrapper) — step 3bb regression", fdwOID)
	}
	if got.RelName != "pg_foreign_data_wrapper" {
		t.Fatalf("OID %d: RelName=%q want %q", fdwOID, got.RelName, "pg_foreign_data_wrapper")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", fdwOID, got.RelKind)
	}
	if got.RelNatts != 7 {
		t.Fatalf("OID %d: RelNatts=%d want 7 (PG18 Natts_pg_foreign_data_wrapper)", fdwOID, got.RelNatts)
	}
	if len(got.Attrs) != 7 {
		t.Fatalf("OID %d: len(Attrs)=%d want 7", fdwOID, len(got.Attrs))
	}

	// Per-attribute pins against PG18 pg_foreign_data_wrapper.h /
	// pg_foreign_data_wrapper_d.h. fdwacl (aclitem[] = 1034) and
	// fdwoptions (text[] = 1009) are in the CATALOG_VARLEN block with
	// no BKI_FORCE_NOT_NULL — nullable.
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"oid", 26, 1, 4, true},
		{"fdwname", 19, 2, 64, true},
		{"fdwowner", 26, 3, 4, true},
		{"fdwhandler", 26, 4, 4, true},
		{"fdwvalidator", 26, 5, 4, true},
		{"fdwacl", 1034, 6, -1, false},
		{"fdwoptions", 1009, 7, -1, false},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignDataWrapper pins
// that the step 3bb seed wires pg_foreign_data_wrapper (OID 2328) into
// the empty-heap placeholder list so PG's mdopen finds a valid 8-KiB
// heap file at base/{1,5}/2328 once the pg_class row resolves the
// relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignDataWrapper(t *testing.T) {
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
		path := filepath.Join(dir, db, "2328")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3bb regression)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}
