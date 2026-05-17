package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgExtension pins the M0106-0010 step 3aw
// catalog seed for pg_extension (OID 3079). PG's standby boot opens
// `RelationBuildDesc(3079)`; without this entry it FATALs with
// `could not open relation with OID 3079`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_extension.h
//   - postgres/src/include/catalog/pg_extension_d.h
//     (ExtensionRelationId = 3079, Natts_pg_extension = 8).
func TestNailedLocalRelsContainsPgExtension(t *testing.T) {
	const extensionOID = 3079

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == extensionOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_extension) — step 3aw regression", extensionOID)
	}
	if got.RelName != "pg_extension" {
		t.Fatalf("OID %d: RelName=%q want %q", extensionOID, got.RelName, "pg_extension")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", extensionOID, got.RelKind)
	}
	if got.RelNatts != 8 {
		t.Fatalf("OID %d: RelNatts=%d want 8 (PG18 Natts_pg_extension)", extensionOID, got.RelNatts)
	}
	if len(got.Attrs) != 8 {
		t.Fatalf("OID %d: len(Attrs)=%d want 8", extensionOID, len(got.Attrs))
	}

	// Per-attribute pins against PG18 pg_extension.h / pg_extension_d.h.
	// extversion is BKI_FORCE_NOT_NULL (so NOT NULL); extconfig (oid[] =
	// 1028) and extcondition (text[] = 1009) are nullable per
	// pg_extension.h:39 ("extversion may never be null, but the others can be").
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"oid", 26, 1, 4, true},
		{"extname", 19, 2, 64, true},
		{"extowner", 26, 3, 4, true},
		{"extnamespace", 26, 4, 4, true},
		{"extrelocatable", 16, 5, 1, true},
		{"extversion", 25, 6, -1, true},
		{"extconfig", 1028, 7, -1, false},
		{"extcondition", 1009, 8, -1, false},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgExtension pins that the
// step 3aw seed wires pg_extension (OID 3079) into the empty-heap
// placeholder list so PG's mdopen finds a valid 8-KiB heap file at
// base/{1,5}/3079 once the pg_class row resolves the relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgExtension(t *testing.T) {
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
		path := filepath.Join(dir, db, "3079")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3aw regression)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}
