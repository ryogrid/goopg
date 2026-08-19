package parser

// OpCode is the typed dispatch key for UnaryOp / BinaryOp.
// Replaces the historical `Op string` field — switching on
// OpCode produces a jump-table dispatch, eliminating the
// per-row string-compare cost the M0072-final pprof flagged
// on Q5 (`evalBinary` 8.78 % flat / 29.20 % cum CPU).
//
// The zero value (OpUnknown) is reserved for the unset /
// parse-error state; all valid expressions carry a non-zero
// OpCode.
//
// See `docs/design/0073-0003-opcode-int-enum.md`.
type OpCode int8

const (
	OpUnknown OpCode = iota

	// Unary operators
	OpUnaryNeg // -x
	OpUnaryPos // +x
	OpNot      // NOT x

	// Binary arithmetic
	OpAdd // a + b
	OpSub // a - b
	OpMul // a * b
	OpDiv // a / b
	OpMod // a % b
	OpPow // a ^ b (float8 exponentiation, PG dpow semantics)

	// Binary text
	OpConcat // a || b

	// Binary comparison
	OpEq // a = b
	OpLt // a < b
	OpGt // a > b
	OpLe // a <= b
	OpGe // a >= b
	OpNe // a <> b OR a != b — single OpCode for both forms

	// Binary boolean
	OpAnd
	OpOr

	// Binary pattern
	OpLike
	OpNotLike
	OpILike
	OpNotILike

	// POSIX regex operators (M0097-0011)
	OpRegexMatch    // a ~ b   (case-sensitive match)
	OpRegexIMatch   // a ~* b  (case-insensitive match)
	OpRegexNoMatch  // a !~ b  (case-sensitive no-match)
	OpRegexINoMatch // a !~* b (case-insensitive no-match)

	// Bitwise operators (M0097-0003)
	OpBitAnd       // a & b
	OpBitOr        // a | b
	OpBitXor       // a # b   (PostgreSQL uses # for XOR, not ^)
	OpBitNot       // ~a      (unary bitwise NOT)
	OpBitShiftLeft  // a << b
	OpBitShiftRight // a >> b

	// Geometric operators (M0097-0023)
	OpContainedBy // a <@ b  (a is contained by b)
	OpContains    // a @> b  (a contains b)
	OpOverlap     // a && b  (a overlaps b)

	// JSON accessor operators (M0118-0009, horizons enabler)
	OpJSONGet     // a -> b   (json array element / object field, returns json)
	OpJSONGetText // a ->> b  (json array element / object field as text)
)

// ParseUnaryOp converts the token text the parser emits
// ("-", "+", "NOT") to an OpCode. Unknown strings return
// OpUnknown so callers can surface a parse-time error
// instead of silently constructing a UnaryOp with an
// unknown op.
func ParseUnaryOp(s string) OpCode {
	switch s {
	case "-":
		return OpUnaryNeg
	case "+":
		return OpUnaryPos
	case "NOT":
		return OpNot
	}
	return OpUnknown
}

// ParseBinaryOp converts the token text to an OpCode.
// "<>" and "!=" both map to OpNe (the canonical Postgres
// spelling is "<>"; "!=" is an accepted alias).
func ParseBinaryOp(s string) OpCode {
	switch s {
	case "+":
		return OpAdd
	case "-":
		return OpSub
	case "*":
		return OpMul
	case "/":
		return OpDiv
	case "%":
		return OpMod
	case "^":
		return OpPow
	case "||":
		return OpConcat
	case "=":
		return OpEq
	case "<":
		return OpLt
	case ">":
		return OpGt
	case "<=":
		return OpLe
	case ">=":
		return OpGe
	case "<>", "!=":
		return OpNe
	case "AND":
		return OpAnd
	case "OR":
		return OpOr
	case "LIKE":
		return OpLike
	case "NOT LIKE":
		return OpNotLike
	case "ILIKE":
		return OpILike
	case "NOT ILIKE":
		return OpNotILike
	case "~":
		return OpRegexMatch
	case "~*":
		return OpRegexIMatch
	case "!~":
		return OpRegexNoMatch
	case "!~*":
		return OpRegexINoMatch
	case "&":
		return OpBitAnd
	case "|":
		return OpBitOr
	case "#":
		return OpBitXor
	case "<<":
		return OpBitShiftLeft
	case ">>":
		return OpBitShiftRight
	case "<@":
		return OpContainedBy
	case "@>":
		return OpContains
	case "&&":
		return OpOverlap
	case "->":
		return OpJSONGet
	case "->>":
		return OpJSONGetText
	}
	return OpUnknown
}

// String returns the canonical SQL surface form for o.
// "!="  canonicalises to "<>" on output (matches upstream
// Postgres's preferred spelling). Used by EXPLAIN / debug
// printers and test helpers that previously read Op as a
// string directly.
func (o OpCode) String() string {
	switch o {
	case OpUnaryNeg:
		return "-"
	case OpUnaryPos:
		return "+"
	case OpNot:
		return "NOT"
	case OpAdd:
		return "+"
	case OpSub:
		return "-"
	case OpMul:
		return "*"
	case OpDiv:
		return "/"
	case OpMod:
		return "%"
	case OpPow:
		return "^"
	case OpConcat:
		return "||"
	case OpEq:
		return "="
	case OpLt:
		return "<"
	case OpGt:
		return ">"
	case OpLe:
		return "<="
	case OpGe:
		return ">="
	case OpNe:
		return "<>"
	case OpAnd:
		return "AND"
	case OpOr:
		return "OR"
	case OpLike:
		return "LIKE"
	case OpNotLike:
		return "NOT LIKE"
	case OpILike:
		return "ILIKE"
	case OpNotILike:
		return "NOT ILIKE"
	case OpRegexMatch:
		return "~"
	case OpRegexIMatch:
		return "~*"
	case OpRegexNoMatch:
		return "!~"
	case OpRegexINoMatch:
		return "!~*"
	case OpBitAnd:
		return "&"
	case OpBitOr:
		return "|"
	case OpBitXor:
		return "#"
	case OpBitNot:
		return "~"
	case OpBitShiftLeft:
		return "<<"
	case OpBitShiftRight:
		return ">>"
	case OpContainedBy:
		return "<@"
	case OpContains:
		return "@>"
	case OpOverlap:
		return "&&"
	case OpJSONGet:
		return "->"
	case OpJSONGetText:
		return "->>"
	}
	return "<unknown>"
}

// IsBoolean reports whether o is AND or OR. Used by
// evalBinary to dispatch the boolean short-circuit path
// before the NULL-on-NULL early-out.
func (o OpCode) IsBoolean() bool {
	return o == OpAnd || o == OpOr
}

// IsComparison reports whether o is one of =, <, >, <=,
// >=, <>. Used by predicate-shape walkers (NLI rule,
// join-order rebind) that previously checked
// `op == "=" || op == "<" || ...`.
func (o OpCode) IsComparison() bool {
	switch o {
	case OpEq, OpLt, OpGt, OpLe, OpGe, OpNe:
		return true
	}
	return false
}
