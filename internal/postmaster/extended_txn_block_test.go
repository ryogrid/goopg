package postmaster

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/libpq"
)

// M0132-S1 — the acceptance bar for "explicit transactions across the extended
// query protocol", written BEFORE the behaviour exists.
//
// Today the extended path (Parse/Bind/Execute) begins and commits its own
// READ COMMITTED transaction on EVERY Execute and treats the client's
// BEGIN/COMMIT/ROLLBACK as accepted-but-ignored command tags
// (`dispatch_extended.go`: the `*planner.Transaction` short-circuit, the
// unconditional `TxnMgr.Begin` on the offset proc slot, the unconditional
// commit at the end). So `BEGIN → UPDATE → ROLLBACK` has already durably
// committed the update by the time the ROLLBACK arrives, and ReadyForQuery
// reports `I` where PostgreSQL reports `T`.
//
// Each test below runs one scenario and then asserts EITHER the PostgreSQL
// behaviour (once M0132-S2..S5 land) OR today's divergence — selected by the
// single const below. This shape is deliberate and is the whole point of the
// slice:
//
//   - at HEAD the suite is GREEN while still executing every scenario, so the
//     bar is committable ahead of the fix;
//   - flipping the const to true turns every divergence bar RED at HEAD, which
//     is how the redness was proven for this slice (6 of 8 tests fail; the two
//     that stay green are the no-divergence guards —
//     TestM0132S1_ExtendedCommitPersistsBlockWork and
//     TestM0132S1_SyncDoesNotEndAnOpenBlock);
//   - when S2..S5 land, the `else` arms start FAILING because the divergence
//     they pin has disappeared. The fix therefore cannot land without flipping
//     the const, and the const cannot be flipped without the fix.
//
// Do not "fix" a failing divergence arm by relaxing it — flip the const.
// Flipped to true by M0132-S2..S5 (2026-08-13): the extended path now drives
// the shared transaction-verb state machine, joins an open block instead of
// beginning its own transaction, marks the block failed on error, and gates
// aborted blocks ahead of the constant-answer fast paths on BOTH protocols.
const m0132ExtendedBlocksLanded = true

// --- helpers -------------------------------------------------------------

// extendedReader is one FrameReader per connection. The package's
// readUntilReady() builds a fresh (buffered) reader per call, which is safe
// only because the server never writes ahead of a request; these tests
// interleave simple and extended traffic on one connection, so they keep a
// single reader instead.
func extendedReader(t *testing.T, conn net.Conn) *libpq.FrameReader {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	return libpq.NewFrameReader(conn)
}

// drainToReady reads frames until (and including) ReadyForQuery.
func drainToReady(t *testing.T, r *libpq.FrameReader) []libpq.Frame {
	t.Helper()
	var frames []libpq.Frame
	for {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		cp := make([]byte, len(f.Payload))
		copy(cp, f.Payload)
		frames = append(frames, libpq.Frame{Type: f.Type, Payload: cp})
		if f.Type == libpq.MsgReadyForQuery {
			return frames
		}
	}
}

// readyStatus returns the transaction-status byte of the trailing
// ReadyForQuery frame ('I' idle, 'T' in a block, 'E' in a failed block).
func readyStatus(t *testing.T, frames []libpq.Frame) byte {
	t.Helper()
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].Type == libpq.MsgReadyForQuery {
			if len(frames[i].Payload) != 1 {
				t.Fatalf("ReadyForQuery payload = %v, want 1 byte", frames[i].Payload)
			}
			return frames[i].Payload[0]
		}
	}
	t.Fatal("no ReadyForQuery frame")
	return 0
}

// simpleStmt runs one statement down the SIMPLE query path — the path pgx and
// lib/pq use for argument-less statements, BEGIN/COMMIT/ROLLBACK included.
func simpleStmt(t *testing.T, conn net.Conn, r *libpq.FrameReader, sql string) []libpq.Frame {
	t.Helper()
	writeQuery(t, conn, sql)
	return drainToReady(t, r)
}

// extendedStmt runs one statement down the EXTENDED path as a full
// Parse/Bind/Execute/Sync round trip, using a distinct prepared-statement name
// so repeated calls on one connection do not collide.
func extendedStmt(t *testing.T, conn net.Conn, r *libpq.FrameReader, name, sql string) []libpq.Frame {
	t.Helper()
	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload(name, sql, nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", name, nil, nil, nil))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)
	return drainToReady(t, r)
}

func hasError(frames []libpq.Frame) bool {
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			return true
		}
	}
	return false
}

func errorContains(frames []libpq.Frame, want string) bool {
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse && strings.Contains(string(f.Payload), want) {
			return true
		}
	}
	return false
}

// countItems counts rows in the seeded `items` table over the simple path.
func countItems(t *testing.T, conn net.Conn, r *libpq.FrameReader) int {
	t.Helper()
	frames := simpleStmt(t, conn, r, "SELECT * FROM items")
	n := 0
	for _, f := range frames {
		if f.Type == libpq.MsgErrorResponse {
			t.Fatalf("SELECT items: ErrorResponse %q", f.Payload)
		}
		if f.Type == libpq.MsgDataRow {
			n++
		}
	}
	return n
}

// --- bar 1: a block spans several Executes, and ROLLBACK rolls back ------

// TestM0132S1_ExtendedRollbackDiscardsBlockWork drives an entire block over the
// extended protocol: BEGIN, two INSERTs, ROLLBACK. PostgreSQL discards both
// rows. goopg commits each INSERT as its own transaction and answers the
// ROLLBACK with a bare success tag, so both rows survive — atomicity across
// statements is simply absent.
func TestM0132S1_ExtendedRollbackDiscardsBlockWork(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	if f := extendedStmt(t, conn, r, "s_begin", "BEGIN"); hasError(f) {
		t.Fatalf("extended BEGIN errored: %+v", f)
	}
	if f := extendedStmt(t, conn, r, "s_i1", "INSERT INTO items VALUES (1, 'a')"); hasError(f) {
		t.Fatalf("extended INSERT #1 errored: %+v", f)
	}
	if f := extendedStmt(t, conn, r, "s_i2", "INSERT INTO items VALUES (2, 'b')"); hasError(f) {
		t.Fatalf("extended INSERT #2 errored: %+v", f)
	}
	if f := extendedStmt(t, conn, r, "s_rb", "ROLLBACK"); hasError(f) {
		t.Fatalf("extended ROLLBACK errored: %+v", f)
	}

	got := countItems(t, conn, r)
	if m0132ExtendedBlocksLanded {
		if got != 0 {
			t.Errorf("after extended BEGIN/INSERT×2/ROLLBACK: %d rows, want 0 (PG discards the block)", got)
		}
		return
	}
	if got != 2 {
		t.Errorf("divergence arm: %d rows, want 2 (today every Execute auto-commits). "+
			"If the block now rolls back, flip m0132ExtendedBlocksLanded", got)
	}
}

// TestM0132S1_ExtendedCommitPersistsBlockWork is the positive half of bar 1:
// whatever the transaction model, a committed block's rows must be visible.
// It passes today for the wrong reason (each INSERT already committed), and
// must keep passing after S2..S5 — that is exactly its value: it catches a fix
// that makes ROLLBACK work by dropping writes on the floor.
func TestM0132S1_ExtendedCommitPersistsBlockWork(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	for _, step := range []struct{ name, sql string }{
		{"c_begin", "BEGIN"},
		{"c_i1", "INSERT INTO items VALUES (1, 'a')"},
		{"c_i2", "INSERT INTO items VALUES (2, 'b')"},
		{"c_commit", "COMMIT"},
	} {
		if f := extendedStmt(t, conn, r, step.name, step.sql); hasError(f) {
			t.Fatalf("extended %q errored: %+v", step.sql, f)
		}
	}
	if got := countItems(t, conn, r); got != 2 {
		t.Errorf("after extended BEGIN/INSERT×2/COMMIT: %d rows, want 2", got)
	}
}

// --- bar 2: the status byte ---------------------------------------------

// TestM0132S1_StatusByteInExtendedBlock pins the observable protocol
// divergence drivers gate on: after a BEGIN, every ReadyForQuery must report
// 'T' until the block ends. The byte comes from connTxState.wireStatus
// (installed once per connection as w.TxStatusFn), so it starts working the
// moment the extended BEGIN calls connTx.Begin — which today it never does.
func TestM0132S1_StatusByteInExtendedBlock(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	afterBegin := readyStatus(t, extendedStmt(t, conn, r, "z_begin", "BEGIN"))
	afterStmt := readyStatus(t, extendedStmt(t, conn, r, "z_ins", "INSERT INTO items VALUES (1, 'a')"))
	afterCommit := readyStatus(t, extendedStmt(t, conn, r, "z_commit", "COMMIT"))

	if afterCommit != byte(libpq.TxStatusIdle) {
		t.Errorf("status after extended COMMIT = %q, want 'I'", afterCommit)
	}
	if m0132ExtendedBlocksLanded {
		if afterBegin != byte(libpq.TxStatusInTransaction) {
			t.Errorf("status after extended BEGIN = %q, want 'T'", afterBegin)
		}
		if afterStmt != byte(libpq.TxStatusInTransaction) {
			t.Errorf("status after in-block extended INSERT = %q, want 'T'", afterStmt)
		}
		return
	}
	if afterBegin != byte(libpq.TxStatusIdle) || afterStmt != byte(libpq.TxStatusIdle) {
		t.Errorf("divergence arm: status after BEGIN/INSERT = %q/%q, want 'I'/'I' "+
			"(today the extended BEGIN never reaches connTx.Begin). "+
			"If they now report 'T', flip m0132ExtendedBlocksLanded", afterBegin, afterStmt)
	}
}

// --- bar 2b: the MIXED driver shape (the one real clients emit) ----------

// TestM0132S1_MixedProtocolBlockRollback is acceptance bar 2b, and the shape
// that actually reaches goopg in production: pgx v5 and lib/pq send
// argument-less statements — BEGIN and ROLLBACK included — down the SIMPLE
// path while parameterised DML goes down the EXTENDED one. The result today is
// TWO live transactions on one connection: the block sits on the connection's
// own proc slot while each Execute begins and commits its own on the offset
// slot. The client's write lands in the auto-committing one, so the ROLLBACK
// discards an empty transaction.
func TestM0132S1_MixedProtocolBlockRollback(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	if f := simpleStmt(t, conn, r, "BEGIN"); hasError(f) {
		t.Fatalf("simple BEGIN errored: %+v", f)
	}
	// Parameterised DML — precisely what the drivers route to the extended path.
	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("m_ins", "INSERT INTO items VALUES ($1, $2)", nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "m_ins", nil, []bindParam{{value: "7"}, {value: "seven"}}, nil))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)
	if f := drainToReady(t, r); hasError(f) {
		t.Fatalf("extended in-block INSERT errored: %+v", f)
	}
	if f := simpleStmt(t, conn, r, "ROLLBACK"); hasError(f) {
		t.Fatalf("simple ROLLBACK errored: %+v", f)
	}

	got := countItems(t, conn, r)
	if m0132ExtendedBlocksLanded {
		if got != 0 {
			t.Errorf("mixed simple-BEGIN / extended-INSERT / simple-ROLLBACK: %d rows, want 0", got)
		}
		return
	}
	if got != 1 {
		t.Errorf("divergence arm: %d rows, want 1 (the extended INSERT commits on its own "+
			"offset-slot transaction, so the simple ROLLBACK discards nothing). "+
			"If it is now rolled back, flip m0132ExtendedBlocksLanded", got)
	}
}

// --- bar 3: aborted-block semantics --------------------------------------

// TestM0132S1_AbortedBlockRejectsLaterExecute pins PG's aborted-block rule: an
// error inside an open block fails the block, so every later statement is
// rejected with 25P02 "current transaction is aborted" until ROLLBACK, and
// ReadyForQuery reports 'E'.
//
// The block is opened over the simple path here on purpose: that path calls
// connTx.Fail() on an EXECUTOR error (dispatch.go, errQueryErrorSent) and
// enforces the 25P02 gate before dispatching each statement, so this test
// isolates the extended path's missing enforcement from the separate missing
// Fail() call site the extended path also lacks.
//
// The failure is therefore a NOT NULL violation — a genuine executor error.
// A plan-time error does NOT fail the block today; that is a second,
// simple-path divergence, pinned separately in
// TestM0132S1_PlanTimeErrorDoesNotFailTheBlock.
func TestM0132S1_AbortedBlockRejectsLaterExecute(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	if f := simpleStmt(t, conn, r, "BEGIN"); hasError(f) {
		t.Fatalf("simple BEGIN errored: %+v", f)
	}
	bad := simpleStmt(t, conn, r, "INSERT INTO items VALUES (NULL, 'x')")
	if !hasError(bad) {
		t.Fatal("INSERT of NULL into a NOT NULL column did not error")
	}
	if st := readyStatus(t, bad); st != byte(libpq.TxStatusInFailedTransaction) {
		t.Fatalf("status after in-block error = %q, want 'E'", st)
	}
	// The simple path's own gate is the control: it must already reject.
	if ctl := simpleStmt(t, conn, r, "INSERT INTO items VALUES (4, 'd')"); !errorContains(ctl, "25P02") {
		t.Fatalf("control: simple statement in a failed block was not rejected with 25P02: %+v", ctl)
	}

	after := extendedStmt(t, conn, r, "a_ins", "INSERT INTO items VALUES (1, 'a')")
	if f := simpleStmt(t, conn, r, "ROLLBACK"); hasError(f) {
		t.Fatalf("simple ROLLBACK errored: %+v", f)
	}
	rows := countItems(t, conn, r)

	if m0132ExtendedBlocksLanded {
		if !errorContains(after, "25P02") {
			t.Errorf("extended Execute in a failed block: want 25P02, got %+v", after)
		}
		if rows != 0 {
			t.Errorf("failed block wrote %d rows, want 0", rows)
		}
		return
	}
	if hasError(after) {
		t.Errorf("divergence arm: extended Execute in a failed block errored (%+v); "+
			"today it runs happily in its own transaction. Flip m0132ExtendedBlocksLanded", after)
	}
	if rows != 1 {
		t.Errorf("divergence arm: %d rows after a failed block, want 1 "+
			"(the extended INSERT committed despite the aborted block). "+
			"If it is now rejected, flip m0132ExtendedBlocksLanded", rows)
	}
}

// --- discovered by this slice: two SIMPLE-path aborted-block gaps ---------

// TestM0132S1_PlanTimeErrorDoesNotFailTheBlock records a divergence found while
// writing the bar above, on the path M0132 was not looking at. connTx.Fail()
// has exactly two call sites (dispatch.go, both on the errQueryErrorSent
// executor-error path), so an error raised BEFORE execution — a parse or plan
// error such as a reference to a missing relation — leaves the block open and
// healthy: goopg reports 'T' and happily runs the next statement. PostgreSQL
// aborts a block on ANY error inside it (xact.c AbortCurrentTransaction is
// reached from the error handler in postgres.c, not from the executor), so the
// next statement gets 25P02 and the status is 'E'.
//
// It matters to M0132-S5 rather than being a stray bug report: S5 must ADD a
// Fail() call site on the extended path, and if it copies the simple path's
// placement it inherits this hole. Ledger row 2026-08-13 / M0132-S1.
func TestM0132S1_PlanTimeErrorDoesNotFailTheBlock(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	if f := simpleStmt(t, conn, r, "BEGIN"); hasError(f) {
		t.Fatalf("simple BEGIN errored: %+v", f)
	}
	if bad := simpleStmt(t, conn, r, "INSERT INTO no_such_table_m0132 VALUES (1)"); !hasError(bad) {
		t.Fatal("INSERT into a missing table did not error")
	}
	after := simpleStmt(t, conn, r, "INSERT INTO items VALUES (9, 'i')")
	st := readyStatus(t, after)

	if m0132ExtendedBlocksLanded {
		if !errorContains(after, "25P02") {
			t.Errorf("statement after a plan-time error in a block: want 25P02, got %+v", after)
		}
		if st != byte(libpq.TxStatusInFailedTransaction) {
			t.Errorf("status after a plan-time error in a block = %q, want 'E'", st)
		}
		return
	}
	if hasError(after) || st != byte(libpq.TxStatusInTransaction) {
		t.Errorf("divergence arm: after a plan-time error the next statement gave err=%v status=%q; "+
			"today the block stays live and healthy (err=false, 'T') because connTx.Fail() is only "+
			"reached from the executor-error path. If it now aborts, flip m0132ExtendedBlocksLanded",
			hasError(after), st)
	}
}

// TestM0132S1_ConstantSelectBypassesTheAbortedBlockGate records the second gap:
// the 25P02 gate (dispatch.go, before per-statement dispatch) rejects
// `SELECT * FROM items` and `INSERT …` inside a failed block, but a
// constant-only `SELECT 1` is answered normally. PostgreSQL rejects every
// statement except COMMIT/ROLLBACK/ROLLBACK TO — a constant SELECT included.
// Same ledger row.
func TestM0132S1_ConstantSelectBypassesTheAbortedBlockGate(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	if f := simpleStmt(t, conn, r, "BEGIN"); hasError(f) {
		t.Fatalf("simple BEGIN errored: %+v", f)
	}
	if bad := simpleStmt(t, conn, r, "INSERT INTO items VALUES (NULL, 'x')"); !hasError(bad) {
		t.Fatal("NOT NULL violation did not error")
	}
	// Control: a table-touching statement IS rejected, so the block really is failed.
	if ctl := simpleStmt(t, conn, r, "SELECT * FROM items"); !errorContains(ctl, "25P02") {
		t.Fatalf("control: SELECT * in a failed block was not rejected: %+v", ctl)
	}
	konst := simpleStmt(t, conn, r, "SELECT 1")

	if m0132ExtendedBlocksLanded {
		if !errorContains(konst, "25P02") {
			t.Errorf("`SELECT 1` in a failed block: want 25P02, got %+v", konst)
		}
		return
	}
	if hasError(konst) {
		t.Errorf("divergence arm: `SELECT 1` in a failed block was rejected (%+v); today it is "+
			"answered normally. If the gate now covers it, flip m0132ExtendedBlocksLanded", konst)
	}
}

// --- bar 4 (M0132-S4): the deferred sequence reaches the extended COMMIT ---

// TestM0132S4_ExtendedCommitRunsDeferredFKChecks is M0132-S4's direct probe:
// a constraint DEFERRABLE INITIALLY DEFERRED is checked at COMMIT, and that
// check lives in the simple path's inline verb arm — the arm S2 extracted into
// applyTransactionVerb precisely so the extended COMMIT would inherit it
// rather than re-derive it. If the extraction were unfaithful, the block below
// would commit a dangling child row instead of raising 23503.
//
// It complements, rather than replaces, S4's stated gates (the FK isolation
// specs and internal/testport's deferred-constraint tests), which exercise the
// same sequence through the real client stack.
func TestM0132S4_ExtendedCommitRunsDeferredFKChecks(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	for _, ddl := range []string{
		"CREATE TABLE m0132_parent (id int4 PRIMARY KEY)",
		"CREATE TABLE m0132_child (id int4, pid int4 REFERENCES m0132_parent(id) DEFERRABLE INITIALLY DEFERRED)",
	} {
		if f := simpleStmt(t, conn, r, ddl); hasError(f) {
			t.Fatalf("%s errored: %+v", ddl, f)
		}
	}

	if f := extendedStmt(t, conn, r, "d_begin", "BEGIN"); hasError(f) {
		t.Fatalf("extended BEGIN errored: %+v", f)
	}
	// The violation is invisible until COMMIT: the FK is deferred.
	if f := extendedStmt(t, conn, r, "d_ins", "INSERT INTO m0132_child VALUES (1, 99)"); hasError(f) {
		t.Fatalf("deferred FK violated at INSERT time (it must not be checked until COMMIT): %+v", f)
	}
	commitFrames := extendedStmt(t, conn, r, "d_commit", "COMMIT")
	if !errorContains(commitFrames, "23503") {
		t.Fatalf("extended COMMIT of a deferred FK violation: want 23503, got %+v", commitFrames)
	}
	if st := readyStatus(t, commitFrames); st != byte(libpq.TxStatusIdle) {
		t.Errorf("status after a failed extended COMMIT = %q, want 'I' (the block is over)", st)
	}

	rows := simpleStmt(t, conn, r, "SELECT * FROM m0132_child")
	for _, f := range rows {
		if f.Type == libpq.MsgDataRow {
			t.Fatal("the rolled-back child row survived the failed deferred-FK COMMIT")
		}
	}
}

// --- finding (c): Sync must NOT end the block ----------------------------

// TestM0132S1_SyncDoesNotEndAnOpenBlock locks doc-09 correction (c). Sync
// (server.go, MsgSync) clears syncRequired and writes ReadyForQuery without
// touching transaction state — which is exactly PostgreSQL's behaviour for an
// explicit block, and is already correct today. It is recorded as a test
// because the tempting "end the transaction at Sync" edit would silently
// re-break every block that S2..S5 are about to make work; M0132-S9 extends
// this guard to the post-S2 world.
//
// This bar has no divergence arm: it must pass both before and after the fix.
func TestM0132S1_SyncDoesNotEndAnOpenBlock(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	if f := simpleStmt(t, conn, r, "BEGIN"); hasError(f) {
		t.Fatalf("simple BEGIN errored: %+v", f)
	}
	// A bare Sync between statements: PG answers ReadyForQuery('T') and leaves
	// the block open.
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)
	if st := readyStatus(t, drainToReady(t, r)); st != byte(libpq.TxStatusInTransaction) {
		t.Fatalf("status after a bare Sync inside a block = %q, want 'T'", st)
	}
	// And a full extended round trip (which ends in its own Sync) must not end
	// it either.
	if f := extendedStmt(t, conn, r, "y_sel", "SELECT 1"); hasError(f) {
		t.Fatalf("extended SELECT errored: %+v", f)
	}
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)
	if st := readyStatus(t, drainToReady(t, r)); st != byte(libpq.TxStatusInTransaction) {
		t.Errorf("status after Sync following an extended statement = %q, want 'T' "+
			"(Sync must not end an explicit block)", st)
	}
	if f := simpleStmt(t, conn, r, "ROLLBACK"); hasError(f) {
		t.Fatalf("simple ROLLBACK errored: %+v", f)
	}
}

// TestM0132S9_SyncBetweenExecutesLeavesBlockOpen is M0132-S9, the post-S2
// extension of finding (c) that S1's bar above only sketches. It drives a full
// block over the extended protocol and places a bare Sync BETWEEN two in-block
// Executes, then asserts two things the "end the transaction at Sync" edit
// would break:
//
//   1. the ReadyForQuery after that mid-block Sync reports 'T' — the block is
//      still open, so the next Execute must not silently start a fresh
//      auto-commit transaction;
//   2. the work done by the Execute AFTER the Sync is still rolled back by a
//      later ROLLBACK — i.e. both Executes joined the one block, not an
//      auto-commit transaction that the ROLLBACK discards nothing of.
//
// Unlike TestM0132S1_SyncDoesNotEndAnOpenBlock (which only checks the status
// byte, with the block open over the simple path), this one also checks the
// second Execute's writes, and opens the block over the extended path — so a
// future reader cannot "fix" Sync into ending the block and still pass this.
func TestM0132S9_SyncBetweenExecutesLeavesBlockOpen(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	if f := extendedStmt(t, conn, r, "s9_begin", "BEGIN"); hasError(f) {
		t.Fatalf("extended BEGIN errored: %+v", f)
	}

	// Execute #1 — INSERT, then a bare Sync in the middle of the block.
	writeFrontendFrame(t, conn, libpq.MsgParse, parsePayload("s9_i1", "INSERT INTO items VALUES (1, 'a')", nil))
	writeFrontendFrame(t, conn, libpq.MsgBind, bindPayload("", "s9_i1", nil, nil, nil))
	writeFrontendFrame(t, conn, libpq.MsgExecute, executePayload("", 0))
	writeFrontendFrame(t, conn, libpq.MsgSync, nil)
	if st := readyStatus(t, drainToReady(t, r)); st != byte(libpq.TxStatusInTransaction) {
		t.Fatalf("status after Sync between two in-block Executes = %q, want 'T' "+
			"(Sync must not end the block)", st)
	}

	// Execute #2 — this work must land in the SAME block the Sync just left open.
	if f := extendedStmt(t, conn, r, "s9_i2", "INSERT INTO items VALUES (2, 'b')"); hasError(f) {
		t.Fatalf("extended INSERT #2 errored: %+v", f)
	}

	if f := extendedStmt(t, conn, r, "s9_rb", "ROLLBACK"); hasError(f) {
		t.Fatalf("extended ROLLBACK errored: %+v", f)
	}

	if got := countItems(t, conn, r); got != 0 {
		t.Errorf("after BEGIN / Execute / Sync / Execute / ROLLBACK: %d rows, want 0 "+
			"(both Executes joined the one block, so the ROLLBACK discards both)", got)
	}
}
