package pgnodes

// resolver_expr.go — M0123-S2 (first sub-slice): resolve a goopg default-
// expression AST (parser.Expr) into the canonical pg_node_tree scalar IR so a
// real PG18 standby can EVALUATE the stored expression. This is the *forward*
// direction that pairs with S0's OID indexes (catalog.LookupOperatorForNode)
// and the S1 codec (Out/Read).
//
// Scope of THIS sub-slice: literal Consts (int4/int8/text), a unary minus
// folded onto an integer literal (PG's doNegate + make_const), and binary
// operators (OpExpr) whose operand types forward-resolve to a built-in
// pg_operator row. FuncExpr resolution is deferred: it needs a leaf-package
// pg_proc return-type map (funcresulttype) that S0 did not generate — tracked
// in the deferral ledger. Any expression outside this subset makes ResolveExpr
// return ErrUnsupported so the writer degrades to storing SQL text
// (all-or-nothing; see unsupported.go and 02e §3's graceful-degradation
// invariant — never partial-emit).
//
// Provenance for the coercion/typing decisions: PostgreSQL types an integer
// literal by its own magnitude (make_const in parse_node.c: int4 if it fits,
// else int8), folds `- <literal>` into a single negative Const (doNegate in
// gram.y), and coerces a bare string literal (type "unknown") to the context
// type — here only the text case is supported.

import (
	"errors"
	"math"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// ErrUnsupported is returned by ResolveExpr when the expression contains any
// shape outside the canonical scalar subset. Callers treat it as "store SQL
// text instead" — it is never a hard failure.
var ErrUnsupported = errors.New("pgnodes: expression outside canonical scalar subset")

// intoInt4Max is PG's boundary for typing an integer literal: a value whose
// magnitude fits a signed int4 is typed int4, otherwise int8. Mirrors
// make_const's `if (val >= INT_MIN && val <= INT_MAX)` for the already-signed
// value (after any doNegate fold).
func fitsInt4(v int64) bool { return v >= math.MinInt32 && v <= math.MaxInt32 }

// ResolveExpr converts a column-DEFAULT (or statistics-expression) AST into the
// canonical pg_node_tree scalar IR. targetType is the OID of the type the
// expression is stored for (the column type for a DEFAULT); 0 means "unknown /
// any" and is used when recursing into operator/function operands. It only
// affects a bare string literal, which PG coerces from "unknown" to the context
// type. Returns ErrUnsupported for anything outside the supported subset.
func ResolveExpr(e parser.Expr, targetType uint32) (Node, error) {
	n, _, err := resolve(e, targetType)
	return n, err
}

// resolve returns the IR node AND its result type OID (needed to forward-resolve
// an enclosing operator/function). The returned type is 0 only for a Const whose
// type is genuinely unknown, which cannot happen here (every leaf resolves to a
// concrete OID or errors).
func resolve(e parser.Expr, expected uint32) (Node, uint32, error) {
	switch v := e.(type) {
	case *parser.IntegerConst:
		return resolveIntLiteral(v.Value, expected)

	case *parser.StringConst:
		// A bare string literal is PG type "unknown"; in a text context it is
		// text. Only the text (or unknown/any) context is supported here.
		if expected == OidText || expected == 0 {
			return NewTextConst(v.Value), OidText, nil
		}
		return nil, 0, ErrUnsupported

	case *parser.UnaryOp:
		switch v.Op {
		case parser.OpUnaryPos:
			return resolve(v.Operand, expected)
		case parser.OpUnaryNeg:
			// PG folds `- <int literal>` into one negative Const (doNegate).
			if lit, ok := v.Operand.(*parser.IntegerConst); ok {
				return resolveIntLiteral(-lit.Value, expected)
			}
			return nil, 0, ErrUnsupported
		default:
			return nil, 0, ErrUnsupported
		}

	case *parser.BinaryOp:
		return resolveBinaryOp(v)

	default:
		return nil, 0, ErrUnsupported
	}
}

// resolveIntLiteral types a (possibly already-negated) integer value the way PG
// make_const does — int4 when it fits, else int8 — and honours a widening int8
// context (DEFAULT 5 on a bigint column stores an int8 Const).
func resolveIntLiteral(v int64, expected uint32) (Node, uint32, error) {
	if expected == OidInt8 || !fitsInt4(v) {
		return NewInt8Const(v), OidInt8, nil
	}
	// expected is int4, unknown, or a compatible context: emit int4.
	return NewInt4Const(int32(v)), OidInt4, nil
}

// resolveBinaryOp resolves a two-operand operator to an OpExpr by forward-
// resolving both operands, then looking the operator up by spelling + operand
// type OIDs (S0's catalog.LookupOperatorForNode). Collation follows PG: the
// only collatable type this subset emits is text, so inputcollid/opcollid are
// DEFAULT_COLLATION_OID (100) when a text operand/result is involved, else 0.
func resolveBinaryOp(b *parser.BinaryOp) (Node, uint32, error) {
	lNode, lType, err := resolve(b.Left, 0)
	if err != nil {
		return nil, 0, err
	}
	rNode, rType, err := resolve(b.Right, 0)
	if err != nil {
		return nil, 0, err
	}
	spelling := b.Op.String()
	op, ok := catalog.LookupOperatorForNode(spelling, lType, rType)
	if !ok {
		return nil, 0, ErrUnsupported
	}
	inputcollid := uint32(0)
	if isCollatable(lType) || isCollatable(rType) {
		inputcollid = DefaultCollationOid
	}
	opcollid := uint32(0)
	if isCollatable(op.ResultType) {
		opcollid = DefaultCollationOid
	}
	return &OpExpr{
		Opno:         op.OID,
		Opfuncid:     op.Code,
		Opresulttype: op.ResultType,
		Opretset:     false,
		Opcollid:     opcollid,
		Inputcollid:  inputcollid,
		Args:         []Node{lNode, rNode},
		Location:     -1,
	}, op.ResultType, nil
}

// isCollatable reports whether a type OID carries a collation in the subset the
// resolver emits. Only text is collatable here; varchar/bpchar/name coercions
// are not part of this sub-slice.
func isCollatable(oid uint32) bool { return oid == OidText }
