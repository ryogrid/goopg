package parser

import "testing"

// TestNotLookaheadParity pins the NOT / NOT_LA split, which is easy to get
// wrong in BOTH directions and has been wrong in both.
//
// base_yylex.go substitutes NOT -> NOT_LA only for upstream's follower set
// (parser.c: BETWEEN, IN_P, LIKE, ILIKE, SIMILAR). EXISTS had been added to
// that set so `opt_if_not_exists: IF_P NOT_LA EXISTS` would reduce — which
// made every ordinary `NOT EXISTS (...)` a syntax error, TPC-H Q21 and Q22
// included. The fix is upstream's: keep the follower set exact and spell the
// grammar rules with plain NOT (gram.y opt_if_not_exists, transaction_mode_item).
//
// Both directions are asserted here so neither repair can silently undo the
// other.
func TestNotLookaheadParity(t *testing.T) {
	for _, q := range []string{
		// plain NOT: must NOT be substituted
		"SELECT 1 WHERE NOT EXISTS (SELECT 1)",
		"SELECT * FROM t WHERE NOT EXISTS (SELECT 1 FROM u WHERE u.a = t.a)",
		"SELECT * FROM t WHERE NOT a",
		"SELECT * FROM t WHERE a IS NOT NULL",
		"CREATE TABLE IF NOT EXISTS t (a int)",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS mv AS SELECT 1",
		"BEGIN NOT DEFERRABLE",
		// NOT_LA: must BE substituted
		"SELECT * FROM t WHERE a NOT IN (1, 2)",
		"SELECT * FROM t WHERE a NOT IN (SELECT b FROM u)",
		"SELECT * FROM t WHERE a NOT BETWEEN 1 AND 2",
		"SELECT * FROM t WHERE a NOT LIKE 'x%'",
		"SELECT * FROM t WHERE a NOT ILIKE 'x%'",
		"SELECT * FROM t WHERE a NOT SIMILAR TO 'x%'",
	} {
		assertParity(t, q)
	}
}
