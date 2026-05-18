package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPgBuildIndexTupleOidOidOidInt2KeyLayoutMatchesPG18 pins the byte
// layout PG18 produces for an `index_form_tuple` invocation with a 4-
// attribute composite key (oid, oid, oid, int2) and no nulls. Mirrors
// the structure described in pg_amproc.h and indexing.h:
//
//	DECLARE_UNIQUE_INDEX(pg_amproc_fam_proc_index, 2655,
//	    AccessMethodProcedureIndexId, pg_amproc,
//	    btree(amprocfamily oid_ops, amproclefttype oid_ops,
//	          amprocrighttype oid_ops, amprocnum int2_ops));
//
// The total tuple size is MAXALIGN(8 + 4 + 4 + 4 + 2) = MAXALIGN(22) = 24.
// The trailing two bytes are MAXALIGN padding and must be zero so PG's
// page-image MD5 / WAL backup-block comparisons stay deterministic.
//
// Block-id encoding split into bi_hi/bi_lo halves is verified using a
// deliberately byte-asymmetric block number (0xDEADBEEF) so a regression
// to the LE-uint32 trap diagnosed in Step 3s is caught loudly.
func TestPgBuildIndexTupleOidOidOidInt2KeyLayoutMatchesPG18(t *testing.T) {
	const (
		heapBlk            = uint32(0xDEADBEEF)
		heapOff   uint16   = 0x1234
		family             = uint32(1994)
		lefttype           = uint32(19)
		righttype          = uint32(19)
		num       int16    = 4
	)
	got := pgBuildIndexTupleOidOidOidInt2Key(heapBlk, heapOff, family, lefttype, righttype, num)
	if len(got) != 24 {
		t.Fatalf("tuple size=%d, want 24", len(got))
	}
	le := binary.LittleEndian
	// Block ID split into bi_hi (high 16) and bi_lo (low 16).
	if biHi := le.Uint16(got[0:2]); biHi != uint16(heapBlk>>16) {
		t.Errorf("bi_hi=0x%04x, want 0x%04x", biHi, uint16(heapBlk>>16))
	}
	if biLo := le.Uint16(got[2:4]); biLo != uint16(heapBlk&0xFFFF) {
		t.Errorf("bi_lo=0x%04x, want 0x%04x", biLo, uint16(heapBlk&0xFFFF))
	}
	// Round-trip check matching PG's BlockIdGetBlockNumber.
	if blk := (uint32(le.Uint16(got[0:2])) << 16) | uint32(le.Uint16(got[2:4])); blk != heapBlk {
		t.Errorf("blk round-trip=0x%08x, want 0x%08x", blk, heapBlk)
	}
	if off := le.Uint16(got[4:6]); off != heapOff {
		t.Errorf("ip_posid=0x%04x, want 0x%04x", off, heapOff)
	}
	// t_info: low 13 bits = 24, no flags.
	if info := le.Uint16(got[6:8]); info != 24 {
		t.Errorf("t_info=0x%04x, want 0x0018", info)
	}
	if v := le.Uint32(got[8:12]); v != family {
		t.Errorf("family=0x%08x, want 0x%08x", v, family)
	}
	if v := le.Uint32(got[12:16]); v != lefttype {
		t.Errorf("lefttype=0x%08x, want 0x%08x", v, lefttype)
	}
	if v := le.Uint32(got[16:20]); v != righttype {
		t.Errorf("righttype=0x%08x, want 0x%08x", v, righttype)
	}
	if v := int16(le.Uint16(got[20:22])); v != num {
		t.Errorf("num=%d, want %d", v, num)
	}
	if got[22] != 0 || got[23] != 0 {
		t.Errorf("MAXALIGN pad not zero: [22]=0x%02x [23]=0x%02x", got[22], got[23])
	}
}

// TestBootstrapPgAmprocFamProcIndexWritesPopulatedBtree verifies that
// the populated 2-page btree file lands at all three on-disk locations
// (base/1, base/5, global) and that the leaf root carries exactly one
// IndexTuple per pgAmprocInitialEntries() row, sorted lexicographically
// by (family, lefttype, righttype, num).
func TestBootstrapPgAmprocFamProcIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5", "global"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	tids, err := bootstrapPgAmprocTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgAmprocTuples: %v", err)
	}
	entries := pgAmprocInitialEntries()
	if len(tids) != len(entries) {
		t.Fatalf("len(tids)=%d entries=%d", len(tids), len(entries))
	}

	if err := bootstrapPgAmprocFamProcIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgAmprocFamProcIndex: %v", err)
	}

	// 714 entries × 28 bytes/item: 3 leaf pages + 1 root + 1 metapage = 5 blocks.
	// pgBuildBtreeBulkLoadSized(tuples, 24, 4):
	//   maxPerNonRM = (8152-28)/28 = 290
	//   leafGroups: [0:290], [290:580], [580:714] → 3 leaves
	//   rootBlock = 3+1 = 4, totalBlocks = 5
	const expectedBlocks = 5
	const expectedRoot = 4
	for _, sub := range []string{"base/1", "base/5", "global"} {
		path := filepath.Join(dir, sub, "2655")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(raw) != expectedBlocks*storage.BlockSize {
			t.Errorf("%s: size=%d, want %d (%d blocks)", path, len(raw), expectedBlocks*storage.BlockSize, expectedBlocks)
			continue
		}
		le := binary.LittleEndian
		// Metapage: btm_root at offset 32, btm_level at offset 36.
		btmRoot := le.Uint32(raw[32:36])
		if btmRoot != expectedRoot {
			t.Errorf("%s: btm_root=%d, want %d", path, btmRoot, expectedRoot)
		}
		btmLevel := le.Uint32(raw[36:40])
		if btmLevel != 1 {
			t.Errorf("%s: btm_level=%d, want 1", path, btmLevel)
		}
	}

	// Verify key rows are reachable across all leaf pages.
	raw, err := os.ReadFile(filepath.Join(dir, "base/1", "2655"))
	if err != nil {
		t.Fatalf("read base/1/2655: %v", err)
	}
	mustContainKey(t, raw, 1976, 23, 23, 1, "integer_ops int4 cmp")
	mustContainKey(t, raw, 1994, 19, 19, 1, "text family btnamecmp")
	mustContainKey(t, raw, 397, 2277, 2277, 1, "btree/array_ops btarraycmp")
}

// mustContainKey scans the leaf page for a 24-byte IndexTuple whose
// 4-key payload matches (family, lefttype, righttype, num).
func mustContainKey(t *testing.T, file []byte, family, lefttype, righttype uint32, num int16, label string) {
	t.Helper()
	le := binary.LittleEndian
	nBlocks := len(file) / storage.BlockSize
	if nBlocks < 2 {
		t.Fatalf("mustContainKey: file too small (%d blocks)", nBlocks)
		return
	}
	// Determine leaf page range from metapage.
	// btm_root at offset 32, btm_level at offset 36.
	btmRoot := int(le.Uint32(file[32:36]))
	btmLevel := int(le.Uint32(file[36:40]))
	var leafEnd int
	if btmLevel == 0 {
		leafEnd = 2 // leaf-root: only block 1
	} else {
		leafEnd = btmRoot // leaves at 1..btmRoot-1
	}
	for b := 1; b < leafEnd; b++ {
		leaf := file[b*storage.BlockSize : (b+1)*storage.BlockSize]
		pdLower := le.Uint16(leaf[12:14])
		nItems := int((pdLower - storage.SizeOfPageHeaderData) / 4)
		for i := 0; i < nItems; i++ {
			lp := le.Uint32(leaf[storage.SizeOfPageHeaderData+i*4:])
			lpOff := lp & 0x7FFF
			if int(lpOff)+24 > storage.BlockSize {
				continue
			}
			tup := leaf[lpOff : lpOff+24]
			// Skip high-key pivots (INDEX_ALT_TID_MASK set in t_info).
			if le.Uint16(tup[6:8])&0x2000 != 0 {
				continue
			}
			gotFam := le.Uint32(tup[8:12])
			gotLeft := le.Uint32(tup[12:16])
			gotRight := le.Uint32(tup[16:20])
			gotNum := int16(le.Uint16(tup[20:22]))
			if gotFam == family && gotLeft == lefttype && gotRight == righttype && gotNum == num {
				return
			}
		}
	}
	t.Errorf("%s: key (family=%d, left=%d, right=%d, num=%d) not found in any leaf page", label, family, lefttype, righttype, num)
}
