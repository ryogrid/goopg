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
		if len(buf) != 2*storage.BlockSize {
			t.Fatalf("%s: size=%d bytes, want %d (meta + leaf-root)", path, len(buf), 2*storage.BlockSize)
		}

		leaf := buf[storage.BlockSize:]
		pdLower := binary.LittleEndian.Uint16(leaf[12:14])
		const pageHeaderSize = 24
		const itemIDSize = 4
		if pdLower < pageHeaderSize {
			t.Fatalf("%s: pd_lower=%d < page header size", path, pdLower)
		}
		numItems := (int(pdLower) - pageHeaderSize) / itemIDSize
		if numItems != len(tids) {
			t.Fatalf("%s: leaf line-pointer count=%d, want %d (one per heap tuple)", path, numItems, len(tids))
		}

		gotOIDs := make([]uint32, 0, numItems)
		for slot := 1; slot <= numItems; slot++ {
			ipPos := pageHeaderSize + (slot-1)*itemIDSize
			raw := binary.LittleEndian.Uint32(leaf[ipPos : ipPos+4])
			offset := int(raw & 0x7FFF)
			length := int((raw >> 17) & 0x7FFF)
			if offset == 0 || length == 0 {
				t.Fatalf("%s: slot %d empty line pointer (off=%d len=%d)", path, slot, offset, length)
			}
			if offset+length > len(leaf) {
				t.Fatalf("%s: slot %d tuple past page (off=%d len=%d)", path, slot, offset, length)
			}
			// IndexTuple: t_tid (6) + t_info (2) + key data.
			// hoff = MAXALIGN(sizeof(IndexTupleData)) = 8 (no-nulls).
			const hoff = 8
			if length < hoff+4 {
				t.Fatalf("%s: slot %d tuple too short for oid key (len=%d)", path, slot, length)
			}
			oid := binary.LittleEndian.Uint32(leaf[offset+hoff : offset+hoff+4])
			gotOIDs = append(gotOIDs, oid)
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
