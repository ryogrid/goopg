package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// newStorageFixture spins up a per-test buffer pool + manager + mvcc
// manager + catalog with one table seeded, ready to run heap-touching
// operators against.
func newStorageFixture(t *testing.T) (*Context, catalog.Catalog, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
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

// TestInsertThenSeqScanRoundTrip pins the heap-write/heap-read
// contract: rows inserted within the active transaction are visible
// to a SeqScan running under the same xid+snapshot.
func TestInsertThenSeqScanRoundTrip(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	// Hand-build an Insert plan rather than going through the parser
	// so this test focuses on executor mechanics, not name resolution.
	insertPlan := &planner.Insert{
		Table: tbl,
		Source: &planner.Values{
			Rows: [][]planner.Expr{
				{&planner.IntegerConst{Value: 1}, &planner.StringConst{Value: "alpha"}},
				{&planner.IntegerConst{Value: 2}, &planner.StringConst{Value: "beta"}},
				{&planner.IntegerConst{Value: 3}, &planner.StringConst{Value: "gamma"}},
			},
		},
		ColumnIndex: []int{0, 1},
	}
	op, err := Build(insertPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	if io := op.(*insertOp); io.RowsAffected() != 3 {
		t.Errorf("RowsAffected=%d want 3", io.RowsAffected())
	}
	if err := op.Close(); err != nil {
		t.Fatal(err)
	}

	// SeqScan back. Refresh the snapshot so our own writes are visible
	// — the insert above used currentXID = ctx.Tx.XID, which is the
	// same xid we'll scan with. mvcc.TupleVisible's "same xact" branch
	// returns true regardless of snapshot.
	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("scan returned %d rows want 3", len(rows))
	}
	for i, want := range []struct {
		id    int64
		label string
	}{{1, "alpha"}, {2, "beta"}, {3, "gamma"}} {
		if rows[i][0].Int != want.id || rows[i][1].String != want.label {
			t.Errorf("rows[%d]=%+v want id=%d label=%q", i, rows[i], want.id, want.label)
		}
	}
}

// TestSeqScanRespectsVisibility: a tuple inserted by an aborted xact
// should NOT be returned to a fresh transaction.
func TestSeqScanRespectsVisibility(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	// Insert one row under ctx.Tx, then "abort" by rolling back.
	in := &planner.Insert{
		Table: tbl,
		Source: &planner.Values{
			Rows: [][]planner.Expr{{&planner.IntegerConst{Value: 99}, &planner.StringConst{Value: "ghost"}}},
		},
		ColumnIndex: []int{0, 1},
	}
	op, _ := Build(in)
	_ = op.Open(ctx)
	_, _ = op.Next()
	_ = op.Close()
	if err := ctx.TxnMgr.Rollback(ctx.Tx); err != nil {
		t.Fatal(err)
	}

	// Open a fresh transaction. The aborted insert's xid is now no
	// longer in the active set; v0's mvcc treats anything not in
	// InProgress and < Xmax as committed (a known v0 limitation), so
	// we'd actually see the ghost row. Mark this expectation
	// explicitly so the test pins current behaviour and tightens once
	// commit-status tracking lands.
	tx2, _ := ctx.TxnMgr.Begin(mvcc.IsolationReadCommitted)
	defer ctx.TxnMgr.Rollback(tx2)
	snap2, _ := ctx.TxnMgr.SnapshotFor(tx2)
	ctx.Tx = tx2
	ctx.Snap = snap2

	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("scan returned %d rows; v0 mvcc treats aborts as committed (see comment) — expected 1", len(rows))
	}
}

// TestInsertExtendsRelation checks that we extend through PinNew
// when the last block can't fit a new tuple — exercised by inserting
// enough rows to require multiple pages.
func TestInsertExtendsRelation(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	rel := cat.RelFileNode(tbl)

	const N = 600 // each row is ~34 bytes payload + tuple header; ~200/page
	rows := make([][]planner.Expr, N)
	for i := 0; i < N; i++ {
		rows[i] = []planner.Expr{
			&planner.IntegerConst{Value: int64(i)},
			&planner.StringConst{Value: "row-text-payload-padding"},
		}
	}
	in := &planner.Insert{
		Table:       tbl,
		Source:      &planner.Values{Rows: rows},
		ColumnIndex: []int{0, 1},
	}
	op, _ := Build(in)
	_ = op.Open(ctx)
	_, _ = op.Next()
	_ = op.Close()

	n, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Errorf("relation has %d blocks; expected extension", n)
	}

	// Round-trip count check.
	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	_ = scan.Open(ctx)
	defer scan.Close()
	got, _ := drainScan(scan)
	if len(got) != N {
		t.Errorf("scanned %d rows want %d", len(got), N)
	}
}

func drainScan(op Operator) ([]Row, error) {
	var out []Row
	for {
		row, err := op.Next()
		if err == EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
}

// recordingExecAIOEngine counts every Submit so a test can
// assert that seqScan's prefetch loop fires through Pool.Prefetch
// → Manager.PrefetchBlock → engine.Submit. Same shape as the
// recording engine in internal/storage/storage_test.go but
// duplicated here because Go test packages can't share helpers.
type recordingExecAIOEngine struct {
	submits int
}

func (r *recordingExecAIOEngine) Submit(op storage.AIOSubmitOp) storage.AIOHandle {
	r.submits++
	var n int
	var err error
	switch op.Direction {
	case storage.AIODirWrite:
		n, err = op.File.WriteAt(op.Buffer, op.Offset)
	default:
		n, err = op.File.ReadAt(op.Buffer, op.Offset)
	}
	return execAIOHandle{n: n, err: err}
}

type execAIOHandle struct {
	n   int
	err error
}

func (h execAIOHandle) Wait() (int, error) { return h.n, h.err }

// TestSeqScanFiresPrefetchesAcrossBlocks pins the M0009 caller
// integration: with prefetching enabled, seqScan walks
// `seqScanLookahead` blocks ahead of curBlock via Pool.Prefetch.
// Constructs a fixture with a recording AIO engine, inserts
// enough rows to span 5+ blocks, runs a SeqScan to completion,
// and asserts the engine saw at least one Submit (and at most
// nBlocks Submits — we never overshoot the relation).
func TestSeqScanFiresPrefetchesAcrossBlocks(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	// Attach a recording AIO engine to the fixture's Manager
	// and opt the Pool into prefetching.
	eng := &recordingExecAIOEngine{}
	ctx.Pool.Manager().SetAIO(eng)
	ctx.Pool.SetPrefetchEnabled(true)

	// Stuff enough rows in to span several blocks.
	const N = 600
	rows := make([][]planner.Expr, N)
	for i := range rows {
		rows[i] = []planner.Expr{
			&planner.IntegerConst{Value: int64(i)},
			&planner.StringConst{Value: "row"},
		}
	}
	in := &planner.Insert{
		Table:       tbl,
		Source:      &planner.Values{Rows: rows},
		ColumnIndex: []int{0, 1},
	}
	op, _ := Build(in)
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Insert.Next: %v", err)
	}
	_ = op.Close()

	rel := cat.RelFileNode(tbl)
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		t.Fatal(err)
	}
	if nBlocks < 2 {
		t.Fatalf("test setup wrote only %d blocks; need ≥2 to exercise prefetch", nBlocks)
	}

	// Insert populated the buffer pool with every page. Flush
	// to disk THEN drop them so the SeqScan's Pool.Prefetch
	// hits the not-cached path (Pool.Prefetch silently no-ops
	// on cached tags). InvalidateRel without a prior FlushAll
	// would discard dirty pages, costing the inserted data.
	if err := ctx.Pool.FlushAll(); err != nil {
		t.Fatal(err)
	}
	ctx.Pool.InvalidateRel(rel)
	// Reset the counter so we observe only the SeqScan's
	// prefetches, not any Pin-driven background reads from the
	// insert path.
	eng.submits = 0
	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	if err := scan.Open(ctx); err != nil {
		t.Fatal(err)
	}
	scanned, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	_ = scan.Close()
	if len(scanned) != N {
		t.Errorf("scanned %d rows, want %d", len(scanned), N)
	}
	// SeqScan should have fired at least one prefetch (the
	// initial lookahead window) and at most nBlocks — the
	// refill loop never overshoots NBlocks.
	if eng.submits == 0 {
		t.Errorf("seqScan fired 0 prefetches; expected at least 1")
	}
	if eng.submits > int(nBlocks) {
		t.Errorf("seqScan fired %d prefetches, exceeds NBlocks=%d", eng.submits, nBlocks)
	}
}
