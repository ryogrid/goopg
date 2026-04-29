package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

func TestWindowOpRowNumberByPartitionAndOrder(t *testing.T) {
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
	wantRN := []int64{1, 2, 1}
	for i, w := range want {
		if rows[i][0].Kind != KindInt || rows[i][0].Int != w[0] {
			t.Fatalf("row[%d][0]=%+v want int=%d", i, rows[i][0], w[0])
		}
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w[1] {
			t.Fatalf("row[%d][1]=%+v want int=%d", i, rows[i][1], w[1])
		}
		if len(rows[i]) != 3 || rows[i][2].Kind != KindInt || rows[i][2].Int != wantRN[i] {
			t.Fatalf("row[%d]=%+v want row_number=%d", i, rows[i], wantRN[i])
		}
	}
}

func TestWindowOpRankWithoutOrderAllPeers(t *testing.T) {
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
		if rows[i][1].Kind != KindInt || rows[i][1].Int != 1 {
			t.Fatalf("row[%d] rank=%+v want 1", i, rows[i][1])
		}
	}
}

func TestWindowOpRankWithPeersAndPartitions(t *testing.T) {
	plan := &planner.WindowAgg{
		Child: &planner.Values{Rows: [][]planner.Expr{
			{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 10}},
			{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 20}},
			{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 10}},
			{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 5}},
			{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 5}},
		}},
		PartitionBy: []planner.Expr{
			&planner.ColumnRef{Index: 0, Name: "grp", Type: catalog.Type{Name: "int4"}},
		},
		OrderBy: []planner.SortKey{
			{Expr: &planner.ColumnRef{Index: 1, Name: "val", Type: catalog.Type{Name: "int4"}}},
		},
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
	want := []struct {
		grp  int64
		val  int64
		rank int64
	}{
		{grp: 1, val: 10, rank: 1},
		{grp: 1, val: 10, rank: 1},
		{grp: 1, val: 20, rank: 3},
		{grp: 2, val: 5, rank: 1},
		{grp: 2, val: 5, rank: 1},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][0].Kind != KindInt || rows[i][0].Int != w.grp {
			t.Fatalf("row[%d] grp=%+v want %d", i, rows[i][0], w.grp)
		}
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w.val {
			t.Fatalf("row[%d] val=%+v want %d", i, rows[i][1], w.val)
		}
		if rows[i][2].Kind != KindInt || rows[i][2].Int != w.rank {
			t.Fatalf("row[%d] rank=%+v want %d", i, rows[i][2], w.rank)
		}
	}
}
