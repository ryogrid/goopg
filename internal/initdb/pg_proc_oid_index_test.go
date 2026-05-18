package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapPgProcOidIndexWritesPopulatedBtree pins the on-disk shape of
// bootstrapPgProcOidIndex for pg_proc_oid_index (OID 2690). With 3397 entries
// the index spans multiple leaves (multi-leaf btree). The invariants are:
//   - The file exists at base/1/2690 and base/5/2690
//   - Length is a positive multiple of storage.BlockSize
//   - Length > 2*storage.BlockSize (confirms multi-leaf, not single-leaf-root)
//   - Metapage has BTREE_MAGIC and btm_root > 0
//   - bthandler (OID 330) is present in some leaf page
//
// M0106-0010 batched-14: upgraded from single-leaf (pgBuildBtreeLeafRootPage)
// to multi-leaf (pgBuildBtreeBulkLoad) to accommodate the full 3397 entries.
func TestBootstrapPgProcOidIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	tids, err := bootstrapPgProcTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgProcTuples: %v", err)
	}
	if err := bootstrapPgProcOidIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgProcOidIndex: %v", err)
	}

	for _, base := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, base, "2690")
		buf, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// Must be a positive multiple of BlockSize.
		if len(buf) == 0 || len(buf)%storage.BlockSize != 0 {
			t.Fatalf("%s: size=%d bytes, want non-zero multiple of %d", path, len(buf), storage.BlockSize)
		}
		// With 3397 entries the index must span more than 2 blocks (meta + single leaf).
		if len(buf) <= 2*storage.BlockSize {
			t.Fatalf("%s: size=%d bytes, want >%d (multi-leaf expected for 3397 entries)",
				path, len(buf), 2*storage.BlockSize)
		}

		// Metapage (block 0): check BTREE_MAGIC and btm_root > 0.
		meta := buf[:storage.BlockSize]
		le := binary.LittleEndian
		const btreeMagic = 0x053162
		// btm_magic is at offset 24 (after PageHeaderData).
		magic := le.Uint32(meta[24:28])
		if magic != btreeMagic {
			t.Errorf("%s: metapage magic=0x%x, want 0x%x", path, magic, btreeMagic)
		}
		// btm_root is at offset 28.
		root := le.Uint32(meta[28:32])
		if root == 0 {
			t.Errorf("%s: metapage btm_root=0, want >0", path)
		}

		// Scan all non-meta pages looking for bthandler (OID 330).
		foundBthandler := false
		const pageHeaderSize = 24
		const itemIDSize = 4
		const hoff = 8
		nBlocks := len(buf) / storage.BlockSize
		for blk := 1; blk < nBlocks; blk++ {
			page := buf[blk*storage.BlockSize : (blk+1)*storage.BlockSize]
			pdLower := le.Uint16(page[12:14])
			if int(pdLower) < pageHeaderSize {
				continue
			}
			numItems := (int(pdLower) - pageHeaderSize) / itemIDSize
			for slot := 0; slot < numItems; slot++ {
				ipPos := pageHeaderSize + slot*itemIDSize
				raw := le.Uint32(page[ipPos : ipPos+4])
				offset := int(raw & 0x7FFF)
				length := int((raw >> 17) & 0x7FFF)
				if offset == 0 || length < hoff+4 {
					continue
				}
				oid := le.Uint32(page[offset+hoff : offset+hoff+4])
				if oid == 330 {
					foundBthandler = true
				}
			}
		}
		if !foundBthandler {
			t.Errorf("%s: pg_proc_oid_index missing bthandler leaf (oid=330)", path)
		}
	}
}
