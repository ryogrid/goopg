package wal

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestClassifyPGHeapInsertRoutesByXlXid verifies the logical-decoding classifier
// handles the PG-format xl_heap_insert record (no native Payload): the change is
// routed to the inserting xact by the record header's xl_xid (== t_xmin), and
// the reconstructed tuple reaches the plugin. Mirrors the native
// TestClassifyHeapInsertRoutesByXmin for the flipped (A2) record shape.
func TestClassifyPGHeapInsertRoutesByXlXid(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	tup := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte("payload"))
	tup.Header.CTID = storage.ItemPointer{Block: 5, Offset: 1} // stored self-pointing (A2-pre)
	tuple, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	framed, err := EncodeHeapInsertPG(rel, 5, 1, tuple)
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
	if dec.Payload != nil {
		t.Fatalf("PG-format record must have nil native Payload")
	}

	if err := Classify(d, Record{XLog: dec.XLog, EndLSN: 100}); err != nil {
		t.Fatal(err)
	}
	if d.Active() != 1 {
		t.Fatalf("Active=%d want 1 (xid 42 from xl_xid)", d.Active())
	}

	if err := Classify(d, Record{Payload: EncodeXactCommit(42), EndLSN: 200}); err != nil {
		t.Fatal(err)
	}
	want := []string{"Begin", "Change", "Commit"}
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("plugin calls=%v want %v", p.calls, want)
	}
	if d.Active() != 0 {
		t.Errorf("Active after commit=%d want 0", d.Active())
	}
}
