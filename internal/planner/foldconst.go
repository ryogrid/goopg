package planner

import (
	"fmt"
	"strconv"

	"github.com/goopg/goopg/internal/parser"
)

// FoldConstants performs a bottom-up constant-folding pass on a resolved
// planner expression tree. It evaluates every sub-expression whose operands
// are all literals and replaces it with the resulting literal node.
//
// The function is pure (no side-effects, returns a new node when folding
// occurs) and safe to call on any resolved Expr, including those that
// contain non-literal sub-trees (ColumnRef, OuterColumnRef, etc.).
//
// Upstream reference: postgres/src/backend/optimizer/util/clauses.c —
// eval_const_expressions.
func FoldConstants(e Expr) Expr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	// ── Literals: return unchanged ─────────────────────────────────────
	case *IntegerConst, *StringConst, *NumericConst, *BooleanConst, *NullConst,
		*TypedStringLit, *IntervalLit, *ParamRef:
		return e

	// ── Binary operator ────────────────────────────────────────────────
	case *BinaryOp:
		l := FoldConstants(x.Left)
		r := FoldConstants(x.Right)
		if folded := tryFoldBinaryOp(x.pos, x.Op, l, r); folded != nil {
			return folded
		}
		return &BinaryOp{pos: x.pos, Op: x.Op, Left: l, Right: r, ResultType: x.ResultType}

	// ── Cast expression ────────────────────────────────────────────────
	case *CastExpr:
		operand := FoldConstants(x.Operand)
		return &CastExpr{pos: x.pos, Operand: operand, TargetType: x.TargetType, SourceType: x.SourceType, Typmod: x.Typmod}

	// ── Unary operator ─────────────────────────────────────────────────
	case *UnaryOp:
		operand := FoldConstants(x.Operand)
		if folded := tryFoldUnaryOp(x.pos, x.Op, operand); folded != nil {
			return folded
		}
		return &UnaryOp{pos: x.pos, Op: x.Op, Operand: operand}

	// ── CASE expression ────────────────────────────────────────────────
	case *CaseExpr:
		return foldCaseExpr(x)

	// ── Expressions with sub-expressions: recurse ──────────────────────
	case *ExtractExpr:
		return &ExtractExpr{pos: x.pos, Field: x.Field, Source: FoldConstants(x.Source), SourceTypeName: x.SourceTypeName}

	case *InExpr:
		folded := make([]Expr, len(x.List))
		for i, item := range x.List {
			folded[i] = FoldConstants(item)
		}
		return &InExpr{pos: x.pos, Operand: FoldConstants(x.Operand), Negated: x.Negated, NotEqualAny: x.NotEqualAny, Plan: x.Plan, List: folded, IsNonCorrelated: x.IsNonCorrelated}

	case *FuncCall:
		foldedArgs := make([]Expr, len(x.Args))
		for i, a := range x.Args {
			foldedArgs[i] = FoldConstants(a)
		}
		return &FuncCall{pos: x.pos, Name: x.Name, Args: foldedArgs, Star: x.Star, Variadic: x.Variadic}

	// ── Non-foldable: return unchanged ─────────────────────────────────
	default:
		return e
	}
}

// foldEvalPanic is the panic payload used to propagate a constant-fold
// evaluation error (e.g. integer division by zero) out of FoldConstants and
// up to foldPlanConstants, which converts it into a *PlanError.
// This matches PostgreSQL's behaviour of not suppressing constant-folding of
// potentially-reachable sub-expressions (see case.sql commentary).
type foldEvalPanic struct{ err error }

// foldPlanConstants applies FoldConstants to every expression embedded in a
// plan tree, mutating the tree in-place. Called at the end of planSelect and
// other plan-building functions to collapse constant sub-expressions.
// Returns a non-nil *PlanError when a constant sub-expression raises an
// evaluation error (e.g. division by zero) that PostgreSQL would also raise at
// planning time. M0097-0047.
func foldPlanConstants(node Node) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			if fe, ok := r.(foldEvalPanic); ok {
				retErr = fe.err
			} else {
				panic(r) // re-panic for unexpected panics
			}
		}
	}()
	foldPlanConstantsInner(node)
	return nil
}

func foldPlanConstantsInner(node Node) {
	if node == nil {
		return
	}
	switch n := node.(type) {
	case *Filter:
		if n.Predicate != nil {
			n.Predicate = FoldConstants(n.Predicate)
		}
		foldPlanConstantsInner(n.Child)
	case *Project:
		for i, t := range n.Targets {
			n.Targets[i] = FoldConstants(t)
		}
		foldPlanConstantsInner(n.Child)
	case *Sort:
		for i, k := range n.Keys {
			n.Keys[i].Expr = FoldConstants(k.Expr)
		}
		foldPlanConstantsInner(n.Child)
	case *Limit:
		if n.Limit != nil {
			n.Limit = FoldConstants(n.Limit)
		}
		if n.Offset != nil {
			n.Offset = FoldConstants(n.Offset)
		}
		foldPlanConstantsInner(n.Child)
	case *Aggregate:
		// HAVING predicate is modelled as a Filter node wrapping Aggregate;
		// it will be folded when foldPlanConstantsInner visits that Filter node.
		for i, g := range n.GroupExprs {
			n.GroupExprs[i] = FoldConstants(g)
		}
		for i, a := range n.Aggs {
			if a.Arg != nil {
				n.Aggs[i].Arg = FoldConstants(a.Arg)
			}
		}
		foldPlanConstantsInner(n.Child)
	case *Join:
		if n.LeftKey != nil {
			n.LeftKey = FoldConstants(n.LeftKey)
		}
		if n.RightKey != nil {
			n.RightKey = FoldConstants(n.RightKey)
		}
		if n.Predicate != nil {
			n.Predicate = FoldConstants(n.Predicate)
		}
		foldPlanConstantsInner(n.Left)
		foldPlanConstantsInner(n.Right)
	case *WindowAgg:
		foldPlanConstantsInner(n.Child)
	case *Distinct:
		foldPlanConstantsInner(n.Child)
	case *SeqScan, *IndexScan, *IndexOnlyScan, *Values, *WorkTableScan:
		// leaf nodes: nothing to fold
	case *GenerateSeries:
		n.Start = FoldConstants(n.Start)
		n.Stop = FoldConstants(n.Stop)
		if n.Step != nil {
			n.Step = FoldConstants(n.Step)
		}
	case *PgInputErrorInfo:
		n.Value = FoldConstants(n.Value)
		n.Type = FoldConstants(n.Type)
	case *PgGetPublicationTables:
		for i := range n.Args {
			n.Args[i] = FoldConstants(n.Args[i])
		}
	case *PgAvailableWalSummaries:
		// no sub-expressions to fold
	}
}

// ── Internal helpers ─────────────────────────────────────────────────────────

// tryFoldBinaryOp attempts to evaluate a binary operation whose operands have
// already been folded. Returns nil when the fold cannot be performed (e.g.,
// one operand is a non-literal ColumnRef).
func tryFoldBinaryOp(pos int, op parser.OpCode, l, r Expr) Expr {
	// Boolean short-circuit for AND/OR regardless of the other operand's type.
	switch op {
	case parser.OpAnd:
		if lb, ok := l.(*BooleanConst); ok {
			if !lb.Value {
				return &BooleanConst{pos: pos, Value: false} // FALSE AND x → FALSE
			}
			return r // TRUE AND x → x
		}
		if rb, ok := r.(*BooleanConst); ok {
			if !rb.Value {
				return &BooleanConst{pos: pos, Value: false} // x AND FALSE → FALSE
			}
			return l // x AND TRUE → x
		}
		// NULL AND FALSE → FALSE
		if _, ok := l.(*NullConst); ok {
			if rb, ok := r.(*BooleanConst); ok && !rb.Value {
				return &BooleanConst{pos: pos, Value: false}
			}
		}
		if _, ok := r.(*NullConst); ok {
			if lb, ok := l.(*BooleanConst); ok && !lb.Value {
				return &BooleanConst{pos: pos, Value: false}
			}
		}
	case parser.OpOr:
		if lb, ok := l.(*BooleanConst); ok {
			if lb.Value {
				return &BooleanConst{pos: pos, Value: true} // TRUE OR x → TRUE
			}
			return r // FALSE OR x → x
		}
		if rb, ok := r.(*BooleanConst); ok {
			if rb.Value {
				return &BooleanConst{pos: pos, Value: true} // x OR TRUE → TRUE
			}
			return l // x OR FALSE → x
		}
		// NULL OR TRUE → TRUE
		if _, ok := l.(*NullConst); ok {
			if rb, ok := r.(*BooleanConst); ok && rb.Value {
				return &BooleanConst{pos: pos, Value: true}
			}
		}
		if _, ok := r.(*NullConst); ok {
			if lb, ok := l.(*BooleanConst); ok && lb.Value {
				return &BooleanConst{pos: pos, Value: true}
			}
		}
	}

	// If either side is NULL and it's not AND/OR, the result is NULL.
	if _, ok := l.(*NullConst); ok {
		return &NullConst{pos: pos}
	}
	if _, ok := r.(*NullConst); ok {
		return &NullConst{pos: pos}
	}

	// Both sides must be literals to fold.
	lv, lok := toLiteralValue(l)
	rv, rok := toLiteralValue(r)
	if !lok || !rok {
		return nil
	}
	result, err := evalLiteralBinary(pos, op, lv, rv)
	if err != nil {
		// Division by zero in a constant expression must propagate to the planner
		// immediately — this matches PostgreSQL's behaviour of not suppressing
		// constant-folding of potentially-reachable subexpressions (case.sql).
		// Other errors (type mismatch, unsupported op) are silently ignored and
		// the expression is left unfolded. M0097-0047.
		if err.Error() == "division by zero" {
			panic(foldEvalPanic{err: &PlanError{Code: "22012", Message: "division by zero"}})
		}
		return nil // silently skip: leave expression unfolded on type mismatch
	}
	return result
}

// tryFoldUnaryOp attempts to evaluate a unary operation on a literal.
func tryFoldUnaryOp(pos int, op parser.OpCode, operand Expr) Expr {
	if _, ok := operand.(*NullConst); ok {
		return &NullConst{pos: pos}
	}
	switch op {
	case parser.OpNot:
		if bc, ok := operand.(*BooleanConst); ok {
			return &BooleanConst{pos: pos, Value: !bc.Value}
		}
	case parser.OpUnaryNeg:
		if ic, ok := operand.(*IntegerConst); ok {
			return &IntegerConst{pos: pos, Value: -ic.Value}
		}
		if nc, ok := operand.(*NumericConst); ok {
			if len(nc.Value) > 0 && nc.Value[0] == '-' {
				return &NumericConst{pos: pos, Value: nc.Value[1:]}
			}
			return &NumericConst{pos: pos, Value: "-" + nc.Value}
		}
	case parser.OpUnaryPos:
		if _, ok := operand.(*IntegerConst); ok {
			return operand
		}
		if _, ok := operand.(*NumericConst); ok {
			return operand
		}
	}
	return nil
}

// foldCaseExpr folds a CASE expression.
// Constant WHEN conditions are evaluated at plan time:
//   - WHEN FALSE → branch and its THEN are dropped (THEN is NOT folded)
//   - WHEN TRUE  → branch taken; subsequent branches dropped
//
// Critically, dead branches are NOT folded — this matches PostgreSQL's
// simplify_case_when_conditions: an unreachable THEN expression like 1/0 does
// not throw, but a THEN under a non-constant WHEN (potentially reachable) IS
// folded and may throw if it contains a constant evaluation error. M0097-0047.
func foldCaseExpr(x *CaseExpr) Expr {
	operand := FoldConstants(x.Operand)
	var newWhens []CaseWhen
	for _, w := range x.Whens {
		cond := FoldConstants(w.When)

		// ── Searched CASE (no operand) ────────────────────────────────────────
		// Check dead/live BEFORE folding the THEN body.
		if operand == nil {
			if bc, ok := cond.(*BooleanConst); ok {
				if !bc.Value {
					// WHEN FALSE: dead branch — do NOT fold the THEN body.
					// An unreachable 1/0 here must not throw.
					continue
				}
				// WHEN TRUE: fold THEN (it is definitely reachable).
				then := FoldConstants(w.Then)
				if len(newWhens) == 0 {
					return then
				}
				return &CaseExpr{pos: x.pos, Operand: nil, Whens: newWhens, Else: then}
			}
		}

		// ── Simple CASE (with operand) ────────────────────────────────────────
		// If both the operand and the WHEN value are literals, compare them now
		// to skip dead branches without folding their THEN body.
		if operand != nil {
			if opLit, opOk := toLiteralValue(operand); opOk {
				if whenLit, wOk := toLiteralValue(cond); wOk {
					cmp, cerr := litCompare(opLit, whenLit)
					if cerr == nil {
						if cmp != 0 {
							// Dead branch: operand != WHEN value — do NOT fold THEN.
							continue
						}
						// Matching branch: fold THEN and terminate.
						then := FoldConstants(w.Then)
						if len(newWhens) == 0 {
							return then
						}
						return &CaseExpr{pos: x.pos, Operand: operand, Whens: newWhens, Else: then}
					}
				}
			}
		}

		// Non-dead / non-constant branch: fold THEN (it is potentially reachable).
		then := FoldConstants(w.Then)
		newWhens = append(newWhens, CaseWhen{When: cond, Then: then})
	}
	elseExpr := FoldConstants(x.Else)
	if len(newWhens) == 0 {
		// All branches were dead; return the ELSE.
		if elseExpr != nil {
			return elseExpr
		}
		return &NullConst{pos: x.pos}
	}
	return &CaseExpr{pos: x.pos, Operand: operand, Whens: newWhens, Else: elseExpr}
}

// literalValue holds the Go-native value of a planner literal expression.
type literalValue struct {
	kind    string // "int", "str", "num", "bool"
	intV    int64
	strV    string
	boolV   bool
	numStr  string // raw decimal text for KindNumeric
}

// toLiteralValue extracts a Go-native value from a planner literal expression.
// Returns (lv, true) on success, (zero, false) when e is not a foldable literal.
func toLiteralValue(e Expr) (literalValue, bool) {
	switch x := e.(type) {
	case *IntegerConst:
		return literalValue{kind: "int", intV: x.Value}, true
	case *StringConst:
		return literalValue{kind: "str", strV: x.Value}, true
	case *NumericConst:
		return literalValue{kind: "num", numStr: x.Value}, true
	case *BooleanConst:
		return literalValue{kind: "bool", boolV: x.Value}, true
	}
	return literalValue{}, false
}

// evalLiteralBinary evaluates a binary operator over two literal values and
// returns the result as a planner literal expression. Returns an error when
// the types are incompatible with the operator.
func evalLiteralBinary(pos int, op parser.OpCode, l, r literalValue) (Expr, error) {
	switch op {
	case parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod:
		return evalArith(pos, op, l, r)
	case parser.OpConcat:
		if l.kind == "str" && r.kind == "str" {
			return &StringConst{pos: pos, Value: l.strV + r.strV}, nil
		}
		return nil, fmt.Errorf("|| requires string operands")
	case parser.OpEq, parser.OpNe, parser.OpLt, parser.OpGt, parser.OpLe, parser.OpGe:
		cmp, err := litCompare(l, r)
		if err != nil {
			return nil, err
		}
		return &BooleanConst{pos: pos, Value: cmpResult(op, cmp)}, nil
	case parser.OpAnd:
		if l.kind == "bool" && r.kind == "bool" {
			return &BooleanConst{pos: pos, Value: l.boolV && r.boolV}, nil
		}
		return nil, fmt.Errorf("AND requires boolean operands")
	case parser.OpOr:
		if l.kind == "bool" && r.kind == "bool" {
			return &BooleanConst{pos: pos, Value: l.boolV || r.boolV}, nil
		}
		return nil, fmt.Errorf("OR requires boolean operands")
	}
	return nil, fmt.Errorf("unsupported operator %s", op)
}

func evalArith(pos int, op parser.OpCode, l, r literalValue) (Expr, error) {
	// Promote both sides: int×int stays int; anything with num → numeric.
	if l.kind == "int" && r.kind == "int" {
		a, b := l.intV, r.intV
		switch op {
		case parser.OpAdd:
			return &IntegerConst{pos: pos, Value: a + b}, nil
		case parser.OpSub:
			return &IntegerConst{pos: pos, Value: a - b}, nil
		case parser.OpMul:
			return &IntegerConst{pos: pos, Value: a * b}, nil
		case parser.OpDiv:
			if b == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			return &IntegerConst{pos: pos, Value: a / b}, nil
		case parser.OpMod:
			if b == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			return &IntegerConst{pos: pos, Value: a % b}, nil
		}
	}
	// At least one numeric: promote both to decimal strings and fold.
	ls, rs, err := toDecimalStrings(l, r)
	if err != nil {
		return nil, err
	}
	lf, err2 := strconv.ParseFloat(ls, 64)
	if err2 != nil {
		return nil, err2
	}
	rf, err3 := strconv.ParseFloat(rs, 64)
	if err3 != nil {
		return nil, err3
	}
	var result float64
	switch op {
	case parser.OpAdd:
		result = lf + rf
	case parser.OpSub:
		result = lf - rf
	case parser.OpMul:
		result = lf * rf
	case parser.OpDiv:
		if rf == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		result = lf / rf
	default:
		return nil, fmt.Errorf("unsupported numeric op %s", op)
	}
	return &NumericConst{pos: pos, Value: strconv.FormatFloat(result, 'f', -1, 64)}, nil
}

func toDecimalStrings(l, r literalValue) (string, string, error) {
	toString := func(v literalValue) (string, error) {
		switch v.kind {
		case "int":
			return strconv.FormatInt(v.intV, 10), nil
		case "num":
			return v.numStr, nil
		}
		return "", fmt.Errorf("cannot promote %s to numeric", v.kind)
	}
	ls, err := toString(l)
	if err != nil {
		return "", "", err
	}
	rs, err := toString(r)
	if err != nil {
		return "", "", err
	}
	return ls, rs, nil
}

// litCompare compares two literal values. Returns -1 / 0 / +1.
func litCompare(l, r literalValue) (int, error) {
	// Same kind: direct comparison.
	switch l.kind {
	case "int":
		if r.kind == "int" {
			return cmpI64(l.intV, r.intV), nil
		}
		if r.kind == "num" {
			lf, rf, err := parseNumericPair(strconv.FormatInt(l.intV, 10), r.numStr)
			if err != nil {
				return 0, err
			}
			return cmpF64(lf, rf), nil
		}
	case "num":
		ls := l.numStr
		rs := r.numStr
		if r.kind == "int" {
			rs = strconv.FormatInt(r.intV, 10)
		}
		lf, rf, err := parseNumericPair(ls, rs)
		if err != nil {
			return 0, err
		}
		return cmpF64(lf, rf), nil
	case "str":
		if r.kind != "str" {
			return 0, fmt.Errorf("cannot compare string with %s", r.kind)
		}
		if l.strV < r.strV {
			return -1, nil
		} else if l.strV > r.strV {
			return 1, nil
		}
		return 0, nil
	case "bool":
		if r.kind != "bool" {
			return 0, fmt.Errorf("cannot compare bool with %s", r.kind)
		}
		if l.boolV == r.boolV {
			return 0, nil
		} else if !l.boolV {
			return -1, nil
		}
		return 1, nil
	}
	return 0, fmt.Errorf("cannot compare %s with %s", l.kind, r.kind)
}

func parseNumericPair(a, b string) (float64, float64, error) {
	af, err := strconv.ParseFloat(a, 64)
	if err != nil {
		return 0, 0, err
	}
	bf, err := strconv.ParseFloat(b, 64)
	if err != nil {
		return 0, 0, err
	}
	return af, bf, nil
}

func cmpI64(a, b int64) int {
	if a < b {
		return -1
	} else if a > b {
		return 1
	}
	return 0
}

func cmpF64(a, b float64) int {
	if a < b {
		return -1
	} else if a > b {
		return 1
	}
	return 0
}

func cmpResult(op parser.OpCode, cmp int) bool {
	switch op {
	case parser.OpEq:
		return cmp == 0
	case parser.OpNe:
		return cmp != 0
	case parser.OpLt:
		return cmp < 0
	case parser.OpGt:
		return cmp > 0
	case parser.OpLe:
		return cmp <= 0
	case parser.OpGe:
		return cmp >= 0
	}
	return false
}

