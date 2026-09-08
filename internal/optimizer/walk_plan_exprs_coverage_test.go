package optimizer

import (
	"testing"
)

// B-01c third cut, step 1 — walkPlanExprs coverage: the Aggregate arm visits
// AggregateCall.Filter + Aggregate.Passthrough, and the WindowAgg arm visits
// Funcs[].Args + Funcs[].Filter + frame offsets. These tests pin the widened
// walk so a future arm narrowing fails loudly instead of silently hiding
// correlations from every walkPlanExprs caller again.

// wpcVisitedNames collects every *ColumnRef name walkPlanExprs reaches.
func wpcVisitedNames(t *testing.T, n Node) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	walkPlanExprs(n, func(e Expr) {
		if cr, ok := e.(*ColumnRef); ok {
			got[cr.Name] = true
		}
	})
	return got
}

// TestWalkPlanExprsAggregateFilter: an AggregateCall FILTER predicate is
// visited — a correlation there must trip the all-accounted bail and the
// remap pass alike.
func TestWalkPlanExprsAggregateFilter(t *testing.T) {
	a := &Aggregate{
		Child:      &noNode{sch: noSchema("a", "b")},
		GroupExprs: []Expr{gitCol("a", 0)},
		Aggs: []AggregateCall{{
			Name:   "sum",
			Arg:    gitCol("b", 1),
			Filter: &BinaryOp{Left: gitCol("b", 1), Right: &IntegerConst{Value: 0}},
		}},
	}
	got := wpcVisitedNames(t, a)
	if !got["a"] || !got["b"] {
		t.Fatalf("walk visited %v; want group key a and arg/filter col b", got)
	}
	// The filter-only column must be reached even when no other arm names it.
	a2 := &Aggregate{
		Child:      &noNode{sch: noSchema("a", "b", "f")},
		GroupExprs: []Expr{gitCol("a", 0)},
		Aggs: []AggregateCall{{
			Name:   "sum",
			Arg:    gitCol("b", 1),
			Filter: gitCol("f", 2),
		}},
	}
	if got := wpcVisitedNames(t, a2); !got["f"] {
		t.Fatalf("walk visited %v; want filter-only col f", got)
	}
}

// TestWalkPlanExprsAggregatePassthrough: Passthrough expressions are visited.
func TestWalkPlanExprsAggregatePassthrough(t *testing.T) {
	a := &Aggregate{
		Child:       &noNode{sch: noSchema("a", "b", "p")},
		GroupExprs:  []Expr{gitCol("a", 0)},
		Aggs:        []AggregateCall{{Name: "sum", Arg: gitCol("b", 1)}},
		Passthrough: []Expr{gitCol("p", 2)},
	}
	if got := wpcVisitedNames(t, a); !got["p"] {
		t.Fatalf("walk visited %v; want passthrough col p", got)
	}
}

// TestWalkPlanExprsWindowFuncArgs: WindowAgg func args are visited.
func TestWalkPlanExprsWindowFuncArgs(t *testing.T) {
	w := &WindowAgg{
		Child:       &noNode{sch: noSchema("a", "b", "v")},
		PartitionBy: []Expr{gitCol("a", 0)},
		OrderBy:     []SortKey{{Expr: gitCol("b", 1)}},
		Funcs:       []WindowFunc{{Name: "sum", Args: []Expr{gitCol("v", 2)}}},
	}
	got := wpcVisitedNames(t, w)
	for _, want := range []string{"a", "b", "v"} {
		if !got[want] {
			t.Fatalf("walk visited %v; want partition/order/arg cols a b v", got)
		}
	}
}

// TestWalkPlanExprsWindowFuncFilter: a window FILTER predicate is visited,
// including when it names a column no other arm names.
func TestWalkPlanExprsWindowFuncFilter(t *testing.T) {
	w := &WindowAgg{
		Child:       &noNode{sch: noSchema("a", "v", "f")},
		PartitionBy: []Expr{gitCol("a", 0)},
		Funcs: []WindowFunc{{
			Name:   "sum",
			Args:   []Expr{gitCol("v", 1)},
			Filter: gitCol("f", 2),
		}},
	}
	if got := wpcVisitedNames(t, w); !got["f"] {
		t.Fatalf("walk visited %v; want filter-only col f", got)
	}
}

// TestWalkPlanExprsWindowFrameOffsets: frame offset expressions are visited,
// including when they name a column no other arm names.
func TestWalkPlanExprsWindowFrameOffsets(t *testing.T) {
	w := &WindowAgg{
		Child:       &noNode{sch: noSchema("a", "v", "o")},
		PartitionBy: []Expr{gitCol("a", 0)},
		Funcs:       []WindowFunc{{Name: "sum", Args: []Expr{gitCol("v", 1)}}},
		Frame: &WindowFrame{
			StartOffset: gitCol("o", 2),
			EndOffset:   &IntegerConst{Value: 1},
		},
	}
	if got := wpcVisitedNames(t, w); !got["o"] {
		t.Fatalf("walk visited %v; want frame-offset col o", got)
	}
}

// TestWalkPlanExprsWindowNilSafe: nil Filter / nil Frame / empty funcs walk
// without panic and still cover partition/order keys.
func TestWalkPlanExprsWindowNilSafe(t *testing.T) {
	w := &WindowAgg{
		Child:       &noNode{sch: noSchema("a", "b")},
		PartitionBy: []Expr{gitCol("a", 0)},
		OrderBy:     []SortKey{{Expr: gitCol("b", 1)}},
	}
	got := wpcVisitedNames(t, w)
	if !got["a"] || !got["b"] {
		t.Fatalf("walk visited %v; want partition/order cols a b", got)
	}
}
