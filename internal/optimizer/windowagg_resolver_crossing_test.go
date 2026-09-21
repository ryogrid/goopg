package optimizer

// M0145-0009 slice 4 — the `*WindowAgg` arm of the resolver family.
//
// B-06 gap G1 is that goopg's statistics resolution stops below the CTE
// boundary. The slice-3 census located it precisely: the window CTE bodies in
// TPC-DS are `Project(WindowAgg(Sort(...)))`, and every pass-through column
// declined because no walker could cross the `*WindowAgg`.
//
// The arm is sound because a WindowAgg is ROW-PRESERVING — one output row per
// input row, publishing `child row ++ func outputs`, values unchanged — so a
// pass-through coordinate describes exactly the child's column. That is
// precisely what the `*Aggregate` arm CANNOT claim (its rows are groups), and
// why that arm stays restricted to AggModePartial after M0127-P5.6-g-ii
// measured the unrestricted version worse.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func winArmTable() *catalog.Table {
	return &catalog.Table{
		Name: "warm_src",
		Columns: []catalog.Column{
			{Name: "cust", Type: catalog.Type{Name: "int4"}},
			{Name: "yr", Type: catalog.Type{Name: "int4"}},
		},
		Stats: &catalog.TableStats{
			RowCount: 100000,
			Columns:  []catalog.ColumnStats{{NDistinct: 40000}, {NDistinct: 5}},
		},
	}
}

// winArmNode builds `WindowAgg(Sort(SeqScan))` — the exact shape the corpus
// census found, since R6 stacks a Sort below every WindowAgg for presorted
// input. Output is child row ++ func outputs.
func winArmNode() *WindowAgg {
	tbl := winArmTable()
	scan := &SeqScan{Table: tbl, EstRelRows: 100000, schema: tableSchema(tbl)}
	sorted := &Sort{Child: scan}
	return &WindowAgg{
		Child:  sorted,
		Funcs:  []WindowFunc{{Name: "rank"}},
		schema: Schema{{Name: "cust"}, {Name: "yr"}, {Name: "rnk"}},
	}
}

func TestResolveBaseColumnCrossesWindowAgg(t *testing.T) {
	win := winArmNode()

	// Pass-through columns resolve to the base table, carrying its stats.
	for _, tc := range []struct {
		idx  int
		col  string
		nd   int64
	}{{0, "cust", 40000}, {1, "yr", 5}} {
		ref, ok := resolveBaseColumn(tc.idx, win)
		if !ok {
			t.Fatalf("col %d did not resolve across the WindowAgg", tc.idx)
		}
		if ref.col != tc.col {
			t.Errorf("col %d resolved to %q, want %q", tc.idx, ref.col, tc.col)
		}
		if ref.ndistinct != tc.nd {
			t.Errorf("col %d ndistinct = %d, want %d", tc.idx, ref.ndistinct, tc.nd)
		}
	}

	// The window function's own output is a computed value — no base column.
	if _, ok := resolveBaseColumn(2, win); ok {
		t.Error("the window function output must NOT resolve to a base column")
	}
	// Out of range and nil child fail closed.
	if _, ok := resolveBaseColumn(99, win); ok {
		t.Error("out-of-range index must decline")
	}
	if _, ok := resolveBaseColumn(0, &WindowAgg{}); ok {
		t.Error("nil child must decline")
	}
}

// TestColumnNDistinctCrossesWindowAgg is the consumer-level pin: the number
// now reaches columnNDistinctForChild, which is what the CTE-stats
// pass-through rule (slice 3) asks for and previously never got.
func TestColumnNDistinctCrossesWindowAgg(t *testing.T) {
	win := winArmNode()
	if got := columnNDistinctForChild(1, win); got != 5 {
		t.Fatalf("yr ndistinct through WindowAgg = %d, want 5", got)
	}
	if got := columnNDistinctForChild(2, win); got != 0 {
		t.Fatalf("window func output ndistinct = %d, want 0 (unknown)", got)
	}
}

// TestRelFilteredRowsCrossesWindowAgg pins the third family member. A
// WindowAgg still describes its child's relation because it is row-preserving,
// so the walk must find the relation through it.
func TestRelFilteredRowsCrossesWindowAgg(t *testing.T) {
	win := winArmNode()
	sorted := win.Child
	scan := sorted.(*Sort).Child
	if _, found, _ := relFilteredRowsWalk(win, scan); !found {
		t.Fatal("relFilteredRowsWalk must find the relation through a WindowAgg")
	}
}
