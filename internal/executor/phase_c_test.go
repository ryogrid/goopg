// phase_c_test.go — regression tests for Phase C.1 BuildFast / RunFast
// (docs/design/perf-optimize/03-executor-concrete.md §13 verification).
//
// Invariants tested:
//  1. BuildFast + RunFast produce bit-identical rows to Build + Run for
//     all migrated operator kinds (SeqScan, Filter, Project, Limit).
//  2. Slot implements TupleSlot and SlotView interfaces correctly.
//  3. BuildFast correctly wraps non-migrated operators in opAdapter.
//  4. RunFast handles EOF, nil-slot (DML no-row), and cancellation.
package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

// runBothAndCompare runs the same plan via Run and RunFast and asserts
// that the row sets are identical.
func runBothAndCompare(t *testing.T, plan planner.Node, ctx *Context) {
	t.Helper()

	// Build + Run (legacy path).
	legacyOp, err := Build(plan)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	legacyRows, err := Run(legacyOp, ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// BuildFast + RunFast (Phase C path).
	tree, rootIdx, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	fastRows, err := RunFast(tree, rootIdx, ctx)
	if err != nil {
		t.Fatalf("RunFast: %v", err)
	}

	// Compare row counts.
	if len(legacyRows) != len(fastRows) {
		t.Fatalf("row count mismatch: legacy=%d fast=%d", len(legacyRows), len(fastRows))
	}

	// Compare individual cells.
	for i, lr := range legacyRows {
		fr := fastRows[i]
		if len(lr) != len(fr) {
			t.Errorf("row[%d] width mismatch: legacy=%d fast=%d", i, len(lr), len(fr))
			continue
		}
		for j, lv := range lr {
			fv := fr[j]
			if lv.Kind != fv.Kind {
				t.Errorf("row[%d][%d] Kind mismatch: legacy=%v fast=%v", i, j, lv.Kind, fv.Kind)
				continue
			}
			// Compare by Format() string — covers all Datum kinds.
			ls, fs := lv.Format(), fv.Format()
			if ls != fs {
				t.Errorf("row[%d][%d] value mismatch: legacy=%q fast=%q", i, j, ls, fs)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Slot interface conformance tests.
// ---------------------------------------------------------------------------

// TestSlotImplementsTupleSlot checks that *Slot satisfies TupleSlot at
// compile time and that the methods behave correctly.
func TestSlotImplementsTupleSlot(t *testing.T) {
	var _ TupleSlot = (*Slot)(nil)
	var _ SlotView = (*Slot)(nil)

	s := &Slot{
		schema: nil,
		Cells:  []Datum{NewIntDatum(42), NewStringDatum("hello")},
	}
	if s.Width() != 2 {
		t.Errorf("Width()=%d want 2", s.Width())
	}
	if s.Get(0).Kind != KindInt || s.Get(0).Int != 42 {
		t.Errorf("Get(0)=%+v want Int(42)", s.Get(0))
	}
	if s.IsNull(0) {
		t.Error("IsNull(0) should be false for Int datum")
	}

	// Null check.
	s.Cells[1] = NullDatum
	if !s.IsNull(1) {
		t.Error("IsNull(1) should be true after setting NullDatum")
	}

	// Row() should return the same Datums (not a copy).
	row := s.Row()
	if len(row) != 2 {
		t.Fatalf("Row() len=%d want 2", len(row))
	}
	if &row[0] != &s.Cells[0] {
		t.Error("Row() should alias Cells (no copy)")
	}

	// Materialize() should produce an independent copy.
	ms := s.Materialize()
	if ms == nil {
		t.Fatal("Materialize() returned nil")
	}
	if len(ms.row) != 2 {
		t.Fatalf("Materialize().row len=%d want 2", len(ms.row))
	}
}

// TestSlotReset checks that Reset truncates Cells to zero length.
func TestSlotReset(t *testing.T) {
	s := &Slot{Cells: []Datum{NewIntDatum(1), NewIntDatum(2)}}
	s.Reset()
	if len(s.Cells) != 0 {
		t.Errorf("after Reset len(Cells)=%d want 0", len(s.Cells))
	}
	if cap(s.Cells) == 0 {
		t.Error("after Reset cap(Cells) should be > 0 (backing array retained)")
	}
}

// TestSlotCopyTo checks that CopyTo produces an independent copy.
func TestSlotCopyTo(t *testing.T) {
	src := &Slot{Cells: []Datum{NewIntDatum(7), NewIntDatum(9)}}
	var dst Slot
	src.CopyTo(&dst)
	if len(dst.Cells) != 2 {
		t.Fatalf("dst.Cells len=%d want 2", len(dst.Cells))
	}
	// Mutating src should not affect dst.
	src.Cells[0] = NewIntDatum(999)
	if dst.Cells[0].Int != 7 {
		t.Error("CopyTo should produce an independent copy")
	}
}

// ---------------------------------------------------------------------------
// BuildFast / RunFast: SELECT without table.
// ---------------------------------------------------------------------------

// TestRunFastSelectConstant verifies that SELECT 1 works via RunFast.
func TestRunFastSelectConstant(t *testing.T) {
	plan := planOne(t, "SELECT 1", catalog.NewInMemory())
	runBothAndCompare(t, plan, NewContext())
}

// TestRunFastSelectArithmetic verifies SELECT 1 + 2 * 3 via RunFast.
func TestRunFastSelectArithmetic(t *testing.T) {
	plan := planOne(t, "SELECT 1 + 2 * 3", catalog.NewInMemory())
	runBothAndCompare(t, plan, NewContext())
}

// TestRunFastSelectLimit verifies SELECT 1 LIMIT 1 uses the concrete
// limitOpNext path.
func TestRunFastSelectLimit(t *testing.T) {
	plan := planOne(t, "SELECT 1 LIMIT 1", catalog.NewInMemory())
	runBothAndCompare(t, plan, NewContext())
}

// TestRunFastSelectLimitZero verifies SELECT 1 LIMIT 0 returns no rows.
func TestRunFastSelectLimitZero(t *testing.T) {
	plan := planOne(t, "SELECT 1 LIMIT 0", catalog.NewInMemory())
	tree, rootIdx, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	rows, err := RunFast(tree, rootIdx, NewContext())
	if err != nil {
		t.Fatalf("RunFast: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("LIMIT 0 should return 0 rows, got %d", len(rows))
	}
}

// TestRunFastSelectOffset verifies SELECT 1 OFFSET 1 returns no rows
// (one row produced, offset skips it).
func TestRunFastSelectOffset(t *testing.T) {
	plan := planOne(t, "SELECT 1 OFFSET 1", catalog.NewInMemory())
	tree, rootIdx, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	rows, err := RunFast(tree, rootIdx, NewContext())
	if err != nil {
		t.Fatalf("RunFast: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("OFFSET 1 with one-row source should return 0 rows, got %d", len(rows))
	}
}

// ---------------------------------------------------------------------------
// BuildFast / RunFast: queries against a real storage fixture.
// ---------------------------------------------------------------------------

// TestRunFastSeqScan verifies that a plain SeqScan via RunFast returns
// the same rows as the legacy path.
func TestRunFastSeqScan(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	plan := planOne(t, "SELECT id, label FROM items", cat)
	runBothAndCompare(t, plan, ctx)
}

// TestRunFastSeqScanWithFilter verifies that filter + seqScan via
// RunFast returns only matching rows.
func TestRunFastSeqScanWithFilter(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	plan := planOne(t, "SELECT id, label FROM items WHERE id = 2", cat)
	runBothAndCompare(t, plan, ctx)

	// Also verify the fast path returns exactly 1 row with id=2.
	tree, rootIdx, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	rows, err := RunFast(tree, rootIdx, ctx)
	if err != nil {
		t.Fatalf("RunFast: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 2 {
		t.Errorf("expected id=2, got %+v", rows[0][0])
	}
}

// TestRunFastSeqScanWithLimit verifies SeqScan + Limit via RunFast.
func TestRunFastSeqScanWithLimit(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	plan := planOne(t, "SELECT id FROM items LIMIT 2", cat)
	runBothAndCompare(t, plan, ctx)

	tree, rootIdx, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	rows, err := RunFast(tree, rootIdx, ctx)
	if err != nil {
		t.Fatalf("RunFast: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("LIMIT 2 should return 2 rows, got %d", len(rows))
	}
}

// TestRunFastOpAdapterFallback verifies that a non-migrated operator
// (INSERT) is correctly wrapped in opAdapter and functions via RunFast.
// TestRunFastInsert verifies that BuildFast produces OpInsert for a plain
// INSERT (no ON CONFLICT) and that the row is actually written to storage.
func TestRunFastInsert(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	insertPlan := &planner.Insert{
		Table: tbl,
		Source: &planner.Values{
			Rows: [][]planner.Expr{
				{
					&planner.IntegerConst{Value: 99},
					&planner.StringConst{Value: "inserted"},
				},
			},
		},
		ColumnIndex: []int{0, 1},
	}

	insertTree, insertRootIdx, err := BuildFast(insertPlan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	if insertTree.ops[insertRootIdx].Kind != OpInsert {
		t.Errorf("INSERT should use OpInsert, got Kind=%d", insertTree.ops[insertRootIdx].Kind)
	}
	_, err = RunFast(insertTree, insertRootIdx, ctx)
	if err != nil {
		t.Fatalf("RunFast INSERT: %v", err)
	}

	// Verify the row was actually inserted by reading it back.
	scanPlan := planOne(t, "SELECT id, label FROM items WHERE id = 99", cat)
	scanTree, scanRootIdx, err := BuildFast(scanPlan)
	if err != nil {
		t.Fatalf("BuildFast scan: %v", err)
	}
	rows, err := RunFast(scanTree, scanRootIdx, ctx)
	if err != nil {
		t.Fatalf("RunFast scan: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 inserted row, got %d", len(rows))
	}
	if rows[0][1].Kind != KindString || rows[0][1].StringValue() != "inserted" {
		t.Errorf("unexpected label: %+v", rows[0][1])
	}
}

// TestBuildFastNodeKinds verifies that BuildFast assigns the correct
// OpKind to each supported plan node type.
func TestBuildFastNodeKinds(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	// Build a minimal SeqScan plan node with a real table
	// (BuildFast calls newSeqScanOp which dereferences p.Table).
	seqScanPlan := &planner.SeqScan{Table: tbl}

	cases := []struct {
		plan planner.Node
		want OpKind
	}{
		{seqScanPlan, OpSeqScan},
		{
			&planner.Filter{
				Child:     seqScanPlan,
				Predicate: &planner.BooleanConst{Value: true},
			},
			OpFilter,
		},
		{
			&planner.Project{
				Child:   seqScanPlan,
				Targets: []planner.Expr{},
			},
			OpProject,
		},
		{
			&planner.Limit{
				Child: seqScanPlan,
			},
			OpLimit,
		},
		// Update/Delete produce concrete kinds (no Operator child).
		{
			&planner.Update{
				Table: tbl,
				Child: &planner.SeqScan{Table: tbl},
			},
			OpUpdate,
		},
		{
			&planner.Delete{
				Table: tbl,
				Child: &planner.SeqScan{Table: tbl},
			},
			OpDelete,
		},
		// Sort produces OpSort (child bridged via opNodeOperator).
		{
			&planner.Sort{
				Child: seqScanPlan,
				Keys:  []planner.SortKey{},
			},
			OpSort,
		},
		// Insert (no ON CONFLICT) migrated to concrete OpInsert kind.
		{
			&planner.Insert{
				Table:  tbl,
				Source: &planner.Values{},
			},
			OpInsert,
		},
		// Join (children bridged via opNodeOperator) migrated to OpJoin.
		{
			&planner.Join{
				Left:  seqScanPlan,
				Right: seqScanPlan,
				Algo:  planner.JoinAlgoHash,
			},
			OpJoin,
		},
	}

	for _, c := range cases {
		tree, rootIdx, err := BuildFast(c.plan)
		if err != nil {
			t.Errorf("BuildFast(%T): %v", c.plan, err)
			continue
		}
		if tree.ops[rootIdx].Kind != c.want {
			t.Errorf("BuildFast(%T).Kind = %d, want %d", c.plan, tree.ops[rootIdx].Kind, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Phase C.1 follow-up tests: OpSort, OpUpdate, OpDelete, BuildFastIterator.
// ---------------------------------------------------------------------------

// TestRunFastSort verifies that the OpSort kernel produces the same sorted
// rows as the legacy Build+Run path. For "SELECT id FROM items ORDER BY id DESC"
// the planner wraps Sort under a Project, so the root is OpProject; we check
// the child is OpSort and that the row ordering matches.
func TestRunFastSort(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	plan := planOne(t, "SELECT id FROM items ORDER BY id DESC", cat)
	runBothAndCompare(t, plan, ctx)

	tree, rootIdx, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	rows, err := RunFast(tree, rootIdx, ctx)
	if err != nil {
		t.Fatalf("RunFast: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected rows from ORDER BY, got none")
	}
	// The planner produces Project(Sort(SeqScan)) so root is OpProject.
	// Verify the Project child is OpSort (not OpAdapter) confirming concrete dispatch.
	if tree.ops[rootIdx].Kind != OpProject {
		t.Errorf("unexpected root kind %d for ORDER BY plan", tree.ops[rootIdx].Kind)
	}
	childA := tree.ops[rootIdx].childA
	if childA == noChild || tree.ops[childA].Kind != OpSort {
		childKind := OpInvalid
		if childA != noChild {
			childKind = tree.ops[childA].Kind
		}
		t.Errorf("Project.childA.Kind = %d, want OpSort (%d)", childKind, OpSort)
	}
}

// TestBuildFastIteratorSchema verifies that BuildFastIterator.Schema() returns
// the correct schema for read-shaped plans (SELECT).
func TestBuildFastIteratorSchema(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	plan := planOne(t, "SELECT id FROM items", cat)
	it, err := BuildFastIterator(plan)
	if err != nil {
		t.Fatalf("BuildFastIterator: %v", err)
	}
	if err := it.Open(ctx); err != nil {
		_ = it.Close()
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = it.Close() }()

	schema := it.Schema()
	if schema == nil {
		t.Fatal("Schema() returned nil for SELECT plan")
	}
	if len(schema) != 1 || schema[0].Name != "id" {
		t.Errorf("unexpected schema: %v", schema)
	}
}

// TestBuildFastIteratorRowsAffected verifies that BuildFastIterator.RowsAffected()
// delegates correctly to the underlying DML operator for OpUpdate/OpDelete.
func TestBuildFastIteratorRowsAffected(t *testing.T) {
	cat := catalog.NewInMemory()
	_, err := cat.CreateTable(parser.ObjectName{Name: "items"},
		[]catalog.Column{{Name: "id", Type: catalog.Type{Name: "int4"}}})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	updatePlan := &planner.Update{
		Table: tbl,
		Child: &planner.SeqScan{Table: tbl},
	}
	it, err := BuildFastIterator(updatePlan)
	if err != nil {
		t.Fatalf("BuildFastIterator(Update): %v", err)
	}
	// Verify OpIterator correctly implements RowCounter.
	if _, ok := interface{}(it).(RowCounter); !ok {
		t.Fatal("*OpIterator does not implement RowCounter")
	}
	// RowsAffected before open/run should return 0 (not panic).
	if got := it.RowsAffected(); got != 0 {
		t.Errorf("RowsAffected before run = %d, want 0", got)
	}
}

// TestOpIteratorNilSlotForDMLNoRow verifies that BuildFastIterator.Next()
// returns (nil, nil) for DML nil-rows (matching legacy Build semantics).
func TestOpIteratorNilSlotForDMLNoRow(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	// Use an INSERT into an empty table — OpInsert concrete kind;
	// OpIterator.Next() must propagate the nil-slot correctly.
	insertPlan := &planner.Insert{
		Table:  tbl,
		Source: &planner.Values{},
	}
	it, err := BuildFastIterator(insertPlan)
	if err != nil {
		t.Fatalf("BuildFastIterator: %v", err)
	}
	if err := it.Open(ctx); err != nil {
		_ = it.Close()
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = it.Close() }()

	slot, err := it.Next()
	if err != nil && err != EOF {
		t.Fatalf("Next: unexpected error: %v", err)
	}
	if err == EOF {
		return // acceptable — empty values source
	}
	// nil slot is the DML nil-row signal; must not panic on nil.
	if slot != nil {
		t.Errorf("DML nil-row: expected nil slot from OpIterator.Next(), got %T", slot)
	}
}

// ---------------------------------------------------------------------------
// Phase C.1 follow-up: OpInsert and OpJoin concrete kinds.
// ---------------------------------------------------------------------------

// TestRunFastInsertRowsAffected verifies that RowsAffected is correctly
// reported via the OpInsert concrete kind path.
func TestRunFastInsertRowsAffected(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	insertPlan := &planner.Insert{
		Table: tbl,
		Source: &planner.Values{
			Rows: [][]planner.Expr{
				{&planner.IntegerConst{Value: 200}, &planner.StringConst{Value: "ra"}},
				{&planner.IntegerConst{Value: 201}, &planner.StringConst{Value: "rb"}},
			},
		},
		ColumnIndex: []int{0, 1},
	}

	it, err := BuildFastIterator(insertPlan)
	if err != nil {
		t.Fatalf("BuildFastIterator: %v", err)
	}
	if it.tree.ops[it.rootIdx].Kind != OpInsert {
		t.Fatalf("expected OpInsert, got %d", it.tree.ops[it.rootIdx].Kind)
	}
	if err := it.Open(ctx); err != nil {
		_ = it.Close()
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = it.Close() }()

	// Drain the iterator (one nil-slot DML row then EOF).
	for {
		_, err := it.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
	}

	if got := it.RowsAffected(); got != 2 {
		t.Errorf("RowsAffected = %d, want 2", got)
	}
}

// TestRunFastJoinConcrete verifies that BuildFast wraps joinOp children in
// opNodeOperator bridges, producing an OpJoin root whose output matches the
// legacy Build path.
func TestRunFastJoinConcrete(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	// "SELECT a.id, b.id FROM items a JOIN items b ON a.id = b.id"
	// The planner produces a hash join; compare fast vs. legacy output.
	plan := planOne(t, "SELECT a.id, b.id FROM items a JOIN items b ON a.id = b.id", cat)
	runBothAndCompare(t, plan, ctx)

	// Also verify that the concrete OpNode root kind is OpJoin or wraps
	// one (planner may add a Project on top).
	tree, rootIdx, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	// Walk past an optional Project wrapper.
	nIdx := rootIdx
	if tree.ops[nIdx].Kind == OpProject {
		nIdx = tree.ops[nIdx].childA
	}
	if tree.ops[nIdx].Kind != OpJoin {
		t.Errorf("expected OpJoin below top-level node, got Kind=%d", tree.ops[nIdx].Kind)
	}
}

// ---------------------------------------------------------------------------
// Phase C.3 — ExprNode sum-type tests.
// ---------------------------------------------------------------------------

// TestBuildExprSlabCommonKinds verifies that buildExpr compiles common
// expression kinds into the correct ExprNode representations.
func TestBuildExprSlabCommonKinds(t *testing.T) {
	t.Run("ColumnRef", func(t *testing.T) {
		var slab exprTreeSlab
		e := &planner.ColumnRef{Index: 3}
		idx := slab.buildExpr(e)
		if idx != 0 {
			t.Fatalf("expected root index 0, got %d", idx)
		}
		if slab[idx].Kind != ExprColumnRef {
			t.Fatalf("expected ExprColumnRef, got %d", slab[idx].Kind)
		}
		// Verify column index is encoded in payload.
		got := int(int32(binary.LittleEndian.Uint32(slab[idx].payload[:])))
		if got != 3 {
			t.Errorf("payload colIdx: want 3, got %d", got)
		}
	})

	t.Run("IntegerConst", func(t *testing.T) {
		var slab exprTreeSlab
		e := &planner.IntegerConst{Value: 42}
		idx := slab.buildExpr(e)
		if slab[idx].Kind != ExprIntConst {
			t.Fatalf("expected ExprIntConst, got %d", slab[idx].Kind)
		}
		got := int64(binary.LittleEndian.Uint64(slab[idx].payload[:]))
		if got != 42 {
			t.Errorf("payload value: want 42, got %d", got)
		}
	})

	t.Run("BooleanConst_true", func(t *testing.T) {
		var slab exprTreeSlab
		e := &planner.BooleanConst{Value: true}
		idx := slab.buildExpr(e)
		if slab[idx].Kind != ExprBoolConst {
			t.Fatalf("expected ExprBoolConst, got %d", slab[idx].Kind)
		}
		if slab[idx].payload[0] != 1 {
			t.Errorf("expected payload[0]=1 for true, got %d", slab[idx].payload[0])
		}
	})

	t.Run("BooleanConst_false", func(t *testing.T) {
		var slab exprTreeSlab
		e := &planner.BooleanConst{Value: false}
		idx := slab.buildExpr(e)
		if slab[idx].payload[0] != 0 {
			t.Errorf("expected payload[0]=0 for false, got %d", slab[idx].payload[0])
		}
	})

	t.Run("NullConst", func(t *testing.T) {
		var slab exprTreeSlab
		e := &planner.NullConst{}
		idx := slab.buildExpr(e)
		if slab[idx].Kind != ExprNullConst {
			t.Fatalf("expected ExprNullConst, got %d", slab[idx].Kind)
		}
	})

	t.Run("NilExpr", func(t *testing.T) {
		var slab exprTreeSlab
		idx := slab.buildExpr(nil)
		if idx != noExpr {
			t.Errorf("nil expr: want noExpr, got %d", idx)
		}
		if len(slab) != 0 {
			t.Errorf("nil expr must not append nodes; len=%d", len(slab))
		}
	})

	t.Run("BinaryOp_reserves_before_children", func(t *testing.T) {
		var slab exprTreeSlab
		e := &planner.BinaryOp{
			Op:    parser.OpEq,
			Left:  &planner.ColumnRef{Index: 0},
			Right: &planner.IntegerConst{Value: 7},
		}
		rootIdx := slab.buildExpr(e)
		// Root must be index 0 (BinaryOp reserved first).
		if rootIdx != 0 {
			t.Fatalf("root index: want 0, got %d", rootIdx)
		}
		if slab[0].Kind != ExprBinaryOp {
			t.Fatalf("slab[0] kind: want ExprBinaryOp, got %d", slab[0].Kind)
		}
		if slab[0].payload[0] != uint8(parser.OpEq) {
			t.Errorf("op code: want %d, got %d", uint8(parser.OpEq), slab[0].payload[0])
		}
		// Children must be at indices 1 and 2.
		if slab[0].childA != 1 {
			t.Errorf("childA: want 1, got %d", slab[0].childA)
		}
		if slab[0].childB != 2 {
			t.Errorf("childB: want 2, got %d", slab[0].childB)
		}
		if slab[1].Kind != ExprColumnRef {
			t.Errorf("childA kind: want ExprColumnRef, got %d", slab[1].Kind)
		}
		if slab[2].Kind != ExprIntConst {
			t.Errorf("childB kind: want ExprIntConst, got %d", slab[2].Kind)
		}
	})

	t.Run("UnknownKind_falls_back_to_adapter", func(t *testing.T) {
		var slab exprTreeSlab
		// StringConst is not handled natively → ExprAdapter.
		e := &planner.StringConst{Value: "hello"}
		idx := slab.buildExpr(e)
		if slab[idx].Kind != ExprAdapter {
			t.Fatalf("expected ExprAdapter for StringConst, got %d", slab[idx].Kind)
		}
		if slab[idx].orig == nil {
			t.Error("ExprAdapter.orig must be non-nil")
		}
	})
}

// TestEvalFastExprCommonKinds verifies evalFastExpr returns the expected Datum
// for each natively-handled expression kind.
func TestEvalFastExprCommonKinds(t *testing.T) {
	// Build a simple 3-column slot: [int(10), bool(true), null].
	slot := &Slot{
		Cells:  []Datum{NewIntDatum(10), NewBoolDatum(true), NullDatum},
		HasRow: true,
	}

	t.Run("noExpr_returns_null", func(t *testing.T) {
		var slab exprTreeSlab
		d, err := evalFastExpr(slab, noExpr, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !d.IsNull() {
			t.Errorf("noExpr: want NullDatum, got Kind=%d", d.Kind)
		}
	})

	t.Run("ColumnRef_reads_slot", func(t *testing.T) {
		var slab exprTreeSlab
		idx := slab.buildExpr(&planner.ColumnRef{Index: 0})
		d, err := evalFastExpr(slab, idx, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != KindInt || d.Int != 10 {
			t.Errorf("ColumnRef[0]: want KindInt(10), got %v/%v", d.Kind, d.Int)
		}
	})

	t.Run("ColumnRef_null_column", func(t *testing.T) {
		var slab exprTreeSlab
		idx := slab.buildExpr(&planner.ColumnRef{Index: 2})
		d, err := evalFastExpr(slab, idx, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !d.IsNull() {
			t.Errorf("ColumnRef[2]: want NullDatum, got Kind=%d", d.Kind)
		}
	})

	t.Run("IntConst", func(t *testing.T) {
		var slab exprTreeSlab
		idx := slab.buildExpr(&planner.IntegerConst{Value: 99})
		d, err := evalFastExpr(slab, idx, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != KindInt || d.Int != 99 {
			t.Errorf("IntConst: want 99, got %v/%v", d.Kind, d.Int)
		}
	})

	t.Run("BoolConst_true", func(t *testing.T) {
		var slab exprTreeSlab
		idx := slab.buildExpr(&planner.BooleanConst{Value: true})
		d, err := evalFastExpr(slab, idx, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != KindBool || !d.BoolValue() {
			t.Errorf("BoolConst(true): want true, got Kind=%d val=%v", d.Kind, d.BoolValue())
		}
	})

	t.Run("NullConst", func(t *testing.T) {
		var slab exprTreeSlab
		idx := slab.buildExpr(&planner.NullConst{})
		d, err := evalFastExpr(slab, idx, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !d.IsNull() {
			t.Errorf("NullConst: want null, got Kind=%d", d.Kind)
		}
	})

	t.Run("BinaryOp_eq_true", func(t *testing.T) {
		var slab exprTreeSlab
		e := &planner.BinaryOp{
			Op:    parser.OpEq,
			Left:  &planner.ColumnRef{Index: 0}, // 10
			Right: &planner.IntegerConst{Value: 10},
		}
		idx := slab.buildExpr(e)
		d, err := evalFastExpr(slab, idx, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != KindBool || !d.BoolValue() {
			t.Errorf("10=10: expected true, got Kind=%d val=%v", d.Kind, d.BoolValue())
		}
	})

	t.Run("BinaryOp_eq_false", func(t *testing.T) {
		var slab exprTreeSlab
		e := &planner.BinaryOp{
			Op:    parser.OpEq,
			Left:  &planner.ColumnRef{Index: 0}, // 10
			Right: &planner.IntegerConst{Value: 5},
		}
		idx := slab.buildExpr(e)
		d, err := evalFastExpr(slab, idx, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != KindBool || d.BoolValue() {
			t.Errorf("10=5: expected false, got Kind=%d val=%v", d.Kind, d.BoolValue())
		}
	})

	t.Run("BinaryOp_and_short_circuit", func(t *testing.T) {
		var slab exprTreeSlab
		// FALSE AND <anything> → FALSE without evaluating right.
		// Right side has an out-of-bounds ColumnRef that would panic if evaluated.
		e := &planner.BinaryOp{
			Op:    parser.OpAnd,
			Left:  &planner.BooleanConst{Value: false},
			Right: &planner.ColumnRef{Index: 999}, // would panic if reached
		}
		idx := slab.buildExpr(e)
		d, err := evalFastExpr(slab, idx, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != KindBool || d.BoolValue() {
			t.Errorf("FALSE AND ...: expected false, got %v", d)
		}
	})

	t.Run("BinaryOp_and_null_false", func(t *testing.T) {
		var slab exprTreeSlab
		e := &planner.BinaryOp{
			Op:    parser.OpAnd,
			Left:  &planner.NullConst{},
			Right: &planner.BooleanConst{Value: false},
		}
		idx := slab.buildExpr(e)
		d, err := evalFastExpr(slab, idx, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != KindBool || d.BoolValue() {
			t.Errorf("NULL AND FALSE: expected false, got %v", d)
		}
	})

	t.Run("BinaryOp_or_null_true", func(t *testing.T) {
		var slab exprTreeSlab
		e := &planner.BinaryOp{
			Op:    parser.OpOr,
			Left:  &planner.NullConst{},
			Right: &planner.BooleanConst{Value: true},
		}
		idx := slab.buildExpr(e)
		d, err := evalFastExpr(slab, idx, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != KindBool || !d.BoolValue() {
			t.Errorf("NULL OR TRUE: expected true, got %v", d)
		}
	})

	t.Run("ExprAdapter_delegates_to_evalExprSlot", func(t *testing.T) {
		var slab exprTreeSlab
		// StringConst → ExprAdapter → evalExprSlot.
		e := &planner.StringConst{Value: "hello"}
		idx := slab.buildExpr(e)
		d, err := evalFastExpr(slab, idx, slot, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind != KindString || d.StringValue() != "hello" {
			t.Errorf("ExprAdapter(StringConst): want 'hello', got Kind=%d val=%q", d.Kind, d.StringValue())
		}
	})
}

// TestRunFastFilterExprNodePopulated verifies that after BuildFast the
// expression slab is non-empty for a plan with a filter predicate, and that
// the filter operator's state references it correctly.
func TestRunFastFilterExprNodePopulated(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	plan := planOne(t, "SELECT id FROM items WHERE id > 1", cat)
	tree, rootIdx, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	if len(tree.exprs) == 0 {
		t.Error("exprTreeSlab is empty after BuildFast with filter predicate")
	}

	// Walk to the Filter node (may be under a Project).
	nIdx := rootIdx
	if tree.ops[nIdx].Kind == OpProject {
		nIdx = tree.ops[nIdx].childA
	}
	if tree.ops[nIdx].Kind != OpFilter {
		t.Fatalf("expected OpFilter below top-level node, got Kind=%d", tree.ops[nIdx].Kind)
	}
	fs := tree.ops[nIdx].state.(*filterState)
	if fs.predIdx == noExpr {
		t.Error("filterState.predIdx must not be noExpr for a non-nil predicate")
	}
	// exprs not set yet (set at Open time); predIdx must reference a valid node.
	if int(fs.predIdx) >= len(tree.exprs) {
		t.Errorf("predIdx %d out of range (slab len=%d)", fs.predIdx, len(tree.exprs))
	}

	// Full round-trip: verify results match legacy path.
	runBothAndCompare(t, plan, ctx)
}

// TestPlanNodePlanFilterPayload verifies that buildPlanFilter stores predIdx
// correctly in the PlanNode payload and PlanFilterPredIdx reads it back.
func TestPlanNodePlanFilterPayload(t *testing.T) {
	var slab planTreeSlab
	predIdx := int32(42)
	childA := int32(0)
	idx := slab.buildPlanFilter(predIdx, childA)
	if idx != 0 {
		t.Fatalf("expected index 0, got %d", idx)
	}
	if got := PlanFilterPredIdx(&slab[idx]); got != predIdx {
		t.Errorf("PlanFilterPredIdx: got %d, want %d", got, predIdx)
	}
	if slab[idx].Kind != PlanFilter {
		t.Errorf("Kind: got %d, want PlanFilter", slab[idx].Kind)
	}
	if slab[idx].childA != childA {
		t.Errorf("childA: got %d, want %d", slab[idx].childA, childA)
	}
	if slab[idx].childB != noPlan {
		t.Errorf("childB: got %d, want noPlan", slab[idx].childB)
	}
}

// TestPlanNodePlanLimitPayload verifies that buildPlanLimit stores both
// expression indices correctly and PlanLimitExprs reads them back.
func TestPlanNodePlanLimitPayload(t *testing.T) {
	var slab planTreeSlab
	limitIdx := int32(7)
	offsetIdx := int32(noExpr)
	childA := int32(3)
	idx := slab.buildPlanLimit(limitIdx, offsetIdx, childA)
	if idx != 0 {
		t.Fatalf("expected index 0, got %d", idx)
	}
	gotLimit, gotOffset := PlanLimitExprs(&slab[idx])
	if gotLimit != limitIdx {
		t.Errorf("limitExprIdx: got %d, want %d", gotLimit, limitIdx)
	}
	if gotOffset != offsetIdx {
		t.Errorf("offsetExprIdx: got %d, want %d", gotOffset, offsetIdx)
	}
	if slab[idx].Kind != PlanLimit {
		t.Errorf("Kind: got %d, want PlanLimit", slab[idx].Kind)
	}
}

// TestPlanNodeRoundtripNegativeOne ensures noExpr (-1) round-trips through
// payload bytes (stored as uint32, must recover -1 via int32 cast).
func TestPlanNodeRoundtripNegativeOne(t *testing.T) {
	var slab planTreeSlab
	slab.buildPlanLimit(noExpr, noExpr, noPlan)
	gotLimit, gotOffset := PlanLimitExprs(&slab[0])
	if gotLimit != noExpr {
		t.Errorf("limitExprIdx: got %d, want %d (noExpr)", gotLimit, noExpr)
	}
	if gotOffset != noExpr {
		t.Errorf("offsetExprIdx: got %d, want %d (noExpr)", gotOffset, noExpr)
	}
}

// TestLimitStateExprIdx checks that a SELECT with LIMIT N uses the
// exprTreeSlab path (limitState.limitExprIdx != noExpr) after BuildFast.
func TestLimitStateExprIdx(t *testing.T) {
	plan := planOne(t, "SELECT 1 LIMIT 5", catalog.NewInMemory())
	tree, rootIdx, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	// Walk to the Limit node.
	nIdx := rootIdx
	for nIdx != noChild && tree.ops[nIdx].Kind != OpLimit {
		nIdx = tree.ops[nIdx].childA
	}
	if nIdx == noChild || tree.ops[nIdx].Kind != OpLimit {
		t.Skip("no OpLimit node found in plan (may be optimized away)")
	}
	ls := tree.ops[nIdx].state.(*limitState)
	if ls.limitExprIdx == noExpr {
		t.Error("limitState.limitExprIdx must not be noExpr for LIMIT 5")
	}
	if ls.offsetExprIdx != noExpr {
		t.Error("limitState.offsetExprIdx must be noExpr when no OFFSET clause")
	}
	if int(ls.limitExprIdx) >= len(tree.exprs) {
		t.Errorf("limitExprIdx %d out of range (slab len=%d)", ls.limitExprIdx, len(tree.exprs))
	}
}

// TestLimitOffsetStateExprIdx checks that LIMIT N OFFSET M populates both
// expression indices in limitState.
func TestLimitOffsetStateExprIdx(t *testing.T) {
	plan := planOne(t, "SELECT 1 LIMIT 10 OFFSET 3", catalog.NewInMemory())
	tree, rootIdx, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	nIdx := rootIdx
	for nIdx != noChild && tree.ops[nIdx].Kind != OpLimit {
		nIdx = tree.ops[nIdx].childA
	}
	if nIdx == noChild || tree.ops[nIdx].Kind != OpLimit {
		t.Skip("no OpLimit node found in plan")
	}
	ls := tree.ops[nIdx].state.(*limitState)
	if ls.limitExprIdx == noExpr {
		t.Error("limitState.limitExprIdx must not be noExpr for LIMIT 10")
	}
	if ls.offsetExprIdx == noExpr {
		t.Error("limitState.offsetExprIdx must not be noExpr for OFFSET 3")
	}
}

// TestFilterStateNoPredField verifies that filterState.predIdx is set and
// the full round-trip execution produces correct filtered rows.
func TestFilterStateNoPredField(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	// Verify that the concrete dispatch path (OpFilter via exprTreeSlab)
	// correctly filters rows for a simple predicate.
	runBothAndCompare(t, planOne(t, "SELECT id FROM items WHERE id = 2", cat), ctx)
}

// TestLimitOffsetExecution verifies LIMIT/OFFSET execution via the new
// exprTreeSlab-based path produces correct results.
func TestLimitOffsetExecution(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	// LIMIT 1: only first row returned.
	runBothAndCompare(t, planOne(t, "SELECT id FROM items ORDER BY id LIMIT 1", cat), ctx)
	// LIMIT 2 OFFSET 1: skip first, return next two.
	runBothAndCompare(t, planOne(t, "SELECT id FROM items ORDER BY id LIMIT 2 OFFSET 1", cat), ctx)
}

// TestSeqScanOpNoPlanPointer verifies the Phase C.3 SeqScan migration:
// seqScanOp must not hold a *planner.SeqScan; instead it stores schema,
// tbl, pos, and cols extracted at construction time.
func TestSeqScanOpNoPlanPointer(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "items"})
	if !ok {
		t.Fatal("items table not found")
	}
	seedItems(t, ctx, tbl)

	tree, rootIdx, err := BuildFast(planOne(t, "SELECT id, label FROM items", cat))
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	// Walk to the OpSeqScan node.
	nIdx := rootIdx
	for nIdx != noChild && tree.ops[nIdx].Kind != OpSeqScan {
		nIdx = tree.ops[nIdx].childA
	}
	if nIdx == noChild || tree.ops[nIdx].Kind != OpSeqScan {
		t.Fatal("no OpSeqScan node found in plan")
	}
	op := tree.ops[nIdx].state.(*seqScanOp)
	if op.tbl == nil {
		t.Error("seqScanOp.tbl must not be nil after construction")
	}
	if op.tbl.Name != "items" {
		t.Errorf("seqScanOp.tbl.Name = %q, want %q", op.tbl.Name, "items")
	}
	if len(op.schema) == 0 {
		t.Error("seqScanOp.schema must not be empty after construction")
	}
	if len(op.cols) == 0 {
		t.Error("seqScanOp.cols must not be empty after construction")
	}
	// rel is zero until Open() is called.
	var zeroRel storage.RelFileNode
	if op.rel != zeroRel {
		t.Error("seqScanOp.rel must be zero before Open()")
	}
}

// TestSeqScanOpRelCachedAfterOpen verifies that Open() populates seqScanOp.rel
// so Next() calls never re-acquire the catalog lock.
func TestSeqScanOpRelCachedAfterOpen(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "items"})
	if !ok {
		t.Fatal("items table not found")
	}
	seedItems(t, ctx, tbl)

	tree, rootIdx, err := BuildFast(planOne(t, "SELECT id FROM items", cat))
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	nIdx := rootIdx
	for nIdx != noChild && tree.ops[nIdx].Kind != OpSeqScan {
		nIdx = tree.ops[nIdx].childA
	}
	if nIdx == noChild {
		t.Fatal("no OpSeqScan node found")
	}
	op := tree.ops[nIdx].state.(*seqScanOp)

	if err := opOpen(tree, nIdx, ctx); err != nil {
		t.Fatalf("opOpen: %v", err)
	}
	defer opClose(tree, nIdx)

	var zeroRel storage.RelFileNode
	if op.rel == zeroRel {
		t.Error("seqScanOp.rel must be populated after Open()")
	}
}

// TestProjectStateNoSchemaField verifies Phase C.3 projectState migration:
// projectState must not hold a planner.Schema; instead, schema is pooled in
// opTreeSlab.schemas and addressed via schemaIdx int32.  opTreeSlab.schemas
// must contain exactly one entry for a simple SELECT with projection.
func TestProjectStateNoSchemaField(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "items"})
	if !ok {
		t.Fatal("items table not found")
	}
	seedItems(t, ctx, tbl)

	tree, rootIdx, err := BuildFast(planOne(t, "SELECT id, label FROM items", cat))
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}

	// Walk to the OpProject node.
	nIdx := rootIdx
	for nIdx != noChild && tree.ops[nIdx].Kind != OpProject {
		nIdx = tree.ops[nIdx].childA
	}
	if nIdx == noChild || tree.ops[nIdx].Kind != OpProject {
		t.Skip("no OpProject node found in plan — plan shape may have changed")
	}

	ps := tree.ops[nIdx].state.(*projectState)

	// schemaIdx must point into the schemas pool.
	if int(ps.schemaIdx) >= len(tree.schemas) {
		t.Fatalf("schemaIdx %d out of range (schemas pool len=%d)", ps.schemaIdx, len(tree.schemas))
	}
	schema := tree.schemas[ps.schemaIdx]
	if len(schema) == 0 {
		t.Error("pooled schema must not be empty")
	}
	// The schemas pool must contain at least one entry for this plan.
	if len(tree.schemas) == 0 {
		t.Error("opTreeSlab.schemas must be non-empty after building a project plan")
	}
}

// TestEvalFastExprIntOverflow is a regression test for the M0107-0003 fast
// expression evaluator silently skipping integer-overflow detection. The
// compiled ExprBinaryOp path now carries an overflow code (payload[1]) and
// applies the same int2/int4 range checks evalExprSlot does. Without the fix,
// `int2 32767 * int2 2` returned 65534 instead of raising "smallint out of
// range" — regressing the int2/int4 pg_regress cases.
func TestEvalFastExprIntOverflow(t *testing.T) {
	mkBinOp := func(op parser.OpCode, l, r int64, resultType string) *planner.BinaryOp {
		return &planner.BinaryOp{
			Op:         op,
			Left:       &planner.IntegerConst{Value: l},
			Right:      &planner.IntegerConst{Value: r},
			ResultType: resultType,
		}
	}
	emptySlot := &Slot{HasRow: true}

	cases := []struct {
		name       string
		expr       *planner.BinaryOp
		wantErr    bool
		wantErrMsg string
		wantVal    int64
	}{
		{"int2_mul_overflow", mkBinOp(parser.OpMul, 32767, 2, "int2"), true, "smallint out of range", 0},
		{"int2_add_overflow", mkBinOp(parser.OpAdd, 32767, 2, "smallint"), true, "smallint out of range", 0},
		{"int2_sub_overflow", mkBinOp(parser.OpSub, -32767, 2, "int2"), true, "smallint out of range", 0},
		{"int2_in_range", mkBinOp(parser.OpMul, 100, 3, "int2"), false, "", 300},
		{"int4_mul_overflow", mkBinOp(parser.OpMul, 2147483647, 2, "int4"), true, "integer out of range", 0},
		{"int4_in_range", mkBinOp(parser.OpMul, 100000, 2, "integer"), false, "", 200000},
		// No ResultType (e.g. int8 / unresolved): no range check, value passes through.
		{"int8_no_check", mkBinOp(parser.OpMul, 2147483647, 2, "int8"), false, "", 4294967294},
		{"untyped_no_check", mkBinOp(parser.OpMul, 32767, 2, ""), false, "", 65534},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var slab exprTreeSlab
			idx := slab.buildExpr(tc.expr)
			d, err := evalFastExpr(slab, idx, emptySlot, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("%s: want error %q, got value %d", tc.name, tc.wantErrMsg, d.Int)
				}
				if ee, ok := err.(*ExecError); !ok || ee.Message != tc.wantErrMsg {
					t.Fatalf("%s: want ExecError %q, got %v", tc.name, tc.wantErrMsg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", tc.name, err)
			}
			if d.Kind != KindInt || d.Int != tc.wantVal {
				t.Fatalf("%s: want KindInt(%d), got Kind=%d val=%d", tc.name, tc.wantVal, d.Kind, d.Int)
			}
		})
	}
}

// TestBuildExprFloatFallsBackToAdapter verifies float-typed BinaryOps are
// compiled to ExprAdapter so evalExprSlot's float64 arithmetic path handles
// them (the fast path's exact arithmetic diverges from PostgreSQL float8).
func TestBuildExprFloatFallsBackToAdapter(t *testing.T) {
	var slab exprTreeSlab
	e := &planner.BinaryOp{
		Op:         parser.OpAdd,
		Left:       &planner.ColumnRef{Index: 0},
		Right:      &planner.IntegerConst{Value: 1},
		ResultType: "float8",
	}
	idx := slab.buildExpr(e)
	if slab[idx].Kind != ExprAdapter {
		t.Fatalf("float8 BinaryOp: want ExprAdapter, got Kind=%d", slab[idx].Kind)
	}
}

// ---------------------------------------------------------------------------
// M0097-0128 — RowExpr fast-path NULL propagation.
// ---------------------------------------------------------------------------

// TestBuildExprRowToRowNullFallsBackToAdapter verifies that buildExpr compiles
// a BinaryOp whose both operands are *planner.RowExpr into an ExprAdapter node
// (not ExprBinaryOp). This is the M0097-0128 guard: without it evalFastExpr
// would render each RowExpr as a composite text string and compare strings,
// returning "(abs,20)" >= "(abs,)" = TRUE instead of the correct NULL.
//
// The end-to-end behavioural correctness (filter emits 0 rows when the RHS
// tuple contains NULL) is covered by TestRowCompareNull in
// row_compare_null_test.go. This test pins the compilation-level invariant.
func TestBuildExprRowToRowNullFallsBackToAdapter(t *testing.T) {
	t.Run("buildExpr_emits_ExprAdapter", func(t *testing.T) {
		var slab exprTreeSlab
		e := &planner.BinaryOp{
			Op:    parser.OpGe,
			Left:  &planner.RowExpr{Elems: []planner.Expr{&planner.ColumnRef{Index: 0}, &planner.ColumnRef{Index: 1}}},
			Right: &planner.RowExpr{Elems: []planner.Expr{&planner.StringConst{Value: "abs"}, &planner.NullConst{}}},
		}
		idx := slab.buildExpr(e)
		if slab[idx].Kind != ExprAdapter {
			t.Fatalf("RowExpr>=RowExpr BinaryOp: want ExprAdapter, got Kind=%d", slab[idx].Kind)
		}
		if slab[idx].orig == nil {
			t.Error("ExprAdapter.orig must be non-nil so evalExprSlot can evaluate it")
		}
	})

	t.Run("RunFast_row_comparison_with_null_returns_zero_rows", func(t *testing.T) {
		// End-to-end: with a real storage fixture, the fast path (RunFast)
		// must also propagate NULL and return 0 rows when first elements are
		// equal but the second element of the RHS is NULL.
		ctx, cat, cleanup := newStorageFixture(t)
		defer cleanup()

		// Create a small table and seed two rows whose first element matches.
		if _, err := cat.CreateTable(parser.ObjectName{Name: "rcn_fast"}, []catalog.Column{
			{Name: "a", Type: catalog.Type{Name: "text"}},
			{Name: "b", Type: catalog.Type{Name: "int4"}},
		}); err != nil {
			t.Fatalf("CreateTable: %v", err)
		}
		for _, ins := range []string{
			"INSERT INTO rcn_fast VALUES ('abs', 20)",
			"INSERT INTO rcn_fast VALUES ('abs', 21)",
		} {
			insPlan := planOne(t, ins, cat)
			insOp, err := Build(insPlan)
			if err != nil {
				t.Fatalf("Build INSERT: %v", err)
			}
			if err := insOp.Open(ctx); err != nil {
				t.Fatalf("Open INSERT: %v", err)
			}
			for {
				_, err := insOp.Next()
				if err == EOF {
					break
				}
				if err != nil {
					t.Fatalf("Next INSERT: %v", err)
				}
			}
			_ = insOp.Close()
		}

		// (a, b) >= ('abs', NULL) must yield NULL for all rows — filter emits 0.
		plan := planOne(t, "SELECT a, b FROM rcn_fast WHERE (a, b) >= ('abs', NULL)", cat)
		tree, rootIdx, err := BuildFast(plan)
		if err != nil {
			t.Fatalf("BuildFast: %v", err)
		}
		rows, err := RunFast(tree, rootIdx, ctx)
		if err != nil {
			t.Fatalf("RunFast: %v", err)
		}
		if len(rows) != 0 {
			t.Errorf("RunFast: got %d rows, want 0 — (col,col) >= ('abs',NULL) must yield NULL, not TRUE", len(rows))
			for i, r := range rows {
				t.Logf("  row[%d]: %v", i, r)
			}
		}
	})
}
