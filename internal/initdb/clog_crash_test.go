package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCrashMidTransactionTableNotVisibleAfterRestart is the definitive
// regression test for M0030-0007 (pg_xact commit log):
//
// Scenario: a transaction that ran CREATE TABLE is interrupted by a server
// crash — no COMMIT or ROLLBACK reached. After restart, the table must NOT
// appear in the catalog even though its pg_class/pg_attribute rows survived
// WAL replay.
//
// Without the clog fix, loadUserTablesFromHeap trusted xmax==0 alone, so the
// rows from the crashed transaction were treated as committed and the table
// reappeared. With the clog, xmin is Unknown (no entry written before crash)
// and is filtered out.
//
// Flow:
//  1. Init + Open
//  2. BEGIN; CREATE TABLE crash_ghost — pg_class rows written with
//     xmin=in-progress-xid
//  3. Close runtime WITHOUT COMMIT or ROLLBACK (simulate crash)
//     — clog has no entry for that xid (Unknown)
//  4. Re-Open: WAL replays pg_class rows → loadUserTablesFromHeap checks
//     clog → Unknown → skip → table absent
//  5. Assert crash_ghost NOT in catalog
func TestCrashMidTransactionTableNotVisibleAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// Phase 1: open, begin tx, create table, then hard-close (no rollback).
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	conn := newTxnConn(rt1, t)
	conn.run("BEGIN")
	conn.run("CREATE TABLE crash_ghost (id int4, val text)")

	// Sanity: table is in the live catalog now (transaction still open).
	if _, ok := rt1.Catalog.LookupTable(parser.ObjectName{Name: "crash_ghost"}); !ok {
		t.Fatal("crash_ghost not in live catalog after CREATE TABLE — test setup broken")
	}

	// Hard-close WITHOUT COMMIT or ROLLBACK: simulates a crash.
	// The in-progress transaction's xid never gets a clog entry.
	rt1.Close()

	// Phase 2: re-open (WAL replay, then loadUserTablesFromHeap with clog check).
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("re-open after crash: %v", err)
	}
	defer rt2.Close()

	// Phase 3: the crashed-in-progress table must NOT reappear.
	if _, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "crash_ghost"}); ok {
		t.Error("crash_ghost reappeared after crash-restart — clog filter not working")
	}
}

// TestCommittedTableSurvivesCrashRestart ensures that a table whose creating
// transaction DID commit is still visible after a crash-style restart (no
// SaveCatalog called, so JSON snapshot is stale). This guards against the
// clog filter being too aggressive.
func TestCommittedTableSurvivesCrashRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// Phase 1: open, create a table with auto-commit, hard-close.
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	conn := newTxnConn(rt1, t)
	conn.run("CREATE TABLE committed_survivor (id int4)") // auto-commit
	rt1.Close()                                           // no SaveCatalog (simulate crash after commit)

	// Phase 2: re-open.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer rt2.Close()

	// Phase 3: committed table must be present despite no SaveCatalog.
	if _, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "committed_survivor"}); !ok {
		t.Error("committed_survivor absent after crash-restart — clog filtered it incorrectly")
	}
}
