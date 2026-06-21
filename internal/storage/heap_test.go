package storage

import (
	"errors"
	"testing"
)

func TestHeapTupleMarshalParseRoundTrip(t *testing.T) {
	orig := NewHeapTuple(TransactionID(10), TransactionID(20), []byte("hello"))
	orig.Header.Xvac = TransactionID(30)
	orig.Header.CTID = ItemPointer{Block: 7, Offset: 3}
	orig.Header.Infomask = 0x0021
	orig.Header.Infomask2 = 0x0003

	raw, err := orig.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseHeapTuple(raw)
	if err != nil {
		t.Fatal(err)
	}

	if got.Header.Xmin != orig.Header.Xmin {
		t.Fatalf("xmin=%d want=%d", got.Header.Xmin, orig.Header.Xmin)
	}
	if got.Header.Xmax != orig.Header.Xmax {
		t.Fatalf("xmax=%d want=%d", got.Header.Xmax, orig.Header.Xmax)
	}
	if got.Header.Xvac != orig.Header.Xvac {
		t.Fatalf("xvac=%d want=%d", got.Header.Xvac, orig.Header.Xvac)
	}
	if got.Header.CTID != orig.Header.CTID {
		t.Fatalf("ctid=%+v want=%+v", got.Header.CTID, orig.Header.CTID)
	}
	if got.Header.Infomask != orig.Header.Infomask {
		t.Fatalf("infomask=%#x want=%#x", got.Header.Infomask, orig.Header.Infomask)
	}
	if got.Header.Infomask2 != orig.Header.Infomask2 {
		t.Fatalf("infomask2=%#x want=%#x", got.Header.Infomask2, orig.Header.Infomask2)
	}
	if string(got.Data) != "hello" {
		t.Fatalf("data=%q want=%q", string(got.Data), "hello")
	}
}

func TestPageAddGetHeapTuple(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}

	tuple := NewHeapTuple(TransactionID(101), InvalidTransactionID, []byte("abc"))
	slot, err := PageAddHeapTuple(p, tuple)
	if err != nil {
		t.Fatal(err)
	}
	if slot != 1 {
		t.Fatalf("slot=%d want=1", slot)
	}

	count, err := PageLinePointerCount(p)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("line pointer count=%d want=1", count)
	}

	got, err := PageGetHeapTuple(p, slot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.Xmin != TransactionID(101) {
		t.Fatalf("xmin=%d want=101", got.Header.Xmin)
	}
	if got.Header.Xmax != InvalidTransactionID {
		t.Fatalf("xmax=%d want=0", got.Header.Xmax)
	}
	if string(got.Data) != "abc" {
		t.Fatalf("data=%q want=abc", string(got.Data))
	}
}

func TestPageAddHeapTupleNoSpace(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}

	payload := make([]byte, 700)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	for {
		_, err := PageAddHeapTuple(p, NewHeapTuple(TransactionID(1), InvalidTransactionID, payload))
		if err == nil {
			continue
		}
		if !errors.Is(err, ErrNoSpaceInPage) {
			t.Fatalf("unexpected err=%v", err)
		}
		break
	}
}

func TestPageGetHeapTupleInvalidSlot(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	if _, err := PageGetHeapTuple(p, 1); !errors.Is(err, ErrInvalidSlot) {
		t.Fatalf("err=%v want ErrInvalidSlot", err)
	}
}

// TestPageSetHeapTupleLockOnly pins the storage primitive added
// for tuple-level pessimistic locking (M0021 follow-up step 1):
// stamping a lock-only xmax sets the xmax field, sets the
// HEAP_XMAX_LOCK_ONLY infomask bit, sets the chosen lock-strength
// bit, and clears the HEAP_XMAX_INVALID bit so future readers
// honour the xmax value as a real (lock-only) holder.
func TestPageSetHeapTupleLockOnly(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	tuple := NewHeapTuple(TransactionID(100), InvalidTransactionID, []byte("locked"))
	tuple.Header.Infomask = HeapXmaxInvalid // start with the canonical "no xmax" hint
	slot, err := PageAddHeapTuple(p, tuple)
	if err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleLockOnly(p, slot, TransactionID(42), HeapXmaxExclLock); err != nil {
		t.Fatal(err)
	}
	got, err := PageGetHeapTuple(p, slot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.Xmax != TransactionID(42) {
		t.Errorf("Xmax = %d, want 42", got.Header.Xmax)
	}
	if got.Header.Infomask&HeapXmaxLockOnly == 0 {
		t.Errorf("Infomask = %#x, want HeapXmaxLockOnly bit set", got.Header.Infomask)
	}
	if got.Header.Infomask&HeapXmaxExclLock == 0 {
		t.Errorf("Infomask = %#x, want HeapXmaxExclLock bit set", got.Header.Infomask)
	}
	if got.Header.Infomask&HeapXmaxInvalid != 0 {
		t.Errorf("Infomask = %#x, HeapXmaxInvalid should have been cleared", got.Header.Infomask)
	}
	if !IsHeapTupleLockOnly(got.Header.Infomask) {
		t.Errorf("IsHeapTupleLockOnly(%#x) = false, want true", got.Header.Infomask)
	}
}

// TestPageSetHeapTupleLockOnlyClearsStaleStrength — re-stamping a
// lock-only xmax with a different strength clears prior
// strength bits (KeyShr → Excl, Excl → Shr) instead of OR-ing
// them. Without this, a tuple could end up with both ExclLock
// and KeyShrLock bits set and a future predicate
// `(infomask & HeapXmaxLockMask) == HeapXmaxExclLock` would
// false-negative.
func TestPageSetHeapTupleLockOnlyClearsStaleStrength(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	tuple := NewHeapTuple(TransactionID(100), InvalidTransactionID, []byte("v"))
	slot, err := PageAddHeapTuple(p, tuple)
	if err != nil {
		t.Fatal(err)
	}
	// Stamp ExclLock first.
	if err := PageSetHeapTupleLockOnly(p, slot, TransactionID(7), HeapXmaxExclLock); err != nil {
		t.Fatal(err)
	}
	// Re-stamp with KeyShrLock. The Excl bit must be cleared.
	if err := PageSetHeapTupleLockOnly(p, slot, TransactionID(8), HeapXmaxKeyShrLock); err != nil {
		t.Fatal(err)
	}
	got, _ := PageGetHeapTuple(p, slot)
	if got.Header.Infomask&HeapXmaxExclLock != 0 {
		t.Errorf("Infomask = %#x, ExclLock should have been cleared", got.Header.Infomask)
	}
	if got.Header.Infomask&HeapXmaxKeyShrLock == 0 {
		t.Errorf("Infomask = %#x, KeyShrLock bit not set", got.Header.Infomask)
	}
}

// TestPageSetHeapTupleLockOnlyRejectsZeroStrength — guards
// against API misuse: lockStrength must include at least one
// HeapXmaxLockMask bit, otherwise the resulting infomask would
// have HeapXmaxLockOnly set but no strength, which is the
// "lock-only with unknown mode" corruption upstream's
// HeapTupleHeaderGetCmax / cleanup paths can't interpret.
func TestPageSetHeapTupleLockOnlyRejectsZeroStrength(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	tuple := NewHeapTuple(TransactionID(100), InvalidTransactionID, []byte("v"))
	slot, err := PageAddHeapTuple(p, tuple)
	if err != nil {
		t.Fatal(err)
	}
	err = PageSetHeapTupleLockOnly(p, slot, TransactionID(7), 0)
	if err == nil {
		t.Error("expected error on zero lockStrength")
	}
}

// TestPageSetHeapTupleLockOnlyInvalidSlot — out-of-range slot
// numbers fall through to ErrInvalidSlot like the sibling
// PageSetHeapTupleXmax helper.
func TestPageSetHeapTupleLockOnlyInvalidSlot(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleLockOnly(p, 0, TransactionID(7), HeapXmaxExclLock); !errors.Is(err, ErrInvalidSlot) {
		t.Errorf("slot 0 err = %v, want ErrInvalidSlot", err)
	}
	if err := PageSetHeapTupleLockOnly(p, 99, TransactionID(7), HeapXmaxExclLock); !errors.Is(err, ErrInvalidSlot) {
		t.Errorf("slot 99 err = %v, want ErrInvalidSlot", err)
	}
}

// TestPageSetHeapTupleLockKeysUpdated round-trips the HEAP_KEYS_UPDATED
// infomask2 bit the row-lock producer sets for a FOR UPDATE lock (and clears
// for the weaker strengths so a stale bit from a prior FOR UPDATE lock on the
// same line pointer can't mis-decode a later FOR NO KEY UPDATE holder as FOR
// UPDATE). M0118-0003.
func TestPageSetHeapTupleLockKeysUpdated(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	tuple := NewHeapTuple(TransactionID(100), InvalidTransactionID, []byte("v"))
	slot, err := PageAddHeapTuple(p, tuple)
	if err != nil {
		t.Fatal(err)
	}
	// FOR UPDATE: lock-only ExclLock + KEYS_UPDATED.
	if err := PageSetHeapTupleLockOnly(p, slot, TransactionID(7), HeapXmaxExclLock); err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleLockKeysUpdated(p, slot, true); err != nil {
		t.Fatal(err)
	}
	got, _ := PageGetHeapTuple(p, slot)
	if got.Header.Infomask2&HeapKeysUpdated == 0 {
		t.Errorf("Infomask2 = %#x, KEYS_UPDATED should be set for FOR UPDATE", got.Header.Infomask2)
	}
	// Re-lock FOR NO KEY UPDATE on the same line pointer: the stale bit must clear.
	if err := PageSetHeapTupleLockOnly(p, slot, TransactionID(8), HeapXmaxExclLock); err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleLockKeysUpdated(p, slot, false); err != nil {
		t.Fatal(err)
	}
	got, _ = PageGetHeapTuple(p, slot)
	if got.Header.Infomask2&HeapKeysUpdated != 0 {
		t.Errorf("Infomask2 = %#x, KEYS_UPDATED should have cleared for FOR NO KEY UPDATE", got.Header.Infomask2)
	}
	// Invalid slots behave like the sibling helpers.
	if err := PageSetHeapTupleLockKeysUpdated(p, 0, true); !errors.Is(err, ErrInvalidSlot) {
		t.Errorf("slot 0 err = %v, want ErrInvalidSlot", err)
	}
}

// TestPageSetHeapTupleMovedPartition pins the storage primitive added
// for cross-partition UPDATE (M0100-0005n): stamping the
// moved-to-another-partition sentinel sets xmax, writes
// (InvalidBlockNumber, MovedPartitionsOffsetNumber) into t_ctid, and
// clears any HEAP_XMAX_LOCK_ONLY / lock-strength bits a prior
// SELECT FOR UPDATE may have stamped.  EPQ retries that observe this
// sentinel after the writer commits must raise the upstream
// `tuple to be locked was already moved to another partition due to
// concurrent update` error instead of silently skipping the row.
func TestPageSetHeapTupleMovedPartition(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	tuple := NewHeapTuple(TransactionID(100), InvalidTransactionID, []byte("moved"))
	// Pre-set a stale lock-only stamp; the moved-partition stamp must clear it.
	tuple.Header.Infomask = HeapXmaxLockOnly | HeapXmaxExclLock
	slot, err := PageAddHeapTuple(p, tuple)
	if err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleMovedPartition(p, slot, TransactionID(42)); err != nil {
		t.Fatal(err)
	}
	got, err := PageGetHeapTuple(p, slot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.Xmax != TransactionID(42) {
		t.Errorf("Xmax = %d, want 42", got.Header.Xmax)
	}
	if got.Header.CTID.Block != InvalidBlockNumber {
		t.Errorf("CTID.Block = %d, want InvalidBlockNumber", got.Header.CTID.Block)
	}
	if got.Header.CTID.Offset != MovedPartitionsOffsetNumber {
		t.Errorf("CTID.Offset = %#x, want %#x", got.Header.CTID.Offset, MovedPartitionsOffsetNumber)
	}
	if !IsMovedToAnotherPartition(got.Header.CTID) {
		t.Errorf("IsMovedToAnotherPartition(%+v) = false, want true", got.Header.CTID)
	}
	if got.Header.Infomask&HeapXmaxLockOnly != 0 {
		t.Errorf("Infomask = %#x, HeapXmaxLockOnly should have been cleared", got.Header.Infomask)
	}
	if got.Header.Infomask&HeapXmaxLockMask != 0 {
		t.Errorf("Infomask = %#x, lock-strength bits should have been cleared", got.Header.Infomask)
	}
}


// TestPageSetHeapTupleCtid verifies that PageSetHeapTupleCtid updates only
// the t_ctid field of an existing tuple — used by non-HOT cross-page UPDATE
// to link the old version to its successor (M0100-0005z).
func TestPageSetHeapTupleCtid(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	tuple := NewHeapTuple(TransactionID(100), TransactionID(200), []byte("old-version"))
	tuple.Header.Infomask = HeapXmaxLockOnly // pre-set; must NOT be cleared by ctid-only update.
	slot, err := PageAddHeapTuple(p, tuple)
	if err != nil {
		t.Fatal(err)
	}
	want := ItemPointer{Block: 7, Offset: 13}
	if err := PageSetHeapTupleCtid(p, slot, want); err != nil {
		t.Fatal(err)
	}
	got, err := PageGetHeapTuple(p, slot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.CTID != want {
		t.Errorf("CTID = %+v, want %+v", got.Header.CTID, want)
	}
	if got.Header.Xmin != TransactionID(100) {
		t.Errorf("Xmin = %d, want 100 (xmin must be untouched)", got.Header.Xmin)
	}
	if got.Header.Xmax != TransactionID(200) {
		t.Errorf("Xmax = %d, want 200 (xmax must be untouched)", got.Header.Xmax)
	}
	if got.Header.Infomask&HeapXmaxLockOnly == 0 {
		t.Errorf("Infomask = %#x, HeapXmaxLockOnly was cleared (must remain)", got.Header.Infomask)
	}
}

// TestPageSetHeapTupleCtidInvalidSlot verifies the helper rejects bogus slots
// rather than corrupting page state.
func TestPageSetHeapTupleCtidInvalidSlot(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleCtid(p, 0, ItemPointer{Block: 1, Offset: 1}); !errors.Is(err, ErrInvalidSlot) {
		t.Errorf("slot=0: err = %v, want ErrInvalidSlot", err)
	}
	if err := PageSetHeapTupleCtid(p, 99, ItemPointer{Block: 1, Offset: 1}); !errors.Is(err, ErrInvalidSlot) {
		t.Errorf("slot=99 (unallocated): err = %v, want ErrInvalidSlot", err)
	}
}

// TestPageSetHeapTupleMovedPartitionInvalidSlot — out-of-range slot
// numbers fall through to ErrInvalidSlot like the sibling helpers.
func TestPageSetHeapTupleMovedPartitionInvalidSlot(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleMovedPartition(p, 0, TransactionID(7)); !errors.Is(err, ErrInvalidSlot) {
		t.Errorf("slot 0 err = %v, want ErrInvalidSlot", err)
	}
	if err := PageSetHeapTupleMovedPartition(p, 99, TransactionID(7)); !errors.Is(err, ErrInvalidSlot) {
		t.Errorf("slot 99 err = %v, want ErrInvalidSlot", err)
	}
}

// TestPageSetHeapTupleXmaxClearsHeapXmaxInvalid pins the fix for the
// M0100-0005 regression: canonical-WAL inserts stamp HeapXmaxInvalid on
// fresh tuples to mark "xmax is not a deleter". PageSetHeapTupleXmax MUST
// clear that flag so isConcurrentlyUpdated returns true and the EPQ wait
// fires on concurrent DELETE/UPDATE. Without the fix, external-cluster
// isolation tests that use canonical WAL see the DELETE/UPDATE complete
// immediately instead of blocking on the in-flight xmax.
func TestPageSetHeapTupleXmaxClearsHeapXmaxInvalid(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	// Simulate a canonical-WAL insert: tuple with HeapXmaxInvalid set.
	tuple := NewHeapTuple(TransactionID(10), InvalidTransactionID, []byte("row"))
	tuple.Header.Infomask = HeapXmaxInvalid | HeapHasVarWidth
	slot, err := PageAddHeapTuple(p, tuple)
	if err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleXmax(p, slot, TransactionID(99)); err != nil {
		t.Fatal(err)
	}
	got, err := PageGetHeapTuple(p, slot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.Xmax != TransactionID(99) {
		t.Errorf("Xmax = %d, want 99", got.Header.Xmax)
	}
	if got.Header.Infomask&HeapXmaxInvalid != 0 {
		t.Errorf("Infomask = %#x: HeapXmaxInvalid should be cleared after xmax stamp", got.Header.Infomask)
	}
}

// TestPageSetHeapTupleMovedPartitionClearsHeapXmaxInvalid is the
// PageSetHeapTupleMovedPartition analogue of the above.
func TestPageSetHeapTupleMovedPartitionClearsHeapXmaxInvalid(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	tuple := NewHeapTuple(TransactionID(10), InvalidTransactionID, []byte("moved"))
	tuple.Header.Infomask = HeapXmaxInvalid | HeapHasVarWidth
	slot, err := PageAddHeapTuple(p, tuple)
	if err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleMovedPartition(p, slot, TransactionID(42)); err != nil {
		t.Fatal(err)
	}
	got, err := PageGetHeapTuple(p, slot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.Xmax != TransactionID(42) {
		t.Errorf("Xmax = %d, want 42", got.Header.Xmax)
	}
	if got.Header.Infomask&HeapXmaxInvalid != 0 {
		t.Errorf("Infomask = %#x: HeapXmaxInvalid should be cleared after moved-partition stamp", got.Header.Infomask)
	}
}

// TestIsMovedToAnotherPartitionNegatives — the predicate must reject
// "normal" CTIDs (any block≠Invalid or offset≠sentinel) so that
// regular HOT chain pointers and zeroed CTIDs aren't misread as
// partition-move tombstones.
func TestIsMovedToAnotherPartitionNegatives(t *testing.T) {
	cases := []ItemPointer{
		{Block: 0, Offset: 1},                                // ordinary heap pointer
		{Block: InvalidBlockNumber, Offset: 0},               // zeroed CTID (fresh tuple)
		{Block: 0, Offset: MovedPartitionsOffsetNumber},      // sentinel offset but real block
		{Block: InvalidBlockNumber, Offset: 1},               // invalid block but normal offset
	}
	for _, c := range cases {
		if IsMovedToAnotherPartition(c) {
			t.Errorf("IsMovedToAnotherPartition(%+v) = true, want false", c)
		}
	}
	moved := ItemPointer{Block: InvalidBlockNumber, Offset: MovedPartitionsOffsetNumber}
	if !IsMovedToAnotherPartition(moved) {
		t.Errorf("IsMovedToAnotherPartition(%+v) = false, want true", moved)
	}
}

// TestItemIDConstantsMatchPG verifies ItemID flag constants match PG18 itemid.h.
func TestItemIDConstantsMatchPG(t *testing.T) {
	if ItemIDUnused != 0 {
		t.Errorf("ItemIDUnused=%d, want 0 (PG LP_UNUSED)", ItemIDUnused)
	}
	if ItemIDNormal != 1 {
		t.Errorf("ItemIDNormal=%d, want 1 (PG LP_NORMAL)", ItemIDNormal)
	}
	if ItemIDRedirect != 2 {
		t.Errorf("ItemIDRedirect=%d, want 2 (PG LP_REDIRECT)", ItemIDRedirect)
	}
	if ItemIDDead != 3 {
		t.Errorf("ItemIDDead=%d, want 3 (PG LP_DEAD)", ItemIDDead)
	}
}

// TestItemIDPackRoundTrip verifies ItemID pack/unpack preserves values
// and matches PG18 bit layout (bits 0-14=offset, 15-16=flags, 17-31=length).
func TestItemIDPackRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		item ItemID
	}{
		{"unused", ItemID{Offset: 0, Flags: ItemIDUnused, Length: 0}},
		{"normal", ItemID{Offset: 24, Flags: ItemIDNormal, Length: 100}},
		{"redirect", ItemID{Offset: 500, Flags: ItemIDRedirect, Length: 0}},
		{"dead", ItemID{Offset: 8191, Flags: ItemIDDead, Length: 5}},
		{"max_values", ItemID{Offset: 0x7FFF, Flags: 3, Length: 0x7FFF}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := tc.item.pack()
			if err != nil {
				t.Fatalf("pack: %v", err)
			}
			got := unpackItemID(raw)
			if got.Offset != tc.item.Offset {
				t.Errorf("Offset=%d, want %d", got.Offset, tc.item.Offset)
			}
			if got.Flags != tc.item.Flags {
				t.Errorf("Flags=%d, want %d", got.Flags, tc.item.Flags)
			}
			if got.Length != tc.item.Length {
				t.Errorf("Length=%d, want %d", got.Length, tc.item.Length)
			}
		})
	}
}

// TestItemIDPackedBitLayout verifies the exact PG18 bit layout:
// bits 0-14 = lp_off, bits 15-16 = lp_flags, bits 17-31 = lp_len.
func TestItemIDPackedBitLayout(t *testing.T) {
	item := ItemID{Offset: 0x1234, Flags: 2, Length: 0x5678}
	raw, err := item.pack()
	if err != nil {
		t.Fatal(err)
	}

	extractedOffset := uint16(raw & 0x7FFF)
	if extractedOffset != item.Offset {
		t.Errorf("bits[0:14]=%#x, want %#x", extractedOffset, item.Offset)
	}
	extractedFlags := uint8((raw >> 15) & 0x3)
	if extractedFlags != uint8(item.Flags) {
		t.Errorf("bits[15:16]=%d, want %d", extractedFlags, item.Flags)
	}
	extractedLength := uint16((raw >> 17) & 0x7FFF)
	if extractedLength != item.Length {
		t.Errorf("bits[17:31]=%#x, want %#x", extractedLength, item.Length)
	}
}

// TestItemIDPackRejectsOverflow verifies pack() errors on out-of-range values
// (each field constrained to its bit-field width: 15/2/15).
func TestItemIDPackRejectsOverflow(t *testing.T) {
	tests := []struct {
		name string
		item ItemID
	}{
		{"offset overflow", ItemID{Offset: 0x8000, Flags: ItemIDNormal, Length: 10}},
		{"length overflow", ItemID{Length: 0x8000, Flags: ItemIDNormal, Offset: 10}},
		{"flags overflow", ItemID{Flags: 4, Offset: 10, Length: 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.item.pack()
			if err == nil {
				t.Errorf("expected error for %+v", tt.item)
			}
		})
	}
}

// TestReadWriteItemIDRoundTrip verifies readItemID / writeItemID round-trip
// through a real page, covering the line-pointer array access path.
func TestReadWriteItemIDRoundTrip(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		id := ItemID{Offset: uint16(1000 + i*50), Flags: ItemIDNormal, Length: uint16(20 + i)}
		if err := writeItemID(p, i, id); err != nil {
			t.Fatalf("writeItemID idx=%d: %v", i, err)
		}
		got, err := readItemID(p, i)
		if err != nil {
			t.Fatalf("readItemID idx=%d: %v", i, err)
		}
		if got != id {
			t.Errorf("idx=%d: read %+v, wrote %+v", i, got, id)
		}
	}
}

// TestItemIDSize verifies itemIDSize is 4 (PG's sizeof(ItemIdData)).
func TestItemIDSize(t *testing.T) {
	if itemIDSize != 4 {
		t.Errorf("itemIDSize=%d, want 4 (PG sizeof(ItemIdData))", itemIDSize)
	}
}

// TestReadItemIDOutOfRange verifies readItemID rejects an index that would
// read past the page boundary.
func TestReadItemIDOutOfRange(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	maxIdx := (BlockSize - SizeOfPageHeaderData) / itemIDSize
	_, err := readItemID(p, maxIdx)
	if err == nil {
		t.Errorf("readItemID at idx=%d (past valid range) should have returned error", maxIdx)
	}
	_, err = readItemID(p, maxIdx+10)
	if err == nil {
		t.Errorf("readItemID at idx=%d (well past valid range) should have returned error", maxIdx+10)
	}
}

// TestHeapTupleHeaderSize verifies heap tuple header constants match PG18.
func TestHeapTupleHeaderSize(t *testing.T) {
	if SizeOfHeapTupleHeaderData != 23 {
		t.Errorf("SizeOfHeapTupleHeaderData=%d, want 23 (PG offsetof(HeapTupleHeaderData, t_bits))",
			SizeOfHeapTupleHeaderData)
	}
	if DefaultHeapTupleHoff != 24 {
		t.Errorf("DefaultHeapTupleHoff=%d, want 24 (PG MAXALIGN(SizeofHeapTupleHeader))",
			DefaultHeapTupleHoff)
	}
}

// TestHeapTupleHeaderOffsetsPGCompat verifies MarshalBinary writes each
// field at the correct byte offset matching PG18 HeapTupleHeaderData layout.
func TestHeapTupleHeaderOffsetsPGCompat(t *testing.T) {
	tuple := NewHeapTuple(TransactionID(10), TransactionID(20), []byte("data"))
	tuple.Header.Xvac = TransactionID(30)
	tuple.Header.CTID = ItemPointer{Block: 7, Offset: 3}
	tuple.Header.Infomask = 0x0021
	tuple.Header.Infomask2 = 0x0003

	raw, err := tuple.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	// PG18 layout (htup_details.h):
	// [0:4]=xmin, [4:8]=xmax, [8:12]=t_field3/xvac,
	// [12:16]=ctid.blk, [16:18]=ctid.off,
	// [18:20]=infomask2, [20:22]=infomask, [22]=hoff
	if v := TransactionID(raw[0]) | TransactionID(raw[1])<<8 | TransactionID(raw[2])<<16 | TransactionID(raw[3])<<24; v != 10 {
		t.Errorf("xmin at [0:4]=%d, want 10", v)
	}
	if v := TransactionID(raw[4]) | TransactionID(raw[5])<<8 | TransactionID(raw[6])<<16 | TransactionID(raw[7])<<24; v != 20 {
		t.Errorf("xmax at [4:8]=%d, want 20", v)
	}
	if v := TransactionID(raw[8]) | TransactionID(raw[9])<<8 | TransactionID(raw[10])<<16 | TransactionID(raw[11])<<24; v != 30 {
		t.Errorf("xvac at [8:12]=%d, want 30", v)
	}
	if v := BlockNumber(raw[12]) | BlockNumber(raw[13])<<8 | BlockNumber(raw[14])<<16 | BlockNumber(raw[15])<<24; v != 7 {
		t.Errorf("ctid.blk at [12:16]=%d, want 7", v)
	}
	if v := uint16(raw[16]) | uint16(raw[17])<<8; v != 3 {
		t.Errorf("ctid.off at [16:18]=%d, want 3", v)
	}
	if v := uint16(raw[18]) | uint16(raw[19])<<8; v != 0x0003 {
		t.Errorf("infomask2 at [18:20]=%#x, want 0x0003", v)
	}
	if v := uint16(raw[20]) | uint16(raw[21])<<8; v != 0x0021 {
		t.Errorf("infomask at [20:22]=%#x, want 0x0021", v)
	}
	if raw[22] != DefaultHeapTupleHoff {
		t.Errorf("hoff at [22]=%d, want %d", raw[22], DefaultHeapTupleHoff)
	}
}

// TestInfomaskBitsMatchPG verifies every infomask constant matches PG18 htup_details.h.
func TestInfomaskBitsMatchPG(t *testing.T) {
	if HeapXmaxInvalid != 0x0800 {
		t.Errorf("HeapXmaxInvalid=%#x, want 0x0800 (PG HEAP_XMAX_INVALID)", HeapXmaxInvalid)
	}
	if HeapXmaxCommitted != 0x0400 {
		t.Errorf("HeapXmaxCommitted=%#x, want 0x0400 (PG HEAP_XMAX_COMMITTED)", HeapXmaxCommitted)
	}
	if HeapXmaxLockOnly != 0x0080 {
		t.Errorf("HeapXmaxLockOnly=%#x, want 0x0080 (PG HEAP_XMAX_LOCK_ONLY)", HeapXmaxLockOnly)
	}
	if HeapXmaxKeyShrLock != 0x0010 {
		t.Errorf("HeapXmaxKeyShrLock=%#x, want 0x0010 (PG HEAP_XMAX_KEYSHR_LOCK)", HeapXmaxKeyShrLock)
	}
	if HeapXmaxExclLock != 0x0040 {
		t.Errorf("HeapXmaxExclLock=%#x, want 0x0040 (PG HEAP_XMAX_EXCL_LOCK)", HeapXmaxExclLock)
	}
	if HeapHotUpdated != 0x4000 {
		t.Errorf("HeapHotUpdated=%#x, want 0x4000 (PG HEAP_HOT_UPDATED)", HeapHotUpdated)
	}
	if HeapOnlyTuple != 0x8000 {
		t.Errorf("HeapOnlyTuple=%#x, want 0x8000 (PG HEAP_ONLY_TUPLE)", HeapOnlyTuple)
	}
	if MovedPartitionsOffsetNumber != 0xFFFD {
		t.Errorf("MovedPartitionsOffsetNumber=%#x, want 0xFFFD (PG MovedPartitionsOffsetNumber)",
			MovedPartitionsOffsetNumber)
	}
	// HeapXmaxShrLock = KeyShrLock | ExclLock = 0x0010 | 0x0040 = 0x0050
	if HeapXmaxShrLock != 0x0050 {
		t.Errorf("HeapXmaxShrLock=%#x, want 0x0050", HeapXmaxShrLock)
	}
	// HeapXmaxLockMask = KeyShrLock | ExclLock = 0x0050
	if HeapXmaxLockMask != 0x0050 {
		t.Errorf("HeapXmaxLockMask=%#x, want 0x0050", HeapXmaxLockMask)
	}
}

// TestDefaultTupleMarshalLength verifies MarshalBinary produces
// DefaultHeapTupleHoff + len(data) bytes.
func TestDefaultTupleMarshalLength(t *testing.T) {
	cases := []int{0, 1, 10, 100, 1000}
	for _, n := range cases {
		data := make([]byte, n)
		tuple := NewHeapTuple(TransactionID(1), InvalidTransactionID, data)
		raw, err := tuple.MarshalBinary()
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		want := int(DefaultHeapTupleHoff) + n
		if len(raw) != want {
			t.Errorf("n=%d: marshal len=%d, want %d", n, len(raw), want)
		}
	}
}

// TestParseHeapTupleRejectsCorruptInput covers the error paths that
// return ErrCorruptTuple for truncated or malformed on-disk data.
func TestParseHeapTupleRejectsCorruptInput(t *testing.T) {
	// Too short for the fixed header (23 bytes).
	_, err := ParseHeapTuple(make([]byte, 10))
	if err == nil {
		t.Error("expected error for 10-byte input")
	}

	// Header present but t_hoff points past the slice.
	raw := make([]byte, 30)
	raw[22] = 31 // hoff > len(raw)
	_, err = ParseHeapTuple(raw)
	if err == nil {
		t.Error("expected error for hoff > len(raw)")
	}

	// t_hoff smaller than the fixed header.
	raw[22] = 20 // hoff < 23
	_, err = ParseHeapTuple(raw)
	if err == nil {
		t.Error("expected error for hoff < SizeOfHeapTupleHeaderData")
	}
}
