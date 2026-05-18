package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgForeignServer pins the M0106-0010 step 3be
// catalog seed for pg_foreign_server (OID 1417). PG's standby boot opens
// `RelationBuildDesc(1417)`; without this entry it FATALs with
// `could not open relation with OID 1417`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_foreign_server.h
//   - postgres/src/include/catalog/pg_foreign_server_d.h
//     (ForeignServerRelationId = 1417, Natts_pg_foreign_server = 8).
func TestNailedLocalRelsContainsPgForeignServer(t *testing.T) {
	const fsOID = 1417

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == fsOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_foreign_server) — step 3be regression", fsOID)
	}
	if got.RelName != "pg_foreign_server" {
		t.Fatalf("OID %d: RelName=%q want %q", fsOID, got.RelName, "pg_foreign_server")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", fsOID, got.RelKind)
	}
	if got.RelNatts != 8 {
		t.Fatalf("OID %d: RelNatts=%d want 8 (PG18 Natts_pg_foreign_server)", fsOID, got.RelNatts)
	}
	if len(got.Attrs) != 8 {
		t.Fatalf("OID %d: len(Attrs)=%d want 8", fsOID, len(got.Attrs))
	}

	// Per-attribute pins against PG18 pg_foreign_server.h /
	// pg_foreign_server_d.h. srvtype, srvversion, srvacl, srvoptions
	// are in the CATALOG_VARLEN block with no BKI_FORCE_NOT_NULL —
	// nullable.
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"oid", 26, 1, 4, true},
		{"srvname", 19, 2, 64, true},
		{"srvowner", 26, 3, 4, true},
		{"srvfdw", 26, 4, 4, true},
		{"srvtype", 25, 5, -1, false},
		{"srvversion", 25, 6, -1, false},
		{"srvacl", 1034, 7, -1, false},
		{"srvoptions", 1009, 8, -1, false},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignServer pins that the
// step 3be seed wires pg_foreign_server (OID 1417) into the empty-heap
// placeholder list so PG's mdopen finds a valid 8-KiB heap file at
// base/{1,5}/1417 once the pg_class row resolves the relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignServer(t *testing.T) {
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
		path := filepath.Join(dir, db, "1417")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3be regression)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}
