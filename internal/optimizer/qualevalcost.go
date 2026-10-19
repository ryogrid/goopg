package optimizer

import (
	"sort"

	"github.com/goopg/goopg/internal/executor/hashsize"
	"github.com/goopg/goopg/internal/parser"
)

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
	return qualEvalOpsPriced(e, true)
}

// qualEvalOpsPriced is qualEvalOps with the sublinks' own plan costs left
// out when priceSublinks is false: the comparison an IN sublink performs
// still counts, but the SubPlan's per-call cost is the caller's to charge
// (joinQualPerTuple prices correlated SubPlans through subPlanJoinQualOps).
func qualEvalOpsPriced(e Expr, priceSublinks bool) (startup, perTuple float64) {
	walkExprRefs(e, scopeSignal, exprVisitor{Visit: func(x Expr) bool {
		switch n := x.(type) {
		case *BinaryOp:
			if n.Op != parser.OpAnd && n.Op != parser.OpOr {
				perTuple++
				perTuple += implicitNumericCoercionOps(n.Left, n.Right)
			}
		case *CastExpr:
			perTuple += numericFamilyCastOps(n)
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
				if !priceSublinks {
					return true
				}
				// M0146-0019a: an uncorrelated hashable ANY is a hashed
				// SubPlan (build_subplan sets useHashTable): cost_subplan
				// charges the plan's total plus cpu_operator_cost per row
				// once, to load the table, and nothing per evaluation beyond
				// the comparison counted above.
				if inSubPlanHashed(n) {
					pc := legacyDisplayCostOf(n.Plan)
					startup += (pc.TotalCost + pgBootCPUOperatorCost*pc.PlanRows) / pgBootCPUOperatorCost
					return true
				}
				s, p := subPlanCostOps(n.Plan, sublinkAnyAll, len(n.ParParam) > 0)
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
			if priceSublinks {
				s, p := subPlanCostOps(n.Plan, sublinkExpr, len(n.ParParam) > 0)
				startup += s
				perTuple += p
			}
		case *ArraySubqueryExpr:
			if priceSublinks {
				s, p := subPlanCostOps(n.Plan, sublinkExpr, false)
				startup += s
				perTuple += p
			}
		case *ExistsExpr:
			if priceSublinks {
				s, p := subPlanCostOps(n.Plan, sublinkExists, len(n.ParParam) > 0)
				startup += s
				perTuple += p
			}
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

// M0146-0228 slice 2: cost_qual_eval prices a coercion by the function
// that performs it. Among the numeric family (int2, int4, int8, float4,
// float8, numeric) every pg_cast entry is a function cast (castmethod 'f')
// whose function has procost 1: int4_numeric, i4tod, numeric_float8, int48,
// ... Constant operands are pre-coerced by eval_const_expressions and cost
// nothing at run time.

// numericFamily classifies a type for operator resolution: 1 integer,
// 2 float, 3 numeric, 0 anything else.
func numericFamily(t string) int {
	switch t {
	case "int2", "int4", "int8", "smallint", "integer", "bigint":
		return 1
	case "float4", "float8", "real", "double precision":
		return 2
	case "numeric", "decimal":
		return 3
	}
	return 0
}

// implicitNumericCoercionOps is the implicit cast PG's operator resolution
// inserts for a binary operator over two numeric-family operands of
// different classes. Cross-type operators exist within the integers
// (int48mul, int24eq) and within the floats (float84mul), so those need no
// cast. Integer against numeric resolves to the numeric operator over
// int4_numeric(x); integer against float to the float8 operator over i4tod(x);
// numeric against float to the float8 operator over numeric_float8(x), since
// numeric→float8 is the implicit direction. One function call, procost 1, for
// a non-constant coerced operand.
func implicitNumericCoercionOps(l, r Expr) float64 {
	lt, lok := ExprResultType(l)
	rt, rok := ExprResultType(r)
	if !lok || !rok || lt.IsArray || rt.IsArray {
		return 0
	}
	lf, rf := numericFamily(lt.Name), numericFamily(rt.Name)
	if lf == 0 || rf == 0 || lf == rf {
		return 0
	}
	// The operand converted: the integer side against numeric or float, the
	// numeric side against float.
	coerced := l
	switch {
	case lf == 1:
		coerced = l
	case rf == 1:
		coerced = r
	case lf == 3:
		coerced = l
	default:
		coerced = r
	}
	if isPlanTimeConst(coerced) {
		return 0
	}
	return 1
}

// numericFamilyCastOps prices a CastExpr between two numeric-family types:
// a function cast (procost 1) unless its operand folds to a constant. Other
// casts keep the free reading (see qualEvalOps).
func numericFamilyCastOps(c *CastExpr) float64 {
	if c == nil || c.Operand == nil {
		return 0
	}
	src := c.SourceType
	if src == "" {
		if t, ok := ExprResultType(c.Operand); ok && !t.IsArray {
			src = t.Name
		}
	}
	if numericFamily(src) == 0 || numericFamily(c.TargetType) == 0 || src == c.TargetType {
		return 0
	}
	if isPlanTimeConst(c.Operand) {
		return 0
	}
	return 1
}

// isPlanTimeConst reports whether eval_const_expressions folds e to a
// constant: no column, parameter or function call anywhere in it, and no
// sublink (scopeVeto). An expression the walker cannot enumerate aborts the
// walk and reads as not constant, so it is still priced.
func isPlanTimeConst(e Expr) bool {
	if e == nil {
		return true
	}
	constant := true
	walked := walkExprRefs(e, scopeVeto, exprVisitor{Visit: func(x Expr) bool {
		switch x.(type) {
		case *ColumnRef, *OuterColumnRef, *ParamRef, *FuncCall:
			constant = false
			return false
		}
		return true
	}})
	return walked && constant
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
//
// lowered reports the sublink already carries PARAM_EXEC params (a
// pre-lowered kept sublink, M0146-0015c / M0146-0012a): its plan reads them
// as ExecParamRefs, not OuterColumnRefs, and is just as correlated.
func subPlanCostOps(plan Node, kind int, lowered bool) (startup, perTuple float64) {
	if plan == nil {
		return 0, 1
	}
	correlated := lowered || planHasOuterRef(plan)
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

// orderQualClauses is order_qual_clauses (createplan.c): a stable sort of a
// qual list by cost_qual_eval's per-tuple cost (qualEvalOps), so cheaper
// clauses are evaluated first and ties keep their order. PG sorts by
// security_level first; goopg's quals carry none (every level is 0, PG's
// value for a query without security-barrier views or RLS — ledgered).
func orderQualClauses(clauses []Expr) []Expr {
	if len(clauses) < 2 {
		return clauses
	}
	out := append([]Expr(nil), clauses...)
	cost := make(map[Expr]float64, len(out))
	for _, c := range out {
		_, cost[c] = qualEvalOps(c)
	}
	sort.SliceStable(out, func(i, j int) bool { return cost[out[i]] < cost[out[j]] })
	return out
}

// orderQualRestrictInfos is orderQualClauses over a join's restrictInfo
// list, keyed on each clause.
func orderQualRestrictInfos(ris []*restrictInfo) []*restrictInfo {
	if len(ris) < 2 {
		return ris
	}
	out := append([]*restrictInfo(nil), ris...)
	cost := make(map[*restrictInfo]float64, len(out))
	for _, ri := range out {
		_, cost[ri] = qualEvalOps(ri.clause)
	}
	sort.SliceStable(out, func(i, j int) bool { return cost[out[i]] < cost[out[j]] })
	return out
}

// inSubPlanHashed is subplan_is_hashable (subselect.c) plus build_subplan's
// other conditions for an ANY sublink goopg's executor hashes
// (evalInHashProbe; the EXPLAIN twin is executor subPlanUsesHashTable): no
// correlation, a single-operand plain equality (never ALL, never <>), and an
// estimated result — rows × (MAXALIGN(width) + MAXALIGN(SizeofHeapTupleHeader))
// — within hash_mem. hash_mem is the boot value (work_mem 4MB × 2); a session
// that changes either GUC is not seen here (ledgered).
func inSubPlanHashed(x *InExpr) bool {
	if x == nil || x.Plan == nil || x.AllOp || x.NotEqualAny || (x.AnyOp != 0 && x.AnyOp != parser.OpEq) ||
		len(x.ParParam) > 0 || planHasOuterRef(x.Plan) {
		return false
	}
	if row, isRow := x.Operand.(*RowExpr); isRow && len(row.Elems) > 1 && !x.UnknownEqFalse {
		return false
	}
	pc := legacyDisplayCostOf(x.Plan)
	maxAlign := func(n int) int { return (n + 7) &^ 7 }
	size := pc.PlanRows * float64(maxAlign(pc.PlanWidth)+maxAlign(23))
	return size <= float64(hashsize.HashMemLimit(0, 0))
}
