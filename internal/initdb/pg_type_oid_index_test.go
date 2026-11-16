package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapPgTypeOidIndexWritesPopulatedBtree (M0106-0010 step 3cz)
// pins the populated 2-page btree layout produced by
// bootstrapPgTypeOidIndex for pg_type_oid_index (OID 2703). The file
// must contain a metapage plus a leaf-root with one IndexTuple per
// pg_type heap row, ordered by oid ascending, and the int4 (oid=23)
// row MUST be present — that exact lookup
// (SearchSysCache1(TYPEOID, 23)) is what triggered the Step 3cy
// "cache lookup failed for type 23" FATAL.
func TestBootstrapPgTypeOidIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	tids, err := bootstrapPgTypeTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgTypeTuples: %v", err)
	}
	if err := bootstrapPgTypeOidIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgTypeOidIndex: %v", err)
	}

	for _, base := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, base, "2703")
		buf, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// Since M0149-0001g slice 18 pg_type outgrows one leaf page; walk
		// every leaf page (BTP_LEAF in btpo_flags) in file order, skipping
		// the high key (slot 1) of non-rightmost leaves.
		if len(buf) < 2*storage.BlockSize || len(buf)%storage.BlockSize != 0 {
			t.Fatalf("%s: size=%d bytes, want a metapage plus at least one leaf", path, len(buf))
		}
		const pageHeaderSize = 24
		const itemIDSize = 4
		gotOIDs := make([]uint32, 0, len(tids))
		for blk := 1; blk < len(buf)/storage.BlockSize; blk++ {
			page := buf[blk*storage.BlockSize : (blk+1)*storage.BlockSize]
			special := int(binary.LittleEndian.Uint16(page[16:18]))
			next := binary.LittleEndian.Uint32(page[special+4 : special+8])
			flags := binary.LittleEndian.Uint16(page[special+12 : special+14])
			if flags&1 == 0 { // not a leaf
				continue
			}
			pdLower := binary.LittleEndian.Uint16(page[12:14])
			numItems := (int(pdLower) - pageHeaderSize) / itemIDSize
			first := 1
			if next != 0 {
				first = 2 // high key
			}
			for slot := first; slot <= numItems; slot++ {
				ipPos := pageHeaderSize + (slot-1)*itemIDSize
				raw := binary.LittleEndian.Uint32(page[ipPos : ipPos+4])
				offset := int(raw & 0x7FFF)
				length := int((raw >> 17) & 0x7FFF)
				if offset == 0 || length < 12 || offset+length > len(page) {
					t.Fatalf("%s: block %d slot %d bad line pointer (off=%d len=%d)", path, blk, slot, offset, length)
				}
				gotOIDs = append(gotOIDs, binary.LittleEndian.Uint32(page[offset+8:offset+12]))
			}
		}
		if len(gotOIDs) != len(tids) {
			t.Fatalf("%s: leaf entries=%d, want %d (one per heap tuple)", path, len(gotOIDs), len(tids))
		}

		// Strictly ascending order (sorted by oid).
		for i := 1; i < len(gotOIDs); i++ {
			if gotOIDs[i-1] >= gotOIDs[i] {
				t.Errorf("%s: slot %d oid=%d, slot %d oid=%d — not strictly ascending",
					path, i, gotOIDs[i-1], i+1, gotOIDs[i])
			}
		}

		// Mandatory presence of int4 (oid=23) — the exact key whose
		// absence triggered the Step 3cy FATAL.
		found := false
		for _, o := range gotOIDs {
			if o == 23 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: pg_type_oid_index missing int4 leaf (oid=23); got %v", path, gotOIDs)
		}
	}
}
