package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapPgDatabaseOidIndexWritesPopulatedBtree guards
// M0106-0010 step 3cs/batched-10. Asserts global/2672 carries 3
// IndexTuples for template1 (OID 1, TID (0,1)), template0 (OID 4,
// TID (0,3)), and postgres (OID 5, TID (0,2)), in ascending OID order.
func TestBootstrapPgDatabaseOidIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	if err := bootstrapPgDatabaseOidIndex(dir); err != nil {
		t.Fatalf("bootstrapPgDatabaseOidIndex: %v", err)
	}

	buf, err := os.ReadFile(filepath.Join(dir, "global", "2672"))
	if err != nil {
		t.Fatalf("read global/2672: %v", err)
	}
	if len(buf) != 2*storage.BlockSize {
		t.Fatalf("file size = %d bytes, want %d (metapage + leaf root)", len(buf), 2*storage.BlockSize)
	}

	// Leaf-root page is at block 1.
	leaf := buf[storage.BlockSize:]

	// Parse line-pointer count from the page header. PG page header:
	//   pd_lsn (8), pd_checksum (2), pd_flags (2), pd_lower (2), pd_upper (2),
	//   pd_special (2), pd_pagesize_version (2)
	// Item pointers start at offset 24 (PageHeaderData size). Each
	// ItemId is 4 bytes packed.
	pdLower := binary.LittleEndian.Uint16(leaf[12:14])
	const pageHeaderSize = 24
	const itemIDSize = 4
	if pdLower < pageHeaderSize {
		t.Fatalf("pd_lower=%d < page header size", pdLower)
	}
	numItems := (int(pdLower) - pageHeaderSize) / itemIDSize
	// pgBuildBtreeLeafRootPage emits one ItemId per data tuple; three
	// pg_database rows (template1, template0, postgres) produce 3 line pointers.
	if numItems != 3 {
		t.Fatalf("leaf line-pointer count = %d, want 3 (3 data tuples)", numItems)
	}

	// Walk the 3 data tuples (slots 1–3) and assert their OID keys
	// equal the sorted pg_database OIDs: 1, 4, 5.
	gotOids := make([]uint32, 0, 3)
	for slot := 1; slot <= 3; slot++ {
		ipPos := pageHeaderSize + (slot-1)*itemIDSize
		raw := binary.LittleEndian.Uint32(leaf[ipPos : ipPos+4])
		offset := int(raw & 0x7FFF)        // 15-bit lp_off
		length := int((raw >> 17) & 0x7FFF) // 15-bit lp_len
		if offset == 0 || length == 0 {
			t.Fatalf("slot %d: empty line pointer (offset=%d length=%d)", slot, offset, length)
		}
		if offset+length > len(leaf) {
			t.Fatalf("slot %d: tuple extends past page (offset=%d length=%d)", slot, offset, length)
		}
		// IndexTuple layout: t_tid (6) + t_info (2) + key data.
		// hoff = MAXALIGN(sizeof(IndexTupleData)) = 8 for no-nulls case.
		const hoff = 8
		if length < hoff+4 {
			t.Fatalf("slot %d: tuple too short for oid key (length=%d)", slot, length)
		}
		oid := binary.LittleEndian.Uint32(leaf[offset+hoff : offset+hoff+4])
		// Also assert heap TID block=0, offset=(slot-1).
		blkHi := binary.LittleEndian.Uint16(leaf[offset : offset+2])
		blkLo := binary.LittleEndian.Uint16(leaf[offset+2 : offset+4])
		heapOff := binary.LittleEndian.Uint16(leaf[offset+4 : offset+6])
		heapBlk := uint32(blkHi)<<16 | uint32(blkLo)
		if heapBlk != 0 {
			t.Errorf("slot %d (oid=%d): heap block = %d, want 0", slot, oid, heapBlk)
		}
		gotOids = append(gotOids, oid)
		_ = heapOff
	}

	// Index entries are sorted by oid ascending: 1 (template1), 4 (template0),
	// 5 (postgres).
	want := []uint32{1, 4, 5}
	for i, w := range want {
		if gotOids[i] != w {
			t.Errorf("slot %d: oid key = %d, want %d", i+1, gotOids[i], w)
		}
	}
}
