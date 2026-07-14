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

// TestBufmapInsertSkipsPastTombstoneToExistingKey is a regression test for
// the M-NIGHTLY AI-20260708-064334-001 root cause (see Insert's doc
// comment). Insert used to stop at the FIRST tombstone or empty bucket in
// the probe chain and claim it immediately, without checking whether the
// target key already had a LIVE entry further along the same chain, past
// that tombstone -- contradicting Lookup's/Delete's own invariant that
// tombstones do not terminate probing. That let two different slots
// simultaneously "own" the same BufferTag, which two independent slots
// then raced a disk reload against a flush of the same block, permanently
// discarding a write (found via live per-call bufmap instrumentation in
// TestVerifyBtreeEngineSilentOnRealConcurrentContended after 13 prior
// investigation loops).
func TestBufmapInsertSkipsPastTombstoneToExistingKey(t *testing.T) {
	m := newBufmap(4) // next pow2 >= 2*4 => 8 buckets
	mask := m.inner.Load().mask

	tagA := BufferTag{Rel: RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}, Block: 0}
	hA := bufTagHash(tagA) & mask

	// Find tagB whose starting probe bucket collides with tagA's.
	var tagB BufferTag
	found := false
	for blk := BlockNumber(1); blk < 100000; blk++ {
		cand := BufferTag{Rel: RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}, Block: blk}
		if bufTagHash(cand)&mask == hA {
			tagB = cand
			found = true
			break
		}
	}
	if !found {
		t.Fatal("could not find a tag colliding with tagA's starting bucket")
	}

	// tagA occupies bucket hA. tagB collides on the same starting bucket
	// and, in this fresh table, probes forward to the next (empty) bucket.
	if ok := m.Insert(tagA, 0, 1); !ok {
		t.Fatal("Insert(tagA) failed")
	}
	if ok := m.Insert(tagB, 1, 1); !ok {
		t.Fatal("Insert(tagB) failed")
	}
	// Delete tagA: its bucket (hA) becomes a tombstone. tagB's live entry
	// still sits further along the SAME probe chain, past that tombstone.
	m.Delete(tagA, 0)

	// A second Insert of tagB (e.g. a concurrent pinLoad/PinNew racing to
	// publish a tag that's already live) MUST fail: tagB is still present.
	// The buggy version incorrectly returned true, reusing hA's fresh
	// tombstone without ever checking further, creating two simultaneous
	// live entries for the same tag.
	if ok := m.Insert(tagB, 2, 1); ok {
		t.Fatalf("Insert(tagB) a second time succeeded -- bufmap now holds two live entries for the same tag (double-mapping)")
	}

	// tagB must still resolve to its original slot, unaffected.
	idx, gen := m.Lookup(tagB)
	if idx != 1 || gen != 1 {
		t.Errorf("Lookup(tagB) = (%d,%d), want (1,1)", idx, gen)
	}
}
