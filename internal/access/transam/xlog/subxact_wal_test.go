package xlog

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeDecodeXactAssignment verifies round-trip for XactAssignment.
func TestEncodeDecodeXactAssignment(t *testing.T) {
	parentXid := storage.TransactionID(42)
	subXids := []storage.TransactionID{100, 200, 300}

	payload := EncodeXactAssignment(parentXid, subXids)
	if payload[0] != RecordKindXactAssignment {
		t.Fatalf("kind = %d, want %d", payload[0], RecordKindXactAssignment)
	}

	gotParent, gotSubs, err := DecodeXactAssignment(payload)
	if err != nil {
		t.Fatalf("DecodeXactAssignment: %v", err)
	}
	if gotParent != parentXid {
		t.Errorf("parentXid = %d, want %d", gotParent, parentXid)
	}
	if len(gotSubs) != len(subXids) {
		t.Fatalf("len(subXids) = %d, want %d", len(gotSubs), len(subXids))
	}
	for i, want := range subXids {
		if gotSubs[i] != want {
			t.Errorf("subXids[%d] = %d, want %d", i, gotSubs[i], want)
		}
	}
}

// TestEncodeDecodeXactRollbackTo verifies round-trip for XactRollbackTo.
func TestEncodeDecodeXactRollbackTo(t *testing.T) {
	parentXid := storage.TransactionID(55)
	aborted := []storage.TransactionID{111, 222}

	payload := EncodeXactRollbackTo(parentXid, aborted)
	if payload[0] != RecordKindXactRollbackTo {
		t.Fatalf("kind = %d, want %d", payload[0], RecordKindXactRollbackTo)
	}

	gotParent, gotAborted, err := DecodeXactRollbackTo(payload)
	if err != nil {
		t.Fatalf("DecodeXactRollbackTo: %v", err)
	}
	if gotParent != parentXid {
		t.Errorf("parentXid = %d, want %d", gotParent, parentXid)
	}
	if len(gotAborted) != len(aborted) {
		t.Fatalf("len(aborted) = %d, want %d", len(gotAborted), len(aborted))
	}
	for i, want := range aborted {
		if gotAborted[i] != want {
			t.Errorf("aborted[%d] = %d, want %d", i, gotAborted[i], want)
		}
	}
}

// TestEncodeDecodeXactSubAbort verifies round-trip for XactSubAbort.
func TestEncodeDecodeXactSubAbort(t *testing.T) {
	subXid := storage.TransactionID(777)

	payload := EncodeXactSubAbort(subXid)
	if len(payload) != xactRecordSize {
		t.Fatalf("len = %d, want %d", len(payload), xactRecordSize)
	}
	if payload[0] != RecordKindXactSubAbort {
		t.Fatalf("kind = %d, want %d", payload[0], RecordKindXactSubAbort)
	}

	got, err := DecodeXactSubAbort(payload)
	if err != nil {
		t.Fatalf("DecodeXactSubAbort: %v", err)
	}
	if got != subXid {
		t.Errorf("subXid = %d, want %d", got, subXid)
	}
}

// TestXactAssignmentEmptySubXids verifies that an empty subXids slice
// round-trips correctly (no crash, count=0).
func TestXactAssignmentEmptySubXids(t *testing.T) {
	payload := EncodeXactAssignment(10, nil)
	parent, subs, err := DecodeXactAssignment(payload)
	if err != nil {
		t.Fatalf("DecodeXactAssignment(empty): %v", err)
	}
	if parent != 10 {
		t.Errorf("parent = %d, want 10", parent)
	}
	if len(subs) != 0 {
		t.Errorf("len(subs) = %d, want 0", len(subs))
	}
}

// TestSubxactWALReplayRoundTrip is the DoD test for M0050-0003.
//
// Writes XactAssignment + XactRollbackTo + XactSubAbort records to a real
// WAL segment and reads them back to verify they survive the WAL
// write/read cycle (idempotent encoding, correct segment format).
func TestSubxactWALReplayRoundTrip(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	parentXid := storage.TransactionID(42)
	subXids := []storage.TransactionID{100, 200}
	abortedXids := []storage.TransactionID{200}
	subAbortXid := storage.TransactionID(300)

	// Write three subxact WAL records.
	assignPayload := EncodeXactAssignment(parentXid, subXids)
	if _, _, err := w.Append(assignPayload); err != nil {
		t.Fatalf("Append XactAssignment: %v", err)
	}

	rollbackPayload := EncodeXactRollbackTo(parentXid, abortedXids)
	if _, _, err := w.Append(rollbackPayload); err != nil {
		t.Fatalf("Append XactRollbackTo: %v", err)
	}

	subAbortPayload := EncodeXactSubAbort(subAbortXid)
	if _, _, err := w.Append(subAbortPayload); err != nil {
		t.Fatalf("Append XactSubAbort: %v", err)
	}

	// Flush by closing (WAL writer closes and syncs).
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read all records back.
	records, err := ReadAll(walDir, 4096)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("ReadAll returned %d records, want 3", len(records))
	}

	// Verify XactAssignment.
	if records[0].Payload[0] != RecordKindXactAssignment {
		t.Errorf("records[0] kind = %d, want %d", records[0].Payload[0], RecordKindXactAssignment)
	}
	gotParent, gotSubs, err := DecodeXactAssignment(records[0].Payload)
	if err != nil {
		t.Fatalf("DecodeXactAssignment: %v", err)
	}
	if gotParent != parentXid || len(gotSubs) != 2 || gotSubs[0] != 100 || gotSubs[1] != 200 {
		t.Errorf("XactAssignment: parent=%d subs=%v", gotParent, gotSubs)
	}

	// Verify XactRollbackTo.
	if records[1].Payload[0] != RecordKindXactRollbackTo {
		t.Errorf("records[1] kind = %d, want %d", records[1].Payload[0], RecordKindXactRollbackTo)
	}
	gotParent2, gotAborted, err := DecodeXactRollbackTo(records[1].Payload)
	if err != nil {
		t.Fatalf("DecodeXactRollbackTo: %v", err)
	}
	if gotParent2 != parentXid || len(gotAborted) != 1 || gotAborted[0] != 200 {
		t.Errorf("XactRollbackTo: parent=%d aborted=%v", gotParent2, gotAborted)
	}

	// Verify XactSubAbort.
	if records[2].Payload[0] != RecordKindXactSubAbort {
		t.Errorf("records[2] kind = %d, want %d", records[2].Payload[0], RecordKindXactSubAbort)
	}
	gotSubAbort, err := DecodeXactSubAbort(records[2].Payload)
	if err != nil {
		t.Fatalf("DecodeXactSubAbort: %v", err)
	}
	if gotSubAbort != subAbortXid {
		t.Errorf("XactSubAbort: subXid = %d, want %d", gotSubAbort, subAbortXid)
	}

	t.Logf("all 3 subxact WAL records round-tripped successfully")
}

// TestSubxactApplyRecordSkipsNoOp verifies that ApplyRecord treats
// subxact markers as no-ops for physical recovery (like XactCommit).
func TestSubxactApplyRecordSkipsNoOp(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	for _, payload := range [][]byte{
		EncodeXactAssignment(10, []storage.TransactionID{100}),
		EncodeXactRollbackTo(10, []storage.TransactionID{100}),
		EncodeXactSubAbort(100),
	} {
		applied, err := ApplyRecord(mgr, Record{Payload: payload})
		if err != nil {
			t.Errorf("ApplyRecord %d: %v", payload[0], err)
		}
		if applied {
			t.Errorf("ApplyRecord %d: expected no-op (applied=false)", payload[0])
		}
	}
}
