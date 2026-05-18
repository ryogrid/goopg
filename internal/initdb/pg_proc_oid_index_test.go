package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapPgProcOidIndexWritesPopulatedBtree pins the on-disk shape of
// bootstrapPgProcOidIndex for pg_proc_oid_index (OID 2690). The file must be
// exactly 2 pages (metapage + leaf-root), the leaf must carry one oid-keyed
// IndexTuple per AM handler entry written by bootstrapPgProcTuples, OIDs must
// be strictly ascending, and bthandler (OID 330) — exercised by every nailed
// btree index during InitPostgres → RelationInitIndexAccessInfo — must be
// present. Mirrors the M0106-0010 step 3cz invariants for
// pg_type_oid_index.
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
			const hoff = 8
			if length < hoff+4 {
				t.Fatalf("%s: slot %d tuple too short for oid key (len=%d)", path, slot, length)
			}
			oid := binary.LittleEndian.Uint32(leaf[offset+hoff : offset+hoff+4])
			gotOIDs = append(gotOIDs, oid)
		}

		for i := 1; i < len(gotOIDs); i++ {
			if gotOIDs[i-1] >= gotOIDs[i] {
				t.Errorf("%s: slot %d oid=%d, slot %d oid=%d — not strictly ascending",
					path, i, gotOIDs[i-1], i+1, gotOIDs[i])
			}
		}

		// bthandler (OID 330) is the canary: every nailed btree index
		// exercises it during RelationInitIndexAccessInfo →
		// OidFunctionCall0(amhandler).
		found := false
		for _, o := range gotOIDs {
			if o == 330 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: pg_proc_oid_index missing bthandler leaf (oid=330); got %v", path, gotOIDs)
		}
	}
}
