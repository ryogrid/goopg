package server

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestM0132S7_ExtendedAutocommitUsesOwnSlot pins the proc-slot discipline that
// doc 09 §5 I3 found broken: an out-of-block extended `Execute` must begin its
// autocommit transaction on the connection's OWN ProcArray slot, not on the
// historical `(procNum + halfSize) % ConnSlotCount` offset.
//
// The offset was a bijection onto the connection region, so it deterministically
// landed the autocommit transaction on a DIFFERENT connection's own slot; when
// that other connection ran any transaction on its own slot (simple path, a
// block, or a `BEGIN ISOLATION LEVEL …` re-begin) the two shared a slot, and the
// first to finish cleared `inTxn` underneath the second → `mvcc: unknown
// transaction` from SnapshotFor/AssignXID/finish.
//
// The test reproduces the collision directly: it reserves the connection's
// offset slot with a live transaction (the "other connection"), runs one
// out-of-block extended Execute, and asserts the reserved transaction survived.
// At HEAD the Execute clobbered it (red); after M0132-S7 it lands on the
// connection's own slot (green).
func TestM0132S7_ExtendedAutocommitUsesOwnSlot(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	mvccMgr := mvcc.NewManager()
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Catalog:          cat,
		Pool:             pool,
		TxnMgr:           mvccMgr,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Server.Run did not return within 2s of cancel")
		}
		_ = pool.Close()
		_ = mgr.Close()
	}()

	// A fresh Manager hands the first connection slot 2 (AcquireConnSlot's
	// rotating cursor starts at 1 → i = 1 + 1%959 = 2); reserve the offsets of
	// slots 1..3 so the assertion is robust to that detail. Each reserved
	// transaction models "another connection's live transaction on its own
	// slot" that the offset scheme would clobber.
	const halfSize = mvcc.ConnSlotCount / 2
	reserved := make(map[int32]mvcc.Transaction, 3)
	for _, own := range []int32{1, 2, 3} {
		off := (own + halfSize) % mvcc.ConnSlotCount
		tx, err := mvccMgr.Begin(mvcc.IsolationReadCommitted, off)
		if err != nil {
			t.Fatalf("reserve offset slot %d: %v", off, err)
		}
		reserved[off] = tx
	}

	conn := dialAndComplete(t, srv.Addr().String())
	defer conn.Close()
	r := extendedReader(t, conn)

	if f := extendedStmt(t, conn, r, "s7_sel", "SELECT * FROM items"); hasError(f) {
		t.Fatalf("out-of-block extended SELECT errored: %+v", f)
	}

	for off, tx := range reserved {
		if _, err := mvccMgr.SnapshotFor(tx); err != nil {
			t.Errorf("reserved transaction on slot %d was clobbered by the out-of-block extended Execute: %v", off, err)
		}
	}
}
