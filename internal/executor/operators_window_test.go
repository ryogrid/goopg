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

func TestWindowOpLagBasic(t *testing.T) {
	// lag(val) with no explicit offset (default 1): each row gets the
	// previous row's value; the first row returns NULL.
	valRef := &planner.ColumnRef{Index: 0, Name: "val", Type: catalog.Type{Name: "int8"}}
	plan := &planner.WindowAgg{
		Child: &planner.Values{Rows: [][]planner.Expr{
			{&planner.IntegerConst{Value: 10}},
			{&planner.IntegerConst{Value: 20}},
			{&planner.IntegerConst{Value: 30}},
		}},
		Funcs: []planner.WindowFunc{
			{Name: "lag", Type: catalog.Type{Name: "int8"}, Args: []planner.Expr{valRef}},
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
	wantVals := []int64{10, 20, 30}
	wantLag := []Datum{NullDatum, {Kind: KindInt, Int: 10}, {Kind: KindInt, Int: 20}}
	for i, wv := range wantVals {
		if rows[i][0].Int != wv {
			t.Fatalf("row[%d] val=%v want %d", i, rows[i][0], wv)
		}
		got := rows[i][1]
		if wantLag[i].Kind == KindNull {
			if got.Kind != KindNull {
				t.Fatalf("row[%d] lag=%v want NULL", i, got)
			}
		} else if got.Kind != KindInt || got.Int != wantLag[i].Int {
			t.Fatalf("row[%d] lag=%v want %d", i, got, wantLag[i].Int)
		}
	}
}

func TestWindowOpLeadBasic(t *testing.T) {
	// lead(val) with default offset 1: each row gets the next row's
	// value; the last row returns NULL.
	valRef := &planner.ColumnRef{Index: 0, Name: "val", Type: catalog.Type{Name: "int8"}}
	plan := &planner.WindowAgg{
		Child: &planner.Values{Rows: [][]planner.Expr{
			{&planner.IntegerConst{Value: 10}},
			{&planner.IntegerConst{Value: 20}},
			{&planner.IntegerConst{Value: 30}},
		}},
		Funcs: []planner.WindowFunc{
			{Name: "lead", Type: catalog.Type{Name: "int8"}, Args: []planner.Expr{valRef}},
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
	wantLead := []Datum{{Kind: KindInt, Int: 20}, {Kind: KindInt, Int: 30}, NullDatum}
	for i, want := range wantLead {
		got := rows[i][1]
		if want.Kind == KindNull {
			if got.Kind != KindNull {
				t.Fatalf("row[%d] lead=%v want NULL", i, got)
			}
		} else if got.Kind != KindInt || got.Int != want.Int {
			t.Fatalf("row[%d] lead=%v want %d", i, got, want.Int)
		}
	}
}

func TestWindowOpLagWithExplicitOffset(t *testing.T) {
	// lag(val, 2): each row gets the value 2 rows back.
	valRef := &planner.ColumnRef{Index: 0, Name: "val", Type: catalog.Type{Name: "int8"}}
	plan := &planner.WindowAgg{
		Child: &planner.Values{Rows: [][]planner.Expr{
			{&planner.IntegerConst{Value: 10}},
			{&planner.IntegerConst{Value: 20}},
			{&planner.IntegerConst{Value: 30}},
			{&planner.IntegerConst{Value: 40}},
		}},
		Funcs: []planner.WindowFunc{
			{Name: "lag", Type: catalog.Type{Name: "int8"}, Args: []planner.Expr{
				valRef,
				&planner.IntegerConst{Value: 2},
			}},
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
	// row0→NULL, row1→NULL, row2→10, row3→20
	wantLag := []Datum{NullDatum, NullDatum, {Kind: KindInt, Int: 10}, {Kind: KindInt, Int: 20}}
	for i, want := range wantLag {
		got := rows[i][1]
		if want.Kind == KindNull {
			if got.Kind != KindNull {
				t.Fatalf("row[%d] lag=%v want NULL", i, got)
			}
		} else if got.Kind != KindInt || got.Int != want.Int {
			t.Fatalf("row[%d] lag=%v want %d", i, got, want.Int)
		}
	}
}

func TestWindowOpLagWithDefault(t *testing.T) {
	// lag(val, 1, -1): out-of-range rows return -1 instead of NULL.
	valRef := &planner.ColumnRef{Index: 0, Name: "val", Type: catalog.Type{Name: "int8"}}
	plan := &planner.WindowAgg{
		Child: &planner.Values{Rows: [][]planner.Expr{
			{&planner.IntegerConst{Value: 10}},
			{&planner.IntegerConst{Value: 20}},
		}},
		Funcs: []planner.WindowFunc{
			{Name: "lag", Type: catalog.Type{Name: "int8"}, Args: []planner.Expr{
				valRef,
				&planner.IntegerConst{Value: 1},
				&planner.IntegerConst{Value: -1},
			}},
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
	// row0 → default=-1, row1 → 10
	if rows[0][1].Kind != KindInt || rows[0][1].Int != -1 {
		t.Fatalf("row[0] lag default=%v want -1", rows[0][1])
	}
	if rows[1][1].Kind != KindInt || rows[1][1].Int != 10 {
		t.Fatalf("row[1] lag=%v want 10", rows[1][1])
	}
}

func TestWindowOpLeadWithDefault(t *testing.T) {
	// lead(val, 1, 99): last row returns 99.
	valRef := &planner.ColumnRef{Index: 0, Name: "val", Type: catalog.Type{Name: "int8"}}
	plan := &planner.WindowAgg{
		Child: &planner.Values{Rows: [][]planner.Expr{
			{&planner.IntegerConst{Value: 10}},
			{&planner.IntegerConst{Value: 20}},
		}},
		Funcs: []planner.WindowFunc{
			{Name: "lead", Type: catalog.Type{Name: "int8"}, Args: []planner.Expr{
				valRef,
				&planner.IntegerConst{Value: 1},
				&planner.IntegerConst{Value: 99},
			}},
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
	if rows[0][1].Kind != KindInt || rows[0][1].Int != 20 {
		t.Fatalf("row[0] lead=%v want 20", rows[0][1])
	}
	if rows[1][1].Kind != KindInt || rows[1][1].Int != 99 {
		t.Fatalf("row[1] lead default=%v want 99", rows[1][1])
	}
}

func TestWindowOpLagLeadWithPartitions(t *testing.T) {
	// lag(val) and lead(val) with PARTITION BY grp.
	// Partition boundaries mean lag/lead cannot cross partition edges.
	grpRef := &planner.ColumnRef{Index: 0, Name: "grp", Type: catalog.Type{Name: "int4"}}
	valRef := &planner.ColumnRef{Index: 1, Name: "val", Type: catalog.Type{Name: "int8"}}
	plan := &planner.WindowAgg{
		Child: &planner.Values{Rows: [][]planner.Expr{
			{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 10}},
			{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 20}},
			{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 5}},
			{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 15}},
		}},
		PartitionBy: []planner.Expr{grpRef},
		Funcs: []planner.WindowFunc{
			{Name: "lag", Type: catalog.Type{Name: "int8"}, Args: []planner.Expr{valRef}},
			{Name: "lead", Type: catalog.Type{Name: "int8"}, Args: []planner.Expr{valRef}},
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
	// After partition sort: (1,10), (1,20), (2,5), (2,15)
	// lag:  NULL, 10,   NULL, 5
	// lead: 20,   NULL, 15,   NULL
	type wantRow struct {
		grp, val int64
		lagNull  bool
		lagVal   int64
		leadNull bool
		leadVal  int64
	}
	want := []wantRow{
		{1, 10, true, 0, false, 20},
		{1, 20, false, 10, true, 0},
		{2, 5, true, 0, false, 15},
		{2, 15, false, 5, true, 0},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][0].Int != w.grp {
			t.Fatalf("row[%d] grp=%v want %d", i, rows[i][0], w.grp)
		}
		if rows[i][1].Int != w.val {
			t.Fatalf("row[%d] val=%v want %d", i, rows[i][1], w.val)
		}
		lagDatum := rows[i][2]
		if w.lagNull {
			if lagDatum.Kind != KindNull {
				t.Fatalf("row[%d] lag=%v want NULL", i, lagDatum)
			}
		} else if lagDatum.Kind != KindInt || lagDatum.Int != w.lagVal {
			t.Fatalf("row[%d] lag=%v want %d", i, lagDatum, w.lagVal)
		}
		leadDatum := rows[i][3]
		if w.leadNull {
			if leadDatum.Kind != KindNull {
				t.Fatalf("row[%d] lead=%v want NULL", i, leadDatum)
			}
		} else if leadDatum.Kind != KindInt || leadDatum.Int != w.leadVal {
			t.Fatalf("row[%d] lead=%v want %d", i, leadDatum, w.leadVal)
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
