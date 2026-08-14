package executor

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Fixed-width 4-byte "object identifier" storage family.
//
// PG stores every member of the object-identifier family — oid, the reg* types,
// and cid — as a 4-byte unsigned integer (typbyval, no varlena) and prints them
// with the UNSIGNED %u conversion (`oidout`/`cidout`). goopg's heap codec only
// recognised `oid` and `regproc`; the rest fell through `encodeValuePG`'s
// default and were stored as varlena TEXT, so a `regclass`/`regtype`/
// `regprocedure`/`cid` column shipped its value as text where a hosted PG 18.3
// reads a 4-byte identifier (the same feedback_pg_faithful_binary_over_text
// inversion the serial-spelling and unsigned-identifier slices already fixed for
// their own families). This file closes that gap (M0119-0006, ledger row
// 2026-08-13 — 54th slice deferral):
//
//	oid         (btoidcmp/oidin,    uint32in_subr)      — numeric input only
//	regproc     (regprocin,  regproc.c)                 — function name → OID
//	regprocedure(regprocedurein)                        — function name → OID
//	regclass    (regclassin, regproc.c)                 — relation/index name → OID
//	regtype     (regtypein,  regproc.c)                 — type name → OID
//	regrole     (regrolein,  regproc.c)                 — role name → OID
//	regcollation(regcollationin, regproc.c)             — collation name → OID
//	cid         (cidin/cidout, oid.c/command.c)         — numeric input only
//
// The name→OID half is the reg* family's INPUT function, distinct from the
// bidirectional `::reg*` cast in expr.go (which also renders OID→name). A
// numeric `KindInt` datum is already an OID and passes through; a `KindString`
// is resolved as a NAME (matching upstream `reg*in`, which does a catalog lookup
// and never a numeric parse) and raises the type's own undefined-object error on
// a miss. `regrole`/`regcollation` resolve against pg_roles/pg_collation via
// InMemory.RoleOID / CollationOIDByName (the 67th slice). regoper/
// regoperator/regnamespace/regconfig/regdictionary are deliberately NOT in this
// file: they have no name-resolution seam yet, so keeping them on the varlena
// default is less wrong than storing a numeric parse of a name (see the ledger
// row's deferred column).

// regIdentifierInput resolves a Datum to the 32-bit OID a reg* column stores —
// the input half of the reg* family's name↔OID contract, used by
// coerceRowForConstraintChecks so a bare quoted name literal
// (`INSERT INTO t(r regclass) VALUES ('mytable')`) reaches encodeValuePG as a
// KindInt OID instead of a KindString that the numeric oid arm would misparse.
//
// A KindInt datum is already an OID and returns unchanged (the heap arm's
// pgUnsignedIDFromDatum range-checks it). A KindString is resolved as a NAME:
// regclass against tables+indexes, regtype against builtin+user types,
// regproc/regprocedure against routines+builtin procs, regrole against roles,
// and regcollation against builtin+user collations, each with the undefined-
// object error upstream's reg*in raises. `oid` and `cid` are numeric and need no
// name resolution — they are not routed here.

// ── reg* name parsing (M0119-0006, 72nd slice) ─────────────────────────────
//
// Upstream feeds every reg* INPUT string through stringToQualifiedNameList →
// SplitIdentifierString (varlena.c:3581): a `"…"` segment keeps its exact case
// with adjacent `""` collapsed to a literal `"`, an unquoted segment is
// downcased, whitespace around segments is skipped, and `.` separates segments
// but is not special inside quotes. A syntax error (empty string, empty
// segment, mismatched quotes) returns false → the caller raises 42602
// "invalid name syntax". The 71st slice made RegOut quote names
// PG-faithfully; this is the input half of the same contract — without it,
// `'"MyFunc"'::regproc` reaches LookupByName with the literal quotes and fails
// 42883 (deferral ledger row 1341).

// isRegIdentSpace is C isspace() restricted to single-byte chars — the
// scanner_isspace macro SplitIdentifierString uses.
func isRegIdentSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

// splitRegIdentifiers ports SplitIdentifierString into a list of parsed
// segments. ok=false on any syntax error (empty input, an empty segment,
// mismatched quotes, or two segments run together without a separator).
func splitRegIdentifiers(s string) ([]string, bool) {
	var segs []string
	i := 0
	// Skip leading whitespace (SplitIdentifierString returns false on an
	// empty/all-whitespace string).
	for i < len(s) && isRegIdentSpace(s[i]) {
		i++
	}
	if i >= len(s) {
		return nil, false
	}
	for {
		var seg string
		if s[i] == '"' {
			// Quoted segment: preserve case, collapse "" → ", end at the first
			// closing quote not followed by another quote.
			i++ // opening quote
			var b strings.Builder
			closed := false
			for i < len(s) {
				if s[i] != '"' {
					b.WriteByte(s[i])
					i++
					continue
				}
				if i+1 < len(s) && s[i+1] == '"' {
					// Adjacent quote pair collapses to a literal quote.
					b.WriteByte('"')
					i += 2
					continue
				}
				i++ // closing quote
				closed = true
				break
			}
			if !closed {
				return nil, false // mismatched quotes
			}
			seg = b.String()
		} else {
			// Unquoted segment: extends to the separator or whitespace, then
			// downcased (the truncate_identifier NAMEDATALEN cap is irrelevant
			// in goopg).
			start := i
			for i < len(s) && s[i] != '.' && !isRegIdentSpace(s[i]) {
				i++
			}
			seg = strings.ToLower(s[start:i])
		}
		if seg == "" {
			return nil, false // empty segment — stringToQualifiedNameList rejects
		}
		segs = append(segs, seg)
		// Drop whitespace before the separator (or the end of input).
		for i < len(s) && isRegIdentSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] != '.' {
			return nil, false // expected separator, got garbage
		}
		i++ // the separator
		// Skip whitespace after the separator; a trailing separator leaves an
		// empty segment.
		for i < len(s) && isRegIdentSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			return nil, false
		}
	}
	return segs, true
}

// splitRegQualifiedName reduces splitRegIdentifiers to the 2-level
// (schema, name) contract every goopg catalog lookup uses: the first segment
// is the schema and the rest are joined with '.', so a quoted schema holding a
// dot (`"my.schema".fn`) and a 3-part regclass name (`pg_catalog.pg_class`)
// both stay resolvable (the latter measures 1259 on PG 18.3). ok=false on any
// syntax error.
func splitRegQualifiedName(s string) (schema, name string, ok bool) {
	segs, ok := splitRegIdentifiers(s)
	if !ok {
		return "", "", false
	}
	if len(segs) == 1 {
		return "", segs[0], true
	}
	return segs[0], strings.Join(segs[1:], "."), true
}

// regDisplayName renders the parsed (schema, name) pair the way upstream
// NameListToString joins the parsed segments — the form reg*in miss messages
// print with the quotes stripped (`relation "No Such Table" does not exist`,
// not the raw `"\"No Such Table\""`). regprocin/regprocedurein are the
// exception: their 42883 keeps the RAW input (errmsg "function \"%s\" does
// not exist", regproc.c:112).
func regDisplayName(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

// regprocedureNamePart strips the argument list off a regprocedurein input,
// mirroring parseNameAndArgTypes' leading scan (regproc.c:1899): everything
// before the first LEFT PAREN that is not inside double quotes. The arg list
// itself is NOT parsed — goopg resolves regprocedure names by NAME only (a
// recorded gap, see the design doc's scope exclusions) — so the `(…)` group
// is simply dropped and the name part resolves like regproc. A bare name with
// no left paren is returned unchanged: upstream raises "expected a left
// parenthesis" (22P02) there, but goopg has never enforced it and a bare name
// resolving is a pre-existing leniency, not a regression.
func regprocedureNamePart(s string) string {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case '(':
			if !inQuote {
				return s[:i]
			}
		}
	}
	return s
}

func regIdentifierInput(v Datum, typeName string, ctx *Context, pos int) (Datum, error) {
	if v.Kind != KindString {
		// numeric literal / already-OID datum — the heap arm range-checks.
		return v, nil
	}
	name := strings.TrimSpace(v.StringValue())
	// parseDashOrOid first, for every reg* type — upstream reg*in (regproc.c)
	// runs it before ANY name resolution: a literal "-" is OID 0 (InvalidOid)
	// and a pure-digit string is a numeric OID via oidin (IsOidString), never a
	// name to resolve. This closes the family-wide latent gap the 66th slice
	// left (its four types went straight to name resolution on a KindString).
	// M0119-0006 (67th slice).
	if oid, ok, err := parseRegDashOrOid(name); ok {
		if err != nil {
			return NullDatum, err
		}
		return NewIntDatum(int64(oid)), nil
	}
	if ctx == nil || ctx.Catalog == nil {
		return v, nil
	}
	switch strings.ToLower(typeName) {
	case "regclass":
		connDBOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
		schema, rel, nameOK := splitRegQualifiedName(name)
		if !nameOK {
			return NullDatum, &ExecError{Code: "42602", Pos: pos, Message: "invalid name syntax"}
		}
		objName := parser.ObjectName{Schema: schema, Name: rel}
		if tbl, found := ctx.Catalog.LookupTable(objName, connDBOid); found && tbl != nil {
			return NewIntDatum(int64(tbl.OID)), nil
		}
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			if idx, found := im.LookupIndex(objName, connDBOid); found && idx != nil {
				return NewIntDatum(int64(idx.OID)), nil
			}
		}
		// Miss message uses the STRIPPED parsed name — regclassin's
		// `relation "%s" does not exist` takes NameListToString(names), so a
		// quoted miss renders `relation "No Such Table"` not the raw quotes.
		return NullDatum, &ExecError{Code: "42P01", Pos: pos,
			Message: fmt.Sprintf("relation %q does not exist", regDisplayName(schema, rel))}
	case "regtype":
		// TypeNameToOID falls back to OIDText for any name it does not know, so
		// `oid != 0` never fires on a miss — an unknown name would resolve to
		// text's OID and the 42704 miss-path below would be dead. The established
		// idiom (castKeyTypeName, pgTypeofOIDForName) treats the fallback as a
		// miss unless the name really is `text`.
		schema, typeName, nameOK := splitRegQualifiedName(name)
		if !nameOK {
			return NullDatum, &ExecError{Code: "42602", Pos: pos, Message: "invalid name syntax"}
		}
		// regtypein (regproc.c:1176) runs parseTypeString on the raw text, so a
		// double-quoted name must be unquoted first; splitting also hands
		// userTypeOIDForName a bare name, letting a schema-qualified user type
		// resolve instead of dying as `schema.type` in a bare-name lookup.
		if oid := catalog.TypeNameToOID(typeName); oid != catalog.OIDText || strings.EqualFold(typeName, "text") {
			return NewIntDatum(int64(oid)), nil
		}
		if oid, ok := userTypeOIDForName(ctx.Catalog, typeName); ok {
			return NewIntDatum(int64(oid)), nil
		}
		return NullDatum, &ExecError{Code: "42704", Pos: pos,
			Message: fmt.Sprintf("type %q does not exist", regDisplayName(schema, typeName))}
	case "regproc", "regprocedure":
		// regprocin resolves the WHOLE input as a name; regprocedurein first
		// splits off the arg list (parseNameAndArgTypes, regproc.c:1899) and
		// resolves only the name part. Both feed the name through
		// stringToQualifiedNameList, so a quoted routine name must be unquoted
		// before the lookups. The 42883 keeps the RAW input — regprocin/
		// regprocedurein's errmsg is `function "%s" does not exist` with the
		// original string, parens and quotes included (regproc.c:112, regproc.c
		// :279) — so the message below uses `raw`, not the parsed form.
		raw := name
		if strings.EqualFold(typeName, "regprocedure") {
			name = regprocedureNamePart(name)
		}
		schema, fn, nameOK := splitRegQualifiedName(name)
		if !nameOK {
			return NullDatum, &ExecError{Code: "42602", Pos: pos, Message: "invalid name syntax"}
		}
		// Thread the connection's dbOid into the routine lookup — the 4e-series
		// routine registry keys routines by (dbOid, schema, name), so an
		// unscoped LookupByName resolves DefaultDBOid and a LIVE-created routine
		// (registered under its real dbOid) either misses or — worse — resolves
		// the same-named routine of ANOTHER database (cross-dbOid leak; deferral
		// row 1348). Mirrors the regclass arm's connDBOid threading directly
		// above. Builtins resolve through the global pg_proc index below, which
		// is not dbOid-scoped (pg_catalog is implicitly visible everywhere).
		connDBOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
		if rs := ctx.Catalog.Routines(); rs != nil {
			candidates := rs.LookupByName(parser.ObjectName{Schema: schema, Name: fn}, connDBOid)
			if len(candidates) > 0 {
				return NewIntDatum(int64(candidates[0].OID)), nil
			}
		}
		if bp, found := catalog.LookupBuiltinProc(fn); found {
			return NewIntDatum(int64(bp.OID)), nil
		}
		return NullDatum, &ExecError{Code: "42883", Pos: pos,
			Message: fmt.Sprintf("function %q does not exist", raw)}
	case "regrole":
		// regrolein (regproc.c:1541): a role name is a single identifier — a
		// qualified name (stringToQualifiedNameList list_length != 1) is 42602
		// "invalid name syntax", roles are never schema-qualified. The check is
		// on the PARSED segment count, not the raw string: a quoted role like
		// `"Alice.Bob"` keeps its dot (inside quotes it is not a separator) and
		// is a valid single-identifier role name.
		segs, nameOK := splitRegIdentifiers(name)
		if !nameOK || len(segs) != 1 {
			return NullDatum, &ExecError{Code: "42602", Pos: pos,
				Message: "invalid name syntax"}
		}
		roleName := segs[0]
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			if oid, found := im.RoleOID(roleName); found {
				return NewIntDatum(int64(oid)), nil
			}
		}
		return NullDatum, &ExecError{Code: "42704", Pos: pos,
			Message: fmt.Sprintf("role %q does not exist", roleName)}
	case "regcollation":
		// regcollationin (regproc.c:1026): possibly schema-qualified; a bare
		// name resolves builtin-then-user through the search path
		// (CollationOIDByName), a qualified name against FindCollation in that
		// schema. Miss → 42704 with the encoding name — goopg is UTF-8 only, so
		// GetDatabaseEncodingName() is the constant "UTF8". The name is parsed
		// through the shared SplitIdentifierString port, so a collation like
		// `"My Coll"` unquotes before the lookups and the miss message shows the
		// STRIPPED form (regcollationin uses NameListToString).
		schema, coll, nameOK := splitRegQualifiedName(name)
		if !nameOK {
			return NullDatum, &ExecError{Code: "42602", Pos: pos, Message: "invalid name syntax"}
		}
		var oid uint32
		if schema != "" {
			if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
				if uc := im.FindCollation(coll, schema, ctx.CurrentDatabaseOid); uc != nil {
					oid = uc.OID
				}
			}
		} else if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			oid = im.CollationOIDByName(coll)
		}
		if oid != 0 {
			return NewIntDatum(int64(oid)), nil
		}
		return NullDatum, &ExecError{Code: "42704", Pos: pos,
			Message: fmt.Sprintf("collation %q for encoding %q does not exist", regDisplayName(schema, coll), "UTF8")}
	}
	return v, nil
}

// parseRegDashOrOid mirrors upstream parseDashOrOid (regproc.c:1865-1882): a
// literal "-" is OID 0 (InvalidOid), and a pure-digit string is a numeric OID
// parsed like oidin (IsOidString). ok=false means the string is neither — the
// caller falls through to NAME resolution. A pure-digit string that overflows
// uint32 returns ok=true with the 22003 error oidin raises, so it never falls
// through to name resolution (and the message names "oid" exactly as PG's
// oidin_cstr does, even for a regrole/regcollation column).
func parseRegDashOrOid(s string) (uint32, bool, error) {
	if s == "-" {
		return 0, true, nil
	}
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
			Message: fmt.Sprintf("value %q is out of range for type oid", s)}
	}
	return uint32(n), true, nil
}

// regIdentifierOIDFromDatum mirrors pgUnsignedIDFromDatum but is scoped to the
// reg*/cid family's shared 4-byte layout and passes the actual type name into
// the 22003 range message (so a `cid` overflow says "cid", not "oid"). Used by
// the heap encode arm so `oid`/`regproc`/`regprocedure`/`regclass`/`regtype`/
// `cid` all store through one path.
func regIdentifierOIDFromDatum(d Datum, typeName string) (uint32, error) {
	var v int64
	switch d.Kind {
	case KindInt:
		v = d.Int
	case KindString:
		parsed, err := coerceStringToInt64(d.StringValue(), typeName)
		if err != nil {
			return 0, err
		}
		v = parsed
	default:
		return 0, fmt.Errorf("expected int for %s, got kind %d", typeName, d.Kind)
	}
	if v < math.MinInt32 || v > math.MaxUint32 {
		return 0, &ExecError{Code: "22003",
			Message: fmt.Sprintf("value %q is out of range for type %s", strings.TrimSpace(d.Format()), typeName)}
	}
	return uint32(v), nil
}

// RegOut renders a reg* OID as its object's name — the OUTPUT half of the reg*
// family's name↔OID contract, one switch implementing every reg*out in
// postgres/src/backend/utils/adt/regproc.c. It is the SINGLE OID→name renderer
// shared by the SELECT wire path (internal/server.appendTypedCellText) and the
// COPY TEXT/CSV TO path (datumToCopyText), so the two cannot drift apart again
// (Hard-won Rule #2, pattern_sibling_paths_must_agree). M0119-0006 (68th slice).
//
// Every reg*out has the same three-verdict shape: OID 0 (InvalidOid) renders
// "-"; a catalog hit renders the object's name — ALWAYS quote_identifier'd, and
// schema-qualified (quote_qualified_identifier, ruleutils.c) when qualify is
// set, i.e. the object's schema is not on the session's effective search_path
// (pg_catalog objects are implicitly visible and so never qualify; regtype keeps
// its own RegtypeName/format_type_be path, regprocedure the format_procedure
// path); anything else renders the numeric OID. A nil cat (or a failed
// *InMemory assertion) skips the lookups and falls through to the numeric form,
// preserving the no-catalog behavior of every call site that predates the
// catalog threading. M0119-0006 (69th slice) added the quoting + qualification
// to the regclass/regproc/regrole/regcollation arms. dbOid scopes the
// database-local relation lookups (pg_class) to a specific database namespace;
// empty keeps the DefaultDBOid default. The reg* cast path (evalCastTyped's
// M0119-0006 guard) passes the connection's real database OID so an OID that
// belongs to another database never renders that database's relation name — the
// same connDBOid scoping the `oid::regclass` CastExpr arm threads (expr.go,
// M0122-0007 4e follow-up 33).
func RegOut(typeName string, oid uint32, cat catalog.Catalog, qualify bool, dbOid ...uint32) string {
	return regOutShared(typeName, oid, cat, func(s string) bool { return qualify }, qualify, nil, dbOid...)
}

// RegOutArgVisible is RegOut with an extra per-arg-type visibility predicate
// for the regprocedure ARGLIST (deferral row 1342). The production SELECT/COPY/
// cast paths pass a closure over the session's search_path
// (executor.RegObjectSchemaVisible); nil keeps RegOut's bare-arglist behavior.
// Name qualification is unchanged — the `qualify` flag still decides whether
// the routine NAME is schema-qualified (quote_qualified_identifier), exactly
// as format_procedure's FunctionIsVisible arm.
func RegOutArgVisible(typeName string, oid uint32, cat catalog.Catalog, qualify bool, argVisible func(schema string) bool, dbOid ...uint32) string {
	return regOutShared(typeName, oid, cat, func(s string) bool { return qualify }, qualify, argVisible, dbOid...)
}

// RegOutRenderer returns a closure over RegOut bound to a fixed catalog and
// qualification flag, so a caller that has no catalog/qualify at hand (the
// logical-replication pgoutput decoder in internal/wal) can still render a reg*
// value's NAME. This mirrors upstream's text-mode pgoutput column serialization:
// logicalrep_write_typ calls OidOutputFunctionCall(typclass->typoutput, ...)
// (postgres/src/backend/replication/logical/proto.c:848) and a reg* type's
// typoutput is regclassout/regprocout/... which converts the OID to a name
// (regproc.c:940) — the 4-byte-OID form belongs to BINARY mode's typsend. The
// closure keeps RegOut the single source of truth for the name resolution.
// M0119-0006 (deferral row 1353).
func RegOutRenderer(cat catalog.Catalog, qualify bool, dbOid ...uint32) func(typeName string, oid uint32) string {
	return func(typeName string, oid uint32) string {
		return RegOut(typeName, oid, cat, qualify, dbOid...)
	}
}

// RegOutRendererVisible is RegOutRenderer with a per-schema VISIBILITY
// predicate in place of the fixed qualify flag: a resolved regclass/regproc/
// regprocedure/regcollation name is schema-qualified iff its schema is NOT
// visible per `visible`, exactly as regclassout/regprocout/regcollationout
// qualify via RelationIsVisible & friends (regproc.c:973-981,
// postgres/src/backend/catalog/namespace.c) — the object's schema off the
// session's effective search_path. The logical-replication walsender binds this
// with the default-search_path predicate `{"", pg_catalog, public}` since the
// publisher side never SETs search_path; pg_catalog is implicitly visible to
// every path and regOutQualified force-disables its qualification anyway. The
// regtype arm keeps its fixed bool (false here): userTypeNameForOID hardcodes a
// "public." prefix and tracks no real schema (a separate pre-existing defect,
// not covered by deferral row 1354's two claims). M0119-0006 (84th slice).
func RegOutRendererVisible(cat catalog.Catalog, visible func(schema string) bool, dbOid ...uint32) func(typeName string, oid uint32) string {
	return func(typeName string, oid uint32) string {
		return regOutShared(typeName, oid, cat, func(s string) bool { return !visible(s) }, false, nil, dbOid...)
	}
}

// regOutShared is the shared RegOut/RegOutArgVisible/RegOutRendererVisible
// body. qualify is a PER-OBJECT predicate "should this object's schema be
// schema-qualified?" — regclassout/regprocout/regprocedure/regcollationout
// qualify iff the schema is NOT on the session's effective search_path
// (RelationIsVisible & friends, postgres/src/backend/catalog/namespace.c). The
// RegOut/RegOutArgVisible wrappers pass the constant predicate
// `func(string) bool { return qualify }` (the fixed flag those callers
// precompute), RegOutRendererVisible passes `func(s string) bool {
// return !visible(s) }`. regtypeQualify is the SEPARATE fixed flag for the
// regtype arm only (the walsender renderer keeps it false; userTypeNameForOID
// hardcodes a "public." prefix and tracks no real schema). argVisible threads
// the per-arg visibility predicate to the regprocedure arm only (nil ⇒ bare
// arglist, the base-RegOut behavior). dbOid scopes the database-local relation
// lookups (see RegOut).
func regOutShared(typeName string, oid uint32, cat catalog.Catalog, qualify func(schema string) bool, regtypeQualify bool, argVisible func(schema string) bool, dbOid ...uint32) string {
	if oid == 0 {
		return "-"
	}
	im, hasIM := cat.(*catalog.InMemory)
	switch strings.ToLower(typeName) {
	case "regclass":
		if hasIM {
			// dbOid-scope the pg_class lookup so a relation owned by ANOTHER
			// database's namespace never renders its name from this connection
			// (TestRegclassCastScopedToConnectionDBOid; mirror of the
			// `oid::regclass` CastExpr arm's connDBOid, M0122-0007 4e follow-up
			// 33). Empty dbOid keeps the DefaultDBOid default for the SELECT/COPY
			// siblings, whose callers predate the scoping.
			if tbl, ok := im.LookupTableByOID(oid, dbOid...); ok {
				return regOutQualified(tbl.Schema, tbl.Name, qualify(tbl.Schema))
			}
			if idx, ok := im.LookupIndexByOID(oid, dbOid...); ok {
				return regOutQualified(idx.Schema, idx.Name, qualify(idx.Schema))
			}
			// A synthetic TOAST relation OID (parent OID + 100M) or TOAST index
			// OID (+200M) lives only in the virtual pg_class builder, not
			// c.tables/c.indexes, so resolve it like the `oid::regclass` CastExpr
			// arm does (expr.go:826-828): ToastRelName returns the already
			// schema-qualified pg_toast.pg_toast_<oid>[_index] name, which we
			// return verbatim (never routed through regOutQualified) to keep the
			// two paths byte-identical. M0119-0006 (deferral row L1305).
			if name, found := im.ToastRelName(oid, dbOid...); found {
				return name
			}
		}
	case "regproc":
		if name, ok := catalog.RegprocName(oid); ok {
			// A builtin proc lives in pg_catalog, which the search path always
			// searches implicitly, so it is never schema-qualified — only the
			// name is quote_identifier'd, as regprocout does for every name.
			return pgQuoteIdent(name)
		}
		if cat != nil {
			if rs := cat.Routines(); rs != nil {
				if r := rs.LookupByOID(oid); r != nil {
					return regOutQualified(r.Schema, r.Name, qualify(r.Schema))
				}
			}
		}
	case "regprocedure":
		// format_procedure (regproc.c:326): the routine NAME is ALWAYS
		// quote_identifier'd, and schema-qualified via
		// quote_qualified_identifier when the routine is NOT visible on the
		// session's effective search_path (FunctionIsVisible) — the `qualify`
		// flag the COPY/SELECT paths compute. The argument list (format_type_be)
		// is appended UNQUOTED — qualifying only the NAME, never the parens
		// (the 69th slice left them unquoted deliberately; pgQuoteIdent on
		// "int4out(integer)" would wrongly quote them). A builtin resolves in
		// pg_catalog, which every search_path searches implicitly, so it is
		// never qualified — only the bare quoted name. M0119-0006 (71st slice,
		// deferral row 1338).
		var routines *catalog.Routines
		if cat != nil {
			routines = cat.Routines()
		}
		if schema, name, argTypes, ok := catalog.RegprocedureNameParts(oid, routines); ok {
			// format_procedure_extended appends each input arg type through
			// format_type_be (regproc.c:326), which schema-qualifies a type
			// whose namespace is off the session's effective search_path —
			// regprocedureArglist implements that per-arg rule (deferral row
			// 1342). Base RegOut passes a nil predicate (bare arglist).
			return regOutQualified(schema, name, qualify(schema)) + "(" + regprocedureArglist(argTypes, argVisible) + ")"
		}
	case "regtype":
		return RegtypeName(cat, oid, regtypeQualify)
	case "regrole":
		if hasIM {
			// regroleout (regproc.c:1609) quote_identifiers the REAL role name
			// (roles are never schema-qualified) but emits a dangling OID as the
			// unquoted %u fallback — RoleNameAtOID distinguishes the two, since
			// RoleNameForOID renders a dangling role numerically too.
			if n, ok := im.RoleNameAtOID(oid); ok {
				return pgQuoteIdent(n)
			}
		}
	case "regcollation":
		if hasIM {
			if n := im.ResolveIndexColumnCollationName(oid); n != "" {
				for _, uc := range im.ListUserCollations() {
					if uc.OID == oid {
						// A user collation: upstream regcollationout qualifies
						// with the collation's ACTUAL namespace
						// (regproc.c:1123 get_namespace_name(collnamespace)) when
						// the search path does not show it. The 69th slice
						// hardcoded "public" — right for a default-session
						// creation schema, wrong for any non-public CREATE
						// COLLATION schema (deferral row 1339). regOutQualified
						// also keeps the pg_catalog-never-qualifies arm for a
						// collation created in pg_catalog.
						collSchema := im.SchemaNameForOID(uc.NamespaceOID)
						return regOutQualified(collSchema, n, qualify(collSchema))
					}
				}
				// A builtin collation resolves in pg_catalog, which every search
				// path searches implicitly — never qualified, only the
				// quote_identifier'd bare name.
				return pgQuoteIdent(n)
			}
		}
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// regOutQualified applies the reg*out family's schema-qualification and quoting
// rule to a resolved object name. Every reg*out runs the name through
// quote_qualified_identifier (regproc.c: regclassout→989, regprocout→184), so
// the name is ALWAYS quote_identifier'd even when it is not schema-qualified;
// quoteQualifiedIdentifier does exactly that (bare identifier when the qualifier
// is empty). qualify is true when the object's schema is NOT on the session's
// effective search_path — the flag the COPY/SELECT paths compute from the
// search_path GUC (regObjectSchemaVisible / publicSchemaVisible). pg_catalog is
// implicitly searched by every search_path, so an object there is never
// qualified (mirroring RelationIsVisible/CollationIsVisible's implicit
// pg_catalog arm). An empty schema (a catalog that never set it) defaults to
// "public", matching userTypeNameForOID's hardcoded prefix. M0119-0006 (69th
// slice).
func regOutQualified(schema, name string, qualify bool) string {
	if schema == "" {
		schema = "public"
	}
	if schema == "pg_catalog" {
		qualify = false
	}
	if !qualify {
		return pgQuoteIdent(name)
	}
	return quoteQualifiedIdentifier(schema, name)
}

// regprocedureArglist renders format_type_be for each INPUT arg type
// (regproc.c format_procedure_extended), schema-qualifying per-arg via the
// session's search_path (deferral row 1342). pg_catalog (builtin) and
// unknown-"" arg types render the bare SQL alias (format_type_be's builtin
// switch — ArgTypeDisplayAlias); a user type whose schema is NOT visible
// renders quote_qualified_identifier(schema, name), the same quote rule the
// family's name path uses (regOutQualified). nil visible ⇒ every schema is
// treated as visible (bare arglist), preserving the base RegOut callers. The
// array suffix is split off BEFORE aliasing/quoting and re-appended after, so
// `offpath."mytype[]"` never happens — the ELEMENT is quoted, `[]` follows
// unquoted, exactly like format_type_be's array arm.
func regprocedureArglist(argTypes []catalog.RegprocArg, visible func(schema string) bool) string {
	args := make([]string, len(argTypes))
	for i, a := range argTypes {
		base, isArray := splitArraySuffix(a.Name)
		name := base
		if a.Schema == "" || a.Schema == "pg_catalog" {
			name = catalog.ArgTypeDisplayAlias(base)
		}
		if a.Schema != "" && a.Schema != "pg_catalog" && visible != nil && !visible(a.Schema) {
			name = quoteQualifiedIdentifier(a.Schema, name)
		}
		if isArray {
			name += "[]"
		}
		args[i] = name
	}
	return strings.Join(args, ",")
}

// splitArraySuffix reports whether name carries the baked-in array suffix
// routineArgTypeName appends ("mytype[]") and returns the element name without
// it. Only ONE trailing "[]" is ever present (the parser stores a single
// IsArray; the suffix is appended once), so a plain HasSuffix check suffices.
func splitArraySuffix(name string) (base string, isArray bool) {
	if strings.HasSuffix(name, "[]") {
		return name[:len(name)-2], true
	}
	return name, false
}

// isRegIdentifierTypeName reports whether name is one of the six reg* types
// with a name→OID seam (regproc/regprocedure/regclass/regtype/regrole/
// regcollation). `oid` and `cid` are numeric-only and deliberately excluded.
// Shared by the COPY FROM coerce filter and the COPY TO renderer guard so the
// two COPY directions agree on the family; the encode/align arms (codec.go)
// keep their own WIDER list because they also cover oid/cid. M0119-0006 (68th
// slice).
func isRegIdentifierTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "regproc", "regprocedure", "regclass", "regtype", "regrole", "regcollation":
		return true
	}
	return false
}
