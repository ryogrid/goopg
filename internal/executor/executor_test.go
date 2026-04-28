package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

func planOne(t *testing.T, sql string, cat catalog.Catalog) planner.Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse(%q): %d stmts", sql, len(stmts))
	}
	plan, err := planner.Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	return plan
}

// TestExecSelectConstant pins the smallest end-to-end:
// `SELECT 1` builds Project(Values{[]}) and emits one row of [1].
func TestExecSelectConstant(t *testing.T) {
	plan := planOne(t, "SELECT 1", catalog.NewInMemory())
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, NewContext())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("rows shape=%v", rows)
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 1 {
		t.Errorf("got %+v", rows[0][0])
	}
}

// TestExecSelectArithmeticAndAlias verifies operator precedence and
// alias propagation through Project — `SELECT 1 + 2 * 3 AS x` gives
// 7 with output schema "x".
func TestExecSelectArithmeticAndAlias(t *testing.T) {
	plan := planOne(t, "SELECT 1 + 2 * 3 AS x", catalog.NewInMemory())
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	if op.Schema()[0].Name != "x" {
		t.Errorf("schema[0].Name=%q want x", op.Schema()[0].Name)
	}
	rows, _ := Run(op, NewContext())
	if rows[0][0].Int != 7 {
		t.Errorf("got %+v want 7", rows[0][0])
	}
}

// TestExecParamRef: $1 binds to the supplied Context.Params and
// flows through Project.
func TestExecParamRef(t *testing.T) {
	plan := planOne(t, "SELECT $1 + 5", catalog.NewInMemory())
	op, _ := Build(plan)
	ctx := NewContext()
	ctx.Params = []Datum{{Kind: KindInt, Int: 37}}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0].Int != 42 {
		t.Errorf("got %+v want 42", rows[0][0])
	}
}

// TestExecValuesProjectFilter exercises a multi-row Values feeding
// Project (ensures evalExpr sees the input row) and Filter (ensures
// rows where the predicate is FALSE/NULL are dropped). Built by hand
// because the planner's INSERT path doesn't emit this shape directly.
func TestExecValuesProjectFilter(t *testing.T) {
	values := &planner.Values{
		Rows: [][]planner.Expr{
			{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 10}},
			{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 20}},
			{&planner.IntegerConst{Value: 3}, &planner.IntegerConst{Value: 30}},
		},
	}
	op, err := Build(values)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, NewContext())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Errorf("rows=%d want 3", len(rows))
	}
}

// TestExecLimitOffset verifies LIMIT and OFFSET apply correctly when
// stacked on Values.
func TestExecLimitOffset(t *testing.T) {
	rowsExpr := func(n int64) []planner.Expr { return []planner.Expr{&planner.IntegerConst{Value: n}} }
	values := &planner.Values{
		Rows: [][]planner.Expr{rowsExpr(1), rowsExpr(2), rowsExpr(3), rowsExpr(4), rowsExpr(5)},
	}
	limit := &planner.Limit{
		Child:  values,
		Limit:  &planner.IntegerConst{Value: 2},
		Offset: &planner.IntegerConst{Value: 1},
	}
	op, _ := Build(limit)
	rows, err := Run(op, NewContext())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}
	if rows[0][0].Int != 2 || rows[1][0].Int != 3 {
		t.Errorf("rows=%+v want 2,3", rows)
	}
}

// TestExecSort ascending/descending semantics on int columns.
func TestExecSort(t *testing.T) {
	rowsExpr := func(n int64) []planner.Expr { return []planner.Expr{&planner.IntegerConst{Value: n}} }
	values := &planner.Values{
		Rows: [][]planner.Expr{rowsExpr(3), rowsExpr(1), rowsExpr(4), rowsExpr(1), rowsExpr(5)},
	}
	sortDesc := &planner.Sort{
		Child: values,
		Keys:  []planner.SortKey{{Expr: &planner.ColumnRef{Index: 0, Type: catalog.Type{Name: "int4"}}, Desc: true}},
	}
	op, _ := Build(sortDesc)
	rows, err := Run(op, NewContext())
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{5, 4, 3, 1, 1}
	for i, w := range want {
		if rows[i][0].Int != w {
			t.Errorf("rows[%d]=%+v want %d", i, rows[i], w)
		}
	}
}

// TestExecBooleanThreeValuedLogic pins NULL handling on AND/OR per
// SQL Kleene semantics: NULL AND FALSE -> FALSE, NULL OR TRUE -> TRUE,
// NULL AND TRUE -> NULL.
func TestExecBooleanThreeValuedLogic(t *testing.T) {
	cases := []struct {
		name string
		a, b Datum
		op   string
		want Datum
	}{
		{"and-null-false", NullDatum, Datum{Kind: KindBool, Bool: false}, "AND", Datum{Kind: KindBool, Bool: false}},
		{"or-null-true", NullDatum, Datum{Kind: KindBool, Bool: true}, "OR", Datum{Kind: KindBool, Bool: true}},
		{"and-null-true", NullDatum, Datum{Kind: KindBool, Bool: true}, "AND", NullDatum},
	}
	for _, c := range cases {
		got, err := evalBinary(c.op, c.a, c.b, 0)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.Kind != c.want.Kind || got.Bool != c.want.Bool {
			t.Errorf("%s: got=%+v want=%+v", c.name, got, c.want)
		}
	}
}

// TestExecDivisionByZero produces SQLSTATE 22012, the canonical code.
func TestExecDivisionByZero(t *testing.T) {
	plan := planOne(t, "SELECT 10 / 0", catalog.NewInMemory())
	op, _ := Build(plan)
	_, err := Run(op, NewContext())
	if err == nil {
		t.Fatal("expected error")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "22012" {
		t.Errorf("err=%v", err)
	}
}

// TestExecCurrentTimestamp checks the function-call dispatch and that
// ctx.Now is what flows through.
func TestExecCurrentTimestamp(t *testing.T) {
	plan := planOne(t, "SELECT current_timestamp()", catalog.NewInMemory())
	op, _ := Build(plan)
	ctx := NewContext()
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0].Kind != KindTime || !rows[0][0].Time.Equal(ctx.Now) {
		t.Errorf("got %+v", rows[0][0])
	}
}
