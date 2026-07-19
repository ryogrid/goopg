package pgnodes

import (
	"fmt"
	"strconv"
	"strings"
)

// Out serializes an IR node into the canonical PostgreSQL pg_node_tree text
// form — the exact bytes nodeToString / pg_attrdef.adbin produce. Field order
// per tag mirrors postgres/src/backend/nodes/outfuncs.c (and the generated
// outfuncs.funcs.c); see the inline provenance comments on each out<Tag>.
func Out(n Node) string {
	var sb strings.Builder
	outNode(&sb, n)
	return sb.String()
}

// outNode writes a single node wrapped in braces, or "<>" for a nil node —
// mirroring outNode() in outfuncs.c (a NULL node prints as "<>").
func outNode(sb *strings.Builder, n Node) {
	if n == nil {
		sb.WriteString("<>")
		return
	}
	sb.WriteByte('{')
	switch v := n.(type) {
	case *Const:
		outConst(sb, v)
	case *FuncExpr:
		outFuncExpr(sb, v)
	case *OpExpr:
		outOpExpr(sb, v)
	case *DistinctExpr:
		outDistinctExpr(sb, v)
	case *RelabelType:
		outRelabelType(sb, v)
	case *CoerceViaIO:
		outCoerceViaIO(sb, v)
	case *SQLValueFunction:
		outSQLValueFunction(sb, v)
	case *BoolExpr:
		outBoolExpr(sb, v)
	case *NullTest:
		outNullTest(sb, v)
	case *BooleanTest:
		outBooleanTest(sb, v)
	case *CaseExpr:
		outCaseExpr(sb, v)
	case *CaseWhen:
		outCaseWhen(sb, v)
	case *Query:
		outQuery(sb, v)
	case *RangeTblEntry:
		outRangeTblEntry(sb, v)
	case *RTEPermissionInfo:
		outRTEPermissionInfo(sb, v)
	case *FromExpr:
		outFromExpr(sb, v)
	case *RangeTblRef:
		outRangeTblRef(sb, v)
	case *TargetEntry:
		outTargetEntry(sb, v)
	case *Var:
		outVar(sb, v)
	case *Alias:
		outAlias(sb, v)
	default:
		panic(fmt.Sprintf("pgnodes: outNode: unsupported node type %T", n))
	}
	sb.WriteByte('}')
}

// --- WRITE_* field helpers (outfuncs.c macros) ---

func wInt(sb *strings.Builder, name string, v int32) {
	sb.WriteString(" :")
	sb.WriteString(name)
	sb.WriteByte(' ')
	sb.WriteString(strconv.FormatInt(int64(v), 10))
}

func wOid(sb *strings.Builder, name string, v uint32) {
	sb.WriteString(" :")
	sb.WriteString(name)
	sb.WriteByte(' ')
	sb.WriteString(strconv.FormatUint(uint64(v), 10))
}

func wBool(sb *strings.Builder, name string, v bool) {
	sb.WriteString(" :")
	sb.WriteString(name)
	if v {
		sb.WriteString(" true")
	} else {
		sb.WriteString(" false")
	}
}

// wLoc mirrors WRITE_LOCATION_FIELD. goopg always stores normalized locations
// (-1), so no write_location_fields toggle is needed here.
func wLoc(sb *strings.Builder, name string, v int32) {
	wInt(sb, name, v)
}

// wNode mirrors WRITE_NODE_FIELD for a single Node.
func wNode(sb *strings.Builder, name string, n Node) {
	sb.WriteString(" :")
	sb.WriteString(name)
	sb.WriteByte(' ')
	outNode(sb, n)
}

// wNodeList mirrors WRITE_NODE_FIELD for a List. An empty/nil list prints "<>"
// (PostgreSQL's NIL List), a non-empty list "(elem elem ...)".
func wNodeList(sb *strings.Builder, name string, nodes []Node) {
	sb.WriteString(" :")
	sb.WriteString(name)
	sb.WriteByte(' ')
	if len(nodes) == 0 {
		sb.WriteString("<>")
		return
	}
	sb.WriteByte('(')
	for i, nd := range nodes {
		if i > 0 {
			sb.WriteByte(' ')
		}
		outNode(sb, nd)
	}
	sb.WriteByte(')')
}

// outDatum mirrors outfuncs.c:outDatum. By-value: print constlen as the length
// prefix but emit all sizeof(Datum)==8 bytes; by-reference: print VARSIZE and
// emit that many bytes. Each byte is a SIGNED decimal (%d of a char), so 0xFF
// prints as -1.
func outDatum(sb *strings.Builder, c *Const) {
	if c.ConstByval {
		sb.WriteString(strconv.FormatInt(int64(c.ConstLen), 10))
		sb.WriteString(" [ ")
		for i := range 8 {
			sb.WriteString(strconv.Itoa(int(int8(c.Datum[i]))))
			sb.WriteByte(' ')
		}
		sb.WriteByte(']')
		return
	}
	// by-reference (varlena). A nil pointer prints "0 [ ]" in PostgreSQL; our
	// scalar constructors never produce a non-null by-ref Const with nil Datum,
	// but handle it for fidelity.
	sb.WriteString(strconv.Itoa(len(c.Datum)))
	sb.WriteString(" [ ")
	for _, b := range c.Datum {
		sb.WriteString(strconv.Itoa(int(int8(b))))
		sb.WriteByte(' ')
	}
	sb.WriteByte(']')
}

// --- per-tag out functions (field order mirrors outfuncs.c EXACTLY) ---

// outConst mirrors _outConst (outfuncs.c): consttype, consttypmod, constcollid,
// constlen, constbyval, constisnull, location, constvalue.
func outConst(sb *strings.Builder, c *Const) {
	sb.WriteString("CONST")
	wOid(sb, "consttype", c.ConstType)
	wInt(sb, "consttypmod", c.ConstTypmod)
	wOid(sb, "constcollid", c.ConstCollid)
	wInt(sb, "constlen", c.ConstLen)
	wBool(sb, "constbyval", c.ConstByval)
	wBool(sb, "constisnull", c.ConstIsNull)
	wLoc(sb, "location", c.Location)
	sb.WriteString(" :constvalue ")
	if c.ConstIsNull {
		sb.WriteString("<>")
	} else {
		outDatum(sb, c)
	}
}

// outFuncExpr mirrors _outFuncExpr: funcid, funcresulttype, funcretset,
// funcvariadic, funcformat, funccollid, inputcollid, args, location.
func outFuncExpr(sb *strings.Builder, f *FuncExpr) {
	sb.WriteString("FUNCEXPR")
	wOid(sb, "funcid", f.Funcid)
	wOid(sb, "funcresulttype", f.Funcresulttype)
	wBool(sb, "funcretset", f.Funcretset)
	wBool(sb, "funcvariadic", f.Funcvariadic)
	wInt(sb, "funcformat", f.Funcformat)
	wOid(sb, "funccollid", f.Funccollid)
	wOid(sb, "inputcollid", f.Inputcollid)
	wNodeList(sb, "args", f.Args)
	wLoc(sb, "location", f.Location)
}

// outOpExpr mirrors _outOpExpr: opno, opfuncid, opresulttype, opretset,
// opcollid, inputcollid, args, location.
func outOpExpr(sb *strings.Builder, o *OpExpr) {
	sb.WriteString("OPEXPR")
	outOpExprFields(sb, o)
}

// outDistinctExpr mirrors _outDistinctExpr: identical field list to _outOpExpr
// (DistinctExpr and OpExpr are the same struct in PG), only the type token
// differs.
func outDistinctExpr(sb *strings.Builder, d *DistinctExpr) {
	sb.WriteString("DISTINCTEXPR")
	outOpExprFields(sb, (*OpExpr)(d))
}

// outOpExprFields writes the shared OpExpr/DistinctExpr field list (everything
// after the type token): opno, opfuncid, opresulttype, opretset, opcollid,
// inputcollid, args, location.
func outOpExprFields(sb *strings.Builder, o *OpExpr) {
	wOid(sb, "opno", o.Opno)
	wOid(sb, "opfuncid", o.Opfuncid)
	wOid(sb, "opresulttype", o.Opresulttype)
	wBool(sb, "opretset", o.Opretset)
	wOid(sb, "opcollid", o.Opcollid)
	wOid(sb, "inputcollid", o.Inputcollid)
	wNodeList(sb, "args", o.Args)
	wLoc(sb, "location", o.Location)
}

// outRelabelType mirrors _outRelabelType: arg, resulttype, resulttypmod,
// resultcollid, relabelformat, location.
func outRelabelType(sb *strings.Builder, r *RelabelType) {
	sb.WriteString("RELABELTYPE")
	wNode(sb, "arg", r.Arg)
	wOid(sb, "resulttype", r.Resulttype)
	wInt(sb, "resulttypmod", r.Resulttypmod)
	wOid(sb, "resultcollid", r.Resultcollid)
	wInt(sb, "relabelformat", r.Relabelformat)
	wLoc(sb, "location", r.Location)
}

// outCoerceViaIO mirrors _outCoerceViaIO: arg, resulttype, resultcollid,
// coerceformat, location.
func outCoerceViaIO(sb *strings.Builder, c *CoerceViaIO) {
	sb.WriteString("COERCEVIAIO")
	wNode(sb, "arg", c.Arg)
	wOid(sb, "resulttype", c.Resulttype)
	wOid(sb, "resultcollid", c.Resultcollid)
	wInt(sb, "coerceformat", c.Coerceformat)
	wLoc(sb, "location", c.Location)
}

// outSQLValueFunction mirrors _outSQLValueFunction: op, type, typmod, location.
func outSQLValueFunction(sb *strings.Builder, s *SQLValueFunction) {
	sb.WriteString("SQLVALUEFUNCTION")
	wInt(sb, "op", s.Op)
	wOid(sb, "type", s.Type)
	wInt(sb, "typmod", s.Typmod)
	wLoc(sb, "location", s.Location)
}

// boolopToken maps a BoolExprType to the bare token _outBoolExpr writes for the
// :boolop field ("and"/"or"/"not" — the do-it-yourself enum representation in
// outfuncs.c). An out-of-range value panics: it can only arise from a corrupt IR
// (the resolver never produces one), and a silent wrong token would desync Read.
func boolopToken(op int32) string {
	switch op {
	case BoolExprAnd:
		return "and"
	case BoolExprOr:
		return "or"
	case BoolExprNot:
		return "not"
	default:
		panic(fmt.Sprintf("pgnodes: outBoolExpr: bad boolop %d", op))
	}
}

// outBoolExpr mirrors _outBoolExpr (a custom_read_write node in outfuncs.c):
// boolop (as a bare token, NOT an integer), then args, then location.
func outBoolExpr(sb *strings.Builder, b *BoolExpr) {
	sb.WriteString("BOOLEXPR")
	sb.WriteString(" :boolop ")
	sb.WriteString(boolopToken(b.Boolop))
	wNodeList(sb, "args", b.Args)
	wLoc(sb, "location", b.Location)
}

// outNullTest mirrors _outNullTest (generated outfuncs.funcs.c): arg,
// nulltesttype, argisrow, location.
func outNullTest(sb *strings.Builder, n *NullTest) {
	sb.WriteString("NULLTEST")
	wNode(sb, "arg", n.Arg)
	wInt(sb, "nulltesttype", n.NullTestType)
	wBool(sb, "argisrow", n.ArgIsRow)
	wLoc(sb, "location", n.Location)
}

// outBooleanTest mirrors _outBooleanTest (generated outfuncs.funcs.c): arg,
// booltesttype, location. booltesttype is a plain integer ordinal.
func outBooleanTest(sb *strings.Builder, b *BooleanTest) {
	sb.WriteString("BOOLEANTEST")
	wNode(sb, "arg", b.Arg)
	wInt(sb, "booltesttype", b.BoolTestType)
	wLoc(sb, "location", b.Location)
}

// outCaseExpr mirrors _outCaseExpr (generated outfuncs.funcs.c): casetype,
// casecollid, arg, args, defresult, location.
func outCaseExpr(sb *strings.Builder, c *CaseExpr) {
	sb.WriteString("CASEEXPR")
	wOid(sb, "casetype", c.Casetype)
	wOid(sb, "casecollid", c.Casecollid)
	wNode(sb, "arg", c.Arg)
	wNodeList(sb, "args", c.Args)
	wNode(sb, "defresult", c.Defresult)
	wLoc(sb, "location", c.Location)
}

// outCaseWhen mirrors _outCaseWhen: expr, result, location.
func outCaseWhen(sb *strings.Builder, w *CaseWhen) {
	sb.WriteString("CASEWHEN")
	wNode(sb, "expr", w.Expr)
	wNode(sb, "result", w.Result)
	wLoc(sb, "location", w.Location)
}
