package wal

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

func TestApplyRecordReplaysDecodedXLogHeapInsert(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 905, Fork: storage.MainFork}
	rec := Record{
		StartLSN: 1,
		EndLSN:   100,
		XLog: &XLogDecodedRecord{
			Header:   XLogRecord{Rmid: RmgrHeap, Info: xlogHeapInsert | xlogHeapInit, XID: 42},
			MainData: testXLogHeapInsertMainData(1),
			Blocks: []XLogBlockRef{{
				ID:       0,
				Rel:      rel,
				Block:    0,
				WillInit: true,
				Data:     testXLogHeapInsertTupleData(storage.DefaultHeapTupleHoff, []byte("hello")),
			}},
		},
	}

	applied, err := ApplyRecord(mgr, rec)
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("ApplyRecord applied=false, want true")
	}

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	if got := storage.MustHeader(page).LSN(); got != storage.LSN(rec.EndLSN) {
		t.Fatalf("pd_lsn = %d, want %d", got, rec.EndLSN)
	}
	tup, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if tup.Header.Xmin != 42 {
		t.Fatalf("xmin = %d, want 42", tup.Header.Xmin)
	}
	if tup.Header.CTID.Block != 0 || tup.Header.CTID.Offset != 1 {
		t.Fatalf("ctid = %+v, want block=0 off=1", tup.Header.CTID)
	}
	if string(tup.Data) != "hello" {
		t.Fatalf("tuple data = %q, want %q", tup.Data, "hello")
	}
}

func TestApplyRecordReplaysDecodedXLogHeapInsertStripsZeroTuplePrefix(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 908, Fork: storage.MainFork}
	attrBytes := append(testPhysicalPGInt4(-999), testPhysicalPGShortText("bootstrap")...)
	rec := Record{
		StartLSN: 1,
		EndLSN:   100,
		XLog: &XLogDecodedRecord{
			Header:   XLogRecord{Rmid: RmgrHeap, Info: xlogHeapInsert | xlogHeapInit, XID: 42},
			MainData: testXLogHeapInsertMainData(1),
			Blocks: []XLogBlockRef{{
				ID:       0,
				Rel:      rel,
				Block:    0,
				WillInit: true,
				Data:     testXLogHeapInsertTupleDataWithPrefix(storage.DefaultHeapTupleHoff, []byte{0}, attrBytes),
			}},
		},
	}

	applied, err := ApplyRecord(mgr, rec)
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("ApplyRecord applied=false, want true")
	}

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	tup, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(tup.Data) != string(attrBytes) {
		t.Fatalf("tuple data = %x, want %x", tup.Data, attrBytes)
	}
}

func TestApplyRecordRestoresDecodedXLogBlockImage(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 906, Fork: storage.MainFork}
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	tup := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte("from-image"))
	tup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	raw, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.PageInsertItemRawAt(page, 1, raw); err != nil {
		t.Fatal(err)
	}

	rec := Record{
		StartLSN: 1,
		EndLSN:   200,
		XLog: &XLogDecodedRecord{
			Header:   XLogRecord{Rmid: RmgrHeap, Info: xlogHeapInsert | xlogHeapInit, XID: 42},
			MainData: testXLogHeapInsertMainData(1),
			Blocks: []XLogBlockRef{{
				ID:         0,
				Rel:        rel,
				Block:      0,
				HasImage:   true,
				ImageApply: true,
				Image:      page,
			}},
		},
	}

	applied, err := ApplyRecord(mgr, rec)
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("ApplyRecord applied=false, want true")
	}

	gotPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, gotPage); err != nil {
		t.Fatal(err)
	}
	if got := storage.MustHeader(gotPage).LSN(); got != storage.LSN(rec.EndLSN) {
		t.Fatalf("pd_lsn = %d, want %d", got, rec.EndLSN)
	}
	tupOut, err := storage.PageGetHeapTuple(gotPage, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(tupOut.Data) != "from-image" {
		t.Fatalf("tuple data = %q, want %q", tupOut.Data, "from-image")
	}
}

func TestApplyRecordRecognizesDecodedXLogStandbyRunningXacts(t *testing.T) {
	rec := Record{
		StartLSN: 11,
		EndLSN:   22,
		XLog: &XLogDecodedRecord{
			Header: XLogRecord{Rmid: RmgrStandby, Info: xlogStandbyRunningXacts},
		},
	}

	applied, err := ApplyRecord(nil, rec)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("ApplyRecord applied=true, want false")
	}
}

func TestApplyRecordRejectsUnknownDecodedXLogStandbyRecord(t *testing.T) {
	rec := Record{
		StartLSN: 33,
		EndLSN:   44,
		XLog: &XLogDecodedRecord{
			Header: XLogRecord{Rmid: RmgrStandby, Info: 0x20},
		},
	}

	applied, err := ApplyRecord(nil, rec)
	if err == nil {
		t.Fatal("ApplyRecord err=nil, want unsupported standby opcode error")
	}
	if applied {
		t.Fatal("ApplyRecord applied=true, want false")
	}
	if !strings.Contains(err.Error(), "rmid=8 info=0x20") {
		t.Fatalf("err = %v, want standby rmgr/opcode context", err)
	}
}

func TestApplyRecordPrefersDecodedXLogForUnknownPayloadKind(t *testing.T) {
	rec := Record{
		StartLSN: 55,
		EndLSN:   66,
		Payload:  []byte{0x00, 0x11, 0x22, 0x33},
		XLog: &XLogDecodedRecord{
			Header: XLogRecord{Rmid: RmgrXLog, Info: xlogInfoDefault},
		},
	}

	applied, err := ApplyRecord(nil, rec)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("ApplyRecord applied=true, want false")
	}
}

func TestDecodedXLogHeapInsertVisibleThroughPreloadedBufferPoolAfterCommit(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	mgr.OnBlockWritten = func(rel storage.RelFileNode, blk storage.BlockNumber) {
		pool.InvalidateBlock(storage.BufferTag{Rel: rel, Block: blk})
	}
	txnMgr := mvcc.NewManager()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 907, Fork: storage.MainFork}
	empty := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(empty); err != nil {
		t.Fatal(err)
	}
	if got, err := mgr.Extend(rel, empty); err != nil {
		t.Fatal(err)
	} else if got != 0 {
		t.Fatalf("Extend block = %d, want 0", got)
	}

	preloaded, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(preloaded)

	rec := Record{
		StartLSN: 1,
		EndLSN:   100,
		XLog: &XLogDecodedRecord{
			Header:   XLogRecord{Rmid: RmgrHeap, Info: xlogHeapInsert | xlogHeapInit, XID: 42},
			MainData: testXLogHeapInsertMainData(1),
			Blocks: []XLogBlockRef{{
				ID:       0,
				Rel:      rel,
				Block:    0,
				WillInit: true,
				Data:     testXLogHeapInsertTupleData(storage.DefaultHeapTupleHoff, []byte("hello")),
			}},
		},
	}

	applied, err := ApplyRecord(mgr, rec)
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("ApplyRecord applied=false, want true")
	}
	txnMgr.ReplayXactCommit(42)

	tx, err := txnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	defer txnMgr.Rollback(tx)
	snap, err := txnMgr.SnapshotFor(tx)
	if err != nil {
		t.Fatal(err)
	}

	slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(slot)
	slot.RLock()
	defer slot.RUnlock()
	tup, err := storage.PageGetHeapTuple(slot.Page(), 1)
	if err != nil {
		t.Fatalf("PageGetHeapTuple: %v", err)
	}
	if !mvcc.TupleVisible(tup.Header, snap, tx.XID) {
		t.Fatalf("tuple header=%+v not visible through preloaded buffer-pool page", tup.Header)
	}
	if string(tup.Data) != "hello" {
		t.Fatalf("tuple data = %q, want %q", tup.Data, "hello")
	}
}

func TestReplayedXactInfoUsesDecodedXLogRecord(t *testing.T) {
	tests := []struct {
		name      string
		rec       Record
		wantXID   storage.TransactionID
		committed bool
	}{
		{
			name:      "commit",
			rec:       Record{XLog: &XLogDecodedRecord{Header: XLogRecord{Rmid: RmgrXact, Info: xlogXactCommit, XID: 77}}},
			wantXID:   77,
			committed: true,
		},
		{
			name:      "abort",
			rec:       Record{XLog: &XLogDecodedRecord{Header: XLogRecord{Rmid: RmgrXact, Info: xlogXactAbort, XID: 88}}},
			wantXID:   88,
			committed: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xid, committed, ok := replayedXactInfo(tt.rec)
			if !ok {
				t.Fatal("ok=false, want true")
			}
			if xid != tt.wantXID {
				t.Fatalf("xid = %d, want %d", xid, tt.wantXID)
			}
			if committed != tt.committed {
				t.Fatalf("committed = %v, want %v", committed, tt.committed)
			}
		})
	}
}

func testXLogHeapInsertMainData(offnum uint16) []byte {
	buf := make([]byte, sizeOfXLogHeapInsertData)
	putUint16LE(buf[0:2], offnum)
	return buf
}

func testXLogHeapInsertTupleData(hoff uint8, tupleData []byte) []byte {
	prefixLen := int(hoff) - storage.SizeOfHeapTupleHeaderData
	if prefixLen < 0 {
		prefixLen = 0
	}
	return testXLogHeapInsertTupleDataWithPrefix(hoff, make([]byte, prefixLen), tupleData)
}

func testXLogHeapInsertTupleDataWithPrefix(hoff uint8, prefix, tupleData []byte) []byte {
	buf := make([]byte, sizeOfXLogHeapHeaderData+len(prefix)+len(tupleData))
	buf[4] = hoff
	copy(buf[sizeOfXLogHeapHeaderData:], prefix)
	copy(buf[sizeOfXLogHeapHeaderData+len(prefix):], tupleData)
	return buf
}

func testPhysicalPGInt4(v int32) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(v))
	return buf
}

func testPhysicalPGShortText(s string) []byte {
	buf := make([]byte, 1+len(s))
	buf[0] = byte((len(s)+1)<<1 | 0x01)
	copy(buf[1:], s)
	return buf
}

func putUint16LE(dst []byte, v uint16) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
}
