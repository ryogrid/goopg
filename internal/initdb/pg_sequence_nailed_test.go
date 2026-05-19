package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgSequence pins the M0106-0010 step 3cb
// catalog seed for pg_sequence (OID 2224). PG's standby boot opens
// `RelationBuildDesc(2224)` once Step 3ca's pg_replication_origin
// family cleared the previous FATAL; without this entry it FATALs
// with `could not open relation with OID 2224`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_sequence.h
//     (SequenceRelationId = 2224, 8 columns, no oid system column).
func TestNailedLocalRelsContainsPgSequence(t *testing.T) {
	const sequenceOID = 2224

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == sequenceOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_sequence) — step 3cb regression", sequenceOID)
	}
	if got.RelName != "pg_sequence" {
		t.Fatalf("OID %d: RelName=%q want %q", sequenceOID, got.RelName, "pg_sequence")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", sequenceOID, got.RelKind)
	}
	if got.RelNatts != 8 {
		t.Fatalf("OID %d: RelNatts=%d want 8 (PG18 Natts_pg_sequence)", sequenceOID, got.RelNatts)
	}
	if len(got.Attrs) != 8 {
		t.Fatalf("OID %d: len(Attrs)=%d want 8", sequenceOID, len(got.Attrs))
	}

	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"seqrelid", 26, 1, 4, true},
		{"seqtypid", 26, 2, 4, true},
		{"seqstart", 20, 3, 8, true},
		{"seqincrement", 20, 4, 8, true},
		{"seqmax", 20, 5, 8, true},
		{"seqmin", 20, 6, 8, true},
		{"seqcache", 20, 7, 8, true},
		{"seqcycle", 16, 8, 1, true},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgSequence pins that the
// step 3cb seed wires pg_sequence (OID 2224) into the empty-heap
// placeholder list so PG's mdopen finds a valid 8-KiB heap file at
// base/{1,5}/2224 once the pg_class row resolves the relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgSequence(t *testing.T) {
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
		path := filepath.Join(dir, db, "2224")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3cb regression)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
	}
}

// TestPgSequenceSeqrelidIndexInitialEntry pins the pgIndexInitialEntries
// row for the UNIQUE PRIMARY index (5002) over pg_sequence (2224) on
// btree(seqrelid oid_ops). PG's RelationInitIndexAccessInfo requires
// indkey/indclass/indcollation/indisunique/indisprimary to agree with
// the upstream pg_sequence.h declaration.
func TestPgSequenceSeqrelidIndexInitialEntry(t *testing.T) {
	const (
		idxOID = 5002
		relOID = 2224
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
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_sequence_seqrelid_index) — step 3cb regression", idxOID)
	}
	if got.IndRelid != relOID {
		t.Errorf("IndRelid=%d want %d (pg_sequence)", got.IndRelid, relOID)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 1 {
		t.Errorf("IndKey=%v want [1] (seqrelid attnum)", got.IndKey)
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

// TestNailedLocalRelsContainsPgSequenceSeqrelidIndex pins that the
// step 3cb idxSpec list includes pg_sequence_seqrelid_index (5002) so
// flattenRels emits a nailed-index entry with RelNatts derived via
// pgIndexNattsByOID; without this PG's relcache_init.c
// RelationCacheInitializePhase3 FATALs on index 5002.
func TestNailedLocalRelsContainsPgSequenceSeqrelidIndex(t *testing.T) {
	const idxOID = 5002
	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == idxOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_sequence_seqrelid_index) — step 3cb regression", idxOID)
	}
	if got.RelName != "pg_sequence_seqrelid_index" {
		t.Errorf("OID %d: RelName=%q want pg_sequence_seqrelid_index", idxOID, got.RelName)
	}
	if got.RelKind != 'i' {
		t.Errorf("OID %d: RelKind=%q want 'i'", idxOID, got.RelKind)
	}
	if got.RelNatts != 1 {
		t.Errorf("OID %d: RelNatts=%d want 1 (single-key oid_ops)", idxOID, got.RelNatts)
	}
}
