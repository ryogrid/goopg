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
