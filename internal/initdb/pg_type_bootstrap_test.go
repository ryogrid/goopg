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
	if err := bootstrapPgTypeTuples(dataDir); err != nil {
		t.Fatalf("bootstrapPgTypeTuples: %v", err)
	}
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
		if seenOIDs != len(pgTypeInitialEntries()) {
			t.Errorf("%s: walked %d tuples, expected %d entries", path, seenOIDs, len(pgTypeInitialEntries()))
		}
	}
}
