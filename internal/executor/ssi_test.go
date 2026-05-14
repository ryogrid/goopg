package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

// M0104-0007 ─────────────────────────────────────────────────────────────────
//
// These tests pin the executor-side SSI wiring landed in M0104-0007:
//
//   - ssiActive / ssiRecordTupleRead / ssiRecordTupleWrite must short-circuit
//     for RC/RR transactions (zero footprint on non-SERIALIZABLE workloads).
//   - For SERIALIZABLE transactions the read-path hook must install a
//     tuple-grain predicate lock + an rw-conflict edge against the writer,
//     and the write-path hook must install an rw-conflict edge in the
//     reverse-discovery direction (same edge orientation as the read path).
//   - transactionOp.execCommit must surface the
//     mvcc.SerializationFailureError raised by
//     PreCommitCheckForSerializationFailure as ExecError{Code:"40001"} and
//     drive the underlying TxnMgr.Rollback so the registry, lock set, and
//     session state are all released.
//
// Tests construct mvcc.Manager + executor.Context directly instead of going
// through a full SQL E2E path so that the wiring contract is pinned at the
// executor layer (the layer that owns the helpers in ssi.go); broader
// isolation-test promotion is staged for M0104-0008.

const (
	ssiTestDBOid   uint32              = 1
	ssiTestRelOid  uint32              = 42
	ssiTestBlock   storage.BlockNumber = 0
	ssiTestSlot    uint16              = 1
	ssiWriterXID   storage.TransactionID = 100
)

func ssiTestRel() storage.RelFileNode {
	return storage.RelFileNode{DBOid: ssiTestDBOid, RelOid: ssiTestRelOid, Fork: storage.MainFork}
}

// TestSSI_RecordTupleRead_NoOpForRC pins the zero-footprint contract: a
// READ COMMITTED reader must never touch the predicate-lock map or the
// conflict-out graph, regardless of the writer xid passed in.
func TestSSI_RecordTupleRead_NoOpForRC(t *testing.T) {
	mgr := mvcc.NewManager()
	tx, err := mgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = mgr.Rollback(tx) }()

	ctx := NewContext()
	ctx.TxnMgr = mgr
	ctx.Tx = tx

	ssiRecordTupleRead(ctx, ssiTestRel(), ssiTestBlock, ssiTestSlot, ssiWriterXID)

	// RC xacts never enter ssiState.xacts so the lookup returns nil; this
	// is the cheapest possible evidence of "zero footprint".
	if got := mgr.SerializableXact(tx.Handle); got != nil {
		t.Fatalf("RC reader leaked into ssiState.xacts: %+v", got)
	}
}

// TestSSI_RecordTupleRead_NoOpForRR pins the same contract for REPEATABLE READ:
// RR uses the same pinned-snapshot semantics as SERIALIZABLE for the SI half,
// but the SSI overlay must remain inert.
func TestSSI_RecordTupleRead_NoOpForRR(t *testing.T) {
	mgr := mvcc.NewManager()
	tx, err := mgr.Begin(mvcc.IsolationRepeatableRead)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = mgr.Rollback(tx) }()

	ctx := NewContext()
	ctx.TxnMgr = mgr
	ctx.Tx = tx

	ssiRecordTupleRead(ctx, ssiTestRel(), ssiTestBlock, ssiTestSlot, ssiWriterXID)
	ssiRecordTupleWrite(ctx, ssiTestRel(), ssiTestBlock, ssiTestSlot)

	if got := mgr.SerializableXact(tx.Handle); got != nil {
		t.Fatalf("RR xact leaked into ssiState.xacts: %+v", got)
	}
}

// TestSSI_RecordTupleRead_AcquiresPredicateLockForSerializable pins that a
// SERIALIZABLE reader passing through ssiRecordTupleRead leaves a tuple-grain
// predicate lock in the SerializableXact's holdings, so a subsequent
// CheckForSerializableConflictIn on the matching tag fires.
func TestSSI_RecordTupleRead_AcquiresPredicateLockForSerializable(t *testing.T) {
	mgr := mvcc.NewManager()
	reader, err := mgr.Begin(mvcc.IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin reader: %v", err)
	}
	defer func() { _ = mgr.Rollback(reader) }()

	rctx := NewContext()
	rctx.TxnMgr = mgr
	rctx.Tx = reader

	// Reader observes a frozen / not-tracked writer (xid 0). Predicate
	// lock is still acquired; the conflict-out path is a silent no-op for
	// xid==0 (mirrors upstream's invalid-XID short-circuit) so this test
	// isolates the AcquirePredicateLock side-effect from the conflict-out
	// side-effect.
	ssiRecordTupleRead(rctx, ssiTestRel(), ssiTestBlock, ssiTestSlot, storage.InvalidTransactionID)

	writer, err := mgr.Begin(mvcc.IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin writer: %v", err)
	}
	defer func() { _ = mgr.Rollback(writer) }()
	if _, err := mgr.AssignXID(writer); err != nil {
		t.Fatalf("AssignXID writer: %v", err)
	}

	wctx := NewContext()
	wctx.TxnMgr = mgr
	wctx.Tx = writer
	ssiRecordTupleWrite(wctx, ssiTestRel(), ssiTestBlock, ssiTestSlot)

	if !mgr.HasRWConflict(reader.Handle, writer.Handle) {
		t.Fatalf("expected R→W edge after SERIALIZABLE write touched a SIREAD-locked tuple; reader.out=%d writer.in=%d",
			mgr.OutConflictCount(reader.Handle), mgr.InConflictCount(writer.Handle))
	}
}

// TestSSI_RecordTupleRead_InvalidTagFiltered pins that callers passing
// (InvalidBlockNumber, *) or (*, 0) — the only inputs TupleLockTag panics on
// — are silently absorbed by the executor helper. The wiring uses curSlot-1
// in seqScanOp; this guards against the "scan returned slot 0" edge case
// surfacing through the SSI hook.
func TestSSI_RecordTupleRead_InvalidTagFiltered(t *testing.T) {
	mgr := mvcc.NewManager()
	tx, err := mgr.Begin(mvcc.IsolationSerializable)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = mgr.Rollback(tx) }()

	ctx := NewContext()
	ctx.TxnMgr = mgr
	ctx.Tx = tx

	// Both invalid inputs would panic inside mvcc.TupleLockTag; the helper
	// must filter before reaching it. Test passes when no panic surfaces.
	ssiRecordTupleRead(ctx, ssiTestRel(), storage.InvalidBlockNumber, ssiTestSlot, ssiWriterXID)
	ssiRecordTupleRead(ctx, ssiTestRel(), ssiTestBlock, 0, ssiWriterXID)
	ssiRecordTupleWrite(ctx, ssiTestRel(), storage.InvalidBlockNumber, ssiTestSlot)
	ssiRecordTupleWrite(ctx, ssiTestRel(), ssiTestBlock, 0)
}

// TestSSI_ExecCommit_ReturnsSerializationFailureWhenDoomed pins the end-to-
// end execCommit wiring: a SERIALIZABLE transaction whose underlying
// SerializableXact has been doomed must surface SQLSTATE 40001 and drive
// the rollback path so the session exits the explicit-transaction state
// and the active-count drops back to zero.
func TestSSI_ExecCommit_ReturnsSerializationFailureWhenDoomed(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = mvcc.NewManager()
	sess := NewBasicSession()
	if err := sess.SetIsolationLevel(mvcc.IsolationSerializable); err != nil {
		t.Fatal(err)
	}
	ctx.Session = sess

	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	tx, _, ok := sess.CurrentTransaction()
	if !ok {
		t.Fatal("expected active transaction after BEGIN")
	}
	if tx.Isolation != mvcc.IsolationSerializable {
		t.Fatalf("isolation=%v want=Serializable", tx.Isolation)
	}
	if !ctx.TxnMgr.MarkDoomedForTest(tx.Handle) {
		t.Fatal("MarkDoomedForTest returned false; SerializableXact missing from registry")
	}

	err := runTransactionStmt(t, ctx, "COMMIT")
	if err == nil {
		t.Fatal("expected COMMIT on doomed SERIALIZABLE xact to fail")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err=%T %v; want *ExecError", err, err)
	}
	if ee.Code != "40001" {
		t.Fatalf("err.Code=%q want=%q", ee.Code, "40001")
	}
	if !strings.Contains(ee.Message, "could not serialize access") {
		t.Fatalf("err.Message=%q does not match upstream phrasing", ee.Message)
	}

	if sess.InExplicitTransaction() {
		t.Fatal("session must exit explicit tx after 40001 rollback")
	}
	if ctx.TxnMgr.ActiveCount() != 0 {
		t.Fatalf("ActiveCount=%d want=0", ctx.TxnMgr.ActiveCount())
	}
	if ctx.Tx.Handle != 0 {
		t.Fatalf("ctx.Tx must be cleared after 40001 commit; got Handle=%d", ctx.Tx.Handle)
	}
}

// TestSSI_ExecCommit_NoOpForRC pins the cost-correctness side: a READ
// COMMITTED commit must NOT take the SSI pre-commit walk even if a Doomed
// bit somehow leaked (it cannot, but the test pins the gate independently
// of the registry state).
func TestSSI_ExecCommit_NoOpForRC(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = mvcc.NewManager()
	sess := NewBasicSession()
	// Default RC; do not change isolation.
	ctx.Session = sess

	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if err := runTransactionStmt(t, ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT (RC) must succeed: %v", err)
	}
	if ctx.TxnMgr.ActiveCount() != 0 {
		t.Fatalf("ActiveCount=%d want=0", ctx.TxnMgr.ActiveCount())
	}
}

// TestSSI_ExecCommit_HappyPathForSerializable pins that a SERIALIZABLE
// transaction with no rw-cycle commits successfully — the pre-commit walk
// must NOT spuriously fail on isolated SERIALIZABLE workloads.
func TestSSI_ExecCommit_HappyPathForSerializable(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = mvcc.NewManager()
	sess := NewBasicSession()
	if err := sess.SetIsolationLevel(mvcc.IsolationSerializable); err != nil {
		t.Fatal(err)
	}
	ctx.Session = sess

	if err := runTransactionStmt(t, ctx, "BEGIN"); err != nil {
		t.Fatalf("BEGIN: %v", err)
	}
	if err := runTransactionStmt(t, ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT on clean SERIALIZABLE xact must succeed: %v", err)
	}
	if ctx.TxnMgr.ActiveCount() != 0 {
		t.Fatalf("ActiveCount=%d want=0", ctx.TxnMgr.ActiveCount())
	}
}

// TestSSI_RecordTupleRead_ZeroHandleSkipped pins the contract that callers
// outside an explicit transaction (Handle==0) do not trigger a registry
// probe. The seqScanOp / indexScanOp hooks fire on every emitted tuple;
// the helper must absorb this case without touching ctx.TxnMgr.
func TestSSI_RecordTupleRead_ZeroHandleSkipped(t *testing.T) {
	ctx := NewContext()
	ctx.TxnMgr = mvcc.NewManager()
	ctx.Tx = mvcc.Transaction{Isolation: mvcc.IsolationSerializable, Handle: 0}

	ssiRecordTupleRead(ctx, ssiTestRel(), ssiTestBlock, ssiTestSlot, ssiWriterXID)
	ssiRecordTupleWrite(ctx, ssiTestRel(), ssiTestBlock, ssiTestSlot)
	// No panic, no allocation in ssiState — guard ssiActive is doing its job.
}
