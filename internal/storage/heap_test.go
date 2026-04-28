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
