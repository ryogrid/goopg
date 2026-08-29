package storage

import "testing"

// TestHeapPageSizeConstantsMatchPG pins the derived page-geometry constants
// against the values PostgreSQL 18.3 computes for an 8 KiB BLCKSZ. They are
// spelled out arithmetically in heap.go, so a typo there would silently move
// every fillfactor decision; these are the numbers the oracle uses.
//
//	MaxHeapTupleSize     = 8192 - MAXALIGN(24) - MAXALIGN(4)      = 8160
//	MaxHeapTuplesPerPage = (8192 - 24) / (MAXALIGN(23) + 4)       = 291
//	nearlyEmptyFreeSpace = 8160 - (291 / 8) * 4                   = 8016
func TestHeapPageSizeConstantsMatchPG(t *testing.T) {
	if MaxHeapTupleSize != 8160 {
		t.Errorf("MaxHeapTupleSize = %d, want 8160 (PG htup_details.h)", MaxHeapTupleSize)
	}
	if MaxHeapTuplesPerPage != 291 {
		t.Errorf("MaxHeapTuplesPerPage = %d, want 291 (PG htup_details.h)", MaxHeapTuplesPerPage)
	}
	if nearlyEmptyFreeSpace != 8016 {
		t.Errorf("nearlyEmptyFreeSpace = %d, want 8016 (PG hio.c:546)", nearlyEmptyFreeSpace)
	}
	if HeapDefaultFillfactor != 100 {
		t.Errorf("HeapDefaultFillfactor = %d, want 100 (PG rel.h:360)", HeapDefaultFillfactor)
	}
}

// TestHeapInsertTargetFreeSpace pins the targetFreeSpace arithmetic of PG's
// RelationGetBufferForTuple (hio.c:539-556).
func TestHeapInsertTargetFreeSpace(t *testing.T) {
	cases := []struct {
		name       string
		tupleLen   int
		fillfactor int
		want       int
	}{
		// fillfactor 100 (and the "unset" encodings) reserve nothing, so the
		// target is just the maxaligned tuple length — byte-identical to the
		// pre-M0134-0175a "does the tuple physically fit" test.
		{"default-unset", 232, 0, 232},
		{"default-100", 232, 100, 232},
		{"default-maxaligns", 225, 100, 232},
		{"default-negative-is-unset", 232, -1, 232},

		// The regress fixture: `CREATE TABLE test_tablesample (id int, name
		// text) WITH (fillfactor=10)` holding `repeat(i::text, 200)` rows.
		// The tuple is 23-byte header -> t_hoff 24, + int4 = 28, + a 4-byte
		// varlena header and 200 chars = 232, already maxaligned. The reserve
		// is 8192*90/100 = 7372, so the target is 7604 — which fits three
		// tuples on a page and rejects the fourth, exactly PG's four-block
		// layout for ten rows.
		{"fixture-ff10", 232, 10, 232 + 7372},

		{"ff50", 232, 50, 232 + 4096},
		{"ff90", 232, 90, 232 + 819},

		// nearlyEmptyFreeSpace clamp: a 4 KiB tuple in a fillfactor=10 table
		// would nominally demand 4096+7372 = 11468 bytes of free space, more
		// than a page holds. Upstream clamps to Max(len, nearlyEmpty) so a
		// freshly extended page always satisfies the target; without this the
		// relation would extend forever.
		{"clamp-to-nearly-empty", 4096, 10, 8016},
		// Past the clamp point the tuple length itself wins, so an
		// almost-page-sized tuple is still accepted rather than looping.
		{"clamp-tuple-larger-than-nearly-empty", 8120, 10, 8120},
	}
	for _, c := range cases {
		if got := HeapInsertTargetFreeSpace(c.tupleLen, c.fillfactor); got != c.want {
			t.Errorf("%s: HeapInsertTargetFreeSpace(%d, %d) = %d, want %d",
				c.name, c.tupleLen, c.fillfactor, got, c.want)
		}
	}
}

// TestPageGetHeapFreeSpaceTracksAdds checks that PageGetHeapFreeSpace stays
// exactly comparable to a maxaligned tuple length as tuples are added: the
// value it reports is the largest tuple PageAddHeapTuple would still accept.
// The two must agree or the fillfactor gate would reject pages the allocator
// would happily have used (or vice versa).
func TestPageGetHeapFreeSpaceTracksAdds(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	if got, want := PageGetHeapFreeSpace(p), BlockSize-SizeOfPageHeaderData-itemIDSize; got != want {
		t.Fatalf("empty page free space = %d, want %d", got, want)
	}

	// Body sized so the marshalled tuple is the fixture's 232 bytes:
	// SizeOfHeapTupleHeaderData maxaligns to 24, leaving 208 of payload.
	body := make([]byte, 232-DefaultHeapTupleHoff)
	for i := 0; i < 3; i++ {
		before := PageGetHeapFreeSpace(p)
		tup := NewHeapTuple(TransactionID(100), InvalidTransactionID, body)
		raw, err := tup.MarshalBinary()
		if err != nil {
			t.Fatalf("MarshalBinary: %v", err)
		}
		if len(raw) != 232 {
			t.Fatalf("fixture tuple is %d bytes, want 232", len(raw))
		}
		if before < len(raw) {
			t.Fatalf("add %d: free space %d < tuple %d", i, before, len(raw))
		}
		if _, err := PageAddHeapTuple(p, tup); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
		// One tuple plus one line pointer come off the reported free space.
		if got, want := PageGetHeapFreeSpace(p), before-232-itemIDSize; got != want {
			t.Fatalf("add %d: free space = %d, want %d", i, got, want)
		}
	}

	// Three of these tuples fit under fillfactor=10 and the fourth does not —
	// the property that produces PG's 3/3/3/1 block layout for the regress
	// fixture's ten rows.
	target := HeapInsertTargetFreeSpace(232, 10)
	if PageGetHeapFreeSpace(p) >= target {
		t.Errorf("page still has %d free after 3 tuples; fillfactor=10 target %d should already exclude it",
			PageGetHeapFreeSpace(p), target)
	}
	// ...while the default fillfactor keeps accepting them.
	if PageGetHeapFreeSpace(p) < HeapInsertTargetFreeSpace(232, 100) {
		t.Errorf("page should still accept a 4th tuple at fillfactor=100, free=%d",
			PageGetHeapFreeSpace(p))
	}
}
