package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// ExprResultType resolves the STATIC result type of a resolved planner Expr —
// the type PostgreSQL's parse analysis would record in the node's
// `exprType()` (src/backend/nodes/nodeFuncs.c). It returns ok=false when the
// type cannot be resolved with confidence; callers must then decline whatever
// type-dependent work they wanted to do rather than guess.
//
// Why this exists (M0119-0006): an *expression* index key column
// (`CREATE INDEX ON t ((lower(a)))`) has no catalog column to consult
// (`catalog.Index.Columns[i] == ""`), so every consumer that needs the key's
// type — the B-tree key decode behind amcheck's opclass comparator, the
// expression-key encoder's kind assumption — had to decline expression columns
// outright. Resolving the type statically removes that blanket bail-out.
//
// Faithfulness: for function calls and operators the type is not guessed from
// a hand-written name table but read out of the PG 18 catalog seed goopg
// already ships — pg_proc (`catalog.LookupProcForNode` → `ProcResultType`) and
// pg_operator (`catalog.LookupOperatorForNode` → `OperatorEntry.ResultType`).
// A resolution therefore agrees with what a real PG backend would compute for
// the same overload, including for overloads goopg's evaluator implements
// loosely.
//
// This is deliberately a separate, stricter entry point from inferExprType,
// which answers "what type should lag()/lead() report" and defaults to text for
// everything it does not recognize. A text default is exactly what a key
// decoder must NOT be handed, so the two must not be merged.
func ExprResultType(e Expr) (catalog.Type, bool) {
	switch x := e.(type) {
	case *ColumnRef:
		if x.Type.Name == "" {
			return catalog.Type{}, false
		}
		return x.Type, true
	case *OuterColumnRef:
		if x.Type.Name == "" {
			return catalog.Type{}, false
		}
		return x.Type, true
	case *CastExpr:
		if x.TargetType == "" {
			return catalog.Type{}, false
		}
		return catalog.Type{Name: strings.ToLower(x.TargetType)}, true
	case *CollateExpr:
		// COLLATE changes the collation, never the type (exprType() recurses
		// into CollateExpr's arg the same way).
		return ExprResultType(x.Operand)
	case *IntegerConst:
		// PG's make_const: an integer literal that fits in int32 is int4,
		// otherwise int8 (numeric only beyond int64, which the lexer would
		// have produced a NumericConst for).
		if x.Value >= -2147483648 && x.Value <= 2147483647 {
			return catalog.Type{Name: "int4"}, true
		}
		return catalog.Type{Name: "int8"}, true
	case *NumericConst:
		return catalog.Type{Name: "numeric"}, true
	case *StringConst:
		// PG types a bare string literal UNKNOWN and lets coercion settle it;
		// once it reaches an index expression it has been resolved to text.
		return catalog.Type{Name: "text"}, true
	case *BooleanConst:
		return catalog.Type{Name: "bool"}, true
	case *TypedStringLit:
		if x.Type == "" {
			return catalog.Type{}, false
		}
		return catalog.Type{Name: strings.ToLower(x.Type)}, true
	case *IntervalLit:
		return catalog.Type{Name: "interval"}, true
	case *ExtractExpr:
		// PG 14+ returns numeric from EXTRACT (float8 before that).
		return catalog.Type{Name: "numeric"}, true
	case *IsNullExpr, *IsBoolExpr, *IsDistinctFromExpr, *InExpr, *ExistsExpr:
		return catalog.Type{Name: "bool"}, true
	case *CaseExpr:
		return caseExprResultType(x)
	case *UnaryOp:
		return unaryOpResultType(x)
	case *BinaryOp:
		return binaryOpResultType(x)
	case *FuncCall:
		return funcCallResultType(x)
	}
	return catalog.Type{}, false
}

// caseExprResultType takes the type of the first branch that resolves. PG
// computes the common supertype of every branch (select_common_type); an index
// expression whose branches disagree is rare enough that resolving on the first
// branch and letting a mismatch surface as a decline is the honest bound — see
// the deferral ledger row for CASE with heterogeneous branch types.
func caseExprResultType(x *CaseExpr) (catalog.Type, bool) {
	for _, w := range x.Whens {
		if _, isNull := w.Then.(*NullConst); isNull {
			continue
		}
		if t, ok := ExprResultType(w.Then); ok {
			return t, true
		}
	}
	if x.Else != nil {
		if _, isNull := x.Else.(*NullConst); !isNull {
			return ExprResultType(x.Else)
		}
	}
	return catalog.Type{}, false
}

func unaryOpResultType(x *UnaryOp) (catalog.Type, bool) {
	switch x.Op {
	case parser.OpNot:
		return catalog.Type{Name: "bool"}, true
	case parser.OpUnaryNeg, parser.OpUnaryPos:
		// Unary +/- preserve the operand's type (PG resolves them through
		// pg_operator's prefix rows, whose result type equals the operand's
		// for every numeric type).
		return ExprResultType(x.Operand)
	}
	return catalog.Type{}, false
}

// binaryOpResultType resolves through pg_operator: (spelling, left OID, right
// OID) → oprresult, exactly the lookup PG's oper() performs for an exact-match
// operator. The catalog answer is preferred over the planner's own ResultType
// annotation, which records the width goopg's evaluator happens to compute in
// (`int4 + int4` is annotated int8) rather than the width PG's parse analysis
// assigns; the annotation is the fallback when no seeded operator matches.
func binaryOpResultType(x *BinaryOp) (catalog.Type, bool) {
	lt, lok := ExprResultType(x.Left)
	rt, rok := ExprResultType(x.Right)
	if lok && rok {
		lOID, lresolved := exactTypeOID(lt)
		rOID, rresolved := exactTypeOID(rt)
		if lresolved && rresolved {
			if entry, ok := catalog.LookupOperatorForNode(x.Op.String(), lOID, rOID); ok {
				if name := catalog.OIDToTypeName(entry.ResultType); name != "" {
					return catalog.Type{Name: name}, true
				}
			}
		}
	}
	if x.ResultType != "" {
		return catalog.Type{Name: strings.ToLower(x.ResultType)}, true
	}
	return catalog.Type{}, false
}

// funcCallResultType resolves through pg_proc: (name, argument type OIDs) →
// prorettype. A user-defined function carries its declared return type on the
// node already (FuncCall.ReturnType), which takes precedence — it is not in the
// built-in seed.
func funcCallResultType(x *FuncCall) (catalog.Type, bool) {
	if x.ReturnType != "" {
		return catalog.Type{Name: strings.ToLower(x.ReturnType)}, true
	}
	if x.Star || x.Variadic {
		// `count(*)` and VARIADIC expansion do not have a literal argument
		// list to key the pg_proc lookup on.
		return catalog.Type{}, false
	}
	argOIDs := make([]uint32, 0, len(x.Args))
	for _, a := range x.Args {
		at, ok := ExprResultType(a)
		if !ok {
			return catalog.Type{}, false
		}
		oid, ok := exactTypeOID(at)
		if !ok {
			return catalog.Type{}, false
		}
		argOIDs = append(argOIDs, oid)
	}
	funcid, ok := catalog.LookupProcForNode(strings.ToLower(x.Name), argOIDs)
	if !ok {
		return catalog.Type{}, false
	}
	retOID, ok := catalog.ProcResultType(funcid)
	if !ok {
		return catalog.Type{}, false
	}
	name := catalog.OIDToTypeName(retOID)
	if name == "" {
		return catalog.Type{}, false
	}
	return catalog.Type{Name: name}, true
}

// exactTypeOID maps a type name to its OID, accepting only an EXACT
// resolution. catalog.TypeNameToOID falls back to text(25) for every name it
// does not know (user-defined enums, domains, composite types), which would
// silently resolve `lower(<some enum>)` as `lower(text)` and hand back a
// confidently wrong answer. Anything that lands on text without literally
// spelling "text" is therefore treated as unresolvable — the same
// distrust-the-fallback guard buildNodeProcIndex applies when it builds the
// forward index. SQL-standard spellings ("integer", "boolean", …) still
// resolve, since TypeNameToOID maps those aliases explicitly.
func exactTypeOID(t catalog.Type) (uint32, bool) {
	if t.IsArray || t.Name == "" {
		return 0, false
	}
	name := strings.ToLower(t.Name)
	oid := catalog.TypeNameToOID(name)
	if oid == 0 {
		return 0, false
	}
	if oid == catalog.OIDText && name != "text" {
		return 0, false // the fallback fired, not a real resolution
	}
	return oid, true
}
