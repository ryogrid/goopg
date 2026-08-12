package initdb

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0131-S20.1 — DECLARE_TOAST(pg_rewrite, 2838, 2839).
//
// Every value asserted below was probed on a freshly-initdb'd PostgreSQL 18.3
// oracle rather than inferred from the headers; the probe is recorded in
// docs/design/0131-0035-pg-rewrite-toast.md §"Oracle measurement". A hosted PG
// reaches the TOAST relation by OID (pg_class.reltoastrelid → table_open), so
// every one of these fields is load-bearing, not cosmetic.

// TestPgRewriteToastPairPgClassRows asserts the pg_class heap on a fresh data
// dir carries both halves of the pair with upstream's field values, and that
// pg_rewrite itself now points at the TOAST heap. Without reltoastrelid a
// hosted PG detoasting an external ev_action pointer raises "missing chunk
// number 0 for toast value"; with a wrong relnamespace the pair is unreachable
// through pg_class_relname_nsp_index.
func TestPgRewriteToastPairPgClassRows(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

	rows := readPgClassHeapRows(t, filepath.Join(dir, "base", "5", "1259"))

	tests := []struct {
		oid           uint32
		relName       string
		relKind       string
		relNamespace  uint32
		relAm         uint32
		relNatts      int16
		relHasIndex   bool
		relToastRelID uint32
	}{
		{oid: 2838, relName: "pg_toast_2618", relKind: "t", relNamespace: 99, relAm: 2, relNatts: 3, relHasIndex: true},
		{oid: 2839, relName: "pg_toast_2618_index", relKind: "i", relNamespace: 99, relAm: 403, relNatts: 2},
		{oid: 2618, relName: "pg_rewrite", relKind: "r", relNamespace: 11, relAm: 2, relNatts: 8, relToastRelID: 2838},
	}
	for _, tc := range tests {
		got, ok := rows[tc.oid]
		if !ok {
			t.Fatalf("pg_class heap has no row for OID %d (%s)", tc.oid, tc.relName)
		}
		if got.RelName != tc.relName {
			t.Errorf("OID %d relname = %q, want %q", tc.oid, got.RelName, tc.relName)
		}
		if got.RelKind != tc.relKind {
			t.Errorf("OID %d relkind = %q, want %q", tc.oid, got.RelKind, tc.relKind)
		}
		if got.RelNamespace != tc.relNamespace {
			t.Errorf("OID %d relnamespace = %d, want %d", tc.oid, got.RelNamespace, tc.relNamespace)
		}
		if got.RelAM != tc.relAm {
			t.Errorf("OID %d relam = %d, want %d", tc.oid, got.RelAM, tc.relAm)
		}
		if got.RelNatts != tc.relNatts {
			t.Errorf("OID %d relnatts = %d, want %d", tc.oid, got.RelNatts, tc.relNatts)
		}
		if got.RelHasIndex != tc.relHasIndex {
			t.Errorf("OID %d relhasindex = %v, want %v", tc.oid, got.RelHasIndex, tc.relHasIndex)
		}
		if got.RelToastRelID != tc.relToastRelID {
			t.Errorf("OID %d reltoastrelid = %d, want %d", tc.oid, got.RelToastRelID, tc.relToastRelID)
		}
	}

	// reltype MUST stay 0 on both halves: upstream writes no pg_type row for a
	// TOAST relation, and goopg's bootstrapPgTypeTuples only walks
	// nailedLocalRels/nailedSharedRels, so a defaulted reltype=OID would name a
	// pg_type row that does not exist and trip PG's tdtypeid assertion.
	for _, oid := range []uint32{2838, 2839} {
		if got := rows[oid].RelType; got != 0 {
			t.Errorf("OID %d reltype = %d, want 0 (TOAST relations have no rowtype)", oid, got)
		}
	}
}

// TestPgRewriteToastPairAttributes asserts the three chunk columns (and the
// index's two key columns) reach pg_attribute with upstream's types and the
// 'p' attstorage a TOAST relation's own columns carry — 'x' (the bytea
// default) would tell a hosted PG that a chunk may itself be toasted.
func TestPgRewriteToastPairAttributes(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

	attrs := readPgAttributeHeapRows(t, filepath.Join(dir, "base", "5", "1249"))

	want := []struct {
		relOID  uint32
		attNum  int16
		name    string
		typeOID uint32
		length  int16
	}{
		{2838, 1, "chunk_id", 26, 4},
		{2838, 2, "chunk_seq", 23, 4},
		{2838, 3, "chunk_data", 17, -1},
		{2839, 1, "chunk_id", 26, 4},
		{2839, 2, "chunk_seq", 23, 4},
	}
	for _, w := range want {
		got, ok := attrs[[2]int64{int64(w.relOID), int64(w.attNum)}]
		if !ok {
			t.Fatalf("pg_attribute has no row for (%d, %d)", w.relOID, w.attNum)
		}
		if got.AttName != w.name {
			t.Errorf("(%d,%d) attname = %q, want %q", w.relOID, w.attNum, got.AttName, w.name)
		}
		if got.AttTypID != w.typeOID {
			t.Errorf("(%d,%d) atttypid = %d, want %d", w.relOID, w.attNum, got.AttTypID, w.typeOID)
		}
		if got.AttLen != w.length {
			t.Errorf("(%d,%d) attlen = %d, want %d", w.relOID, w.attNum, got.AttLen, w.length)
		}
		if got.AttStorage != "p" {
			t.Errorf("(%d,%d) attstorage = %q, want \"p\"", w.relOID, w.attNum, got.AttStorage)
		}
	}
}

// TestPgRewriteToastPairIndexRowAndFiles asserts the pg_index row for
// pg_toast_2618_index matches the oracle (indkey "1 2", oid_ops + int4_ops,
// UNIQUE PRIMARY) and that both physical files exist with valid page headers
// in each per-database directory. pg_class.reltoastrelid is resolved by
// RelationInitPhysicalAddr on relcache load, so the file must exist from the
// moment the pg_class row does — even while the heap holds no chunks.
//
// M0131-S20.2b: the heap no longer holds no chunks. S20.1 wrote exactly one
// empty page per file and this guard pinned that; the four out-of-line
// ev_action captures now fill six pages of 2838 (22 chunk tuples at ~2032 B,
// four to a page) and two of 2839. The assertion is therefore "a whole number
// of valid pages, at least one" plus an explicit page-count expectation —
// checking every page's header, not just block 0, is what the size check was
// standing in for.
func TestPgRewriteToastPairIndexRowAndFiles(t *testing.T) {
	var idx *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == 2839 {
			idx = &pgIndexInitialEntries()[i]
			break
		}
	}
	if idx == nil {
		t.Fatal("pgIndexInitialEntries has no row for pg_toast_2618_index (2839)")
	}
	if idx.IndRelid != 2838 {
		t.Errorf("indrelid = %d, want 2838", idx.IndRelid)
	}
	if got := idx.IndKey; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("indkey = %v, want [1 2]", got)
	}
	if got := idx.IndClass; len(got) != 2 || got[0] != 1981 || got[1] != 1978 {
		t.Errorf("indclass = %v, want [1981 1978] (oid_ops, int4_ops)", got)
	}
	if !idx.IsUnique || !idx.IsPrimary {
		t.Errorf("indisunique/indisprimary = %v/%v, want true/true", idx.IsUnique, idx.IsPrimary)
	}

	dir := t.TempDir()
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	// Both per-database copies hold the same seeded corpus, so the page counts
	// are identical: 2838 carries the 46 chunk tuples of the six out-of-line
	// ev_action captures, 2839 the btree over them (metapage + root).
	// M0131-S9.3f's pg_seclabels contributes 18 of those 46 chunks on its own,
	// which is what took 2838 from 6 pages to 10; M0131-S9.3g's
	// pg_stats_ext_exprs adds the last 6 and takes it to 12.
	wantPages := map[string]int{"2838": 12, "2839": 2}
	for _, db := range []string{"1", "5"} {
		for _, oid := range []string{"2838", "2839"} {
			path := filepath.Join(dir, "base", db, oid)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("base/%s/%s: %v", db, oid, err)
			}
			if len(data) == 0 || len(data)%storage.BlockSize != 0 {
				t.Errorf("base/%s/%s: size %d, want a positive multiple of %d",
					db, oid, len(data), storage.BlockSize)
				continue
			}
			if got := len(data) / storage.BlockSize; got != wantPages[oid] {
				t.Errorf("base/%s/%s: %d pages, want %d — the seeded chunk set "+
					"changed (M0131-S20.2b captured four out-of-line ev_actions, S9.3f a fifth)",
					db, oid, got, wantPages[oid])
			}
			for blk := 0; blk < len(data); blk += storage.BlockSize {
				if _, err := storage.Header(storage.Page(data[blk : blk+storage.BlockSize])); err != nil {
					t.Errorf("base/%s/%s: block %d has an invalid page header: %v",
						db, oid, blk/storage.BlockSize, err)
				}
			}
		}
	}
}

// pgClassFixedRow is the fixed-offset prefix of a PG18 FormData_pg_class as a
// hosted PG casts it. The offsets are the ones pgClassColDefs documents, read
// back from the bytes actually written so the assertions cover the LAYOUT and
// not only the Go-side values.
type pgClassFixedRow struct {
	OID           uint32
	RelName       string
	RelNamespace  uint32
	RelType       uint32
	RelAM         uint32
	RelFileNode   uint32
	RelToastRelID uint32
	RelHasIndex   bool
	RelKind       string
	RelNatts      int16
}

func decodePgClassFixedRow(p []byte) (pgClassFixedRow, bool) {
	if len(p) < 122 {
		return pgClassFixedRow{}, false
	}
	name := p[4:68]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	return pgClassFixedRow{
		OID:           binary.LittleEndian.Uint32(p[0:4]),
		RelName:       string(name),
		RelNamespace:  binary.LittleEndian.Uint32(p[68:72]),
		RelType:       binary.LittleEndian.Uint32(p[72:76]),
		RelAM:         binary.LittleEndian.Uint32(p[84:88]),
		RelFileNode:   binary.LittleEndian.Uint32(p[88:92]),
		RelToastRelID: binary.LittleEndian.Uint32(p[112:116]),
		RelHasIndex:   p[116] != 0,
		RelKind:       string(p[119:120]),
		RelNatts:      int16(binary.LittleEndian.Uint16(p[120:122])),
	}, true
}

// pgAttributeFixedRow is the same idea for FormData_pg_attribute; offsets
// follow pgAttrColDefs (attrelid 0, attname 4, atttypid 68, attlen 72,
// attnum 74, atttypmod 76, attndims 80, attbyval 82, attalign 83,
// attstorage 84).
type pgAttributeFixedRow struct {
	AttRelID   uint32
	AttName    string
	AttTypID   uint32
	AttLen     int16
	AttNum     int16
	AttStorage string
}

func decodePgAttributeFixedRow(p []byte) (pgAttributeFixedRow, bool) {
	if len(p) < 85 {
		return pgAttributeFixedRow{}, false
	}
	name := p[4:68]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	return pgAttributeFixedRow{
		AttRelID:   binary.LittleEndian.Uint32(p[0:4]),
		AttName:    string(name),
		AttTypID:   binary.LittleEndian.Uint32(p[68:72]),
		AttLen:     int16(binary.LittleEndian.Uint16(p[72:74])),
		AttNum:     int16(binary.LittleEndian.Uint16(p[74:76])),
		AttStorage: string(p[84:85]),
	}, true
}

// readPgClassHeapRows decodes every live pg_class tuple in a bootstrap heap
// file, keyed by OID.
func readPgClassHeapRows(t *testing.T, path string) map[uint32]pgClassFixedRow {
	t.Helper()
	out := map[uint32]pgClassFixedRow{}
	forEachHeapTuple(t, path, func(raw []byte) {
		if row, ok := decodePgClassFixedRow(raw[int(raw[22]):]); ok {
			out[row.OID] = row
		}
	})
	return out
}

// readPgAttributeHeapRows decodes every live pg_attribute tuple, keyed by
// (attrelid, attnum).
func readPgAttributeHeapRows(t *testing.T, path string) map[[2]int64]pgAttributeFixedRow {
	t.Helper()
	out := map[[2]int64]pgAttributeFixedRow{}
	forEachHeapTuple(t, path, func(raw []byte) {
		if row, ok := decodePgAttributeFixedRow(raw[int(raw[22]):]); ok {
			out[[2]int64{int64(row.AttRelID), int64(row.AttNum)}] = row
		}
	})
	return out
}

// forEachHeapTuple walks every live tuple of a bootstrap heap file, handing the
// raw tuple bytes (header included; t_hoff is byte 22) to fn.
func forEachHeapTuple(t *testing.T, path string, fn func(raw []byte)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for pi := 0; pi < len(data)/storage.BlockSize; pi++ {
		page := storage.Page(data[pi*storage.BlockSize : (pi+1)*storage.BlockSize])
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			t.Fatalf("%s page %d: %v", path, pi, err)
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			itemID, err := storage.PageGetItemID(page, slot)
			if err != nil || itemID.Flags != storage.ItemIDNormal {
				continue
			}
			raw, err := storage.PageGetItemRaw(page, slot)
			if err != nil || len(raw) < 24 {
				continue
			}
			fn(raw)
		}
	}
}
