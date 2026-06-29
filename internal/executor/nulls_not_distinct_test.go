package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// nndNewTable creates a fresh table with two nullable int4 columns (a, b) in the
// fixture catalog plus a UNIQUE btree index over `idxCols`. When nnd is true the
// index is marked NULLS NOT DISTINCT. Design 0119-0004.
func nndNewTable(t *testing.T, ctx *Context, cat catalog.Catalog, name string, idxCols []string, nnd bool) *catalog.Table {
	t.Helper()
	tbl, err := cat.(*catalog.InMemory).CreateTable(parser.ObjectName{Name: name}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatalf("CreateTable %s: %v", name, err)
	}
	idx, err := cat.CreateIndex(parser.ObjectName{Name: name + "_key"}, tbl, idxCols, true /*unique*/, "btree", false /*primary*/)
	if err != nil {
		t.Fatalf("CreateIndex %s: %v", name, err)
	}
	idx.NullsNotDistinct = nnd
	if _, err := btree.Create(ctx.Pool, cat.IndexRelFileNode(idx)); err != nil {
		t.Fatalf("btree.Create %s: %v", name, err)
	}
	return tbl
}

// nndInsert runs a single-row INSERT through the executor (which invokes the
// uniqueness check) and returns its error (nil on success).
func nndInsert(t *testing.T, ctx *Context, tbl *catalog.Table, vals ...planner.Expr) error {
	t.Helper()
	op, err := Build(&planner.Insert{
		Table:       tbl,
		Source:      &planner.Values{Rows: [][]planner.Expr{vals}},
		ColumnIndex: []int{0, 1},
	})
	if err != nil {
		return err
	}
	if err := op.Open(ctx); err != nil {
		return err
	}
	_, err = op.Next()
	_ = op.Close()
	if err == EOF {
		return nil
	}
	return err
}

func nndAssert23505(t *testing.T, err error, wantConstraint, wantDetailSub string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected 23505 duplicate-key error, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("want *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23505" {
		t.Fatalf("Code=%q want 23505 (err=%v)", ee.Code, err)
	}
	if !strings.Contains(ee.Message, "duplicate key value violates unique constraint") {
		t.Errorf("Message=%q missing upstream prefix", ee.Message)
	}
	if !strings.Contains(ee.Message, wantConstraint) {
		t.Errorf("Message=%q does not name constraint %q", ee.Message, wantConstraint)
	}
	if wantDetailSub != "" && !strings.Contains(ee.Detail, wantDetailSub) {
		t.Errorf("Detail=%q missing %q", ee.Detail, wantDetailSub)
	}
}

// TestNullsNotDistinctSingleColEnforced: a UNIQUE NULLS NOT DISTINCT index over
// one nullable column rejects a second NULL-keyed row (PG 18.3 treats the NULLs
// as equal), while still rejecting duplicate non-NULL keys.
func TestNullsNotDistinctSingleColEnforced(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl := nndNewTable(t, ctx, cat, "nnd1", []string{"a"}, true)

	null := func() planner.Expr { return &planner.NullConst{} }
	i := func(v int64) planner.Expr { return &planner.IntegerConst{Value: v} }

	if err := nndInsert(t, ctx, tbl, null(), i(1)); err != nil {
		t.Fatalf("first NULL insert should succeed: %v", err)
	}
	// Second NULL key collides under NULLS NOT DISTINCT.
	nndAssert23505(t, nndInsert(t, ctx, tbl, null(), i(2)), "nnd1_key", "null")

	// Non-NULL keys keep the ordinary btree behaviour.
	if err := nndInsert(t, ctx, tbl, i(5), i(3)); err != nil {
		t.Fatalf("non-NULL insert should succeed: %v", err)
	}
	nndAssert23505(t, nndInsert(t, ctx, tbl, i(5), i(4)), "nnd1_key", "5")
}

// TestNullsDistinctControlAllowsDuplicateNulls: the default (NULLS DISTINCT)
// index must continue to admit multiple NULL-keyed rows — proving the new branch
// is strictly gated on idx.NullsNotDistinct.
func TestNullsDistinctControlAllowsDuplicateNulls(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl := nndNewTable(t, ctx, cat, "nnd2", []string{"a"}, false)

	null := func() planner.Expr { return &planner.NullConst{} }
	i := func(v int64) planner.Expr { return &planner.IntegerConst{Value: v} }

	if err := nndInsert(t, ctx, tbl, null(), i(1)); err != nil {
		t.Fatalf("first NULL insert: %v", err)
	}
	if err := nndInsert(t, ctx, tbl, null(), i(2)); err != nil {
		t.Fatalf("second NULL insert under NULLS DISTINCT should succeed: %v", err)
	}
}

// TestNullsNotDistinctMultiColEnforced: a two-column NND index treats two rows
// equal iff their NULL pattern AND non-NULL values both match.
func TestNullsNotDistinctMultiColEnforced(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl := nndNewTable(t, ctx, cat, "nnd3", []string{"a", "b"}, true)

	null := func() planner.Expr { return &planner.NullConst{} }
	i := func(v int64) planner.Expr { return &planner.IntegerConst{Value: v} }

	if err := nndInsert(t, ctx, tbl, null(), i(5)); err != nil {
		t.Fatalf("(NULL,5): %v", err)
	}
	// Same NULL pattern + equal non-NULL tail → conflict.
	nndAssert23505(t, nndInsert(t, ctx, tbl, null(), i(5)), "nnd3_key", "null")
	// Different non-NULL tail → no conflict.
	if err := nndInsert(t, ctx, tbl, null(), i(6)); err != nil {
		t.Fatalf("(NULL,6) should not conflict with (NULL,5): %v", err)
	}
	// NULL in the other position is a distinct pattern from (NULL,5)/(NULL,6).
	if err := nndInsert(t, ctx, tbl, i(5), null()); err != nil {
		t.Fatalf("(5,NULL): %v", err)
	}
	nndAssert23505(t, nndInsert(t, ctx, tbl, i(5), null()), "nnd3_key", "null")
	if err := nndInsert(t, ctx, tbl, i(6), null()); err != nil {
		t.Fatalf("(6,NULL) should not conflict with (5,NULL): %v", err)
	}
}

// TestNullsNotDistinctUpdateNoSelfConflict: a no-key-change NULL→NULL UPDATE must
// not self-conflict (the old version is stamped dead before the check), yet the
// NND constraint stays enforced for a subsequent duplicate NULL INSERT.
func TestNullsNotDistinctUpdateNoSelfConflict(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl := nndNewTable(t, ctx, cat, "nnd4", []string{"a"}, true)

	null := func() planner.Expr { return &planner.NullConst{} }
	i := func(v int64) planner.Expr { return &planner.IntegerConst{Value: v} }

	if err := nndInsert(t, ctx, tbl, null(), i(1)); err != nil {
		t.Fatalf("seed (NULL,1): %v", err)
	}

	// UPDATE nnd4 SET b = 2 WHERE b = 1  (a stays NULL — no key change).
	upd := &planner.Update{
		Table: tbl,
		Child: &planner.Filter{
			Child: &planner.SeqScan{Table: tbl},
			Predicate: &planner.BinaryOp{
				Op:    parser.OpEq,
				Left:  &planner.ColumnRef{Index: 1, Name: "b", Type: catalog.Type{Name: "int4"}},
				Right: &planner.IntegerConst{Value: 1},
			},
		},
		Set: []planner.Expr{nil, &planner.IntegerConst{Value: 2}},
	}
	op, err := Build(upd)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("no-key-change NULL→NULL UPDATE should succeed, got: %v", err)
	}
	if uo := op.(*updateOp); uo.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1", uo.RowsAffected())
	}
	_ = op.Close()

	// Constraint still enforced: a fresh duplicate NULL INSERT must fail.
	nndAssert23505(t, nndInsert(t, ctx, tbl, null(), i(9)), "nnd4_key", "null")
}
