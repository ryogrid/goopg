package optimizer

import "github.com/goopg/goopg/internal/catalog"

// aggSplit is PG's AggSplit (nodes.h) as get_agg_clause_costs reads it.
type aggSplit int

const (
	// aggSplitSimple is AGGSPLIT_SIMPLE: transfn per row, finalfn per group.
	aggSplitSimple aggSplit = iota
	// aggSplitInitialSerial is AGGSPLIT_INITIAL_SERIAL, the Partial
	// Aggregate: transfn per row, serialfn per group, the finalfn skipped.
	aggSplitInitialSerial
	// aggSplitFinalDeserial is AGGSPLIT_FINAL_DESERIAL, the Finalize
	// Aggregate: combinefn plus deserialfn per row, finalfn per group.
	aggSplitFinalDeserial
)

// aggCosts is AggClauseCosts' transCost.per_tuple and finalCost.per_tuple.
type aggCosts struct {
	transPerTuple, finalPerGroup float64
}

// legacyAggCosts is the pre-M0146-0221 pricing: one cpu_operator_cost per
// aggregate, per input tuple and per group, in every split mode.
func legacyAggCosts(cp costParams, nAggs int) aggCosts {
	c := cp.cpuOperatorCost * float64(nAggs)
	return aggCosts{transPerTuple: c, finalPerGroup: c}
}

// aggClauseCosts ports the function-cost half of get_agg_clause_costs
// (optimizer/prep/prepagg.c) for an Aggregate's calls under one split mode.
//
// Per transition state (AggTransInfo), it charges:
//   - the transfn per input row, or the combinefn when combining;
//   - the deserialfn per row when deserializing;
//   - the serialfn per group when serializing.
//
// Per distinct aggregate (AggInfo), it charges the finalfn per group unless
// the final step is skipped. Each term is the support function's procost
// times cpu_operator_cost, read from pg_aggregate.dat and pg_proc.dat
// (catalog.LookupBuiltinAggCost).
//
// Identical calls share one AggInfo and one AggTransInfo. Calls over the same
// arguments with compatible transitions (sum and avg of a numeric column)
// share one AggTransInfo, as find_compatible_trans arranges.
//
// A call the table cannot resolve keeps the legacy one-unit-each pricing:
// user-defined aggregates, and argument types it cannot name.
//
// M0146-0228: per transition state, outside the combine step, the arguments'
// and the FILTER's evaluation cost (cost_qual_eval_node, qualEvalOps) is added
// to the per-input-row cost, as get_agg_clause_costs does ("these costs are
// incurred only by the initial aggregate node"). TPC-DS Q23's
// sum(ss_quantity*ss_sales_price) priced its serial HashAggregate at half PG's
// per-row cost, so the serial arm undercut PG's Partial/Finalize split.
func aggClauseCosts(cp costParams, agg *Aggregate, split aggSplit) aggCosts {
	if agg == nil {
		return aggCosts{}
	}
	var trans, final float64
	type seenTrans struct {
		key  string
		call AggregateCall
	}
	var calls []AggregateCall
	var transStates []seenTrans
	for _, c := range agg.Aggs {
		dup := false
		for _, prev := range calls {
			if sameAggArgs(prev, c) && prev.Name == c.Name {
				dup = true
				break
			}
		}
		if dup {
			continue // one AggInfo, one AggTransInfo
		}
		calls = append(calls, c)
		cost, ok := aggCallCost(c)
		if !ok {
			trans++
			final++
			if split != aggSplitFinalDeserial {
				trans += aggInputEvalOps(c)
			}
			continue
		}
		shared := false
		for _, t := range transStates {
			if t.key == cost.TransKey && sameAggArgs(t.call, c) {
				shared = true
				break
			}
		}
		if !shared {
			transStates = append(transStates, seenTrans{key: cost.TransKey, call: c})
			switch split {
			case aggSplitFinalDeserial:
				trans += cost.Combine + cost.Deserial
			case aggSplitInitialSerial:
				trans += cost.Trans + aggInputEvalOps(c)
				final += cost.Serial
			default:
				trans += cost.Trans + aggInputEvalOps(c)
			}
		}
		if split != aggSplitInitialSerial {
			final += cost.Final
		}
	}
	return aggCosts{transPerTuple: trans * cp.cpuOperatorCost, finalPerGroup: final * cp.cpuOperatorCost}
}

// aggInputEvalOps is get_agg_clause_costs' input-expression charge for one
// transition state, in cpu_operator_cost units: cost_qual_eval_node over the
// arguments plus the FILTER, paid per input row. Startup costs are not
// separated out here; aggregate arguments carry none in practice.
func aggInputEvalOps(c AggregateCall) float64 {
	var ops float64
	for _, e := range append([]Expr{c.Arg, c.Arg2, c.Filter}, c.ExtraArgs...) {
		if e == nil {
			continue
		}
		_, per := qualEvalOps(e)
		ops += per
	}
	return ops
}

// aggCallCost resolves one call to its built-in aggregate's support costs.
func aggCallCost(c AggregateCall) (catalog.BuiltinAggCost, bool) {
	if c.UserAgg != nil {
		return catalog.BuiltinAggCost{}, false
	}
	var argTypes []string
	if !c.Star {
		args := []Expr{c.Arg}
		if c.Arg2 != nil {
			args = append(args, c.Arg2)
		}
		args = append(args, c.ExtraArgs...)
		for i, a := range args {
			var t catalog.Type
			if i == 0 && c.InputType.Name != "" {
				t = c.InputType
			} else if a != nil {
				t, _ = ExprResultType(a)
			}
			argTypes = append(argTypes, aggArgTypeName(t))
		}
	}
	return catalog.LookupBuiltinAggCost(c.Name, argTypes)
}

// aggArgTypeName renders a type as pg_aggregate.dat's regprocedure keys spell
// it; "" when unknown, which only polymorphic or cost-equal overloads match.
func aggArgTypeName(t catalog.Type) string {
	if t.Name == "" {
		return ""
	}
	if t.IsArray {
		return t.Name + "[]"
	}
	return t.Name
}

// sameAggArgs is the argument half of find_compatible_agg /
// find_compatible_trans: the same inputs, DISTINCT, ORDER BY and FILTER.
func sameAggArgs(a, b AggregateCall) bool {
	if a.Star != b.Star || a.Distinct != b.Distinct || a.WithinGroup != b.WithinGroup ||
		len(a.OrderBy) > 0 || len(b.OrderBy) > 0 || len(a.WithinGroupOrderBy) > 0 || len(b.WithinGroupOrderBy) > 0 ||
		len(a.ExtraArgs) != len(b.ExtraArgs) {
		return false
	}
	pair := func(x, y Expr) bool {
		if x == nil || y == nil {
			return x == nil && y == nil
		}
		return exprEqual(x, y)
	}
	if !pair(a.Arg, b.Arg) || !pair(a.Arg2, b.Arg2) || !pair(a.Filter, b.Filter) {
		return false
	}
	for i := range a.ExtraArgs {
		if !pair(a.ExtraArgs[i], b.ExtraArgs[i]) {
			return false
		}
	}
	return true
}
