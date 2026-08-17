package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

func TestSeqScanReadsPostgresPhysicalTuple(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer mgr.Close()
	defer pool.Close()

	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Schema: "public", Name: "bench_log"}, []catalog.Column{
		{Name: "client", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "src", Type: catalog.Type{Name: "text"}, NotNull: true},
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	rel := cat.RelFileNode(tbl)
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	// A real PostgreSQL physical tuple records its attribute count in
	// t_infomask2 (HeapTupleHeaderSetNatts). Set it so the header-driven
	// decoder recognises this as PG-physical rather than goopg-legacy
	// (M0111-0002: natts>0 || bitmap!=nil ⇒ PG-physical).
	physTup := storage.NewHeapTuple(storage.FrozenTransactionID, storage.InvalidTransactionID,
		encodePhysicalBenchLogRow(-999, "bootstrap"))
	physTup.Header.SetNatts(len(tbl.Columns))
	rawTuple, err := physTup.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if _, err := storage.PageAddItemRaw(page, rawTuple); err != nil {
		t.Fatalf("PageAddItemRaw: %v", err)
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatalf("Extend: %v", err)
	}

	txnMgr := transam.NewManager()
	tx, err := txnMgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	snap, err := txnMgr.SnapshotFor(tx)
	if err != nil {
		t.Fatalf("SnapshotFor: %v", err)
	}
	ctx := NewContext()
	ctx.Pool = pool
	ctx.Catalog = cat
	ctx.TxnMgr = txnMgr
	ctx.Tx = tx
	ctx.Snap = snap

	rows := runQuery(t, ctx, "SELECT src FROM public.bench_log WHERE client = -999")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "bootstrap" {
		t.Fatalf("src = %q, want bootstrap", got)
	}
}

func encodePhysicalBenchLogRow(client int32, src string) []byte {
	data := make([]byte, 0, 4+1+len(src))
	var int4buf [4]byte
	binary.LittleEndian.PutUint32(int4buf[:], uint32(client))
	data = append(data, int4buf[:]...)
	data = append(data, encodePhysicalShortText(src)...)
	return data
}

func encodePhysicalShortText(s string) []byte {
	raw := []byte(s)
	total := len(raw) + 1
	if total < 0x80 {
		out := make([]byte, 1+len(raw))
		out[0] = byte((total << 1) | 0x01)
		copy(out[1:], raw)
		return out
	}
	// Fallback to a regular 4-byte varlena header for longer test strings.
	out := make([]byte, 4+len(raw))
	binary.LittleEndian.PutUint32(out[:4], uint32((len(raw)+4)<<2))
	copy(out[4:], raw)
	return out
}
