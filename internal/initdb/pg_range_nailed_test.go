package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgRange pins the M0106-0010 step 3bz
// catalog seed for pg_range (OID 3541). PG's standby boot opens
// `RelationBuildDesc(3541)` once Step 3by's pg_publication_rel
// family cleared the previous FATAL; without this entry it FATALs
// with `could not open relation with OID 3541`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_range.h
//     (RangeRelationId = 3541, 7 columns, no oid system column).
func TestNailedLocalRelsContainsPgRange(t *testing.T) {
	const rangeOID = 3541

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == rangeOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_range) — step 3bz regression", rangeOID)
	}
	if got.RelName != "pg_range" {
		t.Fatalf("OID %d: RelName=%q want %q", rangeOID, got.RelName, "pg_range")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", rangeOID, got.RelKind)
	}
	if got.RelNatts != 7 {
		t.Fatalf("OID %d: RelNatts=%d want 7 (PG18 Natts_pg_range)", rangeOID, got.RelNatts)
	}
	if len(got.Attrs) != 7 {
		t.Fatalf("OID %d: len(Attrs)=%d want 7", rangeOID, len(got.Attrs))
	}

	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"rngtypid", 26, 1, 4, true},
		{"rngsubtype", 26, 2, 4, true},
		{"rngmultitypid", 26, 3, 4, true},
		{"rngcollation", 26, 4, 4, true},
		{"rngsubopc", 26, 5, 4, true},
		{"rngcanonical", 24, 6, 4, true},
		{"rngsubdiff", 24, 7, 4, true},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgRange pins that the
// step 3bz seed wires pg_range (OID 3541) into the empty-heap
// placeholder list so PG's mdopen finds a valid 8-KiB heap file at
// base/{1,5}/3541 once the pg_class row resolves the relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgRange(t *testing.T) {
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
		path := filepath.Join(dir, db, "3541")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3bz regression)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}

// TestPgRangeRngtypidIndexInitialEntry pins the pgIndexInitialEntries
// row for the UNIQUE PRIMARY index (3542) over pg_range (3541) on
// btree(rngtypid oid_ops). PG's RelationInitIndexAccessInfo requires
// indkey/indclass/indcollation/indisunique/indisprimary to agree
// with the upstream pg_range.h declaration.
func TestPgRangeRngtypidIndexInitialEntry(t *testing.T) {
	const (
		idxOID = 3542
		relOID = 3541
		oidOps = uint32(1981)
	)
	var got *pgIndexEntry
	for _, e := range pgIndexInitialEntries() {
		if e.IndexRelid == idxOID {
			e := e
			got = &e
			break
		}
	}
	if got == nil {
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_range_rngtypid_index) — step 3bz regression", idxOID)
	}
	if got.IndRelid != relOID {
		t.Errorf("IndRelid=%d want %d (pg_range)", got.IndRelid, relOID)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 1 {
		t.Errorf("IndKey=%v want [1] (rngtypid attnum)", got.IndKey)
	}
	if len(got.IndClass) != 1 || got.IndClass[0] != oidOps {
		t.Errorf("IndClass=%v want [%d] (oid_ops)", got.IndClass, oidOps)
	}
	if len(got.IndCollation) != 1 || got.IndCollation[0] != 0 {
		t.Errorf("IndCollation=%v want [0] (no collation for oid_ops)", got.IndCollation)
	}
	if !got.IsUnique {
		t.Errorf("IsUnique=false want true (DECLARE_UNIQUE_INDEX_PKEY)")
	}
	if !got.IsPrimary {
		t.Errorf("IsPrimary=false want true (DECLARE_UNIQUE_INDEX_PKEY)")
	}
}

// TestPgRangeRngmultitypidIndexInitialEntry pins the pgIndexInitialEntries
// row for the UNIQUE (non-PKEY) single-column index (2228) over pg_range
// (3541) on btree(rngmultitypid oid_ops). attnum 3 = rngmultitypid per
// pg_range_d.h.
func TestPgRangeRngmultitypidIndexInitialEntry(t *testing.T) {
	const (
		idxOID = 2228
		relOID = 3541
		oidOps = uint32(1981)
	)
	var got *pgIndexEntry
	for _, e := range pgIndexInitialEntries() {
		if e.IndexRelid == idxOID {
			e := e
			got = &e
			break
		}
	}
	if got == nil {
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_range_rngmultitypid_index) — step 3bz regression", idxOID)
	}
	if got.IndRelid != relOID {
		t.Errorf("IndRelid=%d want %d (pg_range)", got.IndRelid, relOID)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 3 {
		t.Errorf("IndKey=%v want [3] (rngmultitypid attnum)", got.IndKey)
	}
	if len(got.IndClass) != 1 || got.IndClass[0] != oidOps {
		t.Errorf("IndClass=%v want [%d] (oid_ops)", got.IndClass, oidOps)
	}
	if len(got.IndCollation) != 1 || got.IndCollation[0] != 0 {
		t.Errorf("IndCollation=%v want [0] (no collation for oid_ops)", got.IndCollation)
	}
	if !got.IsUnique {
		t.Errorf("IsUnique=false want true (DECLARE_UNIQUE_INDEX)")
	}
	if got.IsPrimary {
		t.Errorf("IsPrimary=true want false (DECLARE_UNIQUE_INDEX, not the _PKEY variant)")
	}
}
