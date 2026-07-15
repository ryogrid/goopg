package wal

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeBtreeInsertPGRoundTrip drives a leaf index-tuple insertion through
// the PG-format emit + decode path — EncodeBtreeInsertPG → encodeRecordXLog →
// decodeRecordXLogDetailed — and asserts the record decodes to RM_BTREE /
// INSERT_LEAF with the IndexTuple carried verbatim as block-0 data (the form
// replayDecodedXLogBtreeInsert re-inserts by key). Like the native btree
// round-trip, the item is opaque bytes here; real IndexTuple replay is covered
// by crash-recovery + replication e2e.
func TestEncodeBtreeInsertPGRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 33, RelOid: 44, Fork: storage.MainFork}
	item := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}

	framed, err := EncodeBtreeInsertPG(rel, 9, 0, item)
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
	if dec.Header.Rmid != RmgrBtree || dec.Header.Info != xlogBtreeInsertLeaf {
		t.Fatalf("header rmid/info = %d/%#x, want %d/%#x", dec.Header.Rmid, dec.Header.Info, RmgrBtree, xlogBtreeInsertLeaf)
	}
	if dec.Payload != nil {
		t.Fatalf("PG-format record must have nil native Payload")
	}
	block, ok := xlogBlockRefByID(dec.XLog, 0)
	if !ok {
		t.Fatalf("decoded record missing block 0")
	}
	if block.Rel != rel || block.Block != 9 {
		t.Fatalf("block ref rel/blk = %+v/%d, want %+v/9", block.Rel, block.Block, rel)
	}
	if !bytes.Equal(block.Data, item) {
		t.Fatalf("item mismatch: got %x want %x", block.Data, item)
	}
}
