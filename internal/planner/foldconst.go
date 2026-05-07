package planner

import (
	"fmt"
	"strconv"
	"strings"
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
		return &BinaryOp{pos: x.pos, Op: x.Op, Left: l, Right: r}

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
		return &ExtractExpr{pos: x.pos, Field: x.Field, Source: FoldConstants(x.Source)}

	case *InExpr:
		folded := make([]Expr, len(x.List))
		for i, item := range x.List {
			folded[i] = FoldConstants(item)
		}
		return &InExpr{pos: x.pos, Operand: FoldConstants(x.Operand), Negated: x.Negated, Plan: x.Plan, List: folded, IsNonCorrelated: x.IsNonCorrelated}

	case *FuncCall:
		foldedArgs := make([]Expr, len(x.Args))
		for i, a := range x.Args {
			foldedArgs[i] = FoldConstants(a)
		}
		return &FuncCall{pos: x.pos, Name: x.Name, Args: foldedArgs, Star: x.Star}

	// ── Non-foldable: return unchanged ─────────────────────────────────
	default:
		return e
	}
}

// foldPlanConstants applies FoldConstants to every expression embedded in a
// plan tree, mutating the tree in-place. Called at the end of planSelect and
// other plan-building functions to collapse constant sub-expressions.
func foldPlanConstants(node Node) {
	if node == nil {
		return
	}
	switch n := node.(type) {
	case *Filter:
		if n.Predicate != nil {
			n.Predicate = FoldConstants(n.Predicate)
		}
		foldPlanConstants(n.Child)
	case *Project:
		for i, t := range n.Targets {
			n.Targets[i] = FoldConstants(t)
		}
		foldPlanConstants(n.Child)
	case *Sort:
		for i, k := range n.Keys {
			n.Keys[i].Expr = FoldConstants(k.Expr)
		}
		foldPlanConstants(n.Child)
	case *Limit:
		if n.Limit != nil {
			n.Limit = FoldConstants(n.Limit)
		}
		if n.Offset != nil {
			n.Offset = FoldConstants(n.Offset)
		}
		foldPlanConstants(n.Child)
	case *Aggregate:
		// HAVING predicate is modelled as a Filter node wrapping Aggregate;
		// it will be folded when foldPlanConstants visits that Filter node.
		for i, g := range n.GroupExprs {
			n.GroupExprs[i] = FoldConstants(g)
		}
		for i, a := range n.Aggs {
			if a.Arg != nil {
				n.Aggs[i].Arg = FoldConstants(a.Arg)
			}
		}
		foldPlanConstants(n.Child)
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
		foldPlanConstants(n.Left)
		foldPlanConstants(n.Right)
	case *WindowAgg:
		foldPlanConstants(n.Child)
	case *SeqScan, *IndexScan, *IndexOnlyScan, *Values, *WorkTableScan:
		// leaf nodes: nothing to fold
	}
}

// ── Internal helpers ─────────────────────────────────────────────────────────

// tryFoldBinaryOp attempts to evaluate a binary operation whose operands have
// already been folded. Returns nil when the fold cannot be performed (e.g.,
// one operand is a non-literal ColumnRef).
func tryFoldBinaryOp(pos int, op string, l, r Expr) Expr {
	opU := strings.ToUpper(op)

	// Boolean short-circuit for AND/OR regardless of the other operand's type.
	switch opU {
	case "AND":
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
	case "OR":
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
	result, err := evalLiteralBinary(pos, opU, lv, rv)
	if err != nil {
		return nil // silently skip: leave expression unfolded on type mismatch
	}
	return result
}

// tryFoldUnaryOp attempts to evaluate a unary operation on a literal.
func tryFoldUnaryOp(pos int, op string, operand Expr) Expr {
	if _, ok := operand.(*NullConst); ok {
		return &NullConst{pos: pos}
	}
	switch strings.ToUpper(op) {
	case "NOT":
		if bc, ok := operand.(*BooleanConst); ok {
			return &BooleanConst{pos: pos, Value: !bc.Value}
		}
	case "-":
		if ic, ok := operand.(*IntegerConst); ok {
			return &IntegerConst{pos: pos, Value: -ic.Value}
		}
		if nc, ok := operand.(*NumericConst); ok {
			if len(nc.Value) > 0 && nc.Value[0] == '-' {
				return &NumericConst{pos: pos, Value: nc.Value[1:]}
			}
			return &NumericConst{pos: pos, Value: "-" + nc.Value}
		}
	case "+":
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
//   - WHEN FALSE → branch dropped
//   - WHEN TRUE  → branch taken; subsequent branches dropped
func foldCaseExpr(x *CaseExpr) Expr {
	operand := FoldConstants(x.Operand)
	var newWhens []CaseWhen
	for _, w := range x.Whens {
		cond := FoldConstants(w.When)
		then := FoldConstants(w.Then)
		// Searched CASE: evaluate the WHEN condition if it's a boolean literal.
		if operand == nil {
			if bc, ok := cond.(*BooleanConst); ok {
				if bc.Value {
					// WHEN TRUE: this branch is always taken; subsequent are dead.
					if len(newWhens) == 0 {
						// Simplify entire CASE to this THEN value.
						return then
					}
					// Use this THEN as the ELSE and stop.
					return &CaseExpr{pos: x.pos, Operand: nil, Whens: newWhens, Else: then}
				}
				// WHEN FALSE: dead branch, skip.
				continue
			}
		}
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
func evalLiteralBinary(pos int, op string, l, r literalValue) (Expr, error) {
	switch op {
	case "+", "-", "*", "/", "%":
		return evalArith(pos, op, l, r)
	case "||":
		if l.kind == "str" && r.kind == "str" {
			return &StringConst{pos: pos, Value: l.strV + r.strV}, nil
		}
		return nil, fmt.Errorf("|| requires string operands")
	case "=", "<>", "!=", "<", ">", "<=", ">=":
		cmp, err := litCompare(l, r)
		if err != nil {
			return nil, err
		}
		return &BooleanConst{pos: pos, Value: cmpResult(op, cmp)}, nil
	case "AND":
		if l.kind == "bool" && r.kind == "bool" {
			return &BooleanConst{pos: pos, Value: l.boolV && r.boolV}, nil
		}
		return nil, fmt.Errorf("AND requires boolean operands")
	case "OR":
		if l.kind == "bool" && r.kind == "bool" {
			return &BooleanConst{pos: pos, Value: l.boolV || r.boolV}, nil
		}
		return nil, fmt.Errorf("OR requires boolean operands")
	}
	return nil, fmt.Errorf("unsupported operator %s", op)
}

func evalArith(pos int, op string, l, r literalValue) (Expr, error) {
	// Promote both sides: int×int stays int; anything with num → numeric.
	if l.kind == "int" && r.kind == "int" {
		a, b := l.intV, r.intV
		switch op {
		case "+":
			return &IntegerConst{pos: pos, Value: a + b}, nil
		case "-":
			return &IntegerConst{pos: pos, Value: a - b}, nil
		case "*":
			return &IntegerConst{pos: pos, Value: a * b}, nil
		case "/":
			if b == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			return &IntegerConst{pos: pos, Value: a / b}, nil
		case "%":
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
	case "+":
		result = lf + rf
	case "-":
		result = lf - rf
	case "*":
		result = lf * rf
	case "/":
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

func cmpResult(op string, cmp int) bool {
	switch op {
	case "=":
		return cmp == 0
	case "<>", "!=":
		return cmp != 0
	case "<":
		return cmp < 0
	case ">":
		return cmp > 0
	case "<=":
		return cmp <= 0
	case ">=":
		return cmp >= 0
	}
	return false
}

