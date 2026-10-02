package optimizer

import "github.com/goopg/goopg/internal/parser"

// minArraySizeForHashedSAOP is clauses.c's MIN_ARRAY_SIZE_FOR_HASHED_SAOP: an
// `x = ANY (list of constants)` of at least this many elements is evaluated
// through a hash table built at startup.
const minArraySizeForHashedSAOP = 9

// defaultArrayLength is estimate_array_length's answer when the array's size
// cannot be seen (selfuncs.c: "otherwise, make a guess" = 10).
const defaultArrayLength = 10

// qualEvalOps is cost_qual_eval (costsize.c) for one expression, in units of
// cpu_operator_cost: the startup part and the part paid on every evaluation.
//
// It ports cost_qual_eval_walker's charges for the node kinds goopg's
// expressions carry. Each operator and function call costs its procost, which
// is 1 for the built-ins goopg prices. AND, OR, NOT, CASE, NULL and boolean
// tests, column references and constants are free; their operands are still
// walked. An `x = ANY (list)` is a ScalarArrayOpExpr: half the list per
// evaluation when linear, or one hash plus one comparison per evaluation with
// the hash table built at startup once the list reaches
// MIN_ARRAY_SIZE_FOR_HASHED_SAOP constants. A sublink is priced by
// subPlanCostOps (cost_subplan) once its plan exists; an unplanned one costs 1.
//
// The walk is walkExprRefs' exhaustive one (sublink bodies are other scopes
// and are not entered); an expression type it does not know aborts the walk
// and the count gathered so far stands.
//
// Casts count 0: goopg's CastExpr does not say whether PG would build a free
// RelabelType, a cast function (1) or a CoerceViaIO (2); the free reading is
// the common one for the text-family casts TPC-DS filters carry.
func qualEvalOps(e Expr) (startup, perTuple float64) {
	walkExprRefs(e, scopeSignal, exprVisitor{Visit: func(x Expr) bool {
		switch n := x.(type) {
		case *BinaryOp:
			if n.Op != parser.OpAnd && n.Op != parser.OpOr {
				perTuple++
			}
		case *UnaryOp:
			if n.Op != parser.OpNot {
				perTuple++
			}
		case *FuncCall, *IsDistinctFromExpr:
			perTuple++
		case *InExpr:
			if n.Plan != nil {
				// The test expression's comparison, then the SubPlan.
				perTuple++
				s, p := subPlanCostOps(n.Plan, sublinkAnyAll)
				startup += s
				perTuple += p
				return true
			}
			if n.Subquery != nil {
				perTuple++
				return true
			}
			length := len(n.List)
			if length == 0 {
				length = defaultArrayLength
			}
			if length >= minArraySizeForHashedSAOP && allConstExprs(n.List) {
				startup += float64(length)
				perTuple += 2
				return true
			}
			perTuple += 0.5 * float64(length)
		case *SubqueryExpr:
			s, p := subPlanCostOps(n.Plan, sublinkExpr)
			startup += s
			perTuple += p
		case *ArraySubqueryExpr:
			s, p := subPlanCostOps(n.Plan, sublinkExpr)
			startup += s
			perTuple += p
		case *ExistsExpr:
			s, p := subPlanCostOps(n.Plan, sublinkExists)
			startup += s
			perTuple += p
		}
		return true
	}})
	return startup, perTuple
}

// conjunctsEvalOps sums qualEvalOps over a conjunct list.
func conjunctsEvalOps(conjuncts []Expr) (startup, perTuple float64) {
	for _, c := range conjuncts {
		s, p := qualEvalOps(c)
		startup += s
		perTuple += p
	}
	return startup, perTuple
}

func allConstExprs(es []Expr) bool {
	for _, e := range es {
		switch e.(type) {
		case *IntegerConst, *StringConst, *NumericConst, *BooleanConst, *NullConst, *TypedStringLit:
		default:
			return false
		}
	}
	return len(es) > 0
}

// indexClausesEvalOps is the qual cost of the restriction conjuncts an index
// path consumes as index quals: cost_index's qpquals are the baserestrictinfo
// minus these (costsize.c), so a scan's qual cost less theirs is what the heap
// tuples still pay.
func indexClausesEvalOps(clauses []indexPathClause) float64 {
	var ops float64
	for _, c := range clauses {
		_, p := qualEvalOps(c.local)
		ops += p
	}
	return ops
}

// pgBootCPUOperatorCost is cpu_operator_cost's boot value (guc_tables.c),
// the unit subPlanCostOps converts a subplan's absolute costs into.
const pgBootCPUOperatorCost = 0.0025

// sublink kinds cost_subplan distinguishes.
const (
	sublinkExpr   = iota // EXPR / ARRAY: every output tuple is fetched
	sublinkExists        // EXISTS: one tuple is fetched
	sublinkAnyAll        // ANY / ALL: half the output, plus a comparison per row read
)

// subPlanCostOps is cost_subplan (costsize.c) for one planned sublink, as
// cost_qual_eval_walker adds it to the enclosing qual (M0146-0005di), in
// cpu_operator_cost units so it sums with qualEvalOps' operator counts:
//
//   - each evaluation pays the share of the run cost it reads
//     (EXPR all of it, EXISTS one tuple's, ANY/ALL half plus half the rows
//     at cpu_operator_cost), plus the plan's startup — once when the SubPlan
//     is uncorrelated (goopg caches the output, PG's ExecMaterializesOutput
//     case), on every evaluation when it is correlated;
//   - an uncorrelated EXPR / ARRAY / EXISTS sublink is an InitPlan upstream:
//     the qual reads its output Param, which costs nothing here (the
//     initplan's own cost is charged to the plan, not the qual);
//   - an unplanned sublink keeps the old 1-operator charge.
//
// A hashable uncorrelated IN is priced like any other uncorrelated ANY, not
// as the hashed SubPlan goopg executes (subplan_hash.go): make_subplan wraps
// the two forms in an AlternativeSubPlan listing the plain one first, and
// cost_qual_eval_walker "arbitrarily use[s] the first alternative plan for
// costing" — the choice is only made later, in setrefs.
//
// The plan's costs are its stamped path costs, or the legacy display
// estimate where the search did not produce it. Converting to operator units
// uses cpu_operator_cost's boot value; a session that changes the GUC prices
// the subplan part slightly off (ledgered).
func subPlanCostOps(plan Node, kind int) (startup, perTuple float64) {
	if plan == nil {
		return 0, 1
	}
	correlated := planHasOuterRef(plan)
	if !correlated && kind != sublinkAnyAll {
		return 0, 0
	}
	pc := legacyDisplayCostOf(plan)
	const op = pgBootCPUOperatorCost
	run := pc.TotalCost - pc.StartupCost
	var per float64
	switch kind {
	case sublinkExists:
		per = run / clampRowEst(pc.PlanRows)
	case sublinkAnyAll:
		per = 0.5*run + 0.5*pc.PlanRows*op
	default:
		per = run
	}
	if correlated {
		per += pc.StartupCost
	} else {
		startup = pc.StartupCost
	}
	return startup / op, per / op
}
