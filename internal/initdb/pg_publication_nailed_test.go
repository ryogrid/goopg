package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgPublication pins the M0106-0010
// step 3bu catalog seed for pg_publication (OID 6104). PG's standby
// boot opens `RelationBuildDesc(6104)` once Step 3bt's
// pg_partitioned_table_partrelid_index seed cleared the previous
// FATAL; without this entry it FATALs with `could not open relation
// with OID 6104`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_publication.h
//     (PublicationRelationId = 6104, 10 columns, all fixed-width NOT NULL).
func TestNailedLocalRelsContainsPgPublication(t *testing.T) {
	const pubOID = 6104

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == pubOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_publication) — step 3bu regression", pubOID)
	}
	if got.RelName != "pg_publication" {
		t.Fatalf("OID %d: RelName=%q want %q", pubOID, got.RelName, "pg_publication")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", pubOID, got.RelKind)
	}
	if got.RelNatts != 10 {
		t.Fatalf("OID %d: RelNatts=%d want 10 (PG18 Natts_pg_publication)", pubOID, got.RelNatts)
	}
	if len(got.Attrs) != 10 {
		t.Fatalf("OID %d: len(Attrs)=%d want 10", pubOID, len(got.Attrs))
	}

	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"oid", 26, 1, 4, true},
		{"pubname", 19, 2, 64, true},
		{"pubowner", 26, 3, 4, true},
		{"puballtables", 16, 4, 1, true},
		{"pubinsert", 16, 5, 1, true},
		{"pubupdate", 16, 6, 1, true},
		{"pubdelete", 16, 7, 1, true},
		{"pubtruncate", 16, 8, 1, true},
		{"pubviaroot", 16, 9, 1, true},
		{"pubgencols", 18, 10, 1, true},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgPublication pins that
// the step 3bu seed wires pg_publication (OID 6104) into the
// empty-heap placeholder list so PG's mdopen finds a valid 8-KiB
// heap file at base/{1,5}/6104 once the pg_class row resolves the
// relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgPublication(t *testing.T) {
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
		path := filepath.Join(dir, db, "6104")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3bu regression)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}
