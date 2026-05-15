package wal

import (
	"testing"

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
			Header: XLogRecord{Rmid: RmgrHeap, Info: xlogHeapInsert | xlogHeapInit, XID: 42},
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
	buf := make([]byte, sizeOfXLogHeapHeaderData+len(tupleData))
	buf[4] = hoff
	copy(buf[sizeOfXLogHeapHeaderData:], tupleData)
	return buf
}

func putUint16LE(dst []byte, v uint16) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
}