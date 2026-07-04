package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCreateIndexSurvivesRestartViaWAL pins the M0079-0001
// recovery path: after CREATE INDEX (no SaveCatalog),
// the index is recovered from the WAL on the next Open.
//
// Flow:
//  1. Init + Open + CREATE TABLE + CREATE INDEX + Close (no SaveCatalog)
//  2. Re-Open: heap scan → loadUserTablesFromHeap finds the table
//             WAL replay → replayIndexDDLRecords finds the CREATE INDEX
//  3. Assert the index is in the catalog with correct metadata
//
// This is the surfacing case for pgbench: pgbench-init creates
// `pgbench_accounts_pkey` and similar indexes; without WAL-driven
// recovery the index disappears after every server restart, and
// the planner falls back to Seq Scan on a 10M-row table.
func TestCreateIndexSurvivesRestartViaWAL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	// Phase 1: CREATE TABLE + CREATE INDEX, then Close WITHOUT
	// SaveCatalog (simulating a crash).
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE accts (aid int4 NOT NULL, abalance int4)")
	runDDL(t, rt1, "CREATE UNIQUE INDEX accts_pkey ON accts (aid)")
	// Note: NO SaveCatalog call here — simulating a crash.
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	// Phase 2: Re-Open — index must be recovered from WAL.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	// Phase 4: verify table + index both recovered.
	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "accts"})
	if !ok {
		t.Fatal("accts not found after restart — table heap recovery failed")
	}
	idx, ok := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "accts_pkey"})
	if !ok {
		t.Fatal("accts_pkey not found after restart — WAL-driven index recovery failed (M0079-0001 regression)")
	}
	if idx.Table == nil || idx.Table.OID != tbl.OID {
		t.Errorf("index.Table OID mismatch: got=%v want=%d", idx.Table, tbl.OID)
	}
	if !idx.Unique {
		t.Error("recovered index lost Unique flag")
	}
	if idx.Method != "btree" {
		t.Errorf("recovered index Method=%q want btree", idx.Method)
	}
	if len(idx.Columns) != 1 || idx.Columns[0] != "aid" {
		t.Errorf("recovered index Columns=%v want [aid]", idx.Columns)
	}
}

// TestDropIndexSurvivesRestartViaWAL pins the symmetric DROP
// path: a CREATE INDEX followed by DROP INDEX must NOT
// resurrect the index from WAL on restart. (M0079-0001.)
func TestDropIndexSurvivesRestartViaWAL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE t (id int4 NOT NULL)")
	runDDL(t, rt1, "CREATE INDEX t_id_idx ON t (id)")
	runDDL(t, rt1, "DROP INDEX t_id_idx")
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	if _, ok := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "t_id_idx"}); ok {
		t.Error("dropped index resurrected after restart — DROP INDEX WAL replay missing")
	}
}

// TestRenameIndexSurvivesRestartViaWAL pins the DU-002 slice 443 recovery
// path: `ALTER INDEX ... RENAME TO` must survive a restart via
// wal.RecordKindRenameIndex, the same way CREATE/DROP INDEX already do.
func TestRenameIndexSurvivesRestartViaWAL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE ridx (id int4 NOT NULL)")
	runDDL(t, rt1, "CREATE INDEX ridx_old_name ON ridx (id)")
	runDDL(t, rt1, "ALTER INDEX ridx_old_name RENAME TO ridx_new_name")
	// Note: NO SaveCatalog call here — simulating a crash.
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	tbl, ok := rt2.Catalog.LookupTable(parser.ObjectName{Name: "ridx"})
	if !ok {
		t.Fatal("ridx not found after restart — table heap recovery failed")
	}
	idx, ok := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "ridx_new_name"})
	if !ok {
		t.Fatal("ridx_new_name not found after restart — rename WAL replay failed")
	}
	if idx.Table == nil || idx.Table.OID != tbl.OID {
		t.Errorf("index.Table OID mismatch: got=%v want=%d", idx.Table, tbl.OID)
	}
	if _, stillThere := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "ridx_old_name"}); stillThere {
		t.Error("old index name ridx_old_name resurrected after restart")
	}
}

// TestCreateIndexRecoveredOIDDoesNotCollide pins the
// nextOID-advance step in `RegisterIndexDuringRecovery`:
// after recovery, a new CREATE INDEX must allocate an OID
// strictly greater than any OID restored from WAL.
// (M0079-0001.)
func TestCreateIndexRecoveredOIDDoesNotCollide(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE u (id int4 NOT NULL)")
	runDDL(t, rt1, "CREATE INDEX u_idx_a ON u (id)")
	idxA, _ := rt1.Catalog.LookupIndex(parser.ObjectName{Name: "u_idx_a"})
	if idxA == nil {
		t.Fatal("u_idx_a not registered before crash")
	}
	preCrashOID := idxA.OID
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	idxARecovered, ok := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "u_idx_a"})
	if !ok {
		t.Fatal("u_idx_a not recovered")
	}
	if idxARecovered.OID != preCrashOID {
		t.Errorf("recovered OID=%d want %d (must match pre-crash for relfile mapping)", idxARecovered.OID, preCrashOID)
	}
	// Now create a new index post-recovery; its OID must be > preCrashOID.
	runDDL(t, rt2, "CREATE INDEX u_idx_b ON u (id)")
	idxB, ok := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "u_idx_b"})
	if !ok {
		t.Fatal("u_idx_b creation failed after recovery")
	}
	if idxB.OID <= preCrashOID {
		t.Errorf("post-recovery index OID=%d must exceed recovered OID=%d (nextOID advance failed)", idxB.OID, preCrashOID)
	}
}
