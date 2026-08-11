package wal

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestClassifyPGHeapHotUpdateRoutesByXid verifies the logical-decoding classifier
// handles the PG-format xl_heap_update (HOT) record: the change is routed to the
// updating xact by the record's xl_xid and reaches the plugin as an update with
// the reconstructed new tuple.
func TestClassifyPGHeapHotUpdateRoutesByXid(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	newTup := storage.NewHeapTuple(0, storage.InvalidTransactionID, []byte("newrow"))
	newTup.Header.SetHeapOnly()
	newBytes, err := newTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	const xmax = storage.TransactionID(88)
	framed, err := EncodeHeapHotUpdatePG(rel, 3, 1, 2, xmax, newBytes)
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
		t.Fatalf("Active=%d want 1 (xid 88)", d.Active())
	}

	if err := Classify(d, Record{Payload: EncodeXactCommit(88), EndLSN: 200}); err != nil {
		t.Fatal(err)
	}
	want := []string{"Begin", "Change", "Commit"}
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("plugin calls=%v want %v", p.calls, want)
	}
}
