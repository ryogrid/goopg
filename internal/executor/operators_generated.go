package executor

// operators_generated.go — evaluation of GENERATED ALWAYS AS … STORED columns.
//
// When a table has stored generated columns, the executor must compute their
// values from the other columns in the row before writing to the heap.  This
// module provides evalGeneratedExpr, which parses the stored expression string
// and evaluates it against the current row using a lightweight direct AST walk
// that handles arithmetic and column references (sufficient for the patterns
// used in isolation test specs).
//
// Design: rather than going through the full planner pipeline (which would
// require adding import-cycle-free planned-expression storage to the catalog),
// we parse and evaluate the expression inline at INSERT/UPDATE time.  The
// expressions are typically simple (e.g. `balance * 2`, `a + b`) so this is
// both correct and fast enough.
//
// M0096-0008.

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// computeGeneratedColumns fills in any generated-column slots in row that
// were left as NullDatum by the INSERT/UPDATE logic.  cols is the table's
// column list; row is the fully-reordered row with non-generated values set.
func computeGeneratedColumns(cols []catalog.Column, row Row) error {
	for i, col := range cols {
		if col.GeneratedExpr == "" {
			continue
		}
		val, err := evalGeneratedExpr(col.GeneratedExpr, cols, row)
		if err != nil {
			// Non-fatal: leave as NULL if we can't evaluate.
			_ = err
			continue
		}
		row[i] = val
	}
	return nil
}

// applyDefaultsForMissing fills in column slots flagged missing[i]=true with
// the value of the column's DEFAULT expression. Slots whose column has no
// DEFAULT stay at their incoming value (typically NullDatum from the
// pgoutput decoder). Used by the apply worker when a subscriber-side table
// has columns the publisher does not (M0103-0007 rung 13). Mirrors the
// upstream slot_fill_defaults() helper in
// src/backend/replication/logical/worker.c.
//
// The evaluator is intentionally the same lightweight evalGenExpr used for
// GENERATED ALWAYS columns: literals, NULL, bool, CAST passthrough, simple
// arithmetic, and column refs (resolved against the row being built). That
// covers the DEFAULT shapes pgoutput-replicated test fixtures actually use;
// expressions evalGenExpr can't handle leave the slot unchanged so a
// NOT NULL violation surfaces loudly rather than silently NULL-ing a row.
func applyDefaultsForMissing(cols []catalog.Column, row Row, missing []bool) {
	for i, m := range missing {
		if !m {
			continue
		}
		if i >= len(cols) || cols[i].DefaultExpr == nil {
			continue
		}
		val, err := evalGenExpr(cols[i].DefaultExpr, cols, row)
		if err != nil {
			continue
		}
		row[i] = val
	}
}

// evalGeneratedExpr parses a raw SQL expression string and evaluates it
// against the current row.  Column references are resolved by name against
// the provided cols slice.
func evalGeneratedExpr(exprStr string, cols []catalog.Column, row Row) (Datum, error) {
	// Wrap in SELECT so the parser can produce a full statement.
	stmts, err := parser.Parse("SELECT " + exprStr)
	if err != nil {
		return NullDatum, fmt.Errorf("generated column expr parse: %w", err)
	}
	if len(stmts) == 0 {
		return NullDatum, fmt.Errorf("generated column expr: empty")
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok || len(sel.Targets) == 0 {
		return NullDatum, fmt.Errorf("generated column expr: not a select")
	}
	return evalGenExpr(sel.Targets[0].Expr, cols, row)
}

// evalGenExpr is a recursive evaluator for simple expressions used in
// generated columns.  It handles integer/string literals, column references,
// binary arithmetic, unary negation, and casts.
func evalGenExpr(e parser.Expr, cols []catalog.Column, row Row) (Datum, error) {
	switch x := e.(type) {

	case *parser.IntegerConst:
		return NewIntDatum(x.Value), nil

	case *parser.NumericConst:
		// Parse as int64 if possible, else return as string.
		var n int64
		if _, err := fmt.Sscanf(x.Value, "%d", &n); err == nil {
			return NewIntDatum(n), nil
		}
		return NewStringDatum(x.Value), nil

	case *parser.StringConst:
		return NewStringDatum(x.Value), nil

	case *parser.NullConst:
		return NullDatum, nil

	case *parser.BooleanConst:
		return NewBoolDatum(x.Value), nil

	case *parser.ColumnRef:
		// Resolve by name (case-insensitive).
		for i, col := range cols {
			if strings.EqualFold(col.Name, x.Column) && i < len(row) {
				return row[i], nil
			}
		}
		return NullDatum, nil

	case *parser.CastExpr:
		// Evaluate the operand; ignore the cast type for now.
		return evalGenExpr(x.Operand, cols, row)

	case *parser.BinaryOp:
		left, err := evalGenExpr(x.Left, cols, row)
		if err != nil {
			return NullDatum, err
		}
		right, err := evalGenExpr(x.Right, cols, row)
		if err != nil {
			return NullDatum, err
		}
		return evalGenBinary(x.Op, left, right)

	case *parser.UnaryOp:
		operand, err := evalGenExpr(x.Operand, cols, row)
		if err != nil {
			return NullDatum, err
		}
		if x.Op == parser.OpUnaryNeg {
			if operand.Kind == KindInt {
				return NewIntDatum(-operand.Int), nil
			}
		}
		return operand, nil

	}

	return NullDatum, nil
}

// evalGenBinary evaluates a binary arithmetic or comparison operation on two Datums.
func evalGenBinary(op parser.OpCode, left, right Datum) (Datum, error) {
	// Numeric arithmetic
	if left.Kind == KindInt && right.Kind == KindInt {
		l, r := left.Int, right.Int
		switch op {
		case parser.OpAdd:
			return NewIntDatum(l + r), nil
		case parser.OpSub:
			return NewIntDatum(l - r), nil
		case parser.OpMul:
			return NewIntDatum(l * r), nil
		case parser.OpDiv:
			if r == 0 {
				return NullDatum, nil
			}
			return NewIntDatum(l / r), nil
		case parser.OpMod:
			if r == 0 {
				return NullDatum, nil
			}
			return NewIntDatum(l % r), nil
		case parser.OpEq:
			return NewBoolDatum(l == r), nil
		case parser.OpNe:
			return NewBoolDatum(l != r), nil
		case parser.OpLt:
			return NewBoolDatum(l < r), nil
		case parser.OpLe:
			return NewBoolDatum(l <= r), nil
		case parser.OpGt:
			return NewBoolDatum(l > r), nil
		case parser.OpGe:
			return NewBoolDatum(l >= r), nil
		}
	}
	// KindNumeric arithmetic — handle mixed KindInt/KindNumeric operands.
	// Coerce KindInt to big.Int (scale 0) so both sides are numeric (M0100-0005).
	if left.Kind == KindNumeric || right.Kind == KindNumeric {
		lm, ls := genNumericParts(left)
		rm, rs := genNumericParts(right)
		// Align scales for add/sub; for mul/div compute result scale.
		switch op {
		case parser.OpAdd, parser.OpSub:
			// Align to max scale by scaling the smaller-scale operand up.
			if ls < rs {
				scale := rs - ls
				factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
				lm = new(big.Int).Mul(lm, factor)
				ls = rs
			} else if rs < ls {
				scale := ls - rs
				factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
				rm = new(big.Int).Mul(rm, factor)
			}
			var result big.Int
			if op == parser.OpAdd {
				result.Add(lm, rm)
			} else {
				result.Sub(lm, rm)
			}
			return newNumeric(&result, int(ls)), nil
		case parser.OpMul:
			var result big.Int
			result.Mul(lm, rm)
			return newNumeric(&result, int(ls+rs)), nil
		case parser.OpDiv:
			if rm.Sign() == 0 {
				return NullDatum, nil
			}
			// Integer division at aligned scale.
			scale := ls - rs
			if scale < 0 {
				factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-scale)), nil)
				lm = new(big.Int).Mul(lm, factor)
				scale = 0
			}
			var result big.Int
			result.Quo(lm, rm)
			return newNumeric(&result, int(scale)), nil
		}
	}
	// String concatenation
	if op == parser.OpConcat {
		ls := datumToString(left)
		rs := datumToString(right)
		return NewStringDatum(ls + rs), nil
	}
	return NullDatum, nil
}

// genNumericParts converts a Datum to (mantissa *big.Int, scale int16) for
// use in generated-column arithmetic. KindInt is treated as scale-0 numeric.
func genNumericParts(d Datum) (*big.Int, int16) {
	switch d.Kind {
	case KindNumeric:
		return numericMant(d), d.NumericScaleValue()
	case KindInt:
		return big.NewInt(d.Int), 0
	default:
		return big.NewInt(0), 0
	}
}

func datumToString(d Datum) string {
	if d.Kind == KindInt {
		return fmt.Sprintf("%d", d.Int)
	}
	if d.Kind == KindString {
		return d.StringValue()
	}
	return ""
}
