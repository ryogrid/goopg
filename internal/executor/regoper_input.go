package executor

// regoper / regoperator input and output (utils/adt/regproc.c regoperin,
// regoperatorin, parseNameAndArgTypes, regoperout, regoperatorout /
// format_operator), resolved over the full pg_operator seed plus the user
// operators. M0149-0001h1a14.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// regoperCandidate is one OpernameGetCandidates result.
type regoperCandidate struct {
	oid         uint32
	left, right uint32
}

// regTypeOIDOf resolves a type name the way the operator's declaration
// spelled it (user types included); 0 when it does not resolve.
func regTypeOIDOf(ctx *Context, name string) uint32 {
	if name == "" {
		return 0
	}
	d, err := regIdentifierInput(NewStringDatum(name), "regtype", ctx, 0)
	if err != nil || d.Kind != KindInt {
		return catalog.TypeNameToOID(name)
	}
	return uint32(d.Int)
}

// userOperatorSchema is the schema name a user operator lives in.
func userOperatorSchema(im *catalog.InMemory, op *catalog.UserOperator) string {
	ns := op.NamespaceOIDOrDefault()
	if ns == catalog.PublicNamespaceOID {
		return "public"
	}
	if s := im.SchemaNameForOID(ns); s != "" {
		return s
	}
	return "public"
}

// regoperCandidates is OpernameGetCandidates(names, '\0', missing_schema_ok
// = true): an explicit schema restricts to that schema (none when it does
// not exist); a bare name searches pg_catalog then the visible schemas, an
// earlier schema's operator masking a later one of the same signature.
func regoperCandidates(ctx *Context, schema, name string) []regoperCandidate {
	im, _ := ctx.Catalog.(*catalog.InMemory)
	var out []regoperCandidate
	seen := map[[2]uint32]bool{}
	add := func(c regoperCandidate) {
		k := [2]uint32{c.left, c.right}
		if schema == "" {
			if seen[k] {
				return
			}
			seen[k] = true
		}
		out = append(out, c)
	}
	if schema == "" || schema == "pg_catalog" {
		for _, e := range catalog.BuiltinOperatorsByName(name) {
			add(regoperCandidate{e.OID, e.LeftType, e.RightType})
		}
	}
	if im == nil {
		return out
	}
	for _, op := range im.UserOperatorsByName(name) {
		s := userOperatorSchema(im, op)
		if schema != "" {
			if s != schema {
				continue
			}
		} else if !RegObjectSchemaVisible(ctx, s) {
			continue
		}
		add(regoperCandidate{op.OID, regTypeOIDOf(ctx, op.LeftType), regTypeOIDOf(ctx, op.RightType)})
	}
	return out
}

// regNumericOid is parseNumericOid: regoper / regoperator take digits as an
// OID but, unlike the other reg* inputs, give "-" no special meaning.
func regNumericOid(s string) (uint32, bool, error) {
	if s == "" {
		return 0, false, nil
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false, nil
		}
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, true, &ExecError{Code: "22003",
			Message: fmt.Sprintf("value \"%s\" is out of range for type oid", s)}
	}
	return uint32(n), true, nil
}

// regoperIn is regoperin.
func regoperIn(raw string, ctx *Context, pos int) (Datum, error) {
	if oid, ok, err := regNumericOid(raw); ok {
		if err != nil {
			return NullDatum, err
		}
		return NewIntDatum(int64(oid)), nil
	}
	schema, name, ok := splitRegQualifiedName(raw)
	if !ok {
		return NullDatum, &ExecError{Code: "42602", Pos: pos, Message: "invalid name syntax"}
	}
	cands := regoperCandidates(ctx, schema, name)
	switch {
	case len(cands) == 0:
		return NullDatum, &ExecError{Code: "42883", Pos: pos, Message: "operator does not exist: " + raw}
	case len(cands) > 1:
		return NullDatum, &ExecError{Code: "42725", Pos: pos, Message: "more than one operator named " + raw}
	}
	return NewIntDatum(int64(cands[0].oid)), nil
}

func regIsSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// regParseNameAndArgTypes is parseNameAndArgTypes: the name before the
// first unquoted '(' and the comma-separated type list up to the trailing
// ')'. allowNone maps NONE to InvalidOid.
func regParseNameAndArgTypes(s string, allowNone bool, ctx *Context, pos int) (schema, name string, args []uint32, err error) {
	inQuote := false
	open := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			inQuote = !inQuote
		} else if s[i] == '(' && !inQuote {
			open = i
			break
		}
	}
	invalid := func(msg string) error {
		return &ExecError{Code: "22P02", Pos: pos, Message: msg}
	}
	if open < 0 {
		return "", "", nil, invalid("expected a left parenthesis")
	}
	var ok bool
	if schema, name, ok = splitRegQualifiedName(s[:open]); !ok {
		return "", "", nil, &ExecError{Code: "42602", Pos: pos, Message: "invalid name syntax"}
	}
	rest := s[open+1:]
	end := len(rest) - 1
	for end > 0 && regIsSpace(rest[end]) {
		end--
	}
	if end < 0 || rest[end] != ')' {
		return "", "", nil, invalid("expected a right parenthesis")
	}
	rest = rest[:end]
	args = []uint32{}
	hadComma := false
	i := 0
	for {
		for i < len(rest) && regIsSpace(rest[i]) {
			i++
		}
		if i >= len(rest) {
			if hadComma {
				return "", "", nil, invalid("expected a type name")
			}
			break
		}
		start := i
		inQuote = false
		depth := 0
		for ; i < len(rest); i++ {
			c := rest[i]
			if c == '"' {
				inQuote = !inQuote
			} else if c == ',' && !inQuote && depth == 0 {
				break
			} else if !inQuote {
				switch c {
				case '(', '[':
					depth++
				case ')', ']':
					depth--
				}
			}
		}
		if inQuote || depth != 0 {
			return "", "", nil, invalid("improper type name")
		}
		typ := strings.TrimRight(rest[start:i], " \t\n\r\f\v")
		hadComma = i < len(rest)
		if hadComma {
			i++
		}
		var oid uint32
		if !(allowNone && strings.EqualFold(typ, "none")) {
			d, terr := regIdentifierInput(NewStringDatum(typ), "regtype", ctx, pos)
			if terr != nil {
				return "", "", nil, terr
			}
			if d.Kind != KindInt {
				return "", "", nil, &ExecError{Code: "42704", Pos: pos, Message: fmt.Sprintf("type \"%s\" does not exist", typ)}
			}
			oid = uint32(d.Int)
		}
		if len(args) >= 100 {
			return "", "", nil, &ExecError{Code: "54023", Pos: pos, Message: "too many arguments"}
		}
		args = append(args, oid)
	}
	return schema, name, args, nil
}

// regoperatorIn is regoperatorin.
func regoperatorIn(raw string, ctx *Context, pos int) (Datum, error) {
	if oid, ok, err := regNumericOid(raw); ok {
		if err != nil {
			return NullDatum, err
		}
		return NewIntDatum(int64(oid)), nil
	}
	schema, name, args, err := regParseNameAndArgTypes(raw, true, ctx, pos)
	if err != nil {
		return NullDatum, err
	}
	if len(args) == 1 {
		return NullDatum, &ExecError{Code: "42P02", Pos: pos, Message: "missing argument",
			Hint: "Use NONE to denote the missing argument of a unary operator."}
	}
	if len(args) != 2 {
		return NullDatum, &ExecError{Code: "54023", Pos: pos, Message: "too many arguments",
			Hint: "Provide two argument types for operator."}
	}
	// OpernameGetOprid: the first exact (left, right) match along the path.
	for _, c := range regoperCandidates(ctx, schema, name) {
		if c.left == args[0] && c.right == args[1] {
			return NewIntDatum(int64(c.oid)), nil
		}
	}
	return NullDatum, &ExecError{Code: "42883", Pos: pos, Message: "operator does not exist: " + raw}
}

// regoperLookupOID returns an operator's name, schema and operand types.
func regoperLookupOID(ctx *Context, oid uint32) (name, schema string, left, right uint32, ok bool) {
	if e, found := catalog.BuiltinOperatorEntryByOID(oid); found {
		return e.Name, "pg_catalog", e.LeftType, e.RightType, true
	}
	if im, isIM := ctx.Catalog.(*catalog.InMemory); isIM {
		if op := im.LookupUserOperatorByOID(oid); op != nil && op.FuncOID != 0 {
			return op.Name, userOperatorSchema(im, op), regTypeOIDOf(ctx, op.LeftType), regTypeOIDOf(ctx, op.RightType), true
		}
	}
	return "", "", 0, 0, false
}

// regoperOut is regoperout: "0" for InvalidOid, the bare name when regoperin
// would find exactly this operator, else schema-qualified; an unknown OID
// prints as its number.
func regoperOut(oid uint32, ctx *Context) string {
	if oid == 0 {
		return "0"
	}
	name, schema, _, _, ok := regoperLookupOID(ctx, oid)
	if !ok {
		return strconv.FormatUint(uint64(oid), 10)
	}
	if c := regoperCandidates(ctx, "", name); len(c) == 1 && c[0].oid == oid {
		return name
	}
	return pgQuoteIdent(schema) + "." + name
}

// regoperatorOut is regoperatorout / format_operator: name(lefttype,
// righttype) with NONE for a missing operand, the name qualified when the
// operator is not visible (OperatorIsVisible).
func regoperatorOut(oid uint32, ctx *Context) string {
	if oid == 0 {
		return "0"
	}
	name, schema, left, right, ok := regoperLookupOID(ctx, oid)
	if !ok {
		return strconv.FormatUint(uint64(oid), 10)
	}
	typ := func(t uint32) string {
		if t == 0 {
			return "NONE"
		}
		return RegtypeName(ctx.Catalog, t, func(s string) bool { return !RegObjectSchemaVisible(ctx, s) })
	}
	visible := false
	for _, c := range regoperCandidates(ctx, "", name) {
		if c.left == left && c.right == right {
			visible = c.oid == oid
			break
		}
	}
	prefix := ""
	if !visible {
		prefix = pgQuoteIdent(schema) + "."
	}
	return prefix + name + "(" + typ(left) + "," + typ(right) + ")"
}
