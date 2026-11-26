package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestApplyLocalIndexSelection covers FindLogicalRepLocalIndex /
// IsIndexUsableForReplicaIdentityFull (M0149-0054a,
// subscription/t/032_subscribe_use_index.pl): the primary key wins; under
// REPLICA IDENTITY FULL the first index in OID order whose leftmost key is
// a plain column the remote relation has is used, and partial or
// expression-leading indexes are not; DEFAULT identity without a key uses
// no index.
func TestApplyLocalIndexSelection(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "people"}, []catalog.Column{
		{Name: "firstname", Type: catalog.Type{Name: "text"}, Ordinal: 0},
		{Name: "lastname", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string, cols []string, method string, primary bool) *catalog.Index {
		t.Helper()
		idx, err := cat.CreateIndex(parser.ObjectName{Name: name}, tbl, cols, primary, method, primary)
		if err != nil {
			t.Fatal(err)
		}
		return idx
	}
	exprOnly := mk("people_expr_only", []string{""}, "btree", false)
	exprOnly.ColExprs = []*parser.Expr{new(parser.Expr)}
	partial := mk("people_partial", []string{"firstname"}, "btree", false)
	partial.HasPredicate = true
	gin := mk("people_gin", []string{"lastname"}, "gin", false)
	_ = gin
	ctx := NewContext()
	ctx.Catalog = cat
	full := &xlog.DecodedRelation{Replident: 'f'}
	if got := applyLocalIndex(ctx, tbl, full, nil); got != nil {
		t.Fatalf("expression-only / partial / gin indexes chosen: %s", got.Name)
	}
	hash := mk("people_hash", []string{"lastname"}, "hash", false)
	if got := applyLocalIndex(ctx, tbl, full, nil); got != hash {
		t.Fatalf("FULL: got %v, want people_hash", got)
	}
	// The leftmost column must be one the remote relation has.
	if got := applyLocalIndex(ctx, tbl, full, []bool{true, false}); got != nil {
		t.Fatalf("FULL with lastname missing remotely: got %s, want none", got.Name)
	}
	if got := applyLocalIndex(ctx, tbl, &xlog.DecodedRelation{Replident: 'd'}, nil); got != nil {
		t.Fatalf("DEFAULT identity without a key: got %s, want none", got.Name)
	}
	pk := mk("people_pkey", []string{"firstname"}, "btree", true)
	if got := applyLocalIndex(ctx, tbl, full, nil); got != pk {
		t.Fatalf("primary key not preferred: got %v", got)
	}
}

// TestApplyUpdateReplacesOneOfIdenticalRows: RelationFindReplTupleSeq stops
// at the first match, so a REPLICA IDENTITY FULL UPDATE of one of two
// identical rows changes one of them (goopg's earlier lookup deleted every
// match and inserted one row back, losing the duplicate). M0149-0054a.
func TestApplyUpdateReplacesOneOfIdenticalRows(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pool.Close()
		_ = mgr.Close()
	})
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	mvcc := transam.NewManager()
	newCtx := func() *Context {
		t.Helper()
		tx, err := mvcc.Begin(transam.IsolationReadCommitted)
		if err != nil {
			t.Fatal(err)
		}
		ctx := NewContext()
		ctx.Pool, ctx.Catalog, ctx.TxnMgr, ctx.Tx = pool, cat, mvcc, tx
		if err := ctx.MaterializeWriterXID(); err != nil {
			t.Fatal(err)
		}
		ctx.Snap, _ = mvcc.SnapshotFor(tx)
		return ctx
	}
	rel := cat.RelFileNode(tbl)
	seed := newCtx()
	for i := 0; i < 2; i++ {
		if _, err := writeHeapRowReturning(seed, rel, tbl.Columns, Row{NewIntDatum(1)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := mvcc.Commit(seed.Tx); err != nil {
		t.Fatal(err)
	}
	apply := newCtx()
	if err := applyUpdateByKey(apply, rel, tbl, tbl.Columns, Row{NewIntDatum(1)}, Row{NewIntDatum(2)}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := mvcc.Commit(apply.Tx); err != nil {
		t.Fatal(err)
	}
	read := newCtx()
	defer mvcc.Rollback(read.Tx)
	scan := newSeqScanOp(&optimizer.SeqScan{Table: tbl})
	if err := scan.Open(read); err != nil {
		t.Fatal(err)
	}
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	ones, twos := 0, 0
	for _, r := range rows {
		switch r[0].Int {
		case 1:
			ones++
		case 2:
			twos++
		}
	}
	if ones != 1 || twos != 1 {
		t.Fatalf("after updating one of two identical rows: x=1 ×%d, x=2 ×%d; want 1 and 1", ones, twos)
	}
}
