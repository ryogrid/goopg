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
		schema, rel := splitQualifiedTable(name)
		objName := parser.ObjectName{Schema: schema, Name: rel}
		if tbl, found := ctx.Catalog.LookupTable(objName, connDBOid); found && tbl != nil {
			return NewIntDatum(int64(tbl.OID)), nil
		}
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			if idx, found := im.LookupIndex(objName, connDBOid); found && idx != nil {
				return NewIntDatum(int64(idx.OID)), nil
			}
		}
		return NullDatum, &ExecError{Code: "42P01", Pos: pos,
			Message: fmt.Sprintf("relation %q does not exist", name)}
	case "regtype":
		// TypeNameToOID falls back to OIDText for any name it does not know, so
		// `oid != 0` never fires on a miss — an unknown name would resolve to
		// text's OID and the 42704 miss-path below would be dead. The established
		// idiom (castKeyTypeName, pgTypeofOIDForName) treats the fallback as a
		// miss unless the name really is `text`.
		if oid := catalog.TypeNameToOID(name); oid != catalog.OIDText || strings.EqualFold(name, "text") {
			return NewIntDatum(int64(oid)), nil
		}
		if oid, ok := userTypeOIDForName(ctx.Catalog, name); ok {
			return NewIntDatum(int64(oid)), nil
		}
		return NullDatum, &ExecError{Code: "42704", Pos: pos,
			Message: fmt.Sprintf("type %q does not exist", name)}
	case "regproc", "regprocedure":
		schema, fn := splitQualifiedTable(strings.ToLower(name))
		if rs := ctx.Catalog.Routines(); rs != nil {
			candidates := rs.LookupByName(parser.ObjectName{Schema: schema, Name: fn})
			if len(candidates) > 0 {
				return NewIntDatum(int64(candidates[0].OID)), nil
			}
		}
		if bp, found := catalog.LookupBuiltinProc(fn); found {
			return NewIntDatum(int64(bp.OID)), nil
		}
		return NullDatum, &ExecError{Code: "42883", Pos: pos,
			Message: fmt.Sprintf("function %q does not exist", name)}
	case "regrole":
		// regrolein (regproc.c:1541): a role name is a single identifier — a
		// qualified name (stringToQualifiedNameList list_length != 1) is 42602
		// "invalid name syntax", roles are never schema-qualified.
		if strings.Contains(name, ".") {
			return NullDatum, &ExecError{Code: "42602", Pos: pos,
				Message: "invalid name syntax"}
		}
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			if oid, found := im.RoleOID(name); found {
				return NewIntDatum(int64(oid)), nil
			}
		}
		return NullDatum, &ExecError{Code: "42704", Pos: pos,
			Message: fmt.Sprintf("role %q does not exist", name)}
	case "regcollation":
		// regcollationin (regproc.c:1026): possibly schema-qualified; a bare
		// name resolves builtin-then-user through the search path
		// (CollationOIDByName), a qualified name against FindCollation in that
		// schema. Miss → 42704 with the encoding name — goopg is UTF-8 only, so
		// GetDatabaseEncodingName() is the constant "UTF8".
		var oid uint32
		if schema, coll := splitQualifiedTable(name); schema != "" {
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
			Message: fmt.Sprintf("collation %q for encoding %q does not exist", name, "UTF8")}
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
// to the regclass/regproc/regrole/regcollation arms.
func RegOut(typeName string, oid uint32, cat catalog.Catalog, qualify bool) string {
	if oid == 0 {
		return "-"
	}
	im, hasIM := cat.(*catalog.InMemory)
	switch strings.ToLower(typeName) {
	case "regclass":
		if hasIM {
			if tbl, ok := im.LookupTableByOID(oid); ok {
				return regOutQualified(tbl.Schema, tbl.Name, qualify)
			}
			if idx, ok := im.LookupIndexByOID(oid); ok {
				return regOutQualified(idx.Schema, idx.Name, qualify)
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
					return regOutQualified(r.Schema, r.Name, qualify)
				}
			}
		}
	case "regprocedure":
		// RegprocedureName is nil-safe for routines; mirror appendTypedCellText,
		// which passes nil when the catalog is nil. format_procedure's signature
		// qualification (public.myfunc() when the proc is off the search path) is
		// a separate machinery — see the deferral ledger row (69th slice).
		var routines *catalog.Routines
		if cat != nil {
			routines = cat.Routines()
		}
		if sig, ok := catalog.RegprocedureName(oid, routines); ok {
			return sig
		}
	case "regtype":
		return RegtypeName(cat, oid, qualify)
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
						return regOutQualified(im.SchemaNameForOID(uc.NamespaceOID), n, qualify)
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
