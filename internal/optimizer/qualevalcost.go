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
// MIN_ARRAY_SIZE_FOR_HASHED_SAOP constants. A sublink costs 1 here — PG
// charges the planned SubPlan's per-call cost, which this walk cannot see.
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
			if n.Plan != nil || n.Subquery != nil {
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
		case *SubqueryExpr, *ArraySubqueryExpr, *ExistsExpr:
			perTuple++
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
