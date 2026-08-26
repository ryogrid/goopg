package sqlparser

import "testing"

// TestCastTypmodParity — CAST(x AS t(n)) and CAST(x AS t(p,s)).
//
// The '::' spelling has carried typmods since P2.5
// (`a_expr TYPECAST cast_typename '(' ICONST ')'`), but the CAST(...) spelling
// never did, so `CAST(x AS decimal(15,4))` was a syntax error while
// `x::decimal(15,4)` worked. A textbook sibling-path divergence, and SELECT is
// routed: it broke five TPC-DS queries (Q18/Q49/Q61/Q75/Q90).
func TestCastTypmodParity(t *testing.T) {
	for _, q := range []string{
		"SELECT CAST(1 AS decimal(15,4))",
		"SELECT CAST(1 AS numeric(10,2))",
		"SELECT CAST(a AS varchar(20)) FROM t",
		"SELECT CAST(a AS char(3)) FROM t",
		"SELECT CAST(a AS timestamp(3)) FROM t",
		"SELECT CAST(1 AS decimal)",
		// float(p) folds into float4/float8 — must match the TYPECAST arm.
		"SELECT CAST(1 AS float(20))",
		"SELECT CAST(1 AS float(30))",
		"SELECT 1::float(20)",
		"SELECT 1::float(30)",
		// the '::' sibling must stay identical
		"SELECT 1::decimal(15,4)",
		"SELECT a::numeric(10,2) FROM t",
		"SELECT CAST(x AS decimal(15,4)) / CAST(y AS decimal(15,4)) FROM t",
	} {
		assertParity(t, q)
	}
}

// TestSimpleCaseParity — the `CASE operand WHEN value THEN ...` form.
//
// Only the SEARCHED form (`CASE WHEN cond THEN ...`) was in the grammar;
// NewCaseExpr had always accepted an operand. Every simple-CASE shape was a
// syntax error on the routed SELECT path — ordinary, extremely common SQL.
func TestSimpleCaseParity(t *testing.T) {
	for _, q := range []string{
		"SELECT CASE a WHEN 1 THEN 2 END FROM t",
		"SELECT CASE a WHEN 1 THEN 2 ELSE 3 END FROM t",
		"SELECT CASE a WHEN 1 THEN 'x' WHEN 2 THEN 'y' ELSE 'z' END FROM t",
		"SELECT CASE x.a WHEN 1 THEN 2 END FROM t x",
		"SELECT CASE 1 WHEN 1 THEN 2 END",
		"SELECT CASE lower(a) WHEN 'x' THEN 1 END FROM t",
		"SELECT CASE mean WHEN 0 THEN NULL ELSE stdev/mean END cov FROM t",
		// the searched form must stay identical
		"SELECT CASE WHEN a > 1 THEN 2 ELSE 3 END FROM t",
		"SELECT CASE WHEN a > 1 THEN 2 END FROM t",
	} {
		assertParity(t, q)
	}
}

// TestBareAliasKeywordParity — a bare (AS-less) derived-table alias may be any
// UNRESERVED keyword. opt_derived_alias accepted only IDENT, and unreserved
// keywords lex as TokenKeyword, so TPC-DS Q90's `(...) at, (...) pt` failed.
func TestBareAliasKeywordParity(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM (SELECT 1) at",
		"SELECT * FROM (SELECT 1) at, (SELECT 2) pt",
		"SELECT * FROM (SELECT 1) AS at",
		"SELECT * FROM (SELECT 1) value",
		"SELECT * FROM (SELECT 1) name",
		"SELECT * FROM (SELECT 1) t2",
		"SELECT * FROM (SELECT 1) x (c)",
	} {
		assertParity(t, q)
	}
}
