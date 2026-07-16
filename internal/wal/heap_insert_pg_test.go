package wal

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeHeapInsertPGRoundTrip drives a marshaled heap tuple through the full
// PG-format emit + decode path — EncodeHeapInsertPG → encodeRecordXLog →
// decodeRecordXLogDetailed → decodeXLogHeapInsertTuple — and asserts the tuple
// is reconstructed byte-for-byte. Covers a NULL-bearing tuple (non-zero null
// bitmap), which the previous prefix-stripping reconstruction rejected.
func TestEncodeHeapInsertPGRoundTrip(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 16400, RelOid: 24576, Fork: storage.MainFork}
	const blk = storage.BlockNumber(4)
	const offnum = uint16(3)
	const xmin = storage.TransactionID(0x11223344)

	cases := []struct {
		name string
		tup  storage.HeapTuple
	}{
		{"plain", storage.NewHeapTuple(xmin, storage.InvalidTransactionID, []byte("hello-columns"))},
		// 2-col tuple, second column NULL → null bitmap byte 0x01 (bit set = NOT
		// NULL); t_hoff = MAXALIGN(23+1) = 24. The non-zero bitmap is exactly what
		// the old decoder rejected.
		{"nulls", storage.NewHeapTupleWithNulls(xmin, storage.InvalidTransactionID, []byte{0x01}, []byte("col1only"))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tup := c.tup
			// Stored form after A2-pre: self-pointing t_ctid.
			tup.Header.CTID = storage.ItemPointer{Block: blk, Offset: offnum}
			want, err := tup.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary: %v", err)
			}

			framed, err := EncodeHeapInsertPG(rel, blk, offnum, want, false)
			if err != nil {
				t.Fatalf("EncodeHeapInsertPG: %v", err)
			}
			record, _, err := encodeRecordXLog(framed, 0)
			if err != nil {
				t.Fatalf("encodeRecordXLog: %v", err)
			}
			dec, err := decodeRecordXLogDetailed(record)
			if err != nil {
				t.Fatalf("decodeRecordXLogDetailed: %v", err)
			}
			if dec.Header.Rmid != RmgrHeap || dec.Header.Info != xlogHeapInsert {
				t.Fatalf("header rmid/info = %d/%#x, want %d/%#x", dec.Header.Rmid, dec.Header.Info, RmgrHeap, xlogHeapInsert)
			}
			if dec.Header.XID != uint32(xmin) {
				t.Fatalf("xl_xid = %#x, want %#x (t_xmin)", dec.Header.XID, uint32(xmin))
			}
			block, ok := xlogBlockRefByID(dec.XLog, 0)
			if !ok {
				t.Fatalf("decoded record missing block 0")
			}
			if block.Rel != rel || block.Block != blk {
				t.Fatalf("block ref rel/blk = %+v/%d, want %+v/%d", block.Rel, block.Block, rel, blk)
			}
			gotOff, err := decodeXLogHeapInsertMainData(dec.XLog.MainData)
			if err != nil {
				t.Fatalf("decodeXLogHeapInsertMainData: %v", err)
			}
			if gotOff != offnum {
				t.Fatalf("offnum = %d, want %d", gotOff, offnum)
			}
			got, err := decodeXLogHeapInsertTuple(block, storage.TransactionID(dec.Header.XID), gotOff)
			if err != nil {
				t.Fatalf("decodeXLogHeapInsertTuple: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("tuple did not round-trip byte-for-byte:\n got=%x\nwant=%x", got, want)
			}
		})
	}
}
