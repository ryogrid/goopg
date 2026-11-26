package xlog

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// xl_heap_truncate flag bits (heapam_xlog.h XLH_TRUNCATE_*). They have the
// same values as the pgoutput Truncate message's option bits.
const (
	XLHTruncateCascade     uint8 = 1 << 0
	XLHTruncateRestartSeqs uint8 = 1 << 1
)

// sizeOfHeapTruncate is SizeOfHeapTruncate: offsetof(xl_heap_truncate,
// relids) — dbId(4) + nrelids(4) + flags(1), padded to the Oid array's
// 4-byte alignment.
const sizeOfHeapTruncate = 12

// HeapTruncate is a decoded xl_heap_truncate: the database the TRUNCATE ran
// in, its CASCADE / RESTART IDENTITY flags and the logically logged relation
// OIDs it truncated.
type HeapTruncate struct {
	DBOid  uint32
	Flags  uint8
	Relids []uint32
}

// EncodeHeapTruncatePG builds the XLOG_HEAP_TRUNCATE record
// ExecuteTruncateGuts inserts at wal_level = logical (tablecmds.c): main data
// only, no block references. xid is the truncating transaction's xid.
// recovery treats the record as a no-op (heap_redo), so it exists only for
// logical decoding.
func EncodeHeapTruncatePG(xid storage.TransactionID, t HeapTruncate) ([]byte, error) {
	mainData := make([]byte, sizeOfHeapTruncate+4*len(t.Relids))
	binary.LittleEndian.PutUint32(mainData[0:4], t.DBOid)
	binary.LittleEndian.PutUint32(mainData[4:8], uint32(len(t.Relids)))
	mainData[8] = t.Flags
	for i, oid := range t.Relids {
		binary.LittleEndian.PutUint32(mainData[sizeOfHeapTruncate+4*i:], oid)
	}
	return framedAssemble(RmgrHeap, xlogHeapTruncate, uint32(xid), mainData, nil)
}

// DecodeHeapTruncate parses an XLOG_HEAP_TRUNCATE record's main data.
func DecodeHeapTruncate(mainData []byte) (HeapTruncate, error) {
	if len(mainData) < sizeOfHeapTruncate {
		return HeapTruncate{}, fmt.Errorf("wal: heap truncate: %d-byte main data is shorter than the header", len(mainData))
	}
	n := binary.LittleEndian.Uint32(mainData[4:8])
	if uint64(len(mainData)-sizeOfHeapTruncate) != 4*uint64(n) {
		return HeapTruncate{}, fmt.Errorf("wal: heap truncate: %d relids do not fit %d payload bytes", n, len(mainData)-sizeOfHeapTruncate)
	}
	t := HeapTruncate{
		DBOid:  binary.LittleEndian.Uint32(mainData[0:4]),
		Flags:  mainData[8],
		Relids: make([]uint32, n),
	}
	for i := range t.Relids {
		t.Relids[i] = binary.LittleEndian.Uint32(mainData[sizeOfHeapTruncate+4*i:])
	}
	return t, nil
}

// applyHeapTruncate is DecodeTruncate: a TRUNCATE in the slot's database is
// queued in its transaction as one REORDER_BUFFER_CHANGE_TRUNCATE.
func (d *Decoder) applyHeapTruncate(xid storage.TransactionID, lsn uint64, t HeapTruncate) {
	if d.dbOid != 0 && t.DBOid != d.dbOid {
		return
	}
	d.buf.Append(xid, Change{Kind: ChangeTruncate, LSN: lsn, Relids: t.Relids, TruncateFlags: t.Flags})
}
