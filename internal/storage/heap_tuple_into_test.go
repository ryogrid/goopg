package storage

import (
	"reflect"
	"testing"
)

// TestPageGetHeapTupleNoCopyIntoReusesTuple pins M0122-0012 C48: the seq scan
// fills one HeapTuple in place for every tuple it reads, so the in-place fetch
// must leave nothing behind from the previous tuple — above all a NULL bitmap
// from a NULL-bearing tuple carried onto a tuple without one, which would make
// the decode treat present columns as NULL. Each fetch into the reused tuple
// must equal the by-value PageGetHeapTupleNoCopy, and an error must leave the
// tuple untouched.
func TestPageGetHeapTupleNoCopyIntoReusesTuple(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		tup := NewHeapTuple(TransactionID(100+i), InvalidTransactionID, []byte{byte(i), 1, 2, 3})
		if i%2 == 0 {
			tup = NewHeapTupleWithNulls(TransactionID(100+i), TransactionID(900+i), []byte{0x05}, []byte{byte(i), 9})
			tup.Header.Infomask |= HeapHasNull
			tup.Header.Infomask2 = 3
		}
		if _, err := PageAddHeapTuple(p, tup); err != nil {
			t.Fatal(err)
		}
	}
	var reused HeapTuple
	for slot := uint16(1); slot <= 6; slot++ {
		want, err := PageGetHeapTupleNoCopy(p, slot)
		if err != nil {
			t.Fatal(err)
		}
		if err := PageGetHeapTupleNoCopyInto(p, slot, &reused); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(reused, want) {
			t.Fatalf("slot %d: in-place fetch %+v, by-value %+v", slot, reused, want)
		}
		if (slot%2 == 0) != (reused.Bitmap == nil) {
			t.Fatalf("slot %d: bitmap %v carried across tuples", slot, reused.Bitmap)
		}
	}
	before := reused
	if err := PageGetHeapTupleNoCopyInto(p, 99, &reused); err == nil {
		t.Fatal("out-of-range slot fetched")
	}
	if !reflect.DeepEqual(reused, before) {
		t.Fatal("a failed fetch wrote the tuple")
	}
}
