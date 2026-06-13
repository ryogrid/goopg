package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPgDatabaseAttrsMatchesPG18FormPgDatabase pins the 18-column PG18
// pg_database schema to prevent silently drifting from
// postgres/src/include/catalog/pg_database.h. PG's
// RelationCacheInitializePhase2 hardcodes Desc_pg_database from that
// header into formrdesc and Phase3 decodes our heap row through it; any
// drift either rots GETSTRUCT or trips heaptuple.c:642 Assert(j > attnum)
// inside nocachegetattr. M0106-0010 Step 3ct.
func TestPgDatabaseAttrsMatchesPG18FormPgDatabase(t *testing.T) {
	attrs := pgDatabaseAttrs()
	if got, want := len(attrs), 18; got != want {
		t.Fatalf("pgDatabaseAttrs len = %d, want %d (PG18 Natts_pg_database)", got, want)
	}
	type expect struct {
		name    string
		typeOID uint32
		num     int16
		length  int16
	}
	want := []expect{
		{"oid", 26, 1, 4},
		{"datname", 19, 2, 64},
		{"datdba", 26, 3, 4},
		{"encoding", 23, 4, 4},
		{"datlocprovider", 18, 5, 1},
		{"datistemplate", 16, 6, 1},
		{"datallowconn", 16, 7, 1},
		{"dathasloginevt", 16, 8, 1},
		{"datconnlimit", 23, 9, 4},
		{"datfrozenxid", 28, 10, 4},
		{"datminmxid", 28, 11, 4},
		{"dattablespace", 26, 12, 4},
		{"datcollate", 25, 13, -1},
		{"datctype", 25, 14, -1},
		{"datlocale", 25, 15, -1},
		{"daticurules", 25, 16, -1},
		{"datcollversion", 25, 17, -1},
		{"datacl", 1034, 18, -1},
	}
	for i, w := range want {
		got := attrs[i]
		if got.Name != w.name {
			t.Errorf("attr %d Name = %q, want %q", i, got.Name, w.name)
		}
		if got.TypeOID != w.typeOID {
			t.Errorf("attr %d (%s) TypeOID = %d, want %d", i, w.name, got.TypeOID, w.typeOID)
		}
		if got.Num != w.num {
			t.Errorf("attr %d (%s) Num = %d, want %d", i, w.name, got.Num, w.num)
		}
		if got.Len != w.length {
			t.Errorf("attr %d (%s) Len = %d, want %d", i, w.name, got.Len, w.length)
		}
	}
}

// TestBootstrapPostgresDatabaseTupleHasVarWidthAndNullBitmap is the
// regression pin for the Assert("j > attnum") FATAL closed by Step 3ct.
// PG18 nocachegetattr enters its fast path (no slow walk) when
// HEAP_HASVARWIDTH is unset, then breaks the fixed-prefix loop at the
// first attlen<=0 attribute and asserts that j (the breaking attribute's
// 0-based index) is strictly greater than the target attnum. CheckMyDatabase
// reads datcollversion (attnum 17) via SysCacheGetAttr, which is exactly
// the path that tripped before this fix. We assert HEAP_HASNULL +
// HEAP_HASVARWIDTH on every pg_database row written under global/1262 and
// that t_natts == 18 (the PG18 column count).
func TestBootstrapPostgresDatabaseTupleHasVarWidthAndNullBitmap(t *testing.T) {
	dir := t.TempDir()
	// bootstrapPostgresDatabase writes more than just global/1262 — it
	// also seeds the per-database directories (base/1, base/4, base/5).
	for _, sub := range []string{"global", "base/1", "base/4", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if err := bootstrapPostgresDatabase(dir, pgEncUTF8); err != nil {
		t.Fatalf("bootstrapPostgresDatabase: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "global", "1262"))
	if err != nil {
		t.Fatalf("read global/1262: %v", err)
	}
	if len(raw) < int(storage.BlockSize) {
		t.Fatalf("global/1262 length = %d, want >= %d", len(raw), storage.BlockSize)
	}
	page := raw[:storage.BlockSize]

	// PageHeaderData: pd_lower at offset 12, pd_upper at offset 14.
	pdLower := binary.LittleEndian.Uint16(page[12:14])
	// Line pointer array starts at offset 24, 4 bytes each.
	nItems := (int(pdLower) - 24) / 4
	// Three pg_database rows: template1 (OID 1), postgres (OID 5), template0 (OID 4).
	if nItems != 3 {
		t.Fatalf("expected 3 line pointers (template1 + postgres + template0), got %d", nItems)
	}
	const (
		heapHasNull     uint16 = 0x0001
		heapHasVarWidth uint16 = 0x0002
	)
	for i := 0; i < nItems; i++ {
		lp := binary.LittleEndian.Uint32(page[24+i*4 : 28+i*4])
		off := int(lp & 0x7FFF)
		length := int((lp >> 17) & 0x7FFF)
		if off == 0 || length == 0 {
			t.Fatalf("line pointer %d empty (off=%d len=%d)", i, off, length)
		}
		// PG18 HeapTupleHeader layout (postgres/src/include/access/htup_details.h):
		//   t_choice  (12 bytes — xmin/xmax/cmin|cmax|xvac)
		//   t_ctid    (6 bytes)
		//   t_infomask2 (uint16) at offset 18 — natts + index flags
		//   t_infomask  (uint16) at offset 20 — HEAP_HAS* + xact flags
		//   t_hoff      (uint8)  at offset 22
		infomask2 := binary.LittleEndian.Uint16(page[off+18 : off+20])
		infomask := binary.LittleEndian.Uint16(page[off+20 : off+22])
		if infomask&heapHasVarWidth == 0 {
			t.Errorf("tuple %d: HEAP_HASVARWIDTH not set (infomask=0x%04x); PG18 nocachegetattr will trip Assert(j > attnum) when reading datcollversion", i, infomask)
		}
		if infomask&heapHasNull == 0 {
			t.Errorf("tuple %d: HEAP_HASNULL not set (infomask=0x%04x); 4 nullable text/aclitem cols (datlocale/daticurules/datcollversion/datacl) require a null bitmap", i, infomask)
		}
		natts := infomask2 & 0x07FF
		if natts != 18 {
			t.Errorf("tuple %d: t_natts = %d, want 18 (PG18 Natts_pg_database)", i, natts)
		}
	}
}
