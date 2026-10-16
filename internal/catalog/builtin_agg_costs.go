package catalog

import (
	"sort"
	"strings"
)

// BuiltinAggCost is the procost of each support function of one built-in
// aggregate (pg_aggregate + pg_proc), 0 where the aggregate has none. It is
// what get_agg_clause_costs (optimizer/prep/prepagg.c) multiplies by
// cpu_operator_cost. M0146-0221.
type BuiltinAggCost struct {
	Trans, Final, Combine, Serial, Deserial float64
	// TransKey identifies the transition state (transfn, serialfn,
	// deserialfn, combinefn, transtype, initval): two calls over the same
	// arguments with equal keys share one state, as find_compatible_trans
	// (prepagg.c) arranges, and get_agg_clause_costs charges it once.
	TransKey string
}

// builtinAggCostsByName indexes builtinAggCosts by proname, each overload with
// its regprocedure argument list.
var builtinAggCostsByName = func() map[string][]aggCostOverload {
	out := map[string][]aggCostOverload{}
	keys := make([]string, 0, len(builtinAggCosts))
	for k := range builtinAggCosts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		i := strings.IndexByte(k, '(')
		name, args := k[:i], strings.TrimSuffix(k[i+1:], ")")
		var argTypes []string
		if args != "" {
			argTypes = strings.Split(args, ",")
		}
		out[name] = append(out[name], aggCostOverload{args: argTypes, cost: builtinAggCosts[k]})
	}
	return out
}()

type aggCostOverload struct {
	args []string
	cost BuiltinAggCost
}

// LookupBuiltinAggCost resolves an aggregate call to its pg_aggregate row's
// support-function costs: the overload whose argument types match exactly
// (a polymorphic `any*` parameter matches any type). A call that matches
// several overloads, or whose types are unknown (empty), still resolves when
// every candidate of that arity costs the same; otherwise it reports false.
func LookupBuiltinAggCost(name string, argTypes []string) (BuiltinAggCost, bool) {
	overloads := builtinAggCostsByName[strings.ToLower(name)]
	var cands []BuiltinAggCost
	var exact []BuiltinAggCost
	for _, o := range overloads {
		if len(o.args) != len(argTypes) {
			continue
		}
		cands = append(cands, o.cost)
		match, poly := true, false
		for i, a := range o.args {
			switch {
			case argTypes[i] == a:
			case strings.HasPrefix(a, "any"):
				poly = true
			default:
				match = false
			}
		}
		if match && !poly {
			return o.cost, true
		}
		if match {
			exact = append(exact, o.cost)
		}
	}
	// Polymorphic matches decide when there are any; otherwise every
	// overload of the arity is a candidate. Either way the costs must agree.
	set := exact
	if len(set) == 0 {
		set = cands
	}
	if len(set) > 0 && allAggCostsEqual(set) {
		return set[0], true
	}
	return BuiltinAggCost{}, false
}

func allAggCostsEqual(set []BuiltinAggCost) bool {
	for _, c := range set[1:] {
		if c != set[0] {
			return false
		}
	}
	return true
}

// BuiltinAggCostByOID returns the support-function costs of the built-in
// aggregate whose pg_proc OID is aggfnoid, keyed through the generated pg_proc
// name and argument-type indexes.
func BuiltinAggCostByOID(aggfnoid uint32) (BuiltinAggCost, bool) {
	name, ok := pgProcNamesByOID[aggfnoid]
	if !ok {
		return BuiltinAggCost{}, false
	}
	c, ok := builtinAggCosts[name+"("+strings.Join(pgProcArgTypeNamesByOID[aggfnoid], ",")+")"]
	return c, ok
}

// BuiltinAggCostCount is the number of built-in aggregates in the table.
func BuiltinAggCostCount() int { return len(builtinAggCosts) }
