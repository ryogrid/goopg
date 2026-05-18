package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedPgDatabaseDatnameIndexHasNameDescriptor (M0106-0010 Step 3dh)
// pins the relcache nailedRel descriptor for pg_database_datname_index (OID
// 2671). Same shape as pg_authid_rolname_index (Step 3dg): a 1-column UNIQUE
// btree on NameData (name_ops). The default indexKeyAttrs() stamp of
// oid-typed (attbyval=1, attlen=4) would make PG's
// _bt_compare→index_getattr load the first 4 inline NameData bytes as a
// by-val Datum and pass them to btnamecmp as a pointer — a latent SEGV that
// reproduces verbatim the failure mode fixed for 2676.
//
// The fix supplies an explicit attr with TypeOID=19 (name), Len=64, which
// makes buildPgAttributeBlob emit attbyval=0, attlen=64, attalign='c' —
// the contract btnamecmp expects.
func TestNailedPgDatabaseDatnameIndexHasNameDescriptor(t *testing.T) {
	var rel *nailedRel
	for i := range nailedSharedRels {
		if nailedSharedRels[i].OID == 2671 {
			rel = &nailedSharedRels[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("nailedSharedRels: OID 2671 (pg_database_datname_index) missing")
	}
	if rel.RelKind != 'i' {
		t.Errorf("RelKind=%q, want 'i'", rel.RelKind)
	}
	if rel.RelNatts != 1 {
		t.Errorf("RelNatts=%d, want 1", rel.RelNatts)
	}
	if len(rel.Attrs) != 1 {
		t.Fatalf("Attrs len=%d, want 1", len(rel.Attrs))
	}
	a := rel.Attrs[0]
	if a.TypeOID != 19 {
		t.Errorf("Attrs[0].TypeOID=%d, want 19 (name)", a.TypeOID)
	}
	if a.Len != 64 {
		t.Errorf("Attrs[0].Len=%d, want 64 (NAMEDATALEN)", a.Len)
	}
	if a.Name != "datname" {
		t.Errorf("Attrs[0].Name=%q, want \"datname\"", a.Name)
	}
	blob := buildPgAttributeBlob(a)
	if blob[82] != 0 {
		t.Errorf("buildPgAttributeBlob.attbyval=%d, want 0", blob[82])
	}
	if blob[83] != 'c' {
		t.Errorf("buildPgAttributeBlob.attalign=%q, want 'c'", blob[83])
	}
	if got := uint16(blob[72]) | uint16(blob[73])<<8; got != 64 {
		t.Errorf("buildPgAttributeBlob.attlen=%d, want 64", got)
	}
}

// TestBootstrapPgDatabaseDatnameIndexWritesPopulatedBtree verifies that the
// bootstrap helper lands a 2-page btree at global/2671 carrying name-keyed
// IndexTuples for both canonical pg_database rows ("template1" at tid=1 and
// "postgres" at tid=2). Without this, InitPostgres → get_db_info() FATALs
// with `3D000: database "postgres" does not exist` on the first connect.
func TestBootstrapPgDatabaseDatnameIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	if err := bootstrapPgDatabaseDatnameIndex(dir); err != nil {
		t.Fatalf("bootstrapPgDatabaseDatnameIndex: %v", err)
	}
	path := filepath.Join(dir, "global", "2671")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(raw) != 2*storage.BlockSize {
		t.Fatalf("file size=%d, want %d", len(raw), 2*storage.BlockSize)
	}
	le := binary.LittleEndian
	btmRoot := le.Uint32(raw[24+4+4 : 24+4+4+4])
	if btmRoot != 1 {
		t.Errorf("btm_root=%d, want 1", btmRoot)
	}
	leaf := raw[storage.BlockSize : 2*storage.BlockSize]
	pdLower := le.Uint16(leaf[12:14])
	nItems := int((pdLower - storage.SizeOfPageHeaderData) / 4)
	if nItems != 2 {
		t.Errorf("nItems=%d, want 2", nItems)
	}
	if !leafContainsName(raw, "template1") {
		t.Error("leaf missing tuple keyed on \"template1\"")
	}
	if !leafContainsName(raw, "postgres") {
		t.Error("leaf missing tuple keyed on \"postgres\"")
	}
}

// TestBootstrapPgDatabaseDatnameIndexLeafKeysAscending pins the on-disk
// ordering of leaf tuples: the btree invariant requires ascending keys, and
// btnamecmp does an unsigned byte comparison on the zero-padded NameData
// blob. For the two seeded datnames the lexicographic order is:
//
//	"postgres"  < "template1"
//
// because 'p' (0x70) < 't' (0x74) at byte 0. A regression that sorts in
// insertion order ("template1", "postgres") would break _bt_search and
// surface as a missed lookup even though both tuples are physically present.
func TestBootstrapPgDatabaseDatnameIndexLeafKeysAscending(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	if err := bootstrapPgDatabaseDatnameIndex(dir); err != nil {
		t.Fatalf("bootstrapPgDatabaseDatnameIndex: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "global", "2671"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	leaf := raw[storage.BlockSize : 2*storage.BlockSize]
	le := binary.LittleEndian
	pdLower := le.Uint16(leaf[12:14])
	nItems := int((pdLower - storage.SizeOfPageHeaderData) / 4)
	if nItems != 2 {
		t.Fatalf("nItems=%d, want 2", nItems)
	}
	readName := func(idx int) string {
		lp := le.Uint32(leaf[storage.SizeOfPageHeaderData+idx*4:])
		off := int(lp & 0x7FFF)
		nd := leaf[off+8 : off+72]
		n := 0
		for n < len(nd) && nd[n] != 0 {
			n++
		}
		return string(nd[:n])
	}
	got0, got1 := readName(0), readName(1)
	if got0 != "postgres" {
		t.Errorf("leaf[0]=%q, want \"postgres\"", got0)
	}
	if got1 != "template1" {
		t.Errorf("leaf[1]=%q, want \"template1\"", got1)
	}
}
