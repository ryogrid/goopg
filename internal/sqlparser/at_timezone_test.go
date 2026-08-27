package sqlparser

import "testing"

// TestAtTimeZone pins AT TIME ZONE / AT LOCAL, which had NO productions at all
// — every spelling was a hard 42601 on the routed path. Both are rewritten
// into a timezone() call with the ZONE argument FIRST, and both are
// SYNTHESISED calls, so Variadic stays nil (specialFormCall, not NewFuncCall).
//
// The rule carries %prec Op rather than %prec AT: legacy parses the zone with
// parseExprPrec(precCompare+1), which binds looser than '+' and '*', so
// `x AT TIME ZONE 'UTC' + interval '1 day'` groups as
// timezone('UTC' + interval, x) there and as timezone('UTC', x) + interval
// under upstream's %left AT. Upstream reads better, but a migration's routed
// parser must not disagree with the parser it replaces.
func TestAtTimeZone(t *testing.T) {
	for _, q := range []string{
		"SELECT f1 AT TIME ZONE 'UTC' FROM t",
		"SELECT f1 AT TIME ZONE z FROM t",
		"SELECT f1 AT LOCAL FROM t",
		"SELECT a AT TIME ZONE 'x' AS y FROM t",
		// Precedence, both directions.
		"SELECT f1 AT TIME ZONE 'UTC' + interval '1 day' FROM t",
		"SELECT f1 AT TIME ZONE 'x' = y FROM t",
		"SELECT f1 AT TIME ZONE INTERVAL '-10:00' FROM t",
	} {
		assertParity(t, q)
	}
}

// TestIntervalLiteralForms pins parseIntervalLiteral's ORDER of attempts,
// which buildIntervalLit had wrong: the Form-2 split (`interval '<N> <unit>'`,
// keeping Value/Unit with PreComputed=false) must be tried BEFORE the
// whole-body decode. Going straight to ParseIntervalBody turned
// `interval '1 day'` into a PreComputed literal — and the single-unit spelling
// is exactly what the TPC-H query templates use, so every routed query
// carrying one held a different node than legacy.
func TestIntervalLiteralForms(t *testing.T) {
	for _, q := range []string{
		"SELECT interval '1 day'",
		"SELECT interval '90 day'",
		"SELECT interval '3 month'",
		"SELECT interval '1 year'",
		// Whole-body decode still owns the multi-field and HH:MM:SS bodies.
		"SELECT interval '1 year 2 months'",
		"SELECT interval '-10:00'",
		"SELECT interval '1 day 05:00:00'",
		// Bare magnitude and the trailing-qualifier form are unchanged.
		"SELECT interval '90'",
		"SELECT interval '1' day",
		"SELECT interval '1.5' hour",
	} {
		assertParity(t, q)
	}
}

// TestKeywordTypedLiterals pins the six keyword-tokenised typed-literal
// prefixes legacy accepts that the lexer's fold was missing. Measured, not
// guessed: legacy still REJECTS character, nchar, bit, int, float, double
// precision and national character, so those stay out.
func TestKeywordTypedLiterals(t *testing.T) {
	for _, q := range []string{
		"SELECT char 'c'",
		"SELECT char 'c' = char 'c' AS true",
		"SELECT decimal '1'",
		"SELECT integer '1'",
		"SELECT smallint '1'",
		"SELECT bigint '1'",
		"SELECT real '1'",
	} {
		assertParity(t, q)
	}
	for _, q := range []string{
		"SELECT character 'c'",
		"SELECT nchar 'c'",
		"SELECT bit 'c'",
		"SELECT int '1'",
		"SELECT float '1'",
	} {
		assertBothReject(t, q)
	}
}

// TestUpdateFromAndDeleteUsing pins the alias-carrying relation list. Both
// clauses shared a BARE-NAME list that dropped every alias, so
// `UPDATE t SET ... FROM u b WHERE ...` was a hard 42601. base_table_ref is
// exactly right here: legacy accepts everything it covers — aliases, AS
// aliases, ONLY, the inheritance star, derived tables, function tables,
// LATERAL — and rejects only JOIN, which base_table_ref also excludes.
func TestUpdateFromAndDeleteUsing(t *testing.T) {
	for _, q := range []string{
		"UPDATE t SET i = 1 FROM u b WHERE j = 1",
		"UPDATE t SET i = 1 FROM u AS b WHERE j = 1",
		"UPDATE t SET i = 1 FROM u b, v c",
		"UPDATE t SET i = 1 FROM ONLY u",
		"UPDATE t SET i = 1 FROM (SELECT 1) x",
		"UPDATE t SET i = 1 FROM generate_series(1,3) g",
		"UPDATE case_tbl SET i = CASE WHEN b.i >= 2 THEN (2*j) ELSE (3*j) END FROM case2_tbl b WHERE j = 1",
		"DELETE FROM t USING u b WHERE t.a = b.a",
		"DELETE FROM t USING ONLY u",
		"DELETE FROM t USING (SELECT 1) x",
		// No-clause forms unchanged.
		"UPDATE t SET i = 1",
		"DELETE FROM t",
	} {
		assertParity(t, q)
	}
	// JOIN is table_ref, not base_table_ref — legacy refuses it here too.
	assertBothReject(t, "UPDATE t SET i = 1 FROM u JOIN v ON u.a = v.a")
}
