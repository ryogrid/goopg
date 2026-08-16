package xlog

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestClassifyPGHeapDeleteRoutesByXmax verifies the logical-decoding classifier
// handles the PG-format xl_heap_delete record (no native Payload): the change is
// routed to the deleting xact by the record's xmax, and reaches the plugin with
// the reconstructed old tuple. Mirrors the native TestClassifyHeapDeleteRoutesByXmax.
func TestClassifyPGHeapDeleteRoutesByXmax(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	old := storage.NewHeapTuple(0, storage.InvalidTransactionID, []byte("oldrow"))
	old.Header.CTID = storage.ItemPointer{Block: 2, Offset: 3}
	oldBytes, err := old.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	const xmax = storage.TransactionID(77)
	framed, err := EncodeHeapDeletePG(rel, 2, 3, xmax, oldBytes)
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
		t.Fatalf("Active=%d want 1 (xmax 77)", d.Active())
	}

	if err := Classify(d, Record{Payload: EncodeXactCommit(77), EndLSN: 200}); err != nil {
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
