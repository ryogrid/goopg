package pgnodes

// rebuild.go — M0123-S2: the inverse of resolver_expr.go. On startup the
// per-database heap reload reads a stored pg_attrdef.adbin (a canonical
// pg_node_tree, discriminated by a leading '{') with Read, then Rebuild turns
// the IR back into a goopg parser.Expr so goopg can re-evaluate the DEFAULT the
// same way it did before the restart. (SQL-text adbin values keep going through
// parser.ParseExpr; the discriminator lives in the reload, not here.)
//
// Rebuild handles exactly the node shapes resolver_expr.go emits this sub-slice
// (int4/int8/text Const, negative-int Const → unary minus, OpExpr → BinaryOp);
// anything else is a reader/producer mismatch and returns an error so the
// reload can surface it rather than silently drop a default.

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Rebuild converts a canonical scalar IR node back into a goopg default-
// expression AST. It is the reload-time inverse of ResolveExpr.
func Rebuild(n Node) (parser.Expr, error) {
	switch v := n.(type) {
	case *Const:
		return rebuildConst(v)
	case *OpExpr:
		return rebuildOpExpr(v)
	case *FuncExpr:
		return rebuildFuncExpr(v)
	case *BoolExpr:
		return rebuildBoolExpr(v)
	case *NullTest:
		return rebuildNullTest(v)
	default:
		return nil, fmt.Errorf("pgnodes: Rebuild: unsupported node %T", n)
	}
}

// rebuildBoolExpr reconstructs an AND/OR/NOT BOOLEXPR into goopg's AST. goopg has
// no n-ary boolean node, so an n-arg AND/OR folds back into a LEFT-nested chain
// of BinaryOps (`[a b c]` → `(a AND b) AND c`) — the exact tree PG's makeAndExpr
// flattened on the way in, so resolve→Rebuild→re-resolve is a fixed point. NOT
// is a single-operand UnaryOp.
func rebuildBoolExpr(b *BoolExpr) (parser.Expr, error) {
	return rebuildBoolExprWith(b, Rebuild)
}

// rebuildBoolExprWith rebuilds a BOOLEXPR through the injected recursion `rec`.
// NOT -> a single UnaryOp{OpNot}; AND/OR -> a left-nested chain of BinaryOps
// (the inverse of the forward flattening: an n-arg BoolExpr becomes
// `((a op b) op c) …`). The query-scoped reload threads
// viewRebuildScope.rebuildExpr so a bool operand may itself be a column Var.
func rebuildBoolExprWith(b *BoolExpr, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	switch b.Boolop {
	case BoolExprNot:
		if len(b.Args) != 1 {
			return nil, fmt.Errorf("pgnodes: Rebuild: NOT BoolExpr with %d args (want 1)", len(b.Args))
		}
		operand, err := rec(b.Args[0])
		if err != nil {
			return nil, err
		}
		return &parser.UnaryOp{Op: parser.OpNot, Operand: operand}, nil
	case BoolExprAnd, BoolExprOr:
		op := parser.OpAnd
		if b.Boolop == BoolExprOr {
			op = parser.OpOr
		}
		if len(b.Args) < 2 {
			return nil, fmt.Errorf("pgnodes: Rebuild: AND/OR BoolExpr with %d args (want >=2)", len(b.Args))
		}
		acc, err := rec(b.Args[0])
		if err != nil {
			return nil, err
		}
		for _, a := range b.Args[1:] {
			r, err := rec(a)
			if err != nil {
				return nil, err
			}
			acc = &parser.BinaryOp{Op: op, Left: acc, Right: r}
		}
		return acc, nil
	default:
		return nil, fmt.Errorf("pgnodes: Rebuild: bad boolop %d", b.Boolop)
	}
}

// rebuildNullTest reconstructs `x IS [NOT] NULL`.
func rebuildNullTest(nt *NullTest) (parser.Expr, error) {
	return rebuildNullTestWith(nt, Rebuild)
}

// rebuildNullTestWith rebuilds a NULLTEST through the injected recursion `rec`
// (so the argument may be a column Var in the view-reload path).
func rebuildNullTestWith(nt *NullTest, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	operand, err := rec(nt.Arg)
	if err != nil {
		return nil, err
	}
	return &parser.IsNullExpr{Operand: operand, Negated: nt.NullTestType == IsNotNull}, nil
}

// rebuildFuncExpr reconstructs a plain function call whose arguments are scalar
// nodes (the S2 DEFAULT-expression scope, where Rebuild is the recursion).
func rebuildFuncExpr(f *FuncExpr) (parser.Expr, error) {
	return rebuildFuncExprWith(f, Rebuild)
}

// rebuildFuncExprWith reconstructs a plain function call, rebuilding each
// argument through the caller-supplied recursion. The funcid is reverse-mapped
// to its proname (catalog.RegprocName, the same PG18 seed the forward resolver
// used) and the call is emitted unqualified (proname only): goopg resolves
// built-ins by bare name, and the forward resolver accepts an empty schema, so
// resolve→Rebuild→re-resolve is a fixed point. The recursion parameter lets the
// query-tree scope (rebuild_query.go) reuse this to rebuild FuncExprs whose
// arguments may be column Vars, not only scalars.
func rebuildFuncExprWith(f *FuncExpr, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	name, ok := catalog.RegprocName(f.Funcid)
	if !ok {
		return nil, fmt.Errorf("pgnodes: Rebuild: unknown function OID %d", f.Funcid)
	}
	args := make([]parser.Expr, 0, len(f.Args))
	for _, a := range f.Args {
		arg, err := rec(a)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
	}
	return &parser.FuncCall{Name: parser.ObjectName{Name: name}, Args: args}, nil
}

// rebuildConst reconstructs a literal. int4/int8 datums decode from the 8-byte
// by-value word (a negative value becomes UnaryOp{-, |v|}, matching how the
// parser tags `-N`); a text datum decodes from its varlena.
func rebuildConst(c *Const) (parser.Expr, error) {
	if c.ConstIsNull {
		return nil, fmt.Errorf("pgnodes: Rebuild: NULL Const has no goopg AST form")
	}
	switch c.ConstType {
	case OidInt4, OidInt8:
		v := int64FromByvalWord(c.Datum)
		if v < 0 {
			return &parser.UnaryOp{Op: parser.OpUnaryNeg, Operand: &parser.IntegerConst{Value: -v}}, nil
		}
		return &parser.IntegerConst{Value: v}, nil
	case OidBool:
		// The by-value word is 0 (false) or 1 (true); see datum.go:NewBoolConst.
		return &parser.BooleanConst{Value: int64FromByvalWord(c.Datum) != 0}, nil
	case OidText:
		return &parser.StringConst{Value: textFromVarlena(c.Datum)}, nil
	case OidNumeric:
		// Decode the packed NumericData back to its canonical decimal text
		// (preserving dscale trailing zeros so a re-resolve is a fixed point);
		// a negative value becomes UnaryOp{-, |v|}, matching doNegate.
		v, err := decodeNumericVar(c.Datum)
		if err != nil {
			return nil, fmt.Errorf("pgnodes: Rebuild: numeric Const: %w", err)
		}
		lit := &parser.NumericConst{Value: v.text()}
		if v.negative {
			return &parser.UnaryOp{Op: parser.OpUnaryNeg, Operand: lit}, nil
		}
		return lit, nil
	default:
		return nil, fmt.Errorf("pgnodes: Rebuild: unsupported Const type OID %d", c.ConstType)
	}
}

// rebuildOpExpr reconstructs a binary operator whose operands are scalar nodes
// (the S2 DEFAULT-expression scope, where Rebuild is the recursion).
func rebuildOpExpr(o *OpExpr) (parser.Expr, error) {
	return rebuildOpExprWith(o, Rebuild)
}

// rebuildOpExprWith reconstructs a binary operator, rebuilding each operand
// through the caller-supplied recursion. The opno is reverse-mapped to its
// spelling (from the same pg_operator seed S0 resolved forward), then to a
// parser OpCode. The recursion parameter lets the query-tree scope
// (rebuild_query.go) reuse this to rebuild OpExprs whose operands may be column
// Vars, not only scalars.
func rebuildOpExprWith(o *OpExpr, rec func(Node) (parser.Expr, error)) (parser.Expr, error) {
	if len(o.Args) != 2 {
		return nil, fmt.Errorf("pgnodes: Rebuild: OpExpr with %d args (want 2)", len(o.Args))
	}
	name, ok := operatorNameForOID(o.Opno)
	if !ok {
		return nil, fmt.Errorf("pgnodes: Rebuild: unknown operator OID %d", o.Opno)
	}
	op := parser.ParseBinaryOp(name)
	if op == parser.OpUnknown {
		return nil, fmt.Errorf("pgnodes: Rebuild: operator %q has no goopg OpCode", name)
	}
	left, err := rec(o.Args[0])
	if err != nil {
		return nil, err
	}
	right, err := rec(o.Args[1])
	if err != nil {
		return nil, err
	}
	return &parser.BinaryOp{Op: op, Left: left, Right: right}, nil
}

// int64FromByvalWord decodes the little-endian 8-byte Datum word (see
// datum.go:byvalWord) as a signed int64, undoing Int32GetDatum/Int64GetDatum's
// sign extension.
func int64FromByvalWord(b []byte) int64 {
	return int64(binary.LittleEndian.Uint64(b[:8]))
}

// textFromVarlena decodes the in-memory 4-byte-header varlena (see
// datum.go:textVarlena): VARSIZE = header>>2, string = bytes [4:VARSIZE].
func textFromVarlena(b []byte) string {
	total := binary.LittleEndian.Uint32(b[:4]) >> 2
	return string(b[4:total])
}

// operatorNameForOID reverse-maps a pg_operator OID to its spelling, lazily
// building the index from the same PG18 seed S0's forward index uses. Only
// binary operators are indexed (the resolver emits OpExpr for binary ops only).
var (
	operatorNameByOIDOnce sync.Once
	operatorNameByOID     map[uint32]string
)

func operatorNameForOID(oid uint32) (string, bool) {
	operatorNameByOIDOnce.Do(func() {
		entries := catalog.PGOperatorAllEntries()
		operatorNameByOID = make(map[uint32]string, len(entries))
		for _, e := range entries {
			if e.Kind != 'b' {
				continue
			}
			// pg_operator OID is unique; first write wins (all rows distinct OIDs).
			if _, dup := operatorNameByOID[e.OID]; !dup {
				operatorNameByOID[e.OID] = e.Name
			}
		}
	})
	name, ok := operatorNameByOID[oid]
	return name, ok
}
