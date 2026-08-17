package postmaster

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/storage"
)

// M0132-S8 — mixed simple↔extended blocks.
//
// The shape real drivers emit (pgx v5 `conn.go:515` "Always use simple protocol
// when there are no arguments"; lib/pq `conn.go:901` `if len(args) == 0 {
// simpleQuery }`) is MIXED: argument-less `BEGIN`/`COMMIT`/`ROLLBACK` go down the
// SIMPLE path while parameterised DML goes down the EXTENDED path. Before S2–S7
// that produced TWO live transactions on one connection (the block on the
// connection's own slot, each `Execute` auto-committing its own on the offset
// slot), so the client's writes landed in the auto-committing one and the
// `ROLLBACK` discarded an empty block. This slice pins, at the wire level, that
// the two protocols now form ONE block.
//
// The engine work landed in S2–S7; these tests are the verification. (a) is the
// structural "one live transaction" assertion (offset slot never touched by an
// in-block Execute); (b)–(d) are the behavioural halves.

// mixedExtendedInsert sends one parameterised INSERT over the EXTENDED protocol
// (the shape drivers use for DML that carries arguments), on conn, draining to
// ReadyForQuery.
func mixedExtendedInsert(t *testing.T, conn net.Conn, r *libpq.FrameReader, name, id, label string) []libpq.Frame {
	t.Helper()
	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload(name, "INSERT INTO items VALUES ($1, $2)", nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", name, nil, []bindParam{{value: id}, {value: label}}, nil))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)
	return drainToReady(t, r)
}

// mixedExtendedNullInsert is the parameterised INSERT with a NULL id (a NOT NULL
// violation) over the extended protocol — the in-block error half of (d).
func mixedExtendedNullInsert(t *testing.T, conn net.Conn, r *libpq.FrameReader, name string) []libpq.Frame {
	t.Helper()
	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload(name, "INSERT INTO items VALUES ($1, $2)", nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", name, nil, []bindParam{{isNull: true}, {value: "x"}}, nil))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)
	return drainToReady(t, r)
}

// --- (a)+(b): ONE live transaction, and ROLLBACK on either protocol ---------

// TestM0132S8_MixedBlockOneTransactionRollback is the canonical mixed shape, the
// acceptance-bar-7 / bar-2b assertion: simple `BEGIN`, parameterised INSERT over
// the extended protocol, simple `ROLLBACK`. The INSERT must be invisible to a
// second connection until COMMIT (proving the extended Execute joined the block
// instead of auto-committing its own transaction), self-visible on the same
// connection (proving it ran in the block's transaction), and gone after ROLLBACK.
func TestM0132S8_MixedBlockOneTransactionRollback(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	c1 := dialAndComplete(t, addr)
	defer c1.Close()
	r1 := extendedReader(t, c1)
	c2 := dialAndComplete(t, addr)
	defer c2.Close()
	r2 := extendedReader(t, c2)

	if f := simpleStmt(t, c1, r1, "BEGIN"); hasError(f) {
		t.Fatalf("simple BEGIN errored: %+v", f)
	}
	if f := mixedExtendedInsert(t, c1, r1, "m_ins", "1", "one"); hasError(f) {
		t.Fatalf("extended INSERT errored: %+v", f)
	}

	// ONE live transaction: invisible to a second connection before COMMIT.
	if got := countItems(t, c2, r2); got != 0 {
		t.Errorf("mixed block INSERT visible to a second connection before COMMIT: %d rows, want 0", got)
	}
	// ... but self-visible (the block's earlier write).
	if got := countItems(t, c1, r1); got != 1 {
		t.Errorf("mixed block INSERT not self-visible: %d rows, want 1", got)
	}

	if f := simpleStmt(t, c1, r1, "ROLLBACK"); hasError(f) {
		t.Fatalf("simple ROLLBACK errored: %+v", f)
	}
	if got := countItems(t, c2, r2); got != 0 {
		t.Errorf("after mixed ROLLBACK: %d rows, want 0", got)
	}
}

// TestM0132S8_MixedRollbackMirror is the mirror shape: `BEGIN` over the extended
// protocol, DML over the SIMPLE path, `ROLLBACK` over the extended protocol. It
// proves the two protocols compose symmetrically, not just in the pgx/lib/pq
// direction.
func TestM0132S8_MixedRollbackMirror(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	c1 := dialAndComplete(t, addr)
	defer c1.Close()
	r1 := extendedReader(t, c1)
	c2 := dialAndComplete(t, addr)
	defer c2.Close()
	r2 := extendedReader(t, c2)

	if f := extendedStmt(t, c1, r1, "m_begin", "BEGIN"); hasError(f) {
		t.Fatalf("extended BEGIN errored: %+v", f)
	}
	if f := simpleStmt(t, c1, r1, "INSERT INTO items VALUES (2, 'two')"); hasError(f) {
		t.Fatalf("simple INSERT errored: %+v", f)
	}
	if got := countItems(t, c2, r2); got != 0 {
		t.Errorf("mirror mixed block visible to a second connection before COMMIT: %d rows, want 0", got)
	}
	if f := extendedStmt(t, c1, r1, "m_rb", "ROLLBACK"); hasError(f) {
		t.Fatalf("extended ROLLBACK errored: %+v", f)
	}
	if got := countItems(t, c2, r2); got != 0 {
		t.Errorf("after mirror mixed ROLLBACK: %d rows, want 0", got)
	}
}

// --- (b): COMMIT on either protocol ----------------------------------------

// TestM0132S8_MixedCommitBothDirections runs both directions and asserts the
// committed row becomes visible after the cross-protocol COMMIT.
func TestM0132S8_MixedCommitBothDirections(t *testing.T) {
	// Direction 1: simple BEGIN → extended INSERT → simple COMMIT.
	t.Run("simple-begin-extended-dml-simple-commit", func(t *testing.T) {
		addr, _, stop := startCopyExecServer(t)
		defer stop()
		c1 := dialAndComplete(t, addr)
		defer c1.Close()
		r1 := extendedReader(t, c1)
		c2 := dialAndComplete(t, addr)
		defer c2.Close()
		r2 := extendedReader(t, c2)

		if f := simpleStmt(t, c1, r1, "BEGIN"); hasError(f) {
			t.Fatalf("BEGIN errored: %+v", f)
		}
		if f := mixedExtendedInsert(t, c1, r1, "c_ins", "1", "one"); hasError(f) {
			t.Fatalf("extended INSERT errored: %+v", f)
		}
		if f := simpleStmt(t, c1, r1, "COMMIT"); hasError(f) {
			t.Fatalf("COMMIT errored: %+v", f)
		}
		if got := countItems(t, c2, r2); got != 1 {
			t.Errorf("after mixed COMMIT: %d rows, want 1", got)
		}
	})

	// Direction 2 (mirror): extended BEGIN → simple INSERT → extended COMMIT.
	t.Run("extended-begin-simple-dml-extended-commit", func(t *testing.T) {
		addr, _, stop := startCopyExecServer(t)
		defer stop()
		c1 := dialAndComplete(t, addr)
		defer c1.Close()
		r1 := extendedReader(t, c1)
		c2 := dialAndComplete(t, addr)
		defer c2.Close()
		r2 := extendedReader(t, c2)

		if f := extendedStmt(t, c1, r1, "m_begin", "BEGIN"); hasError(f) {
			t.Fatalf("extended BEGIN errored: %+v", f)
		}
		if f := simpleStmt(t, c1, r1, "INSERT INTO items VALUES (2, 'two')"); hasError(f) {
			t.Fatalf("simple INSERT errored: %+v", f)
		}
		if f := extendedStmt(t, c1, r1, "m_commit", "COMMIT"); hasError(f) {
			t.Fatalf("extended COMMIT errored: %+v", f)
		}
		if got := countItems(t, c2, r2); got != 1 {
			t.Errorf("after mirror mixed COMMIT: %d rows, want 1", got)
		}
	})
}

// --- (c): status byte is coherent across the protocol switch ---------------

// TestM0132S8_StatusByteCoherentAcrossSwitch pins that the ReadyForQuery status
// byte stays 'T' across a simple↔extended switch and returns to 'I' after the
// cross-protocol COMMIT, in both directions.
func TestM0132S8_StatusByteCoherentAcrossSwitch(t *testing.T) {
	t.Run("simple-begin-extended-dml-simple-commit", func(t *testing.T) {
		addr, _, stop := startCopyExecServer(t)
		defer stop()
		c1 := dialAndComplete(t, addr)
		defer c1.Close()
		r1 := extendedReader(t, c1)

		if st := readyStatus(t, simpleStmt(t, c1, r1, "BEGIN")); st != byte(libpq.TxStatusInTransaction) {
			t.Fatalf("status after simple BEGIN = %q, want 'T'", st)
		}
		if st := readyStatus(t, mixedExtendedInsert(t, c1, r1, "s_ins", "1", "one")); st != byte(libpq.TxStatusInTransaction) {
			t.Errorf("status after in-block extended INSERT = %q, want 'T'", st)
		}
		if st := readyStatus(t, simpleStmt(t, c1, r1, "COMMIT")); st != byte(libpq.TxStatusIdle) {
			t.Errorf("status after simple COMMIT = %q, want 'I'", st)
		}
	})

	t.Run("extended-begin-simple-dml-extended-commit", func(t *testing.T) {
		addr, _, stop := startCopyExecServer(t)
		defer stop()
		c1 := dialAndComplete(t, addr)
		defer c1.Close()
		r1 := extendedReader(t, c1)

		if st := readyStatus(t, extendedStmt(t, c1, r1, "s_begin", "BEGIN")); st != byte(libpq.TxStatusInTransaction) {
			t.Fatalf("status after extended BEGIN = %q, want 'T'", st)
		}
		if st := readyStatus(t, simpleStmt(t, c1, r1, "INSERT INTO items VALUES (2, 'two')")); st != byte(libpq.TxStatusInTransaction) {
			t.Errorf("status after in-block simple INSERT = %q, want 'T'", st)
		}
		if st := readyStatus(t, extendedStmt(t, c1, r1, "s_commit", "COMMIT")); st != byte(libpq.TxStatusIdle) {
			t.Errorf("status after extended COMMIT = %q, want 'I'", st)
		}
	})
}

// --- (d): an in-block error on one protocol aborts the other ---------------

// TestM0132S8_InBlockErrorAbortsOtherProtocol pins that a statement error inside
// a block fails the block, so a statement on the OTHER protocol is rejected with
// 25P02 until ROLLBACK — in both directions.
func TestM0132S8_InBlockErrorAbortsOtherProtocol(t *testing.T) {
	t.Run("extended-error-aborts-simple", func(t *testing.T) {
		addr, _, stop := startCopyExecServer(t)
		defer stop()
		c1 := dialAndComplete(t, addr)
		defer c1.Close()
		r1 := extendedReader(t, c1)

		if f := simpleStmt(t, c1, r1, "BEGIN"); hasError(f) {
			t.Fatalf("BEGIN errored: %+v", f)
		}
		bad := mixedExtendedNullInsert(t, c1, r1, "e_ins")
		if !hasError(bad) {
			t.Fatal("extended INSERT of NULL id did not error")
		}
		if st := readyStatus(t, bad); st != byte(libpq.TxStatusInFailedTransaction) {
			t.Fatalf("status after in-block extended error = %q, want 'E'", st)
		}
		if ctl := simpleStmt(t, c1, r1, "INSERT INTO items VALUES (4, 'd')"); !errorContains(ctl, "25P02") {
			t.Errorf("simple INSERT after an extended in-block error: want 25P02, got %+v", ctl)
		}
		if f := simpleStmt(t, c1, r1, "ROLLBACK"); hasError(f) {
			t.Fatalf("ROLLBACK errored: %+v", f)
		}
	})

	t.Run("simple-error-aborts-extended", func(t *testing.T) {
		addr, _, stop := startCopyExecServer(t)
		defer stop()
		c1 := dialAndComplete(t, addr)
		defer c1.Close()
		r1 := extendedReader(t, c1)

		if f := extendedStmt(t, c1, r1, "e_begin", "BEGIN"); hasError(f) {
			t.Fatalf("extended BEGIN errored: %+v", f)
		}
		bad := simpleStmt(t, c1, r1, "INSERT INTO items VALUES (NULL, 'x')")
		if !hasError(bad) {
			t.Fatal("simple INSERT of NULL id did not error")
		}
		if st := readyStatus(t, bad); st != byte(libpq.TxStatusInFailedTransaction) {
			t.Fatalf("status after in-block simple error = %q, want 'E'", st)
		}
		if ctl := mixedExtendedInsert(t, c1, r1, "e_ins2", "4", "d"); !errorContains(ctl, "25P02") {
			t.Errorf("extended INSERT after a simple in-block error: want 25P02, got %+v", ctl)
		}
		if f := extendedStmt(t, c1, r1, "e_rb", "ROLLBACK"); hasError(f) {
			t.Fatalf("extended ROLLBACK errored: %+v", f)
		}
	})
}

// --- (a) structural: the in-block Execute never touches the offset slot -----

// TestM0132S8_MixedInBlockExecuteLeavesOffsetSlotAlone is the structural half of
// (a), the twin of TestM0132S7_ExtendedAutocommitUsesOwnSlot. S7 proved the
// OUT-of-block Execute lands on the connection's OWN slot; this test proves the
// IN-block Execute (the mixed shape) does not allocate a second transaction at
// all — specifically, it never clobbers the historical offset slot. It reserves
// that slot with a live transaction, runs a mixed block (simple BEGIN + one
// extended Execute), and asserts the reserved transaction survived.
func TestM0132S8_MixedInBlockExecuteLeavesOffsetSlotAlone(t *testing.T) {
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
	mvccMgr := transam.NewManager()
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

	// Reserve the offset slot of the connection's own slot with a live
	// transaction, modelling "another connection's transaction" the offset
	// scheme would clobber. Mirror TestM0132S7's reservation of the first
	// three own slots to be robust to AcquireConnSlot's rotating cursor.
	const halfSize = transam.ConnSlotCount / 2
	reserved := make(map[int32]transam.Transaction, 3)
	for _, own := range []int32{1, 2, 3} {
		off := (own + halfSize) % transam.ConnSlotCount
		tx, err := mvccMgr.Begin(transam.IsolationReadCommitted, off)
		if err != nil {
			t.Fatalf("reserve offset slot %d: %v", off, err)
		}
		reserved[off] = tx
	}

	conn := dialAndComplete(t, srv.Addr().String())
	defer conn.Close()
	r := extendedReader(t, conn)

	// Mixed block: simple BEGIN, one in-block extended Execute.
	if f := simpleStmt(t, conn, r, "BEGIN"); hasError(f) {
		t.Fatalf("simple BEGIN errored: %+v", f)
	}
	if f := mixedExtendedInsert(t, conn, r, "s8_ins", "1", "one"); hasError(f) {
		t.Fatalf("in-block extended INSERT errored: %+v", f)
	}
	if f := simpleStmt(t, conn, r, "ROLLBACK"); hasError(f) {
		t.Fatalf("simple ROLLBACK errored: %+v", f)
	}

	for off, tx := range reserved {
		if _, err := mvccMgr.SnapshotFor(tx); err != nil {
			t.Errorf("reserved transaction on slot %d was clobbered by the in-block extended Execute: %v", off, err)
		}
	}
}
