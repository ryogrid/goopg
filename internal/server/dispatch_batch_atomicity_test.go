package server

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/protocol"
)

// TestSimpleQueryBatchAbortUndoesEarlierCreateTable pins the M0110-0001
// DU-002 slice 444 deferral-ledger discovery (2026-07-04): a multi-statement
// simple-query batch runs under a single implicit autocommit transaction
// (dispatchSimpleQueryViaExecutor begins exactly one mvcc.Transaction for the
// whole Query message), so a LATER statement's failure must roll back
// EVERY earlier statement in that same message — including an earlier
// CREATE TABLE — matching real PostgreSQL's simple-query atomicity
// (postgres/src/backend/tcop/postgres.c exec_simple_query).
//
// Before the fix, CREATE TABLE's in-memory catalog registration
// (catalog.InMemory.RegisterTable, invoked via execCreateTable) was
// unconditional and non-transactional, while the corresponding pg_class /
// pg_attribute catalog-heap rows were written under the mvcc transaction and
// therefore DID roll back — leaving the table permanently registered in the
// live catalog with no catalog-heap rows behind it (a pg_dump-visible
// desync). The fix wires a message-scoped *executor.BasicSession for
// autocommit batches so the existing RecordDDLCreate/ProcessRollbackUndos
// machinery (already used by explicit ROLLBACK) also fires when an implicit
// batch aborts.
func TestSimpleQueryBatchAbortUndoesEarlierCreateTable(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is not *catalog.InMemory")
	}

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	const tblName = "zz_batch_atomicity_rollback"
	// Second statement references a nonexistent relation — guaranteed 42P01,
	// no reliance on numeric/runtime semantics.
	writeQuery(t, conn, "CREATE TABLE "+tblName+" (a int4); SELECT * FROM zz_definitely_missing_relation;")
	frames := readUntilReady(t, conn)

	sawTableCreateComplete := false
	sawError := false
	for _, f := range frames {
		switch f.Type {
		case 'C': // CommandComplete
			if string(f.Payload) == "CREATE TABLE\x00" {
				sawTableCreateComplete = true
			}
		case 'E': // ErrorResponse
			sawError = true
		}
	}
	if !sawTableCreateComplete {
		t.Fatalf("expected CREATE TABLE CommandComplete before the batch-aborting error; frames=%+v", frames)
	}
	if !sawError {
		t.Fatalf("expected an ErrorResponse for the invalid second statement; frames=%+v", frames)
	}

	if _, found := im.LookupTable(parser.ObjectName{Name: tblName}); found {
		t.Fatalf("table %q survived the aborted implicit batch — CREATE TABLE was not rolled back", tblName)
	}

	// A fresh, single-statement CREATE TABLE in its own message must still
	// succeed and persist normally — the fix must not make autocommit DDL
	// always transient.
	writeQuery(t, conn, "CREATE TABLE "+tblName+" (a int4);")
	frames = readUntilReady(t, conn)
	if _, found := im.LookupTable(parser.ObjectName{Name: tblName}); !found {
		t.Fatalf("table %q was not created by a standalone (non-aborting) CREATE TABLE; frames=%+v", tblName, frames)
	}
}

// TestSimpleQueryBatchExplicitBeginUndoesEarlierAutocommitCreateTable pins the
// root-0024 design doc's second documented residual: a
// `CREATE TABLE t1(...); BEGIN; CREATE TABLE t2(...); ROLLBACK;` compound
// batch — all in ONE simple-query message — must roll back BOTH t1 (created
// before the explicit BEGIN, under the message's throwaway autocommit
// session) and t2 (created inside the explicit block).
//
// Before the fix, `t1`'s CREATE was recorded via RecordDDLCreate on the
// throwaway *executor.BasicSession wired at dispatch entry (no explicit
// transaction existed yet). The mid-batch BEGIN handler
// (internal/server/dispatch.go's planner.TxBegin case) then lazily allocates
// connTx's own *executor.BasicSession and re-wires ctx.Session onto it for
// the rest of the batch — silently discarding the throwaway session's
// pendingDDL list. The subsequent ROLLBACK's ProcessRollbackUndos call only
// sees t2's entry, so t1 survives the rollback despite never having
// committed on its own (PostgreSQL rolls back the whole block). The fix
// drains the throwaway session's pending DDL-create list before it is
// discarded and replays it onto the newly-allocated session.
func TestSimpleQueryBatchExplicitBeginUndoesEarlierAutocommitCreateTable(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is not *catalog.InMemory")
	}

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	const t1Name = "zz_compound_batch_t1"
	const t2Name = "zz_compound_batch_t2"
	writeQuery(t, conn, "CREATE TABLE "+t1Name+" (a int4); BEGIN; CREATE TABLE "+t2Name+" (b int4); ROLLBACK;")
	frames := readUntilReady(t, conn)

	if _, found := im.LookupTable(parser.ObjectName{Name: t1Name}); found {
		t.Fatalf("table %q (created before the mid-batch BEGIN) survived the ROLLBACK; frames=%+v", t1Name, frames)
	}
	if _, found := im.LookupTable(parser.ObjectName{Name: t2Name}); found {
		t.Fatalf("table %q (created inside the explicit block) survived the ROLLBACK; frames=%+v", t2Name, frames)
	}
}

// TestSimpleQueryBatchAbortUndoesEarlierCreateType pins root-0024's first
// documented residual (M0110-0001): enum/composite-type creation was not
// undo-tracked for a message-scoped autocommit batch, the same bug class
// TestSimpleQueryBatchAbortUndoesEarlierCreateTable fixed for CREATE
// TABLE/INDEX. Before the fix, CREATE TYPE ... AS ENUM inside an autocommit
// multi-statement batch was never recorded for undo at all (the record sites
// in operators_ddl.go gated on Session.InExplicitTransaction(), which is
// false for the message-scoped throwaway session) — so a LATER statement's
// failure in the same message left the enum permanently registered despite
// the whole implicit batch transaction rolling back everywhere else. The fix
// adds Session.TracksDDLUndo() (true for both a real explicit transaction and
// the autocommit throwaway session) and wires the abort defer to call the
// existing undoEnumDDLFromContext machinery (exported as
// executor.UndoEnumDDLOnAbort) alongside ProcessRollbackUndos.
func TestSimpleQueryBatchAbortUndoesEarlierCreateType(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is not *catalog.InMemory")
	}

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	const typeName = "zz_batch_atomicity_mood"
	writeQuery(t, conn, "CREATE TYPE "+typeName+" AS ENUM ('sad','ok','happy'); SELECT * FROM zz_definitely_missing_relation;")
	frames := readUntilReady(t, conn)

	sawTypeCreateComplete := false
	sawError := false
	for _, f := range frames {
		switch f.Type {
		case 'C': // CommandComplete
			if string(f.Payload) == "CREATE TYPE\x00" {
				sawTypeCreateComplete = true
			}
		case 'E': // ErrorResponse
			sawError = true
		}
	}
	if !sawTypeCreateComplete {
		t.Fatalf("expected CREATE TYPE CommandComplete before the batch-aborting error; frames=%+v", frames)
	}
	if !sawError {
		t.Fatalf("expected an ErrorResponse for the invalid second statement; frames=%+v", frames)
	}

	if _, found := im.LookupEnum(typeName); found {
		t.Fatalf("enum type %q survived the aborted implicit batch — CREATE TYPE was not rolled back", typeName)
	}

	// A fresh, single-statement CREATE TYPE in its own message must still
	// succeed and persist normally — the fix must not make autocommit DDL
	// always transient.
	writeQuery(t, conn, "CREATE TYPE "+typeName+" AS ENUM ('sad','ok','happy');")
	frames = readUntilReady(t, conn)
	if _, found := im.LookupEnum(typeName); !found {
		t.Fatalf("enum type %q was not created by a standalone (non-aborting) CREATE TYPE; frames=%+v", typeName, frames)
	}
}

// TestSimpleQueryMidBatchBeginUndoesEarlierAutocommitCreateType pins the
// resolution of root-0024's first-residual sub-part (2) (deferral ledger row
// for `M0110-0001 (root-0024 residual (1), enum/composite half, loop #104)`):
// a `CREATE TYPE mood AS ENUM(...); BEGIN; ROLLBACK;` compound batch, all one
// simple-query message, must undo the pre-BEGIN autocommit `CREATE TYPE` too,
// mirroring the identical `pendingDDL`/`CREATE TABLE` case already covered by
// TestSimpleQueryBatchExplicitBeginUndoesEarlierAutocommitCreateTable.
//
// That loop's ledger row predicted this combination still leaked the type,
// reasoning that the `connTx.InExplicit()` write-back guard added alongside
// `TracksDDLUndo()` would skip carrying `ectx.PendingCreatedEnums`/etc. into
// `connTx` "before BEGIN promotes the session". That reasoning was wrong: the
// write-back runs unconditionally after EVERY successful statement — including
// `BEGIN` itself — and by the time `BEGIN`'s own statement finishes,
// `connTx.InExplicit()` is already true (`connTx.Begin()` runs inside the
// `TxBegin` case). Since `ectx.PendingCreatedEnums` lives on the
// message-scoped `*executor.Context` (not on the session object `BEGIN`
// replaces), it already held the pre-BEGIN `CREATE TYPE` entry, so that same
// write-back carries it into `connTx` — no dedicated hand-off (like
// `pendingDDL`'s `priorDDLCreates` drain-and-replay) was actually needed.
// This test formalizes that discovery so it stays covered.
func TestSimpleQueryMidBatchBeginUndoesEarlierAutocommitCreateType(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is not *catalog.InMemory")
	}

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	const typeName = "zz_batch_atomicity_midbegin_mood"
	writeQuery(t, conn, "CREATE TYPE "+typeName+" AS ENUM ('sad','ok'); BEGIN; ROLLBACK;")
	frames := readUntilReady(t, conn)
	if _, found := im.LookupEnum(typeName); found {
		t.Fatalf("enum type %q survived autocommit-CREATE-then-mid-batch-BEGIN-ROLLBACK; frames=%+v", typeName, frames)
	}

	// A real PG multi-statement abort: the failing statement aborts the
	// WHOLE simple-query message (remaining statements, including a trailing
	// ROLLBACK in that same message, are never executed — matching
	// postgres.c's exec_simple_query semantics), so the explicit ROLLBACK
	// must be sent as its own follow-up message to actually fire.
	const typeName2 = "zz_batch_atomicity_midbegin_mood2"
	writeQuery(t, conn, "CREATE TYPE "+typeName2+" AS ENUM ('sad','ok'); BEGIN; SELECT * FROM zz_definitely_missing_relation;")
	readUntilReady(t, conn)
	writeQuery(t, conn, "ROLLBACK;")
	frames = readUntilReady(t, conn)
	if _, found := im.LookupEnum(typeName2); found {
		t.Fatalf("enum type %q survived autocommit-CREATE-then-mid-batch-BEGIN-error-then-ROLLBACK; frames=%+v", typeName2, frames)
	}
}

// TestSimpleQueryMidBatchBeginUndoesEarlierAutocommitCreateComposite and
// TestSimpleQueryMidBatchBeginUndoesEarlierAutocommitAddValue extend the
// coverage above to the other two record sites `TracksDDLUndo()` gates
// (composite-type creation, `ALTER TYPE ... ADD VALUE`) — same mechanism,
// same expected behavior.
func TestSimpleQueryMidBatchBeginUndoesEarlierAutocommitCreateComposite(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is not *catalog.InMemory")
	}

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	const typeName = "zz_batch_atomicity_midbegin_composite"
	writeQuery(t, conn, "CREATE TYPE "+typeName+" AS (a int4, b text); BEGIN; ROLLBACK;")
	frames := readUntilReady(t, conn)
	if ct := im.LookupCompositeType(typeName); ct != nil {
		t.Fatalf("composite type %q survived autocommit-CREATE-then-mid-batch-BEGIN-ROLLBACK; frames=%+v", typeName, frames)
	}
}

func TestSimpleQueryMidBatchBeginUndoesEarlierAutocommitAddValue(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is not *catalog.InMemory")
	}

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	const typeName = "zz_batch_atomicity_midbegin_addvalue"
	writeQuery(t, conn, "CREATE TYPE "+typeName+" AS ENUM ('sad','ok');")
	readUntilReady(t, conn)

	writeQuery(t, conn, "ALTER TYPE "+typeName+" ADD VALUE 'happy'; BEGIN; ROLLBACK;")
	frames := readUntilReady(t, conn)

	et, found := im.LookupEnum(typeName)
	if !found {
		t.Fatalf("enum type %q vanished entirely; frames=%+v", typeName, frames)
	}
	for _, v := range et.Values {
		if v.Label == "happy" {
			t.Fatalf("enum value 'happy' survived autocommit-ADD-VALUE-then-mid-batch-BEGIN-ROLLBACK; frames=%+v", frames)
		}
	}
}

// TestSimpleQueryCommitPersistsEnumAddValue pins M-NIGHTLY AI-20260814-011711-001
// (regress/enum). M0132-S2's extraction of the transaction-verb state machine
// (txn_verb.go applyTransactionVerb) collapsed the terminal teardown of all
// five block-ending paths into endExplicitBlock, which unconditionally called
// undoEnumDDLForRollback. That undoes an ALTER TYPE … ADD VALUE on ROLLBACK —
// correct — but it also ran on a SUCCESSFUL COMMIT, so the newly-added label
// was silently removed from the in-memory catalog the moment the block ended.
// The regress enum case caught it as `invalid input value for enum bogus:
// "new"` on the post-COMMIT `SELECT 'new'::bogus`, and `pg_enum` reporting only
// the pre-existing label. The fix gates the undo behind a flag that is true on
// every rollback/abort path and false only on a successful COMMIT.
func TestSimpleQueryCommitPersistsEnumAddValue(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is not *catalog.InMemory")
	}

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	const typeName = "zz_batch_atomicity_commit_addvalue"
	writeQuery(t, conn, "CREATE TYPE "+typeName+" AS ENUM ('sad','ok');")
	readUntilReady(t, conn)

	writeQuery(t, conn, "BEGIN; ALTER TYPE "+typeName+" ADD VALUE 'happy'; COMMIT;")
	readUntilReady(t, conn)

	et, found := im.LookupEnum(typeName)
	if !found {
		t.Fatalf("enum type %q vanished entirely", typeName)
	}
	for _, v := range et.Values {
		if v.Label == "happy" {
			return // committed ADD VALUE survived — the bug is fixed
		}
	}
	t.Fatalf("enum value 'happy' did not survive BEGIN-ADD VALUE-COMMIT; values=%+v", et.Values)
}

// TestSimpleQueryBatchAbortUndoesEarlierTruncate closes root-0024's first
// residual's remaining TRUNCATE half (deferral-ledger row "M0110-0001
// (root-0024 residual (1), enum/composite half, loop #104)" resume point
// (1)): a TRUNCATE inside a message-scoped autocommit batch was never
// undo-tracked, the same bug class already fixed for CREATE TABLE/INDEX
// (TestSimpleQueryBatchAbortUndoesEarlierCreateTable) and CREATE TYPE
// (TestSimpleQueryBatchAbortUndoesEarlierCreateType). Before the fix,
// truncateTableAndPartitions (internal/executor/operators_ddl.go) only called
// RecordTruncate when Session.InExplicitTransaction() was true, so the
// message-scoped throwaway autocommit session never got a page-snapshot undo
// entry — a LATER statement's failure in the same message left the table
// permanently empty despite the whole implicit batch transaction rolling back
// everywhere else. The fix gates on TracksDDLUndo() instead, matching the
// enum/composite fix; ProcessRollbackUndos already unconditionally drains and
// restores TakePendingTruncates() on abort, so no new plumbing was needed.
func TestSimpleQueryBatchAbortUndoesEarlierTruncate(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	const tblName = "zz_batch_atomicity_truncate"
	writeQuery(t, conn, "CREATE TABLE "+tblName+" (a int4);")
	readUntilReady(t, conn)
	writeQuery(t, conn, "INSERT INTO "+tblName+" VALUES (1), (2), (3);")
	readUntilReady(t, conn)

	writeQuery(t, conn, "TRUNCATE "+tblName+"; SELECT * FROM zz_definitely_missing_relation;")
	frames := readUntilReady(t, conn)

	sawTruncateComplete := false
	sawError := false
	for _, f := range frames {
		switch f.Type {
		case 'C': // CommandComplete
			if string(f.Payload) == "TRUNCATE TABLE\x00" {
				sawTruncateComplete = true
			}
		case 'E': // ErrorResponse
			sawError = true
		}
	}
	if !sawTruncateComplete {
		t.Fatalf("expected TRUNCATE TABLE CommandComplete before the batch-aborting error; frames=%+v", frames)
	}
	if !sawError {
		t.Fatalf("expected an ErrorResponse for the invalid second statement; frames=%+v", frames)
	}

	writeQuery(t, conn, "SELECT * FROM "+tblName+" ORDER BY a;")
	frames = readUntilReady(t, conn)
	var rowCount int
	for _, f := range frames {
		if f.Type == protocol.MsgDataRow {
			rowCount++
		}
	}
	if rowCount != 3 {
		t.Fatalf("row count after aborted autocommit TRUNCATE = %d, want 3 (TRUNCATE should have been undone); frames=%+v", rowCount, frames)
	}
}
