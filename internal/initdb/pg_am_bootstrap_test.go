package initdb

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/storage"
)

// TestPgAmRowBtreeMatchesFormPgAm pins the pg_am heap tuple encoding
// for M0106-0010 step 2. PG18's RelationInitIndexAccessInfo casts the
// returned HeapTuple data as Form_pg_am, so the four fields must land
// at the expected struct offsets:
//
//	offset  0  oid       (uint32 LE)
//	offset  4  amname    (NameData, 64 bytes, NUL-padded)
//	offset 68  amhandler (regproc / uint32 LE)
//	offset 72  amtype    (char, 1 byte)
//
// Any alignment or encoder regression that breaks the PG read path
// surfaces here without needing the full E2E harness.
func TestPgAmRowBtreeMatchesFormPgAm(t *testing.T) {
	cols := pgAmColDefs()
	row := pgAmRow(pgAmEntry{OID: 403, Name: "btree", HandlerID: 330, AmType: 'i'})
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	if got := len(payload); got < 73 {
		t.Fatalf("payload too short: %d bytes, want ≥73", got)
	}
	le := binary.LittleEndian
	if oid := le.Uint32(payload[0:4]); oid != 403 {
		t.Fatalf("oid: got %d, want 403", oid)
	}
	name := payload[4:68]
	wantName := make([]byte, 64)
	copy(wantName, []byte("btree"))
	if !bytes.Equal(name, wantName) {
		t.Fatalf("amname: got % x, want NUL-padded 'btree'", name)
	}
	if h := le.Uint32(payload[68:72]); h != 330 {
		t.Fatalf("amhandler: got %d, want 330", h)
	}
	if payload[72] != 'i' {
		t.Fatalf("amtype: got %q, want 'i'", payload[72])
	}
}

// TestPgAmInitialEntriesCoverPg18Defaults guards the seven AM seed
// rows against accidental loss (drop any one and PG fails the
// corresponding CREATE INDEX during recovery). Mirrors PG18's
// src/include/catalog/pg_am.dat.
func TestPgAmInitialEntriesCoverPg18Defaults(t *testing.T) {
	entries := pgAmInitialEntries()
	want := map[uint32]pgAmEntry{
		2:    {2, "heap", 3, 't'},
		403:  {403, "btree", 330, 'i'},
		405:  {405, "hash", 331, 'i'},
		783:  {783, "gist", 332, 'i'},
		2742: {2742, "gin", 333, 'i'},
		4000: {4000, "spgist", 334, 'i'},
		3580: {3580, "brin", 335, 'i'},
	}
	if len(entries) != len(want) {
		t.Fatalf("entry count: got %d, want %d", len(entries), len(want))
	}
	for _, e := range entries {
		exp, ok := want[e.OID]
		if !ok {
			t.Fatalf("unexpected AM OID %d", e.OID)
		}
		if e != exp {
			t.Fatalf("entry %d: got %+v, want %+v", e.OID, e, exp)
		}
	}
}

// TestBootstrapPgAmTuplesWritesBtreeRowToBase1And5 covers the
// integration: bootstrapPgAmTuples must write base/1/2601 and
// base/5/2601, each a heap page that contains the btree (OID 403)
// tuple at a recoverable offset. PG opens these files directly via
// the local relation map, so both copies are load-bearing.
func TestBootstrapPgAmTuplesWritesBtreeRowToBase1And5(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if err := bootstrapPgAmTuples(dir); err != nil {
		t.Fatalf("bootstrapPgAmTuples: %v", err)
	}
	for _, sub := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, sub, "2601")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if got := len(raw); got != storage.BlockSize {
			t.Fatalf("%s: page size %d, want %d", path, got, storage.BlockSize)
		}
		// All seven rows fit on one page. Spot-check by searching for
		// the NUL-padded "btree" name inside the page contents.
		needle := make([]byte, 64)
		copy(needle, []byte("btree"))
		if !bytes.Contains(raw, needle) {
			t.Fatalf("%s: btree amname not found in page", path)
		}
	}
}
