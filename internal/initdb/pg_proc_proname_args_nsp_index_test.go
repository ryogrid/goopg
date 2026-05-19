package initdb

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapPgProcPronameArgsNspIndexWritesPopulatedBtree pins the
// on-disk shape of bootstrapPgProcPronameArgsNspIndex for OID 2691.
//
//   - File exists at base/1/2691, base/5/2691, global/2691.
//   - Length is a positive multiple of storage.BlockSize.
//   - Multi-leaf (size > 2 blocks) because the seed has ~3400 entries
//     averaging ~104 B/tuple = ~75 entries per leaf = ~50 leaves.
//   - Metapage carries BTREE_MAGIC and btm_root > 0.
//   - The 64-byte NameData payload "count" appears in some leaf — this is
//     the exact byte sequence PG18's SearchSysCacheList1 binary-searches
//     when parse-analysing `SELECT count(*) ...`.
func TestBootstrapPgProcPronameArgsNspIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"base/1", "base/5", "global"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	tids, err := bootstrapPgProcTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgProcTuples: %v", err)
	}
	if err := bootstrapPgProcPronameArgsNspIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgProcPronameArgsNspIndex: %v", err)
	}

	// NameData("count") = 'c','o','u','n','t' + 59 NUL bytes.
	wantName := make([]byte, 64)
	copy(wantName, "count")

	for _, base := range []string{"base/1", "base/5", "global"} {
		path := filepath.Join(dir, base, "2691")
		buf, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(buf) == 0 || len(buf)%storage.BlockSize != 0 {
			t.Fatalf("%s: size=%d, want non-zero multiple of %d", path, len(buf), storage.BlockSize)
		}
		if len(buf) <= 2*storage.BlockSize {
			t.Fatalf("%s: size=%d, want >%d (multi-leaf expected for ~3400 entries)",
				path, len(buf), 2*storage.BlockSize)
		}

		le := binary.LittleEndian
		const btreeMagic = 0x053162
		magic := le.Uint32(buf[24:28])
		if magic != btreeMagic {
			t.Errorf("%s: metapage magic=0x%x, want 0x%x", path, magic, btreeMagic)
		}
		root := le.Uint32(buf[28:32])
		if root == 0 {
			t.Errorf("%s: metapage btm_root=0, want >0", path)
		}

		if !bytes.Contains(buf, wantName) {
			t.Errorf("%s: pg_proc_proname_args_nsp_index missing NameData(\"count\") payload", path)
		}
	}
}

// TestPgBuildIndexTupleProcKeyLayout pins the byte layout of the
// IndexTuple for count(*) (OID 2803): empty proargtypes, pronamespace=11.
// Tuple size = 8 (header) + 64 (NameData) + 24 (empty oidvector) + 4
// (oid) = 100, MAXALIGN'd to 104.
func TestPgBuildIndexTupleProcKeyLayout(t *testing.T) {
	// Heap location chosen arbitrarily — only the encoded layout matters.
	const (
		heapBlk uint32 = 1
		heapOff uint16 = 7
		nsp     uint32 = 11
	)
	tup := pgBuildIndexTupleProcKey(heapBlk, heapOff, "count", nil, nsp)
	if len(tup) != 104 {
		t.Fatalf("tuple size=%d, want 104 (8+64+24+4 MAXALIGN'd)", len(tup))
	}

	le := binary.LittleEndian
	if le.Uint16(tup[0:2]) != 0 {
		t.Errorf("bi_hi=%d, want 0", le.Uint16(tup[0:2]))
	}
	if le.Uint16(tup[2:4]) != 1 {
		t.Errorf("bi_lo=%d, want 1", le.Uint16(tup[2:4]))
	}
	if le.Uint16(tup[4:6]) != heapOff {
		t.Errorf("ip_posid=%d, want %d", le.Uint16(tup[4:6]), heapOff)
	}
	// t_info: size 104 in low 13 bits + INDEX_VAR_MASK (0x4000).
	tinfo := le.Uint16(tup[6:8])
	wantTinfo := uint16(104) | indexVarMask
	if tinfo != wantTinfo {
		t.Errorf("t_info=0x%04x, want 0x%04x", tinfo, wantTinfo)
	}

	wantName := make([]byte, 64)
	copy(wantName, "count")
	if !bytes.Equal(tup[8:72], wantName) {
		t.Errorf("proname=%q, want %q", tup[8:72], wantName)
	}

	// Empty oidvector header: vl_len_ = 24<<2, ndim=1, dataoffset=0,
	// elemtype=26 (OIDOID), dim1=0, lbound1=0.
	if le.Uint32(tup[72:76]) != 24<<2 {
		t.Errorf("oidvector vl_len_=0x%x, want 0x%x", le.Uint32(tup[72:76]), 24<<2)
	}
	if le.Uint32(tup[76:80]) != 1 {
		t.Errorf("oidvector ndim=%d, want 1", le.Uint32(tup[76:80]))
	}
	if le.Uint32(tup[84:88]) != 26 {
		t.Errorf("oidvector elemtype=%d, want 26 (OIDOID)", le.Uint32(tup[84:88]))
	}
	if le.Uint32(tup[88:92]) != 0 {
		t.Errorf("oidvector dim1=%d, want 0 (empty)", le.Uint32(tup[88:92]))
	}

	// pronamespace at offset 96.
	if le.Uint32(tup[96:100]) != nsp {
		t.Errorf("pronamespace=%d, want %d", le.Uint32(tup[96:100]), nsp)
	}
	// MAXALIGN tail [100..104] is zero pad.
	if le.Uint32(tup[100:104]) != 0 {
		t.Errorf("MAXALIGN pad non-zero: 0x%x", le.Uint32(tup[100:104]))
	}
}

// TestPgEncodeOidvectorForIndexEmpty pins the empty oidvector binary —
// 24 header bytes, no values, matching PG18 buildoidvector(NULL, 0).
func TestPgEncodeOidvectorForIndexEmpty(t *testing.T) {
	buf := pgEncodeOidvectorForIndex(nil)
	if len(buf) != 24 {
		t.Fatalf("empty oidvector size=%d, want 24", len(buf))
	}
	le := binary.LittleEndian
	if got := le.Uint32(buf[0:4]); got != 24<<2 {
		t.Errorf("vl_len_=0x%x, want 0x%x", got, 24<<2)
	}
	if got := le.Uint32(buf[16:20]); got != 0 {
		t.Errorf("dim1=%d, want 0", got)
	}
}

// TestPgEncodeOidvectorForIndexOneElement covers count("any") (proargtypes
// = [2276]). Size grows to 28 bytes; values[0] == 2276 (ANYOID).
func TestPgEncodeOidvectorForIndexOneElement(t *testing.T) {
	buf := pgEncodeOidvectorForIndex([]uint32{2276})
	if len(buf) != 28 {
		t.Fatalf("oidvector(1) size=%d, want 28", len(buf))
	}
	le := binary.LittleEndian
	if got := le.Uint32(buf[0:4]); got != 28<<2 {
		t.Errorf("vl_len_=0x%x, want 0x%x", got, 28<<2)
	}
	if got := le.Uint32(buf[16:20]); got != 1 {
		t.Errorf("dim1=%d, want 1", got)
	}
	if got := le.Uint32(buf[24:28]); got != 2276 {
		t.Errorf("values[0]=%d, want 2276 (ANYOID)", got)
	}
}
