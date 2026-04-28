package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/planner"
)

// ExecError is the executor's structured error. Code is a SQLSTATE
// value the wire-protocol path forwards to ErrorResponse.
type ExecError struct {
	Code    string
	Message string
	Pos     int
}

func (e *ExecError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("executor error: %s (byte %d)", e.Message, e.Pos)
	}
	return fmt.Sprintf("%s: %s (byte %d)", e.Code, e.Message, e.Pos)
}

// evalExpr resolves a planner expression against the input row.
// Operators that produce no input pass nil for `row`; in that case
// any ColumnRef resolution is an internal error.
func evalExpr(e planner.Expr, row Row, ctx *Context) (Datum, error) {
	switch x := e.(type) {
	case *planner.IntegerConst:
		return Datum{Kind: KindInt, Int: x.Value}, nil
	case *planner.NumericConst:
		// v0 surfaces the literal as a string Datum so the
		// codec stores it verbatim in the varlen text frame
		// shared with VARCHAR/CHAR. Real numeric arithmetic
		// waits on the type system.
		return Datum{Kind: KindString, String: x.Value}, nil
	case *planner.StringConst:
		return Datum{Kind: KindString, String: x.Value}, nil
	case *planner.NullConst:
		return NullDatum, nil
	case *planner.BooleanConst:
		return Datum{Kind: KindBool, Bool: x.Value}, nil
	case *planner.ParamRef:
		if x.Number < 1 || x.Number > len(ctx.Params) {
			return Datum{}, &ExecError{Code: "08P01", Pos: x.Pos(), Message: fmt.Sprintf("parameter $%d not bound", x.Number)}
		}
		return ctx.Params[x.Number-1], nil
	case *planner.ColumnRef:
		if row == nil || x.Index >= len(row) {
			return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: fmt.Sprintf("column ref %s/%d out of range", x.Name, x.Index)}
		}
		return row[x.Index], nil
	case *planner.UnaryOp:
		operand, err := evalExpr(x.Operand, row, ctx)
		if err != nil {
			return Datum{}, err
		}
		return evalUnary(x.Op, operand, x.Pos())
	case *planner.BinaryOp:
		left, err := evalExpr(x.Left, row, ctx)
		if err != nil {
			return Datum{}, err
		}
		right, err := evalExpr(x.Right, row, ctx)
		if err != nil {
			return Datum{}, err
		}
		return evalBinary(x.Op, left, right, x.Pos())
	case *planner.FuncCall:
		return evalFuncCall(x, row, ctx)
	}
	return Datum{}, &ExecError{Code: "XX000", Pos: e.Pos(), Message: fmt.Sprintf("unsupported expression %T", e)}
}

// evalUnary handles -, +, NOT.
func evalUnary(op string, d Datum, pos int) (Datum, error) {
	if d.IsNull() {
		return NullDatum, nil
	}
	switch op {
	case "-":
		if d.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator unary - requires integer"}
		}
		return Datum{Kind: KindInt, Int: -d.Int}, nil
	case "+":
		if d.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator unary + requires integer"}
		}
		return d, nil
	case "NOT":
		if d.Kind != KindBool {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator NOT requires boolean"}
		}
		return Datum{Kind: KindBool, Bool: !d.Bool}, nil
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("unknown unary operator %s", op)}
}

// evalBinary handles arithmetic, comparison, and boolean operators.
// SQL three-valued logic: NULL operand on most operators yields NULL;
// AND/OR follow Kleene's rules.
func evalBinary(op string, left, right Datum, pos int) (Datum, error) {
	switch op {
	case "AND":
		return evalAnd(left, right), nil
	case "OR":
		return evalOr(left, right), nil
	}
	if left.IsNull() || right.IsNull() {
		return NullDatum, nil
	}
	switch op {
	case "+", "-", "*", "/", "%":
		if left.Kind != KindInt || right.Kind != KindInt {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("operator %s requires integer operands", op)}
		}
		return arithmetic(op, left.Int, right.Int, pos)
	case "||":
		if left.Kind != KindString || right.Kind != KindString {
			return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "operator || requires string operands"}
		}
		return Datum{Kind: KindString, String: left.String + right.String}, nil
	case "=", "<", ">", "<=", ">=", "<>", "!=":
		cmp, err := compareDatum(left, right, pos)
		if err != nil {
			return Datum{}, err
		}
		return Datum{Kind: KindBool, Bool: cmpResult(op, cmp)}, nil
	}
	return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("unknown operator %s", op)}
}

func arithmetic(op string, a, b int64, pos int) (Datum, error) {
	var r int64
	switch op {
	case "+":
		r = a + b
	case "-":
		r = a - b
	case "*":
		r = a * b
	case "/":
		if b == 0 {
			return Datum{}, &ExecError{Code: "22012", Pos: pos, Message: "division by zero"}
		}
		r = a / b
	case "%":
		if b == 0 {
			return Datum{}, &ExecError{Code: "22012", Pos: pos, Message: "division by zero"}
		}
		r = a % b
	}
	return Datum{Kind: KindInt, Int: r}, nil
}

// compareDatum returns -1/0/1 the same way upstream's btree
// comparators do, scoped to the v0 type set.
func compareDatum(a, b Datum, pos int) (int, error) {
	if a.Kind != b.Kind {
		return 0, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("comparison across kinds %d vs %d", a.Kind, b.Kind)}
	}
	switch a.Kind {
	case KindInt:
		switch {
		case a.Int < b.Int:
			return -1, nil
		case a.Int > b.Int:
			return 1, nil
		}
		return 0, nil
	case KindBool:
		switch {
		case !a.Bool && b.Bool:
			return -1, nil
		case a.Bool && !b.Bool:
			return 1, nil
		}
		return 0, nil
	case KindString:
		return strings.Compare(a.String, b.String), nil
	case KindTime:
		switch {
		case a.Time.Before(b.Time):
			return -1, nil
		case a.Time.After(b.Time):
			return 1, nil
		}
		return 0, nil
	}
	return 0, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("comparison not supported for kind %d", a.Kind)}
}

func cmpResult(op string, cmp int) bool {
	switch op {
	case "=":
		return cmp == 0
	case "<>", "!=":
		return cmp != 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	}
	return false
}

// evalAnd / evalOr implement Kleene three-valued logic.
func evalAnd(a, b Datum) Datum {
	if !a.IsNull() && a.Kind == KindBool && !a.Bool {
		return Datum{Kind: KindBool, Bool: false}
	}
	if !b.IsNull() && b.Kind == KindBool && !b.Bool {
		return Datum{Kind: KindBool, Bool: false}
	}
	if a.IsNull() || b.IsNull() {
		return NullDatum
	}
	return Datum{Kind: KindBool, Bool: a.Bool && b.Bool}
}

func evalOr(a, b Datum) Datum {
	if !a.IsNull() && a.Kind == KindBool && a.Bool {
		return Datum{Kind: KindBool, Bool: true}
	}
	if !b.IsNull() && b.Kind == KindBool && b.Bool {
		return Datum{Kind: KindBool, Bool: true}
	}
	if a.IsNull() || b.IsNull() {
		return NullDatum
	}
	return Datum{Kind: KindBool, Bool: a.Bool || b.Bool}
}

// evalFuncCall resolves a function name against the in-tree registry.
// v0 is small: current_timestamp / now / current_date are the only
// no-arg time functions pgbench needs.
func evalFuncCall(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	name := strings.ToLower(x.Name)
	switch name {
	case "current_timestamp", "now", "transaction_timestamp", "statement_timestamp":
		return Datum{Kind: KindTime, Time: ctx.Now}, nil
	case "current_date":
		t := ctx.Now
		return Datum{Kind: KindTime, Time: t.Truncate(24 * 60 * 60 * 1e9)}, nil
	}
	return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: fmt.Sprintf("function %s does not exist", x.Name)}
}
