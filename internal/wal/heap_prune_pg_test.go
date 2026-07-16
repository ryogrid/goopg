package wal

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeHeapPruneOptPGRoundTrip drives a prune (redirects + now-unused)
// through the real encode + decode path and asserts the record is a PG
// xl_heap_prune (RM_HEAP2 / PRUNE_ON_ACCESS) whose block-0 sub-records decode
// back to the same redirect pairs and unused slots.
func TestEncodeHeapPruneOptPGRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 44, Fork: storage.MainFork}
	redirects := [][2]uint16{{1, 3}, {4, 6}}
	unused := []uint16{2, 5}

	framed, err := EncodeHeapPruneOptPG(rel, 7, redirects, unused)
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
	if dec.Header.Rmid != RmgrHeap2 || dec.Header.Info != xlogHeap2PruneOnAccess {
		t.Fatalf("rmid/info = %d/%#x, want RmgrHeap2/PRUNE_ON_ACCESS", dec.Header.Rmid, dec.Header.Info)
	}
	if dec.Payload != nil {
		t.Fatalf("PG-format record must have nil native Payload")
	}
	block, ok := xlogBlockRefByID(dec.XLog, 0)
	if !ok {
		t.Fatalf("decoded record missing block 0")
	}
	if block.Rel != rel || block.Block != 7 {
		t.Fatalf("block ref rel/blk = %+v/%d", block.Rel, block.Block)
	}

	gotR, gotU, gotF, err := decodeXLogHeapPrune(dec.XLog.MainData, block.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotR, redirects) {
		t.Fatalf("redirects = %v, want %v", gotR, redirects)
	}
	if !reflect.DeepEqual(gotU, unused) {
		t.Fatalf("unused = %v, want %v", gotU, unused)
	}
	if len(gotF) != 0 {
		t.Fatalf("unexpected frozen slots: %v", gotF)
	}
}
