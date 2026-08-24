package executor

// operators_pg_input_error_info.go — pg_input_error_info(value, type) SRF.
// Returns 1 row in every case: all four columns NULL if the input is valid for
// the type, or (message, detail, hint, sql_error_code) if it is invalid.
// Upstream pg_input_error_info (postgres/src/backend/utils/adt/misc.c:715) sets
// every isnull[i] true on the valid path and heap_forms one tuple regardless, so
// it never returns zero rows — the caller must test `message IS NULL`, not count
// rows. Used by the pg_regress int2 test and other type validation tests.
// M0097-0003, M0119-0006.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

type pgInputErrorInfoOp struct {
	plan *optimizer.PgInputErrorInfo
	ctx  *Context
	done bool
}

func newPgInputErrorInfoOp(p *optimizer.PgInputErrorInfo) *pgInputErrorInfoOp {
	return &pgInputErrorInfoOp{plan: p}
}

func (o *pgInputErrorInfoOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *pgInputErrorInfoOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *pgInputErrorInfoOp) Close() error { return nil }

func (o *pgInputErrorInfoOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true

	// Evaluate the arguments.
	valDatum, err := evalExpr(o.plan.Value, nil, o.ctx)
	if err != nil || valDatum.IsNull() {
		return nil, EOF
	}
	typDatum, err := evalExpr(o.plan.Type, nil, o.ctx)
	if err != nil || typDatum.IsNull() {
		return nil, EOF
	}

	v := strings.TrimSpace(valDatum.StringValue())
	t := strings.ToLower(strings.TrimSpace(typDatum.StringValue()))

	// Validate the input against the type.
	var message, sqlCode string

	switch t {
	case "time":
		// Validate time strings using the same parser as column INSERT. M0097-0004.
		_, e := parseTimeString(v)
		if e != nil {
			if ee, ok := e.(*ExecError); ok {
				message = ee.Message
				sqlCode = ee.Code
			} else {
				message = e.Error()
				sqlCode = "22007"
			}
		}
	case "timetz":
		// Validate timetz strings with timezone-aware parser. M0097-0004.
		_, _, e := parseTimeTZString(v, "")
		if e != nil {
			if ee, ok := e.(*ExecError); ok {
				message = ee.Message
				sqlCode = ee.Code
			} else {
				message = e.Error()
				sqlCode = "22007"
			}
		}
	case "bool", "boolean":
		if !isValidBoolInput(v) {
			message = "invalid input syntax for type boolean: \"" + v + "\""
			sqlCode = "22P02"
		}
	case "int2", "smallint":
		_, e := parseIntegerInput(v, "smallint", 16)
		if e != nil {
			if ee, ok := e.(*ExecError); ok {
				message = ee.Message
				sqlCode = ee.Code
			}
		}
	case "int4", "integer", "int":
		_, e := parseIntegerInput(v, "integer", 32)
		if e != nil {
			if ee, ok := e.(*ExecError); ok {
				message = ee.Message
				sqlCode = ee.Code
			}
		}
	case "int8", "bigint":
		_, e := parseIntegerInput(v, "bigint", 64)
		if e != nil {
			if ee, ok := e.(*ExecError); ok {
				message = ee.Message
				sqlCode = ee.Code
			}
		}
	case "float4", "real":
		if _, ferr := strconv.ParseFloat(v, 32); ferr != nil {
			message = "invalid input syntax for type real: \"" + v + "\""
			sqlCode = "22P02"
		}
	case "float8", "double precision":
		if _, ferr := strconv.ParseFloat(v, 64); ferr != nil {
			message = "invalid input syntax for type double precision: \"" + v + "\""
			sqlCode = "22P02"
		}
	case "oid":
		// For a single oid, PostgreSQL reports the full input string on error.
		// Use parseIntegerInput which preserves the original value in error messages.
		n, e := parseIntegerInput(v, "oid", 64)
		if e != nil {
			if ee, ok := e.(*ExecError); ok {
				message = ee.Message
				sqlCode = ee.Code
			}
		} else {
			if n < 0 {
				n += 4294967296
			}
			if n < 0 || n > 4294967295 {
				message = "value \"" + v + "\" is out of range for type oid"
				sqlCode = "22003"
			}
		}
	case "oidvector":
		// oidvector: space-separated oid values. M0097-0003.
		errMsg, errCode := validateOidVector(v)
		if errMsg != "" {
			message = errMsg
			sqlCode = errCode
		}
	case "uuid":
		// uuid: validate format. M0097-0003.
		if !isValidUUIDStr(v) {
			message = "invalid input syntax for type uuid: \"" + v + "\""
			sqlCode = "22P02"
		}
	case "pg_lsn":
		if _, err := parsePgLSN(v); err != nil {
			message = "invalid input syntax for type pg_lsn: \"" + v + "\""
			sqlCode = "22P02"
		}
	case "tid":
		// tid: validate "(block,offset)" via tidin semantics. M0097-0036.
		if _, _, ok := parseTidInput(v); !ok {
			message = "invalid input syntax for type tid: \"" + v + "\""
			sqlCode = "22P02"
		}
	case "line":
		// line: agrees with the typed-literal/coercion path, same
		// parseLineLiteral, including its SQLSTATE-differentiated overflow
		// (22003) and semantic-validation (A/B-zero, non-distinct-points)
		// error text. M0134-0136.
		if _, _, _, lerr := parseLineLiteral(v); lerr != nil {
			message = lerr.Message
			sqlCode = lerr.Code
		}
	case "lseg":
		// lseg: agrees with the column-coercion path, same parseLsegLiteral.
		// M0134-0137.
		if _, _, _, _, lerr := parseLsegLiteral(v); lerr != nil {
			message = lerr.Message
			sqlCode = lerr.Code
		}
	case "path":
		// path: agrees with the column-coercion path, same parsePathLiteral.
		// M0134-0149.
		if _, _, perr := parsePathLiteral(v); perr != nil {
			message = perr.Message
			sqlCode = perr.Code
		}
	case "point":
		// point: agrees with the column-coercion path, same
		// parsePointLiteral. M0134-0150.
		if _, _, perr := parsePointLiteral(v); perr != nil {
			message = perr.Message
			sqlCode = perr.Code
		}
	case "macaddr":
		// macaddr: agrees with the column-coercion path, same
		// parseMacaddrLiteral. M0134-0138.
		if _, _, _, _, _, _, merr := parseMacaddrLiteral(v); merr != nil {
			message = merr.Message
			sqlCode = merr.Code
		}
	case "macaddr8":
		// macaddr8: agrees with the column-coercion path, same
		// parseMacaddr8Literal. M0134-0139.
		if _, _, _, _, _, _, _, _, merr := parseMacaddr8Literal(v); merr != nil {
			message = merr.Message
			sqlCode = merr.Code
		}
	case "int2vector":
		// int2vector: space-separated int2 values. Validate each.
		message, sqlCode = validateInt2Vector(v)
	case "bytea":
		// bytea: reuse byteaIn (bytea.go, mirrors byteain() in varlena.c) so the
		// hex/escape error text and SQLSTATE (22023/22P02) stay identical to the
		// CAST path. M0134-0070.
		if _, e := byteaIn(v, 0); e != nil {
			if ee, ok := e.(*ExecError); ok {
				message = ee.Message
				sqlCode = ee.Code
			} else {
				message = e.Error()
				sqlCode = "22P02"
			}
		}
	default:
		// varchar(N) / character varying(N) / char(N) — length validation. M0097-0003.
		var im *catalog.InMemory
		if c, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			im = c
		}
		message, sqlCode = validateTypedLen(v, t, im, o.ctx.CurrentDatabaseOid)
		// Check if it's a registered enum type. M0097-0071.
		if message == "" {
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				if et, isEnum := im.LookupEnum(t); isEnum {
					valid := false
					for _, ev := range et.Values {
						if strings.EqualFold(ev.Label, v) {
							valid = true
							break
						}
					}
					if !valid {
						message = fmt.Sprintf("invalid input value for enum %s: %q", et.Name, v)
						sqlCode = "22P02"
					}
				}
			}
		}
	}

	if message == "" {
		// Valid input — return one row whose four columns are all NULL, exactly
		// as upstream's misc.c:731-733 memsets isnull[0..3] before heap_forming
		// the single tuple. A caller that counts rows (rather than testing
		// `message IS NULL`) would otherwise see the opposite answer from PG.
		row := Row{
			NullDatum, // message
			NullDatum, // detail
			NullDatum, // hint
			NullDatum, // sql_error_code
		}
		return SlotFromRow(nil, row), nil
	}

	// Return 1 row with the error info.
	row := Row{
		NewStringDatum(message),
		NullDatum, // detail
		NullDatum, // hint
		NewStringDatum(sqlCode),
	}
	return SlotFromRow(nil, row), nil
}

// validateOidDecimal parses a single OID string using decimal-only parsing,
// mimicking PostgreSQL's strtoul(s, &end, 10). Reports the invalid suffix
// validateTypedLen validates a string value against a length-constrained type
// like varchar(N), char(N), character varying(N). Returns (message, code) on
// error, ("", "") if valid. M0097-0003.
//
// The type text is RESOLVED rather than prefix-matched: the name is parsed the
// way a `::`-cast resolves it (schema qualification dropped, a `(N)` typmod
// parsed with whitespace tolerated, user domains followed to their base type),
// then the width check is driven off the resolved catalog.Type so it shares
// coerceTextLikeDatum's rule outright (Hard-won Rule #2). M0119-0006 60th slice.
func validateTypedLen(v, typStr string, im *catalog.InMemory, dbOid uint32) (string, string) {
	name, typmod, hasTypmod := parseTypeNameAndTypmod(typStr)
	if name == "" {
		return "", ""
	}
	typ, ok := resolveLengthType(name, im, dbOid, typmod, hasTypmod)
	if !ok {
		return "", ""
	}
	if _, err := coerceTextLikeDatum(typ, NewStringDatum(v)); err != nil {
		if ee, ok := err.(*ExecError); ok {
			return ee.Message, ee.Code
		}
		return err.Error(), "22001"
	}
	return "", ""
}

// parseTypeNameAndTypmod splits a (lowercased, trimmed) type string into its
// bare name and an optional `(N)` length typmod. It tolerates the spellings the
// old prefix match missed: a schema qualification (`pg_catalog.varchar(5)`) and
// whitespace between the name and `(` (`varchar (5)`). A multi-argument typmod
// (`numeric(10,2)`) or a non-positive/absent length leaves hasTypmod false and
// the name untouched. M0119-0006.
func parseTypeNameAndTypmod(typStr string) (name string, typmod int, hasTypmod bool) {
	s := typStr
	if strings.HasSuffix(s, ")") {
		if i := strings.LastIndex(s, "("); i > 0 {
			mid := strings.TrimSpace(s[i+1 : len(s)-1])
			if n, err := strconv.Atoi(mid); err == nil && n > 0 {
				return stripTypeSchema(strings.TrimSpace(s[:i])), n, true
			}
		}
	}
	return stripTypeSchema(s), 0, false
}

// stripTypeSchema drops a leading schema qualification (`pg_catalog.varchar`,
// `public.mydomain`) down to the bare type/domain name. Built-in type names and
// domain identifiers never contain a dot, so "after the last dot" is exact.
func stripTypeSchema(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// resolveLengthType maps a resolved (bare, lowercased) type name to the
// catalog.Type coerceTextLikeDatum checks, applying the grammar's implicit
// length defaults the same way the cast/column-type path does: bare
// `char`/`character` are character(1), bare `bpchar` and `varchar` are
// unbounded. A user domain overrides the built-in name (its Base already
// carries the resolved type and its own typmod). ok is false for any type that
// is not length-constrained text. M0119-0006.
func resolveLengthType(name string, im *catalog.InMemory, dbOid uint32, typmod int, hasTypmod bool) (catalog.Type, bool) {
	if im != nil {
		if dom, isDomain := im.LookupDomain(name, dbOid); isDomain {
			// The base type already carries its declared length; a domain name
			// has no further typmod of its own.
			return dom.Base, true
		}
	}
	oid := catalog.TypeNameToOID(name)
	switch oid {
	case catalog.OIDVarChar:
		t := catalog.Type{Name: "varchar"}
		if hasTypmod {
			t.Args = []int64{int64(typmod)}
		}
		return t, true
	case catalog.OIDBpChar:
		// `char`/`character` are grammar-synthesized to bpchar with typmod 1
		// (which is why coerceTextLikeDatum defaults them to 1 when Args is
		// empty); bare `bpchar` spelled directly is typmod -1 (unbounded).
		if name == "char" || name == "character" {
			t := catalog.Type{Name: "char"}
			if hasTypmod {
				t.Args = []int64{int64(typmod)}
			} else {
				t.Args = []int64{1}
			}
			return t, true
		}
		t := catalog.Type{Name: "bpchar"}
		if hasTypmod {
			t.Args = []int64{int64(typmod)}
		}
		return t, true
	default:
		return catalog.Type{}, false
	}
}

// (what "end" points to) in error messages, matching PG's exact output.
// M0097-0003.
func validateOidDecimal(v string) (string, string) {
	s := strings.TrimSpace(v)
	if s == "" {
		return "invalid input syntax for type oid: \"" + v + "\"", "22P02"
	}
	// Consume decimal digits only (base 10, like PostgreSQL's strtoul).
	i := 0
	var n int64
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		digit := int64(s[i] - '0')
		// Check for overflow: PostgreSQL reports "out of range" for values > UINT32_MAX.
		if n > (4294967295-digit)/10 {
			return "value \"" + s + "\" is out of range for type oid", "22003"
		}
		n = n*10 + digit
		i++
	}
	if i == 0 {
		// No digits parsed at all → invalid syntax; report the full string.
		return "invalid input syntax for type oid: \"" + v + "\"", "22P02"
	}
	suffix := strings.TrimSpace(s[i:])
	if suffix != "" {
		// Trailing garbage after valid digits → report the suffix.
		return "invalid input syntax for type oid: \"" + suffix + "\"", "22P02"
	}
	if n > 4294967295 {
		return "value \"" + s + "\" is out of range for type oid", "22003"
	}
	return "", "" // valid
}

// validateOidVector validates a space-separated list of oid values.
// Returns (errorMessage, sqlCode) if invalid, ("", "") if valid. M0097-0003.
func validateOidVector(v string) (string, string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ""
	}
	parts := strings.Fields(v)
	for _, p := range parts {
		// Use decimal-only parsing (like PostgreSQL's oidvectorin). M0097-0003.
		msg, code := validateOidDecimal(p)
		if msg != "" {
			return msg, code
		}
	}
	return "", ""
}

// validateInt2Vector validates a space-separated list of int2 values.
// Returns (errorMessage, sqlErrorCode) if invalid, ("", "") if valid.
func validateInt2Vector(v string) (string, string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ""
	}
	parts := strings.Fields(v)
	for _, p := range parts {
		_, err := parseIntegerInput(p, "smallint", 16)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				return ee.Message, ee.Code
			}
		}
	}
	return "", ""
}
