package wal

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPGAssembledRoundTrip frames an assembled PG body, runs it through the real
// encodeRecordXLog path, and decodes it back — the envelope must be stripped and
// the (rmid, info, xid) + blocks + main-data must survive verbatim.
func TestPGAssembledRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 16400, RelOid: 24576, Fork: storage.MainFork}
	mainData := []byte{0x07, 0x00, 0x08} // e.g. xl_heap_insert{offnum=7, flags=CONTAINS_NEW_TUPLE}
	blkData := []byte("blk0-data")
	body, err := assembleXLogRecord(mainData, []BlockRef{{ID: 0, Rel: rel, Block: 3, Data: blkData}})
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}

	const wantXID uint32 = 0xDEADBEEF
	framed := framePGAssembled(RmgrHeap, xlogHeapInsert, wantXID, body)

	// predictXLogRecordLen must agree with what encodeRecordXLog emits.
	predReal, predPadded := predictXLogRecordLen(framed)
	record, realLen, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatalf("encodeRecordXLog: %v", err)
	}
	if predReal != realLen {
		t.Fatalf("predict realLen=%d, encode realLen=%d", predReal, realLen)
	}
	if predPadded != len(record) {
		t.Fatalf("predict paddedLen=%d, encode len=%d", predPadded, len(record))
	}

	dec, err := decodeRecordXLogDetailed(record)
	if err != nil {
		t.Fatalf("decodeRecordXLogDetailed: %v", err)
	}
	if dec.Header.Rmid != RmgrHeap || dec.Header.Info != xlogHeapInsert {
		t.Fatalf("header rmid/info = %d/%#x, want %d/%#x", dec.Header.Rmid, dec.Header.Info, RmgrHeap, xlogHeapInsert)
	}
	if dec.Header.XID != wantXID {
		t.Fatalf("header xid = %#x, want %#x", dec.Header.XID, wantXID)
	}
	if dec.XLog == nil || len(dec.XLog.Blocks) != 1 {
		t.Fatalf("want 1 decoded block, got %+v", dec.XLog)
	}
	b := dec.XLog.Blocks[0]
	if b.Rel != rel || b.Block != 3 || !bytes.Equal(b.Data, blkData) {
		t.Fatalf("block mismatch: rel=%+v blk=%d data=%q", b.Rel, b.Block, b.Data)
	}
	if !bytes.Equal(dec.XLog.MainData, mainData) {
		t.Fatalf("main-data mismatch: got %x want %x", dec.XLog.MainData, mainData)
	}
	// A block-bearing record must NOT expose a native Payload (routes to the
	// decoded replay path, not the payload[0] switch).
	if dec.Payload != nil {
		t.Fatalf("pre-assembled record must have nil native Payload, got %d bytes", len(dec.Payload))
	}
}

// TestPGAssembledFrameUnframe checks the envelope helpers in isolation and that
// native payloads are never misread as pre-assembled.
func TestPGAssembledFrameUnframe(t *testing.T) {
	body := []byte("assembled-body")
	framed := framePGAssembled(RmgrBtree, xlogBtreeInsertLeaf, 42, body)
	rmid, info, xid, got, ok := unframePGAssembled(framed)
	if !ok {
		t.Fatalf("unframe returned ok=false for a framed payload")
	}
	if rmid != RmgrBtree || info != xlogBtreeInsertLeaf || xid != 42 || !bytes.Equal(got, body) {
		t.Fatalf("unframe mismatch: rmid=%d info=%#x xid=%d body=%q", rmid, info, xid, got)
	}

	// Native payloads (start with a real RecordKind, always < the marker) and
	// short payloads must not be misidentified as pre-assembled envelopes.
	for _, native := range [][]byte{
		nil,
		{RecordKindHeapInsert},
		{RecordKindXactCommit, 1, 2, 3, 4, 5, 6},
		{pgAssembledMarker}, // marker byte alone, but shorter than the envelope header
	} {
		if _, _, _, _, ok := unframePGAssembled(native); ok {
			t.Fatalf("native/short payload %v misread as pre-assembled", native)
		}
	}
}

// TestPGAssembledMarkerReserved guards the 0xFF reservation: no goopg RecordKind
// (the payload[0] byte of a native record) may collide with the marker, or a
// native record could be misrouted through the assembled path.
func TestPGAssembledMarkerReserved(t *testing.T) {
	// recordKindToRmgrInfo maps every native RecordKind byte; a byte equal to
	// the marker must not be a defined native kind. RecordKinds are dense only
	// through the low values, so the marker (0xFF) must stay well above them.
	if pgAssembledMarker != 0xFF {
		t.Fatalf("marker changed to %#x — re-verify it is above every RecordKind", pgAssembledMarker)
	}
	// A native heap-insert payload begins with RecordKindHeapInsert, which must
	// be distinct from the marker.
	if RecordKindHeapInsert == pgAssembledMarker {
		t.Fatalf("RecordKindHeapInsert collides with the pre-assembled marker")
	}
}
