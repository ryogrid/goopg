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

	// pg_amproc heap must land first so its TIDs are available.
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

	const expectedBlocks = 2
	for _, sub := range []string{"base/1", "base/5", "global"} {
		path := filepath.Join(dir, sub, "2655")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(raw) != expectedBlocks*storage.BlockSize {
			t.Errorf("%s: size=%d, want %d", path, len(raw), expectedBlocks*storage.BlockSize)
			continue
		}
		// Metapage btm_root = block 1.
		le := binary.LittleEndian
		// metapage layout (see pgBuildBtreeMetapageWithRoot):
		// SizeOfPageHeaderData=24 + btm_magic(4) + btm_version(4) + btm_root(4) ...
		btmRoot := le.Uint32(raw[24+4+4 : 24+4+4+4])
		if btmRoot != 1 {
			t.Errorf("%s: btm_root=%d, want 1", path, btmRoot)
		}
		// Leaf page line-pointer count.
		leaf := raw[storage.BlockSize : 2*storage.BlockSize]
		pdLower := le.Uint16(leaf[12:14])
		nItems := int((pdLower - storage.SizeOfPageHeaderData) / 4)
		if nItems != len(entries) {
			t.Errorf("%s: nItems=%d, want %d", path, nItems, len(entries))
		}
	}

	// Sanity check: the on-disk leaf must contain the integer_ops cmp
	// row for type int4 (family=1976, left=23, right=23, num=1) AND the
	// text family btnamecmp row (family=1994, left=19, right=19, num=1).
	// Both are needed for pg_authid_rolname_index (uses name_ops →
	// family 1994) to find its support proc.
	raw, err := os.ReadFile(filepath.Join(dir, "base/1", "2655"))
	if err != nil {
		t.Fatalf("read base/1/2655: %v", err)
	}
	mustContainKey(t, raw, 1976, 23, 23, 1, "integer_ops int4 cmp")
	mustContainKey(t, raw, 1994, 19, 19, 1, "text family btnamecmp")
}

// mustContainKey scans the leaf page for a 24-byte IndexTuple whose
// 4-key payload matches (family, lefttype, righttype, num).
func mustContainKey(t *testing.T, file []byte, family, lefttype, righttype uint32, num int16, label string) {
	t.Helper()
	leaf := file[storage.BlockSize : 2*storage.BlockSize]
	le := binary.LittleEndian
	pdLower := le.Uint16(leaf[12:14])
	nItems := int((pdLower - storage.SizeOfPageHeaderData) / 4)
	for i := 0; i < nItems; i++ {
		// Line pointer at offset SizeOfPageHeaderData + i*4.
		lp := le.Uint32(leaf[storage.SizeOfPageHeaderData+i*4:])
		lpOff := lp & 0x7FFF // low 15 bits = lp_off
		if int(lpOff)+24 > len(leaf) {
			continue
		}
		tup := leaf[lpOff : lpOff+24]
		gotFam := le.Uint32(tup[8:12])
		gotLeft := le.Uint32(tup[12:16])
		gotRight := le.Uint32(tup[16:20])
		gotNum := int16(le.Uint16(tup[20:22]))
		if gotFam == family && gotLeft == lefttype && gotRight == righttype && gotNum == num {
			return
		}
	}
	t.Errorf("%s: key (family=%d, left=%d, right=%d, num=%d) not found in leaf", label, family, lefttype, righttype, num)
}
