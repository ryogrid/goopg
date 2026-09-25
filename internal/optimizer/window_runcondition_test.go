package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// runCondFixture is a WindowAgg over a two-column child whose third output
// column is the window function `fn`, ordered by the child's first column.
func runCondFixture(fn string, frame *WindowFrame) *WindowAgg {
	child := upperOrderedInput(1000)
	int8 := catalog.Type{Name: "int8"}
	schema := append(Schema(nil), child.Output()...)
	schema = append(schema, SchemaColumn{Name: fn, Type: int8})
	return &WindowAgg{
		Child:   child,
		OrderBy: []SortKey{{Expr: &ColumnRef{Index: 0, Name: "k"}}},
		Funcs:   []WindowFunc{{Name: fn, Type: int8}},
		Frame:   frame,
		schema:  schema,
	}
}

func runCondQual(op parser.OpCode, wfuncLeft bool) Expr {
	col := &ColumnRef{Index: 3, Name: "w", Type: catalog.Type{Name: "int8"}}
	k := &IntegerConst{Value: 11}
	if wfuncLeft {
		return &BinaryOp{Op: op, Left: col, Right: k}
	}
	return &BinaryOp{Op: op, Left: k, Right: col}
}

// TestWindowRunConditionDropsQual pins M0146-0005i against
// find_window_run_conditions: an increasing window function takes `<`/`<=`
// with the function on the left (or `>`/`>=` with it on the right) as a run
// condition and drops the original qual; the opposite direction and `=` keep
// it.
func TestWindowRunConditionDropsQual(t *testing.T) {
	w := runCondFixture("rank", nil)
	for _, tc := range []struct {
		op        parser.OpCode
		wfuncLeft bool
		want      bool
	}{
		{parser.OpLt, true, true},
		{parser.OpLe, true, true},
		{parser.OpGt, false, true},
		{parser.OpGe, false, true},
		{parser.OpGt, true, false},
		{parser.OpLt, false, false},
		{parser.OpEq, true, false},
	} {
		if got := windowRunConditionDropsQual(runCondQual(tc.op, tc.wfuncLeft), w); got != tc.want {
			t.Errorf("rank op=%v wfuncLeft=%v: drops=%v, want %v", tc.op, tc.wfuncLeft, got, tc.want)
		}
	}
	// A non-window column never qualifies.
	plain := &BinaryOp{Op: parser.OpLt, Left: &ColumnRef{Index: 0, Name: "k"}, Right: &IntegerConst{Value: 11}}
	if windowRunConditionDropsQual(plain, w) {
		t.Error("a plain column must not become a run condition")
	}
	// Through an identity Project.
	proj := &Project{Child: w, Targets: []Expr{&ColumnRef{Index: 3, Name: "rank"}}}
	q := &BinaryOp{Op: parser.OpLt, Left: &ColumnRef{Index: 0, Name: "rnk"}, Right: &IntegerConst{Value: 11}}
	if !windowRunConditionDropsQual(q, proj) {
		t.Error("an identity Project over the window function must resolve")
	}
}

// TestWindowFuncSupportMonotonicCount pins int8inc_support: count is both
// without ORDER BY, increasing for the default frame, and decreasing for a
// frame ending at UNBOUNDED FOLLOWING.
func TestWindowFuncSupportMonotonicCount(t *testing.T) {
	w := runCondFixture("count", nil)
	if m, ok := windowFuncSupportMonotonic(w.Funcs[0], w); !ok || m != monoIncreasing {
		t.Fatalf("default frame: %v %v, want increasing", m, ok)
	}
	w.Frame = &WindowFrame{StartKind: parser.FrameBoundCurrentRow, EndKind: parser.FrameBoundUnboundedFollowing}
	if m, ok := windowFuncSupportMonotonic(w.Funcs[0], w); !ok || m != monoDecreasing {
		t.Fatalf("current row .. unbounded following: %v %v, want decreasing", m, ok)
	}
	w.OrderBy = nil
	if m, ok := windowFuncSupportMonotonic(w.Funcs[0], w); !ok || m != monoBoth {
		t.Fatalf("no ORDER BY: %v %v, want both", m, ok)
	}
	if !windowRunConditionDropsQual(runCondQual(parser.OpEq, true), w) {
		t.Fatal("`=` on a both-monotonic function drops the original qual")
	}
	if _, ok := windowFuncSupportMonotonic(WindowFunc{Name: "sum"}, w); ok {
		t.Fatal("sum has no monotonic support function")
	}
}

// TestFilterSelectivitySkipsWindowRunCondition: the Filter keeps both
// conjuncts for execution, but only the one PG keeps as a qual is priced.
func TestFilterSelectivitySkipsWindowRunCondition(t *testing.T) {
	w := runCondFixture("rank", nil)
	runCond := runCondQual(parser.OpLt, true)
	if got := filterSelectivity(&Filter{Child: w, Predicate: runCond}); got != 1 {
		t.Fatalf("run-condition-only filter selectivity = %g, want 1", got)
	}
	kept := runCondQual(parser.OpGt, true)
	both := &BinaryOp{Op: parser.OpAnd, Left: runCond, Right: kept}
	want := clauseSelectivity(kept, w)
	if got := filterSelectivity(&Filter{Child: w, Predicate: both}); got != want {
		t.Fatalf("selectivity = %g, want the kept conjunct's %g", got, want)
	}
}
