package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// seedItems fills the items table with three rows so DML tests have a
// known starting state.
func seedItems(t *testing.T, ctx *Context, tbl *catalog.Table) {
	t.Helper()
	in := &planner.Insert{
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
	op, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("seed insert: %v", err)
	}
	_ = op.Close()
}

// TestUpdateRewritesMatchingRows pins the v0 update protocol: matching
// tuples have their xmax stamped (so they vanish from a subsequent
// scan via TupleVisible's xmax-equals-current-xact branch), and a
// fresh tuple carrying the new row is appended.
func TestUpdateRewritesMatchingRows(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	// UPDATE items SET label = 'updated' WHERE id = 2
	upd := &planner.Update{
		Table: tbl,
		Child: &planner.Filter{
			Child: &planner.SeqScan{Table: tbl},
			Predicate: &planner.BinaryOp{
				Op:    "=",
				Left:  &planner.ColumnRef{Index: 0, Name: "id", Type: catalog.Type{Name: "int4"}},
				Right: &planner.IntegerConst{Value: 2},
			},
		},
		Set: []planner.Expr{nil, &planner.StringConst{Value: "updated"}},
	}
	op, err := Build(upd)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Update.Next: %v", err)
	}
	if uo := op.(*updateOp); uo.RowsAffected != 1 {
		t.Errorf("RowsAffected=%d want 1", uo.RowsAffected)
	}
	_ = op.Close()

	// Scan back: same xact should see id=1 alpha, id=3 gamma, and the
	// new id=2 updated. The old id=2 beta is invisible because its
	// xmax = ctx.Tx.XID (TupleVisible's "deleted by current xact"
	// branch returns false).
	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	_ = scan.Open(ctx)
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, r := range rows {
		got[r[0].Int] = r[1].String
	}
	want := map[int64]string{1: "alpha", 2: "updated", 3: "gamma"}
	if len(got) != len(want) {
		t.Fatalf("rows=%v want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%d]=%q want %q", k, got[k], v)
		}
	}
}

// TestDeleteStampsXmax: matching tuples become invisible after delete
// without removing the page bytes.
func TestDeleteStampsXmax(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	// DELETE FROM items WHERE id = 2
	del := &planner.Delete{
		Table: tbl,
		Child: &planner.Filter{
			Child: &planner.SeqScan{Table: tbl},
			Predicate: &planner.BinaryOp{
				Op:    "=",
				Left:  &planner.ColumnRef{Index: 0, Name: "id", Type: catalog.Type{Name: "int4"}},
				Right: &planner.IntegerConst{Value: 2},
			},
		},
	}
	op, err := Build(del)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = op.Next()
	if d := op.(*deleteOp); d.RowsAffected != 1 {
		t.Errorf("RowsAffected=%d want 1", d.RowsAffected)
	}
	_ = op.Close()

	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	_ = scan.Open(ctx)
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}
	for _, r := range rows {
		if r[0].Int == 2 {
			t.Errorf("deleted row still visible: %+v", r)
		}
	}
}

// TestDeleteAllRowsWithoutPredicate: no Filter wrapping the SeqScan
// in the child plan means every row matches.
func TestDeleteAllRowsWithoutPredicate(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	del := &planner.Delete{
		Table: tbl,
		Child: &planner.SeqScan{Table: tbl},
	}
	op, _ := Build(del)
	_ = op.Open(ctx)
	_, _ = op.Next()
	if d := op.(*deleteOp); d.RowsAffected != 3 {
		t.Errorf("RowsAffected=%d want 3", d.RowsAffected)
	}
	_ = op.Close()

	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	_ = scan.Open(ctx)
	defer scan.Close()
	rows, _ := drainScan(scan)
	if len(rows) != 0 {
		t.Errorf("rows=%d want 0", len(rows))
	}
}
