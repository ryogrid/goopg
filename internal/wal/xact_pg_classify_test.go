package wal

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// classifyFramed decodes a framed PG record and runs it through Classify.
func classifyFramed(t *testing.T, d *Decoder, framed []byte) {
	t.Helper()
	record, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatalf("encodeRecordXLog: %v", err)
	}
	dec, err := decodeRecordXLogDetailed(record)
	if err != nil {
		t.Fatalf("decodeRecordXLogDetailed: %v", err)
	}
	if err := Classify(d, Record{XLog: dec.XLog, Payload: dec.Payload, EndLSN: 200}); err != nil {
		t.Fatalf("Classify: %v", err)
	}
}

// TestClassifyPGXactCommitDrivesReorderBuffer verifies the logical decoder drains
// and commits its reorder buffer on a PG-format xl_xact_commit (no native
// Payload; xid in xl_xid). Without the RmgrXact branch in classifyDecodedXLog the
// commit would be silently dropped and the change never surface.
func TestClassifyPGXactCommitDrivesReorderBuffer(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	const xid = storage.TransactionID(55)

	tup := storage.NewHeapTuple(xid, storage.InvalidTransactionID, []byte("row"))
	tup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	tupBytes, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	insFramed, err := EncodeHeapInsertPG(rel, 0, 1, tupBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	classifyFramed(t, d, insFramed)
	if d.Active() != 1 {
		t.Fatalf("Active=%d after insert, want 1", d.Active())
	}

	commitFramed, err := EncodeXactCommitPG(xid, false)
	if err != nil {
		t.Fatal(err)
	}
	classifyFramed(t, d, commitFramed)

	want := []string{"Begin", "Change", "Commit"}
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("plugin calls=%v want %v", p.calls, want)
	}
	if d.Active() != 0 {
		t.Errorf("Active after commit=%d want 0", d.Active())
	}
}

// TestClassifyPGXactAbortDropsXact verifies a PG-format xl_xact_abort drops the
// in-flight xact from the decoder (no Change surfaces to the plugin).
func TestClassifyPGXactAbortDropsXact(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	const xid = storage.TransactionID(56)
	tup := storage.NewHeapTuple(xid, storage.InvalidTransactionID, []byte("row"))
	tup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	tupBytes, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	insFramed, err := EncodeHeapInsertPG(rel, 0, 1, tupBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	classifyFramed(t, d, insFramed)

	abortFramed, err := EncodeXactAbortPG(xid)
	if err != nil {
		t.Fatal(err)
	}
	classifyFramed(t, d, abortFramed)

	if d.Active() != 0 {
		t.Errorf("Active after abort=%d want 0", d.Active())
	}
	for _, c := range p.calls {
		if c == "Commit" || c == "Change" {
			t.Errorf("aborted xact leaked %q to the plugin (calls=%v)", c, p.calls)
		}
	}
}
