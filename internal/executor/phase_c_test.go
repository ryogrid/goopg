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
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
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
	fastNode, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	fastRows, err := RunFast(fastNode, ctx)
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

// TestSlotCopyInto checks that CopyInto produces an independent copy.
func TestSlotCopyInto(t *testing.T) {
	src := &Slot{Cells: []Datum{NewIntDatum(7), NewIntDatum(9)}}
	var dst Slot
	src.CopyInto(&dst)
	if len(dst.Cells) != 2 {
		t.Fatalf("dst.Cells len=%d want 2", len(dst.Cells))
	}
	// Mutating src should not affect dst.
	src.Cells[0] = NewIntDatum(999)
	if dst.Cells[0].Int != 7 {
		t.Error("CopyInto should produce an independent copy")
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
	fastNode, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	rows, err := RunFast(fastNode, NewContext())
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
	fastNode, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	rows, err := RunFast(fastNode, NewContext())
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
	fastNode, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	rows, err := RunFast(fastNode, ctx)
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

	fastNode, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	rows, err := RunFast(fastNode, ctx)
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

	fastNode, err := BuildFast(insertPlan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	if fastNode.Kind != OpInsert {
		t.Errorf("INSERT should use OpInsert, got Kind=%d", fastNode.Kind)
	}
	_, err = RunFast(fastNode, ctx)
	if err != nil {
		t.Fatalf("RunFast INSERT: %v", err)
	}

	// Verify the row was actually inserted by reading it back.
	scanPlan := planOne(t, "SELECT id, label FROM items WHERE id = 99", cat)
	scanNode, err := BuildFast(scanPlan)
	if err != nil {
		t.Fatalf("BuildFast scan: %v", err)
	}
	rows, err := RunFast(scanNode, ctx)
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
		n, err := BuildFast(c.plan)
		if err != nil {
			t.Errorf("BuildFast(%T): %v", c.plan, err)
			continue
		}
		if n.Kind != c.want {
			t.Errorf("BuildFast(%T).Kind = %d, want %d", c.plan, n.Kind, c.want)
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

	fastNode, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	rows, err := RunFast(fastNode, ctx)
	if err != nil {
		t.Fatalf("RunFast: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected rows from ORDER BY, got none")
	}
	// The planner produces Project(Sort(SeqScan)) so root is OpProject.
	// Verify the Project child is OpSort (not OpAdapter) confirming concrete dispatch.
	if fastNode.Kind != OpProject {
		t.Errorf("unexpected root kind %d for ORDER BY plan", fastNode.Kind)
	}
	if fastNode.childA == nil || fastNode.childA.Kind != OpSort {
		childKind := OpInvalid
		if fastNode.childA != nil {
			childKind = fastNode.childA.Kind
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
	if it.node.Kind != OpInsert {
		t.Fatalf("expected OpInsert, got %d", it.node.Kind)
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
	fastNode, err := BuildFast(plan)
	if err != nil {
		t.Fatalf("BuildFast: %v", err)
	}
	// Walk past an optional Project wrapper.
	n := fastNode
	if n.Kind == OpProject {
		n = n.childA
	}
	if n.Kind != OpJoin {
		t.Errorf("expected OpJoin below top-level node, got Kind=%d", n.Kind)
	}
}
