package initdb

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPgConversionInitialEntriesCount guards against accidental truncation of
// the 128-row pg_conversion seed table (OID 2607). PG18's pg_conversion.dat
// defines exactly 128 rows (OIDs 4402–4449 + 4452–4531).
func TestPgConversionInitialEntriesCount(t *testing.T) {
	entries := pgConversionInitialEntries()
	if len(entries) != 128 {
		t.Errorf("pgConversionInitialEntries: got %d entries, want 128", len(entries))
	}
}

// TestPgConversionInitialEntriesSpotCheck verifies a representative sample of
// rows against pg_conversion.dat authoritative values.
func TestPgConversionInitialEntriesSpotCheck(t *testing.T) {
	entries := pgConversionInitialEntries()
	byOID := make(map[uint32]pgConversionEntry, len(entries))
	for _, e := range entries {
		byOID[e.OID] = e
	}

	cases := []struct {
		oid    uint32
		name   string
		forEnc int32
		toEnc  int32
		proc   uint32
	}{
		{4402, "koi8_r_to_mic", pgEncKOI8R, pgEncMULEINTERNAL, 4302},
		{4403, "mic_to_koi8_r", pgEncMULEINTERNAL, pgEncKOI8R, 4303},
		{4424, "euc_jp_to_sjis", pgEncEUCJP, pgEncSJIS, 4324},
		{4452, "big5_to_utf8", pgEncBIG5, pgEncUTF8, 4352},
		{4480, "euc_cn_to_utf8", pgEncEUCCN, pgEncUTF8, 4360},
		{4518, "iso_8859_1_to_utf8", pgEncLATIN1, pgEncUTF8, 4374},
		{4530, "euc_jis_2004_to_shift_jis_2004", pgEncEUCJIS2004, pgEncSHIFTJIS2004, 4386},
		{4531, "shift_jis_2004_to_euc_jis_2004", pgEncSHIFTJIS2004, pgEncEUCJIS2004, 4387},
	}

	for _, tc := range cases {
		e, ok := byOID[tc.oid]
		if !ok {
			t.Errorf("OID %d (%s) not found in entries", tc.oid, tc.name)
			continue
		}
		if e.ConName != tc.name {
			t.Errorf("OID %d: ConName=%q, want %q", tc.oid, e.ConName, tc.name)
		}
		if e.ConForEncoding != tc.forEnc {
			t.Errorf("OID %d: ConForEncoding=%d, want %d", tc.oid, e.ConForEncoding, tc.forEnc)
		}
		if e.ConToEncoding != tc.toEnc {
			t.Errorf("OID %d: ConToEncoding=%d, want %d", tc.oid, e.ConToEncoding, tc.toEnc)
		}
		if e.ConProc != tc.proc {
			t.Errorf("OID %d: ConProc=%d, want %d", tc.oid, e.ConProc, tc.proc)
		}
		if e.ConNamespace != 11 {
			t.Errorf("OID %d: ConNamespace=%d, want 11 (pg_catalog)", tc.oid, e.ConNamespace)
		}
		if e.ConOwner != 10 {
			t.Errorf("OID %d: ConOwner=%d, want 10 (BOOTSTRAP_SUPERUSERID)", tc.oid, e.ConOwner)
		}
		if !e.ConDefault {
			t.Errorf("OID %d: ConDefault=false, want true", tc.oid)
		}
	}
}

// TestPgConversionInitialEntriesOIDsUnique guards against duplicate OIDs in
// the seed table.
func TestPgConversionInitialEntriesOIDsUnique(t *testing.T) {
	seen := make(map[uint32]bool)
	for _, e := range pgConversionInitialEntries() {
		if seen[e.OID] {
			t.Errorf("duplicate OID %d in pgConversionInitialEntries", e.OID)
		}
		seen[e.OID] = true
	}
}

// TestBootstrapPgConversionTuplesWritesHeap verifies that
// bootstrapPgConversionTuples writes 128 TIDs to the heap file
// base/{1,5}/2607 under a temporary data directory.
func TestBootstrapPgConversionTuplesWritesHeap(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "base", "1"), 0o700); err != nil {
		t.Fatalf("mkdir base/1: %v", err)
	}
	tids, err := bootstrapPgConversionTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgConversionTuples: %v", err)
	}
	if len(tids) != 128 {
		t.Errorf("TID map length: got %d, want 128", len(tids))
	}
	// Verify every expected OID has a TID entry.
	for _, e := range pgConversionInitialEntries() {
		if _, ok := tids[e.OID]; !ok {
			t.Errorf("no TID for conversion OID %d (%s)", e.OID, e.ConName)
		}
	}
}
