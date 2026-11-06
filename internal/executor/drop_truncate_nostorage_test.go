package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// M-NIGHTLY 0-byte relfilenode-file class (filed 2026-10-31 from
// AI-20261031-002109-005). The filed symptom (DROP TABLE IF EXISTS of a
// missing relation routing through the TRUNCATE executor) was verified NOT
// reproducing at HEAD: execDropTable short-circuits with the "does not
// exist, skipping" NOTICE, consumes no OID, and touches no storage. The
// real, reproducible defect behind the 0-byte files was (a) the smgr
// truncate path opening with O_CREATE (pinned in
// internal/storage/smgr_truncate_nocreate_test.go) and (b) TRUNCATE on a
// partitioned parent running parent-level storage work, while PG's
// ExecuteTruncateGuts skips partitioned tables outright
// (tablecmds.c:2148-2150, "Skip partitioned tables as there is nothing to
// do"). These tests pin the DROP-arm non-side-effects at the executor,
// the executor-level truncate-of-missing-file no-op, and the
// partitioned-parent skip.

// newEmptyStorageCtx builds a Context with pool + catalog + mvcc manager
// over an empty catalog (newStorageFixture pre-seeds a table, which would
// pollute the file inventory these tests assert on).
func newEmptyStorageCtx(t *testing.T) (*Context, *catalog.InMemory, *storage.Manager, string, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	mvcc := transam.NewManager()
	tx, err := mvcc.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	snap, err := mvcc.SnapshotFor(tx)
	if err != nil {
		t.Fatalf("SnapshotFor: %v", err)
	}
	ctx := NewContext()
	ctx.Pool = pool
	ctx.Catalog = cat
	ctx.TxnMgr = mvcc
	ctx.Tx = tx
	ctx.Snap = snap
	cleanup := func() {
		_ = mvcc.Rollback(tx)
		_ = pool.Close()
		_ = mgr.Close()
	}
	return ctx, cat, mgr, dir, cleanup
}

func baseFileSizes(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	ents, err := os.ReadDir(filepath.Join(dir, "base", "1"))
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		t.Fatalf("readdir base/1: %v", err)
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		st, err := e.Info()
		if err != nil {
			t.Fatalf("stat base/1/%s: %v", e.Name(), err)
		}
		out[e.Name()] = st.Size()
	}
	return out
}

func TestDropTableIfExistsMissingNoSideEffects(t *testing.T) {
	ctx, cat, mgr, dir, cleanup := newEmptyStorageCtx(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE keep(a int)"); err != nil {
		t.Fatalf("CREATE keep: %v", err)
	}
	keep, ok := cat.LookupTable(parser.ObjectName{Name: "keep"})
	if !ok {
		t.Fatal("keep not in catalog")
	}
	before := baseFileSizes(t, dir)
	nextBefore := cat.NextOID()

	// pgbench -i's exact statement on a database where the tables do not
	// exist (the nightly diagnosis's repro).
	err := runDDL(t, ctx, "drop table if exists pgbench_accounts, pgbench_branches, pgbench_history, pgbench_tellers cascade")
	if err != nil {
		t.Fatalf("DROP IF EXISTS of missing tables: %v", err)
	}
	if len(ctx.Notices) != 4 {
		t.Fatalf("notices = %v, want one skipping NOTICE per missing table", ctx.Notices)
	}
	for i, want := range []string{"pgbench_accounts", "pgbench_branches", "pgbench_history", "pgbench_tellers"} {
		wantMsg := "table \"" + want + "\" does not exist, skipping"
		if ctx.Notices[i] != wantMsg {
			t.Fatalf("notice[%d] = %q, want %q", i, ctx.Notices[i], wantMsg)
		}
	}
	ctx.TakeNotices()

	// PG's phase-1 name resolution for a missing relation with IF EXISTS
	// raises nothing, allocates no OID (missing_reloids is only fed by the
	// CASCADE pass over existing relations), and opens no storage.
	if got := cat.NextOID(); got != nextBefore {
		t.Fatalf("NextOID moved %d -> %d; DROP IF EXISTS of missing relations must not consume OIDs", nextBefore, got)
	}
	after := baseFileSizes(t, dir)
	if len(after) != len(before) {
		t.Fatalf("files after DROP = %v, want unchanged inventory %v", after, before)
	}

	// The next CREATE must land on the untouched next OID (no holes burned
	// by the drop) and its file must be created by the create path, not
	// pre-materialised 0-byte stubs.
	if err := runDDL(t, ctx, "CREATE TABLE probe_after(b int)"); err != nil {
		t.Fatalf("CREATE probe_after: %v", err)
	}
	pa, ok := cat.LookupTable(parser.ObjectName{Name: "probe_after"})
	if !ok {
		t.Fatal("probe_after not in catalog")
	}
	if pa.OID != keep.OID+1 {
		t.Fatalf("probe_after oid = %d, want %d (next in sequence after the drop)", pa.OID, keep.OID+1)
	}
	if _, err := os.Stat(filepath.Join(mgr.DataDir(), mgr.RelPath(cat.RelFileNode(pa)))); err != nil {
		t.Fatalf("probe_after file missing after CREATE: %v", err)
	}
}

func TestDropTableMissingNoIfExistsErrors(t *testing.T) {
	ctx, _, _, _, cleanup := newEmptyStorageCtx(t)
	defer cleanup()

	err := runDDL(t, ctx, "DROP TABLE no_such_tbl")
	if err == nil {
		t.Fatal("DROP TABLE of a missing relation without IF EXISTS: want 42P01, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42P01" {
		t.Fatalf("DROP TABLE missing: err = %v, want ExecError 42P01", err)
	}
}

func TestDropTableExistingRemovesFile(t *testing.T) {
	ctx, cat, mgr, _, cleanup := newEmptyStorageCtx(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE dropme(a int)"); err != nil {
		t.Fatalf("CREATE dropme: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "dropme"})
	if !ok {
		t.Fatal("dropme not in catalog")
	}
	path := filepath.Join(mgr.DataDir(), mgr.RelPath(cat.RelFileNode(tbl)))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("dropme file missing after CREATE: %v", err)
	}
	if err := runDDL(t, ctx, "DROP TABLE dropme"); err != nil {
		t.Fatalf("DROP TABLE dropme: %v", err)
	}
	if _, ok := cat.LookupTable(parser.ObjectName{Name: "dropme"}); ok {
		t.Fatal("dropme still in catalog after DROP")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dropme file still present after DROP (stat err = %v)", err)
	}
}

func TestTruncateMissingFileNoopNoCreate(t *testing.T) {
	ctx, cat, mgr, _, cleanup := newEmptyStorageCtx(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE truncme(a int)"); err != nil {
		t.Fatalf("CREATE truncme: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "truncme"})
	if !ok {
		t.Fatal("truncme not in catalog")
	}
	path := filepath.Join(mgr.DataDir(), mgr.RelPath(cat.RelFileNode(tbl)))
	// Simulate a relation with no storage (PG: relfilenode 0, no file):
	// drop the 0-byte file the create path made, close the manager handle,
	// then TRUNCATE. The truncate executor must not re-materialise it.
	mgr.CloseRelation(cat.RelFileNode(tbl))
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := runDDL(t, ctx, "TRUNCATE truncme"); err != nil {
		t.Fatalf("TRUNCATE with no storage file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("TRUNCATE created %s (stat err = %v); PG mdtruncate is a no-op without segments", path, err)
	}
}

func TestTruncatePartitionedParentSkipsParentStorage(t *testing.T) {
	ctx, cat, mgr, _, cleanup := newEmptyStorageCtx(t)
	defer cleanup()

	for _, sql := range []string{
		"CREATE TABLE pp(a int, b int) PARTITION BY LIST (a)",
		"CREATE TABLE pp1 PARTITION OF pp FOR VALUES IN (1)",
		"INSERT INTO pp VALUES (1, 10)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	parent, ok := cat.LookupTable(parser.ObjectName{Name: "pp"})
	if !ok {
		t.Fatal("pp not in catalog")
	}
	leaf, ok := cat.LookupTable(parser.ObjectName{Name: "pp1"})
	if !ok {
		t.Fatal("pp1 not in catalog")
	}

	// Materialise parent storage with one content block (goopg parents get
	// a synthesized relfilenode, so a parent file CAN exist; PG parents
	// carry relfilenode 0 and no file). TRUNCATE must skip the parent
	// entirely per tablecmds.c:2148-2150 and only truncate the leaves.
	parentRel := cat.RelFileNode(parent)
	leafRel := cat.RelFileNode(leaf)
	page := make([]byte, storage.BlockSize)
	for i := range page {
		page[i] = 0xCD
	}
	if _, err := mgr.Extend(parentRel, page); err != nil {
		t.Fatalf("Extend parent: %v", err)
	}
	if n, err := mgr.NBlocks(parentRel); err != nil || n != 1 {
		t.Fatalf("parent blocks before TRUNCATE = %v (err %v), want 1", n, err)
	}

	if err := runDDL(t, ctx, "TRUNCATE pp"); err != nil {
		t.Fatalf("TRUNCATE pp: %v", err)
	}

	if n, err := mgr.NBlocks(parentRel); err != nil || n != 1 {
		t.Fatalf("parent blocks after TRUNCATE = %v (err %v), want 1: PG skips partitioned parents, nothing to do", n, err)
	}
	if n, err := mgr.NBlocks(leafRel); err != nil || n != 0 {
		t.Fatalf("leaf blocks after TRUNCATE = %v (err %v), want 0: TRUNCATE on the parent truncates every leaf", n, err)
	}
	rows, qerr := runQueryWithErr(ctx, "SELECT a, b FROM pp")
	if qerr != nil {
		t.Fatalf("SELECT after TRUNCATE: %v", qerr)
	}
	if len(rows) != 0 {
		t.Fatalf("rows after TRUNCATE = %v, want none", rows)
	}

	// The truncate must not have created a parent file where none exists:
	// remove it, truncate again, and verify no resurrection.
	mgr.CloseRelation(parentRel)
	ppath := filepath.Join(mgr.DataDir(), mgr.RelPath(parentRel))
	if err := os.Remove(ppath); err != nil {
		t.Fatalf("remove parent file: %v", err)
	}
	if err := runDDL(t, ctx, "TRUNCATE pp"); err != nil {
		t.Fatalf("second TRUNCATE pp: %v", err)
	}
	if _, err := os.Stat(ppath); !os.IsNotExist(err) {
		t.Fatalf("TRUNCATE resurrected parent file %s (stat err = %v)", ppath, err)
	}
}
