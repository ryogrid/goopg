package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPgBuildIndexTupleNameKeyLayoutMatchesPG18 pins the byte layout PG18
// produces for `index_form_tuple` on a single fixed-width NAME column with
// no nulls (e.g. pg_authid_rolname_index).
//
// IndexTuple header is 8 bytes (ItemPointerData + t_info), followed by a
// NAMEDATALEN=64 zero-padded NameData blob. Total = 72, already MAXALIGN'd
// for 8-byte alignment so no trailing pad is required.
//
// Block ID encoding split into bi_hi/bi_lo halves is verified with a
// deliberately byte-asymmetric block number (0xDEADBEEF) so a regression
// to the LE-uint32 trap diagnosed in Step 3s is caught loudly.
func TestPgBuildIndexTupleNameKeyLayoutMatchesPG18(t *testing.T) {
	const (
		heapBlk        = uint32(0xDEADBEEF)
		heapOff uint16 = 0x1234
	)
	const name = "ryo"
	got := pgBuildIndexTupleNameKey(heapBlk, heapOff, name)
	if len(got) != 72 {
		t.Fatalf("tuple size=%d, want 72", len(got))
	}
	le := binary.LittleEndian
	if biHi := le.Uint16(got[0:2]); biHi != uint16(heapBlk>>16) {
		t.Errorf("bi_hi=0x%04x, want 0x%04x", biHi, uint16(heapBlk>>16))
	}
	if biLo := le.Uint16(got[2:4]); biLo != uint16(heapBlk&0xFFFF) {
		t.Errorf("bi_lo=0x%04x, want 0x%04x", biLo, uint16(heapBlk&0xFFFF))
	}
	if blk := (uint32(le.Uint16(got[0:2])) << 16) | uint32(le.Uint16(got[2:4])); blk != heapBlk {
		t.Errorf("blk round-trip=0x%08x, want 0x%08x", blk, heapBlk)
	}
	if off := le.Uint16(got[4:6]); off != heapOff {
		t.Errorf("ip_posid=0x%04x, want 0x%04x", off, heapOff)
	}
	if info := le.Uint16(got[6:8]); info != 72 {
		t.Errorf("t_info=0x%04x, want 0x0048", info)
	}
	// NameData[0..len(name)] = name bytes; tail zero-padded.
	if string(got[8:8+len(name)]) != name {
		t.Errorf("name prefix=%q, want %q", string(got[8:8+len(name)]), name)
	}
	for i := 8 + len(name); i < 72; i++ {
		if got[i] != 0 {
			t.Errorf("NameData[%d]=0x%02x, want zero", i, got[i])
		}
	}
}

// TestPgBuildIndexTupleNameKeyTruncatesAtNamedataLen ensures that
// overly long names are silently truncated to NAMEDATALEN-1 chars (no
// terminator stored, since PG's NameData is zero-terminated only when
// strictly shorter than NAMEDATALEN). At exactly NAMEDATALEN bytes the
// trailing zero terminator is dropped — matching `namestrcpy` semantics.
func TestPgBuildIndexTupleNameKeyTruncatesAtNamedataLen(t *testing.T) {
	long := make([]byte, 80)
	for i := range long {
		long[i] = 'a'
	}
	got := pgBuildIndexTupleNameKey(0, 1, string(long))
	if len(got) != 72 {
		t.Fatalf("tuple size=%d, want 72", len(got))
	}
	for i := 8; i < 72; i++ {
		if got[i] != 'a' {
			t.Errorf("NameData[%d]=0x%02x, want 'a' (truncation should fill all 64 bytes)", i, got[i])
		}
	}
}

// TestBootstrapPgAuthidIndexesWritesPopulatedBtrees verifies that both
// pg_authid_oid_index (2677) and pg_authid_rolname_index (2676) land at
// global/<oid> as 2-page btrees (metapage + leaf-root), with btm_root=1
// and the expected line-pointer count.
//
// The rolname index must contain a leaf entry for the OS-user role —
// that is the exact lookup that triggers the FATAL `28000: role "ryo"
// does not exist` when the index is empty.
func TestBootstrapPgAuthidIndexesWritesPopulatedBtrees(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}

	entries := []pgAuthidEntry{
		{OID: 10, Rolname: "postgres", TID: heapTID{Block: 0, Offset: 1}},
		{OID: 16384, Rolname: "ryo", TID: heapTID{Block: 0, Offset: 2}},
	}
	if err := bootstrapPgAuthidIndexes(dir, entries); err != nil {
		t.Fatalf("bootstrapPgAuthidIndexes: %v", err)
	}

	for _, oid := range []uint32{2676, 2677} {
		path := filepath.Join(dir, "global", oidPath(oid))
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(raw) != 2*storage.BlockSize {
			t.Errorf("%s: size=%d, want %d", path, len(raw), 2*storage.BlockSize)
			continue
		}
		le := binary.LittleEndian
		btmRoot := le.Uint32(raw[24+4+4 : 24+4+4+4])
		if btmRoot != 1 {
			t.Errorf("%s: btm_root=%d, want 1", path, btmRoot)
		}
		leaf := raw[storage.BlockSize : 2*storage.BlockSize]
		pdLower := le.Uint16(leaf[12:14])
		nItems := int((pdLower - storage.SizeOfPageHeaderData) / 4)
		if nItems != len(entries) {
			t.Errorf("%s: nItems=%d, want %d", path, nItems, len(entries))
		}
	}

	// Verify the rolname leaf actually contains a tuple keyed on "ryo".
	raw, err := os.ReadFile(filepath.Join(dir, "global", "2676"))
	if err != nil {
		t.Fatalf("read global/2676: %v", err)
	}
	if !leafContainsName(raw, "ryo") {
		t.Error("pg_authid_rolname_index leaf does not contain a tuple keyed on \"ryo\"")
	}
	if !leafContainsName(raw, "postgres") {
		t.Error("pg_authid_rolname_index leaf does not contain a tuple keyed on \"postgres\"")
	}

	// Verify the oid leaf actually contains a tuple keyed on 16384.
	rawOid, err := os.ReadFile(filepath.Join(dir, "global", "2677"))
	if err != nil {
		t.Fatalf("read global/2677: %v", err)
	}
	if !leafContainsOid(rawOid, 16384) {
		t.Error("pg_authid_oid_index leaf does not contain a tuple keyed on 16384")
	}
	if !leafContainsOid(rawOid, 10) {
		t.Error("pg_authid_oid_index leaf does not contain a tuple keyed on 10 (BOOTSTRAP_SUPERUSERID)")
	}
}

func oidPath(oid uint32) string {
	// Tests build relfilenode paths via strconv elsewhere; this helper
	// keeps the test focused on the wire layout.
	switch oid {
	case 2676:
		return "2676"
	case 2677:
		return "2677"
	default:
		return ""
	}
}

func leafContainsName(file []byte, name string) bool {
	leaf := file[storage.BlockSize : 2*storage.BlockSize]
	le := binary.LittleEndian
	pdLower := le.Uint16(leaf[12:14])
	nItems := int((pdLower - storage.SizeOfPageHeaderData) / 4)
	for i := 0; i < nItems; i++ {
		lp := le.Uint32(leaf[storage.SizeOfPageHeaderData+i*4:])
		lpOff := lp & 0x7FFF
		if int(lpOff)+72 > len(leaf) {
			continue
		}
		// NameData starts at +8 within the IndexTuple.
		nd := leaf[int(lpOff)+8 : int(lpOff)+72]
		// Compare up to first NUL or NAMEDATALEN.
		n := 0
		for n < len(nd) && nd[n] != 0 {
			n++
		}
		if string(nd[:n]) == name {
			return true
		}
	}
	return false
}

func leafContainsOid(file []byte, oid uint32) bool {
	leaf := file[storage.BlockSize : 2*storage.BlockSize]
	le := binary.LittleEndian
	pdLower := le.Uint16(leaf[12:14])
	nItems := int((pdLower - storage.SizeOfPageHeaderData) / 4)
	for i := 0; i < nItems; i++ {
		lp := le.Uint32(leaf[storage.SizeOfPageHeaderData+i*4:])
		lpOff := lp & 0x7FFF
		if int(lpOff)+16 > len(leaf) {
			continue
		}
		if le.Uint32(leaf[int(lpOff)+8:int(lpOff)+12]) == oid {
			return true
		}
	}
	return false
}
