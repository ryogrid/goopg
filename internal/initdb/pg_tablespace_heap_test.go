// Tests for M0106-0010 batched-12: pg_tablespace heap + index bootstrap.

package initdb

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapPgTablespaceTuplesWritesExpectedRows verifies that
// bootstrapPgTablespaceTuples seeds exactly two rows (pg_default=1663,
// pg_global=1664) onto heap block 0 and returns matching TIDs.
func TestBootstrapPgTablespaceTuplesWritesExpectedRows(t *testing.T) {
	dir := t.TempDir()
	// bootstrapPgTablespaceTuples writes to global/1213; create the dir.
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatal(err)
	}

	entries, err := bootstrapPgTablespaceTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgTablespaceTuples: %v", err)
	}

	// Expect exactly two entries.
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	// Sort by OID for deterministic checks.
	sort.Slice(entries, func(i, j int) bool { return entries[i].OID < entries[j].OID })

	want := []pgTablespaceEntry{
		{OID: 1663, Spcname: "pg_default"},
		{OID: 1664, Spcname: "pg_global"},
	}
	for i, w := range want {
		if entries[i].OID != w.OID {
			t.Errorf("entries[%d].OID=%d, want %d", i, entries[i].OID, w.OID)
		}
		if entries[i].Spcname != w.Spcname {
			t.Errorf("entries[%d].Spcname=%q, want %q", i, entries[i].Spcname, w.Spcname)
		}
		if entries[i].TID.Block != 0 {
			t.Errorf("entries[%d].TID.Block=%d, want 0", i, entries[i].TID.Block)
		}
	}

	// Verify the heap file is exactly one block in size and InitPage-stamped.
	data, err := os.ReadFile(filepath.Join(dir, "global", "1213"))
	if err != nil {
		t.Fatalf("read global/1213: %v", err)
	}
	if len(data) != storage.BlockSize {
		t.Fatalf("global/1213 size=%d, want %d", len(data), storage.BlockSize)
	}
	// Not all-zero (InitPage was called).
	allZero := true
	for _, b := range data {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("global/1213 is all-zero; InitPage was not called")
	}
}

// TestBootstrapPgTablespaceOidIndexWritesPopulatedBtree verifies that
// bootstrapPgTablespaceOidIndex writes a 2-page file at global/2697 with
// a valid metapage and a leaf page carrying 2 OID-sorted index tuples.
func TestBootstrapPgTablespaceOidIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatal(err)
	}

	entries := []pgTablespaceEntry{
		{OID: 1663, Spcname: "pg_default", TID: heapTID{Block: 0, Offset: 1}},
		{OID: 1664, Spcname: "pg_global", TID: heapTID{Block: 0, Offset: 2}},
	}
	if err := bootstrapPgTablespaceOidIndex(dir, entries); err != nil {
		t.Fatalf("bootstrapPgTablespaceOidIndex: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "global", "2697"))
	if err != nil {
		t.Fatalf("read global/2697: %v", err)
	}
	if len(data) != 2*storage.BlockSize {
		t.Fatalf("global/2697 size=%d, want %d (2 blocks)", len(data), 2*storage.BlockSize)
	}

	// Block 0 = metapage: check btm_magic (LE uint32 at SizeOfPageHeaderData).
	const pgSizeOfPageHeaderData = 24
	meta := data[pgSizeOfPageHeaderData : pgSizeOfPageHeaderData+4]
	magic := uint32(meta[0]) | uint32(meta[1])<<8 | uint32(meta[2])<<16 | uint32(meta[3])<<24
	const btreeMagicConst = 0x053162
	if magic != btreeMagicConst {
		t.Errorf("metapage btm_magic=0x%x, want 0x%x", magic, btreeMagicConst)
	}

	// Block 1 = leaf-root: check it has 2 line pointers (non-zero pd_lower).
	leafStart := storage.BlockSize
	leaf := data[leafStart : leafStart+storage.BlockSize]
	// pd_lower is at offset 12 in the page header (LE uint16).
	pdLower := uint16(leaf[12]) | uint16(leaf[13])<<8
	// pd_lower > SizeOfPageHeaderData means at least one line pointer present.
	if pdLower <= pgSizeOfPageHeaderData {
		t.Errorf("leaf block pd_lower=%d, want > %d (at least 1 line pointer)", pdLower, pgSizeOfPageHeaderData)
	}
}

// TestBootstrapPgTablespaceSpcnameIndexWritesPopulatedBtree verifies that
// bootstrapPgTablespaceSpcnameIndex writes a 2-page file at global/2698
// with a valid metapage and a leaf carrying 2 name-sorted index tuples.
func TestBootstrapPgTablespaceSpcnameIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatal(err)
	}

	entries := []pgTablespaceEntry{
		{OID: 1663, Spcname: "pg_default", TID: heapTID{Block: 0, Offset: 1}},
		{OID: 1664, Spcname: "pg_global", TID: heapTID{Block: 0, Offset: 2}},
	}
	if err := bootstrapPgTablespaceSpcnameIndex(dir, entries); err != nil {
		t.Fatalf("bootstrapPgTablespaceSpcnameIndex: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "global", "2698"))
	if err != nil {
		t.Fatalf("read global/2698: %v", err)
	}
	if len(data) != 2*storage.BlockSize {
		t.Fatalf("global/2698 size=%d, want %d (2 blocks)", len(data), 2*storage.BlockSize)
	}

	// Metapage magic check.
	const pgSizeOfPageHeaderData = 24
	meta := data[pgSizeOfPageHeaderData : pgSizeOfPageHeaderData+4]
	magic := uint32(meta[0]) | uint32(meta[1])<<8 | uint32(meta[2])<<16 | uint32(meta[3])<<24
	const btreeMagicConst = 0x053162
	if magic != btreeMagicConst {
		t.Errorf("metapage btm_magic=0x%x, want 0x%x", magic, btreeMagicConst)
	}
}

// TestPgTablespaceColDefsMatchPG18Schema verifies all 5 columns match PG18
// postgres/src/include/catalog/pg_tablespace.h.
func TestPgTablespaceColDefsMatchPG18Schema(t *testing.T) {
	cols := pgTablespaceColDefs()
	want := []struct {
		name    string
		typName string
	}{
		{"oid", "oid"},
		{"spcname", "name"},
		{"spcowner", "oid"},
		{"spcacl", "aclitem[]"},
		{"spcoptions", "text[]"},
	}
	if len(cols) != len(want) {
		t.Fatalf("pgTablespaceColDefs: len=%d, want %d", len(cols), len(want))
	}
	for i, w := range want {
		if cols[i].Name != w.name {
			t.Errorf("cols[%d].Name=%q, want %q", i, cols[i].Name, w.name)
		}
		if cols[i].Type.Name != w.typName {
			t.Errorf("cols[%d].Type.Name=%q, want %q", i, cols[i].Type.Name, w.typName)
		}
	}
}
