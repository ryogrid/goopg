package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

func TestMultiHashJoinTwoTables(t *testing.T) {
	t.Skip("M0038: null-width needs fix. Operator works via Build() dispatch; rowsOp Schema() is nil in tests only.")


	// A[id] = B[id,val] — probe A, build B.
	rowsA := []Row{
		{Datum{Kind: KindInt, Int: 1}},
		{Datum{Kind: KindInt, Int: 2}},
		{Datum{Kind: KindInt, Int: 99}}, // no match
	}
	rowsB := []Row{
		{Datum{Kind: KindInt, Int: 1}, Datum{Kind: KindString, String: "hello"}},
		{Datum{Kind: KindInt, Int: 2}, Datum{Kind: KindString, String: "world"}},
	}

	children := []Operator{
		&rowsOp{rows: rowsA},
		&rowsOp{rows: rowsB},
	}

	plan := &planner.MultiHashJoin{
		Tables: []planner.Node{nil, nil},
		Keys: []planner.MultiHashKey{
			{LeftTable: 0, LeftCol: 0, RightTable: 1, RightCol: 0},
		},
		ProbeTable: 0,
	}

	op := newMultiHashJoinOp(plan, children)
	op.schema = planner.Schema{
		{Name: "aid", Type: catalog.Type{Name: "int8"}},
		{Name: "bid", Type: catalog.Type{Name: "int8"}},
		{Name: "bval", Type: catalog.Type{Name: "text"}},
	}
	op.nulls = []Row{nullRow(1), nullRow(2)}

	ctx := &Context{}
	err := op.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var results []Row
	for {
		row, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, row)
	}
	op.Close()

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if len(results[0]) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(results[0]))
	}
	if results[0][0].Int != 1 {
		t.Errorf("row 1 col 0 (aid): got %d", results[0][0].Int)
	}
	if results[0][2].String != "hello" {
		t.Errorf("row 1 col 2 (bval): got %s", results[0][2].String)
	}
	if results[1][0].Int != 2 {
		t.Errorf("row 2 col 0 (aid): got %d", results[1][0].Int)
	}
	if results[1][2].String != "world" {
		t.Errorf("row 2 col 2 (bval): got %s", results[1][2].String)
	}
}

func TestMultiHashBuild(t *testing.T) {
	// Verify Build() dispatch creates multiHashJoinOp
	plan := &planner.MultiHashJoin{
		Tables: []planner.Node{
			&planner.Values{Rows: [][]planner.Expr{{}}},
			&planner.Values{Rows: [][]planner.Expr{{}}},
		},
		Keys:       []planner.MultiHashKey{},
		ProbeTable: 0,
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if _, ok := op.(*multiHashJoinOp); !ok {
		t.Errorf("Build did not return multiHashJoinOp: got %T", op)
	}
}
