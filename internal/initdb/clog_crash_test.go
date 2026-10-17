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
	// One committed transaction first, so the WAL holds xact history and the
	// restart takes the crash-recovery sweep. With NO committed transaction
	// since initdb the restart takes the old-cluster upgrade branch
	// (clog.InitializeAsCommitted), which stamps the crashed xid Committed
	// and resurrects the table — M0146-0152. HEAD passed this test without
	// the commit only because the second Open registered tables from the
	// M0114 JSON cache (written before the CREATE), retired by
	// M0141-S2a-fix2r-c.
	conn.run("CREATE TABLE committed_first (id int4)")
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

// TestCrashBeforeFirstCommitTableNotVisible pins M0146-0152: the same crash as
// TestCrashMidTransactionTableNotVisibleAfterRestart, but with no transaction
// committed since initdb. The WAL then holds no xact record and the on-disk
// clog is empty, which Open used to read as an old (pre-M0030-0007) cluster:
// it stamped every xid below NextXID Committed and the crashed CREATE TABLE
// came back. PostgreSQL treats every xid below nextXid that never committed
// as aborted. A cluster this binary initialised says so in
// global/pg_goopg_features, and Open runs the crash-recovery sweep instead.
func TestCrashBeforeFirstCommitTableNotVisible(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	conn := newTxnConn(rt1, t)
	conn.run("BEGIN")
	conn.run("CREATE TABLE crash_ghost (id int4, val text)")
	if _, ok := rt1.Catalog.LookupTable(parser.ObjectName{Name: "crash_ghost"}); !ok {
		t.Fatal("crash_ghost not in live catalog after CREATE TABLE — test setup broken")
	}
	rt1.Close() // no COMMIT / ROLLBACK: a crash

	for restart := 1; restart <= 2; restart++ {
		rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
		if err != nil {
			t.Fatalf("re-open %d: %v", restart, err)
		}
		if _, ok := rt.Catalog.LookupTable(parser.ObjectName{Name: "crash_ghost"}); ok {
			t.Errorf("restart %d: crash_ghost reappeared — the crashed xid was stamped committed", restart)
		}
		// The cluster stays usable: a new table commits and survives.
		if restart == 1 {
			newTxnConn(rt, t).run("CREATE TABLE after_crash (id int4)")
		} else if _, ok := rt.Catalog.LookupTable(parser.ObjectName{Name: "after_crash"}); !ok {
			t.Error("after_crash (committed after the crash) is missing")
		}
		rt.Close()
	}
}
