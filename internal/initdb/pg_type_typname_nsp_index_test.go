package initdb

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapPgTypeTypnameNspIndexWritesPopulatedBtree (B2.1a) pins the
// populated multi-leaf btree produced by bootstrapPgTypeTypnameNspIndex for
// pg_type_typname_nsp_index (OID 2704): metapage names a real root, every
// leaf tuple is the 80-byte (typname, typnamespace=11) shape in ascending
// name order, and the "text" row MUST be findable — `::text` resolution via
// SearchSysCache2(TYPENAMENSP, ...) is exactly what failed while this index
// was an empty placeholder (`type "text" does not exist` on a PG standby).
func TestBootstrapPgTypeTypnameNspIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"base/1", "base/5", "global"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	tids, err := bootstrapPgTypeTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgTypeTuples: %v", err)
	}
	if err := bootstrapPgTypeTypnameNspIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgTypeTypnameNspIndex: %v", err)
	}

	le := binary.LittleEndian
	path := filepath.Join(dir, "base", "1", "2704")
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	nPages := len(buf) / storage.BlockSize
	if nPages < 2 || nPages*storage.BlockSize != len(buf) {
		t.Fatalf("%s: %d bytes (%d pages) — want >= 2 whole pages", path, len(buf), nPages)
	}

	// Metapage must name a real root.
	base := storage.SizeOfPageHeaderData
	rootBlk := le.Uint32(buf[base+8 : base+12])
	if rootBlk == 0 {
		t.Fatal("metapage btm_root = P_NONE — index still empty")
	}

	// Walk every leaf (blocks 1..) collecting data tuples; verify shape,
	// ascending name order, and that "text" (nsp 11) is present.
	var prevName string
	sawText := false
	total := 0
	for blk := 1; blk < nPages; blk++ {
		page := buf[blk*storage.BlockSize : (blk+1)*storage.BlockSize]
		opaqueOff := storage.BlockSize - 16
		flags := le.Uint16(page[opaqueOff+12 : opaqueOff+14])
		if flags&0x1 == 0 { // BTP_LEAF
			continue // internal root page
		}
		next := le.Uint32(page[opaqueOff+4 : opaqueOff+8])
		count := int(le.Uint16(page[12:14])-uint16(storage.SizeOfPageHeaderData)) / 4
		start := 1
		if next != 0 {
			start = 2 // skip P_HIKEY on non-rightmost leaves
		}
		for i := start; i <= count; i++ {
			itemID := le.Uint32(page[storage.SizeOfPageHeaderData+(i-1)*4:])
			off := int(itemID & 0x7FFF)
			size := int((itemID >> 17) & 0x7FFF)
			if size != 80 {
				t.Fatalf("leaf %d slot %d: tuple size %d, want 80", blk, i, size)
			}
			name := string(bytes.TrimRight(page[off+8:off+72], "\x00"))
			if prevName != "" && name < prevName {
				t.Fatalf("leaf %d slot %d: name %q < previous %q — not sorted", blk, i, name, prevName)
			}
			prevName = name
			if nsp := le.Uint32(page[off+72 : off+76]); nsp != 11 {
				t.Fatalf("leaf %d slot %d (%q): nsp = %d, want 11", blk, i, name, nsp)
			}
			if name == "text" {
				sawText = true
			}
			total++
		}
	}
	if !sawText {
		t.Fatal(`no entry for typname "text"`)
	}
	if total != len(tids) {
		t.Fatalf("index carries %d entries, heap has %d rows", total, len(tids))
	}

	// All three copies must be identical.
	for _, d := range []string{"base/5", "global"} {
		other, err := os.ReadFile(filepath.Join(dir, d, "2704"))
		if err != nil || !bytes.Equal(other, buf) {
			t.Fatalf("%s/2704 differs from base/1 copy (err %v)", d, err)
		}
	}
}
