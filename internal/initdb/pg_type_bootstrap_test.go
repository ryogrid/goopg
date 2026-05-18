package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/storage"
)

// TestPgTypeColDefsLayoutMatchesPG18 pins that the catalog.Column slice
// returned by pgTypeColDefs() lays out tuple bytes such that typalign
// lands at offset 128 — the exact byte PG18's `Form_pg_type *` cast
// reads via `typeForm->typalign` in TupleDescInitEntry (tupdesc.c:902).
// Without this invariant, populate_compact_attribute_internal FATALs at
// tupdesc.c:105 with "invalid attalign value:" on every PG-standby
// backend startup.
func TestPgTypeColDefsLayoutMatchesPG18(t *testing.T) {
	cols := pgTypeColDefs()
	if len(cols) != 32 {
		t.Fatalf("pgTypeColDefs: want 32 columns, got %d", len(cols))
	}
	// Probe with a canonical entry (oid=23 int4) whose typalign='i'.
	entry, ok := pgTypeCanonical(23)
	if !ok {
		t.Fatal("pgTypeCanonical(23): missing")
	}
	payload, err := executor.EncodeRowPG(cols, pgTypeRow(entry))
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	if len(payload) < 148 {
		t.Fatalf("pg_type fixed part: want >=148 bytes, got %d", len(payload))
	}
	if got := payload[128]; got != 'i' {
		t.Errorf("typalign at offset 128: want 'i', got %#x (%q)", got, got)
	}
	if got := payload[129]; got != 'p' {
		t.Errorf("typstorage at offset 129: want 'p', got %#x (%q)", got, got)
	}
}

// TestPgTypeInitialEntriesCoverNailedAttrTypeOIDs guards that every
// TypeOID referenced by any nailedRel attribute has a corresponding
// pgTypeCanonical entry. A future column addition that references a
// new pg_type OID will fail this test loudly rather than silently
// regressing the PG-standby boot FATAL at tupdesc.c:105.
func TestPgTypeInitialEntriesCoverNailedAttrTypeOIDs(t *testing.T) {
	for _, oid := range pgTypeOIDsUsedByNailedAttrs() {
		if _, ok := pgTypeCanonical(oid); !ok {
			t.Errorf("pgTypeCanonical(%d): missing — add a case to pgTypeCanonical with PG18-canonical typname/typlen/typbyval/typalign/typstorage", oid)
		}
	}
}

// TestPgTypeRowCanonicalTypalignByte walks every entry from
// pgTypeInitialEntries(), encodes it, and asserts that the byte at
// offset 128 matches pgTypeCanonical's authoritative typalign value.
// This is the heap-level companion to PG18's
// `populate_compact_attribute_internal` invariant.
func TestPgTypeRowCanonicalTypalignByte(t *testing.T) {
	cols := pgTypeColDefs()
	for _, e := range pgTypeInitialEntries() {
		row := pgTypeRow(e)
		payload, err := executor.EncodeRowPG(cols, row)
		if err != nil {
			t.Fatalf("oid=%d (%s): encode: %v", e.OID, e.Name, err)
		}
		if len(payload) < 148 {
			t.Errorf("oid=%d (%s): fixed part %d bytes < 148", e.OID, e.Name, len(payload))
			continue
		}
		if got := payload[128]; got != e.Align {
			t.Errorf("oid=%d (%s): typalign at offset 128: want %#x (%q), got %#x (%q)",
				e.OID, e.Name, e.Align, e.Align, got, got)
		}
		if got := payload[129]; got != e.Storage {
			t.Errorf("oid=%d (%s): typstorage at offset 129: want %#x (%q), got %#x (%q)",
				e.OID, e.Name, e.Storage, e.Storage, got, got)
		}
	}
}

// TestPgTypeRowEmbedsCanonicalIORegprocOIDs pins the I/O regproc OIDs
// emitted in every bootstrapped pg_type row. The int4 case in
// particular is load-bearing: an int4 row with typoutput=0 makes PG18's
// getTypeOutputInfo (lsyscache.c:3063) raise
// `ERROR: 42883: no output function available for type integer` on the
// standby's very first `SELECT 1` probe — the M0106-0010 Step 3da
// FATAL chain. typoutput lives at FormData_pg_type offset 104.
func TestPgTypeRowEmbedsCanonicalIORegprocOIDs(t *testing.T) {
	cols := pgTypeColDefs()
	cases := []struct {
		oid                                      uint32
		wantIn, wantOut, wantRecv, wantSend      uint32
	}{
		{23, 42, 43, 2406, 2407},   // int4
		{16, 1242, 1243, 2436, 2437}, // bool
		{25, 46, 47, 2414, 2415},   // text
		{26, 1798, 1799, 2418, 2419}, // oid
		{19, 34, 35, 2422, 2423},   // name
	}
	for _, tc := range cases {
		e, ok := pgTypeCanonical(tc.oid)
		if !ok {
			t.Errorf("pgTypeCanonical(%d): missing", tc.oid)
			continue
		}
		if e.Input != tc.wantIn || e.Output != tc.wantOut || e.Receive != tc.wantRecv || e.Send != tc.wantSend {
			t.Errorf("oid=%d (%s): I/O OIDs = (%d,%d,%d,%d); want (%d,%d,%d,%d)",
				tc.oid, e.Name, e.Input, e.Output, e.Receive, e.Send,
				tc.wantIn, tc.wantOut, tc.wantRecv, tc.wantSend)
		}
		payload, err := executor.EncodeRowPG(cols, pgTypeRow(e))
		if err != nil {
			t.Fatalf("oid=%d: EncodeRowPG: %v", tc.oid, err)
		}
		// FormData_pg_type fixed-part layout: typinput at offset 100,
		// typoutput at 104, typreceive at 108, typsend at 112.
		gotIn := uint32(payload[100]) | uint32(payload[101])<<8 | uint32(payload[102])<<16 | uint32(payload[103])<<24
		gotOut := uint32(payload[104]) | uint32(payload[105])<<8 | uint32(payload[106])<<16 | uint32(payload[107])<<24
		gotRecv := uint32(payload[108]) | uint32(payload[109])<<8 | uint32(payload[110])<<16 | uint32(payload[111])<<24
		gotSend := uint32(payload[112]) | uint32(payload[113])<<8 | uint32(payload[114])<<16 | uint32(payload[115])<<24
		if gotIn != tc.wantIn || gotOut != tc.wantOut || gotRecv != tc.wantRecv || gotSend != tc.wantSend {
			t.Errorf("oid=%d (%s): encoded I/O OIDs = (%d,%d,%d,%d); want (%d,%d,%d,%d)",
				tc.oid, e.Name, gotIn, gotOut, gotRecv, gotSend,
				tc.wantIn, tc.wantOut, tc.wantRecv, tc.wantSend)
		}
	}
}

// TestBootstrapPgTypeTuplesWritesCanonicalHeap end-to-end test: invoke
// bootstrapPgTypeTuples in a temp data dir (after seeding base/1 and
// base/5), then walk base/1/1247 page 0 and assert every heap tuple's
// data bytes carry a valid 'c'/'s'/'i'/'d' at offset 128 (typalign) and
// 'p'/'e'/'x'/'m' at offset 129 (typstorage). Mirrors the invariant
// PG-standby's TupleDescInitEntry depends on.
func TestBootstrapPgTypeTuplesWritesCanonicalHeap(t *testing.T) {
	dataDir := t.TempDir()
	for _, d := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tidMap, err := bootstrapPgTypeTuples(dataDir)
	if err != nil {
		t.Fatalf("bootstrapPgTypeTuples: %v", err)
	}
	wantTuples := len(tidMap)
	for _, base := range []string{"base/1", "base/5"} {
		path := filepath.Join(dataDir, base, "1247")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(raw)%int(storage.BlockSize) != 0 {
			t.Fatalf("%s: length %d not a multiple of BlockSize %d", path, len(raw), storage.BlockSize)
		}
		seenOIDs := 0
		// Walk all pages, every tuple on each page.
		for blk := 0; blk < len(raw)/int(storage.BlockSize); blk++ {
			page := storage.Page(raw[blk*int(storage.BlockSize) : (blk+1)*int(storage.BlockSize)])
			for off := 1; ; off++ {
				tuple, err := storage.PageGetHeapTuple(page, uint16(off))
				if err != nil {
					break
				}
				// HeapTuple.Data is the column-data payload after
				// the header. typalign at offset 128.
				data := tuple.Data
				if len(data) < 148 {
					t.Errorf("%s blk=%d off=%d: tuple data %d bytes < 148", path, blk, off, len(data))
					continue
				}
				align := data[128]
				if align != 'c' && align != 's' && align != 'i' && align != 'd' {
					t.Errorf("%s blk=%d off=%d: typalign at 128 = %#x (%q), expected c/s/i/d", path, blk, off, align, align)
				}
				storageByte := data[129]
				if storageByte != 'p' && storageByte != 'e' && storageByte != 'x' && storageByte != 'm' {
					t.Errorf("%s blk=%d off=%d: typstorage at 129 = %#x (%q), expected p/e/x/m", path, blk, off, storageByte, storageByte)
				}
				seenOIDs++
			}
		}
		if seenOIDs != wantTuples {
			t.Errorf("%s: walked %d tuples, expected %d entries", path, seenOIDs, wantTuples)
		}
	}
}

// TestPgTypeAllEntriesCountAndCoverage pins the total count of pgTypeAllEntries()
// and verifies that all critical OIDs needed for PG18 boot are present.
func TestPgTypeAllEntriesCountAndCoverage(t *testing.T) {
	entries := pgTypeAllEntries()
	// 113 base types from pg_type.dat + 83 array peers (minus a few without
	// array_type_oid) = 193 total. Guard with a minimum and exact count.
	const wantMin = 180
	const wantExact = 193
	if len(entries) < wantMin {
		t.Errorf("pgTypeAllEntries: %d entries, want >= %d", len(entries), wantMin)
	}
	if len(entries) != wantExact {
		t.Errorf("pgTypeAllEntries: %d entries, want exactly %d (update if pg_type.dat changes)", len(entries), wantExact)
	}

	// Critical OIDs that must be present for PG18 standby boot.
	mustHave := []uint32{
		16,   // bool
		17,   // bytea
		18,   // char
		19,   // name
		20,   // int8
		21,   // int2
		23,   // int4
		25,   // text
		26,   // oid
		700,  // float4
		701,  // float8
		1043, // varchar
		1184, // timestamptz
		// array peers
		1000, // _bool
		1007, // _int4
		1009, // _text
		1028, // _oid
	}
	oidSet := make(map[uint32]bool, len(entries))
	for _, e := range entries {
		oidSet[e.OID] = true
	}
	for _, oid := range mustHave {
		if !oidSet[oid] {
			t.Errorf("pgTypeAllEntries: missing critical OID %d", oid)
		}
	}
}

// TestPgTypeAllEntriesTypalignValid verifies every entry from pgTypeAllEntries()
// has a valid typalign byte ('c'/'s'/'i'/'d') — the exact invariant PG18's
// populate_compact_attribute_internal enforces at tupdesc.c:105.
func TestPgTypeAllEntriesTypalignValid(t *testing.T) {
	for _, e := range pgTypeAllEntries() {
		switch e.Align {
		case 'c', 's', 'i', 'd':
			// ok
		default:
			t.Errorf("oid=%d (%s): typalign=%#x (%q) — not in {c,s,i,d}",
				e.OID, e.Name, e.Align, e.Align)
		}
		switch e.Storage {
		case 'p', 'e', 'x', 'm':
			// ok
		default:
			t.Errorf("oid=%d (%s): typstorage=%#x (%q) — not in {p,e,x,m}",
				e.OID, e.Name, e.Storage, e.Storage)
		}
	}
}
