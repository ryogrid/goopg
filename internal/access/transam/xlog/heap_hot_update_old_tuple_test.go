package xlog

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestClassifyPGHeapHotUpdateOldTuple: a HOT xl_heap_update carrying the old
// tuple (XLH_UPDATE_CONTAINS_OLD_TUPLE, REPLICA IDENTITY FULL) decodes into an
// update whose OldTuple holds the old row's data (DecodeUpdate), and the same
// record without it keeps OldTuple empty.
func TestClassifyPGHeapHotUpdateOldTuple(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	mk := func(data string, heapOnly bool) []byte {
		tup := storage.NewHeapTuple(0, storage.InvalidTransactionID, []byte(data))
		if heapOnly {
			tup.Header.SetHeapOnly()
		}
		b, err := tup.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, withOld := range []bool{true, false} {
		var old []byte
		if withOld {
			old = mk("oldrow", false)
		}
		framed, err := EncodeHeapHotUpdatePGOld(rel, 3, 1, 2, 88, mk("newrow", true), old, false)
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
		p := &recordingPlugin{}
		d := NewDecoder(p)
		if err := Classify(d, Record{XLog: dec.XLog, EndLSN: 100}); err != nil {
			t.Fatal(err)
		}
		if err := Classify(d, Record{Payload: EncodeXactCommit(88), EndLSN: 200}); err != nil {
			t.Fatal(err)
		}
		if len(p.changes) != 1 {
			t.Fatalf("withOld=%v: %d changes", withOld, len(p.changes))
		}
		got := p.changes[0].OldTuple
		if withOld != (len(got) > 0) || (withOld && !bytes.HasSuffix(got, []byte("oldrow"))) {
			t.Errorf("withOld=%v: OldTuple %q", withOld, got)
		}
	}
}
