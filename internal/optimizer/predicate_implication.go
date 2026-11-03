package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// provePartialIndexPredicate is the single-leaf specialization of PG's
// `operator_predicate_proof` (postgres/src/backend/optimizer/util/predtest.c),
// invoked from `check_index_predicates`
// (postgres/src/backend/optimizer/path/indxpath.c:3943) via
// `predicate_implied_by` (:4048).
//
// It proves ONLY the narrowest possible case: both `predicate` (the index's
// resolved WHERE clause) and `clause` (a restriction clause from the query)
// are `Var <op> Const` over the SAME column, with the SAME operator and an
// EQUAL constant datum. That is sufficient — and necessary for soundness — to
// conclude every row the index contains satisfies the query's restriction,
// because the two clauses are then literally the same predicate.
//
// Every other shape (AND/OR trees, function calls, casts,
// two-column comparisons, differing operators, differing constants) returns
// false — i.e., exactly today's decline. This is deliberate: a full
// `predicate_implied_by` port (CNF/DNF normalization, btree strategy-number
// implication tables, refute mode) is REFACTOR-tier and out of scope here
// (docs/design/m0134-0017-partial-index-predicate-implication.md). Widening
// this helper without that full port risks reopening the wrong-answer bug
// recorded in the deferral ledger (2026-08-07, `M0127 S7 gate /
// AI-20260806-232940-001,-002`): an UNSOUND "proof" that accepts a partial
// index whose predicate does not actually cover the query's rows silently
// drops rows from the result.
//
// M0148-0001c3 adds the two NullTest rules of
// predicate_implied_by_simple_clause (predtest.c), both sound by construction:
//
//   - a clause implies itself (`equal(predicate, clause)`) — here for the
//     NullTest shape, `x IS [NOT] NULL` against the same test on the same
//     column;
//   - `x IS NOT NULL` is implied by any clause STRICT for x
//     (clause_is_strict_for with allow_false): the clause cannot be true when
//     x is NULL, so every row it admits has a non-null x. Strictness is
//     proven only through the operators and casts nullStrictFor lists.
func provePartialIndexPredicate(predicate, clause Expr) bool {
	if predicate == nil || clause == nil {
		return false
	}
	if pn, isNT := predicate.(*IsNullExpr); isNT {
		if cn, cIsNT := clause.(*IsNullExpr); cIsNT && cn.Negated == pn.Negated && sameNamedColumn(pn.Operand, cn.Operand) {
			return true
		}
		if pn.Negated && isPlainColumn(pn.Operand) && nullStrictFor(clause, pn.Operand) {
			return true
		}
		return false
	}
	pCol, pOp, pLit, ok := asColOpConst(predicate)
	if !ok {
		return false
	}
	cCol, cOp, cLit, ok := asColOpConst(clause)
	if !ok {
		return false
	}
	if pCol.Name != cCol.Name || pOp != cOp {
		return false
	}
	cmp, err := litCompare(pLit, cLit)
	if err != nil {
		return false
	}
	return cmp == 0
}

// asColOpConst recognizes the `Var <op> Const` shape (in either clause
// ordering) and returns its parts. `const op col` is normalized to
// `col op const` by flipping the operator to its mirror (e.g. `9999 < col`
// becomes `col > 9999`). Every other shape — including a non-Const RHS such
// as a ParamRef, another ColumnRef, or a function call — returns ok=false,
// matching upstream's refusal to reason about anything but a plain Var vs. a
// plain Const.
func asColOpConst(e Expr) (col *ColumnRef, op parser.OpCode, lit literalValue, ok bool) {
	b, isBin := e.(*BinaryOp)
	if !isBin {
		return nil, 0, literalValue{}, false
	}
	if lc, lok := b.Left.(*ColumnRef); lok {
		if lv, lvok := toLiteralValue(b.Right); lvok {
			return lc, b.Op, lv, true
		}
		return nil, 0, literalValue{}, false
	}
	if rc, rok := b.Right.(*ColumnRef); rok {
		if lv, lvok := toLiteralValue(b.Left); lvok {
			flipped, flipOk := mirrorOpCode(b.Op)
			if !flipOk {
				return nil, 0, literalValue{}, false
			}
			return rc, flipped, lv, true
		}
	}
	return nil, 0, literalValue{}, false
}

// mirrorOpCode returns the operator that expresses `const op col` as
// `col op' const`, e.g. `9999 < col` <=> `col > 9999`. Returns ok=false for
// any operator this table does not cover (kept intentionally narrow — see
// provePartialIndexPredicate's soundness note).
func mirrorOpCode(op parser.OpCode) (parser.OpCode, bool) {
	switch op {
	case parser.OpEq, parser.OpNe:
		return op, true
	case parser.OpLt:
		return parser.OpGt, true
	case parser.OpGt:
		return parser.OpLt, true
	case parser.OpLe:
		return parser.OpGe, true
	case parser.OpGe:
		return parser.OpLe, true
	}
	return 0, false
}

// isPlainColumn reports whether e is a bare column reference — the only
// NullTest argument the proof reasons about (a row-valued `IS NOT NULL` does
// not act in the simple way the strictness rule needs, predtest.c).
func isPlainColumn(e Expr) bool {
	_, ok := e.(*ColumnRef)
	return ok
}

// sameNamedColumn reports whether a and b are column references to the same
// column. The index predicate is resolved against the table and the clause
// against the scan leaf, so the two are compared by name, as asColOpConst's
// callers do.
func sameNamedColumn(a, b Expr) bool {
	ac, ok := a.(*ColumnRef)
	if !ok {
		return false
	}
	bc, ok := b.(*ColumnRef)
	return ok && strings.EqualFold(ac.Name, bc.Name)
}

// nullStrictFor is clause_is_strict_for (predtest.c): clause yields NULL (or,
// at the top level, at least not TRUE) whenever col is NULL. The base case is
// the column itself; a strict operator or a cast is strict for col when one of
// its operands is. Only operators whose PG implementations are all strict are
// listed — comparisons, arithmetic, pattern matching — never AND/OR, IS
// DISTINCT FROM, `||` (array_append is not strict) or anything else; an
// unlisted shape is simply not proven.
func nullStrictFor(clause, col Expr) bool {
	if cr, ok := clause.(*ColumnRef); ok {
		return sameNamedColumn(cr, col)
	}
	if c, ok := clause.(*CastExpr); ok {
		return nullStrictFor(c.Operand, col)
	}
	b, ok := clause.(*BinaryOp)
	if !ok {
		return false
	}
	switch b.Op {
	case parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe,
		parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod,
		parser.OpLike, parser.OpNotLike, parser.OpILike, parser.OpNotILike,
		parser.OpRegexMatch, parser.OpRegexIMatch, parser.OpRegexNoMatch, parser.OpRegexINoMatch:
		return nullStrictFor(b.Left, col) || nullStrictFor(b.Right, col)
	}
	return false
}
