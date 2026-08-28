package parser

import "testing"

// TestVariadicCallParity — `f(VARIADIC arg)`.
//
// VARIADIC has to attach to an INDIVIDUAL argument, so the name_or_call
// alternatives moved from the plain `opt_func_call_args` ([]Expr) to a carrier
// holding the per-argument flags that FuncCall.Variadic keeps parallel to Args.
// All eight of those alternatives share the `qualified_name '('` prefix and so
// had to move together; ARRAY[...] and the SQL value functions, which cannot
// take VARIADIC, keep the plain list.
//
// The subtle part is legacy's EXPANSION: `f(VARIADIC array[a,b])` becomes two
// arguments each flagged variadic, not one array argument, and with a
// `::int[]` cast the ELEMENT type is pushed onto each expanded element as its
// own cast (internal/parser/select.go:4815-4875). Reproducing only the flag
// yields a silently different Variadic slice.
func TestVariadicCallParity(t *testing.T) {
	for _, q := range []string{
		"SELECT f(variadic array[1,2])",
		"SELECT f(VARIADIC ARRAY[1,2,3])",
		"SELECT f(a, variadic b)",
		"SELECT f(variadic array[1,2]::int[])",
		"SELECT satisfies_hash_partition('mchash'::regclass, 2, 1, variadic array[1,2]::int[])",
		// non-variadic call forms must stay identical
		"SELECT f(a, b)",
		"SELECT f()",
		"SELECT now()",
		"SELECT count(*) FROM t",
		"SELECT f(DISTINCT a) FROM t",
		"SELECT row_number() OVER (ORDER BY a) FROM t",
		"SELECT sum(a) FILTER (WHERE b > 0) FROM t",
	} {
		assertParity(t, q)
	}
}

// TestAggregateOrderByParity — `array_agg(a ORDER BY b)`, gram.y's
// func_application `func_name '(' func_arg_list opt_sort_clause ')'`.
// Legacy records it as FuncCall.OrderBy.
func TestAggregateOrderByParity(t *testing.T) {
	for _, q := range []string{
		"SELECT array_agg(a ORDER BY b) FROM t",
		"SELECT string_agg(a, ',' ORDER BY b DESC) FROM t",
		"SELECT array_agg(a ORDER BY b, c) FROM t",
		"SELECT array_agg(a) FROM t",
	} {
		assertParity(t, q)
	}
}
