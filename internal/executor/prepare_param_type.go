package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// ResolvePrepareParamType is typenameTypeId for one declared PREPARE
// parameter type (commands/prepare.c PrepareQuery → parse_type.c
// typenameTypeId → LookupTypeName). spelled is the declared text, typmod and
// array suffix included; isBuiltin answers for a built-in type spelling with
// no schema qualifier (the caller's table of PG's built-in names).
//
// A schema qualifier is resolved, not decoration: the schema must exist in
// the connection's database (3F000), built-in types answer only to
// pg_catalog, and user types (enum / domain / composite / range /
// multirange) and table row types answer to any other schema. The returned
// spelling has the qualifier resolved away, because everything downstream
// (EXECUTE's argument coercion, the inferred parameter types) reads the
// declared spelling as a bare type name. pg_dump depends on this: it runs
// `PREPARE dumpFunc(pg_catalog.oid)` before dumping the first function, so a
// rejected qualifier made pg_dump of any database with a user function fail
// (M0122-0015c).
func ResolvePrepareParamType(ctx *Context, spelled string, isBuiltin func(string) bool) (string, *ExecError) {
	s := strings.TrimSpace(spelled)
	schema, rest := splitTypeQualifier(s)
	notFound := &ExecError{Code: "42704", Message: fmt.Sprintf("type %q does not exist", s)}
	if schema != "" && ctx != nil && ctx.Catalog != nil &&
		!ctx.Catalog.SchemaExists(schema, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		return "", &ExecError{Code: "3F000", Message: fmt.Sprintf("schema %q does not exist", schema)}
	}
	if (schema == "" || schema == "pg_catalog") && isBuiltin(rest) {
		return rest, nil
	}
	if schema == "pg_catalog" || ctx == nil || ctx.Catalog == nil {
		return "", notFound
	}
	name := typeBaseName(rest)
	if _, ok := userTypeOIDForName(ctx.Catalog, name); ok {
		return rest, nil
	}
	tblSchema := schema
	if tblSchema == "" {
		tblSchema = "public"
	}
	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: tblSchema, Name: name},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)); ok {
		return rest, nil
	}
	return "", notFound
}

// splitTypeQualifier splits a leading `schema.` off a declared type name: the
// first '.' outside double quotes and before any typmod or array suffix. The
// schema is unquoted, or lower-cased when it was not quoted, as the grammar's
// ColId would deliver it. A name with no qualifier returns schema "".
func splitTypeQualifier(s string) (schema, rest string) {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '(' || c == '[':
			return "", s
		case c == '.':
			return unquoteIdent(strings.TrimSpace(s[:i])), strings.TrimSpace(s[i+1:])
		}
	}
	return "", s
}

// typeBaseName strips a typmod and array suffix off a type spelling and
// unquotes it: `"Mood"[]` → Mood, `myrange(3)` → myrange.
func typeBaseName(s string) string {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			inQuote = !inQuote
		case !inQuote && (c == '(' || c == '['):
			return unquoteIdent(strings.TrimSpace(s[:i]))
		}
	}
	return unquoteIdent(strings.TrimSpace(s))
}

// unquoteIdent is the ColId rule for one identifier: a double-quoted one
// keeps its spelling ("" collapsing to "), anything else is lower-cased.
func unquoteIdent(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return strings.ToLower(s)
}
