package parser

import "fmt"

// TimeOperatorStatus says how ResolveTimeOperator resolved an operator.
type TimeOperatorStatus int

const (
	// TimeOperatorNone means the rules below do not cover the operands.
	TimeOperatorNone TimeOperatorStatus = iota
	// TimeOperatorFound means an operator applies and the result type is known.
	TimeOperatorFound
	// TimeOperatorMissing is PG's "operator does not exist".
	TimeOperatorMissing
	// TimeOperatorAmbiguous is PG's "operator is not unique".
	TimeOperatorAmbiguous
)

// ResolveTimeOperator resolves + and - when an operand is time or timetz, as
// oper() does over pg_operator.dat. Operand types are canonical names, with
// "unknown" for an untyped literal or NULL. time also casts implicitly to
// interval, which is how forms such as timestamp + time resolve.
func ResolveTimeOperator(op OpCode, left, right string) (string, TimeOperatorStatus) {
	if op != OpAdd && op != OpSub {
		return "", TimeOperatorNone
	}
	isTime := func(n string) bool { return n == "time" || n == "timetz" }
	if !isTime(left) && !isTime(right) {
		return "", TimeOperatorNone
	}
	other := right
	if isTime(right) && !isTime(left) {
		other = left
	}
	switch other {
	case "time", "timetz", "date", "timestamp", "timestamptz", "interval", "unknown":
	default:
		return "", TimeOperatorNone
	}
	if op == OpAdd {
		switch left + "+" + right {
		case "time+interval", "interval+time", "time+unknown", "unknown+time":
			return "time", TimeOperatorFound
		case "timetz+interval", "interval+timetz", "timetz+time", "time+timetz":
			return "timetz", TimeOperatorFound
		case "date+time", "time+date", "timestamp+time", "time+timestamp":
			return "timestamp", TimeOperatorFound
		case "date+timetz", "timetz+date", "timestamptz+time", "time+timestamptz":
			return "timestamptz", TimeOperatorFound
		case "time+time", "timetz+unknown", "unknown+timetz":
			return "", TimeOperatorAmbiguous
		}
		return "", TimeOperatorMissing
	}
	switch left + "-" + right {
	case "time-interval":
		return "time", TimeOperatorFound
	case "timetz-interval", "timetz-time", "timetz-unknown":
		return "timetz", TimeOperatorFound
	case "time-time", "time-unknown", "unknown-time", "interval-time":
		return "interval", TimeOperatorFound
	case "date-time", "timestamp-time":
		return "timestamp", TimeOperatorFound
	case "timestamptz-time":
		return "timestamptz", TimeOperatorFound
	}
	return "", TimeOperatorMissing
}

// TimeOperatorError returns the SQLSTATE, message and hint PG reports for a
// status of TimeOperatorMissing or TimeOperatorAmbiguous.
func TimeOperatorError(status TimeOperatorStatus, op OpCode, left, right string) (code, message, hint string) {
	display := func(n string) string {
		switch n {
		case "time":
			return "time without time zone"
		case "timetz":
			return "time with time zone"
		case "timestamp":
			return "timestamp without time zone"
		case "timestamptz":
			return "timestamp with time zone"
		}
		return n
	}
	if status == TimeOperatorAmbiguous {
		return "42725", fmt.Sprintf("operator is not unique: %s %s %s", display(left), op, display(right)),
			"Could not choose a best candidate operator. You might need to add explicit type casts."
	}
	return "42883", fmt.Sprintf("operator does not exist: %s %s %s", display(left), op, display(right)),
		"No operator matches the given name and argument types. You might need to add explicit type casts."
}
