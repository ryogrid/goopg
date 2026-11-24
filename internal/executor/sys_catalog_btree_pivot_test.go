package executor

import (
	"encoding/binary"
	"testing"
)

// TestPivotAwareCmpTruncatedPivot: a PG suffix-truncated pivot (t_info
// INDEX_ALT_TID_MASK, attribute count in the t_tid offset) compares on its
// present attributes only; a key equal on them sorts after it, and a
// zero-attribute pivot is minus infinity. The full-key comparator alone
// sliced past the short pivot and panicked. M0149-0016.
func TestPivotAwareCmpTruncatedPivot(t *testing.T) {
	// pg_attribute_relid_attnum_index: (oid, int2).
	key := make([]byte, 8)
	binary.LittleEndian.PutUint32(key[0:4], 500)
	binary.LittleEndian.PutUint16(key[4:6], 3)
	pivot := func(relid uint32, natts uint16) []byte {
		it := make([]byte, sysIndexTupleHoff+8)
		binary.LittleEndian.PutUint16(it[4:6], natts)
		binary.LittleEndian.PutUint16(it[6:8], uint16(len(it))|indexAltTIDMask)
		binary.LittleEndian.PutUint32(it[sysIndexTupleHoff:], relid)
		return it[:sysIndexTupleHoff+4] // only the present attribute's bytes
	}
	cmp := pivotAwareCmp(pgAttributeRelidAttnumIndexOID, cmpKeyOidInt2)
	if c := cmp(key, pivot(500, 1)); c <= 0 {
		t.Fatalf("key equal on the present attribute must sort after the pivot, got %d", c)
	}
	if c := cmp(key, pivot(600, 1)); c >= 0 {
		t.Fatalf("key below the pivot's relid must sort before it, got %d", c)
	}
	if c := cmp(key, pivot(400, 1)); c <= 0 {
		t.Fatalf("key above the pivot's relid must sort after it, got %d", c)
	}
	if c := cmp(key, pivot(9999, 0)); c <= 0 {
		t.Fatalf("a zero-attribute pivot is minus infinity, got %d", c)
	}
	// A full (non-truncated) item compares as before.
	full := make([]byte, sysIndexTupleHoff+8)
	binary.LittleEndian.PutUint16(full[6:8], uint16(len(full)))
	binary.LittleEndian.PutUint32(full[sysIndexTupleHoff:], 500)
	binary.LittleEndian.PutUint16(full[sysIndexTupleHoff+4:], 3)
	if c := cmp(key, full); c != 0 {
		t.Fatalf("equal full key compares %d, want 0", c)
	}
}
