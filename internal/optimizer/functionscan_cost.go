package optimizer

// M0149-0001h1a80: cost_functionscan (costsize.c) for the FROM-clause
// set-returning function scans goopg plans as their own nodes.
//
// PG evaluates the function expression once, to completion, before the first
// row is returned, so cost_qual_eval of the whole call — its procost plus its
// arguments — is STARTUP cost: `startup_cost += exprcost.startup +
// exprcost.per_tuple`. Each output tuple then costs cpu_tuple_cost plus the
// scan's restriction quals. goopg charged only cpu_tuple_cost per row, so
// `generate_series(now(), now() + '1 day', '1 hour')` printed 0.00..0.25
// where PG prints 0.01..0.26.

// functionScanCallOps is cost_qual_eval of the scan's function call(s), in
// cpu_operator_cost units: procost plus the arguments, for each function.
func functionScanCallOps(n Node) (startup, perTuple float64, ok bool) {
	add := func(s, p float64) { startup += s; perTuple += p }
	switch x := n.(type) {
	case *GenerateSeries:
		args := []Expr{x.Start, x.Stop}
		if x.Step != nil {
			args = append(args, x.Step)
		}
		if x.Zone != nil {
			args = append(args, x.Zone)
		}
		add(srfCallOps(args, 1))
	case *FromUnnest:
		switch {
		case x.FuncName != "":
			add(srfCallOps(x.FuncArgs, 1))
		case x.ArrExpr != nil:
			add(srfCallOps([]Expr{x.ArrExpr}, 1))
		default:
			// unnest(a, b, ...) is ROWS FROM (unnest(a), unnest(b), ...).
			for _, a := range x.ArrExprs {
				add(srfCallOps([]Expr{a}, 1))
			}
		}
	case *UserSrfScan:
		add(srfCallOps(x.Args, routineProCost(x.Routine)))
	default:
		return 0, 0, false
	}
	return startup, perTuple, true
}

// functionScanStartup is cost_functionscan's startup cost for n, or false
// when n is not one of the function scans priced here.
func functionScanStartup(cp costParams, n Node) (float64, bool) {
	s, p, ok := functionScanCallOps(n)
	if !ok {
		return 0, false
	}
	return (s + p) * cp.cpuOperatorCost, true
}

// costFunctionScanLeaf is cost_functionscan for a join-search leaf over a
// function scan: the call's evaluation as startup, then cpu_tuple_cost and
// the restriction quals per tuple the function returns (before the quals
// filter it). The sub-plan floor it replaces charged cpu_tuple_cost per row
// twice — once inside the scan's display cost and again as the sub-plan's
// own — and nothing for the call.
func costFunctionScanLeaf(cp costParams, ri baseRelInfo, leaf Node) (Cost, bool) {
	scan := leafBaseScan(leaf)
	startup, ok := functionScanStartup(cp, scan)
	if !ok {
		return Cost{}, false
	}
	tuples := float64(EstimateRows(scan))
	if tuples < 1 {
		tuples = 1
	}
	var quals float64
	if ri.localFilter != nil {
		_, quals = conjunctsEvalOps(splitConjuncts(ri.localFilter, nil))
	}
	return Cost{Startup: startup, Total: startup + (cp.cpuTupleCost+cp.cpuOperatorCost*quals)*tuples}, true
}
