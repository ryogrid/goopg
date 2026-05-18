package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgPublicationNamespace pins the M0106-0010
// step 3bx catalog seed for pg_publication_namespace (OID 6237). PG's
// standby boot opens `RelationBuildDesc(6237)` once Step 3bw's
// pg_publication_oid_index seed cleared the previous FATAL; without
// this entry it FATALs with `could not open relation with OID 6237`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_publication_namespace.h
//     (PublicationNamespaceRelationId = 6237, 3 columns, all fixed-width NOT NULL).
func TestNailedLocalRelsContainsPgPublicationNamespace(t *testing.T) {
	const nsOID = 6237

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == nsOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_publication_namespace) — step 3bx regression", nsOID)
	}
	if got.RelName != "pg_publication_namespace" {
		t.Fatalf("OID %d: RelName=%q want %q", nsOID, got.RelName, "pg_publication_namespace")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", nsOID, got.RelKind)
	}
	if got.RelNatts != 3 {
		t.Fatalf("OID %d: RelNatts=%d want 3 (PG18 Natts_pg_publication_namespace)", nsOID, got.RelNatts)
	}
	if len(got.Attrs) != 3 {
		t.Fatalf("OID %d: len(Attrs)=%d want 3", nsOID, len(got.Attrs))
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
		{"pnpubid", 26, 2, 4, true},
		{"pnnspid", 26, 3, 4, true},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgPublicationNamespace
// pins that the step 3bx seed wires pg_publication_namespace (OID
// 6237) into the empty-heap placeholder list so PG's mdopen finds a
// valid 8-KiB heap file at base/{1,5}/6237 once the pg_class row
// resolves the relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgPublicationNamespace(t *testing.T) {
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
		path := filepath.Join(dir, db, "6237")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3bx regression)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}

// TestPgPublicationNamespaceOidIndexInitialEntry pins the
// pgIndexInitialEntries row for the UNIQUE PRIMARY OID index (6238)
// over pg_publication_namespace (6237). PG's RelationInitIndexAccessInfo
// requires indkey/indclass/indcollation/indisunique/indisprimary to
// agree with the upstream pg_publication_namespace.h declaration.
func TestPgPublicationNamespaceOidIndexInitialEntry(t *testing.T) {
	const (
		idxOID  = 6238
		relOID  = 6237
		oidOps  = uint32(1981)
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
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_publication_namespace_oid_index) — step 3bx regression", idxOID)
	}
	if got.IndRelid != relOID {
		t.Errorf("IndRelid=%d want %d (pg_publication_namespace)", got.IndRelid, relOID)
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

// TestPgPublicationNamespacePnnspidPnpubidIndexInitialEntry pins the
// pgIndexInitialEntries row for the UNIQUE (non-PKEY) composite index
// (6239) over pg_publication_namespace (6237) on
// btree(pnnspid oid_ops, pnpubid oid_ops).
func TestPgPublicationNamespacePnnspidPnpubidIndexInitialEntry(t *testing.T) {
	const (
		idxOID = 6239
		relOID = 6237
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
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_publication_namespace_pnnspid_pnpubid_index) — step 3bx regression", idxOID)
	}
	if got.IndRelid != relOID {
		t.Errorf("IndRelid=%d want %d (pg_publication_namespace)", got.IndRelid, relOID)
	}
	wantKey := []int16{3, 2} // pnnspid (attnum 3), pnpubid (attnum 2)
	if len(got.IndKey) != 2 || got.IndKey[0] != wantKey[0] || got.IndKey[1] != wantKey[1] {
		t.Errorf("IndKey=%v want %v (pnnspid, pnpubid)", got.IndKey, wantKey)
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
