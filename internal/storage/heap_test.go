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
