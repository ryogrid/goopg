package executor

// pg_*_is_visible(oid) → bool — namespace.c's RelationIsVisible,
// TypeIsVisible, FunctionIsVisible and StatisticsObjIsVisible
// (postgres/src/backend/catalog/namespace.c), exposed through
// pg_table_is_visible & friends. M0149-0001h1a32.
//
// PG's rule: an object is visible iff a search for its bare name through
// the session's effective search_path (implicit pg_temp / pg_catalog
// first, then the listed schemas) finds THIS object — not merely iff its
// schema is on the path, since an earlier schema can hold a same-named
// rival that hides it. A missing OID yields NULL (the *IsVisibleExt
// is_missing arm, PG_RETURN_NULL).
//
// The relation, type and function arms reuse the session's own reg*
// input functions, which already resolve an unqualified name through
// search_path the way RelnameGetRelid / TypenameGetTypid /
// FuncnameGetCandidates do: the object is visible exactly when its bare
// name round-trips to the same OID.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// pgObjectIsVisible evaluates one pg_*_is_visible call. handled is false
// for the kinds this port does not model yet; the caller keeps their
// historical answer.
func pgObjectIsVisible(fn string, arg Datum, ctx *Context) (result Datum, handled bool) {
	if arg.IsNull() {
		return NullDatum, true
	}
	oid, ok := visibleOIDArg(arg)
	if !ok || ctx == nil || ctx.Catalog == nil {
		return NullDatum, false
	}
	switch fn {
	case "pg_table_is_visible":
		return relationIsVisible(oid, ctx), true
	case "pg_type_is_visible":
		return typeIsVisible(oid, ctx), true
	case "pg_function_is_visible":
		return functionIsVisible(oid, ctx), true
	case "pg_statistics_obj_is_visible":
		return statisticsObjIsVisible(oid, ctx), true
	}
	return NullDatum, false
}

func visibleOIDArg(d Datum) (uint32, bool) {
	switch d.Kind {
	case KindInt:
		return uint32(d.Int), true
	case KindString:
		n, err := strconv.ParseUint(strings.TrimSpace(d.StringValue()), 10, 32)
		return uint32(n), err == nil
	}
	return 0, false
}

// roundTrips reports whether the bare spelling resolves, through the
// session's search_path, to oid.
func roundTrips(spelling, regType string, oid uint32, ctx *Context) bool {
	r, err := regIdentifierInput(NewStringDatum(spelling), regType, ctx, 0)
	return err == nil && r.Kind == KindInt && uint32(r.Int) == oid
}

// relationIsVisible is RelationIsVisibleExt.
func relationIsVisible(oid uint32, ctx *Context) Datum {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return NullDatum
	}
	dbOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	var name string
	if tbl, found := im.LookupTableByOID(oid, dbOid); found {
		name = tbl.Name
	} else if idx, found := im.LookupIndexByOID(oid, dbOid); found {
		name = idx.Name
	} else if _, found := im.ToastRelName(oid, dbOid); found {
		// pg_toast is never on a search_path.
		return NewBoolDatum(false)
	} else if ct, found := im.LookupCompositeTypeByRelOID(oid); found {
		// A standalone composite type's relation (relkind 'c') lives in the
		// type registry, not the table map; visible when its schema is
		// searched (psql's \d <type> filters on pg_table_is_visible).
		schema := ctx.Catalog.SchemaNameForOID(ct.NamespaceOID)
		if schema == "" {
			schema = "public"
		}
		return NewBoolDatum(RegObjectSchemaVisible(ctx, schema))
	} else {
		return NullDatum
	}
	return NewBoolDatum(roundTrips(pgQuoteIdent(name), "regclass", oid, ctx))
}

// typeIsVisible is TypeIsVisibleExt.
func typeIsVisible(oid uint32, ctx *Context) Datum {
	bare := RegtypeName(ctx.Catalog, oid, func(string) bool { return false })
	if bare == strconv.FormatUint(uint64(oid), 10) || bare == "-" {
		return NullDatum
	}
	return NewBoolDatum(roundTrips(bare, "regtype", oid, ctx))
}

// functionIsVisible is FunctionIsVisibleExt: the bare name with the
// argument list (each argument type schema-qualified only when it is off
// the path, as format_procedure renders it) must resolve to this routine.
func functionIsVisible(oid uint32, ctx *Context) Datum {
	routines := ctx.Catalog.Routines()
	if _, name, args, ok := catalog.RegprocedureNameParts(oid, routines); ok {
		spelling := pgQuoteIdent(name) + "(" +
			regprocedureArglist(args, func(s string) bool { return RegObjectSchemaVisible(ctx, s) }) + ")"
		return NewBoolDatum(roundTrips(spelling, "regprocedure", oid, ctx))
	}
	// A CREATE AGGREGATE routine carries no schema of its own (it lives
	// in the public schema).
	if _, ok := ctx.Catalog.LookupUserAggregateByOID(oid); ok {
		return NewBoolDatum(RegObjectSchemaVisible(ctx, "public"))
	}
	return NullDatum
}

// statisticsObjIsVisible is StatisticsObjIsVisibleExt: walk the path;
// the object's own schema must come before any schema holding a
// same-named statistics object.
func statisticsObjIsVisible(oid uint32, ctx *Context) Datum {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return NullDatum
	}
	obj, found := im.StatisticsByOID(oid)
	if !found {
		return NullDatum
	}
	schema := obj.Schema
	if schema == "" {
		schema = "public"
	}
	for _, s := range searchPathSchemas(ctx) {
		if strings.EqualFold(s, schema) {
			return NewBoolDatum(true)
		}
		if _, rival := im.LookupStatistics(s + "." + obj.Name); rival {
			return NewBoolDatum(false)
		}
	}
	return NewBoolDatum(false)
}

// resolveUserRoutineOnPath is the user-routine half of regprocin /
// regprocedurein (FuncnameGetCandidates, namespace.c): a qualified name
// searches its schema only; a bare one walks the session's search_path in
// order. regprocedure takes the first routine whose input argument types
// equal the parsed list; regproc collects one routine per signature (an
// earlier schema hides a same-signature routine later in the path) and
// needs exactly one. M0149-0001h1a32.
func resolveUserRoutineOnPath(rs *catalog.Routines, schema, fn, raw string, procedure bool, ctx *Context, db uint32, pos int) (uint32, bool, error) {
	var want []uint32
	haveArgs := false
	if procedure {
		want, haveArgs = regprocedureArgTypes(raw, ctx)
	}
	schemas := []string{schema}
	if schema == "" {
		schemas = searchPathSchemas(ctx)
	}
	var found []*catalog.Routine
	seen := map[string]bool{}
	for _, s := range schemas {
		for _, r := range rs.LookupByName(parser.ObjectName{Schema: s, Name: fn}, db) {
			if procedure {
				if !haveArgs || routineInputArgsMatch(r, want, ctx) {
					return r.OID, true, nil
				}
				continue
			}
			if sig := r.Signature(); !seen[sig] {
				seen[sig] = true
				found = append(found, r)
			}
		}
	}
	switch {
	case len(found) == 1:
		return found[0].OID, true, nil
	case len(found) > 1:
		return 0, false, &ExecError{Code: "42725", Pos: pos,
			Message: fmt.Sprintf("more than one function named %q", raw)}
	}
	return 0, false, nil
}

// routineInputArgsMatch compares r's input argument types (OUT excluded,
// as proargtypes) with want. An argument type that no longer resolves
// keeps the earlier name-only match.
func routineInputArgsMatch(r *catalog.Routine, want []uint32, ctx *Context) bool {
	var got []uint32
	for i, t := range r.ArgTypes {
		if i < len(r.ArgModes) && r.ArgModes[i] == "o" {
			continue
		}
		if i < len(r.ArgTypeOIDs) && r.ArgTypeOIDs[i] != 0 {
			got = append(got, r.ArgTypeOIDs[i])
			continue
		}
		spelling := t.Name
		if t.IsArray && !strings.HasSuffix(spelling, "[]") {
			spelling += "[]"
		}
		if i < len(r.ArgTypeSchemas) && r.ArgTypeSchemas[i] != "" {
			spelling = pgQuoteIdent(r.ArgTypeSchemas[i]) + "." + spelling
		}
		d, err := regIdentifierInput(NewStringDatum(spelling), "regtype", ctx, 0)
		if err != nil || d.Kind != KindInt {
			return true
		}
		got = append(got, uint32(d.Int))
	}
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// userTypeOnPath applies TypenameGetTypid's namespace rule to a user type
// found by bare name in goopg's single type namespace: a qualified
// spelling must name the type's own schema; a bare one needs that schema
// on the session's search_path. M0149-0001h1a32.
func userTypeOnPath(ctx *Context, oid uint32, schema string) bool {
	typSchema := "public"
	userTypeNameForOID(ctx.Catalog, oid, func(s string) bool {
		if s != "" {
			typSchema = s
		}
		return false
	})
	if schema != "" {
		return strings.EqualFold(schema, typSchema)
	}
	return RegObjectSchemaVisible(ctx, typSchema)
}

// builtinProcByArgs is LookupFuncWithArgs over pg_catalog: the built-in
// overload of fn whose input argument types (pg_proc.dat typnames,
// resolved by BuiltinTypnameOID) equal want. M0149-0001h1a32.
func builtinProcByArgs(fn string, want []uint32) (uint32, bool) {
	for _, oid := range catalog.BuiltinProcOIDsByName(fn) {
		_, _, args, ok := catalog.RegprocedureNameParts(oid, nil)
		if !ok || len(args) != len(want) {
			continue
		}
		match := true
		for i, a := range args {
			if oid, ok := catalog.BuiltinTypnameOID(a.Name); !ok || oid != want[i] {
				match = false
				break
			}
		}
		if match {
			return oid, true
		}
	}
	return 0, false
}

// regtypeMultiWordBuiltin resolves an unquoted multi-word SQL type name
// (any case, any spacing, optional [] suffix) to its pg_catalog type.
func regtypeMultiWordBuiltin(name string) (uint32, bool) {
	if strings.ContainsRune(name, '"') {
		return 0, false
	}
	fields := strings.Fields(strings.ToLower(name))
	if len(fields) < 2 {
		return 0, false
	}
	return catalog.BuiltinTypeOIDByFormatName(strings.Join(fields, " "))
}

// regtypeQuotedBuiltin resolves `"kw"`, `"kw"[]` and their pg_catalog.
// qualified forms against format_type's quoted pg_catalog spellings.
func regtypeQuotedBuiltin(name string) (uint32, bool) {
	n := strings.TrimPrefix(name, "pg_catalog.")
	if !strings.HasPrefix(n, `"`) {
		return 0, false
	}
	return catalog.BuiltinTypeOIDByFormatName(n)
}

// regArgText renders a reg*-typed argument of an "any"-taking text function
// (concat, concat_ws, format) through its output function, as PG does
// (concat_internal / text_format call the argument type's typoutput):
// 'pt'::regclass contributes "pt", not its OID. Other values pass through.
// M0149-0001h1a33.
func regArgText(e optimizer.Expr, d Datum, ctx *Context) Datum {
	if d.Kind != KindInt {
		return d
	}
	t, ok := optimizer.ExprResultType(e)
	if !ok || t.IsArray || !isRegIdentifierTypeName(t.Name) {
		return d
	}
	out, err := evalCastTyped(d, "text", t.Name, 0, ctx)
	if err != nil || out.Kind != KindString {
		return d
	}
	return out
}

// isRegTypedExpr reports a statically reg*-typed scalar expression.
func isRegTypedExpr(e optimizer.Expr) bool {
	t, ok := optimizer.ExprResultType(e)
	return ok && !t.IsArray && isRegIdentifierTypeName(t.Name)
}

// relationRowtypeForName resolves a type name that names a relation to the
// relation's rowtype (TypenameGetTypid finds it as an ordinary pg_type row
// in the relation's schema); "name[]" is its array type. M0149-0001h1a34.
func relationRowtypeForName(ctx *Context, schema, typeName string) (uint32, bool) {
	base, isArr := strings.TrimSuffix(typeName, "[]"), strings.HasSuffix(typeName, "[]")
	dbOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	tbl, ok := lookupTableOnSearchPath(ctx, parser.ObjectName{Schema: schema, Name: base}, dbOid)
	if !ok || tbl == nil || tbl.RowTypeOID == 0 {
		return 0, false
	}
	if isArr {
		return tbl.RowTypeOID - 1, true
	}
	return tbl.RowTypeOID, true
}
