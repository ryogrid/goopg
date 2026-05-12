package executor

// operators_pg_input_error_info.go — pg_input_error_info(value, type) SRF.
// Returns 0 rows if the input is valid for the type, or 1 row with
// (message, detail, hint, sql_error_code) if the input is invalid.
// Used by the pg_regress int2 test and other type validation tests.
// M0097-0003.

import (
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
		n, e := parseIntegerInput(v, "oid", 64)
		if e != nil {
			if ee, ok := e.(*ExecError); ok {
				message = ee.Message
				sqlCode = ee.Code
			}
		} else {
			// Wrap negative: PostgreSQL wraps -N to uint32.
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
		errMsg := validateOidVector(v)
		if errMsg != "" {
			message = errMsg
			sqlCode = "22P02"
		}
	case "int2vector":
		// int2vector: space-separated int2 values. Validate each.
		message, sqlCode = validateInt2Vector(v)
	default:
		// Unknown type — treat as valid (0 rows returned).
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

// validateOidVector validates a space-separated list of oid values.
// Returns errorMessage if invalid, "" if valid. M0097-0003.
func validateOidVector(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	parts := strings.Fields(v)
	for _, p := range parts {
		n, err := parseIntegerInput(p, "oid", 64)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				return ee.Message
			}
			return "invalid input syntax for type oid: \"" + p + "\""
		}
		// Wrap negative.
		if n < 0 {
			n += 4294967296
		}
		if n < 0 || n > 4294967295 {
			return "value \"" + p + "\" is out of range for type oid"
		}
	}
	return ""
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
