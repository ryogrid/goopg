package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgPartitionedTable pins the M0106-0010
// step 3bs catalog seed for pg_partitioned_table (OID 3350). PG's
// standby boot opens `RelationBuildDesc(3350)` once Step 3br's
// pg_parameter_acl_oid_index seed cleared the previous FATAL; without
// this entry it FATALs with `could not open relation with OID 3350`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_partitioned_table.h
//   - postgres/src/include/catalog/pg_partitioned_table_d.h
//     (PartitionedRelationId = 3350, 8 columns: 4 fixed-width NotNull
//     + 3 CATALOG_VARLEN BKI_FORCE_NOT_NULL + 1 CATALOG_VARLEN nullable).
func TestNailedLocalRelsContainsPgPartitionedTable(t *testing.T) {
	const ptOID = 3350

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == ptOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_partitioned_table) — step 3bs regression", ptOID)
	}
	if got.RelName != "pg_partitioned_table" {
		t.Fatalf("OID %d: RelName=%q want %q", ptOID, got.RelName, "pg_partitioned_table")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", ptOID, got.RelKind)
	}
	if got.RelNatts != 8 {
		t.Fatalf("OID %d: RelNatts=%d want 8 (PG18 Natts_pg_partitioned_table)", ptOID, got.RelNatts)
	}
	if len(got.Attrs) != 8 {
		t.Fatalf("OID %d: len(Attrs)=%d want 8", ptOID, len(got.Attrs))
	}

	// Per-attribute pins against PG18 pg_partitioned_table.h. The four
	// CATALOG_VARLEN columns (Len=-1) include the three BKI_FORCE_NOT_NULL
	// vector columns plus partexprs (nullable).
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"partrelid", 26, 1, 4, true},
		{"partstrat", 18, 2, 1, true},
		{"partnatts", 21, 3, 2, true},
		{"partdefid", 26, 4, 4, true},
		{"partattrs", 22, 5, -1, true},
		{"partclass", 30, 6, -1, true},
		{"partcollation", 30, 7, -1, true},
		{"partexprs", 194, 8, -1, false},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgPartitionedTable pins
// that the step 3bs seed wires pg_partitioned_table (OID 3350) into
// the empty-heap placeholder list so PG's mdopen finds a valid 8-KiB
// heap file at base/{1,5}/3350 once the pg_class row resolves the
// relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgPartitionedTable(t *testing.T) {
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
		path := filepath.Join(dir, db, "3350")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3bs regression)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}
