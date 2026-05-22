package storage

import "testing"

// TestBufmapPackUnpackRoundtrip verifies that every (slotIdx, gen)
// pair packs to a value distinct from the bufmapEmpty (0) and
// bufmapTombstone (1) sentinels, and unpacks back to the original
// values. Regression for a bug where packVal((slotIdx=0, gen=1))
// produced 1 (== bufmapTombstone), and the legacy `val |= 2`
// workaround corrupted gen to 3 — causing pinSlow's Lookup/state
// gen-comparison to mismatch forever on the very first slot.
func TestBufmapPackUnpackRoundtrip(t *testing.T) {
	cases := []struct {
		slotIdx int32
		gen     uint32
	}{
		{0, 0},
		{0, 1},
		{0, 0x7fff},
		{1, 0},
		{1, 1},
		{63, 1},
		{63, 0x7fff},
	}
	for _, c := range cases {
		v := packVal(c.slotIdx, c.gen)
		if v == bufmapEmpty || v == bufmapTombstone {
			t.Errorf("packVal(%d,%d)=%d collides with sentinel", c.slotIdx, c.gen, v)
		}
		gotIdx, gotGen := unpackVal(v)
		if gotIdx != c.slotIdx || gotGen != c.gen {
			t.Errorf("unpackVal(packVal(%d,%d))=(%d,%d)", c.slotIdx, c.gen, gotIdx, gotGen)
		}
	}
}

// TestBufmapInsertLookupSlotZeroGenOne pins the exact regression
// scenario via the bufmap API: insert (slotIdx=0, gen=1) and verify
// Lookup returns the same values rather than (0, 3) or "not found".
func TestBufmapInsertLookupSlotZeroGenOne(t *testing.T) {
	m := newBufmap(8)
	tag := BufferTag{Rel: RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}, Block: 0}
	if ok := m.Insert(tag, 0, 1); !ok {
		t.Fatal("Insert(slot=0, gen=1) failed")
	}
	idx, gen := m.Lookup(tag)
	if idx != 0 || gen != 1 {
		t.Errorf("Lookup after Insert(0,1) = (%d,%d), want (0,1)", idx, gen)
	}
}
