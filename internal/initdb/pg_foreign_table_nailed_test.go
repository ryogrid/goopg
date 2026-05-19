package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgForeignTable pins the M0106-0010 step 3bh
// catalog seed for pg_foreign_table (OID 3118). PG's standby boot opens
// `RelationBuildDesc(3118)`; without this entry it FATALs with
// `could not open relation with OID 3118`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_foreign_table.h
//   - postgres/src/include/catalog/pg_foreign_table_d.h
//     (ForeignTableRelationId = 3118, Natts_pg_foreign_table = 3).
//
// Unlike most system catalogs, pg_foreign_table has no `oid` system
// column; `ftrelid` (the OID of the pg_class row this entry describes)
// is the primary key.
func TestNailedLocalRelsContainsPgForeignTable(t *testing.T) {
	const ftOID = 3118

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == ftOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_foreign_table) — step 3bh regression", ftOID)
	}
	if got.RelName != "pg_foreign_table" {
		t.Fatalf("OID %d: RelName=%q want %q", ftOID, got.RelName, "pg_foreign_table")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", ftOID, got.RelKind)
	}
	if got.RelNatts != 3 {
		t.Fatalf("OID %d: RelNatts=%d want 3 (PG18 Natts_pg_foreign_table)", ftOID, got.RelNatts)
	}
	if len(got.Attrs) != 3 {
		t.Fatalf("OID %d: len(Attrs)=%d want 3", ftOID, len(got.Attrs))
	}

	// Per-attribute pins against PG18 pg_foreign_table.h /
	// pg_foreign_table_d.h. ftoptions is in the CATALOG_VARLEN block
	// with no BKI_FORCE_NOT_NULL — nullable.
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"ftrelid", 26, 1, 4, true},
		{"ftserver", 26, 2, 4, true},
		{"ftoptions", 1009, 3, -1, false},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignTable pins that the
// step 3bh seed wires pg_foreign_table (OID 3118) into the empty-heap
// placeholder list so PG's mdopen finds a valid 8-KiB heap file at
// base/{1,5}/3118 once the pg_class row resolves the relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignTable(t *testing.T) {
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
		path := filepath.Join(dir, db, "3118")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3bh regression)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}
