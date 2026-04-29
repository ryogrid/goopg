package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

func TestWindowOpSkeletonSortsAndAppendsPlaceholders(t *testing.T) {
	plan := &planner.WindowAgg{
		Child: &planner.Values{Rows: [][]planner.Expr{
			{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 20}},
			{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 5}},
			{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 10}},
		}},
		PartitionBy: []planner.Expr{
			&planner.ColumnRef{Index: 0, Name: "grp", Type: catalog.Type{Name: "int4"}},
		},
		OrderBy: []planner.SortKey{
			{Expr: &planner.ColumnRef{Index: 1, Name: "val", Type: catalog.Type{Name: "int4"}}},
		},
		Funcs: []planner.WindowFunc{
			{Name: "row_number", Type: catalog.Type{Name: "int8"}},
		},
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, NewContext())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3", len(rows))
	}
	want := [][2]int64{{1, 10}, {1, 20}, {2, 5}}
	for i, w := range want {
		if rows[i][0].Kind != KindInt || rows[i][0].Int != w[0] {
			t.Fatalf("row[%d][0]=%+v want int=%d", i, rows[i][0], w[0])
		}
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w[1] {
			t.Fatalf("row[%d][1]=%+v want int=%d", i, rows[i][1], w[1])
		}
		if len(rows[i]) != 3 || !rows[i][2].IsNull() {
			t.Fatalf("row[%d]=%+v want third column NULL placeholder", i, rows[i])
		}
	}
}

func TestWindowOpSkeletonKeepsOrderWithoutKeys(t *testing.T) {
	plan := &planner.WindowAgg{
		Child: &planner.Values{Rows: [][]planner.Expr{
			{&planner.IntegerConst{Value: 9}},
			{&planner.IntegerConst{Value: 3}},
			{&planner.IntegerConst{Value: 7}},
		}},
		Funcs: []planner.WindowFunc{{Name: "rank", Type: catalog.Type{Name: "int8"}}},
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, NewContext())
	if err != nil {
		t.Fatal(err)
	}
	got := []int64{rows[0][0].Int, rows[1][0].Int, rows[2][0].Int}
	want := []int64{9, 3, 7}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order changed: got=%v want=%v", got, want)
		}
		if !rows[i][1].IsNull() {
			t.Fatalf("row[%d] placeholder=%+v want NULL", i, rows[i][1])
		}
	}
}
