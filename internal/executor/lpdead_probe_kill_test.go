package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// newHOTFixtureWAL is newHOTFixture with the pool's WAL emitters wired
// (LogPageImage + LogBtreeVacuum + WALFrontier). btree.KillItems self-guards
// to a no-op unless those hooks are present (lpdead_kill.go), so an end-to-end
// kill-marking assertion needs them. Counting no-op emitters returning a
// monotonic LSN are enough — mirrors the btree package's newTestTreeWAL.
func newHOTFixtureWAL(t *testing.T) (*Context, catalog.Catalog, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	var lsn storage.LSN = 100
	next := func() storage.LSN { lsn += 8; return lsn }
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots: 64,
		LogPageImage: func(_ storage.RelFileNode, _ storage.BlockNumber, _ storage.Page) (storage.LSN, error) {
			return next(), nil
		},
		LogBtreeVacuum: func(_ storage.RelFileNode, _ storage.BlockNumber, _ [][]byte, _ uint16) (storage.LSN, error) {
			return next(), nil
		},
		WALFrontier: func() uint64 { return uint64(lsn) },
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	mgrMVCC := mvcc.NewManager()
	tx, err := mgrMVCC.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := mgrMVCC.SnapshotFor(tx)
	if err != nil {
		t.Fatal(err)
	}
	ctx := NewContext()
	ctx.Pool = pool
	ctx.Catalog = cat
	ctx.TxnMgr = mgrMVCC
	ctx.Tx = tx
	ctx.Snap = snap
	cleanup := func() {
		_ = mgrMVCC.Rollback(tx)
		_ = pool.Close()
		_ = mgr.Close()
	}
	return ctx, cat, cleanup
}

// liveIndexKeys enumerates the int4 keys of all NON-dead entries in the named
// index via a plain RangeScan (which skips LP_DEAD line pointers but collects
// no kills of its own — a pure read).
func liveIndexKeys(t *testing.T, ctx *Context, cat catalog.Catalog, idxName string) map[int32]bool {
	t.Helper()
	idx, ok := cat.LookupIndex(parser.ObjectName{Name: idxName})
	if !ok {
		t.Fatalf("index %q not found", idxName)
	}
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		t.Fatalf("btree.Open(%s): %v", idxName, err)
	}
	out := map[int32]bool{}
	if err := tree.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		v, derr := btree.DecodeInt4(key)
		if derr != nil {
			return false, derr
		}
		out[v] = true
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan(%s): %v", idxName, err)
	}
	return out
}

// TestUpdateProbeCollectsLPDeadKill verifies the 08-03 migration: the UPDATE
// index probe (updateViaIndex) now collects an LP_DEAD kill for a stale index
// entry whose HOT chain is dead-to-all (the residual pkey-doubling driver),
// while never killing a live entry.
//
// Scenario:
//  1. T1: INSERT id=1; CREATE INDEX on id; UPDATE id 1->2 (non-HOT — an indexed
//     column changes). The index now holds key=1 -> dead v0 (stale) and
//     key=2 -> live v1. COMMIT T1.
//  2. T2 begins: OldestXmin advances past T1, so v0 is dead-to-all.
//  3. T2: UPDATE ... WHERE id=1 drives the probe over key=1, hits the stale
//     dead-pointing entry (followHOTChain !found), collects + marks the kill;
//     no live row matches key=1.
//  4. Assert: the index's live keys drop key=1 (marked LP_DEAD) but retain
//     key=2, and the live id=2 row is untouched (no over-kill).
//
// The assertion is self-validating: it can only pass if the UPDATE probe ran
// updateViaIndex and killed the stale entry — a seq-scan fallback would leave
// key=1 present, and an over-kill would drop key=2.
func TestUpdateProbeCollectsLPDeadKill(t *testing.T) {
	ctx, cat, cleanup := newHOTFixtureWAL(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int, v text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1, 'a')"); err != nil {
		t.Fatal(err)
	}
	// Create index AFTER the insert so the backfill captures the row.
	if err := runDDL(t, ctx, "CREATE INDEX t_id ON t (id)"); err != nil {
		t.Fatal(err)
	}
	// Non-HOT update: changing the indexed column disables HOT, leaving
	// key=1 -> dead v0 in the index and inserting key=2 -> live v1.
	if err := runDDL(t, ctx, "UPDATE t SET id = 2 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	// COMMIT T1, begin T2 — OldestXmin now exceeds T1.XID so v0 is dead-to-all.
	commitTx(t, ctx)
	beginTx(t, ctx)

	// Baseline (post-T1): the stale key=1 index entry is still live in the
	// index (only its heap target v0 is dead), alongside the live key=2.
	before := liveIndexKeys(t, ctx, cat, "t_id")
	if !before[1] || !before[2] {
		t.Fatalf("pre-condition: expected index keys {1,2} live before T2, got %v", before)
	}

	// T2: probe over key=1. No live row matches, but the stale dead-pointing
	// entry must be collected as an LP_DEAD kill and marked.
	if err := runDDL(t, ctx, "UPDATE t SET v = 'x' WHERE id = 1"); err != nil {
		t.Fatalf("T2 update: %v", err)
	}

	after := liveIndexKeys(t, ctx, cat, "t_id")
	if after[1] {
		t.Errorf("stale key=1 entry survived the UPDATE probe (kill not collected/marked); keys=%v", after)
	}
	if !after[2] {
		t.Errorf("live key=2 entry was killed (over-kill); keys=%v", after)
	}

	// No-over-kill, end-to-end: the live id=2 row is still readable and its
	// (untouched) value is intact — the WHERE id=1 update matched nothing.
	rows := runQuery(t, ctx, "SELECT v FROM t WHERE id = 2")
	if len(rows) != 1 {
		t.Fatalf("expected 1 live row for id=2, got %d", len(rows))
	}
	if rows[0][0].Kind != KindString || rows[0][0].StringValue() != "a" {
		t.Errorf("id=2 row value changed unexpectedly: got %+v, want 'a'", rows[0][0])
	}
}
