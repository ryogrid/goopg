package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestAggClauseCostsPortsGetAggClauseCosts pins M0146-0221 slice 2 against
// get_agg_clause_costs (prepagg.c) and pg_aggregate.dat. TPC-DS Q5's wsr
// branch has four sum(numeric) over distinct columns. sum(numeric) has a
// finalfn, a combinefn, a serialfn and a deserialfn, all procost 1. So:
//   - SIMPLE charges 4 transfns per row and 4 finalfns per group;
//   - the Partial Aggregate charges 4 transfns per row and 4 serialfns per
//     group, skipping the finalfn;
//   - the Finalize charges 4 combinefns plus 4 deserialfns per row and 4
//     finalfns per group.
// The legacy pricing charged one unit each in every mode, so the Finalize was
// 0.0025 x 4 per row too cheap, and Q5's split arm won a tie PG's no-split arm
// wins.
func TestAggClauseCostsPortsGetAggClauseCosts(t *testing.T) {
	cp := defaultCostParams()
	op := cp.cpuOperatorCost
	numeric := catalog.Type{Name: "numeric"}
	col := func(i int) Expr { return &ColumnRef{Index: i, Name: "c", Type: numeric} }
	call := func(name string, i int) AggregateCall {
		return AggregateCall{Name: name, Arg: col(i), InputType: numeric, Type: numeric}
	}
	sums := &Aggregate{Aggs: []AggregateCall{call("sum", 0), call("sum", 1), call("sum", 2), call("sum", 3)}}

	cases := []struct {
		split        aggSplit
		trans, final float64
	}{
		{aggSplitSimple, 4, 4},
		{aggSplitInitialSerial, 4, 4},
		{aggSplitFinalDeserial, 8, 4},
	}
	for _, c := range cases {
		got := aggClauseCosts(cp, sums, c.split)
		if got.transPerTuple != c.trans*op || got.finalPerGroup != c.final*op {
			t.Errorf("split %d: got trans %v final %v, want %v / %v", c.split, got.transPerTuple, got.finalPerGroup,
				c.trans*op, c.final*op)
		}
	}

	// sum and avg of one numeric column share numeric_avg_accum's state
	// (find_compatible_trans): one transfn, two finalfns.
	shared := &Aggregate{Aggs: []AggregateCall{call("sum", 0), call("avg", 0)}}
	if got := aggClauseCosts(cp, shared, aggSplitSimple); got.transPerTuple != op || got.finalPerGroup != 2*op {
		t.Errorf("sum+avg of one column: got %v / %v, want one shared transition and two finals", got.transPerTuple, got.finalPerGroup)
	}
	// An identical call is one AggInfo: nothing is charged twice.
	twice := &Aggregate{Aggs: []AggregateCall{call("sum", 0), call("sum", 0)}}
	if got := aggClauseCosts(cp, twice, aggSplitSimple); got.transPerTuple != op || got.finalPerGroup != op {
		t.Errorf("duplicate call: got %v / %v, want one of each", got.transPerTuple, got.finalPerGroup)
	}
	// count(*) has no finalfn and no serialization: int8inc per row only.
	star := &Aggregate{Aggs: []AggregateCall{{Name: "count", Star: true}}}
	if got := aggClauseCosts(cp, star, aggSplitFinalDeserial); got.transPerTuple != op || got.finalPerGroup != 0 {
		t.Errorf("count(*) finalize: got %v / %v, want int8pl per row and nothing per group", got.transPerTuple, got.finalPerGroup)
	}
	// A user-defined aggregate keeps the legacy one-unit pricing.
	user := &Aggregate{Aggs: []AggregateCall{{Name: "myagg", Arg: col(0), UserAgg: &catalog.UserAggregate{}}}}
	if got := aggClauseCosts(cp, user, aggSplitFinalDeserial); got != legacyAggCosts(cp, 1) {
		t.Errorf("user aggregate: got %+v, want the legacy pricing", got)
	}
}

// TestLookupBuiltinAggCostResolvesOverloads checks the lookup's overload rules:
// an exact signature wins, a polymorphic parameter matches any type, and an
// unknown type resolves only when every overload costs the same.
func TestLookupBuiltinAggCostResolvesOverloads(t *testing.T) {
	if c, ok := catalog.LookupBuiltinAggCost("sum", []string{"int4"}); !ok || c.Final != 0 || c.Serial != 0 {
		t.Errorf("sum(int4): %+v %v, want int4_sum with no final or serial function", c, ok)
	}
	if c, ok := catalog.LookupBuiltinAggCost("sum", []string{"numeric"}); !ok || c.Final != 1 || c.Deserial != 1 {
		t.Errorf("sum(numeric): %+v %v, want final and deserial functions", c, ok)
	}
	if _, ok := catalog.LookupBuiltinAggCost("sum", []string{""}); ok {
		t.Errorf("sum(<unknown>) resolved, but its overloads cost differently")
	}
	if _, ok := catalog.LookupBuiltinAggCost("count", []string{"text"}); !ok {
		t.Errorf("count(text) must resolve through count(any)")
	}
}

// TestAggClauseCostsChargesInputExpressions pins M0146-0228: get_agg_clause_costs
// adds the arguments' and the FILTER's cost_qual_eval to each transition
// state's per-input-row cost, except in the combine step, which only sees
// already-aggregated states. TPC-DS Q23's sum(ss_quantity*ss_sales_price)
// priced its serial HashAggregate without the multiply, half PG's per-row cost.
func TestAggClauseCostsChargesInputExpressions(t *testing.T) {
	cp := defaultCostParams()
	op := cp.cpuOperatorCost
	numeric := catalog.Type{Name: "numeric"}
	col := func(i int) Expr { return &ColumnRef{Index: i, Name: "c", Type: numeric} }
	product := &BinaryOp{Op: parser.OpMul, Left: col(0), Right: col(1)}
	sumOf := func(arg Expr, filter Expr) AggregateCall {
		return AggregateCall{Name: "sum", Arg: arg, Filter: filter, InputType: numeric, Type: numeric}
	}
	// sum(a*b): transfn 1 + multiply 1 per row, outside combine.
	agg := &Aggregate{Aggs: []AggregateCall{sumOf(product, nil)}}
	for _, c := range []struct {
		split aggSplit
		trans float64
	}{{aggSplitSimple, 2}, {aggSplitInitialSerial, 2}, {aggSplitFinalDeserial, 2}} {
		got := aggClauseCosts(cp, agg, c.split)
		want := c.trans * op
		if c.split == aggSplitFinalDeserial {
			// combinefn + deserialfn; the input expression is not re-evaluated.
			want = aggClauseCosts(cp, &Aggregate{Aggs: []AggregateCall{sumOf(col(0), nil)}}, c.split).transPerTuple
		}
		if got.transPerTuple != want {
			t.Errorf("split %d: trans %v, want %v", c.split, got.transPerTuple, want)
		}
	}
	// A FILTER is charged per input row too.
	filter := &BinaryOp{Op: parser.OpGt, Left: col(2), Right: &IntegerConst{Value: 0}}
	filtered := &Aggregate{Aggs: []AggregateCall{sumOf(col(0), filter)}}
	if got := aggClauseCosts(cp, filtered, aggSplitSimple).transPerTuple; got != 2*op {
		t.Errorf("sum(c) FILTER (WHERE c > 0): trans %v, want transfn + one comparison = %v", got, 2*op)
	}
}
