package xlog

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNoLogicalInsertIsNotDecoded pins M0149-0061l against PG 18.3's
// heap_insert / DecodeInsert: an insert made inside a
// BeginNoLogicalInserts window (goopg's in-place table rewrite) is logged
// without XLH_INSERT_CONTAINS_NEW_TUPLE, as HEAP_INSERT_NO_LOGICAL is, and
// logical decoding skips it; the same relation's inserts by another xid, and
// the rewriting xid's inserts once the window closes, are decoded as usual.
func TestNoLogicalInsertIsNotDecoded(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 16401}
	insert := func(xid storage.TransactionID) Record {
		t.Helper()
		tup := storage.NewHeapTuple(xid, storage.InvalidTransactionID, []byte("payload"))
		tup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
		tuple, err := tup.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		framed, err := EncodeHeapInsertPG(rel, 0, 1, tuple, false)
		if err != nil {
			t.Fatal(err)
		}
		record, _, err := encodeRecordXLog(framed, 0)
		if err != nil {
			t.Fatal(err)
		}
		dec, err := decodeRecordXLogDetailed(record)
		if err != nil {
			t.Fatal(err)
		}
		return Record{XLog: dec.XLog, EndLSN: 100}
	}

	end := BeginNoLogicalInserts(rel, 50)
	rewrite := insert(50)
	other := insert(51)
	end()
	after := insert(50)

	if f := xlogHeapInsertFlags(rewrite.XLog.MainData); f&xlhInsertContainsNewTuple != 0 {
		t.Errorf("rewrite insert flags = %#x, want CONTAINS_NEW_TUPLE clear", f)
	}
	for _, r := range []Record{other, after} {
		if f := xlogHeapInsertFlags(r.XLog.MainData); f&xlhInsertContainsNewTuple == 0 {
			t.Errorf("ordinary insert flags = %#x, want CONTAINS_NEW_TUPLE set", f)
		}
	}

	for _, tc := range []struct {
		r    Record
		xid  storage.TransactionID
		want []string
	}{
		{rewrite, 50, nil},
		{other, 51, []string{"Begin", "Change", "Commit"}},
		{after, 50, []string{"Begin", "Change", "Commit"}},
	} {
		p := &recordingPlugin{}
		d := NewDecoder(p)
		if err := Classify(d, tc.r); err != nil {
			t.Fatal(err)
		}
		if err := Classify(d, Record{Payload: EncodeXactCommit(tc.xid), EndLSN: 200}); err != nil {
			t.Fatal(err)
		}
		if tc.want == nil {
			for _, c := range p.calls {
				if c == "Change" {
					t.Errorf("xid %d: a no-logical insert reached the plugin: %v", tc.xid, p.calls)
				}
			}
			continue
		}
		if !reflect.DeepEqual(p.calls, tc.want) {
			t.Errorf("xid %d: plugin calls = %v, want %v", tc.xid, p.calls, tc.want)
		}
	}
}
