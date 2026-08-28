package parser

import "testing"

// TestSingleCharOperatorParity — `~`, `&`, `#`, `|`, `^`.
//
// The adapter turned EVERY one-character operator into a char terminal, though
// its own comment cited scan.l's {self} set — which is only
// `,()[].;:+-*/%^<>=`. The grammar declares just those, so `~`, `&`, `#`, `|`
// and `@` were unreachable: `a ~ 'x'` was a syntax error while its
// multi-character siblings `!~` and `~*` parsed fine, because those reach Op.
// Upstream lexes any other single-char operator through {op_chars} as an
// operator, and the fix is the membership test, not a grammar change.
//
// `^` is separate: it IS in {self} and has a %left entry, but no production
// ever consumed it, so exponentiation was a syntax error too.
func TestSingleCharOperatorParity(t *testing.T) {
	for _, q := range []string{
		"SELECT a ~ 'x' FROM t",
		"SELECT a !~ 'x' FROM t",
		"SELECT a ~* 'x' FROM t",
		"SELECT a & b FROM t",
		"SELECT a | b FROM t",
		"SELECT a # b FROM t",
		"SELECT a ^ b FROM t",
		// the {self} characters must still be char terminals
		"SELECT a + b, a - b, a * b, a / b, a % b FROM t",
		"SELECT a < b, a > b, a = b, a <= b, a >= b, a <> b FROM t",
		"SELECT (a), t.a, a[1] FROM t",
		"SELECT a::int FROM t",
		"SELECT a || b FROM t",
	} {
		assertParity(t, q)
	}
}

// TestNamedFunctionArgParity — `f(name => value)` / `f(name := value)`.
// Legacy DROPS the name and keeps only the value (PostgreSQL maps named
// arguments positionally for built-ins). The rule takes an a_expr on the left
// rather than a ColId so it cannot be ambiguous with a plain argument.
func TestNamedFunctionArgParity(t *testing.T) {
	for _, q := range []string{
		"SELECT f(a => 1)",
		"SELECT f(a := 1)",
		"SELECT f(a, b => 2)",
		"SELECT verify_heapam(relation := c.oid, on_error_stop := false)",
		"SELECT f(a, b)",
		"SELECT f()",
	} {
		assertParity(t, q)
	}
}
