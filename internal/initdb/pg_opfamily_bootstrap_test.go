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

// TestPgOpfamilyInitialEntriesCount pins the expected row count (146)
// matching pg_opfamily.dat from PG18.
func TestPgOpfamilyInitialEntriesCount(t *testing.T) {
	entries := pgOpfamilyInitialEntries()
	if got, want := len(entries), 146; got != want {
		t.Fatalf("pgOpfamilyInitialEntries: got %d entries, want %d", got, want)
	}
}

// TestPgOpfamilyInitialEntriesNoDuplicateOIDs verifies that all OIDs in
// the entry set are unique — a duplicate would corrupt the oid index.
func TestPgOpfamilyInitialEntriesNoDuplicateOIDs(t *testing.T) {
	entries := pgOpfamilyInitialEntries()
	seen := make(map[uint32]string, len(entries))
	for _, e := range entries {
		if prev, dup := seen[e.OID]; dup {
			t.Errorf("duplicate OID %d: %s and %s", e.OID, prev, e.Name)
		}
		seen[e.OID] = e.Name
	}
}

// TestPgOpfamilyInitialEntriesContainsKnownEntries spot-checks well-known
// OIDs from pg_opfamily_d.h symbols against pg_opfamily.dat.
func TestPgOpfamilyInitialEntriesContainsKnownEntries(t *testing.T) {
	entries := pgOpfamilyInitialEntries()
	byOID := make(map[uint32]pgOpfamilyEntry, len(entries))
	for _, e := range entries {
		byOID[e.OID] = e
	}
	type want struct {
		method uint32
		name   string
	}
	cases := map[uint32]want{
		1976: {403, "integer_ops"},  // INTEGER_BTREE_FAM_OID
		1989: {403, "oid_ops"},      // OID_BTREE_FAM_OID
		1994: {403, "text_ops"},     // TEXT_BTREE_FAM_OID
		2095: {403, "text_pattern_ops"},
		424:  {403, "bool_ops"},     // BOOL_BTREE_FAM_OID
		426:  {403, "bpchar_ops"},   // BPCHAR_BTREE_FAM_OID
		434:  {403, "datetime_ops"}, // btree/datetime_ops (shared by timestamptz, date, timestamp)
	}
	for oid, w := range cases {
		got, ok := byOID[oid]
		if !ok {
			t.Errorf("OID %d (%s) missing from pgOpfamilyInitialEntries", oid, w.name)
			continue
		}
		if got.Method != w.method {
			t.Errorf("OID %d: method=%d, want %d", oid, got.Method, w.method)
		}
		if got.Name != w.name {
			t.Errorf("OID %d: name=%q, want %q", oid, got.Name, w.name)
		}
		if got.Namespace != 11 {
			t.Errorf("OID %d: namespace=%d, want 11 (pg_catalog)", oid, got.Namespace)
		}
		if got.Owner != 10 {
			t.Errorf("OID %d: owner=%d, want 10 (bootstrap superuser)", oid, got.Owner)
		}
	}
}

// TestPgOpfamilyColDefsMatchPg18Schema pins the 5-column
// FormData_pg_opfamily layout so future changes that drift the column
// order are caught before they corrupt the relcache init file.
func TestPgOpfamilyColDefsMatchPg18Schema(t *testing.T) {
	cols := pgOpfamilyColDefs()
	if got, want := len(cols), 5; got != want {
		t.Fatalf("pgOpfamilyColDefs: got %d cols, want 5", got)
	}
	want := []struct {
		name string
		typ  string
	}{
		{"oid", "oid"},
		{"opfmethod", "oid"},
		{"opfname", "name"},
		{"opfnamespace", "oid"},
		{"opfowner", "oid"},
	}
	for i, w := range want {
		if cols[i].Name != w.name {
			t.Errorf("col[%d]: name=%q, want %q", i, cols[i].Name, w.name)
		}
		if cols[i].Type.Name != w.typ {
			t.Errorf("col[%d]: type=%q, want %q", i, cols[i].Type.Name, w.typ)
		}
	}
}

// TestPgOpfamilyRowEncoding verifies that pgOpfamilyRow encodes a sample
// entry in PG18 FormData_pg_opfamily byte order. The layout is:
//
//	oid(4) + opfmethod(4) + opfname(64) + opfnamespace(4) + opfowner(4) = 80 bytes
func TestPgOpfamilyRowEncoding(t *testing.T) {
	cols := pgOpfamilyColDefs()
	row := pgOpfamilyRow(pgOpfamilyEntry{
		OID: 1989, Method: 403, Name: "oid_ops", Namespace: 11, Owner: 10,
	})
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	if got, want := len(payload), 80; got != want {
		t.Fatalf("payload length: got %d, want 80", got)
	}
	le := binary.LittleEndian
	if oid := le.Uint32(payload[0:4]); oid != 1989 {
		t.Errorf("oid: got %d, want 1989", oid)
	}
	if m := le.Uint32(payload[4:8]); m != 403 {
		t.Errorf("opfmethod: got %d, want 403 (btree)", m)
	}
	wantName := make([]byte, 64)
	copy(wantName, "oid_ops")
	if !bytes.Equal(payload[8:72], wantName) {
		t.Errorf("opfname: got % x, want NUL-padded 'oid_ops'", payload[8:72])
	}
	if ns := le.Uint32(payload[72:76]); ns != 11 {
		t.Errorf("opfnamespace: got %d, want 11 (pg_catalog)", ns)
	}
	if owner := le.Uint32(payload[76:80]); owner != 10 {
		t.Errorf("opfowner: got %d, want 10", owner)
	}
}

// TestBootstrapPgOpfamilyTuplesWritesRowsToBase1And5 verifies the
// end-to-end bootstrap writes the heap across base/{1,5}/2753,
// spans at least 2 pages (146 rows × ~100 bytes > 8 KiB),
// and the "oid_ops" family name is findable in the raw pages.
func TestBootstrapPgOpfamilyTuplesWritesRowsToBase1And5(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	tids, err := bootstrapPgOpfamilyTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgOpfamilyTuples: %v", err)
	}
	if got := len(tids); got != 146 {
		t.Errorf("TID map size: got %d, want 146", got)
	}
	needle := make([]byte, 64)
	copy(needle, "oid_ops")
	for _, sub := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, sub, "2753")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// 146 rows must fill at least 2 pages.
		if got := len(raw); got < 2*storage.BlockSize || got%storage.BlockSize != 0 {
			t.Fatalf("%s: file size %d want ≥2×%d and multiple of BlockSize", path, got, storage.BlockSize)
		}
		if !bytes.Contains(raw, needle) {
			t.Fatalf("%s: 'oid_ops' opfname not found in file", path)
		}
	}
}

// TestBootstrapPgOpfamilyOidIndexWritesBtree verifies that
// bootstrapPgOpfamilyOidIndex produces a valid btree file at
// base/{1,5}/2755 with correct metapage magic.
func TestBootstrapPgOpfamilyOidIndexWritesBtree(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	// Build TID map from initial entries.
	tids := make(map[uint32]heapTID)
	for i, e := range pgOpfamilyInitialEntries() {
		tids[e.OID] = heapTID{Block: uint32(i / 80), Offset: uint16(i%80 + 1)}
	}
	if err := bootstrapPgOpfamilyOidIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgOpfamilyOidIndex: %v", err)
	}
	const btreeMagic = 0x053162
	le := binary.LittleEndian
	for _, sub := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, sub, "2755")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(raw) < storage.BlockSize || len(raw)%storage.BlockSize != 0 {
			t.Fatalf("%s: file size %d not a valid multiple of BlockSize", path, len(raw))
		}
		// Metapage magic at offset SizeOfPageHeaderData.
		const magicOff = 24
		if magic := le.Uint32(raw[magicOff : magicOff+4]); magic != btreeMagic {
			t.Errorf("%s: btree magic=0x%x, want 0x%x", path, magic, btreeMagic)
		}
	}
}
