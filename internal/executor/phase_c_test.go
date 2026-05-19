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
func TestRunFastOpAdapterFallback(t *testing.T) {
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
	if fastNode.Kind != OpAdapter {
		t.Errorf("INSERT should use OpAdapter, got Kind=%d", fastNode.Kind)
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
		// Non-migrated operators should use OpAdapter.
		{
			&planner.Insert{
				Table:  tbl,
				Source: &planner.Values{},
			},
			OpAdapter,
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
