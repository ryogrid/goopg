package pgnodes

import (
	"fmt"
	"strconv"
)

// Read parses canonical pg_node_tree text (as produced by Out, or by real
// PostgreSQL nodeToString / pg_attrdef.adbin) back into the scalar IR. It is a
// Go port of read.c:pg_strtok + nodeRead and the relevant _read<Tag> functions
// in readfuncs.c. Only the scalar subset (Const/FuncExpr/OpExpr/RelabelType/
// CoerceViaIO/SQLValueFunction) is understood; any other tag is an error, which
// the callers use as their "not canonical, fall back to SQL text" signal.
func Read(s string) (Node, error) {
	t := &tokenizer{s: s}
	n, err := readNode(t)
	if err != nil {
		return nil, err
	}
	if tok, ok := t.next(); ok {
		return nil, fmt.Errorf("pgnodes: trailing token %q after node", tok)
	}
	return n, nil
}

// tokenizer is a port of read.c:pg_strtok. Whitespace separates tokens; the
// four brace/paren characters are single-character tokens; "<>" is the empty
// token. (Backslash escapes are handled for fidelity though the scalar subset
// never emits them.)
type tokenizer struct {
	s   string
	pos int
}

func isTokSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\t' }
func isTokSpecial(c byte) bool {
	return c == '(' || c == ')' || c == '{' || c == '}'
}

// next returns the next token and whether one was found. A "<>" empty token is
// returned verbatim so callers can recognize it.
func (t *tokenizer) next() (string, bool) {
	for t.pos < len(t.s) && isTokSpace(t.s[t.pos]) {
		t.pos++
	}
	if t.pos >= len(t.s) {
		return "", false
	}
	start := t.pos
	if isTokSpecial(t.s[t.pos]) {
		t.pos++
	} else {
		for t.pos < len(t.s) {
			c := t.s[t.pos]
			if isTokSpace(c) || isTokSpecial(c) {
				break
			}
			if c == '\\' && t.pos+1 < len(t.s) {
				t.pos += 2
			} else {
				t.pos++
			}
		}
	}
	return t.s[start:t.pos], true
}

// peek returns the next token without consuming it.
func (t *tokenizer) peek() (string, bool) {
	save := t.pos
	tok, ok := t.next()
	t.pos = save
	return tok, ok
}

// readNode reads one "{TAG ...}" node, or "<>" for a nil node.
func readNode(t *tokenizer) (Node, error) {
	tok, ok := t.next()
	if !ok {
		return nil, fmt.Errorf("pgnodes: unexpected end of input, expected node")
	}
	if tok == "<>" {
		return nil, nil
	}
	if tok != "{" {
		return nil, fmt.Errorf("pgnodes: expected '{', got %q", tok)
	}
	label, ok := t.next()
	if !ok {
		return nil, fmt.Errorf("pgnodes: unexpected end of input, expected node label")
	}
	var (
		n   Node
		err error
	)
	switch label {
	case "CONST":
		n, err = readConst(t)
	case "FUNCEXPR":
		n, err = readFuncExpr(t)
	case "OPEXPR":
		n, err = readOpExpr(t)
	case "RELABELTYPE":
		n, err = readRelabelType(t)
	case "COERCEVIAIO":
		n, err = readCoerceViaIO(t)
	case "SQLVALUEFUNCTION":
		n, err = readSQLValueFunction(t)
	case "BOOLEXPR":
		n, err = readBoolExpr(t)
	case "NULLTEST":
		n, err = readNullTest(t)
	case "BOOLEANTEST":
		n, err = readBooleanTest(t)
	case "CASEEXPR":
		n, err = readCaseExpr(t)
	case "CASEWHEN":
		n, err = readCaseWhen(t)
	case "QUERY":
		n, err = readQuery(t)
	case "RANGETBLENTRY":
		n, err = readRangeTblEntry(t)
	case "RTEPERMISSIONINFO":
		n, err = readRTEPermissionInfo(t)
	case "FROMEXPR":
		n, err = readFromExpr(t)
	case "RANGETBLREF":
		n, err = readRangeTblRef(t)
	case "TARGETENTRY":
		n, err = readTargetEntry(t)
	case "VAR":
		n, err = readVar(t)
	case "ALIAS":
		n, err = readAlias(t)
	default:
		return nil, fmt.Errorf("pgnodes: unsupported node tag %q", label)
	}
	if err != nil {
		return nil, err
	}
	if tok, ok := t.next(); !ok || tok != "}" {
		return nil, fmt.Errorf("pgnodes: expected '}' to close %s, got %q", label, tok)
	}
	return n, nil
}

// --- READ_* field helpers. Each skips the ":fieldname" token then parses the
// value token(s), mirroring readfuncs.c's READ_* macros. ---

// skipField consumes the ":name" label token.
func (t *tokenizer) skipField() {
	t.next()
}

func readUint32(t *tokenizer) (uint32, error) {
	t.skipField()
	tok, ok := t.next()
	if !ok {
		return 0, fmt.Errorf("pgnodes: expected uint value")
	}
	v, err := strconv.ParseUint(tok, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("pgnodes: bad uint %q: %w", tok, err)
	}
	return uint32(v), nil
}

func readInt32(t *tokenizer) (int32, error) {
	t.skipField()
	tok, ok := t.next()
	if !ok {
		return 0, fmt.Errorf("pgnodes: expected int value")
	}
	v, err := strconv.ParseInt(tok, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("pgnodes: bad int %q: %w", tok, err)
	}
	return int32(v), nil
}

func readBool(t *tokenizer) (bool, error) {
	t.skipField()
	tok, ok := t.next()
	if !ok {
		return false, fmt.Errorf("pgnodes: expected bool value")
	}
	return tok == "true", nil
}

// readDatumField skips ":constvalue" then reads the datum (or "<>" for null).
func readDatumField(t *tokenizer, isNull, byval bool) ([]byte, error) {
	t.skipField() // :constvalue
	if isNull {
		t.next() // "<>"
		return nil, nil
	}
	return readDatum(t, byval)
}

// readDatum mirrors readfuncs.c:readDatum. By-value always reads sizeof(Datum)
// == 8 bytes; by-reference reads the declared length. Each byte token is a
// signed decimal.
func readDatum(t *tokenizer, byval bool) ([]byte, error) {
	lenTok, ok := t.next()
	if !ok {
		return nil, fmt.Errorf("pgnodes: expected datum length")
	}
	length, err := strconv.Atoi(lenTok)
	if err != nil {
		return nil, fmt.Errorf("pgnodes: bad datum length %q: %w", lenTok, err)
	}
	if open, ok := t.next(); !ok || open != "[" {
		return nil, fmt.Errorf("pgnodes: expected '[' to start datum, got %q", open)
	}
	n := length
	if byval {
		n = 8
	} else if length <= 0 {
		n = 0
	}
	var out []byte
	if n > 0 {
		out = make([]byte, n)
		for i := 0; i < n; i++ {
			tok, ok := t.next()
			if !ok {
				return nil, fmt.Errorf("pgnodes: truncated datum body")
			}
			v, err := strconv.Atoi(tok)
			if err != nil {
				return nil, fmt.Errorf("pgnodes: bad datum byte %q: %w", tok, err)
			}
			out[i] = byte(int8(v))
		}
	}
	if closeTok, ok := t.next(); !ok || closeTok != "]" {
		return nil, fmt.Errorf("pgnodes: expected ']' to end datum, got %q", closeTok)
	}
	return out, nil
}

// readNodeField skips ":name" then reads a single node.
func readNodeField(t *tokenizer) (Node, error) {
	t.skipField()
	return readNode(t)
}

// readNodeListField skips ":name" then reads a "(...)"/"<>" list.
func readNodeListField(t *tokenizer) ([]Node, error) {
	t.skipField()
	tok, ok := t.next()
	if !ok {
		return nil, fmt.Errorf("pgnodes: expected list")
	}
	if tok == "<>" {
		return nil, nil
	}
	if tok != "(" {
		return nil, fmt.Errorf("pgnodes: expected '(' to start list, got %q", tok)
	}
	var nodes []Node
	for {
		p, ok := t.peek()
		if !ok {
			return nil, fmt.Errorf("pgnodes: unterminated list")
		}
		if p == ")" {
			t.next()
			break
		}
		nd, err := readNode(t)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, nd)
	}
	return nodes, nil
}

// --- per-tag read functions (field order mirrors the out functions) ---

func readConst(t *tokenizer) (*Const, error) {
	c := &Const{}
	var err error
	if c.ConstType, err = readUint32(t); err != nil {
		return nil, err
	}
	if c.ConstTypmod, err = readInt32(t); err != nil {
		return nil, err
	}
	if c.ConstCollid, err = readUint32(t); err != nil {
		return nil, err
	}
	if c.ConstLen, err = readInt32(t); err != nil {
		return nil, err
	}
	if c.ConstByval, err = readBool(t); err != nil {
		return nil, err
	}
	if c.ConstIsNull, err = readBool(t); err != nil {
		return nil, err
	}
	if c.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	if c.Datum, err = readDatumField(t, c.ConstIsNull, c.ConstByval); err != nil {
		return nil, err
	}
	return c, nil
}

func readFuncExpr(t *tokenizer) (*FuncExpr, error) {
	f := &FuncExpr{}
	var err error
	if f.Funcid, err = readUint32(t); err != nil {
		return nil, err
	}
	if f.Funcresulttype, err = readUint32(t); err != nil {
		return nil, err
	}
	if f.Funcretset, err = readBool(t); err != nil {
		return nil, err
	}
	if f.Funcvariadic, err = readBool(t); err != nil {
		return nil, err
	}
	if f.Funcformat, err = readInt32(t); err != nil {
		return nil, err
	}
	if f.Funccollid, err = readUint32(t); err != nil {
		return nil, err
	}
	if f.Inputcollid, err = readUint32(t); err != nil {
		return nil, err
	}
	if f.Args, err = readNodeListField(t); err != nil {
		return nil, err
	}
	if f.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	return f, nil
}

func readOpExpr(t *tokenizer) (*OpExpr, error) {
	o := &OpExpr{}
	var err error
	if o.Opno, err = readUint32(t); err != nil {
		return nil, err
	}
	if o.Opfuncid, err = readUint32(t); err != nil {
		return nil, err
	}
	if o.Opresulttype, err = readUint32(t); err != nil {
		return nil, err
	}
	if o.Opretset, err = readBool(t); err != nil {
		return nil, err
	}
	if o.Opcollid, err = readUint32(t); err != nil {
		return nil, err
	}
	if o.Inputcollid, err = readUint32(t); err != nil {
		return nil, err
	}
	if o.Args, err = readNodeListField(t); err != nil {
		return nil, err
	}
	if o.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	return o, nil
}

func readRelabelType(t *tokenizer) (*RelabelType, error) {
	r := &RelabelType{}
	var err error
	if r.Arg, err = readNodeField(t); err != nil {
		return nil, err
	}
	if r.Resulttype, err = readUint32(t); err != nil {
		return nil, err
	}
	if r.Resulttypmod, err = readInt32(t); err != nil {
		return nil, err
	}
	if r.Resultcollid, err = readUint32(t); err != nil {
		return nil, err
	}
	if r.Relabelformat, err = readInt32(t); err != nil {
		return nil, err
	}
	if r.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	return r, nil
}

func readCoerceViaIO(t *tokenizer) (*CoerceViaIO, error) {
	c := &CoerceViaIO{}
	var err error
	if c.Arg, err = readNodeField(t); err != nil {
		return nil, err
	}
	if c.Resulttype, err = readUint32(t); err != nil {
		return nil, err
	}
	if c.Resultcollid, err = readUint32(t); err != nil {
		return nil, err
	}
	if c.Coerceformat, err = readInt32(t); err != nil {
		return nil, err
	}
	if c.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	return c, nil
}

// readBoolExpr mirrors _readBoolExpr (a custom_read_write node): the :boolop
// field is a bare token ("and"/"or"/"not"), not an integer, so it is parsed by
// hand before the generated args/location fields.
func readBoolExpr(t *tokenizer) (*BoolExpr, error) {
	b := &BoolExpr{}
	t.skipField() // :boolop
	tok, ok := t.next()
	if !ok {
		return nil, fmt.Errorf("pgnodes: expected boolop token")
	}
	switch tok {
	case "and":
		b.Boolop = BoolExprAnd
	case "or":
		b.Boolop = BoolExprOr
	case "not":
		b.Boolop = BoolExprNot
	default:
		return nil, fmt.Errorf("pgnodes: unrecognized boolop %q", tok)
	}
	var err error
	if b.Args, err = readNodeListField(t); err != nil {
		return nil, err
	}
	if b.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	return b, nil
}

// readNullTest mirrors the generated _readNullTest: arg, nulltesttype, argisrow,
// location.
func readNullTest(t *tokenizer) (*NullTest, error) {
	n := &NullTest{}
	var err error
	if n.Arg, err = readNodeField(t); err != nil {
		return nil, err
	}
	if n.NullTestType, err = readInt32(t); err != nil {
		return nil, err
	}
	if n.ArgIsRow, err = readBool(t); err != nil {
		return nil, err
	}
	if n.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	return n, nil
}

// readBooleanTest mirrors the generated _readBooleanTest: arg, booltesttype,
// location.
func readBooleanTest(t *tokenizer) (*BooleanTest, error) {
	b := &BooleanTest{}
	var err error
	if b.Arg, err = readNodeField(t); err != nil {
		return nil, err
	}
	if b.BoolTestType, err = readInt32(t); err != nil {
		return nil, err
	}
	if b.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	return b, nil
}

// readCaseExpr mirrors the generated _readCaseExpr: casetype, casecollid, arg,
// args, defresult, location.
func readCaseExpr(t *tokenizer) (*CaseExpr, error) {
	c := &CaseExpr{}
	var err error
	if c.Casetype, err = readUint32(t); err != nil {
		return nil, err
	}
	if c.Casecollid, err = readUint32(t); err != nil {
		return nil, err
	}
	if c.Arg, err = readNodeField(t); err != nil {
		return nil, err
	}
	if c.Args, err = readNodeListField(t); err != nil {
		return nil, err
	}
	if c.Defresult, err = readNodeField(t); err != nil {
		return nil, err
	}
	if c.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	return c, nil
}

// readCaseWhen mirrors the generated _readCaseWhen: expr, result, location.
func readCaseWhen(t *tokenizer) (*CaseWhen, error) {
	w := &CaseWhen{}
	var err error
	if w.Expr, err = readNodeField(t); err != nil {
		return nil, err
	}
	if w.Result, err = readNodeField(t); err != nil {
		return nil, err
	}
	if w.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	return w, nil
}

func readSQLValueFunction(t *tokenizer) (*SQLValueFunction, error) {
	s := &SQLValueFunction{}
	var err error
	if s.Op, err = readInt32(t); err != nil {
		return nil, err
	}
	if s.Type, err = readUint32(t); err != nil {
		return nil, err
	}
	if s.Typmod, err = readInt32(t); err != nil {
		return nil, err
	}
	if s.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	return s, nil
}
