package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgPublicationRel pins the M0106-0010
// step 3by catalog seed for pg_publication_rel (OID 6106). PG's
// standby boot opens `RelationBuildDesc(6106)` once Step 3bx's
// pg_publication_namespace family cleared the previous FATAL; without
// this entry it FATALs with `could not open relation with OID 6106`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_publication_rel.h
//     (PublicationRelRelationId = 6106, 5 columns: 3 fixed-width
//     NOT NULL + 2 CATALOG_VARLEN nullable).
func TestNailedLocalRelsContainsPgPublicationRel(t *testing.T) {
	const prOID = 6106

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == prOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_publication_rel) — step 3by regression", prOID)
	}
	if got.RelName != "pg_publication_rel" {
		t.Fatalf("OID %d: RelName=%q want %q", prOID, got.RelName, "pg_publication_rel")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", prOID, got.RelKind)
	}
	if got.RelNatts != 5 {
		t.Fatalf("OID %d: RelNatts=%d want 5 (PG18 Natts_pg_publication_rel)", prOID, got.RelNatts)
	}
	if len(got.Attrs) != 5 {
		t.Fatalf("OID %d: len(Attrs)=%d want 5", prOID, len(got.Attrs))
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
		{"prpubid", 26, 2, 4, true},
		{"prrelid", 26, 3, 4, true},
		{"prqual", 194, 4, -1, false},
		{"prattrs", 22, 5, -1, false},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgPublicationRel
// pins that the step 3by seed wires pg_publication_rel (OID 6106)
// into the empty-heap placeholder list so PG's mdopen finds a valid
// 8-KiB heap file at base/{1,5}/6106 once the pg_class row resolves
// the relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgPublicationRel(t *testing.T) {
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
		path := filepath.Join(dir, db, "6106")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3by regression)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}

// TestPgPublicationRelOidIndexInitialEntry pins the
// pgIndexInitialEntries row for the UNIQUE PRIMARY OID index (6112)
// over pg_publication_rel (6106). PG's RelationInitIndexAccessInfo
// requires indkey/indclass/indcollation/indisunique/indisprimary to
// agree with the upstream pg_publication_rel.h declaration.
func TestPgPublicationRelOidIndexInitialEntry(t *testing.T) {
	const (
		idxOID = 6112
		relOID = 6106
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
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_publication_rel_oid_index) — step 3by regression", idxOID)
	}
	if got.IndRelid != relOID {
		t.Errorf("IndRelid=%d want %d (pg_publication_rel)", got.IndRelid, relOID)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 1 {
		t.Errorf("IndKey=%v want [1] (oid attnum)", got.IndKey)
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

// TestPgPublicationRelPrrelidPrpubidIndexInitialEntry pins the
// pgIndexInitialEntries row for the UNIQUE (non-PKEY) composite
// index (6113) over pg_publication_rel (6106) on
// btree(prrelid oid_ops, prpubid oid_ops).
func TestPgPublicationRelPrrelidPrpubidIndexInitialEntry(t *testing.T) {
	const (
		idxOID = 6113
		relOID = 6106
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
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_publication_rel_prrelid_prpubid_index) — step 3by regression", idxOID)
	}
	if got.IndRelid != relOID {
		t.Errorf("IndRelid=%d want %d (pg_publication_rel)", got.IndRelid, relOID)
	}
	wantKey := []int16{3, 2} // prrelid (attnum 3), prpubid (attnum 2)
	if len(got.IndKey) != 2 || got.IndKey[0] != wantKey[0] || got.IndKey[1] != wantKey[1] {
		t.Errorf("IndKey=%v want %v (prrelid, prpubid)", got.IndKey, wantKey)
	}
	wantClass := []uint32{oidOps, oidOps}
	if len(got.IndClass) != 2 || got.IndClass[0] != wantClass[0] || got.IndClass[1] != wantClass[1] {
		t.Errorf("IndClass=%v want %v (oid_ops, oid_ops)", got.IndClass, wantClass)
	}
	if len(got.IndCollation) != 2 || got.IndCollation[0] != 0 || got.IndCollation[1] != 0 {
		t.Errorf("IndCollation=%v want [0 0] (no collation for oid_ops)", got.IndCollation)
	}
	if !got.IsUnique {
		t.Errorf("IsUnique=false want true (DECLARE_UNIQUE_INDEX)")
	}
	if got.IsPrimary {
		t.Errorf("IsPrimary=true want false (DECLARE_UNIQUE_INDEX, not the _PKEY variant)")
	}
}

// TestPgPublicationRelPrpubidIndexInitialEntry pins the
// pgIndexInitialEntries row for the non-UNIQUE single-column index
// (6116) over pg_publication_rel (6106) on btree(prpubid oid_ops).
// First non-UNIQUE entry seeded for the pg_publication_rel family;
// declared via `DECLARE_INDEX` (no _UNIQUE prefix).
func TestPgPublicationRelPrpubidIndexInitialEntry(t *testing.T) {
	const (
		idxOID = 6116
		relOID = 6106
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
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_publication_rel_prpubid_index) — step 3by regression", idxOID)
	}
	if got.IndRelid != relOID {
		t.Errorf("IndRelid=%d want %d (pg_publication_rel)", got.IndRelid, relOID)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 2 {
		t.Errorf("IndKey=%v want [2] (prpubid attnum)", got.IndKey)
	}
	if len(got.IndClass) != 1 || got.IndClass[0] != oidOps {
		t.Errorf("IndClass=%v want [%d] (oid_ops)", got.IndClass, oidOps)
	}
	if len(got.IndCollation) != 1 || got.IndCollation[0] != 0 {
		t.Errorf("IndCollation=%v want [0] (no collation for oid_ops)", got.IndCollation)
	}
	if got.IsUnique {
		t.Errorf("IsUnique=true want false (DECLARE_INDEX, not DECLARE_UNIQUE_INDEX)")
	}
	if got.IsPrimary {
		t.Errorf("IsPrimary=true want false (non-UNIQUE index cannot be PRIMARY)")
	}
}
