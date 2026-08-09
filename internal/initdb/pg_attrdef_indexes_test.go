package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M-NIGHTLY AI-20260810-011258-003 regression gate.
//
// TestE2E_PGStandbyFullCycle's Phase B ran `ALTER TABLE ... ADD COLUMN extra
// int DEFAULT 0` on a goopg primary and then failed EVERY subsequent query
// against that relation on the PG 18.3 standby with
//
//	could not open relation with OID 2656
//
// PG's AttrDefaultFetch (postgres/src/backend/utils/cache/relcache.c) reads
// column defaults through AttrDefaultIndexId = 2656 only — there is no
// seq-scan fallback — so the pg_attrdef catalog surface has to carry BOTH
// declared indexes (pg_attrdef.h:53-54) and a tupledesc that actually exposes
// `adbin`. pg_attrdef is not formrdesc'd, so PG rebuilds its TupleDesc from
// the streamed pg_attribute rows for relid 2604; goopg declared only three
// attributes there while its heap writer had always written four.
//
// These assertions are cheap and static-plus-on-disk; they fire long before
// the (expensive, PG-binary-dependent) standby E2E does.
func TestPgAttrdefCatalogSurfaceIsPGComplete(t *testing.T) {
	byOID := map[uint32]*nailedRel{}
	for i := range nailedLocalRels {
		byOID[nailedLocalRels[i].OID] = &nailedLocalRels[i]
	}

	// (1) The heap's tupledesc must be the full PG 18.3 FormData_pg_attrdef.
	heap, ok := byOID[2604]
	if !ok {
		t.Fatal("nailedLocalRels: pg_attrdef (2604) missing")
	}
	if heap.RelNatts != 4 {
		t.Errorf("pg_attrdef relnatts=%d, want 4 (oid, adrelid, adnum, adbin)", heap.RelNatts)
	}
	wantAttrs := []struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
	}{
		{"oid", 26, 1, 4},
		{"adrelid", 26, 2, 4},
		{"adnum", 21, 3, 2},
		// pg_node_tree, CATALOG_VARLEN — the column AttrDefaultFetch reads.
		{"adbin", 194, 4, -1},
	}
	if len(heap.Attrs) != len(wantAttrs) {
		t.Fatalf("pg_attrdef: %d attrs, want %d", len(heap.Attrs), len(wantAttrs))
	}
	for i, w := range wantAttrs {
		a := heap.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len {
			t.Errorf("pg_attrdef attr %d = {%s typoid=%d num=%d len=%d}, want {%s %d %d %d}",
				i+1, a.Name, a.TypeOID, a.Num, a.Len, w.Name, w.TypeOID, w.Num, w.Len)
		}
	}

	// (2) Both declared indexes must be nailed with the right key shape.
	//     2656 btree(adrelid oid_ops, adnum int2_ops)  UNIQUE
	//     2657 btree(oid oid_ops)                      UNIQUE PRIMARY
	for _, c := range []struct {
		OID  uint32
		Name string
		Keys []string
	}{
		{2656, "pg_attrdef_adrelid_adnum_index", []string{"adrelid", "adnum"}},
		{2657, "pg_attrdef_oid_index", []string{"oid"}},
	} {
		idx, ok := byOID[c.OID]
		if !ok {
			t.Errorf("nailedLocalRels: %s (%d) missing", c.Name, c.OID)
			continue
		}
		if idx.RelKind != 'i' {
			t.Errorf("%s: RelKind=%q, want 'i'", c.Name, idx.RelKind)
		}
		// relnatts MUST equal pg_index.indnatts or PG's
		// RelationInitIndexAccessInfo FATALs "relnatts disagrees with
		// indnatts for index <oid>".
		if int(idx.RelNatts) != len(c.Keys) {
			t.Errorf("%s: relnatts=%d, want %d", c.Name, idx.RelNatts, len(c.Keys))
		}
		if len(idx.Attrs) != len(c.Keys) {
			t.Errorf("%s: %d key attrs, want %d", c.Name, len(idx.Attrs), len(c.Keys))
			continue
		}
		for i, k := range c.Keys {
			if idx.Attrs[i].Name != k {
				t.Errorf("%s key %d = %q, want %q (flattenRels must derive it from the pg_attrdef heap attrs)",
					c.Name, i+1, idx.Attrs[i].Name, k)
			}
		}
	}

	// (3) pg_index seed rows: indkey is the vector systable_beginscan walks
	//     looking for the caller's sk_attno.
	seeds := map[uint32]pgIndexEntry{}
	for _, e := range pgIndexInitialEntries() {
		seeds[e.IndexRelid] = e
	}
	for _, c := range []struct {
		OID     uint32
		Relid   uint32
		IndKey  []int16
		Unique  bool
		Primary bool
	}{
		{2656, 2604, []int16{2, 3}, true, false},
		{2657, 2604, []int16{1}, true, true},
	} {
		e, ok := seeds[c.OID]
		if !ok {
			t.Errorf("pgIndexInitialEntries: %d missing", c.OID)
			continue
		}
		if e.IndRelid != c.Relid {
			t.Errorf("%d: indrelid=%d, want %d", c.OID, e.IndRelid, c.Relid)
		}
		if len(e.IndKey) != len(c.IndKey) {
			t.Errorf("%d: indkey=%v, want %v", c.OID, e.IndKey, c.IndKey)
			continue
		}
		for i := range c.IndKey {
			if e.IndKey[i] != c.IndKey[i] {
				t.Errorf("%d: indkey=%v, want %v", c.OID, e.IndKey, c.IndKey)
				break
			}
		}
		if e.IsUnique != c.Unique || e.IsPrimary != c.Primary {
			t.Errorf("%d: unique=%v primary=%v, want %v/%v", c.OID, e.IsUnique, e.IsPrimary, c.Unique, c.Primary)
		}
	}
}

// TestPgAttrdefIndexFilesBootstrapped pins the physical half: PG's
// RelationInitPhysicalAddr resolves index 2656/2657 to base/<db>/<oid>, and
// a missing file is the literal "could not open relation with OID 2656"
// FATAL. initdb must lay down a valid btree metapage in the default database
// (base/1) and in the seeded `postgres` database (base/5).
func TestPgAttrdefIndexFilesBootstrapped(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, db := range []string{"1", "5"} {
		for _, oid := range []string{"2656", "2657"} {
			path := filepath.Join(dir, "base", db, oid)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("read %s: %v", path, err)
				continue
			}
			if len(raw) < storage.BlockSize {
				t.Errorf("%s: len=%d, want at least one %d-byte block", path, len(raw), storage.BlockSize)
				continue
			}
			if _, err := storage.Header(storage.Page(raw[:storage.BlockSize])); err != nil {
				t.Errorf("%s: block 0 is not a valid page: %v", path, err)
			}
		}
	}
}
