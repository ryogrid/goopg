package server

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
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
