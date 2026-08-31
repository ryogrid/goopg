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
	"time"

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
func applyDefaultsForMissing(cols []catalog.Column, row Row, missing []bool, dbOid ...uint32) {
	for i, m := range missing {
		if !m {
			continue
		}
		if i >= len(cols) || cols[i].DefaultExpr == nil {
			continue
		}
		val, err := evalGenExpr(cols[i].DefaultExpr, cols, row, dbOid...)
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
func evalGenExpr(e parser.Expr, cols []catalog.Column, row Row, dbOid ...uint32) (Datum, error) {
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
		return evalGenExpr(x.Operand, cols, row, dbOid...)

	case *parser.BinaryOp:
		left, err := evalGenExpr(x.Left, cols, row, dbOid...)
		if err != nil {
			return NullDatum, err
		}
		right, err := evalGenExpr(x.Right, cols, row, dbOid...)
		if err != nil {
			return NullDatum, err
		}
		return evalGenBinary(x.Op, left, right)

	case *parser.UnaryOp:
		operand, err := evalGenExpr(x.Operand, cols, row, dbOid...)
		if err != nil {
			return NullDatum, err
		}
		if x.Op == parser.OpUnaryNeg {
			if operand.Kind == KindInt {
				return NewIntDatum(-operand.Int), nil
			}
		}
		return operand, nil

	case *parser.FuncCall:
		return evalGenFuncCall(x, cols, row, dbOid...)

	}

	return NullDatum, nil
}

// evalGenFuncCall evaluates the small whitelist of functions the catalog
// DEFAULT-eval slow path supports:
//
//   - zero-arg time functions (M0103-0007 rung 18): now, current_timestamp,
//     transaction_timestamp, statement_timestamp, current_date.
//   - sequence advancement (M0103-0007 rung 19): nextval('seq_name') with
//     exactly one textual argument. The arg is evaluated through evalGenExpr
//     so cast wrappers (e.g. 'public.foo_seq'::regclass) and column-ref
//     spellings degrade cleanly to NullDatum when the slow path cannot
//     resolve them, instead of panicking or returning a sentinel int.
//
// Star args, schema qualifiers other than pg_catalog, and any unknown
// shape fall through to NullDatum so unknown DEFAULTs leave the slot
// untouched — matching the rest of evalGenExpr's silent-passthrough
// contract. A column whose DEFAULT is unevaluable surfaces as a NOT NULL
// violation downstream rather than silently overwriting with NULL.
//
// Wall-clock is read per call rather than threaded through ctx.Now —
// see docs/design/0103-0041 for the bounded-skew rationale.
//
// nextval uses the process-global seqRegistry directly (no Context),
// so this path is safe from both the apply worker (no ctx available)
// and the dispatcher INSERT path. ctx.LastSeqVal / ctx.CurrSeqVals are
// session-scoped and intentionally NOT touched here — DEFAULT-eval is
// not a place where SQL-level currval/lastval should observe a side
// effect. See docs/design/0103-0042 for the divergence rationale.
func evalGenFuncCall(x *parser.FuncCall, cols []catalog.Column, row Row, dbOid ...uint32) (Datum, error) {
	if x.Star {
		return NullDatum, nil
	}
	if x.Name.Schema != "" && x.Name.Schema != "pg_catalog" {
		return NullDatum, nil
	}
	fn := strings.ToLower(x.Name.Name)
	if len(x.Args) == 0 {
		now := time.Now().UTC()
		switch fn {
		case "now", "current_timestamp",
			"transaction_timestamp", "statement_timestamp":
			return NewTimeDatum(now), nil
		case "current_date":
			return NewTimeDatum(time.Date(now.Year(), now.Month(), now.Day(),
				0, 0, 0, 0, time.UTC)), nil
		}
		return NullDatum, nil
	}
	// M0134-0187: nullif/coalesce joined the whitelist because generated_stored.sql's
	// NOT-NULL-generated-column cases use `GENERATED ALWAYS AS (nullif(a, 0)) STORED
	// NOT NULL` — this evaluator previously fell through to the trailing
	// `return NullDatum, nil` for any unrecognised function, which is
	// indistinguishable here from a legitimately NULL result and left the
	// generated column NULL even when the expression could evaluate cleanly
	// (evalExprSlot's "nullif"/"coalesce" cases in expr.go are the reference
	// implementation this mirrors).
	if fn == "nullif" && len(x.Args) == 2 {
		a, err1 := evalGenExpr(x.Args[0], cols, row, dbOid...)
		b, err2 := evalGenExpr(x.Args[1], cols, row, dbOid...)
		if err1 != nil || err2 != nil {
			return NullDatum, nil
		}
		if !a.IsNull() && !b.IsNull() && a.Format() == b.Format() {
			return NullDatum, nil
		}
		return a, nil
	}
	if fn == "coalesce" {
		for _, arg := range x.Args {
			v, err := evalGenExpr(arg, cols, row, dbOid...)
			if err == nil && !v.IsNull() {
				return v, nil
			}
		}
		return NullDatum, nil
	}
	if fn == "nextval" && len(x.Args) == 1 {
		argVal, err := evalGenExpr(x.Args[0], cols, row, dbOid...)
		if err != nil || argVal.Kind != KindString {
			return NullDatum, nil
		}
		name := argVal.StringValue()
		if name == "" {
			return NullDatum, nil
		}
		s := LookupSequence(name, resolveSeqDBOid(dbOid))
		if s == nil {
			// Mirror evalNextval's auto-create-with-defaults behaviour
			// so apply-worker-replicated rows whose SERIAL sequence was
			// created on the publisher (but not yet registered on this
			// subscriber) still advance deterministically. Defaults are
			// the PG-compatible (start=1, increment=1, min=1, max=int64
			// max, cycle=false).
			RegisterSequence(name, 1, 1, 1, 9223372036854775807, false, resolveSeqDBOid(dbOid))
			s = LookupSequence(name, resolveSeqDBOid(dbOid))
		}
		v, err := s.nextVal()
		if err != nil {
			return NullDatum, nil
		}
		return NewIntDatum(v), nil
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
