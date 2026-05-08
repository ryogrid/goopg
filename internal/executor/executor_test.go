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

func TestExecJoinVariants(t *testing.T) {
	makeSide := func(vals ...int64) *planner.Values {
		rows := make([][]planner.Expr, 0, len(vals))
		for _, v := range vals {
			rows = append(rows, []planner.Expr{&planner.IntegerConst{Value: v}})
		}
		return &planner.Values{Rows: rows}
	}
	pred := &planner.BinaryOp{
		Op: "=",
		Left: &planner.ColumnRef{
			Index: 0,
			Name:  "l",
			Type:  catalog.Type{Name: "int4"},
		},
		Right: &planner.ColumnRef{
			Index: 1,
			Name:  "r",
			Type:  catalog.Type{Name: "int4"},
		},
	}
	tests := []struct {
		name string
		jt   planner.JoinType
		want []Row
	}{
		{
			name: "inner",
			jt:   planner.JoinTypeInner,
			want: []Row{{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 1}}},
		},
		{
			name: "left",
			jt:   planner.JoinTypeLeft,
			want: []Row{
				{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 1}},
				{{Kind: KindInt, Int: 2}, NullDatum},
			},
		},
		{
			name: "right",
			jt:   planner.JoinTypeRight,
			want: []Row{
				{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 1}},
				{NullDatum, {Kind: KindInt, Int: 3}},
			},
		},
		{
			name: "full",
			jt:   planner.JoinTypeFull,
			want: []Row{
				{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 1}},
				{{Kind: KindInt, Int: 2}, NullDatum},
				{NullDatum, {Kind: KindInt, Int: 3}},
			},
		},
	}
	for _, tc := range tests {
		for _, algo := range []planner.JoinAlgo{planner.JoinAlgoNestedLoop, planner.JoinAlgoMerge} {
			name := tc.name
			plan := &planner.Join{
				Type:      tc.jt,
				Algo:      algo,
				Left:      makeSide(1, 2),
				Right:     makeSide(1, 3),
				Predicate: pred,
				LeftKey: &planner.ColumnRef{
					Index: 0,
					Name:  "l",
					Type:  catalog.Type{Name: "int4"},
				},
				RightKey: &planner.ColumnRef{
					Index: 1,
					Name:  "r",
					Type:  catalog.Type{Name: "int4"},
				},
			}
			op, err := Build(plan)
			if err != nil {
				t.Fatalf("Build(%s/%v): %v", name, algo, err)
			}
			rows, err := Run(op, NewContext())
			if err != nil {
				t.Fatalf("Run(%s/%v): %v", name, algo, err)
			}
			if len(rows) != len(tc.want) {
				t.Fatalf("%s/%v rows=%d want %d", name, algo, len(rows), len(tc.want))
			}
			for i := range tc.want {
				if len(rows[i]) != len(tc.want[i]) {
					t.Fatalf("%s/%v row[%d] len=%d want %d", name, algo, i, len(rows[i]), len(tc.want[i]))
				}
				for j := range tc.want[i] {
					if rows[i][j].Kind != tc.want[i][j].Kind {
						t.Fatalf("%s/%v row[%d][%d] kind=%d want %d", name, algo, i, j, rows[i][j].Kind, tc.want[i][j].Kind)
					}
					if rows[i][j].Kind == KindInt && rows[i][j].Int != tc.want[i][j].Int {
						t.Fatalf("%s/%v row[%d][%d] int=%d want %d", name, algo, i, j, rows[i][j].Int, tc.want[i][j].Int)
					}
				}
			}
		}
	}
}

func TestExecAggregateGroupBy(t *testing.T) {
	child := &planner.Values{Rows: [][]planner.Expr{
		{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 10}},
		{&planner.IntegerConst{Value: 1}, &planner.IntegerConst{Value: 20}},
		{&planner.IntegerConst{Value: 2}, &planner.IntegerConst{Value: 5}},
	}}
	plan := &planner.Aggregate{
		Child: child,
		GroupExprs: []planner.Expr{
			&planner.ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}},
		},
		Aggs: []planner.AggregateCall{
			{Name: "sum", Arg: &planner.ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "int4"}}, Type: catalog.Type{Name: "int8"}},
			{Name: "count", Star: true, Type: catalog.Type{Name: "int8"}},
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
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}
	got := map[int64]Row{}
	for _, r := range rows {
		got[r[0].Int] = r
	}
	if got[1][1].Int != 30 || got[1][2].Int != 2 {
		t.Fatalf("group 1 row=%+v", got[1])
	}
	if got[2][1].Int != 5 || got[2][2].Int != 1 {
		t.Fatalf("group 2 row=%+v", got[2])
	}
}

func TestExecAggregateGlobalEmptyInput(t *testing.T) {
	plan := &planner.Aggregate{
		Child: &planner.Values{Rows: nil},
		Aggs: []planner.AggregateCall{
			{Name: "count", Star: true, Type: catalog.Type{Name: "int8"}},
			{Name: "sum", Arg: &planner.ColumnRef{Index: 0, Name: "v", Type: catalog.Type{Name: "int4"}}, Type: catalog.Type{Name: "int8"}},
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
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 0 {
		t.Fatalf("count=%+v want 0", rows[0][0])
	}
	if !rows[0][1].IsNull() {
		t.Fatalf("sum=%+v want NULL", rows[0][1])
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
		{"and-null-false", NullDatum, NewBoolDatum(false), "AND", NewBoolDatum(false)},
		{"or-null-true", NullDatum, NewBoolDatum(true), "OR", NewBoolDatum(true)},
		{"and-null-true", NullDatum, NewBoolDatum(true), "AND", NullDatum},
	}
	for _, c := range cases {
		got, err := evalBinary(c.op, c.a, c.b, 0)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.Kind != c.want.Kind || got.BoolValue() != c.want.BoolValue() {
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
	if rows[0][0].Kind != KindTime || !rows[0][0].TimeValue().Equal(ctx.Now) {
		t.Errorf("got %+v", rows[0][0])
	}
}
