package executor

import (
	"fmt"
	"math"
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
//	cid         (cidin/cidout, oid.c/command.c)         — numeric input only
//
// The name→OID half is the reg* family's INPUT function, distinct from the
// bidirectional `::reg*` cast in expr.go (which also renders OID→name). A
// numeric `KindInt` datum is already an OID and passes through; a `KindString`
// is resolved as a NAME (matching upstream `reg*in`, which does a catalog lookup
// and never a numeric parse) and raises the type's own undefined-object error on
// a miss. regrole/regcollation/regoper/regoperator/regnamespace/regconfig/
// regdictionary are deliberately NOT in this file: they have no name-resolution
// seam yet, so keeping them on the varlena default is less wrong than storing a
// numeric parse of a name (see the ledger row's deferred column).

// regIdentifierInput resolves a Datum to the 32-bit OID a reg* column stores —
// the input half of the reg* family's name↔OID contract, used by
// coerceRowForConstraintChecks so a bare quoted name literal
// (`INSERT INTO t(r regclass) VALUES ('mytable')`) reaches encodeValuePG as a
// KindInt OID instead of a KindString that the numeric oid arm would misparse.
//
// A KindInt datum is already an OID and returns unchanged (the heap arm's
// pgUnsignedIDFromDatum range-checks it). A KindString is resolved as a NAME:
// regclass against tables+indexes, regtype against builtin+user types, and
// regproc/regprocedure against routines+builtin procs, each with the undefined-
// object error upstream's reg*in raises. `oid` and `cid` are numeric and need no
// name resolution — they are not routed here.
func regIdentifierInput(v Datum, typeName string, ctx *Context, pos int) (Datum, error) {
	if v.Kind != KindString {
		// numeric literal / already-OID datum — the heap arm range-checks.
		return v, nil
	}
	name := strings.TrimSpace(v.StringValue())
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
	}
	return v, nil
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
