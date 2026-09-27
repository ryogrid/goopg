package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/optimizer"
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

// TestCreateIndexColumnOrderingSurvivesRestartViaWAL pins M0122-0006: an
// index's per-column ASC/DESC + NULLS FIRST/LAST ordering (indoption) must
// survive a crash restart via the CREATE INDEX WAL record, not silently
// reset to all-ascending/NULLS-LAST. Companion to
// TestCreateIndexSurvivesRestartViaWAL above, which only pinned
// Unique/Method/Columns.
//
// Uses the TestCrashRecoveryReplaysWALAfterUncleanShutdown pattern (flush
// WAL durable, then close WAL + StorageMgr directly WITHOUT Pool.Close) —
// a plain `rt1.Close()` performs a synchronous shutdown checkpoint
// (Runtime.Close, M0089-0002) that flushes the pg_index heap page too,
// which lets `loadUserIndexesFromHeap` win the recovery race and can mask
// a bug that's specific to the CREATE INDEX WAL payload's own
// encode/decode, which only decides the outcome on a genuine unclean
// shutdown (SIGKILL) where no checkpoint ran.
func TestCreateIndexColumnOrderingSurvivesRestartViaWAL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE ord (a int4 NOT NULL, b int4 NOT NULL)")
	// Explicit NULLS clauses on both columns, each overriding its type's
	// default (DESC defaults to NULLS FIRST, ASC to NULLS LAST) so the
	// assertions below can't pass by accident from unset zero values.
	runDDL(t, rt1, "CREATE INDEX ord_idx ON ord (a DESC NULLS LAST, b ASC NULLS FIRST)")

	// Simulate SIGKILL: force WAL durable, then close WAL + StorageMgr
	// directly, dropping every dirty buffer-pool page (including the
	// pg_index heap row) without a checkpoint.
	if err := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); err != nil {
		t.Fatalf("FlushUpTo: %v", err)
	}
	if err := rt1.WAL.Close(); err != nil {
		t.Fatalf("WAL.Close: %v", err)
	}
	if err := rt1.StorageMgr.Close(); err != nil {
		t.Fatalf("StorageMgr.Close: %v", err)
	}
	rt1 = nil

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	idx, ok := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "ord_idx"})
	if !ok {
		t.Fatal("ord_idx not found after restart — WAL-driven index recovery failed")
	}
	if len(idx.ColDescending) != 2 || !idx.ColDescending[0] || idx.ColDescending[1] {
		t.Errorf("recovered ColDescending=%v, want [true false]", idx.ColDescending)
	}
	if len(idx.ColNullsFirst) != 2 || idx.ColNullsFirst[0] || !idx.ColNullsFirst[1] {
		t.Errorf("recovered ColNullsFirst=%v, want [false true]", idx.ColNullsFirst)
	}
}

// TestCreateIndexExtendedPropertiesSurviveRestartViaWAL pins the M0122-0006
// follow-up: a partial-index WHERE predicate, INCLUDE columns, per-column
// opclass/collation, WITH-clause fillfactor/deduplicate_items, and NULLS NOT
// DISTINCT must all survive a genuine crash restart via the CREATE INDEX WAL
// record — not silently reset to their defaults. Companion to
// TestCreateIndexColumnOrderingSurvivesRestartViaWAL above; same
// uncheckpointed-SIGKILL rationale (a plain rt1.Close() would flush the
// pg_index heap row via its shutdown checkpoint and let
// loadUserIndexesFromHeap mask a WAL-payload-specific bug).
func TestCreateIndexExtendedPropertiesSurviveRestartViaWAL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE ext (a int4 NOT NULL, b text, c int4)")
	runDDL(t, rt1, `CREATE UNIQUE INDEX ext_idx ON ext (a, b COLLATE "C" text_pattern_ops) `+
		`INCLUDE (c) WITH (fillfactor=70, deduplicate_items=off) NULLS NOT DISTINCT WHERE (a > 0)`)

	// Simulate SIGKILL: force WAL durable, then close WAL + StorageMgr
	// directly, dropping every dirty buffer-pool page (including the
	// pg_index heap row) without a checkpoint.
	if err := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); err != nil {
		t.Fatalf("FlushUpTo: %v", err)
	}
	if err := rt1.WAL.Close(); err != nil {
		t.Fatalf("WAL.Close: %v", err)
	}
	if err := rt1.StorageMgr.Close(); err != nil {
		t.Fatalf("StorageMgr.Close: %v", err)
	}
	rt1 = nil

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	idx, ok := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "ext_idx"})
	if !ok {
		t.Fatal("ext_idx not found after restart — WAL-driven index recovery failed")
	}
	if !idx.HasPredicate {
		t.Error("recovered HasPredicate = false, want true")
	}
	if idx.PredicateString != "(a > 0)" {
		t.Errorf("recovered PredicateString = %q, want %q", idx.PredicateString, "(a > 0)")
	}
	if len(idx.IncludeColumns) != 1 || idx.IncludeColumns[0] != "c" {
		t.Errorf("recovered IncludeColumns = %v, want [c]", idx.IncludeColumns)
	}
	if len(idx.ColOpClasses) != 2 || idx.ColOpClasses[1] != "text_pattern_ops" {
		t.Errorf("recovered ColOpClasses = %v, want [_ text_pattern_ops]", idx.ColOpClasses)
	}
	if len(idx.ColCollations) != 2 || idx.ColCollations[1] == "" {
		t.Errorf("recovered ColCollations = %v, want a non-empty entry at [1]", idx.ColCollations)
	}
	if idx.Fillfactor != 70 {
		t.Errorf("recovered Fillfactor = %d, want 70", idx.Fillfactor)
	}
	if idx.DeduplicateItems == nil || *idx.DeduplicateItems {
		t.Errorf("recovered DeduplicateItems = %v, want &false", idx.DeduplicateItems)
	}
	if !idx.NullsNotDistinct {
		t.Error("recovered NullsNotDistinct = false, want true")
	}
}

// TestCreateIndexPredicateAndIncludeColumnsSurviveCheckpointedRestart pins the
// M0122-0006 follow-up's second residual (see .ralph/deferral_ledger.md's
// 2026-07-08 row): a partial-index WHERE predicate, INCLUDE columns, and
// NULLS NOT DISTINCT must survive a *checkpointed* restart (a graceful
// rt1.Close(), which flushes the pg_index heap row and drives recovery
// through loadUserIndexesFromHeap — internal/initdb/open.go — not through
// CREATE INDEX WAL replay). This is the read side of buildUserPGIndexRow's
// indpred/indkey(INCLUDE)/indnullsnotdistinct writes
// (internal/executor/pg18_user_catalog_rows.go) plus
// catalog.DecodePGIndexPhysicalRow's matching decode
// (internal/catalog/codec.go). Companion to
// TestCreateIndexExtendedPropertiesSurviveRestartViaWAL above, which only
// exercises the uncheckpointed-crash / WAL-replay path.
//
// ColOpClasses/ColCollations are deliberately NOT asserted here: real
// per-column opclass/collation OID resolution in indclass/indcollation is
// still unimplemented (a separate, larger residual — see the ledger),
// so they do not survive a checkpointed restart yet.
func TestCreateIndexPredicateAndIncludeColumnsSurviveCheckpointedRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE ext2 (a int4 NOT NULL, b text, c int4)")
	runDDL(t, rt1, `CREATE UNIQUE INDEX ext2_idx ON ext2 (a, b) INCLUDE (c) `+
		`NULLS NOT DISTINCT WHERE (a > 0)`)

	// Graceful shutdown: Runtime.Close performs a synchronous checkpoint
	// (M0089-0002), flushing the pg_index heap page — recovery on the next
	// Open is won by loadUserIndexesFromHeap, not WAL replay.
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	idx, ok := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "ext2_idx"})
	if !ok {
		t.Fatal("ext2_idx not found after restart — heap-driven index recovery failed")
	}
	if len(idx.Columns) != 2 || idx.Columns[0] != "a" || idx.Columns[1] != "b" {
		t.Errorf("recovered Columns = %v, want [a b]", idx.Columns)
	}
	if !idx.HasPredicate {
		t.Error("recovered HasPredicate = false, want true")
	}
	if idx.PredicateString != "(a > 0)" {
		t.Errorf("recovered PredicateString = %q, want %q", idx.PredicateString, "(a > 0)")
	}
	if len(idx.IncludeColumns) != 1 || idx.IncludeColumns[0] != "c" {
		t.Errorf("recovered IncludeColumns = %v, want [c]", idx.IncludeColumns)
	}
	if !idx.NullsNotDistinct {
		t.Error("recovered NullsNotDistinct = false, want true")
	}
}

// TestCreateIndexOpclassAndCollationSurviveCheckpointedRestart pins the
// M0122-0006 follow-up 3 fix: a checkpointed restart (graceful Close, not a
// crash) previously reverted an index's declared opclass/collation to the
// column type's plain default, because DecodePGIndexPhysicalRow never
// decoded indclass/indcollation and loadUserIndexesFromHeap always passed
// nil for ColOpClasses/ColCollations. The WAL-replay crash-recovery path was
// never affected — wal.CreateIndexPayload already carried the name strings
// directly — so this test forces the heap-scan recovery path via a graceful
// Close, mirroring TestCreateIndexPredicateAndIncludeColumnsSurviveCheckpointedRestart.
func TestCreateIndexOpclassAndCollationSurviveCheckpointedRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDL(t, rt1, "CREATE TABLE ext4 (a int4, body text, note varchar(40))")
	runDDL(t, rt1, `CREATE INDEX ext4_idx ON ext4 (body text_pattern_ops, note COLLATE "C" varchar_pattern_ops)`)

	// Graceful shutdown: Runtime.Close performs a synchronous checkpoint
	// (M0089-0002), flushing the pg_index heap page — recovery on the next
	// Open is won by loadUserIndexesFromHeap, not WAL replay.
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	idx, ok := rt2.Catalog.LookupIndex(parser.ObjectName{Name: "ext4_idx"})
	if !ok {
		t.Fatal("ext4_idx not found after restart — heap-driven index recovery failed")
	}
	if len(idx.ColOpClasses) != 2 || idx.ColOpClasses[0] != "text_pattern_ops" || idx.ColOpClasses[1] != "varchar_pattern_ops" {
		t.Errorf("recovered ColOpClasses = %v, want [text_pattern_ops varchar_pattern_ops]", idx.ColOpClasses)
	}
	if len(idx.ColCollations) != 2 || idx.ColCollations[0] != "" || idx.ColCollations[1] != "C" {
		t.Errorf("recovered ColCollations = %v, want [\"\" C]", idx.ColCollations)
	}
}

// TestExplicitDefaultOpclassIndexSurvivesCheckpointedRestart pins the
// banner-2a wrong-rows defect (found 2026-09-25 by M0146-0015a; repro
// analysis/m0146/m0146-0015a/opclass-restart-repro.sh): `CREATE INDEX
// … (a int4_ops)` — an explicit opclass that IS the column type's default —
// returned 0 rows after a clean restart for a probe that returned 49
// before it.
//
// Root cause: the live catalog kept ColOpClasses[0]=="int4_ops" while the
// heap-reload path reverse-resolves pg_index.indclass to "" for a
// default-equivalent opclass (ResolveIndexColumnOpclassName). That made
// buildPGIndexKeyDesc's on-disk-format decision flip across the restart —
// goopg blob keys at CREATE, PG per-datum tuple keys after — so probes
// encoded under a different format than the index was built with.
// createBTreeIndex now normalises a default-equivalent explicit opclass to
// "" so both construction paths agree (PG's indclass cannot distinguish
// them either — the default opclass is not spelled out in indexdef).
// A genuinely non-default opclass keeps its spelling on both sides and
// stays on the blob format — already pinned by
// TestCreateIndexOpclassAndCollationSurviveCheckpointedRestart above.
func TestExplicitDefaultOpclassIndexSurvivesCheckpointedRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	runDDLRealDataDir(t, rt1, "CREATE TABLE opc_probe (a int4)")
	runDDLRealDataDir(t, rt1, "INSERT INTO opc_probe SELECT g FROM generate_series(1,1000) g")
	runDDLRealDataDir(t, rt1, "CREATE INDEX opc_probe_a ON opc_probe USING btree (a int4_ops)")

	// The normalisation this fix relies on: the catalog entry carries no
	// opclass spelling — int4_ops IS int4's default opclass, so indclass
	// holds the default OID and the heap-reload path can only reconstruct
	// "" — which is now exactly what the live entry holds too.
	idx1, ok := rt1.Catalog.LookupIndex(parser.ObjectName{Name: "opc_probe_a"})
	if !ok {
		t.Fatal("opc_probe_a not in catalog")
	}
	for _, n := range idx1.ColOpClasses {
		if n != "" {
			t.Fatalf("ColOpClasses=%v: a default-equivalent explicit opclass must normalise to \"\" (int4_ops IS int4's default opclass)", idx1.ColOpClasses)
		}
	}
	// The probe must actually go through the index — a seqscan would
	// quietly pass while the on-disk key format is broken.
	if !planUsesIndexScan(t, rt1, "SELECT count(*) FROM opc_probe WHERE a < 50") {
		t.Fatal("test premise broken: a < 50 probe did not plan an IndexScan (planner cost model changed?)")
	}
	if got := runCountQuery(t, rt1, "SELECT count(*) FROM opc_probe WHERE a < 50"); got != 49 {
		t.Fatalf("indexed probe before restart: count=%d, want 49", got)
	}

	// Graceful shutdown: Runtime.Close performs a synchronous checkpoint
	// (M0089-0002), flushing the pg_index heap page — recovery on the next
	// Open is won by loadUserIndexesFromHeap, not WAL replay.
	if err := rt1.Close(); err != nil {
		t.Fatal(err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()

	if !planUsesIndexScan(t, rt2, "SELECT count(*) FROM opc_probe WHERE a < 50") {
		t.Fatal("test premise broken: a < 50 probe did not plan an IndexScan after restart")
	}
	// Pre-fix this returned 0: the reloaded catalog lost the opclass
	// spelling, the key descriptor flipped to the tuple format, and the
	// probe encoded keys the blob-format index could never match.
	if got := runCountQuery(t, rt2, "SELECT count(*) FROM opc_probe WHERE a < 50"); got != 49 {
		t.Fatalf("indexed probe after restart: count=%d, want 49 (pre-fix bug returned 0 — probe encoded under a different key format than the index was built with)", got)
	}
}

// planUsesIndexScan plans sql and reports whether the plan tree contains an
// index-driven scan — the precondition for the before/after-restart probes
// above to exercise the index key format rather than a seqscan.
func planUsesIndexScan(t *testing.T, rt *Runtime, sql string) bool {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("expected 1 statement, got %d", len(stmts))
	}
	plan, err := optimizer.Plan(stmts[0], rt.Catalog)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	return walkForIndexScan(plan)
}

func walkForIndexScan(n optimizer.Node) bool {
	switch nd := n.(type) {
	case *optimizer.IndexScan, *optimizer.IndexOnlyScan:
		return true
	case *optimizer.Aggregate:
		return walkForIndexScan(nd.Child)
	case *optimizer.Result:
		return walkForIndexScan(nd.Child)
	case *optimizer.Project:
		return walkForIndexScan(nd.Child)
	case *optimizer.Filter:
		return walkForIndexScan(nd.Child)
	case *optimizer.Sort:
		return walkForIndexScan(nd.Child)
	case *optimizer.Limit:
		return walkForIndexScan(nd.Child)
	}
	return false
}

// runCountQuery executes a single-statement SELECT returning one int64 row
// (count(*)) through the full planner+executor pipeline, mirroring runDDL.
func runCountQuery(t *testing.T, rt *Runtime, sql string) int64 {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("expected 1 statement, got %d", len(stmts))
	}
	plan, err := optimizer.Plan(stmts[0], rt.Catalog)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	op, err := executor.Build(plan)
	if err != nil {
		t.Fatalf("build %q: %v", sql, err)
	}

	tx, err := rt.TxnMgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	snap, err := rt.TxnMgr.SnapshotFor(tx)
	if err != nil {
		_ = rt.TxnMgr.Commit(tx)
		t.Fatalf("SnapshotFor: %v", err)
	}
	ctx := executor.NewContext()
	ctx.Pool = rt.Pool
	ctx.Catalog = rt.Catalog
	ctx.TxnMgr = rt.TxnMgr
	ctx.Tx = tx
	ctx.Snap = snap

	if err := op.Open(ctx); err != nil {
		_ = rt.TxnMgr.Commit(tx)
		t.Fatalf("Open %q: %v", sql, err)
	}
	slot, nextErr := op.Next()
	if nextErr != nil {
		op.Close()
		_ = rt.TxnMgr.Commit(tx)
		t.Fatalf("Next %q: %v", sql, nextErr)
	}
	row := slot.Row()
	if len(row) != 1 {
		op.Close()
		_ = rt.TxnMgr.Commit(tx)
		t.Fatalf("%q: want 1-column row, got %d columns", sql, len(row))
	}
	got := row[0].Int
	slot.Release()
	op.Close()
	if err := rt.TxnMgr.Commit(tx); err != nil {
		t.Fatalf("commit after %q: %v", sql, err)
	}
	return got
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
