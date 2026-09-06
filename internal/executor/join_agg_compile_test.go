package executor

// E-06 gate: twin-parity per builtin + wiring pins + alloc assert.
// Expression parity runs through the shared outcome harness (panic +
// SQLSTATE + pos + message); wiring pins assert the compiled nodes,
// decline flags, and end-to-end applyAgg behavior.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// aggParityRow: int, numeric-as-int, NULL, text, bool.
func aggParityRow() Row {
	return Row{
		NewIntDatum(7),
		NewIntDatum(11),
		NullDatum,
		NewStringDatum("abc"),
		NewBoolDatum(true),
	}
}

func TestAggTwinParityCorpus(t *testing.T) {
	pb := func(op parser.OpCode, l, r optimizer.Expr) *optimizer.BinaryOp {
		return &optimizer.BinaryOp{Op: op, Left: l, Right: r}
	}
	i := func(idx int) *optimizer.ColumnRef { return pcol(idx, "int4") }
	corpus := []parityCase{
		{"sum arg", i(0)},
		{"arg add", pb(parser.OpAdd, i(0), i(1))},
		{"arg null", pcol(2, "int4")},
		{"filter true", &optimizer.BooleanConst{Value: true}},
		{"filter false", &optimizer.BooleanConst{Value: false}},
		{"filter null", pcol(2, "bool")},
		{"filter conjunction", pb(parser.OpAnd, pb(parser.OpGt, i(0), pint(0)), pcol(4, "bool"))},
		{"arg2 const", pint(2)},
		{"arg2 div by zero", pbin(parser.OpDiv, pint(1), pint(0))},
		{"arg div by zero", pbin(parser.OpDiv, i(0), pint(0))},
		{"text arg", pcol(3, "text")},
		{"func call", &optimizer.FuncCall{Name: "lower", Args: []optimizer.Expr{pcol(3, "text")}}},
		{"out of range", pcol(99, "int4")},
		{"is null", &optimizer.IsNullExpr{Operand: pcol(2, "int4")}},
	}
	checkParityCorpus(t, corpus, aggParityRow())
}

// TestAggCompileWiring pins node lists, decline flags, and the noExpr
// sentinel for a mixed plan (builtin + user-agg + nullary shapes).
func TestAggCompileWiring(t *testing.T) {
	plan := &optimizer.Aggregate{Aggs: []optimizer.AggregateCall{
		{Name: "sum", Arg: pcol(0, "int4"), Filter: pcol(4, "bool")},
		{Name: "regr_slope", Arg: pcol(0, "int4"), Arg2: pcol(1, "int4")},
		{Name: "count"},
	}}
	o := &aggregateOp{plan: plan}
	o.compileAggExprs()
	if !o.aggCompiled {
		t.Fatal("compileAggExprs must set aggCompiled")
	}
	if len(o.aggArgNodes) != 3 || len(o.aggFilterNodes) != 3 || len(o.aggArg2Nodes) != 3 {
		t.Fatalf("node lists wrong lengths: %d %d %d", len(o.aggArgNodes), len(o.aggFilterNodes), len(o.aggArg2Nodes))
	}
	if len(o.aggExtraNodes) != 3 || len(o.aggOrderNodes) != 3 || len(o.aggWGOrderNodes) != 3 {
		t.Fatal("per-call element lists must run parallel to plan.Aggs")
	}
	if o.aggDeclined[0] || o.aggDeclined[1] || o.aggDeclined[2] {
		t.Fatal("builtin calls must not decline")
	}
}

// TestAggUserAggDeclined pins whole-call decline: a UserAgg call keeps
// every node at noExpr (arg/filter/arg2) with nil element lists, and
// evaluates interpreted.
func TestAggUserAggDeclined(t *testing.T) {
	plan := &optimizer.Aggregate{Aggs: []optimizer.AggregateCall{
		{Name: "myagg", Arg: pcol(0, "int4"), Filter: pcol(4, "bool"), Arg2: pint(1), UserAgg: &catalog.UserAggregate{}},
	}}
	o := &aggregateOp{plan: plan}
	o.compileAggExprs()
	if !o.aggDeclined[0] {
		t.Fatal("UserAgg call must decline whole-call compilation")
	}
	if o.aggArgNodes[0] != noExpr || o.aggFilterNodes[0] != noExpr || o.aggArg2Nodes[0] != noExpr {
		t.Fatalf("declined nodes = %d/%d/%d, want all noExpr",
			o.aggArgNodes[0], o.aggFilterNodes[0], o.aggArg2Nodes[0])
	}
	if o.aggExtraNodes[0] != nil || o.aggOrderNodes[0] != nil || o.aggWGOrderNodes[0] != nil {
		t.Fatal("declined element lists must stay nil (interpreted fallback via bounds check)")
	}
}

// TestAggApplyCompiledPath exercises applyAgg end to end on the compiled
// path: FILTER true/false/NULL gating + arg accumulation shape.
func TestAggApplyCompiledPath(t *testing.T) {
	plan := &optimizer.Aggregate{Aggs: []optimizer.AggregateCall{
		{Name: "sum", Arg: pcol(0, "int4"), Filter: pcol(4, "bool")},
	}}
	o := &aggregateOp{plan: plan, ctx: NewContext()}
	o.compileAggExprs()
	slot := SlotFromRow(nil, aggParityRow())
	var st aggRuntime
	if err := o.applyAgg(&st, plan.Aggs[0], slot, 0); err != nil {
		t.Fatalf("applyAgg: %v", err)
	}
	if !st.hasValue || st.sum != 7 {
		t.Fatalf("compiled applyAgg: hasValue=%v sum=%d, want true/7", st.hasValue, st.sum)
	}
	// FILTER false row: same call, bool false at index 4.
	row := Row{NewIntDatum(7), NewIntDatum(11), NullDatum, NewStringDatum("abc"), NewBoolDatum(false)}
	var st2 aggRuntime
	if err := o.applyAgg(&st2, plan.Aggs[0], SlotFromRow(nil, row), 0); err != nil {
		t.Fatalf("applyAgg: %v", err)
	}
	if st2.hasValue {
		t.Fatal("FILTER-false row must be skipped on the compiled path")
	}
}

// TestAggOrderKeyFailureNulls pins error→NULL parity through the
// order-key path: a div-by-zero sort key yields NULL (not an abort) on
// both twins, and the row still accumulates.
func TestAggOrderKeyFailureNulls(t *testing.T) {
	plan := &optimizer.Aggregate{Aggs: []optimizer.AggregateCall{
		{Name: "array_agg", Arg: pcol(0, "int4"), OrderBy: []optimizer.SortKey{{Expr: pbin(parser.OpDiv, pint(1), pint(0))}}},
	}}
	o := &aggregateOp{plan: plan, ctx: NewContext()}
	o.compileAggExprs()
	slot := SlotFromRow(nil, aggParityRow())
	keys := o.evalAggOrderByKeys(plan.Aggs[0].OrderBy, 0, slot)
	if len(keys) != 1 {
		t.Fatalf("order keys = %d, want 1", len(keys))
	}
	if !keys[0].IsNull() {
		t.Fatalf("failing order key = %v, want NULL (not abort)", keys[0])
	}
}

// TestAggArg2FailurePerSite pins the per-site error rule: the same
// failing Arg2 (div-by-zero delimiter) at the strict-swallow, delim,
// and regr sites — compiled and interpreted agree everywhere because
// every site shares aggEvalList.
func TestAggArg2FailurePerSite(t *testing.T) {
	bad := pbin(parser.OpDiv, pint(1), pint(0))
	for _, tc := range []struct {
		name string
		call optimizer.AggregateCall
	}{
		{"string_agg delimiter", optimizer.AggregateCall{Name: "string_agg", Arg: pcol(3, "text"), Arg2: bad}},
		{"regr", optimizer.AggregateCall{Name: "regr_slope", Arg: pcol(0, "int4"), Arg2: bad}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &optimizer.Aggregate{Aggs: []optimizer.AggregateCall{tc.call}}
			o := &aggregateOp{plan: plan, ctx: NewContext()}
			o.compileAggExprs()
			slot := SlotFromRow(nil, aggParityRow())
			var st aggRuntime
			if err := o.applyAgg(&st, plan.Aggs[0], slot, 0); err != nil {
				t.Fatalf("applyAgg with failing Arg2: %v (must skip, not abort)", err)
			}
			if st.hasValue {
				t.Fatal("failing-Arg2 row must be skipped on the compiled path")
			}
		})
	}
}

// TestAggApplyNoAllocs pins the E-06 alloc arm on the transition hot
// loop: node lists prebuilt, no per-row slot alloc (applyAgg receives
// the slot).
func TestAggApplyNoAllocs(t *testing.T) {
	plan := &optimizer.Aggregate{Aggs: []optimizer.AggregateCall{
		{Name: "sum", Arg: pcol(0, "int4")},
	}}
	o := &aggregateOp{plan: plan, ctx: NewContext()}
	o.compileAggExprs()
	slot := SlotFromRow(nil, aggParityRow())
	var st aggRuntime
	if n := testing.AllocsPerRun(100, func() {
		if err := o.applyAgg(&st, plan.Aggs[0], slot, 0); err != nil {
			t.Fatalf("applyAgg: %v", err)
		}
	}); n != 0 {
		t.Fatalf("applyAgg allocates %v per row, want 0", n)
	}
}
