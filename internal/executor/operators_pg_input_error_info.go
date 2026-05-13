package executor

// operators_pg_input_error_info.go — pg_input_error_info(value, type) SRF.
// Returns 0 rows if the input is valid for the type, or 1 row with
// (message, detail, hint, sql_error_code) if the input is invalid.
// Used by the pg_regress int2 test and other type validation tests.
// M0097-0003.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/planner"
)

type pgInputErrorInfoOp struct {
	plan *planner.PgInputErrorInfo
	ctx  *Context
	done bool
}

func newPgInputErrorInfoOp(p *planner.PgInputErrorInfo) *pgInputErrorInfoOp {
	return &pgInputErrorInfoOp{plan: p}
}

func (o *pgInputErrorInfoOp) Schema() planner.Schema { return o.plan.Output() }

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
	case "time", "timetz":
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
	case "int2vector":
		// int2vector: space-separated int2 values. Validate each.
		message, sqlCode = validateInt2Vector(v)
	default:
		// varchar(N) / character varying(N) / char(N) — length validation. M0097-0003.
		message, sqlCode = validateTypedLen(v, t)
	}

	if message == "" {
		// Valid input — return 0 rows.
		return nil, EOF
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
func validateTypedLen(v, typStr string) (string, string) {
	for _, pfx := range []string{"character varying(", "varchar("} {
		if strings.HasPrefix(typStr, pfx) && strings.HasSuffix(typStr, ")") {
			mid := typStr[len(pfx) : len(typStr)-1]
			n, err := strconv.Atoi(mid)
			if err != nil || n <= 0 {
				return "", ""
			}
			// PostgreSQL's varcharin checks raw length (no trailing-space strip).
			if len(v) > n {
				return fmt.Sprintf("value too long for type character varying(%d)", n), "22001"
			}
			return "", ""
		}
	}
	for _, pfx := range []string{"character(", "char(", "bpchar("} {
		if strings.HasPrefix(typStr, pfx) && strings.HasSuffix(typStr, ")") {
			mid := typStr[len(pfx) : len(typStr)-1]
			n, err := strconv.Atoi(mid)
			if err != nil || n <= 0 {
				return "", ""
			}
			stripped := strings.TrimRight(v, " ")
			if len(stripped) > n {
				return fmt.Sprintf("value too long for type character(%d)", n), "22001"
			}
			return "", ""
		}
	}
	return "", ""
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
